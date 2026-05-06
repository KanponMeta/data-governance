-- Create "event_log" table
CREATE TABLE "event_log" (
  "id" uuid NOT NULL,
  "occurred_at" timestamptz NOT NULL,
  "event_type" character varying NOT NULL,
  "actor_id" uuid NULL,
  "resource_type" character varying NOT NULL,
  "resource_id" character varying NOT NULL,
  "payload" jsonb NULL,
  PRIMARY KEY ("id")
);
-- Create index "eventlog_event_type_occurred_at" to table: "event_log"
CREATE INDEX "eventlog_event_type_occurred_at" ON "event_log" ("event_type", "occurred_at");
-- Create index "eventlog_occurred_at" to table: "event_log"
CREATE INDEX "eventlog_occurred_at" ON "event_log" ("occurred_at");
-- Create index "eventlog_resource_type_resource_id" to table: "event_log"
CREATE INDEX "eventlog_resource_type_resource_id" ON "event_log" ("resource_type", "resource_id");
-- Create "invite_token" table
CREATE TABLE "invite_token" (
  "id" uuid NOT NULL,
  "token_hash" character varying NOT NULL,
  "email" character varying NOT NULL,
  "invited_by" uuid NOT NULL,
  "expires_at" timestamptz NOT NULL,
  "accepted_at" timestamptz NULL,
  "created_at" timestamptz NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "invitetoken_email" to table: "invite_token"
CREATE INDEX "invitetoken_email" ON "invite_token" ("email");
-- Create index "invitetoken_token_hash" to table: "invite_token"
CREATE UNIQUE INDEX "invitetoken_token_hash" ON "invite_token" ("token_hash");
-- Create "user" table
CREATE TABLE "user" (
  "id" uuid NOT NULL,
  "email" character varying NOT NULL,
  "password_hash" character varying NOT NULL,
  "role" character varying NOT NULL DEFAULT 'member',
  "status" character varying NOT NULL DEFAULT 'invited',
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "user_email" to table: "user"
CREATE UNIQUE INDEX "user_email" ON "user" ("email");
-- ===== Hand-managed: roles + immutability for event_log (D-09) =====
-- Idempotent: safe across re-runs.

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'platform_app') THEN
        CREATE ROLE platform_app NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'platform_owner') THEN
        CREATE ROLE platform_owner NOLOGIN;
    END IF;
END
$$;

-- Owner controls schema changes; app does runtime DML.
ALTER TABLE user         OWNER TO platform_owner;
ALTER TABLE invite_token OWNER TO platform_owner;
ALTER TABLE event_log    OWNER TO platform_owner;

-- Default DML privileges for app role.
GRANT SELECT, INSERT, UPDATE, DELETE ON user         TO platform_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON invite_token TO platform_app;

-- event_log is APPEND-ONLY for the application role.
GRANT SELECT, INSERT ON event_log TO platform_app;
REVOKE UPDATE, DELETE, TRUNCATE ON event_log FROM platform_app;

-- Belt-and-suspenders: RLS prevents any future grant from accidentally enabling mutation.
ALTER TABLE event_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE event_log FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS event_log_select ON event_log;
CREATE POLICY event_log_select ON event_log FOR SELECT TO platform_app USING (true);

DROP POLICY IF EXISTS event_log_insert ON event_log;
CREATE POLICY event_log_insert ON event_log FOR INSERT TO platform_app WITH CHECK (true);

-- No UPDATE / DELETE policy => denied for platform_app even if grants are accidentally added.
