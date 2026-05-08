package asset

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

var (
	// ErrAlreadyRegistered is returned by Register when an asset with the
	// same name has already been registered in the registry.
	ErrAlreadyRegistered = errors.New("asset: already registered")

	// ErrNotFound is returned by Get when no asset with the given name is registered.
	ErrNotFound = errors.New("asset: not found")
)

// DefinitionRegistry is the process-global asset registry (D-05).
// Builder.Register() calls Default().Register(asset); the worker / materialize
// subcommands enumerate via Default().List().
//
// All methods are safe for concurrent use.
type DefinitionRegistry struct {
	mu     sync.RWMutex
	assets map[string]*Asset
}

// NewDefinitionRegistry returns a new, empty DefinitionRegistry.
func NewDefinitionRegistry() *DefinitionRegistry {
	return &DefinitionRegistry{assets: make(map[string]*Asset)}
}

// Register adds an asset to the registry. Returns ErrAlreadyRegistered if
// an asset with the same name has already been registered (no silent overwrite,
// per T-02-01-01 threat mitigation).
func (r *DefinitionRegistry) Register(a *Asset) error {
	if a == nil || a.name == "" {
		return fmt.Errorf("asset: register requires non-empty Name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.assets[a.name]; exists {
		return fmt.Errorf("%w: %q", ErrAlreadyRegistered, a.name)
	}
	r.assets[a.name] = a
	return nil
}

// Get returns the registered asset with the given name, or ErrNotFound.
func (r *DefinitionRegistry) Get(name string) (*Asset, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.assets[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	return a, nil
}

// List returns the names of all registered assets, sorted alphabetically.
func (r *DefinitionRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.assets))
	for n := range r.assets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// defaultRegistry is the process-global DefinitionRegistry (D-05).
// Asset definitions written with asset.New(...).Register() land here.
var defaultRegistry = NewDefinitionRegistry()

// Default returns the process-global registry that asset.New(...).Register() writes to.
func Default() *DefinitionRegistry { return defaultRegistry }

// resetForTest replaces the default registry with a fresh empty instance.
// This is a test-only helper — it is not exported but is accessible from
// tests in the same package (package asset) and via t.Cleanup.
func resetForTest() { defaultRegistry = NewDefinitionRegistry() }
