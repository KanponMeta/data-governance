package config

import (
	"fmt"

	"github.com/kanpon/data-governance/internal/connector"
)

// Factory builds a Connector from the params block of a ConnectorConfig.
// Plan 02-04 implements the postgres factory; plan 02-05 implements others.
// All params values are pre-resolved (env-vars already substituted by Load).
type Factory func(params map[string]interface{}) (connector.Connector, error)

// FactoryRegistry maps a connector type (yaml `type:` field) to its Factory.
// Instantiate with NewFactoryRegistry, then call RegisterFactory for each type.
type FactoryRegistry struct {
	factories map[string]Factory
}

// NewFactoryRegistry returns an empty FactoryRegistry.
func NewFactoryRegistry() *FactoryRegistry {
	return &FactoryRegistry{factories: make(map[string]Factory)}
}

// RegisterFactory associates a connector type name with its factory function.
// The type name must match the `type:` field in the yaml config block.
func (r *FactoryRegistry) RegisterFactory(typeName string, f Factory) {
	r.factories[typeName] = f
}

// BuildAll iterates cfg.Connectors and for each entry:
//  1. Looks up the Factory for cc.Type in r.factories.
//  2. Calls Factory(cc.Params) to instantiate the connector.
//  3. Calls reg.RegisterInProcess(name, instance) to register it.
//
// Returns an error if any connector type is unknown, instantiation fails, or
// registration fails (e.g. ErrAlreadyRegistered).
//
// Plan 02-04 calls BuildAll after registering the postgres factory; plan 02-05
// registers additional factories (s3, bigquery, etc.).
func (r *FactoryRegistry) BuildAll(cfg *Config, reg *connector.Registry) error {
	for name, cc := range cfg.Connectors {
		f, ok := r.factories[cc.Type]
		if !ok {
			return fmt.Errorf("config: unknown connector type %q for %q (register a factory with RegisterFactory)", cc.Type, name)
		}
		inst, err := safeBuild(f, cc.Params)
		if err != nil {
			return fmt.Errorf("config: build connector %q: %w", name, err)
		}
		if err := reg.RegisterInProcess(name, inst); err != nil {
			return fmt.Errorf("config: register connector %q: %w", name, err)
		}
	}
	return nil
}

// safeBuild invokes f and converts any panic into an error so a misbehaving
// factory aborts startup with a clear message rather than crashing the worker
// process (T-02-05-05).
func safeBuild(f Factory, params map[string]interface{}) (inst connector.Connector, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("factory panicked: %v", r)
		}
	}()
	return f(params)
}
