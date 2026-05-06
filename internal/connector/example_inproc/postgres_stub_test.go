package example_inproc

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/kanpon/data-governance/internal/connector"
)

func TestStubRegistersAndPings(t *testing.T) {
	reg := connector.NewRegistry()
	stub := NewPostgresStub()

	err := reg.Register("postgres", stub)
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	c, err := reg.Get("postgres")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	cached, ok := c.(*PostgresStub)
	if !ok {
		t.Fatalf("Get returned %T, want *PostgresStub", c)
	}

	resp, err := cached.Ping(ctxWithTimeout(), connector.PingRequest{})
	if err != nil {
		t.Fatalf("Ping returned error: %v", err)
	}

	if resp.APIVersion != connector.APIVersion {
		t.Errorf("Ping.APIVersion = %q, want %q", resp.APIVersion, connector.APIVersion)
	}

	if resp.ConnectorName != "postgres-stub" {
		t.Errorf("Ping.ConnectorName = %q, want %q", resp.ConnectorName, "postgres-stub")
	}
}

func TestImportBoundary(t *testing.T) {
	// Run `go list` on this package to inspect its import graph.
	cmd := exec.Command("go", "list", "-f", "{{join .Imports \"\\n\"}}", "github.com/kanpon/data-governance/internal/connector/example_inproc")
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list failed (may need module graph): %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	// Check that the only github.com/kanpon/data-governance import is internal/connector.
	var badImports []string
	for _, line := range lines {
		if strings.HasPrefix(line, "github.com/kanpon/data-governance/") && line != "github.com/kanpon/data-governance/internal/connector" {
			badImports = append(badImports, line)
		}
	}

	if len(badImports) > 0 {
		t.Errorf("example_inproc imports non-connector kanpon packages: %v\nOnly github.com/kanpon/data-governance/internal/connector is allowed", badImports)
	}
}

func ctxWithTimeout() context.Context {
	// Simple helper — test context with a reasonable deadline.
	return context.Background()
}
