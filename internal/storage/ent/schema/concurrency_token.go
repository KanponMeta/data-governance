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

// ConcurrencyToken is the SINGLE source of truth for all concurrency limits (D-16).
// Run-level, op-level, and resource-level limits all check out / return tokens
// against this table. Three-layer hierarchies are REJECTED (PITFALLS §2).
type ConcurrencyToken struct{ ent.Schema }

func (ConcurrencyToken) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "concurrency_tokens"}}
}

func (ConcurrencyToken) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.UUID("run_id", uuid.UUID{}).Immutable(),
		field.String("asset_name").NotEmpty().MaxLen(256).Immutable(),
		// resource_tag identifies the resource being protected (e.g. "postgres-prod",
		// "global", "asset:users_clean"). The capacity for each tag is configured in
		// startup config; this table tracks ACQUIRED tokens.
		field.String("resource_tag").NotEmpty().MaxLen(128).Immutable(),
		field.Int("weight").Default(1).Immutable(),
		field.Time("acquired_at").Default(time.Now).Immutable(),
	}
}

func (ConcurrencyToken) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("resource_tag"),
		index.Fields("run_id"),
		index.Fields("acquired_at"),
	}
}
