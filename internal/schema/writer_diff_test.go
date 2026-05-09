//go:build !integration

package schema_test

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteSchemaChangesEmpty(t *testing.T) {
	// Empty changes slice → returns empty IDs, nil error, no DB calls.
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	// Expect Begin() since we call db.Begin() in the test.
	mock.ExpectBegin()
	tx, err := db.Begin()
	require.NoError(t, err)

	// No INSERT expectations set — WriteSchemaChanges must not call ExecContext.
	ids, err := schema.WriteSchemaChanges(context.Background(), tx, uuid.New(),
		"my_asset", "codehash123",
		nil, uuid.New(), nil)

	require.NoError(t, err)
	assert.Empty(t, ids, "empty changes should return empty IDs")

	mock.ExpectRollback()
	_ = tx.Rollback()
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWriteSchemaChangesNilChanges(t *testing.T) {
	// Nil changes slice → same as empty (returns nil, nil).
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	require.NoError(t, err)

	ids, err := schema.WriteSchemaChanges(context.Background(), tx, uuid.New(),
		"asset", "code", nil, uuid.New(), nil)

	require.NoError(t, err)
	assert.Nil(t, ids, "nil changes should return nil IDs")

	mock.ExpectRollback()
	_ = tx.Rollback()
	require.NoError(t, mock.ExpectationsWereMet())
}
