-- Create "concurrency_tokens" table
CREATE TABLE "concurrency_tokens" (
  "id" uuid NOT NULL,
  "run_id" uuid NOT NULL,
  "asset_name" character varying(256) NOT NULL,
  "resource_tag" character varying(128) NOT NULL,
  "weight" bigint NOT NULL DEFAULT 1,
  "acquired_at" timestamptz NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "concurrencytoken_resource_tag" to table: "concurrency_tokens"
CREATE INDEX "concurrencytoken_resource_tag" ON "concurrency_tokens" ("resource_tag");
-- Create index "concurrencytoken_run_id" to table: "concurrency_tokens"
CREATE INDEX "concurrencytoken_run_id" ON "concurrency_tokens" ("run_id");
-- Create index "concurrencytoken_acquired_at" to table: "concurrency_tokens"
CREATE INDEX "concurrencytoken_acquired_at" ON "concurrency_tokens" ("acquired_at");

-- ===== Hand-managed: grants for concurrency_tokens (Phase 2 D-16) =====
-- Idempotent: safe across re-runs.

ALTER TABLE concurrency_tokens OWNER TO platform_owner;
GRANT SELECT, INSERT, UPDATE, DELETE ON concurrency_tokens TO platform_app;
