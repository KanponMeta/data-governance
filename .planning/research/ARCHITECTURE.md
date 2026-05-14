# 架构模式

**领域：** 数据编排 + 治理平台（Go 原生，Dagster 启发）
**调研日期：** 2026-04-29
**总体可信度：** Dagster 内部机制 HIGH，治理模式 MEDIUM，血缘图存储 MEDIUM

---

## 1. Dagster 架构深度解析

理解 Dagster 的架构是本设计的主要输入。以下是从文档和源码分析中忠实还原的结果。

### 1.1 整体拓扑（OSS 版本）

Dagster OSS 以三个长期运行的协作服务加可插拔存储的形式运行：

```
┌─────────────────────────────────────────────────────────────────┐
│                        客户端浏览器                              │
└───────────────────────────┬─────────────────────────────────────┘
                            │ HTTP / GraphQL
┌───────────────────────────▼─────────────────────────────────────┐
│                    dagster-webserver (Dagit)                     │
│  • 提供 React SPA                                               │
│  • GraphQL API（Graphene schema-first）                         │
│  • 通过 code-location gRPC 读取定义                             │
│  • 将运行启动写入 RunStorage / RunCoordinator                   │
└────────────┬──────────────────────────────────┬─────────────────┘
             │ SQL / 存储抽象                    │ gRPC
             │                                  │
┌────────────▼──────────────┐    ┌──────────────▼──────────────┐
│    共享存储层              │    │  代码位置服务器              │
│  • RunStorage             │    │  • 每个代码位置一个 gRPC 服务器│
│  • EventLogStorage        │    │  • 加载 Definitions 对象     │
│  • ScheduleStorage        │    │  • 提供资产/作业/            │
│  （SQLite 开发 /          │    │    调度元数据                │
│   Postgres/MySQL 生产）   │    │                              │
└────────────▲──────────────┘    └──────────────▲──────────────┘
             │                                  │ gRPC
┌────────────┴──────────────────────────────────┴──────────────┐
│                    dagster-daemon                              │
│  • SchedulerDaemon  — 从 cron 调度创建运行                    │
│  • SensorDaemon     — 轮询传感器，发出运行请求                │
│  • RunQueueDaemon   — 出队并启动运行                          │
│  • RunMonitorDaemon — 处理工作进程失败/超时                   │
│  （单实例；不可复制）                                         │
└───────────────────────────────────────────────────────────────┘
             │ 生成
┌────────────▼──────────────────────────────────────────────────┐
│                    运行工作进程（子进程/Pod）                  │
│  • 每次运行加载                                               │
│  • 从资产/作业图构建 ExecutionPlan                            │
│  • Executor 决定步骤调度策略                                  │
│  • 将 DagsterEvent 写入 EventLogStorage                       │
└───────────────────────────────────────────────────────────────┘
```

### 1.2 资产定义存储与解析

资产并非"存储"在数据库中——它们存在于用户代码中。解析链如下：

1. 用户代码定义 `@asset` 函数。装饰器捕获：资产键、上游依赖（从函数参数名推断）、分区定义、IO 管理器键、元数据、标签。
2. `Definitions` 对象作为注册表——它是一个代码位置的进程内目录。
3. 代码位置 gRPC 服务器在启动时加载 `Definitions` 对象，并在文件变更信号时重新加载。
4. webserver 和 daemon 都通过 gRPC 连接到代码位置服务器来查询定义（资产键、依赖边、调度、传感器）。
5. `resolve_asset_graph()` 在查询时执行拓扑排序以生成执行计划。不维护单独的图表——图始终从定义重新计算。

**对 Go 移植的关键启示：** 资产定义必须是一等运行时对象（结构体/接口），而非仅仅是注解。`DefinitionRegistry` 组件必须允许在启动时注册并在运行时自省。

### 1.3 执行引擎：作业 → Op → 资产流水线

```
AssetSelection（资产选择）
     │
     ▼
AssetGraph.toJob()          → 从选中资产合成作业
     │
     ▼
ExecutionPlan.build()       → 拓扑排序 → 有序 StepExecutionData
     │
     ▼
Executor.execute()          → 按策略调度步骤：
  • InProcessExecutor       → 串行，同一 goroutine/线程
  • MultiprocessExecutor    → 每步骤一个子进程
  • 分布式（Celery,         → 任务排队到外部代理
    Dask, K8s, ECS）
     │
     ▼
StepWorker                  → 运行用户函数
  • 为每个输入调用 IO manager load()
  • 调用用户资产函数
  • 为返回值调用 IO manager handle_output()
  • 发出 DagsterEvent：STEP_START, STEP_OUTPUT, STEP_SUCCESS/FAILURE
```

步骤仅通过 EventLogStorage 传回结果——协调器与工作进程之间没有直接 IPC 用于结果传输。协调器轮询存储。

### 1.4 事件日志 / 运行存储

三种存储抽象，实践中均由同一 SQL 数据库支撑：

| 存储 | 内容 | 关键操作 |
|------|------|---------|
| `RunStorage` | `DagsterRun` 记录：状态、配置、标签、作业快照 | `add_run`, `update_run`, `get_run_by_id`, `get_runs`（带过滤/分页） |
| `EventLogStorage` | 每次运行的所有 `DagsterEvent` 记录：STEP_START, ASSET_MATERIALIZATION, LOGS 等 | `store_event`, `get_logs_for_run`, `get_asset_records`, `get_event_records` |
| `ScheduleStorage` | 调度/传感器 tick 历史、激励器状态 | `get_instigator_state`, `update_instigator_state`, `create_tick`, `update_tick` |

**SQLite（开发版）：** EventLog 按运行分片——每次运行一个 SQLite 文件。防止锁争用但使跨运行查询困难。

**PostgreSQL（生产版）：** 单一整合表。通过迁移添加二级索引。启用连接池。`asset_keys` 表缓存每个资产的最新物化以供 UI 快速查询。

**模式：** 事件溯源。所有执行状态从不可变事件流派生。运行状态通过扫描事件计算，而非直接存储（尽管为性能缓存）。

### 1.5 Daemon 内部机制

`dagster-daemon` 是一个单进程，包含多个以固定间隔轮询的 daemon 线程：

- **SchedulerDaemon：** 查询 ScheduleStorage 以获取到期的 tick。对于每个逾期调度，调用代码位置 gRPC 服务器评估调度函数，然后在 RunStorage 创建 `DagsterRun` 并入队。
- **SensorDaemon：** 同样的模式，但评估传感器游标状态并为每次评估发出 `RunRequest` 对象。
- **RunQueueDaemon：** 读取 RunStorage 中排队的运行；应用并发限制和优先级规则；调用 `RunLauncher.launch_run()`。
- **RunMonitorDaemon：** 轮询处于 STARTING/STARTED 状态但工作进程已死亡的运行；将其标记为 FAILURE。

每个 daemon 向存储写入心跳，使 webserver 可以显示 daemon 健康状态。

### 1.6 Webserver ↔ 后端通信（GraphQL API）

- Webserver 暴露单一 GraphQL 端点。
- Schema 使用 Python Graphene 以 schema-first 方式定义。
- 两种资产类型：`GrapheneAssetNode`（定义时：依赖、分区、自动化条件）vs `GrapheneAsset`（运行时：最后物化、新鲜度）。
- 资产查询使用 `DataLoader` 模式：批量加载资产记录以避免对存储的 N+1 查询。
- `WorkspaceRequestContext` 包装存储访问和代码位置 gRPC 连接两者。
- Mutation（`launchPipelineExecution`, `launchPartitionBackfill`）检查权限、写入 `DagsterRun` 记录，然后通过 `RunCoordinator` 入队。
- UI（React）使用 Apollo Client 处理 GraphQL，Recoil 处理客户端状态。TypeScript 类型从 schema 生成。

### 1.7 IO Manager（连接器抽象）

IO manager 是 Dagster 的连接器接口。每个注册在某个键下（默认 `"io_manager"`）。

接口契约：
```python
class IOManager:
    def handle_output(self, context: OutputContext, obj: Any) -> None: ...
    def load_input(self, context: InputContext) -> Any: ...
```

`OutputContext` 和 `InputContext` 携带：资产键、分区键、元数据、运行 ID、资源配置。这使 IO manager 完全上下文感知——它们可以使用分区键路由到正确的 S3 前缀或数据库分区。

IO manager 按资产或全局附加。它们将资产函数（纯转换逻辑）与存储关注点解耦。

---

## 2. 字段级血缘架构

### 2.1 OpenLineage 规范

OpenLineage 定义标准事件模型：`Run → Job → Dataset`。血缘在执行期间以事件形式发出。

列级血缘是附加在输出数据集上的 facet：

```json
{
  "columnLineage": {
    "fields": {
      "revenue_usd": {
        "inputFields": [
          {
            "namespace": "postgresql://prod",
            "name": "orders.order_value",
            "field": "order_value",
            "transformations": [
              { "type": "DIRECT", "subtype": "TRANSFORMATION", "description": "multiply by fx_rate" }
            ]
          }
        ]
      }
    }
  }
}
```

转换类型：
- `DIRECT` 子类型：`IDENTITY`、`TRANSFORMATION`、`AGGREGATION`
- `INDIRECT` 子类型：`JOIN`、`GROUP_BY`、`FILTER`、`SORT`、`WINDOW`、`CONDITIONAL`

**对 Go 平台的影响：** 资产函数必须通过以下方式发出列血缘：(a) 通过对声明查询的 SQL 解析自动实现，或 (b) 通过血缘注解 API 手动实现。两者都应产生兼容 OpenLineage 的事件。

### 2.2 基于 SQL 的列血缘提取

当资产执行 SQL 查询时，可通过解析 SQL AST 提取列血缘：

```
SQL 字符串
    │
    ▼
SQL 解析器（AST）
    │
    ▼
AST 遍历
    │  • 解析表别名 → 全限定名
    │  • 解析列别名
    │  • 递归处理 CTE
    │  • 处理子查询
    │  • 处理 UNION（合并输入列）
    ▼
列映射：output_col → [(source_table, source_col, transform_type)]
    │
    ▼
OpenLineage ColumnLineageFacet 事件
```

用于此目的的 Go SQL 解析器：
- `vitess.io/vitess/go/vt/sqlparser` — 生产级，良好处理 MySQL 方言。在 PlanetScale/Vitess 生产中使用。
- `github.com/pingcap/tidb/parser` — TiDB 的解析器，支持 MySQL + 部分 PostgreSQL。
- `github.com/auxten/postgresql-parser` — PostgreSQL 方言。
- 基于 ANTLR 的解析器用于多方言支持（从语法文件生成 Go 代码）。

**建议：** 对 MySQL/通用 SQL 使用 `vitess.io/vitess/go/vt/sqlparser`，对 PostgreSQL 使用 `github.com/auxten/postgresql-parser` 或 ANTLR 语法。接受 100% 准确率很难——DataHub 使用 schema 感知解析在生产查询语料库上报告 97-99% 的准确率。

Schema 感知至关重要：不知道输入表的 schema，`SELECT *` 和裸列名是有歧义的。血缘提取器需要访问元数据目录来解析 schema。

### 2.3 血缘图存储

针对以 PostgreSQL 为主要存储的 Go 平台，两种可行方案：

**方案 A：PostgreSQL 中的邻接表（v1 推荐）**

```sql
CREATE TABLE lineage_edges (
    id          BIGSERIAL PRIMARY KEY,
    edge_type   TEXT NOT NULL,          -- 'TABLE' 或 'COLUMN'
    src_asset   TEXT NOT NULL,
    src_field   TEXT,                   -- 表级边为 NULL
    dst_asset   TEXT NOT NULL,
    dst_field   TEXT,
    transform   JSONB,                  -- OpenLineage 转换元数据
    run_id      TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ON lineage_edges (dst_asset, dst_field);  -- 影响分析
CREATE INDEX ON lineage_edges (src_asset, src_field);  -- 上游查找
```

影响分析查询："如果我改变 `orders.vat_rate`，哪些输出字段受影响？"
→ 递归 CTE 遍历 `src_asset/src_field` → `dst_asset/dst_field`。

PostgreSQL 的 `WITH RECURSIVE` 在治理相关规模（数百万条边、数百个资产）处理 DAG 遍历效果良好。这避免了 Neo4j 的运维依赖。

**方案 B：专用图数据库（Neo4j 或 Apache AGE）**

Neo4j 通过无索引邻接（指针追踪 vs PostgreSQL 中的索引查找）提供 O(1) 关系遍历。优势在超过 5 跳和超过 1000 万条边时才显现。对于数据治理血缘图（通常是浅层、有界的），PostgreSQL 在运维简单性上胜出。

Apache AGE（PostgreSQL 的图扩展）是中间路径——PostgreSQL 内的 Cypher 查询——但对 v1 而言还未达到生产成熟度。

**决策：** v1 使用 PostgreSQL 邻接表 + 递归 CTE。设计血缘存储接口（Go 接口）使其可在不改变查询调用点的情况下后续切换到 Neo4j。

---

## 3. 治理架构模式

### 3.1 审批工作流

审批工作流是资产发布事件上的状态机。

推荐的状态模型：

```
DRAFT（草稿）
  │ submit_for_review（提交审核）
  ▼
PENDING_REVIEW（待审核）
  │ approve（批准）            │ reject（拒绝）
  ▼                            ▼
PUBLISHED（已发布）       REJECTED（已拒绝，附评论）
  │ deprecate（废弃）
  ▼
DEPRECATED（已废弃）
```

**实现模式：** 工作流状态存储在 `governance_reviews` 表中。转换以行的形式追加到 `governance_events`（追加式）。当前状态通过读取最新事件派生。

```sql
CREATE TABLE governance_reviews (
    id          UUID PRIMARY KEY,
    asset_key   TEXT NOT NULL,
    asset_version TEXT NOT NULL,
    state       TEXT NOT NULL,          -- 当前状态（为查询缓存）
    created_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE governance_events (
    id          BIGSERIAL PRIMARY KEY,
    review_id   UUID NOT NULL REFERENCES governance_reviews(id),
    event_type  TEXT NOT NULL,          -- SUBMITTED, APPROVED, REJECTED, DEPRECATED
    actor       TEXT NOT NULL,
    comment     TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

通知由事件后钩子触发：当一行插入 `governance_events` 时，通知服务分发到配置的渠道（邮件、Slack webhook、应用内）。

BPMN 引擎（Camunda、Zeebe）对 v1 过于重量级——状态机只有 5 个状态和 4 个转换。存储在 PostgreSQL 中的手写 FSM 是合适的。

### 3.2 列级访问控制

三种执行模式，按侵入程度排序：

**模式 1：查询重写（本平台推荐）**

平台维护策略存储：

```sql
CREATE TABLE column_policies (
    id          BIGSERIAL PRIMARY KEY,
    asset_key   TEXT NOT NULL,
    column_name TEXT NOT NULL,
    role        TEXT NOT NULL,
    action      TEXT NOT NULL  -- ALLOW, MASK, REDACT
);
```

当用户通过平台的查询代理查询数据时：
1. 解析 SQL AST。
2. 查找每个引用列的策略。
3. 重写 AST：用 `NULL AS col`（REDACT）或 `mask_fn(col) AS col`（MASK）替换受限列引用。
4. 针对后端执行重写后的 SQL。

这是 Databricks Unity Catalog（列掩码）和 BigQuery 列级安全使用的方案。

**模式 2：线路协议代理**

PostgreSQL 线路协议代理（如扩展了策略逻辑的 PgBouncer）在字节级拦截 `RowDescription` 和 `DataRow` 数据包，剥离或掩码受限列。这以协议速度工作，无需查询重写延迟。被 hoop.dev 用于列级 PostgreSQL 访问控制。

**模式 3：视图层**

生成特定角色的视图（`CREATE VIEW orders_for_analyst AS SELECT id, date, amount FROM orders` 省略 PII 列）。简单但不具通用性——视图激增变得难以管理。

**建议：** v1 使用查询重写代理。平台控制查询路径（用户通过平台而非直接针对数据仓库查询）。这使跨连接器后端的统一执行成为可能。

### 3.3 防篡改审计日志

审计日志必须：
1. 追加式（无 UPDATE/DELETE）
2. 防篡改（修改过去的记录可被检测到）
3. 可查询（合规导出、GDPR 删除证明）

**实现：哈希链追加式日志**

```sql
CREATE TABLE audit_log (
    seq         BIGSERIAL PRIMARY KEY,
    event_type  TEXT NOT NULL,
    actor       TEXT NOT NULL,
    resource    TEXT NOT NULL,
    detail      JSONB,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    entry_hash  TEXT NOT NULL  -- SHA-256(seq || event_type || actor || resource || detail || prev_hash)
);
```

每行的 `entry_hash` 包含前一行的哈希（链）。要验证完整性，从 `seq=1` 重新计算哈希并比较。修改任何行都会破坏后续所有哈希。

更强的保证：定期将当前链头哈希锚定到外部日志（S3、CloudWatch 或 Trillian 等公共透明日志）。这提供某一时间点的存在性证明。

**数据库级保护：**
- 使用 PostgreSQL 行级安全，拒绝除专用 `audit_writer` 角色外所有角色对 `audit_log` 的 UPDATE/DELETE。
- 应用程序通过 `audit_writer` 写入；应用程序服务账号使用仅有 INSERT 权限的角色。

---

## 4. Go 平台推荐组件架构

### 4.1 组件图

```
┌─────────────────────────────────────────────────────────────────────┐
│                           用户代码（Go）                             │
│  • 资产定义（实现 AssetDef 接口的结构体）                           │
│  • 连接器实现                                                        │
│  • 自定义质量规则                                                    │
└───────────────────────────────┬─────────────────────────────────────┘
                                │ 启动时通过注册表加载
┌───────────────────────────────▼─────────────────────────────────────┐
│                       平台核心二进制                                 │
│                                                                      │
│  ┌─────────────────┐  ┌─────────────────┐  ┌──────────────────┐    │
│  │  编排引擎        │  │   治理引擎       │  │  元数据/目录      │    │
│  │                 │  │                 │  │                  │    │
│  │ • AssetGraph    │  │ • WorkflowFSM   │  │ • SchemaRegistry │    │
│  │ • Scheduler     │  │ • PolicyStore   │  │ • LineageStore   │    │
│  │ • RunManager    │  │ • AuditLog      │  │ • TagStore       │    │
│  │ • StepExecutor  │  │ • Notifications │  │ • SearchIndex    │    │
│  └────────┬────────┘  └────────┬────────┘  └──────────┬───────┘    │
│           │                   │                       │            │
│  ┌────────▼───────────────────▼───────────────────────▼───────┐    │
│  │                    存储抽象层                               │    │
│  │  RunStore | EventStore | LineageStore | PolicyStore |       │    │
│  │  AuditStore | SchemaStore | CatalogStore                    │    │
│  └────────────────────────────┬────────────────────────────────┘    │
│                               │                                      │
│  ┌────────────────────────────▼────────────────────────────────┐    │
│  │               PostgreSQL（主存储，所有数据）                 │    │
│  │         + Elasticsearch / Bleve（全文搜索）                  │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                                                                      │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │                    API 服务器（gRPC + REST/GraphQL）         │    │
│  │  • GraphQL API 用于 UI                                       │    │
│  │  • gRPC API 用于连接器 / CLI / 外部工具                      │    │
│  │  • REST webhook 用于 OpenLineage 摄取                        │    │
│  └─────────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────────┘
             │ 服务
┌────────────▼────────────────────────────────────────────────────────┐
│                         Web UI（React + TypeScript）                 │
│  • 资产图可视化                                                      │
│  • 血缘 DAG（字段级下钻）                                            │
│  • 质量仪表板                                                        │
│  • 治理工作流 UI                                                     │
└──────────────────────────────────────────────────────────────────────┘
```

### 4.2 组件边界

| 组件 | 职责 | 禁止事项 | 交互对象 |
|------|------|---------|---------|
| **AssetGraph** | 持有资产定义，构建依赖 DAG，拓扑排序 | 执行步骤、写存储 | RunManager, StepExecutor |
| **RunManager** | 创建/更新运行记录，管理运行生命周期状态机 | 执行步骤 | EventStore, RunStore, StepExecutor |
| **StepExecutor** | 按依赖顺序调度资产函数；管理并发 | 了解治理事务 | ConnectorRegistry, EventStore, RunManager |
| **ConnectorRegistry** | 加载 + 验证连接器插件；提供 IOManager 接口 | 执行转换 | StepExecutor |
| **Scheduler** | 轮询 cron 调度；入队物化运行 | 直接启动运行 | RunManager, AssetGraph |
| **SensorEngine** | 评估传感器函数；发出运行请求 | 直接启动运行 | RunManager, ConnectorRegistry |
| **WorkflowFSM** | 治理资产审核生命周期的状态转换 | 执行资产 | AuditLog, Notifications, PolicyStore |
| **PolicyStore** | 存储和评估列访问策略 | 在执行时执行 | QueryProxy, UI |
| **QueryProxy** | 拦截查询，为策略执行重写，发出审计事件 | 存储策略 | PolicyStore, AuditLog, ConnectorRegistry |
| **LineageStore** | 存储和查询表/字段血缘边 | 提取血缘 | AssetGraph, CatalogStore |
| **LineageExtractor** | 解析 SQL AST → 列血缘边 | 存储任何内容 | LineageStore, SchemaRegistry |
| **AuditLog** | 以哈希链完整性追加审计事件 | 允许不受信任角色任何读取 | PostgreSQL（直接，追加式） |
| **SchemaRegistry** | 追踪 Schema 版本；对比物化间的 Schema 差异 | 执行任何操作 | CatalogStore, LineageExtractor |
| **CatalogStore** | 持久化元数据：描述、标签、所有者、质量历史 | 执行策略 | UI, SchemaRegistry, LineageStore |
| **API Server** | 提供 GraphQL（UI）、gRPC（工具/连接器）、OpenLineage webhook | 业务逻辑 | 所有其他组件 |

### 4.3 数据流：资产物化

```
1. 触发器（调度 tick / 传感器事件 / 用户在 UI 点击）
   │
2. RunManager.CreateRun(assetKey, partitionKey, config)
   → 将 DagsterRun{status: QUEUED} 写入 RunStore
   │
3. 调度器轮询 RunStore 获取 QUEUED 运行
   → 调用 RunManager.LaunchRun(runID)
   → 将 Run{status: STARTING} 写入 RunStore
   │
4. StepExecutor.Execute(executionPlan)
   → 步骤拓扑排序
   → 对每个步骤按顺序：
      a. ConnectorRegistry.LoadInput(upstreamAsset)  ← IO manager 加载
      b. 调用用户资产函数
      c. LineageExtractor.ExtractFromSQL(query)       ← 如果使用了 SQL
      d. ConnectorRegistry.HandleOutput(result)       ← IO manager 存储
      e. EventStore.Store(ASSET_MATERIALIZATION 事件)
      f. SchemaRegistry.UpdateSchema(assetKey, schema)
      g. LineageStore.StoreEdges(edges)
      h. QualityEngine.EvaluateRules(assetKey)
   │
5. RunManager 更新 Run{status: SUCCESS/FAILED}
   │
6. EventStore 通知 SensorEngine（基于事件的传感器）
   │
7. UI 轮询 GraphQL → 显示更新的资产状态
```

### 4.4 数据流：治理工作流

```
1. 用户通过 UI 提交资产审核
   → WorkflowFSM.Transition(assetKey, DRAFT → PENDING_REVIEW)
   → AuditLog.Append(REVIEW_SUBMITTED)
   → Notifications.Send(reviewers, "已请求审核")
   │
2. 审核人通过 UI 批准/拒绝
   → WorkflowFSM.Transition(assetKey, PENDING_REVIEW → PUBLISHED/REJECTED)
   → AuditLog.Append(REVIEW_APPROVED/REJECTED, actor, comment)
   → Notifications.Send(submitter, "决定：...")
   │
3. 发布时（PUBLISHED）：资产可通过平台查询
   PolicyStore.ActivatePolicies(assetKey)
   │
4. 列级查询执行：
   用户查询 → QueryProxy
     → PolicyStore.Evaluate(user, asset, columns)
     → SQL 重写（掩码/编辑受限列）
     → 执行重写后的查询
     → AuditLog.Append(DATA_ACCESS, user, asset, columns_accessed)
     → 向用户返回结果
```

### 4.5 连接器接口设计

连接器是 Dagster IO manager 的 Go 等价物。它们应从第一天起就成为稳定的、版本化的公共 API。

```go
// Connector 是所有数据连接器的稳定公共接口。
// 第三方连接器实现此接口。
type Connector interface {
    // 元数据
    Name() string
    Version() string
    Capabilities() ConnectorCapabilities

    // 生命周期
    Configure(cfg map[string]any) error
    Ping(ctx context.Context) error
    Close() error

    // IO
    Read(ctx context.Context, ref DataRef) (DataSet, error)
    Write(ctx context.Context, ref DataRef, data DataSet) error

    // Schema
    InspectSchema(ctx context.Context, ref DataRef) (Schema, error)

    // 血缘（可选——连接器可返回 nil）
    ExtractLineage(ctx context.Context, query string) ([]LineageEdge, error)
}

type ConnectorCapabilities struct {
    SupportsPartitions bool
    SupportsStreaming   bool
    SupportsLineage     bool
    SupportsSchemaEvo   bool
    SupportedDialects   []SQLDialect
}
```

**插件部署拓扑：**

三种选项，按复杂度排序：

| 选项 | 机制 | 优点 | 缺点 |
|------|------|------|------|
| **编译内置** | 连接器在同一 Go 二进制中 | 零 IPC 开销；类型安全 | 添加连接器需要重新构建 |
| **go-plugin（gRPC 子进程）** | HashiCorp go-plugin：连接器作为子进程运行，通过 gRPC 通信 | 进程隔离；崩溃安全；多语言 | 启动延迟；IPC 开销 |
| **外部 gRPC 服务** | 连接器作为独立服务运行；平台通过 gRPC 调用 | 完全语言独立；独立部署 | 网络延迟；服务管理 |

**建议：** 所有第一方连接器（PostgreSQL、MySQL、BigQuery、Snowflake、S3、GCS）编译内置。第三方连接器使用 go-plugin 子进程模式。这与 Terraform/Vault 的插件模型一致，该模型已在生产中验证。

### 4.6 部署拓扑

**开发 / 单机：**
```
docker-compose.yml:
  - platform（单二进制：API + 引擎 + daemon）
  - postgres
  - elasticsearch（可选，开发环境可使用进程内 Bleve）
  - ui（nginx 提供 React SPA）
```

核心二进制应支持三种运行模式：
- `platform server` — 启动所有子系统（用于开发/小规模部署）
- `platform api` — 仅 API 服务器（水平扩展）
- `platform worker` — 仅步骤执行器（通过增加 worker 扩展）
- `platform daemon` — 调度器/传感器 daemon（单例）

这与 Dagster 的分离方式一致，但在单一二进制中通过功能标志实现——运维简单得多。

**生产 / Kubernetes：**
```
Deployment: platform-api       (replicas: 3)
Deployment: platform-worker    (replicas: N, 自动扩缩)
Deployment: platform-daemon    (replicas: 1, 单例)
StatefulSet: postgres
Deployment: elasticsearch
```

---

## 5. 建议构建顺序（阶段依赖）

依赖从上到下流动——每一层必须在下一层之前构建。

```
第一阶段：基础
  ├── 存储层（PostgreSQL Schema 迁移、存储接口）
  ├── AssetDefinition 类型系统（AssetKey, AssetGraph, DependencyGraph）
  ├── EventLog（存储 + 检索 DagsterEvent）
  └── CLI 脚手架（项目初始化、run 命令）

第二阶段：执行引擎
  ├── [需要第一阶段] RunManager（创建/追踪运行）
  ├── [需要第一阶段] StepExecutor（拓扑调度）
  ├── [需要第一阶段] ConnectorRegistry + Connector 接口
  ├── [需要 ConnectorRegistry] 第一方连接器（PostgreSQL, S3）
  └── [需要 StepExecutor] 进程内和多进程执行器

第三阶段：调度 + 传感器
  ├── [需要第二阶段] 调度器 daemon（cron → 运行创建）
  ├── [需要第二阶段] 传感器引擎（事件轮询 → 运行创建）
  └── [需要第二阶段] 分区系统（基于时间、分类、动态）

第四阶段：血缘 + Schema
  ├── [需要第二阶段] Schema 注册表（物化时捕获 schema）
  ├── [需要第二阶段] SQL 血缘提取器（AST 解析 → 列边）
  ├── [需要第一阶段] LineageStore（邻接表、递归 CTE 查询）
  └── [需要 SchemaRegistry] 影响分析 API

第五阶段：治理
  ├── [需要第四阶段] 策略存储（列访问策略）
  ├── [需要策略存储] 查询代理（SQL 重写用于执行）
  ├── [需要第一阶段] 审批工作流 FSM
  ├── [需要所有阶段] 审计日志（哈希链、追加式）
  └── [需要第二阶段] 数据质量规则引擎

第六阶段：API + UI
  ├── [需要第二阶段] GraphQL API（资产图、运行、事件）
  ├── [需要第四阶段] 血缘 API（字段级图查询）
  ├── [需要第五阶段] 治理 API（策略、工作流、审计）
  ├── [需要 GraphQL] React UI：资产目录 + 运行历史
  ├── [需要血缘 API] React UI：血缘 DAG 可视化
  └── [需要治理 API] React UI：治理工作流
```

---

## 6. 关键架构决策

| 决策 | 建议 | 理由 |
|------|------|------|
| 单二进制 vs 微服务 | 带运行模式标志的单二进制 | 运维简单；规模有需求时可拆分 |
| 资产定义机制 | Go 接口 + 启动时注册（非代码生成） | 避免反射复杂性；类型安全 |
| 存储后端 | v1 仅 PostgreSQL（通过构建标签 SQLite 用于开发） | 避免管理两个存储后端；PostgreSQL 满足所有需求 |
| 血缘图 | PostgreSQL 邻接表 + 递归 CTE | 避免 Neo4j 运维依赖；v1 规模足够 |
| 连接器插件系统 | 编译内置（第一方）+ go-plugin 子进程（第三方） | 与成熟的 HashiCorp 模型匹配；第三方崩溃隔离 |
| API 协议 | GraphQL 用于 UI（丰富查询、schema 演化），gRPC 用于 CLI/编程接口 | 与 Dagster 一致的成熟模式；两者均有生成类型 |
| 列访问执行 | 查询重写代理 | 跨连接器统一执行；无每连接器逻辑 |
| 审计日志完整性 | PostgreSQL 中的哈希链 | 简单，无外部依赖；后续添加 Merkle 锚定 |
| 工作流引擎 | 手写 FSM | 5 个状态，4 个转换；BPMN 过度设计 |
| 搜索 | 内嵌 Bleve（开发）/ Elasticsearch（生产） | 保持开发设置简单；Elasticsearch 用于规模 |

---

## 7. 需要避免的反模式

### 反模式 1：将资产定义存储在数据库中
**现象：** 将资产结构体序列化为数据库行作为真实来源。
**危害：** 代码中的定义是真实来源；数据库副本会产生同步问题、版本歧义，并使 Schema 变得僵化。
**替代方案：** 资产定义存在于用户代码中，启动时加载到内存注册表。存储仅捕获执行历史（运行、事件）和派生元数据（schema、血缘）。

### 反模式 2：资产函数中直接访问数据库
**现象：** 资产函数直接调用 `sql.Open()` 而不通过 Connector 接口。
**危害：** 破坏血缘提取（平台无法看到查询了什么）、破坏策略执行、破坏 Schema 捕获。
**替代方案：** 资产函数通过依赖注入接收类型化连接器。平台透明地包装调用以提取血缘和执行策略。

### 反模式 3：可变审计日志
**现象：** `UPDATE audit_log SET detail = ... WHERE id = X` 用于"纠正"。
**危害：** 破坏防篡改性；合规框架要求不可变性。
**替代方案：** 纠正是新的追加式条目（`CORRECTION` 事件类型，引用原始条目）。

### 反模式 4：在 API 处理器中同步执行步骤
**现象：** API 处理器在 HTTP 请求/响应周期中执行资产函数。
**危害：** 长时间运行的资产阻塞 API；无重试、无并发、无可观察进度。
**替代方案：** API 处理器入队一次运行；后台 worker daemon 接收并执行。进度通过 EventLog 可见。

### 反模式 5：每连接器策略执行
**现象：** 每个连接器独立检查列策略。
**危害：** 策略逻辑重复；容易遗漏；连接器是第三方代码。
**替代方案：** QueryProxy 在任何连接器看到查询之前执行所有策略。连接器对策略无感知。

---

## 参考来源

- [Dagster OSS 部署架构](https://docs.dagster.io/deployment/oss/oss-deployment-architecture)
- [Dagster Daemon 文档](https://docs.dagster.io/deployment/execution/dagster-daemon)
- [Dagster I/O Managers](https://docs.dagster.io/guides/build/io-managers)
- [Dagster 代码位置架构博客](https://dagster.io/blog/dagster-code-locations)
- [Dagster GraphQL API（DeepWiki）](https://deepwiki.com/dagster-io/dagster/6-graphql-api)
- [Dagster 核心定义系统（DeepWiki）](https://deepwiki.com/dagster-io/dagster/2-core-definitions-system)
- [Dagster 存储与持久化（DeepWiki）](https://deepwiki.com/dagster-io/dagster/5-storage-and-persistence)
- [OpenLineage 列血缘 Facet](https://openlineage.io/docs/spec/facets/dataset-facets/column_lineage_facet/)
- [DataHub：从 SQL 提取列级血缘](https://datahub.com/blog/extracting-column-level-lineage-from-sql/)
- [Hoop.dev：以协议速度实现 Postgres 列级访问](https://hoop.dev/blog/column-level-access-for-postgres-at-protocol-speed)
- [OpenMetadata 高层设计](https://docs.open-metadata.org/latest/main-concepts/high-level-design)
- [HashiCorp go-plugin](https://github.com/hashicorp/go-plugin)
- [Neo4j vs PostgreSQL 图数据](https://medium.com/self-study-notes/exploring-graph-database-capabilities-neo4j-vs-postgresql-105c9e85bb5d)
- [防篡改日志：高效数据结构](https://static.usenix.org/event/sec09/tech/full_papers/crosby.pdf)