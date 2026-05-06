package storage_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/storage"
	"github.com/kanpon/data-governance/internal/storage/ent"
)

func openTestStorage(t *testing.T) (storage.Storage, func()) {
	t.Helper()
	dsn := os.Getenv("INTEGRATION_DATABASE_URL")
	if dsn == "" {
		t.Skip("INTEGRATION_DATABASE_URL not set; skipping")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := storage.NewPostgres(ctx, dsn)
	require.NoError(t, err)
	return store, func() { _ = store.Close() }
}

func TestPing(t *testing.T) {
	store, closeFn := openTestStorage(t)
	defer closeFn()
	require.NoError(t, store.Ping(context.Background()))
}

func TestEventLogIsAppendOnly(t *testing.T) {
	// This test must run as the platform_app role (DSN env supplies it).
	store, closeFn := openTestStorage(t)
	defer closeFn()
	ctx := context.Background()

	// Create an event_log row using ent (INSERT should succeed).
	created, err := store.Ent().EventLog.Create().
		SetEventType("test.append_only").
		SetResourceType("test").
		SetResourceID("rls-check-" + uuid.NewString()).
		SetPayload(map[string]any{"hello": "world"}).
		Save(ctx)
	require.NoError(t, err)

	// Attempt UPDATE via raw SQL — must be denied by RLS / revoked privilege.
	// ent doesn't generate setters for Immutable() fields, so we use raw SQL.
	_, err = store.DB().ExecContext(ctx, "UPDATE event_log SET event_type = 'tampered' WHERE id = $1", created.ID)
	require.Error(t, err, "UPDATE on event_log must be denied")
	require.True(t, strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), "42501"),
		"expected permission-denied error, got: %v", err)

	// Attempt DELETE via raw SQL — must be denied.
	_, err = store.DB().ExecContext(ctx, "DELETE FROM event_log WHERE id = $1", created.ID)
	require.Error(t, err, "DELETE on event_log must be denied")
	require.True(t, strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), "42501"),
		"expected permission-denied error, got: %v", err)
}

func TestWithTxRollsBackOnError(t *testing.T) {
	store, closeFn := openTestStorage(t)
	defer closeFn()

	sentinel := errors.New("rollback")
	err := store.WithTx(context.Background(), func(tx *ent.Tx) error {
		_, _ = tx.User.Create().SetEmail("rollback@example.com").SetPasswordHash("x").Save(context.Background())
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)
	// Verify no row persisted.
	count, err := store.Ent().User.Query().Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, count, "rolled back row must not persist")
}
