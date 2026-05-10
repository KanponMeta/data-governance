package asset

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/kanpon/data-governance/internal/connector"
)

// MaskApplyFunc is the dependency-injection point that lets MaskingIO call
// the in-pipeline transforms from internal/policy without creating a
// circular import (internal/policy imports internal/asset for ColumnPolicy).
//
// The executor wires policy.Apply directly; tests can pass any function with
// matching signature. mt is the MaskType, value the cleartext, reveal the
// partial-reveal length (interpreted only by MaskPartial).
type MaskApplyFunc func(mt connector.MaskType, value string, reveal int) (string, error)

// errMaskApplyNotWired is returned by MaskingIO.Write when the constructor
// was called without an apply function. Loud failure beats silent passthrough.
var errMaskApplyNotWired = errMaskNotWired{}

type errMaskNotWired struct{}

func (errMaskNotWired) Error() string {
	return "asset: MaskingIO.Write called without an apply function (NewMaskingIO requires a non-nil MaskApplyFunc)"
}

// MaskRule is one resolved mask directive applied by MaskingIO at Write time.
// It is the runtime view of a column_policies / pii-tag row — collected by the
// executor (Plan 05-03 D-05 capability assertion order) before invoking the
// user materialize function.
type MaskRule struct {
	// Column is the name of the field on each connector.Row.Fields entry.
	Column string
	// Mask selects the transform applied to the column value.
	Mask connector.MaskType
	// Reveal is the partial-reveal length (only consulted for MaskPartial).
	// Zero falls back to the policy default (2 leading + 2 trailing).
	Reveal int
}

// MaskingIO is a decorator that wraps an AssetIO and rewrites per-row column
// values inside Write using the supplied MaskRule slice (Plan 05-03 / RBAC-05).
//
// The decorator is installed by the executor when:
//
//  1. The asset's bound connector does NOT implement
//     connector.MaskingProvisioner (warehouse-native masking is unavailable).
//  2. AND at least one column on the asset has either an active
//     column_policies row with enforcement_mode='in-pipeline' (or 'unknown'
//     pending sync) OR carries pii=true after propagation.
//
// MaskingIO MUST be wrapped INSIDE TrackingIO so that drift detection
// records the actually-read upstream set rather than masking-augmented data.
type MaskingIO struct {
	inner AssetIO
	asset string
	rules []MaskRule
	apply MaskApplyFunc
	mu    sync.Mutex
	rows  int64
}

// NewMaskingIO snapshots rules at construction time so concurrent policy
// changes during a long materialize are NOT picked up mid-run (Phase 5 D-04
// invariant: policies that were active at runStep claim time apply for the
// whole run).
//
// apply is the dependency-injection point (typically policy.Apply); a nil
// apply panics-fast at first Write so misuse is loud rather than silent.
func NewMaskingIO(inner AssetIO, asset string, rules []MaskRule, apply MaskApplyFunc) *MaskingIO {
	cp := append([]MaskRule(nil), rules...)
	return &MaskingIO{inner: inner, asset: asset, rules: cp, apply: apply}
}

// Read passes through unchanged — masking applies on the egress (Write) side.
func (m *MaskingIO) Read(ctx context.Context, upstream string) ([]connector.Row, error) {
	return m.inner.Read(ctx, upstream)
}

// PartitionKey passes through unchanged.
func (m *MaskingIO) PartitionKey() string { return m.inner.PartitionKey() }

// Write applies every MaskRule in turn to each row's Fields map and then
// delegates to the inner AssetIO. If rules is empty Write is a pure
// pass-through (zero-allocation hot path).
//
// Mask transforms operate on string values only — non-string columns pass
// through unchanged. This matches v1 scope: warehouse-native DDM/CLS also
// only masks string-like types (numerics for partial only when explicitly
// CAST). When/if additional types become a requirement, extend Apply().
func (m *MaskingIO) Write(ctx context.Context, rows []connector.Row) (int64, error) {
	if len(m.rules) == 0 {
		return m.inner.Write(ctx, rows)
	}

	maskedColumns := make(map[string]struct{}, len(m.rules))

	out := make([]connector.Row, len(rows))
	for i, r := range rows {
		newFields := make(map[string]any, len(r.Fields))
		for k, v := range r.Fields {
			newFields[k] = v
		}
		for _, rule := range m.rules {
			raw, ok := newFields[rule.Column]
			if !ok {
				continue
			}
			s, isStr := raw.(string)
			if !isStr {
				// Non-string column: leave it alone in v1.
				continue
			}
			if m.apply == nil {
				return 0, errMaskApplyNotWired
			}
			masked, err := m.apply(rule.Mask, s, rule.Reveal)
			if err != nil {
				return 0, err
			}
			newFields[rule.Column] = masked
			maskedColumns[rule.Column] = struct{}{}
		}
		out[i] = connector.Row{Fields: newFields}
	}

	m.mu.Lock()
	m.rows += int64(len(rows))
	m.mu.Unlock()

	if len(maskedColumns) > 0 {
		// Stable column list for log readability.
		keys := make([]string, 0, len(maskedColumns))
		for k := range maskedColumns {
			keys = append(keys, k)
		}
		slog.Debug("mask applied",
			"asset", m.asset,
			"rows_masked", len(rows),
			"columns_masked", strings.Join(keys, ","),
		)
	}

	return m.inner.Write(ctx, out)
}

// Compile-time check: MaskingIO satisfies AssetIO.
var _ AssetIO = (*MaskingIO)(nil)
