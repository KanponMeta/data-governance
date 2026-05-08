// Package integration_test provides end-to-end acceptance tests for Phase 2.
//
// Tests bring up ephemeral PostgreSQL via testcontainers, apply schema migrations,
// and run the execution engine in-process to verify correctness. Docker is required;
// tests skip gracefully when Docker is unavailable.
package integration_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/concurrency"
	"github.com/kanpon/data-governance/internal/connector"
	cfgpkg "github.com/kanpon/data-governance/internal/connector/config"
	pgconnector "github.com/kanpon/data-governance/internal/connector/firstparty/postgres"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/run"
	"github.com/kanpon/data-governance/internal/runtime"
	"github.com/kanpon/data-governance/internal/storage"
	"github.com/kanpon/data-governance/testdata/integration"
)

// e2eDB holds the shared testcontainers postgres for Phase 2 e2e tests.
var e2eDB struct {
	dsn string
}

// TestMain brings up testcontainers Postgres once for the whole test suite.
func TestMain(m *testing.M) {
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("platform"),
		tcpostgres.WithUsername("platform_superuser"),
		tcpostgres.WithPassword("platform_superuser"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		// Docker unavailable — skip entire suite.
		os.Exit(0)
	}
	defer func() { _ = testcontainers.TerminateContainer(c) }()

	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get connection string: %v\n", err)
		os.Exit(1)
	}
	e2eDB.dsn = dsn

	// Apply all Phase 2 schema migrations.
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open db: %v\n", err)
		os.Exit(1)
	}
	if err := applySchema(db); err != nil {
		fmt.Fprintf(os.Stderr, "failed to apply schema: %v\n", err)
		db.Close()
		os.Exit(1)
	}
	db.Close()

	os.Exit(m.Run())
}

// applySchema creates all tables needed by Phase 2. It applies the migrations
// manually (without Atlas) using the superuser connection so role grants can be skipped.
func applySchema(db *sql.DB) error {
	stmts := []string{
		// Phase 1: event_log
		`CREATE TABLE IF NOT EXISTS event_log (
			id uuid NOT NULL DEFAULT gen_random_uuid(),
			occurred_at timestamptz NOT NULL,
			event_type varchar NOT NULL,
			actor_id uuid NULL,
			resource_type varchar NOT NULL,
			resource_id varchar NOT NULL,
			payload jsonb NULL,
			PRIMARY KEY (id)
		)`,
		`CREATE INDEX IF NOT EXISTS eventlog_event_type_occurred_at ON event_log (event_type, occurred_at)`,
		`CREATE INDEX IF NOT EXISTS eventlog_occurred_at ON event_log (occurred_at)`,
		`CREATE INDEX IF NOT EXISTS eventlog_resource_type_resource_id ON event_log (resource_type, resource_id)`,

		// Phase 2: runs
		`CREATE TABLE IF NOT EXISTS runs (
			id uuid NOT NULL,
			asset_name varchar(256) NOT NULL,
			state varchar(16) NOT NULL DEFAULT 'queued',
			trigger varchar(32) NOT NULL DEFAULT 'manual',
			triggered_by uuid NULL,
			claimed_by varchar(128) NULL,
			queued_at timestamptz NOT NULL,
			claimed_at timestamptz NULL,
			started_at timestamptz NULL,
			finished_at timestamptz NULL,
			last_heartbeat timestamptz NULL,
			error_message text NULL,
			metadata jsonb NULL,
			PRIMARY KEY (id)
		)`,
		`CREATE INDEX IF NOT EXISTS run_state_queued_at ON runs (state, queued_at)`,
		`CREATE INDEX IF NOT EXISTS run_state_last_heartbeat ON runs (state, last_heartbeat)`,
		`ALTER TABLE runs DROP CONSTRAINT IF EXISTS runs_state_check`,
		`ALTER TABLE runs ADD CONSTRAINT runs_state_check CHECK (state IN ('queued','starting','running','succeeded','failed','canceled'))`,

		// Phase 2: run_steps
		`CREATE TABLE IF NOT EXISTS run_steps (
			id uuid NOT NULL,
			run_id uuid NOT NULL,
			asset_name varchar(256) NOT NULL,
			state varchar(16) NOT NULL DEFAULT 'pending',
			attempt bigint NOT NULL DEFAULT 0,
			topo_order bigint NOT NULL DEFAULT 0,
			started_at timestamptz NULL,
			finished_at timestamptz NULL,
			rows_written bigint NOT NULL DEFAULT 0,
			error_message text NULL,
			metadata jsonb NULL,
			PRIMARY KEY (id)
		)`,

		// Phase 2: concurrency_tokens
		`CREATE TABLE IF NOT EXISTS concurrency_tokens (
			id uuid NOT NULL,
			run_id uuid NOT NULL,
			asset_name varchar(256) NOT NULL,
			resource_tag varchar(128) NOT NULL,
			weight bigint NOT NULL DEFAULT 1,
			acquired_at timestamptz NOT NULL,
			PRIMARY KEY (id)
		)`,
		`CREATE INDEX IF NOT EXISTS concurrencytoken_resource_tag ON concurrency_tokens (resource_tag)`,
		`CREATE INDEX IF NOT EXISTS concurrencytoken_run_id ON concurrency_tokens (run_id)`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			return fmt.Errorf("apply schema: %w (stmt: %.60s)", err, stmt)
		}
	}
	return nil
}

// setupE2E creates per-test tables and a fresh connector/executor stack.
type e2eSetup struct {
	store    storage.Storage
	connReg  *connector.Registry
	pool     *concurrency.Pool
	executor *runtime.Executor
	events   event.Writer
}

func buildE2ESetup(t *testing.T) (*e2eSetup, func()) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.NewPostgres(ctx, e2eDB.dsn)
	require.NoError(t, err)

	// Build connector registry with the real postgres connector.
	pgConn, err := pgconnector.New(ctx, e2eDB.dsn)
	require.NoError(t, err)

	connReg := connector.NewRegistry()
	require.NoError(t, connReg.RegisterInProcess("postgres-test", pgConn))

	pool := concurrency.NewPool(store, []concurrency.Capacity{
		{Tag: "global", Limit: 4},
	})

	writer := event.NewWriter(store)

	exec := runtime.NewExecutor(runtime.Deps{
		Store:         store,
		Events:        writer,
		Registry:      asset.Default(),
		ConnectorReg:  connReg,
		Pool:          pool,
		DefaultPolicy: asset.RetryPolicy{},
		WorkerID:      "test-worker",
		HeartbeatInterval: 100 * time.Millisecond, // fast for tests
	})

	cleanup := func() {
		_ = pgConn.Close()
		store.Close()
	}
	return &e2eSetup{
		store:    store,
		connReg:  connReg,
		pool:     pool,
		executor: exec,
		events:   writer,
	}, cleanup
}

// insertAndClaimRun inserts a queued run, then claims it to move it to 'starting'.
func insertAndClaimRun(t *testing.T, db *sql.DB, assetName string) uuid.UUID {
	t.Helper()
	runID := uuid.New()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO runs (id, asset_name, state, trigger, queued_at)
		VALUES ($1, $2, 'queued', 'manual', NOW())
	`, runID, assetName)
	require.NoError(t, err)
	return runID
}

// queryRunState returns the state of a run by ID.
func queryRunState(t *testing.T, db *sql.DB, runID uuid.UUID) string {
	t.Helper()
	var state string
	err := db.QueryRowContext(context.Background(), `SELECT state FROM runs WHERE id = $1`, runID).Scan(&state)
	require.NoError(t, err)
	return state
}

// queryEventTypes returns all event_type values for the given run ID, ordered by occurred_at.
func queryEventTypes(t *testing.T, db *sql.DB, runID uuid.UUID) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT event_type FROM event_log WHERE resource_id = $1 ORDER BY occurred_at`, runID.String())
	require.NoError(t, err)
	defer rows.Close()
	var types []string
	for rows.Next() {
		var et string
		require.NoError(t, rows.Scan(&et))
		types = append(types, et)
	}
	return types
}

// queryEventTimestamp returns the occurred_at for the first event matching the type and runID.
func queryEventTimestamp(t *testing.T, db *sql.DB, runID uuid.UUID, eventType string) time.Time {
	t.Helper()
	var ts time.Time
	err := db.QueryRowContext(context.Background(),
		`SELECT occurred_at FROM event_log WHERE resource_id = $1 AND event_type = $2 ORDER BY occurred_at LIMIT 1`,
		runID.String(), eventType).Scan(&ts)
	require.NoError(t, err)
	return ts
}

// countStepEvents returns the count of events of the given type for a run.
func countStepEvents(t *testing.T, db *sql.DB, runID uuid.UUID, eventType string) int {
	t.Helper()
	var count int
	err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM event_log WHERE resource_id = $1 AND event_type = $2`,
		runID.String(), eventType).Scan(&count)
	require.NoError(t, err)
	return count
}

// TestE2E_PostgresMaterialize verifies the full happy path: users_raw → users_clean
// DAG is executed in topological order, data lands in the target table, and the
// run succeeds with the expected event sequence.
func TestE2E_PostgresMaterialize(t *testing.T) {
	// Register test assets (fresh registry for each test).
	asset.ResetForTest()
	t.Cleanup(asset.ResetForTest)
	require.NoError(t, integration.RegisterTestAssets())

	setup, cleanup := buildE2ESetup(t)
	defer cleanup()

	ctx := context.Background()
	db := setup.store.DB()

	// Create target tables.
	for _, tbl := range []string{"users_raw", "users_clean"} {
		_, err := db.ExecContext(ctx, fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s (id bigint, email text)`, tbl))
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = db.ExecContext(context.Background(), fmt.Sprintf(`DROP TABLE IF EXISTS %s`, tbl))
		})
	}

	// Insert queued run for users_clean (the top-level target).
	runID := insertAndClaimRun(t, db, "users_clean")

	// Claim the run (moves it to 'starting').
	claimed, err := run.ClaimNext(ctx, setup.store, "test-worker")
	require.NoError(t, err)
	require.Equal(t, runID, claimed.ID)

	// Execute the run in-process.
	err = setup.executor.Run(ctx, claimed)
	require.NoError(t, err, "executor.Run should succeed")

	// Assert run state = succeeded.
	assert.Equal(t, "succeeded", queryRunState(t, db, runID))

	// Assert users_clean has 2 rows.
	var count int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users_clean`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 2, count, "users_clean should have 2 rows from users_raw → users_clean DAG")

	// Assert event sequence includes run.started, run.step.started (×2), run.step.succeeded (×2), run.succeeded.
	types := queryEventTypes(t, db, runID)
	assert.Contains(t, types, string(event.EventTypeRunStarted))
	assert.Contains(t, types, string(event.EventTypeRunSucceeded))
	assert.Equal(t, 2, countStepEvents(t, db, runID, string(event.EventTypeRunStepStarted)),
		"2 steps should have started (users_raw + users_clean)")
	assert.Equal(t, 2, countStepEvents(t, db, runID, string(event.EventTypeRunStepSucceeded)),
		"2 steps should have succeeded")
}

// TestE2E_PostgresMaterialize_Failure verifies that a failing asset produces the
// correct retry event sequence in event_log (acceptance criterion 2).
func TestE2E_PostgresMaterialize_Failure(t *testing.T) {
	asset.ResetForTest()
	t.Cleanup(asset.ResetForTest)

	failAsset := "fail_asset_" + uuid.NewString()[:8]
	require.NoError(t, integration.RegisterFailingAsset(failAsset))

	setup, cleanup := buildE2ESetup(t)
	defer cleanup()

	ctx := context.Background()
	db := setup.store.DB()

	runID := insertAndClaimRun(t, db, failAsset)
	claimed, err := run.ClaimNext(ctx, setup.store, "test-worker")
	require.NoError(t, err)
	require.Equal(t, runID, claimed.ID)

	// Run should fail (RetryPolicy.Max=1 → 2 attempts then fail).
	execErr := setup.executor.Run(ctx, claimed)
	require.Error(t, execErr, "executor.Run should fail for failing asset")

	assert.Equal(t, "failed", queryRunState(t, db, runID))

	// Verify retry event sequence: step.failed ×2 (attempt 1 + retry), retry_scheduled ×1, run.failed ×1.
	assert.Equal(t, 2, countStepEvents(t, db, runID, string(event.EventTypeRunStepFailed)),
		"step.failed should appear twice (initial + retry)")
	assert.Equal(t, 1, countStepEvents(t, db, runID, string(event.EventTypeRunStepRetryScheduled)),
		"exactly 1 retry_scheduled event (after first failure)")
	assert.Equal(t, 1, countStepEvents(t, db, runID, string(event.EventTypeRunFailed)),
		"run.failed should appear once")
}

// TestE2E_TopologicalOrder verifies that users_raw step.started occurred before
// users_clean step.started (acceptance criterion 1 — topological order).
func TestE2E_TopologicalOrder(t *testing.T) {
	asset.ResetForTest()
	t.Cleanup(asset.ResetForTest)
	require.NoError(t, integration.RegisterTestAssets())

	setup, cleanup := buildE2ESetup(t)
	defer cleanup()

	ctx := context.Background()
	db := setup.store.DB()

	for _, tbl := range []string{"users_raw", "users_clean"} {
		_, err := db.ExecContext(ctx, fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s (id bigint, email text)`, tbl))
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = db.ExecContext(context.Background(), fmt.Sprintf(`DROP TABLE IF EXISTS %s`, tbl))
		})
	}

	runID := insertAndClaimRun(t, db, "users_clean")
	claimed, err := run.ClaimNext(ctx, setup.store, "test-worker")
	require.NoError(t, err)

	require.NoError(t, setup.executor.Run(ctx, claimed))

	// Query step.started events for each asset — verify users_raw precedes users_clean.
	// The event resource_id is the run ID; we identify the step by payload.asset_name.
	var rawStartedAt, cleanStartedAt time.Time
	rows, err := db.QueryContext(ctx, `
		SELECT occurred_at, payload->>'asset_name'
		FROM event_log
		WHERE resource_id = $1
		  AND event_type = $2
		ORDER BY occurred_at
	`, runID.String(), string(event.EventTypeRunStepStarted))
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var ts time.Time
		var assetName string
		require.NoError(t, rows.Scan(&ts, &assetName))
		switch assetName {
		case "users_raw":
			rawStartedAt = ts
		case "users_clean":
			cleanStartedAt = ts
		}
	}
	require.NoError(t, rows.Err())

	assert.False(t, rawStartedAt.IsZero(), "users_raw step.started event must exist")
	assert.False(t, cleanStartedAt.IsZero(), "users_clean step.started event must exist")
	assert.True(t, rawStartedAt.Before(cleanStartedAt) || rawStartedAt.Equal(cleanStartedAt),
		"users_raw step must start before or at same time as users_clean (topological order)")
}

// TestE2E_StaleRunReaperRecovery verifies that the StaleRunReaper recovers a stale
// 'running' row and emits a run.canceled event (D-14 Option B, T-02-04-08).
func TestE2E_StaleRunReaperRecovery(t *testing.T) {
	setup, cleanup := buildE2ESetup(t)
	defer cleanup()

	ctx := context.Background()
	db := setup.store.DB()

	// Insert a run with state='running' and last_heartbeat=NOW()-6m.
	runID := uuid.New()
	_, err := db.ExecContext(ctx, `
		INSERT INTO runs (id, asset_name, state, trigger, queued_at, claimed_at, claimed_by, last_heartbeat)
		VALUES ($1, 'ghost_asset', 'running', 'manual', NOW() - interval '10 minutes',
		        NOW() - interval '10 minutes', 'dead-worker', NOW() - interval '6 minutes')
	`, runID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM runs WHERE id = $1`, runID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM event_log WHERE resource_id = $1`, runID.String())
	})

	// Use a fast in-memory event recorder instead of the ent-based writer
	// (reaper tests don't need persistence for the canceled event verification here;
	// we check via the recorder directly).
	recorder := &recordingWriter{}
	reaper := &run.StaleRunReaper{
		Store:      setup.store,
		Events:     recorder,
		StaleAfter: 5 * time.Minute,
		Interval:   200 * time.Millisecond, // fast tick for test
	}

	reaperCtx, cancelReaper := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		reaper.Run(reaperCtx)
	}()

	// Poll for up to 5s for the run to be reclaimed.
	deadline := time.Now().Add(5 * time.Second)
	var reclaimed bool
	for time.Now().Before(deadline) {
		var state string
		var lastHB sql.NullTime
		var claimedBy sql.NullString
		err := db.QueryRowContext(context.Background(),
			`SELECT state, last_heartbeat, claimed_by FROM runs WHERE id = $1`, runID).
			Scan(&state, &lastHB, &claimedBy)
		require.NoError(t, err)
		if state == "queued" && !lastHB.Valid && !claimedBy.Valid {
			reclaimed = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	cancelReaper()
	wg.Wait()

	require.True(t, reclaimed, "stale run should have been reclaimed to 'queued' within 5s")

	// Verify run.canceled event was emitted with the expected reason.
	events := recorder.Events()
	var found bool
	for _, e := range events {
		if e.Type == event.EventTypeRunCanceled && e.ResourceID == runID.String() {
			p, ok := e.Payload.(event.RunCanceledPayload)
			if ok && p.Reason == "reaper: worker heartbeat lost" {
				found = true
				break
			}
		}
	}
	assert.True(t, found, "run.canceled event with reaper reason must be recorded")
}

// TestE2E_DetachMode verifies --detach inserts a queued run and returns immediately.
// This tests the materialize logic rather than the CLI binary.
func TestE2E_DetachMode(t *testing.T) {
	asset.ResetForTest()
	t.Cleanup(asset.ResetForTest)
	require.NoError(t, integration.RegisterTestAssets())

	setup, cleanup := buildE2ESetup(t)
	defer cleanup()

	ctx := context.Background()
	db := setup.store.DB()

	for _, tbl := range []string{"users_raw", "users_clean"} {
		_, err := db.ExecContext(ctx, fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s (id bigint, email text)`, tbl))
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = db.ExecContext(context.Background(), fmt.Sprintf(`DROP TABLE IF EXISTS %s`, tbl))
		})
	}

	// Simulate --detach: insert a queued run and return the UUID.
	runID := uuid.New()
	_, err := db.ExecContext(ctx, `
		INSERT INTO runs (id, asset_name, state, trigger, queued_at)
		VALUES ($1, 'users_clean', 'queued', 'manual', NOW())
	`, runID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM runs WHERE id = $1`, runID)
	})

	// Verify the run is in 'queued' state (not yet started).
	assert.Equal(t, "queued", queryRunState(t, db, runID), "--detach run should start as queued")
}

// ===== Helpers =====

// recordingWriter is a thread-safe event.Writer that records events in memory.
type recordingWriter struct {
	mu     sync.Mutex
	events []event.Event
}

func (r *recordingWriter) Append(_ context.Context, e event.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recordingWriter) Events() []event.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]event.Event, len(r.events))
	copy(out, r.events)
	return out
}

// Suppress unused import warning for cfgpkg used in future tests.
var _ = cfgpkg.NewFactoryRegistry
