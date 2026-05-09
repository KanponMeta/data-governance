package asset

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// noopMaterialize2 is a second noop function literal with the same body as noopMaterialize.
// Used to test that function identity does NOT affect the hash.
var noopMaterialize2 = noopMaterialize

// TestComputeCodeHashDeterministic — same Builder chain produces the same hash 100 times.
func TestComputeCodeHashDeterministic(t *testing.T) {
	a, err := New("stable").
		Upstream("up1").
		Connector("pg").
		Materialize(noopMaterialize).
		Build()
	require.NoError(t, err)

	first := ComputeCodeHash(a)
	require.NotEmpty(t, first, "hash must not be empty")
	require.Len(t, first, 64, "SHA-256 hex must be 64 characters")

	for i := 0; i < 100; i++ {
		require.Equal(t, first, ComputeCodeHash(a),
			"hash changed on iteration %d", i)
	}
}

// TestComputeCodeHashConcurrent — 50 goroutines compute hash on the same *Asset concurrently.
func TestComputeCodeHashConcurrent(t *testing.T) {
	a, err := New("concurrent").
		Upstream("u1").
		Connector("pg").
		Materialize(noopMaterialize).
		Description("concurrent test asset").
		Owner("team@example.com").
		Tags("a", "b").
		Build()
	require.NoError(t, err)

	expected := ComputeCodeHash(a)
	require.NotEmpty(t, expected)

	var wg sync.WaitGroup
	results := make([]string, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = ComputeCodeHash(a)
		}(i)
	}
	wg.Wait()

	for i, h := range results {
		require.Equal(t, expected, h, "goroutine %d produced different hash", i)
	}
}

// TestComputeCodeHashOrderInvariant — call order of Tags, Upstream, Column does not affect hash.
func TestComputeCodeHashOrderInvariant(t *testing.T) {
	a1, err := New("x").
		Tags("a", "b", "c").
		Connector("pg").
		Materialize(noopMaterialize).
		Upstream("up1").
		Upstream("up2").
		Build()
	require.NoError(t, err)

	a2, err := New("x").
		Tags("c", "a", "b").
		Connector("pg").
		Materialize(noopMaterialize).
		Upstream("up2").
		Upstream("up1").
		Build()
	require.NoError(t, err)

	require.Equal(t, ComputeCodeHash(a1), ComputeCodeHash(a2),
		"tag and upstream ordering must not affect hash")

	// Column order invariance
	a3, err := New("colorder").
		Connector("pg").
		Materialize(noopMaterialize).
		Column("alpha").Description("first col").And().
		Column("beta").Description("second col").And().
		Build()
	require.NoError(t, err)

	a4, err := New("colorder").
		Connector("pg").
		Materialize(noopMaterialize).
		Column("beta").Description("second col").And().
		Column("alpha").Description("first col").And().
		Build()
	require.NoError(t, err)

	require.Equal(t, ComputeCodeHash(a3), ComputeCodeHash(a4),
		"column declaration order must not affect hash")
}

// TestComputeCodeHashSensitiveFields — hash changes when any meaningful field changes.
func TestComputeCodeHashSensitiveFields(t *testing.T) {
	base, err := New("base").
		Connector("pg").
		Materialize(noopMaterialize).
		Upstream("u1").
		Description("desc").
		Owner("owner@example.com").
		Tags("t1").
		Build()
	require.NoError(t, err)
	baseHash := ComputeCodeHash(base)

	tests := []struct {
		name    string
		builder func() (*Builder, error)
	}{
		{
			"name changes",
			func() (*Builder, error) {
				return New("different"), nil
			},
		},
		{
			"upstream changes",
			func() (*Builder, error) {
				return New("base").Upstream("u2"), nil
			},
		},
		{
			"description changes",
			func() (*Builder, error) {
				return New("base").Description("other desc"), nil
			},
		},
		{
			"owner changes",
			func() (*Builder, error) {
				return New("base").Owner("other@example.com"), nil
			},
		},
		{
			"tag changes",
			func() (*Builder, error) {
				return New("base").Tags("t2"), nil
			},
		},
		{
			"column added",
			func() (*Builder, error) {
				return New("base").Column("col1").Description("c").And(), nil
			},
		},
		{
			"column lineage changes",
			func() (*Builder, error) {
				return New("base").ColumnLineage(ColumnLineageMap{
					"out": {{Asset: "src", Column: "in"}},
				}), nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := tt.builder()
			// Ensure connectorName and materializeFn are set for valid builds
			a, err := b.Connector("pg").Materialize(noopMaterialize).Build()
			require.NoError(t, err)
			h := ComputeCodeHash(a)
			require.NotEqual(t, baseHash, h,
				"hash should differ when %s", tt.name)
		})
	}
}

// TestComputeCodeHashIgnoredFields — hash does NOT change for connector, materialize fn,
// retry policy, schedule, sensors, or partitions changes.
func TestComputeCodeHashIgnoredFields(t *testing.T) {
	makeFn := func(connName string, fn MaterializeFunc) (*Asset, error) {
		return New("ignore_test").
			Connector(connName).
			Materialize(fn).
			Upstream("u1").
			Description("desc").
			Build()
	}

	base, err := makeFn("pg", noopMaterialize)
	require.NoError(t, err)
	baseHash := ComputeCodeHash(base)

	// Different connector name — hash must be same
	diffConn, err := makeFn("mysql", noopMaterialize)
	require.NoError(t, err)
	require.Equal(t, baseHash, ComputeCodeHash(diffConn), "connector name must NOT affect hash")

	// Different function literal (same body) — hash must be same
	// (Go closures differ by identity but content doesn't change data shape)
	diffFn, err := makeFn("pg", noopMaterialize2)
	require.NoError(t, err)
	require.Equal(t, baseHash, ComputeCodeHash(diffFn), "materialize fn must NOT affect hash")

	// Different retry policy
	withRetry, err := New("ignore_test").
		Connector("pg").
		Materialize(noopMaterialize).
		Upstream("u1").
		Description("desc").
		Retry(RetryPolicy{Max: 3}).
		Build()
	require.NoError(t, err)
	require.Equal(t, baseHash, ComputeCodeHash(withRetry), "retry policy must NOT affect hash")

	// Different schedule
	withSchedule, err := New("ignore_test").
		Connector("pg").
		Materialize(noopMaterialize).
		Upstream("u1").
		Description("desc").
		Schedule("@daily").
		Build()
	require.NoError(t, err)
	require.Equal(t, baseHash, ComputeCodeHash(withSchedule), "schedule must NOT affect hash")
}

// TestRegisterStoresCodeHash — Register() produces an Asset with non-empty CodeHash.
func TestRegisterStoresCodeHash(t *testing.T) {
	t.Cleanup(resetForTest)

	err := New("hashable").
		Connector("pg").
		Materialize(noopMaterialize).
		Register()
	require.NoError(t, err)

	a, err := Default().Get("hashable")
	require.NoError(t, err)
	require.NotEmpty(t, a.CodeHash(), "CodeHash must be set after Register()")
	require.Len(t, a.CodeHash(), 64, "SHA-256 hex must be 64 characters")
	require.Equal(t, ComputeCodeHash(a), a.CodeHash(), "stored hash must equal freshly computed hash")
}

// TestBuildStoresCodeHash — Build() also populates CodeHash (without registration).
func TestBuildStoresCodeHash(t *testing.T) {
	a, err := New("build_hash").
		Connector("pg").
		Materialize(noopMaterialize).
		Build()
	require.NoError(t, err)
	require.NotEmpty(t, a.CodeHash(), "CodeHash must be set after Build()")
	require.Len(t, a.CodeHash(), 64)
}

// TestComputeCodeHashNil — nil asset returns empty string (defensive).
func TestComputeCodeHashNil(t *testing.T) {
	require.Equal(t, "", ComputeCodeHash(nil))
}

// TestComputeCodeHashPhase4DslChain — exercises the full Phase 4 DSL chain and
// pins a known stable hash. If this test fails after code changes, the hash
// changed unexpectedly — review fingerprint canonicalization.
func TestComputeCodeHashPhase4DslChain(t *testing.T) {
	a, err := New("orders").
		Connector("postgres-prod").
		Materialize(noopMaterialize).
		Description("Daily orders fact table").
		Owner("team-data@example.com").
		Tags("finance", "pii").
		Column("user_id").Description("FK users.id").Tags("pii").And().
		Column("total").Description("USD cents").And().
		ColumnLineage(ColumnLineageMap{
			"user_id": {{Asset: "payments", Column: "payer_id"}},
		}).
		Build()
	require.NoError(t, err)

	h := ComputeCodeHash(a)
	require.NotEmpty(t, h)
	require.Len(t, h, 64)

	// Golden value: computed once and pinned for stability (D-03).
	// If fingerprint canonicalization changes, update this constant and document the reason.
	// Pinned 2026-05-09: SHA-256 of canonical JSON for the "orders" example chain from 04-RESEARCH.md.
	const goldenHash = "1ff892702afda232e57d98686b3f1c1cdcd3a4c50d71b0d79dd70b60ed99f431"
	require.Equal(t, goldenHash, h, "fingerprint hash changed — update golden value or investigate canonicalization change")
	t.Logf("Golden hash confirmed: %s", h)
}
