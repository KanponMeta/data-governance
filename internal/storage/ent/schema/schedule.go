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

// Schedule is the Phase 3 D-02 schedule record. Each row represents a single
// cron-driven asset. The scheduler tick loop (plan 03-03) selects rows where
// next_fire_at <= NOW() AND paused_at IS NULL, fires the schedule (inserts
// a runs row with trigger='schedule'), and recomputes next_fire_at via
// robfig/cron/v3.
type Schedule struct{ ent.Schema }

func (Schedule) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "schedules"}}
}

func (Schedule) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("asset_name").NotEmpty().MaxLen(256).Immutable(),
		// cron_expr is parsed via robfig/cron/v3. Validation happens at
		// builder/registration time; the DB stores the raw expression.
		field.String("cron_expr").NotEmpty().MaxLen(128),
		// last_fire_at is NULL until the first fire. Tick loop uses this as
		// the start argument for sched.Next() when computing missed windows.
		field.Time("last_fire_at").Optional().Nillable(),
		// next_fire_at is precomputed by the recompute step after each fire,
		// indexed for the WHERE next_fire_at <= NOW() tick scan.
		field.Time("next_fire_at").Optional().Nillable(),
		// paused_at: non-NULL means paused. Phase 3 schema placeholder; the
		// pause/resume CLI is Phase 6 scope per D-02.
		field.Time("paused_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Schedule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("asset_name"),                // lookup by asset
		index.Fields("next_fire_at"),              // tick scan: WHERE next_fire_at <= NOW() AND paused_at IS NULL
		index.Fields("paused_at", "next_fire_at"), // pause-filtered tick scan
	}
}
