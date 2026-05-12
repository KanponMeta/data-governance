# Phase 6: Web UI 与 API - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-05-10
**Phase:** 06-web-ui-api
**Areas discussed:** API surface scope, Frontend repo & build, Catalog search backend, Plan partitioning hint, Lineage DAG strategy, Real-time updates, Logs surfacing, Auth flow in UI

---

## API surface scope

| Option | Description | Selected |
|--------|-------------|----------|
| ConnectRPC (single IDL) | One protobuf IDL, serve both REST and gRPC from same handlers | ✓ |
| Separate REST + gRPC | OpenAPI for REST, protobuf for gRPC, no shared IDL | |

**User's choice:** ConnectRPC (single IDL)
**Notes:** All API handlers (existing chi + new ConnectRPC) live in internal/api/. Migration: swaggo annotations during migration; protoc-gen-connect-openapi at end state.

---

## Frontend repo & build

| Option | Description | Selected |
|--------|-------------|----------|
| web/ at repo root | React SPA at web/ in repo root, go:embed at build time | ✓ |
| Separate frontend repo | Frontend in its own repo, imported as module | |

**User's choice:** web/ at repo root, go:embed at build time
**Notes:** Dev workflow: Vite dev server with proxy to Go. Frontend package manager: pnpm.

---

## Catalog search backend

| Option | Description | Selected |
|--------|-------------|----------|
| Postgres FTS (tsvector + GIN) | Existing tables get GENERATED tsvector column + GIN index | ✓ |
| Elasticsearch/OpenSearch | Separate search cluster, index platform data | |
| SQLite FTS5 | For dev/CI only; not production scale | |

**User's choice:** Postgres FTS (tsvector + GIN)
**Notes:** Single endpoint /v1/catalog/search. CJK tokenization: English-first, CJK best-effort via simple config. Full pg_jieba/zhparser deferred to v1.x.

---

## Plan partitioning hint

| Option | Description | Selected |
|--------|-------------|----------|
| Horizontal-then-vertical hybrid | ConnectRPC migration plan first, then feature plans | ✓ |
| All-at-once | One big plan for everything | |

**User's choice:** Horizontal-then-vertical hybrid. ConnectRPC migration plan is first. First feature plan: Asset dashboard (UI-01).

---

## Lineage DAG strategy

| Option | Description | Selected |
|--------|-------------|----------|
| Neighborhood on demand | Fetch focus node + depth from API, no full graph load | ✓ |
| Full graph load + client-side filter | Load entire graph, filter in browser | |
| Server-side precomputed layout | Precompute layouts server-side, stream to client | |

**User's choice:** Neighborhood on demand
**Notes:** Layout algorithm: client-side dagre via ReactFlow. Column drilldown: side panel on node click. Asset vs column rendering: one canvas, two zoom levels.

---

## Real-time updates

| Option | Description | Selected |
|--------|-------------|----------|
| TanStack Query polling (no push) | Polling is default; SSE/server-streaming is v1.x | ✓ |
| WebSocket push | Real-time push from server to client | |
| SSE (Server-Sent Events) | Server pushes events to client | |

**User's choice:** TanStack Query polling, no push in v1
**Notes:** Hot screens: 3-5s active runs, 15-30s inbox/alerts, 60s catalog.

---

## Logs surfacing

| Option | Description | Selected |
|--------|-------------|----------|
| event_log filtered by run_id | event_log is the source; cursor pagination on (run_id, seq) | ✓ |
| Separate run_logs table | Dedicated table for UI-visible log entries | |
| stdout capture per run | Capture and store stdout from executor | |

**User's choice:** event_log filtered by run_id, cursor pagination
**Notes:** Live tail: static page with TanStack Query polling at 3-5s cadence during active run. No WebSocket/stream in v1.

---

## Auth flow in UI

| Option | Description | Selected |
|--------|-------------|----------|
| httpOnly Secure cookie + CSRF | Session cookie + X-CSRF-Token header | ✓ |
| JWT in memory (no cookie) | JWT stored in memory, sent as Authorization header | |
| OAuth 2.0 + PKCE | Social login / external IdP | |

**User's choice:** httpOnly Secure cookie + CSRF token
**Notes:** GET /v1/me returns user + roles + permission flags. Hard logout to /login on 401. No silent refresh in v1.

---

## Deferred Ideas

- CJK tokenization via pg_jieba/zhparser — v1.x
- Refresh-token endpoint (AUTH-05?) — v1.x
- Free-text per-run logger / stdout capture — v1.x
- ConnectRPC server streaming for live updates — v1.x
- Server-side ELK / precomputed lineage layout — v1.x