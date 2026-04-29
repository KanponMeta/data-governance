# Feature Landscape

**Domain:** Data governance + orchestration platform (combined)
**Researched:** 2026-04-29
**Confidence:** HIGH (verified against Dagster docs, DataHub docs, OpenMetadata docs, Collibra, Apache Atlas)

---

## Platform Baseline Survey

Before categorizing, here is what each surveyed platform covers:

### Dagster (orchestration baseline)

Strengths: asset-centric model, rich UI (Dagit), scheduling/sensors/partitioning, materialization metadata, run history, software-defined assets, op-level retry, backfill, test harness. Column-level lineage exists but is: (a) manually declared via `TableColumnLineage` in `MaterializeResult` — not inferred from SQL, (b) visualization only available in Dagster+ (closed source). No governance workflows. No access control. No approval/certification. No compliance audit trail. No column-level security.

### DataHub (metadata + lineage)

Strengths: SQL-parser-based automatic column-level lineage (97-99% accuracy using SQLGlot), broad connector coverage (Snowflake, BigQuery, Redshift, Databricks, dbt, Looker, Tableau), lineage graph UI, business glossary, domains, data products, compliance forms and certification workflows, ML-based anomaly detection, AI-generated documentation. Weakness: Not an orchestrator — it observes and catalogs but does not execute pipelines.

### OpenMetadata (open-source governance)

Strengths: Column-level lineage, fine-grained RBAC, classification/tagging for PII policy enforcement, glossary approval workflow (Draft → Approved/Rejected), data contracts (Draft → Review → Active → Deprecated → Retired), asset certification backed by tag_usage table, data quality with no-code profiling, collaboration (tasks, conversations on assets), business glossary, domains, data products. Weakness: Not an orchestrator.

### Apache Atlas (data governance + metadata)

Strengths: Type-and-entity metadata model (highly extensible), automatic classification propagation (tag PII on source, descendants inherit), column-level lineage across Hadoop-native sources, Apache Ranger integration for policy enforcement, hierarchical business glossary. Weakness: Hadoop-era design — UI is dated, not Kubernetes-native, slow-moving project (2.4.0 broke a two-year release gap), limited modern connector support, no orchestration.

### Collibra (enterprise commercial)

Strengths: Full governance lifecycle — configurable approval workflows with Slack/Teams integration, business stewardship modules, business glossary as organizational backbone, data catalog with searchable inventory, automated metadata enrichment, audit trail for all decisions, AI-generated asset descriptions. Weakness: Expensive, closed-source, no orchestration, complex to deploy/operate.

---

## Table Stakes

Features users expect from any serious entry in this space. Missing = product feels incomplete and users go elsewhere.

### Orchestration (from Dagster benchmark)

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| Software-defined assets in code | Industry standard since Dagster popularized it; task-centric (Airflow-style) is now considered legacy for data work | Med | Core mental model of the platform |
| Explicit upstream dependency graph | Users need DAG execution order; without this it's a cron scheduler, not an orchestrator | Med | Dependency resolution is the orchestrator's core job |
| Cron + event-trigger scheduling | Every orchestrator has this; missing = cannot run production pipelines | Med | Sensors (file-landed, upstream-materialized) are also expected |
| Configurable retry with backoff | Transient failures are universal; no retry = manual babysitting | Low | Per-asset or per-run configurability required |
| Time-based and categorical partitioning | Standard for daily/hourly pipelines and multi-region/multi-tenant pipelines | High | Backfill of partitions is equally expected |
| Run history and execution logs | Users must be able to debug; no logs = unusable in production | Low | Per-run, per-asset log streaming is baseline |
| Asset materialization status (fresh/stale) | Dagster established this as table stakes; knowing what's stale drives re-runs | Med | Freshness indicators per asset |
| Configurable alerting on failure | PagerDuty/Slack/email on failure is universally expected | Low | Webhook or plugin interface is sufficient |

### Metadata & Catalog (from DataHub/OpenMetadata benchmark)

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| Auto-capture schema on materialization | Users should not manually register schemas; auto-discovery is the baseline | Med | Infer from Go struct tags or query result schemas |
| Asset/table/column descriptions + tags | Without annotation the catalog is useless for discovery | Low | Free-text + structured tags |
| Full-text search across catalog | Discovery is the primary analyst workflow; no search = no adoption | Med | Minimum: name, description, tag, owner search |
| Asset ownership assignment | Accountability requires owners; required for governance workflows | Low | Per-asset owner(s), team assignment |
| Asset-level lineage DAG (table/asset level) | Every governance platform has this; missing = no trust in data provenance | Med | Interactive DAG in UI is expected |
| Schema evolution tracking | Schema drift is a top source of data incidents; users need to see diffs | Med | Version-to-version schema diff |
| Business glossary | Standard on every governance platform; maps business terms to technical assets | Med | Terms linked to columns/assets |

### Data Quality (from OpenMetadata/DataHub benchmark)

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| Built-in null/range/uniqueness checks | Baseline quality checks are the entry point for data quality users | Low | Declarative rule definition in Go |
| Quality evaluation on materialization | Quality checks triggered automatically; not manual runs = adoption blocker | Med | Hooks into the execution lifecycle |
| Quality history and trend per asset | Users need to see degradation over time, not just pass/fail today | Med | Time-series storage of check results |
| Failure alerts | Quality failures are high-priority incidents; silence = lost trust | Low | Re-uses alerting infrastructure |

### Access Control (from Collibra/OpenMetadata/Unity Catalog benchmark)

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| Role-based access control (RBAC) | Every enterprise system requires RBAC at minimum | Med | Roles → permissions model |
| Immutable audit log | Required for SOC2 and GDPR; no audit = failed compliance | Med | Append-only, tamper-evident event store |
| User authentication (SSO/OIDC) | Enterprise deployments require SSO; local-user-only = rejected by enterprise IT | Med | OIDC integration; local users as fallback |

---

## Differentiators

Features that set this platform apart from existing tools. Not universally expected today, but highly valued — and where this project's stated advantage lives.

### Field-Level Lineage (Primary Differentiator)

| Feature | Value Proposition | Complexity | Notes |
|---------|-------------------|------------|-------|
| Automatic SQL-inferred column lineage | DataHub does this via SQLGlot; Dagster requires manual declaration. A Go platform that auto-infers from transformation SQL is genuinely differentiated for the orchestration space | High | Requires SQL parser integration (sqlparser-go or pg_query_go); or explicit API for Go-native transforms where SQL isn't the transform |
| Explicit column lineage API for Go transforms | For non-SQL transforms (pure Go code), users declare which output columns derive from which input columns — compiler-checked, not string-based | Med | `ColumnLineage{OutputCol: "revenue", InputCols: []ColRef{{Asset: "orders", Col: "amount"}}}` style API |
| Column lineage visualization in open-source UI | Dagster hides this behind Dagster+ (paid). An open-source platform with full column lineage UI in the free tier is a direct competitive advantage against Dagster | Med | Interactive graph with column-to-column edge rendering |
| Field-level impact analysis | "If I change column X in asset Y, what breaks downstream?" — requires traversing the column lineage graph | High | Graph traversal query over stored lineage edges |

### Governance Workflows (Primary Differentiator)

| Feature | Value Proposition | Complexity | Notes |
|---------|-------------------|------------|-------|
| Asset publication approval workflow | Collibra and OpenMetadata have this for glossary terms; no open-source orchestrator has it for pipeline assets. Governance teams demand gated publication | Med | States: Draft → Pending Review → Approved → Published / Rejected; configurable reviewers per asset |
| Inline review comments | Reviewers leave structured comments on specific fields or the asset as a whole before approve/reject | Low | Attached to approval request entity |
| Notification dispatch on workflow events | Notifications to reviewers on submission; to submitter on decision. Collibra integrates with Slack/Teams — critical for workflow completion rates | Low | Webhook + email; Slack plugin as extension |
| Rejection with required remediation | Rejected assets return to Draft with mandatory comments — forces feedback loop rather than silent rejection | Low | Workflow state constraint |

### Column-Level Security Enforcement (Primary Differentiator)

| Feature | Value Proposition | Complexity | Notes |
|---------|-------------------|------------|-------|
| Column masking policies | Unity Catalog, BigQuery, and Apache Ranger all do this; no open-source governance+orchestration combo does. Enforcing at query time by role is the key ask from governance teams | High | Requires a query proxy layer or catalog-enforced view generation; masking functions applied per-role per-column |
| Policy-as-code column ACL definition | Governance teams want to version-control access policies; YAML/Go struct definitions committed to Git | Med | `ColumnPolicy{Column: "ssn", Roles: []string{"pii-analyst"}, Mask: MaskType_Hash}` |
| PII tag-to-policy propagation | Tag a column "PII" → policy automatically applied. Inspired by Apache Atlas classification propagation | Med | Requires tag→policy rule engine |
| Downstream column policy inheritance | If column A in asset X is masked, derived column B in asset Y (which lineage shows came from A) should inherit the restriction unless explicitly overridden | High | Requires field-level lineage as a prerequisite |

### Compliance & Audit Trail (Differentiator vs. Orchestrators; Table Stakes vs. Enterprise Governance)

| Feature | Why Differentiating | Complexity | Notes |
|---------|---------------------|------------|-------|
| Tamper-evident append-only audit log | Standard governance platforms have this; orchestrators (including Dagster) do not. Combining orchestration + audit in one system eliminates a whole integration | Med | Write-once event store; cryptographic chaining optional but valuable for SOC2 |
| GDPR/SOC2 compliance export | Pre-formatted compliance reports that governance teams can deliver to auditors without manual extraction | Med | Parameterized queries over audit log → structured report |
| Data retention / TTL policies on assets | GDPR right-to-erasure requires this; no orchestrator has it | Med | Policy attached to asset: `RetentionPolicy{TTL: 365 * 24 * time.Hour, Action: Delete}` |

### Go-Native Developer Experience (Differentiator vs. Python Ecosystem)

| Feature | Why Differentiating | Complexity | Notes |
|---------|---------------------|------------|-------|
| Type-safe asset definitions in Go | Python's duck typing in Dagster leads to runtime errors; Go structs give compile-time guarantees on asset interface, lineage declarations, and quality rule definitions | Med | The asset SDK is the platform's public API — stability from day one is critical |
| Single binary deployment | Go compiles to a single binary; no Python virtualenv, no pip install, no JVM. Operations teams strongly prefer this | Low | Key adoption driver for self-hosted; Docker image is ~20MB not ~500MB |
| Connector interface as stable public API | Extensibility via Go interface allows third-party connectors without platform changes — drives ecosystem | Med | `Connector` interface defined as v1 public API before first release |

### Data Asset Freshness SLA

| Feature | Why Differentiating | Complexity | Notes |
|---------|---------------------|------------|-------|
| Per-asset freshness SLA declaration | "This asset must be materialized within 6 hours of its upstream" — violation triggers alert and governance flag | Med | SLA attached to asset definition; evaluated by scheduler |
| SLA breach surfaced in governance workflow | SLA breach = automatic review trigger; governance team is notified, not just ops team | Med | Bridge between orchestration and governance layers |

---

## Anti-Features

Features to deliberately NOT build in v1. These are either out of scope, complexity traps, or dilute the core value proposition.

| Anti-Feature | Why Avoid | What to Do Instead |
|--------------|-----------|-------------------|
| Python SDK / Python runtime dependency | Python dependency in core would require users to have Python installed to use a Go platform. Defeats the single-binary deployment goal and Go-native DX | Defer Python bindings post-stable API; Go SDK only for v1 |
| Row-level security | Adds significant complexity (query proxy required, interaction with every connector), highly database-specific, and is a separate product surface. Column-level security is the stated scope and is differentiated enough | Document as future extension; design column ACL system so row-level can be added without re-architecture |
| Built-in Spark / dbt execution | The platform orchestrates; it does not execute Spark jobs or run dbt models itself. Adding an embedded execution engine blurs the value proposition and massively increases surface area | Provide first-class Dagster-style op wrappers that delegate to external dbt/Spark processes; capture lineage from those runs |
| Multi-tenant SaaS hosting | SaaS requires per-tenant isolation, billing, onboarding flows, and ops overhead that pulls engineering away from core governance features. Open-source adoption comes first | Self-hosted only for v1; commercial SaaS is a v2+ business decision |
| AI-generated metadata / LLM descriptions | DataHub and Collibra already do this; it's becoming table stakes but is not a differentiator. It also requires significant infrastructure (embedding model, vector store) | Defer to v2; design metadata model so AI enrichment can be layered on without schema changes |
| Built-in BI / dashboards / charts | Tableau/Looker integration is a common catalog feature but adds enormous UI scope. Analysts use BI tools they already have | Track BI assets as catalog entries and capture lineage from BI tools (Looker, Tableau extractors); don't build BI tooling |
| Managed connector marketplace | A connector registry with ratings, versions, and downloads is a significant product. It creates community overhead before the platform has users | Ship 6-8 first-party connectors; design the public `Connector` interface well; let community build and self-distribute connectors |
| Workflow automation / data contracts enforcement at query time | Data contracts that block queries at the database level require a query proxy, which is a major infrastructure component (similar to what Privacera builds). This is not orchestration | Enforce governance decisions at materialization time (pipeline-level), not at query time; column masking is the limit of query-time enforcement for v1 |

---

## Feature Dependencies

```
Column lineage visualization ← Field-level lineage capture (SQL parser or explicit API)
Field-level impact analysis ← Field-level lineage (complete graph required)
Downstream column policy inheritance ← Field-level lineage + column masking policy engine
SLA breach → governance workflow ← Asset publication workflow + freshness SLA declaration
Approval workflow ← Asset ownership (owner is notified/assigned as reviewer)
Column masking enforcement ← RBAC (roles required before policies can reference them)
GDPR compliance export ← Immutable audit log (data source for the report)
Data retention TTL enforcement ← Asset ownership + audit log (who authorized deletion)
Schema drift detection ← Schema evolution tracking (diff stored versions)
PII tag → policy propagation ← Classification/tagging system + column masking policy engine
```

### Hard Prerequisites (must build before dependent feature)

1. Asset + schema metadata model → everything else depends on it
2. RBAC roles model → column masking, approval workflow, audit log attribution
3. Immutable audit log → compliance export, tamper-evident governance
4. Field-level lineage graph → impact analysis, downstream policy inheritance, field lineage UI
5. Asset publication states (Draft/Published) → approval workflow (states are workflow states)

---

## MVP Recommendation

The minimum viable product that demonstrates the core differentiators:

**Priority 1 (without these it's just another orchestrator):**
1. Software-defined assets in Go — the execution foundation
2. Dependency graph execution + scheduling — operational baseline
3. Auto-captured schema metadata on materialization — catalog backbone
4. Asset-level lineage DAG — lineage baseline
5. RBAC + immutable audit log — governance foundation

**Priority 2 (the actual differentiators that justify building this over Dagster):**
6. Field-level lineage (explicit Go API first, SQL parser second) — primary differentiator
7. Asset publication workflow (Draft → Pending → Approved/Published) — governance differentiator
8. Column-level masking policies enforced at materialization — access control differentiator

**Priority 3 (valuable but not MVP-blocking):**
9. Data quality rules + evaluation on materialization
10. GDPR/SOC2 compliance audit export
11. Downstream column policy inheritance via lineage

**Defer:**
- SQL-inferred automatic column lineage (complex; start with explicit API, add SQL parser later)
- PII tag → policy propagation (requires policy rule engine; design hooks but defer)
- Freshness SLA → governance bridge (nice-to-have; core SLA alerting is enough for MVP)

---

## Sources

- Dagster column lineage docs: https://docs.dagster.io/guides/build/assets/metadata-and-tags/column-level-lineage
- DataHub SQL parser / column lineage: https://docs.datahub.com/docs/lineage/sql_parsing and https://datahub.com/blog/extracting-column-level-lineage-from-sql/
- DataHub lineage API: https://docs.datahub.com/docs/api/tutorials/lineage
- OpenMetadata governance: https://docs.open-metadata.org/latest/how-to-guides/data-governance
- OpenMetadata glossary approval workflow: https://docs.open-metadata.org/latest/how-to-guides/data-governance/glossary/approval
- Collibra governance: https://www.collibra.com/products/data-governance
- Apache Atlas: https://atlas.apache.org/
- Column masking patterns (Unity Catalog): https://docs.databricks.com/aws/en/data-governance/unity-catalog/filters-and-masks/
- BigQuery column-level security: https://cloud.google.com/bigquery/docs/column-level-security
- DataHub 2026 features overview: https://atlan.com/know/data-catalog/datahub/column-level-lineage/
- Dagster data governance gaps: https://www.secoda.co/learn/dagster-data-governance
- Open source data governance landscape 2025: https://atlan.com/open-source-data-governance-tools/
