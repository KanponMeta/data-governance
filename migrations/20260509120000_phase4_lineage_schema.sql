-- Create "asset_edges" table
CREATE TABLE "asset_edges" (
  "id" uuid NOT NULL,
  "from_asset" character varying(256) NOT NULL,
  "to_asset" character varying(256) NOT NULL,
  "code_hash_first" character varying(64) NOT NULL,
  "code_hash_latest" character varying(64) NOT NULL,
  "first_seen_run_id" uuid NOT NULL,
  "first_seen_at" timestamptz NOT NULL,
  "last_seen_run_id" uuid NOT NULL,
  "last_seen_at" timestamptz NOT NULL,
  "superseded_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- Create index "assetedge_from_asset" to table: "asset_edges"
CREATE INDEX "assetedge_from_asset" ON "asset_edges" ("from_asset");
-- Create index "assetedge_to_asset" to table: "asset_edges"
CREATE INDEX "assetedge_to_asset" ON "asset_edges" ("to_asset");
-- Create "column_edges" table
CREATE TABLE "column_edges" (
  "id" uuid NOT NULL,
  "from_asset" character varying(256) NOT NULL,
  "from_column" character varying(256) NOT NULL,
  "to_asset" character varying(256) NOT NULL,
  "to_column" character varying(256) NOT NULL,
  "code_hash_first" character varying(64) NOT NULL,
  "code_hash_latest" character varying(64) NOT NULL,
  "first_seen_run_id" uuid NOT NULL,
  "first_seen_at" timestamptz NOT NULL,
  "last_seen_run_id" uuid NOT NULL,
  "last_seen_at" timestamptz NOT NULL,
  "superseded_at" timestamptz NULL,
  "partition_key" character varying(128) NULL,
  PRIMARY KEY ("id")
);
-- Create index "columnedge_from_asset_from_column" to table: "column_edges"
CREATE INDEX "columnedge_from_asset_from_column" ON "column_edges" ("from_asset", "from_column");
-- Create index "columnedge_to_asset_to_column" to table: "column_edges"
CREATE INDEX "columnedge_to_asset_to_column" ON "column_edges" ("to_asset", "to_column");
-- Create "schema_versions" table
CREATE TABLE "schema_versions" (
  "id" uuid NOT NULL,
  "asset" character varying(256) NOT NULL,
  "code_hash" character varying(64) NOT NULL,
  "schema_hash" character varying(64) NOT NULL,
  "schema_data" jsonb NOT NULL,
  "captured_at" timestamptz NOT NULL,
  "last_seen_at" timestamptz NOT NULL,
  "last_seen_run_id" uuid NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "schemaversion_asset_captured_at" to table: "schema_versions"
CREATE INDEX "schemaversion_asset_captured_at" ON "schema_versions" ("asset", "captured_at");
-- Create index "schemaversion_schema_hash" to table: "schema_versions"
CREATE INDEX "schemaversion_schema_hash" ON "schema_versions" ("schema_hash");
-- Create "schema_changes" table
CREATE TABLE "schema_changes" (
  "id" uuid NOT NULL,
  "asset" character varying(256) NOT NULL,
  "run_id" uuid NOT NULL,
  "code_hash" character varying(64) NOT NULL,
  "prev_version_id" uuid NULL,
  "new_version_id" uuid NOT NULL,
  "change_type" character varying(32) NOT NULL,
  "column_name" character varying(256) NULL,
  "prev_type" character varying(64) NULL,
  "new_type" character varying(64) NULL,
  "prev_nullable" boolean NULL,
  "new_nullable" boolean NULL,
  "is_breaking" boolean NOT NULL DEFAULT false,
  "observed_at" timestamptz NOT NULL,
  "acknowledged_at" timestamptz NULL,
  "acknowledged_by" uuid NULL,
  "acknowledgement_reason" text NULL,
  PRIMARY KEY ("id")
);
-- Create index "schemachange_asset_observed_at" to table: "schema_changes"
CREATE INDEX "schemachange_asset_observed_at" ON "schema_changes" ("asset", "observed_at");
-- Create index "schemachange_asset_column_name_observed_at" to table: "schema_changes"
CREATE INDEX "schemachange_asset_column_name_observed_at" ON "schema_changes" ("asset", "column_name", "observed_at");
-- Create index "schemachange_acknowledged_at" to table: "schema_changes"
CREATE INDEX "schemachange_acknowledged_at" ON "schema_changes" ("acknowledged_at");
-- Create "asset_versions" table
CREATE TABLE "asset_versions" (
  "id" uuid NOT NULL,
  "asset" character varying(256) NOT NULL,
  "code_hash" character varying(64) NOT NULL,
  "description" text NULL,
  "owner" character varying(256) NULL,
  "tags" jsonb NULL,
  "column_lineage" jsonb NULL,
  "drift_status" character varying(16) NOT NULL DEFAULT 'clean',
  "created_at" timestamptz NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "assetversion_asset_created_at" to table: "asset_versions"
CREATE INDEX "assetversion_asset_created_at" ON "asset_versions" ("asset", "created_at");
-- Create unique index "assetversion_asset_code_hash" to table: "asset_versions"
CREATE UNIQUE INDEX "assetversion_asset_code_hash" ON "asset_versions" ("asset", "code_hash");
-- Create "asset_metadata" table
CREATE TABLE "asset_metadata" (
  "id" uuid NOT NULL,
  "asset" character varying(256) NOT NULL,
  "column_name" character varying(256) NULL,
  "description" text NULL,
  "owner" character varying(256) NULL,
  "tags" jsonb NULL,
  "set_by" uuid NOT NULL,
  "set_at" timestamptz NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "assetmetadata_asset_column_name_set_at" to table: "asset_metadata"
CREATE INDEX "assetmetadata_asset_column_name_set_at" ON "asset_metadata" ("asset", "column_name", "set_at");

-- ===== Hand-managed: Phase 4 D-13 partial indices (active edges hot path) =====
DROP INDEX IF EXISTS asset_edges_active_from;
CREATE INDEX asset_edges_active_from
  ON asset_edges (from_asset) WHERE superseded_at IS NULL;

DROP INDEX IF EXISTS asset_edges_active_to;
CREATE INDEX asset_edges_active_to
  ON asset_edges (to_asset) WHERE superseded_at IS NULL;

DROP INDEX IF EXISTS column_edges_active_from;
CREATE INDEX column_edges_active_from
  ON column_edges (from_asset, from_column) WHERE superseded_at IS NULL;

DROP INDEX IF EXISTS column_edges_active_to;
CREATE INDEX column_edges_active_to
  ON column_edges (to_asset, to_column) WHERE superseded_at IS NULL;

-- ===== Hand-managed: Phase 4 CHECK constraints =====
-- D-13 Pitfall 2 — prevent self-edges (recursive CTE cycle guard at DB level).
ALTER TABLE asset_edges DROP CONSTRAINT IF EXISTS asset_edges_no_self;
ALTER TABLE asset_edges
  ADD CONSTRAINT asset_edges_no_self CHECK (from_asset != to_asset);

-- column_edges may have same asset+different column (still valid lineage edge).
-- Guard only against same column on same asset.
ALTER TABLE column_edges DROP CONSTRAINT IF EXISTS column_edges_no_self;
ALTER TABLE column_edges
  ADD CONSTRAINT column_edges_no_self
  CHECK (NOT (from_asset = to_asset AND from_column = to_column));

-- D-09 change_type enum (granular internal values; REST exposes minimum set).
ALTER TABLE schema_changes DROP CONSTRAINT IF EXISTS schema_changes_change_type_check;
ALTER TABLE schema_changes
  ADD CONSTRAINT schema_changes_change_type_check CHECK (change_type IN (
    'column_added','column_dropped',
    'type_narrowed','type_widened',
    'nullable_added','nullable_removed',
    'pk_changed','comment_changed','default_changed'
  ));

-- drift_status on asset_versions (D-04).
ALTER TABLE asset_versions DROP CONSTRAINT IF EXISTS asset_versions_drift_status_check;
ALTER TABLE asset_versions
  ADD CONSTRAINT asset_versions_drift_status_check
  CHECK (drift_status IN ('clean','pending','acknowledged'));

-- ===== Hand-managed: Phase 4 role grants =====
ALTER TABLE asset_edges      OWNER TO platform_owner;
ALTER TABLE column_edges     OWNER TO platform_owner;
ALTER TABLE schema_versions  OWNER TO platform_owner;
ALTER TABLE schema_changes   OWNER TO platform_owner;
ALTER TABLE asset_versions   OWNER TO platform_owner;
ALTER TABLE asset_metadata   OWNER TO platform_owner;

-- D-15 soft-retire pattern: edges UPDATE last_seen_*/superseded_at; never deleted.
GRANT SELECT, INSERT, UPDATE ON asset_edges     TO platform_app;
GRANT SELECT, INSERT, UPDATE ON column_edges    TO platform_app;
-- D-08 dedup needs UPDATE last_seen_*; full snapshot is immutable in app-layer ent mutation.
GRANT SELECT, INSERT, UPDATE ON schema_versions TO platform_app;
-- D-10/D-11 schema_changes: append-only except for ack columns enforced in app-layer ent mutation.
GRANT SELECT, INSERT, UPDATE ON schema_changes  TO platform_app;
REVOKE DELETE, TRUNCATE ON schema_changes FROM platform_app;
-- asset_versions are append-only (new code_hash -> new row).
GRANT SELECT, INSERT, UPDATE ON asset_versions  TO platform_app;
-- asset_metadata is append-only history (D-17 INSERT model -- last set_at wins on read).
GRANT SELECT, INSERT ON asset_metadata TO platform_app;
REVOKE UPDATE, DELETE, TRUNCATE ON asset_metadata FROM platform_app;

-- D-09 RLS-immutability (mirrors event_log pattern from migrations/20260506062521_initial.sql).
-- schema_changes: RLS allows SELECT + INSERT for all rows; UPDATE only on ack columns
-- (enforced in app-layer ent mutation, not in DB -- see 04-RESEARCH.md Open Question 3).
-- We do NOT enable FORCE ROW LEVEL SECURITY here because the ent ack mutation needs
-- a clear UPDATE path; column-level RLS would require PostgreSQL 15+ FOR UPDATE OF (col).
-- The audit guarantee comes from REVOKE DELETE/TRUNCATE above + the mandatory
-- schema.break_acknowledged event_log row written by Wave 6 ack handler.
-- (See 04-RESEARCH.md Pitfall 6 for the rationale.)

-- asset_metadata is RLS-protected like event_log (history rows are immutable).
ALTER TABLE asset_metadata ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_metadata FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS asset_metadata_select ON asset_metadata;
CREATE POLICY asset_metadata_select ON asset_metadata FOR SELECT TO platform_app USING (true);
DROP POLICY IF EXISTS asset_metadata_insert ON asset_metadata;
CREATE POLICY asset_metadata_insert ON asset_metadata FOR INSERT TO platform_app WITH CHECK (true);
-- No UPDATE / DELETE policy => denied for platform_app even if grants are accidentally added.

-- ===== Hand-managed: Phase 4 D-21 -- extend event_log event_type CHECK constraint =====
-- Idempotent DROP + ADD pattern (precedent: 20260508120000_phase3_runs_columns.sql lines 14-19).
-- Includes ALL Phase 1 + Phase 2 + Phase 3 + Phase 4 event_type values.
ALTER TABLE event_log DROP CONSTRAINT IF EXISTS event_log_event_type_check;
ALTER TABLE event_log ADD CONSTRAINT event_log_event_type_check
  CHECK (event_type IN (
    -- Phase 1 (D-10)
    'user.registered', 'user.invited',
    'auth.login', 'auth.logout', 'auth.token_expired',
    'platform.started', 'platform.migration_applied',
    -- Phase 2 (D-18)
    'run.queued', 'run.started',
    'run.step.started', 'run.step.succeeded', 'run.step.failed',
    'run.step.retry_scheduled',
    'run.succeeded', 'run.failed', 'run.canceled',
    -- Phase 3 (D-17)
    'schedule.fired', 'schedule.missed', 'schedule.paused', 'schedule.resumed',
    'sensor.evaluated', 'sensor.fired', 'sensor.evaluation_failed',
    'sensor.disabled', 'sensor.cooldown_skipped', 'sensor.dedup_skipped',
    'backfill.submitted', 'backfill.run_enqueued', 'backfill.completed',
    -- Phase 4 (D-21)
    'lineage.captured', 'lineage.drift_detected',
    'schema.captured', 'schema.unchanged', 'schema.change_detected',
    'schema.capture_failed', 'schema.break_acknowledged',
    'metadata.updated'
  ));

-- ===== Hand-managed: Active-edge uniqueness for ON CONFLICT upserts (D-15 soft-retire pattern) =====
-- These UNIQUE partial indices provide the ON CONFLICT target used by
-- lineage.Writer.SyncStaticEdges and lineage.Writer.CaptureRun in plan 04-04.
-- They complement the hot-path partial indices added above (asset_edges_active_from, etc.)
-- by providing a named constraint for the ON CONFLICT clause.
DROP INDEX IF EXISTS asset_edges_active_unique;
CREATE UNIQUE INDEX asset_edges_active_unique
  ON asset_edges (from_asset, to_asset)
  WHERE superseded_at IS NULL;

-- column_edges uniqueness for the NULL partition_key case (most common).
-- Partition-aware uniqueness (partition_key IS NOT NULL case) is a deferred concern
-- (CONTEXT.md §D-13 open question); the NULL-key index covers Phase 4 requirements.
DROP INDEX IF EXISTS column_edges_active_unique;
CREATE UNIQUE INDEX column_edges_active_unique
  ON column_edges (from_asset, from_column, to_asset, to_column)
  WHERE superseded_at IS NULL AND partition_key IS NULL;
