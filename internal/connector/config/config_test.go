package config_test

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/kanpon/data-governance/internal/connector/config"
)

const exampleYAML = `
connectors:
  postgres-prod:
    type: postgres
    dsn: ${PG_PROD_DSN}
    pool:
      max_connections: 10
  s3-warehouse:
    type: s3
    region: us-east-1
    bucket: warehouse-prod
    access_key_id: ${AWS_ACCESS_KEY_ID}
    secret_access_key: ${AWS_SECRET_ACCESS_KEY}
retry:
  default:
    max: 3
    initial_delay: 30s
    max_delay: 5m
    jitter_pct: 25
concurrency:
  default_run_tokens: 8
`

// TestLoad_ParsesConnectorsAndRetryAndConcurrency verifies basic yaml parsing.
func TestLoad_ParsesConnectorsAndRetryAndConcurrency(t *testing.T) {
	t.Setenv("PG_PROD_DSN", "postgres://localhost/testdb")
	t.Setenv("AWS_ACCESS_KEY_ID", "test-key-id")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret")

	cfg, err := config.Load([]byte(exampleYAML))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(cfg.Connectors) != 2 {
		t.Errorf("expected 2 connectors, got %d", len(cfg.Connectors))
	}
	if _, ok := cfg.Connectors["postgres-prod"]; !ok {
		t.Error("expected postgres-prod connector")
	}
	if _, ok := cfg.Connectors["s3-warehouse"]; !ok {
		t.Error("expected s3-warehouse connector")
	}
	if cfg.Retry.Default.Max != 3 {
		t.Errorf("expected retry.default.max=3, got %d", cfg.Retry.Default.Max)
	}
	if cfg.Concurrency.DefaultRunTokens != 8 {
		t.Errorf("expected concurrency.default_run_tokens=8, got %d", cfg.Concurrency.DefaultRunTokens)
	}
}

// TestLoad_ResolvesEnvVarPlaceholders verifies ${VAR} resolution.
func TestLoad_ResolvesEnvVarPlaceholders(t *testing.T) {
	t.Setenv("PG_PROD_DSN", "postgres://resolved-host/db")
	t.Setenv("AWS_ACCESS_KEY_ID", "resolved-key-id")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "resolved-secret")

	cfg, err := config.Load([]byte(exampleYAML))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	pgConn := cfg.Connectors["postgres-prod"]
	// The dsn field should have been resolved.
	dsn, ok := pgConn.Params["dsn"].(string)
	if !ok {
		t.Fatal("postgres-prod.dsn is not a string")
	}
	if dsn != "postgres://resolved-host/db" {
		t.Errorf("dsn not resolved: got %q", dsn)
	}
}

// TestLoad_MissingEnvVar returns ErrMissingEnvVar listing missing variable names.
func TestLoad_MissingEnvVar(t *testing.T) {
	// Ensure PG_PROD_DSN is not set.
	os.Unsetenv("PG_PROD_DSN")
	os.Unsetenv("AWS_ACCESS_KEY_ID")
	os.Unsetenv("AWS_SECRET_ACCESS_KEY")

	_, err := config.Load([]byte(exampleYAML))
	if err == nil {
		t.Fatal("expected error for missing env vars, got nil")
	}
	if !errors.Is(err, config.ErrMissingEnvVar) {
		t.Fatalf("expected ErrMissingEnvVar, got: %v", err)
	}
	// Error should mention the variable names (not values).
	errMsg := err.Error()
	if !strings.Contains(errMsg, "PG_PROD_DSN") {
		t.Errorf("error should mention PG_PROD_DSN; got: %s", errMsg)
	}
}

// TestLoad_NestedEnvVarInterpolation verifies ${VAR} inside deeply nested fields.
func TestLoad_NestedEnvVarInterpolation(t *testing.T) {
	t.Setenv("NESTED_DSN", "postgres://nested-host/db")
	yaml := `
connectors:
  pg:
    type: postgres
    dsn: ${NESTED_DSN}
`
	cfg, err := config.Load([]byte(yaml))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	pg := cfg.Connectors["pg"]
	dsn, _ := pg.Params["dsn"].(string)
	if dsn != "postgres://nested-host/db" {
		t.Errorf("nested dsn not resolved: got %q", dsn)
	}
}

// TestLoad_DoesNotLogSecrets verifies that resolved secret values NEVER appear in
// slog output. The test captures slog output by installing a custom handler.
func TestLoad_DoesNotLogSecrets(t *testing.T) {
	const secretValue = "SUPER_SECRET_DATABASE_PASSWORD_12345"
	t.Setenv("SECRET_DSN", secretValue)

	// Capture all slog output.
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	origDefault := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(origDefault) })

	yaml := `
connectors:
  pg:
    type: postgres
    dsn: ${SECRET_DSN}
`
	cfg, err := config.Load([]byte(yaml))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	// Sanity check: the value was actually resolved.
	pg := cfg.Connectors["pg"]
	dsn, _ := pg.Params["dsn"].(string)
	if dsn != secretValue {
		t.Errorf("dsn not resolved correctly, got %q", dsn)
	}

	// The secret value must NOT appear in any slog output.
	logOutput := buf.String()
	if strings.Contains(logOutput, secretValue) {
		t.Errorf("secret value leaked into slog output: %s", logOutput)
	}
}
