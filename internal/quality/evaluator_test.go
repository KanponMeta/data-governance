package quality_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/quality"
)

// memEvents is an in-memory event.Writer that also implements EventTxAppender
// so the evaluator can verify both code paths.
type memEvents struct {
	mu     sync.Mutex
	events []event.Event
}

func (m *memEvents) Append(_ context.Context, evt event.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, evt)
	return nil
}

func (m *memEvents) AppendTx(_ context.Context, _ *sql.Tx, evt event.Event) error {
	return m.Append(context.Background(), evt)
}

func (m *memEvents) Types() []event.EventType {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]event.EventType, len(m.events))
	for i, e := range m.events {
		out[i] = e.Type
	}
	return out
}

// fakeQAConn satisfies connector.Connector + connector.QueryAggregate.
type fakeQAConn struct {
	rowFn func(ctx context.Context, ref connector.AssetRef, sqlText string) (connector.AggregateRow, error)
}

func (f *fakeQAConn) APIVersion() string { return connector.APIVersion }
func (f *fakeQAConn) Ping(_ context.Context, _ connector.PingRequest) (connector.PingResponse, error) {
	return connector.PingResponse{}, nil
}
func (f *fakeQAConn) Schema(_ context.Context, _ connector.SchemaRequest) (connector.SchemaResponse, error) {
	return connector.SchemaResponse{}, nil
}
func (f *fakeQAConn) Read(_ context.Context, _ connector.ReadRequest) (connector.ReadResponse, error) {
	return connector.ReadResponse{}, nil
}
func (f *fakeQAConn) Write(_ context.Context, _ connector.WriteRequest) (connector.WriteResponse, error) {
	return connector.WriteResponse{}, nil
}
func (f *fakeQAConn) QueryAggregate(ctx context.Context, ref connector.AssetRef, sqlText string) (connector.AggregateRow, error) {
	return f.rowFn(ctx, ref, sqlText)
}

// nonAggConn intentionally does NOT implement connector.QueryAggregate.
type nonAggConn struct{ fakeQAConn }

func (n *nonAggConn) QueryAggregate() {} // shadow with wrong signature so the type assert fails

// We can't shadow with wrong signature directly, so make a fresh struct.
type plainConn struct{}

func (plainConn) APIVersion() string { return connector.APIVersion }
func (plainConn) Ping(_ context.Context, _ connector.PingRequest) (connector.PingResponse, error) {
	return connector.PingResponse{}, nil
}
func (plainConn) Schema(_ context.Context, _ connector.SchemaRequest) (connector.SchemaResponse, error) {
	return connector.SchemaResponse{}, nil
}
func (plainConn) Read(_ context.Context, _ connector.ReadRequest) (connector.ReadResponse, error) {
	return connector.ReadResponse{}, nil
}
func (plainConn) Write(_ context.Context, _ connector.WriteRequest) (connector.WriteResponse, error) {
	return connector.WriteResponse{}, nil
}

// helper builds an Asset with the supplied rules.
func mustBuildAsset(t *testing.T, name string, rules ...asset.QualityRule) *asset.Asset {
	t.Helper()
	b := asset.New(name).Connector("pg").Materialize(func(_ context.Context, _ asset.AssetIO) (asset.MaterializeResult, error) {
		return asset.MaterializeResult{}, nil
	})
	for _, r := range rules {
		b = b.QualityRule(r)
	}
	a, err := b.Build()
	require.NoError(t, err)
	return a
}

func TestEvaluator_NoRules_SetsSkipped(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE runs SET run_quality_status='skipped'`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	store := quality.NewStore(db)
	ev := &memEvents{}
	eval := quality.NewEvaluator(ev, store, 0)
	a := mustBuildAsset(t, "orders")

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	worst, err := eval.Evaluate(ctx, tx, uuid.New(), a, &fakeQAConn{rowFn: func(_ context.Context, _ connector.AssetRef, _ string) (connector.AggregateRow, error) {
		return connector.AggregateRow{}, nil
	}}, connector.AssetRef{Identifier: "public.orders"})
	require.NoError(t, err)
	require.Equal(t, "skipped", worst)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEvaluator_PassingNullCheck_SetsPassed(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO quality_results`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE runs SET run_quality_status=\$1`).
		WithArgs("passed", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	store := quality.NewStore(db)
	ev := &memEvents{}
	eval := quality.NewEvaluator(ev, store, 0)
	a := mustBuildAsset(t, "orders", asset.NullCheck{Column: "customer_id", MaxRate: 0.5})
	conn := &fakeQAConn{rowFn: func(_ context.Context, _ connector.AssetRef, _ string) (connector.AggregateRow, error) {
		return connector.AggregateRow{Values: []any{100.0, 0.0}}, nil
	}}

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	worst, err := eval.Evaluate(ctx, tx, uuid.New(), a, conn, connector.AssetRef{Identifier: "public.orders"})
	require.NoError(t, err)
	require.Equal(t, "passed", worst)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEvaluator_FailingNullCheck_SetsFailed_RunStateStillSucceeded(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO quality_results`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE runs SET run_quality_status=\$1`).
		WithArgs("failed", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	store := quality.NewStore(db)
	ev := &memEvents{}
	eval := quality.NewEvaluator(ev, store, 0)
	a := mustBuildAsset(t, "orders", asset.NullCheck{Column: "customer_id", MaxRate: 0.0})
	conn := &fakeQAConn{rowFn: func(_ context.Context, _ connector.AssetRef, _ string) (connector.AggregateRow, error) {
		return connector.AggregateRow{Values: []any{100.0, 5.0}}, nil
	}}

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	worst, err := eval.Evaluate(ctx, tx, uuid.New(), a, conn, connector.AssetRef{Identifier: "public.orders"})
	require.NoError(t, err)
	require.Equal(t, "failed", worst)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
	// quality.rule_failed event must have been emitted.
	require.Contains(t, ev.Types(), event.EventTypeQualityRuleFailed)
}

func TestEvaluator_NonAggregateConnector_SetsError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO quality_results`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE runs SET run_quality_status=\$1`).
		WithArgs("error", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	store := quality.NewStore(db)
	ev := &memEvents{}
	eval := quality.NewEvaluator(ev, store, 0)
	a := mustBuildAsset(t, "orders", asset.NullCheck{Column: "customer_id", MaxRate: 0.0})

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	worst, err := eval.Evaluate(ctx, tx, uuid.New(), a, plainConn{}, connector.AssetRef{Identifier: "public.orders"})
	require.NoError(t, err)
	require.Equal(t, "error", worst)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}
