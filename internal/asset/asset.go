// Package asset provides the user-facing SDK for defining, registering, and
// executing data assets in the platform.
//
// The primary entry point is asset.New(name), which returns a Builder that
// accumulates asset configuration via chained method calls. Calling Register()
// commits the definition to the process-global DefinitionRegistry; Build() is
// the test-friendly alternative that returns the *Asset without registration.
//
// Stability commitment: The asset package is an external-facing SDK surface
// starting in Phase 2. Builder method names, AssetIO interface, and all exported
// types are treated as public API from this point onward.
package asset

import (
	"context"
	"time"

	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/partition"
)

// MaterializeFunc is the user-supplied transformation. AssetIO wraps connector
// calls (D-04), so user code does not import internal/connector directly.
type MaterializeFunc func(ctx context.Context, io AssetIO) (MaterializeResult, error)

// MaterializeResult is the return value from a Materialize call.
// RowsWritten is business-meaningful row count; Metadata is the Phase 3
// sensor Payload hook (D-04) — values are free-form.
// Phase 4 additions (additive — existing call sites with RowsWritten+Metadata still compile):
//   - ColumnLineage: runtime override for per-run column-level lineage (D-02); nil = use builder default.
//   - Schema: inline schema for connectors without SchemaDescriber (D-06 fallback); nil = use capability.
type MaterializeResult struct {
	RowsWritten   int64
	ColumnLineage ColumnLineageMap  // nil = use builder default (D-02 runtime override hook)
	Schema        *connector.Schema // nil = rely on SchemaDescriber capability (D-06 fallback)
	Metadata      map[string]any    // retained for sensor Payload coexistence (Phase 3 D-06)
}

// Resource attaches a named resource constraint with weight (D-16). Plan 02-03
// reads these when checking out tokens from the global concurrency_tokens table.
type Resource struct {
	Name   string // e.g. "postgres-prod"
	Weight int    // default 1; tokens consumed per acquisition
}

// ScheduleSpec is the user-facing cron schedule attached via Builder.Schedule (D-03, D-12).
// CronExpr accepts standard 5-field expressions plus robfig/cron/v3 descriptors
// (e.g., @every 30s, @hourly, @daily). TZ is optional; defaults to "UTC" when empty
// and affects only cron firing wall-clock alignment, not partition-key encoding.
type ScheduleSpec struct {
	CronExpr string
	TZ       string
}

// SensorResult is returned by SensorFunc to indicate whether the sensor fired (D-06).
// RunKey is the dedup key compared against sensors.last_run_key (layer 1 of D-07);
// if equal to the previous fire, no run is enqueued.
// Payload is a Phase 4 lineage hook — flows into MaterializeResult.Metadata of the
// triggered run (consistent with Phase 2 D-04 reasoning).
type SensorResult struct {
	Fired   bool
	RunKey  string
	Payload map[string]any
}

// SensorFunc is the user-supplied evaluation closure (D-06). It is called by the
// scheduler-daemon tick loop (plan 03-05) inside a per-sensor timeout.
type SensorFunc func(ctx context.Context) (SensorResult, error)

// SensorSpec attaches an event sensor to an asset via Builder.Sensor (D-06).
// MinInterval is the minimum gap between evaluation calls (defaulted to 30s by the
// daemon when zero). Cooldown is layer 2 of D-07's two-layer dedup; defaults to 0 (off).
type SensorSpec struct {
	Name        string
	MinInterval time.Duration
	Cooldown    time.Duration
	Sense       SensorFunc
}

// Asset is the immutable runtime representation of a user-defined asset.
// Construct only via asset.New(...).Build() or asset.New(...).Register().
// Builder writes; Asset reads — all fields are private, accessed via methods.
type Asset struct {
	name          string
	upstreams     []string
	connectorName string
	materializeFn MaterializeFunc
	retryPolicy   RetryPolicy
	resources     []Resource
	// Phase 3 additions (D-03, D-06, D-09, D-12):
	schedule   *ScheduleSpec
	sensors    []SensorSpec
	partitions partition.PartitionStrategy
	// Phase 4 additions (D-02, D-03, D-17):
	description   string
	owner         string
	tags          []string
	columns       []ColumnMeta
	columnLineage ColumnLineageMap
	codeHash      string // computed at Build()/Register() via fingerprint.go (D-03)
	// Phase 5 additions (D-02 / D-04 / RBAC-03):
	columnPolicies []ColumnPolicy // builder-default column-level masking declarations
	// Phase 5 additions (Plan 05-05):
	qualityRules []QualityRule
	freshnessSLA *FreshnessSLA
	// Phase 5 additions (Plan 05-04 — governance routing config; NOT in code_hash):
	reviewerRoles      []string
	quorum             Quorum
	requireHumanReview bool
	escalationRoles    []string
}

// Name returns the unique asset identifier.
func (a *Asset) Name() string { return a.name }

// Upstreams returns a defensive copy of the upstream asset name list.
// The DAG executor (plan 02-02) reads this to build the dependency graph.
func (a *Asset) Upstreams() []string { return append([]string(nil), a.upstreams...) }

// ConnectorName returns the connector name bound to this asset (D-03).
// Resolution happens at materialize-time via connector.Registry.Get(name).
func (a *Asset) ConnectorName() string { return a.connectorName }

// MaterializeFn returns the user-supplied transformation function (D-04).
func (a *Asset) MaterializeFn() MaterializeFunc { return a.materializeFn }

// RetryPolicy returns the per-asset retry configuration (D-15).
// IsZero() == true means the platform-level default applies.
func (a *Asset) RetryPolicy() RetryPolicy { return a.retryPolicy }

// Resources returns a defensive copy of the resource constraint list (D-16).
// Plan 02-03 uses these to acquire tokens from the concurrency token pool.
func (a *Asset) Resources() []Resource { return append([]Resource(nil), a.resources...) }

// Schedule returns the cron schedule attached via .Schedule(...). Nil if none (D-03).
func (a *Asset) Schedule() *ScheduleSpec { return a.schedule }

// Sensors returns a defensive copy of the attached SensorSpec list (D-06).
func (a *Asset) Sensors() []SensorSpec { return append([]SensorSpec(nil), a.sensors...) }

// Partitions returns the partition strategy attached via .Partitions(...) (D-09). Nil if none.
func (a *Asset) Partitions() partition.PartitionStrategy { return a.partitions }

// Phase 4 accessors (D-02, D-03, D-17) — used by Wave 4 lineage/schema writers and Wave 6 REST.

// Description returns the human-readable description declared via Builder.Description (D-17).
func (a *Asset) Description() string { return a.description }

// Owner returns the declared owner declared via Builder.Owner (D-17).
func (a *Asset) Owner() string { return a.owner }

// Tags returns a defensive copy of the declared tag set (D-17).
func (a *Asset) Tags() []string {
	if a.tags == nil {
		return nil
	}
	return append([]string(nil), a.tags...)
}

// Columns returns a defensive copy of the per-column metadata declarations (D-17).
func (a *Asset) Columns() []ColumnMeta {
	if a.columns == nil {
		return nil
	}
	return append([]ColumnMeta(nil), a.columns...)
}

// ColumnLineage returns a defensive deep copy of the builder-default column lineage map (D-02).
// Wave 4's lineage writer uses this as the default; MaterializeResult.ColumnLineage overrides per-run.
func (a *Asset) ColumnLineage() ColumnLineageMap {
	if a.columnLineage == nil {
		return nil
	}
	out := make(ColumnLineageMap, len(a.columnLineage))
	for k, v := range a.columnLineage {
		out[k] = append([]ColumnRef(nil), v...)
	}
	return out
}

// CodeHash returns the deterministic SHA-256 fingerprint of this asset's declaration (D-03).
// Populated by Build()/Register(); empty string if asset was constructed directly (test usage without Build).
func (a *Asset) CodeHash() string { return a.codeHash }

// ColumnPolicies returns a defensive deep copy of the builder-default column
// masking declarations (Phase 5 D-02 / RBAC-03). Phase 5 plan 05-02's
// internal/policy.Store.Apply consumes this at registration / capture time
// to write a column_policies row with source='builder'. Runtime PATCH
// overrides via internal/policy.Store.Patch take precedence on read.
func (a *Asset) ColumnPolicies() []ColumnPolicy {
	if a.columnPolicies == nil {
		return nil
	}
	out := make([]ColumnPolicy, len(a.columnPolicies))
	for i, p := range a.columnPolicies {
		cp := p
		if p.AllowRoles != nil {
			cp.AllowRoles = append([]string(nil), p.AllowRoles...)
		}
		out[i] = cp
	}
	return out
}

// QualityRules returns a defensive copy of the declared quality rules (Plan 05-05).
// Order matches declaration order on the Builder. Rule definitions ARE part of
// code_hash via fingerprint.go.
func (a *Asset) QualityRules() []QualityRule {
	if a.qualityRules == nil {
		return nil
	}
	return append([]QualityRule(nil), a.qualityRules...)
}

// FreshnessSLA returns the asset's freshness SLA, or nil if none declared.
// FreshnessSLA is operational config only — NOT in code_hash.
func (a *Asset) FreshnessSLA() *FreshnessSLA {
	if a.freshnessSLA == nil {
		return nil
	}
	cp := *a.freshnessSLA
	return &cp
}

// ===== Phase 5 Plan 05-04: Governance routing accessors =====
// All governance routing config below is intentionally EXCLUDED from
// fingerprint.go (D-12). It is approval-flow metadata, not data shape.

// ReviewerRoles returns a defensive copy of the declared reviewer role pool
// (Plan 05-04). Multiple Builder.Reviewers calls accumulate; nil = no
// builder-level reviewer roles (resolution falls back to YAML tag rules and
// finally team_owners by D-09 ResolveReviewers).
func (a *Asset) ReviewerRoles() []string {
	if a.reviewerRoles == nil {
		return nil
	}
	return append([]string(nil), a.reviewerRoles...)
}

// Quorum returns the declared approval quorum (Plan 05-04). Zero value means
// "use the platform default" — the workflow service treats 0 as Quorum1.
func (a *Asset) Quorum() Quorum { return a.quorum }

// RequireHumanReview reports whether the builder explicitly forced human
// review (Plan 05-04). When true, AutoApprovalChecker MUST return
// DecisionMustHumanReview even when all 5 checks pass.
func (a *Asset) RequireHumanReview() bool { return a.requireHumanReview }

// EscalationRoles returns the additional notification recipients on SLA
// breach (Plan 05-04 D-11). They do NOT auto-escalate the review state —
// SOC 2 compliance requires deliberate human action on every approval.
func (a *Asset) EscalationRoles() []string {
	if a.escalationRoles == nil {
		return nil
	}
	return append([]string(nil), a.escalationRoles...)
}
