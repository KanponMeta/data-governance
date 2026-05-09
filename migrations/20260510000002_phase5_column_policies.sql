-- Phase 5 (plan 05-02) migration: column_policies temporal table for column-level
-- access control policies. Implements D-07 — three-layer policy expression
-- (builder default + REST runtime + YAML tag default) with COALESCE precedence.
--
-- Filename note (deviation): original plan specified
-- 20260510000000_phase5_governance.sql but that timestamp is reserved by the
-- governance baseline. 20260510000001 is owned by plan 05-01 (audit + RBAC).
-- This plan owns 20260510000002 to avoid collision with plan 05-05 quality.
BEGIN;

-- ============== column_policies (D-07 temporal table; RBAC-03/04) ==============
CREATE TABLE IF NOT EXISTS column_policies (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    asset              VARCHAR(255) NOT NULL,
    column_name        VARCHAR(255) NOT NULL,
    mask_type          VARCHAR(32)  NOT NULL,
    -- allow_roles is JSONB ([]string semantics) — matches Phase 4 asset_metadata.tags
    -- pattern; pgx encodes/decodes via standard json.Marshal without lib/pq.
    allow_roles        JSONB        NOT NULL DEFAULT '[]'::jsonb,
    code_hash_first    VARCHAR(64)  NOT NULL,
    code_hash_latest   VARCHAR(64)  NOT NULL,
    first_seen_run_id  UUID NULL,
    first_seen_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    last_seen_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    superseded_at      TIMESTAMPTZ NULL,
    source             VARCHAR(16)  NOT NULL,
    reason             TEXT NOT NULL DEFAULT '',
    enforcement_mode   VARCHAR(16)  NOT NULL DEFAULT 'unknown',
    sync_status        VARCHAR(16)  NOT NULL DEFAULT 'pending',
    created_by_id      UUID NULL REFERENCES "user"(id),
    CONSTRAINT column_policies_mask_type_check CHECK (mask_type IN ('hash','redact','partial')),
    CONSTRAINT column_policies_source_check    CHECK (source IN ('builder','runtime','yaml-default')),
    CONSTRAINT column_policies_enforce_check   CHECK (enforcement_mode IN ('warehouse-native','in-pipeline','unknown')),
    CONSTRAINT column_policies_sync_status_check CHECK (sync_status IN ('pending','syncing','synced','failed'))
);

CREATE UNIQUE INDEX IF NOT EXISTS column_policies_active_unique
    ON column_policies (asset, column_name, source) WHERE superseded_at IS NULL;
CREATE INDEX IF NOT EXISTS column_policies_asset_active
    ON column_policies (asset) WHERE superseded_at IS NULL;
CREATE INDEX IF NOT EXISTS column_policies_first_seen_at
    ON column_policies (first_seen_at);
CREATE INDEX IF NOT EXISTS column_policies_last_seen_at
    ON column_policies (last_seen_at);

ALTER TABLE column_policies OWNER TO platform_owner;
GRANT SELECT, INSERT, UPDATE ON column_policies TO platform_app;

COMMIT;
