---
phase: 3
plan: 04
title: Scheduler tick loop — schedules table fire logic + missed-window LatestOnly + partition unique constraint test
type: execute
wave: 2
depends_on: [01, 02]
requirements: [ORCH-05, ORCH-07]
decisions_implemented: [D-01, D-02, D-03, D-04, D-10, D-12]
files_modified:
  - internal/schedule/daemon.go
  - internal/schedule/daemon_test.go
  - internal/schedule/fire.go
  - internal/schedule/fire_test.go
  - internal/schedule/missed.go
  - internal/schedule/missed_test.go
  - internal/schedule/registry.go
  - internal/partition/partition_unique_test.go
autonomous: true
must_haves:
  truths:
    - "schedule.Daemon's tick driver (unexported `run` method, used only by package-internal tests) ticks every configured interval (default 30s); first tick runs immediately on Run() start to handle missed windows"
    - "Each tick selects rows from schedules WHERE next_fire_at<=NOW() AND paused_at IS NULL using SELECT FOR UPDATE SKIP LOCKED, and fires one row at a time in its own transaction"
    - "Fire logic: INSERT into runs (state='queued', trigger='schedule', priority='normal', partition_key=<current partition or NULL>) and UPDATE schedules SET last_fire_at=NOW(), next_fire_at=sched.Next(NOW()) — same transaction"
    - "Missed-window detection emits schedule.missed event with skipped_count when more than one window has elapsed since last_fire_at; only the most recent window is fired (D-04 LatestOnly)"
    - "Schedule registration via UpsertSchedules(ctx, registry) inserts/updates rows for every Asset with a non-nil Schedule; idempotent across restarts"
    - "Partial unique index on runs (asset_name, partition_key) WHERE state IN ('queued','starting','running') rejects duplicate in-flight partition runs — TestPartitionUniqueConstraint proves both rejection and successful re-enqueue after terminal state"
    - "Schedule firing combined with .Partitions(daily) enqueues a partitioned run with partition_key = CurrentDailyKey(now, 24h) (D-12 + Open Question 1 default)"
    - "schedule.Daemon's tick method is UNEXPORTED — the production scheduler subcommand (plan 03-06) calls `schedule.FireOneSchedule()` directly, not `Daemon.Run`. Daemon's internal `run()` method exists for package-internal tests only and is not consumed by cmd/platform/scheduler.go."
  artifacts:
    - path: "internal/schedule/daemon.go"
      provides: "Daemon struct + unexported `run(ctx)` tick driver (package-internal test use only) + graceful shutdown"
      contains: "type Daemon struct"
    - path: "internal/schedule/fire.go"
      provides: "FireOneSchedule(): SELECT FOR UPDATE SKIP LOCKED + insert run + update schedule (single tx). EXPORTED so plan 03-06's scheduler subcommand can drive the tick loop."
      contains: "FOR UPDATE SKIP LOCKED"
    - path: "internal/schedule/missed.go"
      provides: "computeNextAndDetectMiss(sched, lastFiredAt, now): (nextFire, missedCount)"
      contains: "func computeNextAndDetectMiss"
    - path: "internal/schedule/registry.go"
      provides: "UpsertSchedules(ctx, registry): syncs asset.DefinitionRegistry → schedules table"
      contains: "func UpsertSchedules"
    - path: "internal/partition/partition_unique_test.go"
      provides: "TestPartitionUniqueConstraint integration test"
      contains: "TestPartitionUniqueConstraint"
  key_links:
    - from: "internal/schedule.Daemon.run (unexported)"
      to: "PostgreSQL schedules table"
      via: "tick loop SELECT next_fire_at <= NOW() FOR UPDATE SKIP LOCKED — used by package-internal tests only; production scheduler uses FireOneSchedule directly"
      pattern: "next_fire_at <= NOW().*FOR UPDATE SKIP LOCKED"
    - from: "internal/schedule.FireOneSchedule"
      to: "PostgreSQL runs table"
      via: "INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority, partition_key)"
      pattern: "INSERT INTO runs.*priority.*partition_key"
    - from: "internal/schedule.FireOneSchedule"
      to: "internal/event.Writer"
      via: "writer.Append(EventTypeScheduleFired) after successful tx commit"
      pattern: "EventTypeScheduleFired"
    - from: "internal/schedule.computeNextAndDetectMiss"
      to: "robfig/cron/v3 Schedule.Next()"
      via: "iterate sched.Next(candidate) until candidate.After(now); count missed windows"
      pattern: "sched.Next"
---

<objective>
Land the cron scheduler daemon: a tick-loop goroutine that periodically scans the `schedules` table for due rows, fires the most recent missed window per row (D-04 LatestOnly), and enqueues a `runs` row in the same transaction. The daemon shares the SKIP LOCKED multi-replica safety primitive with the run claim path (D-03) — no leader election needed.

This plan delivers everything *internal* to the scheduler package (tick driver, fire logic, missed-window detection, schedule registry sync) but does **not** wire the subcommand. The `./platform scheduler` CLI entry point is in plan 03-06.

This plan also delivers `TestPartitionUniqueConstraint` — the integration test proving that `runs.partition_key` partial unique index rejects duplicate in-flight partition runs but accepts re-enqueue after terminal state (Pitfall 7).

**Note on `Daemon.run` vs `schedule.FireOneSchedule` (W3 resolution):** Plan 03-06's scheduler subcommand drives its own tick loop and calls `schedule.FireOneSchedule(ctx, ...)` directly (so it can interleave sensor.Daemon.RunOnce after schedule firing per D-05). The package-internal `Daemon.run(ctx)` method is therefore UNEXPORTED — it is used only by tests in `internal/schedule/daemon_test.go` to verify the loop behavior in isolation. This avoids dead exported code in the production binary while keeping a self-contained test surface for the loop.

**Note on Task 1 file density (W2 justification):** Task 1 creates 7 files (daemon.go, daemon_test.go, fire.go, fire_test.go, missed.go, missed_test.go, registry.go) within one `<task>` element. The density is acceptable because the files are tightly cohesive single-package Go (~50–80 lines each, total ~400 lines): missed.go is a single function with a single test; registry.go is a single function with no test; fire.go and daemon.go reference each other and share the same `cronParser` package var. Splitting would require duplicating the file dependency graph across two tasks and would not reduce reviewer load. The plan still sits at 2 tasks (within the 2-3 budget) and leaves Task 2 (the partition unique constraint test) cleanly separated.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
This plan implements D-01 (scheduler subcommand pattern — daemon internal), D-02 (schedules table + lazy state), D-03 (robfig/cron/v3 parser-only + SKIP LOCKED multi-replica safety), D-04 (Missed-window LatestOnly), D-10 (partition_key column behavior — partial unique index test), D-12 (orthogonal Schedule×Partitions composition — schedule firing produces partition_key when asset has .Partitions).

**Why Wave 2:** Depends on plan 03-01 (schedules table must exist) and plan 03-02 (asset.Asset.Schedule() accessor + partition.CurrentDailyKey + asset cron parser exists). Cannot run before either. depends_on = [01, 02].

**Why parallel with 03-03 and 03-05 in Wave 2:** This plan touches `internal/schedule/*` and `internal/partition/partition_unique_test.go`. Plan 03-03 touches `internal/run/*`, `internal/runtime/*`, and `cmd/platform/{worker.go,materialize.go}`. Plan 03-05 touches `internal/sensor/*`. Zero file overlap on the production-code path.

**Note on `internal/partition/partition_unique_test.go`:** Plan 03-02 created the partition keygen tests; this plan adds an INTEGRATION test (`partition_unique_test.go` is a separate file in the same package, requiring DATABASE_URL). The package builds fine because both files declare `package partition`. The validation map specifies this test belongs in this plan because it directly exercises the partial unique index behavior that plan 03-01 created.

**Why FireOneSchedule per tx, not batch:** Per 03-RESEARCH.md Pattern 3 — "One transaction per schedule row (not a batch transaction) to minimize lock hold time." A long-running tx covering N schedules would block other replicas from claiming any. Per-row tx + SKIP LOCKED gives natural sharding across replicas with zero coordination.

**Why FireOneSchedule is exported (capital F) from this plan:** Plan 03-06's scheduler subcommand drives its own tick loop (interleaves schedule firing + sensor.Daemon.RunOnce + jitter). To drive the schedule pass, it calls `schedule.FireOneSchedule(ctx, store, registry, events, time.Now().UTC())` directly. So this plan exports the function from day one — there is no rename in plan 03-06.

**Why `Daemon.run` is unexported:** With FireOneSchedule already exported, the only consumer of a wrapping ticker that calls FireOneSchedule in a loop is the package-internal `Daemon.run` (used by `daemon_test.go` to test loop behavior in isolation). Production callers use FireOneSchedule + their own ticker. To prevent dead exported surface — and to make `daemon_test.go` the only place the loop lives — `Daemon.run` is lowercase.

**Why missed-window logic is "find the most recent window <= now":** Per Pattern 1 in research — `sched.Next(lastFiredAt)` returns the immediate next window after lastFiredAt. If multiple windows have elapsed (e.g., daemon was down for hours), iterating `sched.Next` starting from lastFiredAt produces a sequence; we walk it forward until the next candidate would exceed `now`, fire the last one that didn't, and emit `schedule.missed` with `skipped_count = total_iterations - 1`. D-04 LatestOnly means we DON'T enqueue all the windows — only the most recent. Avoids run-avalanche after multi-hour outage (Dagster default behavior).

**Why UpsertSchedules at daemon start (Open Question 3):** The schedules table is the persistent source of truth for `next_fire_at`. When a deployment changes a cron expression, the daemon must reconcile the registry against the table. UPSERT semantics: if a schedule row exists with a different `cron_expr`, update it and recompute `next_fire_at` from "now" (not from the old `last_fire_at` — a different cron expr means the previous schedule is invalid). New schedules INSERT with `next_fire_at = parsed.Next(time.Now())`. Removed assets (registry no longer has them) leave their schedule rows in place — that's harmless (the row will fire forever to a non-existent asset, generating queue rows that fail to claim because no asset definition exists; operator must clean up explicitly via REST or SQL — full pause/disable surface is Phase 6).

**Schedule×Partitions composition (D-12):** When a schedule fires for an asset with `.Partitions(daily)`, the inserted `runs.partition_key` is `partition.CurrentDailyKey(now, 24*time.Hour)` (yesterday's daily partition, matching Dagster's "cron fires for the preceding window"). For weekly: last week's ISO week. For monthly: last month. For category: schedule×category is uncommon but legal — fire one run per category at every cron tick, picking the first key. (Open Question 4 — schedule×category convention defaults to "first category in Keys list"; documented in fire.go comment.)

**Frozen interfaces consumed:**
- `internal/asset.DefinitionRegistry`, `Asset.Schedule()`, `Asset.Partitions()` (plan 03-02 frozen)
- `internal/partition.PartitionStrategy`, `partition.CurrentDailyKey`, `partition.WeeklyKey`, `partition.MonthlyKey` (plan 03-02 frozen)
- `internal/event.Writer.Append`, `EventTypeScheduleFired`, `EventTypeScheduleMissed` (plan 03-01 frozen)
- `internal/storage.Storage.DB()`, `Storage.Ent()` (Phase 1 frozen)
- `internal/run.PriorityOrder`, `PriorityNormal` constant (plan 03-03 — but this plan does NOT depend on plan 03-03 because we can write the literal string "normal" in the INSERT statement; no goroutine in schedule.fire.go calls ClaimNext)

**Why this plan does NOT depend on 03-03:** This plan only INSERTs into the runs table (priority='normal' literal). It does not invoke `run.ClaimNext` or read `runs.priority`. Plan 03-03 changes the claim path; this plan only fires NEW runs. depends_on = [01, 02] is correct.

@.planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md
@.planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md
@.planning/phases/03-scheduling-sensors-partitions/03-VALIDATION.md
@internal/asset/asset.go
@internal/asset/registry.go
@internal/event/types.go
@internal/run/claim.go

<interfaces>
<!-- Plan 03-01 + 03-02 surfaces this plan consumes. -->

From plan 03-01 (storage):
```sql
-- schedules table
id UUID, asset_name VARCHAR(256), cron_expr VARCHAR(128), last_fire_at TIMESTAMPTZ NULL,
next_fire_at TIMESTAMPTZ NULL, paused_at TIMESTAMPTZ NULL, created_at, updated_at
-- runs table extensions
partition_key VARCHAR(128) NULL, priority VARCHAR(16) NOT NULL DEFAULT 'normal',
backfill_id UUID NULL
-- partial unique index
run_partition_inflight_unique ON runs (asset_name, partition_key) WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL
```

From plan 03-01 (events):
```go
EventTypeScheduleFired   = "schedule.fired"
EventTypeScheduleMissed  = "schedule.missed"
```

From plan 03-02 (asset SDK):
```go
type ScheduleSpec struct { CronExpr string; TZ string }
func (a *Asset) Schedule() *ScheduleSpec
func (a *Asset) Partitions() partition.PartitionStrategy
type DefinitionRegistry  // existing — has All() []*Asset
```

From plan 03-02 (partition keygen):
```go
func DailyKey(t time.Time) string
func WeeklyKey(t time.Time) string
func MonthlyKey(t time.Time) string
func CurrentDailyKey(now time.Time, offset time.Duration) string
```

From robfig/cron/v3 (verified pkg.go.dev):
```go
import "github.com/robfig/cron/v3"
parser := cron.NewParser(cron.Minute|cron.Hour|cron.Dom|cron.Month|cron.Dow|cron.Descriptor)
sched, err := parser.Parse(expr)  // returns cron.Schedule
nextFire := sched.Next(t)         // returns time.Time strictly > t
```

Phase 3 surface this plan produces:
```go
package schedule

type Daemon struct {
    Store    storage.Storage
    Registry *asset.DefinitionRegistry
    Events   event.Writer
    Interval time.Duration  // default 30s
    // internal: jitter source
}
// run is the unexported tick driver — used by package-internal tests only.
// Production code (cmd/platform/scheduler.go) drives its own tick loop and calls
// schedule.FireOneSchedule directly (interleaved with sensor evaluation per D-05).
func (d *Daemon) run(ctx context.Context) error

// EXPORTED — production caller (cmd/platform/scheduler.go) uses this directly.
var ErrNoDueSchedule = errors.New("schedule: no due schedule")
func FireOneSchedule(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry, events event.Writer, now time.Time) error

func UpsertSchedules(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry) error

// internal/schedule/missed.go
func computeNextAndDetectMiss(sched cron.Schedule, lastFiredAt, now time.Time) (next time.Time, missed int)
```
</interfaces>
</context>

<tasks>

<task id="3.4.1" type="auto" tdd="true">
  <name>Task 1: Create internal/schedule package — Daemon (unexported `run` driver) + exported FireOneSchedule + missed-window LatestOnly + UpsertSchedules registry sync</name>
  <files>internal/schedule/daemon.go, internal/schedule/daemon_test.go, internal/schedule/fire.go, internal/schedule/fire_test.go, internal/schedule/missed.go, internal/schedule/missed_test.go, internal/schedule/registry.go</files>
  <read_first>
    - internal/asset/asset.go (Asset.Schedule() + Partitions() + Sensors() accessors)
    - internal/asset/registry.go (DefinitionRegistry surface — All() method)
    - internal/event/types.go (EventTypeScheduleFired/Missed constants from plan 03-01)
    - internal/run/claim.go (transaction pattern for runs INSERT)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 1 (cron parser usage), § Pattern 2 (schedules table), § Pattern 3 (tick loop + FireOneSchedule SQL)
    - .planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md § D-02, D-03, D-04, D-12
  </read_first>
  <behavior>
    - Daemon's unexported `run(ctx)` (used by tests only) runs one tick immediately, then ticks every Interval (default 30s) until ctx canceled
    - On each tick, the loop iterates due schedules calling FireOneSchedule per row, until SELECT returns no rows (ErrNoDueSchedule)
    - FireOneSchedule (EXPORTED) transactionally: SELECT FOR UPDATE SKIP LOCKED LIMIT 1 due row → compute next_fire_at via cron parser → INSERT runs row (with partition_key derived from asset.Partitions()) → UPDATE schedules SET last_fire_at, next_fire_at → commit
    - After commit, append schedule.fired event (best-effort outside tx). If missed_count > 0 from prior gap, also append schedule.missed event with `skipped_count` payload (D-04)
    - If asset has Partitions, partition_key = current key for that strategy; if no Partitions, partition_key = NULL
    - UpsertSchedules iterates registry.All(), for each asset with Schedule() != nil: INSERT ... ON CONFLICT (asset_name) DO UPDATE SET cron_expr, next_fire_at, updated_at — only when cron_expr actually changed (avoid unnecessary updates)
    - missed.go: computeNextAndDetectMiss(sched, lastFiredAt, now) — if lastFiredAt zero, treat as never fired (set to time.Unix(0,0)). Iterate sched.Next forward starting from lastFiredAt until next candidate > now. Return (most-recent-candidate-<=-now, count-of-iterations-skipped).
    - Tests use a fake event.Writer (capture appended events in slice) and a real DB via DATABASE_URL env var (mirror claim_test.go pattern).
  </behavior>
  <action>
    1. Create `internal/schedule/missed.go`:
       ```go
       // Package schedule implements the cron scheduler daemon (D-01..D-04).
       package schedule

       import (
           "time"
           "github.com/robfig/cron/v3"
       )

       // cronParser is the package-level parser. Parser-only usage (D-03) — Cron runner is NEVER instantiated.
       var cronParser = cron.NewParser(cron.Minute|cron.Hour|cron.Dom|cron.Month|cron.Dow|cron.Descriptor)

       // computeNextAndDetectMiss walks forward from lastFiredAt to find the most recent
       // window <= now (D-04 LatestOnly). Returns (windowToFire, missedCount).
       // missedCount = number of windows skipped (count > 0 ⇒ daemon missed firings during downtime).
       //
       // If lastFiredAt is zero, treat as never fired (epoch start). The first call after
       // schedule registration (where last_fire_at is NULL in DB) hits this path and fires
       // the most recent window without flagging "missed" (skip count is the number of
       // theoretical windows since epoch, which is enormous and noisy — return 0 instead).
       func computeNextAndDetectMiss(sched cron.Schedule, lastFiredAt, now time.Time) (time.Time, int) {
           lastFiredAt = lastFiredAt.UTC()
           now = now.UTC()
           // Treat zero / pre-epoch as "never fired" — fire the next window without missed accounting.
           if lastFiredAt.IsZero() || lastFiredAt.Before(time.Unix(0, 0)) {
               return sched.Next(now.Add(-time.Second)), 0  // most-recent window <= now
           }
           candidate := sched.Next(lastFiredAt)
           if candidate.After(now) {
               return candidate, 0  // not yet due — return the next future window
           }
           missed := 0
           for {
               nextCandidate := sched.Next(candidate)
               if nextCandidate.After(now) {
                   return candidate, missed
               }
               missed++
               candidate = nextCandidate
           }
       }
       ```
       Add `internal/schedule/missed_test.go` with `TestMissedWindowLatestOnly` (validation map: same name).
       Tests:
       - Schedule "0 * * * *" (top of every hour). lastFiredAt = 2026-01-01 00:00:00 UTC. now = 2026-01-01 03:30:00 UTC. Expected: (window=2026-01-01 03:00:00, missed=2 — windows 01:00, 02:00 were skipped, 03:00 is the most recent).
       - lastFiredAt = zero time → returns (next window before now, 0).
       - lastFiredAt = now - 30s on 1-hour schedule → returns (next future window, 0). i.e., not due.
       - lastFiredAt = now (just fired) → not yet due, returns (sched.Next(now), 0).
    2. Create `internal/schedule/registry.go`:
       ```go
       package schedule

       import (
           "context"
           "fmt"
           "time"

           "github.com/kanpon/data-governance/internal/asset"
           "github.com/kanpon/data-governance/internal/storage"
       )

       // UpsertSchedules reconciles asset.DefinitionRegistry.All() with the schedules table.
       // For each asset whose Schedule() is non-nil: INSERT a row, or UPDATE if cron_expr changed.
       // Idempotent across daemon restarts. Called once at daemon start (Open Question 3).
       func UpsertSchedules(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry) error {
           db := store.DB()
           for _, a := range reg.All() {
               sp := a.Schedule()
               if sp == nil {
                   continue
               }
               // Validate cron expression — fail loudly if registry held a bad value.
               sched, err := cronParser.Parse(sp.CronExpr)
               if err != nil {
                   return fmt.Errorf("schedule: registry asset %q has invalid cron %q: %w", a.Name(), sp.CronExpr, err)
               }
               nextFire := sched.Next(time.Now().UTC())
               // SELECT-then-INSERT/UPDATE pattern (no ON CONFLICT) since schedules.asset_name does not have a
               // unique constraint (only an index). Avoids revising plan 03-01.
               const selectSQL = `SELECT id, cron_expr FROM schedules WHERE asset_name = $1 LIMIT 1`
               var existingID, existingCron string
               err = db.QueryRowContext(ctx, selectSQL, a.Name()).Scan(&existingID, &existingCron)
               if err == nil {
                   if existingCron == sp.CronExpr {
                       continue // unchanged — no UPDATE needed
                   }
                   const updateSQL = `UPDATE schedules SET cron_expr = $1, next_fire_at = $2, updated_at = NOW() WHERE id = $3::uuid`
                   if _, err := db.ExecContext(ctx, updateSQL, sp.CronExpr, nextFire, existingID); err != nil {
                       return fmt.Errorf("schedule: update %q: %w", a.Name(), err)
                   }
                   continue
               }
               // Not found — INSERT.
               const insertSQL = `
                   INSERT INTO schedules (id, asset_name, cron_expr, next_fire_at, created_at, updated_at)
                   VALUES (gen_random_uuid(), $1, $2, $3, NOW(), NOW())
               `
               if _, err := db.ExecContext(ctx, insertSQL, a.Name(), sp.CronExpr, nextFire); err != nil {
                   return fmt.Errorf("schedule: insert %q: %w", a.Name(), err)
               }
           }
           return nil
       }
       ```
    3. Create `internal/schedule/fire.go`:
       ```go
       package schedule

       import (
           "context"
           "database/sql"
           "errors"
           "fmt"
           "time"

           "github.com/google/uuid"
           "github.com/kanpon/data-governance/internal/asset"
           "github.com/kanpon/data-governance/internal/event"
           "github.com/kanpon/data-governance/internal/partition"
           "github.com/kanpon/data-governance/internal/storage"
       )

       // ErrNoDueSchedule is returned by FireOneSchedule when no due row exists.
       var ErrNoDueSchedule = errors.New("schedule: no due schedule")

       // FireOneSchedule transactionally claims the next due schedule row, enqueues a run,
       // updates last_fire_at/next_fire_at, and commits. After commit, emits schedule.fired
       // and (if missedCount > 0) schedule.missed events.
       //
       // Returns ErrNoDueSchedule when no rows are due.
       //
       // EXPORTED so plan 03-06's scheduler subcommand can drive its own tick loop with
       // interleaved sensor evaluation (D-05 single-loop architecture).
       //
       // The asset.DefinitionRegistry is consulted to determine partition strategy at fire time.
       func FireOneSchedule(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry, events event.Writer, now time.Time) error {
           db := store.DB()
           tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
           if err != nil {
               return fmt.Errorf("schedule.fire: begin tx: %w", err)
           }
           defer func() { _ = tx.Rollback() }()

           const selectSQL = `
               SELECT id, asset_name, cron_expr, last_fire_at
               FROM schedules
               WHERE next_fire_at <= $1
                 AND paused_at IS NULL
               ORDER BY next_fire_at
               FOR UPDATE SKIP LOCKED
               LIMIT 1
           `
           var (
               schedID    uuid.UUID
               assetName  string
               cronExpr   string
               lastFireAt sql.NullTime
           )
           if err := tx.QueryRowContext(ctx, selectSQL, now).Scan(&schedID, &assetName, &cronExpr, &lastFireAt); err != nil {
               if errors.Is(err, sql.ErrNoRows) {
                   return ErrNoDueSchedule
               }
               return fmt.Errorf("schedule.fire: select due: %w", err)
           }
           sched, err := cronParser.Parse(cronExpr)
           if err != nil {
               return fmt.Errorf("schedule.fire: parse cron %q for asset %q: %w", cronExpr, assetName, err)
           }
           lf := time.Time{}
           if lastFireAt.Valid {
               lf = lastFireAt.Time
           }
           windowToFire, missedCount := computeNextAndDetectMiss(sched, lf, now)
           nextFire := sched.Next(now)

           // Determine partition_key from asset registry (D-12 Schedule×Partitions composition).
           partitionKey := computeFirePartitionKey(reg, assetName, windowToFire)

           runID := uuid.New()
           const insertRunSQL = `
               INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority, partition_key)
               VALUES ($1, $2, 'queued', 'schedule', NOW(), 'normal', $3)
           `
           var pkArg interface{} = nil
           if partitionKey != "" {
               pkArg = partitionKey
           }
           if _, err := tx.ExecContext(ctx, insertRunSQL, runID, assetName, pkArg); err != nil {
               // Partial unique index may reject if a prior run for same partition is still in-flight.
               // Treat as "skip this fire" — emit nothing (or emit a skip event); next tick re-evaluates.
               return fmt.Errorf("schedule.fire: insert run: %w", err)
           }

           const updateSchedSQL = `
               UPDATE schedules
                  SET last_fire_at = NOW(),
                      next_fire_at = $1,
                      updated_at   = NOW()
                WHERE id = $2
           `
           if _, err := tx.ExecContext(ctx, updateSchedSQL, nextFire, schedID); err != nil {
               return fmt.Errorf("schedule.fire: update schedule: %w", err)
           }
           if err := tx.Commit(); err != nil {
               return fmt.Errorf("schedule.fire: commit: %w", err)
           }

           // Best-effort event emission outside tx.
           _ = events.Append(ctx, event.Event{
               Type: event.EventTypeScheduleFired,
               OccurredAt: time.Now().UTC(),
               ResourceType: "schedule",
               ResourceID:   schedID.String(),
               Payload: map[string]any{
                   "asset_name":     assetName,
                   "run_id":         runID.String(),
                   "window_fired":   windowToFire,
                   "partition_key":  partitionKey,
               },
           })
           if missedCount > 0 {
               _ = events.Append(ctx, event.Event{
                   Type: event.EventTypeScheduleMissed,
                   OccurredAt: time.Now().UTC(),
                   ResourceType: "schedule",
                   ResourceID:   schedID.String(),
                   Payload: map[string]any{
                       "asset_name":    assetName,
                       "skipped_count": missedCount,
                   },
               })
           }
           return nil
       }

       // computeFirePartitionKey returns the partition_key for a scheduled fire, given
       // the asset's PartitionStrategy. Returns "" for non-partitioned assets.
       // For schedule×category composition: pick the first key (D-12 + Open Question 4 default).
       func computeFirePartitionKey(reg *asset.DefinitionRegistry, assetName string, windowFiredAt time.Time) string {
           a, err := reg.Get(assetName)
           if err != nil || a == nil {
               return ""
           }
           strat := a.Partitions()
           if strat == nil {
               return ""
           }
           switch s := strat.(type) {
           case partition.DailyPartitions:
               // Cron fires for the preceding window — yesterday for daily.
               return partition.CurrentDailyKey(windowFiredAt, 24*time.Hour)
           case partition.WeeklyPartitions:
               return partition.WeeklyKey(windowFiredAt.Add(-7 * 24 * time.Hour))
           case partition.MonthlyPartitions:
               return partition.MonthlyKey(windowFiredAt.AddDate(0, -1, 0))
           case partition.CategoryPartitions:
               if len(s.Keys) == 0 {
                   return ""
               }
               return s.Keys[0]
           default:
               return ""
           }
       }
       ```
       (Notes: `reg.Get(assetName)` is the existing Phase 2 method on `asset.DefinitionRegistry` — confirm via `internal/asset/registry.go`.)
    4. Create `internal/schedule/daemon.go` with the tick driver as an UNEXPORTED method `run`:
       ```go
       package schedule

       import (
           "context"
           "errors"
           "log/slog"
           "math/rand/v2"
           "time"

           "github.com/kanpon/data-governance/internal/asset"
           "github.com/kanpon/data-governance/internal/event"
           "github.com/kanpon/data-governance/internal/storage"
       )

       const DefaultInterval = 30 * time.Second

       type Daemon struct {
           Store    storage.Storage
           Registry *asset.DefinitionRegistry
           Events   event.Writer
           Interval time.Duration  // default DefaultInterval
       }

       // run executes the tick loop until ctx is canceled. UNEXPORTED — production code
       // (cmd/platform/scheduler.go) drives its own tick loop and calls FireOneSchedule
       // directly so it can interleave sensor evaluation per D-05. This method exists
       // for package-internal tests in daemon_test.go that exercise the loop in isolation.
       //
       // Calls UpsertSchedules at start (Open Question 3 default). Each tick fires due
       // schedules until SELECT returns no rows. Adds 0..5s jitter to the tick interval
       // to avoid thundering-herd across replicas.
       func (d *Daemon) run(ctx context.Context) error {
           if d.Interval == 0 {
               d.Interval = DefaultInterval
           }
           if err := UpsertSchedules(ctx, d.Store, d.Registry); err != nil {
               slog.Error("schedule.upsert_failed", "error", err)
               // Continue — daemon can still fire whatever rows already exist.
           }
           // Run one tick immediately on start to handle any missed windows.
           d.tick(ctx)
           for {
               jitter := time.Duration(rand.Int64N(5000)) * time.Millisecond
               select {
               case <-time.After(d.Interval + jitter):
                   d.tick(ctx)
               case <-ctx.Done():
                   slog.Info("schedule.shutdown")
                   return ctx.Err()
               }
           }
       }

       // tick fires due schedules until no more are due (or ctx canceled). Unexported —
       // called only by `run` and by package-internal tests.
       func (d *Daemon) tick(ctx context.Context) {
           now := time.Now().UTC()
           for {
               if ctx.Err() != nil {
                   return
               }
               err := FireOneSchedule(ctx, d.Store, d.Registry, d.Events, now)
               if errors.Is(err, ErrNoDueSchedule) {
                   return
               }
               if err != nil {
                   slog.Error("schedule.fire_failed", "error", err)
                   return  // back off; next tick retries
               }
           }
       }
       ```
    5. Create `internal/schedule/fire_test.go`:
       - `TestSchedulerFiresDueRow` (validation map: TestScheduler) — integration test.
         Set up: insert a schedules row with `next_fire_at = NOW() - 1 minute`, register a matching asset (no partitions). Build a Daemon with mock event writer, run one `FireOneSchedule(ctx, store, reg, events, time.Now())`. Assert: a runs row exists with state='queued', trigger='schedule', priority='normal', partition_key IS NULL; the schedules row has last_fire_at != NULL and next_fire_at > NOW(); event writer captured one schedule.fired event.
       - `TestSchedulerFireWithDailyPartition` — asset has `.Partitions(DailyPartitions{})`. After fire, runs.partition_key matches `partition.CurrentDailyKey(now, 24h)`.
       - `TestSchedulerFireMissedWindow` — schedule with cron "0 * * * *" (hourly). Set last_fire_at = NOW() - 4 hours. Fire. Assert: only ONE runs row inserted (LatestOnly, D-04); event writer captured BOTH schedule.fired and schedule.missed; the schedule.missed payload `skipped_count` is 3 (4 hours elapsed, 3 windows skipped, the most recent fired).
       - `TestSchedulerNoDueRows` — no schedules table rows OR all are paused; `FireOneSchedule` returns `ErrNoDueSchedule`.
       - `TestSchedulerSkipLocked` — insert one due schedule, run two `FireOneSchedule` calls in parallel goroutines on separate transactions; assert exactly one fire, one ErrNoDueSchedule (SKIP LOCKED contract).
    6. Create `internal/schedule/daemon_test.go` (light, since the unexported `run` method is mostly orchestration):
       - `TestDaemonRunCancellation` — start a Daemon with Interval=10ms; call `(&Daemon{...}).run(ctx)` from within the same package (unexported access), cancel ctx after 50ms, assert run returns ctx.Canceled within 100ms. The test is in the same package (`internal/schedule`) so it can call the unexported method.
       - `TestDaemonUpsertOnStart` — pre-register an asset with .Schedule("@every 1m") in a registry, call `daemon.run(ctx)` for 100ms then cancel; assert a schedules row was inserted for that asset (UpsertSchedules ran). Same-package access required.
    7. Helper test fixtures: a `fakeEventWriter` in `internal/schedule/fire_test.go` that captures events into a slice with a Mutex; an `openTestDB(t)` helper mirroring the claim_test.go pattern.
  </action>
  <acceptance_criteria>
    - `grep -q 'package schedule' internal/schedule/daemon.go`
    - `grep -q 'type Daemon struct' internal/schedule/daemon.go`
    - `grep -q 'func (d \\*Daemon) run(ctx context.Context) error' internal/schedule/daemon.go` (UNEXPORTED `run` method — lowercase r)
    - `! grep -q 'func (d \\*Daemon) Run(' internal/schedule/daemon.go` (no exported `Run` method exists — production callers use FireOneSchedule directly)
    - `grep -q 'func computeNextAndDetectMiss' internal/schedule/missed.go`
    - `grep -q 'func FireOneSchedule' internal/schedule/fire.go` (EXPORTED — capital F)
    - `grep -q 'FOR UPDATE SKIP LOCKED' internal/schedule/fire.go`
    - `grep -q 'WHERE next_fire_at <= \\$1' internal/schedule/fire.go`
    - `grep -q 'WHERE next_fire_at <= \\$1' internal/schedule/fire.go && grep -q 'paused_at IS NULL' internal/schedule/fire.go`
    - `grep -q "INSERT INTO runs.*priority.*partition_key" internal/schedule/fire.go`
    - `grep -q 'EventTypeScheduleFired' internal/schedule/fire.go`
    - `grep -q 'EventTypeScheduleMissed' internal/schedule/fire.go`
    - `grep -q 'func UpsertSchedules' internal/schedule/registry.go`
    - `grep -q 'cronParser = cron.NewParser' internal/schedule/missed.go`
    - `grep -q 'func TestMissedWindowLatestOnly' internal/schedule/missed_test.go`
    - `grep -q 'func TestSchedulerFiresDueRow\\|func TestScheduler' internal/schedule/fire_test.go`
    - `grep -q 'daemon.run(ctx)\\|d.run(ctx)' internal/schedule/daemon_test.go` (test calls unexported `run` method — same-package access)
    - `go test ./internal/schedule/... -run TestMissedWindowLatestOnly -count=1 -timeout 30s` exits 0
    - `DATABASE_URL=... go test ./internal/schedule/... -run TestScheduler -count=1 -timeout 60s` exits 0
    - `go build ./...` passes
  </acceptance_criteria>
  <verify>
    <automated>DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./internal/schedule/... -count=1 -timeout 120s</automated>
  </verify>
  <done>internal/schedule package created with Daemon (unexported `run` driver) + EXPORTED FireOneSchedule + missed + registry; LatestOnly missed-window logic verified; FireOneSchedule produces correct partition_key for partitioned assets; SKIP LOCKED multi-replica safety verified; event writer receives schedule.fired and (when applicable) schedule.missed events; UpsertSchedules idempotent; no dead exported `Daemon.Run` since production code calls FireOneSchedule directly (W3 fix).</done>
</task>

<task id="3.4.2" type="auto" tdd="true">
  <name>Task 2: Add TestPartitionUniqueConstraint integration test in internal/partition</name>
  <files>internal/partition/partition_unique_test.go</files>
  <read_first>
    - migrations/20260508120000_phase3_runs_columns.sql (verify the WHERE predicate of run_partition_inflight_unique)
    - internal/run/claim_test.go (helper patterns: openTestDB, deleteRuns, sqlStorage stub)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pitfall 7 — Partial Unique Index Missing
  </read_first>
  <behavior>
    - Two concurrent INSERTs into runs with state='queued' + same (asset_name, partition_key) — second INSERT fails with unique-constraint error
    - INSERT with state='succeeded' and partition_key='X', then INSERT with state='queued' and partition_key='X' — both succeed (terminal state does not block re-enqueue)
    - INSERT with state='queued' and partition_key=NULL, then second INSERT with same asset_name and partition_key=NULL — both succeed (NULL is not unique)
  </behavior>
  <action>
    1. Create `internal/partition/partition_unique_test.go`:
       ```go
       package partition_test

       import (
           "context"
           "database/sql"
           "os"
           "testing"
           "time"

           _ "github.com/jackc/pgx/v5/stdlib"
           "github.com/stretchr/testify/assert"
           "github.com/stretchr/testify/require"
       )

       func openTestDB(t *testing.T) *sql.DB {
           t.Helper()
           dsn := os.Getenv("DATABASE_URL")
           if dsn == "" {
               t.Skip("requires DATABASE_URL")
           }
           db, err := sql.Open("pgx", dsn)
           require.NoError(t, err)
           ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
           defer cancel()
           require.NoError(t, db.PingContext(ctx))
           return db
       }

       // TestPartitionUniqueConstraint exercises the partial unique index on
       // (asset_name, partition_key) WHERE state IN ('queued','starting','running')
       // AND partition_key IS NOT NULL (D-10 + Pitfall 7).
       func TestPartitionUniqueConstraint(t *testing.T) {
           db := openTestDB(t)
           defer db.Close()
           ctx := context.Background()
           const asset = "test-partition-unique"
           defer db.ExecContext(ctx, "DELETE FROM runs WHERE asset_name=$1", asset)

           insert := func(state, partitionKey string) error {
               var pk interface{} = partitionKey
               if partitionKey == "" { pk = nil }
               _, err := db.ExecContext(ctx,
                   `INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority, partition_key)
                    VALUES (gen_random_uuid(), $1, $2, 'manual', NOW(), 'normal', $3)`,
                   asset, state, pk)
               return err
           }

           // 1. Two queued rows with same (asset, partition_key) — second must fail.
           require.NoError(t, insert("queued", "2024-01-01"))
           err := insert("queued", "2024-01-01")
           assert.Error(t, err, "second queued INSERT for same partition must fail unique constraint")

           // 2. Mark first run succeeded — re-INSERT should now succeed (terminal state ignored by partial index).
           _, err = db.ExecContext(ctx, "UPDATE runs SET state='succeeded' WHERE asset_name=$1 AND partition_key=$2", asset, "2024-01-01")
           require.NoError(t, err)
           assert.NoError(t, insert("queued", "2024-01-01"), "INSERT for re-run after terminal state must succeed")

           // 3. Two queued rows with partition_key=NULL — both must succeed (NULL is not unique).
           require.NoError(t, insert("queued", ""))
           assert.NoError(t, insert("queued", ""))

           // 4. queued + running for same partition — running must fail unique constraint
           //    (both states are in-flight, partial index covers both).
           require.NoError(t, insert("queued", "2024-02-01"))
           err = insert("running", "2024-02-01")
           assert.Error(t, err, "INSERT 'running' alongside 'queued' for same partition must fail unique constraint")
       }
       ```
    2. Run the test against the local DB:
       ```bash
       DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable \
         go test ./internal/partition/... -run TestPartitionUniqueConstraint -count=1 -timeout 60s
       ```
       Must exit 0.
  </action>
  <acceptance_criteria>
    - File `internal/partition/partition_unique_test.go` exists
    - `grep -q 'func TestPartitionUniqueConstraint' internal/partition/partition_unique_test.go`
    - `grep -q 'run_partition_inflight_unique\\|state IN' internal/partition/partition_unique_test.go` (test references the constraint behavior)
    - `DATABASE_URL=... go test ./internal/partition/... -run TestPartitionUniqueConstraint -count=1 -timeout 60s` exits 0
  </acceptance_criteria>
  <verify>
    <automated>DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./internal/partition/... -run TestPartitionUniqueConstraint -count=1 -timeout 60s</automated>
  </verify>
  <done>TestPartitionUniqueConstraint passes against the local DB, proving D-10 partial unique index correctly rejects duplicate in-flight partition runs while allowing re-enqueue after terminal state and accepting NULL partition_keys.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| robfig/cron/v3 parser → schedules table | User-supplied cron expressions stored in DB; parser is the validation gate (already enforced at builder time in plan 03-02; re-validated at fire time as defense-in-depth) |
| Multiple Daemon replicas → schedules table | SELECT FOR UPDATE SKIP LOCKED is the multi-replica coordination primitive |
| Daemon → runs table | Daemon INSERTs runs rows; partition_key is parametrized (no SQL injection surface) |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-03-04-01 | Denial of Service | Malformed cron expression in registered schedule causes daemon crash | mitigate | Builder validates at Build()/Register() (plan 03-02). Daemon defense-in-depth: FireOneSchedule re-parses and returns error per-row instead of crashing the loop. UpsertSchedules also validates and returns error before inserting. |
| T-03-04-02 | Denial of Service | Fire-loop pegs the DB by re-firing the same row infinitely if UPDATE schedules fails | mitigate | FireOneSchedule does both INSERT runs + UPDATE schedules in the SAME transaction. If UPDATE fails, tx rolls back and INSERT runs is also rolled back — the row stays "due" but the overall fire is atomic. SKIP LOCKED + per-row tx prevents starvation across replicas. |
| T-03-04-03 | Tampering | Daemon inserts a runs row for a partition that already has an in-flight run (race) | mitigate | Partial unique index on (asset_name, partition_key) WHERE state IN ('queued','starting','running') rejects the second INSERT atomically. FireOneSchedule treats unique-constraint-violation as "skip this fire" (returns error from tx, logged as schedule.fire_failed; next tick re-evaluates). TestPartitionUniqueConstraint validates the constraint behavior. |
| T-03-04-04 | Information Disclosure | schedule.fired event payload contains asset_name and partition_key | accept | Both are non-sensitive metadata. event_log RLS (Phase 1 D-09) prevents tamper after write. |
| T-03-04-05 | Spoofing | One Daemon replica claims a schedule another replica is processing | mitigate | SELECT FOR UPDATE SKIP LOCKED guarantees only one replica holds the row lock at any instant; the other sees ErrNoRows / waits its tick. Same primitive as Phase 2 ClaimNext, already proven by 50-goroutine atomicity test. |
| T-03-04-06 | Denial of Service | Long sched.Next() iteration in computeNextAndDetectMiss after multi-year outage | mitigate | The iteration is bounded by elapsed time / cron period. Worst case: 10 years × 365 days × 24 hours = 87,600 iterations for hourly cron — completes in tens of milliseconds. No bound needed in practice; if a pathological case emerges, add a hard cap (e.g., 10000 iterations → log warning + force-fire most-recent). Documented in missed.go comment. |
| T-03-04-07 | Tampering | event_log Phase 3 events tampered | accept | Phase 1 D-09 RLS already prevents UPDATE/DELETE on event_log [VERIFIED]. |
</threat_model>

<verification>
- `go build ./...` passes.
- `go test ./internal/schedule/... -count=1 -timeout 120s` passes (integration tests requiring DATABASE_URL).
- `go test ./internal/partition/... -run TestPartitionUniqueConstraint -count=1 -timeout 60s` passes.
- `go test ./internal/run/... -run TestClaimAtomicity50Goroutines -count=1 -timeout 60s` still passes (regression — the schedules table additions do not affect run claim).
- The SKIP LOCKED test (TestSchedulerSkipLocked) confirms two parallel `FireOneSchedule` calls produce one fire and one ErrNoDueSchedule.
- No dead exported `Daemon.Run` surface — only `FireOneSchedule` is consumed by plan 03-06.
</verification>

<success_criteria>
- internal/schedule package exists with Daemon (unexported `run` tick driver), EXPORTED FireOneSchedule, missed, registry components.
- Daemon.run executes one tick immediately on start, then ticks every Interval (default 30s) with 0..5s jitter — used by package-internal tests only.
- FireOneSchedule (exported) transactionally inserts runs row + updates schedules row + emits schedule.fired event; emits schedule.missed when missedCount > 0 (D-04).
- UpsertSchedules idempotently syncs registry → schedules table at daemon start.
- TestMissedWindowLatestOnly proves LatestOnly recovery with skipped_count semantics.
- TestSchedulerFiresDueRow integration proves end-to-end firing.
- TestPartitionUniqueConstraint proves D-10 partial unique index behavior.
- No leader election, no advisory locks for scheduler — SKIP LOCKED is the only coordination primitive (D-03).
- W3 resolution: `Daemon.run` is unexported; plan 03-06's scheduler subcommand calls `FireOneSchedule` directly (no dead exported Run method).
</success_criteria>

<output>
After completion, create `.planning/phases/03-scheduling-sensors-partitions/03-04-SUMMARY.md` documenting:
- Final scheduler package surface — emphasize that `Daemon.run` is UNEXPORTED (test-only) and `FireOneSchedule` is EXPORTED (production caller).
- Tick interval default + jitter range.
- Missed-window LatestOnly behavior — confirm by quoting the schedule.missed payload shape.
- Decision-coverage: D-01 (subcommand internal), D-02 (lazy schedules table), D-03 (parser-only + SKIP LOCKED), D-04 (LatestOnly), D-10 (partition_key + partial unique), D-12 (Schedule×Partitions composition).
- Note: scheduler subcommand wiring (`./platform scheduler` entry point) belongs to plan 03-06 and consumes `FireOneSchedule` directly (no `Daemon.Run` dependency).
</output>
