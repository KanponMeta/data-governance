package asset

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// noopMaterialize is a zero-value MaterializeFunc used in tests that don't
// exercise the function itself.
func noopMaterialize(ctx context.Context, io AssetIO) (MaterializeResult, error) {
	return MaterializeResult{}, nil
}

// ---- TestAsset: Asset accessor methods ----

func TestAsset_Accessors(t *testing.T) {
	policy := RetryPolicy{Max: 3, InitialDelay: time.Second, MaxDelay: time.Minute, JitterPct: 10}
	resources := []Resource{{Name: "postgres-prod", Weight: 2}}

	a := &Asset{
		name:          "users_clean",
		upstreams:     []string{"users_raw", "events"},
		connectorName: "postgres-prod",
		materializeFn: noopMaterialize,
		retryPolicy:   policy,
		resources:     resources,
	}

	require.Equal(t, "users_clean", a.Name())
	require.Equal(t, []string{"users_raw", "events"}, a.Upstreams())
	require.Equal(t, "postgres-prod", a.ConnectorName())
	require.NotNil(t, a.MaterializeFn())
	require.Equal(t, policy, a.RetryPolicy())
	require.Equal(t, resources, a.Resources())
}

func TestAsset_Upstreams_ReturnsCopy(t *testing.T) {
	a := &Asset{upstreams: []string{"a", "b"}}
	ups := a.Upstreams()
	ups[0] = "mutated"
	// Original must be unchanged
	require.Equal(t, "a", a.Upstreams()[0])
}

func TestAsset_Resources_ReturnsCopy(t *testing.T) {
	a := &Asset{resources: []Resource{{Name: "r1", Weight: 1}}}
	res := a.Resources()
	res[0].Name = "mutated"
	// Original must be unchanged
	require.Equal(t, "r1", a.Resources()[0].Name)
}

// ---- TestRetryPolicy ----

func TestRetryPolicy_IsZero(t *testing.T) {
	require.True(t, RetryPolicy{Max: 0}.IsZero(), "RetryPolicy{Max:0} should be zero")
	require.True(t, RetryPolicy{}.IsZero(), "empty RetryPolicy should be zero")
	require.False(t, RetryPolicy{Max: 1}.IsZero(), "RetryPolicy with Max=1 should not be zero")
	require.False(t, RetryPolicy{InitialDelay: time.Second}.IsZero(), "non-zero delay should not be zero")
}

func TestDefaultRetryPolicy_IsZero(t *testing.T) {
	p := DefaultRetryPolicy()
	require.True(t, p.IsZero(), "DefaultRetryPolicy() should be zero-value")
	require.Equal(t, 0, p.Max)
	require.Equal(t, time.Duration(0), p.InitialDelay)
	require.Equal(t, time.Duration(0), p.MaxDelay)
	require.Equal(t, 0, p.JitterPct)
}

// ---- TestMaterializeResult ----

func TestMaterializeResult_Fields(t *testing.T) {
	r := MaterializeResult{
		RowsWritten: 42,
		Metadata:    map[string]any{"key": "val"},
	}
	require.Equal(t, int64(42), r.RowsWritten)
	require.Equal(t, "val", r.Metadata["key"])
}

// ---- TestResource ----

func TestResource_Fields(t *testing.T) {
	r := Resource{Name: "postgres-prod", Weight: 2}
	require.Equal(t, "postgres-prod", r.Name)
	require.Equal(t, 2, r.Weight)
}
