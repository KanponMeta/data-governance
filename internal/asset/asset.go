// Package asset provides the user-facing SDK for defining, registering, and
// executing data assets in the platform.
//
// The primary entry point is asset.New(name), which returns a Builder that
// accumulates asset configuration via chained method calls. Calling Register()
// commits the definition to the process-global DefinitionRegistry; Build() is
// the test-friendly alternative that returns the *Asset without registration.
//
// Stability commitment: The asset package is an external-facing SDK surface
// starting in Phase 2. Builder method names, AssetIO interface, and all exported
// types are treated as public API from this point onward.
package asset

import "context"

// MaterializeFunc is the user-supplied transformation. AssetIO wraps connector
// calls (D-04), so user code does not import internal/connector directly.
type MaterializeFunc func(ctx context.Context, io AssetIO) (MaterializeResult, error)

// MaterializeResult is the return value from a Materialize call.
// RowsWritten is business-meaningful row count; Metadata is the Phase 4
// lineage hook (D-04) — values are free-form in Phase 2.
type MaterializeResult struct {
	RowsWritten int64
	Metadata    map[string]any
}

// Resource attaches a named resource constraint with weight (D-16). Plan 02-03
// reads these when checking out tokens from the global concurrency_tokens table.
type Resource struct {
	Name   string // e.g. "postgres-prod"
	Weight int    // default 1; tokens consumed per acquisition
}

// Asset is the immutable runtime representation of a user-defined asset.
// Construct only via asset.New(...).Build() or asset.New(...).Register().
// Builder writes; Asset reads — all fields are private, accessed via methods.
type Asset struct {
	name          string
	upstreams     []string
	connectorName string
	materializeFn MaterializeFunc
	retryPolicy   RetryPolicy
	resources     []Resource
}

// Name returns the unique asset identifier.
func (a *Asset) Name() string { return a.name }

// Upstreams returns a defensive copy of the upstream asset name list.
// The DAG executor (plan 02-02) reads this to build the dependency graph.
func (a *Asset) Upstreams() []string { return append([]string(nil), a.upstreams...) }

// ConnectorName returns the connector name bound to this asset (D-03).
// Resolution happens at materialize-time via connector.Registry.Get(name).
func (a *Asset) ConnectorName() string { return a.connectorName }

// MaterializeFn returns the user-supplied transformation function (D-04).
func (a *Asset) MaterializeFn() MaterializeFunc { return a.materializeFn }

// RetryPolicy returns the per-asset retry configuration (D-15).
// IsZero() == true means the platform-level default applies.
func (a *Asset) RetryPolicy() RetryPolicy { return a.retryPolicy }

// Resources returns a defensive copy of the resource constraint list (D-16).
// Plan 02-03 uses these to acquire tokens from the concurrency token pool.
func (a *Asset) Resources() []Resource { return append([]Resource(nil), a.resources...) }
