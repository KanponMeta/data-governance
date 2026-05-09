package quality_test

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/quality"
)

// TestDispatcher_OnQualityFailed_EnqueuesJob verifies the dispatcher routes a
// quality.rule_failed event into the queue.
func TestDispatcher_OnQualityFailed_EnqueuesJob(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectCommit()

	q := &stubQueue{}
	d := quality.NewDispatcher(q)

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, d.OnQualityFailed(ctx, tx, uuid.New(), "orders", "null_check_customer_id", map[string]any{"x": 1}))
	require.NoError(t, tx.Commit())
	require.Len(t, q.insert, 1)
	require.Equal(t, "quality.rule_failed", q.insert[0].EventType)
	require.Equal(t, "orders", q.insert[0].AssetName)
	require.NotEmpty(t, q.insert[0].WebhookID, "WebhookID must be assigned for idempotency")
}

// TestDispatcher_OnSLABreach_EnqueuesJob verifies the dispatcher routes an
// sla.breached event into the queue.
func TestDispatcher_OnSLABreach_EnqueuesJob(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectCommit()

	q := &stubQueue{}
	d := quality.NewDispatcher(q)

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, d.OnSLABreach(ctx, tx, "orders", map[string]any{"max_lag_seconds": 3600}))
	require.NoError(t, tx.Commit())
	require.Len(t, q.insert, 1)
	require.Equal(t, "sla.breached", q.insert[0].EventType)
}

// TestDispatcher_NilQueue_NoOp ensures a nil queue is safe (defensive — used
// in test fixtures that don't wire the notification subsystem).
func TestDispatcher_NilQueue_NoOp(t *testing.T) {
	d := quality.NewDispatcher(nil)
	require.NoError(t, d.OnQualityFailed(context.Background(), nil, uuid.New(), "x", "y", nil))
	require.NoError(t, d.OnSLABreach(context.Background(), nil, "x", nil))
}
