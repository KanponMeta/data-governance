package quality

import (
	"context"
	"database/sql"

	"github.com/google/uuid"

	"github.com/kanpon/data-governance/internal/notification"
)

// Dispatcher routes quality / SLA events to the notification queue. The
// underlying queue is abstracted through notification.JobInserter so this
// package compiles without depending on River directly.
//
// All Insert calls are made AFTER the executor's tx.Commit so a rolled-back
// transaction never produces a phantom notification. The InsertTx method on
// the queue mirrors River's surface but is implemented as Insert in the
// in-process queue (see notification/worker.go for migration notes).
type Dispatcher struct {
	queue notification.JobInserter
}

// NewDispatcher wires the queue used for OnQualityFailed / OnSLABreach.
func NewDispatcher(queue notification.JobInserter) *Dispatcher {
	return &Dispatcher{queue: queue}
}

// OnQualityFailed enqueues a quality.rule_failed notification job. tx is the
// executor's per-step tx — kept on the surface so a future River backend can
// use river.InsertTx for atomic enqueue. The in-process queue ignores tx and
// inserts immediately; callers MUST ensure tx.Commit() succeeds before the
// notification is consumed (River's InsertTx solves this; the in-process
// queue requires the caller to defer Insert until after Commit).
func (d *Dispatcher) OnQualityFailed(ctx context.Context, tx *sql.Tx, runID uuid.UUID, asset, rule string, payload map[string]any) error {
	if d == nil || d.queue == nil {
		return nil
	}
	args := notification.NotificationDispatchArgs{
		EventType: "quality.rule_failed",
		AssetName: asset,
		Payload:   payload,
		WebhookID: uuid.New().String(),
	}
	return d.queue.InsertTx(ctx, tx, args)
}

// OnSLABreach enqueues an sla.breached notification job. payload should at
// minimum carry max_lag_seconds and last_succeeded_at.
func (d *Dispatcher) OnSLABreach(ctx context.Context, tx *sql.Tx, asset string, payload map[string]any) error {
	if d == nil || d.queue == nil {
		return nil
	}
	args := notification.NotificationDispatchArgs{
		EventType: "sla.breached",
		AssetName: asset,
		Payload:   payload,
		WebhookID: uuid.New().String(),
	}
	return d.queue.InsertTx(ctx, tx, args)
}
