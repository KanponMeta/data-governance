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

// SchemaVersion stores full Schema snapshots deduplicated by schema_hash (D-08/D-11).
// When hash matches the latest row, only last_seen_* is updated. When hash changes,
// a new row is inserted and a SchemaChange row is created.
type SchemaVersion struct{ ent.Schema }

func (SchemaVersion) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "schema_versions"}}
}

func (SchemaVersion) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("asset").NotEmpty().MaxLen(256).Immutable(),
		field.String("code_hash").NotEmpty().MaxLen(64).Immutable(),
		field.String("schema_hash").NotEmpty().MaxLen(64).Immutable(),
		// schema_data stores the full Schema JSON snapshot (connector.Schema serialized).
		// JSONB for efficient nested access via Postgres operators.
		field.JSON("schema_data", map[string]any{}).Immutable(),
		field.Time("captured_at").Default(time.Now).Immutable(),
		field.Time("last_seen_at").Default(time.Now),
		field.UUID("last_seen_run_id", uuid.UUID{}),
	}
}

func (SchemaVersion) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("asset", "captured_at"),
		// schema_hash index: not unique at DB level because two different assets may
		// produce the same hash by coincidence. App-layer dedup uses (asset, schema_hash).
		index.Fields("schema_hash"),
	}
}
