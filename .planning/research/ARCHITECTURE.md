# Architecture Patterns

**Domain:** Data orchestration + governance platform (Go-native, Dagster-inspired)
**Researched:** 2026-04-29
**Overall confidence:** HIGH (Dagster internals), MEDIUM (governance patterns), MEDIUM (lineage graph storage)

---

## 1. Dagster Architecture Dissected

Understanding Dagster's architecture is the primary input to this design. Below is a faithful reconstruction from documentation and source analysis.

### 1.1 Overall Topology (OSS)

Dagster OSS runs as three cooperating long-running services plus pluggable storage:

```
┌─────────────────────────────────────────────────────────────────┐
│                        Client Browser                           │
└───────────────────────────┬─────────────────────────────────────┘
                            │ HTTP / GraphQL
┌───────────────────────────▼─────────────────────────────────────┐
│                    dagster-webserver (Dagit)                     │
│  • Serves React SPA                                             │
│  • GraphQL API (Graphene schema-first)                          │
│  • Reads definitions via code-location gRPC                     │
│  • Writes run launches to RunStorage / RunCoordinator           │
└────────────┬──────────────────────────────────┬─────────────────┘
             │ SQL / storage abstraction         │ gRPC
             │                                  │
┌────────────▼──────────────┐    ┌──────────────▼──────────────┐
│    Shared Storage Layer   │    │  Code Location Server(s)    │
│  • RunStorage             │    │  • One gRPC server per      │
│  • EventLogStorage        │    │    code location            │
│  • ScheduleStorage        │    │  • Loads Definitions object │
│  (SQLite dev /            │    │  • Serves asset/job/        │
│   Postgres/MySQL prod)    │    │    schedule metadata        │
└────────────▲──────────────┘    └──────────────▲──────────────┘
             │                                  │ gRPC
┌────────────┴──────────────────────────────────┴──────────────┐
│                    dagster-daemon                              │
│  • SchedulerDaemon  — creates runs from cron schedules        │
│  • SensorDaemon     — polls sensors, emits run requests       │
│  • RunQueueDaemon   — dequeues and launches runs              │
│  • RunMonitorDaemon — handles worker failures / timeouts      │
│  (single instance only; not replicable)                       │
└───────────────────────────────────────────────────────────────┘
             │ spawns
┌────────────▼──────────────────────────────────────────────────┐
│                    Run Worker (subprocess/pod)                 │
│  • Loaded per run                                             │
│  • Builds ExecutionPlan from asset/job graph                  │
│  • Executor decides step dispatch strategy                    │
│  • Writes DagsterEvents to EventLogStorage                    │
└───────────────────────────────────────────────────────────────┘
```

### 1.2 Asset Definition Storage and Resolution

Assets are not "stored" in a database — they live in user code. The resolution chain is:

1. User code defines `@asset` functions. The decorator captures: asset key, upstream dependencies (inferred from function parameter names), partitions definition, IO manager key, metadata, tags.
2. A `Definitions` object acts as the registry — it's the in-process catalog for one code location.
3. The code location gRPC server loads the `Definitions` object on startup and re-loads it on file-change signals.
4. Both the webserver and daemon connect to code location servers over gRPC to query definitions (asset keys, dependency edges, schedules, sensors).
5. `resolve_asset_graph()` performs topological sort at query time to produce the execution plan. No separate graph table is maintained — the graph is always recomputed from definitions.

**Key insight for Go port:** Asset definitions must be first-class runtime objects (structs/interfaces), not just annotations. A `DefinitionRegistry` component must allow registration at startup and introspection at runtime.

### 1.3 Execution Engine: Job → Op → Asset Pipeline

```
AssetSelection
     │
     ▼
AssetGraph.toJob()          → Synthesizes a Job from selected assets
     │
     ▼
ExecutionPlan.build()       → Topological sort → ordered StepExecutionData
     │
     ▼
Executor.execute()          → Dispatches steps per strategy:
  • InProcessExecutor       → serial, same goroutine/thread
  • MultiprocessExecutor    → one subprocess per step
  • Distributed (Celery,    → tasks queued to external broker
    Dask, K8s, ECS)
     │
     ▼
StepWorker                  → Runs user function
  • Calls IO manager load() for each input
  • Calls user asset function
  • Calls IO manager handle_output() for return value
  • Emits DagsterEvents: STEP_START, STEP_OUTPUT, STEP_SUCCESS/FAILURE
```

Steps communicate results back exclusively via the EventLogStorage — there is no direct worker-to-coordinator IPC for results. The coordinator polls storage.

### 1.4 Event Log / Run Storage

Three storage abstractions, all backed by the same SQL database in practice:

| Store | What It Holds | Key Operations |
|-------|--------------|----------------|
| `RunStorage` | `DagsterRun` records: status, config, tags, job snapshot | `add_run`, `update_run`, `get_run_by_id`, `get_runs` (with filter/pagination) |
| `EventLogStorage` | All `DagsterEvent` records per run: STEP_START, ASSET_MATERIALIZATION, LOGS, etc. | `store_event`, `get_logs_for_run`, `get_asset_records`, `get_event_records` |
| `ScheduleStorage` | Schedule/sensor tick history, instigator state | `get_instigator_state`, `update_instigator_state`, `create_tick`, `update_tick` |

**SQLite (dev):** EventLog is run-sharded — one SQLite file per run. Prevents lock contention but makes cross-run queries hard.

**PostgreSQL (prod):** Single consolidated tables. Secondary indexes added via migration. Connection pooling enabled. `asset_keys` table caches latest materialization per asset for fast UI queries.

**Pattern:** Event sourcing. All execution state is derived from the immutable event stream. Run status is computed by scanning events, not stored directly (though it is cached for performance).

### 1.5 Daemon Internals

`dagster-daemon` is a single process with multiple daemon threads, each polling on a fixed interval:

- **SchedulerDaemon:** Queries ScheduleStorage for ticks due. For each overdue schedule, calls code location gRPC server to evaluate the schedule function, then creates a `DagsterRun` in RunStorage and enqueues it.
- **SensorDaemon:** Same pattern but evaluates sensor cursor state and emits `RunRequest` objects per evaluation.
- **RunQueueDaemon:** Reads RunStorage for queued runs; applies concurrency limits and priority rules; calls `RunLauncher.launch_run()`.
- **RunMonitorDaemon:** Polls for runs in STARTING/STARTED state whose worker process has died; marks them FAILURE.

Each daemon writes a heartbeat to storage so the webserver can show daemon health status.

### 1.6 Webserver ↔ Backend Communication (GraphQL API)

- Webserver exposes a single GraphQL endpoint.
- Schema is defined schema-first in Python using Graphene.
- Two asset types: `GrapheneAssetNode` (definition-time: dependencies, partitions, automation conditions) vs `GrapheneAsset` (runtime: last materialization, freshness).
- Asset queries use `DataLoader` pattern: batch-loads asset records to avoid N+1 queries against storage.
- `WorkspaceRequestContext` wraps both storage access and code location gRPC connections.
- Mutations (`launchPipelineExecution`, `launchPartitionBackfill`) check permissions, write a `DagsterRun` record, then enqueue via `RunCoordinator`.
- The UI (React) uses Apollo Client for GraphQL, Recoil for client state. TypeScript types generated from schema.

### 1.7 IO Managers (Connector Abstraction)

IO managers are Dagster's connector interface. Each is a resource registered under a key (default `"io_manager"`).

Interface contract:
```python
class IOManager:
    def handle_output(self, context: OutputContext, obj: Any) -> None: ...
    def load_input(self, context: InputContext) -> Any: ...
```

`OutputContext` and `InputContext` carry: asset key, partition key, metadata, run ID, resource config. This makes IO managers fully context-aware — they can use the partition key to route to the right S3 prefix or database partition.

IO managers are attached per-asset or globally. They decouple the asset function (pure transformation logic) from storage concerns.

---

## 2. Field-Level Lineage Architecture

### 2.1 OpenLineage Specification

OpenLineage defines a standard event model: `Run → Job → Dataset`. Lineage is emitted as events during execution.

Column-level lineage is a facet attached to output datasets:

```json
{
  "columnLineage": {
    "fields": {
      "revenue_usd": {
        "inputFields": [
          {
            "namespace": "postgresql://prod",
            "name": "orders.order_value",
            "field": "order_value",
            "transformations": [
              { "type": "DIRECT", "subtype": "TRANSFORMATION", "description": "multiply by fx_rate" }
            ]
          }
        ]
      }
    }
  }
}
```

Transformation types:
- `DIRECT` subtypes: `IDENTITY`, `TRANSFORMATION`, `AGGREGATION`
- `INDIRECT` subtypes: `JOIN`, `GROUP_BY`, `FILTER`, `SORT`, `WINDOW`, `CONDITIONAL`

**Implication for Go platform:** Asset functions must emit column lineage either (a) automatically via SQL parsing of declared queries, or (b) manually via a lineage annotation API. Both should produce OpenLineage-compatible events.

### 2.2 SQL-Based Column Lineage Extraction

When an asset executes a SQL query, column lineage can be extracted by parsing the SQL AST:

```
SQL string
    │
    ▼
SQL Parser (AST)
    │
    ▼
AST Traversal
    │  • Resolve table aliases → fully qualified names
    │  • Resolve column aliases
    │  • Handle CTEs recursively
    │  • Handle subqueries
    │  • Handle UNION (merge input columns)
    ▼
Column mapping: output_col → [(source_table, source_col, transform_type)]
    │
    ▼
OpenLineage ColumnLineageFacet event
```

Go SQL parsers for this:
- `vitess.io/vitess/go/vt/sqlparser` — production-grade, handles MySQL dialect well. Used by PlanetScale/Vitess in production.
- `github.com/pingcap/tidb/parser` — TiDB's parser, strong MySQL + some PostgreSQL support.
- `github.com/auxten/postgresql-parser` — PostgreSQL dialect.
- ANTLR-based parsers for multi-dialect support (generates Go code from grammar).

**Recommendation:** Use `vitess.io/vitess/go/vt/sqlparser` for MySQL/generic SQL and `github.com/auxten/postgresql-parser` or a ANTLR grammar for PostgreSQL. Accept that 100% accuracy is hard — DataHub reports 97-99% accuracy on production query corpora using schema-aware parsing.

Schema-awareness is critical: without knowing the schema of input tables, `SELECT *` and bare column names are ambiguous. The lineage extractor needs access to the metadata catalog to resolve schemas.

### 2.3 Lineage Graph Storage

Two viable approaches for a Go platform targeting PostgreSQL as primary store:

**Option A: Adjacency list in PostgreSQL (recommended for v1)**

```sql
CREATE TABLE lineage_edges (
    id          BIGSERIAL PRIMARY KEY,
    edge_type   TEXT NOT NULL,          -- 'TABLE' or 'COLUMN'
    src_asset   TEXT NOT NULL,
    src_field   TEXT,                   -- NULL for table-level edges
    dst_asset   TEXT NOT NULL,
    dst_field   TEXT,
    transform   JSONB,                  -- OpenLineage transformation metadata
    run_id      TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ON lineage_edges (dst_asset, dst_field);  -- impact analysis
CREATE INDEX ON lineage_edges (src_asset, src_field);  -- upstream lookup
```

Impact analysis query: "which output fields are affected if I change `orders.vat_rate`?"
→ Recursive CTE walking `src_asset/src_field` → `dst_asset/dst_field`.

PostgreSQL's `WITH RECURSIVE` handles DAG traversal well at governance-relevant scales (millions of edges, hundreds of assets). This avoids a Neo4j operational dependency.

**Option B: Dedicated graph DB (Neo4j or Apache AGE)**

Neo4j gives O(1) relationship traversal via index-free adjacency (pointer chasing vs. index lookups in PostgreSQL). The advantage emerges at >5 hops and >10M edges. For data governance lineage graphs (typically shallow, bounded), PostgreSQL wins on operational simplicity.

Apache AGE (graph extension for PostgreSQL) is a middle path — Cypher queries inside PostgreSQL — but it is not production-mature enough for v1.

**Decision:** Adjacency list in PostgreSQL with recursive CTEs for v1. Design the lineage storage interface (Go interface) so it can be swapped to Neo4j later without changing query call sites.

---

## 3. Governance Architecture Patterns

### 3.1 Approval Workflows

Approval workflows are state machines over asset publication events.

Recommended state model:

```
DRAFT
  │ submit_for_review
  ▼
PENDING_REVIEW
  │ approve                │ reject
  ▼                        ▼
PUBLISHED             REJECTED (with comment)
  │ deprecate
  ▼
DEPRECATED
```

**Implementation pattern:** Store workflow state in a `governance_reviews` table. Transitions are appended as rows in `governance_events` (append-only). Current state is derived by reading the latest event.

```sql
CREATE TABLE governance_reviews (
    id          UUID PRIMARY KEY,
    asset_key   TEXT NOT NULL,
    asset_version TEXT NOT NULL,
    state       TEXT NOT NULL,          -- current state (cached for queries)
    created_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE governance_events (
    id          BIGSERIAL PRIMARY KEY,
    review_id   UUID NOT NULL REFERENCES governance_reviews(id),
    event_type  TEXT NOT NULL,          -- SUBMITTED, APPROVED, REJECTED, DEPRECATED
    actor       TEXT NOT NULL,
    comment     TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Notifications are triggered by a post-event hook: when a row is inserted into `governance_events`, the notification service dispatches to configured channels (email, Slack webhook, in-app).

BPMN engines (Camunda, Zeebe) are overkill for v1 — the state machine has 5 states and 4 transitions. A hand-rolled FSM stored in PostgreSQL is appropriate.

### 3.2 Column-Level Access Control

Three enforcement patterns, ordered by invasiveness:

**Pattern 1: Query Rewriting (recommended for this platform)**

The platform maintains a policy store:

```sql
CREATE TABLE column_policies (
    id          BIGSERIAL PRIMARY KEY,
    asset_key   TEXT NOT NULL,
    column_name TEXT NOT NULL,
    role        TEXT NOT NULL,
    action      TEXT NOT NULL  -- ALLOW, MASK, REDACT
);
```

When a user queries data through the platform's query proxy:
1. Parse the SQL AST.
2. Look up policies for each referenced column.
3. Rewrite the AST: replace restricted column references with `NULL AS col` (REDACT) or `mask_fn(col) AS col` (MASK).
4. Execute rewritten SQL against the backend.

This is the approach used by Databricks Unity Catalog (column masks) and BigQuery column-level security.

**Pattern 2: Wire-Protocol Proxy**

A PostgreSQL wire-protocol proxy (like PgBouncer extended with policy logic) intercepts `RowDescription` and `DataRow` packets, stripping or masking restricted columns at byte level. This works at protocol speed without query rewriting latency. Used by hoop.dev for column-level PostgreSQL access control.

**Pattern 3: View Layer**

Generate role-specific views (`CREATE VIEW orders_for_analyst AS SELECT id, date, amount FROM orders` omitting PII columns). Simple but doesn't generalize — view proliferation becomes unmanageable.

**Recommendation:** Query rewriting proxy for v1. The platform controls the query path (users query through the platform, not directly against the data warehouse). This enables uniform enforcement regardless of connector backend.

### 3.3 Tamper-Evident Audit Log

The audit log must be:
1. Append-only (no UPDATE/DELETE)
2. Tamper-evident (modifying a past record is detectable)
3. Queryable (compliance exports, GDPR deletion proof)

**Implementation: Hash-chain append-only log**

```sql
CREATE TABLE audit_log (
    seq         BIGSERIAL PRIMARY KEY,
    event_type  TEXT NOT NULL,
    actor       TEXT NOT NULL,
    resource    TEXT NOT NULL,
    detail      JSONB,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    entry_hash  TEXT NOT NULL  -- SHA-256(seq || event_type || actor || resource || detail || prev_hash)
);
```

Each row's `entry_hash` includes the hash of the previous row (chain). To verify integrity, re-compute hashes from `seq=1` and compare. Modification of any row breaks all subsequent hashes.

For stronger guarantees: periodically anchor the current chain head hash to an external log (S3, CloudWatch, or a public transparency log like Trillian). This provides proof of existence at a point in time.

**Database-level protection:**
- Use PostgreSQL row-level security to deny UPDATE/DELETE on `audit_log` for all roles except a dedicated `audit_writer` role.
- The application writes via `audit_writer`; application service accounts use a role with INSERT-only.

---

## 4. Recommended Component Architecture for Go Platform

### 4.1 Component Map

```
┌─────────────────────────────────────────────────────────────────────┐
│                           User Code (Go)                            │
│  • Asset definitions (structs implementing AssetDef interface)      │
│  • Connector implementations                                        │
│  • Custom quality rules                                             │
└───────────────────────────────┬─────────────────────────────────────┘
                                │ loaded at startup via registry
┌───────────────────────────────▼─────────────────────────────────────┐
│                       Platform Core Binary                          │
│                                                                     │
│  ┌─────────────────┐  ┌─────────────────┐  ┌──────────────────┐   │
│  │  Orchestration  │  │   Governance    │  │  Metadata/       │   │
│  │  Engine         │  │   Engine        │  │  Catalog         │   │
│  │                 │  │                 │  │                  │   │
│  │ • AssetGraph    │  │ • WorkflowFSM   │  │ • SchemaRegistry │   │
│  │ • Scheduler     │  │ • PolicyStore   │  │ • LineageStore   │   │
│  │ • RunManager    │  │ • AuditLog      │  │ • TagStore       │   │
│  │ • StepExecutor  │  │ • Notifications │  │ • SearchIndex    │   │
│  └────────┬────────┘  └────────┬────────┘  └──────────┬───────┘   │
│           │                   │                       │           │
│  ┌────────▼───────────────────▼───────────────────────▼───────┐   │
│  │                    Storage Abstraction Layer                │   │
│  │  RunStore | EventStore | LineageStore | PolicyStore |       │   │
│  │  AuditStore | SchemaStore | CatalogStore                    │   │
│  └────────────────────────────┬────────────────────────────────┘   │
│                               │                                     │
│  ┌────────────────────────────▼────────────────────────────────┐   │
│  │               PostgreSQL (primary store, all data)          │   │
│  │         + Elasticsearch / Bleve (full-text search)          │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │                    API Server (gRPC + REST/GraphQL)          │   │
│  │  • GraphQL API for UI                                        │   │
│  │  • gRPC API for connectors / CLI / external tools           │   │
│  │  • REST webhooks for OpenLineage ingestion                   │   │
│  └─────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
             │ serves
┌────────────▼────────────────────────────────────────────────────────┐
│                         Web UI (React + TypeScript)                  │
│  • Asset graph visualization                                         │
│  • Lineage DAG (field-level drill-down)                              │
│  • Quality dashboard                                                 │
│  • Governance workflow UI                                            │
└──────────────────────────────────────────────────────────────────────┘
```

### 4.2 Component Boundaries

| Component | Responsibility | Must Not Do | Talks To |
|-----------|---------------|-------------|----------|
| **AssetGraph** | Hold asset definitions, build dependency DAG, topological sort | Execute steps, write storage | RunManager, StepExecutor |
| **RunManager** | Create/update run records, manage run lifecycle state machine | Execute steps | EventStore, RunStore, StepExecutor |
| **StepExecutor** | Dispatch asset functions in dependency order; manage parallelism | Know about governance | ConnectorRegistry, EventStore, RunManager |
| **ConnectorRegistry** | Load + validate connector plugins; provide IOManager interface | Execute transforms | StepExecutor |
| **Scheduler** | Poll cron schedules; enqueue materialization runs | Launch runs directly | RunManager, AssetGraph |
| **SensorEngine** | Evaluate sensor functions; emit run requests | Launch runs directly | RunManager, ConnectorRegistry |
| **WorkflowFSM** | Govern state transitions for asset review lifecycle | Execute assets | AuditLog, Notifications, PolicyStore |
| **PolicyStore** | Store and evaluate column access policies | Enforce at execution time | QueryProxy, UI |
| **QueryProxy** | Intercept queries, rewrite for policy enforcement, emit audit events | Store policies | PolicyStore, AuditLog, ConnectorRegistry |
| **LineageStore** | Store and query table/field lineage edges | Extract lineage | AssetGraph, CatalogStore |
| **LineageExtractor** | Parse SQL AST → column lineage edges | Store anything | LineageStore, SchemaRegistry |
| **AuditLog** | Append audit events with hash-chain integrity | Allow any reads by untrusted roles | PostgreSQL (direct, append-only) |
| **SchemaRegistry** | Track schema versions; diff schemas between materializations | Execute anything | CatalogStore, LineageExtractor |
| **CatalogStore** | Persistent metadata: descriptions, tags, owners, quality history | Enforce policies | UI, SchemaRegistry, LineageStore |
| **API Server** | Serve GraphQL (UI), gRPC (tools/connectors), OpenLineage webhook | Business logic | All other components |

### 4.3 Data Flow: Asset Materialization

```
1. Trigger (schedule tick / sensor event / user click in UI)
   │
2. RunManager.CreateRun(assetKey, partitionKey, config)
   → writes DagsterRun{status: QUEUED} to RunStore
   │
3. Scheduler polls RunStore for QUEUED runs
   → calls RunManager.LaunchRun(runID)
   → writes Run{status: STARTING} to RunStore
   │
4. StepExecutor.Execute(executionPlan)
   → topological sort of steps
   → for each step in order:
      a. ConnectorRegistry.LoadInput(upstreamAsset)  ← IO manager load
      b. call user asset function
      c. LineageExtractor.ExtractFromSQL(query)       ← if SQL was used
      d. ConnectorRegistry.HandleOutput(result)       ← IO manager store
      e. EventStore.Store(ASSET_MATERIALIZATION event)
      f. SchemaRegistry.UpdateSchema(assetKey, schema)
      g. LineageStore.StoreEdges(edges)
      h. QualityEngine.EvaluateRules(assetKey)
   │
5. RunManager updates Run{status: SUCCESS/FAILED}
   │
6. EventStore notifies SensorEngine (event-based sensors)
   │
7. UI polls GraphQL → shows updated asset status
```

### 4.4 Data Flow: Governance Workflow

```
1. User submits asset for review via UI
   → WorkflowFSM.Transition(assetKey, DRAFT → PENDING_REVIEW)
   → AuditLog.Append(REVIEW_SUBMITTED)
   → Notifications.Send(reviewers, "Review requested")
   │
2. Reviewer approves/rejects via UI
   → WorkflowFSM.Transition(assetKey, PENDING_REVIEW → PUBLISHED/REJECTED)
   → AuditLog.Append(REVIEW_APPROVED/REJECTED, actor, comment)
   → Notifications.Send(submitter, "Decision: ...")
   │
3. On PUBLISHED: asset becomes queryable through platform
   PolicyStore.ActivatePolicies(assetKey)
   │
4. Column-level query enforcement:
   User query → QueryProxy
     → PolicyStore.Evaluate(user, asset, columns)
     → SQL rewrite (mask/redact restricted columns)
     → Execute rewritten query
     → AuditLog.Append(DATA_ACCESS, user, asset, columns_accessed)
     → Return results to user
```

### 4.5 Connector Interface Design

Connectors are the Go equivalent of Dagster's IO managers. They should be a stable, versioned public API from day one.

```go
// Connector is the stable public interface for all data connectors.
// Third-party connectors implement this interface.
type Connector interface {
    // Metadata
    Name() string
    Version() string
    Capabilities() ConnectorCapabilities

    // Lifecycle
    Configure(cfg map[string]any) error
    Ping(ctx context.Context) error
    Close() error

    // IO
    Read(ctx context.Context, ref DataRef) (DataSet, error)
    Write(ctx context.Context, ref DataRef, data DataSet) error

    // Schema
    InspectSchema(ctx context.Context, ref DataRef) (Schema, error)

    // Lineage (optional — connectors may return nil)
    ExtractLineage(ctx context.Context, query string) ([]LineageEdge, error)
}

type ConnectorCapabilities struct {
    SupportsPartitions bool
    SupportsStreaming   bool
    SupportsLineage     bool
    SupportsSchemaEvo   bool
    SupportedDialects   []SQLDialect
}
```

**Plugin deployment topology:**

Three options, ordered by complexity:

| Option | Mechanism | Pro | Con |
|--------|-----------|-----|-----|
| **Compiled-in** | Connector is in same Go binary | Zero IPC overhead; type-safe | Requires rebuild to add connector |
| **go-plugin (gRPC subprocess)** | HashiCorp go-plugin: connector runs as subprocess, communicates over gRPC | Process isolation; crash-safe; multi-language | Startup latency; IPC overhead |
| **External gRPC service** | Connector runs as separate service; platform calls it over gRPC | Full language independence; independent deployment | Network latency; service management |

**Recommendation:** Compiled-in for all first-party connectors (PostgreSQL, MySQL, BigQuery, Snowflake, S3, GCS). Use go-plugin subprocess model for third-party connectors. This matches Terraform/Vault's plugin model, which is proven in production.

### 4.6 Deployment Topology

**Dev / single-machine:**
```
docker-compose.yml:
  - platform (single binary: API + engine + daemon)
  - postgres
  - elasticsearch (optional, can use in-process Bleve for dev)
  - ui (nginx serving React SPA)
```

The core binary should support three run modes:
- `platform server` — starts all subsystems (for dev/small deployments)
- `platform api` — API server only (scales horizontally)
- `platform worker` — step executor only (scales by adding workers)
- `platform daemon` — scheduler/sensor daemon (singleton)

This mirrors Dagster's separation but in a single binary with feature flags — much simpler to operate.

**Production / Kubernetes:**
```
Deployment: platform-api       (replicas: 3)
Deployment: platform-worker    (replicas: N, autoscaled)
Deployment: platform-daemon    (replicas: 1, singleton)
StatefulSet: postgres
Deployment: elasticsearch
```

---

## 5. Suggested Build Order (Phase Dependencies)

Dependencies flow top-to-bottom — each layer must be built before the next.

```
Phase 1: Foundation
  ├── Storage layer (PostgreSQL schema migrations, storage interfaces)
  ├── AssetDefinition type system (AssetKey, AssetGraph, DependencyGraph)
  ├── EventLog (store + retrieve DagsterEvents)
  └── CLI scaffolding (project init, run command)

Phase 2: Execution Engine
  ├── [requires Phase 1] RunManager (create/track runs)
  ├── [requires Phase 1] StepExecutor (topological dispatch)
  ├── [requires Phase 1] ConnectorRegistry + Connector interface
  ├── [requires ConnectorRegistry] First-party connectors (PostgreSQL, S3)
  └── [requires StepExecutor] In-process and multi-process executors

Phase 3: Scheduling + Sensors
  ├── [requires Phase 2] Scheduler daemon (cron → run creation)
  ├── [requires Phase 2] Sensor engine (event polling → run creation)
  └── [requires Phase 2] Partition system (time-based, categorical, dynamic)

Phase 4: Lineage + Schema
  ├── [requires Phase 2] Schema registry (capture schemas on materialization)
  ├── [requires Phase 2] SQL lineage extractor (AST parsing → column edges)
  ├── [requires Phase 1] LineageStore (adjacency list, recursive CTE queries)
  └── [requires SchemaRegistry] Impact analysis API

Phase 5: Governance
  ├── [requires Phase 4] Policy store (column access policies)
  ├── [requires Policy store] Query proxy (SQL rewrite for enforcement)
  ├── [requires Phase 1] Approval workflow FSM
  ├── [requires all] Audit log (hash-chain, append-only)
  └── [requires Phase 2] Data quality rules engine

Phase 6: API + UI
  ├── [requires Phase 2] GraphQL API (asset graph, runs, events)
  ├── [requires Phase 4] Lineage API (field-level graph queries)
  ├── [requires Phase 5] Governance API (policies, workflows, audit)
  ├── [requires GraphQL] React UI: asset catalog + run history
  ├── [requires Lineage API] React UI: lineage DAG visualization
  └── [requires Governance API] React UI: governance workflows
```

---

## 6. Critical Architecture Decisions

| Decision | Recommendation | Rationale |
|----------|---------------|-----------|
| Single binary vs microservices | Single binary with run-mode flags | Operational simplicity; can split later when scale demands it |
| Asset definition mechanism | Go interfaces + registration at startup (not code generation) | Avoid reflection complexity; type-safe |
| Storage backend | PostgreSQL only for v1 (SQLite for dev via build tag) | Avoid managing two storage backends; PostgreSQL covers all needs |
| Lineage graph | Adjacency list in PostgreSQL + recursive CTEs | Avoid Neo4j operational dependency; sufficient for v1 scale |
| Connector plugin system | Compiled-in (first-party) + go-plugin subprocess (third-party) | Matches proven HashiCorp model; crash isolation for third-party |
| API protocol | GraphQL for UI (rich queries, schema evolution), gRPC for CLI/programmatic | Established pattern from Dagster; generated types for both |
| Column access enforcement | Query rewriting proxy | Uniform enforcement across connectors; no per-connector logic |
| Audit log integrity | Hash-chain in PostgreSQL | Simple, no external dependency; add Merkle anchoring later |
| Workflow engine | Hand-rolled FSM | 5 states, 4 transitions; BPMN is overkill |
| Search | Embedded Bleve (dev) / Elasticsearch (prod) | Keep dev setup simple; Elasticsearch for scale |

---

## 7. Anti-Patterns to Avoid

### Anti-Pattern 1: Storing Asset Definitions in the Database
**What:** Serializing asset structs to database rows as the source of truth.
**Why bad:** Definitions in code are the source of truth; a database copy creates a sync problem, versioning ambiguity, and makes the schema rigid.
**Instead:** Asset definitions live in user code, loaded at startup into an in-memory registry. Storage captures only execution history (runs, events) and derived metadata (schemas, lineage).

### Anti-Pattern 2: Direct Database Access from Asset Functions
**What:** Asset functions call `sql.Open()` directly rather than going through the Connector interface.
**Why bad:** Breaks lineage extraction (platform can't see what was queried), breaks policy enforcement, breaks schema capture.
**Instead:** Asset functions receive a typed connector via dependency injection. The platform wraps calls to extract lineage and enforce policy transparently.

### Anti-Pattern 3: Mutable Audit Log
**What:** `UPDATE audit_log SET detail = ... WHERE id = X` for "corrections."
**Why bad:** Destroys tamper-evidence; compliance frameworks require immutability.
**Instead:** Corrections are new append-only entries (`CORRECTION` event type referencing the original entry).

### Anti-Pattern 4: Synchronous Step Execution in the API Handler
**What:** API handler executes the asset function in the HTTP request/response cycle.
**Why bad:** Long-running assets block the API; no retry, no parallelism, no observable progress.
**Instead:** API handler enqueues a run; a background worker daemon picks it up and executes. Progress visible via EventLog.

### Anti-Pattern 5: Per-Connector Policy Enforcement
**What:** Each connector independently checks column policies.
**Why bad:** Policy logic duplicated; easy to forget; connectors are third-party code.
**Instead:** QueryProxy enforces all policies before any connector sees the query. Connectors are policy-unaware.

---

## Sources

- [Dagster OSS Deployment Architecture](https://docs.dagster.io/deployment/oss/oss-deployment-architecture)
- [Dagster Daemon Documentation](https://docs.dagster.io/deployment/execution/dagster-daemon)
- [Dagster I/O Managers](https://docs.dagster.io/guides/build/io-managers)
- [Dagster Code Locations Architecture Blog](https://dagster.io/blog/dagster-code-locations)
- [Dagster GraphQL API (DeepWiki)](https://deepwiki.com/dagster-io/dagster/6-graphql-api)
- [Dagster Core Definitions System (DeepWiki)](https://deepwiki.com/dagster-io/dagster/2-core-definitions-system)
- [Dagster Storage and Persistence (DeepWiki)](https://deepwiki.com/dagster-io/dagster/5-storage-and-persistence)
- [OpenLineage Column Lineage Facet](https://openlineage.io/docs/spec/facets/dataset-facets/column_lineage_facet/)
- [DataHub: Extracting Column-Level Lineage from SQL](https://datahub.com/blog/extracting-column-level-lineage-from-sql/)
- [Hoop.dev: Column-Level Access for Postgres at Protocol Speed](https://hoop.dev/blog/column-level-access-for-postgres-at-protocol-speed)
- [OpenMetadata High Level Design](https://docs.open-metadata.org/latest/main-concepts/high-level-design)
- [HashiCorp go-plugin](https://github.com/hashicorp/go-plugin)
- [Neo4j vs PostgreSQL for Graph Data](https://medium.com/self-study-notes/exploring-graph-database-capabilities-neo4j-vs-postgresql-105c9e85bb5d)
- [Tamper-Evident Logging: Efficient Data Structures](https://static.usenix.org/event/sec09/tech/full_papers/crosby.pdf)
