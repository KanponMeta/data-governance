# Project Research Summary

**Project:** Data Governance + Orchestration Platform (Go-native, Dagster-inspired)
**Domain:** Data orchestration + governance platform
**Researched:** 2026-04-29
**Confidence:** HIGH

---

## Executive Summary

This project fills a genuine gap: no open-source tool today combines reliable pipeline orchestration with enterprise-grade governance (field-level lineage, approval workflows, column-level access control, and compliance audit trails) in a single deployable binary. Dagster handles orchestration well but has no governance primitives. DataHub and OpenMetadata handle governance and catalog but cannot execute pipelines. Collibra does it all but is closed-source and expensive. A Go-native platform occupies an uncontested position in this matrix and has a clear, defensible set of differentiators.

The recommended approach is a single Go binary with three run-mode flags (`server`, `worker`, `daemon`), backed by PostgreSQL as the sole durable store, with an in-memory DAG scheduler (`heimdalr/dag`), a Postgres-backed job queue (River), and a subprocess-isolated connector system (hashicorp/go-plugin). The asset-centric model (Dagster-style) is the right mental model — assets are first-class runtime objects registered at startup, not stored in the database. The governance layer is built alongside the execution engine from day one; both share the same storage abstraction and audit log.

The dominant risks are scope and sequencing. Research from Dagster's issue history and failed open-source platform projects confirms that attempting to ship orchestration + catalog + governance simultaneously produces a platform that is bad at all three. The mitigation is strict vertical slices: execution engine ships and stabilizes before lineage or governance are added. The second major risk class is concurrency correctness — non-atomic run state transitions and multi-layer concurrency limits are documented failure modes in Dagster that must be designed out from the first commit using `SELECT FOR UPDATE SKIP LOCKED` and a unified concurrency token pool. The third major risk is column-level access control: enforcement via a custom query proxy is bypassed by direct warehouse access; the right model is to provision masking policies to each warehouse's native mechanism and sync them from the platform.

---

## Key Findings

### Recommended Stack

The Go backend is well-served by a small, high-confidence library set. River (`riverqueue/river` v0.35.x) replaces Temporal as the job queue — it is Postgres-backed, transactional, and requires no external broker. `heimdalr/dag` v1.5.x provides thread-safe in-memory DAG resolution. `entgo.io/ent` v0.14.x owns schema definition and writes (the metadata model is graph-shaped; ent's edge-based model matches it directly); `sqlc` v1.31.x handles hot read paths. `ariga.io/atlas` handles migrations with dirty-state recovery that `golang-migrate` cannot provide. `casbin/casbin` v2.135.x handles RBAC + ABAC column policies without rolling custom auth. `hashicorp/go-plugin` v1.7.x provides subprocess-isolated connectors, the same model Terraform and Vault use in production. For the frontend: React 19 + TypeScript, ReactFlow v12 for the lineage DAG, shadcn/ui + Tailwind v4 for components, TanStack Query v5 for server state, Zustand v5 for UI state.

**Core technologies:**
- `riverqueue/river`: Postgres-backed job queue for run dispatch and cron — no external broker required
- `heimdalr/dag`: In-memory DAG with topological sort and cycle detection — thread-safe, generic, v1.5.1 Apr 2026
- `entgo.io/ent`: ORM for graph-shaped metadata schema — type-safe codegen, Atlas migration integration
- `sqlc`: Generated type-safe SQL for hot read paths (catalog search, lineage traversal, audit export)
- `ariga.io/atlas`: Schema migrations with dirty-state recovery — recommended by the ent project itself
- `casbin/casbin v2`: RBAC + ABAC policy engine for column-level access control — externalized policy model
- `hashicorp/go-plugin`: Out-of-process connector subprocess management — crash isolation, multi-language capable
- `connectrpc/connect-go`: HTTP/1.1 + HTTP/2 RPC for connector protocol and platform API
- `go-chi/chi v5`: net/http-native REST router — ecosystem compatible, no framework lock-in
- ReactFlow v12: Node-based lineage graph visualization — used by Dagster, supports interactive custom nodes

**Critical exclusions:** Atlas required over golang-migrate (dirty state on partial failure is a production incident). GORM excluded (reflection-based, implicit auto-migration, poor performance on complex joins). Go native `plugin` package excluded (same-binary-version requirement, no subprocess isolation). Fiber excluded (fasthttp breaks net/http ecosystem compatibility).

### Expected Features

Feature landscape verified against Dagster, DataHub, OpenMetadata, Apache Atlas, and Collibra.

**Must have (table stakes):**
- Software-defined assets in Go with explicit upstream dependency graph — the core execution mental model
- Cron + event-trigger scheduling with configurable retry and backoff
- Time-based and categorical partitioning with backfill support
- Run history, per-run execution logs, and asset freshness (stale/fresh) indicators
- Auto-captured schema metadata on materialization — no manual registration
- Asset/column descriptions + tags, full-text catalog search, ownership assignment
- Asset-level lineage DAG (table/asset level) with interactive UI
- Schema evolution tracking (version-to-version diff)
- Built-in null/range/uniqueness quality checks evaluated on materialization
- RBAC roles model, immutable audit log, SSO/OIDC authentication

**Should have (differentiators — these justify the project):**
- Field-level lineage: explicit Go API first, SQL parser as supplementary signal — Dagster hides this behind Dagster+ (paid); this ships it free in open source
- Asset publication approval workflow (Draft → Pending Review → Approved/Published/Rejected) — no open-source orchestrator has this
- Column-level masking policies with PII tag-to-policy propagation and downstream inheritance via lineage
- Compliance-grade tamper-evident audit log (hash-chain) with GDPR/SOC2 export
- Field-level impact analysis ("what downstream columns break if I change field X?")
- Freshness SLA declarations that bridge into governance workflow alerts on breach

**Defer to v2+:**
- SQL-inferred automatic column lineage (start with explicit Go API; SQL parsing is complex and dialect-specific)
- Row-level security (column-level is the stated differentiator; row-level requires a query proxy and is highly connector-specific)
- Python SDK (Go-first; Python bindings after API is stable)
- AI-generated metadata / LLM descriptions (design schema to accept AI enrichment without changes, but defer the feature)
- Multi-tenant SaaS hosting, managed connector marketplace

**Hard prerequisite chain (roadmap must respect this order):**
```
Asset + schema model
  → RBAC roles model
    → Immutable audit log
    → Field-level lineage graph
      → Asset publication states
        → Approval workflow
        → Column masking + downstream policy inheritance
```

### Architecture Approach

The platform mirrors Dagster's topology but collapses it into a single Go binary with run-mode flags (`server`, `api`, `worker`, `daemon`). Asset definitions live in user code (Go structs/interfaces), are registered into an in-memory `DefinitionRegistry` at startup, and are never serialized to the database — only execution history (runs, events) and derived metadata (schemas, lineage edges) are stored in PostgreSQL. The execution engine topologically sorts the asset graph at run time using the in-memory DAG, dispatches steps to a background worker pool backed by River, and writes all results to an append-only event log. The governance engine runs alongside the orchestration engine, sharing the same storage layer but with strict component boundaries — `WorkflowFSM` never touches `StepExecutor` and vice versa. Column access enforcement lives in a `QueryProxy` component that rewrites SQL before any connector sees it; connectors are deliberately policy-unaware.

**Major components:**
1. **Storage Abstraction Layer** — Go interfaces over PostgreSQL (SQLite for dev); Atlas-managed schema
2. **AssetGraph + DefinitionRegistry** — in-memory DAG of user-defined assets; loaded at startup from user code
3. **EventStore + RunStore** — append-only event log; run lifecycle state machine; source of truth for all execution state
4. **RunManager + StepExecutor** — run lifecycle management + topological step dispatch via River queue
5. **ConnectorRegistry** — compiled-in first-party and subprocess third-party (go-plugin) connectors; `Connector` interface is a stable public API from day one
6. **Scheduler + SensorEngine** — cron daemon and event-polling daemon; both write to RunStore, never execute directly
7. **LineageStore + LineageExtractor** — PostgreSQL adjacency list with recursive CTE traversal; SQL AST parser for supplementary extraction; explicit Go API for non-SQL transforms
8. **SchemaRegistry** — captures and diffs schema versions on each materialization; feeds LineageExtractor
9. **WorkflowFSM** — asset publication state machine (5 states, 4 transitions); hand-rolled FSM, no BPMN engine needed
10. **PolicyStore + QueryProxy** — column access policies in PostgreSQL; QueryProxy rewrites SQL AST before connector execution
11. **AuditLog** — hash-chain append-only PostgreSQL table; row security prevents UPDATE/DELETE even for the application user
12. **API Server** — chi REST + connect-go gRPC; GraphQL for UI; REST webhooks for OpenLineage ingestion
13. **React UI** — asset catalog, run history, ReactFlow lineage DAG, governance workflow UI, quality dashboard

**Critical interfaces (must be stable before first use):**
- `Connector` interface — public API, separate Go module, semantic versioning, compliance test suite
- `AssetDef` interface — the SDK surface users code against
- Storage interfaces — enables SQLite dev swap without changing business logic

### Critical Pitfalls

1. **Non-atomic run state transitions causing duplicate materializations** — Use `SELECT FOR UPDATE SKIP LOCKED` at the run claim step from day one. Write a test with 50 concurrent goroutines all trying to claim the same queued run; only one must succeed. This is Dagster issue #15155 in production.

2. **Multi-layer concurrency limits that interact and deadlock** — Design one centralized concurrency token table before adding any concurrency control. Never add op-level concurrency after run-level concurrency as separate features. Dagster issue #25743 (confirmed v1.8.13+) shows how this ends: backfills permanently stuck with no error.

3. **Column masking proxy bypassed by direct warehouse access** — Do not implement a custom SQL proxy as the sole enforcement mechanism. Use each warehouse's native column masking (Snowflake dynamic data masking, BigQuery column-level security) and have the platform provision and sync policies to those systems.

4. **Audit log tamper-evident only by convention, not cryptographically** — Implement the hash chain (`sha256(prev_hash || record_content)`) before the first audit record is written — retrofitting requires rewriting all existing records. PostgreSQL row security must prevent DELETE/UPDATE on the audit table even for the application DB user.

5. **Field-level lineage going stale after code changes** — Tie lineage version to asset code hash. On re-materialization with a changed code hash, require lineage re-declaration or flag it as potentially stale. Static declarations that drift from reality are worse than no lineage.

6. **Lineage adjacency list timing out at scale** — Use PostgreSQL `WITH RECURSIVE` from the start, not application-level traversal. Add depth limits to all traversal queries (default max 10). Benchmark at 500K edges before launching the lineage UI.

7. **Shipping scope too wide too early** — Execution engine must ship and stabilize before lineage or governance are added. This is the most common cause of open-source data platform abandonment.

---

## Implications for Roadmap

Based on the combined research, the following phase structure respects hard prerequisite dependencies and de-risks the most dangerous pitfalls at the earliest possible point.

### Phase 1: Foundation
**Rationale:** Every other component depends on storage abstraction, the asset definition type system, and the event log. Atomic run state transitions must be solved here — before any scheduler logic is written.
**Delivers:** Go module structure, Atlas migrations, PostgreSQL schema, `AssetDef` interface, `DefinitionRegistry`, `EventStore`, `RunStore` with `SELECT FOR UPDATE SKIP LOCKED`, SQLite dev mode, CLI scaffolding, `Connector` interface defined in separate module (no connectors yet).
**Addresses:** Software-defined assets, dependency graph, run history.
**Avoids:** Pitfall 1 (non-atomic transitions), Pitfall 11 (dirty migration state), Pitfall 14 (scope creep).

### Phase 2: Execution Engine
**Rationale:** The platform's core promise is reliable pipeline execution. Concurrency token system must be fully designed here — adding it later creates the Dagster deadlock pattern.
**Delivers:** `RunManager`, `StepExecutor` with topological dispatch, River job queue integration, `ConnectorRegistry` with compiled-in + go-plugin subprocess model, PostgreSQL and S3 first-party connectors, unified concurrency token table, goroutine leak tests (`goleak`), context cancellation propagated through all connector calls.
**Addresses:** Dependency-order execution, retry with backoff, connector extensibility.
**Avoids:** Pitfall 2 (concurrency deadlock), Pitfall 10 (context cancellation), Pitfall 9 (connector API instability).

### Phase 3: Scheduling, Sensors, and Partitions
**Rationale:** Scheduling and partitions are prerequisites for production use. Backfill resource isolation (priority queue) must be built before the backfill submit API.
**Delivers:** Scheduler daemon, sensor engine, time-based and categorical partition system, backfill with partition-chunked execution, run priority queues (`NORMAL`, `BACKFILL`, `CRITICAL`), daemon health heartbeats.
**Addresses:** Cron + event-trigger scheduling, partitioned assets, backfill, failure alerting.
**Avoids:** Pitfall 6 (backfill resource exhaustion).

### Phase 4: Lineage and Schema
**Rationale:** Field-level lineage is the primary technical differentiator. It must precede governance because downstream column policy inheritance and PII propagation depend on a complete lineage graph.
**Delivers:** `SchemaRegistry` with version diffs, `LineageStore` (PostgreSQL adjacency list, recursive CTE, depth-limited), 500K-edge benchmark, explicit Go column lineage API, optional SQL AST parsing as supplementary signal, lineage version tied to asset code hash with staleness detection, impact analysis API, materialized upstream/downstream count summary table.
**Addresses:** Table-level and field-level lineage, schema evolution, impact analysis.
**Avoids:** Pitfall 3 (stale lineage), Pitfall 4 (lineage scale), Pitfall 15 (PII propagation hooks).

### Phase 5: Governance Engine
**Rationale:** Governance depends on lineage (downstream policy inheritance), RBAC, and schema metadata. Audit log hash chain must be built before the first audit record is written.
**Delivers:** `PolicyStore` with Casbin RBAC, column masking policies, PII tag-to-policy propagation, warehouse-native masking sync (Snowflake, BigQuery, PostgreSQL), `QueryProxy` with SQL AST rewrite, `WorkflowFSM` (5 states, 4 transitions), governance review tables, notification dispatch, auto-approval policy, SLA escalation timer, hash-chain `AuditLog` with PostgreSQL row security, GDPR/SOC2 compliance export, data retention TTL policies, async data quality rules engine with blocking/non-blocking classification.
**Addresses:** Column-level masking, approval workflows, tamper-evident audit, compliance export, data quality.
**Avoids:** Pitfall 5 (audit not tamper-evident), Pitfall 7 (approval workflow bottleneck), Pitfall 8 (proxy bypassed), Pitfall 13 (quality rules blocking materialization).

### Phase 6: API and Web UI
**Rationale:** API and UI built last so the underlying data models are stable. Repeated UI rewrites are the cost of building the UI before Phase 1-5 are stable.
**Delivers:** chi REST API + connect-go gRPC API + OpenLineage webhook, swaggo OpenAPI 3.0 docs, React + TypeScript SPA (Vite, TanStack Router + Query, Zustand), asset catalog with search, run history with log streaming, ReactFlow lineage DAG (depth-limited, opt-in expansion, field-level drill-down), quality dashboard (Recharts), governance workflow UI, audit log viewer, shadcn/ui component library.
**Addresses:** Full observability UI, lineage visualization, governance workflow UI.
**Avoids:** Pitfall 12 (lineage graph unusable at scale — server-side depth limits from Phase 4; UI defaults to 1-2 hops).

### Phase Ordering Rationale

- Phases 1-2 are non-negotiable first — you cannot build a governance platform on an unreliable execution engine.
- Phase 3 follows execution because backfill concurrency interacts directly with Phase 2's token system; they must be adjacent.
- Phase 4 follows Phase 3 because lineage capture requires real runs producing real schemas; you need materialization events before lineage makes sense to capture.
- Phase 5 follows Phase 4 because downstream column policy inheritance and PII propagation require a complete lineage graph to be meaningful.
- Phase 6 is last by design — prevents repeated UI rewrites as the underlying models change across Phases 1-5.

### Research Flags

**Phases needing deeper research during planning:**
- **Phase 2 (Connector framework):** go-plugin subprocess protocol + connect-go interface contract need a focused design spike before the `Connector` interface is committed to a public module. This is an irreversible API surface.
- **Phase 4 (SQL lineage extraction):** Go SQL parser landscape (vitess vs. postgresql-parser vs. ANTLR) needs testing against real query corpora. DataHub achieves 97-99% accuracy using SQLGlot (Python); the Go equivalent is unvalidated.
- **Phase 5 (Warehouse-native masking sync):** Snowflake, BigQuery, and PostgreSQL each have different APIs for column masking policy management. Research before designing the PolicyStore sync interface.

**Phases with standard patterns (skip research-phase):**
- **Phase 1 (Foundation):** PostgreSQL schema design with ent + Atlas is extremely well-documented.
- **Phase 3 (Scheduling):** Cron daemon + sensor polling patterns in Go are standard.
- **Phase 6 (UI):** React + ReactFlow data platform UI patterns are well-documented; a short ReactFlow field-level drill-down spike is sufficient.

---

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| Stack | HIGH | All library versions verified on pkg.go.dev as of Apr 2026. One LOW item: ThijsKoot/openlineage-go is community-maintained; may need to vendor or reimplement event emission directly. |
| Features | HIGH | Verified against Dagster, DataHub, OpenMetadata, Collibra, and Apache Atlas docs. Feature prioritization reflects published adoption patterns. |
| Architecture | HIGH (execution), MEDIUM (governance) | Dagster internals thoroughly documented via DeepWiki and official docs. Governance architecture patterns have fewer Go-specific precedents. |
| Pitfalls | HIGH (execution), MEDIUM (governance workflows) | Execution pitfalls verified against specific Dagster GitHub issues with issue numbers. Governance workflow pitfalls backed by good sources with less Go-specific precedent. |

**Overall confidence:** HIGH

### Gaps to Address

- **OpenLineage Go client:** `ThijsKoot/openlineage-go` is community-maintained. Plan to vendor it and prepare a fallback: the OpenLineage JSON event format is simple enough to implement directly if the client proves insufficient.
- **SQL parser accuracy in Go:** DataHub achieves 97-99% column lineage accuracy using SQLGlot (Python). The Go equivalent has not been benchmarked on production query corpora. Measure in Phase 4.
- **Warehouse-native masking API coverage:** Snowflake and BigQuery masking APIs are confirmed to exist. The specific Go SDK calls for programmatic policy provisioning need validation before designing the PolicyStore sync interface in Phase 5.
- **Custom DAG scheduler vs. Temporal at scale:** Correct decision for Phases 1-3. If multi-worker distributed execution across separate machines becomes a hard requirement, revisit. Design storage interfaces in Phase 1 so the scheduler backend can be swapped.
- **Casbin v3 timing:** Pin to v2 (v2.135.x, stable). Watch v3 release cadence; avoid adopting v3 until ecosystem adapters are updated.

---

## Sources

### Primary (HIGH confidence)

- Dagster OSS Architecture: https://docs.dagster.io/deployment/oss/oss-deployment-architecture
- Dagster issue #15155 (duplicate runs in backfill): https://github.com/dagster-io/dagster/issues/15155
- Dagster issue #25743 (concurrency deadlock, v1.8.13+): https://github.com/dagster-io/dagster/issues/25743
- River: https://riverqueue.com/ and https://pkg.go.dev/github.com/riverqueue/river (v0.35.x, Apr 2026)
- heimdalr/dag: https://pkg.go.dev/github.com/heimdalr/dag (v1.5.1, Apr 2026)
- ent ORM: https://entgo.io/ (v0.14.x, Mar 2026)
- sqlc: https://docs.sqlc.dev/ (v1.31.1, Apr 2026)
- Atlas vs golang-migrate: https://atlasgo.io/blog/2025/04/06/atlas-and-golang-migrate
- hashicorp/go-plugin: https://github.com/hashicorp/go-plugin (v1.7.0, Aug 2025)
- connect-go: https://connectrpc.com/ (v1.19.2, Apr 2026)
- Casbin: https://casbin.apache.org/ (v2.135.x, Dec 2025)
- OpenLineage column lineage facet: https://openlineage.io/docs/spec/facets/dataset-facets/column_lineage_facet/
- DataHub SQL column lineage: https://datahub.com/blog/extracting-column-level-lineage-from-sql/
- OpenMetadata governance: https://docs.open-metadata.org/latest/how-to-guides/data-governance

### Secondary (MEDIUM confidence)

- Go HTTP framework comparison (JetBrains 2026): https://blog.jetbrains.com/go/2026/04/28/popular-golang-web-frameworks/
- Go ORM comparison: https://www.bytebase.com/blog/golang-orm-query-builder/
- Hoop.dev column-level PostgreSQL access: https://hoop.dev/blog/column-level-access-for-postgres-at-protocol-speed
- Dagster column lineage docs: https://docs.dagster.io/guides/build/assets/metadata-and-tags/column-level-lineage
- Open source data governance landscape 2025: https://atlan.com/open-source-data-governance-tools/
- Backfill resource isolation (LakeFS): https://lakefs.io/blog/backfilling-data-foolproof-guide/
- Tamper-evident logging (ACM CCS 2025): https://dl.acm.org/doi/10.1145/3719027.3765024

### Tertiary (LOW confidence)

- ThijsKoot/openlineage-go: https://github.com/ThijsKoot/openlineage-go — community-maintained Go client; not an official OpenLineage project; accuracy and completeness unverified for production use

---

*Research completed: 2026-04-29*
*Ready for roadmap: yes*
