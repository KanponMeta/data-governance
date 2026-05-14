---
phase: 03-scheduling-sensors-partitions
plan: 05
title: 传感器评估器 — 安全的 Sense() 调用 + RunKey/冷却间隔双层去重 + N 次连续失败后自动禁用
type: execute
wave: 2
depends_on: [01, 02]
requirements: [ORCH-06]
decisions_implemented: [D-05, D-06, D-07, D-08, D-12]
files_modified:
  - internal/sensor/daemon.go
  - internal/sensor/daemon_test.go
  - internal/sensor/evaluate.go
  - internal/sensor/evaluate_test.go
  - internal/sensor/registry.go
autonomous: true
must_haves:
  truths:
    - "sensor.Daemon.RunOnce(ctx) 是入口点 — 通过 SELECT FOR UPDATE SKIP LOCKED 选择待评估的传感器行，在一次事务中评估每个传感器并更新状态"
    - "safeEvaluate 使用 context.WithTimeout（默认 = SensorSpec.MinInterval）和 defer recover() 包装 SensorFunc.Sense(ctx) — panic 转换为错误，不会向上传播"
    - "当 Fired=true 时：如果 RunKey == sensors.last_run_key 则发送 sensor.dedup_skipped（不入队）；否则如果 NOW() < cooldown_until 则发送 sensor.cooldown_skipped（不入队）；否则原子性地 INSERT runs 行并 UPDATE last_run_key/last_fired_at/cooldown_until"
    - "当 Sense() 错误或 panic：发送 sensor.evaluation_failed 事件，递增 consecutive_failures；如果 consecutive_failures+1 >= AutoDisableThreshold（默认 60）则设置 disabled_at 并发送 sensor.disabled — 首次成功评估后自动重置为 0"
    - "带有 .Partitions(daily) 的传感器将 runs.partition_key = SensorResult.RunKey（如果能解析为日键），否则为 CurrentDailyKey(now, 24h)（开放问题 1 默认值）"
    - "UpsertSensors 将注册表的 asset.Sensors() 同步到 sensors 表（跨重启幂等）"
    - "Tick 从 sensors 中选择 WHERE disabled_at IS NULL AND (last_evaluated_at IS NULL OR last_evaluated_at + min_interval_seconds * interval '1 second' <= NOW())"
  artifacts:
    - path: "internal/sensor/daemon.go"
      provides: "Daemon.RunOnce(ctx) — 传感器 tick 驱动，选择待评估行 + 评估 + 更新状态"
      contains: "type Daemon struct"
    - path: "internal/sensor/evaluate.go"
      provides: "safeEvaluate（超时 + panic 恢复）+ handleResult（去重 + 冷却间隔 + 入队）+ handleError（失败计数 + 自动禁用）"
      contains: "func safeEvaluate"
    - path: "internal/sensor/registry.go"
      provides: "UpsertSensors(ctx, registry): 将 asset.DefinitionRegistry SensorSpec 同步到 sensors 表"
      contains: "func UpsertSensors"
  key_links:
    - from: "internal/sensor.safeEvaluate"
      to: "用户提供的 asset.SensorFunc"
      via: "context.WithTimeout(ctx, spec.MinInterval) + defer recover() 包装器"
      pattern: "context.WithTimeout.*defer.*recover"
    - from: "internal/sensor.handleResult"
      to: "PostgreSQL runs + sensors 表"
      via: "INSERT runs (priority=normal, trigger=sensor, partition_key=...) + UPDATE sensors (last_run_key, last_fired_at, cooldown_until) — 同一事务"
      pattern: "INSERT INTO runs.*trigger.*sensor"
    - from: "internal/sensor.handleError"
      to: "sensors.consecutive_failures + sensors.disabled_at"
      via: "UPDATE sensors SET consecutive_failures = consecutive_failures + 1, disabled_at = CASE WHEN consecutive_failures + 1 >= $threshold THEN NOW() ELSE disabled_at END"
      pattern: "consecutive_failures \\+ 1 >= "
---

<objective>
实现传感器评估框架：`Daemon.RunOnce(ctx)` 驱动通过 SELECT FOR UPDATE SKIP LOCKED 选择待评估的传感器，调用用户提供的 `Sense(ctx)` 函数（带超时上下文和 panic 恢复），应用双层去重（RunKey ⇒ 跳过，冷却间隔 ⇒ 跳过），然后要么入队一个 runs 行，要么通过事件记录去重决策。当 Sense() 错误/panic 时：递增 `consecutive_failures` 并在达到阈值时自动禁用（D-08）。

与计划 03-04 一样，这提供的是 *internal* 包；将两个守护进程连接起来的 `./platform scheduler` 子命令是计划 03-06。

Daemon 暴露的是 `RunOnce(ctx)` 而不是 `Run(ctx)`，因为计划 03-06 的调度器子命令将从调度器 tick 循环内部调用 `RunOnce`，共享 goroutine — 不需要单独的传感器 goroutine 池（D-05："传感器在与 cron 相同的调度器子命令中运行，共享 tick 循环"）。
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
本计划实现 D-05（传感器共享调度器子命令）、D-06（SensorResult 契约）、D-07（双层去重：RunKey + 冷却间隔）、D-08（Sense() 错误 → 日志 + 重试；N 次失败自动禁用）、D-12（传感器×分区组合）。

**为什么是 Wave 2：** 依赖于计划 03-01（sensors 表）和计划 03-02（asset.SensorSpec、asset.SensorResult、asset.SensorFunc、partition.PartitionStrategy、partition.CurrentDailyKey）。在两者之前都无法运行。depends_on = [01, 02]。

**为什么与 03-03 和 03-04 并行在 Wave 2：** 本计划涉及 `internal/sensor/*`。计划 03-03 和 03-04 分别涉及 `internal/run/*` 和 `internal/schedule/*`。零文件重叠。

**为什么使用 RunOnce 而不是 Run（per-tick 驱动，而不是独立循环）：** D-05 规定传感器"在与 cron 相同的调度器子命令中运行，共享 tick 循环"。调度器 Daemon 的 tick 循环（计划 03-04）将在 `schedule.Daemon.tick(ctx)` 后调用 `sensor.Daemon.RunOnce(ctx)`。这避免了第二个定时器并共享 SKIP LOCKED 安全原语。**但是：** RunOnce 应该也可以独立测试，因此有公共签名。

**为什么超时 = MinInterval（陷阱 3）：** 传感器的 `Sense()` 可能在外部 HTTP 调用时挂起而没有超时。我们用 `context.WithTimeout(ctx, spec.MinInterval)` 来限制它，因为用户明确声明"我希望至少每隔 MinInterval 评估一次"——超过 MinInterval 的 Sense() 调用已经违反了用户的契约。记录在 evaluate.go 注释中。

**为什么 panic 恢复是强制性的（陷阱 2）：** 用户提供的 Sense() 代码中的 panic 会杀死 goroutine；没有 recover，调度器子命令会静默停止评估传感器。`defer recover()` 将 panic 转换为类型化错误，走相同的 handleError 路径。测试 `TestSensorPanicRecovery` 验证这一点。

**为什么双层去重（D-07）：** 仅 RunKey 在用户代码有 bug（为真正不同的事件返回相同键）时会失败。仅冷却间隔在用户代码故意嘈杂（冷却间隔内的合法相同键事件）时会失败。双重保险：两层都必须允许触发。先检查 RunKey（廉价的字符串比较）。然后检查冷却间隔（时间比较）。

**为什么成功时自动重置 consecutive_failures（Claude 的判断 + 03-RESEARCH.md A5）：** 根据 CONTEXT.md "除非测试数据另有说明，否则成功后自动重置"。测试夹具故意失败 5 次，成功 1 次，再失败 5 次 — 确认每个"失败运行"是独立的（不会累积到禁用）。如果我们要求 N 次成功才重置，一个不稳定的传感器在自我恢复后将永远不会自动禁用。自动重置是更安全的默认值。

**为什么传感器×分区组合使用 RunKey 作为分区键（D-12 + 开放问题 1）：** 当 SensorResult.RunKey 被设置且资产有 .Partitions 时，runs.partition_key 是 RunKey **如果它能被解析为该策略的有效键**。否则（或 RunKey 为空），回退到 `partition.CurrentDailyKey(now, 24h)` 用于日策略。这允许用户明确地针对每个传感器触发的特定分区。根据 03-RESEARCH.md 开放问题 1 将此作为规划决策固定："使用 CurrentKey(strategy, now)（默认为前一个窗口）。将此记录为 SensorSpec.DefaultPartitionOffset。"在这里使用更简单的约定：显式 RunKey 有效时优先；否则回退到当前。

**为什么传感器不需要单独的数据库咨询锁（与调度器相比）：** 相同的 SKIP LOCKED 原语在 `sensors` 表上工作得很好。每个传感器行在其评估事务期间被锁定（最多可达 MinInterval）。不同传感器不会被 SKIP LOCKED 相互阻塞。

**使用的冻结接口：**
- `internal/asset.DefinitionRegistry`、`Asset.Sensors()`、`Asset.Partitions()`（计划 03-02）
- `internal/asset.SensorSpec`、`SensorResult`、`SensorFunc`（计划 03-02）
- `internal/partition.*`（计划 03-02）
- `internal/event.Writer.Append`、`EventTypeSensor*` 常量（计划 03-01）
- `internal/storage.Storage.DB()`（第 1 阶段）

**生成的冻结接口（被计划 03-06 调度器子命令使用）：**
- `sensor.Daemon` 结构体 + `RunOnce(ctx)` 方法
- `sensor.UpsertSensors(ctx, store, registry)` 函数
- `sensor.AutoDisableThreshold` 常量（默认 60）

@.planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md
@.planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md
@.planning/phases/03-scheduling-sensors-partitions/03-VALIDATION.md
@internal/asset/asset.go
@internal/asset/registry.go
@internal/event/types.go

<interfaces>
<!-- 计划 03-01 + 03-02 暴露本计划使用的接口。 -->

来自计划 03-01（sensors 表 — 来自 ent schema 的逐字记录）：
```
id UUID, asset_name VARCHAR(256), sensor_name VARCHAR(128), min_interval_seconds INT8 DEFAULT 30,
last_evaluated_at TIMESTAMPTZ NULL, last_fired_at TIMESTAMPTZ NULL, last_run_key VARCHAR(256) NULL,
cooldown_until TIMESTAMPTZ NULL, consecutive_failures INT DEFAULT 0, disabled_at TIMESTAMPTZ NULL,
created_at, updated_at
```

来自计划 03-01（事件）：
```go
EventTypeSensorEvaluated        = "sensor.evaluated"
EventTypeSensorFired            = "sensor.fired"
EventTypeSensorEvaluationFailed = "sensor.evaluation_failed"
EventTypeSensorDisabled         = "sensor.disabled"
EventTypeSensorCooldownSkipped  = "sensor.cooldown_skipped"
EventTypeSensorDedupSkipped     = "sensor.dedup_skipped"
```

来自计划 03-02（asset SDK）：
```go
type SensorSpec struct {
    Name        string
    MinInterval time.Duration  // 0 → 在评估时默认为 30s
    Cooldown    time.Duration  // 0 → 无冷却间隔
    Sense       SensorFunc
}
type SensorResult struct {
    Fired   bool
    RunKey  string
    Payload map[string]any
}
type SensorFunc func(ctx context.Context) (SensorResult, error)
func (a *Asset) Sensors() []SensorSpec
func (a *Asset) Partitions() partition.PartitionStrategy
```

本计划生成：
```go
package sensor

const DefaultMinInterval = 30 * time.Second
const AutoDisableThreshold = 60  // D-08 默认值

type Daemon struct {
    Store    storage.Storage
    Registry *asset.DefinitionRegistry
    Events   event.Writer
    // 自动禁用阈值；0 → DefaultAutoDisable
    DisableAfter int
}

// RunOnce 评估所有当前待评估的传感器。当没有更多行待处理时返回。
// 设计为从调度器子命令的 tick 循环调用。
func (d *Daemon) RunOnce(ctx context.Context) error

func UpsertSensors(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry) error

// safeEvaluate 为可测试性导出 — 生产调用者使用 evaluateOneSensor。
func safeEvaluate(ctx context.Context, spec asset.SensorSpec, timeout time.Duration) (asset.SensorResult, error)
```
</interfaces>
</context>

<tasks>

<task id="3.5.1" type="auto" tdd="true">
  <name>Task 1: 创建 internal/sensor/evaluate.go — safeEvaluate（超时 + panic 恢复）+ handleResult（去重 + 冷却间隔 + 入队）+ handleError（失败计数 + 自动禁用）</name>
  <files>internal/sensor/evaluate.go, internal/sensor/evaluate_test.go</files>
  <read_first>
    - internal/asset/asset.go（SensorSpec, SensorResult, SensorFunc — 计划 03-02）
    - internal/event/types.go（EventTypeSensor* 常量 — 计划 03-01）
    - internal/partition/keygen.go（CurrentDailyKey, DailyKey — 计划 03-02）
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 5 — 传感器评估框架（逐字）
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pitfall 2, Pitfall 3 — panic 恢复 + 超时
    - .planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md § D-06, D-07, D-08
  </read_first>
  <behavior>
    - safeEvaluate(ctx, spec, timeout) 以线程安全方式调用 spec.Sense，使用 context.WithTimeout(ctx, timeout)；spec.Sense 中的 panic 通过 defer recover() 转换为错误
    - safeEvaluate 超时默认值：当调用者传递 0 时为 max(spec.MinInterval, DefaultMinInterval)
    - handleResult 当 Fired=true 时：
      - 如果 sensorRow.LastRunKey == result.RunKey 且 result.RunKey != "" → 发送 sensor.dedup_skipped，返回（无 INSERT）
      - 否则如果 sensorRow.CooldownUntil != nil 且 NOW() < *sensorRow.CooldownUntil → 发送 sensor.cooldown_skipped，返回（无 INSERT）
      - 否则：INSERT runs (state='queued', trigger='sensor', priority='normal', partition_key=resolveSensorPartitionKey(...))，UPDATE sensors SET last_run_key, last_fired_at=NOW(), cooldown_until=NOW()+spec.Cooldown, consecutive_failures=0；同一事务
    - handleResult 当 Fired=false 时：仅 UPDATE sensors SET last_evaluated_at=NOW(), consecutive_failures=0（成功但不触发仍重置失败计数器）
    - handleError 当 Sense() 错误或 panic：
      - 发送 sensor.evaluation_failed 事件，包含错误消息
      - UPDATE sensors SET consecutive_failures = consecutive_failures + 1, last_evaluated_at = NOW(), disabled_at = (CASE WHEN consecutive_failures+1 >= $threshold THEN NOW() ELSE disabled_at END)
      - 如果更新后 consecutive_failures+1 >= 阈值：发送 sensor.disabled 事件
    - resolveSensorPartitionKey(strategy, runKey)：如果 strategy 为 nil → ""；否则如果 RunKey 非空且对 strategy 是语法有效的键 → RunKey；否则对于日策略回退到 CurrentDailyKey(now, 24h)，周策略为 WeeklyKey(now-7d)，月策略为 MonthlyKey(now-1mo)，类别策略 RunKey 为空时为 ""
  </behavior>
  <action>
    1. 创建 `internal/sensor/evaluate.go`：
       ```go
       // Package sensor implements the sensor evaluation harness (D-05..D-08).
       package sensor

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

       const (
           DefaultMinInterval   = 30 * time.Second
           AutoDisableThreshold = 60  // D-08 default
       )

       // ErrNoDueSensor is returned by evaluateOneSensor when no due sensor row exists.
       var ErrNoDueSensor = errors.New("sensor: no due sensor")

       // safeEvaluate wraps SensorFunc with a timeout-bounded ctx and panic recovery.
       // Pitfall 2: a panic in user code must not crash the daemon.
       // Pitfall 3: an unbounded Sense() call must not block the tick loop.
       //
       // timeout defaults to spec.MinInterval (or DefaultMinInterval if MinInterval==0)
       // when the caller passes 0 — the user has acknowledged that "evaluate at least
       // this often" implies "Sense() must complete within this window."
       func safeEvaluate(ctx context.Context, spec asset.SensorSpec, timeout time.Duration) (result asset.SensorResult, err error) {
           if timeout == 0 {
               timeout = spec.MinInterval
               if timeout < DefaultMinInterval {
                   timeout = DefaultMinInterval
               }
           }
           evalCtx, cancel := context.WithTimeout(ctx, timeout)
           defer cancel()

           defer func() {
               if r := recover(); r != nil {
                   err = fmt.Errorf("sensor %q panic: %v", spec.Name, r)
               }
           }()
           return spec.Sense(evalCtx)
       }

       // sensorRow is the state read from the sensors table for evaluation.
       type sensorRow struct {
           ID                  uuid.UUID
           AssetName           string
           SensorName          string
           MinIntervalSeconds  int64
           LastRunKey          sql.NullString
           CooldownUntil       sql.NullTime
           ConsecutiveFailures int
       }

       // evaluateOneSensor selects the next due sensor with FOR UPDATE SKIP LOCKED, calls safeEvaluate,
       // and applies handleResult or handleError in the same transaction.
       // Returns ErrNoDueSensor when no rows are due.
       func evaluateOneSensor(
           ctx context.Context, store storage.Storage,
           reg *asset.DefinitionRegistry, events event.Writer,
           disableAfter int,
       ) error {
           db := store.DB()
           tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
           if err != nil {
               return fmt.Errorf("sensor.evaluate: begin tx: %w", err)
           }
           defer func() { _ = tx.Rollback() }()

           // Select due sensor: NOT disabled AND (never evaluated OR last_evaluated_at + min_interval <= NOW()).
           const selectSQL = `
               SELECT id, asset_name, sensor_name, min_interval_seconds, last_run_key, cooldown_until, consecutive_failures
               FROM sensors
               WHERE disabled_at IS NULL
                 AND (last_evaluated_at IS NULL
                      OR last_evaluated_at + (min_interval_seconds * interval '1 second') <= NOW())
               ORDER BY last_evaluated_at NULLS FIRST
               FOR UPDATE SKIP LOCKED
               LIMIT 1
           `
           var row sensorRow
           if err := tx.QueryRowContext(ctx, selectSQL).Scan(
               &row.ID, &row.AssetName, &row.SensorName, &row.MinIntervalSeconds,
               &row.LastRunKey, &row.CooldownUntil, &row.ConsecutiveFailures,
           ); err != nil {
               if errors.Is(err, sql.ErrNoRows) {
                   return ErrNoDueSensor
               }
               return fmt.Errorf("sensor.evaluate: select due: %w", err)
           }

           // Resolve SensorSpec from registry.
           a, err := reg.Get(row.AssetName)
           if err != nil || a == nil {
               // Asset definition gone — disable this sensor row.
               return autoDisableOrphan(ctx, tx, events, row.ID, row.AssetName, row.SensorName)
           }
           var spec *asset.SensorSpec
           for i := range a.Sensors() {
               s := a.Sensors()[i]
               if s.Name == row.SensorName {
                   spec = &s
                   break
               }
           }
           if spec == nil {
               return autoDisableOrphan(ctx, tx, events, row.ID, row.AssetName, row.SensorName)
           }

           // Evaluate.
           result, evalErr := safeEvaluate(ctx, *spec, 0)

           if evalErr != nil {
               return handleError(ctx, tx, events, &row, evalErr, disableAfter)
           }

           // Always emit sensor.evaluated (audit trail) — best effort, post-commit.
           defer func() {
               _ = events.Append(context.Background(), event.Event{
                   Type: event.EventTypeSensorEvaluated,
                   OccurredAt: time.Now().UTC(),
                   ResourceType: "sensor",
                   ResourceID:   row.ID.String(),
                   Payload: map[string]any{
                       "asset_name":  row.AssetName,
                       "sensor_name": row.SensorName,
                       "fired":       result.Fired,
                   },
               })
           }()

           if !result.Fired {
               return updateSensorOnNoFire(ctx, tx, &row)
           }
           return handleFired(ctx, tx, events, a, &row, *spec, result)
       }

       // autoDisableOrphan disables a sensor row whose asset/sensor was removed from the registry.
       func autoDisableOrphan(ctx context.Context, tx *sql.Tx, events event.Writer, sensorID uuid.UUID, assetName, sensorName string) error {
           _, err := tx.ExecContext(ctx, `UPDATE sensors SET disabled_at = NOW(), updated_at = NOW() WHERE id = $1`, sensorID)
           if err != nil {
               return fmt.Errorf("sensor.evaluate: disable orphan: %w", err)
           }
           if err := tx.Commit(); err != nil {
               return err
           }
           _ = events.Append(context.Background(), event.Event{
               Type: event.EventTypeSensorDisabled,
               OccurredAt: time.Now().UTC(),
               ResourceType: "sensor",
               ResourceID:   sensorID.String(),
               Payload: map[string]any{"asset_name": assetName, "sensor_name": sensorName, "reason": "orphaned"},
           })
           return nil
       }

       // updateSensorOnNoFire updates last_evaluated_at and resets consecutive_failures (D-08 auto-reset).
       func updateSensorOnNoFire(ctx context.Context, tx *sql.Tx, row *sensorRow) error {
           _, err := tx.ExecContext(ctx,
               `UPDATE sensors SET last_evaluated_at = NOW(), consecutive_failures = 0, updated_at = NOW() WHERE id = $1`,
               row.ID)
           if err != nil {
               return fmt.Errorf("sensor.evaluate: update no-fire: %w", err)
           }
           return tx.Commit()
       }

       // handleFired applies the two-layer dedup (D-07): RunKey check, then cooldown check.
       // If both pass, INSERT runs row + UPDATE sensors row in same tx.
       func handleFired(
           ctx context.Context, tx *sql.Tx, events event.Writer,
           a *asset.Asset, row *sensorRow, spec asset.SensorSpec, result asset.SensorResult,
       ) error {
           now := time.Now().UTC()

           // Layer 1: RunKey dedup.
           if result.RunKey != "" && row.LastRunKey.Valid && row.LastRunKey.String == result.RunKey {
               // Update last_evaluated_at, do NOT enqueue.
               _, _ = tx.ExecContext(ctx,
                   `UPDATE sensors SET last_evaluated_at = NOW(), consecutive_failures = 0, updated_at = NOW() WHERE id = $1`,
                   row.ID)
               if err := tx.Commit(); err != nil {
                   return err
               }
               _ = events.Append(context.Background(), event.Event{
                   Type: event.EventTypeSensorDedupSkipped,
                   OccurredAt: now,
                   ResourceType: "sensor",
                   ResourceID:   row.ID.String(),
                   Payload: map[string]any{"asset_name": row.AssetName, "sensor_name": row.SensorName, "run_key": result.RunKey},
               })
               return nil
           }

           // Layer 2: cooldown.
           if row.CooldownUntil.Valid && now.Before(row.CooldownUntil.Time) {
               _, _ = tx.ExecContext(ctx,
                   `UPDATE sensors SET last_evaluated_at = NOW(), consecutive_failures = 0, updated_at = NOW() WHERE id = $1`,
                   row.ID)
               if err := tx.Commit(); err != nil {
                   return err
               }
               _ = events.Append(context.Background(), event.Event{
                   Type: event.EventTypeSensorCooldownSkipped,
                   OccurredAt: now,
                   ResourceType: "sensor",
                   ResourceID:   row.ID.String(),
                   Payload: map[string]any{"asset_name": row.AssetName, "sensor_name": row.SensorName, "cooldown_until": row.CooldownUntil.Time},
               })
               return nil
           }

           // Pass both layers → enqueue.
           runID := uuid.New()
           partitionKey := resolveSensorPartitionKey(a.Partitions(), result.RunKey, now)
           var pkArg interface{} = nil
           if partitionKey != "" {
               pkArg = partitionKey
           }
           if _, err := tx.ExecContext(ctx,
               `INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority, partition_key)
                VALUES ($1, $2, 'queued', 'sensor', NOW(), 'normal', $3)`,
               runID, row.AssetName, pkArg,
           ); err != nil {
               // Likely partial-unique-index violation — sensor fired for an in-flight partition.
               // Treat as cooldown-skipped (avoid spamming runs table).
               return fmt.Errorf("sensor.handleFired: insert run (likely in-flight collision): %w", err)
           }

           cooldownUntil := now.Add(spec.Cooldown)
           if _, err := tx.ExecContext(ctx,
               `UPDATE sensors
                   SET last_evaluated_at = NOW(),
                       last_fired_at     = NOW(),
                       last_run_key      = $1,
                       cooldown_until    = $2,
                       consecutive_failures = 0,
                       updated_at        = NOW()
                 WHERE id = $3`,
               sql.NullString{String: result.RunKey, Valid: result.RunKey != ""}, cooldownUntil, row.ID,
           ); err != nil {
               return fmt.Errorf("sensor.handleFired: update sensor: %w", err)
           }
           if err := tx.Commit(); err != nil {
               return err
           }
           _ = events.Append(context.Background(), event.Event{
               Type: event.EventTypeSensorFired,
               OccurredAt: now,
               ResourceType: "sensor",
               ResourceID:   row.ID.String(),
               Payload: map[string]any{
                   "asset_name":    row.AssetName,
                   "sensor_name":   row.SensorName,
                   "run_id":        runID.String(),
                   "run_key":       result.RunKey,
                   "partition_key": partitionKey,
                   "payload":       result.Payload,
               },
           })
           return nil
       }

       // handleError increments consecutive_failures and auto-disables at the threshold (D-08).
       // Auto-resets on subsequent successful evaluation (handled in handleFired/updateSensorOnNoFire).
       func handleError(ctx context.Context, tx *sql.Tx, events event.Writer, row *sensorRow, evalErr error, disableAfter int) error {
           threshold := disableAfter
           if threshold <= 0 {
               threshold = AutoDisableThreshold
           }
           const updateSQL = `
               UPDATE sensors
                  SET consecutive_failures = consecutive_failures + 1,
                      last_evaluated_at    = NOW(),
                      updated_at           = NOW(),
                      disabled_at          = CASE
                          WHEN consecutive_failures + 1 >= $1 THEN NOW()
                          ELSE disabled_at
                      END
                WHERE id = $2
               RETURNING consecutive_failures, disabled_at
           `
           var newFailures int
           var disabledAt sql.NullTime
           if err := tx.QueryRowContext(ctx, updateSQL, threshold, row.ID).Scan(&newFailures, &disabledAt); err != nil {
               return fmt.Errorf("sensor.handleError: update: %w", err)
           }
           if err := tx.Commit(); err != nil {
               return err
           }
           _ = events.Append(context.Background(), event.Event{
               Type: event.EventTypeSensorEvaluationFailed,
               OccurredAt: time.Now().UTC(),
               ResourceType: "sensor",
               ResourceID:   row.ID.String(),
               Payload: map[string]any{
                   "asset_name":           row.AssetName,
                   "sensor_name":          row.SensorName,
                   "error":                evalErr.Error(),
                   "consecutive_failures": newFailures,
               },
           })
           if disabledAt.Valid && newFailures >= threshold {
               _ = events.Append(context.Background(), event.Event{
                   Type: event.EventTypeSensorDisabled,
                   OccurredAt: time.Now().UTC(),
                   ResourceType: "sensor",
                   ResourceID:   row.ID.String(),
                   Payload: map[string]any{
                       "asset_name":           row.AssetName,
                       "sensor_name":          row.SensorName,
                       "consecutive_failures": newFailures,
                       "threshold":            threshold,
                   },
               })
           }
           return nil
       }

       // resolveSensorPartitionKey returns runs.partition_key for a sensor-fired run.
       // If RunKey is non-empty AND validates for the strategy, use it; else fall back to current window.
       func resolveSensorPartitionKey(strategy partition.PartitionStrategy, runKey string, now time.Time) string {
           if strategy == nil {
               return ""
           }
           switch s := strategy.(type) {
           case partition.DailyPartitions:
               // Validate runKey format YYYY-MM-DD.
               if runKey != "" {
                   if _, err := time.Parse("2006-01-02", runKey); err == nil {
                       return runKey
                   }
               }
               return partition.CurrentDailyKey(now, 24*time.Hour)
           case partition.WeeklyPartitions:
               // Validate format YYYY-Wnn — fall back to current ISO week.
               return partition.WeeklyKey(now.Add(-7 * 24 * time.Hour))
           case partition.MonthlyPartitions:
               return partition.MonthlyKey(now.AddDate(0, -1, 0))
           case partition.CategoryPartitions:
               // For category, RunKey must be one of the declared keys.
               if runKey != "" {
                   for _, k := range s.Keys {
                       if k == runKey {
                           return runKey
                       }
                   }
               }
               // No fallback for category — return "" (will produce non-partitioned run; caller may treat as error).
               return ""
           }
           return ""
       }
       ```
    2. 创建 `internal/sensor/evaluate_test.go`，包含验证映射要求的测试：
       - `TestSensorPanicRecovery` — `safeEvaluate(ctx, SensorSpec{Sense: func(ctx context.Context) (SensorResult, error) { panic("boom") }})` 返回包含 "panic: boom" 的错误，且不传播 panic。
       - `TestSensorTimeoutEnforced` — `Sense` 阻塞 5s 且 `MinInterval=50ms`；`safeEvaluate` 在约 100ms 内返回 ctx.DeadlineExceeded。
       - `TestResolveSensorPartitionKey` — DailyPartitions + 有效 RunKey "2024-01-15" → "2024-01-15"；DailyPartitions + 无效 RunKey "foo" → CurrentDailyKey(now, 24h)；CategoryPartitions{Keys:[]string{"us"}} + RunKey "us" → "us"；CategoryPartitions + RunKey "eu"（不在键中）→ ""。
       - `TestSensorRunKeyDedup`（集成）— 设置 last_run_key="K1" 的 sensors 行；SensorFunc 返回 Fired=true, RunKey="K1"；断言无 runs 行插入，捕获 sensor.dedup_skipped 事件。
       - `TestSensorCooldown`（集成）— cooldown_until=NOW()+10min 的 sensors 行；SensorFunc 返回 Fired=true, RunKey=""；断言无 runs 行插入，捕获 sensor.cooldown_skipped 事件。
       - `TestSensorFire`（集成 — 验证映射：TestSensorFire）— 无 last_run_key、无冷却的 sensors 行；SensorFunc 返回 Fired=true, RunKey="K2"；断言插入一个 runs 行，trigger='sensor', priority='normal', partition_key=""（无 .Partitions）；sensor 行更新（last_run_key="K2", last_fired_at=NOW()）；捕获 sensor.fired 事件。
       - `TestSensorAutoDisable`（集成）— 设置 consecutive_failures=AutoDisableThreshold-1 的 sensors 行；SensorFunc 返回错误；断言一次评估后 sensor 行的 disabled_at != NULL，捕获 sensor.disabled 事件。
       - `TestSensorAutoResetOnSuccess`（集成）— consecutive_failures=10 的 sensors 行；SensorFunc 返回 Fired=false（成功，无触发）；断言评估后 consecutive_failures=0。
       使用与计划 03-04 模式一致的 `fakeEventWriter` 辅助函数。
  </action>
  <acceptance_criteria>
    - `grep -q 'package sensor' internal/sensor/evaluate.go`
    - `grep -q 'func safeEvaluate' internal/sensor/evaluate.go`
    - `grep -q 'context.WithTimeout' internal/sensor/evaluate.go`
    - `grep -q 'recover()' internal/sensor/evaluate.go`
    - `grep -q 'func handleFired' internal/sensor/evaluate.go`
    - `grep -q 'func handleError' internal/sensor/evaluate.go`
    - `grep -q 'last_run_key' internal/sensor/evaluate.go`
    - `grep -q 'cooldown_until' internal/sensor/evaluate.go`
    - `grep -q 'consecutive_failures + 1 >= ' internal/sensor/evaluate.go`
    - `grep -q "trigger='sensor'\\|trigger.*sensor" internal/sensor/evaluate.go`（sensor 触发的运行）
    - `grep -q 'EventTypeSensorDedupSkipped' internal/sensor/evaluate.go`
    - `grep -q 'EventTypeSensorCooldownSkipped' internal/sensor/evaluate.go`
    - `grep -q 'EventTypeSensorFired' internal/sensor/evaluate.go`
    - `grep -q 'EventTypeSensorEvaluationFailed' internal/sensor/evaluate.go`
    - `grep -q 'EventTypeSensorDisabled' internal/sensor/evaluate.go`
    - `grep -q 'AutoDisableThreshold' internal/sensor/evaluate.go`
    - `go test ./internal/sensor/... -run TestSensorPanicRecovery -count=1 -timeout 30s` 退出 0
    - `go test ./internal/sensor/... -run TestSensorTimeoutEnforced -count=1 -timeout 30s` 退出 0
    - `go test ./internal/sensor/... -run TestResolveSensorPartitionKey -count=1 -timeout 30s` 退出 0
    - `DATABASE_URL=... go test ./internal/sensor/... -run TestSensorRunKeyDedup -count=1 -timeout 60s` 退出 0
    - `DATABASE_URL=... go test ./internal/sensor/... -run TestSensorCooldown -count=1 -timeout 60s` 退出 0
    - `DATABASE_URL=... go test ./internal/sensor/... -run TestSensorFire -count=1 -timeout 60s` 退出 0
    - `DATABASE_URL=... go test ./internal/sensor/... -run TestSensorAutoDisable -count=1 -timeout 60s` 退出 0
  </acceptance_criteria>
  <verify>
    <automated>DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./internal/sensor/... -count=1 -timeout 120s</automated>
  </verify>
  <done>safeEvaluate 具有超时 + panic 恢复；handleFired 实现双层去重和正确的事件；handleError 递增 consecutive_failures 并在阈值时自动禁用，成功时自动重置；resolveSensorPartitionKey 在使用前验证 RunKey；所有 8 个测试通过。</done>
</task>

<task id="3.5.2" type="auto" tdd="true">
  <name>Task 2: 创建 internal/sensor/daemon.go（RunOnce 驱动）+ internal/sensor/registry.go（UpsertSensors）+ 测试</name>
  <files>internal/sensor/daemon.go, internal/sensor/daemon_test.go, internal/sensor/registry.go</files>
  <read_first>
    - internal/schedule/registry.go（UpsertSchedules 模式来自计划 03-04 — 镜像 SELECT-then-INSERT/UPDATE 方法）
    - internal/schedule/daemon.go（Daemon.tick 模式 — 镜像循环直到无行的行为）
    - internal/sensor/evaluate.go（刚创建）— evaluateOneSensor 签名，ErrNoDueSensor 哨兵
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 5
  </read_first>
  <behavior>
    - Daemon.RunOnce(ctx) 循环调用 evaluateOneSensor 直到返回 ErrNoDueSensor
    - 其他错误（DB 瞬态）时，记录并退出 RunOnce — 调用方的下一个 tick 重试
    - UpsertSensors 迭代 registry.All()，对每个 Asset.Sensors() spec：SELECT id FROM sensors WHERE asset_name=$1 AND sensor_name=$2；如果找到则当 changed 时 UPDATE min_interval_seconds；否则 INSERT 新行，默认零值
    - Daemon.RunOnce 在干净耗尽后返回 nil
    - TestDaemonRunOnceDrains — 设置 3 个待评估传感器行；断言 RunOnce 在一次调用中处理全部 3 个（每个评估触发 Fired=false，传感器获得 last_evaluated_at=NOW()）
    - TestUpsertSensors — 注册带有单个 SensorSpec 的资产；调用 UpsertSensors；断言插入一个 sensors 行；再次调用相同 spec；断言没有第二行，无错误（幂等）
    - TestUpsertSensorsMinIntervalUpdate — 注册 MinInterval=30s 的传感器；UpsertSensors；将注册表更改为 MinInterval=60s；UpsertSensors；断言 sensors.min_interval_seconds=60
  </behavior>
  <action>
    1. 创建 `internal/sensor/registry.go`：
       ```go
       package sensor

       import (
           "context"
           "database/sql"
           "errors"
           "fmt"

           "github.com/google/uuid"
           "github.com/kanpon/data-governance/internal/asset"
           "github.com/kanpon/data-governance/internal/storage"
       )

       // UpsertSensors reconciles asset.DefinitionRegistry → sensors table.
       // Idempotent: identical specs cause no update; changed MinInterval is propagated.
       // Removed sensors are NOT deleted from the table — they are left to be evaluated
       // and orphan-disabled by evaluateOneSensor (consistent with schedules behavior).
       func UpsertSensors(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry) error {
           db := store.DB()
           for _, a := range reg.All() {
               specs := a.Sensors()
               for _, spec := range specs {
                   if err := upsertOneSensor(ctx, db, a.Name(), spec); err != nil {
                       return fmt.Errorf("sensor.upsert(%s/%s): %w", a.Name(), spec.Name, err)
                   }
               }
           }
           return nil
       }

       func upsertOneSensor(ctx context.Context, db *sql.DB, assetName string, spec asset.SensorSpec) error {
           minIntervalSec := int64(spec.MinInterval.Seconds())
           if minIntervalSec <= 0 {
               minIntervalSec = int64(DefaultMinInterval.Seconds())
           }
           const selectSQL = `SELECT id, min_interval_seconds FROM sensors WHERE asset_name=$1 AND sensor_name=$2 LIMIT 1`
           var (
               id              uuid.UUID
               existingMinIvl  int64
           )
           err := db.QueryRowContext(ctx, selectSQL, assetName, spec.Name).Scan(&id, &existingMinIvl)
           if err == nil {
               if existingMinIvl == minIntervalSec {
                   return nil // unchanged
               }
               const updateSQL = `UPDATE sensors SET min_interval_seconds=$1, updated_at=NOW() WHERE id=$2`
               _, err = db.ExecContext(ctx, updateSQL, minIntervalSec, id)
               return err
           }
           if !errors.Is(err, sql.ErrNoRows) {
               return err
           }
           const insertSQL = `
               INSERT INTO sensors (id, asset_name, sensor_name, min_interval_seconds, consecutive_failures, created_at, updated_at)
               VALUES (gen_random_uuid(), $1, $2, $3, 0, NOW(), NOW())
           `
           _, err = db.ExecContext(ctx, insertSQL, assetName, spec.Name, minIntervalSec)
           return err
       }
       ```
    2. 创建 `internal/sensor/daemon.go`：
       ```go
       package sensor

       import (
           "context"
           "errors"
           "log/slog"

           "github.com/kanpon/data-governance/internal/asset"
           "github.com/kanpon/data-governance/internal/event"
           "github.com/kanpon/data-governance/internal/storage"
       )

       type Daemon struct {
           Store        storage.Storage
           Registry     *asset.DefinitionRegistry
           Events       event.Writer
           DisableAfter int  // 0 → AutoDisableThreshold default
       }

       // RunOnce drains the sensor evaluation queue. Designed to be called from the
       // scheduler subcommand's tick loop — the scheduler tick does schedule firing
       // then sensor evaluation, both backed by SKIP LOCKED.
       func (d *Daemon) RunOnce(ctx context.Context) error {
           for {
               if ctx.Err() != nil {
                   return ctx.Err()
               }
               err := evaluateOneSensor(ctx, d.Store, d.Registry, d.Events, d.DisableAfter)
               if errors.Is(err, ErrNoDueSensor) {
                   return nil
               }
               if err != nil {
                   slog.Error("sensor.evaluate_failed", "error", err)
                   return nil  // back off; next tick retries
               }
           }
       }
       ```
    3. 创建 `internal/sensor/daemon_test.go`：
       - `TestDaemonRunOnceDrains` — 设置 3 个待评估传感器（3 个 MinInterval=1ns 的 SensorSpec 以确保全部待评估），每个 Sense 返回 Fired=false。调用 RunOnce。断言每个传感器的 last_evaluated_at 被设置。
       - `TestUpsertSensors` — 注册带有 SensorSpec 的资产；调用 UpsertSensors；SELECT count from sensors → 1。再次调用 → 仍然是 1。
       - `TestUpsertSensorsMinIntervalUpdate` — 注册表 SensorSpec MinInterval 在调用之间更改；断言 DB 中 min_interval_seconds 更新。
       - `TestRunOnceContextCancellation` — 使用已取消的 ctx 启动 RunOnce，断言在 50ms 内返回 ctx.Canceled 错误。
  </action>
  <acceptance_criteria>
    - `grep -q 'type Daemon struct' internal/sensor/daemon.go`
    - `grep -q 'func (d \\*Daemon) RunOnce' internal/sensor/daemon.go`
    - `grep -q 'func UpsertSensors' internal/sensor/registry.go`
    - `grep -q 'INSERT INTO sensors' internal/sensor/registry.go`
    - `DATABASE_URL=... go test ./internal/sensor/... -run TestDaemonRunOnceDrains -count=1 -timeout 60s` 退出 0
    - `DATABASE_URL=... go test ./internal/sensor/... -run TestUpsertSensors -count=1 -timeout 60s` 退出 0
    - `go build ./...` 通过
  </acceptance_criteria>
  <verify>
    <automated>DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./internal/sensor/... -count=1 -timeout 120s</automated>
  </verify>
  <done>internal/sensor 包完整，包含 Daemon（RunOnce）+ UpsertSensors；幂等 upsert 在相同 spec 时保留行并更新 MinInterval 更改；完整的传感器测试套件通过。</done>
</task>

</tasks>

<threat_model>
## 信任边界

| 边界 | 描述 |
|----------|-------------|
| 用户代码（Sense 函数）→ sensor.safeEvaluate | 不受信任的用户 goroutine；超时 + panic 恢复是安全包装器 |
| Sense() 结果 → handleFired 入队路径 | RunKey + 冷却间隔 + 部分唯一索引组合防止重复运行 |
| 多个 sensor.Daemon 副本 → sensors 表 | SKIP LOCKED 多副本安全 |

## STRIDE 威胁注册表

| 威胁 ID | 类别 | 组件 | 处理方式 | 缓解计划 |
|-----------|----------|-----------|-------------|-----------------|
| T-03-05-01 | 拒绝服务 | Sense() panic 并崩溃守护进程（陷阱 2） | 缓解 | safeEvaluate 使用 defer recover() 将 panic 转换为错误；TestSensorPanicRecovery 验证。 |
| T-03-05-02 | 拒绝服务 | Sense() 在外部 HTTP 调用上无限阻塞（陷阱 3） | 缓解 | context.WithTimeout(ctx, spec.MinInterval) 限制每个 Sense() 调用。记录契约：Sense() 必须尊重 ctx 取消。TestSensorTimeoutEnforced 验证。 |
| T-03-05-03 | 拒绝服务 | Sense() 总是返回相同 RunKey 的 Fired=true，向 runs 表发送垃圾信息 | 缓解 | 双层去重（D-07）：RunKey 检查 + 冷却间隔检查。两层都必须通过才能入队。TestSensorRunKeyDedup 和 TestSensorCooldown 验证。另外 runs 上的部分唯一索引 (asset_name, partition_key) 防止重复的进行中运行。 |
| T-03-05-04 | 拒绝服务 | 有 bug 的 Sense() 每个 tick 都返回错误 — sensors 行永远被 evaluation_failed 事件占用 | 缓解 | 连续失败次数达到 AutoDisableThreshold（默认 60）后，设置 sensor.disabled_at；后续 tick 跳过它（selectSQL 中的 `WHERE disabled_at IS NULL`）。TestSensorAutoDisable 验证。 |
| T-03-05-05 | 篡改 | Sense() 返回精心制作的 RunKey 以绕过去重（例如，每次调用随机 UUID） | 接受 | 这是预期的用户行为 — 为真正不同的事件触发传感器的确应该使用不同的 RunKeys。纵深防御：冷却间隔第 2 层强制两次触发的最小间隔；用户控制冷却间隔。 |
| T-03-05-06 | 信息泄露 | SensorResult.Payload 可能通过 event_log 泄露凭证 | 接受 | 根据 D-06，Payload 是不透明的用户数据。事件日志受 RLS 保护（第 1 阶段）。记录：不要将秘密放在 Payload 中。 |
| T-03-05-07 | 欺骗 | 一个 sensor 行被两个副本 claim | 缓解 | sensors 表上的 SELECT FOR UPDATE SKIP LOCKED — 与调度器触发和运行 claim 相同的原语。 |
| T-03-05-08 | 篡改 | 通过直接 SQL 递增 sensors.consecutive_failures | 接受 | platform_app 角色对 sensors 有完整 DML；与 runs/schedules 相同的信任模型。未来增强：添加 Postgres CHECK 约束或触发器以强制单调增加，但不是第 3 阶段工作。 |
</threat_model>

<verification>
- `go build ./...` 通过。
- `DATABASE_URL=... go test ./internal/sensor/... -count=1 -timeout 120s` 通过（8+ 个测试）。
- safeEvaluate panic 恢复测试在不连接数据库的情况下通过（纯单元测试）。
- TestSensorAutoDisable 在恰好 N=AutoDisableThreshold-1 + 1 = 阈值次评估后触发，观察到 disabled_at 设置 + sensor.disabled 事件发送。
- 第 2 阶段 50 goroutine 原子性测试仍然通过（回归 — 传感器更改不影响运行 claim 路径）。
</verification>

<success_criteria>
- internal/sensor 包存在，包含 daemon、evaluate、registry 组件。
- safeEvaluate 强制超时 = max(spec.MinInterval, DefaultMinInterval) 并恢复 panic。
- handleFired 实现 RunKey 去重（第 1 层）+ 冷却间隔去重（第 2 层）+ 在与 UPDATE sensors 相同事务中 INSERT runs。
- handleError 递增 consecutive_failures 并在阈值时设置 disabled_at；成功时自动重置。
- resolveSensorPartitionKey 在使用前验证 RunKey 作为 partition_key；对时间策略回退到当前窗口；拒绝无法识别的类别键。
- UpsertSensors 幂等地将注册表 → sensors 表同步。
- 所有传感器测试（panic 恢复、超时、RunKey 去重、冷却、触发、自动禁用、自动重置）通过。
</success_criteria>

<output>
完成后，创建 `.planning/phases/03-scheduling-sensors-partitions/03-05-SUMMARY.md`，记录：
- 最终传感器包表面（Daemon, RunOnce, UpsertSensors）。
- 双层去重行为 — 引用 handleFired 的检查顺序。
- 自动禁用阈值默认值（60）和自动重置语义。
- 决策覆盖：D-05（传感器共享调度器子命令 — Daemon.RunOnce）、D-06（SensorResult 契约）、D-07（双层去重）、D-08（自动禁用 + 自动重置）、D-12（通过 resolveSensorPartitionKey 的传感器×分区组合）。
</output>
