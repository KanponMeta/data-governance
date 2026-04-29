<!-- GSD:project-start source:PROJECT.md -->
## 项目

**数据治理平台**

一个用 Go 编写的开源数据治理平台，灵感来自 Dagster 的资产中心架构。它将数据编排（软件定义资产、流水线调度、执行引擎）与企业级治理（字段级血缘、数据质量规则、元数据目录、列级访问控制和审批工作流）相结合。专为构建流水线的数据工程师、探索数据的分析师以及执行策略的治理团队设计——全部在单一平台中完成。

**核心价值：** 数据从业者可以在代码中定义、运行和治理数据资产——每个下游消费者都可以信任他们正在使用的数据，追溯其字段级来源，并了解谁有权查看它。

### 约束

- **技术栈**：Go 后端（核心无 Python 运行时依赖）——Go 是团队的主要语言
- **开源**：Apache 2.0 或类似宽松许可证——目标是社区采用
- **自包含**：开发环境必须在单台机器上运行（Docker Compose）
- **连接器可扩展性**：连接器接口从第一天起就必须是稳定的公共 API——第三方连接器是关键的采用驱动因素
<!-- GSD:project-end -->

<!-- GSD:stack-start source:research/STACK.md -->
## 技术栈

## 推荐技术栈
### 执行引擎
| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| 自研 DAG 调度器（进程内） | — | 资产依赖解析 + 拓扑排序执行 | Temporal 对第一阶段过于重量级；内嵌调度器使部署简单（单二进制）。Temporal 引入外部服务依赖。 |
| `riverqueue/river` | v0.35.x | 物化任务、异步任务、定时调度的作业队列 | 基于 Postgres 的事务性入队（事务提交则任务不丢失）、重试、唯一任务、周期性/cron、Web UI。无需外部消息代理。与 PostgreSQL 元数据存储天然配合。 |
| `heimdalr/dag` | v1.5.x | 内存 DAG 表示、拓扑排序、环检测 | 线程安全、泛型、BFS/DFS 遍历器、拓扑排序、传递性约减、JSON 序列化。v1.5.1 发布于 2026 年 4 月。BSD-3 简洁许可证。 |
### 元数据存储
| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| PostgreSQL | 16+ | 主元数据存储：资产、运行、血缘、质量、审计日志 | 结构化元数据的行业标准。River 本就需要 Postgres。JSONB 用于 Schema 快照。强大的外键约束保证血缘图完整性。 |
| SQLite | 3.x（via `mattn/go-sqlite3` 或 `modernc.org/sqlite`） | 嵌入式开发模式（单二进制、零配置） | 支持无外部依赖运行 `./platform start`。SQLite 被 go-workflows 和 River 使用（River SQLite 驱动为预览版）。仅用于开发/CI 环境。 |
### 数据库迁移
| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| Atlas（`ariga.io/atlas`） | latest | PostgreSQL 和 SQLite 的 Schema 迁移 | 声明式 Schema 差异对比——无需手动编写回滚脚本。自动处理脏状态恢复（golang-migrate 不支持）。与 ent schema 集成。在 CI 中对迁移进行 lint 检查。 |
### ORM / 查询层
| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| `entgo.io/ent` | v0.14.x（最新：2026 年 3 月） | Schema 定义、复杂图查询、代码生成 | 元数据模型是一个图（资产 → 血缘 → 列 → 规则）。Ent 基于边的 Schema 与此直接对应。100% 类型安全的生成代码，无运行时反射。Atlas 集成迁移。支持 PostgreSQL 和 SQLite。 |
| `sqlc` | v1.31.x（最新：2026 年 4 月） | 高性能读查询、报表、审计日志读取 | 用于热读路径（目录搜索、血缘遍历查询、审计导出），手写 SQL + 类型安全生成代码优于 ORM 开销。 |
| `database/sql` + `pgx/v5` | pgx v5.x | 原始 PostgreSQL 驱动 | pgx 是现代高性能的 Postgres 驱动。被 River 使用。直接用于批量操作和 COPY FROM。 |
### HTTP API 框架
| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| `github.com/go-chi/chi/v5` | v5.2.5（2026 年 2 月） | 平台后端的 REST API 路由 | 纯 net/http 兼容——Go 生态系统中所有中间件均可使用。支持子路由组合。轻量（无魔法）。符合 Go 习惯。最易测试。无框架锁定意味着连接器 SDK 保持可移植性。 |
- **Echo v4/v5**：强大框架，中间件完善，略微偏主观。Casbin 中间件可用。如果团队偏好更丰富的框架约定，是可接受的备选。Echo v5 正在活跃开发中。
- **Gin**：最流行（根据 JetBrains 2025 年调查，48% 的 Go 开发者使用），但在与标准库中间件组合时在 net/http 之上增加了抽象层。`gin.Context` 不是 `http.Request`——对库使用者来说不方便。
- **Fiber**：基于 fasthttp，而非 net/http。破坏生态兼容性。不应用于将暴露公共 SDK 的平台——第三方连接器可能引入 net/http 中间件。
### 连接器接口（插件系统）
| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| `connectrpc/connect-go` | v1.19.x（2026 年 4 月） | 连接器 RPC 协议 | 支持 HTTP/1.1 和 HTTP/2。可用 curl 测试。兼容现有 gRPC 客户端。浏览器调用无需代理。比 grpc-go（130K 行代码）更简洁。已达生产就绪。 |
| `hashicorp/go-plugin` | v1.7.x（2025 年 8 月） | 进程外连接器子进程管理 | 在 Terraform、Vault、Nomad 中经过生产验证。连接器作为隔离子进程运行——连接器崩溃不会影响平台主进程。语言无关（连接器可用任意语言编写）。通过 stdio 的 gRPC 传输。 |
| Protobuf（`google.golang.org/protobuf`） | v1.x | 连接器接口 IDL | 连接器规范的单一真实来源。语言无关。版本稳定的连接器 ABI。 |
### 血缘捕获
| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| OpenLineage 事件规范（JSON） | 1.x | 血缘事件格式 | 开放标准，被 Airflow、Spark、dbt、Flink、Debezium 采用。兼容的事件发送使平台血缘可被外部工具（Marquez）查询。ThijsKoot/openlineage-go 提供 Go 客户端。 |
| 进程内同步捕获 | — | 物化时的字段级血缘记录 | 异步方式（Kafka/NATS）增加运维负担。在第一阶段，在与资产元数据相同的事务中同步写入血缘。待吞吐量有需求时，在后续阶段解耦为异步事件总线。 |
| `ThijsKoot/openlineage-go` | latest | 用于发送 OpenLineage 事件的 Go 客户端 | 唯一持续维护的 OpenLineage Go 客户端。部分为社区库，但对于事件发送已足够。 |
### 授权（RBAC + 列级访问控制）
| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| `casbin/casbin` | v2.135.x（2025 年 12 月，v3 开发中） | RBAC + ABAC 策略执行 | 支持 ACL、RBAC、ABAC。策略模型外置到 `.conf` 文件——修改策略语义无需改代码。Postgres 适配器可用。在 Go 数据/平台工具中广泛使用。 |
| `golang-jwt/jwt/v5` | v5.3.x（2026 年 1 月） | JWT 令牌创建和验证 | jwt-go 的维护继承者。v5 增加了正确的声明验证、ECDSA/RSA-PSS 支持、改进的错误处理。 |
| `golang.org/x/oauth2` | latest | OAuth2 / OIDC 集成用于 SSO | 官方 Go OAuth2 包。用于 OIDC 集成（如连接组织 IdP）。 |
### 前端
| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| React + TypeScript | React 19.x, TS 5.x | UI 框架 | Dagster 的 UI 是 React——贡献者生态熟悉。对于复杂数据模型的平台 UI，TypeScript 不可或缺。 |
| Vite | 6.x | 构建工具 | 比 CRA 快 10 倍的 HMR。2025 年新 React 项目的标准。 |
| TanStack Router | v1.x | 客户端路由 | 端到端完整类型安全（路由参数有类型）。与 Vite 配合。对于类型安全优先项目优于 React Router。 |
| TanStack Query（React Query） | v5.x | 服务器状态管理 | 缓存、后台重新获取、stale-while-revalidate 用于运行状态轮询。适合频繁服务器状态的数据平台 UI 的标准方案。 |
| shadcn/ui + Tailwind CSS | shadcn v2.x, Tailwind v4.x | 组件库 | 组件复制到项目中（非外部依赖）。对未使用组件零 bundle 开销，完全可自定义。TypeScript 优先。2025 年自定义设计数据平台 UI 的最佳开发体验。 |
| ReactFlow (xyflow) | v12.x | DAG / 血缘图可视化 | 专为节点图 UI 构建。原生 dagre 布局支持层级 DAG。交互式：缩放、平移、每种资产类型的自定义节点。Dagster、n8n 等数据平台使用。比 Cytoscape.js 在 React 中有更好的开发体验。 |
| Recharts 或 Visx | latest | 时间序列图表（质量历史、运行时间线） | Recharts 用于简单图表；Visx（Airbnb）用于复杂自定义可视化。两者均为 React 原生且对 TypeScript 友好。 |
| Zustand | v5.x | 轻量级全局 UI 状态 | 用于纯 UI 状态（选中的血缘节点、侧边栏状态）。TanStack Query 处理服务器状态；Zustand 处理临时 UI 状态。避免 Redux 复杂性。 |
### 可观测性 + 日志
| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| `log/slog`（标准库） | Go 1.21+ | 结构化日志 | Go 1.21 起内置于标准库。JSON 和文本处理器。无外部依赖。 |
| `prometheus/client_golang` | v1.x | 指标暴露 | Prometheus 是 Go 服务指标的事实标准。支持自托管部署的 Grafana 仪表板。 |
| OpenTelemetry Go SDK | v1.x | 分布式追踪 | `go.opentelemetry.io/otel`。用于追踪资产物化执行 span。第一阶段可选，但从第一天起就桩接口。 |
### 基础设施 / 部署
| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| Docker Compose | v2.x | 单机开发部署 | 满足"单机运行"约束。平台 + Postgres + 可选监控栈。 |
| Kubernetes（Helm chart） | — | 生产部署 | 自托管数据平台部署的标准。第 2 阶段及以后。 |
| `goreleaser` | v2.x | 二进制发布构建 | 跨平台 Go 二进制发布。开源 Go 工具的标准。 |
## 备选方案对比
| 类别 | 推荐方案 | 备选方案 | 未选择原因 |
|---|---|---|---|
| 工作流引擎 | River + 自研 DAG 调度器 | Temporal | 需要外部 Temporal 服务器；对第一阶段自托管单二进制部署过于重量级 |
| 工作流引擎 | River + 自研 DAG 调度器 | go-workflows（内嵌） | 增加 DTFx 风格的协程复杂性；River 更简单且更易理解 |
| ORM | ent | GORM | 基于反射、隐式自动迁移，在复杂图 Schema 上性能下降 |
| ORM | ent | sqlx | 无代码生成，复杂查询需更多手动模板代码 |
| HTTP 框架 | chi | Echo | 两者都好；chi 更精简且原生 net/http——对 SDK 使用者更友好 |
| HTTP 框架 | chi | Fiber | 基于 fasthttp，破坏 net/http 生态兼容性 |
| 插件系统 | hashicorp/go-plugin | Go 原生 `plugin` 包 | 无法卸载，需相同 Go 二进制版本，无子进程隔离 |
| 组件库 | shadcn/ui | Ant Design | bundle 体积大，设计语言刚性，难以自定义 |
| 图可视化 | ReactFlow | Cytoscape.js | 基于 Canvas，不适合 React 富交互节点 |
| 授权 | Casbin | 自研 RBAC | Casbin 能正确处理边界情况；自研 RBAC 是陷阱 |
| 迁移 | Atlas | golang-migrate | golang-migrate 的脏状态失败模式对于 CI/CD 优先平台不可接受 |
| 血缘事件格式 | OpenLineage JSON | 自定义 Schema | OpenLineage 是开放标准；使用它可实现生态互操作 |
## 安装快照
# 核心后端
# 开发工具
# 前端
## 可信度评估
| 评估项 | 可信度 | 备注 |
|---|---|---|
| River 用于作业队列 | HIGH | 当前版本 v0.35.x 已于 2026 年 4 月在 pkg.go.dev 验证；持续维护；生产使用已确认 |
| chi 用于 HTTP | HIGH | v5.2.5 已于 2026 年 2 月验证；模式成熟稳定 |
| ent 用于 ORM | HIGH | v0.14.x 已于 2026 年 3 月验证；在图形 Schema 中广泛采用 |
| sqlc 用于读查询 | HIGH | v1.31.1 已于 2026 年 4 月验证；稳定 |
| hashicorp/go-plugin 用于连接器 | HIGH | v1.7.0 已于 2025 年 8 月验证；在 Terraform/Vault 生产中使用 |
| connect-go 用于 RPC | HIGH | v1.19.2 已于 2026 年 4 月验证；由 Buf.build 持续维护 |
| heimdalr/dag 用于 DAG | HIGH | v1.5.1 已于 2026 年 4 月验证；线程安全，拓扑排序已确认 |
| Atlas 用于迁移 | HIGH | 由 ent 项目自身推荐；能优雅处理脏状态 |
| Casbin 用于 RBAC | HIGH | v2.135.x 已于 2025 年 12 月验证；v3 开发中但 v2 稳定 |
| golang-jwt/v5 用于 JWT | HIGH | v5.3.1 已于 2026 年 1 月验证 |
| shadcn/ui + ReactFlow | MEDIUM | ReactFlow v12.x 已验证；shadcn 持续维护——具体版本锁定应在项目初始化时完成 |
| 自研 DAG 调度器（非 Temporal） | MEDIUM | 第一阶段的正确决策；如果多工作节点分布式执行成为硬性需求，可能需要重新评估 |
| OpenLineage Go 客户端 | LOW | ThijsKoot/openlineage-go 是社区维护，非 OpenLineage 官方项目。可能需要 vendor 并在本地维护。通过 HTTP 发送 JSON 格式足够简单，如客户端不足可直接实现。 |
## 参考来源
- River: https://riverqueue.com/ and https://pkg.go.dev/github.com/riverqueue/river
- heimdalr/dag: https://pkg.go.dev/github.com/heimdalr/dag
- ent: https://entgo.io/ and https://pkg.go.dev/entgo.io/ent
- sqlc: https://docs.sqlc.dev/ and https://pkg.go.dev/github.com/sqlc-dev/sqlc
- chi: https://pkg.go.dev/github.com/go-chi/chi/v5
- connect-go: https://connectrpc.com/ and https://pkg.go.dev/github.com/connectrpc/connect-go
- hashicorp/go-plugin: https://github.com/hashicorp/go-plugin and https://pkg.go.dev/github.com/hashicorp/go-plugin
- Casbin: https://casbin.apache.org/ and https://pkg.go.dev/github.com/casbin/casbin/v2
- golang-jwt: https://pkg.go.dev/github.com/golang-jwt/jwt/v5
- Atlas vs golang-migrate: https://atlasgo.io/blog/2025/04/06/atlas-and-golang-migrate
- ReactFlow: https://reactflow.dev/
- OpenLineage: https://openlineage.io/ and https://github.com/ThijsKoot/openlineage-go
- Go ORM 对比: https://www.bytebase.com/blog/golang-orm-query-builder/ and https://www.glukhov.org/post/2025/03/which-orm-to-use-in-go/
- Go HTTP 框架对比: https://blog.logrocket.com/top-go-frameworks-2025/ and https://blog.jetbrains.com/go/2026/04/28/popular-golang-web-frameworks/
- shadcn vs Ant Design: https://www.subframe.com/tips/ant-design-vs-shadcn
- Temporal Go SDK: https://docs.temporal.io/develop/go and https://pkg.go.dev/go.temporal.io/sdk
<!-- GSD:stack-end -->

<!-- GSD:conventions-start source:CONVENTIONS.md -->
## 约定

约定尚未建立。将在开发过程中随模式涌现而补充。
<!-- GSD:conventions-end -->

<!-- GSD:architecture-start source:ARCHITECTURE.md -->
## 架构

架构尚未映射。遵循代码库中现有的模式。
<!-- GSD:architecture-end -->

<!-- GSD:skills-start source:skills/ -->
## 项目技能

未找到项目技能。可将技能添加到以下任一目录：`.claude/skills/`、`.agents/skills/`、`.cursor/skills/` 或 `.github/skills/`，并附带 `SKILL.md` 索引文件。
<!-- GSD:skills-end -->

<!-- GSD:workflow-start source:GSD defaults -->
## GSD 工作流规范

在使用 Edit、Write 或其他文件变更工具之前，请通过 GSD 命令启动工作，以保持规划产物和执行上下文同步。

使用以下入口点：
- `/gsd-quick` 用于小型修复、文档更新和临时任务
- `/gsd-debug` 用于调查和错误修复
- `/gsd-execute-phase` 用于计划阶段的工作

除非用户明确要求绕过，否则不要在 GSD 工作流之外直接编辑仓库。
<!-- GSD:workflow-end -->



<!-- GSD:profile-start -->
## 开发者画像

> 画像尚未配置。运行 `/gsd-profile-user` 以生成您的开发者画像。
> 本节由 `generate-claude-profile` 管理——请勿手动编辑。
<!-- GSD:profile-end -->
