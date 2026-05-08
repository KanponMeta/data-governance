// Package sensor implements the sensor evaluation harness (D-05..D-08, D-12).
//
// The harness exposes a Daemon.RunOnce(ctx) driver that selects due sensor
// rows via SELECT FOR UPDATE SKIP LOCKED, calls each user-supplied Sense(ctx)
// under a timeout-bounded context with panic recovery, applies the two-layer
// dedup (RunKey == last_run_key OR NOW() < cooldown_until → skip), and either
// enqueues a runs row or records the dedup decision via event_log.
//
// On Sense() error or panic the harness increments consecutive_failures and
// auto-disables the sensor at the configured threshold (D-08). A successful
// evaluation (Fired=false) resets the failure counter.
//
// Design notes:
//   - safeEvaluate's timeout defaults to spec.MinInterval (or DefaultMinInterval
//     if MinInterval is 0). Pitfall 3 (long Sense() blocks the tick loop) and
//     Pitfall 2 (panic crashes daemon) are mitigated here.
//   - handleFired runs INSERT runs + UPDATE sensors in the same transaction
//     to keep last_run_key / cooldown_until / runs row in lockstep with the
//     SKIP LOCKED row lock.
//   - resolveSensorPartitionKey implements D-12: explicit RunKey wins when it
//     validates for the asset's PartitionStrategy; otherwise we fall back to
//     the previous-window key (Open Question 1 default).
package sensor

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

const (
	// DefaultMinInterval is the floor for safeEvaluate timeout when spec.MinInterval is 0.
	DefaultMinInterval = 30 * time.Second

	// AutoDisableThreshold is the default consecutive-failure count before
	// disabled_at is set (D-08). Override via Daemon.DisableAfter.
	AutoDisableThreshold = 60
)

// ErrNoDueSensor is returned by evaluateOneSensor when the SELECT FOR UPDATE
// SKIP LOCKED query finds no rows that are currently due. RunOnce treats this
// as the drain signal to exit the loop.
var ErrNoDueSensor = errors.New("sensor: no due sensor")

// safeEvaluate wraps SensorFunc with a timeout-bounded ctx and panic recovery.
//
// Pitfall 2 (T-03-05-01): a panic in user code must not crash the daemon
// goroutine. defer recover() converts panics to typed errors that flow into
// handleError just like a returned error.
//
// Pitfall 3 (T-03-05-02): an unbounded Sense() must not block the tick loop.
// context.WithTimeout enforces a deadline; the user's contract is "evaluate at
// least every MinInterval", so a Sense() that exceeds MinInterval has already
// violated that contract.
//
// timeout=0 is interpreted as max(spec.MinInterval, DefaultMinInterval).
func safeEvaluate(ctx context.Context, spec asset.SensorSpec, timeout time.Duration) (result asset.SensorResult, err error) {
	if timeout <= 0 {
		timeout = spec.MinInterval
		if timeout < DefaultMinInterval {
			timeout = DefaultMinInterval
		}
	}
	evalCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("sensor %q panic: %v", spec.Name, r)
		}
	}()
	return spec.Sense(evalCtx)
}

// sensorRow captures the columns of a sensors row needed during evaluation.
type sensorRow struct {
	ID                  uuid.UUID
	AssetName           string
	SensorName          string
	MinIntervalSeconds  int64
	LastRunKey          sql.NullString
	CooldownUntil       sql.NullTime
	ConsecutiveFailures int
}

// evaluateOneSensor selects the next due sensor with FOR UPDATE SKIP LOCKED,
// calls safeEvaluate, then applies handleFired / updateSensorOnNoFire / handleError
// in the same transaction (atomic dedup-state advancement).
//
// Returns ErrNoDueSensor when no rows are due (RunOnce drain signal).
func evaluateOneSensor(
	ctx context.Context,
	store storage.Storage,
	reg *asset.DefinitionRegistry,
	events event.Writer,
	disableAfter int,
) error {
	db := store.DB()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("sensor.evaluate: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Select the next due sensor row. NOT disabled, evaluation interval elapsed.
	const selectSQL = `
		SELECT id, asset_name, sensor_name, min_interval_seconds, last_run_key, cooldown_until, consecutive_failures
		  FROM sensors
		 WHERE disabled_at IS NULL
		   AND (last_evaluated_at IS NULL
		        OR last_evaluated_at + (min_interval_seconds * interval '1 second') <= NOW())
		 ORDER BY last_evaluated_at NULLS FIRST
		 FOR UPDATE SKIP LOCKED
		 LIMIT 1
	`
	var row sensorRow
	if err := tx.QueryRowContext(ctx, selectSQL).Scan(
		&row.ID, &row.AssetName, &row.SensorName, &row.MinIntervalSeconds,
		&row.LastRunKey, &row.CooldownUntil, &row.ConsecutiveFailures,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoDueSensor
		}
		return fmt.Errorf("sensor.evaluate: select due: %w", err)
	}

	// Resolve the asset and matching SensorSpec from the registry. If the asset
	// or sensor was removed from the in-process registry, disable the row so we
	// stop wasting cycles on it.
	a, err := reg.Get(row.AssetName)
	if err != nil || a == nil {
		return autoDisableOrphan(ctx, tx, events, row.ID, row.AssetName, row.SensorName)
	}
	var spec *asset.SensorSpec
	for i := range a.Sensors() {
		s := a.Sensors()[i]
		if s.Name == row.SensorName {
			spec = &s
			break
		}
	}
	if spec == nil {
		return autoDisableOrphan(ctx, tx, events, row.ID, row.AssetName, row.SensorName)
	}

	// Evaluate the user-supplied Sense() under timeout + panic recovery.
	result, evalErr := safeEvaluate(ctx, *spec, 0)

	if evalErr != nil {
		return handleError(ctx, tx, events, &row, evalErr, disableAfter)
	}

	// Always emit sensor.evaluated as audit-trail post-commit (best-effort).
	defer func() {
		_ = events.Append(context.Background(), event.Event{
			Type:         event.EventTypeSensorEvaluated,
			OccurredAt:   time.Now().UTC(),
			ResourceType: "sensor",
			ResourceID:   row.ID.String(),
			Payload: map[string]any{
				"asset_name":  row.AssetName,
				"sensor_name": row.SensorName,
				"fired":       result.Fired,
			},
		})
	}()

	if !result.Fired {
		return updateSensorOnNoFire(ctx, tx, &row)
	}
	return handleFired(ctx, tx, events, a, &row, *spec, result)
}

// autoDisableOrphan disables a sensors row whose asset/sensor is no longer in
// the registry. Same tx as the SELECT; emits sensor.disabled with reason=orphaned.
func autoDisableOrphan(
	ctx context.Context, tx *sql.Tx, events event.Writer,
	sensorID uuid.UUID, assetName, sensorName string,
) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE sensors SET disabled_at = NOW(), updated_at = NOW() WHERE id = $1`,
		sensorID,
	); err != nil {
		return fmt.Errorf("sensor.evaluate: disable orphan: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_ = events.Append(context.Background(), event.Event{
		Type:         event.EventTypeSensorDisabled,
		OccurredAt:   time.Now().UTC(),
		ResourceType: "sensor",
		ResourceID:   sensorID.String(),
		Payload: map[string]any{
			"asset_name":  assetName,
			"sensor_name": sensorName,
			"reason":      "orphaned",
		},
	})
	return nil
}

// updateSensorOnNoFire advances last_evaluated_at and resets consecutive_failures
// (D-08 auto-reset on success).
func updateSensorOnNoFire(ctx context.Context, tx *sql.Tx, row *sensorRow) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE sensors SET last_evaluated_at = NOW(), consecutive_failures = 0, updated_at = NOW() WHERE id = $1`,
		row.ID,
	); err != nil {
		return fmt.Errorf("sensor.evaluate: update no-fire: %w", err)
	}
	return tx.Commit()
}

// handleFired implements D-07's two-layer dedup:
//   1. RunKey == last_run_key (and non-empty) → emit sensor.dedup_skipped, no enqueue.
//   2. NOW() < cooldown_until → emit sensor.cooldown_skipped, no enqueue.
//   3. Otherwise: INSERT runs (trigger='sensor', priority='normal',
//      partition_key=resolveSensorPartitionKey(...)) + UPDATE sensors (last_run_key,
//      last_fired_at, cooldown_until, consecutive_failures=0) in the same tx.
func handleFired(
	ctx context.Context, tx *sql.Tx, events event.Writer,
	a *asset.Asset, row *sensorRow, spec asset.SensorSpec, result asset.SensorResult,
) error {
	now := time.Now().UTC()

	// Layer 1: RunKey dedup (cheap string compare; only meaningful when both sides non-empty).
	if result.RunKey != "" && row.LastRunKey.Valid && row.LastRunKey.String == result.RunKey {
		if _, err := tx.ExecContext(ctx,
			`UPDATE sensors SET last_evaluated_at = NOW(), consecutive_failures = 0, updated_at = NOW() WHERE id = $1`,
			row.ID,
		); err != nil {
			return fmt.Errorf("sensor.handleFired: update on dedup: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		_ = events.Append(context.Background(), event.Event{
			Type:         event.EventTypeSensorDedupSkipped,
			OccurredAt:   now,
			ResourceType: "sensor",
			ResourceID:   row.ID.String(),
			Payload: map[string]any{
				"asset_name":  row.AssetName,
				"sensor_name": row.SensorName,
				"run_key":     result.RunKey,
			},
		})
		return nil
	}

	// Layer 2: cooldown window (time compare).
	if row.CooldownUntil.Valid && now.Before(row.CooldownUntil.Time) {
		if _, err := tx.ExecContext(ctx,
			`UPDATE sensors SET last_evaluated_at = NOW(), consecutive_failures = 0, updated_at = NOW() WHERE id = $1`,
			row.ID,
		); err != nil {
			return fmt.Errorf("sensor.handleFired: update on cooldown: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		_ = events.Append(context.Background(), event.Event{
			Type:         event.EventTypeSensorCooldownSkipped,
			OccurredAt:   now,
			ResourceType: "sensor",
			ResourceID:   row.ID.String(),
			Payload: map[string]any{
				"asset_name":     row.AssetName,
				"sensor_name":    row.SensorName,
				"cooldown_until": row.CooldownUntil.Time,
			},
		})
		return nil
	}

	// Both layers passed → enqueue a run.
	runID := uuid.New()
	partitionKey := resolveSensorPartitionKey(a.Partitions(), result.RunKey, now)
	var pkArg interface{} = nil
	if partitionKey != "" {
		pkArg = partitionKey
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority, partition_key)
		 VALUES ($1, $2, 'queued', 'sensor', NOW(), 'normal', $3)`,
		runID, row.AssetName, pkArg,
	); err != nil {
		// Most likely cause: partial unique index on (asset_name, partition_key)
		// rejected the INSERT because an in-flight run already exists for this
		// partition. Surface the error so the caller can log + retry next tick.
		return fmt.Errorf("sensor.handleFired: insert run (likely in-flight collision): %w", err)
	}

	cooldownUntil := now.Add(spec.Cooldown)
	if _, err := tx.ExecContext(ctx,
		`UPDATE sensors
		    SET last_evaluated_at    = NOW(),
		        last_fired_at        = NOW(),
		        last_run_key         = $1,
		        cooldown_until       = $2,
		        consecutive_failures = 0,
		        updated_at           = NOW()
		  WHERE id = $3`,
		sql.NullString{String: result.RunKey, Valid: result.RunKey != ""},
		cooldownUntil,
		row.ID,
	); err != nil {
		return fmt.Errorf("sensor.handleFired: update sensor: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_ = events.Append(context.Background(), event.Event{
		Type:         event.EventTypeSensorFired,
		OccurredAt:   now,
		ResourceType: "sensor",
		ResourceID:   row.ID.String(),
		Payload: map[string]any{
			"asset_name":    row.AssetName,
			"sensor_name":   row.SensorName,
			"run_id":        runID.String(),
			"run_key":       result.RunKey,
			"partition_key": partitionKey,
			"payload":       result.Payload,
		},
	})
	return nil
}

// handleError increments consecutive_failures and sets disabled_at when the
// post-update count reaches the threshold (D-08). Auto-resets via
// updateSensorOnNoFire / handleFired success paths.
func handleError(
	ctx context.Context, tx *sql.Tx, events event.Writer,
	row *sensorRow, evalErr error, disableAfter int,
) error {
	threshold := disableAfter
	if threshold <= 0 {
		threshold = AutoDisableThreshold
	}
	const updateSQL = `
		UPDATE sensors
		   SET consecutive_failures = consecutive_failures + 1,
		       last_evaluated_at    = NOW(),
		       updated_at           = NOW(),
		       disabled_at          = CASE
		           WHEN consecutive_failures + 1 >= $1 THEN NOW()
		           ELSE disabled_at
		       END
		 WHERE id = $2
		 RETURNING consecutive_failures, disabled_at
	`
	var (
		newFailures int
		disabledAt  sql.NullTime
	)
	if err := tx.QueryRowContext(ctx, updateSQL, threshold, row.ID).Scan(&newFailures, &disabledAt); err != nil {
		return fmt.Errorf("sensor.handleError: update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_ = events.Append(context.Background(), event.Event{
		Type:         event.EventTypeSensorEvaluationFailed,
		OccurredAt:   time.Now().UTC(),
		ResourceType: "sensor",
		ResourceID:   row.ID.String(),
		Payload: map[string]any{
			"asset_name":           row.AssetName,
			"sensor_name":          row.SensorName,
			"error":                evalErr.Error(),
			"consecutive_failures": newFailures,
		},
	})
	if disabledAt.Valid && newFailures >= threshold {
		_ = events.Append(context.Background(), event.Event{
			Type:         event.EventTypeSensorDisabled,
			OccurredAt:   time.Now().UTC(),
			ResourceType: "sensor",
			ResourceID:   row.ID.String(),
			Payload: map[string]any{
				"asset_name":           row.AssetName,
				"sensor_name":          row.SensorName,
				"consecutive_failures": newFailures,
				"threshold":            threshold,
			},
		})
	}
	return nil
}

// resolveSensorPartitionKey computes runs.partition_key for a sensor-fired run
// per D-12 (Sensor × Partitions composition).
//
// If the asset has no PartitionStrategy → "".
// If RunKey is non-empty AND syntactically valid for the strategy → RunKey.
// Otherwise:
//   - DailyPartitions: fall back to CurrentDailyKey(now, 24h) (Open Question 1 default — yesterday).
//   - WeeklyPartitions: previous ISO week.
//   - MonthlyPartitions: previous calendar month.
//   - CategoryPartitions: no fallback (key must validate against declared Keys).
func resolveSensorPartitionKey(strategy partition.PartitionStrategy, runKey string, now time.Time) string {
	if strategy == nil {
		return ""
	}
	switch s := strategy.(type) {
	case partition.DailyPartitions:
		if runKey != "" {
			if _, err := time.Parse("2006-01-02", runKey); err == nil {
				return runKey
			}
		}
		return partition.CurrentDailyKey(now, 24*time.Hour)
	case partition.WeeklyPartitions:
		// RunKey format is "YYYY-Www"; if it round-trips through WeeklyKey we accept it.
		if runKey != "" && weeklyKeyValid(runKey) {
			return runKey
		}
		return partition.WeeklyKey(now.Add(-7 * 24 * time.Hour))
	case partition.MonthlyPartitions:
		if runKey != "" {
			if _, err := time.Parse("2006-01", runKey); err == nil {
				return runKey
			}
		}
		return partition.MonthlyKey(now.AddDate(0, -1, 0))
	case partition.CategoryPartitions:
		if runKey == "" {
			return ""
		}
		for _, k := range s.Keys {
			if k == runKey {
				return runKey
			}
		}
		// Unknown category key — reject (no fallback for category, by design).
		return ""
	}
	return ""
}

// weeklyKeyValid checks for the "YYYY-Www" format (e.g., 2024-W03). Accepts any
// 1- or 2-digit week between 01 and 53.
func weeklyKeyValid(s string) bool {
	// Minimum length "2024-W01" = 8.
	if len(s) < 8 {
		return false
	}
	if s[4] != '-' || (s[5] != 'W' && s[5] != 'w') {
		return false
	}
	for i := 0; i < 4; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	weekPart := s[6:]
	if len(weekPart) != 2 {
		return false
	}
	for i := 0; i < 2; i++ {
		if weekPart[i] < '0' || weekPart[i] > '9' {
			return false
		}
	}
	weekNum := int(weekPart[0]-'0')*10 + int(weekPart[1]-'0')
	return weekNum >= 1 && weekNum <= 53
}
