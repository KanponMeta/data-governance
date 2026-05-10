package notification

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/kanpon/data-governance/internal/event"
)

// NotificationDispatchArgs is the job payload consumed by Worker (and the same
// shape that a future River backend would marshal into river.Insert).
//
// Kind() returns "notification_dispatch" so the surface matches river.JobArgs.
// InsertOpts() encodes the retry + uniqueness contract:
//   - MaxAttempts=5 caps webhook storms (T-05-05-06).
//   - UniqueByArgs+ByPeriod=1m dedups identical events within a one-minute
//     window so a flaky upstream cannot trigger thousands of identical
//     notifications.
type NotificationDispatchArgs struct {
	EventType   string         `json:"event_type"`
	AssetName   string         `json:"asset_name"`
	Payload     map[string]any `json:"payload"`
	Recipients  []string       `json:"recipients,omitempty"`
	WebhookID   string         `json:"webhook_id"` // stable across retries
	EnqueuedAt  time.Time      `json:"enqueued_at"`
}

// Kind implements the river.JobArgs surface. Returning the string literal is
// the contract — receivers may switch on this to dispatch by type.
func (NotificationDispatchArgs) Kind() string { return "notification_dispatch" }

// JobInsertOpts mirrors river.InsertOpts — kept locally so callers don't need
// to import river. A real river backend can map this struct to river.InsertOpts
// 1:1 in a single adapter file.
type JobInsertOpts struct {
	MaxAttempts int
	UniqueByArgs bool
	UniquePeriod time.Duration
}

// InsertOpts encodes the retry + uniqueness contract (T-05-05-06 + Pitfall #9).
func (NotificationDispatchArgs) InsertOpts() JobInsertOpts {
	return JobInsertOpts{
		MaxAttempts:  5,
		UniqueByArgs: true,
		UniquePeriod: 1 * time.Minute,
	}
}

// JobInserter is the queue surface a producer (dispatcher) needs. Mirrors
// river.Client.InsertTx so a future swap to River is a one-file adapter.
type JobInserter interface {
	// Insert enqueues a job at top-level (no transaction).
	Insert(ctx context.Context, args NotificationDispatchArgs) error
	// InsertTx enqueues a job atomically with the supplied tx.
	InsertTx(ctx context.Context, tx *sql.Tx, args NotificationDispatchArgs) error
}

// JobConsumer is the inverse — Worker satisfies this surface so an external
// orchestrator can pump jobs into Worker.Work without owning the queue itself.
type JobConsumer interface {
	Work(ctx context.Context, args NotificationDispatchArgs, attempt int) error
}

// JobRecord is the wire form a consumer sees. attempt counts retries (1-indexed).
type JobRecord struct {
	Args       NotificationDispatchArgs
	Attempt    int
	MaxAttempts int
}

// Worker consumes NotificationDispatchArgs jobs. It resolves channels via
// Router and dispatches the payload to each.
//
// Permanent failure path (Plan 05-05 D-21):
// when attempt >= MaxAttempts and at least one channel returned an error,
// the worker emits notification.dispatch_failed event_log + slog.Error.
type Worker struct {
	Router *Router
	Events event.Writer
	DB     *sql.DB // optional — used to emit notification.dispatched events; nil = best-effort post-tx writes
	mu     sync.Mutex
}

// Work implements JobConsumer. attempt is the 1-indexed attempt counter; the
// caller (queue) decides whether to re-enqueue on error.
func (w *Worker) Work(ctx context.Context, args NotificationDispatchArgs, attempt int) error {
	channels := w.Router.Route(ctx, args.EventType)
	if len(channels) == 0 {
		return nil // no rule matched — silent OK
	}
	body, err := json.Marshal(args.Payload)
	if err != nil {
		body = []byte(`{}`)
	}
	emailTo := w.Router.EmailToFor(args.EventType)
	vars := map[string]string{
		"asset":      args.AssetName,
		"event_type": args.EventType,
		"recipient":  RenderTemplate(emailTo, flattenStringValues(args.Payload)),
	}

	subject := fmt.Sprintf("[%s] %s", args.EventType, args.AssetName)
	bodyText := RenderTemplate(
		"Event {event_type} for asset {asset}.",
		vars,
	)

	payload := SendPayload{
		EventType: args.EventType,
		AssetName: args.AssetName,
		Vars:      vars,
		Body:      body,
		Subject:   subject,
		BodyText:  bodyText,
		WebhookID: args.WebhookID,
		Timestamp: time.Now().UTC(),
	}

	var firstErr error
	for _, ch := range channels {
		if err := ch.Send(ctx, payload); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			slog.Error("notification.send", "channel", ch.Name(), "event", args.EventType, "attempt", attempt, "err", err)
			continue
		}
		_ = w.Events.Append(ctx, event.Event{
			Type:         event.EventTypeNotificationDispatched,
			ResourceType: "notification",
			ResourceID:   args.WebhookID,
			Payload: event.NotificationDispatchedPayload{
				Channel:   ch.Name(),
				EventType: args.EventType,
				Asset:     args.AssetName,
			},
		})
	}

	maxAttempts := NotificationDispatchArgs{}.InsertOpts().MaxAttempts
	if firstErr != nil && attempt >= maxAttempts {
		_ = w.Events.Append(ctx, event.Event{
			Type:         event.EventTypeNotificationDispatchFailed,
			ResourceType: "notification",
			ResourceID:   args.WebhookID,
			Payload: event.NotificationDispatchFailedPayload{
				EventType: args.EventType,
				Asset:     args.AssetName,
				Error:     firstErr.Error(),
			},
		})
	}
	return firstErr
}

// flattenStringValues collects string-valued payload entries so RenderTemplate
// can substitute them. Non-string values are skipped.
func flattenStringValues(payload map[string]any) map[string]string {
	out := make(map[string]string, len(payload))
	for k, v := range payload {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// InProcessQueue is a minimal JobInserter / runner suitable for the
// single-binary deployment target. Replace with a River adapter when the
// project adopts riverqueue.
type InProcessQueue struct {
	consumer JobConsumer
	jobs     chan jobEnvelope
}

type jobEnvelope struct {
	args    NotificationDispatchArgs
	attempt int
	maxAttempts int
}

// NewInProcessQueue starts a queue with `workers` background dispatcher
// goroutines. Cancel ctx to drain.
func NewInProcessQueue(ctx context.Context, consumer JobConsumer, workers int, buffer int) *InProcessQueue {
	if workers <= 0 {
		workers = 1
	}
	if buffer <= 0 {
		buffer = 256
	}
	q := &InProcessQueue{
		consumer: consumer,
		jobs:     make(chan jobEnvelope, buffer),
	}
	for i := 0; i < workers; i++ {
		go q.worker(ctx)
	}
	return q
}

func (q *InProcessQueue) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case env, ok := <-q.jobs:
			if !ok {
				return
			}
			err := q.consumer.Work(ctx, env.args, env.attempt)
			if err != nil && env.attempt < env.maxAttempts {
				// Simple exponential backoff: 100ms * attempt^2 (cap at 30s).
				delay := time.Duration(env.attempt*env.attempt) * 100 * time.Millisecond
				if delay > 30*time.Second {
					delay = 30 * time.Second
				}
				go func(env jobEnvelope) {
					select {
					case <-ctx.Done():
					case <-time.After(delay):
						q.jobs <- jobEnvelope{args: env.args, attempt: env.attempt + 1, maxAttempts: env.maxAttempts}
					}
				}(env)
			}
		}
	}
}

// Insert enqueues a job non-transactionally (current implementation does not
// persist to DB — lost on process restart). For durability, swap in River.
func (q *InProcessQueue) Insert(ctx context.Context, args NotificationDispatchArgs) error {
	if args.WebhookID == "" {
		args.WebhookID = uuid.New().String()
	}
	args.EnqueuedAt = time.Now().UTC()
	max := args.InsertOpts().MaxAttempts
	select {
	case <-ctx.Done():
		return ctx.Err()
	case q.jobs <- jobEnvelope{args: args, attempt: 1, maxAttempts: max}:
		return nil
	}
}

// InsertTx mirrors river.Client.InsertTx for source-compatibility, but is
// **non-transactional** in the in-process queue: the supplied tx is IGNORED
// and the job is enqueued immediately. If tx subsequently rolls back, the
// notification will still fire — producing a phantom notification for a
// transition that never persisted (CR-04).
//
// Callers that require atomic enqueue MUST instead build their args before
// commit and call Insert AFTER tx.Commit() succeeds (see
// internal/governance/workflow.go, internal/governance/sla_scanner.go, and
// internal/quality/freshness.go for the post-commit pattern).
//
// This method is retained so the JobInserter surface stays compatible with
// the eventual River backend, where InsertTx will honour the tx natively.
func (q *InProcessQueue) InsertTx(ctx context.Context, _ *sql.Tx, args NotificationDispatchArgs) error {
	return q.Insert(ctx, args)
}
