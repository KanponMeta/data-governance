package run_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	entpkg "github.com/kanpon/data-governance/internal/storage/ent"
	"github.com/kanpon/data-governance/internal/run"
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
