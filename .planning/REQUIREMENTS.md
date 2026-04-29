# Requirements: Data Governance Platform

**Defined:** 2026-04-29
**Core Value:** A data practitioner can define, run, and govern data assets in code — and every downstream consumer can trust what they're working with, trace where it came from to the field level, and know who is allowed to see it.

## v1 Requirements

### Foundation (CORE)

- [ ] **CORE-01**: Platform stores all state in PostgreSQL and exposes a storage abstraction for testing
- [ ] **CORE-02**: Platform manages schema migrations via Atlas (ent-driven, versioned, no dirty-state corruption)
- [ ] **CORE-03**: Platform emits a structured event log (run started, step completed, quality passed, etc.) append-only to the database
- [ ] **CORE-04**: Platform can be started and run as a single binary (Docker Compose for dev, Kubernetes for prod)
- [ ] **CORE-05**: Platform exposes a REST + gRPC API for CLI and SDK integration

### Authentication (AUTH)

- [ ] **AUTH-01**: User can create an account with email and password
- [ ] **AUTH-02**: Admin can invite users via email
- [ ] **AUTH-03**: User can log in and receive a JWT session token
- [ ] **AUTH-04**: User session expires and requires re-authentication after configurable TTL

### Orchestration (ORCH)

- [ ] **ORCH-01**: Data engineer can define a data asset in Go code with an explicit list of upstream asset dependencies
- [ ] **ORCH-02**: Data engineer can implement a `Materialize` function on an asset that the platform calls to produce the asset
- [ ] **ORCH-03**: Platform resolves the full asset dependency DAG and executes assets in topological order
- [ ] **ORCH-04**: Platform retries failed asset materializations with configurable backoff (max retries, delay, jitter)
- [ ] **ORCH-05**: Data engineer can attach a cron schedule to an asset for automatic periodic materialization
- [ ] **ORCH-06**: Data engineer can define an event sensor that triggers asset materialization when an external condition is met
- [ ] **ORCH-07**: Data engineer can define time-based partitioned assets (daily, weekly, monthly)
- [ ] **ORCH-08**: Data engineer can define categorical partitioned assets (e.g., per-region, per-customer)
- [ ] **ORCH-09**: Platform enforces a concurrency limit via a unified token pool (prevents duplicate and runaway runs)
- [ ] **ORCH-10**: Data engineer can trigger ad-hoc asset materialization via CLI or UI

### Lineage (LINE)

- [ ] **LINE-01**: Platform automatically captures asset-to-asset lineage edges from asset dependency declarations
- [ ] **LINE-02**: Data engineer can declare column-level lineage in Go code (output column X derived from input column Y on asset Z)
- [ ] **LINE-03**: Platform stores lineage as an adjacency list in PostgreSQL traversable via recursive CTE
- [ ] **LINE-04**: User can view the full asset lineage graph as an interactive DAG in the UI
- [ ] **LINE-05**: User can drill into a node in the lineage DAG to see column-level lineage for that asset
- [ ] **LINE-06**: User can run an impact analysis: given a field, see all downstream assets and columns that depend on it

### Data Quality (QUAL)

- [ ] **QUAL-01**: Data engineer can define quality rules on an asset in Go code (null rate, range bounds, custom SQL assertion)
- [ ] **QUAL-02**: Platform evaluates all quality rules for an asset after each successful materialization
- [ ] **QUAL-03**: Platform marks an asset materialization as quality-failed if any rule fails, and surfaces the failure in the UI
- [ ] **QUAL-04**: Data engineer can configure SLA thresholds per asset (e.g., must materialize within N hours of schedule)
- [ ] **QUAL-05**: Platform sends an alert (webhook/email) when a quality rule fails or an SLA is breached
- [ ] **QUAL-06**: User can view per-asset quality history as a trend chart in the UI

### Metadata (META)

- [ ] **META-01**: Platform automatically captures table/column schema metadata after each asset materialization
- [ ] **META-02**: Platform diffs schema between versions and records any breaking changes (column removal, type change)
- [ ] **META-03**: User can add a description, owner, and tags to any asset, table, or column via UI or API
- [ ] **META-04**: User can search the data catalog by asset name, column name, tag, owner, or description
- [ ] **META-05**: Platform surfaces a schema evolution timeline showing when each column was added, changed, or removed

### Access Control (RBAC)

- [ ] **RBAC-01**: Admin can define named roles (e.g., data-engineer, analyst, governance-team)
- [ ] **RBAC-02**: Admin can assign users to one or more roles
- [ ] **RBAC-03**: Admin can define a column-level access policy specifying which roles can access which columns on which assets
- [ ] **RBAC-04**: Platform syncs column masking policies to Snowflake dynamic data masking and BigQuery column-level security APIs
- [ ] **RBAC-05**: Platform enforces column masking at pipeline materialization time for non-warehouse connectors (masked values written to sink)
- [ ] **RBAC-06**: All data access events, policy changes, and user actions are written to an append-only, hash-chain audit log

### Governance Workflows (GOV)

- [ ] **GOV-01**: Data engineer can submit an asset for governance review, transitioning it from Draft to In Review state
- [ ] **GOV-02**: Platform assigns the review to configured governance-team reviewers and notifies them
- [ ] **GOV-03**: Reviewer can approve or reject an asset with a required comment; approval transitions asset to Active, rejection returns it to Draft
- [ ] **GOV-04**: Platform notifies the submitter of the review decision with the reviewer's comment
- [ ] **GOV-05**: All approval decisions, reviewer identities, and timestamps are recorded in the audit log
- [ ] **GOV-06**: Admin can export the full audit log as a structured file (JSON/CSV) for GDPR/SOC2 compliance reporting
- [ ] **GOV-07**: Admin can configure data retention policies (TTL) for asset materializations and audit log records

### Connectors (CONN)

- [ ] **CONN-01**: Platform provides a PostgreSQL connector (read/write assets, auto schema capture, quality assertion execution)
- [ ] **CONN-02**: Platform provides a MySQL connector (read/write assets, auto schema capture)
- [ ] **CONN-03**: Platform provides a BigQuery connector (read/write assets, schema capture, masking policy sync)
- [ ] **CONN-04**: Platform provides a Snowflake connector (read/write assets, schema capture, dynamic data masking sync)
- [ ] **CONN-05**: Platform provides an S3 connector (read/write assets as Parquet/CSV/JSON)
- [ ] **CONN-06**: Platform provides a GCS connector (read/write assets as Parquet/CSV/JSON)
- [ ] **CONN-07**: Platform provides an HDFS connector (read/write assets)
- [ ] **CONN-08**: Platform exposes a stable, versioned Go connector interface (via hashicorp/go-plugin) that third parties can implement to add new connectors

### Web UI (UI)

- [ ] **UI-01**: User can view all assets, their current state, last materialization time, and quality status in a dashboard
- [ ] **UI-02**: User can view run history and execution logs per asset
- [ ] **UI-03**: User can browse the data catalog with search, filter by tag/owner, and view asset/column metadata
- [ ] **UI-04**: User can view the lineage DAG with interactive navigation and column-level drill-down
- [ ] **UI-05**: User can view quality score trends and active alerts per asset
- [ ] **UI-06**: Governance team can view and action pending approval requests from a governance inbox
- [ ] **UI-07**: Admin can manage users, roles, and column-level access policies from the UI

## v2 Requirements

### SDK Ecosystem

- **SDK-01**: Python SDK — define assets in Python and register them with the Go platform
- **SDK-02**: dbt integration — treat dbt models as first-class assets with auto lineage from dbt manifest

### Advanced Lineage

- **ALINE-01**: SQL-inferred field-level lineage — automatically extract column lineage from SQL queries via AST parsing (no user declaration required)
- **ALINE-02**: OpenLineage event ingest — accept OpenLineage events from external tools (Spark, Airflow) to extend the lineage graph

### Advanced Governance

- **AGOV-01**: Row-level security policies — restrict which roles can see which rows based on row-level attribute conditions
- **AGOV-02**: Data classification tagging — auto-classify columns as PII, PHI, SENSITIVE using pattern matching

### Platform

- **PLAT-01**: Multi-worker distributed execution — run asset materializations on separate worker machines
- **PLAT-02**: SSO / OAuth2 integration (OIDC) — allow users to log in via corporate identity provider

## Out of Scope

| Feature | Reason |
|---------|--------|
| Python runtime in core | Go-only constraint; Python SDK is v2, optional |
| Query-time column masking proxy | A proxy is bypassed by direct warehouse connections; warehouse-native sync is safer and correct |
| Built-in compute (Spark jobs, dbt runs) | Platform orchestrates and tracks; execution is delegated to external systems |
| Multi-tenant SaaS hosting | Open-source self-hosted only for v1 |
| Row-level security | Column-level is the scope; row-level complexity is disproportionate for v1 |
| Cell-level access control | Below column-level; impractical to enforce at platform layer |
| Realtime streaming assets (Kafka, Flink) | Batch-centric model for v1; streaming adds significant complexity |
| Mobile app | Web UI is sufficient; no mobile-specific use cases identified |

## Traceability

| Requirement | Phase | Status |
|-------------|-------|--------|
| CORE-01 | Phase 1 | Pending |
| CORE-02 | Phase 1 | Pending |
| CORE-03 | Phase 1 | Pending |
| CORE-04 | Phase 1 | Pending |
| CORE-05 | Phase 1 | Pending |
| AUTH-01 | Phase 1 | Pending |
| AUTH-02 | Phase 1 | Pending |
| AUTH-03 | Phase 1 | Pending |
| AUTH-04 | Phase 1 | Pending |
| ORCH-01 | Phase 2 | Pending |
| ORCH-02 | Phase 2 | Pending |
| ORCH-03 | Phase 2 | Pending |
| ORCH-04 | Phase 2 | Pending |
| ORCH-05 | Phase 3 | Pending |
| ORCH-06 | Phase 3 | Pending |
| ORCH-07 | Phase 3 | Pending |
| ORCH-08 | Phase 3 | Pending |
| ORCH-09 | Phase 2 | Pending |
| ORCH-10 | Phase 2 | Pending |
| LINE-01 | Phase 4 | Pending |
| LINE-02 | Phase 4 | Pending |
| LINE-03 | Phase 4 | Pending |
| LINE-04 | Phase 6 | Pending |
| LINE-05 | Phase 6 | Pending |
| LINE-06 | Phase 4 | Pending |
| QUAL-01 | Phase 5 | Pending |
| QUAL-02 | Phase 5 | Pending |
| QUAL-03 | Phase 5 | Pending |
| QUAL-04 | Phase 5 | Pending |
| QUAL-05 | Phase 5 | Pending |
| QUAL-06 | Phase 6 | Pending |
| META-01 | Phase 4 | Pending |
| META-02 | Phase 4 | Pending |
| META-03 | Phase 4 | Pending |
| META-04 | Phase 6 | Pending |
| META-05 | Phase 4 | Pending |
| RBAC-01 | Phase 5 | Pending |
| RBAC-02 | Phase 5 | Pending |
| RBAC-03 | Phase 5 | Pending |
| RBAC-04 | Phase 5 | Pending |
| RBAC-05 | Phase 5 | Pending |
| RBAC-06 | Phase 5 | Pending |
| GOV-01 | Phase 5 | Pending |
| GOV-02 | Phase 5 | Pending |
| GOV-03 | Phase 5 | Pending |
| GOV-04 | Phase 5 | Pending |
| GOV-05 | Phase 5 | Pending |
| GOV-06 | Phase 5 | Pending |
| GOV-07 | Phase 5 | Pending |
| CONN-01 | Phase 2 | Pending |
| CONN-02 | Phase 2 | Pending |
| CONN-03 | Phase 2 | Pending |
| CONN-04 | Phase 2 | Pending |
| CONN-05 | Phase 2 | Pending |
| CONN-06 | Phase 2 | Pending |
| CONN-07 | Phase 2 | Pending |
| CONN-08 | Phase 1 | Pending |
| UI-01 | Phase 6 | Pending |
| UI-02 | Phase 6 | Pending |
| UI-03 | Phase 6 | Pending |
| UI-04 | Phase 6 | Pending |
| UI-05 | Phase 6 | Pending |
| UI-06 | Phase 6 | Pending |
| UI-07 | Phase 6 | Pending |

**Coverage:**
- v1 requirements: 57 total
- Mapped to phases: 57
- Unmapped: 0 ✓

---
*Requirements defined: 2026-04-29*
*Last updated: 2026-04-29 after initial definition*
