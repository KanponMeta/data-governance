package backfill

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/storage"
)

// ValidPriorities is the set of accepted priority values (mirrors run.AllPriorities).
// Stored here to avoid an import cycle with internal/run; checked at CLI parse + Submit.
var ValidPriorities = map[string]struct{}{
	"critical": {},
	"normal":   {},
	"backfill": {},
}

// Submit creates one backfills row and N runs rows in a single transaction.
// Returns the new backfill_id (UUID).
//
// Per D-15: enqueue all immediately. Duplicates already in-flight (per the
// partial unique index from plan 03-01) are silently skipped via
// ON CONFLICT DO NOTHING — making backfill resubmit idempotent.
//
// Default priority: "backfill" (caller may override via spec.Priority).
//
// Multi-row INSERT layout:
//
//	8 columns total (id, asset_name, state, trigger, queued_at, priority,
//	partition_key, backfill_id). 3 of those are SQL literals — state='queued',
//	trigger='backfill', queued_at=NOW(). That leaves 5 PARAMETER PLACEHOLDERS
//	per row: id, asset_name, priority, partition_key, backfill_id.
//	The values builder MUST use base := i*5 — using i*8 would point past
//	the args slice and produce a parameter binding error.
//
// ON CONFLICT predicate match (Pitfall — application-vs-index drift):
//
//	The partial unique index from plan 03-01 has predicate
//	  WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL
//	PostgreSQL's ON CONFLICT inference requires the WHERE clause on the
//	conflict target to match the partial index predicate token-for-token
//	(whitespace-insensitive but operator/literal-sensitive). Omitting
//	`AND partition_key IS NOT NULL` would cause:
//	  ERROR: there is no unique or exclusion constraint matching the
//	         ON CONFLICT specification
//	Acceptance criteria explicitly grep for the matching tail.
func Submit(ctx context.Context, store storage.Storage, events event.Writer, assetName string, spec Spec) (uuid.UUID, error) {
	if assetName == "" {
		return uuid.Nil, fmt.Errorf("backfill.Submit: asset name required")
	}
	if len(spec.Keys) == 0 {
		return uuid.Nil, fmt.Errorf("backfill.Submit: no keys to enqueue")
	}
	priority := spec.Priority
	if priority == "" {
		priority = "backfill"
	}
	if _, ok := ValidPriorities[priority]; !ok {
		return uuid.Nil, fmt.Errorf("backfill.Submit: invalid priority %q (must be critical|normal|backfill)", priority)
	}

	backfillID := uuid.New()
	db := store.DB()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return uuid.Nil, fmt.Errorf("backfill.Submit: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Insert the backfills row recording the operator's intent. total_partitions
	//    reflects len(spec.Keys) — the original spec length — not the actual
	//    inserted count. Operators see the discrepancy via `backfill status`
	//    when ON CONFLICT skipped some keys (see comment on the runs INSERT).
	const insertBackfill = `
		INSERT INTO backfills (id, asset_name, partition_spec, status, total_partitions, submitted_at)
		VALUES ($1, $2, $3, 'submitted', $4, NOW())
	`
	if _, err := tx.ExecContext(ctx, insertBackfill, backfillID, assetName, spec.Source, len(spec.Keys)); err != nil {
		return uuid.Nil, fmt.Errorf("backfill.Submit: insert backfill row: %w", err)
	}

	// 2. Multi-row INSERT into runs. base := i*5 (NOT i*8). See doc comment.
	values := make([]string, 0, len(spec.Keys))
	args := make([]interface{}, 0, len(spec.Keys)*5)
	for i, key := range spec.Keys {
		base := i * 5
		values = append(values, fmt.Sprintf("($%d, $%d, 'queued', 'backfill', NOW(), $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5))
		args = append(args, uuid.New(), assetName, priority, key, backfillID)
	}
	// ON CONFLICT predicate MUST match the partial unique index from plan 03-01:
	//   WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL
	query := `INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority, partition_key, backfill_id) VALUES ` +
		strings.Join(values, ", ") +
		` ON CONFLICT (asset_name, partition_key) WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL DO NOTHING`
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return uuid.Nil, fmt.Errorf("backfill.Submit: bulk insert runs: %w", err)
	}
	inserted, _ := result.RowsAffected()

	if err := tx.Commit(); err != nil {
		return uuid.Nil, fmt.Errorf("backfill.Submit: commit: %w", err)
	}

	// 3. Emit backfill.submitted event (best-effort — observability, not coordination).
	if events != nil {
		_ = events.Append(ctx, event.Event{
			Type:         event.EventTypeBackfillSubmitted,
			OccurredAt:   time.Now().UTC(),
			ResourceType: "backfill",
			ResourceID:   backfillID.String(),
			Payload: map[string]any{
				"asset_name":       assetName,
				"partition_spec":   spec.Source,
				"total_partitions": len(spec.Keys),
				"enqueued":         inserted,
				"skipped_inflight": int64(len(spec.Keys)) - inserted,
				"priority":         priority,
			},
		})
	}
	return backfillID, nil
}
