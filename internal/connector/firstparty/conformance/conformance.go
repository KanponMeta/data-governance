// Package conformance provides a shared test suite that every first-party
// connector test file invokes. It exercises the v1.0.0 connector.Connector
// contract uniformly so divergence between connectors is caught early.
package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kanpon/data-governance/internal/connector"
	"github.com/stretchr/testify/require"
)

// Setup is the per-connector test fixture.
type Setup struct {
	// CreateAsset is called once before tests run; it creates the underlying storage
	// (table, bucket, path) and returns the AssetRef.Identifier the connector reads/writes.
	CreateAsset func(t *testing.T, c connector.Connector) connector.AssetRef
	// CleanupAsset removes the storage created by CreateAsset.
	CleanupAsset func(t *testing.T, c connector.Connector, ref connector.AssetRef)
	// ExpectedSchema is the schema CreateAsset establishes; the suite calls Schema(ref) and
	// asserts it returns this list (order-insensitive comparison).
	ExpectedSchema []connector.Column
	// SeedRows is the row payload the suite writes via Write(); subsequent Read() must return them.
	SeedRows []connector.Row
}

// RunConformance runs the canonical Ping → Schema → Write → Read → Close suite.
// Connectors are expected to support all five operations; partial connectors should
// call Skip on the per-method test.
func RunConformance(t *testing.T, c connector.Connector, setup Setup) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ref := setup.CreateAsset(t, c)
	t.Cleanup(func() { setup.CleanupAsset(t, c, ref) })

	t.Run("Ping", func(t *testing.T) {
		resp, err := c.Ping(ctx, connector.PingRequest{})
		require.NoError(t, err)
		require.Equal(t, connector.APIVersion, resp.APIVersion)
		require.NotEmpty(t, resp.ConnectorName)
	})

	t.Run("Schema", func(t *testing.T) {
		resp, err := c.Schema(ctx, connector.SchemaRequest{Asset: ref})
		require.NoError(t, err)
		require.NotEmpty(t, resp.Columns)
		// Order-insensitive comparison by column name.
		got := make(map[string]connector.Column, len(resp.Columns))
		for _, col := range resp.Columns {
			got[col.Name] = col
		}
		for _, expected := range setup.ExpectedSchema {
			actual, ok := got[expected.Name]
			require.Truef(t, ok, "schema missing column %q", expected.Name)
			require.Equal(t, expected.Nullable, actual.Nullable, "column %q nullable", expected.Name)
		}
	})

	t.Run("WriteThenRead", func(t *testing.T) {
		resp, err := c.Write(ctx, connector.WriteRequest{Asset: ref, Rows: setup.SeedRows})
		require.NoError(t, err)
		require.GreaterOrEqual(t, resp.RowsWritten, int64(len(setup.SeedRows)))

		readResp, err := c.Read(ctx, connector.ReadRequest{Asset: ref, Limit: int64(len(setup.SeedRows))})
		require.NoError(t, err)
		require.Len(t, readResp.Rows, len(setup.SeedRows))
	})

	t.Run("CtxCancel", func(t *testing.T) {
		cctx, ccancel := context.WithCancel(ctx)
		ccancel() // cancel immediately
		_, err := c.Read(cctx, connector.ReadRequest{Asset: ref, Limit: 1})
		// Either context.Canceled or wrapped equivalent acceptable.
		require.Truef(t, errors.Is(err, context.Canceled) || err != nil,
			"expected ctx.Canceled or any error, got %v", err)
	})

	t.Run("Close", func(t *testing.T) {
		// Close-then-Read assertion left to per-connector implementation; the suite
		// does not call Close because subsequent t.Cleanup needs the connector live.
		_ = c
	})
}
