package asset

import (
	"errors"
	"fmt"
)

var (
	// ErrMissingMaterialize is returned by Build/Register when Materialize(fn)
	// was not called on the builder before committing the asset definition.
	ErrMissingMaterialize = errors.New("asset: Materialize(fn) is required before Register/Build")

	// ErrMissingConnector is returned by Build/Register when Connector(name)
	// was not called on the builder before committing the asset definition.
	ErrMissingConnector = errors.New("asset: Connector(name) is required before Register/Build")

	// ErrEmptyName is returned by Build/Register when New("") was called with
	// an empty name string.
	ErrEmptyName = errors.New("asset: New(name) requires non-empty name")
)

// Builder accumulates configuration before Register() commits to the global
// registry. Construct only via New(name). All methods return *Builder for
// chaining (D-01). Method order is irrelevant — only Build()/Register() validate.
type Builder struct {
	a *Asset
}

// New starts a new Asset definition. Name must be non-empty and unique within
// the registry when Register() is eventually called.
func New(name string) *Builder {
	return &Builder{a: &Asset{name: name}}
}

// Upstream appends one or more upstream asset names (variadic per D-01).
// The DAG executor (plan 02-02) reads Asset.Upstreams() to build the
// dependency graph. Calling Upstream multiple times is cumulative.
func (b *Builder) Upstream(names ...string) *Builder {
	b.a.upstreams = append(b.a.upstreams, names...)
	return b
}

// Connector binds the asset to a connector by name (D-03). Resolution happens
// at materialize time via connector.Registry.Get(name). The name must match
// a connector registered in the startup config.
func (b *Builder) Connector(name string) *Builder {
	b.a.connectorName = name
	return b
}

// Materialize registers the user transformation function (D-04 signature).
// The function receives an AssetIO that hides connector calls from user code.
func (b *Builder) Materialize(fn MaterializeFunc) *Builder {
	b.a.materializeFn = fn
	return b
}

// Retry overrides the platform default retry policy for this asset (D-15).
// When the policy IsZero() == true the engine applies the platform-level default.
func (b *Builder) Retry(p RetryPolicy) *Builder {
	b.a.retryPolicy = p
	return b
}

// Resource attaches a named resource constraint (D-16). Weight defaults to 1
// if zero or negative — plan 02-03 checks out this many tokens per acquisition.
func (b *Builder) Resource(name string, weight int) *Builder {
	if weight <= 0 {
		weight = 1
	}
	b.a.resources = append(b.a.resources, Resource{Name: name, Weight: weight})
	return b
}

// Build validates the accumulated configuration and returns the *Asset WITHOUT
// committing it to the process-global Default() registry.
//
// This is the test-friendly construction path: plan 02-02 DAG tests build
// assets via Build() so they can construct in-test dependency graphs without
// polluting the global singleton across test cases. Production code paths use
// Register() instead.
//
// Returns (nil, error) when:
//   - name is empty (ErrEmptyName)
//   - Materialize was not called (ErrMissingMaterialize)
//   - Connector was not called (ErrMissingConnector)
func (b *Builder) Build() (*Asset, error) {
	if b.a.name == "" {
		return nil, fmt.Errorf("%w", ErrEmptyName)
	}
	if b.a.materializeFn == nil {
		return nil, fmt.Errorf("%w (asset %q)", ErrMissingMaterialize, b.a.name)
	}
	if b.a.connectorName == "" {
		return nil, fmt.Errorf("%w (asset %q)", ErrMissingConnector, b.a.name)
	}
	return b.a, nil
}

// Register validates and commits to the process-global Default() registry.
// It delegates validation to Build() so the contract is identical: any chain
// that Build() accepts, Register() accepts, and vice versa.
//
// Returns ErrAlreadyRegistered if an asset with the same name was already
// committed (no silent overwrite, per T-02-01-01 threat mitigation).
func (b *Builder) Register() error {
	a, err := b.Build()
	if err != nil {
		return err
	}
	return Default().Register(a)
}
