package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

type EventLog struct{ ent.Schema }

func (EventLog) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.Time("occurred_at").Default(time.Now).Immutable(),
		field.String("event_type").NotEmpty().MaxLen(64).Immutable(),
		field.UUID("actor_id", uuid.UUID{}).Optional().Nillable().Immutable(),
		field.String("resource_type").NotEmpty().MaxLen(64).Immutable(),
		field.String("resource_id").NotEmpty().MaxLen(128).Immutable(),
		field.JSON("payload", map[string]any{}).Optional().Immutable(),
	}
}

func (EventLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_type", "occurred_at"),
		index.Fields("resource_type", "resource_id"),
		index.Fields("occurred_at"),
	}
}
