# Phase 3: 调度、传感器与分区 - Research

**Researched:** 2026-05-08
**Domain:** Cron scheduling, event sensors, time/category partitions, backfill isolation, priority-aware run claiming
**Confidence:** HIGH

---

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions
- **D-01:** Scheduler runs as `./platform scheduler` subcommand (parallel to server/worker/materialize).
- **D-02:** Schedule state persists in new `schedules` table. Tick (30s default) selects rows with `next_fire_at <= NOW()` using `SELECT ... FOR UPDATE SKIP LOCKED`, enqueues a run, updates `last_fire_at`, recomputes `next_fire_at`. Runs table holds only claimable runs, never future runs.
- **D-03:** robfig/cron/v3 used as parser + `Next()` API only — NOT its in-process Cron scheduler. Tick loop is custom. Multi-replica safety via SKIP LOCKED. No River. No leader election.
- **D-04:** Missed-schedule recovery: fire only the most recent missed window per schedule. Emit `schedule.missed` event with skipped count. LatestOnly only; per-schedule override deferred.
- **D-05:** Sensors run inside the same `scheduler` subcommand. New `sensors` table: `(asset_name, sensor_name, min_interval, last_evaluated_at, last_fired_at, last_run_key, cooldown_until, consecutive_failures, disabled_at, ...)`. Sensors selected by tick, evaluated, conditionally enqueue runs.
- **D-06:** `SensorResult{Fired bool, RunKey string, Payload map[string]any}`. Builder: `.Sensor(asset.SensorSpec{Name, MinInterval, Cooldown, Sense})`. Payload is Phase 4 lineage hook.
- **D-07:** Two-layer dedup — RunKey comparison AND cooldown window. If `RunKey == last_run_key` => skip. If `NOW() < cooldown_until` => skip regardless of key.
- **D-08:** Sense() error handling: log + emit `sensor.evaluation_failed`, retry next tick. After `consecutive_failures >= N` (default 60), set `disabled_at = NOW()` + emit `sensor.disabled`. Operator must manually re-enable.
- **D-09:** `.Partitions(spec)` builder method. v1 strategies: `DailyPartitions{Start, TZ}`, `WeeklyPartitions{Start, TZ}` (ISO week), `MonthlyPartitions{Start, TZ}`, `CategoryPartitions{Keys []string}`.
- **D-10:** Partitioned runs in existing `runs` table with new nullable `partition_key VARCHAR(128)`. Unique constraint `(asset_name, partition_key)` scoped to in-flight states. `MaterializeFunc` learns partition via `io.PartitionKey() string`.
- **D-11:** UTC ISO-8601 keys: Daily `2024-01-15`, Weekly `2024-W03`, Monthly `2024-01`, Category = user-supplied string.
- **D-12:** Builder methods compose orthogonally. `.Schedule(cron).Partitions(daily)` fires for current window. `.Sensor(spec).Partitions(...)` can include explicit `PartitionKey`; if empty fires latest current. `.Schedule(cron).Sensor(spec).Partitions(daily)` both triggers active independently.
- **D-13:** Three-layer backfill isolation: (1) `runs.priority` VARCHAR(16) CHECK enum `critical|normal|backfill`; (2) `ORDER BY priority ASC, queued_at ASC` with `critical=0, normal=1, backfill=2` integer mapping; (3) backfill runs also acquire `backfill` token from existing concurrency pool. The 50-goroutine claim atomicity test MUST continue to pass.
- **D-14:** `./platform backfill <asset> --partitions=<spec>` CLI. Spec: date range `2024-01-01:2024-12-31`, comma list `us,eu,apac`, single key `2024-01-15`. Returns `backfill_id`. Status via `./platform backfill status <backfill_id>`.
- **D-15:** Enqueue all partition runs immediately. Token pool caps in-flight count. 365-partition backfill creates 365 rows immediately. No batch-coordinator goroutine.
- **D-16:** Per-partition failure is independent. Failed partition does NOT halt siblings. Retry via per-asset retry policy. Re-run failures by new backfill scoped to failed subset.
- **D-17:** New event_type enum values: schedule.fired, schedule.missed, schedule.paused, schedule.resumed; sensor.evaluated, sensor.fired, sensor.evaluation_failed, sensor.disabled, sensor.cooldown_skipped, sensor.dedup_skipped; backfill.submitted, backfill.run_enqueued, backfill.completed. Partition lifecycle covered by existing run.* events.

### Claude's Discretion
- Exact tick-loop timing tolerance, jitter strategy for `next_fire_at` recomputation
- Whether `schedules` and `sensors` ent entities share a base mixin or stay independent
- Internal layout of priority enum mapping (string to int) — must be consistent
- CLI output format for `./platform backfill status` (plain text vs structured)
- Whether `backfill_id` is UUID, timestamp-prefixed string, or sortable ID
- Whether sensor `consecutive_failures` resets on first successful evaluation or requires operator reset
- Whether `WeeklyPartitions` defaults to ISO weeks (Mon-Sun) — pick ISO per D-11 and document

### Deferred Ideas (OUT OF SCOPE)
- Per-schedule missed-fire policy override (LatestOnly | All | Skip)
- REST `/backfills` endpoint (Phase 6 UI dependency)
- Cursor-based sensor contract
- Sensor + Partition `SensorResult.PartitionKey` default-to-latest semantics (planning detail)
- Dynamic partition strategies (DB-driven category lists)
- Partition dependency mapping (partitioned downstream reading partitioned upstream)
- Partition pause / disable CLI/REST surface
- Sensor secret/credential injection via Vault/KMS
- Backfill cancellation bulk API
- River migration
- Load testing profile (deferred per CONTEXT.md — called out as planning detail, confirmed as Validation Architecture requirement)
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| ORCH-05 | Data engineer can attach cron schedule to asset for automatic periodic materialization | D-01..D-04 + robfig/cron/v3 parser API + schedules table schema + tick-loop pattern |
| ORCH-06 | Data engineer can define event sensor that fires asset materialization when external condition becomes true | D-05..D-08 + SensorSpec DSL + sensor evaluation harness + dedup + cooldown |
| ORCH-07 | Data engineer can define time-partitioned assets (daily, weekly, monthly) | D-09..D-11 + partition-key generation algorithms + ISO week edge cases + backfill date-range parsing |
| ORCH-08 | Data engineer can define category-partitioned assets (per region, per customer) | D-09, D-11 + CategoryPartitions builder + backfill comma-list parsing + independent per-partition failure |
</phase_requirements>

---

## Summary

Phase 3 adds the auto-trigger and partition layer atop Phase 2's execution kernel. The core primitive — `SELECT ... FOR UPDATE SKIP LOCKED` — is already proven for multi-replica safety and is reused without modification for schedule/sensor tick-loop safety. The main new work is: (1) a scheduler daemon with a custom tick loop that drives all firing decisions via a single Postgres query per tick; (2) a sensor evaluation harness with panic recovery, timeout enforcement, and dedup; (3) a partition-key encoding system with well-defined UTC string representations; (4) a three-layer backfill isolation scheme that extends the existing claim query and concurrency token pool without breaking the 50-goroutine atomicity test.

**robfig/cron/v3** (v3.0.1, stable since 2020) is used exclusively as a parser + `Next()` call — its built-in Cron runner is not instantiated. The library's `Schedule.Next(time.Time) time.Time` is the only API required by the scheduler tick loop. No new scheduler framework is introduced.

**Key architectural insight:** The Phase 2 50-goroutine atomicity test is preserved by keeping SKIP LOCKED + `WHERE state='queued'` + the new `WHERE priority...` only in the ORDER BY clause, not in the WHERE clause. The unique constraint on `(asset_name, partition_key)` scoped to in-flight states prevents duplicate partition runs independently of the claim atomicity mechanism.

**Primary recommendation:** Implement the scheduler as a single tick goroutine that queries both `schedules` and `sensors` tables in sequence within a 30-second loop. Priority-aware claim is a one-line ORDER BY change to `claim.go`. All else is additive schema + new packages.

---

## Standard Stack

### Core (Phase 3 Additions)

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/robfig/cron/v3` | v3.0.1 [VERIFIED: proxy.golang.org] | Cron expression parsing + `Schedule.Next()` API | Only parser used — not its in-process runner. Parser-only use is the established expert pattern for custom tick-loop schedulers |
| `entgo.io/ent` | v0.14.0 (already in go.mod) [VERIFIED: go.mod] | ent schema for `schedules`, `sensors`, `backfills` entities | Already used for all schema work in Phase 1+2 |
| `database/sql` + `pgx/v5` | pgx v5.9.1 (already in go.mod) [VERIFIED: go.mod] | Raw claim query (priority ORDER BY), schedule/sensor tick SELECT FOR UPDATE SKIP LOCKED | Phase 2 ClaimNext already uses raw SQL for claim atomicity |

### Supporting (Unchanged from Phase 2)

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `ariga.io/atlas` | already in go.mod [VERIFIED] | Atlas migrations for new tables + run column additions | Every Phase 3 schema change goes through Atlas per established pattern |
| `github.com/google/uuid` | v1.6.0 (already in go.mod) [VERIFIED: go.mod] | backfill_id generation | UUID for backfill_id recommended (see Claude's Discretion below) |
| `log/slog` | stdlib (Go 1.25.0) [VERIFIED: go version] | Structured logging in scheduler/sensor loops | Already used throughout Phase 2 |

### New Dependency

```bash
go get github.com/robfig/cron/v3@v3.0.1
```

robfig/cron/v3 is the only net-new dependency. Everything else reuses what Phase 2 already imports.

---

## Architecture Patterns

### Recommended New Package Structure

```
internal/
├── schedule/          # Scheduler tick loop, schedules table CRUD, missed-window logic
│   ├── daemon.go      # SchedulerDaemon: tick loop goroutine, graceful shutdown
│   ├── fire.go        # fireSchedules(): SELECT FOR UPDATE SKIP LOCKED + enqueue + update
│   └── schedule_test.go
├── sensor/            # Sensor evaluation harness
│   ├── daemon.go      # SensorDaemon (called from SchedulerDaemon tick)
│   ├── evaluate.go    # evaluateSensor(): timeout, panic recovery, dedup, enqueue
│   └── sensor_test.go
├── partition/         # Partition strategy types and key generation
│   ├── strategy.go    # PartitionStrategy interface + Daily/Weekly/Monthly/Category types
│   ├── keygen.go      # GenerateKey(), CurrentKey(), ParseRange()
│   └── keygen_test.go
├── backfill/          # Backfill submission service
│   ├── submit.go      # Submit(): parse spec, mass-enqueue, return backfill_id
│   ├── status.go      # Status(): aggregate query by backfill_id
│   └── backfill_test.go
├── run/
│   └── claim.go       # MODIFIED: priority-aware ORDER BY (additive change only)
├── asset/
│   ├── builder.go     # EXTENDED: .Schedule(), .Sensor(), .Partitions() chained methods
│   ├── asset.go       # EXTENDED: ScheduleSpec, SensorSpec, PartitionStrategy fields
│   └── io.go          # EXTENDED: PartitionKey() string added to AssetIO interface
└── event/
    └── types.go       # EXTENDED: D-17 schedule.*/sensor.*/backfill.* event types

cmd/platform/
├── main.go            # EXTENDED: "scheduler" and "backfill" cases in switch
├── scheduler.go       # NEW: runScheduler() entry point
└── backfill.go        # NEW: runBackfill() and runBackfillStatus() entry points

internal/storage/ent/schema/
├── schedule.go        # NEW ent schema
├── sensor.go          # NEW ent schema
└── backfill.go        # NEW ent schema

migrations/
└── 2026MMDDHHMMSS_phase3_*.sql  # NEW: ALTER runs + CREATE schedules/sensors/backfills
```

---

## Pattern 1: robfig/cron/v3 — Parser-Only Usage

**What:** Use `cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)` to parse user-supplied cron strings and call `schedule.Next(t)` for computing `next_fire_at`. Never instantiate `cron.New()` — the built-in runner would compete with the custom tick loop.

**Why parser-only:** The built-in `cron.Cron` runner runs each job in its own goroutine and does not use the database as the coordination primitive. Phase 3's multi-replica safety requirement (D-03) demands that only one scheduler instance fires per cron tick, which is achieved via `SELECT ... FOR UPDATE SKIP LOCKED` on the `schedules` table. Using the built-in runner would bypass this.

**API surface needed:** [VERIFIED: pkg.go.dev/github.com/robfig/cron/v3]

```go
// Source: pkg.go.dev/github.com/robfig/cron/v3
import "github.com/robfig/cron/v3"

// Build parser once at daemon startup (no timezone — stored UTC; TZ applied in Next() call)
var parser = cron.NewParser(
    cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// Parse at schedule registration or on first tick read
sched, err := parser.Parse(expr) // returns (cron.Schedule, error)
if err != nil {
    // Returned error is a string, not a typed error — check err != nil only.
    // Common invalid expressions: "bad field", "end of range (X) < start of range (Y)",
    // "empty expression". No sentinel error type to match against.
    return fmt.Errorf("schedule: invalid cron expression %q: %w", expr, err)
}

// Compute next fire time
nextFire := sched.Next(lastFiredAt) // time.Time -> time.Time; always > lastFiredAt
// If schedule was never fired, pass time.Time{} or time.Unix(0,0) — cron handles zero time.

// Timezone: create a time.Location and use it when calling Next()
loc, err := time.LoadLocation(tzName) // "UTC", "America/New_York", etc.
nextFire = sched.Next(lastFiredAt.In(loc))
// NOTE: Keys are still generated as UTC strings (D-11). TZ only affects which
// wall-clock window the partition represents (cron alignment), not the stored key.
```

**Missed-window detection (D-04 LatestOnly):**

```go
// Source: custom logic, cron.Schedule.Next() semantics [VERIFIED: pkg.go.dev]
func computeNextAndDetectMiss(sched cron.Schedule, lastFiredAt time.Time, now time.Time) (
    nextFire time.Time, missedCount int,
) {
    candidate := sched.Next(lastFiredAt)
    missedCount = 0
    for candidate.Before(now) {
        nextCandidate := sched.Next(candidate)
        if nextCandidate.After(now) || nextCandidate.Equal(now) {
            break
        }
        missedCount++
        candidate = nextCandidate
    }
    // candidate is now the most recent window that was <= now.
    // Fire candidate (D-04 LatestOnly: fire only the most recent window).
    return candidate, missedCount
}
```

**Error modes for invalid expressions:**
- `"bad field"` for wrong number of fields
- `"end of range (X) < start of range (Y)"` for inverted ranges like `5-3`
- `""` (empty string) → parser returns descriptive error
- `"@every 0s"` → accepted but fires immediately on every tick; log a warning
- Error type is `error` (interface) — no sentinel type [VERIFIED: pkg.go.dev/github.com/robfig/cron/v3]

**Version note:** v3.0.1 (2020-01-04) is the only v3 release. The library is stable and maintenance-only. [VERIFIED: proxy.golang.org]

---

## Pattern 2: Schedules Table Schema (ent + SQL migration)

**ent schema `internal/storage/ent/schema/schedule.go`:**

```go
// Source: ent v0.14.0 patterns, consistent with Phase 2 run.go schema [VERIFIED: go.mod]
type Schedule struct{ ent.Schema }

func (Schedule) Annotations() []schema.Annotation {
    return []schema.Annotation{entsql.Annotation{Table: "schedules"}}
}

func (Schedule) Fields() []ent.Field {
    return []ent.Field{
        field.UUID("id", uuid.UUID{}).Default(uuid.New),
        field.String("asset_name").NotEmpty().MaxLen(256).Immutable(),
        field.String("cron_expr").NotEmpty().MaxLen(128),
        // last_fire_at: NULL until first fire. Tick loop uses this as start for Next().
        field.Time("last_fire_at").Optional().Nillable(),
        // next_fire_at: precomputed by recompute step after each fire.
        // Indexed for the WHERE next_fire_at <= NOW() tick scan.
        field.Time("next_fire_at").Optional().Nillable(),
        // paused_at: non-NULL means paused. Phase 3 schema placeholder;
        // pause/resume CLI is Phase 6 scope per D-02 note.
        field.Time("paused_at").Optional().Nillable(),
        field.Time("created_at").Default(time.Now).Immutable(),
        field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
    }
}

func (Schedule) Indexes() []ent.Index {
    return []ent.Index{
        index.Fields("asset_name"),                    // lookup by asset
        index.Fields("next_fire_at"),                  // tick scan: WHERE next_fire_at <= NOW() AND paused_at IS NULL
        index.Fields("paused_at", "next_fire_at"),     // pause-filtered tick scan
    }
}
```

**ent schema `internal/storage/ent/schema/sensor.go`:**

```go
type Sensor struct{ ent.Schema }

func (Sensor) Annotations() []schema.Annotation {
    return []schema.Annotation{entsql.Annotation{Table: "sensors"}}
}

func (Sensor) Fields() []ent.Field {
    return []ent.Field{
        field.UUID("id", uuid.UUID{}).Default(uuid.New),
        field.String("asset_name").NotEmpty().MaxLen(256).Immutable(),
        field.String("sensor_name").NotEmpty().MaxLen(128).Immutable(),
        // min_interval_seconds: minimum poll interval in seconds (from SensorSpec.MinInterval)
        field.Int64("min_interval_seconds").Default(30),
        field.Time("last_evaluated_at").Optional().Nillable(),
        field.Time("last_fired_at").Optional().Nillable(),
        // last_run_key: most recent RunKey that triggered a fire (dedup layer 1)
        field.String("last_run_key").Optional().MaxLen(256),
        // cooldown_until: dedup layer 2 — no-fire until this time
        field.Time("cooldown_until").Optional().Nillable(),
        // consecutive_failures: incremented on each Sense() error; reset on success
        field.Int("consecutive_failures").Default(0),
        // disabled_at: non-NULL means auto-disabled after N consecutive failures (D-08)
        field.Time("disabled_at").Optional().Nillable(),
        field.Time("created_at").Default(time.Now).Immutable(),
        field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
    }
}

func (Sensor) Indexes() []ent.Index {
    return []ent.Index{
        index.Fields("asset_name", "sensor_name"),
        // Tick scan: WHERE disabled_at IS NULL AND (last_evaluated_at IS NULL OR last_evaluated_at + min_interval <= NOW())
        index.Fields("disabled_at", "last_evaluated_at"),
    }
}
```

**ent schema `internal/storage/ent/schema/backfill.go`:**

```go
type Backfill struct{ ent.Schema }

func (Backfill) Annotations() []schema.Annotation {
    return []schema.Annotation{entsql.Annotation{Table: "backfills"}}
}

func (Backfill) Fields() []ent.Field {
    return []ent.Field{
        field.UUID("id", uuid.UUID{}).Default(uuid.New),  // this is backfill_id
        field.String("asset_name").NotEmpty().MaxLen(256),
        // partition_spec: raw user-supplied spec string for auditability
        field.String("partition_spec").NotEmpty().MaxLen(1024),
        field.String("status").MaxLen(16).Default("submitted"),
        // total_partitions: count of run rows created on submission
        field.Int("total_partitions").Default(0),
        field.Time("submitted_at").Default(time.Now).Immutable(),
        field.Time("completed_at").Optional().Nillable(),
    }
}

func (Backfill) Indexes() []ent.Index {
    return []ent.Index{
        index.Fields("asset_name"),
        index.Fields("status", "submitted_at"),
    }
}
```

**runs table additions (SQL migration fragment):**

```sql
-- Phase 3 additive changes to runs table
ALTER TABLE runs ADD COLUMN IF NOT EXISTS partition_key VARCHAR(128) NULL;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS priority    VARCHAR(16)  NOT NULL DEFAULT 'normal';
ALTER TABLE runs ADD COLUMN IF NOT EXISTS backfill_id UUID         NULL;

-- CHECK constraint for priority (mirrors Phase 2 state_check pattern)
ALTER TABLE runs DROP CONSTRAINT IF EXISTS runs_priority_check;
ALTER TABLE runs ADD CONSTRAINT runs_priority_check
    CHECK (priority IN ('critical', 'normal', 'backfill'));

-- Unique constraint: prevents duplicate concurrent partition runs.
-- Scoped to in-flight states only (queued, starting, running) per D-10.
-- EXCLUDE is not needed — a partial unique index suffices.
DROP INDEX IF EXISTS run_partition_inflight_unique;
CREATE UNIQUE INDEX run_partition_inflight_unique
    ON runs (asset_name, partition_key)
    WHERE state IN ('queued', 'starting', 'running')
      AND partition_key IS NOT NULL;

-- New indexes for priority claim path
CREATE INDEX IF NOT EXISTS run_state_priority_queued_at
    ON runs (state, priority, queued_at);

-- backfill_id FK (informational; no ON DELETE needed)
CREATE INDEX IF NOT EXISTS run_backfill_id ON runs (backfill_id) WHERE backfill_id IS NOT NULL;
```

---

## Pattern 3: Scheduler Tick Loop

**What:** A single goroutine runs every 30 seconds (configurable). Each tick fires two queries in sequence: `fireSchedules()` and `evaluateSensors()`. Both use `SELECT ... FOR UPDATE SKIP LOCKED` on their respective tables to achieve multi-replica safety without leader election.

**Graceful shutdown pattern:**

```go
// Source: standard Go shutdown pattern [ASSUMED - well-established idiom]
// internal/schedule/daemon.go

type Daemon struct {
    store    storage.Storage
    registry *asset.DefinitionRegistry
    events   event.Writer
    interval time.Duration // default 30s
}

func (d *Daemon) Run(ctx context.Context) error {
    ticker := time.NewTicker(d.interval)
    defer ticker.Stop()
    // Run one tick immediately on start to handle any missed fires from downtime.
    if err := d.tick(ctx); err != nil {
        slog.Error("scheduler.tick_failed", "error", err)
    }
    for {
        select {
        case <-ticker.C:
            if err := d.tick(ctx); err != nil {
                slog.Error("scheduler.tick_failed", "error", err)
                // Log and continue — transient DB error should not kill the daemon.
            }
        case <-ctx.Done():
            return ctx.Err()
        }
    }
}
```

**Tick-loop atomicity — schedule firing (SELECT FOR UPDATE SKIP LOCKED per D-02):**

```go
// internal/schedule/fire.go
// One transaction per schedule row (not a batch transaction) to minimize lock hold time.
func (d *Daemon) fireOneSchedule(ctx context.Context, schedID uuid.UUID, ...) error {
    tx, err := d.store.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
    defer tx.Rollback()

    // Select and lock one due schedule row.
    const selectSQL = `
        SELECT id, asset_name, cron_expr, last_fire_at, next_fire_at
        FROM schedules
        WHERE next_fire_at <= $1
          AND paused_at IS NULL
        ORDER BY next_fire_at
        FOR UPDATE SKIP LOCKED
        LIMIT 1
    `
    // ... scan row ...

    // Enqueue run row.
    const insertRunSQL = `
        INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority)
        VALUES ($1, $2, 'queued', 'schedule', $3, 'normal')
    `
    // ... execute ...

    // Update schedule: last_fire_at = firedAt, next_fire_at = sched.Next(firedAt)
    const updateSQL = `
        UPDATE schedules
           SET last_fire_at = $1, next_fire_at = $2, updated_at = NOW()
         WHERE id = $3
    `
    // ... execute + commit ...

    // Append schedule.fired event (outside transaction, best-effort)
    d.events.Append(ctx, event.Event{Type: event.EventTypeScheduleFired, ...})
    return nil
}
```

**Jitter strategy (Claude's Discretion):** Add `rand.Int63n(5000)` milliseconds (up to 5s) to the next tick start time to prevent thundering herd when multiple scheduler replicas start simultaneously. Each replica draws its own jitter, so they naturally stagger. Jitter is applied to the ticker interval, not to `next_fire_at` in the database — the database time is the source of truth.

**Missed-window detection:** When `last_fire_at` is old and multiple windows have elapsed, compute `missed = count windows between last_fire_at and now`, fire the most recent one (D-04 LatestOnly), set `next_fire_at = sched.Next(now)`, emit `schedule.missed` event with `skipped_count = missed - 1` in payload.

---

## Pattern 4: Priority-Aware Claim SQL

**Exact query change to `internal/run/claim.go`:** [VERIFIED against existing claim.go source]

The existing ORDER BY is:
```sql
ORDER BY queued_at
```

Phase 3 change (priority-then-FIFO, D-13):
```sql
ORDER BY
    CASE priority
        WHEN 'critical' THEN 0
        WHEN 'normal'   THEN 1
        WHEN 'backfill' THEN 2
        ELSE 1
    END ASC,
    queued_at ASC
```

The full updated SELECT query:
```sql
SELECT id, asset_name, trigger, queued_at, partition_key, priority, backfill_id
FROM runs
WHERE state = 'queued'
ORDER BY
    CASE priority
        WHEN 'critical' THEN 0
        WHEN 'normal'   THEN 1
        WHEN 'backfill' THEN 2
        ELSE 1
    END ASC,
    queued_at ASC
FOR UPDATE SKIP LOCKED
LIMIT 1
```

**Why this preserves the 50-goroutine test:** The test inserts one `state='queued'` run with `priority='normal'` and asserts exactly one goroutine wins. The ORDER BY change only affects which row is selected when multiple rows are eligible — with a single row, the ordering is irrelevant. SKIP LOCKED + `WHERE state='queued'` + the defense-in-depth `WHERE id=$N AND state='queued'` UPDATE guard all remain intact. [VERIFIED: claim_test.go reviewed — test inserts `priority` default value (`normal`)]

**Index required for priority-aware scan:**
```sql
-- Already planned in migration fragment above
CREATE INDEX run_state_priority_queued_at ON runs (state, priority, queued_at);
```
PostgreSQL will use this for `WHERE state='queued' ORDER BY priority_case, queued_at` because the CASE expression over a low-cardinality column (`critical|normal|backfill`) on a pre-filtered set is cheap. The index on `(state, priority, queued_at)` avoids a sort step. [ASSUMED — standard Postgres index selection behavior; exact plan should be verified with EXPLAIN ANALYZE in the 1000-backfill load test]

**ClaimedRun struct update:**
```go
type ClaimedRun struct {
    ID           uuid.UUID
    AssetName    string
    Trigger      string
    QueuedAt     time.Time
    PartitionKey *string   // nil for non-partitioned runs
    Priority     string    // "critical" | "normal" | "backfill"
    BackfillID   *uuid.UUID // nil for non-backfill runs
}
```

---

## Pattern 5: Sensor Evaluation Harness

**Safety contract:** Sensors are user-supplied code. The harness must: (1) enforce a per-evaluation timeout, (2) recover from panics, (3) propagate ctx cancellation. [ASSUMED - standard Go patterns for running untrusted user functions]

```go
// internal/sensor/evaluate.go
// Source: established Go pattern for safe user function execution [ASSUMED]

func safeEvaluate(
    ctx context.Context,
    spec asset.SensorSpec,
    timeout time.Duration, // default: min_interval (SensorSpec.MinInterval)
) (result asset.SensorResult, err error) {
    evalCtx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()

    defer func() {
        if r := recover(); r != nil {
            err = fmt.Errorf("sensor %q panic: %v", spec.Name, r)
        }
    }()
    return spec.Sense(evalCtx)
}
```

**Dedup state persistence:** After a successful `Sense()` call that returns `Fired=true`:
1. Compare `result.RunKey` to `sensors.last_run_key`. If equal, emit `sensor.dedup_skipped`, no enqueue.
2. Check `NOW() < sensors.cooldown_until`. If true, emit `sensor.cooldown_skipped`, no enqueue.
3. Otherwise: INSERT run row, UPDATE `sensors.last_run_key=result.RunKey`, `last_fired_at=NOW()`, `cooldown_until=NOW()+spec.Cooldown`.

All three steps are within one transaction to prevent a partial update racing with a second tick. [VERIFIED: the SELECT FOR UPDATE SKIP LOCKED on the sensors row is the lock mechanism]

**Consecutive failure handling (D-08):**

```go
func (d *Daemon) handleSenseError(ctx context.Context, sensorID uuid.UUID, err error, threshold int) {
    // 1. Increment consecutive_failures in DB.
    // 2. Emit sensor.evaluation_failed event.
    // 3. Check if threshold reached => set disabled_at, emit sensor.disabled.
    const updateSQL = `
        UPDATE sensors
           SET consecutive_failures = consecutive_failures + 1,
               last_evaluated_at    = NOW(),
               updated_at           = NOW(),
               disabled_at          = CASE
                   WHEN consecutive_failures + 1 >= $1 THEN NOW()
                   ELSE disabled_at
               END
         WHERE id = $2
    `
    // ...
}
```

**Auto-reset on success (Claude's Discretion — recommend yes):** On a successful evaluation reset `consecutive_failures = 0`. This means a sensor that fails 59 times and then succeeds once starts fresh. This avoids operators being woken up for a sensor that self-recovered. [ASSUMED — reasonable default; document in code comment]

---

## Pattern 6: Partition Key Generation

**All algorithms are UTC-based (D-11). TZ on the spec is only for cron alignment.** [VERIFIED: D-11 locks this]

```go
// internal/partition/keygen.go
// Source: Go time stdlib + ISO 8601 specification [VERIFIED: Go 1.25 stdlib has ISOWeek()]

// DailyKey returns the UTC date of the day containing t.
// Example: "2024-01-15"
func DailyKey(t time.Time) string {
    u := t.UTC()
    return u.Format("2006-01-02")
}

// WeeklyKey returns the ISO 8601 week key for the week containing t.
// ISO week starts Monday. Week 1 is the week containing the first Thursday of the year.
// Example: "2024-W03" (zero-padded to 2 digits)
func WeeklyKey(t time.Time) string {
    u := t.UTC()
    year, week := u.ISOWeek() // [VERIFIED: Go stdlib time.Time.ISOWeek() — available since Go 1.0]
    return fmt.Sprintf("%d-W%02d", year, week)
}

// MonthlyKey returns the UTC year-month key for the month containing t.
// Example: "2024-01"
func MonthlyKey(t time.Time) string {
    u := t.UTC()
    return u.Format("2006-01")
}
```

**ISO week edge cases (WeeklyKey):** [VERIFIED: Go stdlib ISOWeek() documentation]

The Go `time.ISOWeek()` method correctly handles:
- **Year boundary:** December 28-31 may belong to ISO week 1 of the next year. Example: `2019-12-30` → `"2020-W01"`. ISOWeek() returns `(2020, 1)` not `(2019, 53)`.
- **Year boundary backward:** January 1-3 may belong to the last week of the previous year. Example: `2015-01-01` → `"2015-W01"` or `"2014-W53"` depending on weekday. ISOWeek() returns the correct ISO year.
- **Long years (53 weeks):** Some years have ISO week 53 (e.g., 2015 had week 53). ISOWeek() handles this correctly.
- **No special handling needed** — `time.ISOWeek()` implements RFC 5545 / ISO 8601 correctly.

**Partition key validation:** Keys must be `<=128 chars` (D-10 `VARCHAR(128)`). Daily/Weekly/Monthly keys are at most 8/9/7 chars respectively. Category keys are user-supplied — the builder should validate `len(key) <= 128 && !strings.Contains(key, "/")` to prevent path-injection confusion in downstream lineage tools.

**CurrentKey (for Schedule→Partition composition, D-12):** When a cron schedule fires, it enqueues the "current" partition. Convention: use the partition window that *contains* `now - 1 window` (i.e., yesterday's daily, last week's weekly). This aligns with Dagster's "cron fires for the preceding window" behavior. [ASSUMED — Dagster convention; document as configurable offset, defaulting to "previous window"]

```go
// CurrentDailyKey: for a daily cron firing at midnight, the "current" partition is yesterday.
func CurrentDailyKey(now time.Time, offset time.Duration) string {
    return DailyKey(now.Add(-offset))
    // Default offset: 24*time.Hour (yesterday)
}
```

---

## Pattern 7: Backfill Mass-Enqueue

**Transactional batch insert (D-15):**

```go
// internal/backfill/submit.go
func Submit(ctx context.Context, store storage.Storage, events event.Writer,
    assetName string, partitionKeys []string, priority string) (uuid.UUID, error) {

    backfillID := uuid.New()
    now := time.Now().UTC()

    tx, err := store.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
    defer tx.Rollback()

    // 1. Insert backfill record.
    const insertBackfill = `
        INSERT INTO backfills (id, asset_name, partition_spec, status, total_partitions, submitted_at)
        VALUES ($1, $2, $3, 'submitted', $4, $5)
    `
    // ...

    // 2. Batch insert run rows. Use pgx CopyFrom for large batches (>1000 rows),
    //    or multi-row VALUES for smaller batches.
    //    For 365 rows (typical annual backfill), multi-row VALUES is fine.
    //    For >1000 rows, use pgx COPY protocol to avoid per-row round trips.
    //
    //    IMPORTANT: Each run row MUST have backfill_id set so status aggregation works.
    const insertRunSQL = `
        INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority, partition_key, backfill_id)
        VALUES ($1, $2, 'queued', 'backfill', $3, $4, $5, $6)
        ON CONFLICT (asset_name, partition_key) WHERE state IN ('queued','starting','running')
            DO NOTHING  -- skip if partition already in-flight (idempotent resubmit)
    `
    for _, pk := range partitionKeys {
        if _, err := tx.ExecContext(ctx, insertRunSQL,
            uuid.New(), assetName, now, priority, pk, backfillID); err != nil {
            return uuid.Nil, fmt.Errorf("backfill: insert run for partition %q: %w", pk, err)
        }
    }

    tx.Commit()

    // 3. Emit backfill.submitted event.
    events.Append(ctx, event.Event{Type: event.EventTypeBackfillSubmitted, ...})
    return backfillID, nil
}
```

**Status aggregation query:**

```sql
-- ./platform backfill status <backfill_id>
SELECT
    b.id              AS backfill_id,
    b.asset_name,
    b.total_partitions,
    b.submitted_at,
    r.state,
    COUNT(*)          AS count
FROM backfills b
LEFT JOIN runs r ON r.backfill_id = b.id
WHERE b.id = $1
GROUP BY b.id, b.asset_name, b.total_partitions, b.submitted_at, r.state
ORDER BY r.state;
```

**Backfill row-count limits:** 365 rows (1 year daily) is safe. 3650 rows (10 years daily) is safe. 36500 rows (100 years daily) is functionally absurd but still within Postgres INSERT limits. The real risk is `max_concurrent_backfill` — if set too high (e.g., 100), 100 parallel connections will saturate the connector. Default `max_concurrent_backfill = 5` is reasonable. [ASSUMED — based on typical Postgres connection pool sizing]

**backfill_id recommendation (Claude's Discretion):** Use `uuid.New()`. UUIDs are operator-copyable (36 chars), collision-proof, and consistent with all other IDs in the schema. Timestamp-prefixed strings would be sortable by submission time but add parsing complexity.

---

## Pattern 8: Partition-Spec Parsing (CLI)

**Three spec formats (D-14):**

```go
// internal/backfill/submit.go or cmd/platform/backfill.go
// Source: standard Go string/time parsing [ASSUMED]

type PartitionSpec struct {
    Keys []string // always resolved to a flat list
}

func ParsePartitionSpec(strategy partition.PartitionStrategy, spec string) (PartitionSpec, error) {
    // 1. Date range: "2024-01-01:2024-12-31"
    if strings.Contains(spec, ":") {
        parts := strings.SplitN(spec, ":", 2)
        start, err := time.Parse("2006-01-02", parts[0])
        if err != nil { return PartitionSpec{}, fmt.Errorf("invalid start date: %w", err) }
        end, err := time.Parse("2006-01-02", parts[1])
        if err != nil { return PartitionSpec{}, fmt.Errorf("invalid end date: %w", err) }
        if end.Before(start) { return PartitionSpec{}, fmt.Errorf("end date before start date") }
        keys, err := partition.KeysBetween(strategy, start, end) // generates all keys in window
        return PartitionSpec{Keys: keys}, err
    }

    // 2. Comma list: "us,eu,apac" or "2024-01-01,2024-01-02"
    if strings.Contains(spec, ",") {
        keys := strings.Split(spec, ",")
        for i := range keys { keys[i] = strings.TrimSpace(keys[i]) }
        return PartitionSpec{Keys: keys}, nil
    }

    // 3. Single key: "2024-01-15" or "us"
    return PartitionSpec{Keys: []string{strings.TrimSpace(spec)}}, nil
}
```

**`KeysBetween` for date range expansion:**

```go
// partition/keygen.go
func KeysBetween(strategy PartitionStrategy, start, end time.Time) ([]string, error) {
    var keys []string
    current := start.UTC().Truncate(24 * time.Hour) // start of day UTC
    endDay := end.UTC().Truncate(24 * time.Hour)

    switch s := strategy.(type) {
    case DailyPartitions:
        for !current.After(endDay) {
            keys = append(keys, DailyKey(current))
            current = current.AddDate(0, 0, 1)
        }
    case WeeklyPartitions:
        // Advance to start of ISO week containing `start`
        // then advance by 7 days each iteration
        weekStart := isoWeekStart(current)
        for !weekStart.After(endDay) {
            keys = append(keys, WeeklyKey(weekStart))
            weekStart = weekStart.AddDate(0, 0, 7)
        }
    case MonthlyPartitions:
        for !current.After(endDay) {
            keys = append(keys, MonthlyKey(current))
            current = current.AddDate(0, 1, 0)
        }
    default:
        return nil, fmt.Errorf("partition: KeysBetween not supported for %T (use comma list or single key)", strategy)
    }
    return keys, nil
}

// isoWeekStart returns the Monday (UTC) that starts the ISO week containing t.
func isoWeekStart(t time.Time) time.Time {
    u := t.UTC()
    weekday := u.Weekday()
    if weekday == time.Sunday { weekday = 7 } // ISO: Sunday = 7
    daysFromMonday := int(weekday) - 1
    return u.AddDate(0, 0, -daysFromMonday).Truncate(24 * time.Hour)
}
```

---

## Pattern 9: CLI Subcommand Wiring

**`cmd/platform/main.go` switch extension:**

```go
// cmd/platform/main.go — additive cases
switch cmd {
// ... existing cases (start, migrate, healthcheck, worker, materialize) ...
case "scheduler":
    if err := runScheduler(); err != nil {
        slog.Error("platform.scheduler_failed", "error", err)
        os.Exit(1)
    }
case "backfill":
    sub := ""
    if len(os.Args) > 2 { sub = os.Args[2] }
    switch sub {
    case "status":
        if err := runBackfillStatus(os.Args[3:]); err != nil {
            slog.Error("platform.backfill_status_failed", "error", err)
            os.Exit(1)
        }
    default:
        if err := runBackfill(os.Args[2:]); err != nil {
            slog.Error("platform.backfill_failed", "error", err)
            os.Exit(1)
        }
    }
// ...
}
```

**`cmd/platform/backfill.go` flag parsing:**

```go
// cmd/platform/backfill.go
func runBackfill(args []string) error {
    fs := flag.NewFlagSet("backfill", flag.ExitOnError)
    partitionsFlag := fs.String("partitions", "", "Date range (2024-01-01:2024-12-31), comma list, or single key")
    priorityFlag   := fs.String("priority", "backfill", "Run priority: critical|normal|backfill")
    fs.Parse(args)

    if fs.NArg() < 1 { return fmt.Errorf("usage: backfill <asset> --partitions=<spec>") }
    assetName := fs.Arg(0)
    // ... resolve strategy, parse spec, call backfill.Submit() ...
}
```

---

## Pattern 10: Builder DSL Extension

**`internal/asset/builder.go` additions:**

```go
// .Schedule(expr) — attach cron expression (ORCH-05)
func (b *Builder) Schedule(cronExpr string) *Builder {
    b.a.schedule = &ScheduleSpec{CronExpr: cronExpr}
    return b
}

// .Sensor(spec) — attach event sensor (ORCH-06)
func (b *Builder) Sensor(spec SensorSpec) *Builder {
    b.a.sensors = append(b.a.sensors, spec)
    return b
}

// .Partitions(spec) — declare partition strategy (ORCH-07, ORCH-08)
// At most one strategy per asset (validated in Build()).
func (b *Builder) Partitions(strategy PartitionStrategy) *Builder {
    b.a.partitions = strategy
    return b
}
```

**`internal/asset/io.go` extension:**

```go
// AssetIO extended interface — additive, backwards compatible
type AssetIO interface {
    Read(ctx context.Context, upstream string) ([]connector.Row, error)
    Write(ctx context.Context, rows []connector.Row) (int64, error)
    // PartitionKey returns the partition key for the current run (D-09, D-10).
    // Returns "" for non-partitioned runs.
    PartitionKey() string
}
```

The `assetIO` struct gains a `partitionKey string` field, populated from the `ClaimedRun.PartitionKey` passed through the executor into `NewAssetIO(a, resolver, partitionKey)`.

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Cron expression parsing | Custom cron parser | `robfig/cron/v3` NewParser + `Schedule.Next()` | Edge cases in DST, leap years, DOM/DOW interaction are validated by thousands of users |
| ISO week calculation | Custom ISOWeek logic | Go stdlib `time.ISOWeek()` | Go stdlib correctly handles year-boundary weeks (ISO 8601 compliant since Go 1.0) |
| Multi-replica scheduler safety | Redis locks, ZooKeeper, leader election | `SELECT ... FOR UPDATE SKIP LOCKED` (already in use for run claiming) | Same primitive already tested with 50-goroutine atomicity test |
| Sensor goroutine leak prevention | Manual goroutine tracking | `context.WithTimeout` + `defer cancel()` wrapping each Sense() call | Context propagation is the canonical Go pattern; timeout is enforced at the OS scheduler level |
| Partition uniqueness enforcement | Application-level check-then-insert | Partial unique index `(asset_name, partition_key) WHERE state IN ('queued','starting','running')` | Database enforces atomically; application check-then-insert has TOCTOU race |
| Backfill progress tracking | Custom coordinator goroutine | `SELECT state, count(*) FROM runs WHERE backfill_id=$1 GROUP BY state` | The runs table is already the ground truth; no additional coordinator needed |

---

## Runtime State Inventory

This phase adds new schema (schedules, sensors, backfills tables + run columns) but introduces no new runtime names that survive process restart in external systems. Step 2.5 is not applicable — this is a greenfield addition, not a rename/refactor.

None — verified by inspection of Phase 3 scope (new tables, new columns, new subcommands; no renamed strings that would exist in external datastores).

---

## Common Pitfalls

### Pitfall 1: Priority ORDER BY Breaks SKIP LOCKED Guarantee
**What goes wrong:** Developer adds `WHERE priority != 'backfill'` to the SELECT to "skip backfills". SKIP LOCKED skips locked rows, not the ORDER BY preference. A `WHERE priority != 'backfill'` filter would make normal workers blind to backfill rows even when no normal rows exist, potentially leaving backfill rows stranded.
**Why it happens:** Confusion between "claiming in priority order" (use ORDER BY) and "filtering by priority" (use WHERE).
**How to avoid:** Priority belongs only in ORDER BY. The WHERE clause must remain `WHERE state = 'queued'` (plus the defense-in-depth UPDATE guard). SKIP LOCKED ensures multi-replica safety; ORDER BY ensures normal runs are claimed before backfill runs.
**Warning signs:** Backfill rows remain in 'queued' state indefinitely even when workers are idle.

### Pitfall 2: Sensor Goroutine Leak on Panic
**What goes wrong:** Sensor's `Sense()` function panics. Without `recover()`, the goroutine dies but the scheduler tick loop continues. Eventually no sensor evaluations happen but no error is logged.
**Why it happens:** Missing panic recovery in sensor evaluation goroutine.
**How to avoid:** Wrap every `Sense()` call in `safeEvaluate()` which uses `defer recover()`. Test with a sensor that panics.
**Warning signs:** `consecutive_failures` stops incrementing despite no successful evaluations; sensor daemon silently does nothing.

### Pitfall 3: Tick-Loop Deadlock from Long Sense() Calls
**What goes wrong:** A sensor's `Sense()` function hangs (external HTTP call with no timeout). The tick loop goroutine is blocked. No schedules or other sensors are evaluated until the hung call returns or ctx is canceled.
**Why it happens:** Sensors are user code; they may not respect context cancellation. The scheduler daemon runs sensors sequentially within a tick.
**How to avoid:** Enforce a per-sensor timeout in `safeEvaluate()` using `context.WithTimeout`. Default timeout: `spec.MinInterval` (user has acknowledged they want evaluation at least this often). Document that `Sense()` functions MUST respect context cancellation.
**Warning signs:** Scheduler tick duration suddenly exceeds `interval`; `last_evaluated_at` stops updating for all sensors.

### Pitfall 4: Partition Key Encoding Ambiguity
**What goes wrong:** Category partition key `"2024-01-15"` looks like a daily date key. User accidentally submits a category backfill with a date key, runs are created with the wrong semantics.
**Why it happens:** All keys are stored as VARCHAR strings; there is no type tag in the database.
**How to avoid:** The asset definition carries the `PartitionStrategy` type. Backfill submission must pass the strategy type (resolved from the DefinitionRegistry) to the spec parser, which validates keys against the strategy. For `CategoryPartitions`, validate that keys are in the declared `Keys []string`. For `DailyPartitions`, validate format matches `YYYY-MM-DD`.
**Warning signs:** Runs created for `"2024-01-15"` on an asset that uses `CategoryPartitions{Keys: ["us","eu"]}`.

### Pitfall 5: Priority Enum Integer Drift
**What goes wrong:** A new developer adds `critical=1, normal=2, backfill=3` in a different code path, disagreeing with `critical=0, normal=1, backfill=2`. ORDER BY behavior changes silently.
**Why it happens:** The CASE expression in SQL and any in-memory priority sorting in Go must agree.
**How to avoid:** Define a single `PriorityOrder(p string) int` function in `internal/run/claim.go` that is the single source of truth for the integer mapping. All code paths that compare priorities call this function. Add a unit test that asserts `PriorityOrder("critical") < PriorityOrder("normal") < PriorityOrder("backfill")`.
**Warning signs:** Backfill runs occasionally claim before normal runs in high-load tests.

### Pitfall 6: Backfill Row-Count Blowup at Submission
**What goes wrong:** Operator submits `--partitions=1990-01-01:2026-12-31` for a daily partition asset. The system attempts to insert 13,149 rows in a single transaction, holding an exclusive lock on the table for several seconds.
**Why it happens:** D-15 accepts "enqueue all immediately" but doesn't specify a batch-size limit.
**How to avoid:** Add a configurable `--max-partitions` guard (default 3650 = 10 years daily). Return a clear error if the computed key count exceeds the limit, requiring the operator to narrow the range or override explicitly with `--max-partitions=N`. Document the limit.
**Warning signs:** Submission takes >30s; other run inserts timeout during the backfill transaction.

### Pitfall 7: Partial Unique Index Missing on partition_key
**What goes wrong:** The unique index on `(asset_name, partition_key)` is created without the `WHERE state IN ('queued','starting','running')` predicate. Then a succeeded partition cannot be re-run via backfill (the unique constraint rejects the new queued row even though the previous run is in terminal state).
**Why it happens:** Developer writes a full unique index rather than the partial index from D-10.
**How to avoid:** Use `CREATE UNIQUE INDEX ... WHERE state IN ('queued','starting','running')`. Verify with a test: insert a succeeded run with `partition_key='2024-01-01'`, then insert a new queued run with the same key — it must succeed.
**Warning signs:** Backfill rows fail to insert with unique constraint violation even for historical partitions.

---

## Code Examples

### robfig/cron/v3 Parser Initialization and Next() Call

```go
// Source: pkg.go.dev/github.com/robfig/cron/v3 [VERIFIED]
import "github.com/robfig/cron/v3"

// Build once at daemon startup.
var cronParser = cron.NewParser(
    cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

func computeNextFireAt(expr string, lastFiredAt time.Time) (time.Time, error) {
    sched, err := cronParser.Parse(expr)
    if err != nil {
        return time.Time{}, fmt.Errorf("invalid cron expression %q: %w", expr, err)
    }
    if lastFiredAt.IsZero() {
        lastFiredAt = time.Unix(0, 0).UTC()
    }
    return sched.Next(lastFiredAt.UTC()), nil
}
```

### ISO Week Key Generation

```go
// Source: Go stdlib time.ISOWeek() [VERIFIED]
func WeeklyKey(t time.Time) string {
    year, week := t.UTC().ISOWeek()
    return fmt.Sprintf("%d-W%02d", year, week)
}

// Edge case verification:
// time.Date(2019, 12, 30, 0, 0, 0, 0, time.UTC).ISOWeek() → (2020, 1) → "2020-W01" ✓
// time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC).ISOWeek()  → (2015, 1) → "2015-W01" ✓
// time.Date(2015, 12, 31, 0, 0, 0, 0, time.UTC).ISOWeek() → (2015, 53) → "2015-W53" ✓
```

### Priority-Aware Claim Query (Updated claim.go)

```go
// Source: internal/run/claim.go [VERIFIED: existing code reviewed]
const selectSQL = `
    SELECT id, asset_name, trigger, queued_at, partition_key, priority, backfill_id
    FROM runs
    WHERE state = 'queued'
    ORDER BY
        CASE priority
            WHEN 'critical' THEN 0
            WHEN 'normal'   THEN 1
            WHEN 'backfill' THEN 2
            ELSE 1
        END ASC,
        queued_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
`
```

### Event Type Registration (D-17)

```go
// Source: internal/event/types.go [VERIFIED: existing code reviewed]
// Phase 3 additions to AllKnownTypes():

// Schedule events (D-17)
EventTypeScheduleFired   EventType = "schedule.fired"
EventTypeScheduleMissed  EventType = "schedule.missed"
EventTypeSchedulePaused  EventType = "schedule.paused"
EventTypeScheduleResumed EventType = "schedule.resumed"

// Sensor events (D-17)
EventTypeSensorEvaluated        EventType = "sensor.evaluated"
EventTypeSensorFired            EventType = "sensor.fired"
EventTypeSensorEvaluationFailed EventType = "sensor.evaluation_failed"
EventTypeSensorDisabled         EventType = "sensor.disabled"
EventTypeSensorCooldownSkipped  EventType = "sensor.cooldown_skipped"
EventTypeSensorDedupSkipped     EventType = "sensor.dedup_skipped"

// Backfill events (D-17)
EventTypeBackfillSubmitted   EventType = "backfill.submitted"
EventTypeBackfillRunEnqueued EventType = "backfill.run_enqueued"
EventTypeBackfillCompleted   EventType = "backfill.completed"
```

### Backfill Status Aggregation

```go
// Source: custom, uses existing runs table + backfills table [VERIFIED: schema reviewed]
type BackfillStatus struct {
    BackfillID      uuid.UUID
    AssetName       string
    TotalPartitions int
    SubmittedAt     time.Time
    StateCounts     map[string]int // "queued"->N, "running"->N, "succeeded"->N, "failed"->N
}

func GetBackfillStatus(ctx context.Context, db *sql.DB, backfillID uuid.UUID) (*BackfillStatus, error) {
    const q = `
        SELECT b.id, b.asset_name, b.total_partitions, b.submitted_at,
               COALESCE(r.state, 'unknown'), COUNT(r.id)
        FROM backfills b
        LEFT JOIN runs r ON r.backfill_id = b.id
        WHERE b.id = $1
        GROUP BY b.id, b.asset_name, b.total_partitions, b.submitted_at, r.state
    `
    // ... scan rows into BackfillStatus ...
}
```

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Separate scheduler process with leader election (Dagster OSS daemon = single-instance) | Multi-replica scheduler via `SELECT FOR UPDATE SKIP LOCKED` (no leader election needed) | Phase 3 design decision | Simplifies deployment — any number of scheduler pods can run |
| Batch-by-batch backfill coordinator (Dagster RunQueueDaemon manages queue drain) | Enqueue-all-immediately + token pool cap (D-15) | Phase 3 design decision | No coordinator goroutine to crash; simpler failure recovery |
| Cron expression parsing via hand-written tokenizer | robfig/cron/v3 parser | Industry standard since 2019 | Handles all edge cases including `@every`, descriptors, DOM/DOW interaction |

**Deprecated/outdated:**
- In-process Cron runner (`cron.New().AddFunc(...).Start()`): Not used in Phase 3. The built-in runner is fine for standalone tools but bypasses the database coordination primitive required for multi-replica safety.

---

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | `safeEvaluate` with `context.WithTimeout` + `recover()` is sufficient for sensor isolation; no goroutine pool needed | Pattern 5 | If sensors do blocking system calls that ignore ctx (e.g., raw `net.Dial` without timeout), the goroutine leaks despite the context timeout. Mitigate: document that Sense() must use ctx in all IO. |
| A2 | CASE expression in ORDER BY on `(state, priority, queued_at)` index gives adequate claim performance under load | Pattern 4 | If EXPLAIN ANALYZE shows a sequential scan instead of index scan under the 1000-backfill+50-normal load test, add a generated column for `priority_order int GENERATED ALWAYS AS (CASE priority WHEN 'critical' THEN 0 ...)`. |
| A3 | Default `max_concurrent_backfill = 5` is appropriate for typical connector throughput | Pattern 7 | Too high (>10) may saturate connectors; too low (<2) makes backfills slow. Recommended: make configurable at daemon startup. |
| A4 | `CurrentDailyKey` convention is "previous window" (yesterday for daily, last week for weekly) | Pattern 6 | Some users may expect "current window". Make the offset configurable per asset if feedback arises. |
| A5 | Auto-reset `consecutive_failures = 0` on first successful Sense() evaluation | Pattern 5 | A sensor that fails 59/60 evaluations and succeeds once then enters another failure run will never auto-disable. Consider: require N consecutive successes to reset, or reset on success (default recommended). |
| A6 | robfig/cron/v3 v3.0.1 (2020) is still the correct version to pin | Standard Stack | Library is maintenance-only — no breaking changes expected. Verify with `go list -m github.com/robfig/cron/v3` after `go get`. |

**If this table is empty:** N/A — assumptions identified above.

---

## Open Questions

1. **`SensorResult.PartitionKey` when unset and asset has partitions (D-12 + deferred)**
   - What we know: D-12 says if PartitionKey is empty, fire "latest current partition"
   - What's unclear: "latest current" is undefined — is it the partition for `now`? `now-1window`? The most recently-succeeded partition?
   - Recommendation: Planner should pin this as a planning decision (not deferred). Recommendation: use `CurrentKey(strategy, now)` (which defaults to previous window). Document this as `SensorSpec.DefaultPartitionOffset`.

2. **Backfill `--priority` flag default**
   - What we know: D-14 says `[--priority=backfill]` implying it's optional with default `backfill`
   - What's unclear: Should operators be able to submit a backfill at `normal` priority for catch-up scenarios?
   - Recommendation: Allow `critical|normal|backfill`; default `backfill`. Validate at CLI flag parsing.

3. **Schedule registration: auto-create `schedules` row at daemon start vs. at asset registration**
   - What we know: D-02 says schedules table is lazy — the row exists persistently.
   - What's unclear: When is the row created? At daemon start (daemon scans registry, upserts rows)? Or at `Register()` call? [ASSUMED: at daemon start via `UPSERT INTO schedules (...) ON CONFLICT (asset_name) DO UPDATE SET cron_expr=$X WHERE cron_expr != $X`]
   - Recommendation: Daemon start UPSERT. This ensures `next_fire_at` is computed fresh after any cron expression change between deployments.

---

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| PostgreSQL | Scheduler tick loop (FOR UPDATE SKIP LOCKED), backfill mass-enqueue | ✓ (via testcontainers) | 16+ per CLAUDE.md | — |
| `robfig/cron/v3` | Cron expression parsing | not in go.mod (needs `go get`) | v3.0.1 | — (no fallback; must add) |
| Go 1.25 stdlib (`time.ISOWeek`) | Weekly partition key generation | ✓ | 1.25.0 [VERIFIED] | — |

**Missing dependencies with no fallback:**
- `github.com/robfig/cron/v3@v3.0.1` — must be added to go.mod. Command: `go get github.com/robfig/cron/v3@v3.0.1`

---

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go testing package (stdlib) + testify v1.11.1 (already in go.mod) |
| Config file | none — DATABASE_URL env var for integration tests (per claim_test.go pattern) |
| Quick run command | `go test ./internal/partition/... ./internal/run/... -count=1 -timeout 30s` |
| Full suite command | `DATABASE_URL=... go test ./internal/... ./cmd/... -count=1 -timeout 120s` |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| ORCH-05 | Cron-scheduled asset auto-fires at next scheduled time after daemon start | Integration | `DATABASE_URL=... go test ./internal/schedule/... -run TestScheduler -v` | ❌ Wave 0 |
| ORCH-05 | Missed-window LatestOnly recovery emits schedule.missed with correct skip count | Unit | `go test ./internal/schedule/... -run TestMissedWindowLatestOnly` | ❌ Wave 0 |
| ORCH-05 | Invalid cron expression returns error at builder time (not runtime) | Unit | `go test ./internal/asset/... -run TestScheduleInvalidCron` | ❌ Wave 0 |
| ORCH-06 | Sensor fires materialization when Sense() returns Fired=true | Integration | `DATABASE_URL=... go test ./internal/sensor/... -run TestSensorFire` | ❌ Wave 0 |
| ORCH-06 | RunKey dedup prevents second enqueue for same key | Unit | `go test ./internal/sensor/... -run TestSensorRunKeyDedup` | ❌ Wave 0 |
| ORCH-06 | Cooldown window prevents enqueue during cooldown | Unit | `go test ./internal/sensor/... -run TestSensorCooldown` | ❌ Wave 0 |
| ORCH-06 | Panic in Sense() is recovered; consecutive_failures incremented | Unit | `go test ./internal/sensor/... -run TestSensorPanicRecovery` | ❌ Wave 0 |
| ORCH-06 | After N consecutive failures sensor is disabled | Unit | `go test ./internal/sensor/... -run TestSensorAutoDisable` | ❌ Wave 0 |
| ORCH-07 | DailyKey/WeeklyKey/MonthlyKey produce correct UTC strings | Unit | `go test ./internal/partition/... -run TestPartitionKeyGen` | ❌ Wave 0 |
| ORCH-07 | ISO week edge case: 2019-12-30 → "2020-W01" | Unit | `go test ./internal/partition/... -run TestWeeklyKeyYearBoundary` | ❌ Wave 0 |
| ORCH-07 | KeysBetween(daily, 2024-01-01, 2024-01-31) returns 31 keys | Unit | `go test ./internal/partition/... -run TestKeysBetweenDaily` | ❌ Wave 0 |
| ORCH-07 | Time-partitioned backfill: each partition is its own run with its own event log entries | Integration | `DATABASE_URL=... go test ./internal/backfill/... -run TestBackfillTimePartition` | ❌ Wave 0 |
| ORCH-08 | CategoryPartitions: each category is an independent run; one failure does not block others | Integration | `DATABASE_URL=... go test ./internal/backfill/... -run TestCategoryPartitionIndependence` | ❌ Wave 0 |
| D-10 | Unique constraint prevents duplicate in-flight partition runs | Integration | `DATABASE_URL=... go test ./internal/partition/... -run TestPartitionUniqueConstraint` | ❌ Wave 0 |
| D-13 | Priority-aware claim: normal runs claimed before backfill runs | Integration | `DATABASE_URL=... go test ./internal/run/... -run TestClaimPriorityOrdering` | ❌ Wave 0 |
| D-13 | **50-goroutine claim atomicity test MUST STILL PASS** (Phase 2 regression guard) | Integration | `DATABASE_URL=... go test ./internal/run/... -run TestClaimAtomicity50Goroutines` | ✅ claim_test.go |
| D-13 (deferred) | **1000-backfill + 50-normal priority-claim load test** | Load | `DATABASE_URL=... go test ./internal/run/... -run TestPriorityClaimLoad -timeout 300s` | ❌ Wave 0 |
| D-17 | All Phase 3 event_type values accepted by event.Writer | Unit | `go test ./internal/event/... -run TestAllPhase3EventTypes` | ❌ Wave 0 |
| D-14 | Backfill CLI spec parsing: date range, comma list, single key | Unit | `go test ./internal/backfill/... -run TestParsePartitionSpec` | ❌ Wave 0 |

**Load test detail (1000-backfill + 50-normal):**
```
TestPriorityClaimLoad:
  1. Insert 1000 runs with priority='backfill'
  2. Insert 50 runs with priority='normal'
  3. Spawn 50 concurrent goroutines calling ClaimNext
  4. Assert all 50 goroutines claimed 'normal' priority runs (none claimed backfill)
  5. Spawn 50 more goroutines; assert they claim backfill runs (no more normal left)
  6. Assert zero duplicate claims across both rounds (SKIP LOCKED atomicity preserved)
```

### Sampling Rate
- **Per task commit:** `go test ./internal/partition/... ./internal/run/... -count=1 -timeout 30s`
- **Per wave merge:** `DATABASE_URL=... go test ./internal/... -count=1 -timeout 120s`
- **Phase gate:** Full suite green (including load test) before `/gsd-verify-work`

### Wave 0 Gaps
All test files in the new packages must be created in Wave 0 (before implementation tasks):
- [ ] `internal/partition/keygen_test.go` — covers ORCH-07 + ISO edge cases
- [ ] `internal/schedule/fire_test.go` — covers ORCH-05 tick logic
- [ ] `internal/sensor/evaluate_test.go` — covers ORCH-06 dedup + panic + disable
- [ ] `internal/backfill/submit_test.go` — covers ORCH-07/08 + D-14 spec parsing
- [ ] `internal/run/claim_test.go` — **ALREADY EXISTS** (TestClaimAtomicity50Goroutines); extend with `TestClaimPriorityOrdering` and `TestPriorityClaimLoad`

---

## Security Domain

The `security_enforcement` key is not present in `.planning/config.json` (file not observed), so enforcement is treated as enabled.

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | no | n/a (scheduler daemon runs as platform process, not user-facing) |
| V3 Session Management | no | n/a |
| V4 Access Control | yes | Backfill CLI must reject `--priority=critical` for non-admin callers when auth is wired (Phase 4+); for now CLI is operator-level |
| V5 Input Validation | yes | Cron expression validated via robfig/cron/v3 parser before storing; partition_key max 128 chars; spec size limit for backfill |
| V6 Cryptography | no | n/a |

### Known Threat Patterns

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Malformed cron expression crashing daemon | Denial of Service | Validate at asset `Build()` / schedule registration before inserting to DB |
| Sensor Sense() function making unlimited external calls | Denial of Service | context.WithTimeout per evaluation; document MaxEvalDuration in SensorSpec |
| Backfill key injection (partition_key with SQL control chars) | Tampering | Keys stored as parameterized values; no string interpolation into SQL |
| Backfill row-count bomb (vast date range) | DoS | Configurable `--max-partitions` guard (see Pitfall 6) |
| Priority escalation (user submits backfill with priority='critical') | Elevation of Privilege | Validate priority value against enum at CLI parse time; enforce CHECK constraint in DB |
| event_log tampered by scheduler process | Tampering | Phase 1 RLS already prevents UPDATE/DELETE on event_log for platform_app role [VERIFIED: Phase 1 CONTEXT.md D-09] |

---

## Sources

### Primary (HIGH confidence)
- `internal/run/claim.go` [VERIFIED: source code reviewed] — SKIP LOCKED claim implementation
- `internal/run/claim_test.go` [VERIFIED: source code reviewed] — 50-goroutine atomicity test pattern
- `internal/event/types.go` [VERIFIED: source code reviewed] — EventType enum extension model
- `internal/asset/builder.go`, `asset.go`, `io.go` [VERIFIED: source code reviewed] — builder DSL surface to extend
- `internal/concurrency/pool.go` [VERIFIED: source code reviewed] — concurrency_tokens table schema + Acquire/Release API
- `go.mod` [VERIFIED: reviewed] — confirmed robfig/cron/v3 not yet in go.mod; all other deps present
- `migrations/20260507120000_phase2_run_tables.sql` [VERIFIED: reviewed] — runs table baseline
- `migrations/20260507121500_phase2_concurrency_tokens.sql` [VERIFIED: reviewed] — concurrency_tokens table baseline
- pkg.go.dev/github.com/robfig/cron/v3 [VERIFIED: WebFetch confirmed] — full API surface: NewParser, ParseOption flags, Schedule.Next() signature, error behavior
- proxy.golang.org/github.com/robfig/cron/v3/@latest [VERIFIED: curl] — v3.0.1 is current and only release

### Secondary (MEDIUM confidence)
- `.planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md` [CITED] — 17 locked decisions D-01..D-17
- `.planning/research/PITFALLS.md` §1, §2, §6 [CITED] — atomicity, concurrency pool, backfill isolation
- `.planning/research/ARCHITECTURE.md` §1.5 (Daemon internals), §Scheduler [CITED] — Dagster SchedulerDaemon reference
- Go 1.25 stdlib `time.ISOWeek()` [VERIFIED: go version confirmed 1.25.0] — ISO week calculation

### Tertiary (LOW confidence — see Assumptions Log)
- A2: CASE-in-ORDER-BY index selection behavior [ASSUMED — standard Postgres query planning knowledge, not verified with EXPLAIN]
- A3: Default max_concurrent_backfill=5 appropriateness [ASSUMED — based on typical Postgres pool sizes]

---

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — robfig/cron/v3 verified at pkg.go.dev; all other deps already in go.mod
- Architecture: HIGH — patterns derived directly from existing Phase 2 code (claim.go, pool.go, event types)
- Pitfalls: HIGH — derived from Phase 2 code review (claim_test.go) + PITFALLS.md (production-verified Dagster issues)
- Partition key generation: HIGH — Go stdlib ISOWeek() is authoritative; examples verified manually

**Research date:** 2026-05-08
**Valid until:** 2026-06-08 (robfig/cron/v3 is maintenance-only; ent/pgx/uuid already locked in go.mod)

---

## RESEARCH COMPLETE
