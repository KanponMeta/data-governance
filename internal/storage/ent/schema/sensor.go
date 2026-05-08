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

// Sensor is the Phase 3 D-05 sensor record. Each row represents a single
// user-defined polling sensor that may enqueue a run when its Sense(ctx)
// returns Fired=true. The sensor harness (plan 03-04) reads these rows in
// the same scheduler tick loop as Schedule; SELECT FOR UPDATE SKIP LOCKED
// makes multi-replica safe.
type Sensor struct{ ent.Schema }

func (Sensor) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "sensors"}}
}

func (Sensor) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("asset_name").NotEmpty().MaxLen(256).Immutable(),
		field.String("sensor_name").NotEmpty().MaxLen(128).Immutable(),
		// min_interval_seconds is the minimum poll interval in seconds. The
		// Go-side SensorSpec.MinInterval (time.Duration) is converted to
		// integer seconds at registration time.
		field.Int64("min_interval_seconds").Default(30),
		field.Time("last_evaluated_at").Optional().Nillable(),
		field.Time("last_fired_at").Optional().Nillable(),
		// last_run_key is the most recent SensorResult.RunKey that triggered
		// a fire (dedup layer 1 — D-07).
		field.String("last_run_key").Optional().MaxLen(256),
		// cooldown_until is dedup layer 2 — no-fire until this time, regardless
		// of RunKey. Defaults effectively to NULL (cooldown disabled) unless
		// SensorSpec.Cooldown is non-zero.
		field.Time("cooldown_until").Optional().Nillable(),
		// consecutive_failures increments on each Sense() error and resets on
		// the next successful evaluation (D-08). When the count reaches the
		// configured threshold, the harness sets disabled_at.
		field.Int("consecutive_failures").Default(0),
		// disabled_at: non-NULL means auto-disabled after N consecutive
		// failures (D-08). Operator must manually re-enable.
		field.Time("disabled_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Sensor) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("asset_name", "sensor_name"),
		// Tick scan: WHERE disabled_at IS NULL
		//   AND (last_evaluated_at IS NULL OR last_evaluated_at + min_interval <= NOW())
		index.Fields("disabled_at", "last_evaluated_at"),
	}
}
