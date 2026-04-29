# Phase 1: 基础设施 - Context

**Gathered:** 2026-04-29
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 1 delivers the platform skeleton that all downstream phases depend on:
- Single-binary startup via Docker Compose with a healthy PostgreSQL storage layer
- Versioned migrations (Atlas + ent)
- Append-only structured event log with database-enforced immutability
- User authentication (email/password + JWT)
- Stable, versioned connector interface (protobuf IDL + Go interface — no subprocess scaffolding yet)
- Minimal REST + gRPC framework skeleton with chi router and connect-go server stub
- API stubs: health/readiness endpoints + complete Auth routes

New capabilities (asset definitions, DAG execution, connector implementations, UI) belong in later phases.

</domain>

<decisions>
## Implementation Decisions

### D-01: 连接器接口范围 (CONN-08)
- **D-01:** Phase 1 只定义 protobuf IDL + Go interface 类型。hashicorp/go-plugin 的子进程管理和 gRPC over stdio 传输留到 Phase 2 连接器实现阶段搭建。
- **D-02:** IDL 覆盖四个核心操作：Read、Write、Schema、Ping（健康检查）。不包含 Execute、Metadata、CatalogList 等扩展操作。
- **D-03:** v1 只预期 Go 语言连接器。proto 文件是内部实现细节，无需暴露为公开 API 入口（不需要顶级 `api/` 目录）。

### D-04: Go 项目布局
- **D-04:** 采用功能领域分层布局：
  - `cmd/platform/` — 主二进制入口
  - `internal/storage/` — PostgreSQL 存储抽象层 + 接口定义
  - `internal/auth/` — JWT 认证、用户管理
  - `internal/connector/` — 连接器 Go interface + proto IDL + 生成代码
    - `internal/connector/proto/` — .proto 文件
    - `internal/connector/gen/` — protoc 生成的 Go 代码
  - `internal/event/` — 事件日志写入器和事件类型定义
  - 不使用顶级 `pkg/` 目录（v1 无对外导出库的需求）

### D-05: API 骨架范围 (CORE-05)
- **D-05:** Phase 1 搭建最小化框架骨架：
  - chi Router + 中间件链（JWT 验证、结构化日志 slog、错误格式化）
  - 健康检查 / Readiness 端点
  - 完整 Auth HTTP 路由（注册、登录、邀请接受）
  - connect-go gRPC server 空壳，带 placeholder handler，Phase 2+ 填充具体路由
- **D-06:** 全局 HTTP 错误响应格式采用 **RFC 7807 Problem+JSON**：`{"type", "title", "status", "detail"}` 结构，Phase 1 定为全局规范。

### D-07: 事件日志设计 (CORE-03)
- **D-07:** 单一统一表 `event_log`，列包含：`id`、`occurred_at`、`event_type`（枚举）、`actor_id`、`resource_type`、`resource_id`、`payload JSONB`。
- **D-08:** 每种事件类型的 payload 结构在 Go struct 中定义，以类型安全的方式序列化写入。表结构简单，事件语义封装在 Go 层。
- **D-09:** 不可修改性通过 **PostgreSQL Row Security (RLS)** 强制：应用数据库用户对 `event_log` 的 UPDATE/DELETE 权限在数据库层面拒绝，平台代码无法绕过。
- **D-10:** Phase 1 的 `event_type` 枚举仅包含 Auth + 平台生命周期事件：
  - `user.registered`, `user.invited`
  - `auth.login`, `auth.logout`, `auth.token_expired`
  - `platform.started`, `platform.migration_applied`
  - 执行引擎事件（`run.*`）由 Phase 2 添加，治理事件（`governance.*`）由 Phase 5 添加。

### Claude's Discretion
- SQLite 嵌入式开发模式：Phase 1 是否同时实现 SQLite 后端由 Claude 决定（依据工作量评估）。最低要求是 PostgreSQL 可运行。
- ent schema 的具体字段设计和索引策略。
- JWT 刷新令牌策略（access token 必须有，refresh token 是否在 Phase 1 实现由 Claude 评估）。
- chi 中间件链的具体组合顺序。

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### 需求
- `.planning/REQUIREMENTS.md` — Phase 1 在用需求：CORE-01, CORE-02, CORE-03, CORE-04, CORE-05, AUTH-01, AUTH-02, AUTH-03, AUTH-04, CONN-08

### 技术栈决策
- `CLAUDE.md` §技术栈 — 完整的技术选型表及选用理由（River、ent、Atlas、chi、connect-go、hashicorp/go-plugin、Casbin、golang-jwt、shadcn/ui 等）
- `CLAUDE.md` §备选方案对比 — 已排除的方案及原因（GORM、Gin、Fiber、golang-migrate 等）

### 路线图
- `.planning/ROADMAP.md` §Phase 1: 基础设施 — 验收标准和依赖关系

No external specs or ADRs — all decisions captured in decisions section above.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- None — project is brand new, no existing code.

### Established Patterns
- None yet — Phase 1 establishes all foundational patterns for downstream phases.

### Integration Points
- Phase 1 exports the following as stable contracts for Phase 2:
  - `internal/connector` Go interface (Read, Write, Schema, Ping)
  - `internal/storage` storage abstraction interface (testability layer for CORE-01)
  - `internal/event` event writer API
  - `internal/auth` JWT middleware (reused by all Phase 2+ routes)

</code_context>

<specifics>
## Specific Ideas

- 不可变事件日志通过 PostgreSQL RLS 而非仅应用层保证——这与 Phase 5 的哈希链审计日志保持一致的防篡改设计原则。
- v1 仅预期 Go 连接器，因此 proto 文件不需要作为公开 API 暴露；Phase 2+ 如有非 Go 连接器需求，再将 proto 移到 `api/` 目录。

</specifics>

<deferred>
## Deferred Ideas

- **SQLite 嵌入式模式**：CLAUDE.md 提到 SQLite 用于开发模式，是否在 Phase 1 实现双后端（PostgreSQL + SQLite）留给 Claude 评估——不作为强制决策。
- **非 Go 连接器支持**：v1 确定为 Go-only，非 Go 语言连接器（Python、Java、Rust）推迟到 v2 或后续里程碑。
- **JWT 刷新令牌**：访问令牌（access token）必须在 Phase 1 实现；刷新令牌（refresh token）流程暂列为 Claude 自行评估项。
- **gRPC 具体路由**：connect-go 框架在 Phase 1 只建空壳，Phase 2+ 填充资产、运行、血缘等具体 service。

None — discussion stayed within phase scope.

</deferred>

---

*Phase: 01-infrastructure*
*Context gathered: 2026-04-29*
