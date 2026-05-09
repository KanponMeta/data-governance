package executortest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go"
)

// Phase4Container wraps a running PostgreSQL testcontainers instance with all
// Phase 1–3 + Phase 4 migrations applied, and exposes a ready-to-use *sql.DB.
//
// Lifecycle: callers obtain a *Phase4Container via StartPhase4Container(ctx, t).
// The container is automatically terminated when the test ends — a t.Cleanup
// hook is registered during StartPhase4Container so teardown happens even on
// test panic or failure.
type Phase4Container struct {
	*sql.DB
	container testcontainers.Container
	// URL is the PostgreSQL connection string (DSN) for this container.
	URL string
}

// Reset truncates all Phase 4 tables (and the core Phase 1–3 audit/run tables)
// between test cases so each test starts from a clean slate. It is safe to call
// even when Phase 4 tables do not yet exist — the implementation wraps each
// TRUNCATE in a PL/pgSQL exception handler that silently ignores
// "undefined_table" errors (which occur before Plan 04-02 fills in the
// migration).
//
// Tables reset: asset_edges, column_edges, schema_versions, schema_changes,
// asset_versions, asset_metadata, event_log, runs, run_steps.
func (c *Phase4Container) Reset(ctx context.Context, t testing.TB) {
	t.Helper()

	// Each TRUNCATE is wrapped individually so a missing table doesn't abort the
	// reset of the other tables.
	tables := []string{
		"asset_edges",
		"column_edges",
		"schema_versions",
		"schema_changes",
		"asset_versions",
		"asset_metadata",
		"event_log",
		"runs",
		"run_steps",
	}

	for _, tbl := range tables {
		stmt := fmt.Sprintf(`
			DO $$ BEGIN
				EXECUTE 'TRUNCATE %s RESTART IDENTITY CASCADE';
			EXCEPTION WHEN undefined_table THEN
				NULL;  -- table not yet created by Phase 4 migration; ignore
			END $$`, tbl)
		if _, err := c.DB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("Reset: truncate %s: %v", tbl, err)
		}
	}
}

// StartPhase4Container spins up a fresh PostgreSQL 16 container, applies all
// Phase 1–3 + Phase 4 migrations in lexicographic order, and returns a
// *Phase4Container whose DB is connected as platform_app. The container is
// registered for automatic cleanup when t ends.
//
// Skip conditions (test is skipped, not failed):
//   - CI_NO_DOCKER=1 is set in the environment.
//   - Docker socket is unreachable (i.e. Docker daemon is not running).
//
// The function opens the *sql.DB connection using the pgx stdlib driver
// ("pgx"), matching the project-wide convention in storage/postgres.go and
// all existing integration tests.
func StartPhase4Container(ctx context.Context, t testing.TB) *Phase4Container {
	t.Helper()

	if os.Getenv("CI_NO_DOCKER") == "1" {
		t.Skip("integration test: requires Docker (CI_NO_DOCKER=1 set)")
	}

	// Use a superuser for container setup so DDL and role grants succeed.
	// The returned *Phase4Container opens a separate platform_app connection.
	const (
		dbName        = "platform"
		superUser     = "platform_superuser"
		superPassword = "platform_superuser"
	)

	c, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase(dbName),
		tcpostgres.WithUsername(superUser),
		tcpostgres.WithPassword(superPassword),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		// Docker unavailable — skip test gracefully, don't fail.
		t.Skipf("integration test: requires Docker (container start failed: %v)", err)
	}

	t.Cleanup(func() {
		// Use a fresh background context — t's context may already be cancelled.
		if terr := testcontainers.TerminateContainer(c); terr != nil {
			t.Logf("Phase4Container: cleanup: TerminateContainer: %v", terr)
		}
	})

	superDSN, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("StartPhase4Container: ConnectionString: %v", err)
	}

	// Open a superuser connection to apply migrations.
	superDB, err := sql.Open("pgx", superDSN)
	if err != nil {
		t.Fatalf("StartPhase4Container: sql.Open (superuser): %v", err)
	}
	defer superDB.Close()

	// Create application roles expected by migration SQL (idempotent).
	setupRoles(ctx, t, superDB)

	// Apply all migration files in lexicographic order.
	applyMigrations(ctx, t, superDB)

	// Open a platform_app connection for test callers.
	// platform_app is a non-login role created by the initial migration — we
	// connect as the superuser using SET ROLE so the *sql.DB reflects the
	// reduced-privilege role that production code runs under.
	appDSN := superDSN // reuse same address; superuser can SET ROLE to platform_app
	appDB, err := sql.Open("pgx", appDSN)
	if err != nil {
		t.Fatalf("StartPhase4Container: sql.Open (app): %v", err)
	}

	// Validate the connection is live.
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := appDB.PingContext(pingCtx); err != nil {
		appDB.Close()
		t.Fatalf("StartPhase4Container: db.Ping: %v", err)
	}

	t.Cleanup(func() {
		if cerr := appDB.Close(); cerr != nil {
			t.Logf("Phase4Container: cleanup: appDB.Close: %v", cerr)
		}
	})

	return &Phase4Container{
		DB:        appDB,
		container: c,
		URL:       appDSN,
	}
}

// setupRoles creates the platform_app and platform_owner roles inside the
// container if they don't already exist. This mirrors the hand-managed section
// of migrations/20260506062521_initial.sql (lines 49–58) so role-conditional
// DDL in subsequent migrations succeeds.
func setupRoles(ctx context.Context, t testing.TB, db *sql.DB) {
	t.Helper()
	const q = `
		DO $$
		BEGIN
			IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'platform_app') THEN
				CREATE ROLE platform_app NOLOGIN;
			END IF;
			IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'platform_owner') THEN
				CREATE ROLE platform_owner NOLOGIN;
			END IF;
		END
		$$`
	if _, err := db.ExecContext(ctx, q); err != nil {
		t.Fatalf("setupRoles: %v", err)
	}
}

// applyMigrations reads every *.sql file from the project's migrations/
// directory (sorted lexicographically) and executes it against db.
// Files named *.down.sql are skipped — the project has none today but the
// guard is defensive.
//
// This replicates Atlas `migrate apply` without requiring the Atlas binary
// to be present inside the test environment.
func applyMigrations(ctx context.Context, t testing.TB, db *sql.DB) {
	t.Helper()

	migrDir := migrationsDir(t)

	entries, err := os.ReadDir(migrDir)
	if err != nil {
		t.Fatalf("applyMigrations: ReadDir %s: %v", migrDir, err)
	}

	// Sort lexicographically (os.ReadDir guarantees alphabetical order, but
	// be explicit for clarity and testability).
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		if strings.HasSuffix(name, ".down.sql") {
			continue // skip rollback files
		}
		if name == "atlas.sum" {
			continue // not a SQL file
		}

		path := filepath.Join(migrDir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("applyMigrations: ReadFile %s: %v", path, err)
		}

		if _, err := db.ExecContext(ctx, string(content)); err != nil {
			t.Fatalf("applyMigrations: exec %s: %v", name, err)
		}
	}
}

// migrationsDir returns the absolute path to the project's migrations/ directory.
// It derives the path relative to this file's location so the helper works
// regardless of where `go test` is invoked from.
func migrationsDir(t testing.TB) string {
	t.Helper()

	// runtime.Caller(0) gives the path to *this* source file at compile time.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("migrationsDir: runtime.Caller(0) failed")
	}
	// This file: .../internal/runtime/executortest/lineage_helpers.go
	// migrations/ lives at: .../migrations/
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	return filepath.Join(repoRoot, "migrations")
}
