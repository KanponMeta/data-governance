// Package gcs_test tests the GCS connector using fsouza/fake-gcs-server
// running in-process. No Docker or real Google Cloud credentials are required.
package gcs_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/fsouza/fake-gcs-server/fakestorage"

	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/connector/firstparty/conformance"
	"github.com/kanpon/data-governance/internal/connector/firstparty/gcs"
)

const testBucket = "conform-test"

// newFakeServer boots an in-process fake-gcs-server with the test bucket pre-created.
// Call Stop() on the returned server when done.
func newFakeServer(t *testing.T) *fakestorage.Server {
	t.Helper()
	svr, err := fakestorage.NewServerWithOptions(fakestorage.Options{
		NoListener:     true, // no TCP — in-process transport
		InitialObjects: []fakestorage.Object{},
	})
	if err != nil {
		t.Fatalf("fake-gcs-server: %v", err)
	}
	svr.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: testBucket})
	return svr
}

// makeSetup returns a conformance.Setup for the given format.
func makeSetup(format string) conformance.Setup {
	objectKey := fmt.Sprintf("conform-test-%s/users.%s", format, format)
	return conformance.Setup{
		CreateAsset: func(t *testing.T, c connector.Connector) connector.AssetRef {
			t.Helper()
			// GCS object doesn't need pre-creation (Write creates it).
			return connector.AssetRef{
				Identifier: testBucket + "/" + objectKey,
				Config:     map[string]string{"format": format},
			}
		},
		CleanupAsset: func(t *testing.T, c connector.Connector, ref connector.AssetRef) {
			// Object cleanup handled when fake server is stopped.
		},
		ExpectedSchema: []connector.Column{
			{Name: "id", Nullable: true},
			{Name: "email", Nullable: true},
		},
		SeedRows: []connector.Row{
			{Fields: map[string]any{"id": int64(1), "email": "alice@example.com"}},
			{Fields: map[string]any{"id": int64(2), "email": "bob@example.com"}},
			{Fields: map[string]any{"id": int64(3), "email": "carol@example.com"}},
		},
	}
}

func TestGCS_Conformance_Parquet(t *testing.T) {
	svr := newFakeServer(t)
	defer svr.Stop()

	client := svr.Client()
	c := gcs.New(client, testBucket, "parquet")
	defer func() { _ = c.Close() }()

	// Pre-write seed rows so Schema can read the parquet footer.
	ctx := context.Background()
	ref := connector.AssetRef{
		Identifier: testBucket + "/conform-test-parquet/users.parquet",
		Config:     map[string]string{"format": "parquet"},
	}
	setup := makeSetup("parquet")
	_, err := c.Write(ctx, connector.WriteRequest{Asset: ref, Rows: setup.SeedRows})
	if err != nil {
		t.Fatalf("pre-write for Schema: %v", err)
	}
	conformance.RunConformance(t, c, setup)
}

func TestGCS_Conformance_CSV(t *testing.T) {
	svr := newFakeServer(t)
	defer svr.Stop()

	client := svr.Client()
	c := gcs.New(client, testBucket, "csv")
	defer func() { _ = c.Close() }()

	ctx := context.Background()
	ref := connector.AssetRef{
		Identifier: testBucket + "/conform-test-csv/users.csv",
		Config:     map[string]string{"format": "csv"},
	}
	setup := makeSetup("csv")
	_, err := c.Write(ctx, connector.WriteRequest{Asset: ref, Rows: setup.SeedRows})
	if err != nil {
		t.Fatalf("pre-write for Schema: %v", err)
	}
	conformance.RunConformance(t, c, setup)
}

func TestGCS_Conformance_JSON(t *testing.T) {
	svr := newFakeServer(t)
	defer svr.Stop()

	client := svr.Client()
	c := gcs.New(client, testBucket, "json")
	defer func() { _ = c.Close() }()

	ctx := context.Background()
	ref := connector.AssetRef{
		Identifier: testBucket + "/conform-test-json/users.json",
		Config:     map[string]string{"format": "json"},
	}
	setup := makeSetup("json")
	_, err := c.Write(ctx, connector.WriteRequest{Asset: ref, Rows: setup.SeedRows})
	if err != nil {
		t.Fatalf("pre-write for Schema: %v", err)
	}
	conformance.RunConformance(t, c, setup)
}

func TestGCS_Factory_MissingBucket(t *testing.T) {
	_, err := gcs.Factory(map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing bucket, got nil")
	}
}

func TestGCS_PathTraversal(t *testing.T) {
	svr := newFakeServer(t)
	defer svr.Stop()

	client := svr.Client()
	c := gcs.New(client, testBucket, "parquet")
	defer func() { _ = c.Close() }()

	ctx := context.Background()
	_, err := c.Read(ctx, connector.ReadRequest{
		Asset: connector.AssetRef{Identifier: "bucket/../etc/passwd"},
	})
	if err == nil {
		t.Fatal("expected path traversal error, got nil")
	}
}

func TestGCS_CompileTimeAssertion(t *testing.T) {
	var _ connector.Connector = (*gcs.GCS)(nil)
}

func TestGCS_APIVersion(t *testing.T) {
	svr := newFakeServer(t)
	defer svr.Stop()

	client := svr.Client()
	c := gcs.New(client, testBucket, "parquet")
	defer func() { _ = c.Close() }()

	if got := c.APIVersion(); got != connector.APIVersion {
		t.Errorf("APIVersion() = %q, want %q", got, connector.APIVersion)
	}
}
