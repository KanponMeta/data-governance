package schedule

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/partition"
	"github.com/kanpon/data-governance/internal/storage"
)

// ErrNoDueSchedule is returned by FireOneSchedule when no due row exists.
// Callers must treat this as a benign condition (loop exit), not an error.
var ErrNoDueSchedule = errors.New("schedule: no due schedule")

// FireOneSchedule transactionally claims the next due schedule row, enqueues
// a run, updates last_fire_at / next_fire_at, and commits. After commit, it
// emits schedule.fired and (if missedCount > 0) schedule.missed events.
//
// Returns ErrNoDueSchedule when no rows are due.
//
// EXPORTED so plan 03-06's scheduler subcommand can drive its own tick loop
// with interleaved sensor evaluation (D-05 single-loop architecture). The
// per-row transaction + SELECT FOR UPDATE SKIP LOCKED gives natural sharding
// across replicas with zero coordination (D-03).
//
// The asset.DefinitionRegistry is consulted to resolve partition strategy at
// fire time (D-12 Schedule×Partitions composition). Scheduling for an asset
// that is not in the registry inserts a partition_key=NULL run — the worker
// will later fail to claim/execute it (no Asset definition exists), but that
// is the operator's signal to clean up the orphan schedule row.
func FireOneSchedule(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry, events event.Writer, now time.Time) error {
	db := store.DB()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("schedule.fire: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const selectSQL = `
		SELECT id, asset_name, cron_expr, last_fire_at
		FROM schedules
		WHERE next_fire_at <= $1
		  AND paused_at IS NULL
		ORDER BY next_fire_at
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`
	var (
		schedID    uuid.UUID
		assetName  string
		cronExpr   string
		lastFireAt sql.NullTime
	)
	if err := tx.QueryRowContext(ctx, selectSQL, now).Scan(&schedID, &assetName, &cronExpr, &lastFireAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoDueSchedule
		}
		return fmt.Errorf("schedule.fire: select due: %w", err)
	}

	// Defense-in-depth re-parse (T-03-04-01) — asset.Builder validates at
	// definition time but the daemon cannot trust the DB row.
	sched, err := cronParser.Parse(cronExpr)
	if err != nil {
		return fmt.Errorf("schedule.fire: parse cron %q for asset %q: %w", cronExpr, assetName, err)
	}
	lf := time.Time{}
	if lastFireAt.Valid {
		lf = lastFireAt.Time
	}
	windowToFire, missedCount := computeNextAndDetectMiss(sched, lf, now)

	// nextFire is computed from `now` so the next tick lands on the upcoming
	// window regardless of how far behind we were. Using sched.Next(windowToFire)
	// could re-pick a still-past window after a multi-hour outage.
	nextFire := sched.Next(now)

	// Determine partition_key from asset registry (D-12 Schedule × Partitions composition).
	partitionKey := computeFirePartitionKey(reg, assetName, windowToFire)

	runID := uuid.New()
	const insertRunSQL = `
		INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority, partition_key)
		VALUES ($1, $2, 'queued', 'schedule', NOW(), 'normal', $3)
	`
	var pkArg interface{} = nil
	if partitionKey != "" {
		pkArg = partitionKey
	}
	if _, err := tx.ExecContext(ctx, insertRunSQL, runID, assetName, pkArg); err != nil {
		// Partial unique index may reject if a prior run for the same partition
		// is still in-flight (T-03-04-03). Treat as "skip this fire" — the tx
		// rolls back, the schedule row stays due, and the next tick re-evaluates.
		return fmt.Errorf("schedule.fire: insert run: %w", err)
	}

	const updateSchedSQL = `
		UPDATE schedules
		   SET last_fire_at = NOW(),
		       next_fire_at = $1,
		       updated_at   = NOW()
		 WHERE id = $2
	`
	if _, err := tx.ExecContext(ctx, updateSchedSQL, nextFire, schedID); err != nil {
		return fmt.Errorf("schedule.fire: update schedule: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("schedule.fire: commit: %w", err)
	}

	// Best-effort event emission outside tx — emit failures must NOT cause the
	// fire to be lost (the runs row is already queued). Errors are intentionally
	// swallowed; observability for emit failures lives in the writer's own
	// metrics (Phase 1 D-09).
	_ = events.Append(ctx, event.Event{
		Type:         event.EventTypeScheduleFired,
		OccurredAt:   time.Now().UTC(),
		ResourceType: "schedule",
		ResourceID:   schedID.String(),
		Payload: map[string]any{
			"asset_name":    assetName,
			"run_id":        runID.String(),
			"window_fired":  windowToFire,
			"partition_key": partitionKey,
		},
	})
	if missedCount > 0 {
		_ = events.Append(ctx, event.Event{
			Type:         event.EventTypeScheduleMissed,
			OccurredAt:   time.Now().UTC(),
			ResourceType: "schedule",
			ResourceID:   schedID.String(),
			Payload: map[string]any{
				"asset_name":    assetName,
				"skipped_count": missedCount,
			},
		})
	}
	return nil
}

// computeFirePartitionKey returns the partition_key for a scheduled fire,
// given the asset's PartitionStrategy. Returns "" for non-partitioned assets.
//
// Scheduling convention follows Dagster's "cron fires for the preceding
// window" — a daily cron firing at midnight enqueues yesterday's partition
// (Open Question 1 default).
//
// For schedule × category composition: pick the first key in CategoryPartitions.Keys
// (Open Question 4 default — uncommon configuration documented here).
func computeFirePartitionKey(reg *asset.DefinitionRegistry, assetName string, windowFiredAt time.Time) string {
	if reg == nil {
		return ""
	}
	a, err := reg.Get(assetName)
	if err != nil || a == nil {
		return ""
	}
	strat := a.Partitions()
	if strat == nil {
		return ""
	}
	switch s := strat.(type) {
	case partition.DailyPartitions:
		// Cron fires for the preceding window — yesterday for daily.
		return partition.CurrentDailyKey(windowFiredAt, 24*time.Hour)
	case partition.WeeklyPartitions:
		return partition.WeeklyKey(windowFiredAt.Add(-7 * 24 * time.Hour))
	case partition.MonthlyPartitions:
		return partition.MonthlyKey(windowFiredAt.AddDate(0, -1, 0))
	case partition.CategoryPartitions:
		if len(s.Keys) == 0 {
			return ""
		}
		return s.Keys[0]
	default:
		return ""
	}
}
