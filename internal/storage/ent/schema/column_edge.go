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

// ColumnEdge records a column→column lineage edge (D-13 split adjacency table).
// Edges are soft-retired (superseded_at set) when no longer present — never deleted (D-15).
// Partial indices WHERE superseded_at IS NULL and CHECK constraints are hand-managed
// in the SQL appendix (Task 2).
type ColumnEdge struct{ ent.Schema }

func (ColumnEdge) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "column_edges"}}
}

func (ColumnEdge) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("from_asset").NotEmpty().MaxLen(256).Immutable(),
		field.String("from_column").NotEmpty().MaxLen(256).Immutable(),
		field.String("to_asset").NotEmpty().MaxLen(256).Immutable(),
		field.String("to_column").NotEmpty().MaxLen(256).Immutable(),
		field.String("code_hash_first").NotEmpty().MaxLen(64).Immutable(),
		field.String("code_hash_latest").NotEmpty().MaxLen(64),
		field.UUID("first_seen_run_id", uuid.UUID{}).Immutable(),
		field.Time("first_seen_at").Default(time.Now).Immutable(),
		field.UUID("last_seen_run_id", uuid.UUID{}),
		field.Time("last_seen_at").Default(time.Now),
		field.Time("superseded_at").Optional().Nillable(),
		// partition_key is optional — partition-aware column lineage context (D-02).
		field.String("partition_key").Optional().MaxLen(128),
	}
}

func (ColumnEdge) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("from_asset", "from_column"),
		index.Fields("to_asset", "to_column"),
		// NOTE: partial indices WHERE superseded_at IS NULL (column_edges_active_from,
		// column_edges_active_to) are hand-managed in the SQL appendix (Task 2).
	}
}
