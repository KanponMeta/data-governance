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

// AssetMetadata stores runtime-override metadata for assets and their columns (D-17).
// This is an append-only history table: INSERT model — latest set_at wins on read
// (COALESCE logic: runtime override > code default from AssetVersion).
// column_name NULL = asset-level metadata; non-NULL = column-level metadata.
// RLS (SELECT + INSERT only for platform_app) enforced in SQL appendix (Task 2).
type AssetMetadata struct{ ent.Schema }

func (AssetMetadata) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "asset_metadata"}}
}

func (AssetMetadata) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("asset").NotEmpty().MaxLen(256).Immutable(),
		// column_name NULL means this is asset-level metadata.
		field.String("column_name").Optional().MaxLen(256).Immutable(),
		field.Text("description").Optional(),
		field.String("owner").Optional().MaxLen(256),
		field.JSON("tags", []string{}).Optional(),
		field.UUID("set_by", uuid.UUID{}).Immutable(),
		field.Time("set_at").Default(time.Now).Immutable(),
	}
}

func (AssetMetadata) Indexes() []ent.Index {
	return []ent.Index{
		// Read path for COALESCE: fetch latest metadata for an asset/column pair.
		index.Fields("asset", "column_name", "set_at"),
	}
}
