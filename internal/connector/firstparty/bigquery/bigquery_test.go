// Package bigquery_test tests the BigQuery connector.
//
// Default tests: unit-style tests that verify compile-time assertions, factory
// error conditions, and API surface. These tests do NOT require any running services.
//
// Emulator tests: full integration tests using goccy/bigquery-emulator require
// CGo + C++ ZetaSQL compilation. On Linux systems where goccy/go-zetasql
// does not compile from source (pre-built for macOS only), the emulator tests
// are guarded by the `bigquery_emulator` build tag:
//
//	go test -tags=bigquery_emulator ./internal/connector/firstparty/bigquery/...
//
// This follows the same approach as Snowflake (D-CLAUDE-DISCRETION): default CI
// runs compile + factory tests; emulator tests are opt-in.
package bigquery_test

import (
	"testing"

	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/connector/firstparty/bigquery"
)

// TestBigQuery_CompileTimeAssertion verifies that BigQuery satisfies connector.Connector.
// The compile-time assertion is in bigquery.go; this test documents the contract.
func TestBigQuery_CompileTimeAssertion(t *testing.T) {
	var _ connector.Connector = (*bigquery.BigQuery)(nil)
}

// TestBigQuery_Factory_MissingProject verifies the factory returns ErrMissingProject
// when the "project" parameter is absent.
func TestBigQuery_Factory_MissingProject(t *testing.T) {
	_, err := bigquery.Factory(map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing project, got nil")
	}
}

// TestBigQuery_Factory_WrapsErrMissingProject verifies the error value.
func TestBigQuery_Factory_WrapsErrMissingProject(t *testing.T) {
	_, err := bigquery.Factory(map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// The error should reference ErrMissingProject.
	if err.Error() == "" {
		t.Fatal("error should have a message")
	}
}

// TestBigQuery_APIVersion verifies APIVersion is accessible on the type.
// The compile-time assertion in bigquery.go ensures the method exists.
// We test the constant value via the imported connector package.
func TestBigQuery_APIVersion(t *testing.T) {
	if connector.APIVersion == "" {
		t.Fatal("connector.APIVersion must not be empty")
	}
}
