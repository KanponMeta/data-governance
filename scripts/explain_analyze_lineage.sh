#!/usr/bin/env bash
# Phase 4 / D-14 EXPLAIN ANALYZE harness.
#
# Seeds asset_edges with ~10K synthetic rows and captures EXPLAIN ANALYZE output
# for the recursive CTE traversal at depth=10 (downstream), depth=25 (downstream),
# and depth=10 (upstream). Results are written to .planning/phases/04-schema/04-EXPLAIN.md.
#
# Usage:
#   export DATABASE_URL="postgres://platform_owner:platform_owner@localhost:5432/platform?sslmode=disable"
#   bash scripts/explain_analyze_lineage.sh
#
# Requirements:
#   - psql on PATH
#   - Platform migrations applied (Phase 4 asset_edges table + partial indices)
#   - Non-production database (guard aborts if DB name contains 'prod')
#
# T-04-08-03: The seed SQL refuses to run against production-named databases.

set -euo pipefail

DATABASE_URL="${DATABASE_URL:-postgres://platform_owner:platform_owner@localhost:5432/platform?sslmode=disable}"
OUT=".planning/phases/04-schema/04-EXPLAIN.md"
SEED_SQL="scripts/seed_lineage_10k.sql"

# Sanity checks.
if ! command -v psql &>/dev/null; then
  echo "ERROR: psql not found on PATH" >&2
  exit 1
fi

if [ ! -f "$SEED_SQL" ]; then
  echo "ERROR: seed script not found: $SEED_SQL" >&2
  echo "Run this script from the project root." >&2
  exit 1
fi

# T-04-08-03: Production guard — refuse if DB name contains 'prod'.
DBNAME=$(psql "$DATABASE_URL" -tAc "SELECT current_database()")
case "$DBNAME" in
  *prod*)
    echo "ERROR: refusing to run against production-named database: $DBNAME" >&2
    exit 1
    ;;
esac

echo "Using database: $DBNAME"
echo "Running seed script (this may take up to 30s for 10K rows)..."
psql "$DATABASE_URL" -f "$SEED_SQL"

EDGE_COUNT=$(psql "$DATABASE_URL" -tAc "SELECT count(*) FROM asset_edges WHERE superseded_at IS NULL")
echo "Active edges seeded: $EDGE_COUNT"

echo "Capturing EXPLAIN ANALYZE output to $OUT ..."

{
  echo "# Phase 4 — Recursive CTE EXPLAIN ANALYZE Capture"
  echo ""
  echo "Generated: $(date -u +"%Y-%m-%dT%H:%M:%SZ")"
  echo "Database: $DBNAME"
  echo "Active edge count: $EDGE_COUNT"
  echo ""

  # ── Depth-10 Downstream ──────────────────────────────────────────────────────
  echo "## Depth-10 Downstream"
  echo ""
  echo '```'
  psql "$DATABASE_URL" -c "EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)
WITH RECURSIVE lineage AS (
    SELECT to_asset AS asset, 0 AS depth, ARRAY[from_asset] AS path
    FROM asset_edges
    WHERE from_asset = 'layer_0_50'
      AND superseded_at IS NULL
  UNION ALL
    SELECT e.to_asset, l.depth + 1, l.path || e.to_asset
    FROM asset_edges e
    JOIN lineage l ON e.from_asset = l.asset
    WHERE e.superseded_at IS NULL
      AND l.depth < 10
      AND NOT (l.path @> ARRAY[e.to_asset])
)
SELECT DISTINCT asset, depth FROM lineage ORDER BY depth, asset LIMIT 1000;"
  echo '```'
  echo ""

  # ── Depth-25 Downstream ──────────────────────────────────────────────────────
  echo "## Depth-25 Downstream"
  echo ""
  echo '```'
  psql "$DATABASE_URL" -c "EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)
WITH RECURSIVE lineage AS (
    SELECT to_asset AS asset, 0 AS depth, ARRAY[from_asset] AS path
    FROM asset_edges
    WHERE from_asset = 'layer_0_50'
      AND superseded_at IS NULL
  UNION ALL
    SELECT e.to_asset, l.depth + 1, l.path || e.to_asset
    FROM asset_edges e
    JOIN lineage l ON e.from_asset = l.asset
    WHERE e.superseded_at IS NULL
      AND l.depth < 25
      AND NOT (l.path @> ARRAY[e.to_asset])
)
SELECT DISTINCT asset, depth FROM lineage ORDER BY depth, asset LIMIT 5000;"
  echo '```'
  echo ""

  # ── Depth-10 Upstream ────────────────────────────────────────────────────────
  echo "## Depth-10 Upstream"
  echo ""
  echo '```'
  psql "$DATABASE_URL" -c "EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)
WITH RECURSIVE lineage AS (
    SELECT from_asset AS asset, 0 AS depth, ARRAY[to_asset] AS path
    FROM asset_edges
    WHERE to_asset = 'layer_5_50'
      AND superseded_at IS NULL
  UNION ALL
    SELECT e.from_asset, l.depth + 1, l.path || e.from_asset
    FROM asset_edges e
    JOIN lineage l ON e.to_asset = l.asset
    WHERE e.superseded_at IS NULL
      AND l.depth < 10
      AND NOT (l.path @> ARRAY[e.from_asset])
)
SELECT DISTINCT asset, depth FROM lineage ORDER BY depth, asset LIMIT 1000;"
  echo '```'
  echo ""

  # ── Verification Checklist ───────────────────────────────────────────────────
  echo "## Verification"
  echo ""
  echo "Check each item after reviewing the EXPLAIN ANALYZE output above:"
  echo ""
  echo "- [ ] Index Scan on asset_edges_active_from / asset_edges_active_to (NOT Seq Scan)"
  echo "      (indicates partial index WHERE superseded_at IS NULL is used — D-13 structural mitigation)"
  echo "- [ ] Depth-10 runtime < 200ms"
  echo "      (PITFALLS §4 threshold: 'if depth-10 CTE > 200ms, plan graph-DB migration')"
  echo "- [ ] Depth-25 runtime < 1000ms"
  echo "      (acceptable upper bound for the hard-cap edge case — not the hot path)"
  echo "- [ ] No CTE materialization fence ('CTE Scan' + 'Materialize' in plan output)"
  echo "      (CTE materialization adds intermediate materializations that hurt performance at scale)"
  echo ""
  echo "Verified by: (pending)"
  echo "Date: (pending)"
} > "$OUT"

echo "Wrote $OUT"
chmod 644 "$OUT"

echo ""
echo "Next step: open $OUT and:"
echo "  1. Confirm Index Scan (not Seq Scan) for the recursive CTE base scan"
echo "  2. Confirm depth-10 runtime < 200ms"
echo "  3. Confirm depth-25 runtime < 1000ms"
echo "  4. Fill in 'Verified by:' and 'Date:' then reply 'approved' to the orchestrator"
