-- Create "backfills" table
CREATE TABLE "backfills" (
  "id" uuid NOT NULL,
  "asset_name" character varying(256) NOT NULL,
  "partition_spec" character varying(1024) NOT NULL,
  "status" character varying(16) NOT NULL DEFAULT 'submitted',
  "total_partitions" bigint NOT NULL DEFAULT 0,
  "submitted_at" timestamptz NOT NULL,
  "completed_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- Create index "backfill_asset_name" to table: "backfills"
CREATE INDEX "backfill_asset_name" ON "backfills" ("asset_name");
-- Create index "backfill_status_submitted_at" to table: "backfills"
CREATE INDEX "backfill_status_submitted_at" ON "backfills" ("status", "submitted_at");

-- Create "schedules" table
CREATE TABLE "schedules" (
  "id" uuid NOT NULL,
  "asset_name" character varying(256) NOT NULL,
  "cron_expr" character varying(128) NOT NULL,
  "last_fire_at" timestamptz NULL,
  "next_fire_at" timestamptz NULL,
  "paused_at" timestamptz NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "schedule_asset_name" to table: "schedules"
CREATE INDEX "schedule_asset_name" ON "schedules" ("asset_name");
-- Create index "schedule_next_fire_at" to table: "schedules"
CREATE INDEX "schedule_next_fire_at" ON "schedules" ("next_fire_at");
-- Create index "schedule_paused_at_next_fire_at" to table: "schedules"
CREATE INDEX "schedule_paused_at_next_fire_at" ON "schedules" ("paused_at", "next_fire_at");

-- Create "sensors" table
CREATE TABLE "sensors" (
  "id" uuid NOT NULL,
  "asset_name" character varying(256) NOT NULL,
  "sensor_name" character varying(128) NOT NULL,
  "min_interval_seconds" bigint NOT NULL DEFAULT 30,
  "last_evaluated_at" timestamptz NULL,
  "last_fired_at" timestamptz NULL,
  "last_run_key" character varying(256) NULL,
  "cooldown_until" timestamptz NULL,
  "consecutive_failures" bigint NOT NULL DEFAULT 0,
  "disabled_at" timestamptz NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "sensor_asset_name_sensor_name" to table: "sensors"
CREATE INDEX "sensor_asset_name_sensor_name" ON "sensors" ("asset_name", "sensor_name");
-- Create index "sensor_disabled_at_last_evaluated_at" to table: "sensors"
CREATE INDEX "sensor_disabled_at_last_evaluated_at" ON "sensors" ("disabled_at", "last_evaluated_at");

-- ===== Hand-managed: Phase 3 role grants (idempotent) =====
-- Mirrors Phase 2 pattern from migrations/20260507121500_phase2_concurrency_tokens.sql.
-- platform_owner retains DDL ownership; only platform_app gets DML.

ALTER TABLE schedules OWNER TO platform_owner;
ALTER TABLE sensors   OWNER TO platform_owner;
ALTER TABLE backfills OWNER TO platform_owner;

GRANT SELECT, INSERT, UPDATE, DELETE ON schedules TO platform_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON sensors   TO platform_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON backfills TO platform_app;
