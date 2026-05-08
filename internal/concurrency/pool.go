// Package concurrency implements the single global concurrency token pool (D-16).
// ALL concurrency limits — run-level, op-level, and resource-level — are tracked in
// the concurrency_tokens table. Three-layer hierarchical pools are REJECTED (PITFALLS §2).
package concurrency

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/storage"
)

// ErrCapacity is returned by Acquire when the configured limit for a resource tag
// would be exceeded by the requested weight.
var ErrCapacity = errors.New("concurrency: capacity exhausted for resource tag")

// Capacity describes the configured limit for one resource tag.
// Source of truth: startup config (plan 02-04 wires it).
type Capacity struct {
	Tag   string
	Limit int
}

// Pool guards execution slots using the concurrency_tokens table (D-16).
// It is the SINGLE source of truth for run-level / op-level / resource-level limits.
// Construct with NewPool.
type Pool struct {
	store      storage.Storage
	capacities map[string]int // tag -> limit
}

// NewPool constructs a pool with the given capacities. The "global" tag is the
// default run-level cap; assets that declare Resource(name, weight) check out of
// tag=name in addition to the global tag.
func NewPool(store storage.Storage, capacities []Capacity) *Pool {
	m := make(map[string]int, len(capacities))
	for _, c := range capacities {
		m[c.Tag] = c.Limit
	}
	return &Pool{store: store, capacities: m}
}

// Acquire atomically attempts to consume `weight` units of capacity for `tag`
// on behalf of (runID, assetName). Returns ErrCapacity if not enough headroom.
//
// Implementation: BEGIN; SELECT SUM(weight) FROM concurrency_tokens WHERE resource_tag=$1
// FOR UPDATE; if used + weight <= limit, INSERT; COMMIT.
//
// The FOR UPDATE lock ensures no two concurrent Acquire calls race on the same tag.
func (p *Pool) Acquire(ctx context.Context, runID uuid.UUID, assetName, tag string, weight int) error {
	if weight <= 0 {
		weight = 1
	}
	limit, ok := p.capacities[tag]
	if !ok {
		// No configured limit for this tag = unlimited. Still record the token row so
		// ReleaseStale and observability work.
		limit = 1<<31 - 1
	}
	db := p.store.DB()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("concurrency: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Lock advisory to serialize concurrent Acquire calls for the same resource_tag.
	// We use an advisory lock keyed on a hash of the tag name to avoid DDL locks.
	// The SUM aggregate cannot use FOR UPDATE directly (PostgreSQL limitation), so
	// we use a separate lock acquisition followed by the aggregate read.
	const lockSQL = `SELECT pg_advisory_xact_lock(hashtext($1))`
	if _, err := tx.ExecContext(ctx, lockSQL, tag); err != nil {
		return fmt.Errorf("concurrency: advisory lock: %w", err)
	}

	const sumSQL = `
		SELECT COALESCE(SUM(weight), 0)::int
		  FROM concurrency_tokens
		 WHERE resource_tag = $1
	`
	var used int
	if err := tx.QueryRowContext(ctx, sumSQL, tag).Scan(&used); err != nil {
		return fmt.Errorf("concurrency: read used: %w", err)
	}
	if used+weight > limit {
		return fmt.Errorf("%w: tag=%q used=%d weight=%d limit=%d",
			ErrCapacity, tag, used, weight, limit)
	}

	const insertSQL = `
		INSERT INTO concurrency_tokens (id, run_id, asset_name, resource_tag, weight, acquired_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	_, err = tx.ExecContext(ctx, insertSQL,
		uuid.New(), runID, assetName, tag, weight, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("concurrency: insert token: %w", err)
	}
	return tx.Commit()
}

// Release drops every token held by (runID, tag). Idempotent — safe to call even
// if no token exists for the given (runID, tag) pair.
func (p *Pool) Release(ctx context.Context, runID uuid.UUID, tag string) error {
	const sqlText = `DELETE FROM concurrency_tokens WHERE run_id = $1 AND resource_tag = $2`
	_, err := p.store.DB().ExecContext(ctx, sqlText, runID, tag)
	return err
}

// ReleaseAll drops every token held by runID across all tags. Called when the run
// reaches a terminal state (succeeded, failed, canceled).
func (p *Pool) ReleaseAll(ctx context.Context, runID uuid.UUID) error {
	const sqlText = `DELETE FROM concurrency_tokens WHERE run_id = $1`
	_, err := p.store.DB().ExecContext(ctx, sqlText, runID)
	return err
}

// ReleaseStale drops tokens whose acquired_at is older than `staleAfter`. Called once
// at worker startup to clean up tokens left by crashed workers (plan 02-04 invokes this).
// Returns the number of deleted rows.
func (p *Pool) ReleaseStale(ctx context.Context, staleAfter time.Duration) (int64, error) {
	const sqlText = `DELETE FROM concurrency_tokens WHERE acquired_at < $1`
	cutoff := time.Now().UTC().Add(-staleAfter)
	res, err := p.store.DB().ExecContext(ctx, sqlText, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
