---
phase: 3
plan: 01
title: Phase 3 schema migration + ent entities + event_type extensions
type: execute
wave: 1
depends_on: []
requirements: [ORCH-05, ORCH-06, ORCH-07, ORCH-08]
decisions_implemented: [D-02, D-05, D-10, D-13, D-15, D-17]
files_modified:
  - internal/storage/ent/schema/schedule.go
  - internal/storage/ent/schema/sensor.go
  - internal/storage/ent/schema/backfill.go
  - migrations/20260508120000_phase3_runs_columns.sql
  - migrations/20260508121000_phase3_schedules_sensors_backfills.sql
  - migrations/atlas.sum
  - internal/event/types.go
  - internal/event/types_test.go
  - go.mod
  - go.sum
autonomous: true
must_haves:
  truths:
    - "runs table has nullable partition_key VARCHAR(128), priority VARCHAR(16) NOT NULL DEFAULT 'normal' (CHECK in critical|normal|backfill), and nullable backfill_id UUID"
    - "Partial unique index `(asset_name, partition_key) WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL` rejects duplicate in-flight partition runs"
    - "schedules / sensors / backfills tables exist with the columns described in 03-RESEARCH.md Pattern 2"
    - "All thirteen Phase 3 EventType constants from D-17 are present in internal/event/types.go AllKnownTypes()"
    - "platform_app role has SELECT/INSERT/UPDATE/DELETE on schedules, sensors, backfills"
    - "Existing TestClaimAtomicity50Goroutines still passes after migration applied"
  artifacts:
    - path: "migrations/20260508120000_phase3_runs_columns.sql"
      provides: "ALTER runs ADD COLUMN partition_key/priority/backfill_id + CHECK + partial unique index + claim index"
      contains: "ADD COLUMN partition_key"
    - path: "migrations/20260508121000_phase3_schedules_sensors_backfills.sql"
      provides: "CREATE TABLE schedules/sensors/backfills + role grants"
      contains: "CREATE TABLE \"schedules\""
    - path: "internal/storage/ent/schema/schedule.go"
      provides: "Schedule ent entity"
      contains: "type Schedule struct"
    - path: "internal/storage/ent/schema/sensor.go"
      provides: "Sensor ent entity"
      contains: "type Sensor struct"
    - path: "internal/storage/ent/schema/backfill.go"
      provides: "Backfill ent entity"
      contains: "type Backfill struct"
    - path: "internal/event/types.go"
      provides: "Phase 3 EventType constants"
      contains: "EventTypeScheduleFired"
  key_links:
    - from: "runs.partition_key column"
      to: "Partial unique index (asset_name, partition_key)"
      via: "WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL"
      pattern: "CREATE UNIQUE INDEX.*partition_key.*WHERE state IN"
    - from: "runs.priority column"
      to: "claim.go priority-aware ORDER BY (plan 03-03)"
      via: "VARCHAR(16) NOT NULL DEFAULT 'normal' with CHECK (priority IN ('critical','normal','backfill'))"
      pattern: "CHECK \\(priority IN"
    - from: "internal/event/types.go AllKnownTypes()"
      to: "internal/event/writer.go validation"
      via: "writer.Append validates evt.Type via AllKnownTypes()"
      pattern: "AllKnownTypes"
---

<objective>
Land the foundational schema and event-type changes that all subsequent Phase 3 plans depend on. This is the smallest possible Wave 1 plan that unlocks everything else: ent entities + Atlas migrations for `schedules`, `sensors`, `backfills` tables; additive `partition_key`, `priority`, `backfill_id` columns on `runs` (with CHECK + partial unique index + claim index); thirteen new `EventType` constants per D-17; matching role grants for `platform_app`.

No business logic — purely additive schema. Every other Phase 3 plan unlocks once this lands.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
This plan implements D-02 (schedules table), D-05 (sensors table), D-10 (partition_key column + partial unique index), D-13 layer 1 (priority column + CHECK + claim-supporting index), D-15 (backfill_id column + backfills table), D-17 (event_type extensions).

**Why Wave 1 with zero deps:** This plan touches `internal/storage/ent/schema/`, new `migrations/*.sql` files, and `internal/event/types.go`. Plan 03-02 touches `internal/asset/*` and `internal/partition/*` — zero file overlap, both safe to run in parallel.

**Migration ordering:** Two migration files are needed because `migrations/20260508120000_phase3_runs_columns.sql` extends an existing table (idempotent ALTER) while `migrations/20260508121000_phase3_schedules_sensors_backfills.sql` creates new tables. Atlas applies them in lexical order; the `_120000_` prefix ensures runs columns land before any code reads them, then schedules/sensors/backfills tables follow.

**Atlas hand-managed appendix:** Phase 2 established the pattern of appending CHECK constraints, role grants, and partial unique indexes by hand at the end of the migration file (see `migrations/20260507120000_phase2_run_tables.sql` lines 48-72). Phase 3 follows that pattern verbatim. The ent schema generates the column definitions and standard indexes; CHECK / partial unique / role grants are appended in the SQL file directly.

**Why partial unique index (not regular unique):** A succeeded partition run must be re-runnable. A regular `UNIQUE (asset_name, partition_key)` would reject the second queued row even when the first is `succeeded`. The `WHERE state IN ('queued','starting','running')` predicate scopes uniqueness to in-flight runs only — Pitfall 7 in 03-RESEARCH.md.

**Why a separate index for the claim path:** Plan 03-03 changes `ORDER BY queued_at` to `ORDER BY CASE priority WHEN 'critical' THEN 0 ... END, queued_at`. The existing `(state, queued_at)` index is no longer optimal once priority is introduced. We create `(state, priority, queued_at)` so PostgreSQL can serve the priority-aware claim query without an in-memory sort. The existing `(state, queued_at)` index is left in place — dropping it requires a separate study under load.

**Frozen interfaces consumed:**
- `internal/storage.Storage.Ent()`, `Storage.DB()` (Phase 1 frozen)
- `internal/event.Event` struct, `event.Writer.Append` (Phase 1 frozen)
- ent + Atlas migration toolchain (Phase 1 D-04 frozen)

@.planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md
@.planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md
@.planning/phases/03-scheduling-sensors-partitions/03-VALIDATION.md
@.planning/phases/01-infrastructure/01-CONTEXT.md
@.planning/phases/02-execution-engine/02-02-PLAN.md
@migrations/20260507120000_phase2_run_tables.sql
@migrations/20260507121500_phase2_concurrency_tokens.sql
@internal/storage/ent/schema/run.go
@internal/storage/ent/schema/concurrency_token.go
@internal/event/types.go

<interfaces>
<!-- Existing event types this plan extends. Executor uses these directly — no exploration needed. -->

From internal/event/types.go (Phase 1+2 baseline):
```go
type EventType string

// Phase 1 + Phase 2 constants already present:
EventTypeUserRegistered, EventTypeUserInvited, EventTypeAuthLogin, EventTypeAuthLogout,
EventTypeAuthTokenExpired, EventTypePlatformStarted, EventTypePlatformMigrationApplied,
EventTypeRunQueued, EventTypeRunStarted, EventTypeRunStepStarted, EventTypeRunStepSucceeded,
EventTypeRunStepFailed, EventTypeRunStepRetryScheduled, EventTypeRunSucceeded, EventTypeRunFailed,
EventTypeRunCanceled

func AllKnownTypes() []EventType  // returns ALL valid types — extend with Phase 3 constants
```

From migrations/20260507120000_phase2_run_tables.sql (Phase 2 baseline runs schema):
```sql
CREATE TABLE "runs" (id, asset_name, state, trigger, triggered_by, claimed_by,
                     queued_at, claimed_at, started_at, finished_at, last_heartbeat,
                     error_message, metadata);
CREATE INDEX "run_state_queued_at" ON "runs" ("state", "queued_at");
ALTER TABLE runs ADD CONSTRAINT runs_state_check
  CHECK (state IN ('queued','starting','running','succeeded','failed','canceled'));
```

From internal/storage/ent/schema/run.go (Phase 2 baseline):
```go
type Run struct{ ent.Schema }
// Run ent fields already present: id, asset_name, state, trigger, triggered_by,
// claimed_by, queued_at, claimed_at, started_at, finished_at, last_heartbeat,
// error_message, metadata
// Indexes already present: (state,queued_at), (asset_name,queued_at), (queued_at), (state,last_heartbeat)
```

Phase 3 Run ent additions (this plan delivers in task 1):
```go
field.String("partition_key").Optional().MaxLen(128),                  // nullable VARCHAR(128)
field.String("priority").NotEmpty().MaxLen(16).Default("normal"),       // CHECK added in SQL appendix
field.UUID("backfill_id", uuid.UUID{}).Optional().Nillable(),
```

Phase 3 EventType constants to add (D-17 verbatim) — task 3 verifies via TestAllPhase3EventTypes:
```go
// Schedule (4)
EventTypeScheduleFired   = "schedule.fired"
EventTypeScheduleMissed  = "schedule.missed"
EventTypeSchedulePaused  = "schedule.paused"
EventTypeScheduleResumed = "schedule.resumed"
// Sensor (6)
EventTypeSensorEvaluated        = "sensor.evaluated"
EventTypeSensorFired            = "sensor.fired"
EventTypeSensorEvaluationFailed = "sensor.evaluation_failed"
EventTypeSensorDisabled         = "sensor.disabled"
EventTypeSensorCooldownSkipped  = "sensor.cooldown_skipped"
EventTypeSensorDedupSkipped     = "sensor.dedup_skipped"
// Backfill (3)
EventTypeBackfillSubmitted   = "backfill.submitted"
EventTypeBackfillRunEnqueued = "backfill.run_enqueued"
EventTypeBackfillCompleted   = "backfill.completed"
```
</interfaces>
</context>

<tasks>

<task id="3.1.1" type="auto">
  <name>Task 1: Add partition_key, priority, backfill_id columns to Run ent entity + Atlas migration with CHECK + partial unique index + claim-path index</name>
  <files>internal/storage/ent/schema/run.go, migrations/20260508120000_phase3_runs_columns.sql, migrations/atlas.sum</files>
  <read_first>
    - internal/storage/ent/schema/run.go (current Run ent entity — extend)
    - migrations/20260507120000_phase2_run_tables.sql (Phase 2 hand-managed appendix pattern lines 48-72)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 2 — runs table additions SQL fragment
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pitfall 7 — partial unique index requirement
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 4 — index for priority-aware claim
  </read_first>
  <action>
    1. Edit `internal/storage/ent/schema/run.go` — append three new fields to `Run.Fields()`:
       ```go
       field.String("partition_key").Optional().MaxLen(128),                       // nullable VARCHAR(128); Phase 3 D-10
       field.String("priority").NotEmpty().MaxLen(16).Default("normal"),           // VARCHAR(16) NOT NULL DEFAULT 'normal'; Phase 3 D-13 layer 1
       field.UUID("backfill_id", uuid.UUID{}).Optional().Nillable(),               // nullable UUID; Phase 3 D-15
       ```
       Do NOT modify `Run.Indexes()` — the partial unique index and the priority-aware claim index are hand-managed in the SQL appendix because ent does not support `WHERE` predicates on indexes.
    2. Run `make ent-gen` (or `go generate ./internal/storage/ent`) to regenerate ent client code.
    3. Run `make migrate-diff NAME=phase3_runs_columns` to produce `migrations/20260508120000_phase3_runs_columns.sql`. Atlas will diff the ent schema and emit `ALTER TABLE` statements for the three new columns.
    4. Append the hand-managed appendix to `migrations/20260508120000_phase3_runs_columns.sql` BELOW the Atlas-generated diff (mirror Phase 2 pattern):
       ```sql
       -- ===== Hand-managed: Phase 3 D-10 / D-13 / D-15 (idempotent) =====

       ALTER TABLE runs DROP CONSTRAINT IF EXISTS runs_priority_check;
       ALTER TABLE runs
         ADD CONSTRAINT runs_priority_check
         CHECK (priority IN ('critical','normal','backfill'));

       -- D-10 partial unique index — scoped to in-flight states only so terminal runs
       -- can be re-enqueued for the same partition (Pitfall 7).
       DROP INDEX IF EXISTS run_partition_inflight_unique;
       CREATE UNIQUE INDEX run_partition_inflight_unique
         ON runs (asset_name, partition_key)
         WHERE state IN ('queued','starting','running')
           AND partition_key IS NOT NULL;

       -- D-13 priority-aware claim index — supports
       --   WHERE state='queued' ORDER BY <priority CASE>, queued_at
       CREATE INDEX IF NOT EXISTS run_state_priority_queued_at
         ON runs (state, priority, queued_at);

       -- D-15 backfill_id partial index — supports backfill status aggregation
       --   SELECT state, count(*) FROM runs WHERE backfill_id=$1 GROUP BY state
       CREATE INDEX IF NOT EXISTS run_backfill_id
         ON runs (backfill_id) WHERE backfill_id IS NOT NULL;
       ```
    5. Run `atlas migrate hash --env local` to update `migrations/atlas.sum`.
    6. Run `make migrate-lint` to confirm Atlas accepts the migration with no warnings about destructive changes.
  </action>
  <acceptance_criteria>
    - `grep -q 'field.String("partition_key").Optional().MaxLen(128)' internal/storage/ent/schema/run.go`
    - `grep -q 'field.String("priority").NotEmpty().MaxLen(16).Default("normal")' internal/storage/ent/schema/run.go`
    - `grep -q 'field.UUID("backfill_id", uuid.UUID{}).Optional().Nillable()' internal/storage/ent/schema/run.go`
    - File `migrations/20260508120000_phase3_runs_columns.sql` exists
    - `grep -q 'ADD COLUMN.*partition_key' migrations/20260508120000_phase3_runs_columns.sql`
    - `grep -q 'ADD COLUMN.*priority' migrations/20260508120000_phase3_runs_columns.sql`
    - `grep -q 'ADD COLUMN.*backfill_id' migrations/20260508120000_phase3_runs_columns.sql`
    - `grep -q "CHECK (priority IN ('critical','normal','backfill'))" migrations/20260508120000_phase3_runs_columns.sql`
    - `grep -q "CREATE UNIQUE INDEX run_partition_inflight_unique" migrations/20260508120000_phase3_runs_columns.sql`
    - `grep -q "WHERE state IN ('queued','starting','running')" migrations/20260508120000_phase3_runs_columns.sql`
    - `grep -q "CREATE INDEX IF NOT EXISTS run_state_priority_queued_at" migrations/20260508120000_phase3_runs_columns.sql`
    - `migrations/atlas.sum` modified after `atlas migrate hash --env local`
    - `make migrate-lint` exits 0 (no dirty-state warnings)
    - `go build ./...` passes after ent regen
  </acceptance_criteria>
  <verify>
    <automated>cd /home/developer/.kanpon/code/go/data-governance && go build ./... && grep -c 'partition_key\|priority\|backfill_id' migrations/20260508120000_phase3_runs_columns.sql</automated>
  </verify>
  <done>Run ent entity has the three new fields, Atlas migration with hand-managed CHECK + partial unique index + claim-path index is generated and lint-clean, atlas.sum updated, build passes.</done>
</task>

<task id="3.1.2" type="auto" tdd="true">
  <name>Task 2: Create Schedule, Sensor, Backfill ent entities + Atlas migration with grants</name>
  <files>internal/storage/ent/schema/schedule.go, internal/storage/ent/schema/sensor.go, internal/storage/ent/schema/backfill.go, migrations/20260508121000_phase3_schedules_sensors_backfills.sql, migrations/atlas.sum</files>
  <read_first>
    - internal/storage/ent/schema/run.go (annotation + index pattern reference)
    - internal/storage/ent/schema/concurrency_token.go (compact ent schema reference)
    - migrations/20260507121500_phase2_concurrency_tokens.sql (role grants pattern lines 18-22)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 2 — full ent schema definitions
  </read_first>
  <behavior>
    - schedules table CREATE statement contains columns: id, asset_name, cron_expr, last_fire_at, next_fire_at, paused_at, created_at, updated_at
    - sensors table CREATE statement contains columns: id, asset_name, sensor_name, min_interval_seconds, last_evaluated_at, last_fired_at, last_run_key, cooldown_until, consecutive_failures, disabled_at, created_at, updated_at
    - backfills table CREATE statement contains columns: id, asset_name, partition_spec, status, total_partitions, submitted_at, completed_at
    - All three tables have OWNER platform_owner and platform_app role grants (SELECT, INSERT, UPDATE, DELETE)
    - schedules table has tick-scan index on (paused_at, next_fire_at)
    - sensors table has tick-scan index on (disabled_at, last_evaluated_at)
    - migrations/atlas.sum is regenerated and lint-clean
  </behavior>
  <action>
    1. Create `internal/storage/ent/schema/schedule.go` — copy the verbatim ent schema from 03-RESEARCH.md § Pattern 2 "ent schema `internal/storage/ent/schema/schedule.go`" code block. Key fields:
       ```go
       field.UUID("id", uuid.UUID{}).Default(uuid.New),
       field.String("asset_name").NotEmpty().MaxLen(256).Immutable(),
       field.String("cron_expr").NotEmpty().MaxLen(128),
       field.Time("last_fire_at").Optional().Nillable(),
       field.Time("next_fire_at").Optional().Nillable(),
       field.Time("paused_at").Optional().Nillable(),
       field.Time("created_at").Default(time.Now).Immutable(),
       field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
       ```
       Indexes:
       ```go
       index.Fields("asset_name"),
       index.Fields("next_fire_at"),
       index.Fields("paused_at", "next_fire_at"),
       ```
    2. Create `internal/storage/ent/schema/sensor.go` — copy verbatim from 03-RESEARCH.md § Pattern 2 "ent schema `internal/storage/ent/schema/sensor.go`" code block. Key fields include `min_interval_seconds Int64`, `consecutive_failures Int`, `last_run_key String Optional MaxLen(256)`, etc. Indexes: `(asset_name, sensor_name)` and `(disabled_at, last_evaluated_at)`.
    3. Create `internal/storage/ent/schema/backfill.go` — copy verbatim from 03-RESEARCH.md § Pattern 2 "ent schema `internal/storage/ent/schema/backfill.go`" code block. Field `partition_spec String NotEmpty MaxLen(1024)`, `status String MaxLen(16) Default("submitted")`, `total_partitions Int`. Indexes: `(asset_name)` and `(status, submitted_at)`.
    4. Run `make ent-gen` to regenerate ent client.
    5. Run `make migrate-diff NAME=phase3_schedules_sensors_backfills` to produce `migrations/20260508121000_phase3_schedules_sensors_backfills.sql`.
    6. Append hand-managed role grants appendix BELOW Atlas diff:
       ```sql
       -- ===== Hand-managed: Phase 3 role grants (idempotent) =====

       ALTER TABLE schedules OWNER TO platform_owner;
       ALTER TABLE sensors   OWNER TO platform_owner;
       ALTER TABLE backfills OWNER TO platform_owner;

       GRANT SELECT, INSERT, UPDATE, DELETE ON schedules TO platform_app;
       GRANT SELECT, INSERT, UPDATE, DELETE ON sensors   TO platform_app;
       GRANT SELECT, INSERT, UPDATE, DELETE ON backfills TO platform_app;
       ```
    7. Run `atlas migrate hash --env local` to update `migrations/atlas.sum`.
    8. Run `make migrate-lint`.
    9. Run `make migrate-apply` against the local DB and confirm success (one-shot apply — no rollback needed for additive schema).
  </action>
  <acceptance_criteria>
    - `grep -q 'type Schedule struct{ ent.Schema }' internal/storage/ent/schema/schedule.go`
    - `grep -q 'type Sensor struct{ ent.Schema }' internal/storage/ent/schema/sensor.go`
    - `grep -q 'type Backfill struct{ ent.Schema }' internal/storage/ent/schema/backfill.go`
    - `grep -q 'cron_expr' internal/storage/ent/schema/schedule.go`
    - `grep -q 'min_interval_seconds' internal/storage/ent/schema/sensor.go`
    - `grep -q 'consecutive_failures' internal/storage/ent/schema/sensor.go`
    - `grep -q 'partition_spec' internal/storage/ent/schema/backfill.go`
    - File `migrations/20260508121000_phase3_schedules_sensors_backfills.sql` exists
    - `grep -q 'CREATE TABLE "schedules"' migrations/20260508121000_phase3_schedules_sensors_backfills.sql`
    - `grep -q 'CREATE TABLE "sensors"' migrations/20260508121000_phase3_schedules_sensors_backfills.sql`
    - `grep -q 'CREATE TABLE "backfills"' migrations/20260508121000_phase3_schedules_sensors_backfills.sql`
    - `grep -q 'GRANT SELECT, INSERT, UPDATE, DELETE ON schedules TO platform_app' migrations/20260508121000_phase3_schedules_sensors_backfills.sql`
    - `grep -q 'GRANT SELECT, INSERT, UPDATE, DELETE ON sensors' migrations/20260508121000_phase3_schedules_sensors_backfills.sql`
    - `grep -q 'GRANT SELECT, INSERT, UPDATE, DELETE ON backfills' migrations/20260508121000_phase3_schedules_sensors_backfills.sql`
    - `make migrate-lint` exits 0
    - After `make migrate-apply`, querying `psql $DATABASE_URL -c '\dt schedules sensors backfills'` returns 3 rows
  </acceptance_criteria>
  <verify>
    <automated>cd /home/developer/.kanpon/code/go/data-governance && go build ./... && DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./internal/run/... -run TestClaimAtomicity50Goroutines -count=1 -timeout 60s</automated>
  </verify>
  <done>Three new ent entities exist, Atlas migration creates the tables with role grants, migrate-lint passes, migrate-apply succeeds against local DB, Phase 2 50-goroutine atomicity test still passes after the new schema lands.</done>
</task>

<task id="3.1.3" type="auto" tdd="true">
  <name>Task 3: Add 13 new EventType constants per D-17 + extend AllKnownTypes() + write coverage test</name>
  <files>internal/event/types.go, internal/event/types_test.go</files>
  <read_first>
    - internal/event/types.go (existing Phase 1+2 EventType constants pattern)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Code Examples → Event Type Registration (D-17)
    - .planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md § D-17
  </read_first>
  <behavior>
    - All 13 Phase 3 EventType constants exist with the exact string values in D-17
    - AllKnownTypes() returns a slice that includes every Phase 1, Phase 2, and Phase 3 type (no Phase 3 types omitted)
    - TestAllPhase3EventTypes confirms each new constant is present in AllKnownTypes() output and that string values match D-17 exactly
  </behavior>
  <action>
    1. In `internal/event/types.go`, append a Phase 3 block after the Phase 2 run lifecycle constants (mirror the existing comment-block structure):
       ```go
       // Phase 3 (D-17) — schedule lifecycle events.
       EventTypeScheduleFired   EventType = "schedule.fired"
       EventTypeScheduleMissed  EventType = "schedule.missed"
       EventTypeSchedulePaused  EventType = "schedule.paused"
       EventTypeScheduleResumed EventType = "schedule.resumed"

       // Phase 3 (D-17) — sensor lifecycle events.
       EventTypeSensorEvaluated        EventType = "sensor.evaluated"
       EventTypeSensorFired            EventType = "sensor.fired"
       EventTypeSensorEvaluationFailed EventType = "sensor.evaluation_failed"
       EventTypeSensorDisabled         EventType = "sensor.disabled"
       EventTypeSensorCooldownSkipped  EventType = "sensor.cooldown_skipped"
       EventTypeSensorDedupSkipped     EventType = "sensor.dedup_skipped"

       // Phase 3 (D-17) — backfill lifecycle events.
       EventTypeBackfillSubmitted   EventType = "backfill.submitted"
       EventTypeBackfillRunEnqueued EventType = "backfill.run_enqueued"
       EventTypeBackfillCompleted   EventType = "backfill.completed"
       ```
    2. Extend `AllKnownTypes()` to append all 13 new constants (preserve existing Phase 1+2 types).
    3. Create `internal/event/types_test.go` with `TestAllPhase3EventTypes(t *testing.T)` that:
       - Asserts every Phase 3 constant value matches D-17 exactly using `assert.Equal(t, "schedule.fired", string(EventTypeScheduleFired))` etc. for all 13.
       - Asserts each constant is present in `AllKnownTypes()` using a membership check (build a `map[EventType]bool` from the slice and assert each Phase 3 type is true).
       - Asserts `len(AllKnownTypes()) >= 29` (16 baseline + 13 Phase 3 — Phase 1+2 baseline currently has 16 entries; if more were added in interim, the test should still pass).
    4. Per Phase 4 lineage hook precedent, do NOT add typed payload structs for Phase 3 events yet — the writer accepts `Payload any` and downstream plans pass `map[string]any` payloads inline. (This avoids dead struct types if payload shapes evolve in plans 03-04+.)
  </action>
  <acceptance_criteria>
    - `grep -q 'EventTypeScheduleFired   EventType = "schedule.fired"' internal/event/types.go`
    - `grep -q 'EventTypeScheduleMissed  EventType = "schedule.missed"' internal/event/types.go`
    - `grep -q 'EventTypeSensorFired            EventType = "sensor.fired"' internal/event/types.go`
    - `grep -q 'EventTypeSensorDedupSkipped  EventType = "sensor.dedup_skipped"' internal/event/types.go` (or with adjusted spacing)
    - `grep -q 'EventTypeBackfillSubmitted   EventType = "backfill.submitted"' internal/event/types.go`
    - `grep -c 'EventType = "schedule\\.\\|EventType = "sensor\\.\\|EventType = "backfill\\.' internal/event/types.go` returns 13 (or more if existing matches present)
    - `go test ./internal/event/... -run TestAllPhase3EventTypes -count=1 -timeout 30s` exits 0
    - All 13 constants are referenced in `AllKnownTypes()`: `grep -c 'EventTypeSchedule\|EventTypeSensor\|EventTypeBackfill' internal/event/types.go` returns at least 26 (13 declarations + 13 in AllKnownTypes)
  </acceptance_criteria>
  <verify>
    <automated>go test ./internal/event/... -run TestAllPhase3EventTypes -count=1 -timeout 30s</automated>
  </verify>
  <done>Thirteen Phase 3 EventType constants exist with exact D-17 string values, AllKnownTypes() includes all of them, TestAllPhase3EventTypes passes.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| Atlas migration → PostgreSQL | DDL crosses here at deploy time; migration content is checked into git, no runtime-supplied SQL |
| ent client → PostgreSQL | All schema-derived queries crossed parametrized; no string interpolation |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-03-01-01 | Tampering | runs.priority column | mitigate | DB-level CHECK constraint `priority IN ('critical','normal','backfill')` rejects out-of-range values regardless of caller; matches Phase 2 state-check pattern (D-17 Phase 2) |
| T-03-01-02 | Tampering | runs.partition_key column | mitigate | VARCHAR(128) length cap + parameterized queries (no string interpolation in migration or in plans 03-04/05/06/07); see Pitfall 4 |
| T-03-01-03 | Denial of Service | Backfill row-count blowup at migration time | accept | Migration creates empty tables only; row-count guard belongs in plan 03-07 backfill CLI (T-03-07-XX) |
| T-03-01-04 | Tampering | event_log Phase 3 entries | accept | Already mitigated by Phase 1 D-09 RLS — `platform_app` role lacks UPDATE/DELETE on event_log [VERIFIED Phase 2] |
| T-03-01-05 | Information Disclosure | schedules/sensors/backfills tables grants | mitigate | Only `platform_app` (the application role) gets DML; `platform_owner` retains DDL ownership; mirrors Phase 2 pattern |
| T-03-01-06 | Denial of Service | Partial unique index built without WHERE clause | mitigate | Pitfall 7 — explicit `WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL` in migration; tested in plan 03-04 task creating partition unique constraint test |
| T-03-01-07 | Elevation of Privilege | priority='critical' submitted by non-admin via runs INSERT | mitigate (deferred to caller) | DB-level CHECK accepts 'critical' value (no auth at DB layer); enforcement of who may set it lives in CLI parse-time (plan 03-07 T-03-07-XX) and in API auth layer (Phase 4+) |
</threat_model>

<verification>
- `go build ./...` passes after ent regen.
- `make migrate-lint` exits 0 for both new migration files.
- `make migrate-apply` (against local DB) applies cleanly with no errors.
- `psql $DATABASE_URL -c "\d runs"` shows new columns `partition_key`, `priority`, `backfill_id` with correct types and the CHECK constraint.
- `psql $DATABASE_URL -c "\di run_partition_inflight_unique"` shows the partial unique index with the WHERE predicate.
- `psql $DATABASE_URL -c "\dt schedules sensors backfills"` returns 3 rows.
- `DATABASE_URL=... go test ./internal/run/... -run TestClaimAtomicity50Goroutines -count=1 -timeout 60s` passes (regression guard — Phase 2 contract).
- `go test ./internal/event/... -run TestAllPhase3EventTypes -count=1 -timeout 30s` passes.
</verification>

<success_criteria>
- runs table has partition_key (nullable VARCHAR(128)), priority (NOT NULL VARCHAR(16) DEFAULT 'normal' with CHECK), backfill_id (nullable UUID).
- Partial unique index `run_partition_inflight_unique` on (asset_name, partition_key) with the in-flight WHERE predicate exists.
- Composite index `run_state_priority_queued_at` on (state, priority, queued_at) exists.
- schedules, sensors, backfills tables exist with the columns and indexes from 03-RESEARCH.md Pattern 2.
- platform_app role has DML grants on all three new tables.
- 13 Phase 3 EventType constants exist with exact D-17 string values; AllKnownTypes() returns all of them.
- Phase 2 50-goroutine atomicity test still passes (regression guard).
- All migrations are lint-clean and apply-clean.
</success_criteria>

<output>
After completion, create `.planning/phases/03-scheduling-sensors-partitions/03-01-SUMMARY.md` documenting:
- Migration file names + Atlas-applied versions
- Final ent entity definitions (path + struct names)
- Phase 3 EventType constants list (verbatim)
- Confirmed: Phase 2 TestClaimAtomicity50Goroutines still passes after migration
- Any deviations from 03-RESEARCH.md Pattern 2 (should be none)
</output>
