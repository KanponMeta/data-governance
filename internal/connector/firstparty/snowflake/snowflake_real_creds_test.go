//go:build snowflake_real_creds

// Package snowflake_test (real credentials integration tests).
//
// Real Snowflake conformance — runs only with `go test -tags=snowflake_real_creds`.
// Requires SNOWFLAKE_DSN env var in the format:
//
//	user:password@account/database/schema?warehouse=mywh
//
// Documented in README as a nightly job. DO NOT run in normal CI — it connects
// to a real Snowflake account and incurs usage charges.
//
// These tests prove round-trip data correctness (T-02-05-04) that the default
// sqlmock tests cannot.
package snowflake_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/connector/firstparty/conformance"
	"github.com/kanpon/data-governance/internal/connector/firstparty/snowflake"
)

// TestSnowflake_Conformance_RealCreds runs the full conformance suite against
// a real Snowflake account. Requires SNOWFLAKE_DSN env var.
func TestSnowflake_Conformance_RealCreds(t *testing.T) {
	dsn := os.Getenv("SNOWFLAKE_DSN")
	if dsn == "" {
		t.Skip("SNOWFLAKE_DSN not set — skipping real-creds conformance test")
	}

	ctx := context.Background()
	c, err := snowflake.New(ctx, dsn)
	if err != nil {
		t.Fatalf("snowflake.New: %v", err)
	}
	defer func() { _ = c.Close() }()

	const testSchema = "PUBLIC"
	const testTable = "CONFORM_USERS"

	setup := conformance.Setup{
		CreateAsset: func(t *testing.T, c connector.Connector) connector.AssetRef {
			t.Helper()
			// Assumes SNOWFLAKE_DSN connects to a database where PUBLIC schema is writable.
			// The table must be created manually or via a setup script before running.
			// DDL: CREATE OR REPLACE TABLE PUBLIC.CONFORM_USERS (ID NUMBER NOT NULL, EMAIL VARCHAR);
			return connector.AssetRef{
				Identifier: fmt.Sprintf("%s.%s", testSchema, testTable),
			}
		},
		CleanupAsset: func(t *testing.T, c connector.Connector, ref connector.AssetRef) {
			// Cleanup: run `TRUNCATE TABLE PUBLIC.CONFORM_USERS` manually after test.
		},
		ExpectedSchema: []connector.Column{
			{Name: "ID", Nullable: false},
			{Name: "EMAIL", Nullable: true},
		},
		SeedRows: []connector.Row{
			{Fields: map[string]any{"ID": int64(1), "EMAIL": "alice@example.com"}},
			{Fields: map[string]any{"ID": int64(2), "EMAIL": "bob@example.com"}},
			{Fields: map[string]any{"ID": int64(3), "EMAIL": "carol@example.com"}},
		},
	}
	conformance.RunConformance(t, c, setup)
}
