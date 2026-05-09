package quality_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/connector"
)

// fakeEval is an asset.QualityEvaluator stub backed by a programmable
// QueryAggregate function and a fixed table reference.
type fakeEval struct {
	table string
	fn    func(ctx context.Context, sqlText string) (connector.AggregateRow, error)
}

func (f *fakeEval) QueryAggregate(ctx context.Context, sqlText string) (connector.AggregateRow, error) {
	return f.fn(ctx, sqlText)
}

func (f *fakeEval) AssetTable() string { return f.table }

func (f *fakeEval) Timeout() time.Duration { return 30 * time.Second }

func TestNullCheck_Pass_WhenRateBelowThreshold(t *testing.T) {
	rule := asset.NullCheck{Column: "customer_id", MaxRate: 0.1}
	eval := &fakeEval{
		table: "public.orders",
		fn: func(_ context.Context, _ string) (connector.AggregateRow, error) {
			return connector.AggregateRow{Values: []any{100.0, 5.0}}, nil
		},
	}
	res, err := rule.Evaluate(context.Background(), eval)
	require.NoError(t, err)
	require.Equal(t, "passed", res.Status)
	require.NotNil(t, res.MeasuredValue)
	require.InDelta(t, 0.05, *res.MeasuredValue, 1e-9)
}

func TestNullCheck_Fail_WhenRateAboveThreshold(t *testing.T) {
	rule := asset.NullCheck{Column: "customer_id", MaxRate: 0.01}
	eval := &fakeEval{
		table: "public.orders",
		fn: func(_ context.Context, _ string) (connector.AggregateRow, error) {
			return connector.AggregateRow{Values: []any{100.0, 5.0}}, nil
		},
	}
	res, err := rule.Evaluate(context.Background(), eval)
	require.NoError(t, err)
	require.Equal(t, "failed", res.Status)
	require.NotNil(t, res.MeasuredValue)
	require.InDelta(t, 0.05, *res.MeasuredValue, 1e-9)
}

func TestNullCheck_Error_WhenQueryFails(t *testing.T) {
	rule := asset.NullCheck{Column: "customer_id", MaxRate: 0.0}
	eval := &fakeEval{
		table: "public.orders",
		fn: func(_ context.Context, _ string) (connector.AggregateRow, error) {
			return connector.AggregateRow{}, errors.New("connection refused")
		},
	}
	res, err := rule.Evaluate(context.Background(), eval)
	require.NoError(t, err) // Evaluate translates the error into a result.
	require.Equal(t, "error", res.Status)
	require.Contains(t, res.ErrorMessage, "connection refused")
}

func TestRangeCheck_PassesWithinBounds(t *testing.T) {
	rule := asset.RangeCheck{Column: "amount", Min: 0, Max: 1000}
	eval := &fakeEval{
		table: "public.orders",
		fn: func(_ context.Context, _ string) (connector.AggregateRow, error) {
			return connector.AggregateRow{Values: []any{1.0, 999.0}}, nil
		},
	}
	res, err := rule.Evaluate(context.Background(), eval)
	require.NoError(t, err)
	require.Equal(t, "passed", res.Status)
}

func TestRangeCheck_FailsBelowMin(t *testing.T) {
	rule := asset.RangeCheck{Column: "amount", Min: 0, Max: 1000}
	eval := &fakeEval{
		table: "public.orders",
		fn: func(_ context.Context, _ string) (connector.AggregateRow, error) {
			return connector.AggregateRow{Values: []any{-5.0, 999.0}}, nil
		},
	}
	res, err := rule.Evaluate(context.Background(), eval)
	require.NoError(t, err)
	require.Equal(t, "failed", res.Status)
}

func TestRangeCheck_FailsAboveMax(t *testing.T) {
	rule := asset.RangeCheck{Column: "amount", Min: 0, Max: 1000}
	eval := &fakeEval{
		table: "public.orders",
		fn: func(_ context.Context, _ string) (connector.AggregateRow, error) {
			return connector.AggregateRow{Values: []any{0.0, 1500.0}}, nil
		},
	}
	res, err := rule.Evaluate(context.Background(), eval)
	require.NoError(t, err)
	require.Equal(t, "failed", res.Status)
}

func TestSQLAssertion_ScalarEqualsZero_Pass_When0(t *testing.T) {
	rule := asset.SQLAssertion{
		Name_:     "no_dups",
		SQL:       `SELECT COUNT(*) - COUNT(DISTINCT order_id) FROM ${asset}`,
		Predicate: asset.ScalarEqualsZero{},
	}
	eval := &fakeEval{
		table: "public.orders",
		fn: func(_ context.Context, _ string) (connector.AggregateRow, error) {
			return connector.AggregateRow{Values: []any{int64(0)}}, nil
		},
	}
	res, err := rule.Evaluate(context.Background(), eval)
	require.NoError(t, err)
	require.Equal(t, "passed", res.Status)
}

func TestSQLAssertion_ScalarLessThan_Fail_WhenAbove(t *testing.T) {
	rule := asset.SQLAssertion{
		Name_:     "max_lag_under_60s",
		SQL:       `SELECT EXTRACT(EPOCH FROM (NOW() - MAX(created_at))) FROM ${asset}`,
		Predicate: asset.ScalarLessThan{N: 60},
	}
	eval := &fakeEval{
		table: "public.orders",
		fn: func(_ context.Context, _ string) (connector.AggregateRow, error) {
			return connector.AggregateRow{Values: []any{120.0}}, nil
		},
	}
	res, err := rule.Evaluate(context.Background(), eval)
	require.NoError(t, err)
	require.Equal(t, "failed", res.Status)
}

func TestSQLAssertion_RowCountIsZero_Pass_WhenEmpty(t *testing.T) {
	rule := asset.SQLAssertion{
		Name_:     "no_orphans",
		SQL:       `SELECT COUNT(*) FROM ${asset} WHERE customer_id IS NULL`,
		Predicate: asset.RowCountIsZero{},
	}
	eval := &fakeEval{
		table: "public.orders",
		fn: func(_ context.Context, _ string) (connector.AggregateRow, error) {
			return connector.AggregateRow{Values: []any{int64(0)}}, nil
		},
	}
	res, err := rule.Evaluate(context.Background(), eval)
	require.NoError(t, err)
	require.Equal(t, "passed", res.Status)
}

func TestSQLAssertion_AssetSubstitution(t *testing.T) {
	var capturedSQL string
	rule := asset.SQLAssertion{
		Name_:     "no_orphans",
		SQL:       `SELECT COUNT(*) FROM ${asset} WHERE x IS NULL`,
		Predicate: asset.RowCountIsZero{},
	}
	eval := &fakeEval{
		table: `"db"."schema"."orders"`,
		fn: func(_ context.Context, sqlText string) (connector.AggregateRow, error) {
			capturedSQL = sqlText
			return connector.AggregateRow{Values: []any{int64(0)}}, nil
		},
	}
	_, err := rule.Evaluate(context.Background(), eval)
	require.NoError(t, err)
	require.True(t, strings.Contains(capturedSQL, `"db"."schema"."orders"`),
		"expected ${asset} substitution; got: %s", capturedSQL)
	require.False(t, strings.Contains(capturedSQL, "${asset}"),
		"expected substitution to remove placeholder; got: %s", capturedSQL)
}
