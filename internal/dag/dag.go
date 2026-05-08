// Package dag provides the asset dependency graph builder using heimdalr/dag.
// It constructs a topologically-ordered execution plan from user-defined
// assets and their upstream declarations.
package dag

import (
	"errors"
	"fmt"

	heimdalr "github.com/heimdalr/dag"
	"github.com/kanpon/data-governance/internal/asset"
)

var (
	// ErrCycle is returned by BuildDAG when the asset dependency graph contains
	// a cycle (T-02-02-03 mitigation). No execution is started for cyclic plans.
	ErrCycle = errors.New("dag: cycle detected")

	// ErrUnknownUpstream is returned by BuildDAG when an asset references an
	// upstream name that is not present in the input slice (T-02-02-04 mitigation).
	ErrUnknownUpstream = errors.New("dag: upstream not registered")
)

// Graph wraps heimdalr/dag with helpers tailored to asset dependency graphs.
// Use BuildDAG to construct a Graph from a slice of *asset.Asset values.
type Graph struct {
	d     *heimdalr.DAG
	order int // number of vertices
}

// BuildDAG constructs a Graph from the supplied assets. It adds a vertex per asset
// (keyed by Asset.Name()) and an edge per upstream dependency (upstream → asset).
//
// Returns ErrUnknownUpstream if an asset references a name not in the input slice.
// Returns ErrCycle if the resulting graph has a cycle (heimdalr detects this on AddEdge).
func BuildDAG(assets []*asset.Asset) (*Graph, error) {
	d := heimdalr.NewDAG()

	// First pass: register all vertices so edges can reference them by name.
	names := make(map[string]struct{}, len(assets))
	for _, a := range assets {
		if err := d.AddVertexByID(a.Name(), a); err != nil {
			return nil, fmt.Errorf("dag: add vertex %q: %w", a.Name(), err)
		}
		names[a.Name()] = struct{}{}
	}

	// Second pass: add directed edges from upstream → asset (parent → child in DAG terms).
	for _, a := range assets {
		for _, up := range a.Upstreams() {
			if _, ok := names[up]; !ok {
				return nil, fmt.Errorf("%w: asset %q references unknown upstream %q",
					ErrUnknownUpstream, a.Name(), up)
			}
			if err := d.AddEdge(up, a.Name()); err != nil {
				// heimdalr returns EdgeLoopError when the edge would create a cycle.
				return nil, fmt.Errorf("%w: edge %s->%s: %v", ErrCycle, up, a.Name(), err)
			}
		}
	}

	return &Graph{d: d, order: len(assets)}, nil
}

// TopologicalOrder returns asset names in a valid topological order (upstreams before
// the assets that depend on them). Uses Kahn's algorithm on the heimdalr DAG.
//
// The returned order is deterministic within the constraints of the dependency graph;
// assets with no mutual dependency may appear in any relative order.
//
// Returns ErrCycle if a cycle is detected during traversal (defense-in-depth; BuildDAG
// should have caught this, but TopologicalOrder validates independently).
func (g *Graph) TopologicalOrder() ([]string, error) {
	// Compute in-degree (number of parents) for each vertex using heimdalr API.
	inDeg := make(map[string]int, g.order)
	for id := range g.d.GetVertices() {
		parents, err := g.d.GetParents(id)
		if err != nil {
			return nil, fmt.Errorf("dag: get parents of %q: %w", id, err)
		}
		inDeg[id] = len(parents)
	}

	// Seed the queue with root vertices (in-degree zero).
	var queue []string
	for id := range g.d.GetRoots() {
		queue = append(queue, id)
	}

	order := make([]string, 0, g.order)
	for len(queue) > 0 {
		// Dequeue (FIFO for deterministic ordering).
		n := queue[0]
		queue = queue[1:]
		order = append(order, n)

		children, err := g.d.GetChildren(n)
		if err != nil {
			return nil, fmt.Errorf("dag: get children of %q: %w", n, err)
		}
		for cid := range children {
			inDeg[cid]--
			if inDeg[cid] == 0 {
				queue = append(queue, cid)
			}
		}
	}

	if len(order) != g.order {
		// This indicates a cycle that slipped past BuildDAG — treat as ErrCycle.
		return nil, fmt.Errorf("%w: topological sort length %d != vertex count %d",
			ErrCycle, len(order), g.order)
	}
	return order, nil
}

// Order returns the number of vertices (assets) in the graph.
func (g *Graph) Order() int { return g.order }
