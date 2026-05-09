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

// AssetEdge records an asset→asset lineage edge (D-13 split adjacency table).
// Edges are soft-retired (superseded_at set) when no longer present — never deleted (D-15).
// Partial indices WHERE superseded_at IS NULL and CHECK (from_asset != to_asset) are
// hand-managed in the SQL appendix (Task 2) because ent has no WHERE-clause index support.
type AssetEdge struct{ ent.Schema }

func (AssetEdge) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "asset_edges"}}
}

func (AssetEdge) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("from_asset").NotEmpty().MaxLen(256).Immutable(),
		field.String("to_asset").NotEmpty().MaxLen(256).Immutable(),
		field.String("code_hash_first").NotEmpty().MaxLen(64).Immutable(),
		field.String("code_hash_latest").NotEmpty().MaxLen(64),
		field.UUID("first_seen_run_id", uuid.UUID{}).Immutable(),
		field.Time("first_seen_at").Default(time.Now).Immutable(),
		field.UUID("last_seen_run_id", uuid.UUID{}),
		field.Time("last_seen_at").Default(time.Now),
		field.Time("superseded_at").Optional().Nillable(),
	}
}

func (AssetEdge) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("from_asset"),
		index.Fields("to_asset"),
		// NOTE: partial indices WHERE superseded_at IS NULL (asset_edges_active_from,
		// asset_edges_active_to) are hand-managed in the SQL appendix (Task 2).
	}
}
