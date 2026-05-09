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

// AssetVersion records the code-level state of an asset at each unique code_hash (D-17).
// One row per (asset, code_hash) pair — new code_hash → new row (append-only).
// drift_status is the only mutable field: clean|pending|acknowledged (D-04).
// All other fields are immutable — UPDATE grant exists in DB but ent Immutable()
// prevents app-layer mutation (T-04-02-06 defense in depth).
// UNIQUE(asset, code_hash) is enforced via the unique index below.
type AssetVersion struct{ ent.Schema }

func (AssetVersion) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "asset_versions"}}
}

func (AssetVersion) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("asset").NotEmpty().MaxLen(256).Immutable(),
		field.String("code_hash").NotEmpty().MaxLen(64).Immutable(),
		field.Text("description").Optional().Immutable(),
		field.String("owner").Optional().MaxLen(256).Immutable(),
		field.JSON("tags", []string{}).Optional().Immutable(),
		// column_lineage stores the declared column lineage at this code_hash snapshot.
		// Shape: map[dest_column][]{"asset":..., "column":...}
		field.JSON("column_lineage", map[string][]map[string]string{}).Optional().Immutable(),
		// drift_status: clean|pending|acknowledged (D-04). Mutable.
		// CHECK constraint (drift_status IN ('clean','pending','acknowledged')) in SQL appendix.
		field.String("drift_status").NotEmpty().MaxLen(16).Default("clean"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (AssetVersion) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("asset", "created_at"),
		// UNIQUE(asset, code_hash) — enforces one row per code snapshot per asset.
		index.Fields("asset", "code_hash").Unique(),
	}
}
