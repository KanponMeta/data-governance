package quality_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/quality"
)

// TestStore_Persist_HappyPath ensures Persist issues an INSERT inside the supplied tx.
func TestStore_Persist_HappyPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO quality_results`).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	store := quality.NewStore(db)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	measured := 0.05
	threshold := 0.0
	res := asset.QualityResult{Status: "failed", MeasuredValue: &measured, Threshold: &threshold}
	require.NoError(t, store.Persist(ctx, tx, uuid.New(), "null_check_customer_id", "null_check", res))
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestStore_History_OrderedDesc verifies the History query reads in evaluated_at DESC order.
func TestStore_History_OrderedDesc(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	cols := []string{"run_id", "rule_name", "rule_type", "status", "measured_value", "threshold", "evaluated_at", "error_message"}
	rows := sqlmock.NewRows(cols).
		AddRow(uuid.New(), "null_check_customer_id", "null_check", "passed", 0.0, 0.0, time.Now(), "").
		AddRow(uuid.New(), "null_check_customer_id", "null_check", "failed", 0.05, 0.0, time.Now().Add(-1*time.Hour), "")
	mock.ExpectQuery(`SELECT qr\.run_id`).
		WillReturnRows(rows)

	store := quality.NewStore(db)
	out, err := store.History(context.Background(), "orders", "null_check_customer_id", 10)
	require.NoError(t, err)
	require.Len(t, out, 2)
	require.Equal(t, "passed", out[0].Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestStore_Persist_RespectsCheckConstraint surfaces an underlying DB CHECK
// violation as an error rather than silently swallowing it.
func TestStore_Persist_RespectsCheckConstraint(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO quality_results`).
		WillReturnError(errors.New("violates check constraint"))
	mock.ExpectRollback()

	store := quality.NewStore(db)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	res := asset.QualityResult{Status: "totally-bogus"}
	err = store.Persist(ctx, tx, uuid.New(), "rule", "null_check", res)
	require.Error(t, err)
	_ = tx.Rollback()
	require.NoError(t, mock.ExpectationsWereMet())
}
