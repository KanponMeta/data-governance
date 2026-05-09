package asset

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestBuilder_QualityRule_Chainable verifies QualityRule appends and remains in
// declaration order via Asset.QualityRules().
func TestBuilder_QualityRule_Chainable(t *testing.T) {
	t.Cleanup(resetForTest)

	r1 := NullCheck{Column: "customer_id", MaxRate: 0.0}
	r2 := RangeCheck{Column: "amount", Min: 0, Max: 1e9}
	a, err := New("orders").
		Connector("pg").
		Materialize(noopMaterialize).
		QualityRule(r1).
		QualityRule(r2).
		Build()
	require.NoError(t, err)
	rules := a.QualityRules()
	require.Len(t, rules, 2)
	require.Equal(t, "null_check_customer_id", rules[0].Name())
	require.Equal(t, "range_check_amount", rules[1].Name())
}

// TestBuilder_QualityRule_DuplicateNameFails ensures Build rejects two rules with
// the same Name() — required so quality_results rows are unambiguous.
func TestBuilder_QualityRule_DuplicateNameFails(t *testing.T) {
	t.Cleanup(resetForTest)

	r := NullCheck{Column: "customer_id", MaxRate: 0.0}
	_, err := New("orders").
		Connector("pg").
		Materialize(noopMaterialize).
		QualityRule(r).
		QualityRule(r).
		Build()
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrQualityRuleNameDuplicate))
}

// TestBuilder_QualityRule_InCodeHash verifies that adding a QualityRule changes
// the asset's code_hash (D-08 governance reset semantics).
func TestBuilder_QualityRule_InCodeHash(t *testing.T) {
	t.Cleanup(resetForTest)

	a1, err := New("orders").
		Connector("pg").
		Materialize(noopMaterialize).
		Build()
	require.NoError(t, err)

	a2, err := New("orders").
		Connector("pg").
		Materialize(noopMaterialize).
		QualityRule(NullCheck{Column: "customer_id", MaxRate: 0.0}).
		Build()
	require.NoError(t, err)

	require.NotEqual(t, a1.CodeHash(), a2.CodeHash(),
		"adding a QualityRule must change CodeHash")
}

// TestBuilder_FreshnessSLA_Stores verifies the SLA is round-tripped via the asset.
func TestBuilder_FreshnessSLA_Stores(t *testing.T) {
	t.Cleanup(resetForTest)

	a, err := New("orders").
		Connector("pg").
		Materialize(noopMaterialize).
		FreshnessSLA(FreshnessSLA{MaxLag: 6 * time.Hour}).
		Build()
	require.NoError(t, err)
	sla := a.FreshnessSLA()
	require.NotNil(t, sla)
	require.Equal(t, 6*time.Hour, sla.MaxLag)
}

// TestBuilder_FreshnessSLA_RejectsZeroDuration enforces MaxLag > 0 at Build time.
func TestBuilder_FreshnessSLA_RejectsZeroDuration(t *testing.T) {
	t.Cleanup(resetForTest)

	_, err := New("orders").
		Connector("pg").
		Materialize(noopMaterialize).
		FreshnessSLA(FreshnessSLA{MaxLag: 0}).
		Build()
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrFreshnessSLAInvalid))
}

// TestBuilder_FreshnessSLA_NotInCodeHash verifies SLA changes do NOT change
// code_hash (operational config only).
func TestBuilder_FreshnessSLA_NotInCodeHash(t *testing.T) {
	t.Cleanup(resetForTest)

	a1, err := New("orders").
		Connector("pg").
		Materialize(noopMaterialize).
		Build()
	require.NoError(t, err)

	a2, err := New("orders").
		Connector("pg").
		Materialize(noopMaterialize).
		FreshnessSLA(FreshnessSLA{MaxLag: 6 * time.Hour}).
		Build()
	require.NoError(t, err)

	require.Equal(t, a1.CodeHash(), a2.CodeHash(),
		"FreshnessSLA must not influence CodeHash")
}
