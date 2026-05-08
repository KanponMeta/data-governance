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

type RunStep struct{ ent.Schema }

func (RunStep) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "run_steps"}}
}

func (RunStep) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.UUID("run_id", uuid.UUID{}).Immutable(),
		field.String("asset_name").NotEmpty().MaxLen(256).Immutable(),
		// state values: pending | running | succeeded | failed | skipped
		field.String("state").NotEmpty().MaxLen(16).Default("pending"),
		field.Int("attempt").Default(0),            // bumped per engine retry (plan 02-03)
		field.Int("topo_order").Default(0).Immutable(), // position in topological order
		field.Time("started_at").Optional().Nillable(),
		field.Time("finished_at").Optional().Nillable(),
		field.Int64("rows_written").Default(0),
		field.Text("error_message").Optional(),
		field.JSON("metadata", map[string]any{}).Optional(),
	}
}

func (RunStep) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("run_id", "topo_order"),
		index.Fields("run_id", "state"),
		index.Fields("asset_name"),
	}
}

// Ensure time import is used (started_at/finished_at fields).
var _ = (*time.Time)(nil)
