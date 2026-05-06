package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/kanpon/data-governance/internal/storage/ent"
)

// ErrNotFound is returned by lookups that find no row.
var ErrNotFound = errors.New("storage: not found")

// Storage is the persistence boundary for all platform components.
// Tests use an in-memory implementation; production uses Postgres.
type Storage interface {
	// Ping verifies the underlying database is reachable.
	Ping(ctx context.Context) error
	// Ent returns the underlying ent client (read paths use this directly).
	Ent() *ent.Client
	// DB returns the underlying *sql.DB for raw query access (testing only).
	DB() *sql.DB
	// WithTx runs fn inside a single transaction; commits on nil, rolls back on error.
	WithTx(ctx context.Context, fn func(tx *ent.Tx) error) error
	// Close releases all connections.
	Close() error
}
