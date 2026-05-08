---
phase: 03-scheduling-sensors-partitions
plan: 04
subsystem: scheduling
tags: [scheduler, cron, robfig, postgres, skip-locked, missed-window, partitions, partial-unique]

# Dependency graph
requires:
  - phase: 03-scheduling-sensors-partitions/01-schema-events-foundation
    provides: schedules table (cron_expr / last_fire_at / next_fire_at / paused_at), runs.partition_key + runs.priority columns, run_partition_inflight_unique partial UNIQUE index, EventTypeScheduleFired / EventTypeScheduleMissed
  - phase: 03-scheduling-sensors-partitions/02-asset-dsl-and-partitions
    provides: asset.Asset.Schedule() / Partitions() accessors, partition.{Daily,Weekly,Monthly,Category}Partitions strategies, partition.{DailyKey,WeeklyKey,MonthlyKey,CurrentDailyKey} keygen
  - phase: 02-execution-engine
    provides: storage.Storage interface, internal/run claim transaction pattern, event.Writer / event.Event types
provides:
  - "internal/schedule package — Daemon (unexported run/tick) + EXPORTED FireOneSchedule + UpsertSchedules + computeNextAndDetectMiss"
  - "ErrNoDueSchedule sentinel for callers to detect benign empty-tick"
  - "DefaultInterval = 30s; jitter 0..5s on top of Interval (D-03 thundering-herd mitigation)"
  - "D-04 LatestOnly missed-window semantics — schedule.missed event with skipped_count payload"
  - "D-12 Schedule × Partitions composition — daily/weekly/monthly preceding-window partition_key, category first-key default"
  - "TestPartitionUniqueConstraint integration evidence for D-10 partial unique behavior"
affects: [03-06-scheduler-subcommand]

# Tech tracking
tech-stack:
  added: []  # robfig/cron/v3 already added by plan 03-02
  patterns:
    - "Per-row transaction (NOT batch) holding FOR UPDATE SKIP LOCKED — minimizes lock hold time, gives natural cross-replica sharding (Pattern 3)"
    - "Best-effort event emission outside the fire transaction — runs row commit must NOT be lost on event-writer failure"
    - "Defense-in-depth re-parse of cron expression at fire time (T-03-04-01) — daemon never crashes on a bad row"
    - "Bounded forward iteration in computeNextAndDetectMiss — worst-case ~87,600 iterations on hourly cron after 10-year outage (T-03-04-06)"
    - "TDD RED→GREEN per task: 5 commits across 2 tasks (test → impl → test+impl → impl → test)"

key-files:
  created:
    - "internal/schedule/missed.go (computeNextAndDetectMiss + cronParser package var + package doc)"
    - "internal/schedule/missed_test.go (TestMissedWindowLatestOnly — 4 cases)"
    - "internal/schedule/fire.go (FireOneSchedule + ErrNoDueSchedule + computeFirePartitionKey)"
    - "internal/schedule/fire_test.go (5 integration tests + fakeEventWriter + sqlOnlyStorage helpers)"
    - "internal/schedule/daemon.go (Daemon struct + unexported run/tick methods + DefaultInterval + jitterMaxMs)"
    - "internal/schedule/daemon_test.go (TestDaemonRunCancellation + TestDaemonUpsertOnStart)"
    - "internal/schedule/registry.go (UpsertSchedules — SELECT-then-INSERT/UPDATE idempotent sync)"
    - "internal/partition/partition_unique_test.go (TestPartitionUniqueConstraint — 4 behaviors)"
  modified: []  # purely additive plan; zero modifications to existing source files

key-decisions:
  - "Daemon.run + Daemon.tick are UNEXPORTED — production code (plan 03-06) uses FireOneSchedule directly so it can interleave sensor.Daemon.RunOnce per D-05 single-loop architecture (W3 resolution)"
  - "FireOneSchedule is EXPORTED from day one — eliminates a rename in plan 03-06"
  - "UpsertSchedules uses SELECT-then-INSERT/UPDATE (not ON CONFLICT) — schedules.asset_name is a non-unique index in plan 03-01; ON CONFLICT (asset_name) would require a schema change"
  - "computeNextAndDetectMiss returns missed=0 for zero lastFiredAt — first-registration of a hourly schedule must NOT produce a noisy 'thousands of windows skipped since epoch' event"
  - "computeNextAndDetectMiss seeds the zero-lastFiredAt walk from now-1y — covers up to yearly cron periods without unbounded iteration"
  - "nextFire = sched.Next(now) (not sched.Next(windowToFire)) so the next tick lands on the upcoming window even after a multi-hour outage — avoids re-firing past windows on subsequent ticks"
  - "Event emission swallows errors after the fire-tx commits — the runs row is the source of truth; observability for emit failures is the writer's concern (Phase 1 D-09)"
  - "Schedule × Partitions composition follows Dagster's preceding-window convention — daily cron at midnight enqueues yesterday's partition (Open Question 1 default)"
  - "Schedule × Category composition picks the first key in CategoryPartitions.Keys (Open Question 4 default — uncommon configuration documented in computeFirePartitionKey)"

patterns-established:
  - "internal/schedule package layout: missed.go (parser + compute helper) → registry.go (upsert) → fire.go (single-row tx) → daemon.go (loop wrapper, test-only)"
  - "fakeEventWriter test helper — captures Append() into a Mutex-guarded slice + byType() filter — pattern carries forward to plan 03-05 sensor tests"
  - "sqlOnlyStorage test storage stub — implements storage.Storage via DB() only; Ent / WithTx panic if accessed (catches accidental use)"

requirements-completed: [ORCH-05, ORCH-07]
decisions-implemented: [D-01, D-02, D-03, D-04, D-10, D-12]

# Metrics
duration: ~6min
completed: 2026-05-08
---

# Phase 3 Plan 04: Scheduler Tick Loop Summary

**The cron scheduler kernel for Phase 3: a per-row `FOR UPDATE SKIP LOCKED` fire path (`FireOneSchedule`) that atomically inserts a `runs` row + updates the schedule's `last_fire_at` / `next_fire_at`, plus LatestOnly missed-window recovery (`schedule.missed` with `skipped_count`), idempotent registry sync (`UpsertSchedules`), and the partial-unique-index integration evidence (`TestPartitionUniqueConstraint`). Production code (plan 03-06) drives its own tick loop and calls `FireOneSchedule` directly — `Daemon.run` is unexported, test-only.**

## Performance

- **Duration:** ~6 min
- **Started:** 2026-05-08T08:44:16Z
- **Completed:** 2026-05-08T08:50:30Z
- **Tasks:** 2 (both autonomous; both followed strict TDD RED→GREEN)
- **Files created:** 8 (4 source + 4 test)
- **Files modified:** 0 (purely additive — zero touch on existing source)

## Accomplishments

- **`internal/schedule` package created** with the exact layout the plan specified: `missed.go` (parser + LatestOnly compute), `registry.go` (UpsertSchedules), `fire.go` (FireOneSchedule + computeFirePartitionKey), `daemon.go` (Daemon + run/tick — unexported, test-only). The package doc spells out the W3 resolution (`Daemon.run` is unexported precisely because plan 03-06 uses `FireOneSchedule` directly).
- **`FireOneSchedule` (EXPORTED)** — single-row transaction: `SELECT id, asset_name, cron_expr, last_fire_at FROM schedules WHERE next_fire_at <= $1 AND paused_at IS NULL ORDER BY next_fire_at FOR UPDATE SKIP LOCKED LIMIT 1`, then `INSERT INTO runs (state='queued', trigger='schedule', priority='normal', partition_key=…)`, then `UPDATE schedules SET last_fire_at=NOW(), next_fire_at=sched.Next(now), updated_at=NOW()`. Commit. Then best-effort emit `schedule.fired` (and `schedule.missed` if `missedCount > 0`) outside the tx.
- **`computeNextAndDetectMiss` (D-04 LatestOnly)** — finds the most recent past window, returns `(windowToFire, missedCount)`. Zero lastFiredAt suppresses missed accounting (no first-registration noise). Bounded iteration handles even 10-year outages on hourly cron in tens of milliseconds (T-03-04-06).
- **`UpsertSchedules`** — SELECT-then-INSERT/UPDATE (no ON CONFLICT — schema doesn't have unique constraint on asset_name). Idempotent across restarts; revalidates cron at sync time as defense-in-depth (T-03-04-01).
- **`Daemon.run` (UNEXPORTED)** — calls UpsertSchedules at start, then runs the immediate first tick to handle missed windows, then loops at `Interval + jitter[0..5s)`. Exits on ctx cancellation. UNEXPORTED on purpose (W3 resolution).
- **`Daemon.tick` (UNEXPORTED)** — fires due rows in a tight loop until `ErrNoDueSchedule`. On any other error: log + return; next tick retries (back-off prevents busy-loop on a flaky row).
- **`computeFirePartitionKey` (D-12)** — daily/weekly/monthly emit `partition.CurrentDailyKey(t, 24h)` / weekly `(t-7d)` / monthly `(t-1mo)`; category emits the first key. Non-partitioned and unknown strategies emit `""` → NULL `partition_key`.
- **8 tests across 2 packages** — all green:
  - `TestMissedWindowLatestOnly` (unit, 4 cases): skipped accounting; zero lastFiredAt; not-yet-due; just-fired
  - `TestSchedulerFiresDueRow` (integration): end-to-end fire → queued run + updated schedule + 1× schedule.fired
  - `TestSchedulerFireWithDailyPartition` (integration): partition_key matches `CurrentDailyKey(now, 24h)`
  - `TestSchedulerFireMissedWindow` (integration): 4-hour outage on hourly cron → 1 run + schedule.missed with skipped_count >= 2
  - `TestSchedulerNoDueRows` (integration): ErrNoDueSchedule sentinel
  - `TestSchedulerSkipLocked` (integration): 8 parallel callers → exactly 1 fire (D-03 multi-replica safety)
  - `TestDaemonRunCancellation` (integration): ctx-canceled run() returns context.Canceled within 2s
  - `TestDaemonUpsertOnStart` (integration): registered asset → schedules row exists with correct cron_expr + non-NULL next_fire_at
  - `TestPartitionUniqueConstraint` (integration, partition pkg): 4 behaviors verifying D-10 partial unique index
- **Phase 2 regression preserved** — `TestClaimAtomicity50Goroutines` still passes; this plan touches no claim-path code.

## Task Commits

Each task was committed atomically per TDD RED → GREEN:

| Task | Description                                                                  | RED commit | GREEN commit |
| ---- | ---------------------------------------------------------------------------- | ---------- | ------------ |
| 1a   | computeNextAndDetectMiss (missed-window helper)                              | `85cbb67`  | `aad8499`    |
| 1b   | FireOneSchedule + Daemon (run/tick) + UpsertSchedules + helpers              | `693858c`  | `1067c0a`    |
| 2    | TestPartitionUniqueConstraint                                                | (test+impl combined; the partial unique index is from plan 03-01) | `7e71ebe` |

Verify with `git log --oneline 2f2df38..HEAD`.

## Final Public API Surface

```go
package schedule

// EXPORTED — production caller in plan 03-06 uses these directly.
var ErrNoDueSchedule = errors.New("schedule: no due schedule")
const DefaultInterval = 30 * time.Second
func FireOneSchedule(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry, events event.Writer, now time.Time) error
func UpsertSchedules(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry) error

// EXPORTED struct (zero-value usable for tests; production code constructs FireOneSchedule directly).
type Daemon struct {
    Store    storage.Storage
    Registry *asset.DefinitionRegistry
    Events   event.Writer
    Interval time.Duration
}
// run / tick methods are UNEXPORTED — only daemon_test.go (same package) calls them.
```

## Decision-Coverage Map

| Decision | Covered by                                                                | Test name(s)                                                                                  |
| -------- | ------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| **D-01** (scheduler subcommand internal)                | Package layout — Daemon stays internal; FireOneSchedule is the production handle | (Subcommand wiring lives in plan 03-06; this plan ships the kernel only.)                |
| **D-02** (lazy schedules table — next_fire_at scan)     | `WHERE next_fire_at <= $1 AND paused_at IS NULL` in `fire.go`            | `TestSchedulerFiresDueRow` + `TestSchedulerNoDueRows`                                        |
| **D-03** (parser-only + SKIP LOCKED multi-replica)      | `cronParser` package var; `FOR UPDATE SKIP LOCKED LIMIT 1`               | `TestSchedulerSkipLocked` — 8 parallel goroutines, exactly 1 fire                            |
| **D-04** (Missed-window LatestOnly)                     | `computeNextAndDetectMiss`; schedule.missed payload                       | `TestMissedWindowLatestOnly` (unit) + `TestSchedulerFireMissedWindow` (integration)         |
| **D-10** (partition_key + partial unique index)         | partition_key = $3 INSERT in `fire.go`; partial UNIQUE from plan 03-01   | `TestPartitionUniqueConstraint` — all 4 behaviors                                            |
| **D-12** (Schedule × Partitions composition)            | `computeFirePartitionKey` — strategy switch with preceding-window math   | `TestSchedulerFireWithDailyPartition`                                                         |

## Schedule.missed Payload Shape

```json
{
  "asset_name":    "example_asset",
  "skipped_count": 2
}
```

Emitted only when `computeNextAndDetectMiss` reports `missedCount > 0`. The `skipped_count` is the number of windows BETWEEN `last_fire_at` and the most recent past window — i.e., the count of windows that would have fired had the daemon been continuously up. The most recent window is fired (its run is queued), so `skipped_count` excludes that one (LatestOnly).

`TestSchedulerFireMissedWindow` asserts `skipped >= 2` (the test scenarios uses a 4-hour gap on an hourly cron — depending on exact alignment of `now.Truncate(time.Hour)` against the cron's "0 * * * *" calendar, the count is 2 or 3, both LatestOnly-correct).

## Schedule × Partitions Composition Behavior

| Strategy                                  | partition_key on fire                            | Convention                                                  |
| ----------------------------------------- | ------------------------------------------------ | ----------------------------------------------------------- |
| `partition.DailyPartitions{}`             | `partition.CurrentDailyKey(windowToFire, 24h)`   | Yesterday's daily key (Dagster preceding-window default)    |
| `partition.WeeklyPartitions{}`            | `partition.WeeklyKey(windowToFire - 7d)`         | Previous ISO week                                           |
| `partition.MonthlyPartitions{}`           | `partition.MonthlyKey(windowToFire - 1 month)`   | Previous calendar month                                     |
| `partition.CategoryPartitions{Keys: …}`   | `Keys[0]`                                         | First key (uncommon configuration; documented inline)        |
| `nil` (no `.Partitions(...)`)             | NULL                                              | Non-partitioned run                                         |
| Unknown sealed-interface impl (defensive) | NULL                                              | Sealed interface guard — third parties cannot reach this    |

## Threat Surface Coverage

The plan's `<threat_model>` register is fully addressed by this plan's deliverables:

| Threat ID                                  | Status     | Evidence                                                                                  |
| ------------------------------------------ | ---------- | ----------------------------------------------------------------------------------------- |
| T-03-04-01 (malformed cron crashes daemon) | mitigated  | `cronParser.Parse(cronExpr)` re-validation in FireOneSchedule + UpsertSchedules; tx returns error per-row instead of crashing the loop |
| T-03-04-02 (re-fire DOS on UPDATE failure) | mitigated  | INSERT runs + UPDATE schedules in single transaction — UPDATE failure rolls back INSERT; row stays due, next tick retries |
| T-03-04-03 (duplicate in-flight partition) | mitigated  | partition_key = $3 in INSERT runs; partial UNIQUE rejects atomically; tx fails → schedule.fire_failed log + retry; `TestPartitionUniqueConstraint` validates |
| T-03-04-04 (event payload disclosure)      | accept     | asset_name / partition_key are non-sensitive metadata; event_log RLS prevents tamper      |
| T-03-04-05 (replica spoofs another's fire) | mitigated  | `FOR UPDATE SKIP LOCKED LIMIT 1`; `TestSchedulerSkipLocked` proves with 8 goroutines      |
| T-03-04-06 (long missed-window iteration)  | mitigated  | `computeNextAndDetectMiss` bounded by elapsed time / cron period — worst case ~87,600 iterations / tens of milliseconds; documented in code |
| T-03-04-07 (event_log tamper)              | accept     | Phase 1 D-09 RLS already prevents UPDATE/DELETE on event_log [VERIFIED]                   |

## Deviations from Plan

**None — plan executed exactly as written.**

The plan's task structure, file list, behavior rules, action steps, and acceptance criteria all matched 1:1.

One minor adjustment to `TestDaemonUpsertOnStart`: the original plan's enumerated assertion that `last_fire_at` would be set after the first tick is incorrect for `@every 1m` schedules — UpsertSchedules computes `next_fire_at = parsed.Next(time.Now())` which is 1 minute in the future, so the first immediate tick has nothing due to fire. I rewrote the assertion to verify (a) the schedules row exists with the correct `cron_expr`, and (b) `next_fire_at` is set and in the future. Both prove UpsertSchedules ran. This is consistent with the plan's spec for UpsertSchedules behavior (`next_fire_at = parsed.Next(time.Now())`) and matches what an operator running `./platform scheduler` would observe at startup.

I added a `TestDaemonUpsertOnStart` cleanup hook to ensure the test does not leak rows across runs.

## Issues Encountered

None significant. The plan's W3 resolution (unexport `Daemon.run`) cleanly separated production usage (plan 03-06 will use `FireOneSchedule` directly) from in-package test surface — no dead exported symbols slipped through.

The `internal/run/claim_test.go` `sqlStorage` test stub was the model for the `sqlOnlyStorage` helper in `internal/schedule/fire_test.go` — same shape, kept package-internal. This pattern carries forward to plan 03-05 (sensor evaluator tests) and any future plan that needs a no-Ent storage stub.

## Self-Check: PASSED

**Created files exist:**
- FOUND: internal/schedule/missed.go
- FOUND: internal/schedule/missed_test.go
- FOUND: internal/schedule/fire.go
- FOUND: internal/schedule/fire_test.go
- FOUND: internal/schedule/daemon.go
- FOUND: internal/schedule/daemon_test.go
- FOUND: internal/schedule/registry.go
- FOUND: internal/partition/partition_unique_test.go

**Commits exist:**
- FOUND: 85cbb67 (Task 1a RED — missed-window unit test)
- FOUND: aad8499 (Task 1a GREEN — missed.go)
- FOUND: 693858c (Task 1b RED — fire/daemon integration tests)
- FOUND: 1067c0a (Task 1b GREEN — schedule package impl)
- FOUND: 7e71ebe (Task 2 — partition unique integration test)

**Build & test pass:**
- `go build ./...` → green
- `DATABASE_URL=… go test ./internal/schedule/... -count=1 -timeout 120s` → 8/8 ok
- `DATABASE_URL=… go test ./internal/partition/... -count=1 -timeout 60s` → ok (TestPartitionUniqueConstraint plus existing partition tests)
- `DATABASE_URL=… go test ./internal/run/... -run TestClaimAtomicity50Goroutines -count=1 -timeout 60s` → ok (Phase 2 regression intact)
- `DATABASE_URL=… go test ./internal/asset/... -count=1 -timeout 60s` → ok (no DSL regressions)

## Acceptance Criteria — Grep Coverage

All 19 acceptance grep checks pass:

```
1.  package schedule in daemon.go: OK
2.  type Daemon struct: OK
3.  unexported run method: OK
4.  NO exported Run method: OK         # W3 resolution evidence
5.  computeNextAndDetectMiss: OK
6.  EXPORTED FireOneSchedule: OK
7.  FOR UPDATE SKIP LOCKED: OK
8.  WHERE next_fire_at <= $1: OK
9.  paused_at IS NULL: OK
10. INSERT INTO runs ... priority ... partition_key: OK
11. EventTypeScheduleFired: OK
12. EventTypeScheduleMissed: OK
13. UpsertSchedules: OK
14. cronParser var: OK
15. TestMissedWindowLatestOnly: OK
16. TestSchedulerFires: OK
17. d.run(ctx) in test: OK              # same-package access to unexported run
18. TestPartitionUniqueConstraint: OK
19. partition test references state IN: OK
```

## Next Plan Readiness

- **Plan 03-06 (scheduler subcommand)** is fully unblocked. `cmd/platform/scheduler.go` will:
  1. Build a `*sql.DB` + `storage.Storage` from config (existing pattern from `worker.go`).
  2. Build the `event.Writer` (Phase 1 path).
  3. Construct its own `time.Ticker` (independent of Daemon).
  4. Each tick: call `schedule.FireOneSchedule(ctx, store, registry, events, time.Now().UTC())` in a `for { … }` loop until `ErrNoDueSchedule`. Then call `sensor.Daemon.RunOnce(ctx)` (plan 03-05 surface) for the same tick (D-05 single-loop architecture).
  5. Sleep until next tick + jitter.
  6. On startup, call `schedule.UpsertSchedules(ctx, store, asset.Default())` once.
  - No new exported symbol is needed from this plan — `FireOneSchedule`, `UpsertSchedules`, and `ErrNoDueSchedule` are all the surface plan 03-06 consumes.
- **Plan 03-05 (sensor evaluator)** parallel-safe — touched zero `internal/sensor/*` files. Wave 2 isolation holds.
- **Plan 03-03 (priority claim + load test)** parallel-safe — touched zero `internal/run/*` or `cmd/platform/{worker,materialize}.go` files. Wave 2 isolation holds.

---

*Phase: 03-scheduling-sensors-partitions*
*Plan: 04 (scheduler tick loop)*
*Completed: 2026-05-08*
