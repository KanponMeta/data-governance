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
		// Phase 3 D-10 — nullable partition key for partitioned assets. Non-partitioned
		// runs leave this NULL. Length 128 covers ISO weekly/monthly/daily and short
		// category keys with comfortable headroom.
		field.String("partition_key").Optional().MaxLen(128),
		// Phase 3 D-13 layer 1 — three-priority claim ordering. CHECK constraint
		// (priority IN ('critical','normal','backfill')) is appended in the
		// hand-managed SQL appendix because ent has no native CHECK support.
		field.String("priority").NotEmpty().MaxLen(16).Default("normal"),
		// Phase 3 D-15 — links a run row to the originating backfill submission.
		// NULL for non-backfill runs.
		field.UUID("backfill_id", uuid.UUID{}).Optional().Nillable(),
	}
}

func (Run) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("state", "queued_at"),      // claim path scans queued runs by enqueue time
		index.Fields("asset_name", "queued_at"),
		index.Fields("queued_at"),
		// Reaper scan path (plan 02-04): WHERE state IN ('starting','running') AND last_heartbeat < cutoff.
		index.Fields("state", "last_heartbeat"),
		// NOTE: Phase 3 D-10 partial unique index `run_partition_inflight_unique`
		// and the priority-aware claim index `run_state_priority_queued_at` are
		// hand-managed in the SQL appendix because ent does not support partial
		// (WHERE-clause) unique indexes.
	}
}
