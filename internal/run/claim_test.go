package run_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/kanpon/data-governance/internal/run"
	entpkg "github.com/kanpon/data-governance/internal/storage/ent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sqlStorage is a minimal storage.Storage implementation for claim tests
// that only delegates DB() to the underlying *sql.DB. The ent-based methods
// (Ent, WithTx) are not used by ClaimNext or Heartbeat.
type sqlStorage struct {
	db *sql.DB
}

func (s *sqlStorage) Ping(ctx context.Context) error           { return s.db.PingContext(ctx) }
func (s *sqlStorage) Ent() *entpkg.Client                     { return nil }
func (s *sqlStorage) DB() *sql.DB                             { return s.db }
func (s *sqlStorage) Close() error                             { return s.db.Close() }
func (s *sqlStorage) WithTx(_ context.Context, _ func(*entpkg.Tx) error) error {
	return fmt.Errorf("not implemented in test stub")
}

// openTestDB opens a database connection from DATABASE_URL or skips the test.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("requires DATABASE_URL to be set (e.g. postgres://platform_app:platform_app@localhost:5432/platform?sslmode=disable)")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err, "sql.Open failed")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, db.PingContext(ctx), "database ping failed")
	return db
}

// insertQueuedRun inserts a run with state='queued' and returns its id string.
func insertQueuedRun(t *testing.T, db *sql.DB, assetName string) string {
	t.Helper()
	var id string
	const insertSQL = `
		INSERT INTO runs (id, asset_name, state, trigger, queued_at)
		VALUES (gen_random_uuid(), $1, 'queued', 'manual', NOW())
		RETURNING id
	`
	err := db.QueryRowContext(context.Background(), insertSQL, assetName).Scan(&id)
	require.NoError(t, err, "failed to insert queued run")
	return id
}

// deleteRun removes runs with the given asset_name (test cleanup).
func deleteRuns(t *testing.T, db *sql.DB, assetName string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), "DELETE FROM runs WHERE asset_name = $1", assetName)
	require.NoError(t, err)
}

// TestClaimAtomicity50Goroutines spawns 50 concurrent goroutines that each try to
// claim the same single queued run. Asserts exactly one winner and exactly 49
// ErrNoQueuedRun returns, proving that SELECT FOR UPDATE SKIP LOCKED prevents
// double-claiming (ROADMAP acceptance criterion 3, D-17, T-02-02-01).
//
// Also asserts that the winning claim sets last_heartbeat to a non-NULL value
// within 5 seconds of NOW() (D-14 crash recovery foundation).
func TestClaimAtomicity50Goroutines(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	// Clean up before and after test.
	const assetName = "test_asset_50goroutines"
	deleteRuns(t, db, assetName)
	t.Cleanup(func() { deleteRuns(t, db, assetName) })

	// Insert exactly one queued run.
	runID := insertQueuedRun(t, db, assetName)

	// Wrap db in a minimal storage.Storage for ClaimNext.
	store := &sqlStorage{db: db}
	ctx := context.Background()

	var (
		winners atomic.Int32
		noRows  atomic.Int32
		wg      sync.WaitGroup
	)

	const numWorkers = 50
	wg.Add(numWorkers)
	for i := range numWorkers {
		workerID := fmt.Sprintf("worker-%d", i)
		go func() {
			defer wg.Done()
			_, err := run.ClaimNext(ctx, store, workerID)
			if err == nil {
				winners.Add(1)
				return
			}
			if errors.Is(err, run.ErrNoQueuedRun) {
				noRows.Add(1)
				return
			}
			// Unexpected error — log it; it will fail the assertion below.
			t.Errorf("unexpected error from ClaimNext (worker %s): %v", workerID, err)
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), winners.Load(), "exactly one goroutine must win the claim")
	assert.Equal(t, int32(49), noRows.Load(), "the other 49 must get ErrNoQueuedRun")

	// Post-condition: verify the DB row state after claiming.
	var (
		state         string
		claimedBy     sql.NullString
		lastHeartbeat sql.NullTime
	)
	err := db.QueryRowContext(ctx,
		`SELECT state, claimed_by, last_heartbeat FROM runs WHERE id = $1`, runID,
	).Scan(&state, &claimedBy, &lastHeartbeat)
	require.NoError(t, err, "failed to read run row after claim")

	assert.Equal(t, "starting", state, "claimed run must be in 'starting' state")
	assert.True(t, claimedBy.Valid && claimedBy.String != "", "claimed_by must be set")

	require.True(t, lastHeartbeat.Valid, "last_heartbeat must be non-NULL after claim")
	age := time.Since(lastHeartbeat.Time)
	assert.Less(t, age, 5*time.Second,
		"last_heartbeat must be within 5 seconds of NOW() (got age=%v)", age)
}

// TestClaimNextNoQueuedRun verifies that ClaimNext returns ErrNoQueuedRun when
// there are no queued runs in the database.
func TestClaimNextNoQueuedRun(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_no_queued_run"
	deleteRuns(t, db, assetName)

	store := &sqlStorage{db: db}
	ctx := context.Background()

	_, err := run.ClaimNext(ctx, store, "worker-noqueue")
	require.Error(t, err)
	assert.True(t, errors.Is(err, run.ErrNoQueuedRun),
		"expected ErrNoQueuedRun; got: %v", err)
}

// TestClaimNextTwoRuns verifies that two consecutive ClaimNext calls each return
// a distinct run when two queued runs exist.
func TestClaimNextTwoRuns(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetNameA = "test_two_runs_a"
	const assetNameB = "test_two_runs_b"
	deleteRuns(t, db, assetNameA)
	deleteRuns(t, db, assetNameB)
	t.Cleanup(func() {
		deleteRuns(t, db, assetNameA)
		deleteRuns(t, db, assetNameB)
	})

	insertQueuedRun(t, db, assetNameA)
	insertQueuedRun(t, db, assetNameB)

	store := &sqlStorage{db: db}
	ctx := context.Background()

	r1, err := run.ClaimNext(ctx, store, "worker-1")
	require.NoError(t, err, "first claim must succeed")
	r2, err := run.ClaimNext(ctx, store, "worker-2")
	require.NoError(t, err, "second claim must succeed")

	assert.NotEqual(t, r1.ID, r2.ID, "each claim must return a distinct run")
}

// insertWithPriority inserts a queued run with the given asset_name, priority,
// and a queued_at offset (subtracted from NOW()). Returns the inserted UUID.
// Used by TestClaimPriorityOrdering to demonstrate that priority dominates
// queued_at FIFO ordering.
func insertWithPriority(t *testing.T, db *sql.DB, assetName, priority string, queuedAtOffset time.Duration) string {
	t.Helper()
	var id string
	err := db.QueryRowContext(context.Background(),
		`INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority)
		 VALUES (gen_random_uuid(), $1, 'queued', 'manual', NOW() - ($2::text)::interval, $3)
		 RETURNING id`,
		assetName,
		fmt.Sprintf("%d milliseconds", queuedAtOffset.Milliseconds()),
		priority,
	).Scan(&id)
	require.NoError(t, err, "insert queued run with priority=%q failed", priority)
	return id
}

// TestClaimPriorityOrdering proves the CASE priority ORDER BY actually
// reorders claims (D-13 layer 2 — correctness). Insert 5 backfill + 5 normal
// + 1 critical with the BACKFILL rows having the OLDEST queued_at (so any
// FIFO-only implementation would claim them first); call ClaimNext 11 times
// sequentially; assert claims come out in priority tier order:
//   - 1 critical
//   - 5 normals (newest queued_at within tier)
//   - 5 backfills (oldest queued_at within tier)
//
// This test catches both a missing CASE clause AND an integer-mapping
// inversion (which TestPriorityOrderConsistency would also catch).
func TestClaimPriorityOrdering(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test-priority-ordering"
	deleteRuns(t, db, assetName)
	t.Cleanup(func() { deleteRuns(t, db, assetName) })

	// Insert 5 backfills with the OLDEST queued_at — they would claim first
	// under FIFO-only. Each is offset by 10ms within the tier so claim order
	// within the tier is deterministic.
	for i := 0; i < 5; i++ {
		insertWithPriority(t, db, assetName, "backfill", time.Duration(1000-i*10)*time.Millisecond)
	}
	// Insert 5 normal runs in the middle of the queued_at range.
	for i := 0; i < 5; i++ {
		insertWithPriority(t, db, assetName, "normal", time.Duration(500-i*10)*time.Millisecond)
	}
	// Critical with NEWEST queued_at — must still claim FIRST under priority ORDER BY.
	insertWithPriority(t, db, assetName, "critical", 0)

	store := &sqlStorage{db: db}
	gotPriorities := make([]string, 0, 11)
	for i := 0; i < 11; i++ {
		c, err := run.ClaimNext(context.Background(), store, fmt.Sprintf("test-worker-%d", i))
		require.NoErrorf(t, err, "ClaimNext iteration %d failed", i)
		gotPriorities = append(gotPriorities, c.Priority)
	}
	expected := []string{
		"critical",
		"normal", "normal", "normal", "normal", "normal",
		"backfill", "backfill", "backfill", "backfill", "backfill",
	}
	assert.Equal(t, expected, gotPriorities,
		"priority ORDER BY (CASE priority...) did not order claims correctly — drift between Go PriorityOrder and SQL CASE?")
}

// TestPriorityClaimLoad is the deferred load test from D-13 (Pitfall 1
// regression guard at scale). Insert 1000 backfill + 50 normal queued runs
// then run two rounds of 50 concurrent claimers each:
//
//   - Round 1: ALL 50 claims must be 'normal' (no normal-row starvation by
//     backfills); no duplicate claim IDs (SKIP LOCKED atomicity holds at
//     1000+ rows + 50 concurrent transactions).
//   - Round 2: all 50 normals are now claimed, so the next 50 claims must
//     ALL be 'backfill'; again no duplicates.
//
// If Pitfall 1 ever leaked into the WHERE clause (e.g., `WHERE priority !=
// 'backfill'`), round 2 would return ErrNoQueuedRun for all 50 goroutines
// and this test would fail loudly — backfill rows would be stranded.
//
// Skipped under -short to keep `go test -short` fast.
func TestPriorityClaimLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("load test skipped in -short mode")
	}
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test-priority-load"
	deleteRuns(t, db, assetName)
	t.Cleanup(func() { deleteRuns(t, db, assetName) })

	ctx := context.Background()

	// Bulk-insert 1000 backfill + 50 normal in one INSERT each (multi-row VALUES).
	for _, batch := range []struct {
		count    int
		priority string
	}{
		{1000, "backfill"},
		{50, "normal"},
	} {
		values := make([]string, 0, batch.count)
		for i := 0; i < batch.count; i++ {
			values = append(values, "(gen_random_uuid(), $1, 'queued', 'manual', NOW(), $2)")
		}
		q := "INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority) VALUES " + strings.Join(values, ",")
		_, err := db.ExecContext(ctx, q, assetName, batch.priority)
		require.NoErrorf(t, err, "bulk insert of %d %s rows failed", batch.count, batch.priority)
	}

	store := &sqlStorage{db: db}

	// Round 1: 50 concurrent claimers — every claim must be priority='normal'.
	round1 := claimRound(t, ctx, store, 50, "round1")
	require.Equal(t, 50, len(round1.priorities),
		"round 1: expected 50 successful claims, got %d (%v)", len(round1.priorities), round1.errs)
	for i, p := range round1.priorities {
		assert.Equalf(t, "normal", p, "round 1 goroutine %d expected priority=normal, got %q", i, p)
	}
	for id, n := range round1.dedup {
		assert.Equalf(t, 1, n, "round 1 duplicate claim: %s claimed %d times", id, n)
	}

	// Round 2: 50 more concurrent claimers — must all be priority='backfill'
	// because the 50 normal runs are now gone. If Pitfall 1 leaked (e.g.,
	// stray WHERE priority filter), round 2 would be empty.
	round2 := claimRound(t, ctx, store, 50, "round2")
	require.Equal(t, 50, len(round2.priorities),
		"round 2: expected 50 successful claims, got %d (%v) — Pitfall 1 regression?",
		len(round2.priorities), round2.errs)
	for i, p := range round2.priorities {
		assert.Equalf(t, "backfill", p, "round 2 goroutine %d expected priority=backfill, got %q", i, p)
	}
	for id, n := range round2.dedup {
		assert.Equalf(t, 1, n, "round 2 duplicate claim: %s claimed %d times", id, n)
	}
}

// claimRoundResult captures the priorities, dedup map, and any unexpected
// errors observed during a concurrent claim round.
type claimRoundResult struct {
	priorities []string
	dedup      map[uuid.UUID]int
	errs       []error
}

// claimRound spawns `n` concurrent goroutines, each calling ClaimNext once.
// Returns the priorities of successful claims, a dedup map by run ID, and
// any errors that were not ErrNoQueuedRun (which indicate test failure).
func claimRound(t *testing.T, ctx context.Context, store *sqlStorage, n int, label string) claimRoundResult {
	t.Helper()
	var (
		mu         sync.Mutex
		priorities = make([]string, 0, n)
		dedup      = make(map[uuid.UUID]int)
		errs       []error
		wg         sync.WaitGroup
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c, err := run.ClaimNext(ctx, store, fmt.Sprintf("%s-loader-%d", label, idx))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if !errors.Is(err, run.ErrNoQueuedRun) {
					errs = append(errs, err)
				}
				return
			}
			priorities = append(priorities, c.Priority)
			dedup[c.ID]++
		}(i)
	}
	wg.Wait()
	return claimRoundResult{priorities: priorities, dedup: dedup, errs: errs}
}
