-- Phase 5 Plan 05-05: Data Quality + Freshness SLA + Notifications schema.
--
-- This migration adds:
--   * quality_rules         — derived per (asset, code_hash, rule_name); D-18.
--   * quality_results       — per-rule outcome attached to a run; D-19.
--   * runs.run_quality_status — independent column tracking the worst rule outcome.
--                               runs.state is NOT mutated (Phase 1 D-09 + Phase 4 D-04 alignment).
--   * schedules.last_succeeded_at         — updated by executor.commitSuccess.
--   * schedules.freshness_max_lag_seconds — non-null when asset has FreshnessSLA.
--   * schedules.freshness_breach_emitted_at — dedup window marker for SLA scanner.
--
-- Note: filename uses 20260510000003 because Plan 05-02 (running in parallel) is
-- using 20260510000002 for column policies. See SUMMARY for cross-plan rename.

BEGIN;

-- ============== quality_rules (D-18) ==============
CREATE TABLE IF NOT EXISTS quality_rules (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    asset        VARCHAR(255) NOT NULL,
    code_hash    VARCHAR(64) NOT NULL,
    rule_name    VARCHAR(128) NOT NULL,
    rule_type    VARCHAR(32) NOT NULL,
    config_json  JSONB NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT quality_rules_type_check CHECK (rule_type IN ('null_check','range_check','sql_assertion'))
);
CREATE UNIQUE INDEX IF NOT EXISTS quality_rules_active
    ON quality_rules (asset, code_hash, rule_name);
ALTER TABLE quality_rules OWNER TO platform_owner;
GRANT SELECT, INSERT, UPDATE ON quality_rules TO platform_app;

-- ============== quality_results (D-19) ==============
CREATE TABLE IF NOT EXISTS quality_results (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id          UUID NOT NULL REFERENCES runs(id),
    rule_name       VARCHAR(128) NOT NULL,
    rule_type       VARCHAR(32) NOT NULL,
    status          VARCHAR(16) NOT NULL,
    measured_value  DOUBLE PRECISION NULL,
    threshold       DOUBLE PRECISION NULL,
    evaluated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    error_message   TEXT NULL,
    CONSTRAINT quality_results_status_check CHECK (status IN ('passed','failed','error'))
);
CREATE INDEX IF NOT EXISTS quality_results_run ON quality_results(run_id);
CREATE INDEX IF NOT EXISTS quality_results_rule ON quality_results(rule_name, evaluated_at);
ALTER TABLE quality_results OWNER TO platform_owner;
GRANT SELECT, INSERT, UPDATE ON quality_results TO platform_app;

-- ============== runs.run_quality_status (D-19 independent column) ==============
ALTER TABLE runs ADD COLUMN IF NOT EXISTS run_quality_status VARCHAR(16) NULL;
ALTER TABLE runs DROP CONSTRAINT IF EXISTS runs_run_quality_status_check;
ALTER TABLE runs
    ADD CONSTRAINT runs_run_quality_status_check
    CHECK (run_quality_status IS NULL OR run_quality_status IN ('passed','failed','error','skipped'));

-- ============== schedules SLA columns (D-20) ==============
ALTER TABLE schedules ADD COLUMN IF NOT EXISTS last_succeeded_at TIMESTAMPTZ NULL;
ALTER TABLE schedules ADD COLUMN IF NOT EXISTS freshness_max_lag_seconds INTEGER NULL;
ALTER TABLE schedules ADD COLUMN IF NOT EXISTS freshness_breach_emitted_at TIMESTAMPTZ NULL;
CREATE INDEX IF NOT EXISTS schedules_freshness_sla
    ON schedules (last_succeeded_at) WHERE freshness_max_lag_seconds IS NOT NULL;

COMMIT;
