# 技术栈

**项目：** 数据治理 + 编排平台（Go 原生，Dagster 启发）
**调研日期：** 2026-04-29
**可信度：** 核心技术选型 HIGH，少数库级决策 MEDIUM

---

## 推荐技术栈

### 执行引擎

| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| 自研 DAG 调度器（进程内） | — | 资产依赖解析 + 拓扑排序执行 | Temporal 对第一阶段过于重量级；内嵌调度器使部署简单（单二进制）。Temporal 引入外部服务依赖。 |
| `riverqueue/river` | v0.35.x | 物化任务、异步任务、定时调度的作业队列 | 基于 Postgres 的事务性入队（事务提交则任务不丢失）、重试、唯一任务、周期性/cron、Web UI。无需外部消息代理。与 PostgreSQL 元数据存储天然配合。 |
| `heimdalr/dag` | v1.5.x | 内存 DAG 表示、拓扑排序、环检测 | 线程安全、泛型、BFS/DFS 遍历器、拓扑排序、传递性约减、JSON 序列化。v1.5.1 发布于 2026 年 4 月。BSD-3 简洁许可证。 |

**决策理由——为何不选 Temporal：**
Temporal 需要独立运行 Temporal 服务器（或 Temporal Cloud）。对于面向单机开发的自托管开源工具，这在第一阶段是不可接受的外部依赖。Temporal 的持久性原语固然有价值，但 River + 自研 DAG 调度器能以零额外基础设施实现 90% 的功能。当多工作节点分布式执行成为明确需求时，Temporal 可作为后续阶段的可选后端重新评估。

**决策理由——为何不选 go-workflows：**
`cschleiden/go-workflows` 是类 Temporal 的内嵌引擎（支持 SQLite/Postgres 后端）。对本项目有一定价值，但在作业为离散物化任务而非长期协程的场景中，比 River 更复杂。

---

### 元数据存储

| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| PostgreSQL | 16+ | 主元数据存储：资产、运行、血缘、质量、审计日志 | 结构化元数据的行业标准。River 本就需要 Postgres。JSONB 用于 Schema 快照。强大的外键约束保证血缘图完整性。 |
| SQLite | 3.x（via `mattn/go-sqlite3` 或 `modernc.org/sqlite`） | 嵌入式开发模式（单二进制、零配置） | 支持无外部依赖运行 `./platform start`。SQLite 被 go-workflows 和 River 使用（River 的 SQLite 驱动为预览版）。仅用于开发/CI 环境。 |

**避免：** 在第一阶段使用 DynamoDB、Cassandra 或任何分布式存储。这些会增加运维复杂性，与自托管约束相冲突。

---

### 数据库迁移

| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| Atlas（`ariga.io/atlas`） | latest | PostgreSQL 和 SQLite 的 Schema 迁移 | 声明式 Schema 差异对比——无需手动编写回滚脚本。自动处理脏状态恢复（golang-migrate 不支持）。与 ent schema 集成。在 CI 中对迁移进行 lint 检查。 |

**避免：** 以 `golang-migrate` 作为主工具。它在部分失败后会进入不可恢复的"脏"状态，需要人工干预。对于简单场景可作为备用，但 Atlas 对于 Schema 密集型平台来说严格优于前者。

---

### ORM / 查询层

| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| `entgo.io/ent` | v0.14.x（最新：2026 年 3 月） | Schema 定义、复杂图查询、代码生成 | 元数据模型是一个图（资产 → 血缘 → 列 → 规则）。Ent 基于边的 Schema 与此直接对应。100% 类型安全的生成代码，无运行时反射。Atlas 集成迁移。支持 PostgreSQL 和 SQLite。 |
| `sqlc` | v1.31.x（最新：2026 年 4 月） | 高性能读查询、报表、审计日志读取 | 用于热读路径（目录搜索、血缘遍历查询、审计导出），手写 SQL + 类型安全生成代码在这些场景优于 ORM 开销。 |
| `database/sql` + `pgx/v5` | pgx v5.x | 原始 PostgreSQL 驱动 | pgx 是现代高性能的 Postgres 驱动。被 River 使用。直接用于批量操作和 COPY FROM。 |

**决策：ent 用于写操作和 Schema 所有权；sqlc 用于复杂读查询。**

**避免：** GORM。基于反射、运行时类型断言、隐式自动迁移在生产 Schema 上危险。复杂 join 性能下降。不适合 Schema 敏感型平台。

**避免：** Bun。成熟但对图形 Schema 的生态支持不如 ent。

---

### HTTP API 框架

| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| `github.com/go-chi/chi/v5` | v5.2.5（2026 年 2 月） | 平台后端的 REST API 路由 | 纯 net/http 兼容——Go 生态系统中所有中间件均可使用。支持子路由组合。轻量（无魔法）。符合 Go 习惯。最易测试。无框架锁定意味着连接器 SDK 保持可移植性。 |

**决策：选 chi 而非 echo 或 gin。**

- **Echo v4/v5**：强大框架，中间件完善，略微偏主观。Casbin 中间件可用。如果团队偏好更丰富的框架约定，是可接受的备选。Echo v5 正在活跃开发中。
- **Gin**：最流行（根据 JetBrains 2025 年调查，48% 的 Go 开发者使用），但在与标准库中间件组合时，在 net/http 之上增加了抽象层造成摩擦。`gin.Context` 不是 `http.Request`——对库使用者来说不方便。
- **Fiber**：基于 fasthttp，而非 net/http。破坏生态兼容性。不应用于将暴露公共 SDK 的平台——第三方连接器可能引入 net/http 中间件。

**OpenAPI 文档推荐：** `swaggo/swag` v2.x——基于注解，与 chi 集成，生成 OpenAPI 3.0。主导工具（根据 2025 年调查，采用率 70% 以上）。

---

### 连接器接口（插件系统）

| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| `connectrpc/connect-go` | v1.19.x（2026 年 4 月） | 连接器 RPC 协议 | 支持 HTTP/1.1 和 HTTP/2。可用 curl 测试。兼容现有 gRPC 客户端。浏览器调用无需代理。比 grpc-go（130K 行代码）更简洁的聚焦库。已达生产就绪。 |
| `hashicorp/go-plugin` | v1.7.x（2025 年 8 月） | 进程外连接器子进程管理 | 在 Terraform、Vault、Nomad 中经过生产验证。连接器作为隔离子进程运行——连接器崩溃不会影响平台主进程。语言无关（连接器可用任意语言编写）。通过 stdio 的 gRPC 传输。 |
| Protobuf（`google.golang.org/protobuf`） | v1.x | 连接器接口 IDL | 连接器规范的单一真实来源。语言无关。版本稳定的连接器 ABI。 |

**架构：连接器是由 hashicorp/go-plugin 管理的进程外子进程，使用 gRPC 传输。连接器接口通过 Protobuf 定义。connect-go 驱动平台自身的 REST/gRPC API 层。**

**子进程模式优于进程内 Go 插件的理由：**
Go 原生 `plugin` 包要求插件与完全相同的 Go 版本一起编译，且无法被卸载。hashicorp/go-plugin 的子进程模型意味着连接器可独立版本化，可用 Python/Java/Rust 编写，且连接器 panic 不会破坏平台状态。这与 Terraform、Vault 及 Vault Provider 在生产中的工作方式一致。

**连接器接口（Protobuf 契约）：**
```
service Connector {
  rpc Spec(SpecRequest) returns (ConnectorSpec);
  rpc Check(CheckRequest) returns (CheckResponse);
  rpc Discover(DiscoverRequest) returns (Catalog);
  rpc Read(ReadRequest) returns (stream Record);
  rpc Write(stream Record) returns (WriteResponse);
}
```
模式借鉴自 Airbyte 协议（pkg.go.dev 上的 bitstrapped/airbyte-go-cdk）。支持 check（凭证验证）、discover（Schema 自省）、read（数据源）和 write（数据汇）。

---

### 血缘捕获

| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| OpenLineage 事件规范（JSON） | 1.x | 血缘事件格式 | 开放标准，被 Airflow、Spark、dbt、Flink、Debezium 采用。兼容的事件发送使平台血缘可被外部工具（如 Marquez）查询。ThijsKoot/openlineage-go 提供 Go 客户端。 |
| 进程内同步捕获 | — | 物化时的字段级血缘记录 | 异步方式（Kafka/NATS）增加运维负担。在第一阶段，在与资产元数据相同的事务中同步写入血缘。待吞吐量有需求时，在后续阶段解耦为异步事件总线。 |
| `ThijsKoot/openlineage-go` | latest | 用于发送 OpenLineage 事件的 Go 客户端 | 唯一持续维护的 OpenLineage Go 客户端。部分为社区库，但对于事件发送已足够。 |

**血缘存储模型：** DAG 以边的形式存储在 PostgreSQL 中（表血缘：asset_id → upstream_asset_id；字段血缘：column_id → upstream_column_id）。`heimdalr/dag` 库用于运行时内存解析；Postgres 表是持久化存储。

---

### 授权（RBAC + 列级访问控制）

| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| `casbin/casbin` | v2.135.x（2025 年 12 月，v3 开发中） | RBAC + ABAC 策略执行 | 支持 ACL、RBAC、ABAC。策略模型外置到 `.conf` 文件——修改策略语义无需改代码。Postgres 适配器可用。在 Go 数据/平台工具中广泛使用。 |
| `golang-jwt/jwt/v5` | v5.3.x（2026 年 1 月） | JWT 令牌创建和验证 | jwt-go 的维护继承者。v5 增加了正确的声明验证、ECDSA/RSA-PSS 支持、改进的错误处理。 |
| `golang.org/x/oauth2` | latest | OAuth2 / OIDC 集成用于 SSO | 官方 Go OAuth2 包。用于 OIDC 集成（如连接组织 IdP）。 |

**列级访问模型：**
```
policy: (role, asset_id, column_id, action) → allow/deny
```
Casbin 的 ABAC 模型原生支持此模式。列掩码在 API 查询层执行——查询规划器在数据返回前根据 Casbin 策略评估对列进行重写或过滤。

**避免：** 自研 RBAC。Casbin 在数千个生产部署中经过测试，能正确处理边界情况（角色继承、域作用域角色）。

---

### 前端

| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| React + TypeScript | React 19.x, TS 5.x | UI 框架 | Dagster 的 UI 是 React——贡献者生态熟悉。对于复杂数据模型的平台 UI，TypeScript 不可或缺。 |
| Vite | 6.x | 构建工具 | 比 CRA 快 10 倍的 HMR。2025 年新 React 项目的标准。 |
| TanStack Router | v1.x | 客户端路由 | 端到端完整类型安全（路由参数有类型）。与 Vite 配合。正获得强劲采用。对于类型安全优先项目优于 React Router。 |
| TanStack Query（React Query） | v5.x | 服务器状态管理 | 缓存、后台重新获取、stale-while-revalidate 用于运行状态轮询。适合频繁服务器状态的数据平台 UI 的标准方案。 |
| shadcn/ui + Tailwind CSS | shadcn v2.x, Tailwind v4.x | 组件库 | 组件复制到项目中（非外部依赖）。对未使用组件零 bundle 开销，完全可自定义。TypeScript 优先。2025 年自定义设计数据平台 UI 的最佳开发体验。 |
| ReactFlow (xyflow) | v12.x | DAG / 血缘图可视化 | 专为节点图 UI 构建。原生 dagre 布局支持层级 DAG。交互式：缩放、平移、每种资产类型的自定义节点。Dagster、n8n 等数据平台使用。比 Cytoscape.js 在 React 中有更好的开发体验。 |
| Recharts 或 Visx | latest | 时间序列图表（质量历史、运行时间线） | Recharts 用于简单图表；Visx（Airbnb）用于复杂自定义可视化。两者均为 React 原生且对 TypeScript 友好。 |
| Zustand | v5.x | 轻量级全局 UI 状态 | 用于纯 UI 状态（选中的血缘节点、侧边栏状态）。TanStack Query 处理服务器状态；Zustand 处理临时 UI 状态。避免 Redux 复杂性。 |

**为何选 shadcn/ui 而非 Ant Design：**
Ant Design 提供更丰富的开箱即用企业组件集（Transfer、Cascader 等），但其 bundle 体积大，自定义需要与组件样式对抗的 CSS 覆盖，且对设计语言有强主见。对于需要鲜明视觉标识和精心设计血缘图 UI 的数据治理平台，shadcn/ui 的复制到项目模式是正确的权衡。使用 ReactFlow + shadcn 原语将复杂的数据专属组件（血缘图、资产目录表格）构建为一等自定义组件。

**为何选 ReactFlow 而非 Cytoscape.js：**
Cytoscape 基于 Canvas，更适合大型图（1万+节点）。ReactFlow 以 React 组件作为节点渲染——支持丰富交互（内联质量徽章、列下钻、悬停卡片）并遵循标准 React 模式。对于很少超过数百节点的血缘图，ReactFlow 的开发体验优势是决定性的。

---

### 可观测性 + 日志

| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| `log/slog`（标准库） | Go 1.21+ | 结构化日志 | Go 1.21 起内置于标准库。JSON 和文本处理器。无外部依赖。 |
| `prometheus/client_golang` | v1.x | 指标暴露 | Prometheus 是 Go 服务指标的事实标准。支持自托管部署的 Grafana 仪表板。 |
| OpenTelemetry Go SDK | v1.x | 分布式追踪 | `go.opentelemetry.io/otel`。用于追踪资产物化执行 span。第一阶段可选，但从第一天起就桩接口。 |

---

### 基础设施 / 部署

| 技术 | 版本 | 用途 | 选用理由 |
|---|---|---|---|
| Docker Compose | v2.x | 单机开发部署 | 满足"单机运行"约束。平台 + Postgres + 可选监控栈。 |
| Kubernetes（Helm chart） | — | 生产部署 | 自托管数据平台部署的标准。第 2 阶段及以后。 |
| `goreleaser` | v2.x | 二进制发布构建 | 跨平台 Go 二进制发布。开源 Go 工具的标准。 |

---

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

---

## 安装快照

```bash
# 核心后端
go get entgo.io/ent@latest
go get github.com/sqlc-dev/sqlc@latest          # 作为工具安装
go get github.com/go-chi/chi/v5@latest
go get connectrpc.com/connect@latest
go get github.com/hashicorp/go-plugin@latest
go get github.com/riverqueue/river@latest
go get github.com/riverqueue/river/riverdriver/riverpgxv5@latest
go get github.com/jackc/pgx/v5@latest
go get github.com/heimdalr/dag@latest
go get github.com/golang-jwt/jwt/v5@latest
go get github.com/casbin/casbin/v2@latest
go get golang.org/x/oauth2@latest
go get github.com/prometheus/client_golang@latest
go get go.opentelemetry.io/otel@latest

# 开发工具
go install ariga.io/atlas/cmd/atlas@latest
go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
go install entgo.io/ent/cmd/ent@latest
go install github.com/swaggo/swag/cmd/swag@latest

# 前端
npm create vite@latest ui -- --template react-ts
cd ui
npm install @tanstack/react-router @tanstack/react-query
npm install @xyflow/react
npm install recharts
npm install zustand
npm install tailwindcss @tailwindcss/vite
npx shadcn@latest init
```

---

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
| OpenLineage Go 客户端 | LOW | ThijsKoot/openlineage-go 是社区维护，非 OpenLineage 官方项目。可能需要 vendor 并在本地维护。如果客户端不足，通过 HTTP 发送 JSON 格式足够简单，可直接实现。 |

---

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
