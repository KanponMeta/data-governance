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

# Phase 01 Plan 05: HTTP API Surface — Summary

## One-liner

JWT auth with bootstrap admin, bcrypt passwords, sha256 invite tokens, chi router, RFC 7807 problem+json errors, prometheus metrics, health/ready endpoints, and a Phase 2 gRPC stub placeholder.

## Route Map

| Method | Path | Auth | Status Codes | Response Body |
|--------|------|------|--------------|----------------|
| POST | /v1/auth/register | None | 201, 400, 409 | `{user_id, role}` |
| POST | /v1/auth/login | None | 200, 400, 401 | `{access_token, token_type, expires_at, user_id, role}` |
| POST | /v1/auth/invites | Bearer (admin) | 201, 400, 401, 403 | `{invite_id, token, expires_at}` |
| POST | /v1/auth/accept-invite | None | 201, 400, 404, 409, 410 | `{user_id}` |
| GET | /health | None | 200 | `{status:"ok", version}` |
| GET | /ready | None | 200, 503 | `{status:"ok"}` or problem+json |
| GET | /metrics | None | 200 | Prometheus text format |
| GET/POST | /grpc/data_governance.v1.PlatformService/Ping | None (Phase 1) | 200 | Phase 1 placeholder JSON |

## Middleware Order

1. `middleware.RequestID` — unique ID per request, propagated to log and context
2. `middleware.RealIP` — real client IP from X-Forwarded-For / X-Real-IP
3. `requestLogger` — JSON structured log (method, path, status, duration_ms, request_id, remote_ip); Authorization header redacted
4. `middleware.Recoverer` — catches panics, returns 500 problem+json
5. `middleware.Timeout(30s)` — request timeout
6. Body limit middleware (`http.MaxBytesReader(w, r.Body, 1<<20)`) — 1MB max

## Error Code Map

| Auth Error | HTTP Status | problem.title |
|------------|-------------|---------------|
| Missing Authorization header | 401 | Unauthorized |
| Malformed/expired/tampered token | 401 | Unauthorized |
| Wrong role for admin route | 403 | Forbidden |
| Duplicate email on register | 409 | Conflict |
| Invite expired | 410 | Gone |
| Invite already used | 409 | Conflict |
| Invite token not found | 404 | Not Found |
| Invalid JSON / unknown field | 400 | Bad Request |

## Bootstrap Admin Rule

When `User.Query().Count(ctx)` returns zero inside the registration transaction, the new user is assigned `role=admin`. All subsequent registrations get `role=member`.

Recovery: If the first admin registration fails mid-transaction (e.g. email sent but response lost), the user can retry with the same email. If the DB is truly empty, the next successful registration becomes admin.

## Deviations from Plan

None — plan executed exactly as written.

## Technical Debt

### /grpc placeholder (T-05-07)

The `/grpc` sub-tree is currently served by a net/http stub returning canned JSON. Phase 2 must:

1. Define `proto/platform/v1/platform.proto` with `PlatformService.Ping`
2. Run `buf generate` to produce `internal/api/gen/platformv1connect/...`
3. Replace `internal/api/grpc_stub.go` with `connectrpc.com/connect.NewPlatformServiceHandler`
4. Add `auth.Middleware` to the `/grpc` sub-router (Phase 1 leaves it unauthenticated per T-05-07)

This is documented in `internal/api/grpc_stub.go` package comment.

### Plan 04 vs Platform protos

Plan 04 generates the connector ABI proto for third-party connectors. It is separate from the platform's own service surface. The platform proto in Phase 2 will live under `proto/platform/v1/` and is independent of the Plan 04 connector proto under `proto/connector/v1/`.

## Threat Flags

| Flag | File | Description |
|------|------|-------------|
| threat_flag: auth_enumeration | internal/api/auth_handlers.go | Login returns identical 401 for missing email vs wrong password (T-05-01) |
| threat_flag: unauthenticated_grpc | internal/api/router.go | /grpc sub-router has no auth.Middleware in Phase 1 (T-05-07); Phase 2 must add it |
