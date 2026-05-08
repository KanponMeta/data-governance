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
type ClaimedRun struct {
	ID        uuid.UUID
	AssetName string
	Trigger   string
	QueuedAt  time.Time
}

// ClaimNext atomically picks one queued run and transitions it to 'starting',
// recording the claim by `workerID` and stamping last_heartbeat=NOW(). The query
// uses SELECT ... FOR UPDATE SKIP LOCKED so 50 concurrent workers never claim the
// same row (D-17 + ROADMAP criterion 3). last_heartbeat enables plan 02-04's
// stale-run reaper to detect crashed workers (Option B for D-14 crash recovery).
//
// Returns ErrNoQueuedRun when no eligible row exists.
//
// The claim is performed in a single database transaction:
//  1. SELECT the oldest queued run with FOR UPDATE SKIP LOCKED (FIFO, no double-claim)
//  2. UPDATE the same row atomically: state→starting, claimed_by, claimed_at, last_heartbeat
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

	const selectSQL = `
		SELECT id, asset_name, trigger, queued_at
		FROM runs
		WHERE state = 'queued'
		ORDER BY queued_at
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`
	var (
		id        uuid.UUID
		assetName string
		trigger   string
		queuedAt  time.Time
	)
	row := tx.QueryRowContext(ctx, selectSQL)
	if err := row.Scan(&id, &assetName, &trigger, &queuedAt); err != nil {
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
	return &ClaimedRun{ID: id, AssetName: assetName, Trigger: trigger, QueuedAt: queuedAt}, nil
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
