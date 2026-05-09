// Package notification implements Phase 5 Plan 05-05's notification subsystem:
// channels (webhook + SMTP), routing config (notifications.yaml), and a worker
// that consumes NotificationDispatchArgs jobs. Channels are deliberately
// minimal so users can plug in custom implementations without depending on
// the platform's choice of queue.
//
// Job queue abstraction (D-21 deviation):
//
// The plan specifies riverqueue/river as the job queue, but river is not yet
// a project dependency. The deviation chosen here is: define a JobInserter
// interface (Insert / InsertTx) that mirrors river.Client surface so a future
// PR can swap in a real river backend without changing call sites. The
// in-process default in worker.go is a buffered channel — adequate for the
// single-binary deployment target documented in CLAUDE.md.
package notification

import (
	"context"
	"time"
)

// SendPayload is the cross-channel envelope passed to Channel.Send. Body and
// Subject / BodyText / BodyHTML are pre-rendered by the worker before dispatch.
type SendPayload struct {
	EventType string
	AssetName string
	Vars      map[string]string
	Body      []byte // pre-rendered JSON for webhooks
	Subject   string // pre-rendered subject line for SMTP
	BodyText  string // pre-rendered text body for SMTP
	BodyHTML  string // optional HTML alternative for SMTP
	WebhookID string // stable across retries; used by webhook X-Platform-Webhook-ID header
	Timestamp time.Time
}

// Channel is the dispatch endpoint contract. Implementations MUST be safe
// for concurrent use — the worker may invoke Send from multiple goroutines.
type Channel interface {
	// Name returns a stable channel identifier ("webhook" | "smtp" | ...).
	Name() string
	// Send delivers payload through the channel. Errors are surfaced verbatim
	// so the worker can decide whether to retry.
	Send(ctx context.Context, p SendPayload) error
}
