package connector

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Sentinel errors for registry operations.
var (
	// ErrIncompatibleVersion is returned by Register when the connector's
	// APIVersion() does not match the platform's connector.APIVersion constant.
	ErrIncompatibleVersion = errors.New("connector: incompatible API version")

	// ErrAlreadyRegistered is returned by Register when a connector with the
	// given name has already been registered.
	ErrAlreadyRegistered = errors.New("connector: already registered")

	// ErrNotFound is returned by Get when no connector with the given name
	// is registered.
	ErrNotFound = errors.New("connector: not found")
)

// Registry manages in-process connector registrations. It is the mechanism
// by which the platform discovers and validates connectors at startup.
//
// Phase 1 (D-01): In-process registry only. Connectors call Register
// directly from their init() functions or during platform startup.
// Phase 2 will add a subprocess registry backed by hashicorp/go-plugin.
type Registry struct {
	mu         sync.RWMutex
	connectors map[string]Connector
}

// NewRegistry returns a new, empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		connectors: make(map[string]Connector),
	}
}

// Register adds a connector to the registry. The connector's APIVersion()
// MUST return exactly connector.APIVersion or registration fails with
// ErrIncompatibleVersion.
//
// If a connector with the same name is already registered, Register returns
// ErrAlreadyRegistered.
func (r *Registry) Register(name string, c Connector) error {
	if c.APIVersion() != APIVersion {
		return fmt.Errorf("%w: got %q, want %q",
			ErrIncompatibleVersion, c.APIVersion(), APIVersion)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.connectors[name]; exists {
		return fmt.Errorf("%w: %q", ErrAlreadyRegistered, name)
	}

	r.connectors[name] = c
	return nil
}

// Get returns the registered connector with the given name. If no connector
// with that name is registered, Get returns ErrNotFound.
func (r *Registry) Get(name string) (Connector, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	c, ok := r.connectors[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}

	return c, nil
}

// List returns the names of all registered connectors, sorted alphabetically.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.connectors))
	for name := range r.connectors {
		names = append(names, name)
	}

	sort.Strings(names)
	return names
}

// ErrPluginNotImplemented is returned by RegisterPlugin until Phase 2 third-party
// plugin scaffolding ships (D-07: interface reserved, implementation deferred until
// the first real third-party connector ships).
var ErrPluginNotImplemented = errors.New("connector: plugin loader not implemented in v1")

// RegisterInProcess registers an in-process Connector implementation under name.
// It is the first-party connector loading path (D-06, D-07). Equivalent to
// Register; the distinct name documents intent and pairs with RegisterPlugin.
//
// Returns ErrAlreadyRegistered if a connector with the same name exists.
// Returns ErrIncompatibleVersion if the connector's APIVersion() is wrong.
func (r *Registry) RegisterInProcess(name string, c Connector) error {
	return r.Register(name, c)
}

// RegisterPlugin is reserved for hashicorp/go-plugin subprocess loading (D-07).
// Phase 2 keeps the method shape stable; the implementation is deferred until the
// first real third-party connector ships. Currently returns ErrPluginNotImplemented.
func (r *Registry) RegisterPlugin(name string, pluginPath string) error {
	return fmt.Errorf("%w (name=%q path=%q)", ErrPluginNotImplemented, name, pluginPath)
}
