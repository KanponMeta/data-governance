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

# Phase 3 Plan 06: 调度器子命令总结

**Phase 3 调度的可运行表面：现在 `./platform scheduler` 是一个面向操作员的子命令（与 `server` / `worker` / `materialize` 并行），引导存储 + 资产注册表 + 事件写入器，将进程内注册表协调到 `schedules` 和 `sensors` 表，并运行一个 tick 循环驱动 `schedule.FireOneSchedule`（排空）→ `sensor.Daemon.RunOnce`（排空）。SIGINT/SIGTERM 通过 `signal.NotifyContext` 触发优雅关闭；当前 tick 完成，守护进程干净退出。本计划交付 D-01（子命令模式）+ D-05（传感器共享调度器子命令），并消费来自 plans 03-04（`FireOneSchedule`、`UpsertSchedules`、`ErrNoDueSchedule`）和 03-05（`sensor.Daemon`、`UpsertSensors`、`AutoDisableThreshold`）的冻结接口。**

## 性能指标

- **耗时：** 约 3 分钟
- **开始时间：** 2026-05-08T09:00:16Z
- **完成时间：** 2026-05-08T09:03:04Z
- **任务数：** 2（均为自主完成；任务 1 实现；任务 2 集成测试）
- **创建文件数：** 2（cmd/platform/scheduler.go + cmd/platform/scheduler_test.go）
- **修改文件数：** 1（cmd/platform/main.go — 添加 switch case）
- **提交数：** 2

## 完成情况

- **`cmd/platform/main.go`** — 在现有的 `start` / `migrate` / `healthcheck` / `worker` / `materialize` switch 中添加了 `case "scheduler":` 块。镜像了 Phase 2 worker/materialize 子命令使用的精确模式。错误路径使用相同的 `slog.Error("platform.scheduler_failed", ...)` + `os.Exit(1)` 形状。
- **`cmd/platform/scheduler.go runScheduler()`** — 生产 tick 驱动程序：
  1. `signal.NotifyContext(ctx, SIGINT, SIGTERM)` — 操作员信号通过 ctx 传播到整个 tick 体
  2. DSN 检查 — `DATABASE_URL` 未设置时返回显式错误（与 worker.go 一致）
  3. `storage.NewPostgres` + `defer store.Close()` + `event.NewWriter` + `asset.Default()` — 与 worker.go 相同的引导模式
  4. 环境变量解析与安全回退 — `PLATFORM_SCHEDULER_INTERVAL` → `schedule.DefaultInterval`（30s）；`PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT` → 30s；`PLATFORM_SENSOR_DISABLE_AFTER` → `sensor.AutoDisableThreshold`（60）。无效值（零、负数、无法解析）回退到默认值 — T-03-06-02 缓解措施
  5. 启动时的 `schedule.UpsertSchedules` + `sensor.UpsertSensors` — 注册表同步是尽力而为的（失败时记录日志；下一次 tick 重试）
  6. 构造 `sensor.Daemon{Store, Registry, Events, DisableAfter}` — 跨所有 tick 重用的单一实例
  7. `slog.Info("scheduler.started", ...)` 包含 interval/shutdown_timeout/sensor_disable_after — DSN 有意不记录（T-03-06-03）
  8. Tick 体（`runOneTick`）：
     - Schedule 通道：`for { ... }` 调用 `schedule.FireOneSchedule` 直到 `ErrNoDueSchedule` 或任何其他错误（记录日志 + break）
     - Sensor 通道：每个 tick 一次 `sd.RunOnce(tickCtx)`（RunOnce 内部排空所有到期的传感器）
     - 调试日志：`scheduler.tick_completed` 包含持续时间
  9. 初始 tick 在启动时**立即**触发 — 处理漏窗恢复，无需 30s 预热延迟
  10. 循环体：`select { case <-time.After(interval + jitter): runOneTick(ctx); case <-ctx.Done(): scheduler.shutdown + return nil }`。每个迭代通过 `rand.Int64N(5000) * time.Millisecond` 重新计算抖动 — D-03 雷鸣群缓解措施
- **`cmd/platform/scheduler_test.go TestSchedulerGracefulShutdown`** — 子进程集成测试：
  - 通过 `go build -o ./platform ./cmd/platform` 将平台二进制文件构建到 `t.TempDir()`
  - 通过 `filepath.Abs("../..")` 解析 repo root，使测试可在 worktree 间移植（不是硬编码的 `/home/...`）
  - 使用 `PLATFORM_SCHEDULER_INTERVAL=100ms` + `PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT=2s` 生成 `./platform scheduler`，以在 1 秒内执行多个 tick 周期
  - 1 秒后向子进程发送 `syscall.SIGTERM`
  - 断言：
    - 进程在 5 秒内以代码 0 退出（优雅关闭 — T-03-06-01）
    - stdout 包含 `scheduler.started` 日志行
    - stdout 包含 `scheduler.shutdown` 日志行
  - 在 `DATABASE_URL` 未设置时干净跳过（镜像 `claim_test.go` 模式）

## 任务提交

| 任务 | 描述 | 提交 |
| ---- | ----------- | ------ |
| 1    | runScheduler() + main.go switch case | `02e3d19` |
| 2    | TestSchedulerGracefulShutdown 子进程 SIGTERM 集成测试 | `fc1f1ed` |

使用 `git log --oneline bf35bfe..HEAD` 验证。

## 最终公共表面

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

**消费的环境变量：**

| 环境变量 | 默认值 | 用途 |
| ------- | ------- | ------- |
| `DATABASE_URL` | （必需）| PostgreSQL DSN — 与 worker 子命令相同的形状 |
| `PLATFORM_SCHEDULER_INTERVAL` | `30s`（`schedule.DefaultInterval`）| Tick 节拍；每个 tick 添加 `[0, 5000]ms` 抖动 |
| `PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT` | `30s` | 预留用于 in-flight tick 完成（per-row tx 很短；实践中很少重要）|
| `PLATFORM_SENSOR_DISABLE_AFTER` | `60`（`sensor.AutoDisableThreshold`）| 传感器自动禁用前的连续失败阈值 |

**发出的日志行（slog）：**

| 级别 | 键 | 触发条件 |
| ----- | --- | ------- |
| Info  | `scheduler.started` | 进入时一次；payload：interval、shutdown_timeout、sensor_disable_after |
| Info  | `scheduler.shutdown` | 退出时一次；payload：reason（例如，`context canceled`）|
| Error | `scheduler.upsert_schedules_failed` | 尽力而为的启动协调失败 |
| Error | `scheduler.upsert_sensors_failed` | 尽力而为的启动协调失败 |
| Error | `scheduler.fire_failed` | Schedule 通道错误（除了 ErrNoDueSchedule）|
| Error | `scheduler.sensor_runonce_failed` | Sensor 通道错误（除了 context.Canceled）|
| Debug | `scheduler.tick_completed` | 每个 tick 后；payload：duration |

## 决策覆盖图

| 决策 | 覆盖者 | 测试名称 |
| -------- | ---------- | ------------ |
| **D-01**（scheduler 子命令模式，与 server/worker/materialize 并行）| `cmd/platform/main.go` 中的 `case "scheduler":` 块；`cmd/platform/scheduler.go` 中的 `runScheduler()` | TestSchedulerGracefulShutdown — 证明二进制文件实际启动、运行，并通过新的子命令干净退出 |
| **D-05**（传感器共享调度器子命令 goroutine — 单 tick 循环）| `runOneTick` 体在同一次迭代中调用 `schedule.FireOneSchedule`（排空）然后 `sd.RunOnce(tickCtx)` | TestSchedulerGracefulShutdown — 子进程发出 schedule.started 并在 5 秒内干净退出，证明组合循环尊重 ctx 取消 |

## W3 决议确认

**本计划不消费 `schedule.Daemon.Run`** — 该方法在 plan 03-04 中未导出（`run` 小写）。生产代码在 `runScheduler()` 中直接调用 `schedule.FireOneSchedule`。验收标准明确检查：

```
! grep -q 'schedule.Daemon{' cmd/platform/scheduler.go        → 通过（无构造）
! grep -q 'schedule.Daemon.Run|sched.Daemon.Run' cmd/platform/scheduler.go → 通过（无方法调用）
```

这是按设计进行的 — 见 plan 03-04 § "W3 决议" — 为了将生产驱动程序放在 `cmd/platform` 中，以便单 tick 循环可以按 D-05 交错 `sensor.Daemon.RunOnce`。

## 威胁表面覆盖

| 威胁 ID | 状态 | 证据 |
| --------- | ------ | -------- |
| T-03-06-01（DOS — SIGTERM 被忽略）| mitigated | `signal.NotifyContext` + ctx 传播到 tick 体；TestSchedulerGracefulShutdown 断言 <5s 退出 |
| T-03-06-02（篡改 — INTERVAL=0 忙循环 DB）| mitigated | `if d > 0` 守卫在赋值前；回退到 `schedule.DefaultInterval` |
| T-03-06-03（信息泄露 — DSN 被记录）| mitigated | `scheduler.started` payload 仅包含 interval/shutdown_timeout/sensor_disable_after；DSN 从不出现在任何 slog 字段中 |
| T-03-06-04（DOS — 守护进程在瞬态 DB 错误时崩溃）| mitigated | Tick 错误通过 slog.Error 记录，循环继续；只有启动时的 DSN 级错误从 runScheduler 返回 |
| T-03-06-05（EoP — 操作员使用提升的 DSN 运行调度器）| accept | 与 Phase 2 worker 相同的信任模型；部署时关注；DB 角色授权将 DML 限制为 platform_app |

## 占位符跟踪

无 — 生产代码已完全连接。没有空数组、没有占位符文本、没有 TODO/FIXME 标记。`grep -n "TODO|FIXME|placeholder|coming soon|not available" cmd/platform/{scheduler,scheduler_test,main}.go` 返回零匹配。

## 与计划的偏差

**一个 — 测试路径从硬编码的 repo root 改为相对路径。**

**[规则 3 - 阻塞问题] 测试硬编码为 `/home/developer/.kanpon/code/go/data-governance` repo root**

- **发现于：** 任务 2 — 计划中的测试文件模板
- **问题：** 原始计划模板有 `buildCmd.Dir = "/home/developer/.kanpon/code/go/data-governance"`，这在任何 worktree（例如 `.claude/worktrees/agent-a5d884e73ebefca7f/`）中都会失败，因为构建路径不存在。
- **修复：** 改为 `repoRoot, _ := filepath.Abs("../..")`（测试从 `cmd/platform/` 运行，所以 `../..` 解析为 repo root，无论物理位置如何）。还在错误消息中添加了测试输出，以便在 CI 中更容易调试。
- **修改的文件：** `cmd/platform/scheduler_test.go`
- **提交：** `fc1f1ed`

这是规则 3 偏差（自动修复阻塞问题）— 原始模板在 worktree 环境中永远不会通过。计划意图（子进程 SIGTERM 测试）完全保留。

**Worktree 分支基础说明：** 本计划针对 `bf35bfe` "executor merge" 基执行，因为 plans 03-04 和 03-05（本计划的依赖项）驻留在那里。当前的 `master` HEAD（`943de17`）是一个并行的中文翻译分支，不包含 schedule/sensor 包。根据 orchestrator 的 worktree_branch_check，分支是从 `bf35bfe` 直接创建的（`git checkout -b worktree-agent-a5d884e73ebefca7f-03-06 bf35bfe`）。

## 遇到的问题

无重大问题。来自 plans 03-04 和 03-05 的冻结接口与文档完全一致 — 无重命名，无签名不匹配。从第一次迭代开始构建就是绿色的。

## 自我检查：通过

**创建的文件存在：**
- 已找到：cmd/platform/scheduler.go
- 已找到：cmd/platform/scheduler_test.go

**修改的文件存在：**
- 已找到：cmd/platform/main.go（添加了 case "scheduler":）

**提交存在：**
- 已找到：02e3d19（任务 1 — feat(03-06): scheduler subcommand）
- 已找到：fc1f1ed（任务 2 — test(03-06): TestSchedulerGracefulShutdown）

**构建和测试通过：**
- `go build ./...` → 通过
- `go vet ./cmd/platform/...` → 干净
- `DATABASE_URL=… go test ./cmd/platform/... -run TestSchedulerGracefulShutdown -count=1 -timeout 60s` → 通过 3.985s
- `DATABASE_URL=… go test ./cmd/platform/... -count=1 -timeout 120s` → 通过 4.007s（无其他测试回归）
- `DATABASE_URL=… go test ./internal/run/... -run TestClaimAtomicity50Goroutines -count=1 -timeout 60s` → 通过（Phase 2 回归正常）
- `DATABASE_URL=… go test ./internal/schedule/... ./internal/sensor/... -count=1 -timeout 120s` → 通过 通过（Phase 3 plan 04 + 05 回归正常）

**冒烟测试：**
- `DATABASE_URL="" ./platform scheduler` → 以 `scheduler: DATABASE_URL is required` 非零退出
- `DATABASE_URL=… ./platform scheduler` → 在其生命周期周围发出 `scheduler.started` 和 `scheduler.shutdown` 日志行

## 验收标准 — Grep 覆盖

所有 18 个验收 grep 检查通过（任务 1 有 13 个 + 任务 2 有 5 个）：

```
任务 1：
1.  case "scheduler": in cmd/platform/main.go: OK
2.  runScheduler() in cmd/platform/main.go: OK
3.  cmd/platform/scheduler.go 文件存在: OK
4.  func runScheduler in scheduler.go: OK
5.  schedule.FireOneSchedule 引用: OK
6.  ! schedule.Daemon{ — 不构造 schedule.Daemon（W3 修复）: OK
7.  ! schedule.Daemon.Run / sched.Daemon.Run — 不调用假设的 Daemon.Run: OK
8.  sensor.Daemon / sd.RunOnce: OK
9.  schedule.UpsertSchedules: OK
10. sensor.UpsertSensors: OK
11. signal.NotifyContext: OK
12. "scheduler.started" 日志: OK
13. "scheduler.shutdown" 日志: OK

任务 2：
14. cmd/platform/scheduler_test.go 文件存在: OK
15. func TestSchedulerGracefulShutdown: OK
16. syscall.SIGTERM: OK
17. "scheduler.started" 字符串在测试中: OK
18. "scheduler.shutdown" 字符串在测试中: OK
```

## 下一计划就绪状态

- **Plan 03-07（backfill CLI）** — 完全解除阻塞。`cmd/platform/main.go` switch 将获得另一个 `case "backfill":` 块，与本计划的 `case "scheduler":` 并列。由于 Wave 4 内的计划不会与 Wave 3 对 main.go 的行编辑冲突，因此没有冲突。
- **Phase 4 lineage** — `scheduler.tick_completed` 调试日志 + 未来 `scheduler.fire_completed` 事件钩子是 OpenTelemetry tracing 的方便扩展点。超出本计划范围，但已做心理书签。
- **运营** — `./platform scheduler` 现在是一个可运行模式。典型部署将运行一个调度器 pod（通过 SKIP LOCKED 多副本工作，但在这种规模下不必要）以及 N 个 worker pod。

---

*Phase: 03-scheduling-sensors-partitions*
*Plan: 06 (scheduler subcommand)*
*Completed: 2026-05-08*
