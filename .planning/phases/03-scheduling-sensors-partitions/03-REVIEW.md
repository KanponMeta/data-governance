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

# Phase 3 代码审查报告

**审查时间：** 2026-05-08T09:35:09Z
**深度：** standard
**审查的文件：** 44 个源文件 + 测试文件（加 2 个迁移）
**状态：** issues_found

## 总结

Phase 3 在 Phase 2 运行执行引擎之上引入调度、传感器、分区和回填作为附加子系统。代码总体结构良好：SQL 完全参数化（无注入风险），`SELECT FOR UPDATE SKIP LOCKED` 一致用于多副本安全，panic 恢复已连接在 `safeEvaluate`（传感器）和 `safeMaterialize`（executor）中。测试覆盖主要并发不变量（50-goroutine 声明原子性、调度火灾上的 SKIP LOCKED、回填独立性）。

以下发现集中在两个领域：

1. **多副本守护进程启动期间的 TOCTOU 竞争** — `schedule.UpsertSchedules` 和 `sensor.upsertOneSensor` 都使用非事务性 `SELECT` → `INSERT/UPDATE` 模式，在 `(asset_name)` / `(asset_name, sensor_name)` 上没有 `UNIQUE` 约束。同时启动的两个调度器副本可能产生重复行，随后为同一调度器发出两次 `schedule.fired` 事件（和两个排队运行）。这是变更集中最令人担忧的正确性差距。
2. **烦扰/清晰度问题** — 接入 `runScheduler` 的 `shutdownCtx` 意图正确但从未使用；`computeNextAndDetectMiss` 为时钟偏移的 `lastFiredAt` 返回未来窗口，然后 `FireOneSchedule` 将触发它（与注释矛盾）；一些注释与观察到的行为不匹配。

未发现安全漏洞、SQL 注入向量、资源泄漏后果和 panic 转义路径。

---

## 警告

### WR-01: `UpsertSchedules` 竞争在多副本启动时产生重复行

**文件：** `internal/schedule/registry.go:44-73`
**问题：**
reconciler 运行 `SELECT id, cron_expr FROM schedules WHERE asset_name = $1 LIMIT 1`，然后在 `sql.ErrNoRows` 上进行无条件 `INSERT`。没有封闭事务，`schedules` 表只有非唯一 `index.Fields("asset_name")`（见 `internal/storage/ent/schema/schedule.go:48` 和迁移 `20260508121000_phase3_schedules_sensors_backfills.sql:30`）。当两个调度器副本同时执行 `runScheduler` 时，两个 `SELECT` 都观察到无行，两个 `INSERT` 都成功，表现在有两个相同资产的行。每个后续 tick 将并行声明它们并为每个 cron 窗口发出两个 `schedule.fired` 事件（和两个排队运行）——直接击败了 `cmd/platform/scheduler.go:28` 记录的多副本安全声明。

第 44 行的实现者注释承认缺乏 `ON CONFLICT` 路径，但选择的变通方法（SELECT-then-INSERT）重新引入了 `ON CONFLICT` 本应防止的竞争。

**修复：**
在后续迁移中在 `schedules.asset_name` 上添加 `UNIQUE` 约束，并切换到 `INSERT … ON CONFLICT (asset_name) DO UPDATE SET cron_expr = EXCLUDED.cron_expr, next_fire_at = EXCLUDED.next_fire_at, updated_at = NOW() WHERE schedules.cron_expr <> EXCLUDED.cron_expr`。作为无需模式更改的临时防护，将 SELECT/INSERT 包装在可序列化事务中：

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

注意，对不存在行的 `FOR UPDATE` 不会阻止竞争插入——只有 `SERIALIZABLE` 隔离会导致两个事务之一中止并出现可序列化失败，调用方可以重试。

---

### WR-02: `sensor.upsertOneSensor` 与 WR-01 有相同的的多副本启动竞争

**文件：** `internal/sensor/registry.go:39-69`
**问题：**
与 WR-01 相同的模式：`SELECT id, min_interval_seconds FROM sensors WHERE asset_name=$1 AND sensor_name=$2 LIMIT 1`，然后在 `sql.ErrNoRows` 上进行普通 `INSERT`。`sensors` 表在 `(asset_name, sensor_name)` 上只有非唯一复合索引（`internal/storage/ent/schema/sensor.go:57`，迁移第 53 行）。一起启动的两个副本将创建重复传感器行；`evaluate.go:115-124` 中的 SKIP-LOCKED 选择器然后为每个 tick 声明两者，而用户提供的 `Sense()` 将在 `MinInterval` 内运行两次——去重契约（D-07 第 1 层）只对 RunKey 去重，不对重复传感器行去重，直接破坏"至少每 MinInterval 评估一次"承诺为"至少每 MinInterval 2 次"。

**修复：**
与 WR-01 相同的补救：在 `sensors` 表上添加 `UNIQUE (asset_name, sensor_name)` 并切换到 `INSERT … ON CONFLICT (asset_name, sensor_name) DO UPDATE SET min_interval_seconds = EXCLUDED.min_interval_seconds, updated_at = NOW() WHERE sensors.min_interval_seconds <> EXCLUDED.min_interval_seconds`。

---

### WR-03: 调度器 `shutdownCtx` 已创建但从未使用 — graceful-shutdown plumbing 是一个 no-op

**文件：** `cmd/platform/scheduler.go:121-128`
**问题：**
```go
case <-ctx.Done():
    slog.Info("scheduler.shutdown", "reason", ctx.Err().Error())
    shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
    defer cancel()
    _ = shutdownCtx
    return nil
```
`shutdownCtx` 用配置的 `PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT` 创建，然后通过 `_ = shutdownCtx` 明确丢弃。延迟的 `cancel()` 在 `runScheduler` 返回时运行，远在 `return nil` 之后。净效果：环境变量没有行为影响，正在进行中的 tick 如果尚未观察 `ctx.Done()` 不会获得任何额外时间——其 `tickCtx`（== `ctx`）已经被取消。注释"允许 shutdownTimeout 完成任何进行中的 tick"因此不准确。

`TestSchedulerGracefulShutdown`（`scheduler_test.go:25-78`）今天通过是因为每行事务完成得足够快，在 SIGTERM 到达时没有什么在进行中，但测试不会 catch *要求* shutdown timeout 工作的回归。

**修复：** 要么 (a) 实际将 `shutdownCtx` 传递给最终的 `runOneTick(shutdownCtx)` 以在父 ctx 取消后排出进行中的工作，要么 (b) 删除死代码并更新注释以承认 tick 只是被允许在已取消的 ctx 上完成。选项 (a) 是更诚实的实现：

```go
case <-ctx.Done():
    slog.Info("scheduler.shutdown", "reason", ctx.Err().Error())
    shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
    defer cancel()
    runOneTick(shutdownCtx) // final drain on a fresh context
    return nil
```

注意，将 `runOneTick` 切换为仅在关闭时使用 `shutdownCtx` 意味着 `FireOneSchedule` 和 `sd.RunOnce` 将被允许完成它们当前活动的事务。

---

### WR-04: `computeNextAndDetectMiss` 为时钟偏移的 `lastFiredAt` 返回未来窗口，然后 `FireOneSchedule` 触发它

**文件：** `internal/schedule/missed.go:82-86` + `internal/schedule/fire.go:78-97`
**问题：**
`missed.go:48-49` 的文档注释说："如果 lastFiredAt 相对于 now 是未来的（时钟偏移或测试），表现为'尚未到期'——在 lastFiredAt 之后返回下一个未来窗口，missedCount=0。"函数确实返回了未来的 `candidate`，但 `FireOneSchedule` 在 INSERTing 运行行之前不检查返回的 `windowToFire` 相对于 `now`：

```go
windowToFire, missedCount := computeNextAndDetectMiss(sched, lf, now)
nextFire := sched.Next(now)
partitionKey := computeFirePartitionKey(reg, assetName, windowToFire)
// … INSERT runs(…) regardless of whether windowToFire > now
```

所以当时钟偏移情况 `last_fire_at` 以某种方式结束在将来（副本时钟漂移、手动 DB 编辑、恢复的快照）但 `next_fire_at <= now`（SELECT 谓词）时，仍会产生其语义窗口（`windowToFire`）在未来的 `runs` 行——而 `partition_key` 将从未来窗口派生，产生不直观的 `partition_key` 值（例如明天的日期）。

在实践中这是罕见的，因为 SELECT 谓词 `next_fire_at <= now` 和注释的不变式（"lastFiredAt > now"）很少同时出现，但注释目前具有误导性，代码对不一致性没有防御。

**修复：** 要么在语义上收紧谓词，通过返回 sentinel 并跳过火灾：
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
……或更新 `missed.go` 中的注释以反映实际行为（"返回下一个未来窗口，调用者将触发——如果不需要，调用者必须防护"）。

---

## 信息

### IN-01: 关于 `sensor.evaluated` defer 的注释说"总是发出"但路径仅限成功

**文件：** `internal/sensor/evaluate.go:162-175`
**问题：** 注释声称 `sensor.evaluated` *总是* 作为提交后审计追踪发出，但延迟调用在早期 `return handleError(...)` 路径之后注册。在 Sense 错误时，仅发出 `sensor.evaluation_failed`——从不是 `sensor.evaluated`。要么注释错了，要么审计目标未满足。（行为可能是正确的设计；只是注释过度承诺。）

**修复：** 更新注释为"为非错误路径发出 sensor.evaluated（成功：fired 或 no-fire）。错误路径通过 handleError 发出 sensor.evaluation_failed。"或者，如果审计追踪意图真的是"总是"，将 defer 提升到 `if evalErr != nil` 分支之上以覆盖错误。

---

### IN-02: `sensor.evaluated` 延迟事件在 `tx.Commit()` 失败时也会触发

**文件：** `internal/sensor/evaluate.go:163-175` (defer) + `evaluate.go:222, 246, 270, 320` (Commit sites)
**问题：** 延迟的 `events.Append(ctx=context.Background(), …, "fired": result.Fired)` 无条件地在函数返回时运行，包括当 `updateSensorOnNoFire`/`handleFired`/去重并提交路径返回 `tx.Commit()` 错误时。在这种情况下，DB 状态被回滚，但事件日志仍记录"fired=true/false"。不一致性很小（事件是 Phase 1 D-09 的可观察性），但在提交失败时审计日志将与规范 sensors 表分离。

**修复：** 通过命名返回捕获外层 `err` 并在非 nil 时跳过延迟发出：
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

### IN-03: `sensor.evaluate.evaluateOneSensor` 在循环每次迭代中调用 `a.Sensors()` 一次

**文件：** `internal/sensor/evaluate.go:144-149`
**问题：** `a.Sensors()` 返回防御*副本*（per `internal/asset/asset.go:116`），所以在循环内部调用会在每次迭代时分配切片。热路径有界限（每个资产 N 较小）但可以平凡修复。

**修复：**
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

### IN-04: `safeEvaluate` 超时语义：当 `MinInterval == 0` 时默认为 `DefaultMinInterval`（30s）— 但尊重 ctx 并在 100ms 内完成的 Sense 仍需支付 30s tick 预算

**文件：** `internal/sensor/evaluate.go:66-82`
**问题：** 今天这是可以的，因为超时只是一个截止日期天花板，但第 65 行的文档字符串说"timeout=0 被解释为 max(spec.MinInterval, DefaultMinInterval)"。用 `MinInterval: 5*time.Second` 编写的 SensorSpec 将被允许在超时触发前阻塞 tick 循环 30s（因为 `DefaultMinInterval` floor 获胜）。`min(MinInterval, DefaultMinInterval)` 可能是更保守的默认值。

**修复（澄清意图）：** 要么 (a) 在文档中说明超时 floor 是故意宽容的，这样低于默认间隔的用户不会遇到取消惊喜，要么 (b) 当 `spec.MinInterval > 0` 时将 floor 翻转为 `min(spec.MinInterval, DefaultMinInterval)`。选项 (a) 可能是正确的 given 威胁模型原理（Pitfall 3）——只是将文档与代码对齐。

---

### IN-05: `runBackfill` 参数顺序解析器错误分类以 `-` 开头的资产名称

**文件：** `cmd/platform/backfill.go:38-50`
**问题：** 以 `-` 开头的资产名称（例如假设的 `-experimental` 测试资产）被放入 `flagArgs`，`flag.Parse` 将拒绝它们。然后资产不在 `positional` 中，产生通用用法错误而不是更具诊断性的错误。边缘情况（大多数资产名称不会以破折号开头），但记录的"资产位置参数任意"支持在技术上不正确。

**修复：** 在文档注释中注意限制，或添加 `--asset` 标志作为规范形式，并将 positional 保留为方便。

---

### IN-06: `cmd/platform/main.go runHealthcheck` 从 defer 保护范围内调用 `os.Exit`

**文件：** `cmd/platform/main.go:151-168`
**问题：** `os.Exit(1)` 跳过延迟的 `cancel()`。在实践中这没问题（进程终止清理 OS 级计时器），但模式（defer + os.Exit 在同一代码路径上）是一个众所周知的 footgun，如果以后有人添加必须运行的延迟清理（例如刷新指标、释放文件锁）。在注释中明确标记函数，或重构为在最后退出：

**修复：**
```go
func runHealthcheck() {
    code := doHealthcheck() // does all work, returns exit code
    os.Exit(code)
}
```
`doHealthcheck` 中的 defer 然后在 `os.Exit` 之前正常运行。

---

_Reviewed: 2026-05-08T09:35:09Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_