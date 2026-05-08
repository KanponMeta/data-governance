---
phase: 03-scheduling-sensors-partitions
reviewed: 2026-05-08T09:35:09Z
depth: standard
files_reviewed: 44
files_reviewed_list:
  - cmd/platform/backfill.go
  - cmd/platform/main.go
  - cmd/platform/scheduler.go
  - cmd/platform/scheduler_test.go
  - cmd/platform/worker.go
  - internal/asset/asset.go
  - internal/asset/builder.go
  - internal/asset/builder_test.go
  - internal/asset/io.go
  - internal/asset/io_test.go
  - internal/backfill/independence_test.go
  - internal/backfill/spec.go
  - internal/backfill/spec_test.go
  - internal/backfill/status.go
  - internal/backfill/submit.go
  - internal/backfill/submit_test.go
  - internal/event/types.go
  - internal/event/types_test.go
  - internal/partition/keygen.go
  - internal/partition/keygen_test.go
  - internal/partition/partition_unique_test.go
  - internal/partition/strategy.go
  - internal/partition/strategy_test.go
  - internal/run/claim.go
  - internal/run/claim_test.go
  - internal/run/priority.go
  - internal/run/priority_test.go
  - internal/runtime/executor.go
  - internal/runtime/executor_test.go
  - internal/schedule/daemon.go
  - internal/schedule/daemon_test.go
  - internal/schedule/fire.go
  - internal/schedule/fire_test.go
  - internal/schedule/missed.go
  - internal/schedule/missed_test.go
  - internal/schedule/registry.go
  - internal/sensor/daemon.go
  - internal/sensor/daemon_test.go
  - internal/sensor/evaluate.go
  - internal/sensor/evaluate_test.go
  - internal/sensor/registry.go
  - internal/storage/ent/schema/backfill.go
  - internal/storage/ent/schema/run.go
  - internal/storage/ent/schema/schedule.go
  - internal/storage/ent/schema/sensor.go
  - migrations/20260508120000_phase3_runs_columns.sql
  - migrations/20260508121000_phase3_schedules_sensors_backfills.sql
  - test/integration/e2e_postgres_test.go
findings:
  critical: 0
  warning: 4
  info: 6
  total: 10
status: issues_found
---

# Phase 3: Code Review Report

**Reviewed:** 2026-05-08T09:35:09Z
**Depth:** standard
**Files Reviewed:** 44 source + test files (plus 2 migrations)
**Status:** issues_found

## Summary

Phase 3 introduces scheduling, sensors, partitions, and backfills as additive subsystems on top of the Phase 2 run-execution engine. The code is generally well-structured: SQL is fully parameterised (no injection risk), `SELECT FOR UPDATE SKIP LOCKED` is used consistently for multi-replica safety, and panic recovery is wired into both `safeEvaluate` (sensor) and `safeMaterialize` (executor) per the threat model. Tests cover the major concurrency invariants (50-goroutine claim atomicity, SKIP LOCKED on schedule fires, backfill independence).

The findings below are concentrated in two areas:

1. **TOCTOU races during multi-replica daemon startup** — both `schedule.UpsertSchedules` and `sensor.upsertOneSensor` use a non-transactional `SELECT` → `INSERT/UPDATE` pattern with no `UNIQUE` constraint on `(asset_name)` / `(asset_name, sensor_name)`. Two scheduler replicas starting simultaneously can produce duplicate rows that subsequently fire the same schedule twice or evaluate the same sensor twice. This is the single most concerning correctness gap in the changeset.
2. **Nuisance / clarity issues** — a `shutdownCtx` plumbed into `runScheduler` is created with the right intent but never used; `computeNextAndDetectMiss` returns a future window in the clock-skew branch that `FireOneSchedule` will then fire (contradicting the comment); a couple of comments do not match observed behaviour.

No security vulnerabilities, no SQL injection vectors, no resource leaks of consequence, and no panic-escape paths were found.

## Warnings

### WR-01: `UpsertSchedules` race produces duplicate rows under multi-replica startup

**File:** `internal/schedule/registry.go:44-73`
**Issue:**
The reconciler runs `SELECT id, cron_expr FROM schedules WHERE asset_name = $1 LIMIT 1` and, on `sql.ErrNoRows`, does an unconditional `INSERT`. There is no enclosing transaction and the `schedules` table has only a non-unique `index.Fields("asset_name")` (see `internal/storage/ent/schema/schedule.go:48` and migration `20260508121000_phase3_schedules_sensors_backfills.sql:30`). When two scheduler replicas execute `runScheduler` concurrently at startup, both `SELECT`s observe no rows, both `INSERT`s succeed, and the table now has two rows for the same asset. Each subsequent tick will then `FOR UPDATE SKIP LOCKED` claim them in parallel and emit two `schedule.fired` events (and two queued runs) per cron window — directly defeating the multi-replica safety claim documented at `cmd/platform/scheduler.go:28`.

The implementer's comment at line 44 acknowledges the lack of an `ON CONFLICT` path, but the workaround chosen (SELECT-then-INSERT) reintroduces the very race that `ON CONFLICT` exists to prevent.

**Fix:**
Add a `UNIQUE` constraint on `schedules.asset_name` in a follow-up migration and switch to `INSERT … ON CONFLICT (asset_name) DO UPDATE SET cron_expr = EXCLUDED.cron_expr, next_fire_at = EXCLUDED.next_fire_at, updated_at = NOW() WHERE schedules.cron_expr <> EXCLUDED.cron_expr`. As an interim guard without a schema change, wrap the SELECT/INSERT in a serialisable transaction:

```go
tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
if err != nil {
    return err
}
defer func() { _ = tx.Rollback() }()
// SELECT … FOR UPDATE
// INSERT or UPDATE inside tx
return tx.Commit()
```

Note that `FOR UPDATE` on a non-existent row does NOT block competing inserts — only `SERIALIZABLE` isolation will cause one of the two transactions to abort with a serialisation failure that the caller can retry.

---

### WR-02: `sensor.upsertOneSensor` has the same multi-replica startup race as WR-01

**File:** `internal/sensor/registry.go:39-69`
**Issue:**
Identical pattern to WR-01: `SELECT id, min_interval_seconds FROM sensors WHERE asset_name=$1 AND sensor_name=$2 LIMIT 1`, then on `sql.ErrNoRows` a plain `INSERT`. The `sensors` table has only a non-unique composite index on `(asset_name, sensor_name)` (`internal/storage/ent/schema/sensor.go:57`, migration line 53). Two replicas booting together will create duplicate sensor rows; the SKIP-LOCKED selector in `evaluate.go:115-124` will then claim both per tick and the user-supplied `Sense()` will run twice per `MinInterval` — the dedup contract (D-07 layer 1) only deduplicates `RunKey`, not duplicate sensor rows, so this directly breaks the "evaluate at least every MinInterval" promise into "evaluate at least 2× every MinInterval".

**Fix:**
Same remediation as WR-01: add `UNIQUE (asset_name, sensor_name)` on the `sensors` table and switch to `INSERT … ON CONFLICT (asset_name, sensor_name) DO UPDATE SET min_interval_seconds = EXCLUDED.min_interval_seconds, updated_at = NOW() WHERE sensors.min_interval_seconds <> EXCLUDED.min_interval_seconds`.

---

### WR-03: Scheduler `shutdownCtx` is created but never used — graceful-shutdown plumbing is a no-op

**File:** `cmd/platform/scheduler.go:121-128`
**Issue:**
```go
case <-ctx.Done():
    slog.Info("scheduler.shutdown", "reason", ctx.Err().Error())
    shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
    defer cancel()
    _ = shutdownCtx
    return nil
```
The `shutdownCtx` is created with the configured `PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT` and explicitly discarded via `_ = shutdownCtx`. The deferred `cancel()` runs at `runScheduler` return, well after `return nil`. Net effect: the env var has no behavioural impact, and an in-flight tick that had not yet observed `ctx.Done()` is not given any extra time — its `tickCtx` (== `ctx`) is already cancelled. The comment "Allow shutdownTimeout for any in-flight tick to complete" is thus inaccurate.

The `TestSchedulerGracefulShutdown` (`scheduler_test.go:25-78`) passes today because the per-row transactions complete quickly enough that nothing is in-flight when SIGTERM arrives, but the test would not catch a regression that *required* the shutdown timeout to do its job.

**Fix:** Either (a) actually pass `shutdownCtx` to a final `runOneTick(shutdownCtx)` to drain in-flight work after the parent ctx cancels, or (b) delete the dead code and update the comment to acknowledge that ticks are simply allowed to complete on the cancelled ctx. Option (a) is the more honest implementation:

```go
case <-ctx.Done():
    slog.Info("scheduler.shutdown", "reason", ctx.Err().Error())
    shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
    defer cancel()
    runOneTick(shutdownCtx) // final drain on a fresh context
    return nil
```

Note that switching `runOneTick` to use `shutdownCtx` only on shutdown means `FireOneSchedule` and `sd.RunOnce` will actually be allowed to complete their currently-active transaction.

---

### WR-04: `computeNextAndDetectMiss` returns a future window for a clock-skewed `lastFiredAt`, and `FireOneSchedule` then fires it

**File:** `internal/schedule/missed.go:82-86` + `internal/schedule/fire.go:78-97`
**Issue:**
The doc comment at `missed.go:48-49` says: "If lastFiredAt is in the future relative to now (clock skew or tests), behave like 'not yet due' — return the next future window after lastFiredAt with missedCount=0." The function does in fact return a future `candidate`, but `FireOneSchedule` does not check the returned `windowToFire` against `now` before INSERTing the runs row:

```go
windowToFire, missedCount := computeNextAndDetectMiss(sched, lf, now)
nextFire := sched.Next(now)
partitionKey := computeFirePartitionKey(reg, assetName, windowToFire)
// … INSERT runs(…) regardless of whether windowToFire > now
```

So a clock-skew situation where `last_fire_at` somehow ended up in the future (replica clock drift, manual DB edit, restored snapshot) but `next_fire_at <= now` (the SELECT predicate) will still produce a `runs` row whose semantic window (`windowToFire`) is in the future — and `partition_key` will be derived from a future window, producing an unintuitive `partition_key` value (e.g., tomorrow's date).

In practice this is rare because the SELECT predicate `next_fire_at <= now` and the comment's invariant ("lastFiredAt > now") rarely co-occur, but the comment is currently misleading and the code is not defensive against the inconsistency.

**Fix:** Either tighten the predicate semantically by returning a sentinel and skipping the fire:
```go
windowToFire, missedCount := computeNextAndDetectMiss(sched, lf, now)
if windowToFire.After(now) {
    // Clock-skew: schedule says due-by-next_fire_at, but the cron walker
    // says the next valid window is in the future. Roll forward the row
    // and skip this fire to keep semantics honest.
    if _, err := tx.ExecContext(ctx, updateSchedSQL, windowToFire, schedID); err != nil {
        return fmt.Errorf("schedule.fire: roll forward: %w", err)
    }
    return tx.Commit()
}
```
…or update the comment in `missed.go` so it reflects the actual behaviour ("returns the next future window which the caller WILL fire — the caller must guard if that's not desired").

## Info

### IN-01: Comment on `sensor.evaluated` defer says "Always emit" but path is success-only

**File:** `internal/sensor/evaluate.go:162-175`
**Issue:** The comment claims `sensor.evaluated` is *always* emitted as audit-trail post-commit, but the deferred call is registered AFTER the early `return handleError(...)` path. On Sense errors, only `sensor.evaluation_failed` is emitted — never `sensor.evaluated`. Either the comment is wrong or the audit goal is unmet. (Behaviour is probably the right design; the comment just over-promises.)

**Fix:** Update comment to "Emit sensor.evaluated for non-error paths (success: fired or no-fire). Error paths emit sensor.evaluation_failed via handleError instead." Or, if the audit-trail intent really is "always", lift the defer above the `if evalErr != nil` branch so it covers errors too.

---

### IN-02: `sensor.evaluated` deferred event fires even when `tx.Commit()` failed

**File:** `internal/sensor/evaluate.go:163-175` (defer) + `evaluate.go:222, 246, 270, 320` (Commit sites)
**Issue:** The deferred `events.Append(ctx=context.Background(), …, "fired": result.Fired)` runs unconditionally on function return, including when `updateSensorOnNoFire`/`handleFired`/dedup-and-commit path returned a `tx.Commit()` error. In that case the DB state was rolled back but the event log records "fired=true/false" anyway. Inconsistency is small (events are observability per Phase 1 D-09) but the audit log will diverge from the canonical sensors table on commit failures.

**Fix:** Capture the outer `err` via a named return and skip the deferred emit when it is non-nil:
```go
func evaluateOneSensor(...) (err error) { // named return
    ...
    defer func() {
        if err != nil {
            return
        }
        _ = events.Append(...)
    }()
}
```

---

### IN-03: `sensor.evaluate.evaluateOneSensor` calls `a.Sensors()` once per loop iteration

**File:** `internal/sensor/evaluate.go:144-149`
**Issue:** `a.Sensors()` returns a defensive *copy* (per `internal/asset/asset.go:116`), so calling it inside the loop allocates the slice on every iteration. Hot path is bounded (small N per asset) but trivially fixable.

**Fix:**
```go
sensors := a.Sensors()
var spec *asset.SensorSpec
for i := range sensors {
    if sensors[i].Name == row.SensorName {
        s := sensors[i]
        spec = &s
        break
    }
}
```

---

### IN-04: `safeEvaluate` timeout semantics: when `MinInterval == 0`, defaults to `DefaultMinInterval` (30s) — but a Sense that respects ctx and finishes in 100ms still pays a 30s tick budget

**File:** `internal/sensor/evaluate.go:66-82`
**Issue:** Today this is fine because the timeout is just a deadline ceiling, but the docstring at line 65 says "timeout=0 is interpreted as max(spec.MinInterval, DefaultMinInterval)". A SensorSpec authored with `MinInterval: 5*time.Second` would be allowed to block the tick loop for 30s before timeout fires (because `DefaultMinInterval` floor wins). The `min` of (MinInterval, DefaultMinInterval) might be the more conservative default.

**Fix (clarify intent):** Either (a) document that the timeout floor is by design intentionally generous so users with sub-default intervals don't get cancellation surprises, or (b) flip the floor to `min(spec.MinInterval, DefaultMinInterval)` when `spec.MinInterval > 0`. Option (a) is probably correct given the threat model rationale (Pitfall 3) — just align doc with code.

---

### IN-05: `runBackfill` arg-order parser misclassifies asset names that start with `-`

**File:** `cmd/platform/backfill.go:38-50`
**Issue:** Asset names starting with `-` (e.g., a hypothetical `-experimental` test asset) are dropped into `flagArgs` and `flag.Parse` will reject them. The asset is then absent from `positional`, producing the generic usage error rather than a more diagnostic one. Edge case (most asset names won't start with a dash), but the documented support for "asset positional anywhere" is technically incorrect.

**Fix:** Note the limitation in the doc comment, or add a `--asset` flag as the canonical form and keep positional for convenience.

---

### IN-06: `cmd/platform/main.go runHealthcheck` calls `os.Exit` from inside `defer`-protected scope

**File:** `cmd/platform/main.go:151-168`
**Issue:** `os.Exit(1)` skips deferred `cancel()`. In practice this is fine (process termination cleans up the OS-level timer), but the pattern (defer + os.Exit on the same code path) is a known footgun if anyone later adds a deferred cleanup that *must* run (e.g., flushing a metric, releasing a file lock). Mark the function clearly in a comment, or restructure to a single exit at the end:

**Fix:**
```go
func runHealthcheck() {
    code := doHealthcheck() // does all work, returns exit code
    os.Exit(code)
}
```
Defers in `doHealthcheck` then run normally before `os.Exit`.

---

_Reviewed: 2026-05-08T09:35:09Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
