package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"

	"github.com/kanpon/data-governance/internal/connector"
)

var testDSN string

func TestMain(m *testing.M) {
	if os.Getenv("SKIP_DOCKER_TESTS") == "1" {
		os.Exit(m.Run())
	}
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		// Docker unavailable — skip suite gracefully.
		os.Exit(0)
	}
	defer func() { _ = testcontainers.TerminateContainer(c) }()
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		os.Exit(1)
	}
	testDSN = dsn
	os.Exit(m.Run())
}

// TestCompileTimeAssertion verifies that Postgres satisfies connector.Connector.
// The compile-time assertion is in postgres.go; this test documents the contract.
func TestCompileTimeAssertion(t *testing.T) {
	if testDSN == "" {
		t.Skip("no test DSN (docker unavailable)")
	}
	var _ connector.Connector = (*Postgres)(nil)
}

func TestPing(t *testing.T) {
	if testDSN == "" {
		t.Skip("no test DSN")
	}
	ctx := context.Background()
	p, err := New(ctx, testDSN)
	require.NoError(t, err)
	defer func() { _ = p.Close() }()

	resp, err := p.Ping(ctx, connector.PingRequest{})
	require.NoError(t, err)
	require.Equal(t, "postgres", resp.ConnectorName)
	require.Equal(t, connector.APIVersion, resp.APIVersion)
	require.True(t, resp.Capabilities.SupportsSchemaCapture)
}

func TestSchemaRoundTrip(t *testing.T) {
	if testDSN == "" {
		t.Skip("no test DSN")
	}
	ctx := context.Background()
	p, err := New(ctx, testDSN)
	require.NoError(t, err)
	defer func() { _ = p.Close() }()

	// Create table via raw pool access would require pool, but we use Write-free approach.
	// Use a helper to exec raw SQL.
	_, err = p.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_test (id bigint NOT NULL, name text)`)
	require.NoError(t, err)
	defer func() { _, _ = p.pool.Exec(ctx, `DROP TABLE IF EXISTS schema_test`) }()

	resp, err := p.Schema(ctx, connector.SchemaRequest{
		Asset: connector.AssetRef{Identifier: "public.schema_test"},
	})
	require.NoError(t, err)
	require.Len(t, resp.Columns, 2)
	require.Equal(t, "id", resp.Columns[0].Name)
	require.Equal(t, "name", resp.Columns[1].Name)
	require.False(t, resp.Columns[0].Nullable) // NOT NULL
	require.True(t, resp.Columns[1].Nullable)  // nullable by default
	require.True(t, resp.CapturedAt.After(time.Time{}))
}

func TestWriteThenRead(t *testing.T) {
	if testDSN == "" {
		t.Skip("no test DSN")
	}
	ctx := context.Background()
	p, err := New(ctx, testDSN)
	require.NoError(t, err)
	defer func() { _ = p.Close() }()

	_, err = p.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS write_read_test (id bigint, email text)`)
	require.NoError(t, err)
	defer func() { _, _ = p.pool.Exec(ctx, `DROP TABLE IF EXISTS write_read_test`) }()

	rows := []connector.Row{
		{Fields: map[string]any{"id": int64(1), "email": "a@b.com"}},
		{Fields: map[string]any{"id": int64(2), "email": "c@d.com"}},
		{Fields: map[string]any{"id": int64(3), "email": "e@f.com"}},
	}
	writeResp, err := p.Write(ctx, connector.WriteRequest{
		Asset: connector.AssetRef{Identifier: "public.write_read_test"},
		Rows:  rows,
	})
	require.NoError(t, err)
	require.Equal(t, int64(3), writeResp.RowsWritten)

	// Read all rows back.
	readResp, err := p.Read(ctx, connector.ReadRequest{
		Asset: connector.AssetRef{Identifier: "public.write_read_test"},
	})
	require.NoError(t, err)
	require.Len(t, readResp.Rows, 3)

	// Read with Limit=2 returns at most 2 rows.
	readRespLimited, err := p.Read(ctx, connector.ReadRequest{
		Asset: connector.AssetRef{Identifier: "public.write_read_test"},
		Limit: 2,
	})
	require.NoError(t, err)
	require.Len(t, readRespLimited.Rows, 2)
}

func TestReadCtxCancel(t *testing.T) {
	if testDSN == "" {
		t.Skip("no test DSN")
	}
	ctx := context.Background()
	p, err := New(ctx, testDSN)
	require.NoError(t, err)
	defer func() { _ = p.Close() }()

	// Use pg_sleep to simulate a long-running query.
	_, err = p.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS ctx_cancel_test (id bigint)`)
	require.NoError(t, err)
	defer func() { _, _ = p.pool.Exec(ctx, `DROP TABLE IF EXISTS ctx_cancel_test`) }()

	// Insert 1 row so the table exists.
	_, err = p.pool.Exec(ctx, `INSERT INTO ctx_cancel_test VALUES (1)`)
	require.NoError(t, err)

	// Cancel ctx after 50ms; pg_sleep(5) should be interrupted.
	cancelCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	// Execute a query that takes 5 seconds via a subquery with pg_sleep.
	// We abuse the WHERE clause to inject the sleep.
	_, err = p.pool.Exec(cancelCtx, `SELECT pg_sleep(5)`)
	// We expect the exec itself to be cancelled; the Read wrapper would too.
	// Now test Read with a cancelled context.
	alreadyCancelled, cancelNow := context.WithCancel(ctx)
	cancelNow()

	_, readErr := p.Read(alreadyCancelled, connector.ReadRequest{
		Asset: connector.AssetRef{Identifier: "public.ctx_cancel_test"},
	})
	require.Error(t, readErr)
	require.True(t, errors.Is(readErr, context.Canceled), "expected context.Canceled, got: %v", readErr)
}

func TestFactory_MissingDSN(t *testing.T) {
	_, err := Factory(map[string]interface{}{})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrMissingDSN)
}

func TestFactory_Builds(t *testing.T) {
	if testDSN == "" {
		t.Skip("no test DSN")
	}
	c, err := Factory(map[string]interface{}{"dsn": testDSN})
	require.NoError(t, err)
	require.NotNil(t, c)
	p := c.(*Postgres)
	defer func() { _ = p.Close() }()
	ctx := context.Background()
	_, err = p.Ping(ctx, connector.PingRequest{})
	require.NoError(t, err)
}

func TestClose(t *testing.T) {
	if testDSN == "" {
		t.Skip("no test DSN")
	}
	ctx := context.Background()
	p, err := New(ctx, testDSN)
	require.NoError(t, err)

	err = p.Close()
	require.NoError(t, err)

	_, err = p.Read(ctx, connector.ReadRequest{
		Asset: connector.AssetRef{Identifier: "public.any"},
	})
	require.ErrorIs(t, err, ErrClosed)
}

func TestClose_Idempotent(t *testing.T) {
	if testDSN == "" {
		t.Skip("no test DSN")
	}
	ctx := context.Background()
	p, err := New(ctx, testDSN)
	require.NoError(t, err)

	require.NoError(t, p.Close())
	require.NoError(t, p.Close()) // second close is a no-op
}

func TestQuoteIdentifier_RejectQuotes(t *testing.T) {
	_, err := quoteIdentifier(`name"injection`)
	require.Error(t, err, "expected error for identifier with double quote")
}

func TestAPIVersion(t *testing.T) {
	p := &Postgres{}
	require.Equal(t, connector.APIVersion, p.APIVersion())
}
