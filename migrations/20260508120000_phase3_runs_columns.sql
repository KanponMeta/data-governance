-- Phase 3 (D-10 / D-13 / D-15) — additive columns on the existing "runs" table.
-- Idempotent: uses ADD COLUMN IF NOT EXISTS so re-applies are safe.
--
-- Mirrors the hand-managed pattern from migrations/20260507120000_phase2_run_tables.sql:
-- ent generates column declarations via field.* on the Run schema; partial unique
-- indexes, CHECK constraints, and role grants are appended by hand below because
-- ent has no native support for those primitives.

-- Modify "runs" table — add Phase 3 columns
ALTER TABLE "runs" ADD COLUMN IF NOT EXISTS "partition_key" character varying(128) NULL;
ALTER TABLE "runs" ADD COLUMN IF NOT EXISTS "priority"    character varying(16)  NOT NULL DEFAULT 'normal';
ALTER TABLE "runs" ADD COLUMN IF NOT EXISTS "backfill_id" uuid                   NULL;

-- ===== Hand-managed: Phase 3 D-10 / D-13 / D-15 (idempotent) =====

ALTER TABLE runs DROP CONSTRAINT IF EXISTS runs_priority_check;
ALTER TABLE runs
  ADD CONSTRAINT runs_priority_check
  CHECK (priority IN ('critical','normal','backfill'));

-- D-10 partial unique index — scoped to in-flight states only so terminal runs
-- can be re-enqueued for the same partition (Pitfall 7).
DROP INDEX IF EXISTS run_partition_inflight_unique;
CREATE UNIQUE INDEX run_partition_inflight_unique
  ON runs (asset_name, partition_key)
  WHERE state IN ('queued','starting','running')
    AND partition_key IS NOT NULL;

-- D-13 priority-aware claim index — supports
--   WHERE state='queued' ORDER BY <priority CASE>, queued_at
CREATE INDEX IF NOT EXISTS run_state_priority_queued_at
  ON runs (state, priority, queued_at);

-- D-15 backfill_id partial index — supports backfill status aggregation
--   SELECT state, count(*) FROM runs WHERE backfill_id=$1 GROUP BY state
CREATE INDEX IF NOT EXISTS run_backfill_id
  ON runs (backfill_id) WHERE backfill_id IS NOT NULL;
