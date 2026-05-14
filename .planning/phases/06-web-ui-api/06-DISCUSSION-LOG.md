# Phase 6: Web UI 与 API - 讨论日志

> **仅供审计跟踪。** 不要用作规划、研究或执行代理的输入。
> 决策在 CONTEXT.md 中捕获 — 此日志保留考虑的替代方案。

**日期：** 2026-05-10
**阶段：** 06-web-ui-api
**讨论领域：** API surface scope, Frontend repo & build, Catalog search backend, Plan partitioning hint, Lineage DAG strategy, Real-time updates, Logs surfacing, Auth flow in UI

---

## API surface scope

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| ConnectRPC (single IDL) | One protobuf IDL, serve both REST and gRPC from same handlers | ✓ |
| Separate REST + gRPC | OpenAPI for REST, protobuf for gRPC, no shared IDL | |

**用户选择：** ConnectRPC (single IDL)
**备注：** 所有 API 处理器（现有 chi + 新 ConnectRPC）都在 internal/api/ 中。迁移：迁移期间 swaggo 注释；终态 protoc-gen-connect-openapi。

---

## Frontend repo & build

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| web/ at repo root | React SPA at web/ in repo root, go:embed at build time | ✓ |
| Separate frontend repo | Frontend in its own repo, imported as module | |

**用户选择：** web/ at repo root, go:embed at build time
**备注：** 开发工作流：Vite 开发服务器代理到 Go。前端包管理器：pnpm。

---

## Catalog search backend

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| Postgres FTS (tsvector + GIN) | Existing tables get GENERATED tsvector column + GIN index | ✓ |
| Elasticsearch/OpenSearch | Separate search cluster, index platform data | |
| SQLite FTS5 | For dev/CI only; not production scale | |

**用户选择：** Postgres FTS (tsvector + GIN)
**备注：** 单一端点 /v1/catalog/search。CJK 分词：英文优先，CJK 尽力通过简单配置。完整 pg_jieba/zhparser 推迟到 v1.x。

---

## Plan partitioning hint

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| Horizontal-then-vertical hybrid | ConnectRPC migration plan first, then feature plans | ✓ |
| All-at-once | One big plan for everything | |

**用户选择：** Horizontal-then-vertical hybrid。ConnectRPC 迁移计划优先。首个功能计划：Asset dashboard (UI-01)。

---

## Lineage DAG strategy

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| Neighborhood on demand | Fetch focus node + depth from API, no full graph load | ✓ |
| Full graph load + client-side filter | Load entire graph, filter in browser | |
| Server-side precomputed layout | Precompute layouts server-side, stream to client | |

**用户选择：** Neighborhood on demand
**备注：** 布局算法：客户端 dagre 通过 ReactFlow。列钻取：节点点击时侧边面板。资产 vs 列渲染：一个画布，两个缩放级别。

---

## Real-time updates

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| TanStack Query polling (no push) | Polling is default; SSE/server-streaming is v1.x | ✓ |
| WebSocket push | Real-time push from server to client | |
| SSE (Server-Sent Events) | Server pushes events to client | |

**用户选择：** TanStack Query polling, no push in v1
**备注：** 热屏幕：3-5s 活动运行，15-30s 收件箱/告警，60s 目录。

---

## Logs surfacing

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| event_log filtered by run_id | event_log is the source; cursor pagination on (run_id, seq) | ✓ |
| Separate run_logs table | Dedicated table for UI-visible log entries | |
| stdout capture per run | Capture and store stdout from executor | |

**用户选择：** event_log filtered by run_id, cursor pagination
**备注：** 实时尾随：活动运行期间 TanStack Query 轮询 3-5s 的静态页面。v1 无 WebSocket/流。

---

## Auth flow in UI

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| httpOnly Secure cookie + CSRF | Session cookie + X-CSRF-Token header | ✓ |
| JWT in memory (no cookie) | JWT stored in memory, sent as Authorization header | |
| OAuth 2.0 + PKCE | Social login / external IdP | |

**用户选择：** httpOnly Secure cookie + CSRF token
**备注：** GET /v1/me 返回 user + roles + permission flags。401 时硬 logout 到 /login。v1 无静默刷新。

---

## 推迟的想法

- CJK 分词 via pg_jieba/zhparser — v1.x
- Refresh-token endpoint (AUTH-05?) — v1.x
- Per-run logger free-text / stdout capture — v1.x
- ConnectRPC server streaming for live updates — v1.x
- Server-side ELK / precomputed lineage layout — v1.x
