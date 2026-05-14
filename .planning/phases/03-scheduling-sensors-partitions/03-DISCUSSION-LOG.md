# Phase 3: 调度、传感器与分区 - 讨论记录

> **仅作审计跟踪之用。** 不得作为规划、研究或执行代理的输入。
> 决策记录在 CONTEXT.md 中——本日志保留所考虑的替代方案。

**日期：** 2026-05-08
**阶段：** 03-scheduling-sensors-partitions
**讨论领域：** 调度器架构、传感器模型、分区 + DSL 组合、回填优先级 + 隔离

---

## 调度器架构

### Q1: 调度器守护进程应该在哪里运行？

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 新的 `scheduler` 子命令 | 与 server/worker/materialize 并列的 `./platform scheduler`。清晰分离，可独立扩展。符合 D-02 多模式模式。 | ✓ |
| 内嵌在 `worker` 中 | 每个 worker 中的进程内调度器 goroutine。需要 leader 选举以避免多副本重复触发。 | |
| 内嵌在 `server` 中 | 在 API 服务器进程中。将 data-plane 混入 control-plane 节点；仍需 leader 选举。 | |

**用户选择：** 新的 `scheduler` 子命令
**备注：** 与 Phase 2 D-02 单二进制多模式模式保持一致；调度器宕机 ≠ worker 宕机。

### Q2: 如何持久化即将到来的计划运行？

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 懒加载 `schedules` 表持有 (asset_name, cron_expr, last_fire_at) | 每次 tick 选择到期行，入队 `runs`。`runs` 表仅持有可认领的行。通过持久化的 last_fire_at 支持重启。 | ✓ |
| 积极预入队并带有 `not_before` | 预插入未来运行；修改认领查询以过滤 `not_before <= NOW()`。膨胀 `runs` 表；改变认领语义。 | |
| 混合：懒加载 + 预入队立即下一个 | 懒加载持久化 last_fire_at，预先入队每个调度正好一个即将到来的运行。中间路线；更多移动部件。 | |

**用户选择：** 懒加载 `schedules` 表
**备注：** 最小爆炸半径——保持 Phase 2 D-17 原子性保证不变。`runs` 继续表示"可立即认领"。

### Q3: 哪个库实现 cron tick？

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 使用 `robfig/cron/v3` 仅用于解析的自定义 DB 后台循环 | 使用解析器 + `Next()` API；tick 由 SELECT FOR UPDATE SKIP LOCKED 驱动。与运行认领使用相同的原语。多副本安全，无需 leader 选举。 | ✓ |
| 引入带有 PeriodicJobs 的 River | 添加 riverqueue/river（当前不在 go.mod 中）。重大架构转变；替换或覆盖当前认领模型。 | |
| robfig/cron 进程内调度器带 leader 选举 | robfig 的完整 Cron + Postgres  advisory lock 用于领导权。单 leader 瓶颈；新故障模式（锁过期、脑裂）。 | |

**用户选择：** 自定义 DB 后台循环，仅使用 robfig/cron/v3 解析器
**备注：** 确认 Phase 3 保持在 River 之外用于 v1。SKIP LOCKED 是通用的多副本安全原语。

### Q4: 错过调度的补火

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 仅触发最近错过的窗口 | 记录"错过了 N"，入队一个补火。避免运行雪崩。Dagster 默认。 | ✓ |
| 触发所有错过的窗口 | 每次触发都是有意义的。风险：5 分钟 cron 12 小时中断 = 144 次运行。 | |
| 每个调度策略：OnMissed Skip/LatestOnly/All | 最灵活。最大测试面。 | |

**用户选择：** 仅触发最近错过的窗口
**备注：** 每调度策略覆盖延期至 v1.x 完善。

---

## 传感器模型

### Q1: 传感器评估在哪里执行？

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 在 `scheduler` 子命令中与 cron 一起 | 相同的守护进程，相同的 DB 后台锁模式。复用 schedules 基础设施。 | ✓ |
| 单独的 `sensor` 子命令 | 独立扩展用于重型传感器（长外部 API 超时）。更多移动部件。 | |
| 传感器评估作为常规运行类型 | 每次 tick 是由 worker 执行的排队运行。最大一致性；高事件日志量。 | |

**用户选择：** 在 `scheduler` 子命令中
**备注：** 传感器和 cron 都是相同的原语上"评估到期行并入队运行"的循环。

### Q2: 用户面向的传感器契约

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| `func(ctx) (SensorResult{Fired, RunKey, Payload}, error)` | RunKey 支持平台端去重。Payload 是 Phase 4 数据溯源钩子。 | ✓ |
| `func(ctx) (bool, error)` 简单布尔值 | 最小化，无去重，所有逻辑推入用户代码。 | |
| `func(ctx, cursor) (fires []SensorFire, newCursor, error)` 基于 cursor | 功能强大用于 S3/Kafka 高容量源。对于典型传感器过度设计。 | |

**用户选择：** 带有 Fired / RunKey / Payload 的 SensorResult 契约
**备注：** 基于 cursor 的变体延期至第一个用户遇到更简单模型限制时。

### Q3: 去重方案

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 基于 RunKey 去重 + 最小冷却时间 | 多重保障。RunKey 与上次相同 = 跳过；或冷却时间激活 = 跳过。 | ✓ |
| 仅基于 RunKey 去重 | 更干净，信任用户返回不同的 key。 | |
| 仅冷却时间（无 RunKey） | 强制每个传感器设置冷却时间。失去按身份去重的用例。 | |

**用户选择：** RunKey + 冷却时间
**备注：** 冷却时间默认为 0（关闭，可选）；RunKey 是主要机制，冷却时间是安全网。

### Q4: Sense() 错误处理

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 记录 + 发出 `sensor.evaluation_failed` 事件，下一个 tick 重试 | 视为基础设施噪音。连续 N 次失败后自动禁用（默认 60）。 | ✓ |
| 将传感器错误视为失败的运行 | 最大可见性；用基础设施非数据失败污染 runs 表。 | |
| 静默重试（无事件） | 最安静，最差可观测性。 | |

**用户选择：** 记录 + 事件 + 重试，超过阈值后自动禁用
**备注：** 自动禁用大声暴露卡住的传感器。需要操作员手动重新启用。

---

## 分区 + DSL 组合

### Q1: 分区 DSL 形态

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 单个 `.Partitions(spec)` 类型化策略接口 | DailyPartitions / WeeklyPartitions / MonthlyPartitions / CategoryPartitions。新策略通过添加类型插入。 | ✓ |
| 单独的 `.TimePartitions(...)` 和 `.CategoryPartitions(...)` 方法 | 两个方法，无抽象。锁定未来分区类型而不 API 更改。 | |
| 泛型 `.Partitions(keys []string, kind PartitionKind)` | 用户传递原始 key；将时间展开 + 时区正确性推入用户代码。 | |

**用户选择：** 单个 `.Partitions(spec)` 类型化策略
**备注：** 策略模式为以后添加 `DynamicPartitions` 等留出空间而不破坏构建器。

### Q2: 分区运行持久化

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 在 `runs` 添加 `partition_key VARCHAR(128) NULL` | 最小 schema 更改。现有认领/重试/event_log 全部不变。 | ✓ |
| 新建 `partition_runs` 表链接到 `runs` | 更干净的分离；新生命周期代码；JOIN 开销。 | |
| 将分区编码到 asset_name (`sales:2024-01-01`) | 无 schema 更改。破坏 asset-name 语义；对 Phase 4+ 数据溯源/治理混乱。 | |

**用户选择：** 在 `runs` 添加 `partition_key` 列
**备注：** 加上 `(asset_name, partition_key)` 唯一约束，限定在 in-flight 状态范围内以防止重复并发运行。

### Q3: 时间分区 keying

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| UTC 窗口开始 ISO-8601 字符串 (`2024-01-15`, `2024-W03`, `2024-01`) | 避免 DST 地雷。符合 Dagster/dbt 约定。时区在 spec 上仅用于 cron/显示。 | ✓ |
| 时区感知的 key 字符串 (`2024-01-15America/New_York`) | 最大显式；长字符串；比较需要解析。 | |
| Unix 纪元秒数 | 最简单的内部存储；失去人类可读性。 | |

**用户选择：** UTC ISO-8601 字符串
**备注：** 存储保持 UTC；时区感知行为通过 spec 而非 key 提供。

### Q4: 与其他构建器方法的组合

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 所有正交组合 | Schedule + Sensor + Partitions + Retry + Resource 共存。镜像 Dagster。 | ✓ |
| 仅 Schedule + Partitions；Sensor + Partitions 在 v1 中不允许 | 延期罕见组合。失去传感器触发的回填模式。 | |
| 互斥触发器 | 最严格。强制干净的资产设计。失去灵活性。 | |

**用户选择：** 所有正交组合
**备注：** Schedule 触发当前窗口分区；Sensor 返回可选 `PartitionKey`；两者可独立激活。

---

## 回填优先级 + 隔离

### Q1: 回填隔离策略

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 优先级列 + 优先级然后 FIFO 认领 + token 池资源标签 | 三层防御：排序优先于队列位置饿死；token 标签防止连接器饱和。 | ✓ |
| 仅 token 池资源标签 | 纯 D-16 复用，无 schema 更改。FIFO 认领仍受行首阻塞。 | |
| 仅优先级列 | 便宜的认领重新排序。不能解决 PITFALLS #6 的连接器饱和半问题。 | |

**用户选择：** 三层（优先级列 + 认领 ORDER BY + token 池标签）
**备注：** 每层解决 PITFALLS #6 的不同半。Phase 2 的 50 goroutine 原子性测试必须在 ORDER BY 更改后继续通过。

### Q2: 回填提交 API

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| CLI 子命令 `./platform backfill <asset> --partitions=<spec>` | 镜像 `materialize` 子命令。立即返回 `backfill_id`。 | ✓ |
| 仅 REST `POST /backfills` | 更小的二进制表面；开发循环不便。 | |
| CLI 和 REST 两者 | 更多测试面；与 API 优先一致。 | |

**用户选择：** CLI 子命令
**备注：** REST 端点延期至 Phase 6 UI 工作。

### Q3: 回填分块

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 可配置 `max_concurrent_backfill`（默认 5）；立即入队全部 | 插入全部 365 行；认领限制 + token 池强制并发。简单的进度查询。 | ✓ |
| 批量入队：调度器一次入队 N 个，等待，重复 | 回填中途更小的 `runs`；新的批量协调器 goroutine 用于崩溃恢复。 | |
| 每个分区单一入队，并行性仅通过并发 token | 恢复到 PITFALLS #6 故障模式。 | |

**用户选择：** 立即入队全部，通过 `max_concurrent_backfill` 和 token 池限制
**备注：** 365 行 upfront 是可接受的。可通过 `GROUP BY state` 查询进度。重试正常工作。

### Q4: 每个分区失败语义

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 继续——每个分区独立；失败可见且可重新运行 | 映射到验收标准 4。操作员重新提交失败子集。 | ✓ |
| 首次失败停止；取消剩余 | 保守；对因果排序分区有用；对瞬时 flake 嘈杂。 | |
| 重试然后继续 | 遮蔽现有每个资产重试策略。拒绝。 | |

**用户选择：** 继续——分区独立
**备注：** 现有重试策略（Phase 2 D-15）覆盖每个分区重试；不需要回填特定重试层。

---

## Claude 的裁量权

记录在 CONTEXT.md "Claude's Discretion" 小节中。留下未决项目的摘要：
- Tick 循环精确时序容差和 `next_fire_at` 重计算 SQL
- `schedules` 和 `sensors` ent 实体是否共享一个基础 mixin
- 优先级枚举的内部布局（string ⇄ int）
- `./platform backfill status` 的 CLI 输出格式
- `backfill_id` 形态（UUID vs 可排序字符串）
- 首次成功时传感器 `consecutive_failures` 重置语义
- ISO vs locale 周用于 `WeeklyPartitions`（D-11 暗示 ISO）

## 延期想法

记录在 CONTEXT.md `<deferred>` 部分。亮点：
- 每调度错过触发策略覆盖
- REST `/backfills` 端点（Phase 6 UI 依赖）
- 基于 cursor 的传感器契约（高容量源）
- 动态分区策略（数据库驱动的类别列表）
- 分区到分区依赖映射（Phase 4 数据溯源协作设计）
- Schedule/sensor 暂停 CLI/REST 表面（Phase 6）
- Vault/KMS 传感器凭证注入（v2）
- 按 `backfill_id` 批量回填取消
- 可能的 River 迁移（Phase 4+ 如果带来价值）
- 重负载下优先级然后 FIFO 认领的负载测试