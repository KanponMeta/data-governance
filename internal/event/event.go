package event

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Event is the canonical append-only log record. Once Append succeeds, the
// row is immutable at the database layer (see migration RLS).
type Event struct {
	Type         EventType
	OccurredAt   time.Time
	ActorID      *uuid.UUID
	ResourceType string
	ResourceID   string
	Payload      any
}

// Writer is the append-only event log API.
type Writer interface {
	Append(ctx context.Context, evt Event) error
}

// TxWriter optionally extends Writer with a transactional Append. Implementations
// that participate in caller-driven *sql.Tx flows should implement this so the
// event row commits atomically with the data mutation. Phase 5 quality evaluator
// writes events through the same tx as quality_results for atomicity.
type TxWriter interface {
	Writer
	AppendTx(ctx context.Context, tx interface{}, evt Event) error
}
