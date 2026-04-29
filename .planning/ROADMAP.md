# Roadmap: Data Governance Platform

## Overview

Six phases take the platform from a bare Go module to a fully operational data governance and orchestration system. Phase 1 lays the storage and authentication skeleton that every later component depends on. Phase 2 delivers the core execution engine — the platform's reason to exist — including all first-party connectors. Phase 3 adds scheduling, sensors, and partitions to enable production use. Phase 4 captures field-level lineage and schema evolution, the primary technical differentiator over Dagster. Phase 5 builds the governance engine: RBAC, column masking, approval workflows, tamper-evident audit, and data quality. Phase 6 assembles the web UI and completes the API surface once all backend models are stable.

## Phases

**Phase Numbering:**
- Integer phases (1, 2, 3): Planned milestone work
- Decimal phases (2.1, 2.2): Urgent insertions (marked with INSERTED)

Decimal phases appear between their surrounding integers in numeric order.

- [ ] **Phase 1: Foundation** - Storage, migrations, event log, auth, connector interface, single binary + API stubs
- [ ] **Phase 2: Execution Engine** - Asset definitions, DAG execution, retry, concurrency token pool, all first-party connectors
- [ ] **Phase 3: Scheduling, Sensors, and Partitions** - Cron daemon, event sensors, time/categorical partitions, backfill
- [ ] **Phase 4: Lineage and Schema** - Asset + field-level lineage, schema auto-discovery, schema evolution, impact analysis
- [ ] **Phase 5: Governance Engine** - RBAC, column masking, approval workflows, hash-chain audit log, data quality rules
- [ ] **Phase 6: Web UI and API** - Complete REST/gRPC API, React SPA, asset dashboard, lineage DAG, governance inbox

## Phase Details

### Phase 1: Foundation
**Goal**: The platform runs as a single binary with a healthy PostgreSQL-backed storage layer, versioned migrations, an append-only event log, user authentication, and a stable versioned connector interface — all downstream phases build on these foundations
**Depends on**: Nothing (first phase)
**Requirements**: CORE-01, CORE-02, CORE-03, CORE-04, CORE-05, AUTH-01, AUTH-02, AUTH-03, AUTH-04, CONN-08
**Success Criteria** (what must be TRUE):
  1. The platform starts with `docker compose up` and passes a health check endpoint
  2. A user can register an account, receive a JWT, and be rejected after session TTL expires
  3. An admin can invite a new user by email and that user can complete registration
  4. The event log records a structured entry for every platform lifecycle event, and entries are never modified after write
  5. A third-party author can implement the Go connector interface in a separate module and register it with the platform without modifying platform source
**Plans**: TBD
**UI hint**: no

### Phase 2: Execution Engine
**Goal**: Data engineers can define assets in Go code with explicit upstream dependencies, trigger materializations, and rely on the platform to execute them in dependency order with retry, while all seven first-party connectors read and write assets reliably
**Depends on**: Phase 1
**Requirements**: ORCH-01, ORCH-02, ORCH-03, ORCH-04, ORCH-09, ORCH-10, CONN-01, CONN-02, CONN-03, CONN-04, CONN-05, CONN-06, CONN-07
**Success Criteria** (what must be TRUE):
  1. A data engineer can define an asset with upstream dependencies in Go and trigger a materialization that executes all upstreams first in correct topological order
  2. A failed asset materialization retries up to a configured maximum with exponential backoff, and the retry count and timestamps are visible in the event log
  3. Fifty concurrent goroutines attempting to claim the same queued run result in exactly one execution — concurrent duplicate runs cannot occur
  4. An asset can be materialized on-demand via CLI command, and the run completes successfully end-to-end using the PostgreSQL connector against a local database
  5. Each of the seven first-party connectors (PostgreSQL, MySQL, BigQuery, Snowflake, S3, GCS, HDFS) can read and write an asset without error in integration tests
**Plans**: TBD
**UI hint**: no

### Phase 3: Scheduling, Sensors, and Partitions
**Goal**: Assets materialize automatically on schedule or in response to external events, partitioned assets execute per-partition with backfill support, and the scheduler daemon survives process restart without losing scheduled state
**Depends on**: Phase 2
**Requirements**: ORCH-05, ORCH-06, ORCH-07, ORCH-08
**Success Criteria** (what must be TRUE):
  1. An asset with a cron expression attached materializes automatically at the next scheduled time after the daemon starts, with no manual trigger
  2. An asset with an event sensor materializes within the sensor polling interval when the configured external condition becomes true
  3. A time-based partitioned asset can be backfilled for a historical date range, and each partition executes as a separate run with its own event log entries
  4. A categorical partitioned asset runs independently per-category without one category's failure blocking another
**Plans**: TBD
**UI hint**: no

### Phase 4: Lineage and Schema
**Goal**: Every asset materialization automatically records the asset dependency graph, captures the output schema, and surfaces field-level lineage and schema evolution so engineers and governance teams can trace the full provenance of any column
**Depends on**: Phase 3
**Requirements**: LINE-01, LINE-02, LINE-03, LINE-06, META-01, META-02, META-03, META-05
**Success Criteria** (what must be TRUE):
  1. After an asset materializes, its upstream asset edges are automatically recorded and traversable via the lineage API without any manual registration step
  2. A data engineer can declare that output column A derives from input column B on upstream asset Z, and this declaration is queryable and versioned against the asset's code hash
  3. Given any column on any asset, the impact analysis API returns all downstream assets and columns that depend on it, traversing the full lineage graph
  4. The platform captures table and column schema on every materialization, diffs it against the previous version, and records breaking changes (column removal, type change) in the schema evolution timeline
  5. A user can add a description, owner, and tags to an asset, table, or column via the API and retrieve them in a subsequent query
**Plans**: TBD
**UI hint**: no

### Phase 5: Governance Engine
**Goal**: Admins can enforce column-level access policies that sync to Snowflake and BigQuery, data engineers submit assets for approval through a tracked workflow, all governance actions are recorded in a tamper-evident hash-chain audit log, and quality rules run automatically on every materialization
**Depends on**: Phase 4
**Requirements**: RBAC-01, RBAC-02, RBAC-03, RBAC-04, RBAC-05, RBAC-06, GOV-01, GOV-02, GOV-03, GOV-04, GOV-05, GOV-06, GOV-07, QUAL-01, QUAL-02, QUAL-03, QUAL-04, QUAL-05
**Success Criteria** (what must be TRUE):
  1. An admin can define a role, assign users to it, and set a column-level access policy; the platform syncs that policy to Snowflake dynamic data masking and the BigQuery column-level security API without manual warehouse configuration
  2. A data engineer submits an asset for governance review; a reviewer approves or rejects it with a required comment; the submitter is notified; the asset transitions to Active on approval and returns to Draft on rejection
  3. Every governance action (policy change, approval decision, user assignment) produces an audit log entry linked by hash to the previous entry; no existing entry can be modified or deleted by the application database user
  4. An admin can export the full audit log as JSON or CSV and the export contains every entry including reviewer identity and timestamp
  5. A data engineer defines a null-rate quality rule on an asset; after materialization the platform evaluates the rule, marks the run quality-failed if it breaches the threshold, and sends a webhook or email alert
**Plans**: TBD
**UI hint**: no

### Phase 6: Web UI and API
**Goal**: Every platform capability is accessible through a complete REST and gRPC API with OpenAPI documentation, and through a React web UI with an asset dashboard, interactive lineage DAG, quality trends, governance inbox, catalog search, and admin panel
**Depends on**: Phase 5
**Requirements**: UI-01, UI-02, UI-03, UI-04, UI-05, UI-06, UI-07, LINE-04, LINE-05, META-04, QUAL-06
**Success Criteria** (what must be TRUE):
  1. A user can open the asset dashboard and see every asset with its current state, last materialization time, and quality status without making any manual API calls
  2. A user can navigate to an asset in the lineage DAG, expand it to see column-level lineage, and drill into any connected node — all through the interactive UI
  3. A user can search the data catalog by asset name, column name, tag, owner, or description and receive relevant results
  4. A governance team member can view all pending approval requests in the governance inbox and take approve or reject actions without leaving the UI
  5. An admin can manage users, roles, and column-level access policies entirely from the admin panel
**Plans**: TBD
**UI hint**: yes

## Progress

**Execution Order:**
Phases execute in numeric order: 1 → 2 → 3 → 4 → 5 → 6

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Foundation | 0/? | Not started | - |
| 2. Execution Engine | 0/? | Not started | - |
| 3. Scheduling, Sensors, and Partitions | 0/? | Not started | - |
| 4. Lineage and Schema | 0/? | Not started | - |
| 5. Governance Engine | 0/? | Not started | - |
| 6. Web UI and API | 0/? | Not started | - |
