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

// GovernanceReview records a single review lifecycle row for the Phase 5
// Plan 05-04 governance workflow (D-08, D-12). One row per submission;
// re-submission of a rejected asset (same code_hash) inserts a NEW row in
// status='in_review' rather than mutating the closed row — preserves the
// audit trail.
//
// Mutability:
//   - status, decided_at, decided_by_id, comment, sla_breach_emitted_at are mutable.
//   - reviewer_pool_snapshot is rotated by the reassign CLI (D-09); the
//     SQL-level grant allows UPDATE so the workflow can rotate it.
//   - All other fields are Immutable() at the ent layer.
//
// Note: governance routing config (Reviewers / Quorum / RequireHumanReview /
// EscalationRoles) is captured at submission time into reviewer_pool_snapshot
// + quorum + escalation_roles columns so the review's resolved decision
// surface stays stable even when the builder DSL changes between submit and
// approve/reject (D-09 reviewer-snapshot semantics).
type GovernanceReview struct{ ent.Schema }

func (GovernanceReview) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "governance_reviews"}}
}

func (GovernanceReview) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.UUID("asset_version_id", uuid.UUID{}).Immutable(),
		field.String("asset").NotEmpty().MaxLen(255).Immutable(),
		field.String("code_hash").NotEmpty().MaxLen(64).Immutable(),
		field.UUID("submitter_id", uuid.UUID{}).Immutable(),
		field.Time("submitted_at").Default(time.Now).Immutable(),
		// reviewer_pool_snapshot is JSONB — rotated only via the reassign CLI.
		field.JSON("reviewer_pool_snapshot", map[string]any{}),
		field.Int("quorum").Default(1),
		field.Bool("require_human_review").Default(false),
		field.JSON("escalation_roles", []string{}).Optional(),
		// status: in_review | approved | rejected | auto_approved.
		field.String("status").NotEmpty().MaxLen(16).Default("in_review"),
		field.Time("decided_at").Optional().Nillable(),
		field.UUID("decided_by_id", uuid.UUID{}).Optional().Nillable(),
		field.Text("comment").Optional().Nillable(),
		field.Time("sla_breach_emitted_at").Optional().Nillable(),
	}
}

func (GovernanceReview) Indexes() []ent.Index {
	return []ent.Index{
		// Active reviews per asset (decided_at IS NULL) — see partial indices in SQL migration.
		index.Fields("asset"),
		index.Fields("submitted_at"),
		// One active review per asset_version (partial unique in SQL).
		index.Fields("asset_version_id"),
	}
}
