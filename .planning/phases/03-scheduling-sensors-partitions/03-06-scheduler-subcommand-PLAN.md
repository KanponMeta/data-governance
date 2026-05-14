---
phase: 3
plan: 06
title: ./platform scheduler 子命令 — 在单个 tick 循环中连接 schedule.FireOneSchedule + sensor.Daemon.RunOnce，实现优雅关闭
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
    - "./platform scheduler 子命令存在于 cmd/platform/main.go 中，与 server/worker/materialize 案例并列"
    - "runScheduler() 引导 storage + asset registry + event writer，然后运行单个 tick 循环，调用 schedule.FireOneSchedule（耗尽直到 ErrNoDueSchedule）然后 sensor.Daemon.RunOnce — D-05 单循环架构"
    - "runScheduler 不调用 schedule.Daemon.Run（该方法在计划 03-04 中是未导出的）— 它拥有自己的 ticker 并直接调用 FireOneSchedule + sensor.RunOnce"
    - "SIGINT/SIGTERM 触发优雅关闭 — 当前 tick 完成，不再有新 tick，守护进程在配置的 GracefulShutdownTimeout（默认 30s）内退出"
    - "./platform scheduler 在入口处记录 scheduler.started，退出时记录 scheduler.shutdown（slog 结构化日志）"
    - "TestSchedulerGracefulShutdown 在子进程中生成 runScheduler，发送 SIGTERM，断言进程在 5s 内以代码 0 退出，并发送 scheduler.shutdown 日志行"
  artifacts:
    - path: "cmd/platform/scheduler.go"
      provides: "runScheduler() 入口点 — 拥有自己的 tick 循环，每 tick 驱动 schedule.FireOneSchedule + sensor.Daemon.RunOnce"
      contains: "func runScheduler"
    - path: "cmd/platform/main.go"
      provides: "switch 中的 scheduler case — 调用 runScheduler()"
      contains: "case \"scheduler\":"
    - path: "cmd/platform/scheduler_test.go"
      provides: "TestSchedulerGracefulShutdown 集成测试"
      contains: "TestSchedulerGracefulShutdown"
  key_links:
    - from: "cmd/platform/scheduler.go runScheduler"
      to: "internal/schedule.FireOneSchedule + internal/sensor.Daemon.RunOnce"
      via: "共享 tick 循环 — runScheduler 耗尽 FireOneSchedule 直到 ErrNoDueSchedule，然后调用 sensor.Daemon.RunOnce；schedule.Daemon 类型本身未被构造（其 `run` 驱动方法是未导出的，仅存在于包内测试中）"
      pattern: "schedule.FireOneSchedule|sensor.*RunOnce"
    - from: "cmd/platform/main.go switch"
      to: "cmd/platform/scheduler.go runScheduler"
      via: "case \"scheduler\": runScheduler()"
      pattern: "case \"scheduler\":"
---
<objective>
连接调度器子命令：`./platform scheduler` 启动一个进程，一起运行 `schedule` 和 `sensor` 评估通道，共享单个 tick 循环（D-05）。在 SIGINT/SIGTERM 时，守护进程完成当前 tick 并在 `GracefulShutdownTimeout`（默认 30s）内退出 — 没有进行中的调度器触发被在事务中途放弃；计划 03-04 的每行 tx 模型确保一致性。

这是唯一一个涉及 `cmd/platform/main.go` 的第 3 阶段计划。backfill 子命令（计划 03-07）层叠在其上，位于 Wave 4，以避免 Wave 3 内 main.go 的合并冲突。
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
本计划实现 D-01（调度器子命令模式，与 server/worker/materialize 并列）和 D-05（传感器共享调度器子命令 goroutine）。它桥接计划 03-04（schedule.FireOneSchedule + UpsertSchedules）和计划 03-05（sensor.Daemon.RunOnce + UpsertSensors）到一个可运行的二进制模式。

**为什么是 Wave 3：** 依赖于计划 03-04 和 03-05 — 两者都必须存在才能让 scheduler.go 导入它们。计划 03-04 和 03-05 彼此独立（Wave 2 并行），因此依赖图是 `04 || 05 → 06`。depends_on = [04, 05]。

**为什么本计划不依赖于计划 03-03：** 调度器子命令从不直接调用 `run.ClaimNext` — 它只 INSERT 排队的运行（通过计划 03-04/03-05 中的 FireOneSchedule 和 handleFired）。Workers（单独的 `./platform worker` 进程）消费队列。因此优先级 claim 更改（计划 03-03）不是依赖项。

**为什么本计划拥有自己的 tick 循环（W3 决议）：** 计划 03-04 的 `schedule.Daemon` 有一个**未导出**的 `run` 方法（仅供包内测试使用）。它不被生产代码消费。计划 03-04 从第一天就导出 `FireOneSchedule` — 本计划的 runScheduler 直接调用它。两层层叠的理由：

1. **单个 tick 循环驱动调度器触发和传感器评估（D-05）：** 用户面对决策说"传感器与 cron 共享调度器子命令 goroutine，共享 tick 循环。"如果我们对调度器 pass 使用假设的 `schedule.Daemon.Run`，对传感器 pass 使用平行的 `sensor.Daemon` ticker，我们将有两个竞争定时器 — 这对 D-05 的单循环意图不利。
2. **没有死的导出表面：** 生产代码直接调用 `FireOneSchedule`。同时导出 `Daemon.Run` 和 `FireOneSchedule` 会导致其中一个是死代码。计划 03-04 将 `Daemon.run` 保持为小写，因此本计划的 `runScheduler` 是唯一的生产驱动者。

具体来说，runScheduler 实现自己的 tick 循环：
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

**为什么 GracefulShutdownTimeout = 30s：** 调度器触发通常需要 <100ms（tx 覆盖两个 SQL 写入）。30s 是过度杀伤，但匹配第 2 阶段 worker 关闭期望。可通过 `PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT` 环境变量调整。

**消耗的冻结接口（无需重命名/重构 — 计划 03-04 从第一天就导出 FireOneSchedule）：**
- `internal/schedule.FireOneSchedule`、`schedule.UpsertSchedules`、`schedule.ErrNoDueSchedule`（计划 03-04 导出）
- `internal/sensor.Daemon`、`sensor.UpsertSensors`、`sensor.AutoDisableThreshold`（计划 03-05）
- `internal/storage.NewPostgres`（第 1 阶段）
- `internal/event.NewWriter`（第 1 阶段）
- `internal/asset.Default()`（第 2 阶段）
- `internal/connector.Registry`（第 2 阶段 — 需要因为 asset registry 有连接器依赖）

@.planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md
@.planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md
@.planning/phases/03-scheduling-sensors-partitions/03-VALIDATION.md
@cmd/platform/main.go
@cmd/platform/worker.go

<interfaces>
<!-- 来自计划 03-04 + 03-05。FireOneSchedule 在计划 03-04 中从第一天就导出 — 本计划无需重命名。 -->

```go
package schedule

const DefaultInterval = 30 * time.Second

type Daemon struct {
    Store    storage.Storage
    Registry *asset.DefinitionRegistry
    Events   event.Writer
    Interval time.Duration
}
// `run` 是未导出的 — 仅供 daemon_test.go 中的包内测试使用。
// 本计划不消费它。生产驱动者是下面的 FireOneSchedule。
// func (d *Daemon) run(ctx context.Context) error  // not used here

// 导出的 — 生产调用者（本计划的 runScheduler）使用这些：
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
  <name>Task 1: 创建 cmd/platform/scheduler.go runScheduler() — 引导 + 单 tick 循环驱动 FireOneSchedule + sensor.Daemon.RunOnce + 优雅关闭</name>
  <files>cmd/platform/scheduler.go, cmd/platform/main.go</files>
  <read_first>
    - cmd/platform/main.go（现有 switch 块 + runStart/runMaterialize/runWorker 模式）
    - cmd/platform/worker.go（引导辅助模式 + signal.NotifyContext 使用）
    - internal/schedule/daemon.go（Daemon 结构体 + DefaultInterval 常量 — 计划 03-04；注意：Daemon.run 是未导出的，不在这里使用）
    - internal/schedule/fire.go（FireOneSchedule + ErrNoDueSchedule — 计划 03-04，导出）
    - internal/sensor/daemon.go（Daemon 结构体 + RunOnce 方法 — 计划 03-05）
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 9 — CLI 子命令连接
  </read_first>
  <behavior>
    - cmd/platform/main.go switch 有 case "scheduler" 调用 runScheduler()
    - runScheduler 读取 DATABASE_URL，打开 storage，构建 asset registry（使用 asset.Default()），构造 event writer，在开始时调用 schedule.UpsertSchedules + sensor.UpsertSensors
    - runScheduler 运行内部 tick 循环，每隔 PLATFORM_SCHEDULER_INTERVAL（默认 30s）调用 schedule.FireOneSchedule（循环直到 ErrNoDueSchedule）然后 sensor.Daemon.RunOnce，加上 0..5s 抖动
    - SIGINT/SIGTERM 触发优雅关闭：当前 tick 完成，不再有新 tick，在 PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT（默认 30s）内返回
    - 在入口处记录"scheduler.started"（包含配置 interval、shutdown_timeout），退出时记录"scheduler.shutdown"
    - 在每次成功 tick 后以调试级别报告"scheduler.tick_completed"
    - 不构造或调用 schedule.Daemon — 该类型的循环驱动方法是未导出的（仅用于测试）
  </behavior>
  <action>
    1. 编辑 `cmd/platform/main.go`：
       a. 在现有 `case "materialize":` 块之后添加 `case "scheduler":` 块：
          ```go
          case "scheduler":
              if err := runScheduler(); err != nil {
                  slog.Error("platform.scheduler_failed", "error", err)
                  os.Exit(1)
              }
          ```
       b. 更新 `default:` 错误消息，将"scheduler"包含在已知命令列表中（或如果只使用 fmt 则省略）。现有格式已经打印通用"unknown command" — 除非帮助文本明确枚举，否则无需更改。
    2. 创建 `cmd/platform/scheduler.go`：
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
    3. 运行 `go build ./...` 确认。
  </action>
  <acceptance_criteria>
    - `grep -q 'case "scheduler":' cmd/platform/main.go`
    - `grep -q 'runScheduler()' cmd/platform/main.go`
    - 文件 `cmd/platform/scheduler.go` 存在
    - `grep -q 'func runScheduler' cmd/platform/scheduler.go`
    - `grep -q 'schedule.FireOneSchedule' cmd/platform/scheduler.go`
    - `! grep -q 'schedule.Daemon{' cmd/platform/scheduler.go`（不构造 schedule.Daemon — 确认 W3 修复；生产循环由 runScheduler 拥有）
    - `! grep -q 'schedule.Daemon.Run\|sched.Daemon.Run' cmd/platform/scheduler.go`（不调用假设的导出 Daemon.Run）
    - `grep -q 'sensor.Daemon' cmd/platform/scheduler.go` 或 `grep -q 'sd.RunOnce' cmd/platform/scheduler.go`
    - `grep -q 'schedule.UpsertSchedules' cmd/platform/scheduler.go`
    - `grep -q 'sensor.UpsertSensors' cmd/platform/scheduler.go`
    - `grep -q 'signal.NotifyContext' cmd/platform/scheduler.go`
    - `grep -q '"scheduler.started"' cmd/platform/scheduler.go`
    - `grep -q '"scheduler.shutdown"' cmd/platform/scheduler.go`
    - `go build ./...` 退出 0
    - 冒烟测试：`./platform scheduler`（无 DATABASE_URL）以非零退出并显示"DATABASE_URL is required"错误：`PLATFORM_HTTP_ADDR= ./platform scheduler 2>&1 | grep -q "DATABASE_URL is required"`
  </acceptance_criteria>
  <verify>
    <automated>cd /home/developer/.kanpon/code/go/data-governance && go build ./... && grep -c 'runScheduler\|FireOneSchedule\|UpsertSchedules\|UpsertSensors' cmd/platform/scheduler.go</automated>
  </verify>
  <done>./platform scheduler 子命令已连接；main.go switch 处理它；scheduler.go 引导 storage + registry + events；tick 循环驱动 schedule.FireOneSchedule（耗尽）+ sensor.Daemon.RunOnce；通过 signal.NotifyContext 实现优雅关闭；不依赖于假设的 schedule.Daemon.Run（W3 修复 — 该方法在计划 03-04 中是未导出的）。</done>
</task>

<task id="3.6.2" type="auto" tdd="true">
  <name>Task 2: 创建 TestSchedulerGracefulShutdown 集成测试，使用子进程调用</name>
  <files>cmd/platform/scheduler_test.go</files>
  <read_first>
    - cmd/platform/scheduler.go（刚创建 — 确认退出条件和日志行）
    - cmd/platform/worker.go（现有子命令的构建模式参考）
  </read_first>
  <behavior>
    - TestSchedulerGracefulShutdown 使用 `go build` 构建 platform 二进制文件，作为子进程运行 `./platform scheduler`（设置 DATABASE_URL），1s 后发送 SIGTERM，断言：
      - 进程在 5s 内以代码 0 退出
      - stdout 包含"scheduler.started"日志行
      - stdout 包含"scheduler.shutdown"日志行
    - 当 DATABASE_URL 未设置时测试跳过（镜像 claim_test.go 模式）
  </behavior>
  <action>
    1. 创建 `cmd/platform/scheduler_test.go`：
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
    2. 运行测试：
       ```bash
       DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable \
         go test ./cmd/platform/... -run TestSchedulerGracefulShutdown -count=1 -timeout 60s
       ```
       预期：通过。（测试内部的构建步骤重新编译 ./cmd/platform — 确保测试始终使用当前 scheduler.go。）
  </action>
  <acceptance_criteria>
    - 文件 `cmd/platform/scheduler_test.go` 存在
    - `grep -q 'func TestSchedulerGracefulShutdown' cmd/platform/scheduler_test.go`
    - `grep -q 'syscall.SIGTERM' cmd/platform/scheduler_test.go`
    - `grep -q '"scheduler.started"' cmd/platform/scheduler_test.go`
    - `grep -q '"scheduler.shutdown"' cmd/platform/scheduler_test.go`
    - `DATABASE_URL=... go test ./cmd/platform/... -run TestSchedulerGracefulShutdown -count=1 -timeout 60s` 退出 0
  </acceptance_criteria>
  <verify>
    <automated>DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./cmd/platform/... -run TestSchedulerGracefulShutdown -count=1 -timeout 60s</automated>
  </verify>
  <done>TestSchedulerGracefulShutdown 通过 — 证明 SIGTERM 在 5s 内触发优雅关闭，包含正确的日志行。</done>
</task>

</tasks>

<threat_model>
## 信任边界

| 边界 | 描述 |
|----------|-------------|
| OS signals → runScheduler | SIGINT/SIGTERM 是规范的关闭信号；signal.NotifyContext 是既定模式 |
| Env var config → runScheduler | DATABASE_URL、PLATFORM_SCHEDULER_INTERVAL 等是操作员控制的 — 无不可信来源 |
| 多个调度器副本 → schedules/sensors 表 | SKIP LOCKED 确保多副本安全（已由计划 03-04 和 03-05 强制执行）|

## STRIDE 威胁注册表

| 威胁 ID | 类别 | 组件 | 处理方式 | 缓解计划 |
|-----------|----------|-----------|-------------|-----------------|
| T-03-06-01 | 拒绝服务 | SIGTERM 被忽略 — 守护进程永不退出，阻止部署 | 缓解 | signal.NotifyContext + ctx 通过 tick 循环传播。TestSchedulerGracefulShutdown 强制 5s 退出。 |
| T-03-06-02 | 篡改 | 环境变量 PLATFORM_SCHEDULER_INTERVAL 设置为 0 → 忙循环占用 DB | 缓解 | runScheduler 在赋值前验证 `d > 0`；回退到 DefaultInterval（30s）。 |
| T-03-06-03 | 信息泄露 | DATABASE_URL 在 scheduler.started 时记录 | 缓解 | 我们只记录 `interval`、`shutdown_timeout`、`sensor_disable_after` — 不是 DSN。接受标准明确检查启动日志负载。 |
| T-03-06-04 | 拒绝服务 | runScheduler 因瞬态 DB 错误崩溃 | 缓解 | Tick 错误被记录（slog.Error）且循环继续；只有启动时的 DSN 级连接错误才从 runScheduler 返回。计划 03-04 的每行 tx 模型已经隔离每次触发失败。 |
| T-03-06-05 | 权限提升 | 操作员使用更高权限 DB 用户的 DSN 运行调度器 | 接受 | 与第 2 阶段 worker 相同的信任模型 — 操作员控制部署。DB 角色授权（第 1+2+3 阶段）将 DML 限制在 platform_app。 |
</threat_model>

<verification>
- `go build ./...` 通过；`./platform scheduler` 是一个可运行的子命令。
- `DATABASE_URL=... go test ./cmd/platform/... -count=1 -timeout 60s` 通过。
- 当针对本计划落地后的实时 DB 运行所有第 3 阶段测试时仍然通过。
- 计划 03-04 测试仍然通过 — `FireOneSchedule` 从第一天就被本计划消费（无需重命名重构）。
</verification>

<success_criteria>
- ./platform scheduler 子命令在 cmd/platform/main.go switch 中已连接。
- runScheduler 引导 storage + registry + events，在开始时运行 UpsertSchedules + UpsertSensors。
- 单 tick 循环驱动 schedule.FireOneSchedule（耗尽）+ sensor.Daemon.RunOnce — D-05 单循环架构。
- runScheduler 不构造 schedule.Daemon — 该类型的 `run` 驱动方法是未导出的（仅用于测试）per 计划 03-04。生产循环由 runScheduler 拥有。（W3 决议。）
- SIGINT/SIGTERM 在关闭超时内触发优雅关闭。
- TestSchedulerGracefulShutdown 通过（验证映射：TestSchedulerGracefulShutdown）。
- 构建和完整测试套件绿色。
</success_criteria>

<output>
完成后，创建 `.planning/phases/03-scheduling-sensors-partitions/03-06-SUMMARY.md`，记录：
- 最终调度器子命令表面（环境变量 + 行为）。
- Tick 循环序列（schedule.FireOneSchedule 耗尽 → sensor.Daemon.RunOnce）。
- 决策覆盖：D-01（子命令模式）、D-05（传感器共享调度器 — 单 tick 循环驱动两个通道）。
- 确认：本计划不消费 schedule.Daemon.Run（W3 修复；该方法在计划 03-04 中是未导出的）。
- TestSchedulerGracefulShutdown 通过 — 证明优雅关闭。
</output>
