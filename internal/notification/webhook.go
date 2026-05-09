package notification

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// DefaultWebhookTimeout caps each outbound webhook request (T-05-05-06 mitigation).
const DefaultWebhookTimeout = 30 * time.Second

// WebhookChannel POSTs the payload to URL with HMAC-SHA256 signature headers.
//
// Header contract (T-05-05-01..02 mitigations):
//   - X-Platform-Webhook-ID: stable across River retries (idempotency key —
//     receivers SHOULD dedup on this).
//   - X-Platform-Timestamp: Unix seconds when the dispatch attempt began.
//   - X-Platform-Signature: hex(HMAC-SHA256(Secret, timestamp + "." + body)).
//
// Receivers MUST validate the signature with crypto/subtle.ConstantTimeCompare
// (Pitfall #7 timing-attack mitigation) and SHOULD reject timestamps older
// than 5 minutes (replay protection).
type WebhookChannel struct {
	URL     string
	Secret  []byte
	Timeout time.Duration
	Client  *http.Client
}

// NewWebhookChannel constructs a WebhookChannel with sensible defaults.
func NewWebhookChannel(url string, secret []byte) *WebhookChannel {
	return &WebhookChannel{
		URL:     url,
		Secret:  secret,
		Timeout: DefaultWebhookTimeout,
		Client:  &http.Client{Timeout: DefaultWebhookTimeout},
	}
}

// Name implements Channel.
func (w *WebhookChannel) Name() string { return "webhook" }

// Send implements Channel.
func (w *WebhookChannel) Send(ctx context.Context, p SendPayload) error {
	if w.URL == "" {
		return fmt.Errorf("webhook: URL is empty")
	}
	if w.Client == nil {
		w.Client = &http.Client{Timeout: w.Timeout}
	}
	body := p.Body
	ts := strconv.FormatInt(p.Timestamp.Unix(), 10)
	sig := computeHMAC(w.Secret, []byte(ts+"."+string(body)))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Platform-Webhook-ID", p.WebhookID)
	req.Header.Set("X-Platform-Timestamp", ts)
	req.Header.Set("X-Platform-Signature", sig)

	resp, err := w.Client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: status %d", resp.StatusCode)
	}
	return nil
}

// computeHMAC returns the hex-encoded HMAC-SHA256 of msg using secret.
func computeHMAC(secret, msg []byte) string {
	h := hmac.New(sha256.New, secret)
	h.Write(msg)
	return hex.EncodeToString(h.Sum(nil))
}
