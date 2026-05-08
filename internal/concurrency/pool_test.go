package concurrency_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/concurrency"
	"github.com/kanpon/data-governance/internal/storage"
	"github.com/kanpon/data-governance/internal/storage/ent"
)

// rawStorage satisfies storage.Storage using only a *sql.DB.
// The ent client is nil; pool.go only uses DB() and never calls Ent() or WithTx().
type rawStorage struct {
	db *sql.DB
}

var _ storage.Storage = (*rawStorage)(nil)

func (s *rawStorage) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *rawStorage) DB() *sql.DB                    { return s.db }
func (s *rawStorage) Ent() *ent.Client               { return nil }
func (s *rawStorage) Close() error                   { return s.db.Close() }
func (s *rawStorage) WithTx(ctx context.Context, fn func(tx *ent.Tx) error) error {
	return errors.New("WithTx not implemented in test stub")
}

// setupPool creates a Pool with the given capacities using DATABASE_URL.
// Calls t.Skip if DATABASE_URL is not set.
func setupPool(t *testing.T, caps []concurrency.Capacity) (*concurrency.Pool, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed concurrency tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		t.Fatalf("ping db: %v", err)
	}
	s := &rawStorage{db: db}
	pool := concurrency.NewPool(s, caps)
	return pool, db
}

// cleanupTokens removes all concurrency_tokens rows for a given runID.
func cleanupTokens(t *testing.T, db *sql.DB, runID uuid.UUID) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`DELETE FROM concurrency_tokens WHERE run_id = $1`, runID)
	if err != nil {
		t.Logf("cleanup tokens for run %s: %v", runID, err)
	}
}

// TestPool_AcquireCapacity1_SecondFails verifies that when capacity for a tag is 1,
// a second Acquire from a different runID returns ErrCapacity.
func TestPool_AcquireCapacity1_SecondFails(t *testing.T) {
	pool, db := setupPool(t, []concurrency.Capacity{
		{Tag: "postgres-prod", Limit: 1},
	})
	defer db.Close()

	ctx := context.Background()
	runID1 := uuid.New()
	runID2 := uuid.New()

	t.Cleanup(func() { cleanupTokens(t, db, runID1) })
	t.Cleanup(func() { cleanupTokens(t, db, runID2) })

	// First acquire should succeed.
	if err := pool.Acquire(ctx, runID1, "asset-a", "postgres-prod", 1); err != nil {
		t.Fatalf("first Acquire failed: %v", err)
	}

	// Second acquire from different runID should fail with ErrCapacity.
	err := pool.Acquire(ctx, runID2, "asset-b", "postgres-prod", 1)
	if !errors.Is(err, concurrency.ErrCapacity) {
		t.Fatalf("expected ErrCapacity, got: %v", err)
	}
}

// TestPool_ResourceIsolation verifies that postgres-prod and snowflake-prod tags
// do not block each other when each has capacity 1.
func TestPool_ResourceIsolation(t *testing.T) {
	pool, db := setupPool(t, []concurrency.Capacity{
		{Tag: "postgres-prod-isolation", Limit: 1},
		{Tag: "snowflake-prod-isolation", Limit: 1},
	})
	defer db.Close()

	ctx := context.Background()
	runID1 := uuid.New()
	runID2 := uuid.New()

	t.Cleanup(func() { cleanupTokens(t, db, runID1) })
	t.Cleanup(func() { cleanupTokens(t, db, runID2) })

	// Acquire postgres-prod
	if err := pool.Acquire(ctx, runID1, "asset-a", "postgres-prod-isolation", 1); err != nil {
		t.Fatalf("Acquire postgres-prod failed: %v", err)
	}

	// Acquire snowflake-prod with a different runID — should succeed independently.
	if err := pool.Acquire(ctx, runID2, "asset-b", "snowflake-prod-isolation", 1); err != nil {
		t.Fatalf("Acquire snowflake-prod failed (resource isolation broken): %v", err)
	}
}

// TestPool_ReleaseFreesCapacity verifies that Release allows a subsequent Acquire.
func TestPool_ReleaseFreesCapacity(t *testing.T) {
	pool, db := setupPool(t, []concurrency.Capacity{
		{Tag: "test-release-tag", Limit: 1},
	})
	defer db.Close()

	ctx := context.Background()
	runID1 := uuid.New()
	runID2 := uuid.New()

	t.Cleanup(func() { cleanupTokens(t, db, runID1) })
	t.Cleanup(func() { cleanupTokens(t, db, runID2) })

	// Acquire with runID1
	if err := pool.Acquire(ctx, runID1, "asset-a", "test-release-tag", 1); err != nil {
		t.Fatalf("first Acquire failed: %v", err)
	}

	// Release by runID1
	if err := pool.Release(ctx, runID1, "test-release-tag"); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	// Now runID2 should succeed
	if err := pool.Acquire(ctx, runID2, "asset-b", "test-release-tag", 1); err != nil {
		t.Fatalf("second Acquire after Release failed: %v", err)
	}
}

// TestPool_WeightRespected verifies that weight semantics are correct.
// With capacity 3: weight=2 succeeds, then weight=2 fails (only 1 remaining),
// weight=1 succeeds.
func TestPool_WeightRespected(t *testing.T) {
	pool, db := setupPool(t, []concurrency.Capacity{
		{Tag: "weighted-res-tag", Limit: 3},
	})
	defer db.Close()

	ctx := context.Background()
	runID1 := uuid.New()
	runID2 := uuid.New()
	runID3 := uuid.New()

	t.Cleanup(func() { cleanupTokens(t, db, runID1) })
	t.Cleanup(func() { cleanupTokens(t, db, runID2) })
	t.Cleanup(func() { cleanupTokens(t, db, runID3) })

	// First: weight=2 of capacity 3 — succeeds (uses 2/3).
	if err := pool.Acquire(ctx, runID1, "asset-a", "weighted-res-tag", 2); err != nil {
		t.Fatalf("Acquire weight=2 failed: %v", err)
	}

	// Second: weight=2 — fails (1 remaining, need 2).
	err := pool.Acquire(ctx, runID2, "asset-b", "weighted-res-tag", 2)
	if !errors.Is(err, concurrency.ErrCapacity) {
		t.Fatalf("expected ErrCapacity for weight=2 on 1 remaining, got: %v", err)
	}

	// Third: weight=1 — succeeds (1 remaining).
	if err := pool.Acquire(ctx, runID3, "asset-c", "weighted-res-tag", 1); err != nil {
		t.Fatalf("Acquire weight=1 on 1 remaining failed: %v", err)
	}
}

// TestPool_ReleaseStale_RemovesOldRows verifies that ReleaseStale removes tokens
// older than the given staleness threshold.
func TestPool_ReleaseStale_RemovesOldRows(t *testing.T) {
	pool, db := setupPool(t, []concurrency.Capacity{
		{Tag: "stale-res-tag", Limit: 10},
	})
	defer db.Close()

	ctx := context.Background()
	runID := uuid.New()

	t.Cleanup(func() { cleanupTokens(t, db, runID) })

	// Insert a token directly with a very old acquired_at to simulate a crashed worker.
	oldTime := time.Now().UTC().Add(-10 * time.Minute)
	_, err := db.ExecContext(ctx,
		`INSERT INTO concurrency_tokens (id, run_id, asset_name, resource_tag, weight, acquired_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		uuid.New(), runID, "asset-stale", "stale-res-tag", 1, oldTime)
	if err != nil {
		t.Fatalf("insert stale token: %v", err)
	}

	// ReleaseStale with 5-minute threshold — should remove the 10-minute-old token.
	n, err := pool.ReleaseStale(ctx, 5*time.Minute)
	if err != nil {
		t.Fatalf("ReleaseStale: %v", err)
	}
	if n == 0 {
		t.Fatal("expected ReleaseStale to remove at least 1 row; got 0")
	}

	// Verify the row is gone.
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM concurrency_tokens WHERE run_id = $1`, runID,
	).Scan(&count); err != nil {
		t.Fatalf("count check: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 tokens after ReleaseStale, got %d", count)
	}
}
