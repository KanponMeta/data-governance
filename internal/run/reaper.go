package run

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/storage"
)

// Default reaper tuning. These values pair with plan 02-03's HeartbeatInterval (30s):
//   - DefaultReaperStaleAfter (5m) is 10x HeartbeatInterval; transient ticker delays do not trigger.
//   - DefaultReaperInterval (60s) is the scan cadence; combined with StaleAfter, recovery upper bound
//     is StaleAfter + Interval = ~6 minutes after the worker's LAST successful heartbeat.
const (
	DefaultReaperStaleAfter = 5 * time.Minute
	DefaultReaperInterval   = 60 * time.Second
)

// StaleRunReaper recovers runs whose worker died mid-execution (D-14 Option B).
// It scans every Interval for runs in {starting, running} whose last_heartbeat is older
// than StaleAfter, and resets them to 'queued' via TransitionForReset.
//
// Background: D-14 originally described "River handles infrastructure faults". Phase 2
// implements that intent via this reaper instead, keeping the worker as a single Go
// process. River.max_attempts wiring is deferred to a later phase; see plan 02-04
// <context> for the rationale.
type StaleRunReaper struct {
	Store      storage.Storage
	Events     event.Writer
	StaleAfter time.Duration
	Interval   time.Duration
}

// Run executes the reaper loop until ctx is canceled. Intended to run in a worker
// goroutine spawned at process startup (cmd/platform/worker.go runWorker).
func (r *StaleRunReaper) Run(ctx context.Context) {
	if r.StaleAfter == 0 {
		r.StaleAfter = DefaultReaperStaleAfter
	}
	if r.Interval == 0 {
		r.Interval = DefaultReaperInterval
	}
	slog.Info("reaper.started", "stale_after", r.StaleAfter, "interval", r.Interval)
	t := time.NewTicker(r.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("reaper.stopped")
			return
		case <-t.C:
			n, err := r.SweepOnce(ctx)
			if err != nil {
				slog.Error("reaper.sweep_failed", "error", err)
				continue
			}
			if n > 0 {
				slog.Info("reaper.reclaimed", "count", n)
			}
		}
	}
}

// staleRunRow is the projection used by the reaper SELECT.
type staleRunRow struct {
	ID        uuid.UUID
	State     State
	AssetName string
}

// SweepOnce scans for stale runs and resets each to 'queued'. Returns the number
// of rows reclaimed. Safe to invoke concurrently — the UPDATE WHERE clause filters
// on (state, last_heartbeat) so two concurrent sweeps of the same row produce
// exactly one successful UPDATE.
func (r *StaleRunReaper) SweepOnce(ctx context.Context) (int64, error) {
	if r.StaleAfter == 0 {
		r.StaleAfter = DefaultReaperStaleAfter
	}
	cutoff := time.Now().UTC().Add(-r.StaleAfter)

	// Step 1: SELECT candidates (uses the (state, last_heartbeat) index from plan 02-02).
	const selectSQL = `
		SELECT id, state, asset_name
		  FROM runs
		 WHERE state IN ('starting','running')
		   AND last_heartbeat < $1
	`
	rows, err := r.Store.DB().QueryContext(ctx, selectSQL, cutoff)
	if err != nil {
		return 0, fmt.Errorf("reaper: select stale: %w", err)
	}
	defer rows.Close()

	var candidates []staleRunRow
	for rows.Next() {
		var row staleRunRow
		var stateStr string
		if err := rows.Scan(&row.ID, &stateStr, &row.AssetName); err != nil {
			return 0, fmt.Errorf("reaper: scan stale: %w", err)
		}
		row.State = State(stateStr)
		candidates = append(candidates, row)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("reaper: iterate stale: %w", err)
	}

	// Step 2: For each candidate, validate the FSM edge then UPDATE atomically.
	var reclaimed int64
	for _, c := range candidates {
		// Validate: TransitionForReset is the ONLY legal backward edge (plan 02-02).
		if err := TransitionForReset(c.State, StateQueued); err != nil {
			slog.Warn("reaper.skip_illegal_edge", "run_id", c.ID, "from", c.State)
			continue
		}
		// Atomic UPDATE: filter on (state, last_heartbeat) so a re-emerged worker that
		// just heartbeated does not lose the race.
		const updateSQL = `
			UPDATE runs
			   SET state = 'queued',
			       claimed_by = NULL,
			       claimed_at = NULL,
			       last_heartbeat = NULL,
			       error_message = COALESCE(error_message, '') || ' [reaper: worker heartbeat lost]'
			 WHERE id = $1
			   AND state IN ('starting','running')
			   AND last_heartbeat < $2
		`
		res, err := r.Store.DB().ExecContext(ctx, updateSQL, c.ID, cutoff)
		if err != nil {
			slog.Error("reaper.update_failed", "run_id", c.ID, "error", err)
			continue
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			// Row resurrected (heartbeat just landed) or another reaper instance won.
			continue
		}
		reclaimed++

		// Emit run.canceled event with reason="reaper: worker heartbeat lost".
		if r.Events != nil {
			if err := r.Events.Append(ctx, event.Event{
				Type:         event.EventTypeRunCanceled,
				ResourceType: "run",
				ResourceID:   c.ID.String(),
				Payload: event.RunCanceledPayload{
					AssetName: c.AssetName,
					Reason:    "reaper: worker heartbeat lost",
				},
			}); err != nil {
				slog.Warn("reaper.event_append_failed", "run_id", c.ID, "error", err)
			}
		}
	}
	return reclaimed, nil
}
