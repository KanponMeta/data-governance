---
phase: 02-execution-engine
gathered: 2026-05-07
status: ready-for-planning
---

# Phase 2 执行引擎 — Context

**收集时间：** 2026-05-07
**状态：** 准备规划

<domain>

## Phase 边界

Phase 2 交付将用户定义的资产转化为可靠运行的执行内核，加上七个读取/写入数据的第一方连接器：

- **Asset DSL** — 在用户 Go 代码中定义资产的功能构建器，包含显式上游依赖
- **DAG executor** — 内存拓扑解析（heimdalr/dag）+ River 支持的步骤分发
- **Retry engine** — 带有指数退避的资产级重试策略
- **Concurrency control** — 带有资源标记的单一全局 token 池（避免 Dagster issue #25743 死锁）
- **Run claiming** — `SELECT FOR UPDATE SKIP LOCKED` + 状态枚举 CHECK 约束（原子，无重复）
- **CLI** — 通过 `materialize <asset>` 子命令按需触发资产物化
- **七个第一方连接器** — PostgreSQL、MySQL、BigQuery、Snowflake、S3、GCS、HDFS，全部是 v1.0.0 冻结 `connector.Connector` 接口的进程内实现

新能力（cron 调度、传感器、分区、血缘捕获、治理、UI）属于后续阶段。

</domain>

<decisions>

## 实现决策

### Asset 定义 DSL 和加载

- **D-01:** 功能型构建器 DSL — `asset.New("users_clean").Upstream("users_raw").Connector("postgres-prod").Materialize(fn).Register()`。可变参数 `Upstream(...)`、链式选项、`Register()` 添加到进程级 `DefinitionRegistry`。
- **D-02:** 用户编译自己的二进制文件，链接平台 SDK + 他们的资产定义。带有模式子命令的单一二进制文件：`./myproject server`（REST/UI）、`./myproject worker`（执行运行）、`./myproject materialize <asset>`（CLI 触发）。Go 的静态链接消除了 Dagster gRPC 代码位置两步进程模型的需要。
- **D-03:** 资产按**名称**（字符串）引用连接器，而不是直接配置。平台启动时从配置文件（yaml/toml）加载连接器配置，按相同名称键入。凭证作为环境变量插值传输入配置（例如 `password: $PG_PROD_PASSWORD`）。与 Phase 1 connector.proto 约定一致（敏感值通过环境变量间接传递）。
- **D-04:** `Materialize` 签名：`func(ctx context.Context, input AssetIO) (MaterializeResult, error)`。`AssetIO` 提供 `Read(asset)` / `Write(rows)` 辅助方法，委托给幕后的连接器——用户**不**直接调用 `connector.Read/Write`。`MaterializeResult{RowsWritten, Metadata map[string]any}` 返回有业务意义的计数，是 Phase 4 血缘扩展的钩子。
- **D-05:** 平台通过用户 `init()`/`main()` 中的运行时 registry 调用发现资产——无反射扫描，无代码生成。`worker`/`materialize` 子命令依赖于将资产引入 registry 的相同导入图。

### 第一方连接器打包

- **D-06:** 所有七个第一方连接器（PG、MySQL、BQ、Snowflake、S3、GCS、HDFS）编译为进程内进入平台二进制文件。每个直接实现 `connector.Connector`（扩展 Phase 1 `example_inproc/postgres_stub.go` 模式）。
- **D-07:** `connector.Registry` 支持两个加载器：`RegisterInProcess(name, impl)` 用于第一方（Phase 2）和 `RegisterPlugin(name, pluginPath)` 通过 `hashicorp/go-plugin`（推迟到第一个第三方连接器发货，但接口可访问）。
- **D-08:** 连接器生命周期 = 进程级单例。每个命名连接器在平台启动时初始化一次（例如 `pgxpool.New(...)`），在进程生命周期内保持，并跨所有 materialize 运行重用。连接池存在于连接器实现内部。
- **D-09:** 凭证位于启动配置文件中（yaml 或 toml），按连接器名称键入；secret 字段是环境变量插值（例如 `password: ${PG_PROD_PASSWORD}`）。Phase 2 不含 Vault 集成——那是 v2 的关注点。
- **D-10:** 云连接器 CI 测试使用本地模拟器/fakes——CI 中无真实云凭证：
  - PostgreSQL/MySQL → `testcontainers-go` + 真实容器镜像
  - BigQuery → `goccy/bigquery-emulator`
  - Snowflake → 社区 mock（或仅接口一致性测试如果没有可用的 mock；完整集成成为 nightly job）
  - S3 → LocalStack 或 minio
  - GCS → `fsouza/fake-gcs-server`
  - HDFS → `colinmarc/hdfs` + dockerized HDFS 镜像

### Phase 范围拆分

- **D-11:** Phase 2 保持为 ROADMAP.md 中的**一个阶段**。内部粒度来自多个 `02-N-PLAN.md` 文件。建议的计划分区（规划者可以优化）：
  - `02-01-PLAN.md` — Asset DSL + DefinitionRegistry + AssetIO
  - `02-02-PLAN.md` — DAG executor（heimdalr/dag）+ River 步骤分发 + 运行生命周期（状态机、原子声明）
  - `02-03-PLAN.md` — Retry engine + concurrency token pool + run-event log 添加
  - `02-04-PLAN.md` — PostgreSQL 连接器（参考实现）+ CLI `materialize` 子命令
  - `02-05-PLAN.md` — 其余六个连接器（MySQL、BigQuery、Snowflake、S3、GCS、HDFS）—— 可能是一个带每个连接器子任务计划，因为它们共享接口
- **D-12:** 连接器交付顺序：PostgreSQL 领先（验证架构和验收标准 4）；其他六个是"相同接口的替代实现"批次。
- **D-13:** 所有五个 ROADMAP 验收标准（拓扑执行、重试、并发声明安全、PG-on-CLI 运行、所有 7 个连接器通过集成）必须通过，Phase 2 才能视为完成。

### Retry 和并发设计

- **D-14:** 两层重试：**River 处理基础设施故障**（worker 崩溃、网络闪烁——其原生 `max_attempts` + 重试策略）；**引擎处理业务故障**（Materialize 返回 `error`）。引擎重试计数器和退避是每个资产配置，跟踪在 `event_log`（`run.step.retry_scheduled` 事件）。Worker 重启**不**消耗引擎重试预算。
- **D-15:** 重试策略在资产构建器上声明：`asset.New("x").Retry(asset.RetryPolicy{Max: 3, InitialDelay: 30*time.Second, MaxDelay: 5*time.Minute, JitterPct: 25}).Materialize(fn)`。当资产省略 `Retry(...)` 时，应用平台级默认（也在启动配置中声明）。
- **D-16:** 并发 token 池 = **单一全局 `concurrency_tokens` Postgres 表**，行包含 `(run_id, asset_id, resource_tag, weight, acquired_at)`。运行级、op 级和资源级限制都针对此同一表检查/归还 token。资源标签来自资产构建器：`asset.New("x").Resource("postgres-prod", 1)`（允许多个资源；weight 默认为 1）。这是 PITFALLS #2 要求的单一真相来源——明确拒绝三层分层池。
- **D-17:** 运行声明使用 `SELECT ... WHERE state = 'queued' FOR UPDATE SKIP LOCKED`（PostgreSQL）。`runs` 表状态列有 `CHECK` 约束枚举 `(queued|starting|running|succeeded|failed|canceled)`，禁止通过特权 `reset` 操作进行向后转换。**Phase 2 验证的必要测试：** 生成 50 个并发 goroutine 尝试声明同一个排队运行，并断言恰好一个获胜（覆盖验收标准 3 + PITFALLS #1）。
- **D-18:** 运行生命周期事件日志添加到 Phase 1 `event_type` 枚举：`run.queued`、`run.started`、`run.step.started`、`run.step.succeeded`、`run.step.failed`、`run.step.retry_scheduled`、`run.succeeded`、`run.failed`、`run.canceled`。所有重试——次数、计划时间、错误消息——记录为事件（验收标准 2）。

### Claude 的自主决定

- `AssetIO` 的内部实现（是流式延迟、批处理还是缓冲行）——不影响用户面向契约的性能细节。
- 特定的 River 队列拓扑（一个队列 vs 多个优先级队列）——Phase 2 默认是一个队列，如果回填优先级提前出现，规划者可以重新审视。
- `concurrency_tokens` 表索引的内部布局和获取重试策略——只要契约成立，实施自由度。
- `MaterializeResult.Metadata` 键是否在 Phase 2 中通过常量类型化，还是保持为自由形式的 `map[string]any`——小的 UX 调用。
- 启动配置文件使用 yaml vs toml 的选择——选择一个，承诺它，不需要两者。
- `materialize` CLI 子命令是阻塞直到运行完成（同步）还是立即返回 run-id（异步）——Phase 2 默认**同步，带 `--detach` 标志用于异步**，但如果集成测试强制，规划者可以简化到一种模式。
- Snowflake 是否有可用 mock 或其完整集成测试成为 nightly 真实凭证 job 的决定——取决于规划时 Go 生态系统中存在什么。

</decisions>

<canonical_refs>

## 规范参考

**下游 agents 必须在规划或实施前阅读这些。**

### 需求和路线图
- `.planning/REQUIREMENTS.md` — Phase 2 范围内的需求：ORCH-01、ORCH-02、ORCH-03、ORCH-04、ORCH-09、ORCH-10、CONN-01、CONN-02、CONN-03、CONN-04、CONN-05、CONN-06、CONN-07
- `.planning/ROADMAP.md` §Phase 2 — 验证标准 + 依赖 Phase 1

### 项目上下文
- `.planning/PROJECT.md` §关键决策 — 并发 token 池必须与执行引擎一起设计
- `.planning/phases/01-infrastructure/01-CONTEXT.md` — Phase 1 决策（D-01..D-10），包括连接器 ABI 冻结、带有 RLS 的事件日志设计、存储抽象

### 研究（规划前必读）
- `.planning/research/ARCHITECTURE.md` §1 — Dagster 执行管道（AssetGraph → ExecutionPlan → Step dispatch）；§1.2（资产定义是运行时对象，不是 DB 行）；§1.3（executor 模型）
- `.planning/research/PITFALLS.md` §1 — 运行状态原子性（SKIP LOCKED + CHECK 约束 + 50-goroutine 测试）
- `.planning/research/PITFALLS.md` §2 — 单一并发 token 池，不是分层的（Dagster issue #25743）
- `.planning/research/PITFALLS.md` §6 — 回填资源隔离（推迟到 Phase 3 但设计钩子现在就有）
- `.planning/research/PITFALLS.md` §9 — 连接器 API 稳定性（Phase 1 冻结 v1.0.0；Phase 2 不能破坏它）
- `.planning/research/STACK.md` — 技术栈选择和版本（River、heimdalr/dag、hashicorp/go-plugin）
- `.planning/research/SUMMARY.md` §推荐技术栈 — 高层理由

### 技术栈和约定
- `CLAUDE.md` §技术栈 — River v0.35.x、heimdalr/dag v1.5.x、hashicorp/go-plugin v1.7.x、connectrpc/connect-go v1.19.x、ent v0.14.x、sqlc v1.31.x
- `CLAUDE.md` §备选方案对比 — 明确排除（Temporal、GORM、Gin、Fiber、golang-migrate、Go 原生 plugin）

### Phase 1 代码（Phase 2 构建的冻结契约）
- `internal/connector/connector.go` — `Connector` 接口（冻结 v1.0.0）
- `internal/connector/proto/connector.proto` — proto IDL（冻结 v1.0.0）
- `internal/connector/registry.go` — 连接器 registry（Phase 2 通过进程内加载器扩展）
- `internal/connector/example_inproc/postgres_stub.go` — 进程内模式参考
- `internal/storage/storage.go` — 存储接口、ent client、WithTx
- `internal/storage/ent/` — ent schema（Phase 2 通过 ent schema 和 Atlas 迁移添加 Run、RunStep、ConcurrencyToken 实体）
- `internal/event/event.go`、`writer.go`、`types.go` — 事件日志写入器（Phase 2 添加新事件类型）
- `cmd/platform/main.go` — 当前入口点（Phase 2 添加 `worker` 和 `materialize` 子命令）

### 外部参考
- River queue docs: https://riverqueue.com/ — 用于重试策略 + cron + 事务性入队模式
- heimdalr/dag: https://pkg.go.dev/github.com/heimdalr/dag — 拓扑排序、环检测、遍历
- Dagster issue #15155（运行声明原子性）、#25743（并发分层死锁）— 参考用于测试设计

</canonical_refs>

<code_context>

## 现有代码洞察

### 可重用资产（来自 Phase 1）
- **`internal/connector.Connector`** 接口 — Phase 2 的七个第一方连接器直接实现；ABI 冻结。
- **`connector.Registry`** — Phase 2 通过 `RegisterInProcess(name, impl)` 扩展用于第一方加载。
- **`internal/connector/example_inproc/postgres_stub.go`** — 已经展示了进程内模式（D-06 将此泛化到七个连接器）。
- **`Storage` 接口 + ent client** — Phase 2 通过 ent schema 和 Atlas 迁移添加实体（Run、RunStep、ConcurrencyToken），使用现有模式。
- **`event.Writer`** — Phase 2 重用写入器；只有 `event_type` 枚举获取新值（D-18）。
- **`auth` JWT 中间件** — `materialize` CLI 通过与 REST API 相同的 JWT 路径认证；重用，不重建。
- **`api/router.go`** + `grpc_stub.go` — Phase 2 在现有 chi + connect-go 骨架上填充运行相关路由（触发、状态）的具体处理程序。

### 建立的模式
- ent schema + Atlas 迁移用于所有元数据持久化（Phase 1 D-04 布局）
- HTTP 错误的 RFC 7807 Problem+JSON（Phase 1 D-06）
- 通过 `Storage.WithTx` 的单一事务（运行状态 + event_log 写入一起）
- 追加专用事件，带 PostgreSQL RLS 禁止 UPDATE/DELETE（Phase 1 D-09）— Phase 2 事件遵循相同模型
- 功能型构建器 + Register()（D-01）是 Phase 2 建立的新模式；下游阶段（Phase 3 调度、Phase 4 血缘）将挂在同一 registry 上

### 集成点
- `connector.Connector`（冻结 v1.0.0）— Phase 2 实现在 `internal/connector/firstparty/{postgres,mysql,bigquery,snowflake,s3,gcs,hdfs}/`
- `Storage.Ent()` — Phase 2 添加 Run、RunStep、ConcurrencyToken ent 实体
- `event.Writer` — Phase 2 写入新的 `run.*` 事件类型
- `cmd/platform/main.go` — Phase 2 添加 `server`、`worker`、`materialize` 子命令（Phase 1 已经运行 `start` 用于 HTTP API）
- 新的 SDK 包（可能是 `pkg/asset` 或通过 re-export 导出的 `internal/asset`）— 用户二进制文件导入此包以使用 `asset.New(...)`。SDK 边界使这是平台为外部消费导出的第一个包。

</code_context>

<specifics>

## 具体想法

- **单一二进制文件，多个模式** — 利用 Go 静态链接是简化平台 vs Dagster Python 强制两步 gRPC 模型的核心架构洞察。不要仅仅因为 Dagster 有它就引入代码位置子进程机制。
- **PostgreSQL 连接器是参考实现。** 它是唯一被验收标准固定的连接器（"CLI 命令针对本地 Postgres 成功运行"），已经被 Phase 1 存储层验证，它让我们无需首先协调六个外部系统就能关闭引擎验证循环。
- **带有资源标签的单一并发 token 池是 PITFALLS #2 + PROJECT.md 关键决策规定的。** 拒绝任何规划者对"我们从一个简单的每运行计数器开始，然后添加层"的冲动——那是 Dagster 失败模式。
- **50-goroutine 声明测试是验证交付物，不是 nice-to-have。** 它直接映射到 ROADMAP 验收标准 3。
- **event log 中重试可见性直接映射到 ROADMAP 验收标准 2** — 每个重试尝试和时间戳必须可从 `event_log` 查询。
- **作为 SDK 导出的第一个包** — Phase 2 标志着用户开始导入平台代码的时刻。`asset` 包签名稳定性从此时起很重要；将构建器方法名称视为公共 API。

</specifics>

<deferred>

## 推迟的想法

- **Cron 调度 + 传感器** — Phase 3（ORCH-05、ORCH-06）。
- **时间/类别分区 + 回填** — Phase 3（ORCH-07、ORCH-08）。回填资源隔离（PITFALLS #6）需要优先级队列；Phase 2 只必须不妨碍此——token 池资源标签已经给了我们钩子。
- **go-plugin 第三方连接器加载器** — 接口已保留（D-07）但实际子进程脚手架等待第一个真实第三方连接器需求。不是 v1 验收的一部分。
- **Vault / KMS 凭证集成** — Phase 2 仅使用环境变量插值。Vault 是 v2 的关注点。
- **物化期间 OpenLineage 事件发射** — Phase 4 在此处连线；Phase 2 的 `MaterializeResult.Metadata` 是未来的钩子。
- **仅异步 `materialize` CLI** — 默认同步处理简单情况；`--detach` 在 Claude 酌情权；仅异步是 v1.x 改进。
- **从 Materialize 调用中提取血缘** — Phase 4（LINE-01..LINE-06）。
- **每次物化的 Schema 捕获** — Phase 4（META-01、META-02）。
- **Snowflake 真实凭证 nightly 集成 job** — 仅当没有可用的 Go mock 时；否则通过模拟器策略覆盖（D-10）。

</deferred>

---

*Phase: 02-execution-engine*
*Context gathered: 2026-05-07*