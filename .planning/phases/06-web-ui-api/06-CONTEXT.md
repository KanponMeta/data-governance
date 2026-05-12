# Phase 6: Web UI 与 API - Context

**Gathered:** 2026-05-10
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 6 delivers a complete REST/gRPC API and React SPA for the data governance platform. Users can browse assets, explore lineage DAGs, manage governance workflows, and administer the platform — all via a typed API (ConnectRPC) backed by a single Go binary with embedded React UI.

Scope: Full API surface (CORE-04, CORE-05), React SPA (UI-01..07), Catalog search (META-04), Lineage DAG visualization (LINE-04, LINE-05), Quality dashboard (QUAL-06).

Out of scope: SSE/push for live updates (deferred to v1.x), row-level security (v2), SSO/OIDC (v2), per-run stdout capture (v1.x), server-side lineage layout precomputation (v1.x), CJK tokenization (v1.x via pg_jieba/zhparser), refresh-token endpoint (v1.x).
</domain>

<decisions>
## Implementation Decisions

### API Surface & Protocol

- **D-01:** ConnectRPC is the API protocol for both REST and gRPC — single IDL (protobuf), both protocols served from the same handlers. chi REST handlers are migrated to ConnectRPC; swaggo annotations added during migration for OpenAPI docs. `protoc-gen-connect-openapi` generates OpenAPI spec at end state.
- **D-02:** All API handlers (existing chi + new ConnectRPC) live in `internal/api/`. Phase 6 does one cutover migration — legacy endpoints annotated with swaggo, new endpoints in ConnectRPC. Both documented in OpenAPI.
- **D-03:** OpenAPI docs generated via swaggo (`github.com/swaggo/swag`) annotations during migration. `protoc-gen-connect-openapi` at end-state for ConnectRPC-native endpoints.

### Frontend Architecture

- **D-04:** React SPA lives at `web/` in the repo root. Bundled into the Go binary via `go:embed` at build time. Dev workflow: Vite dev server proxies API calls to the Go backend (separate process for development).
- **D-05:** Frontend package manager: **pnpm**. All frontend package entries in root `package.json` use pnpm workspace conventions.
- **D-06:** Auth in UI: httpOnly Secure cookie for session token + CSRF token header on state-changing requests. `/login` is a dedicated route. Token expiry → hard redirect to `/login`. `GET /v1/me` returns user + roles + permission flags for UI to consume.

### Catalog Search Backend

- **D-07:** Search mechanism: **Postgres FTS** via `tsvector` + GIN index. Existing tables get a generated `tsvector` column. Index population: Postgres `GENERATED` column or trigger-maintained.
- **D-08:** Chinese/CJK tokenization: English-first, CJK best-effort via simple config (pgtrgm or simple unigram). Full CJK support via pg_jieba/zhparser is **v1.x** — deferred, not in scope.
- **D-09:** Search REST contract: single endpoint `GET /v1/catalog/search?q=<query>&type=<asset|column|table>&page=<n>`. Returns ranked results with highlighting. No faceted search in v1.

### Plan Partitioning

- **D-10:** Phase 6 uses **horizontal-then-vertical** partitioning:
  - Horizontal (wave 1): ConnectRPC migration plan — establish the API shape first
  - Vertical (waves 2+): Feature plans (Asset dashboard UI-01, then others)
- **D-11:** First feature plan: **Asset dashboard (UI-01)** — the highest-traffic screen every user lands on. ConnectRPC migration runs as a pre-plan first.

### Lineage DAG Visualization

- **D-12:** Large graph handling: **neighborhood-on-demand** — focus node + configurable depth fetched from API. No full graph load. Frontend requests `(asset_id, focus, depth)` and renders the subgraph.
- **D-13:** Column drilldown UX: **side panel on node click** — clicking an asset node opens a side panel showing asset metadata + column-level lineage for that node. Separate from the main DAG canvas.
- **D-14:** Asset vs column rendering: **one canvas, two zoom levels**. Asset-level shows asset nodes and edges. Zoom-in on an asset reveals column nodes and column_edges. Toggle or smooth zoom transition.
- **D-15:** Layout algorithm: **client-side dagre via ReactFlow** — `dagre` computes layout, ReactFlow renders. No server-side layout computation in v1.

### Real-Time Updates

- **D-16:** Push model: **TanStack Query polling, no server push in v1**. Polling is the default; SSE/server-streaming is v1.x.
- **D-17:** Hot screens (higher polling frequency):
  - Active run: 3-5s polling
  - Governance inbox: 15-30s polling
  - Quality alerts: 15-30s polling
  - Asset dashboard / Catalog: 60s polling
- **D-18:** TanStack Query (`@tanstack/react-query` v5) manages all server state: caching, background refetch, stale-while-revalidate.

### Logs Surfacing

- **D-19:** Log source: `event_log` table filtered by `run_id`. `event_type` prefix filter for `run.step.*`, `schedule.*`, `sensor.*` events.
- **D-20:** Pagination: cursor pagination on `(run_id, seq)` + time-window filter. Frontend sends `after=<seq>&limit=50`. Backed by sqlc query.
- **D-21:** Live tail: static page with TanStack Query polling at 3-5s cadence during active run. No WebSocket/stream in v1.

### Auth Flow in UI

- **D-22:** Token storage: httpOnly Secure cookie (same-session cookie, not localStorage). CSRF token in `X-CSRF-Token` request header for state-changing operations.
- **D-23:** Login UX: dedicated `/login` route. Form submits to `POST /v1/auth/login`. On success, server sets cookie, redirects to `/`. On 401, UI redirects to `/login`.
- **D-24:** Token expiry behavior: hard logout to `/login` on 401 response. No silent refresh in v1.
- **D-25:** Authorization in UI: `GET /v1/me` returns `{id, email, name, roles[], permissions: {canApprove, canEditPolicies, canManageUsers}}`. UI derives nav and action visibility from this.

### Claude's Discretion

The following are left to researcher/planner discretion:
- Exact swaggo annotation density during migration (per-handler vs per-route-group)
- React component naming conventions and folder structure within `web/src/`
- TanStack Query query-key organization scheme
- ReactFlow node component implementation details (custom nodes vs base nodes)
- CSS approach within the React app (Tailwind v4 per CLAUDE.md tech stack)
- Specific API endpoint paths for each feature area (researcher maps from requirements)
- Test strategy for the embedded SPA (unit vs integration vs e2e)

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Requirements & Roadmap
- `.planning/REQUIREMENTS.md` — CORE-04 (REST API), CORE-05 (gRPC/ConnectRPC), AUTH-04 (JWT/session), UI-01..07 (Web UI), LINE-04 (lineage graph UI), LINE-05 (column drilldown), META-04 (catalog search), QUAL-06 (quality dashboard)
- `.planning/ROADMAP.md` §Phase 6 — acceptance criteria, dependencies, UI hint: yes

### Project Context
- `.planning/PROJECT.md` §核心价值 — "下游使用者能信任所用数据" + "清楚地知道谁有权访问" — drives UI-01/UI-02/UI-07 focus
- `.planning/PROJECT.md` §约束 — "Go 后端" + "自包含" + "连接器可扩展性" — D-04 (embedded SPA) satisfies "单二进制"
- `.planning/PROJECT.md` §关键决策 — "v1 开源（自托管）" — embedded SPA is correct delivery mechanism

### Prior Phase Decisions
- `.planning/phases/01-infrastructure/01-CONTEXT.md` — D-09 event_log RLS-immutability; D-14 auth middleware pattern (D-22/D-23 extend)
- `.planning/phases/02-execution-engine/02-CONTEXT.md` — D-06 River job queue; D-17 `runs.state` lifecycle (quality status in D-19 informs QUAL-06 dashboard)
- `.planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md` — D-01..D-04 scheduler architecture (D-17 hot screens polling informed by scheduler tick)
- `.planning/phases/04-schema/04-CONTEXT.md` — D-13 column_edges (D-12/D-14 lineage DAG traversal); D-17 metadata three-layer; D-19 Go+REST+CLI three-surface pattern (D-03 OpenAPI follows)
- `.planning/phases/05-governance/05-CONTEXT.md` — D-08 governance state machine; D-12 REST+CLI symmetry for governance (D-25 /me endpoint follows same pattern); D-21 notification routing

### Discuss Phase Decisions
- `.planning/phases/06-web-ui-api/06-DISCUSS-CHECKPOINT.json` — all 8 areas completed with decisions; source of truth for D-01..D-25

### Tech Stack (CLAUDE.md)
- `CLAUDE.md` §技术栈 §HTTP API 框架 — chi v5.2.5 (migrating to ConnectRPC)
- `CLAUDE.md` §技术栈 §连接器接口 — connect-go v1.19.x (D-01)
- `CLAUDE.md` §技术栈 §前端 — React 19.x + TypeScript 5.x + Vite 6.x + TanStack Router v1.x + TanStack Query v5.x + shadcn/ui + Tailwind CSS v4.x + ReactFlow v12.x + Recharts (D-04/D-05/D-13/D-15/D-16/D-18)
- `CLAUDE.md` §技术栈 §授权 — golang-jwt v5.3.x (D-22/D-23)

### External References
- ConnectRPC docs: https://connectrpc.com/docs/go/getting-started
- swaggo/swag: https://github.com/swaggo/swag
- TanStack Query v5: https://tanstack.com/query/latest/docs/framework/react/overview
- ReactFlow: https://reactflow.dev/docs/guides/layouting/

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets (from Phases 1–5)
- **`internal/api/`** — existing chi handlers. Phase 6 migrates these to ConnectRPC one endpoint at a time. Gateway pattern: connect-go handler wrapping existing service methods.
- **`internal/auth/`** — JWT validation middleware, user service. Phase 6 extends with `/v1/me` endpoint and cookie-based session for UI.
- **`internal/event/`** — `event_log` table schema. Phase 6 uses this for logs surfacing (D-19/D-20).
- **`internal/lineage/`** — lineage graph data (asset edges + column_edges). Phase 6 exposes this via API for DAG visualization.
- **`internal/storage/`** — ent schema for assets, runs, schedules. Phase 6 reads from these tables for UI dashboards.
- **`internal/governance/`** — governance state machine. Phase 6 exposes this via REST for the governance inbox UI.
- **`cmd/platform/`** — existing CLI subcommands. Phase 6 adds UI dev commands (`./platform ui dev`).
- **`riverqueue/river`** — already in go.mod. UI can use River client for job status polling if needed.

### Established Patterns
- **Three-surface library (Phase 4 D-19):** API follows Go package + REST + CLI pattern. Phase 6 UI hits REST.
- **Event-driven architecture:** `event_log` is the source of truth for run status. UI polls via REST, not WebSocket.
- **Soft-retire / temporal tables (Phase 4 D-15):** Used for `asset_versions` and `column_edges`. UI search can leverage the same pattern.
- **Subcommand-per-mode binary:** `./platform` already has `materialize`, `lineage`, `schema`, `impact`, `governance`, `audit`. Phase 6 adds `ui` subcommand for dev server.

### Integration Points
- **`internal/api/`** — new ConnectRPC handlers for UI-01..UI-07 + LINE-04/LINE-05 + META-04 + QUAL-06
- **`internal/auth/middleware.go`** — extends with CSRF cookie validation + `/v1/me` endpoint
- **`web/`** (NEW) — React SPA, embedded at build time. Vite dev server proxies to Go backend.
- **`migrations/`** — may need FTS index migration for catalog search (D-07)

</code_context>

<specifics>
## Specific Ideas

- **Embedded SPA is non-negotiable** — single binary constraint means `go:embed` of the React build output. No separate frontend deployment.
- **Vite proxy configuration** — `vite.config.ts` proxies `/api`, `/v1`, `/auth` to `localhost:8080` (or `PLATFORM_PORT` env). Go serves static assets in production; Vite proxies in dev.
- **No mobile UI in v1** — responsive layout for the SPA is a v1.x consideration. Desktop-first.
- **OpenAPI spec at end-state** — swaggo during migration is pragmatic; `protoc-gen-connect-openapi` at the end generates the canonical spec. Both coexist during transition.
- **Neighborhood API shape:** `GET /v1/lineage/neighborhood?asset_id=<id>&depth=<n>` returns `{nodes: [], edges: []}` — asset nodes + column nodes with filtering.
</specifics>

<deferred>
## Deferred Ideas

- **SSE/server streaming for live updates** — v1.x. TanStack Query polling is sufficient for v1.
- **CJK tokenization via pg_jieba/zhparser** — v1.x. English-first FTS is the v1 approach.
- **Refresh-token endpoint (AUTH-05)** — v1.x. Hard logout + re-login is v1.
- **Free-text per-run logger / stdout capture** — v1.x. event_log is structured only in v1.
- **ConnectRPC server streaming for live run updates** — v1.x. Polling is the v1 mechanism.
- **Server-side lineage layout precomputation (ELK-style)** — v1.x. Client-side dagre is v1.
- **Additional notification channels (Slack, SES, SendGrid)** — Phase 5 deferred to v1.x; webhook is the v1 fan-out mechanism.

</deferred>

---

*Phase: 06-web-ui-api*
*Context gathered: 2026-05-10*