---
phase: 03-scheduling-sensors-partitions
plan: 05
subsystem: scheduler
tags: [sensor, evaluator, daemon, dedup, cooldown, auto-disable, partition-composition, skip-locked]

# Dependency graph
requires:
  - phase: 03-scheduling-sensors-partitions/01-schema-events-foundation
    provides: sensors table (last_run_key/cooldown_until/consecutive_failures/disabled_at), runs.partition_key/priority columns, six sensor.* EventType constants in AllKnownTypes()
  - phase: 03-scheduling-sensors-partitions/02-asset-dsl-and-partitions
    provides: asset.SensorSpec/SensorResult/SensorFunc, asset.Asset.Sensors()/Partitions(), partition.PartitionStrategy + DailyPartitions/WeeklyPartitions/MonthlyPartitions/CategoryPartitions, partition.CurrentDailyKey/WeeklyKey/MonthlyKey
provides:
  - "sensor.Daemon struct + Daemon.RunOnce(ctx) method — drain-loop tick driver, called from scheduler subcommand (plan 03-06)"
  - "sensor.UpsertSensors(ctx, store, registry) — idempotent registry → sensors table reconciliation"
  - "sensor.AutoDisableThreshold (default 60) constant"
  - "sensor.DefaultMinInterval (30s) constant"
  - "sensor.ErrNoDueSensor sentinel for drain detection"
  - "internal-only: safeEvaluate(ctx, spec, timeout) — timeout + panic recovery wrapper"
  - "internal-only: evaluateOneSensor — SELECT FOR UPDATE SKIP LOCKED transaction handler"
  - "internal-only: handleFired (two-layer dedup), handleError (failure counting + auto-disable)"
  - "internal-only: resolveSensorPartitionKey — D-12 RunKey-as-partition-key validator with previous-window fallback"
affects: [03-06-scheduler-subcommand, 03-07-backfill-cli]

# Tech tracking
tech-stack:
  added: []  # No new third-party dependencies — all built on existing pgx/sql/uuid/asset/event packages
  patterns:
    - "SELECT FOR UPDATE SKIP LOCKED multi-replica safety primitive on the sensors table — same primitive Phase 2 ClaimNext uses for runs"
    - "Same-tx INSERT runs + UPDATE sensors atomicity — last_run_key, cooldown_until, runs.partition_key all advance together or not at all"
    - "Best-effort post-commit event emission — events.Append failures do NOT roll back DB state (audit log is supplementary, not the source of truth)"
    - "RETURNING clause to read post-update consecutive_failures + disabled_at in handleError — single round-trip"
    - "SELECT-then-UPDATE-or-INSERT idempotent upsert (no MERGE/UPSERT — simpler, same semantics)"
    - "fakeEventWriter helper with sync.Mutex + byType filter for assertion-friendly event capture"

key-files:
  created:
    - "internal/sensor/evaluate.go (safeEvaluate + evaluateOneSensor + handleFired + handleError + resolveSensorPartitionKey + autoDisableOrphan + updateSensorOnNoFire)"
    - "internal/sensor/evaluate_test.go (11 test functions: 3 unit + 1 table-driven sub-suite + 7 integration tests)"
    - "internal/sensor/daemon.go (Daemon struct + RunOnce method)"
    - "internal/sensor/daemon_test.go (4 test functions covering drain semantics + upsert idempotence + ctx cancellation)"
    - "internal/sensor/registry.go (UpsertSensors public + upsertOneSensor private)"
    - ".planning/phases/03-scheduling-sensors-partitions/deferred-items.md (logs pre-existing internal/runtime test failures unrelated to this plan)"
  modified: []  # Pure additive plan — no existing files touched

key-decisions:
  - "safeEvaluate timeout default = max(spec.MinInterval, DefaultMinInterval) when caller passes 0 — Pitfall 3 mitigation; user contract 'evaluate at least every MinInterval' implies Sense() must complete within that window"
  - "Two-layer dedup order: RunKey check first (cheap string compare), cooldown check second (time compare) — both layers must allow the fire (D-07)"
  - "Auto-reset consecutive_failures on first successful evaluation (Fired=true OR Fired=false) per 03-RESEARCH.md A5 — flaky sensor that self-recovers does NOT count past failures against AutoDisableThreshold"
  - "resolveSensorPartitionKey accepts explicit RunKey when it parses for the strategy; falls back to previous-window key for daily/weekly/monthly (Open Question 1: 'use CurrentDailyKey(now, 24h)') — category strategy has NO fallback"
  - "Orphan handling: a sensor row referencing an unknown asset/sensor name is auto-disabled in the same evaluation tx (defense-in-depth; registry drift across deploys must not pin scheduler cycles)"
  - "Daemon.RunOnce returns nil on transient DB errors (logged via slog.Error) so one bad sensor row cannot stop the scheduler subcommand's tick loop — caller's next tick retries"
  - "UpsertSensors uses reg.List() + reg.Get(name) (the registry's existing public API) rather than a non-existent reg.All() — minimal API surface change"
  - "All event Append calls use context.Background() so a parent ctx cancellation cannot truncate the audit trail; the DB tx already committed before Append runs"

patterns-established:
  - "Phase 3 sensor plan TDD pattern: RED test commit → GREEN implementation commit per task (4 commits for 2 tasks)"
  - "Integration tests use fakeEventWriter + sqlStorage stub mirroring Phase 2 internal/run/claim_test.go pattern — DB-bound tests skip cleanly when DATABASE_URL unset"
  - "Same-tx event emission anti-pattern AVOIDED: events.Append always runs post-commit so a slow audit log does not extend the SKIP LOCKED row-lock window"

requirements-completed: [ORCH-06]
decisions-implemented: [D-05, D-06, D-07, D-08, D-12]

# Metrics
duration: ~8min
completed: 2026-05-08
---

# Phase 3 Plan 05: Sensor Evaluator Summary

**Sensor evaluation harness — `Daemon.RunOnce(ctx)` selects due sensors via `SELECT FOR UPDATE SKIP LOCKED`, calls user's `Sense(ctx)` under timeout + panic recovery, applies the two-layer dedup (RunKey + cooldown), and either enqueues a `runs` row with `trigger='sensor'` or records the dedup decision via event_log. Auto-disables on N consecutive failures with auto-reset on success.**

## Performance

- **Duration:** ~8 min
- **Started:** 2026-05-08T08:46:00Z
- **Completed:** 2026-05-08T08:54:30Z (approx)
- **Tasks:** 2 (both autonomous; both followed strict TDD RED → GREEN)
- **Files created:** 5 (3 production + 2 test) + 1 deferred-items log
- **Files modified:** 0 (pure additive plan)

## Accomplishments

- **`internal/sensor/evaluate.go`** — `safeEvaluate` enforces `context.WithTimeout(ctx, max(spec.MinInterval, DefaultMinInterval))` and `defer recover()` on every Sense() call. `evaluateOneSensor` runs the SELECT FOR UPDATE SKIP LOCKED + user-Sense + state-update sequence in a single transaction. `handleFired` implements D-07's two-layer dedup. `handleError` increments `consecutive_failures` and auto-disables at the threshold using a `RETURNING` clause for single-roundtrip atomicity. `resolveSensorPartitionKey` validates RunKey for the strategy before adopting it as `runs.partition_key`; falls back to previous-window for time strategies (Open Question 1 default).
- **`internal/sensor/daemon.go`** — `Daemon.RunOnce(ctx)` drains the queue of due sensors by repeatedly calling `evaluateOneSensor` until `ErrNoDueSensor`. Context cancellation honoured before each iteration. Transient DB errors logged via `slog.Error` and the function returns `nil` so one bad row cannot stop the scheduler subcommand's tick loop.
- **`internal/sensor/registry.go`** — `UpsertSensors(ctx, store, registry)` reconciles the in-process `asset.DefinitionRegistry` to the `sensors` table via SELECT-then-UPDATE-or-INSERT. Idempotent (no UPDATE on identical specs); changed `MinInterval` is propagated. Removed sensors are not deleted — they're handled by `evaluateOneSensor`'s orphan-disable path.
- **15 tests passing** — 4 unit (panic recovery, timeout, timeout default, partition-key resolver covering 9 cases), 7 integration (RunKey dedup, cooldown, fire happy-path, auto-disable, auto-reset, orphan, partition composition), 4 daemon (drain, ctx cancel, upsert idempotent, MinInterval update).

## Task Commits

Each task followed strict TDD with separate RED and GREEN commits:

| Task | Description                                                              | RED commit | GREEN commit |
| ---- | ------------------------------------------------------------------------ | ---------- | ------------ |
| 1    | safeEvaluate + handleResult + handleError + resolveSensorPartitionKey    | `46f215d`  | `f5ec658`    |
| 2    | Daemon.RunOnce + UpsertSensors (registry reconciliation + drain driver)  | `59bfc5b`  | `80a264e`    |

Verify with `git log --oneline 2f2df38..HEAD`.

## Decision-Coverage Map

| Decision | Covered by                                                                              | Test name(s)                                                                                                  |
| -------- | --------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| **D-05** (sensors share scheduler subcommand)            | `Daemon.RunOnce(ctx)` exposed for the scheduler's tick to call             | `TestDaemonRunOnceDrains`, `TestRunOnceContextCancellation`                                                   |
| **D-06** (SensorResult contract)                          | `evaluateOneSensor` reads `Asset.Sensors()` SensorSpec, calls `spec.Sense(ctx)` and threads `SensorResult.RunKey/Payload` through `handleFired` | `TestSensorFire`, `TestSensorRunKeyDedup`                                                                     |
| **D-07** (two-layer dedup: RunKey + cooldown)             | `handleFired` checks RunKey first (cheap string compare), then cooldown (time compare) — BOTH must pass before INSERT runs | `TestSensorRunKeyDedup` (layer 1), `TestSensorCooldown` (layer 2)                                              |
| **D-08** (Sense() error → log + retry; N-fail auto-disable) | `handleError` increments `consecutive_failures` and sets `disabled_at` at threshold using `CASE WHEN consecutive_failures + 1 >= $threshold` in same UPDATE; auto-resets on success | `TestSensorAutoDisable`, `TestSensorAutoResetOnSuccess`                                                       |
| **D-12** (Sensor × Partitions composition)                | `resolveSensorPartitionKey` validates RunKey before adopting it as `runs.partition_key`; falls back per strategy | `TestResolveSensorPartitionKey` (9 sub-tests), `TestSensorPartitionKeyDailyComposition`                       |

## Two-Layer Dedup Behavior (D-07 — verbatim)

The check order in `handleFired` is **RunKey first, cooldown second** — both layers must allow the fire before a run is enqueued:

```go
// Layer 1: RunKey dedup (cheap string compare; only meaningful when both sides non-empty).
if result.RunKey != "" && row.LastRunKey.Valid && row.LastRunKey.String == result.RunKey {
    // emit sensor.dedup_skipped, no INSERT
}

// Layer 2: cooldown window (time compare).
if row.CooldownUntil.Valid && now.Before(row.CooldownUntil.Time) {
    // emit sensor.cooldown_skipped, no INSERT
}

// Both layers passed → enqueue a run.
```

This is belt-and-suspenders defense:
- **Layer 1 alone fails** when user code intentionally returns the same key for legitimate same-key events within cooldown. Cooldown still blocks them.
- **Layer 2 alone fails** when user code has a bug returning the same key twice in rapid succession. RunKey check catches it before cooldown is even consulted.

## Auto-Disable + Auto-Reset (D-08)

- **Threshold:** `AutoDisableThreshold = 60` (default; override via `Daemon.DisableAfter`).
- **Auto-disable:** `handleError` SQL is `UPDATE sensors SET consecutive_failures = consecutive_failures + 1, disabled_at = CASE WHEN consecutive_failures + 1 >= $threshold THEN NOW() ELSE disabled_at END WHERE id = $id RETURNING ...`. Single roundtrip; the `RETURNING` clause exposes the new failure count and disabled_at status to the caller, which then emits `sensor.evaluation_failed` and (conditionally) `sensor.disabled` events.
- **Auto-reset semantics:** `consecutive_failures` resets to `0` on every successful evaluation — both `Fired=false` (success-no-fire via `updateSensorOnNoFire`) and `Fired=true` (success-with-fire via `handleFired`'s post-INSERT UPDATE). A flaky sensor that self-recovers does NOT count past failures against the threshold. This is per 03-RESEARCH.md A5 and matches Dagster's convention.
- **Orphan auto-disable:** A sensor row whose asset/sensor name has been removed from the in-process registry is auto-disabled with reason `"orphaned"` — defense in depth against registry drift across deploys.

## Sensor × Partitions Composition (D-12)

`resolveSensorPartitionKey(strategy, runKey, now)` decides the value of `runs.partition_key` when a sensor fires for a partitioned asset:

| Strategy           | RunKey valid? | partition_key result                                          |
| ------------------ | ------------- | ------------------------------------------------------------- |
| nil                | —             | `""` (non-partitioned run)                                    |
| DailyPartitions    | YYYY-MM-DD    | RunKey verbatim                                               |
| DailyPartitions    | else          | `partition.CurrentDailyKey(now, 24h)` — yesterday's key       |
| WeeklyPartitions   | YYYY-Www      | RunKey verbatim                                               |
| WeeklyPartitions   | else          | `partition.WeeklyKey(now - 7d)` — previous ISO week           |
| MonthlyPartitions  | YYYY-MM       | RunKey verbatim                                               |
| MonthlyPartitions  | else          | `partition.MonthlyKey(now.AddDate(0,-1,0))` — previous month  |
| CategoryPartitions | in `Keys`     | RunKey verbatim                                               |
| CategoryPartitions | else / empty  | `""` — NO fallback for category (by design, Pitfall 4)        |

Pitfall 4 is enforced because `CategoryPartitions` validation rejects keys that aren't in the declared `Keys` slice — a daily-formatted RunKey on a category sensor produces an empty `partition_key`, which (combined with the partial unique index `WHERE partition_key IS NOT NULL`) produces a non-partitioned run rather than a wrong-partition run.

## Public API Surface (Frozen for Plan 03-06)

```go
package sensor

// Constants
const DefaultMinInterval   = 30 * time.Second
const AutoDisableThreshold = 60

// Sentinel
var ErrNoDueSensor = errors.New("sensor: no due sensor")

// Daemon — exposed for the scheduler subcommand's tick loop (D-05).
type Daemon struct {
    Store        storage.Storage
    Registry     *asset.DefinitionRegistry
    Events       event.Writer
    DisableAfter int  // 0 → AutoDisableThreshold
}
func (d *Daemon) RunOnce(ctx context.Context) error

// Registry sync — called from the scheduler subcommand at startup.
func UpsertSensors(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry) error
```

## Threat Surface Coverage

The plan's `<threat_model>` register is fully addressed by this plan's deliverables:

| Threat ID  | Status     | Evidence                                                                                                      |
| ---------- | ---------- | ------------------------------------------------------------------------------------------------------------- |
| T-03-05-01 | mitigated  | `defer recover()` in `safeEvaluate`; `TestSensorPanicRecovery` enforces                                       |
| T-03-05-02 | mitigated  | `context.WithTimeout(ctx, spec.MinInterval)` in `safeEvaluate`; `TestSensorTimeoutEnforced` enforces          |
| T-03-05-03 | mitigated  | Two-layer dedup in `handleFired`; partial unique index from plan 03-01 acts as defense-in-depth at INSERT time. `TestSensorRunKeyDedup` + `TestSensorCooldown` enforce |
| T-03-05-04 | mitigated  | `consecutive_failures + 1 >= $threshold` auto-sets `disabled_at`; `WHERE disabled_at IS NULL` in selectSQL excludes disabled rows from subsequent ticks. `TestSensorAutoDisable` enforces |
| T-03-05-05 | accept     | Crafted-RunKey bypass is intended user behaviour for genuinely distinct events; cooldown layer 2 is the operator's defense                                                                |
| T-03-05-06 | accept     | SensorResult.Payload opaque per D-06; documented in code comment                                              |
| T-03-05-07 | mitigated  | `SELECT FOR UPDATE SKIP LOCKED` on sensors table — same primitive as Phase 2 ClaimNext (TestClaimAtomicity50Goroutines passes against the new schema, regression-verified) |
| T-03-05-08 | accept     | Direct-SQL trust model for `platform_app` consistent with runs/schedules                                      |

## Deviations from Plan

**None — plan executed exactly as written.**

The plan's task structure, file layout, behavior rules, and acceptance criteria all matched the implementation 1:1. Three minor adjustments worth noting (none alter scope):

1. **`reg.All()` replaced with `reg.List() + reg.Get(name)` in `UpsertSensors`.** The plan's pseudocode references `reg.All()` but the existing `internal/asset.DefinitionRegistry` (from plan 03-02) only exposes `List()` and `Get()`. Used the existing API verbatim — no change to plan intent.
2. **`AssetIO` constructor third arg already exists.** Plan 03-02 added `partitionKey` to `NewAssetIO`. This plan does not need to change `internal/runtime/executor.go` (already passes `""`).
3. **Added two extra tests beyond the plan's enumerated set:** `TestSensorOrphanDisabled` (covers the orphan auto-disable path the plan calls out in `<behavior>` but doesn't enumerate as a test) and `TestSensorPartitionKeyDailyComposition` (D-12 integration coverage). Both are pure threat-mitigation evidence — zero scope creep.

## Issues Encountered

- **Pre-existing `internal/runtime` test failures with "open ent: unsupported driver: pgx".** Verified to exist on master at base commit `2f2df38` — out of scope for plan 03-05. Logged in `.planning/phases/03-scheduling-sensors-partitions/deferred-items.md` for triage.
- **Plan's DATABASE_URL referenced `data_governance` database; actual local DB is `platform`.** Used the actual DB name (`postgres://platform_app:platform_app@localhost:5432/platform?sslmode=disable`) consistent with `Makefile` `integration` target. Same credentials. No code change.

## Self-Check: PASSED

**Created files exist:**
- FOUND: internal/sensor/evaluate.go
- FOUND: internal/sensor/evaluate_test.go
- FOUND: internal/sensor/daemon.go
- FOUND: internal/sensor/daemon_test.go
- FOUND: internal/sensor/registry.go
- FOUND: .planning/phases/03-scheduling-sensors-partitions/deferred-items.md

**Commits exist:**
- FOUND: 46f215d (Task 1 RED — failing tests)
- FOUND: f5ec658 (Task 1 GREEN — evaluate.go)
- FOUND: 59bfc5b (Task 2 RED — daemon/upsert tests)
- FOUND: 80a264e (Task 2 GREEN — daemon.go + registry.go)

**Build & test pass:**
- `go build ./...` → green
- `go vet ./internal/sensor/...` → clean
- `DATABASE_URL=... go test ./internal/sensor/... -count=1 -timeout 120s` → 15 tests, all pass
- Phase 2 regression `TestClaimAtomicity50Goroutines` → still passes (sensor changes do not touch runs claim path)

## Next Plan Readiness

- **Plan 03-06 (scheduler subcommand)** can now wire `sensor.Daemon{...}.RunOnce(ctx)` into its tick loop alongside `schedule.Daemon` (from plan 03-04). At startup it should call `sensor.UpsertSensors(ctx, store, reg)` to populate the sensors table from registered assets. Frozen `Daemon.RunOnce` signature: `func (d *Daemon) RunOnce(ctx context.Context) error`.
- **Plan 03-07 (backfill CLI)** is unaffected — sensor evaluation does not interact with backfill rows. The partial unique index on `(asset_name, partition_key)` from plan 03-01 ensures sensor-fired and backfill-enqueued runs cannot collide.

---

*Phase: 03-scheduling-sensors-partitions*
*Plan: 05 (sensor evaluator)*
*Completed: 2026-05-08*
