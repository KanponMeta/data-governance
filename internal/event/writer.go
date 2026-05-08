package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kanpon/data-governance/internal/storage"
)

// ErrInvalidEvent is returned when a caller submits a malformed event.
var ErrInvalidEvent = errors.New("event: invalid event")

type writer struct {
	store storage.Storage
}

// NewWriter returns a Writer that appends events through the supplied Storage.
func NewWriter(store storage.Storage) Writer {
	return &writer{store: store}
}

// Append validates the event and inserts it into event_log. The DB will reject
// any subsequent UPDATE/DELETE on the row (see migration RLS).
func (w *writer) Append(ctx context.Context, evt Event) error {
	if err := evt.validate(); err != nil {
		return err
	}
	if evt.OccurredAt.IsZero() {
		evt.OccurredAt = time.Now().UTC()
	}

	payload := map[string]any{}
	if evt.Payload != nil {
		raw, err := json.Marshal(evt.Payload)
		if err != nil {
			return fmt.Errorf("%w: marshal payload: %v", ErrInvalidEvent, err)
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return fmt.Errorf("%w: re-decode payload: %v", ErrInvalidEvent, err)
		}
	}

	create := w.store.Ent().EventLog.Create().
		SetEventType(string(evt.Type)).
		SetOccurredAt(evt.OccurredAt).
		SetResourceType(evt.ResourceType).
		SetResourceID(evt.ResourceID).
		SetPayload(payload)
	if evt.ActorID != nil {
		create = create.SetActorID(*evt.ActorID)
	}
	if _, err := create.Save(ctx); err != nil {
		return fmt.Errorf("event: append failed: %w", err)
	}
	return nil
}

func (e Event) validate() error {
	if e.Type == "" {
		return fmt.Errorf("%w: type is required", ErrInvalidEvent)
	}
	known := false
	for _, t := range AllKnownTypes() {
		if t == e.Type {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("%w: unknown event_type %q", ErrInvalidEvent, e.Type)
	}
	if e.ResourceType == "" {
		return fmt.Errorf("%w: resource_type is required", ErrInvalidEvent)
	}
	if e.ResourceID == "" {
		return fmt.Errorf("%w: resource_id is required", ErrInvalidEvent)
	}
	return nil
}
