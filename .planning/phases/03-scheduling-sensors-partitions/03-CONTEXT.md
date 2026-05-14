---
phase: 03-scheduling-sensors-partitions
gathered: 2026-05-08
status: ready-for-planning
---

# Phase 3 调度、传感器与分区 — Context

**收集时间：** 2026-05-08
**状态：** 准备规划

<domain>

## Phase 边界

Phase 3 使 Phase 2 执行引擎自驱动。它添加了*什么入队运行以及如何切片*，同时保持运行执行内核不变。具体来说：

- **Cron 调度器守护进程** — 按用户声明的 cron 表达式自动触发资产物化，跨进程重启持久化
- **事件传感器** — 用户提供的 `Sense(ctx)` 轮询函数，在外部条件变为真时触发物化，具有去重和冷却
- **时间分区** — 按 UTC ISO-8601 窗口键入的每日/每周/每月分区资产
- **类别分区** — 按静态类别键入的分区资产（例如按区域、按客户）
- **回填** — 操作员提交的历史范围或类别集物化，具有优先级隔离声明语义，使回填不能饿死正常调度运行（PITFALLS #6 缓解）
- **DSL 扩展** — `.Schedule(cron)`、`.Sensor(spec)`、`.Partitions(strategy)` 链接到现有构建器

新能力（血缘捕获、Schema diff、治理、RBAC、Web UI）属于 Phase 4+。

Phase 2 执行内核 — DAG executor、FIFO 声明与 `SELECT … FOR UPDATE SKIP LOCKED`、重试引擎、并发 token 池、事件日志写入器 — **不被修改**，除了附加的 schema 更改（`runs.partition_key`、`runs.priority`、声明排序更新）和额外的 `event_type` 枚举值。

</domain>

<decisions>

## 实现决策

### 调度器架构

- **D-01:** 调度器作为新的 `./platform scheduler` 子命令运行，与现有的 `server` / `worker` / `materialize` 并行（扩展 Phase 2 D-02 多模式模式）。调度器将运行入队到 `runs` 表；worker 执行它们。调度器关闭 ⇒ 没有新运行排队，但进行中的运行不受影响。操作员可以独立扩展调度器和 worker 池。

- **D-02:** 调度状态持久化是**惰性的**。新的 `schedules` 表保存 `(asset_name, cron_expr, last_fire_at, next_fire_at, paused_at, ...)`。每个 tick（默认 30s），调度器使用 `SELECT ... FOR UPDATE SKIP LOCKED` 选择 `next_fire_at <= NOW()` 的行，将运行行入队到 `runs`，更新 `last_fire_at` 并重新计算 `next_fire_at`。`runs` 表仅保存*可声明的运行*，从不是未来运行。

- **D-03:** Cron 表达式解析使用 `robfig/cron/v3`（解析器 + `Next()` API only — *不是*其进程内 Cron 调度器）。调度器 tick 循环是自定义的：单个 Postgres 查询驱动所有调度触发。多副本安全来自保护运行声明的相同 `SELECT FOR UPDATE SKIP LOCKED` 原语（Phase 2 D-17）。**无 leader 选举，无 advisory locks。** River **未被引入** — `go.mod` 当前不包含 River，SKIP LOCKED 模型对于 ORCH-05 / ORCH-06 验收标准已足够。

- **D-04:** 错过的调度恢复：**仅重新触发每个调度最近一次错过的窗口**，当调度器在停机后重启时。调度器发出 `schedule.missed` 事件，包含跳过火灾计数，以便操作可观察。避免多小时停机后的运行雪崩。对齐 Dagster 默认行为。（每调度策略覆盖是 deferred v1.x 功能。）

### 传感器模型

- **D-05:** 传感器运行**在相同的 `scheduler` 子命令内**与 cron，共享 tick 循环和 SKIP LOCKED 原语。新 `sensors` 表镜像 `schedules`：`(asset_name, sensor_name, min_interval, last_evaluated_at, last_fired_at, last_run_key, cooldown_until, consecutive_failures, disabled_at, ...)`。每个 tick 选择需要评估的传感器，调用用户的 `Sense(ctx)`，有条件地入队运行。

- **D-06:** 用户面向的传感器契约：
  ```go
  type SensorResult struct {
      Fired   bool
      RunKey  string         // 去重键 — 与上次 fire 相同 => 跳过
      Payload map[string]any // 附加到触发运行的 MaterializeResult.Metadata
  }
  type SensorFunc func(ctx context.Context) (SensorResult, error)
  ```
  构建器：`asset.New("x").Sensor(asset.SensorSpec{Name:"...", MinInterval: 30*time.Second, Cooldown: 5*time.Minute, Sense: senseFn})`。`Payload` 成为未来 Phase 4 血缘钩子（与 Phase 2 D-04 `MaterializeResult.Metadata` 设计一致）。

- **D-07:** 两层去重 — RunKey 比较**和** cooldown 窗口（belt-and-suspenders 防止用户代码中的 bug）。如果 `RunKey == last_run_key` ⇒ 不入队。如果 `NOW() < cooldown_until` ⇒ 无论键如何都不入队。`Cooldown` 默认为 `0`（关闭，可选）。

- **D-08:** `Sense()` 错误处理：记录结构化错误，在 `event_log` 中发出 `sensor.evaluation_failed` 事件，下一个 tick 重试（不创建失败的运行行——传感器错误是基础设施噪声，不是数据工作）。在 `consecutive_failures >= N`（可配置，默认为 60）后，设置 `sensors.disabled_at = NOW()` 并发出 `sensor.disabled` 事件。操作员必须手动重新启用。在不炸运行表的情况下突出显示损坏的传感器。

### 分区模型和 DSL 组合

- **D-09:** 分区通过单个 `.Partitions(spec)` 方法声明，其中 `spec` 实现类型化的 `asset.PartitionStrategy` 接口。v1 策略：
  - `asset.DailyPartitions{Start, TZ}`
  - `asset.WeeklyPartitions{Start, TZ}` (ISO week)
  - `asset.MonthlyPartitions{Start, TZ}`
  - `asset.CategoryPartitions{Keys []string}`

  每个资产最多一个策略。`MaterializeFunc` 通过 `io.PartitionKey() string` 在现有 `AssetIO`（Phase 2 D-04 surface）上了解其分区。未来策略（动态/数据库驱动）通过添加新类型插入；构建器方法不变。

- **D-10:** 分区运行持久化为**现有 `runs` 表中的行**，带有新的可空 `partition_key VARCHAR(128)` 列。非分区运行将其留为 `NULL`。新的唯一约束 `(asset_name, partition_key)` 作用域为进行中状态（`queued`、`starting`、`running`）防止重复并发运行相同分区。现有声明查询、重试、event_log、heartbeat reaper 均无变化。验收标准 3（"每个分区有自己的事件日志条目"）免费获得——每个运行已经通过 `run_id` 发出自己的 `run.*` 事件。

- **D-11:** 时间分区键是 **UTC 窗口起始 ISO-8601 字符串**：
  - Daily: `2024-01-15`
  - Weekly: `2024-W03`
  - Monthly: `2024-01`
  - Category: 用户提供的键字符串（例如 `us`、`eu`、`apac`）

  分区规范上的 TZ 用于*cron 对齐和显示*，不用于键编码 — 存储保持 UTC 以避免 DST 陷阱。

- **D-12:** 构建器方法组合正交。所有组合都有效：
  - `.Schedule(cron).Partitions(daily)` — cron 触发为*当前*分区窗口入队运行（例如昨天的每日分区）
  - `.Sensor(spec).Partitions(...)` — `SensorResult` 可能包含显式 `PartitionKey` 以触发一个特定分区；如果为空，触发最新的当前分区
  - `.Schedule(cron).Sensor(spec).Partitions(daily)` — 两个触发器独立活动
  - `.Retry(...)` 和 `.Resource(...)` 无论运行如何触发都适用于每个运行

### 回填（PITFALLS #6 — 回填 API 发货前的必需项）

- **D-13:** 三层回填隔离：
  1. **优先级列** — `runs.priority` 枚举：`critical | normal | backfill`（默认 `normal`）。存储为 VARCHAR(16) 带 CHECK 约束，镜像 Phase 2 `state` 列模式（D-17）。
  2. **优先级然后 FIFO 声明** — `ClaimNext` 查询变为 `ORDER BY priority ASC, queued_at ASC`（优先级枚举在内部映射为整数；`critical=0, normal=1, backfill=2`）。正常运行总是在排队回填运行之前抢占，而不更改 SKIP LOCKED 原子性保证。**现有 50-goroutine 声明原子性测试（Phase 2 D-17）必须在新的 ORDER BY 下继续通过。**
  3. **并发 token 池资源标签** — 回填运行额外从现有 `concurrency_tokens` 表获取 `backfill` 加权资源（Phase 2 D-16）。每个资产 token weight 默认为 `1`；池容量 `max_concurrent_backfill` 默认为 `5`，可按资产和全局配置。结合防止队列位置饥饿**和**连接器饱和（PITFALLS #6 的两个方面）。

- **D-14:** 回填通过新的 `./platform backfill <asset> --partitions=<spec> [--priority=backfill]` CLI 子命令提交。Spec 接受：
  - 日期范围：`--partitions=2024-01-01:2024-12-31`
  - 逗号列表：`--partitions=us,eu,apac`
  - 单个键：`--partitions=2024-01-15`

  立即返回 `backfill_id`。状态通过 `./platform backfill status <backfill_id>` 轮询。CLI 是 v1 表面；REST 端点是 Phase 6 UI 依赖，不是 Phase 3 工作。

- **D-15:** 回填分块策略：**立即入队所有分区运行，依靠 `max_concurrent_backfill` 限制进行中 cap。** 提交将每个分区行与 `priority='backfill'` 和共享 `backfill_id`（新列）插入 `runs`。声明排序 + token 池容量确保任何时候只有 N 个进行中。权衡被接受：一个 365 分区回填立即创建 365 个 `runs` 行，但进度 trivially 可查询（`SELECT state, count(*) FROM runs WHERE backfill_id=$1 GROUP BY state`），重试正常工作，没有批处理协调 goroutine 来崩溃恢复。

- **D-16:** 每分区失败语义：**分区失败是独立的。** 失败的分区不会停止同一回填中的兄弟分区（验收标准 4）。每个分区运行按其自己的每个资产重试策略生存或死亡（Phase 2 D-15）。回填摘要视图聚合终端状态；操作员通过提交限定于失败子集的新回填来重新运行失败者。没有回填特定的重试逻辑——那会 shadow `Retry(...)` 并混淆语义。

### 事件日志添加

- **D-17:** 添加新的 `event_type` 枚举值以扩展 Phase 1 D-10 / Phase 2 D-18：
  - **调度：** `schedule.fired`、`schedule.missed`、`schedule.paused`、`schedule.resumed`
  - **传感器：** `sensor.evaluated`、`sensor.fired`、`sensor.evaluation_failed`、`sensor.disabled`、`sensor.cooldown_skipped`、`sensor.dedup_skipped`
  - **回填：** `backfill.submitted`、`backfill.run_enqueued`、`backfill.completed`
  - **分区：** 分区生命周期已被标准 `run.*` 事件覆盖；不需要新类型（运行行携带 `partition_key`）。

  所有新类型遵循 Phase 1 D-09 RLS 不可变性规则 — 追加专用，应用 DB 用户的 UPDATE/DELETE 权限无。

### Claude 的自主决定

- 精确 tick 循环 timing 容差：调度器 tick 间隔默认为 30s；允许的抖动和 `next_fire_at` 重新计算的精确 SQL 是实现细节。
- `schedules` 和 `sensors` ent 实体是否共享基础 mixin 或保持独立。
- 优先级枚举映射（string ⇄ int）的内部布局 — 必须一致但精确表示是开放的。
- `./platform backfill status` 的 CLI 输出格式（纯文本 vs 结构化）— 选择一个，发货一个。
- `backfill_id` 是 UUID、时间戳前缀字符串还是可排序 ID — 操作员需要复制/粘贴它；用户 UX 调用。
- 传感器 `consecutive_failures` 是否在第一次成功评估时重置或需要操作员明确重置 — 默认为成功时自动重置，除非测试数据另有说明。
- `WeeklyPartitions` 默认是 ISO 周（周一到周日）还是locale 周 — 选择 ISO（D-11 已经暗示它）并记录。

</decisions>

<canonical_refs>

## 规范参考

**下游 agents 必须在规划或实施前阅读这些。**

### 需求和路线图
- `.planning/REQUIREMENTS.md` — Phase 3 范围内：ORCH-05 (cron)、ORCH-06 (sensors)、ORCH-07 (time partitions)、ORCH-08 (category partitions)
- `.planning/ROADMAP.md` §Phase 3 — 四个验收标准 + 依赖 Phase 2

### 项目上下文
- `.planning/PROJECT.md` §关键决策 — 并发 token 池单一池授权（通过 D-13 延续）
- `.planning/phases/01-infrastructure/01-CONTEXT.md` — Phase 1 决策：D-09 事件日志 RLS 不可变性，D-10 event_type 枚举扩展模型
- `.planning/phases/02-execution-engine/02-CONTEXT.md` — Phase 2 决策：D-01 构建器 DSL、D-02 多模式二进制、D-04 MaterializeResult.Metadata 钩子、D-14..D-18 重试/并发/声明/事件扩展

### 研究（规划前必读）
- `.planning/research/ARCHITECTURE.md` §SchedulerDaemon (line 42) — Dagster 调度器架构参考
- `.planning/research/ARCHITECTURE.md` §Scheduler (line 433) — 调度器职责和依赖
- `.planning/research/PITFALLS.md` §陷阱 6 (line 137) — 回填资源隔离：回填 API 发货前*需要*优先级队列（通过 D-13/D-14/D-15/D-16 缓解）
- `.planning/research/PITFALLS.md` §陷阱 1 (line 15) — 运行状态原子性：声明 ORDER BY 更改必须保留 50-goroutine 原子性测试（D-13 约束）
- `.planning/research/PITFALLS.md` §陷阱 2 (line 40) — 单一并发池授权延续到回填资源标记（D-13 第 3 层）
- `.planning/research/PITFALLS.md` 阶段特定警告 (line 300) — 明确的 Phase 3 调度器/并发/回填警告表
- `.planning/research/PITFALLS.md` 外部链接 — LakeFS 回填指南 (https://lakefs.io/blog/backfilling-data-foolproof-guide/)
- `.planning/research/STACK.md` — 注意：River 已记录但*未安装*（D-03 确认保持关闭 River for v1）

### 技术栈和约定
- `CLAUDE.md` §技术栈 — robfig/cron/v3 是可接受的 cron 解析器；River 已记录但 D-03 故意保持关闭 River
- `CLAUDE.md` §备选方案对比 — 确认拒绝进程内插件、GORM、Gin、Fiber（无 Phase-3 特定反转）

### Phase 2 代码（Phase 3 构建的冻结契约）
- `internal/asset/builder.go`、`asset.go` — 构建器 DSL 表面扩展 `.Schedule(...)`、`.Sensor(...)`、`.Partitions(...)`
- `internal/asset/io.go` — `AssetIO` 接口扩展 `PartitionKey() string`
- `internal/run/claim.go` — FIFO 声明查询更新优先级感知 ORDER BY（D-13）
- `internal/run/lifecycle.go`、`state.go` — 状态机（无转换更改；仅 event_type 添加）
- `internal/runtime/executor.go` — executor 未更改；读取带新字段的声明运行并转发 `partition_key` 到 `AssetIO`
- `internal/event/event.go`、`writer.go`、`types.go` — 事件写入器重用；`event_type` 枚举扩展（D-17）
- `internal/concurrency/` — token 池重用于回填资源标签（D-13 第 3 层）
- `cmd/platform/main.go`、`factories.go` — 与现有 server/worker/materialize 并列添加 `scheduler` 和 `backfill` 子命令
- `migrations/20260507120000_phase2_run_tables.sql` — `runs` 表扩展 `partition_key`、`priority`、`backfill_id` 列 + 唯一约束

### 外部参考
- robfig/cron/v3: https://pkg.go.dev/github.com/robfig/cron/v3 — cron 表达式解析器和 D-03 使用的 `Next()` API
- Dagster scheduling docs: https://docs.dagster.io/concepts/partitions-schedules-sensors — 分区和传感器模型参考
- Dagster issue #25743 (concurrency layering deadlock) — 通知 D-13 单一池重用
- Dagster issue #15155 (duplicate runs in backfills) — 通知 D-13 ORDER BY + 原子性保留

</canonical_refs>

<research_refs>

## 现有代码洞察

### 可重用资产（来自 Phase 2）
- **`asset.Builder` / `asset.Asset`** — Phase 3 用三个新的链式构建器方法扩展（`Schedule`、`Sensor`、`Partitions`）；SDK API 添加，无破坏性更改。
- **`asset.AssetIO`** — Phase 3 添加 `PartitionKey() string` 访问器；现有 `Read`/`Write` 语义不变。
- **`run.ClaimNext` / `runs` 表** — Phase 3 添加 `partition_key`、`priority`、`backfill_id` 列和优先级感知 ORDER BY。原子性保证（50-goroutine 测试）必须通过更改保留。
- **`runtime.Executor`** — 未更改。读取带新字段的声明运行并将 `partition_key` 转发到 `AssetIO`。无新生命周期状态。
- **`concurrency.Pool`** — 重用于回填资源标签；无 API 更改。
- **`event.Writer`** — 重用；只有 `event_type` 枚举（Phase 1 D-10）获得 D-17 的新值。
- **`storage.Storage` / ent client** — Phase 3 通过 ent + Atlas 模式添加三个新 ent 实体（`Schedule`、`Sensor`、`Backfill`）和运行表列添加的迁移（Phase 1 D-04）。
- **`cmd/platform/main.go` switch** — Phase 3 添加两个新案例：`scheduler` 和 `backfill`（与现有 server/`worker/`materialize 形状的编译时一致性）。

### 建立的模式
- `SELECT … FOR UPDATE SKIP LOCKED`（Phase 2 D-17）是通用多副本安全原语 — Phase 3 重用于调度火灾和传感器评估。
- 子命令每模式二进制（Phase 2 D-02）— 添加 `scheduler` 和 `backfill` 子命令是纯附加的。
- 功能型构建器 + `.Register()`（Phase 2 D-01）— Phase 3 添加链式方法；方法调用顺序仍然无关。
- 带 PostgreSQL RLS 的追加专用事件日志（Phase 1 D-09）— Phase 3 事件遵循相同模型；`event_log` 上无 UPDATE/DELETE。
- ent + Atlas 迁移与手动管理的 CHECK 约束和角色授予（Phase 2 迁移 `20260507120000_phase2_run_tables.sql`）— 新 schedules/sensors/backfills 表的相同模式。

### 集成点
- `internal/asset/` — DSL 扩展（Schedule/Sensor/Partitions 构建器方法，ent-free Go 类型）
- `internal/schedule/`（新建）— 调度器 tick 循环、schedules 表 CRUD、遗漏窗口检测
- `internal/sensor/`（新建）— 传感器评估循环共享调度器子命令 goroutine 池
- `internal/partition/`（新建）— 分区策略（Daily/Weekly/Monthly/Category）、分区键生成、验证
- `internal/backfill/`（新建）— 回填提交服务（CLI 处理程序）、分区规范解析、大量入队逻辑
- `internal/run/claim.go` — 修改 ORDER BY 添加 `priority ASC` 首先；保留原子性测试
- `cmd/platform/scheduler.go`（新建）— `scheduler` 子命令入口点，将调度和传感器循环捆绑在一起
- `cmd/platform/backfill.go`（新建）— `backfill` 和 `backfill status` 子命令处理程序
- `migrations/2026MMDDHHMMSS_phase3_*.sql` — schedules、sensors、backfills 表 + 运行列添加

</research_refs>

<specifics>

## 具体想法

- **无 River。** Phase 2 记录 River 作为队列但从未安装。Phase 3 故意保持在 SKIP LOCKED + heartbeat reaper 模型上。如果 Phase 4+ 拉入 River 进行事务收件箱模式或 Web UI，则重新考虑；不是 Phase 3 工作。
- **调度器和传感器共享一个守护进程。** 两者都是"评估到期行并入队运行"的循环，相同的原语。拆分为两个二进制文件对于 v1 来说是操作过度。
- **分区键在 `runs.partition_key`，不在单独的表中。** 最小的 schema 更改，让每个现有运行机制（声明、重试、heartbeat reaper、事件日志）无修改地处理分区运行。验收标准 3 的承诺"每个分区有自己的事件日志条目"自动满足，因为每个分区是自己的运行。
- **三层回填隔离是不可协商的。** PITFALLS #6 明确说优先级队列必须在回填 API 发货前设计。结合优先级声明排序 AND token 池资源标签，防止队列位置饥饿和连接器饱和；任一 alone 都会失败于坑洞的另一半。
- **50-goroutine 声明原子性测试（Phase 2 验证交付物）必须继续通过。** D-13 的声明 ORDER BY 更改是附加的（只添加主排序键）；SKIP LOCKED + CHECK + WHERE state='queued' 三重 guard 保持完整。Phase 3 计划必须将该测试作为验收的一部分重新运行。
- **传感器 `Payload` 是 Phase 4 血缘钩子。** 镜像 Phase 2 D-04 推理：现在是结构自由的 `map[string]any`，血缘扩展稍后读取。
- **时间分区存储 UTC 字符串。** 规范上的 TZ 用于 cron 对齐和显示，不用于键身份。DST 陷阱通过构造避免。

</specifics>

<deferred>

## 推迟的想法

- **每调度错过的火灾策略（`OnMissed: Skip | LatestOnly | All`）** — D-04 仅发货 LatestOnly。如果真实用户遇到它，每资产覆盖是 v1.x 改进。
- **REST `/backfills` 端点** — Phase 6 UI 依赖，不是 Phase 3 范围。CLI 是 v1 回填提交表面。
- **基于游标的传感器契约** — `func(ctx, cursor) (fires []SensorFire, newCursor, error)` 对于高容量源（S3-new-objects、Kafka offsets）更强大，但 D-06 首先发货更简单的每 tick 单次火灾契约。当第一个用户达到限制时重新考虑游标传感器。
- **传感器 + 分区 `SensorResult.PartitionKey` 语义** — D-12 说允许；确切的键被省略时的行为（默认为"当前最新"分区）是规划细节，可能移动到 D- 当规划者固定它。
- **动态分区策略** — D-09 为 `DynamicPartitions`（数据库驱动的类别列表）留出空间，但 v1 仅发货静态 `CategoryPartitions{Keys}`。当真实用例出现时重新考虑动态策略。
- **分区依赖映射** — 当分区下游资产读取分区上游资产时，如何完成上游分区选择？（相同键？窗口连接？用户提供的 PartitionMapping？）Phase 3 发货独立分区资产；分区到分区依赖映射推迟到 Phase 4（血缘），在那里分区感知 DAG 语义自然地共同设计。
- **分区暂停/禁用** — 操作员驱动的调度/传感器暂停在 schema 中捕获（每 D-02 的 `paused_at` 列），但 CLI/REST 暂停表面是 Phase 6 UI 工作。
- **传感器 secret/凭证注入** — D-05 传感器在调度器进程内运行，依赖与 Phase 2 D-09 用于连接器凭证的相同 env-var 插值配置。Vault/KMS 集成仍是 v2 关注点（Phase 2 推迟列表）。
- **回填取消** — 操作员可能想要取消进行中的回填。Phase 3 v1 依赖于每运行取消（已由 Phase 2 状态机支持：`running → canceled`）。批量按 backfill_id 取消是 v1.x 便利功能。
- **River 迁移** — 如果 Phase 4+ 从 River 的事务收件箱或 Web UI 中受益，重新考虑。Phase 3 保持关闭 River。
- **在重 Backfill 下优先级 FIFO 声明的负载测试** — 50-goroutine 测试（Phase 2）覆盖原子性。单独的负载配置（1000 回填行 + 50 正常行 + 50 并发声明者断言正常运行首先被声明）是推荐的 Phase 3 验证产物，由 D-13 调用但尚未规划。

</deferred>

---

*Phase: 03-scheduling-sensors-partitions*
*Context gathered: 2026-05-08*
