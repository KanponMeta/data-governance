package asset

import (
	"context"
	"sort"
	"sync"

	"github.com/kanpon/data-governance/internal/connector"
)

// TrackingIO wraps an AssetIO and records every Read(upstream) call so the
// executor can compare observed-vs-declared upstream sets per D-04.
//
// The user's MaterializeFunc sees a normal AssetIO (TrackingIO satisfies the
// interface). After Materialize returns, the executor calls Observed() to
// retrieve the unique upstream names actually consumed this run. The lineage
// writer compares the result with asset.Upstreams() to decide whether to
// emit lineage.drift_detected and set asset_versions.drift_status='pending'.
//
// No user opt-in: every executor-supplied AssetIO is wrapped (Phase 4
// executor.runStep change in plan 04-04 task 3).
type TrackingIO interface {
	AssetIO
	// Observed returns the unique upstream names that Read() was called with,
	// sorted alphabetically for deterministic comparison. Returns a non-nil
	// empty slice if Read was never called.
	Observed() []string
}

// NewTrackingIO wraps inner with read-tracking. inner MUST be non-nil.
func NewTrackingIO(inner AssetIO) TrackingIO {
	return &trackingIO{inner: inner, seen: map[string]struct{}{}}
}

type trackingIO struct {
	inner AssetIO
	mu    sync.Mutex
	seen  map[string]struct{}
}

// Read records the upstream name unconditionally (drift cares about intent, not
// success) then delegates to inner. The recording happens even when inner returns
// an error (e.g., ErrUnknownUpstream) — the user code tried to consume this
// upstream so it is relevant to drift detection.
func (t *trackingIO) Read(ctx context.Context, upstream string) ([]connector.Row, error) {
	t.mu.Lock()
	t.seen[upstream] = struct{}{}
	t.mu.Unlock()
	return t.inner.Read(ctx, upstream)
}

// Write is a pure pass-through to the inner AssetIO.
func (t *trackingIO) Write(ctx context.Context, rows []connector.Row) (int64, error) {
	return t.inner.Write(ctx, rows)
}

// PartitionKey is a pure pass-through to the inner AssetIO.
func (t *trackingIO) PartitionKey() string { return t.inner.PartitionKey() }

// Observed returns the unique upstream names that Read() was called with,
// sorted alphabetically for deterministic comparison. Calling Observed()
// multiple times returns the same set without resetting it.
func (t *trackingIO) Observed() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.seen))
	for k := range t.seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
