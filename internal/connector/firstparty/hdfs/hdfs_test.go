// Package hdfs_test provides integration tests for the HDFS connector.
//
// Tests require a running HDFS namenode. Set HDFS_TEST_NAMENODE to the
// address of the namenode (e.g. "localhost:9000") before running:
//
//	HDFS_TEST_NAMENODE=localhost:9000 go test ./internal/connector/firstparty/hdfs/...
//
// When HDFS_TEST_NAMENODE is not set, all tests are skipped gracefully.
// For local HDFS setup, see testdata/hdfs/docker-compose.yml.
//
// The conformance suite proves round-trip correctness across Parquet, CSV,
// and JSON formats (T-02-05-03 accepted limitation: in-memory for Phase 2).
package hdfs_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	hdfslib "github.com/colinmarc/hdfs/v2"

	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/connector/firstparty/conformance"
	hdfsc "github.com/kanpon/data-governance/internal/connector/firstparty/hdfs"
)

// TestHDFS_CompileTimeAssertion verifies HDFS satisfies connector.Connector.
func TestHDFS_CompileTimeAssertion(t *testing.T) {
	var _ connector.Connector = (*hdfsc.HDFS)(nil)
}

// TestHDFS_Factory_MissingNamenode verifies Factory returns ErrMissingNamenode.
func TestHDFS_Factory_MissingNamenode(t *testing.T) {
	_, err := hdfsc.Factory(map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing namenode, got nil")
	}
}

func namenodeAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("HDFS_TEST_NAMENODE")
	if addr == "" {
		t.Skip("HDFS_TEST_NAMENODE not set — skipping HDFS integration tests")
	}
	return addr
}

func newClient(t *testing.T, addr string) *hdfslib.Client {
	t.Helper()
	opts := hdfslib.ClientOptions{
		Addresses: []string{addr},
		User:      "testuser",
	}
	client, err := hdfslib.NewClient(opts)
	if err != nil {
		t.Fatalf("hdfslib.NewClient: %v", err)
	}
	return client
}

func testAssetPath(format string) string {
	return fmt.Sprintf("/conformance_test_%d_%s/data.%s", time.Now().UnixNano(), format, format)
}

// TestHDFS_Conformance_Parquet runs the conformance suite using Parquet encoding.
func TestHDFS_Conformance_Parquet(t *testing.T) {
	addr := namenodeAddr(t)
	client := newClient(t, addr)
	t.Cleanup(func() { _ = client.Close() })

	assetPath := testAssetPath("parquet")
	c := hdfsc.NewFromClient(client, "parquet")

	setup := conformance.Setup{
		CreateAsset: func(t *testing.T, _ connector.Connector) connector.AssetRef {
			t.Helper()
			// Pre-write seed rows so Schema() can read the parquet footer.
			seedRows := []connector.Row{
				{Fields: map[string]any{"id": "1", "email": "alice@example.com"}},
				{Fields: map[string]any{"id": "2", "email": "bob@example.com"}},
				{Fields: map[string]any{"id": "3", "email": "carol@example.com"}},
			}
			resp, err := c.Write(context.Background(), connector.WriteRequest{
				Asset: connector.AssetRef{Identifier: assetPath},
				Rows:  seedRows,
			})
			if err != nil {
				t.Fatalf("pre-write seed: %v", err)
			}
			if resp.RowsWritten != 3 {
				t.Fatalf("pre-write: expected 3 rows written, got %d", resp.RowsWritten)
			}
			return connector.AssetRef{Identifier: assetPath}
		},
		CleanupAsset: func(t *testing.T, _ connector.Connector, ref connector.AssetRef) {
			t.Helper()
			_ = client.Remove(assetPath)
		},
		ExpectedSchema: []connector.Column{
			{Name: "email", RawType: "string", Nullable: true},
			{Name: "id", RawType: "string", Nullable: true},
		},
		SeedRows: []connector.Row{
			{Fields: map[string]any{"id": "1", "email": "alice@example.com"}},
			{Fields: map[string]any{"id": "2", "email": "bob@example.com"}},
			{Fields: map[string]any{"id": "3", "email": "carol@example.com"}},
		},
	}
	conformance.RunConformance(t, c, setup)
}

// TestHDFS_Conformance_CSV runs the conformance suite using CSV encoding.
func TestHDFS_Conformance_CSV(t *testing.T) {
	addr := namenodeAddr(t)
	client := newClient(t, addr)
	t.Cleanup(func() { _ = client.Close() })

	assetPath := testAssetPath("csv")
	c := hdfsc.NewFromClient(client, "csv")

	setup := conformance.Setup{
		CreateAsset: func(t *testing.T, _ connector.Connector) connector.AssetRef {
			t.Helper()
			seedRows := []connector.Row{
				{Fields: map[string]any{"id": "1", "email": "alice@example.com"}},
				{Fields: map[string]any{"id": "2", "email": "bob@example.com"}},
				{Fields: map[string]any{"id": "3", "email": "carol@example.com"}},
			}
			if _, err := c.Write(context.Background(), connector.WriteRequest{
				Asset: connector.AssetRef{Identifier: assetPath},
				Rows:  seedRows,
			}); err != nil {
				t.Fatalf("pre-write seed: %v", err)
			}
			return connector.AssetRef{Identifier: assetPath}
		},
		CleanupAsset: func(t *testing.T, _ connector.Connector, ref connector.AssetRef) {
			_ = client.Remove(assetPath)
		},
		ExpectedSchema: []connector.Column{
			{Name: "id", RawType: "string", Nullable: true},
			{Name: "email", RawType: "string", Nullable: true},
		},
		SeedRows: []connector.Row{
			{Fields: map[string]any{"id": "1", "email": "alice@example.com"}},
			{Fields: map[string]any{"id": "2", "email": "bob@example.com"}},
			{Fields: map[string]any{"id": "3", "email": "carol@example.com"}},
		},
	}
	conformance.RunConformance(t, c, setup)
}

// TestHDFS_Conformance_JSON runs the conformance suite using JSON (NDJSON) encoding.
func TestHDFS_Conformance_JSON(t *testing.T) {
	addr := namenodeAddr(t)
	client := newClient(t, addr)
	t.Cleanup(func() { _ = client.Close() })

	assetPath := testAssetPath("json")
	c := hdfsc.NewFromClient(client, "json")

	setup := conformance.Setup{
		CreateAsset: func(t *testing.T, _ connector.Connector) connector.AssetRef {
			t.Helper()
			seedRows := []connector.Row{
				{Fields: map[string]any{"id": "1", "email": "alice@example.com"}},
				{Fields: map[string]any{"id": "2", "email": "bob@example.com"}},
				{Fields: map[string]any{"id": "3", "email": "carol@example.com"}},
			}
			if _, err := c.Write(context.Background(), connector.WriteRequest{
				Asset: connector.AssetRef{Identifier: assetPath},
				Rows:  seedRows,
			}); err != nil {
				t.Fatalf("pre-write seed: %v", err)
			}
			return connector.AssetRef{Identifier: assetPath}
		},
		CleanupAsset: func(t *testing.T, _ connector.Connector, ref connector.AssetRef) {
			_ = client.Remove(assetPath)
		},
		ExpectedSchema: []connector.Column{
			{Name: "id", RawType: "json", Nullable: true},
			{Name: "email", RawType: "json", Nullable: true},
		},
		SeedRows: []connector.Row{
			{Fields: map[string]any{"id": "1", "email": "alice@example.com"}},
			{Fields: map[string]any{"id": "2", "email": "bob@example.com"}},
			{Fields: map[string]any{"id": "3", "email": "carol@example.com"}},
		},
	}
	conformance.RunConformance(t, c, setup)
}
