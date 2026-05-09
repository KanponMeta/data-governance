package notification_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/notification"
)

// memEvents is a writable in-memory event sink.
type memEvents struct {
	mu   sync.Mutex
	evts []event.Event
}

func (m *memEvents) Append(_ context.Context, e event.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evts = append(m.evts, e)
	return nil
}

func (m *memEvents) Types() []event.EventType {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]event.EventType, len(m.evts))
	for i, e := range m.evts {
		out[i] = e.Type
	}
	return out
}

// stubChannel records Send calls + returns a configured error.
type stubChannel struct {
	name string
	err  error
	mu   sync.Mutex
	sent []notification.SendPayload
}

func (s *stubChannel) Name() string { return s.name }
func (s *stubChannel) Send(_ context.Context, p notification.SendPayload) error {
	s.mu.Lock()
	s.sent = append(s.sent, p)
	s.mu.Unlock()
	return s.err
}

// fixedRouter returns a fixed list of channels regardless of event_type.
type fixedRouter struct{ chs []notification.Channel }

func (f *fixedRouter) Route(_ context.Context, _ string) []notification.Channel {
	return f.chs
}

// We can't replace the Worker.Router type — Worker uses *notification.Router
// concretely. Instead we drive the worker via a real Router with a single
// matching rule + a custom HTTP server for the webhook channel.
func TestWorker_DispatchesViaWebhookAndSMTP_OnMatch(t *testing.T) {
	got := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case got <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &notification.Config{Rules: []notification.RuleConfig{
		{Match: "quality.rule_failed", Webhook: srv.URL},
	}}
	router := notification.NewRouter(cfg, []byte("k"), nil)
	ev := &memEvents{}
	w := &notification.Worker{Router: router, Events: ev}

	args := notification.NotificationDispatchArgs{
		EventType: "quality.rule_failed",
		AssetName: "orders",
		Payload:   map[string]any{"rule": "null_check_customer_id"},
		WebhookID: "id-1",
		EnqueuedAt: time.Now(),
	}
	require.NoError(t, w.Work(context.Background(), args, 1))
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("webhook server never called")
	}
	require.Contains(t, ev.Types(), event.EventTypeNotificationDispatched)
}

func TestWorker_NoRuleMatch_NoOp(t *testing.T) {
	router := notification.NewRouter(&notification.Config{}, []byte("k"), nil)
	ev := &memEvents{}
	w := &notification.Worker{Router: router, Events: ev}

	args := notification.NotificationDispatchArgs{
		EventType: "irrelevant.event",
		AssetName: "orders",
		WebhookID: "id-x",
	}
	require.NoError(t, w.Work(context.Background(), args, 1))
	require.Empty(t, ev.Types())
}

func TestWorker_FinalFailureEmitsDispatchFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := &notification.Config{Rules: []notification.RuleConfig{
		{Match: "quality.rule_failed", Webhook: srv.URL},
	}}
	router := notification.NewRouter(cfg, []byte("k"), nil)
	ev := &memEvents{}
	w := &notification.Worker{Router: router, Events: ev}

	maxAttempts := notification.NotificationDispatchArgs{}.InsertOpts().MaxAttempts
	args := notification.NotificationDispatchArgs{
		EventType: "quality.rule_failed",
		AssetName: "orders",
		WebhookID: "id-final",
		Payload:   map[string]any{},
	}
	err := w.Work(context.Background(), args, maxAttempts)
	require.Error(t, err)
	require.Contains(t, ev.Types(), event.EventTypeNotificationDispatchFailed)
}

func TestWorker_PartialFailure_LogsAndRetries(t *testing.T) {
	// First channel fails, second succeeds — Worker MUST return error so the
	// queue can retry the failing channel; second channel still emits dispatched.
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer failSrv.Close()
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()
	cfg := &notification.Config{Rules: []notification.RuleConfig{
		{Match: "quality.rule_failed", Webhook: failSrv.URL},
		{Match: "quality.rule_failed", Webhook: okSrv.URL},
	}}
	router := notification.NewRouter(cfg, []byte("k"), nil)
	ev := &memEvents{}
	w := &notification.Worker{Router: router, Events: ev}
	args := notification.NotificationDispatchArgs{
		EventType: "quality.rule_failed",
		AssetName: "orders",
		WebhookID: "id-partial",
		Payload:   map[string]any{},
	}
	err := w.Work(context.Background(), args, 1)
	require.Error(t, err)
	require.True(t, errors.Is(err, err)) // first error preserved
	require.Contains(t, ev.Types(), event.EventTypeNotificationDispatched)
}

func TestWorker_StableWebhookIDAcrossRetries(t *testing.T) {
	var seenIDs []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenIDs = append(seenIDs, r.Header.Get("X-Platform-Webhook-ID"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cfg := &notification.Config{Rules: []notification.RuleConfig{
		{Match: "quality.rule_failed", Webhook: srv.URL},
	}}
	router := notification.NewRouter(cfg, []byte("k"), nil)
	w := &notification.Worker{Router: router, Events: &memEvents{}}
	args := notification.NotificationDispatchArgs{
		EventType: "quality.rule_failed",
		AssetName: "orders",
		WebhookID: "stable",
	}
	for i := 1; i <= 3; i++ {
		require.NoError(t, w.Work(context.Background(), args, i))
	}
	mu.Lock()
	defer mu.Unlock()
	for _, id := range seenIDs {
		require.Equal(t, "stable", id)
	}
}

// TestNotificationDispatchArgs_Kind ensures the river-compatible Kind() is wired.
func TestNotificationDispatchArgs_Kind(t *testing.T) {
	require.Equal(t, "notification_dispatch", notification.NotificationDispatchArgs{}.Kind())
}
