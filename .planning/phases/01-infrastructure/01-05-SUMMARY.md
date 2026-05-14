---
phase: 01-infrastructure
plan: 05
status: complete
tags:
  - auth
  - chi
  - http
  - rfc7807
  - grpc
  - jwt
  - middleware

dependency_graph:
  requires:
    - 01-02 (Storage interface, ent schemas, event Writer)
    - 01-03 (TokenIssuer, HashPassword, Config)
  provides:
    - internal/auth/service.go (Register/Login/Invite/AcceptInvite)
    - internal/auth/middleware.go (JWT validation, RequireRole)
    - internal/api/problem.go (RFC 7807 helpers)
    - internal/api/router.go (chi router with full route mount)
    - internal/api/auth_handlers.go (HTTP handlers for auth endpoints)
    - internal/api/health_handlers.go (Health, Ready)
    - internal/api/grpc_stub.go (Phase 1 placeholder)
    - internal/api/server.go (HTTP server lifecycle)
    - cmd/platform/main.go (fully wired entrypoint)
  affects:
    - Phase 2 (CLI requires these HTTP endpoints; auth middleware is integration point for all protected routes)
    - Phase 3 (UI requires these endpoints for login/session)

tech_stack:
  added:
    - github.com/go-chi/chi/v5 v5.2.5 (HTTP router)
    - github.com/prometheus/client_golang v1.23.2 (metrics endpoint)
  patterns:
    - TDD (RED: integration tests first, GREEN: minimal implementation)
    - RFC 7807 problem+json for all HTTP error responses
    - Bootstrap admin rule (first user in empty DB becomes admin)
    - Single-use invite token with sha256 hash stored, raw token returned exactly once
    - JWT middleware injects Principal into request context (avoids import cycle with internal/api)
    - RequireRole wrapper for admin-only routes

key_files:
  created:
    - internal/auth/service.go (Register/Login/Invite/AcceptInvite with full domain logic)
    - internal/auth/middleware.go (Middleware, RequireRole, PrincipalFromContext)
    - internal/auth/service_test.go (integration tests: bootstrap, login, invite, accept)
    - internal/auth/middleware_test.go (unit tests: missing header, invalid format, tampered token, valid token, RequireRole)
    - internal/api/problem.go (8 RFC 7807 helper functions)
    - internal/api/problem_test.go (unit tests for all helpers)
    - internal/api/router.go (chi router with middleware stack, public/protected routes)
    - internal/api/auth_handlers.go (HTTP handlers: register/login/invite/accept-invite)
    - internal/api/health_handlers.go (Health, Ready handlers)
    - internal/api/grpc_stub.go (Phase 1 placeholder with Phase 2 migration comment)
    - internal/api/server.go (http.Server with graceful shutdown)
    - cmd/platform/main.go (config.Load, storage.NewPostgres, auth.NewService, api.Run, healthcheck subcommand)
  modified:
    - cmd/platform/main.go (replaced stub runStart with full wiring; replaced healthcheck exit-0 with real HTTP GET)

decisions:
  - Auth middleware writes RFC 7807 problem+json inline (instead of calling internal/api/problem.go) to avoid import cycle (auth imported by api, but api would need to import auth for middleware)
  - Invite tokens stored as sha256 hex hash (not bcrypt) because tokens are high-entropy random bytes, not passwords
  - Bootstrap admin rule enforced by counting users in WithTx before creation (atomic with user creation)
  - Login error handling returns identical response for missing-email and wrong-password (T-05-01: prevents user enumeration)
  - grpc_stub.go deliberately uses net/http (not connect-go) because Phase 1 has no platform proto definitions; Phase 2 will generate them

metrics:
  duration: "~30 minutes"
  tasks_completed: 2
  files_created: 12
  commits: 2
  completed_date: "2026-05-06"

requirements:
  AUTH-01: "User can POST /v1/auth/register with {email,password} and receive 201 with new user_id"
  AUTH-02: "User can POST /v1/auth/login with valid credentials and receive a JWT; invalid credentials return 401"
  AUTH-03: "Admin can POST /v1/auth/invites and receive an invite token; invitee can POST /v1/auth/accept-invite"
  AUTH-04: "Requests to protected routes with an expired JWT receive 401 problem+json and an auth.token_expired event"
  CORE-05: "All HTTP error responses use RFC 7807 problem+json"
  D-06: "HTTP error responses use application/problem+json content type"
---

# Phase 01 Plan 05: HTTP API Surface — 总结

## 一句话总结

带引导管理员的 JWT 认证，bcrypt 密码，sha256 邀请令牌，chi 路由器，RFC 7807 problem+json 错误，prometheus 指标，health/ready 端点，以及 Phase 2 gRPC 桩占位符。

## 路由映射

| 方法 | 路径 | 认证 | 状态码 | 响应体 |
|--------|------|------|--------------|----------------|
| POST | /v1/auth/register | 无 | 201, 400, 409 | `{user_id, role}` |
| POST | /v1/auth/login | 无 | 200, 400, 401 | `{access_token, token_type, expires_at, user_id, role}` |
| POST | /v1/auth/invites | Bearer (admin) | 201, 400, 401, 403 | `{invite_id, token, expires_at}` |
| POST | /v1/auth/accept-invite | 无 | 201, 400, 404, 409, 410 | `{user_id}` |
| GET | /health | 无 | 200 | `{status:"ok", version}` |
| GET | /ready | 无 | 200, 503 | `{status:"ok"}` or problem+json |
| GET | /metrics | 无 | 200 | Prometheus text format |
| GET/POST | /grpc/data_governance.v1.PlatformService/Ping | 无 (Phase 1) | 200 | Phase 1 placeholder JSON |

## 中间件顺序

1. `middleware.RequestID` — 每个请求的唯一 ID，传播到日志和上下文
2. `middleware.RealIP` — 来自 X-Forwarded-For / X-Real-IP 的真实客户端 IP
3. `requestLogger` — JSON 结构化日志 (method, path, status, duration_ms, request_id, remote_ip)；Authorization 头被编辑
4. `middleware.Recoverer` — 捕获 panic，返回 500 problem+json
5. `middleware.Timeout(30s)` — 请求超时
6. Body limit 中间件 (`http.MaxBytesReader(w, r.Body, 1<<20)`) — 最大 1MB

## 错误码映射

| 认证错误 | HTTP 状态 | problem.title |
|------------|-------------|---------------|
| Missing Authorization header | 401 | Unauthorized |
| Malformed/expired/tampered token | 401 | Unauthorized |
| Wrong role for admin route | 403 | Forbidden |
| Duplicate email on register | 409 | Conflict |
| Invite expired | 410 | Gone |
| Invite already used | 409 | Conflict |
| Invite token not found | 404 | Not Found |
| Invalid JSON / unknown field | 400 | Bad Request |

## 引导管理员规则

当 `User.Query().Count(ctx)` 在注册事务中返回零时，新用户被分配 `role=admin`。所有后续注册获得 `role=member`。

恢复: 如果第一次管理员注册在事务中途失败 (例如邮件已发送但响应丢失)，用户可以使用相同的电子邮件重试。如果数据库确实为空，下一次成功注册将成为管理员。

## 与计划的偏差

无 — 计划完全按书面执行。

## 技术债务

### /grpc 占位符 (T-05-07)

`/grpc` 子树目前由返回罐头 JSON 的 net/http 桩提供服务。Phase 2 必须:

1. 定义 `proto/platform/v1/platform.proto`，包含 `PlatformService.Ping`
2. 运行 `buf generate` 生成 `internal/api/gen/platformv1connect/...`
3. 用 `connectrpc.com/connect.NewPlatformServiceHandler` 替换 `internal/api/grpc_stub.go`
4. 将 `auth.Middleware` 添加到 `/grpc` 子路由器 (Phase 1 按 T-05-07 使其未认证)

这记录在 `internal/api/grpc_stub.go` 包注释中。

### Plan 04 vs 平台 proto

Plan 04 为第三方连接器生成连接器 ABI proto。它独立于平台的自身服务 surface。Phase 2 中的平台 proto 将位于 `proto/platform/v1/`，独立于 Plan 04 连接器 proto 下的 `proto/connector/v1/`。

## 威胁标志

| 标志 | 文件 | 描述 |
|------|------|-------------|
| threat_flag: auth_enumeration | internal/api/auth_handlers.go | Login 对缺失邮箱和错误密码返回相同的 401 (T-05-01) |
| threat_flag: unauthenticated_grpc | internal/api/router.go | /grpc 子路由器在 Phase 1 中没有 auth.Middleware (T-05-07)；Phase 2 必须添加 |