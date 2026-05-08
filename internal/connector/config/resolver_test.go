package config_test

import (
	"strings"
	"testing"

	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/connector/config"
)

// TestBuildAll_RecoversFromPanickingFactory verifies T-02-05-05: a factory
// that panics during BuildAll yields a returned error rather than crashing
// the worker process.
func TestBuildAll_RecoversFromPanickingFactory(t *testing.T) {
	reg := config.NewFactoryRegistry()
	reg.RegisterFactory("panicky", func(map[string]interface{}) (connector.Connector, error) {
		panic("boom")
	})

	cfg := &config.Config{
		Connectors: map[string]config.ConnectorConfig{
			"bad-conn": {Type: "panicky", Params: map[string]interface{}{}},
		},
	}

	err := reg.BuildAll(cfg, connector.NewRegistry())
	if err == nil {
		t.Fatal("expected error from panicking factory, got nil")
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("error should mention panic; got: %v", err)
	}
	if !strings.Contains(err.Error(), "bad-conn") {
		t.Fatalf("error should include connector name; got: %v", err)
	}
}
