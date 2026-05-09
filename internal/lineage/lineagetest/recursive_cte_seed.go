package lineagetest

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// NOTE: All seeder functions in this file require the asset_edges table from
// Plan 04-02 migration to be applied. Callers should use t.Skip() if Phase 4
// migrations are not present — each function performs an early-detect check:
//
//	_, err := db.ExecContext(ctx, "SELECT 1 FROM asset_edges LIMIT 0")
//	if err != nil { t.Skip("asset_edges not present: Phase 4 migration required") }

// SeedDAG inserts a linear chain of asset_edges in PostgreSQL:
//
//	asset_0 → asset_1 → … → asset_<depth>
//
// Each edge row is set with:
//   - code_hash_first = code_hash_latest = "seed"
//   - first_seen_run_id = last_seen_run_id = a fresh UUID
//   - first_seen_at = last_seen_at = time.Now().UTC()
//   - superseded_at = NULL (active edge)
//
// Returns the asset names in topological order (asset_0 is the root with no
// incoming edges; asset_<depth> is the leaf).
//
// Used by:
//   - LINE-03 TestRecursiveCTE — depths 1, 5, 10
//   - LINE-06 TestImpactDepthCap — depth 25 (allowed), 26 (rejected at query time)
func SeedDAG(ctx context.Context, db *sql.DB, depth int) ([]string, error) {
	// Early-detect: skip if asset_edges table does not exist yet.
	if _, err := db.ExecContext(ctx, "SELECT 1 FROM asset_edges LIMIT 0"); err != nil {
		return nil, fmt.Errorf("asset_edges table not present (Plan 04-02 migration required): %w", err)
	}

	// Build asset names: asset_0, asset_1, ..., asset_<depth>
	names := make([]string, depth+1)
	for i := 0; i <= depth; i++ {
		names[i] = fmt.Sprintf("asset_%d", i)
	}

	now := time.Now().UTC()
	runID := uuid.New()

	// Build a single multi-row INSERT for all edges in the chain.
	edges := make([]edge, depth)
	for i := 0; i < depth; i++ {
		edges[i] = edge{from: names[i], to: names[i+1]}
	}

	if err := insertEdges(ctx, db, edges, runID, now); err != nil {
		return nil, fmt.Errorf("SeedDAG depth=%d: %w", depth, err)
	}
	return names, nil
}

// SeedBranching inserts a balanced binary tree of asset_edges at the given
// depth, producing 2^depth−1 assets and 2^depth−2 edges. The root asset is
// named "branch_root".
//
// Tree structure (depth=3 example):
//
//	branch_root → branch_1_0 → branch_2_0
//	                         → branch_2_1
//	            → branch_1_1 → branch_2_2
//	                         → branch_2_3
//
// Returns all asset names in BFS order.
//
// Used to verify the recursive CTE handles fan-out (multiple downstream
// children), not just linear chains.
func SeedBranching(ctx context.Context, db *sql.DB, depth int) ([]string, error) {
	if _, err := db.ExecContext(ctx, "SELECT 1 FROM asset_edges LIMIT 0"); err != nil {
		return nil, fmt.Errorf("asset_edges table not present (Plan 04-02 migration required): %w", err)
	}
	if depth < 1 {
		return []string{"branch_root"}, nil
	}

	now := time.Now().UTC()
	runID := uuid.New()

	// Generate names level by level. Level 0 = root.
	levels := make([][]string, depth+1)
	levels[0] = []string{"branch_root"}
	for l := 1; l <= depth; l++ {
		nodes := make([]string, 0, 1<<l)
		for _, parent := range levels[l-1] {
			nodes = append(nodes, parent+"_0", parent+"_1")
		}
		levels[l] = nodes
	}

	// Collect all names (BFS order) and all edges.
	var allNames []string
	var edges []edge
	for l, nodes := range levels {
		allNames = append(allNames, nodes...)
		if l < depth {
			for _, parent := range levels[l] {
				edges = append(edges, edge{parent, parent + "_0"}, edge{parent, parent + "_1"})
			}
		}
	}

	if err := insertEdges(ctx, db, edges, runID, now); err != nil {
		return nil, fmt.Errorf("SeedBranching depth=%d: %w", depth, err)
	}
	return allNames, nil
}

// SeedCycle inserts a minimal cycle: cycle_a → cycle_b and cycle_b → cycle_a.
// This allows cycle-guard tests to verify that the recursive CTE path-array
// NOT-contains check prevents infinite traversal.
//
// Returns the two asset names (assetA="cycle_a", assetB="cycle_b") so callers
// can start traversal from either node.
//
// Used by: TestCyclicLineageGuard.
func SeedCycle(ctx context.Context, db *sql.DB) (assetA, assetB string, err error) {
	if _, err := db.ExecContext(ctx, "SELECT 1 FROM asset_edges LIMIT 0"); err != nil {
		return "", "", fmt.Errorf("asset_edges table not present (Plan 04-02 migration required): %w", err)
	}

	assetA, assetB = "cycle_a", "cycle_b"
	now := time.Now().UTC()
	runID := uuid.New()

	edges := []edge{
		{assetA, assetB},
		{assetB, assetA},
	}
	if err := insertEdges(ctx, db, edges, runID, now); err != nil {
		return "", "", fmt.Errorf("SeedCycle: %w", err)
	}
	return assetA, assetB, nil
}

// edge is a directed pair of asset names.
type edge struct{ from, to string }

// insertEdges performs a single multi-row INSERT INTO asset_edges for the
// given edges, using a shared run_id and timestamp for all rows.
// It uses ON CONFLICT DO NOTHING so seeder functions are idempotent when
// called multiple times in the same test run against the same DB.
func insertEdges(ctx context.Context, db *sql.DB, edges []edge, runID uuid.UUID, now time.Time) error {
	if len(edges) == 0 {
		return nil
	}

	const rowPlaceholders = "(gen_random_uuid(), $%d, $%d, 'seed', 'seed', $%d, $%d, $%d, $%d, NULL)"

	placeholders := make([]string, 0, len(edges))
	args := make([]any, 0, len(edges)*6)
	paramIdx := 1
	for _, e := range edges {
		placeholders = append(placeholders, fmt.Sprintf(rowPlaceholders,
			paramIdx, paramIdx+1, paramIdx+2, paramIdx+3, paramIdx+4, paramIdx+5))
		args = append(args, e.from, e.to, runID, now, runID, now)
		paramIdx += 6
	}

	query := `INSERT INTO asset_edges
		(id, from_asset, to_asset, code_hash_first, code_hash_latest,
		 first_seen_run_id, first_seen_at, last_seen_run_id, last_seen_at, superseded_at)
	VALUES ` + strings.Join(placeholders, ", ") + `
	ON CONFLICT (from_asset, to_asset) WHERE superseded_at IS NULL DO NOTHING`

	_, err := db.ExecContext(ctx, query, args...)
	return err
}
