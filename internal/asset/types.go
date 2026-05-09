package asset

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kanpon/data-governance/internal/connector"
)

// ColumnRef declares a column-level lineage source (D-02).
type ColumnRef struct {
	Asset  string `json:"asset"`
	Column string `json:"column"`
}

// ColumnLineageMap maps output column name → source column references.
// Wave 4's lineage writer reads this from the builder default OR
// MaterializeResult.ColumnLineage (runtime override wins per D-02).
type ColumnLineageMap map[string][]ColumnRef

// ColumnMeta is the per-column declaration produced by Builder.Column(name).
type ColumnMeta struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// ---- Phase 5 additions (D-02, D-04, D-07; RBAC-03/04) ----

// MaskType aliases the connector mask enum so user code can write
// asset.MaskHash directly without importing internal/connector.
type MaskType = connector.MaskType

const (
	MaskHash    = connector.MaskHash
	MaskRedact  = connector.MaskRedact
	MaskPartial = connector.MaskPartial
)

// ColumnPolicy is the user-facing column-level masking declaration attached
// via Builder.ColumnPolicy. Reason is populated by the REST PATCH path
// (runtime overrides) — for builder defaults it is the empty string.
//
// Per Phase 5 D-02 the (sorted) ColumnPolicies slice is part of the asset's
// code_hash, so changing a builder mask creates a new asset_versions row.
type ColumnPolicy struct {
	Column        string             `json:"column"`
	Mask          connector.MaskType `json:"mask"`
	AllowRoles    []string           `json:"allow_roles,omitempty"`
	Reason        string             `json:"reason,omitempty"`
	PartialReveal int                `json:"partial_reveal,omitempty"`
}

// ===== Phase 5 Plan 05-05: Quality + Freshness types =====

// QualityRule is the user-facing interface every quality rule implements.
// The three concrete implementations (NullCheck, RangeCheck, SQLAssertion)
// are exported below. Custom user rules MAY implement this interface; the
// rule definitions ARE included in the asset's code_hash (D-08 governance
// reset semantics) so changing a rule reseats the asset version.
type QualityRule interface {
	// Name returns the canonical rule name. Must be unique within an asset.
	Name() string
	// Type returns one of "null_check" | "range_check" | "sql_assertion".
	Type() string
	// Evaluate runs the rule against the supplied evaluator and returns the
	// per-rule result. Implementations MUST NOT panic on bad data: errors
	// belong in QualityResult{Status:"error",ErrorMessage:...}.
	Evaluate(ctx context.Context, eval QualityEvaluator) (QualityResult, error)
	// ConfigJSON returns a deterministic byte-encoding of the rule's
	// configuration for inclusion in code_hash + persistence to quality_rules.
	ConfigJSON() ([]byte, error)
}

// QualityEvaluator is the connector-side interface a QualityRule uses to
// execute its assertion SQL. The evaluator package supplies a concrete
// implementation that wraps a connector.QueryAggregate plus the asset's
// fully-qualified table reference.
type QualityEvaluator interface {
	// QueryAggregate executes the supplied SQL and returns a single result row.
	// Implementations MUST apply a strict context timeout (Pitfall #10).
	QueryAggregate(ctx context.Context, sqlText string) (connector.AggregateRow, error)
	// AssetTable returns the fully-qualified table reference for ${asset}
	// substitution inside SQLAssertion bodies.
	AssetTable() string
	// Timeout returns the per-rule default timeout the caller passed in.
	Timeout() time.Duration
}

// QualityResult is the output of one rule evaluation. Status is the canonical
// outcome ("passed" | "failed" | "error"); MeasuredValue / Threshold are
// optional metadata persisted to quality_results for trend analysis (Phase 6).
type QualityResult struct {
	Status        string
	MeasuredValue *float64
	Threshold     *float64
	ErrorMessage  string
}

// AssertionPredicate evaluates the scalar returned by an SQLAssertion.
// v1 implementations: ScalarEqualsZero, ScalarLessThan, RowCountIsZero.
type AssertionPredicate interface {
	// Test returns (passed, measured). measured is informational only.
	Test(value any) (passed bool, measured *float64)
}

// ScalarEqualsZero passes when the scalar equals zero (numeric coercion).
type ScalarEqualsZero struct{}

// Test implements AssertionPredicate.
func (ScalarEqualsZero) Test(value any) (bool, *float64) {
	v, ok := numericFloat(value)
	if !ok {
		return false, nil
	}
	pass := v == 0
	return pass, &v
}

// ScalarLessThan passes when the scalar is strictly less than N.
type ScalarLessThan struct{ N float64 }

// Test implements AssertionPredicate.
func (s ScalarLessThan) Test(value any) (bool, *float64) {
	v, ok := numericFloat(value)
	if !ok {
		return false, nil
	}
	pass := v < s.N
	return pass, &v
}

// RowCountIsZero passes when the scalar (typically a COUNT(*)) is zero.
type RowCountIsZero struct{}

// Test implements AssertionPredicate.
func (RowCountIsZero) Test(value any) (bool, *float64) {
	v, ok := numericFloat(value)
	if !ok {
		return false, nil
	}
	pass := v == 0
	return pass, &v
}

// numericFloat coerces common database scalar types into float64. Returns
// (0, false) for unsupported types so the caller can mark the rule as 'error'.
func numericFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	case nil:
		return 0, true
	}
	return 0, false
}

// ===== Concrete rule types =====

// NullCheck asserts that the null rate of Column is at most MaxRate (0..1).
type NullCheck struct {
	Column  string
	MaxRate float64
}

// Name implements QualityRule.
func (n NullCheck) Name() string { return "null_check_" + n.Column }

// Type implements QualityRule.
func (NullCheck) Type() string { return "null_check" }

// ConfigJSON returns a stable encoding of the rule definition.
func (n NullCheck) ConfigJSON() ([]byte, error) {
	return json.Marshal(struct {
		Column  string  `json:"column"`
		MaxRate float64 `json:"max_rate"`
	}{n.Column, n.MaxRate})
}

// Evaluate runs the null-rate query against the supplied evaluator.
func (n NullCheck) Evaluate(ctx context.Context, eval QualityEvaluator) (QualityResult, error) {
	sqlText := fmt.Sprintf(
		`SELECT COUNT(*)::float8 AS total, COUNT(*) FILTER (WHERE "%s" IS NULL)::float8 AS nulls FROM %s`,
		n.Column, eval.AssetTable())
	row, err := eval.QueryAggregate(ctx, sqlText)
	if err != nil {
		return QualityResult{Status: "error", ErrorMessage: err.Error()}, nil
	}
	if len(row.Values) < 2 {
		return QualityResult{Status: "error", ErrorMessage: "null_check expected 2 columns"}, nil
	}
	total, _ := numericFloat(row.Values[0])
	nulls, _ := numericFloat(row.Values[1])
	rate := 0.0
	if total > 0 {
		rate = nulls / total
	}
	threshold := n.MaxRate
	if rate > n.MaxRate {
		return QualityResult{Status: "failed", MeasuredValue: &rate, Threshold: &threshold}, nil
	}
	return QualityResult{Status: "passed", MeasuredValue: &rate, Threshold: &threshold}, nil
}

// RangeCheck asserts MIN(Column) >= Min and MAX(Column) <= Max.
type RangeCheck struct {
	Column string
	Min    float64
	Max    float64
}

// Name implements QualityRule.
func (r RangeCheck) Name() string { return "range_check_" + r.Column }

// Type implements QualityRule.
func (RangeCheck) Type() string { return "range_check" }

// ConfigJSON returns a stable encoding of the rule definition.
func (r RangeCheck) ConfigJSON() ([]byte, error) {
	return json.Marshal(struct {
		Column string  `json:"column"`
		Min    float64 `json:"min"`
		Max    float64 `json:"max"`
	}{r.Column, r.Min, r.Max})
}

// Evaluate runs the MIN/MAX query and asserts both fall within bounds.
func (r RangeCheck) Evaluate(ctx context.Context, eval QualityEvaluator) (QualityResult, error) {
	sqlText := fmt.Sprintf(
		`SELECT MIN("%s")::float8, MAX("%s")::float8 FROM %s`,
		r.Column, r.Column, eval.AssetTable())
	row, err := eval.QueryAggregate(ctx, sqlText)
	if err != nil {
		return QualityResult{Status: "error", ErrorMessage: err.Error()}, nil
	}
	if len(row.Values) < 2 {
		return QualityResult{Status: "error", ErrorMessage: "range_check expected 2 columns"}, nil
	}
	minV, _ := numericFloat(row.Values[0])
	maxV, _ := numericFloat(row.Values[1])
	if minV < r.Min {
		measured := minV
		threshold := r.Min
		return QualityResult{Status: "failed", MeasuredValue: &measured, Threshold: &threshold}, nil
	}
	if maxV > r.Max {
		measured := maxV
		threshold := r.Max
		return QualityResult{Status: "failed", MeasuredValue: &measured, Threshold: &threshold}, nil
	}
	measured := maxV
	threshold := r.Max
	return QualityResult{Status: "passed", MeasuredValue: &measured, Threshold: &threshold}, nil
}

// SQLAssertion runs an arbitrary aggregate SQL with ${asset} substitution and
// applies a Predicate to the returned scalar.
type SQLAssertion struct {
	Name_     string
	SQL       string
	Predicate AssertionPredicate
}

// Name implements QualityRule.
func (s SQLAssertion) Name() string { return s.Name_ }

// Type implements QualityRule.
func (SQLAssertion) Type() string { return "sql_assertion" }

// ConfigJSON returns a stable encoding of the rule definition. Predicate is
// represented by its concrete type name so two assertions that differ only by
// predicate produce different code_hash inputs.
func (s SQLAssertion) ConfigJSON() ([]byte, error) {
	predName := fmt.Sprintf("%T", s.Predicate)
	var predConf any
	if p, ok := s.Predicate.(ScalarLessThan); ok {
		predConf = struct {
			N float64 `json:"n"`
		}{p.N}
	}
	return json.Marshal(struct {
		Name      string `json:"name"`
		SQL       string `json:"sql"`
		Predicate string `json:"predicate"`
		PredArgs  any    `json:"predicate_args,omitempty"`
	}{s.Name_, s.SQL, predName, predConf})
}

// Evaluate substitutes ${asset} → eval.AssetTable() and runs the SQL.
func (s SQLAssertion) Evaluate(ctx context.Context, eval QualityEvaluator) (QualityResult, error) {
	sqlText := strings.ReplaceAll(s.SQL, "${asset}", eval.AssetTable())
	row, err := eval.QueryAggregate(ctx, sqlText)
	if err != nil {
		return QualityResult{Status: "error", ErrorMessage: err.Error()}, nil
	}
	if len(row.Values) == 0 {
		return QualityResult{Status: "error", ErrorMessage: "sql_assertion: empty result"}, nil
	}
	if s.Predicate == nil {
		return QualityResult{Status: "error", ErrorMessage: "sql_assertion: predicate is nil"}, nil
	}
	pass, measured := s.Predicate.Test(row.Values[0])
	if !pass {
		return QualityResult{Status: "failed", MeasuredValue: measured}, nil
	}
	return QualityResult{Status: "passed", MeasuredValue: measured}, nil
}

// FreshnessSLA declares the maximum staleness budget for an asset (D-20).
// MaxLag is the wall-clock duration after which the SLA scanner emits
// sla.breached and dispatches a notification. ScopeAfterCronFire (default
// false) — when true, the breach window starts at the next scheduled fire,
// not the previous success.
//
// FreshnessSLA is operational config only; it is NOT included in code_hash.
type FreshnessSLA struct {
	MaxLag             time.Duration
	ScopeAfterCronFire bool
}
