---
phase: 03-scheduling-sensors-partitions
plan: 06
subsystem: scheduling
tags: [scheduler, subcommand, cli, graceful-shutdown, signal-notify-context, single-tick-loop, d-01, d-05]

# Dependency graph
requires:
  - phase: 03-scheduling-sensors-partitions/04-scheduler-tick-loop
    provides: schedule.FireOneSchedule (EXPORTED), schedule.UpsertSchedules, schedule.ErrNoDueSchedule, schedule.DefaultInterval
  - phase: 03-scheduling-sensors-partitions/05-sensor-evaluator
    provides: sensor.Daemon{Store,Registry,Events,DisableAfter}, sensor.Daemon.RunOnce(ctx), sensor.UpsertSensors, sensor.AutoDisableThreshold
  - phase: 02-execution-engine
    provides: storage.NewPostgres, event.NewWriter, asset.Default(), cmd/platform/main.go switch pattern (server/worker/materialize)
provides:
  - "./platform scheduler subcommand — operational entry point that runs the schedule + sensor passes in a single tick loop (D-01, D-05)"
  - "cmd/platform/scheduler.go runScheduler() — production tick driver (replaces hypothetical schedule.Daemon.Run; W3 fix from plan 03-04)"
  - "Graceful shutdown via signal.NotifyContext(SIGINT, SIGTERM) — current tick completes; ctx propagates through schedule + sensor pass"
  - "Three env-var knobs — PLATFORM_SCHEDULER_INTERVAL (default 30s), PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT (default 30s), PLATFORM_SENSOR_DISABLE_AFTER (default 60)"
  - "TestSchedulerGracefulShutdown — subprocess integration test proving 5s SIGTERM exit + scheduler.started/scheduler.shutdown log lines"
affects: [03-07-backfill-cli]

# Tech tracking
tech-stack:
  added: []  # No new third-party dependencies — built on plans 03-04, 03-05, and Phase 1+2 surface
  patterns:
    - "signal.NotifyContext(SIGINT, SIGTERM) → ctx propagation through tick body — same shape as cmd/platform/worker.go (Phase 2 D-02)"
    - "Single-replica-safe + multi-replica-safe via inherited SELECT FOR UPDATE SKIP LOCKED on schedules and sensors (D-03 + D-05); operators may run any number of scheduler pods"
    - "Drain-then-tick pass per loop — schedule pass calls FireOneSchedule until ErrNoDueSchedule, then sensor.Daemon.RunOnce drains its own queue; both share the same tick clock (D-05)"
    - "Env-var configuration with safe fallbacks — invalid PLATFORM_SCHEDULER_INTERVAL (e.g., 0 or unparseable) falls back to schedule.DefaultInterval (T-03-06-02 mitigation)"
    - "Best-effort UpsertSchedules + UpsertSensors at startup — failure is logged but does not abort scheduler (registry sync is idempotent; next tick retries when re-running)"
    - "DSN never logged in scheduler.started payload — only interval/shutdown_timeout/sensor_disable_after (T-03-06-03 mitigation)"

key-files:
  created:
    - "cmd/platform/scheduler.go (runScheduler() — 132 lines including doc comment + tick loop + ctx-aware drains + signal handling)"
    - "cmd/platform/scheduler_test.go (TestSchedulerGracefulShutdown — subprocess SIGTERM integration test, 78 lines)"
  modified:
    - "cmd/platform/main.go (added `case \"scheduler\":` block alongside existing server/worker/materialize cases)"

key-decisions:
  - "runScheduler owns its own tick loop and calls schedule.FireOneSchedule directly — does NOT construct schedule.Daemon (its `run` driver is unexported; W3 resolution from plan 03-04)"
  - "Single tick loop drives BOTH schedule firing AND sensor evaluation (D-05) — eliminates two competing tickers + simplifies graceful shutdown semantics"
  - "First tick fires immediately on startup (NOT after the first interval) — handles missed-window recovery without a 30s warm-up delay"
  - "Tick body uses time.After(interval + jitter) instead of time.Ticker — matches D-03 thundering-herd mitigation (0..5s jitter); next tick recomputes jitter each iteration"
  - "Env-var validation rejects negative/zero durations and falls back to defaults — prevents PLATFORM_SCHEDULER_INTERVAL=0 from busy-looping the DB (T-03-06-02)"
  - "DATABASE_URL is the only required env var — three optional knobs (interval, shutdown_timeout, sensor_disable_after) ship with sensible defaults"
  - "scheduler.started log payload includes interval/shutdown_timeout/sensor_disable_after but NOT the DSN — operator-facing observability without secret leakage"
  - "Sensor pass error handling distinguishes context.Canceled (clean shutdown) from other errors (logged via slog.Error) — clean shutdown should not log a misleading error"
  - "shutdownTimeout is reserved for in-flight tick completion semantics; current schedule.FireOneSchedule per-row tx is short, so the timeout rarely matters in practice — included for safety + future tick body extensions"

patterns-established:
  - "cmd/platform CLI subcommand integration test pattern: build binary into t.TempDir(), exec.Command with custom env, send signal, capture stdout/stderr buffer, assert log lines + exit code"
  - "Worktree-portable test path: filepath.Abs(\"../..\") from cmd/platform/scheduler_test.go works regardless of repo root location (vs. hard-coded /home/... in plan template)"
  - "Two-pass tick body composition: schedule pass uses for-loop drain; sensor pass uses single RunOnce call (sensor.Daemon.RunOnce internally drains)"

requirements-completed: [ORCH-05, ORCH-06]
decisions-implemented: [D-01, D-05]

# Metrics
duration: ~3min
completed: 2026-05-08
---

# Phase 3 Plan 06: Scheduler Subcommand Summary

**The runnable surface for Phase 3 scheduling: `./platform scheduler` is now an operator-facing subcommand (parallel to `server` / `worker` / `materialize`) that bootstraps storage + asset registry + event writer, reconciles the in-process registry into the `schedules` and `sensors` tables, and runs a single tick loop driving `schedule.FireOneSchedule` (drain) → `sensor.Daemon.RunOnce` (drain) per tick. SIGINT/SIGTERM triggers graceful shutdown via `signal.NotifyContext`; the current tick completes and the daemon exits cleanly. This plan ships D-01 (subcommand pattern) + D-05 (sensors share scheduler subcommand) and consumes the frozen interfaces from plans 03-04 (`FireOneSchedule`, `UpsertSchedules`, `ErrNoDueSchedule`) and 03-05 (`sensor.Daemon`, `UpsertSensors`, `AutoDisableThreshold`).**

## Performance

- **Duration:** ~3 min
- **Started:** 2026-05-08T09:00:16Z
- **Completed:** 2026-05-08T09:03:04Z
- **Tasks:** 2 (both autonomous; Task 1 implementation; Task 2 integration test)
- **Files created:** 2 (cmd/platform/scheduler.go + cmd/platform/scheduler_test.go)
- **Files modified:** 1 (cmd/platform/main.go — added switch case)
- **Commits:** 2

## Accomplishments

- **`cmd/platform/main.go`** — added `case "scheduler":` block to the existing `start` / `migrate` / `healthcheck` / `worker` / `materialize` switch. Mirrors the exact pattern used by Phase 2's worker/materialize subcommands. Error path uses the same `slog.Error("platform.scheduler_failed", ...)` + `os.Exit(1)` shape.
- **`cmd/platform/scheduler.go runScheduler()`** — production tick driver:
  1. `signal.NotifyContext(ctx, SIGINT, SIGTERM)` — operator signals propagate via ctx through the entire tick body
  2. DSN check — explicit error if `DATABASE_URL` is unset (consistent with worker.go)
  3. `storage.NewPostgres` + `defer store.Close()` + `event.NewWriter` + `asset.Default()` — same bootstrap pattern as worker.go
  4. Env var parsing with safe fallbacks — `PLATFORM_SCHEDULER_INTERVAL` → `schedule.DefaultInterval` (30s); `PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT` → 30s; `PLATFORM_SENSOR_DISABLE_AFTER` → `sensor.AutoDisableThreshold` (60). Invalid values (zero, negative, unparseable) fall back to defaults — T-03-06-02 mitigation
  5. `schedule.UpsertSchedules` + `sensor.UpsertSensors` at startup — registry sync is best-effort (logged on failure; next tick retries through the same code path)
  6. Construct `sensor.Daemon{Store, Registry, Events, DisableAfter}` — single instance reused across all ticks
  7. `slog.Info("scheduler.started", ...)` with interval/shutdown_timeout/sensor_disable_after — DSN is intentionally NOT logged (T-03-06-03)
  8. Tick body (`runOneTick`):
     - Schedule pass: `for { ... }` calling `schedule.FireOneSchedule` until `ErrNoDueSchedule` or any other error (logged + break)
     - Sensor pass: `sd.RunOnce(tickCtx)` once per tick (RunOnce internally drains all due sensors)
     - Debug log: `scheduler.tick_completed` with duration
  9. Initial tick fires **immediately** on startup — handles missed-window recovery without 30s warm-up delay
  10. Loop body: `select { case <-time.After(interval + jitter): runOneTick(ctx); case <-ctx.Done(): scheduler.shutdown + return nil }`. Jitter recomputed each iteration via `rand.Int64N(5000) * time.Millisecond` — D-03 thundering-herd mitigation across multiple replicas
- **`cmd/platform/scheduler_test.go TestSchedulerGracefulShutdown`** — subprocess integration test:
  - Builds platform binary into `t.TempDir()` via `go build -o ./platform ./cmd/platform`
  - Resolves repo root via `filepath.Abs("../..")` so the test is portable across worktrees (NOT hard-coded `/home/...`)
  - Spawns `./platform scheduler` with `PLATFORM_SCHEDULER_INTERVAL=100ms` + `PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT=2s` to exercise multiple tick cycles in 1s
  - After 1s sends `syscall.SIGTERM` to the child
  - Asserts:
    - Process exits with code 0 within 5s (graceful shutdown — T-03-06-01)
    - stdout contains `scheduler.started` log line
    - stdout contains `scheduler.shutdown` log line
  - Skips cleanly when `DATABASE_URL` is unset (mirrors `claim_test.go` pattern)

## Task Commits

| Task | Description | Commit |
| ---- | ----------- | ------ |
| 1    | runScheduler() + main.go switch case | `02e3d19` |
| 2    | TestSchedulerGracefulShutdown subprocess SIGTERM integration test | `fc1f1ed` |

Verify with `git log --oneline bf35bfe..HEAD`.

## Final Public Surface

```go
// cmd/platform/main.go switch
case "scheduler":
    if err := runScheduler(); err != nil {
        slog.Error("platform.scheduler_failed", "error", err)
        os.Exit(1)
    }

// cmd/platform/scheduler.go
func runScheduler() error  // package main, exported within cmd/platform
```

**Env vars consumed:**

| Env var | Default | Purpose |
| ------- | ------- | ------- |
| `DATABASE_URL` | (required) | PostgreSQL DSN — same shape as worker subcommand |
| `PLATFORM_SCHEDULER_INTERVAL` | `30s` (`schedule.DefaultInterval`) | Tick cadence; jitter `[0, 5000]ms` added per tick |
| `PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT` | `30s` | Reserved for in-flight tick completion (per-row tx is short; rarely matters) |
| `PLATFORM_SENSOR_DISABLE_AFTER` | `60` (`sensor.AutoDisableThreshold`) | Consecutive-failure threshold before sensor auto-disabled |

**Log lines emitted (slog):**

| Level | Key | Trigger |
| ----- | --- | ------- |
| Info  | `scheduler.started` | Once at entry; payload: interval, shutdown_timeout, sensor_disable_after |
| Info  | `scheduler.shutdown` | Once at exit; payload: reason (e.g., `context canceled`) |
| Error | `scheduler.upsert_schedules_failed` | Best-effort startup reconcile failure |
| Error | `scheduler.upsert_sensors_failed` | Best-effort startup reconcile failure |
| Error | `scheduler.fire_failed` | Schedule pass error other than ErrNoDueSchedule |
| Error | `scheduler.sensor_runonce_failed` | Sensor pass error other than context.Canceled |
| Debug | `scheduler.tick_completed` | After each tick; payload: duration |

## Decision-Coverage Map

| Decision | Covered by | Test name(s) |
| -------- | ---------- | ------------ |
| **D-01** (scheduler subcommand pattern, alongside server/worker/materialize) | `case "scheduler":` block in `cmd/platform/main.go`; `runScheduler()` in `cmd/platform/scheduler.go` | TestSchedulerGracefulShutdown — proves the binary actually starts, runs, and exits cleanly via the new subcommand |
| **D-05** (sensors share scheduler subcommand goroutine — single tick loop) | `runOneTick` body calls `schedule.FireOneSchedule` (drain) then `sd.RunOnce(tickCtx)` in the same iteration | TestSchedulerGracefulShutdown — the subprocess emits both schedule.started and exits cleanly within 5s, proving the combined loop respects ctx cancellation |

## W3 Resolution Confirmation

**This plan does NOT consume `schedule.Daemon.Run`** — that method is unexported in plan 03-04 (`run` lowercase). Production code in `runScheduler()` calls `schedule.FireOneSchedule` directly. Acceptance criteria explicitly check:

```
! grep -q 'schedule.Daemon{' cmd/platform/scheduler.go        → PASS (no construction)
! grep -q 'schedule.Daemon.Run|sched.Daemon.Run' cmd/platform/scheduler.go → PASS (no method call)
```

This is by design — see plan 03-04 § "W3 resolution" — to keep the production driver in `cmd/platform` so the single tick loop can interleave `sensor.Daemon.RunOnce` per D-05.

## Threat Surface Coverage

| Threat ID | Status | Evidence |
| --------- | ------ | -------- |
| T-03-06-01 (DOS — SIGTERM ignored) | mitigated | `signal.NotifyContext` + ctx propagation through tick body; TestSchedulerGracefulShutdown asserts <5s exit |
| T-03-06-02 (Tampering — INTERVAL=0 busy-loops DB) | mitigated | `if d > 0` guard before assignment; falls back to `schedule.DefaultInterval` |
| T-03-06-03 (Info disclosure — DSN logged) | mitigated | `scheduler.started` payload contains only interval/shutdown_timeout/sensor_disable_after; DSN never appears in any slog field |
| T-03-06-04 (DOS — daemon crashes on transient DB error) | mitigated | Tick errors are slog.Error'd and the loop continues; only DSN-level errors at startup return from runScheduler |
| T-03-06-05 (EoP — operator runs scheduler with elevated DSN) | accept | Same trust model as Phase 2 worker; deployment-time concern; DB role grants limit DML to platform_app |

## Stub Tracking

None — production code is fully wired. No empty arrays, no placeholder text, no TODO/FIXME markers. `grep -n "TODO|FIXME|placeholder|coming soon|not available" cmd/platform/{scheduler,scheduler_test,main}.go` returns zero matches.

## Deviations from Plan

**One — test path was changed from hard-coded repo root to relative path.**

**[Rule 3 - Blocking issue] Test was hard-coded to `/home/developer/.kanpon/code/go/data-governance` repo root**

- **Found during:** Task 2 — test file template in plan
- **Issue:** Original plan template had `buildCmd.Dir = "/home/developer/.kanpon/code/go/data-governance"` which would fail in any worktree (e.g., `.claude/worktrees/agent-a5d884e73ebefca7f/`) because the build path doesn't exist there.
- **Fix:** Changed to `repoRoot, _ := filepath.Abs("../..")` (test runs from `cmd/platform/`, so `../..` resolves to the repo root regardless of physical location). Also added test output to error messages so debugging in CI is easier.
- **Files modified:** `cmd/platform/scheduler_test.go`
- **Commit:** `fc1f1ed`

This is a Rule 3 deviation (auto-fix blocking issue) — the original template would never have passed in a worktree environment. Plan intent (subprocess SIGTERM test) is fully preserved.

**Worktree branch base note:** This plan executes against the `bf35bfe` "executor merge" base because plans 03-04 and 03-05 (this plan's dependencies) live there. The current `master` HEAD (`943de17`) is a parallel chinese-translation branch that does not contain the schedule/sensor packages. Per the orchestrator's worktree_branch_check, the branch was created from `bf35bfe` directly (`git checkout -b worktree-agent-a5d884e73ebefca7f-03-06 bf35bfe`).

## Issues Encountered

None significant. The frozen interfaces from plans 03-04 and 03-05 were exactly as documented — no rename, no signature mismatch. Build is green from the first iteration.

## Self-Check: PASSED

**Created files exist:**
- FOUND: cmd/platform/scheduler.go
- FOUND: cmd/platform/scheduler_test.go

**Modified files exist:**
- FOUND: cmd/platform/main.go (case "scheduler": added)

**Commits exist:**
- FOUND: 02e3d19 (Task 1 — feat(03-06): scheduler subcommand)
- FOUND: fc1f1ed (Task 2 — test(03-06): TestSchedulerGracefulShutdown)

**Build & test pass:**
- `go build ./...` → green
- `go vet ./cmd/platform/...` → clean
- `DATABASE_URL=… go test ./cmd/platform/... -run TestSchedulerGracefulShutdown -count=1 -timeout 60s` → ok 3.985s
- `DATABASE_URL=… go test ./cmd/platform/... -count=1 -timeout 120s` → ok 4.007s (no other test regression)
- `DATABASE_URL=… go test ./internal/run/... -run TestClaimAtomicity50Goroutines -count=1 -timeout 60s` → ok (Phase 2 regression intact)
- `DATABASE_URL=… go test ./internal/schedule/... ./internal/sensor/... -count=1 -timeout 120s` → ok ok (Phase 3 plan 04 + 05 regression intact)

**Smoke test:**
- `DATABASE_URL="" ./platform scheduler` → exits non-zero with `scheduler: DATABASE_URL is required`
- `DATABASE_URL=… ./platform scheduler` → emits `scheduler.started` and `scheduler.shutdown` log lines around its lifecycle

## Acceptance Criteria — Grep Coverage

All 18 acceptance grep checks pass (13 from Task 1 + 5 from Task 2):

```
Task 1:
1.  case "scheduler": in cmd/platform/main.go: OK
2.  runScheduler() in cmd/platform/main.go: OK
3.  cmd/platform/scheduler.go file exists: OK
4.  func runScheduler in scheduler.go: OK
5.  schedule.FireOneSchedule referenced: OK
6.  ! schedule.Daemon{ — does NOT construct schedule.Daemon (W3 fix): OK
7.  ! schedule.Daemon.Run / sched.Daemon.Run — no call to hypothetical Daemon.Run: OK
8.  sensor.Daemon / sd.RunOnce: OK
9.  schedule.UpsertSchedules: OK
10. sensor.UpsertSensors: OK
11. signal.NotifyContext: OK
12. "scheduler.started" log: OK
13. "scheduler.shutdown" log: OK

Task 2:
14. cmd/platform/scheduler_test.go file exists: OK
15. func TestSchedulerGracefulShutdown: OK
16. syscall.SIGTERM: OK
17. "scheduler.started" string in test: OK
18. "scheduler.shutdown" string in test: OK
```

## Next Plan Readiness

- **Plan 03-07 (backfill CLI)** — fully unblocked. The `cmd/platform/main.go` switch will gain another `case "backfill":` block alongside this plan's `case "scheduler":`. No conflict because plans within Wave 4 do NOT overlap with Wave 3 line edits to main.go.
- **Phase 4 lineage** — the `scheduler.tick_completed` debug log + future `scheduler.fire_completed` event hook are convenient extension points for OpenTelemetry tracing. Out of scope for this plan but mentally bookmarked.
- **Operations** — `./platform scheduler` is now a runnable mode. A typical deployment would run a single scheduler pod (multi-replica works via SKIP LOCKED but is unnecessary at this volume) alongside N worker pods.

---

*Phase: 03-scheduling-sensors-partitions*
*Plan: 06 (scheduler subcommand)*
*Completed: 2026-05-08*
