//go:build integration

package lineageq_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/lineage/impact"
	lineageq "github.com/kanpon/data-governance/internal/lineage/queries"
	"github.com/kanpon/data-governance/internal/lineage/lineagetest"
	"github.com/kanpon/data-governance/internal/runtime/executortest"
)

// openPgxPool opens a pgx-native connection from the *sql.DB DSN so we can
// pass it as lineageq.DBTX (which requires pgx's Exec/Query/QueryRow, not
// database/sql's).
//
// We use pgx.Connect instead of pgxpool so test teardown is simple — each test
// that needs pgx gets its own single connection.
func openPgxConn(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	require.NoError(t, err, "pgx.Connect")
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// mustSeedDAG calls SeedDAG and fails the test on error.
func mustSeedDAG(t *testing.T, db *sql.DB, depth int) []string {
	t.Helper()
	names, err := lineagetest.SeedDAG(context.Background(), db, depth)
	require.NoError(t, err, "SeedDAG depth=%d", depth)
	return names
}

// mustSeedBranching calls SeedBranching and fails the test on error.
func mustSeedBranching(t *testing.T, db *sql.DB, depth int) []string {
	t.Helper()
	names, err := lineagetest.SeedBranching(context.Background(), db, depth)
	require.NoError(t, err, "SeedBranching depth=%d", depth)
	return names
}

// mustSeedCycle calls SeedCycle and fails the test on error.
func mustSeedCycle(t *testing.T, db *sql.DB) (string, string) {
	t.Helper()
	a, b, err := lineagetest.SeedCycle(context.Background(), db)
	require.NoError(t, err, "SeedCycle")
	return a, b
}

// insertColumnEdge inserts a single column edge directly into column_edges.
func insertColumnEdge(t *testing.T, db *sql.DB, fromAsset, fromCol, toAsset, toCol string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO column_edges (
			id, from_asset, from_column, to_asset, to_column,
			code_hash_first, code_hash_latest,
			first_seen_run_id, first_seen_at,
			last_seen_run_id, last_seen_at,
			superseded_at, partition_key
		) VALUES (
			gen_random_uuid(), $1, $2, $3, $4,
			'seed', 'seed',
			gen_random_uuid(), now(),
			gen_random_uuid(), now(),
			NULL, NULL
		)
		ON CONFLICT DO NOTHING`,
		fromAsset, fromCol, toAsset, toCol,
	)
	require.NoError(t, err, "insertColumnEdge %s.%s → %s.%s", fromAsset, fromCol, toAsset, toCol)
}

// insertRetiredEdge inserts an asset edge that is already superseded (soft-retired).
func insertRetiredEdge(t *testing.T, db *sql.DB, fromAsset, toAsset string, retiredAt time.Time) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO asset_edges (
			id, from_asset, to_asset,
			code_hash_first, code_hash_latest,
			first_seen_run_id, first_seen_at,
			last_seen_run_id, last_seen_at,
			superseded_at
		) VALUES (
			gen_random_uuid(), $1, $2,
			'seed', 'seed',
			gen_random_uuid(), $3,
			gen_random_uuid(), $3,
			$4
		)`,
		fromAsset, toAsset, retiredAt.Add(-1*time.Hour), retiredAt,
	)
	require.NoError(t, err, "insertRetiredEdge %s → %s", fromAsset, toAsset)
}

// -------- Linear chain tests --------

func TestRecursiveCTELinearDepth1(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)
	conn := openPgxConn(t, c.URL)

	mustSeedDAG(t, c.DB, 1) // asset_0 → asset_1

	res, err := impact.Analyze(ctx, conn, impact.ImpactQuery{
		Asset:     "asset_0",
		Direction: "downstream",
		Depth:     10,
	})
	require.NoError(t, err)
	require.Len(t, res.Nodes, 1, "expected 1 downstream node from asset_0")
	assert.Equal(t, "asset_1", res.Nodes[0].Asset)
	assert.Equal(t, 1, res.Nodes[0].Depth)
}

func TestRecursiveCTELinearDepth5(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)
	conn := openPgxConn(t, c.URL)

	mustSeedDAG(t, c.DB, 5) // asset_0 → asset_1 → … → asset_5

	// Downstream from asset_0: should return asset_1 … asset_5 (5 nodes).
	res, err := impact.Analyze(ctx, conn, impact.ImpactQuery{
		Asset:     "asset_0",
		Direction: "downstream",
		Depth:     10,
	})
	require.NoError(t, err)
	assert.Len(t, res.Nodes, 5, "downstream from asset_0 depth=10: expected 5 nodes")
	for i, n := range res.Nodes {
		assert.Equal(t, i+1, n.Depth, "node[%d].Depth", i)
	}

	// Upstream from asset_5: should return asset_0 … asset_4 (5 nodes).
	res2, err := impact.Analyze(ctx, conn, impact.ImpactQuery{
		Asset:     "asset_5",
		Direction: "upstream",
		Depth:     10,
	})
	require.NoError(t, err)
	assert.Len(t, res2.Nodes, 5, "upstream from asset_5 depth=10: expected 5 nodes")
}

func TestRecursiveCTELinearDepth10(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)
	conn := openPgxConn(t, c.URL)

	mustSeedDAG(t, c.DB, 10)

	// Full depth returns 10 nodes.
	res, err := impact.Analyze(ctx, conn, impact.ImpactQuery{
		Asset:     "asset_0",
		Direction: "downstream",
		Depth:     10,
	})
	require.NoError(t, err)
	assert.Len(t, res.Nodes, 10)

	// Depth=5 cap returns exactly 5 nodes.
	res2, err := impact.Analyze(ctx, conn, impact.ImpactQuery{
		Asset:     "asset_0",
		Direction: "downstream",
		Depth:     5,
	})
	require.NoError(t, err)
	assert.Len(t, res2.Nodes, 5)
}

// -------- Cycle safety test --------

func TestRecursiveCTECycleSafety(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)
	conn := openPgxConn(t, c.URL)

	assetA, assetB := mustSeedCycle(t, c.DB) // cycle_a → cycle_b and cycle_b → cycle_a

	// Downstream from A: should return only B (not infinite loop, not A itself).
	resA, err := impact.Analyze(ctx, conn, impact.ImpactQuery{
		Asset:     assetA,
		Direction: "downstream",
		Depth:     10,
	})
	require.NoError(t, err, "Analyze from A downstream should not error")
	require.Len(t, resA.Nodes, 1, "cycle: downstream from A should return exactly 1 node (B)")
	assert.Equal(t, assetB, resA.Nodes[0].Asset)

	// Downstream from B: should return only A.
	resB, err := impact.Analyze(ctx, conn, impact.ImpactQuery{
		Asset:     assetB,
		Direction: "downstream",
		Depth:     10,
	})
	require.NoError(t, err, "Analyze from B downstream should not error")
	require.Len(t, resB.Nodes, 1, "cycle: downstream from B should return exactly 1 node (A)")
	assert.Equal(t, assetA, resB.Nodes[0].Asset)
}

// -------- Depth cap tests --------

func TestRecursiveCTEDepthCap25(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)
	conn := openPgxConn(t, c.URL)

	mustSeedDAG(t, c.DB, 25) // 25 edges; asset_0 → … → asset_25

	// depth=25 (MaxDepth) is allowed.
	res, err := impact.Analyze(ctx, conn, impact.ImpactQuery{
		Asset:     "asset_0",
		Direction: "downstream",
		Depth:     25,
	})
	require.NoError(t, err)
	assert.Len(t, res.Nodes, 25, "depth=25: expected 25 nodes")

	// depth=26 is rejected by impact.Analyze before any DB call.
	_, err = impact.Analyze(ctx, conn, impact.ImpactQuery{
		Asset:     "asset_0",
		Direction: "downstream",
		Depth:     26,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, impact.ErrDepthExceeded))
}

// TestCTEMaxDepthSQLEnforced bypasses impact.Analyze and calls the sqlc-generated
// TraverseAssetLineage directly with MaxDepth=26 and MaxDepth=999. It verifies
// that the SQL-level LEAST(@max_depth::int, 25) clause caps the result at 25 rows,
// independent of what the caller supplies. This is D-14 defense-in-depth layer 2.
func TestCTEMaxDepthSQLEnforced(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)
	conn := openPgxConn(t, c.URL)

	mustSeedDAG(t, c.DB, 30) // 30 edges

	q := lineageq.New()

	for _, maxDepth := range []int32{26, 999} {
		t.Run("MaxDepth="+itoa(maxDepth), func(t *testing.T) {
			rows, err := q.TraverseAssetLineage(ctx, conn, lineageq.TraverseAssetLineageParams{
				Direction: "downstream",
				Asset:     "asset_0",
				UseAsOf:   false,
				AsOf:      pgtype.Timestamptz{},
				MaxDepth:  maxDepth,
			})
			require.NoError(t, err, "TraverseAssetLineage MaxDepth=%d", maxDepth)

			// The SQL LEAST(@max_depth::int, 25) should cap at 25.
			maxActual := int32(math.MinInt32)
			for _, r := range rows {
				if r.Depth > maxActual {
					maxActual = r.Depth
				}
			}
			assert.LessOrEqual(t, int(maxActual), 25,
				"SQL-level LEAST cap: max depth in results must be ≤ 25 even with MaxDepth=%d", maxDepth)
			assert.LessOrEqual(t, len(rows), 25,
				"SQL-level LEAST cap: result count must be ≤ 25 even with MaxDepth=%d", maxDepth)
		})
	}
}

// itoa converts int32 to string for test naming.
func itoa(n int32) string {
	return fmt.Sprintf("%d", n)
}

// -------- Branching test --------

func TestRecursiveCTEBranching(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)
	conn := openPgxConn(t, c.URL)

	// SeedBranching depth=3: balanced binary tree.
	// Level 0: branch_root (1 node)
	// Level 1: branch_root_0, branch_root_1 (2 nodes)
	// Level 2: 4 nodes
	// Level 3: 8 nodes
	// Total: 15 nodes, 14 edges.
	mustSeedBranching(t, c.DB, 3)

	res, err := impact.Analyze(ctx, conn, impact.ImpactQuery{
		Asset:     "branch_root",
		Direction: "downstream",
		Depth:     3,
	})
	require.NoError(t, err)
	// 14 downstream nodes (all non-root), ordered by depth then asset name.
	assert.Len(t, res.Nodes, 14,
		"branching depth=3: downstream from root should return 14 nodes")
}

// -------- Active-only vs AsOf --------

func TestRecursiveCTEActiveOnly(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)
	conn := openPgxConn(t, c.URL)

	// Insert one active edge and one retired edge.
	mustSeedDAG(t, c.DB, 1) // active: asset_0 → asset_1

	retireTime := time.Now().UTC()
	insertRetiredEdge(t, c.DB, "asset_0", "asset_retired", retireTime)

	// Active-only (AsOf=nil): only asset_1 is returned.
	res, err := impact.Analyze(ctx, conn, impact.ImpactQuery{
		Asset:     "asset_0",
		Direction: "downstream",
		Depth:     10,
		AsOf:      nil, // active edges only
	})
	require.NoError(t, err)
	assets := nodeAssets(res.Nodes)
	assert.Contains(t, assets, "asset_1", "active-only: should include asset_1")
	assert.NotContains(t, assets, "asset_retired", "active-only: should NOT include retired edge")

	// AsOf=before retirement: the retired edge was active before retireTime, so it's included.
	beforeRetirement := retireTime.Add(-30 * time.Minute)
	res2, err := impact.Analyze(ctx, conn, impact.ImpactQuery{
		Asset:     "asset_0",
		Direction: "downstream",
		Depth:     10,
		AsOf:      &beforeRetirement,
	})
	require.NoError(t, err)
	assets2 := nodeAssets(res2.Nodes)
	assert.Contains(t, assets2, "asset_retired",
		"AsOf=before-retirement: retired edge should be visible at that point in time")
}

// -------- Column traversal --------

func TestRecursiveCTEColumnTraversal(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)
	conn := openPgxConn(t, c.URL)

	// Seed: A.col1 → B.col2 → C.col3
	insertColumnEdge(t, c.DB, "A", "col1", "B", "col2")
	insertColumnEdge(t, c.DB, "B", "col2", "C", "col3")

	col := "col1"
	res, err := impact.Analyze(ctx, conn, impact.ImpactQuery{
		Asset:     "A",
		Column:    &col,
		Direction: "downstream",
		Depth:     10,
	})
	require.NoError(t, err)
	require.Len(t, res.Nodes, 2, "column traversal: expected 2 downstream column nodes")

	// Node 0: B.col2 at depth 1
	assert.Equal(t, "B", res.Nodes[0].Asset)
	require.NotNil(t, res.Nodes[0].Column)
	assert.Equal(t, "col2", *res.Nodes[0].Column)
	assert.Equal(t, 1, res.Nodes[0].Depth)

	// Node 1: C.col3 at depth 2
	assert.Equal(t, "C", res.Nodes[1].Asset)
	require.NotNil(t, res.Nodes[1].Column)
	assert.Equal(t, "col3", *res.Nodes[1].Column)
	assert.Equal(t, 2, res.Nodes[1].Depth)
}

// -------- Performance smoke test --------

func TestRecursiveCTEPerformanceSmoke(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)
	conn := openPgxConn(t, c.URL)

	mustSeedDAG(t, c.DB, 10)

	start := time.Now()
	res, err := impact.Analyze(ctx, conn, impact.ImpactQuery{
		Asset:     "asset_0",
		Direction: "downstream",
		Depth:     10,
	})
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Len(t, res.Nodes, 10)
	assert.Less(t, elapsed, 1*time.Second,
		"performance: SeedDAG depth=10 + Analyze depth=10 should complete within 1s (testcontainers), took %s", elapsed)
}

// -------- helpers --------

// nodeAssets extracts the asset names from a node slice.
func nodeAssets(nodes []impact.ImpactNode) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Asset
	}
	return out
}
