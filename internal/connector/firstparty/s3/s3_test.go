package s3_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	tclocalstack "github.com/testcontainers/testcontainers-go/modules/localstack"

	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/connector/firstparty/conformance"
	"github.com/kanpon/data-governance/internal/connector/firstparty/s3"
)

// localstackEndpoint holds the endpoint URL set up in TestMain.
var localstackEndpoint string

const testBucket = "conform-test"

func TestMain(m *testing.M) {
	if os.Getenv("SKIP_DOCKER_TESTS") == "1" {
		os.Exit(m.Run())
	}
	ctx := context.Background()
	ls, err := tclocalstack.Run(ctx, "localstack/localstack:3")
	if err != nil {
		// Docker unavailable — skip suite gracefully.
		os.Exit(0)
	}
	defer func() {
		if ls != nil {
			_ = ls.Terminate(ctx)
		}
	}()

	mappedPort, err := ls.MappedPort(ctx, "4566/tcp")
	if err != nil {
		os.Exit(1)
	}
	host, err := ls.Host(ctx)
	if err != nil {
		os.Exit(1)
	}
	localstackEndpoint = fmt.Sprintf("http://%s:%s", host, mappedPort.Port())

	// Create the test bucket using the AWS SDK directly.
	client, err := buildTestClient(ctx, localstackEndpoint)
	if err != nil {
		os.Exit(1)
	}
	if _, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{
		Bucket: strPtr(testBucket),
	}); err != nil {
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func buildTestClient(ctx context.Context, endpoint string) (*awss3.Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		return nil, err
	}
	return awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = strPtr(endpoint)
		o.UsePathStyle = true
	}), nil
}

func strPtr(s string) *string { return &s }

// makeSetup returns a conformance.Setup for the given format.
func makeSetup(format string) conformance.Setup {
	objectKey := fmt.Sprintf("conform-test-%s/users.%s", format, format)
	return conformance.Setup{
		CreateAsset: func(t *testing.T, c connector.Connector) connector.AssetRef {
			t.Helper()
			// The object doesn't need pre-creation for S3 (Write creates it).
			return connector.AssetRef{
				Identifier: testBucket + "/" + objectKey,
				Config:     map[string]string{"format": format},
			}
		},
		CleanupAsset: func(t *testing.T, c connector.Connector, ref connector.AssetRef) {
			t.Helper()
			ctx := context.Background()
			client, err := buildTestClient(ctx, localstackEndpoint)
			if err != nil {
				return
			}
			_, _ = client.DeleteObject(ctx, &awss3.DeleteObjectInput{
				Bucket: strPtr(testBucket),
				Key:    strPtr(objectKey),
			})
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

func buildConnector(t *testing.T, format string) *s3.S3 {
	t.Helper()
	if localstackEndpoint == "" {
		t.Skip("no localstack endpoint (docker unavailable)")
	}
	ctx := context.Background()
	client, err := buildTestClient(ctx, localstackEndpoint)
	if err != nil {
		t.Fatalf("buildTestClient: %v", err)
	}
	// Write first so Schema/Read can follow (S3 has no DDL, object must exist for Schema).
	return s3.New(client, testBucket, format)
}

func TestS3_Conformance_Parquet(t *testing.T) {
	c := buildConnector(t, "parquet")
	defer func() { _ = c.Close() }()

	setup := makeSetup("parquet")
	// Pre-write seed rows so Schema can read the parquet footer.
	ctx := context.Background()
	ref := connector.AssetRef{
		Identifier: testBucket + "/conform-test-parquet/users.parquet",
		Config:     map[string]string{"format": "parquet"},
	}
	_, err := c.Write(ctx, connector.WriteRequest{Asset: ref, Rows: setup.SeedRows})
	if err != nil {
		t.Fatalf("pre-write for Schema: %v", err)
	}
	// Reset setup to reuse the same key (WriteThenRead will append; skip by running separately).
	conformance.RunConformance(t, c, setup)
}

func TestS3_Conformance_CSV(t *testing.T) {
	c := buildConnector(t, "csv")
	defer func() { _ = c.Close() }()

	setup := makeSetup("csv")
	// Pre-write so Schema works.
	ctx := context.Background()
	ref := connector.AssetRef{
		Identifier: testBucket + "/conform-test-csv/users.csv",
		Config:     map[string]string{"format": "csv"},
	}
	_, err := c.Write(ctx, connector.WriteRequest{Asset: ref, Rows: setup.SeedRows})
	if err != nil {
		t.Fatalf("pre-write for Schema: %v", err)
	}
	conformance.RunConformance(t, c, setup)
}

func TestS3_Conformance_JSON(t *testing.T) {
	c := buildConnector(t, "json")
	defer func() { _ = c.Close() }()

	setup := makeSetup("json")
	// Pre-write so Schema works.
	ctx := context.Background()
	ref := connector.AssetRef{
		Identifier: testBucket + "/conform-test-json/users.json",
		Config:     map[string]string{"format": "json"},
	}
	_, err := c.Write(ctx, connector.WriteRequest{Asset: ref, Rows: setup.SeedRows})
	if err != nil {
		t.Fatalf("pre-write for Schema: %v", err)
	}
	conformance.RunConformance(t, c, setup)
}

func TestS3_Factory_MissingBucket(t *testing.T) {
	_, err := s3.Factory(map[string]interface{}{"region": "us-east-1"})
	if err == nil {
		t.Fatal("expected error for missing bucket")
	}
}

func TestS3_Factory_MissingRegion(t *testing.T) {
	_, err := s3.Factory(map[string]interface{}{"bucket": "mybucket"})
	if err == nil {
		t.Fatal("expected error for missing region")
	}
}

func TestS3_PathTraversal(t *testing.T) {
	if localstackEndpoint == "" {
		t.Skip("no localstack endpoint (docker unavailable)")
	}
	ctx := context.Background()
	client, err := buildTestClient(ctx, localstackEndpoint)
	if err != nil {
		t.Fatalf("buildTestClient: %v", err)
	}
	c := s3.New(client, testBucket, "parquet")
	defer func() { _ = c.Close() }()

	_, err = c.Read(ctx, connector.ReadRequest{
		Asset: connector.AssetRef{Identifier: "bucket/../etc/passwd"},
	})
	if err == nil {
		t.Fatal("expected path traversal error, got nil")
	}
}

func TestS3_CompileTimeAssertion(t *testing.T) {
	var _ connector.Connector = (*s3.S3)(nil)
}
