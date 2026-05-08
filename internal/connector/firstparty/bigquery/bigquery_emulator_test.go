//go:build bigquery_emulator

// Package bigquery_test (emulator integration tests).
// Run with: go test -tags=bigquery_emulator ./internal/connector/firstparty/bigquery/...
//
// This file uses goccy/bigquery-emulator which requires CGo + C++ ZetaSQL
// compilation. The bigquery-emulator ships pre-built for macOS; Linux users
// must compile the C++ ZetaSQL dependency from source using the instructions at:
// https://github.com/goccy/bigquery-emulator/README.md#linux
//
// CI runs these tests as a separate nightly job (similar to Snowflake real-creds tests).
package bigquery_test

import (
	"context"
	"fmt"
	"testing"

	bq "cloud.google.com/go/bigquery"
	bqemulator "github.com/goccy/bigquery-emulator/server"
	"github.com/goccy/bigquery-emulator/types"
	"google.golang.org/api/option"

	"github.com/kanpon/data-governance/internal/connector"
	bqconn "github.com/kanpon/data-governance/internal/connector/firstparty/bigquery"
	"github.com/kanpon/data-governance/internal/connector/firstparty/conformance"
)

const (
	testProject = "test-project"
	testDataset = "test_dataset"
	testTable   = "conform_users"
)

// TestBigQuery_Conformance_WithEmulator runs the shared conformance suite
// against the goccy/bigquery-emulator in-process test server.
// Only compiled and run with -tags=bigquery_emulator.
func TestBigQuery_Conformance_WithEmulator(t *testing.T) {
	ctx := context.Background()

	bqServer, err := bqemulator.New(bqemulator.TempStorage)
	if err != nil {
		t.Fatalf("bigquery-emulator new: %v", err)
	}
	if err := bqServer.SetProject(testProject); err != nil {
		t.Fatalf("bigquery-emulator SetProject: %v", err)
	}
	if err := bqServer.Load(bqemulator.StructSource(types.NewProject(testProject))); err != nil {
		t.Fatalf("bigquery-emulator Load: %v", err)
	}
	testServer := bqServer.TestServer()
	defer func() {
		testServer.Close()
		bqServer.Stop(ctx)
	}()

	c, err := bqconn.New(ctx, testProject,
		option.WithEndpoint(testServer.URL),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("bigquery.New: %v", err)
	}
	defer func() { _ = c.Close() }()

	adminClient, err := bq.NewClient(ctx, testProject,
		option.WithEndpoint(testServer.URL),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("admin bq client: %v", err)
	}
	defer adminClient.Close()

	if err := adminClient.Dataset(testDataset).Create(ctx, &bq.DatasetMetadata{Name: testDataset}); err != nil {
		t.Fatalf("create dataset: %v", err)
	}
	schema := bq.Schema{
		{Name: "id", Type: bq.IntegerFieldType, Required: true},
		{Name: "email", Type: bq.StringFieldType},
	}
	if err := adminClient.Dataset(testDataset).Table(testTable).Create(ctx, &bq.TableMetadata{Schema: schema}); err != nil {
		t.Fatalf("create table: %v", err)
	}

	assetID := fmt.Sprintf("%s.%s.%s", testProject, testDataset, testTable)
	setup := conformance.Setup{
		CreateAsset: func(t *testing.T, c connector.Connector) connector.AssetRef {
			return connector.AssetRef{Identifier: assetID}
		},
		CleanupAsset: func(t *testing.T, c connector.Connector, ref connector.AssetRef) {},
		ExpectedSchema: []connector.Column{
			{Name: "id", Nullable: false},
			{Name: "email", Nullable: true},
		},
		SeedRows: []connector.Row{
			{Fields: map[string]any{"id": int64(1), "email": "alice@example.com"}},
			{Fields: map[string]any{"id": int64(2), "email": "bob@example.com"}},
			{Fields: map[string]any{"id": int64(3), "email": "carol@example.com"}},
		},
	}
	conformance.RunConformance(t, c, setup)
}
