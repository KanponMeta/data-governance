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

# Phase 3 Plan 05: 传感器求值器总结

**传感器求值 harness——`Daemon.RunOnce(ctx)` 通过 `SELECT FOR UPDATE SKIP LOCKED` 选择到期传感器，在 timeout + panic recovery 下调用用户的 `Sense(ctx)`，应用双层去重（RunKey + cooldown），然后要么入队一个 `runs` 行（`trigger='sensor'`），要么通过 event_log 记录去重决策。连续 N 次失败后自动禁用，成功后自动重置。**

## 性能指标

- **耗时：** 约 8 分钟
- **开始时间：** 2026-05-08T08:46:00Z
- **完成时间：** 2026-05-08T08:54:30Z（约）
- **任务数：** 2（均为自主完成；均遵循严格的 TDD RED → GREEN 流程）
- **创建文件数：** 5（3 个生产文件 + 2 个测试文件）+ 1 个 deferred-items 日志
- **修改文件数：** 0（纯增量计划）

## 完成情况

- **`internal/sensor/evaluate.go`** — `safeEvaluate` 对每个 `Sense()` 调用强制执行 `context.WithTimeout(ctx, max(spec.MinInterval, DefaultMinInterval))` 和 `defer recover()`。`evaluateOneSensor` 在单个事务中运行 SELECT FOR UPDATE SKIP LOCKED + user-Sense + state-update 序列。`handleFired` 实现 D-07 的双层去重。`handleError` 使用 `RETURNING` 子句增加 `consecutive_failures` 并在阈值处自动禁用（单次往返）。`resolveSensorPartitionKey` 在将 RunKey 采用为 `runs.partition_key` 之前验证其对策略的有效性；时间策略回退到前一个窗口（Open Question 1 默认值）。
- **`internal/sensor/daemon.go`** — `Daemon.RunOnce(ctx)` 通过重复调用 `evaluateOneSensor` 直到 `ErrNoDueSensor` 来排空到期传感器队列。每次迭代前都检查上下文取消。瞬态 DB 错误通过 `slog.Error` 记录，函数返回 `nil`，因此一个坏的传感器行无法停止调度器子命令的 tick 循环。
- **`internal/sensor/registry.go`** — `UpsertSensors(ctx, store, registry)` 通过 SELECT-then-UPDATE-or-INSERT 协调进程内 `asset.DefinitionRegistry` 到 `sensors` 表。幂等（相同 spec 不更新）；变更的 `MinInterval` 被传播。已移除的传感器不会被删除——它们由 `evaluateOneSensor` 的 orphan-disable 路径处理。
- **15 个测试通过** — 4 个单元测试（panic recovery、timeout、timeout 默认值、9 种情况的分区键解析器）、7 个集成测试（RunKey 去重、cooldown、fire 快乐路径、自动禁用、自动重置、orphan、分区组合）、4 个 daemon 测试（drain、ctx 取消、upsert 幂等、MinInterval 更新）。

## 任务提交

每个任务遵循严格的 TDD，分别提交 RED 和 GREEN 提交：

| 任务 | 描述 | RED 提交 | GREEN 提交 |
| ---- | ------------------------------------------------------------------------ | ---------- | ------------ |
| 1    | safeEvaluate + handleResult + handleError + resolveSensorPartitionKey | `46f215d`  | `f5ec658`    |
| 2    | Daemon.RunOnce + UpsertSensors (registry reconciliation + drain driver) | `59bfc5b`  | `80a264e`    |

使用 `git log --oneline 2f2df38..HEAD` 验证。

## 决策覆盖图

| 决策 | 覆盖者 | 测试名称 |
| -------- | --------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| **D-05**（传感器共享调度器子命令）| `Daemon.RunOnce(ctx)` 暴露给调度器的 tick 调用 | `TestDaemonRunOnceDrains`, `TestRunOnceContextCancellation`|
| **D-06**（SensorResult 契约）| `evaluateOneSensor` 读取 `Asset.Sensors()` SensorSpec，调用 `spec.Sense(ctx)` 并将 `SensorResult.RunKey/Payload` 贯穿到 `handleFired` | `TestSensorFire`, `TestSensorRunKeyDedup`|
| **D-07**（双层去重：RunKey + cooldown）| `handleFired` 首先检查 RunKey（廉价字符串比较），然后检查 cooldown（时间比较）——两者都必须通过才能 INSERT runs | `TestSensorRunKeyDedup`（层 1）, `TestSensorCooldown`（层 2）|
| **D-08**（Sense() 错误 → 日志 + 重试；N 次失败自动禁用）| `handleError` 使用相同的 UPDATE 中的 `CASE WHEN consecutive_failures + 1 >= $threshold` 增加 `consecutive_failures` 并设置 `disabled_at`；成功后自动重置 | `TestSensorAutoDisable`, `TestSensorAutoResetOnSuccess`|
| **D-12**（Sensor × Partitions 组合）| `resolveSensorPartitionKey` 在将 RunKey 采用为 `runs.partition_key` 之前验证 RunKey；按策略回退 | `TestResolveSensorPartitionKey`（9 个子测试）, `TestSensorPartitionKeyDailyComposition`|

## 双层去重行为（D-07 — 原文）

`handleFired` 中的检查顺序是 **RunKey 第一，cooldown 第二**——在 run 入队之前，两层都必须允许 fire：

```go
// 层 1：RunKey 去重（廉价字符串比较；仅在两侧都非空时有意义）。
if result.RunKey != "" && row.LastRunKey.Valid && row.LastRunKey.String == result.RunKey {
    // emit sensor.dedup_skipped, no INSERT
}

// 层 2：cooldown 窗口（时间比较）。
if row.CooldownUntil.Valid && now.Before(row.CooldownUntil.Time) {
    // emit sensor.cooldown_skipped, no INSERT
}

// 两层都通过 → 入队一个 run。
```

这是 belt-and-suspenders 防御：
- **仅层 1 失败**当用户代码故意为 cooldown 内合法的相同键事件返回相同的键时。Cooldown 仍然会阻止它们。
- **仅层 2 失败**当用户代码有 bug 在快速连续返回相同键时。RunKey 检查在甚至咨询 cooldown 之前就捕获了它。

## 自动禁用 + 自动重置（D-08）

- **阈值：** `AutoDisableThreshold = 60`（默认值；通过 `Daemon.DisableAfter` 覆盖）。
- **自动禁用：** `handleError` SQL 是 `UPDATE sensors SET consecutive_failures = consecutive_failures + 1, disabled_at = CASE WHEN consecutive_failures + 1 >= $threshold THEN NOW() ELSE disabled_at END WHERE id = $id RETURNING ...`。单次往返；`RETURNING` 子句向调用者暴露新的失败计数和 disabled_at 状态，然后调用者发出 `sensor.evaluation_failed` 和（有条件的）`sensor.disabled` 事件。
- **自动重置语义：** `consecutive_failures` 在每次成功求值时重置为 `0`——无论是 `Fired=false`（通过 `updateSensorOnNoFire` 的成功无 fire）还是 `Fired=true`（通过 `handleFired` 的 INSERT 后 UPDATE 的成功有 fire）。自我恢复的不稳定传感器不会将过去的失败计入阈值。这符合 03-RESEARCH.md A5 并匹配 Dagster 的约定。
- **孤儿自动禁用：** 其 asset/sensor 名称已从进程内注册表中移除的传感器行被禁用，禁用原因为 `"orphaned"`——针对跨部署注册表漂移的纵深防御。

## Sensor × Partitions 组合（D-12）

当传感器为分区资产触发时，`resolveSensorPartitionKey(strategy, runKey, now)` 决定 `runs.partition_key` 的值：

| 策略 | RunKey 有效？| partition_key 结果 |
| ------------------ | ------------- | ------------------------------------------------------------- |
| nil | — | `""`（非分区 run）|
| DailyPartitions | YYYY-MM-DD | RunKey 原文 |
| DailyPartitions | 其他 | `partition.CurrentDailyKey(now, 24h)` — 昨天的键 |
| WeeklyPartitions | YYYY-Www | RunKey 原文 |
| WeeklyPartitions | 其他 | `partition.WeeklyKey(now - 7d)` — 上一个 ISO 周 |
| MonthlyPartitions | YYYY-MM | RunKey 原文 |
| MonthlyPartitions | 其他 | `partition.MonthlyKey(now.AddDate(0,-1,0))` — 上一个日历月 |
| CategoryPartitions | 在 `Keys` 中 | RunKey 原文 |
| CategoryPartitions | 其他 / 空 | `""` — category 无回退（按设计，陷阱 4）|

陷阱 4 被强制执行，因为 `CategoryPartitions` 验证拒绝不在声明的 `Keys` 切片中的键——带 category 传感器的 daily 格式化 RunKey 产生空的 `partition_key`，与部分唯一索引 `WHERE partition_key IS NOT NULL` 结合，产生非分区 run 而不是错误分区的 run。

## 公共 API 表面（为 Plan 03-06 冻结）

```go
package sensor

// 常量
const DefaultMinInterval   = 30 * time.Second
const AutoDisableThreshold = 60

// Sentinel
var ErrNoDueSensor = errors.New("sensor: no due sensor")

// Daemon — 暴露给调度器子命令的 tick 循环（D-05）。
type Daemon struct {
    Store        storage.Storage
    Registry     *asset.DefinitionRegistry
    Events       event.Writer
    DisableAfter int  // 0 → AutoDisableThreshold
}
func (d *Daemon) RunOnce(ctx context.Context) error

// 注册表同步 — 在启动时从调度器子命令调用。
func UpsertSensors(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry) error
```

## 威胁表面覆盖

计划的 `<threat_model>` 登记已通过本计划的交付物完全解决：

| 威胁 ID | 状态 | 证据 |
| ---------- | ---------- | ------------------------------------------------------------------------------------------------------------- |
| T-03-05-01 | mitigated | `defer recover()` in `safeEvaluate`; `TestSensorPanicRecovery` 强制执行 |
| T-03-05-02 | mitigated | `context.WithTimeout(ctx, spec.MinInterval)` in `safeEvaluate`; `TestSensorTimeoutEnforced` 强制执行 |
| T-03-05-03 | mitigated | 双层去重 in `handleFired`；来自 plan 03-01 的部分唯一索引在 INSERT 时作为纵深防御。`TestSensorRunKeyDedup` + `TestSensorCooldown` 强制执行 |
| T-03-05-04 | mitigated | `consecutive_failures + 1 >= $threshold` 自动设置 `disabled_at`；`WHERE disabled_at IS NULL` 在 selectSQL 中排除后续 tick 中的禁用行。`TestSensorAutoDisable` 强制执行 |
| T-03-05-05 | accept | 定制的 RunKey 绕过是针对真正不同事件的预期用户行为；cooldown 层 2 是操作员的防御 |
| T-03-05-06 | accept | SensorResult.Payload 按 D-06 不透明；代码注释中已记录 |
| T-03-05-07 | mitigated | `SELECT FOR UPDATE SKIP LOCKED` 在 sensors 表上——与 Phase 2 ClaimNext 相同的原语（TestClaimAtomicity50Goroutines 通过新 schema 验证，回归验证） |
| T-03-05-08 | accept | 直接 SQL 信任模型对于 `platform_app` 与 runs/schedules 一致 |

## 与计划的偏差

**无——计划完全按书面执行。**

计划的任务结构、文件布局、行为规则和验收标准均 1:1 匹配。值得注意的三个小调整（均未改变范围）：

1. **`reg.All()` 替换为 `reg.List() + reg.Get(name)` 在 `UpsertSensors` 中。** 计划的伪代码引用 `reg.All()`，但现有的 `internal/asset.DefinitionRegistry`（来自 plan 03-02）只暴露 `List()` 和 `Get()`。逐字使用现有 API——不改变计划意图。
2. **`AssetIO` 构造函数第三个参数已存在。** Plan 03-02 向 `NewAssetIO` 添加了 `partitionKey`。本计划不需要更改 `internal/runtime/executor.go`（已经传递 `""`）。
3. **添加了超出计划枚举集的两个额外测试：** `TestSensorOrphanDisabled`（覆盖计划在 `<behavior>` 中调用但未枚举为测试的孤儿自动禁用路径）和 `TestSensorPartitionKeyDailyComposition`（D-12 集成覆盖）。两者都是纯威胁缓解证据——零范围蔓延。

## 遇到的问题

- **预先存在的 `internal/runtime` 测试失败，显示 "open ent: unsupported driver: pgx"。** 验证存在于基础提交 `2f2df38` 的 master 上——超出 plan 03-05 范围。在 `.planning/phases/03-scheduling-sensors-partitions/deferred-items.md` 中记录以供分类。
- **计划的 DATABASE_URL 引用 `data_governance` 数据库；实际本地 DB 是 `platform`。** 使用实际 DB 名称（`postgres://platform_app:platform_app@localhost:5432/platform?sslmode=disable`）与 `Makefile` `integration` 目标一致。相同的凭证。无代码更改。

## 自我检查：通过

**创建的文件存在：**
- 已找到：internal/sensor/evaluate.go
- 已找到：internal/sensor/evaluate_test.go
- 已找到：internal/sensor/daemon.go
- 已找到：internal/sensor/daemon_test.go
- 已找到：internal/sensor/registry.go
- 已找到：.planning/phases/03-scheduling-sensors-partitions/deferred-items.md

**提交存在：**
- 已找到：46f215d（任务 1 RED — 失败测试）
- 已找到：f5ec658（任务 1 GREEN — evaluate.go）
- 已找到：59bfc5b（任务 2 RED — daemon/upsert 测试）
- 已找到：80a264e（任务 2 GREEN — daemon.go + registry.go）

**构建和测试通过：**
- `go build ./...` → 通过
- `go vet ./internal/sensor/...` → 干净
- `DATABASE_URL=... go test ./internal/sensor/... -count=1 -timeout 120s` → 15 个测试，全部通过
- Phase 2 回归 `TestClaimAtomicity50Goroutines` → 仍然通过（传感器更改未触碰 runs claim 路径）

## 下一计划就绪状态

- **Plan 03-06（scheduler 子命令）** 现在可以将 `sensor.Daemon{...}.RunOnce(ctx)` 接入其 tick 循环， alongside `schedule.Daemon`（来自 plan 03-04）。在启动时它应该调用 `sensor.UpsertSensors(ctx, store, reg)` 来从已注册资产填充 sensors 表。冻结的 `Daemon.RunOnce` 签名：`func (d *Daemon) RunOnce(ctx context.Context) error`。
- **Plan 03-07（backfill CLI）** 不受影响——传感器求值不与 backfill 行交互。来自 plan 03-01 的 `(asset_name, partition_key)` 部分唯一索引确保传感器触发和 backfill 入队的 run 不能冲突。

---

*Phase: 03-scheduling-sensors-partitions*
*Plan: 05 (sensor evaluator)*
*Completed: 2026-05-08*
