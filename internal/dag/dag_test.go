package dag_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/dag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustBuild constructs an *asset.Asset via Builder.Build() without registering it
// in the global Default() registry. This keeps DAG tests hermetic across test cases.
func mustBuild(t *testing.T, name string, upstreams ...string) *asset.Asset {
	t.Helper()
	b := asset.New(name).Connector("c").Materialize(func(_ context.Context, _ asset.AssetIO) (asset.MaterializeResult, error) {
		return asset.MaterializeResult{}, nil
	})
	if len(upstreams) > 0 {
		b = b.Upstream(upstreams...)
	}
	a, err := b.Build()
	require.NoError(t, err, "mustBuild(%q) failed: %v", name, err)
	return a
}

// positionOf returns the index of name in order, or -1 if not found.
func positionOf(order []string, name string) int {
	for i, n := range order {
		if n == name {
			return i
		}
	}
	return -1
}

// TestDAGLinearChain builds a three-node chain a→b→c and asserts that the
// topological order places a before b and b before c.
func TestDAGLinearChain(t *testing.T) {
	a := mustBuild(t, "a")
	b := mustBuild(t, "b", "a")
	c := mustBuild(t, "c", "b")

	g, err := dag.BuildDAG([]*asset.Asset{a, b, c})
	require.NoError(t, err)

	order, err := g.TopologicalOrder()
	require.NoError(t, err)
	require.Len(t, order, 3)

	posA := positionOf(order, "a")
	posB := positionOf(order, "b")
	posC := positionOf(order, "c")

	assert.GreaterOrEqual(t, posA, 0, "a must appear in order")
	assert.GreaterOrEqual(t, posB, 0, "b must appear in order")
	assert.GreaterOrEqual(t, posC, 0, "c must appear in order")
	assert.Less(t, posA, posB, "a must come before b in topological order")
	assert.Less(t, posB, posC, "b must come before c in topological order")
}

// TestDAGFanIn builds a fan-in pattern (a→c, b→c) and asserts that c appears
// after both a and b in the topological order.
func TestDAGFanIn(t *testing.T) {
	a := mustBuild(t, "a")
	b := mustBuild(t, "b")
	c := mustBuild(t, "c", "a", "b")

	g, err := dag.BuildDAG([]*asset.Asset{a, b, c})
	require.NoError(t, err)

	order, err := g.TopologicalOrder()
	require.NoError(t, err)
	require.Len(t, order, 3)

	posA := positionOf(order, "a")
	posB := positionOf(order, "b")
	posC := positionOf(order, "c")

	assert.Less(t, posA, posC, "a must come before c (fan-in)")
	assert.Less(t, posB, posC, "b must come before c (fan-in)")
}

// TestDAGFanOut builds a fan-out pattern (a→b, a→c) and asserts that a appears
// before both b and c.
func TestDAGFanOut(t *testing.T) {
	a := mustBuild(t, "a")
	b := mustBuild(t, "b", "a")
	c := mustBuild(t, "c", "a")

	g, err := dag.BuildDAG([]*asset.Asset{a, b, c})
	require.NoError(t, err)

	order, err := g.TopologicalOrder()
	require.NoError(t, err)
	require.Len(t, order, 3)

	posA := positionOf(order, "a")
	assert.Less(t, posA, positionOf(order, "b"), "a must come before b (fan-out)")
	assert.Less(t, posA, positionOf(order, "c"), "a must come before c (fan-out)")
}

// TestDAGSingleNode builds a graph with a single asset and asserts the order
// contains exactly that asset.
func TestDAGSingleNode(t *testing.T) {
	a := mustBuild(t, "only")

	g, err := dag.BuildDAG([]*asset.Asset{a})
	require.NoError(t, err)

	order, err := g.TopologicalOrder()
	require.NoError(t, err)
	require.Equal(t, []string{"only"}, order)
}

// TestDAGCycle verifies that BuildDAG returns ErrCycle when the asset graph is cyclic.
func TestDAGCycle(t *testing.T) {
	// To create a cycle we use Upstreams() only — builder doesn't allow self-referencing
	// names that aren't in the input slice, so we build two assets that reference each other.
	//
	// a references b as upstream; b references a as upstream → cycle a→b→a.
	a := mustBuild(t, "a", "b")
	b := mustBuild(t, "b", "a")

	_, err := dag.BuildDAG([]*asset.Asset{a, b})
	require.Error(t, err, "BuildDAG must return an error for cyclic input")
	assert.True(t, errors.Is(err, dag.ErrCycle),
		"error must wrap dag.ErrCycle; got: %v", err)
}

// TestDAGUnknownUpstream verifies that BuildDAG returns ErrUnknownUpstream when an
// asset references an upstream name not present in the input slice.
func TestDAGUnknownUpstream(t *testing.T) {
	a := mustBuild(t, "a", "missing_upstream")

	_, err := dag.BuildDAG([]*asset.Asset{a})
	require.Error(t, err, "BuildDAG must return an error for unknown upstream")
	assert.True(t, errors.Is(err, dag.ErrUnknownUpstream),
		"error must wrap dag.ErrUnknownUpstream; got: %v", err)
}

// TestDAGBuildDoesNotRegister confirms that mustBuild (via builder.Build()) does
// NOT add assets to the global Default() registry, keeping tests hermetic.
func TestDAGBuildDoesNotRegister(t *testing.T) {
	const name = "dag_isolation_test_asset"
	_ = mustBuild(t, name)

	_, err := asset.Default().Get(name)
	assert.Error(t, err, "Build() must not register asset in Default() registry")
	assert.True(t, errors.Is(err, asset.ErrNotFound),
		"expected ErrNotFound; got: %v", err)
}

// TestDAGOrder verifies that Graph.Order() returns the vertex count.
func TestDAGOrder(t *testing.T) {
	assets := []*asset.Asset{
		mustBuild(t, "x"),
		mustBuild(t, "y", "x"),
		mustBuild(t, "z", "y"),
	}
	g, err := dag.BuildDAG(assets)
	require.NoError(t, err)
	assert.Equal(t, 3, g.Order())
}
