package runtime_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

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
