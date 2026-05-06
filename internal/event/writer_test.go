package event_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/storage"
	eventlogpred "github.com/kanpon/data-governance/internal/storage/ent/eventlog"
)

func openIntegration(t *testing.T) storage.Storage {
	t.Helper()
	dsn := os.Getenv("INTEGRATION_DATABASE_URL")
	if dsn == "" {
		t.Skip("INTEGRATION_DATABASE_URL not set; skipping")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := storage.NewPostgres(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestAppend_RejectsEmptyType(t *testing.T) {
	w := event.NewWriter(openIntegration(t))
	err := w.Append(context.Background(), event.Event{
		Type:         "",
		ResourceType: "test",
		ResourceID:   "x",
	})
	require.True(t, errors.Is(err, event.ErrInvalidEvent), "expected ErrInvalidEvent, got %v", err)
}

func TestAppend_RejectsUnknownType(t *testing.T) {
	w := event.NewWriter(openIntegration(t))
	err := w.Append(context.Background(), event.Event{
		Type:         "asset.materialized", // Phase 2 type, not allowed in Phase 1
		ResourceType: "test",
		ResourceID:   "x",
	})
	require.ErrorIs(t, err, event.ErrInvalidEvent)
}

func TestAppend_PersistsAndIsImmutable(t *testing.T) {
	store := openIntegration(t)
	w := event.NewWriter(store)
	ctx := context.Background()

	resID := "writer-test-" + time.Now().Format("150405.000")
	err := w.Append(ctx, event.Event{
		Type:         event.EventTypeUserRegistered,
		ResourceType: "user",
		ResourceID:   resID,
		Payload:      event.UserRegisteredPayload{UserID: resID, Email: "alice@example.com"},
	})
	require.NoError(t, err)

	row, err := store.Ent().EventLog.Query().
		Where(eventlogpred.ResourceIDEQ(resID)).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, "user.registered", row.EventType)
	require.Equal(t, "alice@example.com", row.Payload["email"])
}

func TestEventTypeStringsMatchD10(t *testing.T) {
	want := map[event.EventType]string{
		event.EventTypeUserRegistered:           "user.registered",
		event.EventTypeUserInvited:              "user.invited",
		event.EventTypeAuthLogin:                "auth.login",
		event.EventTypeAuthLogout:               "auth.logout",
		event.EventTypeAuthTokenExpired:         "auth.token_expired",
		event.EventTypePlatformStarted:          "platform.started",
		event.EventTypePlatformMigrationApplied: "platform.migration_applied",
	}
	for got, expect := range want {
		require.Equal(t, expect, string(got))
	}
}
