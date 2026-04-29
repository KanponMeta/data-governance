<!-- GSD:project-start source:PROJECT.md -->
## Project

**Data Governance Platform**

An open-source data governance platform written in Go, inspired by Dagster's asset-centric architecture. It combines data orchestration (software-defined assets, pipeline scheduling, execution engine) with enterprise-grade governance (field-level lineage, data quality rules, metadata catalog, column-level access control, and approval workflows). Designed for data engineers who build pipelines, analysts who explore data, and governance teams who enforce policies — all from a single platform.

**Core Value:** A data practitioner can define, run, and govern data assets in code — and every downstream consumer can trust what they're working with, trace where it came from to the field level, and know who is allowed to see it.

### Constraints

- **Tech Stack**: Go backend (no Python runtime dependency in core) — Go is the team's primary language
- **Open Source**: Apache 2.0 or similar permissive license — target community adoption
- **Self-contained**: Must run on a single machine for development (Docker Compose)
- **Connector extensibility**: Connector interface must be a stable public API from day one — third-party connectors are a key adoption driver
<!-- GSD:project-end -->

<!-- GSD:stack-start source:research/STACK.md -->
## Technology Stack

## Recommended Stack
### Execution Engine
| Technology | Version | Purpose | Why |
|---|---|---|---|
| Custom DAG scheduler (in-process) | — | Asset dependency resolution + topological execution | Temporal is overkill for phase 1; embedding a scheduler keeps deployment simple (single binary). Temporal adds an external service dependency. |
| `riverqueue/river` | v0.35.x | Job queue for materializations, async tasks, cron scheduling | Postgres-backed, transactional enqueue (job never lost if transaction commits), retries, unique jobs, periodic/cron, web UI. No external broker. Pairs naturally with PostgreSQL metadata store. |
| `heimdalr/dag` | v1.5.x | In-memory DAG representation, topological sort, cycle detection | Thread-safe, generic, BFS/DFS walkers, topological sort, transitive reduction, JSON serialization. v1.5.1 published Apr 2026. Simple BSD-3 license. |
### Metadata Storage
| Technology | Version | Purpose | Why |
|---|---|---|---|
| PostgreSQL | 16+ | Primary metadata store: assets, runs, lineage, quality, audit log | Industry standard for structured metadata. River requires Postgres anyway. JSONB for schema snapshots. Robust FK constraints for lineage graph integrity. |
| SQLite | 3.x (via `mattn/go-sqlite3` or `modernc.org/sqlite`) | Embedded dev mode (single binary, zero-config) | Enables `./platform start` with no external dependencies. SQLite is used by go-workflows and River (River SQLite driver is preview). Use for dev/CI only. |
### Database Migration
| Technology | Version | Purpose | Why |
|---|---|---|---|
| Atlas (`ariga.io/atlas`) | latest | Schema migrations for PostgreSQL and SQLite | Declarative schema diffing — no manual rollback scripts needed. Handles dirty-state recovery automatically (golang-migrate does not). Integrates with ent schemas. Lints migrations in CI. |
### ORM / Query Layer
| Technology | Version | Purpose | Why |
|---|---|---|---|
| `entgo.io/ent` | v0.14.x (latest: Mar 2026) | Schema definition, complex graph queries, code generation | The metadata model is a graph (assets → lineage → columns → rules). Ent's edge-based schema maps directly to this. 100% type-safe generated code, no runtime reflection. Atlas integration for migrations. Supports PostgreSQL and SQLite. |
| `sqlc` | v1.31.x (latest: Apr 2026) | High-performance read queries, reporting, audit log reads | For hot read paths (catalog search, lineage traversal queries, audit export) where hand-written SQL + type-safe generated code beats ORM overhead. |
| `database/sql` + `pgx/v5` | pgx v5.x | Raw PostgreSQL driver | pgx is the modern, performant Postgres driver. Used by River. Use directly for bulk operations and COPY FROM. |
### HTTP API Framework
| Technology | Version | Purpose | Why |
|---|---|---|---|
| `github.com/go-chi/chi/v5` | v5.2.5 (Feb 2026) | REST API router for the platform backend | Pure net/http compatible — every middleware in the Go ecosystem works. Composable via sub-routers. Lightweight (no magic). Idiomatic Go. Easiest to test. No framework lock-in means the connector SDK stays portable. |
- **Echo v4/v5**: Strong framework, good middleware, slightly more opinionated. Casbin middleware is available. Acceptable alternative if team prefers richer framework conventions. Echo v5 is in active development.
- **Gin**: Most popular (48% Go devs per JetBrains 2025 survey) but adds abstractions over net/http that create friction when composing with standard library middleware. `gin.Context` is not `http.Request` — awkward for library consumers.
- **Fiber**: Based on fasthttp, not net/http. Breaks ecosystem compatibility. Do not use in a platform that will expose a public SDK — third-party connectors may bring net/http middleware.
### Connector Interface (Plugin System)
| Technology | Version | Purpose | Why |
|---|---|---|---|
| `connectrpc/connect-go` | v1.19.x (Apr 2026) | Connector RPC protocol | Works over HTTP/1.1 and HTTP/2. Testable with curl. Compatible with existing gRPC clients. No proxy needed for browser calls. Simpler than grpc-go (130K LOC vs focused library). Production-ready. |
| `hashicorp/go-plugin` | v1.7.x (Aug 2025) | Out-of-process connector subprocess management | Battle-tested in Terraform, Vault, Nomad. Connectors run as isolated subprocesses — a crash in a connector cannot crash the platform host. Language-agnostic (connector can be any language). gRPC transport over stdio. |
| Protobuf (`google.golang.org/protobuf`) | v1.x | Connector interface IDL | Single source of truth for connector spec. Language-agnostic. Version-stable connector ABI. |
### Lineage Capture
| Technology | Version | Purpose | Why |
|---|---|---|---|
| OpenLineage event spec (JSON) | 1.x | Lineage event format | Open standard, adopted by Airflow, Spark, dbt, Flink, Debezium. Emit-compatible events makes the platform's lineage queryable by external tools (Marquez). ThijsKoot/openlineage-go provides a Go client. |
| In-process synchronous capture | — | Field-level lineage recording at materialization time | Async (Kafka/NATS) adds operational burden. For phase 1, write lineage synchronously in the same transaction as asset metadata. Decouple to async event bus in a later phase when throughput demands it. |
| `ThijsKoot/openlineage-go` | latest | Go client for emitting OpenLineage events | Only maintained Go client for OpenLineage. Partial community library but sufficient for event emission. |
### Authorization (RBAC + Column-Level Access Control)
| Technology | Version | Purpose | Why |
|---|---|---|---|
| `casbin/casbin` | v2.135.x (Dec 2025, v3 in progress) | RBAC + ABAC policy enforcement | Supports ACL, RBAC, ABAC. Policy model is externalized to a `.conf` file — no code changes to modify policy semantics. Postgres adapter available. Widely used in Go data/platform tools. |
| `golang-jwt/jwt/v5` | v5.3.x (Jan 2026) | JWT token creation and validation | The maintained successor to jwt-go. v5 adds proper claims validation, ECDSA/RSA-PSS support, improved error handling. |
| `golang.org/x/oauth2` | latest | OAuth2 / OIDC integration for SSO | Official Go OAuth2 package. For OIDC integration (e.g., connect to organization IdP). |
### Frontend
| Technology | Version | Purpose | Why |
|---|---|---|---|
| React + TypeScript | React 19.x, TS 5.x | UI framework | Dagster's UI is React — ecosystem familiarity for contributors. TypeScript is non-negotiable for a platform UI with complex data models. |
| Vite | 6.x | Build tool | 10x faster HMR than CRA. Standard for new React projects in 2025. |
| TanStack Router | v1.x | Client-side routing | Full type safety end-to-end (route params are typed). Works with Vite. Gaining strong adoption. Better than React Router for type-safety-first projects. |
| TanStack Query (React Query) | v5.x | Server state management | Cache, background refetch, stale-while-revalidate for run status polling. Standard for data platform UIs with frequent server state. |
| shadcn/ui + Tailwind CSS | shadcn v2.x, Tailwind v4.x | Component library | Components are copied into the project (not an external dependency). Full customization with zero bundle overhead from unused components. TypeScript-first. Best DX for custom-designed data platform UIs in 2025. |
| ReactFlow (xyflow) | v12.x | DAG / lineage graph visualization | Purpose-built for node-based graph UIs. Native dagre layout for hierarchical DAGs. Interactive: zoom, pan, custom nodes per asset type. Used by Dagster, n8n, and other data platforms. Better DX than Cytoscape.js for React. |
| Recharts or Visx | latest | Time-series charts (quality history, run timelines) | Recharts for simple charts; Visx (Airbnb) for complex custom viz. Both are React-native and TypeScript-friendly. |
| Zustand | v5.x | Lightweight global UI state | For UI-only state (selected lineage node, sidebar state). TanStack Query handles server state; Zustand handles ephemeral UI state. Avoids Redux complexity. |
### Observability + Logging
| Technology | Version | Purpose | Why |
|---|---|---|---|
| `log/slog` (stdlib) | Go 1.21+ | Structured logging | Built into Go standard library since 1.21. JSON and text handlers. No external dependency. |
| `prometheus/client_golang` | v1.x | Metrics exposure | Prometheus is the de facto standard for Go service metrics. Enables Grafana dashboards for self-hosted deployments. |
| OpenTelemetry Go SDK | v1.x | Distributed tracing | `go.opentelemetry.io/otel`. For tracing asset materialization execution spans. Optional in phase 1 but stub the interfaces from day one. |
### Infrastructure / Deployment
| Technology | Version | Purpose | Why |
|---|---|---|---|
| Docker Compose | v2.x | Single-machine dev deployment | Satisfies the "runs on a single machine" constraint. Platform + Postgres + optional monitoring stack. |
| Kubernetes (Helm chart) | — | Production deployment | Standard for self-hosted data platform deployments. Phase 2+. |
| `goreleaser` | v2.x | Binary release builds | Cross-platform Go binary releases. Standard for open-source Go tools. |
## Alternatives Considered
| Category | Recommended | Alternative Considered | Why Not Chosen |
|---|---|---|---|
| Workflow engine | River + custom DAG scheduler | Temporal | Requires external Temporal server; too heavy for self-hosted single-binary deployment in phase 1 |
| Workflow engine | River + custom DAG scheduler | go-workflows (embedded) | Adds DTFx-style coroutine complexity; River is simpler and better understood |
| ORM | ent | GORM | Reflection-based, implicit auto-migration, degrades on complex graph schemas |
| ORM | ent | sqlx | No code generation, more manual boilerplate for complex queries |
| HTTP framework | chi | Echo | Both are good; chi is more minimal and net/http native — better for SDK consumers |
| HTTP framework | chi | Fiber | fasthttp-based, breaks net/http ecosystem compatibility |
| Plugin system | hashicorp/go-plugin | Go native `plugin` package | Cannot be unloaded, requires same Go binary version, no subprocess isolation |
| Component library | shadcn/ui | Ant Design | Large bundle, rigid design language, hard to customize |
| Graph visualization | ReactFlow | Cytoscape.js | Canvas-based, less suited to rich interactive React nodes |
| Auth | Casbin | Custom RBAC | Casbin handles edge cases correctly; custom RBAC is a footgun |
| Migration | Atlas | golang-migrate | golang-migrate's dirty-state failure mode is unacceptable for a CI/CD-first platform |
| Lineage event format | OpenLineage JSON | Custom schema | OpenLineage is the open standard; using it enables ecosystem interoperability |
## Installation Snapshot
# Core backend
# Dev tools
# Frontend
## Confidence Assessment
| Area | Confidence | Notes |
|---|---|---|
| River for job queue | HIGH | Current version v0.35.x verified on pkg.go.dev Apr 2026; actively maintained; production use confirmed |
| chi for HTTP | HIGH | v5.2.5 verified Feb 2026; pattern well-established |
| ent for ORM | HIGH | v0.14.x verified Mar 2026; widely adopted for graph schemas |
| sqlc for read queries | HIGH | v1.31.1 verified Apr 2026; stable |
| hashicorp/go-plugin for connectors | HIGH | v1.7.0 verified Aug 2025; used in Terraform/Vault production |
| connect-go for RPC | HIGH | v1.19.2 verified Apr 2026; actively maintained by Buf.build |
| heimdalr/dag for DAG | HIGH | v1.5.1 verified Apr 2026; thread-safe, topological sort confirmed |
| Atlas for migrations | HIGH | Recommended by ent project itself; handles dirty state gracefully |
| Casbin for RBAC | HIGH | v2.135.x verified Dec 2025; v3 in progress but v2 stable |
| golang-jwt/v5 for JWT | HIGH | v5.3.1 verified Jan 2026 |
| shadcn/ui + ReactFlow | MEDIUM | ReactFlow v12.x verified; shadcn is actively maintained — specific version pin should be done at project init |
| Custom DAG scheduler (not Temporal) | MEDIUM | Correct decision for phase 1; may need revisiting if multi-worker distributed execution becomes a hard requirement |
| OpenLineage Go client | LOW | ThijsKoot/openlineage-go is community-maintained, not official OpenLineage project. May need to vendor and maintain locally. Emit format (JSON over HTTP) is simple enough to implement directly if the client proves insufficient. |
## Sources
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
- Go ORM comparison: https://www.bytebase.com/blog/golang-orm-query-builder/ and https://www.glukhov.org/post/2025/03/which-orm-to-use-in-go/
- Go HTTP framework comparison: https://blog.logrocket.com/top-go-frameworks-2025/ and https://blog.jetbrains.com/go/2026/04/28/popular-golang-web-frameworks/
- shadcn vs Ant Design: https://www.subframe.com/tips/ant-design-vs-shadcn
- Temporal Go SDK: https://docs.temporal.io/develop/go and https://pkg.go.dev/go.temporal.io/sdk
<!-- GSD:stack-end -->

<!-- GSD:conventions-start source:CONVENTIONS.md -->
## Conventions

Conventions not yet established. Will populate as patterns emerge during development.
<!-- GSD:conventions-end -->

<!-- GSD:architecture-start source:ARCHITECTURE.md -->
## Architecture

Architecture not yet mapped. Follow existing patterns found in the codebase.
<!-- GSD:architecture-end -->

<!-- GSD:skills-start source:skills/ -->
## Project Skills

No project skills found. Add skills to any of: `.claude/skills/`, `.agents/skills/`, `.cursor/skills/`, or `.github/skills/` with a `SKILL.md` index file.
<!-- GSD:skills-end -->

<!-- GSD:workflow-start source:GSD defaults -->
## GSD Workflow Enforcement

Before using Edit, Write, or other file-changing tools, start work through a GSD command so planning artifacts and execution context stay in sync.

Use these entry points:
- `/gsd-quick` for small fixes, doc updates, and ad-hoc tasks
- `/gsd-debug` for investigation and bug fixing
- `/gsd-execute-phase` for planned phase work

Do not make direct repo edits outside a GSD workflow unless the user explicitly asks to bypass it.
<!-- GSD:workflow-end -->



<!-- GSD:profile-start -->
## Developer Profile

> Profile not yet configured. Run `/gsd-profile-user` to generate your developer profile.
> This section is managed by `generate-claude-profile` -- do not edit manually.
<!-- GSD:profile-end -->
