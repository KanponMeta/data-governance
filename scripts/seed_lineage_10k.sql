-- Phase 4 / D-14 EXPLAIN ANALYZE harness seed.
--
-- Seeds ~10,000 active edges across 1,000 synthetic assets in a 10-layer DAG.
-- Layer 0: 100 root assets (no upstreams) — no edges.
-- Layers 1–9: each layer-N asset picks 1–3 random upstreams from layer-N-1.
-- Deterministic: setseed(0.42) ensures stable query plan capture across runs.
--
-- T-04-08-03 production-DB guard: refuses to run if current_database() contains 'prod'.
--
-- Usage:
--   psql "$DATABASE_URL" -f scripts/seed_lineage_10k.sql
--
-- Note: This script truncates asset_edges. Use ONLY against a dev or CI database.

\set ON_ERROR_STOP on

-- Production guard (T-04-08-03): refuse to seed if DB name suggests production.
DO $$
BEGIN
  IF current_database() ILIKE '%prod%' THEN
    RAISE EXCEPTION 'seed refuses to run on production-named database (%)',
      current_database();
  END IF;
END $$;

\echo 'WARNING: seeds ~10K rows into asset_edges. NOT for production use.'
\echo 'Database:' :DBNAME

-- Set a deterministic random seed for reproducible edge selection.
SELECT setseed(0.42);

-- Truncate to start clean (idempotent re-run).
TRUNCATE asset_edges;

-- Seed the layered DAG:
--   Layer 0 (100 root assets): no edges; they are referenced as from_asset below.
--   Layers 1–9: for each destination asset in layer N (0..99 dst), generate
--   1–3 upstream picks from layer N-1 using LATERAL + generate_series.
--   The CROSS JOIN LATERAL generates between 1 and 3 upstream picks per destination.
--   ON CONFLICT DO NOTHING deduplies any (from, to) repeats.
--
-- Expected row count: ~10K active edges (depends on random seed fanout).
INSERT INTO asset_edges (
  id, from_asset, to_asset,
  code_hash_first, code_hash_latest,
  first_seen_run_id, first_seen_at,
  last_seen_run_id, last_seen_at,
  superseded_at
)
SELECT
  gen_random_uuid(),
  format('layer_%s_%s', layer - 1, ABS((random() * 99)::int % 100)),
  format('layer_%s_%s', layer, dst_idx),
  repeat('a', 64),
  repeat('a', 64),
  gen_random_uuid(),
  NOW() - (random() * INTERVAL '30 days'),
  gen_random_uuid(),
  NOW(),
  NULL
FROM generate_series(1, 9) AS layer
CROSS JOIN generate_series(0, 99) AS dst_idx
CROSS JOIN LATERAL (
  -- Each destination gets 1–3 upstream picks (fanout varies by dst_idx).
  SELECT gs
  FROM generate_series(1, 1 + (dst_idx % 3)) AS gs
) AS fanout
ON CONFLICT DO NOTHING;

-- Update planner statistics so EXPLAIN ANALYZE uses fresh row-count estimates.
ANALYZE asset_edges;

-- Report seeded row counts for human verification.
SELECT
  count(*) AS total_edges,
  count(*) FILTER (WHERE superseded_at IS NULL) AS active_edges,
  count(DISTINCT from_asset) AS distinct_from_assets,
  count(DISTINCT to_asset) AS distinct_to_assets
FROM asset_edges;
