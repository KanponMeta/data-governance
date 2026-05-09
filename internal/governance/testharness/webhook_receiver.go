package testharness

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Capture records a single webhook request received by the test server.
type Capture struct {
	Method     string
	Path       string
	Headers    http.Header
	Body       []byte
	ReceivedAt time.Time
}

// Receiver is an httptest.Server that buffers received webhook requests.
type Receiver struct {
	*httptest.Server
	mu       sync.Mutex
	captures []Capture
}

// NewWebhookReceiver starts an httptest.Server that records all requests.
func NewWebhookReceiver(t *testing.T) *Receiver {
	t.Helper()
	r := &Receiver{captures: []Capture{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		body := make([]byte, req.ContentLength)
		if req.ContentLength > 0 {
			if _, err := req.Body.Read(body); err != nil {
				// Read error — capture what we have.
			}
		}
		c := Capture{
			Method:     req.Method,
			Path:       req.URL.Path,
			Headers:    req.Header.Clone(),
			Body:       body,
			ReceivedAt: time.Now().UTC(),
		}
		r.mu.Lock()
		r.captures = append(r.captures, c)
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	r.Server = httptest.NewServer(mux)
	return r
}

// Captured returns a copy of all captured requests.
func (r *Receiver) Captured() []Capture {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Capture, len(r.captures))
	copy(out, r.captures)
	return out
}

// Reset clears all captured requests.
func (r *Receiver) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.captures = r.captures[:0]
}

// RespondWith sets a response code for the next request only.
// After the next request is handled, the response code resets to 200.
func (r *Receiver) RespondWith(status int) {
	// Not implemented in v1 — stub for future use.
	_ = status
}

// DeliverPayload sends a POST request to the receiver with the given payload.
func (r *Receiver) DeliverPayload(t *testing.T, payload any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("DeliverPayload: marshal: %v", err)
	}
	resp, err := http.Post(r.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("DeliverPayload: post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DeliverPayload: unexpected status %d", resp.StatusCode)
	}
}
