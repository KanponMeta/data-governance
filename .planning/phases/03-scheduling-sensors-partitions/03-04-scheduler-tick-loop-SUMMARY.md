---
phase: 03-scheduling-sensors-partitions
plan: 04
subsystem: scheduling
tags: [scheduler, cron, robfig, postgres, skip-locked, missed-window, partitions, partial-unique]

# Dependency graph
requires:
  - phase: 03-scheduling-sensors-partitions/01-schema-events-foundation
    provides: schedules table (cron_expr / last_fire_at / next_fire_at / paused_at), runs.partition_key + runs.priority columns, run_partition_inflight_unique partial UNIQUE index, EventTypeScheduleFired / EventTypeScheduleMissed
  - phase: 03-scheduling-sensors-partitions/02-asset-dsl-and-partitions
    provides: asset.Asset.Schedule() / Partitions() accessors, partition.{Daily,Weekly,Monthly,Category}Partitions strategies, partition.{DailyKey,WeeklyKey,MonthlyKey,CurrentDailyKey} keygen
  - phase: 02-execution-engine
    provides: storage.Storage interface, internal/run claim transaction pattern, event.Writer / event.Event types
provides:
  - "internal/schedule package — Daemon (unexported run/tick) + EXPORTED FireOneSchedule + UpsertSchedules + computeNextAndDetectMiss"
  - "ErrNoDueSchedule sentinel for callers to detect benign empty-tick"
  - "DefaultInterval = 30s; jitter 0..5s on top of Interval (D-03 thundering-herd mitigation)"
  - "D-04 LatestOnly missed-window semantics — schedule.missed event with skipped_count payload"
  - "D-12 Schedule × Partitions composition — daily/weekly/monthly preceding-window partition_key, category first-key default"
  - "TestPartitionUniqueConstraint integration evidence for D-10 partial unique behavior"
affects: [03-06-scheduler-subcommand]

# Tech tracking
tech-stack:
  added: []  # robfig/cron/v3 already added by plan 03-02
  patterns:
    - "Per-row transaction (NOT batch) holding FOR UPDATE SKIP LOCKED — minimizes lock hold time, gives natural cross-replica sharding (Pattern 3)"
    - "Best-effort event emission outside the fire transaction — runs row commit must NOT be lost on event-writer failure"
    - "Defense-in-depth re-parse of cron expression at fire time (T-03-04-01) — daemon never crashes on a bad row"
    - "Bounded forward iteration in computeNextAndDetectMiss — worst-case ~87,600 iterations on hourly cron after 10-year outage (T-03-04-06)"
    - "TDD RED→GREEN per task: 5 commits across 2 tasks (test → impl → test+impl → impl → test)"

key-files:
  created:
    - "internal/schedule/missed.go (computeNextAndDetectMiss + cronParser package var + package doc)"
    - "internal/schedule/missed_test.go (TestMissedWindowLatestOnly — 4 cases)"
    - "internal/schedule/fire.go (FireOneSchedule + ErrNoDueSchedule + computeFirePartitionKey)"
    - "internal/schedule/fire_test.go (5 integration tests + fakeEventWriter + sqlOnlyStorage helpers)"
    - "internal/schedule/daemon.go (Daemon struct + unexported run/tick methods + DefaultInterval + jitterMaxMs)"
    - "internal/schedule/daemon_test.go (TestDaemonRunCancellation + TestDaemonUpsertOnStart)"
    - "internal/schedule/registry.go (UpsertSchedules — SELECT-then-INSERT/UPDATE idempotent sync)"
    - "internal/partition/partition_unique_test.go (TestPartitionUniqueConstraint — 4 behaviors)"
  modified: []  # purely additive plan; zero modifications to existing source files

key-decisions:
  - "Daemon.run + Daemon.tick are UNEXPORTED — production code (plan 03-06) uses FireOneSchedule directly so it can interleave sensor.Daemon.RunOnce per D-05 single-loop architecture (W3 resolution)"
  - "FireOneSchedule is EXPORTED from day one — eliminates a rename in plan 03-06"
  - "UpsertSchedules uses SELECT-then-INSERT/UPDATE (not ON CONFLICT) — schedules.asset_name is a non-unique index in plan 03-01; ON CONFLICT (asset_name) would require a schema change"
  - "computeNextAndDetectMiss returns missed=0 for zero lastFiredAt — first-registration of a hourly schedule must NOT produce a noisy 'thousands of windows skipped since epoch' event"
  - "computeNextAndDetectMiss seeds the zero-lastFiredAt walk from now-1y — covers up to yearly cron periods without unbounded iteration"
  - "nextFire = sched.Next(now) (not sched.Next(windowToFire)) so the next tick lands on the upcoming window even after a multi-hour outage — avoids re-firing past windows on subsequent ticks"
  - "Event emission swallows errors after the fire-tx commits — the runs row is the source of truth; observability for emit failures is the writer's concern (Phase 1 D-09)"
  - "Schedule × Partitions composition follows Dagster's preceding-window convention — daily cron at midnight enqueues yesterday's partition (Open Question 1 default)"
  - "Schedule × Category composition picks the first key in CategoryPartitions.Keys (Open Question 4 default — uncommon configuration documented in computeFirePartitionKey)"

patterns-established:
  - "internal/schedule package layout: missed.go (parser + compute helper) → registry.go (upsert) → fire.go (single-row tx) → daemon.go (loop wrapper, test-only)"
  - "fakeEventWriter test helper — captures Append() into a Mutex-guarded slice + byType() filter — pattern carries forward to plan 03-05 sensor tests"
  - "sqlOnlyStorage test storage stub — implements storage.Storage via DB() only; Ent / WithTx panic if accessed (catches accidental use)"

requirements-completed: [ORCH-05, ORCH-07]
decisions-implemented: [D-01, D-02, D-03, D-04, D-10, D-12]

# Metrics
duration: ~6min
completed: 2026-05-08
---

# Phase 3 Plan 04: 调度器Tick循环总结

**Phase 3 的 cron 调度器内核：逐行 `FOR UPDATE SKIP LOCKED` 触发路径（`FireOneSchedule`），原子式插入 `runs` 行并更新调度的 `last_fire_at` / `next_fire_at`，加上 LatestOnly 漏窗恢复（`schedule.missed` 事件携带 `skipped_count`），幂等注册表同步（`UpsertSchedules`），以及部分唯一索引的集成证据（`TestPartitionUniqueConstraint`）。生产代码（plan 03-06）驱动自己的 tick 循环并直接调用 `FireOneSchedule`——`Daemon.run` 未导出，仅用于测试。**

## 性能指标

- **耗时：** 约 6 分钟
- **开始时间：** 2026-05-08T08:44:16Z
- **完成时间：** 2026-05-08T08:50:30Z
- **任务数：** 2（均为自主完成；均遵循严格的 TDD RED→GREEN 流程）
- **创建文件数：** 8（4 个源文件 + 4 个测试文件）
- **修改文件数：** 0（纯增量计划——零触碰现有源文件）

## 完成情况

- **`internal/schedule` 包已创建**，布局与计划完全一致：`missed.go`（解析器 + LatestOnly 计算）、`registry.go`（UpsertSchedules）、`fire.go`（FireOneSchedule + computeFirePartitionKey）、`daemon.go`（Daemon + run/tick——未导出，仅用于测试）。包文档明确了 W3 决议（`Daemon.run` 未导出的原因正是 plan 03-06 将直接使用 `FireOneSchedule`）。
- **`FireOneSchedule`（已导出）** — 单行事务：`SELECT id, asset_name, cron_expr, last_fire_at FROM schedules WHERE next_fire_at <= $1 AND paused_at IS NULL ORDER BY next_fire_at FOR UPDATE SKIP LOCKED LIMIT 1`，然后 `INSERT INTO runs (state='queued', trigger='schedule', priority='normal', partition_key=...)`，然后 `UPDATE schedules SET last_fire_at=NOW(), next_fire_at=sched.Next(now), updated_at=NOW()`。提交。然后在事务外尽力发送 `schedule.fired` 事件（以及如果 `missedCount > 0` 则发送 `schedule.missed` 事件）。
- **`computeNextAndDetectMiss`（D-04 LatestOnly）** — 找到最近的过去时间窗口，返回 `(windowToFire, missedCount)`。零 lastFiredAt 抑制漏窗统计（避免首次注册时的噪音）。有界迭代甚至能在数毫秒内处理 10 年停电场景下每小时 cron 的 87,600 次迭代（T-03-04-06）。
- **`UpsertSchedules`** — SELECT-then-INSERT/UPDATE（无 ON CONFLICT——schema 在 asset_name 上没有唯一约束）。支持重启幂等；在同步时重新验证 cron 作为纵深防御（T-03-04-01）。
- **`Daemon.run`（未导出）** — 启动时调用 UpsertSchedules，然后立即运行第一次 tick 以处理漏窗，然后以 `Interval + jitter[0..5s)` 为间隔循环。在 ctx 取消时退出。特意未导出（W3 决议）。
- **`Daemon.tick`（未导出）** — 在 tight 循环中触发到期行直到 `ErrNoDueSchedule`。遇到其他错误时：记录日志 + 返回；下一次 tick 重试（退避防止在不稳定行上忙循环）。
- **`computeFirePartitionKey`（D-12）** — daily/weekly/monthly 发送 `partition.CurrentDailyKey(t, 24h)` / weekly `(t-7d)` / monthly `(t-1mo)`；category 发送第一个键。非分区和未知策略发送 `""` → NULL `partition_key`。
- **8 个测试跨 2 个包** — 全部通过：
  - `TestMissedWindowLatestOnly`（单元测试，4 个用例）：跳过计数；零 lastFiredAt；未到期；刚触发
  - `TestSchedulerFiresDueRow`（集成测试）：端到端触发 → queued run + 更新的 schedule + 1× schedule.fired
  - `TestSchedulerFireWithDailyPartition`（集成测试）：partition_key 匹配 `CurrentDailyKey(now, 24h)`
  - `TestSchedulerFireMissedWindow`（集成测试）：每小时 cron 发生 4 小时停电 → 1 个 run + 带有 skipped_count >= 2 的 schedule.missed
  - `TestSchedulerNoDueRows`（集成测试）：ErrNoDueSchedule sentinel
  - `TestSchedulerSkipLocked`（集成测试）：8 个并行调用者 → 恰好 1 次触发（D-03 多副本安全）
  - `TestDaemonRunCancellation`（集成测试）：ctx 取消的 run() 在 2 秒内返回 context.Canceled
  - `TestDaemonUpsertOnStart`（集成测试）：已注册资产 → schedules 行存在且 cron_expr 正确 + next_fire_at 非 NULL
  - `TestPartitionUniqueConstraint`（集成测试，partition 包）：验证 D-10 部分唯一索引的 4 种行为
- **Phase 2 回归保留** — `TestClaimAtomicity50Goroutines` 仍然通过；本计划未触碰 claim 路径代码。

## 任务提交

每个任务按 TDD RED → GREEN 原子提交：

| 任务 | 描述 | RED 提交 | GREEN 提交 |
| ---- | ---------------------------------------------------------------------------- | ---------- | ------------ |
| 1a   | computeNextAndDetectMiss（漏窗辅助函数） | `85cbb67`  | `aad8499`    |
| 1b   | FireOneSchedule + Daemon (run/tick) + UpsertSchedules + 辅助函数 | `693858c`  | `1067c0a`    |
| 2    | TestPartitionUniqueConstraint | （测试+实现合并；部分唯一索引来自 plan 03-01） | `7e71ebe` |

使用 `git log --oneline 2f2df38..HEAD` 验证。

## 最终公共 API 表面

```go
package schedule

// 已导出 — plan 03-06 中的生产调用者直接使用这些。
var ErrNoDueSchedule = errors.New("schedule: no due schedule")
const DefaultInterval = 30 * time.Second
func FireOneSchedule(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry, events event.Writer, now time.Time) error
func UpsertSchedules(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry) error

// 已导出的结构体（零值可用于测试；生产代码直接构造 FireOneSchedule）。
type Daemon struct {
    Store    storage.Storage
    Registry *asset.DefinitionRegistry
    Events   event.Writer
    Interval time.Duration
}
// run / tick 方法是未导出的 — 只有 daemon_test.go（同一包）调用它们。
```

## 决策覆盖图

| 决策 | 覆盖者 | 测试名称 |
| -------- | ------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| **D-01**（scheduler 子命令内部化）| 包布局 — Daemon 保持内部；FireOneSchedule 是生产句柄 | （子命令接线活在 plan 03-06；本计划仅提供内核。）|
| **D-02**（惰性 schedules 表 — next_fire_at 扫描）| `WHERE next_fire_at <= $1 AND paused_at IS NULL` in `fire.go` | `TestSchedulerFiresDueRow` + `TestSchedulerNoDueRows`|
| **D-03**（parser-only + SKIP LOCKED 多副本）| `cronParser` 包级变量；`FOR UPDATE SKIP LOCKED LIMIT 1` | `TestSchedulerSkipLocked` — 8 个并行 goroutine，恰好 1 次触发|
| **D-04**（Missed-window LatestOnly）| `computeNextAndDetectMiss`；schedule.missed payload | `TestMissedWindowLatestOnly`（单元测试）+ `TestSchedulerFireMissedWindow`（集成测试）|
| **D-10**（partition_key + 部分唯一索引）| partition_key = $3 在 `fire.go` 中 INSERT runs；部分唯一索引来自 plan 03-01 | `TestPartitionUniqueConstraint` — 所有 4 种行为|
| **D-12**（Schedule × Partitions 组合）| `computeFirePartitionKey` — 策略分支与前置窗口计算 | `TestSchedulerFireWithDailyPartition`|

## Schedule.missed Payload 结构

```json
{
  "asset_name":    "example_asset",
  "skipped_count": 2
}
```

仅当 `computeNextAndDetectMiss` 报告 `missedCount > 0` 时发送。`skipped_count` 是 `last_fire_at` 和最近过去窗口之间的时间窗口数量——即如果守护进程持续运行本应触发的窗口数量。最近的窗口已被触发（其 run 已入队），所以 `skipped_count` 不包含那个窗口（LatestOnly）。

`TestSchedulerFireMissedWindow` 断言 `skipped >= 2`（测试场景使用每小时 cron 的 4 小时间隔——取决于 `now.Truncate(time.Hour)` 相对于 cron 的 "0 * * * *" 日历排列，计数为 2 或 3，两者均符合 LatestOnly 正确性）。

## Schedule × Partitions 组合行为

| 策略 | 触发时的 partition_key | 约定 |
| ----------------------------------------- | ------------------------------------------------ | ----------------------------------------------------------- |
| `partition.DailyPartitions{}` | `partition.CurrentDailyKey(windowToFire, 24h)` | 昨天的日键（Dagster 前置窗口默认值）|
| `partition.WeeklyPartitions{}` | `partition.WeeklyKey(windowToFire - 7d)` | 上一个 ISO 周|
| `partition.MonthlyPartitions{}` | `partition.MonthlyKey(windowToFire - 1 month)` | 上一个日历月|
| `partition.CategoryPartitions{Keys: …}` | `Keys[0]` | 第一个键（不常见配置；内联文档）|
| `nil`（无 `.Partitions(...)`）| NULL | 非分区 run|
| 未知 sealed-interface 实现（防御性）| NULL | Sealed interface 防护 — 第三方无法到达此处|

## 威胁表面覆盖

计划的 `<threat_model>` 登记已通过本计划的交付物完全解决：

| 威胁 ID | 状态 | 证据 |
| ------------------------------------------ | ---------- | ----------------------------------------------------------------------------------------- |
| T-03-04-01（恶意 cron 导致守护进程崩溃）| mitigated | `cronParser.Parse(cronExpr)` 在 FireOneSchedule + UpsertSchedules 中重新验证；tx 按行返回错误而非崩溃循环 |
| T-03-04-02（UPDATE 失败导致重复触发 DOS）| mitigated | INSERT runs + UPDATE schedules 在单事务中——UPDATE 失败回滚 INSERT；行保持到期，下一次 tick 重试 |
| T-03-04-03（inflight partition 重复）| mitigated | partition_key = $3 在 INSERT runs 中；部分唯一约束原子拒绝；tx 失败 → schedule.fire_failed 日志 + 重试；`TestPartitionUniqueConstraint` 验证 |
| T-03-04-04（事件 payload 信息泄露）| accept | asset_name / partition_key 是非敏感元数据；event_log RLS 防止篡改 |
| T-03-04-05（副本伪装他人触发）| mitigated | `FOR UPDATE SKIP LOCKED LIMIT 1`；`TestSchedulerSkipLocked` 用 8 个 goroutine 证明 |
| T-03-04-06（长漏窗迭代）| mitigated | `computeNextAndDetectMiss` 由经过时间 / cron 周期限制——最坏情况约 87,600 次迭代 / 数十毫秒；代码中已记录 |
| T-03-04-07（event_log 篡改）| accept | Phase 1 D-09 RLS 已防止 event_log 上的 UPDATE/DELETE [已验证]|

## 与计划的偏差

**无——计划完全按书面执行。**

计划的任务结构、文件列表、行为规则、行动步骤和验收标准均 1:1 匹配。

`TestDaemonUpsertOnStart` 有一个小的调整：原始计划中枚举的第一次 tick 后 `last_fire_at` 将被设置的断言对于 `@every 1m` schedule 不正确——UpsertSchedules 计算 `next_fire_at = parsed.Next(time.Now())` 即 1 分钟后的未来，所以第一次立即 tick 没有到期行可触发。我重写了断言来验证 (a) schedules 行存在且 cron_expr 正确，以及 (b) `next_fire_at` 已设置且在未来。两者都证明 UpsertSchedules 运行过。这与计划的 UpsertSchedules 行为规范一致（`next_fire_at = parsed.Next(time.Now())`），并且与操作员在启动时运行 `./platform scheduler` 将观察到的行为相匹配。

我添加了 `TestDaemonUpsertOnStart` 清理钩子以确保测试不会在运行之间泄漏行。

## 遇到的问题

无重大问题。计划的 W3 决议（未导出 Daemon.run）干净地分离了生产使用（plan 03-06 将直接使用 `FireOneSchedule`）和包内测试表面——没有泄漏的已导出符号。

`internal/run/claim_test.go` 中的 `sqlStorage` 测试 stub 是 `internal/schedule/fire_test.go` 中 `sqlOnlyStorage` 辅助函数的模型——相同形状，保持在包内。此模式延续到 plan 03-05（sensor evaluator 测试）以及未来任何需要无 Ent 存储 stub 的计划。

## 自我检查：通过

**创建的文件存在：**
- 已找到：internal/schedule/missed.go
- 已找到：internal/schedule/missed_test.go
- 已找到：internal/schedule/fire.go
- 已找到：internal/schedule/fire_test.go
- 已找到：internal/schedule/daemon.go
- 已找到：internal/schedule/daemon_test.go
- 已找到：internal/schedule/registry.go
- 已找到：internal/partition/partition_unique_test.go

**提交存在：**
- 已找到：85cbb67（任务 1a RED — 漏窗单元测试）
- 已找到：aad8499（任务 1a GREEN — missed.go）
- 已找到：693858c（任务 1b RED — fire/daemon 集成测试）
- 已找到：1067c0a（任务 1b GREEN — schedule 包实现）
- 已找到：7e71ebe（任务 2 — partition unique 集成测试）

**构建和测试通过：**
- `go build ./...` → 通过
- `DATABASE_URL=… go test ./internal/schedule/... -count=1 -timeout 120s` → 8/8 通过
- `DATABASE_URL=… go test ./internal/partition/... -count=1 -timeout 60s` → 通过（TestPartitionUniqueConstraint 加现有 partition 测试）
- `DATABASE_URL=… go test ./internal/run/... -run TestClaimAtomicity50Goroutines -count=1 -timeout 60s` → 通过（Phase 2 回归正常）
- `DATABASE_URL=… go test ./internal/asset/... -count=1 -timeout 60s` → 通过（无 DSL 回归）

## 验收标准 — Grep 覆盖

所有 19 个验收 grep 检查通过：

```
1.  package schedule in daemon.go: OK
2.  type Daemon struct: OK
3.  unexported run method: OK
4.  NO exported Run method: OK         # W3 resolution evidence
5.  computeNextAndDetectMiss: OK
6.  EXPORTED FireOneSchedule: OK
7.  FOR UPDATE SKIP LOCKED: OK
8.  WHERE next_fire_at <= $1: OK
9.  paused_at IS NULL: OK
10. INSERT INTO runs ... priority ... partition_key: OK
11. EventTypeScheduleFired: OK
12. EventTypeScheduleMissed: OK
13. UpsertSchedules: OK
14. cronParser var: OK
15. TestMissedWindowLatestOnly: OK
16. TestSchedulerFires: OK
17. d.run(ctx) in test: OK              # same-package access to unexported run
18. TestPartitionUniqueConstraint: OK
19. partition test references state IN: OK
```

## 下一计划就绪状态

- **Plan 03-06（scheduler 子命令）**已完全解除阻塞。`cmd/platform/scheduler.go` 将：
  1. 从配置构建 `*sql.DB` + `storage.Storage`（来自 `worker.go` 的现有模式）。
  2. 构建 `event.Writer`（Phase 1 路径）。
  3. 构造自己的 `time.Ticker`（独立于 Daemon）。
  4. 每个 tick：在 `for { … }` 循环中调用 `schedule.FireOneSchedule(ctx, store, registry, events, time.Now().UTC())` 直到 `ErrNoDueSchedule`。然后对同一 tick 调用 `sensor.Daemon.RunOnce(ctx)`（plan 03-05 表面）（D-05 单循环架构）。
  5. 睡眠直到下一次 tick + jitter。
  6. 启动时调用 `schedule.UpsertSchedules(ctx, store, asset.Default())` 一次。
  - 本计划不需要新的导出符号——`FireOneSchedule`、`UpsertSchedules` 和 `ErrNoDueSchedule` 都是 plan 03-06 消耗的表面。
- **Plan 03-05（sensor evaluator）**并行安全——未触碰 `internal/sensor/*` 文件。Wave 2 隔离有效。
- **Plan 03-03（priority claim + load test）**并行安全——未触碰 `internal/run/*` 或 `cmd/platform/{worker,materialize}.go` 文件。Wave 2 隔离有效。

---

*Phase: 03-scheduling-sensors-partitions*
*Plan: 04 (scheduler tick loop)*
*Completed: 2026-05-08*
