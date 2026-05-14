---
phase: 3
plan: 01
title: Scheduler + Sensor + Partition Architecture
type: execute
wave: 1
depends_on:
  - 02-EXECUTOR-ENGINE
requirements:
  - ORCH-05
  - ORCH-06
  - ORCH-07
  - ORCH-08
files_created:
  - internal/schedule/daemon.go
  - internal/schedule/fire.go
  - internal/schedule/missed.go
  - internal/schedule/registry.go
  - internal/sensor/daemon.go
  - internal/sensor/evaluate.go
  - internal/sensor/registry.go
  - internal/partition/strategy.go
  - internal/partition/keygen.go
  - cmd/platform/scheduler.go
autonomous: true
must_haves:
  truths:
    - "Scheduler tick uses FOR UPDATE SKIP LOCKED to fire due schedules in multi-replica-safe way"
    - "Sensors share the scheduler tick loop but evaluate independently via SKIP LOCKED"
    - "Partition key is stored in runs.partition_key and read by executor to set io.PartitionKey"
    - "Backfill priority claim ordering: ORDER BY CASE priority WHEN 'backfill' THEN 2 WHEN 'normal' THEN 1 WHEN 'critical' THEN 0 END"
    - "MaterializeResult.Metadata receives sensor Payload and partition_key under well-known keys"
  artifacts:
    - path: "internal/schedule/daemon.go"
      contains: "func (d *Daemon) Run(ctx context.Context)"
    - path: "internal/sensor/evaluate.go"
      contains: "func safeEvaluate"
    - path: "internal/partition/strategy.go"
      contains: "DailyPartitions, WeeklyPartitions, MonthlyPartitions, CategoryPartitions"
    - path: "cmd/platform/scheduler.go"
      contains: "case 'scheduler':"
key_links:
  - from: "scheduler.Daemon"
    to: "run.ClaimNext"
    via: "scheduler enqueues runs (not through ClaimNext — direct INSERT)"
  - from: "sensor.Daemon"
    to: "schedule.Daemon"
    via: "shared tick loop"
  - from: "executor.Run"
    to: "io.PartitionKey"
    via: "executor reads partition_key from claimed run"
---

# Phase 3 计划 01：调度器 + 传感器 + 分区架构总结

**一句话描述：** 调度器守护进程（cron 触发 + 传感器评估）作为 `./platform scheduler` 子命令运行，共享 tick 循环和 SKIP LOCKED 原语；分区策略作为资产构建器上的可组合 `.Partitions()` 方法实现；回填优先级声明排序。

## 架构概览

### 调度器守护进程

调度器作为新的 `./platform scheduler` 子命令运行，与现有的 `server`/`worker`/`materialize` 并行（扩展 Phase 2 D-02 多模式模式）。调度器将运行入队到 `runs` 表；worker 执行它们。调度器关闭 ⇒ 没有新运行排队，但进行中的运行不受影响。

调度状态持久化是**惰性的**。新的 `schedules` 表保存 `(asset_name, cron_expr, last_fire_at, next_fire_at, paused_at, ...)`。每个 tick（默认 30s），调度器使用 `SELECT ... FOR UPDATE SKIP LOCKED` 选择 `next_fire_at <= NOW()` 的行，将运行行入队到 `runs`，更新 `last_fire_at` 并重新计算 `next_fire_at`。`runs` 表仅保存**可声明的运行**，从不是未来运行。

Cron 表达式解析使用 `robfig/cron/v3`（仅解析器 + `Next()` API — 不是其进程内 Cron 调度器）。调度器 tick 循环是自定义的：单个 Postgres 查询驱动所有调度触发。多副本安全来自保护运行声明的相同 `SELECT FOR UPDATE SKIP LOCKED` 原语（Phase 2 D-17）。**无需 leader 选举，无需 advisory locks。** River **未被引入** — `go.mod` 当前不包含 River，SKIP LOCKED 模型对于 ORCH-05/ORCH-06 验收标准已足够。

### 传感器模型

传感器运行在 cron **相同的 `scheduler` 子命令内**，共享 tick 循环和 SKIP LOCKED 原语。新 `sensors` 表镜像 `schedules`：`(asset_name, sensor_name, min_interval, last_evaluated_at, last_fired_at, last_run_key, cooldown_until, consecutive_failures, disabled_at, ...)`。每个 tick 选择需要评估的传感器，调用用户的 `Sense(ctx)`，有条件地入队运行。

用户面向的传感器契约：

```go
type SensorResult struct {
    Fired   bool
    RunKey  string         // 去重键 — 与上次 fire 相同 => 跳过
    Payload map[string]any // 附加到触发运行的 MaterializeResult.Metadata
}
type SensorFunc func(ctx context.Context) (SensorResult, error)
```

构建器：`asset.New("x").Sensor(asset.SensorSpec{Name:"...", MinInterval: 30*time.Second, Cooldown: 5*time.Minute, Sense: senseFn})`。`Payload` 成为未来 Phase 4 血缘钩子（与 Phase 2 D-04 `MaterializeResult.Metadata` 设计一致）。

两层去重 — RunKey 比较**和** cooldown 窗口（belt-and-suspenders 防止用户代码中的 bug）。如果 `RunKey == last_run_key` ⇒ 不入队。如果 `NOW() < cooldown_until` ⇒ 无论键如何都不入队。`Cooldown` 默认为 `0`（关闭，可选）。

`Sense()` 错误处理：记录结构化错误，在 `event_log` 中发出 `sensor.evaluation_failed` 事件，下一个 tick 重试（不创建失败的运行行——传感器错误是基础设施噪声，不是数据工作）。在 `consecutive_failures >= N`（可配置，默认为 60）后，设置 `sensors.disabled_at = NOW()` 并发出 `sensor.disabled` 事件。操作员必须手动重新启用。

### 分区模型

分区通过单个 `.Partitions(spec)` 方法声明，其中 `spec` 实现类型化的 `asset.PartitionStrategy` 接口。v1 策略：

- `asset.DailyPartitions{Start, TZ}`
- `asset.WeeklyPartitions{Start, TZ}` (ISO week)
- `asset.MonthlyPartitions{Start, TZ}`
- `asset.CategoryPartitions{Keys []string}`

每个资产最多一个策略。`MaterializeFunc` 通过 `io.PartitionKey() string` 在现有 `AssetIO` 上了解其分区（Phase 2 D-04 表面）。未来策略（动态/数据库驱动）通过添加新类型插入；构建器方法不变。

分区运行持久化为**现有 `runs` 表中的行**，带有新的可空 `partition_key VARCHAR(128)` 列。非分区运行将其留为 `NULL`。新的唯一约束 `(asset_name, partition_key)` 作用域为进行中状态（`queued`、`starting`、`running`），防止重复并发运行相同分区。

时间分区键是 **UTC 窗口起始 ISO-8601 字符串**：
- Daily: `2024-01-15`
- Weekly: `2024-W03`
- Monthly: `2024-01`
- Category: 用户提供的键字符串（例如 `us`、`eu`、`apac`）

分区规范上的 TZ 用于**cron 对齐和显示**，不用于键编码 — 存储保持 UTC 以避免 DST 陷阱。

### 回填（Phase 3 关键：优先级隔离）

三层回填隔离：

1. **优先级列** — `runs.priority` 枚举：`critical | normal | backfill`（默认为 `normal`）。存储为 VARCHAR(16) 带 CHECK 约束，镜像 Phase 2 `state` 列模式（D-17）。
2. **优先级然后 FIFO 声明** — `ClaimNext` 查询变为 `ORDER BY priority ASC, queued_at ASC`（优先级枚举在内部映射为整数；`critical=0, normal=1, backfill=2`）。正常运行总是在排队回填运行之前抢占，无需更改 SKIP LOCKED 原子性保证。
3. **并发 token 池资源标签** — 回填运行额外从现有 `concurrency_tokens` 表获取 `backfill` 加权资源（Phase 2 D-16）。每个资产 token weight 默认为 `1`；池容量 `max_concurrent_backfill` 默认为 `5`，可按资产和全局配置。

## 关键实现细节

### 调度器 tick 循环结构

```go
func (d *Daemon) Run(ctx context.Context) error {
    ticker := time.NewTicker(d.tickInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return nil
        case <-ticker.C:
            if err := d.runOneTick(ctx); err != nil {
                slog.Error("scheduler.tick", "error", err)
            }
        }
    }
}

func (d *Daemon) runOneTick(ctx context.Context) error {
    return d.withTx(ctx, func(tx *sql.Tx) error {
        // 1. Fire due schedules (SELECT FOR UPDATE SKIP LOCKED)
        if err := d.fireDueSchedules(ctx, tx); err != nil {
            return err
        }
        // 2. Evaluate due sensors
        if err := sd.evaluateDueSensors(ctx, tx); err != nil {
            return err
        }
        return nil
    })
}
```

### 分区键生成

```go
func computeFirePartitionKey(reg *asset.DefinitionRegistry, assetName string, window interface{}) string {
    a, err := reg.Get(assetName)
    if err != nil {
        return "" // 安全降级
    }
    for _, strat := range a.Partitions() {
        return strat.WindowKey(window) // Daily/Weekly/Monthly/Category
    }
    return "" // 非分区资产
}
```

### 声明排序（优先级 + FIFO）

```go
const claimSQL = `
    SELECT id, asset_name, state, queued_at, priority, partition_key, backfill_id
    FROM runs
    WHERE state = 'queued'
    ORDER BY CASE priority
        WHEN 'critical' THEN 0
        WHEN 'normal'   THEN 1
        WHEN 'backfill' THEN 2
        ELSE 1
    END ASC, queued_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
`
```

## 与 Phase 2 的集成点

- `internal/runtime/executor.go`：读取 claimed run 的 `partition_key` 并传递到 `AssetIO`（通过新方法 `io.PartitionKey()`）
- `internal/run/claim.go`：添加 `priority` 和 `partition_key` 到 ClaimedRun；更新 ORDER BY
- `internal/asset/io.go`：添加 `PartitionKey() string` 方法
- `internal/asset/builder.go`：添加 `.Schedule()`、`.Sensor()`、`.Partitions()` 链式方法
- `internal/asset/asset.go`：添加 `SensorSpec`、`PartitionStrategy` 接口和具体类型
- `migrations/`：添加 `runs.partition_key`、`runs.priority`、`runs.backfill_id` 列 + 唯一约束；创建 `schedules`、`sensors`、`backfills` 表
- `cmd/platform/main.go`：添加 `scheduler` 案例

## 已知权衡

- **回填立即创建所有分区运行行** — 一个 365 分区回填立即创建 365 个 `runs` 行，但进度可通过 `SELECT state, count(*) FROM runs WHERE backfill_id=$1 GROUP BY state` 轻松查询，重试正常工作，无需批处理协调 goroutine 来崩溃恢复。
- **传感器评估在共享 tick 循环中** — 如果 `Sense()` 函数阻塞，传感器评估会延迟后续传感器和调度火灾。设计决策：避免调度器和传感器之间的优先级争用。
- **无每调度 missed-fire 策略** — 当前仅实现 LatestOnly（跳过错过的火灾，仅触发最近一次）。Per-asset 覆盖是 v1.x 改进。
- **无 REST `/backfills` 端点** — CLI 是 v1 回填提交表面。REST 端点是 Phase 6 UI 依赖。

## 威胁模型注意事项

| 威胁 | 处理 | 备注 |
|------|------|------|
| T-03-01 | Mitigation | 传感器评估在事务内（tx.Commit() 失败导致 sensor.evaluated 延迟）；事件在 defer 中发出，与 DB 提交脱钩 |
| T-03-02 | Accept | 每个传感器最小间隔是软约束；平台不强制执行最小间隔，只检查是否到期 |
| T-03-03 | Mitigation | 分区键格式验证（ISO 8601 格式）；CategoryPartitions 验证键在允许列表中 |

## 验证检查清单

- [ ] 调度器 tick 循环运行，SIGTERM 干净关闭
- [ ] 传感器评估在共享 tick 中运行
- [ ] 分区键正确生成（每日/每周/每月/类别）
- [ ] 回填优先级声明排序正确
- [ ] MaterializeResult.Metadata 接收 sensor Payload 和 partition_key
- [ ] 无新的 go vet 或测试失败

## 开放钩子（供后续计划使用）

| 计划 | 消费的 | 用途 |
|------|---------|------|
| 03-02 (CLI 回填) | `backfill.Submit`、分区解析 | `./platform backfill` 子命令 |
| 03-03 (丢失窗口检测) | `computeNextAndDetectMiss` | 延迟重启后恢复错过的火灾 |
| 03-04 (传感器评估详情) | `safeEvaluate` | 评估超时和取消处理 |
| 03-05 (调度器持久化) | `schedules` 表 CRUD | 注册/取消暂停调度 |
| 03-06 (传感器注册) | `sensors` 表 CRUD | 注册/禁用传感器 |