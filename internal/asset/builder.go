package asset

import (
	"errors"
	"fmt"

	"github.com/robfig/cron/v3"

	"github.com/kanpon/data-governance/internal/partition"
)

var (
	// ErrMissingMaterialize is returned by Build/Register when Materialize(fn)
	// was not called on the builder before committing the asset definition.
	ErrMissingMaterialize = errors.New("asset: Materialize(fn) is required before Register/Build")

	// ErrInvalidColumnRef is returned by Build/Register when a ColumnRef in the
	// builder-default ColumnLineageMap has an empty Asset or Column (T-04-03-05).
	ErrInvalidColumnRef = errors.New("asset: ColumnRef must have non-empty Asset and Column")

	// ErrMissingConnector is returned by Build/Register when Connector(name)
	// was not called on the builder before committing the asset definition.
	ErrMissingConnector = errors.New("asset: Connector(name) is required before Register/Build")

	// ErrEmptyName is returned by Build/Register when New("") was called with
	// an empty name string.
	ErrEmptyName = errors.New("asset: New(name) requires non-empty name")

	// ErrInvalidCron is returned by Build/Register when ScheduleSpec.CronExpr
	// fails to parse via robfig/cron/v3 (D-03, T-03-02-01 mitigation, Pitfall 1).
	ErrInvalidCron = errors.New("asset: invalid cron expression")

	// ErrSensorNameRequired is returned when SensorSpec.Name is empty (D-06).
	ErrSensorNameRequired = errors.New("asset: SensorSpec.Name is required")

	// ErrSensorFuncRequired is returned when SensorSpec.Sense is nil (D-06).
	ErrSensorFuncRequired = errors.New("asset: SensorSpec.Sense is required")

	// ErrSensorMinIntervalNegative is returned when MinInterval < 0
	// (T-03-02-03 — guards against a busy-loop sensor evaluation).
	ErrSensorMinIntervalNegative = errors.New("asset: SensorSpec.MinInterval must be ≥ 0")

	// ErrPartitionInvalidKey is returned when a CategoryPartitions key fails
	// the validation rules in partition.ValidateCategoryKey (Pitfall 4).
	ErrPartitionInvalidKey = errors.New("asset: CategoryPartitions key invalid")

	// ErrColumnPolicyInvalidMask is returned when ColumnPolicy is called with
	// a Mask that is not one of MaskHash / MaskRedact / MaskPartial (Phase 5 D-04).
	ErrColumnPolicyInvalidMask = errors.New("asset: ColumnPolicy.Mask invalid")

	// ErrColumnPolicyMissingColumn is returned when ColumnPolicy is called with
	// an empty Column name (Phase 5 D-04).
	ErrColumnPolicyMissingColumn = errors.New("asset: ColumnPolicy.Column required")

	// ErrColumnPolicyDuplicateColumn is returned by Build()/Register() when
	// the same Column appears in two ColumnPolicy declarations on one asset.
	ErrColumnPolicyDuplicateColumn = errors.New("asset: ColumnPolicy duplicate column")
	// ErrQualityRuleNameDuplicate is returned by Build() when two QualityRules
	// share the same Name() (Plan 05-05). Names must be unique per-asset so
	// quality_results rows + emitted events can disambiguate.
	ErrQualityRuleNameDuplicate = errors.New("asset: duplicate QualityRule name")

	// ErrFreshnessSLAInvalid is returned when FreshnessSLA.MaxLag <= 0.
	ErrFreshnessSLAInvalid = errors.New("asset: FreshnessSLA.MaxLag must be > 0")
)

// cronParser is initialised once and reused. Parser-only usage per D-03 — the
// in-process Cron runner from robfig/cron/v3 is NEVER instantiated; the Phase 3
// scheduler daemon (plan 03-05) uses the parser plus a Postgres-coordinated
// SELECT FOR UPDATE SKIP LOCKED tick loop instead.
var cronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// Builder accumulates configuration before Register() commits to the global
// registry. Construct only via New(name). All methods return *Builder for
// chaining (D-01). Method order is irrelevant — only Build()/Register() validate.
type Builder struct {
	a    *Asset
	errs []error // deferred errors collected by chainable setters; surface at Build().
}

// New starts a new Asset definition. Name must be non-empty and unique within
// the registry when Register() is eventually called.
func New(name string) *Builder {
	return &Builder{a: &Asset{name: name}}
}

// Upstream appends one or more upstream asset names (variadic per D-01).
// The DAG executor (plan 02-02) reads Asset.Upstreams() to build the
// dependency graph. Calling Upstream multiple times is cumulative.
func (b *Builder) Upstream(names ...string) *Builder {
	b.a.upstreams = append(b.a.upstreams, names...)
	return b
}

// Connector binds the asset to a connector by name (D-03). Resolution happens
// at materialize time via connector.Registry.Get(name). The name must match
// a connector registered in the startup config.
func (b *Builder) Connector(name string) *Builder {
	b.a.connectorName = name
	return b
}

// Materialize registers the user transformation function (D-04 signature).
// The function receives an AssetIO that hides connector calls from user code.
func (b *Builder) Materialize(fn MaterializeFunc) *Builder {
	b.a.materializeFn = fn
	return b
}

// Retry overrides the platform default retry policy for this asset (D-15).
// When the policy IsZero() == true the engine applies the platform-level default.
func (b *Builder) Retry(p RetryPolicy) *Builder {
	b.a.retryPolicy = p
	return b
}

// Resource attaches a named resource constraint (D-16). Weight defaults to 1
// if zero or negative — plan 02-03 checks out this many tokens per acquisition.
func (b *Builder) Resource(name string, weight int) *Builder {
	if weight <= 0 {
		weight = 1
	}
	b.a.resources = append(b.a.resources, Resource{Name: name, Weight: weight})
	return b
}

// Schedule attaches a cron expression to the asset (ORCH-05, D-03, D-12).
// Validation is deferred to Build()/Register() — invalid expressions surface
// there, ensuring fail-fast semantics before any scheduler daemon ever sees
// the bad expression (Pitfall 1).
func (b *Builder) Schedule(cronExpr string) *Builder {
	b.a.schedule = &ScheduleSpec{CronExpr: cronExpr}
	return b
}

// Sensor appends a SensorSpec to the asset (ORCH-06, D-06, D-12).
// Multiple .Sensor calls are cumulative; each spec produces an independent
// evaluation row in the sensors table (plan 03-04).
func (b *Builder) Sensor(spec SensorSpec) *Builder {
	b.a.sensors = append(b.a.sensors, spec)
	return b
}

// Partitions attaches a partition strategy (ORCH-07/08, D-09, D-12).
// At most one strategy per asset — successive calls overwrite (last wins).
// Validation of category keys is deferred to Build()/Register() (Pitfall 4).
func (b *Builder) Partitions(strategy partition.PartitionStrategy) *Builder {
	b.a.partitions = strategy
	return b
}

// ---- Phase 4 additions (D-02, D-17) ----

// Description sets the asset's human-readable description (Phase 4 D-17).
// Last call wins — multiple calls overwrite, not append.
func (b *Builder) Description(desc string) *Builder {
	b.a.description = desc
	return b
}

// Owner sets the asset's declared owner (Phase 4 D-17).
func (b *Builder) Owner(owner string) *Builder {
	b.a.owner = owner
	return b
}

// Tags replaces the asset's declared tag set with the supplied values (Phase 4 D-17).
// Variadic — pass all tags in one call. Defensive copy prevents caller mutation.
func (b *Builder) Tags(tags ...string) *Builder {
	if tags == nil {
		b.a.tags = nil
		return b
	}
	b.a.tags = append([]string(nil), tags...)
	return b
}

// ColumnPolicy declares a column-level masking policy (Phase 5 D-02 / D-04 / RBAC-03).
// Multiple ColumnPolicy calls accumulate; declaring the same Column twice is a
// Build()/Register() error (ErrColumnPolicyDuplicateColumn). Per Phase 5 D-02
// the (sorted) ColumnPolicies slice is part of the asset's code_hash, so a
// builder mask change forces a new asset_versions row.
//
// Validation of Mask + Column non-emptiness is fail-fast per chained call
// (collected on the builder and surfaced at Build()) rather than panicking,
// matching the deferred-error model used by Schedule/Sensor.
func (b *Builder) ColumnPolicy(p ColumnPolicy) *Builder {
	if !p.Mask.IsValid() {
		b.errs = append(b.errs, fmt.Errorf("%w: %q (asset %q column %q)",
			ErrColumnPolicyInvalidMask, p.Mask, b.a.name, p.Column))
		return b
	}
	if p.Column == "" {
		b.errs = append(b.errs, fmt.Errorf("%w (asset %q)",
			ErrColumnPolicyMissingColumn, b.a.name))
		return b
	}
	// Defensive copy of AllowRoles so caller mutation post-call cannot
	// silently change the fingerprint input.
	cp := p
	if p.AllowRoles != nil {
		cp.AllowRoles = append([]string(nil), p.AllowRoles...)
	}
	b.a.columnPolicies = append(b.a.columnPolicies, cp)
	return b
}

// ColumnLineage sets the builder-default column-level lineage (Phase 4 D-02).
// Runtime MaterializeResult.ColumnLineage overrides this per-run.
// Defensive deep copy.
func (b *Builder) ColumnLineage(cl ColumnLineageMap) *Builder {
	if cl == nil {
		b.a.columnLineage = nil
		return b
	}
	cp := make(ColumnLineageMap, len(cl))
	for k, refs := range cl {
		cp[k] = append([]ColumnRef(nil), refs...)
	}
	b.a.columnLineage = cp
	return b
}

// Column starts a column-level metadata declaration chain (Phase 4 D-17).
// Returns a *ColumnBuilder that scopes subsequent Description/Tags calls
// to the named column. End the chain with .And() to return to *Builder.
func (b *Builder) Column(name string) *ColumnBuilder {
	return &ColumnBuilder{parent: b, name: name}
}

// ColumnBuilder provides fluent column-level metadata declaration (D-17).
// Construct via Builder.Column(name); end the chain with And() to return to *Builder.
type ColumnBuilder struct {
	parent *Builder
	name   string
	desc   string
	tags   []string
}

// Description sets the column's description.
func (cb *ColumnBuilder) Description(desc string) *ColumnBuilder {
	cb.desc = desc
	return cb
}

// Tags replaces the column's tag list. Defensive copy.
func (cb *ColumnBuilder) Tags(tags ...string) *ColumnBuilder {
	cb.tags = append([]string(nil), tags...)
	return cb
}

// And finalizes the column declaration and returns to the parent Builder chain.
func (cb *ColumnBuilder) And() *Builder {
	cb.parent.a.columns = append(cb.parent.a.columns, ColumnMeta{
		Name:        cb.name,
		Description: cb.desc,
		Tags:        cb.tags,
	})
	return cb.parent
}

// ---- Phase 5 Plan 05-05 additions: QualityRule + FreshnessSLA ----

// QualityRule appends a quality rule to the asset definition (Plan 05-05 D-18).
// Rules are evaluated by the executor in commitSuccess (same tx as lineage and
// schema capture). Duplicate Name() across rules fails Build() / Register().
//
// QualityRule definitions ARE included in the asset's code_hash via
// fingerprint.go: changing a rule reseats the asset version (D-08 governance reset).
func (b *Builder) QualityRule(r QualityRule) *Builder {
	b.a.qualityRules = append(b.a.qualityRules, r)
	return b
}

// FreshnessSLA attaches a freshness SLA to the asset (Plan 05-05 D-20).
// MaxLag must be > 0 — validated at Build() time.
//
// Operational config only — NOT included in code_hash.
func (b *Builder) FreshnessSLA(s FreshnessSLA) *Builder {
	cp := s
	b.a.freshnessSLA = &cp
	return b
}

// Build validates the accumulated configuration and returns the *Asset WITHOUT
// committing it to the process-global Default() registry.
//
// This is the test-friendly construction path: plan 02-02 DAG tests build
// assets via Build() so they can construct in-test dependency graphs without
// polluting the global singleton across test cases. Production code paths use
// Register() instead.
//
// Returns (nil, error) when:
//   - name is empty (ErrEmptyName)
//   - Materialize was not called (ErrMissingMaterialize)
//   - Connector was not called (ErrMissingConnector)
//   - Schedule cron expression fails to parse (ErrInvalidCron, D-03 / Pitfall 1)
//   - SensorSpec.Name empty / Sense nil / MinInterval negative
//   - CategoryPartitions key fails ValidateCategoryKey (Pitfall 4)
func (b *Builder) Build() (*Asset, error) {
	if b.a.name == "" {
		return nil, fmt.Errorf("%w", ErrEmptyName)
	}
	if b.a.materializeFn == nil {
		return nil, fmt.Errorf("%w (asset %q)", ErrMissingMaterialize, b.a.name)
	}
	if b.a.connectorName == "" {
		return nil, fmt.Errorf("%w (asset %q)", ErrMissingConnector, b.a.name)
	}

	// Phase 3 validation — defer to Build() so existing error semantics for
	// Phase 2 paths are preserved (cron / sensor / category checks come last).
	if b.a.schedule != nil {
		if _, err := cronParser.Parse(b.a.schedule.CronExpr); err != nil {
			return nil, fmt.Errorf("%w: %q: %v (asset %q)",
				ErrInvalidCron, b.a.schedule.CronExpr, err, b.a.name)
		}
	}
	for _, s := range b.a.sensors {
		if s.Name == "" {
			return nil, fmt.Errorf("%w (asset %q)", ErrSensorNameRequired, b.a.name)
		}
		if s.Sense == nil {
			return nil, fmt.Errorf("%w (asset %q sensor %q)",
				ErrSensorFuncRequired, b.a.name, s.Name)
		}
		if s.MinInterval < 0 {
			return nil, fmt.Errorf("%w (asset %q sensor %q): %s",
				ErrSensorMinIntervalNegative, b.a.name, s.Name, s.MinInterval)
		}
	}
	if cp, ok := b.a.partitions.(partition.CategoryPartitions); ok {
		for _, k := range cp.Keys {
			if err := partition.ValidateCategoryKey(k); err != nil {
				return nil, fmt.Errorf("%w: %v (asset %q)",
					ErrPartitionInvalidKey, err, b.a.name)
			}
		}
	}
	// Phase 4 validation (T-04-03-05): each ColumnRef must have non-empty Asset and Column.
	for outCol, refs := range b.a.columnLineage {
		for _, ref := range refs {
			if ref.Asset == "" || ref.Column == "" {
				return nil, fmt.Errorf("%w: output column %q has ref with empty Asset or Column (asset %q)",
					ErrInvalidColumnRef, outCol, b.a.name)
			}
		}
	}
	// Phase 5 validation (D-04): surface any deferred ColumnPolicy errors and
	// reject duplicate Column declarations.
	if len(b.errs) > 0 {
		// Surface the first deferred error — typical Go pattern (the
		// chained call sites are reported via the wrapped error context).
		return nil, b.errs[0]
	}
	if len(b.a.columnPolicies) > 0 {
		seen := make(map[string]struct{}, len(b.a.columnPolicies))
		for _, p := range b.a.columnPolicies {
			if _, dup := seen[p.Column]; dup {
				return nil, fmt.Errorf("%w: column %q (asset %q)",
					ErrColumnPolicyDuplicateColumn, p.Column, b.a.name)
			}
			seen[p.Column] = struct{}{}
		}
	}
	// Phase 5 Plan 05-05 validation: unique QualityRule names + non-zero FreshnessSLA.
	seenRule := make(map[string]struct{}, len(b.a.qualityRules))
	for _, r := range b.a.qualityRules {
		if _, dup := seenRule[r.Name()]; dup {
			return nil, fmt.Errorf("%w: %q (asset %q)",
				ErrQualityRuleNameDuplicate, r.Name(), b.a.name)
		}
		seenRule[r.Name()] = struct{}{}
	}
	if b.a.freshnessSLA != nil && b.a.freshnessSLA.MaxLag <= 0 {
		return nil, fmt.Errorf("%w (asset %q): %s",
			ErrFreshnessSLAInvalid, b.a.name, b.a.freshnessSLA.MaxLag)
	}
	// D-03: compute deterministic code hash at Build() time; stored on Asset.codeHash.
	// Both Build() (test path) and Register() (production path) get the hash set here.
	// Invalid assets (returned before this line) never get a hash.
	b.a.codeHash = ComputeCodeHash(b.a)
	return b.a, nil
}

// Register validates and commits to the process-global Default() registry.
// It delegates validation to Build() so the contract is identical: any chain
// that Build() accepts, Register() accepts, and vice versa.
//
// Returns ErrAlreadyRegistered if an asset with the same name was already
// committed (no silent overwrite, per T-02-01-01 threat mitigation).
func (b *Builder) Register() error {
	a, err := b.Build()
	if err != nil {
		return err
	}
	return Default().Register(a)
}
