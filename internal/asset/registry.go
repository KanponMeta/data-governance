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

	// OnRegister is an optional hook (Phase 4 D-01) invoked after an asset
	// passes the duplicate check and is committed to the in-memory registry.
	// The primary use case is wiring lineage.Writer.SyncStaticEdges so every
	// registered asset has its static edges synced to asset_edges at startup.
	//
	// Hook contract:
	//   - Called with the registry's write lock RELEASED (to avoid deadlock if
	//     the hook itself calls Registry.Get).
	//   - Failure returns the error to the caller of Register but does NOT undo
	//     the in-memory registration (the hook is for DB sync; in-memory must
	//     succeed so the executor can see the asset).
	//   - nil means no-op — existing test code that constructs DefinitionRegistry
	//     without this field is unaffected.
	OnRegister func(*Asset) error
}

// NewDefinitionRegistry returns a new, empty DefinitionRegistry.
func NewDefinitionRegistry() *DefinitionRegistry {
	return &DefinitionRegistry{assets: make(map[string]*Asset)}
}

// Register adds an asset to the registry. Returns ErrAlreadyRegistered if
// an asset with the same name has already been registered (no silent overwrite,
// per T-02-01-01 threat mitigation).
//
// If OnRegister is set, it is called after the in-memory registration succeeds.
// A hook failure returns the error to the caller but does NOT undo the in-memory
// registration (callers such as production startup choose to abort; tests leave
// OnRegister nil to skip DB writes entirely).
func (r *DefinitionRegistry) Register(a *Asset) error {
	if a == nil || a.name == "" {
		return fmt.Errorf("asset: register requires non-empty Name")
	}
	r.mu.Lock()
	if _, exists := r.assets[a.name]; exists {
		r.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrAlreadyRegistered, a.name)
	}
	r.assets[a.name] = a
	hook := r.OnRegister // capture under lock; avoid holding lock during hook call
	r.mu.Unlock()

	if hook != nil {
		return hook(a)
	}
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

// ResetForTest is an exported variant of resetForTest for use by integration
// tests in other packages (e.g., test/integration). Call via t.Cleanup so each
// test starts with a clean registry.
// WARNING: NOT safe for concurrent test use — use only from TestMain or serially.
func ResetForTest() { defaultRegistry = NewDefinitionRegistry() }
