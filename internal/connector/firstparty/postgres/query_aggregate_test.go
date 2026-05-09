package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/connector"
)

// TestPostgres_QueryAggregate_HappyPath issues a single-column scalar query
// and asserts the AggregateRow contains exactly that scalar.
func TestPostgres_QueryAggregate_HappyPath(t *testing.T) {
	if testDSN == "" {
		t.Skip("no test DSN (docker unavailable)")
	}
	ctx := context.Background()
	p, err := New(ctx, testDSN)
	require.NoError(t, err)
	defer p.Close()

	row, err := p.QueryAggregate(ctx, connector.AssetRef{Identifier: "noop"}, `SELECT 42::float8 AS answer`)
	require.NoError(t, err)
	require.Len(t, row.Values, 1)
	require.Equal(t, []string{"answer"}, row.Columns)
	v, ok := row.Values[0].(float64)
	require.True(t, ok)
	require.InDelta(t, 42.0, v, 1e-9)
}

// TestPostgres_QueryAggregate_ContextTimeout asserts that a deadline-exceeded
// ctx surfaces as an error rather than blocking. Pitfall #10 mitigation.
func TestPostgres_QueryAggregate_ContextTimeout(t *testing.T) {
	if testDSN == "" {
		t.Skip("no test DSN (docker unavailable)")
	}
	ctx := context.Background()
	p, err := New(ctx, testDSN)
	require.NoError(t, err)
	defer p.Close()

	tctx, cancel := context.WithTimeout(ctx, 1*time.Millisecond)
	defer cancel()
	// pg_sleep returns void slowly; ctx will deadline.
	_, err = p.QueryAggregate(tctx, connector.AssetRef{Identifier: "noop"}, `SELECT pg_sleep(1)`)
	require.Error(t, err)
}

// TestPostgres_QueryAggregate_NoRows asserts that an aggregate query returning
// zero rows surfaces an explicit error so the evaluator can mark the result
// as 'error' with a clear message.
func TestPostgres_QueryAggregate_NoRows(t *testing.T) {
	if testDSN == "" {
		t.Skip("no test DSN (docker unavailable)")
	}
	ctx := context.Background()
	p, err := New(ctx, testDSN)
	require.NoError(t, err)
	defer p.Close()

	_, err = p.QueryAggregate(ctx, connector.AssetRef{Identifier: "noop"}, `SELECT 1 WHERE FALSE`)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "no rows"))
}

// TestPostgres_QueryAggregate_MultiColumn verifies a query returning multiple
// columns yields a Values slice with positional alignment to Columns.
func TestPostgres_QueryAggregate_MultiColumn(t *testing.T) {
	if testDSN == "" {
		t.Skip("no test DSN (docker unavailable)")
	}
	ctx := context.Background()
	p, err := New(ctx, testDSN)
	require.NoError(t, err)
	defer p.Close()

	row, err := p.QueryAggregate(ctx, connector.AssetRef{Identifier: "noop"}, `SELECT 1::float8 AS a, 2::float8 AS b`)
	require.NoError(t, err)
	require.Len(t, row.Values, 2)
	require.Equal(t, []string{"a", "b"}, row.Columns)
}
