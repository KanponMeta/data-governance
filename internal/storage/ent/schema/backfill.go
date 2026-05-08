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

// Backfill is the Phase 3 D-15 backfill submission record. Each row tracks one
// operator-submitted historical materialization. Submission inserts every
// computed partition row into runs with priority='backfill' and a shared
// backfill_id. Status is queryable via:
//
//	SELECT state, count(*) FROM runs WHERE backfill_id=$1 GROUP BY state
//
// The status field is updated by the backfill aggregator goroutine when all
// partition runs reach terminal state.
type Backfill struct{ ent.Schema }

func (Backfill) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "backfills"}}
}

func (Backfill) Fields() []ent.Field {
	return []ent.Field{
		// id IS the backfill_id surfaced in CLI output and in
		// runs.backfill_id. Default uuid.New keeps it operator-copy/paste-able.
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("asset_name").NotEmpty().MaxLen(256),
		// partition_spec is the raw user-supplied partitions string for
		// auditability ("2024-01-01:2024-12-31", "us,eu,apac", "2024-01-15").
		field.String("partition_spec").NotEmpty().MaxLen(1024),
		// status: submitted | running | succeeded | failed | partially_failed
		// (free-form string for now; a CHECK is appended in the migration
		// when CLI semantics in plan 03-07 land.)
		field.String("status").MaxLen(16).Default("submitted"),
		// total_partitions is the count of run rows created on submission.
		field.Int("total_partitions").Default(0),
		field.Time("submitted_at").Default(time.Now).Immutable(),
		field.Time("completed_at").Optional().Nillable(),
	}
}

func (Backfill) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("asset_name"),
		index.Fields("status", "submitted_at"),
	}
}
