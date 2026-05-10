-- Phase 5 Plan 05-04: Governance workflow tables — review state machine,
-- team owners fallback for reviewer resolution, and event_log CHECK extension
-- for governance.materialization_blocked + reviewer_reassigned (event_log scope).
--
-- Filename note: 20260510000005 leaves the prior numeric prefixes for the
-- other Phase 5 plans (20260510000001 audit/RBAC, 20260510000002 column
-- policies, 20260510000003 quality, 20260510000004 reserved for plan 05-03).
--
-- This migration adds:
--   * governance_reviews — review state machine table (D-08, D-12)
--   * team_owners        — owner_email → reviewer roles fallback (D-09)
--   * event_log CHECK constraint extension to include
--     governance.materialization_blocked + governance.reviewer_reassigned +
--     SLA / quality / notification event_log subset (D-23).

BEGIN;

-- ============== governance_reviews (D-08, D-12) ==============
CREATE TABLE IF NOT EXISTS governance_reviews (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    asset_version_id         UUID NOT NULL REFERENCES asset_versions(id),
    asset                    VARCHAR(255) NOT NULL,
    code_hash                VARCHAR(64) NOT NULL,
    submitter_id             UUID NOT NULL REFERENCES "user"(id),
    submitted_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reviewer_pool_snapshot   JSONB NOT NULL,
    quorum                   INTEGER NOT NULL DEFAULT 1,
    require_human_review     BOOLEAN NOT NULL DEFAULT FALSE,
    escalation_roles         JSONB NOT NULL DEFAULT '[]'::jsonb,
    status                   VARCHAR(16) NOT NULL DEFAULT 'in_review',
    decided_at               TIMESTAMPTZ NULL,
    decided_by_id            UUID NULL REFERENCES "user"(id),
    comment                  TEXT NULL,
    sla_breach_emitted_at    TIMESTAMPTZ NULL,
    CONSTRAINT governance_reviews_status_check CHECK (status IN ('in_review','approved','rejected','auto_approved'))
);
CREATE INDEX IF NOT EXISTS governance_reviews_asset_active
    ON governance_reviews (asset) WHERE decided_at IS NULL;
CREATE INDEX IF NOT EXISTS governance_reviews_sla
    ON governance_reviews (submitted_at) WHERE decided_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS governance_reviews_active_per_version
    ON governance_reviews (asset_version_id) WHERE decided_at IS NULL;

ALTER TABLE governance_reviews OWNER TO platform_owner;
GRANT SELECT, INSERT, UPDATE ON governance_reviews TO platform_app;

-- ============== team_owners (D-09 fallback) ==============
-- Note: roles is JSONB ([]string semantics) — avoids lib/pq dependency
-- (matches Phase 5 Plan 05-02 column_policies.allow_roles pattern).
CREATE TABLE IF NOT EXISTS team_owners (
    owner_email VARCHAR(255) PRIMARY KEY,
    roles       JSONB NOT NULL DEFAULT '[]'::jsonb,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE team_owners OWNER TO platform_owner;
GRANT SELECT, INSERT, UPDATE, DELETE ON team_owners TO platform_app;

-- ============== Extend event_log CHECK to include Phase 5 event_log scope ==============
-- Per D-23: governance.materialization_blocked goes to BOTH event_log (every
-- blocked materialization) AND audit_log (access-control event). Quality /
-- SLA / notification events are event_log only — they are observability,
-- not access-control.
ALTER TABLE event_log DROP CONSTRAINT IF EXISTS event_log_event_type_check;
ALTER TABLE event_log
  ADD CONSTRAINT event_log_event_type_check
  CHECK (event_type IN (
    -- Phase 1
    'user.registered','user.invited','auth.login','auth.logout','auth.token_expired',
    'platform.started','platform.migration_applied',
    -- Phase 2
    'run.queued','run.started','run.step.started','run.step.succeeded','run.step.failed',
    'run.step.retry_scheduled','run.succeeded','run.failed','run.canceled',
    -- Phase 3
    'schedule.fired','schedule.missed','schedule.paused','schedule.resumed',
    'sensor.evaluated','sensor.fired','sensor.evaluation_failed','sensor.disabled',
    'sensor.cooldown_skipped','sensor.dedup_skipped',
    'backfill.submitted','backfill.run_enqueued','backfill.completed',
    -- Phase 4
    'lineage.captured','lineage.drift_detected',
    'schema.captured','schema.unchanged','schema.change_detected','schema.capture_failed','schema.break_acknowledged',
    'metadata.updated',
    -- Phase 5 (event_log scope per D-23 — non-hash-chain events)
    'governance.materialization_blocked','governance.reviewer_reassigned',
    'quality.rule_passed','quality.rule_failed','quality.rule_error','quality.run_evaluated',
    'sla.breached','sla.recovered',
    'notification.dispatched','notification.dispatch_failed'
  ));

COMMIT;
