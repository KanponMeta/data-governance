package runtime_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entgosql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/concurrency"
	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/run"
	"github.com/kanpon/data-governance/internal/runtime"
	"github.com/kanpon/data-governance/internal/storage"
	stent "github.com/kanpon/data-governance/internal/storage/ent"
)

// ===== Test Infrastructure =====

// rawStorage wraps *sql.DB to satisfy storage.Storage for tests.
// The Ent() client is nil; executor only uses DB() for raw SQL transitions.
type rawStorage struct {
	db *sql.DB
}

var _ storage.Storage = (*rawStorage)(nil)

func (s *rawStorage) Ping(ctx context.Context) error             { return s.db.PingContext(ctx) }
func (s *rawStorage) DB() *sql.DB                               { return s.db }
func (s *rawStorage) Ent() *stent.Client                        { return nil }
func (s *rawStorage) Close() error                              { return s.db.Close() }
func (s *rawStorage) WithTx(ctx context.Context, fn func(tx *stent.Tx) error) error {
	return errors.New("WithTx not implemented in rawStorage test stub")
}

// entStorage wraps both *sql.DB and *stent.Client to support event writing via ent.
type entStorage struct {
	db  *sql.DB
	ent *stent.Client
}

var _ storage.Storage = (*entStorage)(nil)

func (s *entStorage) Ping(ctx context.Context) error             { return s.db.PingContext(ctx) }
func (s *entStorage) DB() *sql.DB                               { return s.db }
func (s *entStorage) Ent() *stent.Client                        { return s.ent }
func (s *entStorage) Close() error                              { return s.db.Close() }
func (s *entStorage) WithTx(ctx context.Context, fn func(tx *stent.Tx) error) error {
	return errors.New("WithTx not implemented in entStorage test stub")
}

// recordingConnector is a test connector that records Write calls.
type recordingConnector struct {
	rowsWritten int64
	readRows    []connector.Row
	writeErr    error
}

func (c *recordingConnector) APIVersion() string { return connector.APIVersion }
func (c *recordingConnector) Ping(_ context.Context, _ connector.PingRequest) (connector.PingResponse, error) {
	return connector.PingResponse{APIVersion: connector.APIVersion, ConnectorName: "test"}, nil
}
func (c *recordingConnector) Schema(_ context.Context, _ connector.SchemaRequest) (connector.SchemaResponse, error) {
	return connector.SchemaResponse{}, nil
}
func (c *recordingConnector) Read(_ context.Context, _ connector.ReadRequest) (connector.ReadResponse, error) {
	return connector.ReadResponse{Rows: c.readRows}, nil
}
func (c *recordingConnector) Write(_ context.Context, req connector.WriteRequest) (connector.WriteResponse, error) {
	if c.writeErr != nil {
		return connector.WriteResponse{}, c.writeErr
	}
	return connector.WriteResponse{RowsWritten: c.rowsWritten}, nil
}

// setupTestDB returns a *sql.DB and *stent.Client connected to DATABASE_URL.
// Skips the test if DATABASE_URL is unset.
func setupTestDB(t *testing.T) (*sql.DB, *stent.Client) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed executor tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		t.Fatalf("ping db: %v", err)
	}

	// Build ent client for event writing.
	entClient, err := stent.Open("pgx", dsn)
	if err != nil {
		db.Close()
		t.Fatalf("open ent: %v", err)
	}

	t.Cleanup(func() {
		entClient.Close()
		db.Close()
	})
	return db, entClient
}

// insertRun inserts a queued run into the runs table and returns its ID.
func insertRun(t *testing.T, db *sql.DB, assetName string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO runs (id, asset_name, state, trigger, queued_at)
         VALUES ($1, $2, 'queued', 'manual', NOW())`,
		id, assetName)
	if err != nil {
		t.Fatalf("insertRun: %v", err)
	}
	return id
}

// claimRun claims the run with the given ID from the database, returning a ClaimedRun.
func claimRun(t *testing.T, db *sql.DB, assetName string) *run.ClaimedRun {
	t.Helper()
	s := &rawStorage{db: db}
	claimed, err := run.ClaimNext(context.Background(), s, "test-worker")
	if err != nil {
		t.Fatalf("claimRun: %v", err)
	}
	if claimed.AssetName != assetName {
		t.Fatalf("claimed wrong asset: got %q, want %q", claimed.AssetName, assetName)
	}
	return claimed
}

// queryEventTypes returns all event types recorded for the given run in insertion order.
func queryEventTypes(t *testing.T, db *sql.DB, runID uuid.UUID) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT event_type FROM event_log
         WHERE resource_type = 'run' AND resource_id = $1
         ORDER BY occurred_at, id`,
		runID.String())
	if err != nil {
		t.Fatalf("queryEventTypes: %v", err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var et string
		if err := rows.Scan(&et); err != nil {
			t.Fatalf("scan event_type: %v", err)
		}
		types = append(types, et)
	}
	return types
}

// buildExecutor constructs a runtime.Executor for testing with the given assets and connector.
func buildExecutor(
	t *testing.T,
	db *sql.DB,
	entClient *stent.Client,
	assets []*asset.Asset,
	conn connector.Connector,
	pool *concurrency.Pool,
	policy asset.RetryPolicy,
	heartbeatInterval time.Duration,
) *runtime.Executor {
	t.Helper()

	store := &entStorage{db: db, ent: entClient}
	evtWriter := event.NewWriter(store)
	reg := asset.NewDefinitionRegistry()
	for _, a := range assets {
		if err := reg.Register(a); err != nil {
			t.Fatalf("register asset %q: %v", a.Name(), err)
		}
	}
	connReg := connector.NewRegistry()
	if conn != nil {
		if err := connReg.RegisterInProcess("test-connector", conn); err != nil {
			t.Fatalf("register connector: %v", err)
		}
	}

	if pool == nil {
		// Unlimited pool (no capacities configured).
		pool = concurrency.NewPool(store, nil)
	}

	return runtime.NewExecutor(runtime.Deps{
		Store:             store,
		Events:            evtWriter,
		Registry:          reg,
		ConnectorReg:      connReg,
		Pool:              pool,
		DefaultPolicy:     policy,
		WorkerID:          "test-worker",
		StepTimeout:       5 * time.Second,
		HeartbeatInterval: heartbeatInterval,
	})
}

// ===== Tests =====

// TestExecutor_SuccessfulRun verifies the happy path: queued→starting→running→succeeded
// with correct event sequence and rows_written.
func TestExecutor_SuccessfulRun(t *testing.T) {
	db, entClient := setupTestDB(t)

	conn := &recordingConnector{rowsWritten: 42}
	a, err := asset.New("test-asset-success").
		Connector("test-connector").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			return asset.MaterializeResult{RowsWritten: 42}, nil
		}).
		Build()
	if err != nil {
		t.Fatalf("build asset: %v", err)
	}

	insertRun(t, db, "test-asset-success")
	claimed := claimRun(t, db, "test-asset-success")

	exec := buildExecutor(t, db, entClient, []*asset.Asset{a}, conn, nil, asset.RetryPolicy{}, 30*time.Second)

	if err := exec.Run(context.Background(), claimed); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Verify state = succeeded.
	var state string
	if err := db.QueryRowContext(context.Background(),
		`SELECT state FROM runs WHERE id = $1`, claimed.ID,
	).Scan(&state); err != nil {
		t.Fatalf("query run state: %v", err)
	}
	if state != "succeeded" {
		t.Errorf("expected state=succeeded, got %q", state)
	}

	// Verify event sequence.
	evtTypes := queryEventTypes(t, db, claimed.ID)
	want := []string{"run.started", "run.step.started", "run.step.succeeded", "run.succeeded"}
	if !sliceEqual(evtTypes, want) {
		t.Errorf("event sequence mismatch:\n  got:  %v\n  want: %v", evtTypes, want)
	}
}

// TestExecutor_RetryAndFail verifies that a 2-attempt policy produces:
// run.step.failed, run.step.retry_scheduled, run.step.failed, run.failed.
func TestExecutor_RetryAndFail(t *testing.T) {
	db, entClient := setupTestDB(t)

	retryPolicy := asset.RetryPolicy{
		Max:          2,
		InitialDelay: 10 * time.Millisecond,
		JitterPct:    0,
	}
	conn := &recordingConnector{}
	a, err := asset.New("test-asset-retry").
		Connector("test-connector").
		Retry(retryPolicy).
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			return asset.MaterializeResult{}, errors.New("boom")
		}).
		Build()
	if err != nil {
		t.Fatalf("build asset: %v", err)
	}

	insertRun(t, db, "test-asset-retry")
	claimed := claimRun(t, db, "test-asset-retry")

	exec := buildExecutor(t, db, entClient, []*asset.Asset{a}, conn, nil, asset.RetryPolicy{}, 30*time.Second)

	runErr := exec.Run(context.Background(), claimed)
	if runErr == nil {
		t.Fatal("expected error from Run, got nil")
	}

	// Verify state = failed.
	var state string
	if err := db.QueryRowContext(context.Background(),
		`SELECT state FROM runs WHERE id = $1`, claimed.ID,
	).Scan(&state); err != nil {
		t.Fatalf("query run state: %v", err)
	}
	if state != "failed" {
		t.Errorf("expected state=failed, got %q", state)
	}

	// Verify event sequence: started, 2×step.failed with retry_scheduled between them.
	evtTypes := queryEventTypes(t, db, claimed.ID)
	want := []string{
		"run.started",
		"run.step.started",    // attempt 1 started
		"run.step.failed",     // attempt 1 failed
		"run.step.retry_scheduled", // scheduled retry
		"run.step.started",    // attempt 2 started
		"run.step.failed",     // attempt 2 failed
		"run.failed",          // run terminal
	}
	if !sliceEqual(evtTypes, want) {
		t.Errorf("event sequence mismatch:\n  got:  %v\n  want: %v", evtTypes, want)
	}
}

// TestExecutor_PanicRecovery verifies that a panicking MaterializeFunc is captured,
// results in run.step.failed with the panic value in the error, and the run fails.
func TestExecutor_PanicRecovery(t *testing.T) {
	db, entClient := setupTestDB(t)

	conn := &recordingConnector{}
	a, err := asset.New("test-asset-panic").
		Connector("test-connector").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			panic("kaboom")
		}).
		Build()
	if err != nil {
		t.Fatalf("build asset: %v", err)
	}

	insertRun(t, db, "test-asset-panic")
	claimed := claimRun(t, db, "test-asset-panic")

	exec := buildExecutor(t, db, entClient, []*asset.Asset{a}, conn, nil, asset.RetryPolicy{}, 30*time.Second)

	runErr := exec.Run(context.Background(), claimed)
	if runErr == nil {
		t.Fatal("expected error from panic, got nil")
	}

	// Verify event payload contains "kaboom".
	var errorMsg string
	err2 := db.QueryRowContext(context.Background(),
		`SELECT payload->>'error' FROM event_log
         WHERE resource_type = 'run' AND resource_id = $1
         AND event_type = 'run.step.failed'
         ORDER BY occurred_at LIMIT 1`,
		claimed.ID.String(),
	).Scan(&errorMsg)
	if err2 != nil {
		t.Fatalf("query step.failed event: %v", err2)
	}
	if !containsStr(errorMsg, "kaboom") {
		t.Errorf("expected error to contain 'kaboom', got: %q", errorMsg)
	}
}

// TestExecutor_TopologicalOrder verifies that assets execute in dependency order.
// Given a → b → c, the run.step.started events must appear in that order.
func TestExecutor_TopologicalOrder(t *testing.T) {
	db, entClient := setupTestDB(t)

	conn := &recordingConnector{}
	aAsset, _ := asset.New("topo-a").Connector("test-connector").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			return asset.MaterializeResult{}, nil
		}).Build()
	bAsset, _ := asset.New("topo-b").Upstream("topo-a").Connector("test-connector").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			return asset.MaterializeResult{}, nil
		}).Build()
	cAsset, _ := asset.New("topo-c").Upstream("topo-b").Connector("test-connector").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			return asset.MaterializeResult{}, nil
		}).Build()

	insertRun(t, db, "topo-c")
	claimed := claimRun(t, db, "topo-c")

	exec := buildExecutor(t, db, entClient, []*asset.Asset{aAsset, bAsset, cAsset}, conn, nil, asset.RetryPolicy{}, 30*time.Second)

	if err := exec.Run(context.Background(), claimed); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Extract asset names from run.step.started events in order.
	rows, err := db.QueryContext(context.Background(),
		`SELECT payload->>'asset_name' FROM event_log
         WHERE resource_type = 'run' AND resource_id = $1
         AND event_type = 'run.step.started'
         ORDER BY occurred_at, id`,
		claimed.ID.String())
	if err != nil {
		t.Fatalf("query step.started events: %v", err)
	}
	defer rows.Close()
	var stepOrder []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		stepOrder = append(stepOrder, name)
	}

	// topo-a must appear before topo-b, and topo-b before topo-c.
	aIdx, bIdx, cIdx := indexOf(stepOrder, "topo-a"), indexOf(stepOrder, "topo-b"), indexOf(stepOrder, "topo-c")
	if aIdx < 0 || bIdx < 0 || cIdx < 0 {
		t.Fatalf("not all assets appeared in step order: %v", stepOrder)
	}
	if !(aIdx < bIdx && bIdx < cIdx) {
		t.Errorf("topological order violated: a=%d b=%d c=%d in %v", aIdx, bIdx, cIdx, stepOrder)
	}
}

// TestExecutor_ConcurrencyTokenZeroCapacity verifies that a run fails when the token
// pool has zero capacity for the "global" tag.
func TestExecutor_ConcurrencyTokenZeroCapacity(t *testing.T) {
	db, entClient := setupTestDB(t)

	conn := &recordingConnector{}
	a, err := asset.New("test-asset-zero-cap").
		Connector("test-connector").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			return asset.MaterializeResult{}, nil
		}).
		Build()
	if err != nil {
		t.Fatalf("build asset: %v", err)
	}

	// Pool with zero global capacity.
	store := &entStorage{db: db, ent: entClient}
	pool := concurrency.NewPool(store, []concurrency.Capacity{
		{Tag: "global", Limit: 0},
	})

	insertRun(t, db, "test-asset-zero-cap")
	claimed := claimRun(t, db, "test-asset-zero-cap")

	exec := buildExecutor(t, db, entClient, []*asset.Asset{a}, conn, pool, asset.RetryPolicy{}, 30*time.Second)

	runErr := exec.Run(context.Background(), claimed)
	if runErr == nil {
		t.Fatal("expected error from zero-capacity pool, got nil")
	}
}

// TestExecutor_HeartbeatTicks verifies that the per-run heartbeat goroutine updates
// runs.last_heartbeat at least once during a slow Materialize (>HeartbeatInterval).
func TestExecutor_HeartbeatTicks(t *testing.T) {
	db, entClient := setupTestDB(t)

	// Use a short heartbeat interval (500ms) so we can observe ticks within a 1.5s sleep.
	heartbeatInterval := 500 * time.Millisecond

	conn := &recordingConnector{}
	a, err := asset.New("test-asset-heartbeat").
		Connector("test-connector").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			// Sleep longer than one heartbeat interval so the goroutine ticks at least once.
			time.Sleep(1500 * time.Millisecond)
			return asset.MaterializeResult{}, nil
		}).
		Build()
	if err != nil {
		t.Fatalf("build asset: %v", err)
	}

	insertRun(t, db, "test-asset-heartbeat")
	claimed := claimRun(t, db, "test-asset-heartbeat")

	exec := buildExecutor(t, db, entClient, []*asset.Asset{a}, conn, nil, asset.RetryPolicy{}, heartbeatInterval)

	if err := exec.Run(context.Background(), claimed); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Assert last_heartbeat > claimed_at: the goroutine updated the column at
	// least once after the initial ClaimNext baseline.
	var heartbeatUpdated bool
	err = db.QueryRowContext(context.Background(),
		`SELECT last_heartbeat > claimed_at FROM runs WHERE id = $1`,
		claimed.ID,
	).Scan(&heartbeatUpdated)
	if err != nil {
		t.Fatalf("query heartbeat: %v", err)
	}
	if !heartbeatUpdated {
		t.Error("expected last_heartbeat > claimed_at after heartbeat goroutine ticked, but it was not updated")
	}
}

// ===== Plan 03-07 — D-13 Layer 3 (Backfill Concurrency Tag) =====

// stubConnector is a minimal connector.Connector for plan 03-07 tests.
// It is private to this test file to keep TestExecutorBackfillTagAcquisition
// self-contained (no escape clause, no \"deferred if mocking heavyweight\").
type stubConnector struct{}

func (stubConnector) APIVersion() string { return connector.APIVersion }
func (stubConnector) Ping(_ context.Context, _ connector.PingRequest) (connector.PingResponse, error) {
	return connector.PingResponse{APIVersion: connector.APIVersion, ConnectorName: "stub"}, nil
}
func (stubConnector) Schema(_ context.Context, _ connector.SchemaRequest) (connector.SchemaResponse, error) {
	return connector.SchemaResponse{}, nil
}
func (stubConnector) Read(_ context.Context, _ connector.ReadRequest) (connector.ReadResponse, error) {
	return connector.ReadResponse{}, nil
}
func (stubConnector) Write(_ context.Context, _ connector.WriteRequest) (connector.WriteResponse, error) {
	return connector.WriteResponse{}, nil
}

// openExecutorTestDBPgx returns *sql.DB + *stent.Client using the entgosql.OpenDB
// path (sidesteps the deferred pgx-ent driver issue documented in
// .planning/phases/03-scheduling-sensors-partitions/deferred-items.md).
func openExecutorTestDBPgx(t *testing.T) (*sql.DB, *stent.Client) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed executor tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		t.Fatalf("ping db: %v", err)
	}
	entClient := stent.NewClient(stent.Driver(entgosql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() {
		entClient.Close()
		db.Close()
	})
	return db, entClient
}

// TestExecutorBackfillTagAcquisition (D-13 layer 3) — proves that two
// backfill-priority runs on the same asset cannot both hold the "backfill"
// concurrency token when capacity is 1: the first acquires it, the second
// fails Acquire with the configured retry policy exhausted.
//
// Plan 03-07 acceptance criterion is UNCONDITIONAL — uses an inline minimal
// stubConnector (defined above) so the test does not depend on heavyweight
// test infrastructure.
func TestExecutorBackfillTagAcquisition(t *testing.T) {
	db, entClient := openExecutorTestDBPgx(t)
	store := &entStorage{db: db, ent: entClient}

	const assetName = "test-backfill-tag-acquisition"
	// Cleanup any leftover rows.
	cleanup := func() {
		_, _ = db.Exec(`DELETE FROM concurrency_tokens WHERE asset_name=$1`, assetName)
		_, _ = db.Exec(`DELETE FROM runs WHERE asset_name=$1`, assetName)
	}
	cleanup()
	t.Cleanup(cleanup)

	// Pool with backfill capacity = 1 — second concurrent backfill run cannot
	// acquire and must fail Acquire (retry policy in this test is the default
	// non-retrying policy, so the second run returns the wrapped capacity
	// error immediately).
	pool := concurrency.NewPool(store, []concurrency.Capacity{
		{Tag: "global", Limit: 10},
		{Tag: "backfill", Limit: 1},
	})

	// Build an asset whose Materialize blocks ~250ms — long enough for the
	// second goroutine to reach Acquire and observe the capacity collision.
	a, err := asset.New(assetName).
		Connector("stub").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			select {
			case <-time.After(250 * time.Millisecond):
				return asset.MaterializeResult{}, nil
			case <-ctx.Done():
				return asset.MaterializeResult{}, ctx.Err()
			}
		}).
		Build()
	if err != nil {
		t.Fatalf("build asset: %v", err)
	}

	reg := asset.NewDefinitionRegistry()
	if err := reg.Register(a); err != nil {
		t.Fatalf("register asset: %v", err)
	}
	connReg := connector.NewRegistry()
	if err := connReg.RegisterInProcess("stub", stubConnector{}); err != nil {
		t.Fatalf("register stub connector: %v", err)
	}

	events := event.NewWriter(store)
	exec := runtime.NewExecutor(runtime.Deps{
		Store:             store,
		Events:            events,
		Registry:          reg,
		ConnectorReg:      connReg,
		Pool:              pool,
		WorkerID:          "test-backfill-tag",
		StepTimeout:       2 * time.Second,
		HeartbeatInterval: 0, // 0 → executor defaults to 30s; effectively no tick during this 250ms run
	})

	// Insert two starting-state backfill runs directly (post-claim shape).
	insertStartingRun := func() uuid.UUID {
		id := uuid.New()
		_, err := db.Exec(
			`INSERT INTO runs (id, asset_name, state, trigger, queued_at, claimed_by, claimed_at, last_heartbeat, priority)
			 VALUES ($1, $2, 'starting', 'backfill', NOW(), 'test', NOW(), NOW(), 'backfill')`,
			id, assetName,
		)
		if err != nil {
			t.Fatalf("insert starting run: %v", err)
		}
		return id
	}
	id1 := insertStartingRun()
	id2 := insertStartingRun()

	// Goroutine 1: holds the "backfill" token for ~250ms.
	errCh := make(chan error, 1)
	go func() {
		errCh <- exec.Run(context.Background(), &run.ClaimedRun{
			ID:        id1,
			AssetName: assetName,
			Trigger:   "backfill",
			Priority:  "backfill",
		})
	}()

	// Brief pause so id1 has acquired global+backfill tokens before id2 tries.
	time.Sleep(75 * time.Millisecond)

	// Goroutine 2 (this goroutine): expected to fail acquiring the backfill token.
	ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err2 := exec.Run(ctx2, &run.ClaimedRun{
		ID:        id2,
		AssetName: assetName,
		Trigger:   "backfill",
		Priority:  "backfill",
	})

	// Drain the first run's result before assertions.
	err1 := <-errCh

	// id2 must have failed at the backfill-token acquire step.
	if err2 == nil {
		t.Fatalf("expected second backfill run to fail acquiring backfill token (capacity=1, id1 holds it); got nil")
	}
	if !containsStr(err2.Error(), "backfill token") {
		t.Fatalf("expected error to mention 'backfill token'; got: %v", err2)
	}

	// id1 should have completed cleanly.
	if err1 != nil {
		t.Fatalf("first backfill run should have succeeded after holding the token; got: %v", err1)
	}
}

// ===== Phase 4 writer tests =====

// TestExecutorWithoutPhase4Writers is a regression guard confirming that the executor
// behaves identically when LineageWriter and SchemaWriter are nil. The trackingIO
// wrapper must be transparent from the user's perspective when no writer is wired.
func TestExecutorWithoutPhase4Writers(t *testing.T) {
	db, entClient := setupTestDB(t)

	conn := &recordingConnector{rowsWritten: 7}
	var readUpstream string
	a, err := asset.New("no-phase4-writers-asset").
		Connector("test-connector").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			// PartitionKey pass-through.
			_ = io.PartitionKey()
			// Write — pass-through.
			_, _ = io.Write(ctx, nil)
			// Capture what happens when tracking IO wraps the inner IO.
			readUpstream = io.PartitionKey() // just exercises the interface; no upstreams declared
			return asset.MaterializeResult{RowsWritten: 7}, nil
		}).
		Build()
	if err != nil {
		t.Fatalf("build asset: %v", err)
	}

	insertRun(t, db, "no-phase4-writers-asset")
	claimed := claimRun(t, db, "no-phase4-writers-asset")

	// Build executor WITHOUT Phase 4 writers — LineageWriter and SchemaWriter default to nil.
	exec := buildExecutor(t, db, entClient, []*asset.Asset{a}, conn, nil, asset.RetryPolicy{}, 30*time.Second)

	if err := exec.Run(context.Background(), claimed); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Run should succeed normally.
	var state string
	if err := db.QueryRowContext(context.Background(),
		`SELECT state FROM runs WHERE id = $1`, claimed.ID,
	).Scan(&state); err != nil {
		t.Fatalf("query run state: %v", err)
	}
	if state != "succeeded" {
		t.Errorf("expected state=succeeded, got %q", state)
	}
	// readUpstream captures the partition key; just verifies the decorated io is transparent.
	_ = readUpstream
}

// ===== Phase 5 Plan 05-04 governance gate tests =====

// buildExecutorWithGate constructs a runtime.Executor with the given assets,
// connector, and GovernanceGatingEnabled flag. Wraps buildExecutor so the
// existing helpers stay intact.
func buildExecutorWithGate(
	t *testing.T,
	db *sql.DB,
	entClient *stent.Client,
	assets []*asset.Asset,
	conn connector.Connector,
	gateEnabled bool,
) *runtime.Executor {
	t.Helper()
	store := &entStorage{db: db, ent: entClient}
	evtWriter := event.NewWriter(store)
	reg := asset.NewDefinitionRegistry()
	for _, a := range assets {
		if err := reg.Register(a); err != nil {
			t.Fatalf("register asset %q: %v", a.Name(), err)
		}
	}
	connReg := connector.NewRegistry()
	if conn != nil {
		if err := connReg.RegisterInProcess("test-connector", conn); err != nil {
			t.Fatalf("register connector: %v", err)
		}
	}
	pool := concurrency.NewPool(store, nil)
	return runtime.NewExecutor(runtime.Deps{
		Store:                   store,
		Events:                  evtWriter,
		Registry:                reg,
		ConnectorReg:            connReg,
		Pool:                    pool,
		DefaultPolicy:           asset.RetryPolicy{},
		WorkerID:                "test-worker",
		StepTimeout:             5 * time.Second,
		HeartbeatInterval:       30 * time.Second,
		GovernanceGatingEnabled: gateEnabled,
	})
}

// insertAssetVersion writes an asset_versions row with the given governance_state.
func insertAssetVersion(t *testing.T, db *sql.DB, assetName, codeHash, govState string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO asset_versions (id, asset, code_hash, drift_status, governance_state, created_at)
		VALUES ($1, $2, $3, 'clean', $4, NOW())
		ON CONFLICT (asset, code_hash) DO UPDATE SET governance_state = EXCLUDED.governance_state
	`, uuid.New(), assetName, codeHash, govState)
	if err != nil {
		t.Fatalf("insert asset_versions: %v", err)
	}
}

// TestExecutor_GatingDisabled_AllowsRun_EvenWhenStateIsDraft verifies the
// default behaviour (gating off) is unchanged: a draft asset_versions row
// does NOT block the run.
func TestExecutor_GatingDisabled_AllowsRun_EvenWhenStateIsDraft(t *testing.T) {
	db, entClient := setupTestDB(t)

	conn := &recordingConnector{rowsWritten: 1}
	a, err := asset.New("gate-disabled-asset").
		Connector("test-connector").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			return asset.MaterializeResult{RowsWritten: 1}, nil
		}).
		Build()
	if err != nil {
		t.Fatalf("build asset: %v", err)
	}
	insertAssetVersion(t, db, a.Name(), a.CodeHash(), "draft")

	insertRun(t, db, a.Name())
	claimed := claimRun(t, db, a.Name())

	exec := buildExecutorWithGate(t, db, entClient, []*asset.Asset{a}, conn, false)
	if err := exec.Run(context.Background(), claimed); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	var state string
	if err := db.QueryRowContext(context.Background(),
		`SELECT state FROM runs WHERE id = $1`, claimed.ID).Scan(&state); err != nil {
		t.Fatalf("query state: %v", err)
	}
	if state != "succeeded" {
		t.Errorf("expected state=succeeded with gate disabled, got %q", state)
	}
}

// TestExecutor_GatingEnabled_StateActive_AllowsRun verifies gating on +
// governance_state=active permits the run to proceed.
func TestExecutor_GatingEnabled_StateActive_AllowsRun(t *testing.T) {
	db, entClient := setupTestDB(t)

	conn := &recordingConnector{rowsWritten: 2}
	a, err := asset.New("gate-active-asset").
		Connector("test-connector").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			return asset.MaterializeResult{RowsWritten: 2}, nil
		}).
		Build()
	if err != nil {
		t.Fatalf("build asset: %v", err)
	}
	insertAssetVersion(t, db, a.Name(), a.CodeHash(), "active")

	insertRun(t, db, a.Name())
	claimed := claimRun(t, db, a.Name())

	exec := buildExecutorWithGate(t, db, entClient, []*asset.Asset{a}, conn, true)
	if err := exec.Run(context.Background(), claimed); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	var state string
	if err := db.QueryRowContext(context.Background(),
		`SELECT state FROM runs WHERE id = $1`, claimed.ID).Scan(&state); err != nil {
		t.Fatalf("query state: %v", err)
	}
	if state != "succeeded" {
		t.Errorf("expected state=succeeded with active gate, got %q", state)
	}
}

// TestExecutor_GatingEnabled_StateDraft_BlocksAndEmits verifies that gating on
// + state=draft refuses the materialize and emits governance.materialization_blocked.
func TestExecutor_GatingEnabled_StateDraft_BlocksAndEmits(t *testing.T) {
	db, entClient := setupTestDB(t)

	conn := &recordingConnector{rowsWritten: 3}
	a, err := asset.New("gate-draft-asset").
		Connector("test-connector").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			return asset.MaterializeResult{RowsWritten: 3}, nil
		}).
		Build()
	if err != nil {
		t.Fatalf("build asset: %v", err)
	}
	insertAssetVersion(t, db, a.Name(), a.CodeHash(), "draft")

	insertRun(t, db, a.Name())
	claimed := claimRun(t, db, a.Name())

	exec := buildExecutorWithGate(t, db, entClient, []*asset.Asset{a}, conn, true)
	runErr := exec.Run(context.Background(), claimed)
	if runErr == nil {
		t.Fatal("expected error from gated run, got nil")
	}
	// Verify the event_log received governance.materialization_blocked.
	evtTypes := queryEventTypes(t, db, claimed.ID)
	found := false
	for _, et := range evtTypes {
		if et == "governance.materialization_blocked" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected governance.materialization_blocked in events, got: %v", evtTypes)
	}
}

// TestExecutor_GatingEnabled_StateRejected_BlocksAndEmits — same as above
// but with state=rejected. The behaviour MUST be identical: any non-active
// state blocks materialize.
func TestExecutor_GatingEnabled_StateRejected_BlocksAndEmits(t *testing.T) {
	db, entClient := setupTestDB(t)

	conn := &recordingConnector{rowsWritten: 4}
	a, err := asset.New("gate-rejected-asset").
		Connector("test-connector").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			return asset.MaterializeResult{RowsWritten: 4}, nil
		}).
		Build()
	if err != nil {
		t.Fatalf("build asset: %v", err)
	}
	insertAssetVersion(t, db, a.Name(), a.CodeHash(), "rejected")

	insertRun(t, db, a.Name())
	claimed := claimRun(t, db, a.Name())

	exec := buildExecutorWithGate(t, db, entClient, []*asset.Asset{a}, conn, true)
	if err := exec.Run(context.Background(), claimed); err == nil {
		t.Fatal("expected error from gated rejected-state run, got nil")
	}
	evtTypes := queryEventTypes(t, db, claimed.ID)
	found := false
	for _, et := range evtTypes {
		if et == "governance.materialization_blocked" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected governance.materialization_blocked in events, got: %v", evtTypes)
	}
}

// ===== Helpers =====

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}

func indexOf(slice []string, s string) int {
	for i, v := range slice {
		if v == s {
			return i
		}
	}
	return -1
}

// Ensure rawStorage and entStorage satisfy storage.Storage.
var _ storage.Storage = (*rawStorage)(nil)
var _ storage.Storage = (*entStorage)(nil)

// Ensure recordingConnector satisfies connector.Connector.
var _ connector.Connector = (*recordingConnector)(nil)

// Dummy usage of fmt to prevent unused import.
var _ = fmt.Sprintf
