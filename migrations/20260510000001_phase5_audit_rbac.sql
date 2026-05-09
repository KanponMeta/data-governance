-- Phase 5 (plan 05-01) migration: audit schema + hash-chain + RBAC tables + RLS.
-- This is the exclusive migration for plan 05-01. Each subsequent Phase 5 plan
-- (05-02, 05-04, 05-05) owns its own timestamped migration file.
--
-- Plan 05-01 task list:
--   Task 1a: audit schema + hash-chain + asset_versions.governance_state
--   Task 2:  roles + role_assignments + casbin_rule (appended below)
BEGIN;

-- ============== Phase 5 Roles (D-13, Pitfall #5) ==============
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='audit_migrator') THEN
        CREATE ROLE audit_migrator NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='audit_purge') THEN
        CREATE ROLE audit_purge NOLOGIN;
    END IF;
END $$;

-- ============== Audit Schema + Hash-Chain (D-13/D-14/D-16) ==============
CREATE SCHEMA IF NOT EXISTS audit AUTHORIZATION audit_migrator;

CREATE TABLE IF NOT EXISTS audit.audit_log (
    seq           BIGSERIAL PRIMARY KEY,
    prev_hash     BYTEA NOT NULL,
    self_hash     BYTEA NOT NULL,
    occurred_at   TIMESTAMPTZ NOT NULL,
    event_type    TEXT NOT NULL,
    actor_id      UUID NULL,
    resource_type TEXT NOT NULL,
    resource_id   TEXT NOT NULL,
    payload       JSONB NOT NULL,
    expires_at    TIMESTAMPTZ NULL,
    CONSTRAINT audit_log_event_type_check CHECK (event_type IN (
        'policy.changed','policy.removed','masking.sync_failed','masking.sync_drift_detected',
        'role.created','role.deleted','role.assigned','role.revoked',
        'governance.submitted','governance.approved','governance.rejected',
        'governance.auto_approved','governance.review_sla_breached','governance.materialization_blocked',
        'governance.reviewer_reassigned',
        'audit.exported','audit.verify_failed','metadata.tag_overridden'
    ))
);
CREATE INDEX IF NOT EXISTS audit_log_event_type_occurred_at ON audit.audit_log(event_type, occurred_at);
CREATE INDEX IF NOT EXISTS audit_log_resource ON audit.audit_log(resource_type, resource_id);
CREATE INDEX IF NOT EXISTS audit_log_expires_at ON audit.audit_log(expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS audit.audit_sentinel (
    id        SMALLINT PRIMARY KEY DEFAULT 1,
    seq       BIGINT NOT NULL DEFAULT 0,
    self_hash BYTEA NOT NULL DEFAULT decode('0000000000000000000000000000000000000000000000000000000000000000','hex'),
    CHECK (id = 1)
);
INSERT INTO audit.audit_sentinel (id, seq, self_hash)
VALUES (1, 0, decode('0000000000000000000000000000000000000000000000000000000000000000','hex'))
ON CONFLICT DO NOTHING;

ALTER SCHEMA  audit                OWNER TO audit_migrator;
ALTER TABLE   audit.audit_log      OWNER TO audit_migrator;
ALTER TABLE   audit.audit_sentinel OWNER TO audit_migrator;

GRANT USAGE   ON SCHEMA audit                                        TO platform_app;
GRANT SELECT, INSERT ON audit.audit_log                              TO platform_app;
GRANT USAGE   ON SEQUENCE audit.audit_log_seq_seq                    TO platform_app;
GRANT SELECT, UPDATE ON audit.audit_sentinel                         TO platform_app;
REVOKE UPDATE, DELETE, TRUNCATE ON audit.audit_log                   FROM platform_app;

GRANT USAGE   ON SCHEMA audit                                        TO audit_purge;
GRANT DELETE  ON audit.audit_log                                     TO audit_purge;
REVOKE INSERT, UPDATE, TRUNCATE ON audit.audit_log                   FROM audit_purge;

ALTER TABLE audit.audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.audit_log FORCE  ROW LEVEL SECURITY;

CREATE POLICY audit_log_select ON audit.audit_log
  FOR SELECT TO platform_app USING (true);
CREATE POLICY audit_log_insert ON audit.audit_log
  FOR INSERT TO platform_app WITH CHECK (true);
CREATE POLICY audit_log_purge_delete ON audit.audit_log
  FOR DELETE TO audit_purge USING (expires_at IS NOT NULL AND expires_at < NOW());

-- ============== asset_versions.governance_state (D-08) ==============
ALTER TABLE asset_versions ADD COLUMN IF NOT EXISTS
  governance_state VARCHAR(16) NOT NULL DEFAULT 'draft';
ALTER TABLE asset_versions DROP CONSTRAINT IF EXISTS asset_versions_governance_state_check;
ALTER TABLE asset_versions
  ADD CONSTRAINT asset_versions_governance_state_check
  CHECK (governance_state IN ('draft','in_review','active','rejected'));

-- ============== Casbin policy table (D-01) ==============
CREATE TABLE IF NOT EXISTS casbin_rule (
    id    SERIAL PRIMARY KEY,
    ptype VARCHAR(100) NOT NULL,
    v0    VARCHAR(100),
    v1    VARCHAR(100),
    v2    VARCHAR(100),
    v3    VARCHAR(100),
    v4    VARCHAR(100),
    v5    VARCHAR(100)
);
CREATE UNIQUE INDEX IF NOT EXISTS casbin_rule_ptype_v0_v1_v2_v3_v4_v5
    ON casbin_rule (ptype, v0, v1, v2, v3, v4, v5);
ALTER TABLE casbin_rule OWNER TO platform_owner;
GRANT SELECT, INSERT, UPDATE, DELETE ON casbin_rule TO platform_app;
GRANT USAGE ON SEQUENCE casbin_rule_id_seq TO platform_app;

-- ============== roles + role_assignments (RBAC-01/02) ==============
CREATE TABLE IF NOT EXISTS roles (
    name          VARCHAR(64) PRIMARY KEY,
    description   TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by_id UUID NULL REFERENCES "user"(id)
);
ALTER TABLE roles OWNER TO platform_owner;
GRANT SELECT, INSERT, UPDATE, DELETE ON roles TO platform_app;

CREATE TABLE IF NOT EXISTS role_assignments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES "user"(id),
    role_name       VARCHAR(64) NOT NULL REFERENCES roles(name),
    granted_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    granted_by_id   UUID NULL REFERENCES "user"(id),
    revoked_at      TIMESTAMPTZ NULL,
    revoked_by_id   UUID NULL REFERENCES "user"(id)
);
CREATE INDEX IF NOT EXISTS role_assignments_user_active
    ON role_assignments (user_id) WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS role_assignments_active_unique
    ON role_assignments (user_id, role_name) WHERE revoked_at IS NULL;
ALTER TABLE role_assignments OWNER TO platform_owner;
GRANT SELECT, INSERT, UPDATE ON role_assignments TO platform_app;

-- Seed default policies (idempotent via unique index).
INSERT INTO casbin_rule (ptype, v0, v1, v2) VALUES
    ('p', 'role:data-engineer', '/assets/*/manage',    'write'),
    ('p', 'role:data-engineer', '/governance/submit',   'write'),
    ('p', 'role:governance',    '/governance/reviews/*','write'),
    ('p', 'role:governance',    '/policies/*',          'write'),
    ('p', 'role:admin',         '/users/*',              'manage'),
    ('p', 'role:admin',         '/audit/export',         'read'),
    ('p', 'role:admin',         '/audit/verify',         'read'),
    ('p', 'role:admin',         '/governance/reviews/*', 'manage')
ON CONFLICT (ptype, v0, v1, v2, v3, v4, v5) DO NOTHING;

COMMIT;
