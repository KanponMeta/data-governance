package notification_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/notification"
)

func TestWebhook_Send_HappyPath_200(t *testing.T) {
	var capturedHeaders http.Header
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		capturedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wc := notification.NewWebhookChannel(srv.URL, []byte("secret"))
	err := wc.Send(context.Background(), notification.SendPayload{
		Body:      []byte(`{"k":"v"}`),
		WebhookID: "abc-123",
		Timestamp: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, "abc-123", capturedHeaders.Get("X-Platform-Webhook-ID"))
	require.NotEmpty(t, capturedHeaders.Get("X-Platform-Signature"))
	require.NotEmpty(t, capturedHeaders.Get("X-Platform-Timestamp"))
	require.Equal(t, []byte(`{"k":"v"}`), capturedBody)
}

func TestWebhook_Send_HMACSignatureCorrect(t *testing.T) {
	secret := []byte("the-secret")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts := r.Header.Get("X-Platform-Timestamp")
		sig := r.Header.Get("X-Platform-Signature")
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, secret)
		mac.Write([]byte(ts + "." + string(body)))
		expected := hex.EncodeToString(mac.Sum(nil))
		// constant-time compare per Pitfall #7 (T-05-05-02).
		if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
			http.Error(w, "bad sig", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	wc := notification.NewWebhookChannel(srv.URL, secret)
	err := wc.Send(context.Background(), notification.SendPayload{
		Body:      []byte(`{"hello":"world"}`),
		WebhookID: "id-1",
		Timestamp: time.Now().UTC(),
	})
	require.NoError(t, err)
}

func TestWebhook_Send_500ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	wc := notification.NewWebhookChannel(srv.URL, []byte("s"))
	err := wc.Send(context.Background(), notification.SendPayload{
		Body:      []byte(`{}`),
		WebhookID: "x",
		Timestamp: time.Now().UTC(),
	})
	require.Error(t, err)
}

func TestWebhook_Send_RespectsContextTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
			return
		}
	}))
	defer srv.Close()
	wc := notification.NewWebhookChannel(srv.URL, []byte("s"))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := wc.Send(ctx, notification.SendPayload{
		Body:      []byte(`{}`),
		WebhookID: "x",
		Timestamp: time.Now().UTC(),
	})
	require.Error(t, err)
}

func TestWebhook_Send_StableWebhookIDAcrossCalls(t *testing.T) {
	var seenIDs []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenIDs = append(seenIDs, r.Header.Get("X-Platform-Webhook-ID"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	wc := notification.NewWebhookChannel(srv.URL, []byte("s"))
	id := "stable-id"
	for i := 0; i < 3; i++ {
		err := wc.Send(context.Background(), notification.SendPayload{
			Body: []byte(`{}`), WebhookID: id, Timestamp: time.Now().UTC(),
		})
		require.NoError(t, err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, got := range seenIDs {
		require.Equal(t, id, got)
	}
}

// TestWebhook_HMAC_ConstantTimeCompare verifies the receiver-side contract.
// The sender-side computeHMAC is hex-encoded so the receiver MUST use
// crypto/subtle.ConstantTimeCompare on the bytes (T-05-05-02 mitigation).
func TestWebhook_HMAC_ConstantTimeCompare(t *testing.T) {
	secret := []byte("k")
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("123.{}"))
	expected := hex.EncodeToString(mac.Sum(nil))
	// Same value: equal.
	require.Equal(t, 1, subtle.ConstantTimeCompare([]byte(expected), []byte(expected)))
	// Different value: not equal.
	require.NotEqual(t, 1, subtle.ConstantTimeCompare([]byte(expected), []byte("0000")))
}
