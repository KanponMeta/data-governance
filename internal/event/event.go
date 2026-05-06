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
