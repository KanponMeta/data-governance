package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// SchemaChange records each diff between consecutive SchemaVersion rows (D-09/D-11).
// Rows are append-only: only acknowledged_at/by/reason may be set after insert (D-10).
// DB-level: REVOKE DELETE/TRUNCATE in SQL appendix; column-update restriction via app-layer ent mutation.
// change_type CHECK constraint is hand-managed in SQL appendix (Task 2).
type SchemaChange struct{ ent.Schema }

func (SchemaChange) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "schema_changes"}}
}

func (SchemaChange) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("asset").NotEmpty().MaxLen(256).Immutable(),
		field.UUID("run_id", uuid.UUID{}).Immutable(),
		field.String("code_hash").NotEmpty().MaxLen(64).Immutable(),
		// prev_version_id is NULL for the first schema capture (no predecessor).
		field.UUID("prev_version_id", uuid.UUID{}).Optional().Nillable().Immutable(),
		field.UUID("new_version_id", uuid.UUID{}).Immutable(),
		// change_type: column_added|column_dropped|type_narrowed|type_widened|
		//   nullable_added|nullable_removed|pk_changed|comment_changed|default_changed.
		// CHECK constraint enforced in SQL appendix (Task 2).
		field.String("change_type").NotEmpty().MaxLen(32).Immutable(),
		field.String("column_name").Optional().MaxLen(256).Immutable(),
		field.String("prev_type").Optional().MaxLen(64).Immutable(),
		field.String("new_type").Optional().MaxLen(64).Immutable(),
		field.Bool("prev_nullable").Optional().Nillable().Immutable(),
		field.Bool("new_nullable").Optional().Nillable().Immutable(),
		field.Bool("is_breaking").Default(false).Immutable(),
		field.Time("observed_at").Default(time.Now).Immutable(),
		// Acknowledgement fields — the only mutable columns post-insert (D-10).
		field.Time("acknowledged_at").Optional().Nillable(),
		field.UUID("acknowledged_by", uuid.UUID{}).Optional().Nillable(),
		field.Text("acknowledgement_reason").Optional(),
	}
}

func (SchemaChange) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("asset", "observed_at"),
		// D-12 per-column timeline query path.
		index.Fields("asset", "column_name", "observed_at"),
		// Phase 5 alerting filter: find unacknowledged breaking changes.
		index.Fields("acknowledged_at"),
	}
}
