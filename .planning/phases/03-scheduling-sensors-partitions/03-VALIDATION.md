---
phase: 3
slug: scheduling-sensors-partitions
status: draft
nyquist_compliant: true
wave_0_complete: true
created: 2026-05-08
---

# Phase 3 — 验证策略

> 执行期间反馈采样的每阶段验证契约。
> 真实来源：`03-RESEARCH.md` § 验证架构。

---

## 测试基础设施

| 属性 | 值 |
|----------|-------|
| **框架** | Go testing package (stdlib) + `testify` v1.11.1（已在 `go.mod` 中） |
| **配置文件** | 无——集成测试需要 `DATABASE_URL` 环境变量（镜像 Phase 2 `internal/run/claim_test.go` 模式） |
| **快速运行命令** | `go test ./internal/partition/... ./internal/run/... -count=1 -timeout 30s` |
| **完整套件命令** | `DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./internal/... ./cmd/... -count=1 -timeout 300s` |
| **预计运行时间** | 完整套件约 120 秒（负载测试约增加 60 秒） |

---

## 采样率

- **每次任务提交后：** 运行 `go test ./internal/partition/... ./internal/run/... -count=1 -timeout 30s`
- **每个计划 wave 后：** 运行 `DATABASE_URL=... go test ./internal/... -count=1 -timeout 120s`
- **在 `/gsd-verify-work` 之前：** 完整套件必须通过（包括 1000-backfill+50-normal 负载测试）
- **最大反馈延迟：** 每提交快速运行 30 秒；每个 wave 完整套件 120 秒

---

## 每任务验证映射

> 计划/波/任务 ID 占位符，直到计划器分配它们。下面的映射按需求 → 行为 → 自动化命令 键值。计划器必须在计划生成期间将这些命令附加到相应的 `<automated>` 块。

| 计划（TBD） | Wave（TBD） | 需求 | 决策参考 | 测试下的行为 | 测试类型 | 自动化命令 | 文件存在 | 状态 |
|------------|------------|-------------|--------------|---------------------|-----------|-------------------|-------------|--------|
| TBD | 1 | ORCH-05 | D-01..D-04 | 守护进程启动后 cron 调度的资产在下次计划时间自动触发 | 集成 | `DATABASE_URL=... go test ./internal/schedule/... -run TestScheduler -v` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 1 | ORCH-05 | D-04 | 错过窗口 LatestOnly 恢复发出 `schedule.missed`，正确跳过计数 | 单元 | `go test ./internal/schedule/... -run TestMissedWindowLatestOnly` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 1 | ORCH-05 | D-03 | 无效 cron 表达式在构建时返回错误，而非运行时 | 单元 | `go test ./internal/asset/... -run TestScheduleInvalidCron` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 2 | ORCH-06 | D-05, D-06 | 当 `Sense()` 返回 `Fired=true` 时传感器触发物化 | 集成 | `DATABASE_URL=... go test ./internal/sensor/... -run TestSensorFire` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 2 | ORCH-06 | D-07 | RunKey 去重防止同一 key 的第二次入队 | 单元 | `go test ./internal/sensor/... -run TestSensorRunKeyDedup` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 2 | ORCH-06 | D-07 | 冷却窗口无论 RunKey 如何都防止入队 | 单元 | `go test ./internal/sensor/... -run TestSensorCooldown` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 2 | ORCH-06 | D-08 | `Sense()` 中的 panic 被恢复；`consecutive_failures` 递增 | 单元 | `go test ./internal/sensor/... -run TestSensorPanicRecovery` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 2 | ORCH-06 | D-08 | N 次连续失败后传感器 `disabled_at` 被设置；发出 `sensor.disabled` 事件 | 单元 | `go test ./internal/sensor/... -run TestSensorAutoDisable` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 1 | ORCH-07 | D-09, D-11 | DailyKey/WeeklyKey/MonthlyKey 生成正确的 UTC ISO-8601 字符串 | 单元 | `go test ./internal/partition/... -run TestPartitionKeyGen` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 1 | ORCH-07 | D-11 | ISO 周边界情况：2019-12-30 → `2020-W01` | 单元 | `go test ./internal/partition/... -run TestWeeklyKeyYearBoundary` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 1 | ORCH-07 | D-09 | `KeysBetween(daily, 2024-01-01, 2024-01-31)` 返回 31 个 key；周/月变体已验证 | 单元 | `go test ./internal/partition/... -run TestKeysBetween` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 3 | ORCH-07 | D-10, D-15 | 时间分区回填：每个分区是自己的运行，有自己的 `event_log` 条目 | 集成 | `DATABASE_URL=... go test ./internal/backfill/... -run TestBackfillTimePartition` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 3 | ORCH-08 | D-09, D-16 | CategoryPartitions：每个类别独立；一个失败不阻塞兄弟 | 集成 | `DATABASE_URL=... go test ./internal/backfill/... -run TestCategoryPartitionIndependence` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 1 | ORCH-07 | D-10 | `(asset_name, partition_key) WHERE state IN ('queued','starting','running')` 上的部分唯一索引拒绝重复的进行中分区运行 | 集成 | `DATABASE_URL=... go test ./internal/partition/... -run TestPartitionUniqueConstraint` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 1 | ORCH-07 / ORCH-08 | D-13 | 优先级感知认领：普通运行在回填运行之前被认领（CASE ORDER BY） | 集成 | `DATABASE_URL=... go test ./internal/run/... -run TestClaimPriorityOrdering` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 1 | ORCH-04（Phase 2 回归） | D-13 | **50 goroutine 认领原子性测试** — 在优先级 ORDER BY 更改后必须继续通过 | 集成 | `DATABASE_URL=... go test ./internal/run/... -run TestClaimAtomicity50Goroutines` | ✅ `internal/run/claim_test.go` | ⬜ 待处理 |
| TBD | 3 | ORCH-07 / ORCH-08（延期） | D-13 | **1000-backfill + 50-normal 优先级认领负载测试** — `normal` 先被认领，在 SKIP LOCKED 下无重复认领 | 负载 | `DATABASE_URL=... go test ./internal/run/... -run TestPriorityClaimLoad -timeout 300s` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 1 | ORCH-05/06/07/08 | D-17 | 所有 Phase 3 `event_type` 枚举值被 `event.Writer` 接受 | 单元 | `go test ./internal/event/... -run TestAllPhase3EventTypes` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 3 | ORCH-07 / ORCH-08 | D-14 | 回填 CLI 规范解析：日期范围、逗号列表、单个 key | 单元 | `go test ./internal/backfill/... -run TestParsePartitionSpec` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 3 | ORCH-07 / ORCH-08 | D-14, D-15 | 回填行数守卫拒绝超过 `--max-partitions` 的规范 | 单元 | `go test ./internal/backfill/... -run TestMaxPartitionsGuard` | ❌ Wave 0 | ⬜ 待处理 |
| TBD | 2 | ORCH-05 / ORCH-06 | D-01..D-08 | 调度器子命令优雅关闭在配置的超时时间内排出进行中的 tick | 集成 | `DATABASE_URL=... go test ./cmd/platform/... -run TestSchedulerGracefulShutdown` | ❌ Wave 0 | ⬜ 待处理 |

*状态图例：* ⬜ 待处理 · ✅ 通过 · ❌ 失败 · ⚠️ 不稳定

---

## Wave 0 需求

**Wave 0**（在任何实现任务之前必须运行）：创建测试桩以暴露缺失的引用，以便下游任务有地方锚定 `<automated>` 验证。

> **注意（计划后）：** Wave 0 测试桩**与其各自的实现计划共存**（例如，分区 keygen 测试在 03-02 计划的任务 1 中与生产代码共存；调度器触发测试在 03-04 任务 1 中；传感器评估器测试在 03-05 任务 1 中；等等），而不是在单独的 Wave 0 计划中。每个计划在其创建生产代码的相同任务中创建其 `_test.go` 文件，根据 `tdd="true"` 先编写测试脚手架，由 `<behavior>` 块驱动期望。这满足了 Wave 0 需求（每个 `<automated>` 引用解析到一个在运行时存在的测试文件）而无需专门的 Wave 0 计划。

- [x] `internal/partition/keygen_test.go` — 覆盖 ORCH-07 + ISO 周年份边界情况 → 在计划 03-02 任务 1 中创建
- [x] `internal/partition/strategy_test.go` — Daily/Weekly/Monthly/Category 策略契约 → 在计划 03-02 任务 1 中创建
- [x] `internal/schedule/fire_test.go` — 覆盖 ORCH-05 tick 逻辑 + 错过窗口恢复 → 在计划 03-04 任务 1 中创建（或按 W2 拆分）
- [x] `internal/schedule/missed_test.go` — 带跳过计数的 `schedule.missed` 事件 → 在计划 03-04 任务 1 中创建（或按 W2 拆分）
- [x] `internal/sensor/evaluate_test.go` — 覆盖 ORCH-06 去重 + panic 恢复 + 自动禁用 → 在计划 03-05 任务 1 中创建
- [x] `internal/backfill/submit_test.go` — 覆盖 ORCH-07/08 + D-14 规范解析 + max-partitions 守卫 → 在计划 03-07 任务 2 中创建
- [x] `internal/backfill/independence_test.go` — 失败下的类别/时间分区独立性 → 在计划 03-07 任务 2 中创建
- [x] `internal/run/claim_test.go` — **存在**（Phase 2）。计划 03-03 任务 2 用 `TestClaimPriorityOrdering` 和 `TestPriorityClaimLoad` 扩展它。现有 `TestClaimAtomicity50Goroutines` 必须继续通过。
- [x] `internal/event/types_test.go` — Phase 3 EventType 枚举值覆盖 → 在计划 03-01 任务 3 中创建
- [x] `internal/asset/builder_test.go` — Schedule/Sensor/Partitions 链式构建器方法（扩展现有测试文件） → 在计划 03-02 任务 2 中创建
- [x] `cmd/platform/scheduler_test.go` — 优雅关闭 → 在计划 03-06 任务 3 中创建
- [x] `migrations/2026MMDDHHMMSS_phase3_*.sql` — schedules、sensors、backfills CREATE TABLE + ALTER TABLE runs（partition_key、priority、backfill_id）+ 部分唯一索引 + CHECK 约束 + 角色授权 → 在计划 03-01 任务 1+2 中创建

*框架安装：* 无——`testify` 已在 `go.mod` 中固定。

---

## 仅手动验证

| 行为 | 需求 | 为什么手动 | 测试说明 |
|----------|-------------|------------|-------------------|
| `./platform backfill` CLI 输出的操作员 UX（状态、ID、失败模式） | ORCH-07 / ORCH-08（D-14） | 输出格式和复制粘贴人体工程学是主观的 | 运行 `./platform backfill assets.users --partitions=2024-01-01:2024-01-07`；确认 `backfill_id` 可复制粘贴；运行 `./platform backfill status <id>`；确认进度聚合读取清晰 |
| 多小时守护进程停机下的调度器重启行为 | ORCH-05（D-04） | 需要操作系统时钟或守护进程停机时间 > tick 间隔；在 CI 中难以可靠伪造 | 停止运行中的调度器；将系统时钟提前超过 3 个错过的窗口；重启调度器；确认仅入队 1 个补火运行并发出 `schedule.missed{skipped: 2}` |

---

## 验证签收

- [x] 所有 Phase 3 任务都有 `<automated>` 验证或 Wave 0 依赖
- [x] 采样连续性：没有连续 3 个任务没有自动化验证
- [x] Wave 0 覆盖验证映射中所有缺失的引用（测试桩与各自计划中的生产代码共存——见上文 Wave 0 需求说明）
- [x] 任何测试命令中没有 watch-mode 标志
- [x] 反馈延迟 < 30 秒（快速）/ < 120 秒（完整套件，不包括负载测试）/ < 300 秒（负载测试）
- [x] `nyquist_compliant: true` — 所有 7 个计划都连接了 Wave/任务 ID 和自动化验证命令

**批准：** 批准执行