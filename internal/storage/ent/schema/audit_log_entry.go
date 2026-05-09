package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema"
	"github.com/google/uuid"
)

// AuditLogEntry is the ent schema for reading audit.audit_log entries.
// Writes go through the raw SQL hash-chain writer (internal/audit/writer.go)
// which is the single writer for audit_log.
type AuditLogEntry struct {
	ent.Schema
}

func (AuditLogEntry) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Schema: "audit", Table: "audit_log"},
	}
}

func (AuditLogEntry) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("seq").Immutable(),
		field.Bytes("prev_hash").Immutable(),
		field.Bytes("self_hash").Immutable(),
		field.Time("occurred_at").Immutable(),
		field.String("event_type").Immutable(),
		field.UUID("actor_id", uuid.UUID{}).Optional().Nillable(),
		field.String("resource_type").Immutable(),
		field.String("resource_id").Immutable(),
		field.JSON("payload", []byte{}).Immutable(),
		field.Time("expires_at").Optional().Nillable(),
	}
}
