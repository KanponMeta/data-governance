package run_test

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/run"
)

// TestReaperStaleRunReclaimed (Test 1): stale running row → reset to queued; fresh row untouched.
func TestReaperStaleRunReclaimed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := &sqlStorage{db: db}
	ctx := context.Background()

	assetStale := "reaper-test-stale-" + uuid.NewString()
	assetFresh := "reaper-test-fresh-" + uuid.NewString()
	defer deleteRuns(t, db, assetStale)
	defer deleteRuns(t, db, assetFresh)

	// Insert stale run: state='running', last_heartbeat=NOW()-6m.
	staleID := insertRunWithState(t, db, assetStale, "running", time.Now().UTC().Add(-6*time.Minute))
	// Insert fresh run: state='running', last_heartbeat=NOW().
	freshID := insertRunWithState(t, db, assetFresh, "running", time.Now().UTC())

	reaper := &run.StaleRunReaper{
		Store:      store,
		StaleAfter: 5 * time.Minute,
	}
	n, err := reaper.SweepOnce(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, int64(1), "at least 1 run should be reclaimed")

	// Stale run must be queued now.
	var state string
	var claimedBy sql.NullString
	var lastHB sql.NullTime
	err = db.QueryRowContext(ctx, `SELECT state, claimed_by, last_heartbeat FROM runs WHERE id = $1`, staleID).
		Scan(&state, &claimedBy, &lastHB)
	require.NoError(t, err)
	assert.Equal(t, "queued", state, "stale run should be queued")
	assert.False(t, claimedBy.Valid, "claimed_by should be NULL")
	assert.False(t, lastHB.Valid, "last_heartbeat should be NULL")

	// Fresh run must be unchanged.
	var freshState string
	err = db.QueryRowContext(ctx, `SELECT state FROM runs WHERE id = $1`, freshID).Scan(&freshState)
	require.NoError(t, err)
	assert.Equal(t, "running", freshState, "fresh run should remain running")
}

// TestReaperStartingState (Test 2): state='starting' + stale heartbeat is also eligible.
func TestReaperStartingState(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := &sqlStorage{db: db}
	ctx := context.Background()

	assetName := "reaper-test-starting-" + uuid.NewString()
	defer deleteRuns(t, db, assetName)

	runID := insertRunWithState(t, db, assetName, "starting", time.Now().UTC().Add(-6*time.Minute))

	reaper := &run.StaleRunReaper{Store: store, StaleAfter: 5 * time.Minute}
	n, err := reaper.SweepOnce(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, int64(1))

	var state string
	err = db.QueryRowContext(ctx, `SELECT state FROM runs WHERE id = $1`, runID).Scan(&state)
	require.NoError(t, err)
	assert.Equal(t, "queued", state, "starting + stale → queued")
}

// TestReaperTerminalStateIgnored (Test 3): succeeded run with old heartbeat is never touched.
func TestReaperTerminalStateIgnored(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := &sqlStorage{db: db}
	ctx := context.Background()

	assetName := "reaper-test-terminal-" + uuid.NewString()
	defer deleteRuns(t, db, assetName)

	// Insert succeeded run with old last_heartbeat.
	runID := insertRunWithState(t, db, assetName, "succeeded", time.Now().UTC().Add(-1*time.Hour))

	reaper := &run.StaleRunReaper{Store: store, StaleAfter: 5 * time.Minute}
	_, err := reaper.SweepOnce(ctx)
	require.NoError(t, err)

	var state string
	err = db.QueryRowContext(ctx, `SELECT state FROM runs WHERE id = $1`, runID).Scan(&state)
	require.NoError(t, err)
	assert.Equal(t, "succeeded", state, "terminal state must not be touched by reaper")
}

// TestReaperEventEmitted (Test 4): SweepOnce emits run.canceled event with reason="reaper: worker heartbeat lost".
func TestReaperEventEmitted(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := &sqlStorage{db: db}
	ctx := context.Background()

	assetName := "reaper-test-event-" + uuid.NewString()
	defer deleteRuns(t, db, assetName)

	runID := insertRunWithState(t, db, assetName, "running", time.Now().UTC().Add(-6*time.Minute))

	// Use a simple in-memory recorder for events since the reaper doesn't need ent.
	recorder := &eventRecorder{}

	reaper := &run.StaleRunReaper{
		Store:      store,
		Events:     recorder,
		StaleAfter: 5 * time.Minute,
	}
	n, err := reaper.SweepOnce(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, int64(1))

	events := recorder.Events()
	var found bool
	for _, e := range events {
		if e.Type == event.EventTypeRunCanceled && e.ResourceID == runID.String() {
			payload, ok := e.Payload.(event.RunCanceledPayload)
			require.True(t, ok, "payload should be RunCanceledPayload")
			assert.Equal(t, "reaper: worker heartbeat lost", payload.Reason)
			found = true
			break
		}
	}
	assert.True(t, found, "run.canceled event with reaper reason should be emitted")
}

// TestReaperConcurrentSweepIdempotent (Test 5): two concurrent SweepOnce calls on the same stale row
// result in exactly ONE reclamation (the UPDATE WHERE filters idempotently).
func TestReaperConcurrentSweepIdempotent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := &sqlStorage{db: db}
	ctx := context.Background()

	assetName := "reaper-test-concurrent-" + uuid.NewString()
	defer deleteRuns(t, db, assetName)

	insertRunWithState(t, db, assetName, "running", time.Now().UTC().Add(-6*time.Minute))

	reaper := &run.StaleRunReaper{Store: store, StaleAfter: 5 * time.Minute}

	var totalReclaimed int64
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := reaper.SweepOnce(ctx)
			if err == nil {
				atomic.AddInt64(&totalReclaimed, n)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(1), totalReclaimed, "exactly 1 reclamation despite 2 concurrent sweeps")
}

// TestReaperGoroutineExitsOnCancel (Test 6): Run goroutine ticks SweepOnce every Interval;
// cancel ctx → goroutine exits within 500ms.
func TestReaperGoroutineExitsOnCancel(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := &sqlStorage{db: db}

	assetName := "reaper-test-goroutine-" + uuid.NewString()
	defer deleteRuns(t, db, assetName)

	// Insert a stale run so SweepOnce actually does work on first tick.
	insertRunWithState(t, db, assetName, "running", time.Now().UTC().Add(-6*time.Minute))

	reaper := &run.StaleRunReaper{
		Store:      store,
		StaleAfter: 50 * time.Millisecond, // very short so first tick catches the row
		Interval:   100 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		reaper.Run(ctx)
	}()

	// Wait for at least one tick to fire (250ms > Interval=100ms).
	time.Sleep(250 * time.Millisecond)

	// Cancel and verify goroutine exits quickly.
	cancel()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// goroutine exited cleanly
	case <-time.After(500 * time.Millisecond):
		t.Fatal("reaper goroutine did not exit within 500ms after ctx cancellation")
	}

	// Verify the stale run was reclaimed.
	var state string
	err := db.QueryRowContext(context.Background(), `SELECT state FROM runs WHERE asset_name = $1`, assetName).Scan(&state)
	require.NoError(t, err)
	assert.Equal(t, "queued", state, "stale run should have been reclaimed by the reaper goroutine")
}

// TestSweepOnce_ReturnsCount verifies SweepOnce returns the count of reclaimed rows.
func TestSweepOnce_ReturnsCount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := &sqlStorage{db: db}
	ctx := context.Background()

	// Insert 3 stale runs, 1 fresh.
	names := make([]string, 3)
	for i := range names {
		names[i] = "reaper-count-test-" + uuid.NewString()
		defer deleteRuns(t, db, names[i])
		insertRunWithState(t, db, names[i], "running", time.Now().UTC().Add(-6*time.Minute))
	}
	freshName := "reaper-count-fresh-" + uuid.NewString()
	defer deleteRuns(t, db, freshName)
	insertRunWithState(t, db, freshName, "running", time.Now().UTC())

	reaper := &run.StaleRunReaper{Store: store, StaleAfter: 5 * time.Minute}
	n, err := reaper.SweepOnce(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(3), "should reclaim at least 3 stale rows")
}

// ===== Helpers =====

// insertRunWithState inserts a run with the given state and last_heartbeat.
func insertRunWithState(t *testing.T, db *sql.DB, assetName, state string, lastHB time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	const insertSQL = `
		INSERT INTO runs (id, asset_name, state, trigger, queued_at, claimed_by, claimed_at, last_heartbeat)
		VALUES ($1, $2, $3, 'manual', NOW(), 'test-worker', NOW() - interval '10 minutes', $4)
	`
	_, err := db.ExecContext(context.Background(), insertSQL, id, assetName, state, lastHB)
	require.NoError(t, err, "failed to insert run with state=%s", state)
	return id
}

// eventRecorder is a thread-safe in-memory event.Writer for tests.
type eventRecorder struct {
	mu     sync.Mutex
	events []event.Event
}

func (r *eventRecorder) Append(_ context.Context, e event.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *eventRecorder) Events() []event.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]event.Event, len(r.events))
	copy(out, r.events)
	return out
}
