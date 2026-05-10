-- Phase 5 (plan 05-03) migration: column_pii_tags table for synchronous PII
-- propagation through column-level lineage (D-06).
--
-- Filename note (deviation): plan 05-03 originally proposed extending
-- asset_metadata with a pii_override_audit_seq column. asset_metadata is
-- append-only INSERT-only with FORCE ROW LEVEL SECURITY (Phase 4 D-17), and
-- its tags column carries JSONB []string. Both constraints prevent the
-- proposed UPSERT-with-JSONB-object pattern. Instead we introduce a dedicated
-- table that supports INSERT/UPDATE for the BFS-union-rule writer, isolates
-- governance state from append-only metadata history, and stores the audit
-- seq directly on the row for idempotency checks. (Rule 3 deviation: blocking)
BEGIN;

-- ============== column_pii_tags (D-06 PII propagation surface) ==============
CREATE TABLE IF NOT EXISTS column_pii_tags (
    asset                VARCHAR(255) NOT NULL,
    column_name          VARCHAR(255) NOT NULL,
    pii                  BOOLEAN      NOT NULL DEFAULT TRUE,
    -- source of the pii=true assignment:
    --   'upstream'   — propagated from a column_edges upstream that carries pii=true
    --   'override'   — declared via Builder.Column(c).TagOverride(asset.TagOverride{Add:"pii", ...})
    --   'manual'     — runtime PATCH (deferred to plan 06)
    source               VARCHAR(16)  NOT NULL DEFAULT 'upstream',
    source_run_id        UUID NULL,
    -- For overrides that REMOVE pii:
    override_reason      TEXT NULL,
    override_actor_id    UUID NULL,
    -- pii_override_audit_seq records the audit_log seq of the FIRST
    -- metadata.tag_overridden entry written for this (asset, column).
    -- A non-NULL value short-circuits subsequent same-code-hash overrides
    -- so the propagator does not emit a duplicate audit entry.
    pii_override_audit_seq BIGINT NULL,
    -- propagated_from is a JSONB array of upstream column refs that contributed
    -- to this row's pii=true assignment (operator visibility).
    propagated_from      JSONB NOT NULL DEFAULT '[]'::jsonb,
    set_at               TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    set_by               UUID NULL,
    PRIMARY KEY (asset, column_name),
    CONSTRAINT column_pii_tags_source_check CHECK (source IN ('upstream','override','manual'))
);

CREATE INDEX IF NOT EXISTS column_pii_tags_pii_true
    ON column_pii_tags (asset, column_name) WHERE pii = TRUE;

ALTER TABLE column_pii_tags OWNER TO platform_owner;
GRANT SELECT, INSERT, UPDATE ON column_pii_tags TO platform_app;

COMMIT;
