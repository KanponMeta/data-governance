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

type Run struct{ ent.Schema }

func (Run) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "runs"}}
}

func (Run) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("asset_name").NotEmpty().MaxLen(256).Immutable(),
		// state values: queued | starting | running | succeeded | failed | canceled
		// CHECK constraint added in the hand-managed SQL appendix.
		field.String("state").NotEmpty().MaxLen(16).Default("queued"),
		field.String("trigger").NotEmpty().MaxLen(32).Default("manual"), // manual | schedule | sensor (Phase 3)
		field.UUID("triggered_by", uuid.UUID{}).Optional().Nillable(),
		field.String("claimed_by").Optional().MaxLen(128),
		field.Time("queued_at").Default(time.Now).Immutable(),
		field.Time("claimed_at").Optional().Nillable(),
		field.Time("started_at").Optional().Nillable(),
		field.Time("finished_at").Optional().Nillable(),
		// last_heartbeat is set on claim and ticked by the executor (plan 02-03) every
		// ~30s while a step runs. Plan 02-04's stale-run reaper resets runs whose
		// last_heartbeat is older than 5m back to 'queued'. NULL when state='queued'.
		field.Time("last_heartbeat").Optional().Nillable(),
		field.Text("error_message").Optional(),
		field.JSON("metadata", map[string]any{}).Optional(),
	}
}

func (Run) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("state", "queued_at"),      // claim path scans queued runs by enqueue time
		index.Fields("asset_name", "queued_at"),
		index.Fields("queued_at"),
		// Reaper scan path (plan 02-04): WHERE state IN ('starting','running') AND last_heartbeat < cutoff.
		index.Fields("state", "last_heartbeat"),
	}
}
