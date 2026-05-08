-- Create "runs" table
CREATE TABLE "runs" (
  "id" uuid NOT NULL,
  "asset_name" character varying(256) NOT NULL,
  "state" character varying(16) NOT NULL DEFAULT 'queued',
  "trigger" character varying(32) NOT NULL DEFAULT 'manual',
  "triggered_by" uuid NULL,
  "claimed_by" character varying(128) NULL,
  "queued_at" timestamptz NOT NULL,
  "claimed_at" timestamptz NULL,
  "started_at" timestamptz NULL,
  "finished_at" timestamptz NULL,
  "last_heartbeat" timestamptz NULL,
  "error_message" text NULL,
  "metadata" jsonb NULL,
  PRIMARY KEY ("id")
);
-- Create index "run_state_queued_at" to table: "runs"
CREATE INDEX "run_state_queued_at" ON "runs" ("state", "queued_at");
-- Create index "run_asset_name_queued_at" to table: "runs"
CREATE INDEX "run_asset_name_queued_at" ON "runs" ("asset_name", "queued_at");
-- Create index "run_queued_at" to table: "runs"
CREATE INDEX "run_queued_at" ON "runs" ("queued_at");
-- Create index "run_state_last_heartbeat" to table: "runs"
CREATE INDEX "run_state_last_heartbeat" ON "runs" ("state", "last_heartbeat");
-- Create "run_steps" table
CREATE TABLE "run_steps" (
  "id" uuid NOT NULL,
  "run_id" uuid NOT NULL,
  "asset_name" character varying(256) NOT NULL,
  "state" character varying(16) NOT NULL DEFAULT 'pending',
  "attempt" bigint NOT NULL DEFAULT 0,
  "topo_order" bigint NOT NULL DEFAULT 0,
  "started_at" timestamptz NULL,
  "finished_at" timestamptz NULL,
  "rows_written" bigint NOT NULL DEFAULT 0,
  "error_message" text NULL,
  "metadata" jsonb NULL,
  PRIMARY KEY ("id")
);
-- Create index "runstep_run_id_topo_order" to table: "run_steps"
CREATE INDEX "runstep_run_id_topo_order" ON "run_steps" ("run_id", "topo_order");
-- Create index "runstep_run_id_state" to table: "run_steps"
CREATE INDEX "runstep_run_id_state" ON "run_steps" ("run_id", "state");
-- Create index "runstep_asset_name" to table: "run_steps"
CREATE INDEX "runstep_asset_name" ON "run_steps" ("asset_name");

-- ===== Hand-managed: CHECK constraints + role grants for runs / run_steps (Phase 2 D-17) =====
-- Idempotent: safe across re-runs.

ALTER TABLE runs OWNER TO platform_owner;
ALTER TABLE run_steps OWNER TO platform_owner;

GRANT SELECT, INSERT, UPDATE, DELETE ON runs      TO platform_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON run_steps TO platform_app;

ALTER TABLE runs DROP CONSTRAINT IF EXISTS runs_state_check;
ALTER TABLE runs
  ADD CONSTRAINT runs_state_check
  CHECK (state IN ('queued','starting','running','succeeded','failed','canceled'));

ALTER TABLE run_steps DROP CONSTRAINT IF EXISTS run_steps_state_check;
ALTER TABLE run_steps
  ADD CONSTRAINT run_steps_state_check
  CHECK (state IN ('pending','running','succeeded','failed','skipped'));

-- last_heartbeat supports plan 02-04's stale-run reaper. See migration 02-04 if a
-- separate alter is needed; if this migration's diff already includes it via the ent
-- field, this comment serves as documentation only.
COMMENT ON COLUMN runs.last_heartbeat IS
  'Set on claim, ticked every ~30s by executor. Reaper resets stale rows (>5m old) to queued.';
