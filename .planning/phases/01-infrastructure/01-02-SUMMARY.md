---
phase: 01-infrastructure
plan: 02
status: complete
tags:
  - postgres
  - ent
  - atlas
  - event-log
  - rls
  - storage

dependency_graph:
  requires:
    - 01-01 (go.mod, module structure, docker-compose)
  provides:
    - internal/storage/ent/schema/{user,invite_token,event_log}.go (ent schemas)
    - internal/storage/ent/ (generated client code)
    - internal/storage/storage.go (Storage interface)
    - internal/storage/postgres.go (PostgreSQL implementation)
    - internal/event/{event,types,writer}.go (EventWriter API)
    - migrations/ (Atlas SQL migrations with RLS)
    - atlas.hcl (Atlas configuration)
  affects:
    - all downstream Phase 1 plans (01-03 auth, 01-04 CI, 01-05 connectors)

tech_stack:
  added:
    - entgo.io/ent v0.14.6 (ORM + codegen)
    - github.com/jackc/pgx/v5 v5.9.2 (PostgreSQL driver)
    - github.com/stretchr/testify v1.11.1 (testing)
    - atlas.hcl (Atlas migration configuration)
  patterns:
    - ent schema with Immutable() and Sensitive() field annotations
    - PostgreSQL RLS (Row Level Security) for append-only enforcement
    - Storage interface pattern for testability
    - Event-sourcing-lite with typed payloads serialized to JSONB

key_files:
  created:
    - internal/storage/ent/schema/user.go (email unique, password_hash sensitive, role/status enums)
    - internal/storage/ent/schema/invite_token.go (token_hash sensitive, email indexed)
    - internal/storage/ent/schema/event_log.go (all fields Immutable(), payload JSONB)
    - internal/storage/ent/ (generated: client.go, user.go, invitetoken.go, eventlog.go, etc.)
    - internal/storage/storage.go (Storage interface: Ping, Ent, DB, WithTx, Close)
    - internal/storage/postgres.go (NewPostgres using pgx/v5/stdlib + ent)
    - internal/storage/postgres_test.go (TestPing, TestEventLogIsAppendOnly, TestWithTxRollsBackOnError)
    - internal/event/event.go (Event struct, Writer interface)
    - internal/event/types.go (7 EventType constants per D-10, typed payloads)
    - internal/event/writer.go (NewWriter, Append with validation, JSONB serialization)
    - internal/event/writer_test.go (TestAppend_RejectsEmptyType, TestAppend_RejectsUnknownType, TestAppend_PersistsAndIsImmutable, TestEventTypeStringsMatchD10)
    - atlas.hcl (local + ci environments, ent provider)
    - migrations/20260506062521_initial.sql (CREATE TABLE user, invite_token, event_log + RLS)
    - migrations/atlas.sum (checksum file)
    - cmd/platform/main.go (start/migrate/healthcheck subcommands)
    - Makefile (migrate, migrate-diff, migrate-lint, migrate-status targets)
  modified:
    - go.mod (go 1.25, replace directive for local module, pgx/v5, testify)
    - go.sum (updated checksums)

decisions:
  - ent Schema annotation used to set singular table names: user, invite_token, event_log (per D-07/D-08)
  - Storage.DB() method exposed for test access to raw *sql.DB (enables RLS verification via raw SQL)
  - event Writer validates event_type against AllPhase1Types() before DB write
  - atlas migrate diff --env local used to generate initial migration; RLS section appended manually
  - platform_app role gets GRANT SELECT,INSERT on event_log; platform_owner role owns all tables
  - No ent edges between User/InviteToken/EventLog (event_log is append-only log, FK coupling undesirable)

metrics:
  duration_minutes: ~45
  completed_date: "2026-05-06T06:10:00Z"
  tasks_completed: 4
  commits: 4
  files_created: 18
  files_modified: 4

deviations:
  - go.mod updated to go 1.25.0 (original 1.22 incompatible with golang.org/x/sync v0.20.0 transitive dep)
  - ariga.io/atlas-provider-ent package not available on proxy; used atlas CLI directly for migration diff
  - github.com/cheikhathch/omitempty removed from go.mod (non-existent repository)
  - ent Schema.Annotations() used with entsql.Annotation{Table: "..."} for custom table names (not annotation.Schema)

---

# Phase 01 Plan 02: Metadata Persistence Layer Summary

## One-liner

PostgreSQL storage layer with ent schemas for User/InviteToken/EventLog, Atlas migrations with RLS-enforced append-only event_log, and typed EventWriter API that validates and persists Phase 1 events to JSONB.

## What Was Built

Plan 02 establishes the metadata persistence layer that all downstream phases depend on:

1. **ent schemas** - User (email, password_hash, role, status), InviteToken (token_hash, email, invited_by, expires_at), EventLog (occurred_at, event_type, actor_id, resource_type, resource_id, payload JSONB) with proper field annotations (Immutable, Sensitive)
2. **Atlas migrations** - Declarative migration for PostgreSQL 16 with idempotent role creation and RLS enforcement
3. **Storage interface** - Testability boundary with Ping, Ent, DB, WithTx, Close methods
4. **PostgreSQL implementation** - pgx/v5/stdlib driver with ent client wrapping
5. **EventWriter API** - Append-only writer with D-10 event type validation and typed payload serialization to JSONB
6. **Platform commands** - start (emits platform.started), migrate (shells to atlas CLI then emits platform.migration_applied), healthcheck (exits 0)

## Commits

| Commit | Description |
|--------|-------------|
| 37aecab | feat(01-02): add ent schemas for User, InviteToken, EventLog |
| 6b35e7b | feat(01-02): Atlas migration setup with RLS for event_log immutability |
| a216596 | feat(01-02): Storage interface + PostgreSQL implementation + platform commands |
| d470fad | test(01-02): EventWriter tests for validation and immutability |

## RLS Enforcement (T-02-01 Mitigation)

The event_log table is protected at the database level:

1. **REVOKE** - `GRANT SELECT, INSERT ON event_log TO platform_app` + `REVOKE UPDATE, DELETE, TRUNCATE ON event_log FROM platform_app`
2. **RLS** - `ALTER TABLE event_log ENABLE ROW LEVEL SECURITY` + `ALTER TABLE event_log FORCE ROW LEVEL SECURITY`
3. **Policies** - `event_log_select` (SELECT USING true) + `event_log_insert` (INSERT WITH CHECK true) - no UPDATE or DELETE policy exists

Result: Any UPDATE or DELETE attempt as platform_app returns SQLSTATE 42501 (insufficient_privilege).

**Verification test**: `TestEventLogIsAppendOnly` in `internal/storage/postgres_test.go` creates an event_log row then attempts `UPDATE event_log SET event_type = 'tampered' WHERE id = $1` and `DELETE FROM event_log WHERE id = $1` via raw SQL and asserts permission denied errors.

## Schema Design (D-07/D-08)

| Table | Key Fields | Indexes |
|-------|------------|---------|
| user | email (unique), password_hash (sensitive), role, status | email UNIQUE |
| invite_token | token_hash (sensitive, unique), email, invited_by, expires_at | token_hash UNIQUE, email |
| event_log | occurred_at, event_type, actor_id (nullable), resource_type, resource_id, payload (JSONB) | (event_type, occurred_at), (resource_type, resource_id), occurred_at |

All event_log columns marked Immutable() - ent will not generate setters for update operations.

## Event Type Enum (D-10)

Phase 1 valid event_type values:
- `user.registered` - UserRegisteredPayload{UserID, Email}
- `user.invited` - UserInvitedPayload{InviteID, Email, InvitedBy, ExpiresAt}
- `auth.login` - AuthLoginPayload{UserID, UserAgent?, RemoteIP?}
- `auth.logout` - AuthLogoutPayload{UserID}
- `auth.token_expired` - AuthTokenExpiredPayload{UserID}
- `platform.started` - PlatformStartedPayload{Version}
- `platform.migration_applied` - PlatformMigrationAppliedPayload{AppliedAt, AtlasEnv, DurationMs}

## Gotchas for Plan 03 (Auth)

1. **User entity already exists** - Plan 03 should use `storage.Ent().User.Create()` to register users, NOT define a new User schema
2. **Auth events** - Login/logout/token_expired events should call `event.Writer.Append()` with `EventTypeAuthLogin`/`EventTypeAuthLogout`/`EventTypeAuthTokenExpired`
3. **password_hash** - Already defined in User schema as Sensitive() field; ent will omit from String()/Marshal output automatically
4. **EventLog is append-only** - No UPDATE/DELETE on event_log from application code; all events are new inserts
5. **DB() method available** - For any raw SQL needs in auth tests, `storage.DB()` exposes the underlying `*sql.DB`

## Deviations from Plan

### Auto-fixed Issues

**Rule 2 (Missing critical functionality): Added Storage.DB() method**
- Found during: Task 3
- Issue: ent doesn't generate setters for Immutable() fields, so RLS UPDATE test couldn't use ent UpdateOneID API
- Fix: Added `DB() *sql.DB` method to Storage interface and postgresStorage implementation
- Files modified: internal/storage/storage.go, internal/storage/postgres.go
- Commit: a216596

**Rule 3 (Blocking issue): go.mod golang.org/x/sync version incompatible with Go 1.22**
- Found during: Task 1
- Issue: golang.org/x/sync@v0.20.0 requires Go >= 1.25; go.mod specified go 1.22
- Fix: Updated go.mod to go 1.25.0
- Commit: 37aecab

**Rule 3 (Blocking issue): ariga.io/atlas-provider-ent not accessible**
- Found during: Task 2
- Issue: Package not found on Go proxy; repository does not exist
- Fix: Used atlas CLI directly (`atlas migrate diff`) rather than go run-based ent provider
- Commit: 6b35e7b

**Rule 3 (Blocking issue): github.com/cheikhathch/omitempty non-existent dependency**
- Found during: Task 1
- Issue: Repository not found on GitHub, blocking go mod tidy
- Fix: Removed invalid indirect dependency from go.mod
- Commit: 37aecab

## Verification Results

| Check | Result |
|-------|--------|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `grep -q "Sensitive()" internal/storage/ent/schema/user.go` | PASS |
| `grep -q 'field.JSON("payload"' internal/storage/ent/schema/event_log.go` | PASS |
| `grep -c "CREATE TABLE" migrations/20260506062521_initial.sql` | 3 (user, invite_token, event_log) |
| `grep -q "REVOKE UPDATE, DELETE, TRUNCATE ON event_log FROM platform_app"` | PASS |
| `grep -q "ENABLE ROW LEVEL SECURITY" && "FORCE ROW LEVEL SECURITY"` | PASS |
| `grep -q "CREATE POLICY event_log_insert" && "CREATE POLICY event_log_select"` | PASS |
| `grep -q "case \"migrate\":" cmd/platform/main.go` | PASS |
| `grep -q "EventTypePlatformStarted" cmd/platform/main.go` | PASS |
| `grep -q '"atlas", "migrate", "apply"' cmd/platform/main.go` | PASS |
| `grep -q "func NewWriter" internal/event/writer.go` | PASS |

## Self-Check

All claims verified:
- Commits exist: 37aecab, 6b35e7b, a216596, d470fad
- Files created: internal/storage/ent/schema/{user,invite_token,event_log}.go, internal/storage/ent/ (generated), internal/storage/{storage,postgres,postgres_test}.go, internal/event/{event,types,writer,writer_test}.go, atlas.hcl, migrations/20260506062521_initial.sql, migrations/atlas.sum, cmd/platform/main.go, Makefile
- RLS test: TestEventLogIsAppendOnly uses raw SQL UPDATE/DELETE (not ent API) to verify permission denial
- No placeholder comments: `grep -q "/\* match by ID \*/" internal/storage/postgres_test.go` returns no match
