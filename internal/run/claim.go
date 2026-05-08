package run

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/storage"
)

// ErrNoQueuedRun is returned by ClaimNext when there are no eligible queued runs.
var ErrNoQueuedRun = errors.New("run: no queued run available")

// ClaimedRun is the projection ClaimNext returns to the worker.
// It contains only the fields needed to start execution; full run details
// are available via ent by ID.
//
// Phase 3 additions (D-09 / D-10 / D-13 / D-15):
//   - PartitionKey: nil for non-partitioned runs; populated from
//     runs.partition_key (VARCHAR(128) NULL).
//   - Priority: enum value from runs.priority — "critical" | "normal" | "backfill".
//     The string is the canonical form; convert to integer ordering via
//     PriorityOrder. Default for legacy / non-Phase-3 inserts is "normal"
//     (DB column default).
//   - BackfillID: nil for non-backfill runs; populated from runs.backfill_id
//     (UUID NULL). Plan 03-07's backfill CLI gates the layer-3 token tag
//     acquisition off this field.
type ClaimedRun struct {
	ID           uuid.UUID
	AssetName    string
	Trigger      string
	QueuedAt     time.Time
	PartitionKey *string    // nil for non-partitioned runs (D-10)
	Priority     string     // "critical" | "normal" | "backfill" (D-13)
	BackfillID   *uuid.UUID // nil for non-backfill runs (D-15)
}

// ClaimNext atomically picks one queued run and transitions it to 'starting',
// recording the claim by `workerID` and stamping last_heartbeat=NOW(). The query
// uses SELECT ... FOR UPDATE SKIP LOCKED so 50 concurrent workers never claim the
// same row (D-17 + ROADMAP criterion 3). last_heartbeat enables plan 02-04's
// stale-run reaper to detect crashed workers (Option B for D-14 crash recovery).
//
// Returns ErrNoQueuedRun when no eligible row exists.
//
// Phase 3 (D-13 layer 2): the ORDER BY clause uses a CASE expression on the
// priority column so 'critical' rows are claimed before 'normal' before 'backfill',
// breaking ties by queued_at (FIFO within tier). The integer mapping in the
// CASE expression MUST match PriorityOrder() in priority.go (Pitfall 5).
//
// IMPORTANT (Pitfall 1): the WHERE clause is `WHERE state = 'queued'` ONLY.
// Adding `WHERE priority != 'backfill'` would strand backfill rows when no
// normal rows exist. Priority belongs in ORDER BY, not WHERE — SKIP LOCKED
// ensures multi-replica safety; ORDER BY ensures normal runs are claimed
// before backfill runs.
//
// The claim is performed in a single database transaction:
//  1. SELECT the priority-then-FIFO oldest queued run with FOR UPDATE SKIP LOCKED
//     (multi-replica safe, no double-claim)
//  2. UPDATE the same row atomically: state→starting, claimed_by, claimed_at,
//     last_heartbeat
//
// Defense-in-depth: the UPDATE also guards with WHERE state='queued' to catch any
// race conditions that SKIP LOCKED should have prevented.
func ClaimNext(ctx context.Context, store storage.Storage, workerID string) (*ClaimedRun, error) {
	db := store.DB()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("run: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Pitfall 5 — the CASE integer mapping below MUST match
	// PriorityOrder() in internal/run/priority.go. The drift-prevention
	// test TestPriorityOrderConsistency catches divergence.
	const selectSQL = `
		SELECT id, asset_name, trigger, queued_at, partition_key, priority, backfill_id
		FROM runs
		WHERE state = 'queued'
		ORDER BY
		    CASE priority
		        WHEN 'critical' THEN 0
		        WHEN 'normal'   THEN 1
		        WHEN 'backfill' THEN 2
		        ELSE 1
		    END ASC,
		    queued_at ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`
	var (
		id           uuid.UUID
		assetName    string
		trigger      string
		queuedAt     time.Time
		partitionKey sql.NullString
		priority     string
		backfillID   uuid.NullUUID
	)
	row := tx.QueryRowContext(ctx, selectSQL)
	if err := row.Scan(&id, &assetName, &trigger, &queuedAt, &partitionKey, &priority, &backfillID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoQueuedRun
		}
		return nil, fmt.Errorf("run: select queued: %w", err)
	}

	// Atomic claim UPDATE: state, claimed_by, claimed_at, last_heartbeat in one shot.
	// last_heartbeat is initialized HERE so the reaper has a non-NULL baseline.
	const updateSQL = `
		UPDATE runs
		   SET state = 'starting',
		       claimed_by = $1,
		       claimed_at = $2,
		       last_heartbeat = $2
		 WHERE id = $3 AND state = 'queued'
	`
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, updateSQL, workerID, now, id)
	if err != nil {
		return nil, fmt.Errorf("run: update claim: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		// Defense-in-depth: if SKIP LOCKED returned a row we shouldn't be able to lose this race,
		// but the CHECK constraint + state pre-filter together make this impossible. Treat it as a bug.
		return nil, fmt.Errorf("run: claim race detected: %d rows affected (expected 1)", n)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("run: commit claim: %w", err)
	}

	claimed := &ClaimedRun{
		ID:        id,
		AssetName: assetName,
		Trigger:   trigger,
		QueuedAt:  queuedAt,
		Priority:  priority,
	}
	if partitionKey.Valid {
		s := partitionKey.String
		claimed.PartitionKey = &s
	}
	if backfillID.Valid {
		u := backfillID.UUID
		claimed.BackfillID = &u
	}
	return claimed, nil
}

// Heartbeat updates the runs.last_heartbeat to NOW() for the given run. Plan 02-03's
// executor calls this every ~30s while a step is running. Returns no error if the row
// is gone (the reaper or a cancel may have moved it); the caller should treat that as
// a signal to abort the current step.
func Heartbeat(ctx context.Context, store storage.Storage, runID uuid.UUID) error {
	const sqlText = `UPDATE runs SET last_heartbeat = NOW() WHERE id = $1 AND state IN ('starting','running')`
	_, err := store.DB().ExecContext(ctx, sqlText, runID)
	if err != nil {
		return fmt.Errorf("run: heartbeat %s: %w", runID, err)
	}
	return nil
}
