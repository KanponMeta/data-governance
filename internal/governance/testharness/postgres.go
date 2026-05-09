// Package testharness provides test infrastructure for Phase 5 governance tests:
// Postgres testcontainers with audit schema, Casbin fixtures, warehouse mocks, and
// webhook receiver for integration testing across all Phase 5 plans.
package testharness

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const postgresImage = "postgres:16-alpine"

// NewTestPostgres starts a Postgres 16 testcontainer, applies all migrations
// from the migrations/ directory in lexical order, and returns a *sql.DB
// connected as the platform_app role with SET ROLE already executed.
// The caller must call the returned cleanup function when the test completes.
func NewTestPostgres(t *testing.T) (*sql.DB, func()) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	container, err := postgres.Run(ctx,
		postgresImage,
		postgres.WithDatabase("platform_test"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
	)
	if err != nil {
		t.Fatalf("failed to start postgres container: %v", err)
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("failed to get connection string: %v", err)
	}

	// Wait for postgres to be ready.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("failed to open pgx pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		_ = container.Terminate(ctx)
		t.Fatalf("postgres not ready: %v", err)
	}
	pool.Close()

	// Apply migrations in lexical order.
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("failed to open db: %v", err)
	}
	if err := ApplyMigrations(db, "migrations/"); err != nil {
		db.Close()
		_ = container.Terminate(ctx)
		t.Fatalf("failed to apply migrations: %v", err)
	}

	// Reconnect as platform_app with single connection.
	appDSN := dsn + "&pool=0"
	appDB, err := sql.Open("pgx", appDSN)
	if err != nil {
		db.Close()
		_ = container.Terminate(ctx)
		t.Fatalf("failed to open app db: %v", err)
	}

	// Set role to platform_app.
	if _, err := appDB.ExecContext(ctx, "SET ROLE platform_app"); err != nil {
		appDB.Close()
		db.Close()
		_ = container.Terminate(ctx)
		t.Fatalf("failed to SET ROLE platform_app: %v", err)
	}

	cleanup := func() {
		_ = appDB.Close()
		_ = db.Close()
		_ = container.Terminate(context.Background())
	}

	return appDB, cleanup
}

// ApplyMigrations runs all .sql files under migrationsDir in lexical order.
// It expects the DB user to have privilege to create the audit schema and tables.
func ApplyMigrations(db *sql.DB, migrationsDir string) error {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var fullPaths []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			fullPaths = append(fullPaths, filepath.Join(migrationsDir, e.Name()))
		}
	}
	sort.Strings(fullPaths)

	for _, path := range fullPaths {
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", path, err)
		}
		if _, err := db.Exec(string(content)); err != nil {
			return fmt.Errorf("exec migration %s: %w", path, err)
		}
	}
	return nil
}

// NewTestPool returns a *pgxpool.Pool connected to the test postgres instance.
// The caller is responsible for closing the pool.
func NewTestPool(ctx context.Context, db *sql.DB) (*pgxpool.Pool, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		// Best-effort default when running inside the container.
		dsn = "postgres://postgres:postgres@localhost:5432/platform_test?sslmode=disable"
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open pgx pool: %w", err)
	}
	return pool, nil
}
