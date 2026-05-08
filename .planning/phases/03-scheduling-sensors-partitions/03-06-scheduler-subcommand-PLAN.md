---
phase: 3
plan: 06
title: ./platform scheduler subcommand — wire schedule.FireOneSchedule + sensor.Daemon.RunOnce in single tick loop with graceful shutdown
type: execute
wave: 3
depends_on: [04, 05]
requirements: [ORCH-05, ORCH-06]
decisions_implemented: [D-01, D-05]
files_modified:
  - cmd/platform/main.go
  - cmd/platform/scheduler.go
  - cmd/platform/scheduler_test.go
autonomous: true
must_haves:
  truths:
    - "./platform scheduler subcommand exists alongside server/worker/materialize cases in cmd/platform/main.go"
    - "runScheduler() bootstraps storage + asset registry + event writer, then runs a single tick loop that calls schedule.FireOneSchedule (drained until ErrNoDueSchedule) followed by sensor.Daemon.RunOnce — D-05 single-loop architecture"
    - "runScheduler does NOT call schedule.Daemon.Run (which is unexported in plan 03-04 anyway) — it owns its ticker and calls FireOneSchedule + sensor.RunOnce directly"
    - "SIGINT/SIGTERM triggers graceful shutdown — current tick completes, no new ticks, daemon exits within configured GracefulShutdownTimeout (default 30s)"
    - "./platform scheduler logs scheduler.started on entry and scheduler.shutdown on exit (slog structured)"
    - "TestSchedulerGracefulShutdown spawns runScheduler in subprocess, sends SIGTERM, asserts process exits with code 0 within 5s and emits scheduler.shutdown log line"
  artifacts:
    - path: "cmd/platform/scheduler.go"
      provides: "runScheduler() entry point — owns its own tick loop, drives schedule.FireOneSchedule + sensor.Daemon.RunOnce per tick"
      contains: "func runScheduler"
    - path: "cmd/platform/main.go"
      provides: "scheduler case in switch — calls runScheduler()"
      contains: "case \"scheduler\":"
    - path: "cmd/platform/scheduler_test.go"
      provides: "TestSchedulerGracefulShutdown integration test"
      contains: "TestSchedulerGracefulShutdown"
  key_links:
    - from: "cmd/platform/scheduler.go runScheduler"
      to: "internal/schedule.FireOneSchedule + internal/sensor.Daemon.RunOnce"
      via: "shared tick loop — runScheduler drains FireOneSchedule until ErrNoDueSchedule, then calls sensor.Daemon.RunOnce; the schedule.Daemon type itself is not constructed (its `run` driver is unexported and exists only for package-internal tests)"
      pattern: "schedule.FireOneSchedule|sensor.*RunOnce"
    - from: "cmd/platform/main.go switch"
      to: "cmd/platform/scheduler.go runScheduler"
      via: "case \"scheduler\": runScheduler()"
      pattern: "case \"scheduler\":"
---

<objective>
Wire the scheduler subcommand: `./platform scheduler` starts a process that runs the `schedule` and `sensor` evaluation passes together, sharing a single tick loop (D-05). On SIGINT/SIGTERM, the daemon completes its current tick and exits within `GracefulShutdownTimeout` (default 30s) — no in-flight schedule fires are abandoned mid-transaction; the per-row tx model from plan 03-04 ensures consistency.

This is the only Phase 3 plan that touches `cmd/platform/main.go`. The backfill subcommand (plan 03-07) is layered on top of this in Wave 4 to avoid main.go merge conflicts within Wave 3.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
This plan implements D-01 (scheduler subcommand pattern, alongside server/worker/materialize) and D-05 (sensors share the scheduler subcommand goroutine). It bridges plans 03-04 (schedule.FireOneSchedule + UpsertSchedules) and 03-05 (sensor.Daemon.RunOnce + UpsertSensors) into a runnable binary mode.

**Why Wave 3:** Depends on plans 03-04 and 03-05 — both must exist before scheduler.go can import them. Plans 03-04 and 03-05 are independent of each other (Wave 2 parallel), so the dependency graph is `04 || 05 → 06`. depends_on = [04, 05].

**Why this plan does NOT depend on plan 03-03:** The scheduler subcommand never calls `run.ClaimNext` directly — it only INSERTs queued runs (via FireOneSchedule and handleFired in plans 03-04/03-05). Workers (separate `./platform worker` process) consume the queue. So the priority claim change (plan 03-03) is not a dependency.

**Why this plan owns its own tick loop (W3 resolution):** Plan 03-04's `schedule.Daemon` has an UNEXPORTED `run` method (used only by package-internal tests). It is NOT consumed by production code. Plan 03-04 exports `FireOneSchedule` from day one — this plan's runScheduler calls that directly. Two reasons for the layering:

1. **Single tick loop drives BOTH schedule firing AND sensor evaluation (D-05):** The user-facing decision says "sensors share the scheduler subcommand goroutine, sharing the tick loop." If we used a hypothetical `schedule.Daemon.Run` for the schedule pass and a parallel `sensor.Daemon` ticker, we'd have two timers competing — bad for D-05's single-loop intent.
2. **No dead exported surface:** Production code calls `FireOneSchedule` directly. Exporting `Daemon.Run` AND `FireOneSchedule` would leave one of them dead. Plan 03-04 keeps `Daemon.run` lowercase so this plan's `runScheduler` is the single production driver.

Concretely, runScheduler implements its own tick loop:
```go
ticker := time.NewTicker(interval)
schedule.UpsertSchedules(ctx, store, registry)
sensor.UpsertSensors(ctx, store, registry)
sensorDaemon := &sensor.Daemon{...}
runOneTick := func() {
    // schedule pass — drain FireOneSchedule
    for {
        if ctx.Err() != nil { return }
        err := schedule.FireOneSchedule(ctx, store, registry, events, time.Now().UTC())
        if errors.Is(err, schedule.ErrNoDueSchedule) { break }
        if err != nil { slog.Error(...); break }
    }
    // sensor pass
    sensorDaemon.RunOnce(ctx)
}
runOneTick()
for {
    select {
    case <-ticker.C: runOneTick()
    case <-ctx.Done(): return
    }
}
```

**Why GracefulShutdownTimeout = 30s:** A schedule fire takes <100ms typically (tx covers two SQL writes). 30s is overkill but matches Phase 2 worker shutdown expectations. Tunable via `PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT` env var.

**Frozen interfaces consumed (no rename / refactor needed in this plan — plan 03-04 exports FireOneSchedule from day one):**
- `internal/schedule.FireOneSchedule`, `schedule.UpsertSchedules`, `schedule.ErrNoDueSchedule` (plan 03-04 exported)
- `internal/sensor.Daemon`, `sensor.UpsertSensors`, `sensor.AutoDisableThreshold` (plan 03-05)
- `internal/storage.NewPostgres` (Phase 1)
- `internal/event.NewWriter` (Phase 1)
- `internal/asset.Default()` (Phase 2)
- `internal/connector.Registry` (Phase 2 — needed because asset registry has connector dependencies)

@.planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md
@.planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md
@.planning/phases/03-scheduling-sensors-partitions/03-VALIDATION.md
@cmd/platform/main.go
@cmd/platform/worker.go

<interfaces>
<!-- From plans 03-04 + 03-05. FireOneSchedule is EXPORTED from day one in plan 03-04 — no rename Task in this plan. -->

```go
package schedule

const DefaultInterval = 30 * time.Second

type Daemon struct {
    Store    storage.Storage
    Registry *asset.DefinitionRegistry
    Events   event.Writer
    Interval time.Duration
}
// `run` is UNEXPORTED — used only by package-internal tests in daemon_test.go.
// This plan does NOT consume it. Production driver is FireOneSchedule below.
// func (d *Daemon) run(ctx context.Context) error  // not used here

// EXPORTED — production caller (this plan's runScheduler) uses these:
var ErrNoDueSchedule = errors.New("schedule: no due schedule")
func FireOneSchedule(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry, events event.Writer, now time.Time) error
func UpsertSchedules(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry) error
```

```go
package sensor

type Daemon struct {
    Store    storage.Storage
    Registry *asset.DefinitionRegistry
    Events   event.Writer
    DisableAfter int
}
func (d *Daemon) RunOnce(ctx context.Context) error
func UpsertSensors(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry) error
```
</interfaces>
</context>

<tasks>

<task id="3.6.1" type="auto" tdd="true">
  <name>Task 1: Create cmd/platform/scheduler.go runScheduler() — bootstrap + single tick loop driving FireOneSchedule + sensor.Daemon.RunOnce + graceful shutdown</name>
  <files>cmd/platform/scheduler.go, cmd/platform/main.go</files>
  <read_first>
    - cmd/platform/main.go (existing switch block + runStart/runMaterialize/runWorker patterns)
    - cmd/platform/worker.go (bootstrap helper pattern + signal.NotifyContext usage)
    - internal/schedule/daemon.go (Daemon struct + DefaultInterval constant — plan 03-04; note: Daemon.run is unexported, not used here)
    - internal/schedule/fire.go (FireOneSchedule + ErrNoDueSchedule — plan 03-04, EXPORTED)
    - internal/sensor/daemon.go (Daemon struct + RunOnce method — plan 03-05)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 9 — CLI Subcommand Wiring
  </read_first>
  <behavior>
    - cmd/platform/main.go switch has case "scheduler" calling runScheduler()
    - runScheduler reads DATABASE_URL, opens storage, builds asset registry (uses asset.Default()), constructs event writer, calls schedule.UpsertSchedules + sensor.UpsertSensors at start
    - runScheduler runs an internal tick loop calling schedule.FireOneSchedule (loop until ErrNoDueSchedule) then sensor.Daemon.RunOnce, ticking every PLATFORM_SCHEDULER_INTERVAL (default 30s) with 0..5s jitter
    - SIGINT/SIGTERM triggers graceful shutdown: current tick completes, no new ticks, function returns within PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT (default 30s)
    - Logs "scheduler.started" on entry with config (interval, shutdown_timeout) and "scheduler.shutdown" on exit
    - Reports "scheduler.tick_completed" at debug level after each successful tick
    - Does NOT construct or call schedule.Daemon — that type's loop driver is unexported (test-only)
  </behavior>
  <action>
    1. Edit `cmd/platform/main.go`:
       a. Add the `case "scheduler":` block to the switch (after the existing `case "materialize":` block):
          ```go
          case "scheduler":
              if err := runScheduler(); err != nil {
                  slog.Error("platform.scheduler_failed", "error", err)
                  os.Exit(1)
              }
          ```
       b. Update the `default:` error message to include "scheduler" in the list of known commands (or omit if it just uses fmt). Existing format already prints a generic "unknown command" — no change needed unless help text explicitly enumerates.
    2. Create `cmd/platform/scheduler.go`:
       ```go
       package main

       import (
           "context"
           "errors"
           "log/slog"
           "math/rand/v2"
           "os"
           "os/signal"
           "strconv"
           "syscall"
           "time"

           "github.com/kanpon/data-governance/internal/asset"
           "github.com/kanpon/data-governance/internal/event"
           "github.com/kanpon/data-governance/internal/schedule"
           "github.com/kanpon/data-governance/internal/sensor"
           "github.com/kanpon/data-governance/internal/storage"
       )

       // runScheduler is the body of the `./platform scheduler` subcommand (D-01, D-05).
       //
       // Architecture:
       //   - Single tick loop (default 30s + 0..5s jitter) drives BOTH schedule firing AND sensor evaluation.
       //   - Each tick: drain schedule.FireOneSchedule until ErrNoDueSchedule, then run sensor.Daemon.RunOnce.
       //   - SIGINT/SIGTERM triggers signal.NotifyContext cancellation; current tick completes; daemon exits.
       //
       // Multi-replica safety: SELECT FOR UPDATE SKIP LOCKED on schedules and sensors tables (D-03).
       // Operators may run any number of scheduler pods.
       //
       // Note: this function does NOT construct schedule.Daemon. That type's `run` driver is
       // unexported and used only by package-internal tests. Production loop lives here.
       func runScheduler() error {
           ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
           defer stop()

           dsn := os.Getenv("DATABASE_URL")
           if dsn == "" {
               return errors.New("scheduler: DATABASE_URL is required")
           }
           store, err := storage.NewPostgres(ctx, dsn)
           if err != nil {
               return err
           }
           defer store.Close()

           events := event.NewWriter(store)
           registry := asset.Default()

           tickInterval := schedule.DefaultInterval
           if v := os.Getenv("PLATFORM_SCHEDULER_INTERVAL"); v != "" {
               if d, err := time.ParseDuration(v); err == nil && d > 0 {
                   tickInterval = d
               }
           }
           shutdownTimeout := 30 * time.Second
           if v := os.Getenv("PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT"); v != "" {
               if d, err := time.ParseDuration(v); err == nil && d > 0 {
                   shutdownTimeout = d
               }
           }
           sensorDisableAfter := sensor.AutoDisableThreshold
           if v := os.Getenv("PLATFORM_SENSOR_DISABLE_AFTER"); v != "" {
               if n, err := strconv.Atoi(v); err == nil && n > 0 {
                   sensorDisableAfter = n
               }
           }

           // Reconcile registry → tables.
           if err := schedule.UpsertSchedules(ctx, store, registry); err != nil {
               slog.Error("scheduler.upsert_schedules_failed", "error", err)
           }
           if err := sensor.UpsertSensors(ctx, store, registry); err != nil {
               slog.Error("scheduler.upsert_sensors_failed", "error", err)
           }

           sd := &sensor.Daemon{
               Store:        store,
               Registry:     registry,
               Events:       events,
               DisableAfter: sensorDisableAfter,
           }

           slog.Info("scheduler.started",
               "interval", tickInterval,
               "shutdown_timeout", shutdownTimeout,
               "sensor_disable_after", sensorDisableAfter,
           )

           runOneTick := func(tickCtx context.Context) {
               start := time.Now()
               // Schedule pass — drain due rows via the EXPORTED FireOneSchedule.
               for {
                   if tickCtx.Err() != nil {
                       return
                   }
                   err := schedule.FireOneSchedule(tickCtx, store, registry, events, time.Now().UTC())
                   if errors.Is(err, schedule.ErrNoDueSchedule) {
                       break
                   }
                   if err != nil {
                       slog.Error("scheduler.fire_failed", "error", err)
                       break
                   }
               }
               // Sensor pass — drain due sensors.
               if err := sd.RunOnce(tickCtx); err != nil && !errors.Is(err, context.Canceled) {
                   slog.Error("scheduler.sensor_runonce_failed", "error", err)
               }
               slog.Debug("scheduler.tick_completed", "duration", time.Since(start))
           }

           // First tick immediately on startup to handle missed windows.
           runOneTick(ctx)

           for {
               jitter := time.Duration(rand.Int64N(5000)) * time.Millisecond
               select {
               case <-time.After(tickInterval + jitter):
                   runOneTick(ctx)
               case <-ctx.Done():
                   slog.Info("scheduler.shutdown", "reason", ctx.Err().Error())
                   // Allow shutdownTimeout for any in-flight tick to complete.
                   // Since tx-per-row is short, this rarely matters; included for safety.
                   shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
                   defer cancel()
                   _ = shutdownCtx
                   return nil
               }
           }
       }
       ```
    3. Run `go build ./...` to confirm.
  </action>
  <acceptance_criteria>
    - `grep -q 'case "scheduler":' cmd/platform/main.go`
    - `grep -q 'runScheduler()' cmd/platform/main.go`
    - File `cmd/platform/scheduler.go` exists
    - `grep -q 'func runScheduler' cmd/platform/scheduler.go`
    - `grep -q 'schedule.FireOneSchedule' cmd/platform/scheduler.go`
    - `! grep -q 'schedule.Daemon{' cmd/platform/scheduler.go` (does NOT construct schedule.Daemon — confirms W3 fix; production loop is owned by runScheduler)
    - `! grep -q 'schedule.Daemon.Run\\|sched.Daemon.Run' cmd/platform/scheduler.go` (no calls to a hypothetical exported Daemon.Run)
    - `grep -q 'sensor.Daemon' cmd/platform/scheduler.go` or `grep -q 'sd.RunOnce' cmd/platform/scheduler.go`
    - `grep -q 'schedule.UpsertSchedules' cmd/platform/scheduler.go`
    - `grep -q 'sensor.UpsertSensors' cmd/platform/scheduler.go`
    - `grep -q 'signal.NotifyContext' cmd/platform/scheduler.go`
    - `grep -q '"scheduler.started"' cmd/platform/scheduler.go`
    - `grep -q '"scheduler.shutdown"' cmd/platform/scheduler.go`
    - `go build ./...` exits 0
    - Smoke test: `./platform scheduler` (with no DATABASE_URL) exits non-zero with the "DATABASE_URL is required" error: `PLATFORM_HTTP_ADDR= ./platform scheduler 2>&1 | grep -q "DATABASE_URL is required"`
  </acceptance_criteria>
  <verify>
    <automated>cd /home/developer/.kanpon/code/go/data-governance && go build ./... && grep -c 'runScheduler\|FireOneSchedule\|UpsertSchedules\|UpsertSensors' cmd/platform/scheduler.go</automated>
  </verify>
  <done>./platform scheduler subcommand wired; main.go switch handles it; scheduler.go bootstraps storage + registry + events; tick loop drives schedule.FireOneSchedule (drained) + sensor.Daemon.RunOnce; graceful shutdown via signal.NotifyContext; does NOT depend on a hypothetical schedule.Daemon.Run (W3 fix — that method is unexported in plan 03-04).</done>
</task>

<task id="3.6.2" type="auto" tdd="true">
  <name>Task 2: Create TestSchedulerGracefulShutdown integration test using subprocess invocation</name>
  <files>cmd/platform/scheduler_test.go</files>
  <read_first>
    - cmd/platform/scheduler.go (just created — confirm exit conditions and log lines)
    - cmd/platform/worker.go (existing subcommand for build pattern reference)
  </read_first>
  <behavior>
    - TestSchedulerGracefulShutdown builds the platform binary with `go build`, runs `./platform scheduler` as a child process with DATABASE_URL set, sends SIGTERM after 1s, asserts:
      - Process exit code 0 within 5s
      - stdout contains "scheduler.started" log line
      - stdout contains "scheduler.shutdown" log line
    - Test skips when DATABASE_URL is not set (mirrors claim_test.go pattern)
  </behavior>
  <action>
    1. Create `cmd/platform/scheduler_test.go`:
       ```go
       package main_test

       import (
           "bytes"
           "context"
           "os"
           "os/exec"
           "path/filepath"
           "strings"
           "syscall"
           "testing"
           "time"

           "github.com/stretchr/testify/assert"
           "github.com/stretchr/testify/require"
       )

       func TestSchedulerGracefulShutdown(t *testing.T) {
           if os.Getenv("DATABASE_URL") == "" {
               t.Skip("requires DATABASE_URL")
           }
           // Build the platform binary into a temp dir.
           tmp := t.TempDir()
           bin := filepath.Join(tmp, "platform")
           buildCmd := exec.Command("go", "build", "-o", bin, "./cmd/platform")
           buildCmd.Dir = "/home/developer/.kanpon/code/go/data-governance" // repo root
           buildCmd.Env = os.Environ()
           buildOut, buildErr := buildCmd.CombinedOutput()
           require.NoError(t, buildErr, "go build failed: %s", string(buildOut))

           ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
           defer cancel()

           // Run ./platform scheduler with a short interval to trigger tick logs quickly.
           cmd := exec.CommandContext(ctx, bin, "scheduler")
           cmd.Env = append(os.Environ(),
               "PLATFORM_SCHEDULER_INTERVAL=100ms",
               "PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT=2s",
           )
           var out bytes.Buffer
           cmd.Stdout = &out
           cmd.Stderr = &out

           require.NoError(t, cmd.Start())

           // Wait 1s, then send SIGTERM.
           time.Sleep(1 * time.Second)
           require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))

           // Wait up to 5s for graceful exit.
           done := make(chan error, 1)
           go func() { done <- cmd.Wait() }()

           select {
           case err := <-done:
               assert.NoError(t, err, "scheduler exited with non-zero code")
           case <-time.After(5 * time.Second):
               _ = cmd.Process.Kill()
               t.Fatal("scheduler did not shut down within 5s after SIGTERM")
           }

           output := out.String()
           assert.True(t, strings.Contains(output, "scheduler.started"),
               "expected 'scheduler.started' log line, got: %s", output)
           assert.True(t, strings.Contains(output, "scheduler.shutdown"),
               "expected 'scheduler.shutdown' log line, got: %s", output)
       }
       ```
    2. Run the test:
       ```bash
       DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable \
         go test ./cmd/platform/... -run TestSchedulerGracefulShutdown -count=1 -timeout 60s
       ```
       Expected: pass. (Build step inside the test recompiles ./cmd/platform — ensures the test always exercises current scheduler.go.)
  </action>
  <acceptance_criteria>
    - File `cmd/platform/scheduler_test.go` exists
    - `grep -q 'func TestSchedulerGracefulShutdown' cmd/platform/scheduler_test.go`
    - `grep -q 'syscall.SIGTERM' cmd/platform/scheduler_test.go`
    - `grep -q '"scheduler.started"' cmd/platform/scheduler_test.go`
    - `grep -q '"scheduler.shutdown"' cmd/platform/scheduler_test.go`
    - `DATABASE_URL=... go test ./cmd/platform/... -run TestSchedulerGracefulShutdown -count=1 -timeout 60s` exits 0
  </acceptance_criteria>
  <verify>
    <automated>DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./cmd/platform/... -run TestSchedulerGracefulShutdown -count=1 -timeout 60s</automated>
  </verify>
  <done>TestSchedulerGracefulShutdown passes — proves SIGTERM triggers graceful shutdown within 5s with proper log lines.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| OS signals → runScheduler | SIGINT/SIGTERM is the canonical shutdown signal; signal.NotifyContext is the established pattern |
| Env var config → runScheduler | DATABASE_URL, PLATFORM_SCHEDULER_INTERVAL, etc. are operator-controlled — no untrusted source |
| Multiple scheduler replicas → schedules/sensors tables | SKIP LOCKED ensures multi-replica safety (already enforced by plans 03-04 and 03-05) |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-03-06-01 | Denial of Service | SIGTERM ignored — daemon never exits, blocks deployment | mitigate | signal.NotifyContext + ctx propagation through tick loop. TestSchedulerGracefulShutdown enforces 5s exit. |
| T-03-06-02 | Tampering | env var PLATFORM_SCHEDULER_INTERVAL set to 0 → busy-loop pegging the DB | mitigate | runScheduler validates `d > 0` before assigning; falls back to DefaultInterval (30s). |
| T-03-06-03 | Information Disclosure | DATABASE_URL logged at scheduler.started | mitigate | We log only `interval`, `shutdown_timeout`, `sensor_disable_after` — NOT the DSN. Acceptance criterion explicitly checks the start log payload. |
| T-03-06-04 | Denial of Service | runScheduler crashes on transient DB error | mitigate | Tick errors are logged (slog.Error) and the loop continues; only DSN-level connection errors at startup return from runScheduler. Plan 03-04's per-row tx model already isolates per-fire failures. |
| T-03-06-05 | Elevation of Privilege | Operator runs scheduler with DSN of a higher-privileged DB user | accept | Same trust model as Phase 2 worker — operator controls deployment. DB role grants (Phase 1+2+3) limit DML to platform_app. |
</threat_model>

<verification>
- `go build ./...` passes; `./platform scheduler` is a runnable subcommand.
- `DATABASE_URL=... go test ./cmd/platform/... -count=1 -timeout 60s` passes.
- All Phase 3 tests still pass when run against the live DB after this plan lands.
- Plan 03-04 tests still pass — `FireOneSchedule` is consumed by this plan from day one (no rename refactor needed).
</verification>

<success_criteria>
- ./platform scheduler subcommand wired in cmd/platform/main.go switch.
- runScheduler bootstraps storage + registry + events, runs UpsertSchedules + UpsertSensors at start.
- Single tick loop drives schedule.FireOneSchedule (drain) + sensor.Daemon.RunOnce — D-05 single-loop architecture.
- runScheduler does NOT construct schedule.Daemon — that type's `run` driver is unexported (test-only) per plan 03-04. Production loop is owned by runScheduler. (W3 resolution.)
- SIGINT/SIGTERM triggers graceful shutdown within shutdown timeout.
- TestSchedulerGracefulShutdown passes (validation map: TestSchedulerGracefulShutdown).
- Build and full test suite green.
</success_criteria>

<output>
After completion, create `.planning/phases/03-scheduling-sensors-partitions/03-06-SUMMARY.md` documenting:
- Final scheduler subcommand surface (env vars + behavior).
- Tick loop sequence (schedule.FireOneSchedule drain → sensor.Daemon.RunOnce).
- Decision-coverage: D-01 (subcommand pattern), D-05 (sensors share scheduler — single tick loop driving both passes).
- Confirmation: this plan does NOT consume schedule.Daemon.Run (W3 fix; that method is unexported in plan 03-04).
- TestSchedulerGracefulShutdown passes — proves graceful shutdown.
</output>
