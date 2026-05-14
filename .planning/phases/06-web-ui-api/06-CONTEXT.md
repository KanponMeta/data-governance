# Phase 6: Web UI 与 API - Context

**Gathered:** 2026-05-10
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 6 为数据治理平台提供完整的 REST/gRPC API 和 React SPA。用户可以浏览资产、探索血缘 DAG、管理治理工作流和管理平台——全部通过 ConnectRPC 类型的 API 实现，由单个 Go 二进制文件与嵌入式 React UI 提供支持。

范围：完整 API 表面（ CORE-04、CORE-05）、React SPA（UI-01..07）、目录搜索（META-04）、血缘 DAG 可视化（LINE-04、LINE-05）、质量仪表板（QUAL-06）。

不在范围内：实时更新的 SSE/推送（推迟到 v1.x）、行级安全（v2）、SSO/OIDC（v2）、单次运行的 stdout 捕获（v1.x）、服务端血缘布局预计算（v1.x）、CJK 分词（v1.x 通过 pg_jieba/zhparser）、refresh-token 端点（v1.x）。
</domain>

<decisions>
## Implementation Decisions

### API Surface & Protocol

- **D-01:** ConnectRPC 是 REST 和 gRPC 两种协议的 API 协议——单一 IDL（protobuf），两个协议从相同的处理器提供服务。chi REST 处理器迁移到 ConnectRPC；迁移期间添加 swaggo 注解以生成 OpenAPI 文档。`protoc-gen-connect-openapi` 在终态生成 OpenAPI 规范。
- **D-02:** 所有 API 处理器（现有 chi + 新 ConnectRPC）位于 `internal/api/`。Phase 6 一次完成迁移——遗留端点添加 swaggo 注解，新端点使用 ConnectRPC。两者都在 OpenAPI 中记录。
- **D-03:** OpenAPI 文档通过 swaggo（`github.com/swaggo/swag`）注解在迁移期间生成。ConnectRPC 原生端点的终态使用 `protoc-gen-connect-openapi`。

### Frontend Architecture

- **D-04:** React SPA 位于仓库根目录的 `web/`。通过 `go:embed` 在构建时打包进 Go 二进制文件。开发工作流：Vite 开发服务器将 API 调用代理到 Go 后端（开发时为独立进程）。
- **D-05:** 前端包管理器：**pnpm**。根 `package.json` 中的所有前端包条目使用 pnpm workspace 约定。
- **D-06:** UI 中的认证：httpOnly Secure cookie 用于会话令牌 + CSRF 令牌头用于状态变更请求。`/login` 是一个专用路由。令牌过期 → 硬重定向到 `/login`。`GET /v1/me` 返回用户 + 角色 + 权限标志供 UI 使用。

### Catalog Search Backend

- **D-07:** 搜索机制：通过 `tsvector` + GIN 索引的 **Postgres FTS**。现有表添加生成的 `tsvector` 列。索引填充：Postgres `GENERATED` 列或触发器维护。
- **D-08:** 中文/CJK 分词：英文优先，CJK 通过简单配置尽力支持。完整 CJK 支持通过 pg_jieba/zhparser 是 **v1.x**——推迟，不在范围内。
- **D-09:** 搜索 REST 契约：单一端点 `GET /v1/catalog/search?q=<query>&type=<asset|column|table>&page=<n>`。返回带高亮的排名结果。v1 无分面搜索。

### Plan Partitioning

- **D-10:** Phase 6 使用 **横向-纵向** 分区：
  - 横向（第一波）：ConnectRPC 迁移计划——首先建立 API 形态
  - 纵向（第二波及之后）：功能计划（资产仪表板 UI-01，然后其他）
- **D-11:** 首个功能计划：**资产仪表板（UI-01）**——用户落地最多的高流量屏幕。ConnectRPC 迁移作为预设计划先行。

### Lineage DAG Visualization

- **D-12:** 大图处理：**按需邻域**——焦点节点 + 可配置深度从 API 获取。不加载完整图。前端请求 `(asset_id, focus, depth)` 并渲染子图。
- **D-13:** 列钻取 UX：**节点点击时侧边面板**——点击资产节点打开侧边面板，显示该节点的资产元数据 + 列级血缘。独立于主 DAG 画布。
- **D-14:** 资产与列渲染：**一个画布，两个缩放级别**。资产级显示资产节点和边。放大资产显示列节点和列边。切换或平滑缩放过渡。
- **D-15:** 布局算法：**客户端 dagre 通过 ReactFlow**——`dagre` 计算布局，ReactFlow 渲染。v1 无服务端布局计算。

### Real-Time Updates

- **D-16:** 推送模型：**TanStack Query 轮询，v1 无服务端推送**。轮询是默认值；SSE/服务端流是 v1.x。
- **D-17:** 热屏幕（更高轮询频率）：
  - 活动运行：3-5s 轮询
  - 治理收件箱：15-30s 轮询
  - 质量告警：15-30s 轮询
  - 资产仪表板 / 目录：60s 轮询
- **D-18:** TanStack Query（`@tanstack/react-query` v5）管理所有服务端状态：缓存、后天重新获取、stale-while-revalidate。

### Logs Surfacing

- **D-19:** 日志源：`event_log` 表按 `run_id` 过滤。按 `run.step.*`、`schedule.*`、`sensor.*` 事件的 `event_type` 前缀过滤。
- **D-20:** 分页：`run_id, seq` 上的游标分页 + 时间窗口过滤。前端发送 `after=<seq>&limit=50`。由 sqlc 查询支持。
- **D-21:** 实时尾随：活动运行期间以 3-5s 周期轮询的静态页面。v1 无 WebSocket/流。

### Auth Flow in UI

- **D-22:** 令牌存储：httpOnly Secure cookie（同会话 cookie，非 localStorage）。CSRF 令牌在状态变更操作的 `X-CSRF-Token` 请求头中。
- **D-23:** 登录 UX：专用 `/login` 路由。表单提交到 `POST /v1/auth/login`。成功时服务器设置 cookie，重定向到 `/`。401 时 UI 重定向到 `/login`。
- **D-24:** 令牌过期行为：401 响应时硬 logout 到 `/login`。v1 无静默刷新。
- **D-25:** UI 中的授权：`GET /v1/me` 返回 `{id, email, name, roles[], permissions: {canApprove, canEditPolicies, canManageUsers}}`。UI 由此派生导航和操作可见性。

### Claude's Discretion

以下内容由研究者/规划者自行决定：
- 迁移期间确切的 swaggo 注解密度（每处理器 vs 每路由组）
- `web/src/` 内的 React 组件命名约定和文件夹结构
- TanStack Query 查询键组织方案
- ReactFlow 节点组件实现细节（自定义节点 vs 基础节点）
- React 应用内的 CSS 方案（按 CLAUDE.md 技术栈的 Tailwind v4）
- 每个功能区域的具体 API 端点路径（研究者从需求映射）
- 嵌入式 SPA 的测试策略（单元 vs 集成 vs e2e）

</decisions>

<canonical_refs>
## Canonical References

**下游代理必须在规划或实现之前阅读这些文件。**

### Requirements & Roadmap
- `.planning/REQUIREMENTS.md` — CORE-04 (REST API), CORE-05 (gRPC/ConnectRPC), AUTH-04 (JWT/session), UI-01..07 (Web UI), LINE-04 (血缘图 UI), LINE-05 (列钻取), META-04 (目录搜索), QUAL-06 (质量仪表板)
- `.planning/ROADMAP.md` §Phase 6 — 验收标准、依赖、UI 提示: yes

### Project Context
- `.planning/PROJECT.md` §核心价值 — "下游使用者能信任所用数据" + "清楚地知道谁有权访问" — 驱动 UI-01/UI-02/UI-07 重点
- `.planning/PROJECT.md` §约束 — "Go 后端" + "自包含" + "连接器可扩展性" — D-04 (嵌入式 SPA) 满足"单二进制"
- `.planning/PROJECT.md` §关键决策 — "v1 开源（自托管）" — 嵌入式 SPA 是正确的交付机制

### Prior Phase Decisions
- `.planning/phases/01-infrastructure/01-CONTEXT.md` — D-09 event_log RLS-不可变性；D-14 auth 中间件模式（D-22/D-23 扩展）
- `.planning/phases/02-execution-engine/02-CONTEXT.md` — D-06 River 作业队列；D-17 `runs.state` 生命周期（D-19 中质量状态为 QUAL-06 仪表板提供参考）
- `.planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md` — D-01..D-04 调度器架构（D-17 热屏幕轮询由调度器 tick 提供参考）
- `.planning/phases/04-schema/04-CONTEXT.md` — D-13 column_edges（D-12/D-14 血缘 DAG 遍历）；D-17 元数据三层；D-19 Go+REST+CLI 三层表面模式（D-03 OpenAPI 遵循）
- `.planning/phases/05-governance/05-CONTEXT.md` — D-08 治理状态机；D-12 REST+CLI 治理对称性（D-25 /me 端点遵循相同模式）；D-21 通知路由

### Discuss Phase Decisions
- `.planning/phases/06-web-ui-api/06-DISCUSS-CHECKPOINT.json` — 所有 8 个区域完成决策；D-01..D-25 的真实来源

### Tech Stack (CLAUDE.md)
- `CLAUDE.md` §技术栈 §HTTP API 框架 — chi v5.2.5（迁移到 ConnectRPC）
- `CLAUDE.md` §技术栈 §连接器接口 — connect-go v1.19.x（D-01）
- `CLAUDE.md` §技术栈 §前端 — React 19.x + TypeScript 5.x + Vite 6.x + TanStack Router v1.x + TanStack Query v5.x + shadcn/ui + Tailwind CSS v4.x + ReactFlow v12.x + Recharts（D-04/D-05/D-13/D-15/D-16/D-18）
- `CLAUDE.md` §技术栈 §授权 — golang-jwt v5.3.x（D-22/D-23）

### External References
- ConnectRPC docs: https://connectrpc.com/docs/go/getting-started
- swaggo/swag: https://github.com/swaggo/swag
- TanStack Query v5: https://tanstack.com/query/latest/docs/framework/react/overview
- ReactFlow: https://reactflow.dev/docs/guides/layouting/

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets (from Phases 1–5)
- **`internal/api/`** — 现有 chi 处理器。Phase 6 每次一个端点迁移到 ConnectRPC。网关模式：connect-go 处理器包装现有服务方法。
- **`internal/auth/`** — JWT 验证中间件、用户服务。Phase 6 扩展 `/v1/me` 端点和基于 cookie 的 UI 会话。
- **`internal/event/`** — `event_log` 表 schema。Phase 6 用这个来暴露日志（D-19/D-20）。
- **`internal/lineage/`** — 血缘图数据（资产边 + column_edges）。Phase 6 通过 API 暴露这些用于 DAG 可视化。
- **`internal/storage/`** — 资产的 ent schema、runs、schedules。Phase 6 从这些表读取用于 UI 仪表板。
- **`internal/governance/`** — 治理状态机。Phase 6 通过 REST 暴露这些用于治理收件箱 UI。
- **`cmd/platform/`** — 现有 CLI 子命令。Phase 6 添加 UI dev 命令（`./platform ui dev`）。
- **`riverqueue/river`** —已在 go.mod 中。UI 可以在需要时使用 River 客户端进行作业状态轮询。

### Established Patterns
- **三表面库（Phase 4 D-19）：** API 遵循 Go 包 + REST + CLI 模式。Phase 6 UI 访问 REST。
- **事件驱动架构：** `event_log` 是运行状态的真实来源。UI 通过 REST 轮询，非 WebSocket。
- **软删除/时间表（Phase 4 D-15）：** 用于 `asset_versions` 和 `column_edges`。UI 搜索可以利用相同模式。
- **子命令-模式二进制文件：** `./platform` 已有 `materialize`、`lineage`、`schema`、`impact`、`governance`、`audit`。Phase 6 添加 `ui` 子命令用于开发服务器。

### Integration Points
- **`internal/api/`** — UI-01..UI-07 + LINE-04/LINE-05 + META-04 + QUAL-06 的新 ConnectRPC 处理器
- **`internal/auth/middleware.go`** — 扩展 CSRF cookie 验证 + `/v1/me` 端点
- **`web/`**（新建）— React SPA，构建时嵌入。Vite dev 服务器代理到 Go 后端。
- **`migrations/`** — 可能需要目录搜索的 FTS 索引迁移（D-07）

</code_context>

<specifics>
## Specific Ideas

- **嵌入式 SPA 是不可妥协的**——单二进制约束意味着 `go:embed` React 构建输出。无单独的前端部署。
- **Vite 代理配置**——`vite.config.ts` 代理 `/api`、`/v1`、`/auth` 到 `localhost:8080`（或 `PLATFORM_PORT` env）。生产中 Go 提供静态资产；开发中 Vite 代理。
- **v1 无移动端 UI**——SPA 响应式布局是 v1.x 的考虑因素。桌面优先。
- **终态 OpenAPI 规范**——迁移期间 swaggo 是务实的；终态 `protoc-gen-connect-openapi` 生成规范规范。两者在过渡期间共存。
- **邻域 API 形态：** `GET /v1/lineage/neighborhood?asset_id=<id>&depth=<n>` 返回 `{nodes: [], edges: []}`——资产节点 + 带过滤的列节点。

</specifics>

<deferred>
## Deferred Ideas

- **SSE/服务端流用于实时更新**——v1.x。TanStack Query 轮询对 v1 足够。
- **通过 pg_jieba/zhparser 的 CJK 分词**——v1.x。英文优先 FTS 是 v1 方法。
- **Refresh-token 端点（AUTH-05）**——v1.x。硬 logout + 重新登录是 v1。
- **每运行 free-text 日志/stdout 捕获**——v1.x。event_log 在 v1 中仅为结构化。
- **ConnectRPC 服务端流用于实时运行更新**——v1.x。轮询是 v1 机制。
- **服务端血缘布局预计算（ELK 风格）**——v1.x。客户端 dagre 是 v1。
- **额外通知渠道（Slack、SES、SendGrid）**——Phase 5 推迟到 v1.x；webhook 是 v1 的扇出机制。

</deferred>

---

*Phase: 06-web-ui-api*
*Context gathered: 2026-05-10*
