# 项目调研摘要

**项目：** 数据治理 + 编排平台（Go 原生，Dagster 风格）
**领域：** 数据编排 + 治理平台
**调研日期：** 2026-04-29
**置信度：** 高

---

## 执行摘要

本项目填补了一个真实缺口：目前没有任何开源工具能在单个可部署二进制中同时提供可靠的 pipeline 编排和企业级治理能力（字段级血缘、审批工作流、列级访问控制和合规审计链）。Dagster 在编排方面表现优秀，但没有治理原语。DataHub 和 OpenMetadata 处理治理和目录，但无法执行 pipeline。Collibra 全都有，但是闭源且昂贵。Go 原生平台在这个矩阵中占据无竞争对手的位置，拥有清晰且可防御的差异化优势集。

推荐方案是一个单体 Go 二进制（三种运行模式标志：`server`、`worker`、`daemon`），以 PostgreSQL 作为唯一持久化存储，配合内存 DAG 调度器（`heimdalr/dag`）、Postgres 支持的作业队列（River）和子进程隔离的连接器系统（hashicorp/go-plugin）。资产中心模型（Dagster 风格）是正确的心智模型——资产是在启动时注册的一等运行时对象，不存储在数据库中。治理层从第一天起就与执行引擎并行构建；两者共享相同的存储抽象和审计日志。

主要风险是范围和顺序。Dagster issue 历史和失败的开源平台项目的调研证实，尝试同时交付编排 + 目录 + 治理会产生三者都做不好的平台。缓解措施是严格的垂直切片：执行引擎先交付并稳定，之后再添加血缘或治理。第二类主要风险是并发正确性——非原子的运行状态转换和多层并发限制是 Dagster 中有记录的故障模式，必须从第一次提交就使用 `SELECT FOR UPDATE SKIP LOCKED` 和统一并发 token 池来规避。第三类主要风险是列级访问控制：通过自定义查询代理执行会被直连数仓的访问绕过；正确做法是将掩码策略下发到各数仓的原生机制并从平台同步。

---

## 关键发现

### 推荐技术栈

Go 后端由一套小型、高置信度的库集合提供支持。River（`riverqueue/river` v0.35.x）替代 Temporal 作为作业队列——它以 Postgres 为后端，支持事务入队，不需要外部消息代理。`heimdalr/dag` v1.5.x 提供线程安全的内存 DAG 解析。`entgo.io/ent` v0.14.x 负责 Schema 定义和写入（元数据模型是图形结构；ent 的边模型与之直接匹配）；`sqlc` v1.31.x 处理热读路径。`ariga.io/atlas` 处理迁移，提供 `golang-migrate` 无法提供的脏状态恢复。`casbin/casbin` v2.135.x 处理 RBAC + ABAC 列策略，无需自行实现鉴权。`hashicorp/go-plugin` v1.7.x 提供子进程隔离的连接器，与 Terraform 和 Vault 生产环境使用的模型相同。前端：React 19 + TypeScript，ReactFlow v12 用于血缘 DAG，shadcn/ui + Tailwind v4 用于组件，TanStack Query v5 用于服务端状态，Zustand v5 用于 UI 状态。

**核心技术：**
- `riverqueue/river`：基于 Postgres 的作业队列，用于运行分发和 cron——无需外部消息代理
- `heimdalr/dag`：带拓扑排序和循环检测的内存 DAG——线程安全，通用，v1.5.1（2026 年 4 月）
- `entgo.io/ent`：用于图形元数据 Schema 的 ORM——类型安全代码生成，Atlas 迁移集成
- `sqlc`：为热读路径生成类型安全 SQL（目录搜索、血缘遍历、审计导出）
- `ariga.io/atlas`：带脏状态恢复的 Schema 迁移——ent 项目本身推荐
- `casbin/casbin v2`：用于列级访问控制的 RBAC + ABAC 策略引擎——外部化策略模型
- `hashicorp/go-plugin`：进程外连接器子进程管理——崩溃隔离，支持多语言
- `connectrpc/connect-go`：连接器协议和平台 API 的 HTTP/1.1 + HTTP/2 RPC
- `go-chi/chi v5`：net/http 原生 REST 路由器——生态兼容，无框架锁定
- ReactFlow v12：基于节点的血缘图可视化——Dagster 也在用，支持交互式自定义节点

**关键排除项：** Atlas 必须替代 golang-migrate（部分失败时的脏状态是生产事故）。GORM 被排除（基于反射、隐式自动迁移、复杂连接性能差）。Go 原生 `plugin` 包被排除（需要相同二进制版本，无子进程隔离）。Fiber 被排除（fasthttp 破坏 net/http 生态兼容性）。

### 预期功能

功能全景经 Dagster、DataHub、OpenMetadata、Apache Atlas 和 Collibra 验证。

**必须具备（表 stakes）：**
- Go 中的软件定义资产，带显式上游依赖图——核心执行心智模型
- cron + 事件触发调度，可配置重试和退避
- 基于时间和类别的分区，支持回填
- 运行历史、每次运行的执行日志和资产新鲜度（陈旧/新鲜）指标
- 物化时自动捕获 Schema 元数据——无需手动注册
- 资产/列描述 + 标签、全文目录搜索、负责人分配
- 带交互式 UI 的资产级血缘 DAG（表/资产级别）
- Schema 演化追踪（版本间 diff）
- 内置空值/范围/唯一性质量检查，物化时评估
- RBAC 角色模型，不可变审计日志，SSO/OIDC 认证

**应该具备（差异化功能——这些是建立该项目而非直接用 Dagster 的理由）：**
- 字段级血缘：先提供显式 Go API，SQL 解析作为补充——Dagster 将此隐藏在 Dagster+（付费）之后；本项目在开源免费版中提供
- 资产发布审批工作流（草稿 → 待审核 → 已批准/已发布/已驳回）——没有任何开源编排器有此功能
- 带 PII 标签到策略传播和通过血缘向下游继承的列级掩码策略
- 合规级防篡改审计日志（哈希链），带 GDPR/SOC2 导出
- 字段级影响分析（"如果我改变字段 X，哪些下游列会受影响？"）
- 新鲜度 SLA 声明，SLA 违反时触发治理工作流告警

**推迟到 v2+：**
- SQL 推断自动列血缘（从显式 Go API 开始；SQL 解析复杂且方言特定）
- 行级安全（列级是声明的差异化点；行级需要查询代理且高度连接器特定）
- Python SDK（Go 优先；API 稳定后再做 Python 绑定）
- AI 生成元数据 / LLM 描述（设计 Schema 时预留 AI 扩展空间，但推迟功能）
- 多租户 SaaS 托管，托管连接器市场

**硬性先决条件链（路线图必须遵守此顺序）：**
```
资产 + Schema 模型
  → RBAC 角色模型
    → 不可变审计日志
    → 字段级血缘图
      → 资产发布状态
        → 审批工作流
        → 列掩码 + 下游策略继承
```

### 架构方案

平台镜像 Dagster 的拓扑，但折叠为带运行模式标志（`server`、`api`、`worker`、`daemon`）的单个 Go 二进制。资产定义存在于用户代码中（Go 结构体/接口），在启动时注册到内存 `DefinitionRegistry`，永不序列化到数据库——只有执行历史（运行、事件）和派生元数据（Schema、血缘边）存储在 PostgreSQL 中。执行引擎在运行时使用内存 DAG 对资产图进行拓扑排序，通过 River 支持的后台 worker 池分发步骤，并将所有结果写入追加式事件日志。治理引擎与编排引擎并行运行，共享相同的存储层，但有严格的组件边界——`WorkflowFSM` 永不接触 `StepExecutor`，反之亦然。列访问执行位于 `QueryProxy` 组件，在任何连接器看到查询之前重写 SQL；连接器刻意不感知策略。

**主要组件：**
1. **存储抽象层** —— PostgreSQL 之上的 Go 接口（开发用 SQLite）；Atlas 管理 Schema
2. **AssetGraph + DefinitionRegistry** —— 用户定义资产的内存 DAG；启动时从用户代码加载
3. **EventStore + RunStore** —— 追加式事件日志；运行生命周期状态机；所有执行状态的真实来源
4. **RunManager + StepExecutor** —— 运行生命周期管理 + 通过 River 队列的拓扑步骤分发
5. **ConnectorRegistry** —— 编译内置的一方连接器和子进程三方连接器（go-plugin）；`Connector` 接口从第一天起就是稳定的公开 API
6. **Scheduler + SensorEngine** —— cron 守护进程和事件轮询守护进程；两者都写入 RunStore，不直接执行
7. **LineageStore + LineageExtractor** —— 带递归 CTE 遍历的 PostgreSQL 邻接表；SQL AST 解析器用于补充提取；显式 Go API 用于非 SQL 转换
8. **SchemaRegistry** —— 在每次物化时捕获和 diff Schema 版本；为 LineageExtractor 提供数据
9. **WorkflowFSM** —— 资产发布状态机（5 个状态，4 个转换）；手写 FSM，不需要 BPMN 引擎
10. **PolicyStore + QueryProxy** —— PostgreSQL 中的列访问策略；QueryProxy 在连接器执行前重写 SQL AST
11. **AuditLog** —— 哈希链追加式 PostgreSQL 表；行安全策略防止应用用户执行 UPDATE/DELETE
12. **API Server** —— chi REST + connect-go gRPC；UI 用 GraphQL；OpenLineage 摄取用 REST webhook
13. **React UI** —— 资产目录、运行历史、ReactFlow 血缘 DAG、治理工作流 UI、质量仪表盘

**必须在首次使用前稳定的关键接口：**
- `Connector` 接口——公开 API，独立 Go 模块，语义版本，合规测试套件
- `AssetDef` 接口——用户编写代码所针对的 SDK 接口
- 存储接口——使 SQLite 开发模式可以在不改变业务逻辑的情况下切换

### 关键陷阱

1. **非原子运行状态转换导致重复物化** —— 从第一天起在运行认领步骤使用 `SELECT FOR UPDATE SKIP LOCKED`。编写一个 50 个并发 goroutine 同时尝试认领同一排队运行的测试；只有一个能成功。这是 Dagster issue #15155 在生产中的表现。

2. **跨多层的并发限制相互作用导致死锁** —— 在添加任何并发控制之前，先设计一个集中式并发 token 表。不要将运行级并发和操作级并发作为独立功能分别叠加。Dagster issue #25743（v1.8.13+ 已在生产中确认）展示了最终结果：回填永久卡死，无任何错误。

3. **列掩码代理被直连数仓的访问绕过** —— 不要将自定义 SQL 代理作为唯一执行机制。使用各数仓的原生列掩码（Snowflake 动态数据掩码、BigQuery 列级安全），让平台向这些系统下发和同步策略。

4. **审计日志仅按惯例追加，而非密码学保证** —— 在写入第一条审计记录之前实现哈希链（`sha256(prev_hash || record_content)`）——事后补救需要重写所有现有记录。PostgreSQL 行安全策略必须防止应用数据库用户对审计表执行 DELETE/UPDATE。

5. **代码变更后字段级血缘变陈旧** —— 将血缘版本与资产代码哈希绑定。使用不同代码哈希重新物化时，要求血缘重新声明或标记为可能陈旧。与实际情况偏离的静态声明比没有血缘更糟糕。

6. **血缘邻接表在规模下超时** —— 从一开始就使用 PostgreSQL `WITH RECURSIVE`，而非应用层遍历。为所有遍历查询添加深度限制（默认最大深度 10）。在上线血缘 UI 之前在 500K 条边下进行基准测试。

7. **过早交付过宽的范围** —— 执行引擎必须在添加血缘或治理之前先交付并稳定。这是开源数据平台放弃的最常见原因。

---

## 对路线图的影响

基于综合调研，以下阶段结构尊重硬性先决条件依赖并在最早可能的时间点对最危险的陷阱进行去风险。

### 阶段 1：基础设施
**理由：** 所有其他组件都依赖于存储抽象、资产定义类型系统和事件日志。原子运行状态转换必须在此解决——在编写任何调度器逻辑之前。
**交付物：** Go 模块结构、Atlas 迁移、PostgreSQL Schema、`AssetDef` 接口、`DefinitionRegistry`、`EventStore`、带 `SELECT FOR UPDATE SKIP LOCKED` 的 `RunStore`、SQLite 开发模式、CLI 脚手架、在独立模块中定义的 `Connector` 接口（尚无连接器实现）。

### 阶段 2：执行引擎
**理由：** 平台的核心承诺是可靠的 pipeline 执行。并发 token 系统必须在此完整设计——事后添加会产生 Dagster 死锁模式。
**交付物：** `RunManager`、带拓扑分发的 `StepExecutor`、River 作业队列集成、带编译内置 + go-plugin 子进程模型的 `ConnectorRegistry`、PostgreSQL 和 S3 一方连接器、统一并发 token 表、goroutine 泄漏测试（`goleak`）、所有连接器调用中传播的 context 取消。

### 阶段 3：调度、传感器与分区
**理由：** 调度和分区是生产使用的先决条件。回填资源隔离（优先级队列）必须在回填提交 API 之前构建。
**交付物：** 调度器守护进程、传感器引擎、基于时间和类别的分区系统、带分区分块执行的回填、运行优先级队列（`NORMAL`、`BACKFILL`、`CRITICAL`）、守护进程健康心跳。

### 阶段 4：血缘与 Schema
**理由：** 字段级血缘是主要技术差异化点。它必须在治理之前，因为下游列策略继承和 PII 传播依赖完整的血缘图。
**交付物：** 带版本 diff 的 `SchemaRegistry`、`LineageStore`（PostgreSQL 邻接表、递归 CTE、深度限制）、500K 条边基准测试、显式 Go 列血缘 API、可选 SQL AST 解析作为补充信号、与资产代码哈希绑定的血缘版本（带陈旧检测）、影响分析 API、物化上游/下游计数摘要表。

### 阶段 5：治理引擎
**理由：** 治理依赖血缘（下游策略继承）、RBAC 和 Schema 元数据。审计日志哈希链必须在写入第一条审计记录之前构建。
**交付物：** 带 Casbin RBAC 的 `PolicyStore`、列掩码策略、PII 标签到策略传播、数仓原生掩码同步（Snowflake、BigQuery、PostgreSQL）、带 SQL AST 重写的 `QueryProxy`、`WorkflowFSM`（5 个状态，4 个转换）、治理审核表、通知分发、自动批准策略、SLA 升级计时器、带 PostgreSQL 行安全的哈希链 `AuditLog`、GDPR/SOC2 合规导出、数据保留 TTL 策略、带阻塞/非阻塞分类的异步数据质量规则引擎。

### 阶段 6：API 与 Web UI
**理由：** API 和 UI 最后构建，这样底层数据模型才是稳定的。在阶段 1-5 稳定之前构建 UI 的代价是反复的 UI 重写。
**交付物：** chi REST API + connect-go gRPC API + OpenLineage webhook、swaggo OpenAPI 3.0 文档、React + TypeScript SPA（Vite、TanStack Router + Query、Zustand）、带搜索的资产目录、带日志流的运行历史、ReactFlow 血缘 DAG（深度限制，按需展开，字段级下钻）、质量仪表盘（Recharts）、治理工作流 UI、审计日志查看器、shadcn/ui 组件库。

### 阶段排序理由

- 阶段 1-2 是不可协商的优先项——无法在不可靠的执行引擎上构建治理平台。
- 阶段 3 紧随执行，因为回填并发与阶段 2 的 token 系统直接交互；它们必须相邻。
- 阶段 4 紧随阶段 3，因为血缘捕获需要真实运行产生真实 Schema；在物化事件存在之前捕获血缘没有意义。
- 阶段 5 紧随阶段 4，因为下游列策略继承和 PII 传播需要完整的血缘图才有意义。
- 阶段 6 刻意最后——防止随着阶段 1-5 模型变化而反复重写 UI。

### 调研标记

**规划时需要更深入调研的阶段：**
- **阶段 2（连接器框架）：** go-plugin 子进程协议 + connect-go 接口契约需要一个专注的设计探针，然后才能将 `Connector` 接口提交到公开模块。这是不可逆的 API 接口。
- **阶段 4（SQL 血缘提取）：** Go SQL 解析器全景（vitess vs. postgresql-parser vs. ANTLR）需要针对真实查询语料库进行测试。DataHub 使用 SQLGlot（Python）达到 97-99% 的准确率；Go 等价方案未经验证。
- **阶段 5（数仓原生掩码同步）：** Snowflake、BigQuery 和 PostgreSQL 各自有不同的列掩码策略管理 API。在设计 PolicyStore 同步接口之前先调研。

**采用标准模式的阶段（跳过调研阶段）：**
- **阶段 1（基础设施）：** PostgreSQL Schema 设计配合 ent + Atlas 有充分文档。
- **阶段 3（调度）：** Go 中的 cron 守护进程 + 传感器轮询模式是标准做法。
- **阶段 6（UI）：** React + ReactFlow 数据平台 UI 模式有充分文档；一个简短的 ReactFlow 字段级下钻探针即可。

---

## 置信度评估

| 领域 | 置信度 | 说明 |
|------|--------|------|
| 技术栈 | 高 | 所有库版本于 2026 年 4 月在 pkg.go.dev 验证。一个低置信度项：ThijsKoot/openlineage-go 为社区维护；可能需要 vendor 或直接实现事件发送。 |
| 功能 | 高 | 经 Dagster、DataHub、OpenMetadata、Collibra 和 Apache Atlas 文档验证。功能优先级反映已发布的采用模式。 |
| 架构 | 高（执行），中（治理） | Dagster 内部通过 DeepWiki 和官方文档有充分记录。治理架构模式的 Go 特定先例较少。 |
| 陷阱 | 高（执行），中（治理工作流） | 执行陷阱经具体 Dagster GitHub issue（含 issue 编号）验证。治理工作流陷阱有良好来源但 Go 特定先例较少。 |

**总体置信度：** 高

### 待解决的缺口

- **OpenLineage Go 客户端：** `ThijsKoot/openlineage-go` 为社区维护。计划 vendor 它并准备备选方案：OpenLineage JSON 事件格式足够简单，如果客户端不够用，可以直接实现。
- **Go 中 SQL 解析准确率：** DataHub 使用 SQLGlot（Python）在生产查询语料库上达到 97-99% 的列血缘准确率。Go 等价方案尚未针对生产查询语料库进行基准测试。在阶段 4 中测量。
- **数仓原生掩码 API 覆盖：** Snowflake 和 BigQuery 掩码 API 已确认存在。在阶段 5 中设计 PolicyStore 同步接口之前，需要验证具体的 Go SDK 调用。
- **自定义 DAG 调度器 vs. Temporal 的规模扩展：** 阶段 1-3 的正确决策。如果跨独立机器的多 worker 分布式执行成为硬性需求，则重新评估。在阶段 1 中设计存储接口，使调度器后端可以换。
- **Casbin v3 时机：** 固定使用 v2（v2.135.x，稳定）。关注 v3 发布节奏；避免在生态适配器更新之前采用 v3。

---

## 来源

### 主要来源（高置信度）

- Dagster OSS 架构：https://docs.dagster.io/deployment/oss/oss-deployment-architecture
- Dagster issue #15155（回填中重复运行）：https://github.com/dagster-io/dagster/issues/15155
- Dagster issue #25743（并发死锁，v1.8.13+）：https://github.com/dagster-io/dagster/issues/25743
- River：https://riverqueue.com/ 和 https://pkg.go.dev/github.com/riverqueue/river（v0.35.x，2026 年 4 月）
- heimdalr/dag：https://pkg.go.dev/github.com/heimdalr/dag（v1.5.1，2026 年 4 月）
- ent ORM：https://entgo.io/（v0.14.x，2026 年 3 月）
- sqlc：https://docs.sqlc.dev/（v1.31.1，2026 年 4 月）
- Atlas vs golang-migrate：https://atlasgo.io/blog/2025/04/06/atlas-and-golang-migrate
- hashicorp/go-plugin：https://github.com/hashicorp/go-plugin（v1.7.0，2025 年 8 月）
- connect-go：https://connectrpc.com/（v1.19.2，2026 年 4 月）
- Casbin：https://casbin.apache.org/（v2.135.x，2025 年 12 月）
- OpenLineage 列血缘 facet：https://openlineage.io/docs/spec/facets/dataset-facets/column_lineage_facet/
- DataHub SQL 列血缘：https://datahub.com/blog/extracting-column-level-lineage-from-sql/
- OpenMetadata 治理：https://docs.open-metadata.org/latest/how-to-guides/data-governance

### 次要来源（中等置信度）

- Go HTTP 框架对比（JetBrains 2026）：https://blog.jetbrains.com/go/2026/04/28/popular-golang-web-frameworks/
- Go ORM 对比：https://www.bytebase.com/blog/golang-orm-query-builder/
- Hoop.dev 列级 PostgreSQL 访问控制：https://hoop.dev/blog/column-level-access-for-postgres-at-protocol-speed
- Dagster 列血缘文档：https://docs.dagster.io/guides/build/assets/metadata-and-tags/column-level-lineage
- 2025 开源数据治理全景：https://atlan.com/open-source-data-governance-tools/
- 回填资源隔离（LakeFS）：https://lakefs.io/blog/backfilling-data-foolproof-guide/
- 防篡改日志（ACM CCS 2025）：https://dl.acm.org/doi/10.1145/3719027.3765024

### 第三方来源（低置信度）

- ThijsKoot/openlineage-go：https://github.com/ThijsKoot/openlineage-go — 社区维护的 Go 客户端；非 OpenLineage 官方项目；生产使用的准确性和完整性未经验证

---

*调研完成日期：2026-04-29*
*路线图就绪：是*
