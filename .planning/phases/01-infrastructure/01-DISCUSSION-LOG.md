# Phase 1: 基础设施 - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-04-29
**Phase:** 01-基础设施
**Areas discussed:** 连接器接口范围, Go 项目结构, API Stub 范围, 事件日志 Schema 设计

---

## 连接器接口范围

| Option | Description | Selected |
|--------|-------------|----------|
| IDL + Go 接口 | 只定义 protobuf IDL 和 Go interface 类型，go-plugin 子进程框架留到 Phase 2 | ✓ |
| 完整 go-plugin 框架 | 同时搭建子进程管理、gRPC over stdio 传输、连接器注册机制 | |
| 仅 Go interface（无 proto） | 用纯 Go interface 定义连接器契约，跳过 protobuf IDL | |

**User's choice:** IDL + Go 接口（核心操作：Read、Write、Schema、Ping）
**Notes:** v1 只预期 Go 连接器，proto 文件是内部实现细节，不需要作为公开 API 暴露。非 Go 语言连接器场景才需要将 proto 放在 `api/` 目录下，v1 不存在该场景。

---

## Go 项目结构

| Option | Description | Selected |
|--------|-------------|----------|
| 功能领域分层 | internal/storage, internal/auth, internal/connector, internal/event, cmd/platform | ✓ |
| 标准 cmd/internal/pkg | cmd/platform/, internal/, pkg/（可导出库） | |

**User's choice:** 功能领域分层
**Notes:** proto 文件存放位置经过深入讨论：由于 v1 只支持 Go 连接器，proto 作为内部细节放在 `internal/connector/` 下，不需要顶级 `api/` 目录。

---

## API Stub 范围

| Option | Description | Selected |
|--------|-------------|----------|
| 最小化框架骨架 | chi Router + 中间件链 + health/readiness + 完整 Auth 路由 + gRPC 空壳 | ✓ |
| 全量 REST 框架 | 同上，但预建所有未来资源的路由组和 handler stub | |
| 仅 HTTP，跳过 gRPC | Phase 1 只搭 chi REST，gRPC/connect-go 推迟 | |

**User's choice:** 最小化框架骨架
**Notes:** 错误响应格式采用 RFC 7807 Problem+JSON，作为全局 API 规范从 Phase 1 定下。

### 错误响应格式

| Option | Description | Selected |
|--------|-------------|----------|
| RFC 7807 Problem+JSON | {"type", "title", "status", "detail"} 结构 | ✓ |
| 简单 JSON 包装 | {"error": "...", "message": "..."} | |

---

## 事件日志 Schema 设计

| Option | Description | Selected |
|--------|-------------|----------|
| 统一表 + 类型化 payload | event_log 单表，payload JSONB，事件语义封装在 Go struct | ✓ |
| 每种事件独立表 | 每种 event_type 有自己的 PostgreSQL 表 | |

**User's choice:** 统一表 + 类型化 payload

### 不可修改性强制方式

| Option | Description | Selected |
|--------|-------------|----------|
| PostgreSQL Row Security | RLS 在数据库层面拒绝应用用户的 UPDATE/DELETE | ✓ |
| 仅应用层保证 | ORM/SQL 不写 UPDATE/DELETE，无数据库层强制 | |

**User's choice:** PostgreSQL Row Security

### Phase 1 事件类型范围

| Option | Description | Selected |
|--------|-------------|----------|
| 仅 Auth + 平台生命周期 | user.*, auth.*, platform.* 7种类型 | ✓ |
| 全量预定义所有类型 | 包含 run.*, quality.*, governance.* 等所有预期类型 | |

**User's choice:** 仅 Auth + 平台生命周期（run.* 由 Phase 2 添加，governance.* 由 Phase 5 添加）

---

## Claude's Discretion

- SQLite 嵌入式后端是否在 Phase 1 实现（最低要求 PostgreSQL）
- JWT 刷新令牌流程（access token 必须有，refresh token 由 Claude 评估）
- ent schema 字段设计和索引策略
- chi 中间件链组合顺序

## Deferred Ideas

- 非 Go 连接器支持 → v2 或后续里程碑
- gRPC 具体路由 → Phase 2+ 填充
