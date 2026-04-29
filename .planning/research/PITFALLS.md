# Domain Pitfalls

**Domain:** Data Orchestration + Governance Platform (Go-native, Dagster-inspired)
**Researched:** 2026-04-29
**Confidence:** HIGH for execution engine and lineage pitfalls (verified against Dagster issues and multiple authoritative sources); MEDIUM for governance workflow pitfalls (good sources, fewer Go-specific precedents); HIGH for Go-specific pitfalls (well-documented language patterns)

---

## Critical Pitfalls

These mistakes cause rewrites, data loss, or adoption failure.

---

### Pitfall 1: Execution State Machine Without Atomic Transitions

**What goes wrong:** Run state transitions (QUEUED -> STARTING -> RUNNING -> COMPLETED/FAILED) are written as non-atomic multi-step database operations. Under concurrent scheduler ticks or crash-recovery, the same run can be double-started, producing duplicate materializations of the same asset partition.

**Why it happens:** Developers write `SELECT ... WHERE state = 'QUEUED'` followed by `UPDATE state = 'RUNNING'` as two separate statements. Between those two statements, a second scheduler worker can pick up the same run.

**Evidence from production:** Dagster issue #15155 documents this exact pattern — duplicate runs in backfills when using `DefaultRunLauncher` with `QueuedRunCoordinator` and `tag_concurrency_limits`. The bug survived multiple releases because it only manifests at 26+ partitions.

**Consequences:** Duplicate writes to production tables. Cost explosions on BigQuery/Snowflake (two full scans for same partition). Impossible to audit which materialization is "the one."

**Prevention:**
- Use `SELECT ... FOR UPDATE SKIP LOCKED` (PostgreSQL) or equivalent optimistic locking to claim a run atomically
- Assign each run a monotonic sequence number at creation, reject any launcher that attempts to start a run not in the expected state
- Model the state machine explicitly as an enum in the database with a `CHECK` constraint; never allow backwards transitions except via a dedicated `reset` operation
- Write a test that spawns 50 goroutines all trying to claim the same queued run simultaneously — only one should succeed

**Detection warning signs:**
- Run count in DB does not match expected partition count after backfill
- Duplicate asset materialization events in the audit log for the same partition key + run ID
- Unexpectedly high warehouse query costs after a backfill

**Phase that should address it:** Execution engine core (Phase 1 / earliest milestone). Fix before writing any higher-level scheduler logic.

---

### Pitfall 2: Concurrency Limit Logic Spread Across Multiple Layers

**What goes wrong:** Concurrency is enforced in more than one place — run coordinator, op executor, and resource manager — with no single source of truth. Limits interact unexpectedly. In Dagster, the interaction between run-tag concurrency limits and op-level concurrency limits causes backfills to hang permanently (issue #25743, confirmed in production as of Dagster 1.8.13+).

**Why it happens:** Each new concurrency feature is bolted onto the existing scheduler rather than going through a unified token/slot system. The first feature works. The second feature interacts with the first in unexpected ways.

**Consequences:** Backfill jobs become permanently stuck. Operators discover this by noticing that queued runs never start — there is no error, just silence. Recovery requires manual database intervention or configuration changes that break other limits.

**Prevention:**
- Design one centralized concurrency token system at the beginning. Every source of concurrency control (run-level, op-level, resource-level) draws from and returns to the same token pool.
- Implement the token system as a table with row-level locking. No in-memory counters — they do not survive crashes.
- Write an integration test that exercises all three concurrency dimensions simultaneously before declaring the scheduler stable.

**Detection warning signs:**
- Runs stay in QUEUED state indefinitely with no error
- Concurrency pool shows available slots but runs are not dequeued
- UI claims limits are applied but behavior does not match configured values

**Phase that should address it:** Execution engine concurrency design. Do not add op-level concurrency after run-level concurrency — design both in the same phase.

---

### Pitfall 3: Field-Level Lineage Captured at Declaration Time Only

**What goes wrong:** Field-level lineage is captured once when the asset is defined (static declaration) and never updated when the asset's transformation logic changes. The lineage graph becomes stale. A perfect lineage graph that is 3 months stale is worse than no graph because it creates false confidence.

**Why it happens:** Capturing lineage from SQL at runtime requires parsing SQL dialects (a hard problem). Teams take the shortcut of requiring users to declare lineage in code at definition time. Users stop updating declarations as logic evolves. There is no enforcement mechanism.

**Evidence from the ecosystem:** dbt column-level lineage discussion (#4458 in dbt-core) documents the SQL parsing challenge. Multiple lineage tools promote automation but fail silently on SELECT *, CTEs, stored procedures, and dialect-specific syntax (Oracle, Snowflake QUALIFY, BigQuery EXCEPT).

**Consequences:** Impact analysis (which downstream columns are affected if I change field X?) returns wrong answers. Governance decisions based on lineage are unsound. GDPR deletion requests that should propagate to derived fields are missed.

**Prevention:**
- Adopt a hybrid approach: declarative annotations at asset definition time (user specifies column mappings explicitly) PLUS optional SQL parsing as a supplementary signal, never as sole source of truth
- Implement a lineage version number per asset. When an asset is re-materialized with a different code hash, prompt (or require) lineage re-declaration. Do not silently keep old lineage.
- Use OpenLineage-compatible event schema so lineage events carry a `runId` and `eventTime` — consumers can detect staleness
- Treat `SELECT *` as a declaration of full column pass-through AND record it as "unknown expansion" that triggers a schema-refresh query on materialization

**Detection warning signs:**
- Asset code hash has changed but lineage graph shows no corresponding update
- Column names in lineage graph do not match current schema (stale column names)
- Users report impact analysis results that contradict reality

**Phase that should address it:** Lineage capture milestone. Explicitly decide the static vs. runtime tradeoff before writing any lineage storage.

---

### Pitfall 4: Graph Storage Chosen Too Late, Replaced Under Load

**What goes wrong:** Lineage is initially stored in a simple adjacency list in PostgreSQL (edges table with `(from_asset, from_field, to_asset, to_field, run_id)` rows). This works at 10K edges. At 1M+ edges — a realistic number for a data platform with hundreds of assets across years of runs — queries for "all ancestors of field X" become full graph traversals that time out.

**Why it happens:** Relational databases are the path of least resistance. The adjacency list model is easy to implement. Performance problems only manifest under real usage, usually 6-12 months after launch.

**Evidence from the ecosystem:** OpenMetadata deliberately avoids graph databases and uses PostgreSQL/Elasticsearch. They've shipped canvas-based rendering optimizations and backend filtering for 3000+ glossary terms — clear evidence that relational storage hits walls at scale. Neo4j supports native graph traversals but adds operational complexity.

**Consequences:** Either the lineage UI becomes unusable (timeouts) or engineering rewrites the storage layer mid-product, breaking the query API.

**Prevention:**
- Use PostgreSQL with recursive CTEs (`WITH RECURSIVE`) for lineage traversals from the start. PostgreSQL's recursive CTE is not as fast as a native graph DB but is vastly faster than application-level traversal in a loop.
- Add a materialized summary table for "direct upstream/downstream counts" per asset to support the UI listing without full traversal
- Add depth limits to all lineage traversal queries (default max depth: 10). Deep traversals without a limit are a denial-of-service vector.
- Benchmark at 500K edges before launch. If recursive CTE depth-10 queries exceed 200ms, plan a graph DB migration before going stable.
- Structure edge rows to be append-only, never updated. This enables future migration to a dedicated graph store without reconciling mutable state.

**Detection warning signs:**
- Lineage queries slow down proportionally as the edge count grows (O(n) instead of O(depth))
- UI lineage DAG rendering takes >2 seconds for assets with 20+ upstream dependencies
- `EXPLAIN ANALYZE` on lineage traversal shows sequential scans instead of index-only scans

**Phase that should address it:** Lineage storage design (early). Define the schema and run load tests before the lineage UI milestone.

---

### Pitfall 5: Audit Logs That Are Queryable but Not Tamper-Evident

**What goes wrong:** The audit log is implemented as a database table with `INSERT` statements. It is "immutable" only by convention (no application deletes rows). A compromised admin account, a misconfigured migration script, or a `DELETE FROM audit_log WHERE ...` run manually can erase evidence without leaving any trace.

**Why it happens:** Developers conflate "append-only by code path" with "tamper-evident." Real tamper-evidence requires cryptographic linkage between records — a hash chain or external write-once storage.

**Consequences:** SOC 2 auditors will ask how you can prove the audit log has not been modified. The answer "we don't allow deletes in the API" is not sufficient. GDPR regulators require demonstrable evidence chain for data access events.

**Prevention:**
- Implement a hash chain: each audit record stores `sha256(prev_record_hash || this_record_content)`. Verification is a sequential scan computing expected vs. stored hashes — any gap or mismatch detects tampering.
- Use PostgreSQL row security policies to prevent `DELETE` and `UPDATE` on the audit table, even for the application's own database user. A separate migration-only user can do schema changes; the application user is insert-only.
- For compliance-tier deployments, optionally write audit record hashes to a separate write-once store (S3 object lock, WORM storage). The main DB is queryable; the hash store is the proof.
- Never store audit records in the same table that stores business data. Separate table, separate schema, separate backup policy.
- Log the log: any access to the audit log itself (queries, exports) should produce an audit event.

**Detection warning signs:**
- No verification mechanism exists for hash chain integrity
- Application DB user has `DELETE` permission on audit tables
- Audit table is in the same schema/database as asset tables with the same migration user

**Phase that should address it:** Compliance and audit milestone. The hash chain must be designed before any audit records are written — retrofitting it requires rewriting all existing records.

---

### Pitfall 6: Backfill Runs Without Resource Isolation

**What goes wrong:** A large backfill (e.g., recompute 365 daily partitions of a heavy asset) is launched concurrently with normal scheduled runs. The backfill saturates CPU, DB connection pool, and downstream connector concurrency limits. Normal scheduled runs fail or time out. Users blame the platform.

**Why it happens:** Backfills are treated as "just a lot of runs." The platform has no concept of run priority classes or resource pools. The connection pool size is fixed. 365 concurrent runs each acquiring a connection from the pool exhaust it immediately.

**Evidence from production:** The Dagster docs have an entire "Managing Concurrency" guide specifically because this problem is ubiquitous. LakeFSM's backfill guide notes that "without partitioning, backfilling becomes compute heavy, making it expensive and prone to errors."

**Consequences:** Production SLAs missed. Connector rate limits (BigQuery 300 concurrent requests/project) hit, causing cascading failures. Memory OOM in the executor process.

**Prevention:**
- Implement run priority queues from day one: `NORMAL`, `BACKFILL`, `CRITICAL`. The scheduler only promotes backfill runs if `NORMAL` and `CRITICAL` queues are empty or have headroom.
- Assign a separate database connection pool (or connection limit) to backfill runs
- Implement partition-aware backfill chunking: instead of enqueuing all 365 partitions at once, enqueue in batches of N (configurable, default 5). The next batch only starts when the previous batch completes.
- Track backfill progress independently from individual run progress so a crash mid-backfill can be resumed without re-running completed partitions

**Phase that should address it:** Partitioned assets and backfill milestone. Design the priority queue before implementing the backfill submit API.

---

## Moderate Pitfalls

---

### Pitfall 7: Approval Workflows That Become Synchronous Bottlenecks

**What goes wrong:** Every asset publication requires human approval. Approval requests pile up. The governance team becomes a bottleneck. Engineers work around the approval system by publishing to "staging" environments permanently. Governance becomes compliance theater.

**Evidence:** 2025 industry data shows data bottlenecks have increased 10% YoY, partially attributed to manual approval workflows. Organizations report employees waiting for manual approvals as a primary governance pain point.

**Prevention:**
- Implement automated pre-approval checks that run before human review: schema validation, quality rule checks, PII sensitivity scan. If all automated checks pass, the approval request starts in a better state.
- Support approval bypass for low-risk assets (no PII, no compliance tags) with a configurable auto-approval policy
- SLA timers: if an approval is not actioned within N hours, it escalates to a secondary approver or auto-approves with an escalation audit record
- Make the approval queue visible to engineers (show current wait time) so the governance team's workload is transparent

**Phase that should address it:** Governance workflows milestone. Design the auto-approval and escalation paths before the happy path, not after.

---

### Pitfall 8: Column-Level Access Control That Doesn't Cover All Query Paths

**What goes wrong:** Column masking is enforced at the platform's own query proxy, but analysts also query the underlying warehouse directly (Snowflake worksheet, BigQuery console, JDBC). The platform's masking is bypassed entirely. The access control appears to work in demos but fails in production.

**Evidence:** Snowflake and BigQuery both document that dynamic data masking must be applied at the warehouse layer to be effective. A 2025 analysis notes "one off-policy exposure breaks the security model" and that enforcement must be consistent across all data endpoints.

**Prevention:**
- Do not implement a custom query proxy for masking. Use each warehouse's native column masking features (Snowflake dynamic data masking, BigQuery column-level security) and manage those policies from the platform.
- The platform's role is to provision and sync masking policies to the warehouses, not to sit in the query path.
- Document explicitly what is NOT covered: direct JDBC connections, warehouse console access, third-party BI tools. Treat this as a known boundary, not a gap.
- Add a configuration check that warns if a warehouse connector's masking sync is failing

**Phase that should address it:** Column-level access control milestone. Verify warehouse-native masking capabilities for each target connector before building the enforcement layer.

---

### Pitfall 9: Connector API Stability Broken by Internal Refactoring

**What goes wrong:** The connector interface is initially defined quickly to get the first connectors working. Internal refactoring changes function signatures, context propagation, or error types. The interface changes break third-party connectors. Community adoption stalls. Once a connector ecosystem exists, breaking changes are fatal to community trust.

**Evidence:** The Airbyte CDK history and HashiCorp go-plugin history both demonstrate that API signature changes (even minor ones like adding a `bool` parameter) break all downstream implementations and require coordinated upgrades.

**Prevention:**
- Treat the connector interface as a public API from the first commit. Use semantic versioning. Any breaking change requires a major version bump.
- Define the connector interface in a separate Go module (`github.com/org/platform/connector`) with its own `go.mod` so third parties can pin it independently of the main platform
- Design the interface around stable primitives: `Read(ctx, query) (RowIterator, error)`, `Write(ctx, batch)`. Avoid passing internal structs that change as the platform evolves.
- Write a compliance test suite that any connector must pass. This becomes the contract. Breaking the contract requires a new interface version, not a silent change.
- Add a `Version() string` method to the interface so the platform can detect and warn about interface version mismatches at startup

**Phase that should address it:** Connector framework milestone (must be among the first milestones, before any connectors are built).

---

### Pitfall 10: Go Context Cancellation Not Propagated to External Queries

**What goes wrong:** A pipeline run is cancelled (user-initiated or due to timeout). The Go context is cancelled. But the connector's SQL query to BigQuery or Snowflake is already in flight and has no cancellation path. The query continues running in the warehouse, consuming credits and holding locks. The run appears cancelled in the platform but the warehouse is still working.

**Evidence:** This is a well-documented Go pattern problem. `goleak` and `go test -timeout` frequently reveal goroutines blocked on external IO after context cancellation because the IO call does not check `ctx.Done()`.

**Prevention:**
- Every connector `Read` and `Write` call must accept and propagate `context.Context` as the first argument. Never use `context.Background()` inside a connector implementation.
- For warehouse connectors, use the warehouse's own cancellation mechanism: BigQuery has `jobs.cancel`, Snowflake has `CANCEL QUERY`. Call the warehouse cancellation API when the context is cancelled.
- Use `select { case <-ctx.Done(): return ctx.Err(); case result := <-queryCh: ... }` pattern for all long-running queries
- Add goroutine leak tests (using `goleak`) to the CI pipeline. A test that starts a run and cancels it must show zero leaked goroutines after cancellation.
- Never use bare `time.Sleep` in pipeline code. Use `select { case <-time.After(d): case <-ctx.Done(): }` so cancellation always wins.

**Phase that should address it:** Execution engine milestone, specifically when implementing the cancellation and retry paths.

---

### Pitfall 11: Database Migrations in a Dirty State After Partial Failure

**What goes wrong:** A migration runs, fails partway through, and leaves the database in an inconsistent state. The migration tool marks the schema version as "dirty." The platform will not start until the dirty state is manually resolved. In production, this means downtime while an engineer diagnoses and force-sets the migration version.

**Evidence:** `golang-migrate` explicitly documents this with its "dirty flag" behavior. The tool refuses to apply further migrations after a partial failure until the operator runs `migrate force <version>`, which is a manual, dangerous operation.

**Prevention:**
- Wrap every migration in an explicit transaction. If the database supports transactional DDL (PostgreSQL does), any failure will roll back cleanly and leave no dirty state.
- Never write migrations that combine DDL (which must be transactional) with data migrations in the same migration file. Separate schema changes from data transformations.
- Test every migration with a rollback: `migrate up 1 && migrate down 1` must be a passing CI step.
- Use `goose` with `-- +goose Up` / `-- +goose Down` annotations and `-- +goose NO TRANSACTION` only when truly necessary (some index builds). Default to transactional.
- Have a documented runbook for the "dirty migration" recovery scenario. Do not discover the recovery procedure during a production incident.

**Phase that should address it:** Infrastructure setup (earliest milestone). Establish migration tooling and conventions before writing the first migration.

---

## Minor Pitfalls

---

### Pitfall 12: Lineage Graph Visualization Becomes Unusable at Scale

**What goes wrong:** The frontend renders the full lineage graph for an asset with 50+ upstream dependencies by default. The browser tab hangs. Users blame the platform and stop using the lineage feature.

**Prevention:**
- Default to showing 1-2 hops from the selected asset. Deeper traversal is opt-in.
- Implement server-side graph pagination and filtering before building the visualization layer.
- Add depth limits to all lineage API responses as non-optional query parameters.

**Phase that should address it:** Lineage UI milestone.

---

### Pitfall 13: Quality Rule Evaluation Blocking Asset Materialization

**What goes wrong:** Data quality rules run synchronously as part of the asset materialization step. A slow quality check (e.g., a COUNT DISTINCT on a billion-row table) delays the completion of the asset and blocks downstream assets that depend on it.

**Prevention:**
- Run quality checks asynchronously after materialization completes. The asset is marked "materialized" when data is written; quality check results are attached as metadata afterward.
- Allow marking quality checks as blocking (must pass before downstream assets can start) or non-blocking (informational).

**Phase that should address it:** Data quality milestone.

---

### Pitfall 14: Trying to Be Catalog, Orchestrator, AND Governance in One Sprint

**What goes wrong:** The first milestone attempts to ship orchestration + lineage + governance + catalog simultaneously. None of the features reach MVP quality. The codebase has cross-cutting dependencies that make individual features impossible to test in isolation.

**Evidence:** This is the most common cause of open-source data platform abandonment. Projects like Amundsen, Apache Atlas, and early versions of DataHub all struggled with scope — they tried to be comprehensive before being good at any single thing.

**Prevention:**
- Execution engine first, everything else second. A data governance platform that cannot reliably run pipelines is useless regardless of how good its catalog is.
- Define hard vertical slices: Phase 1 = execution engine (no governance UI), Phase 2 = lineage (no quality rules), etc.
- Enforce that each phase ships a working vertical slice to a real user, not just a demo.

**Phase that should address it:** Project planning (roadmap). This is an architectural discipline decision, not a code decision.

---

### Pitfall 15: PII Sensitivity Tags Not Propagated Through Lineage

**What goes wrong:** A field is tagged as PII in the metadata catalog. A downstream asset derives a new field from it. The PII tag is not automatically applied to the derived field. A governance report claims the derived field is non-PII. GDPR deletion requests miss downstream derived fields.

**Prevention:**
- Implement PII tag propagation as a first-class concept during lineage capture. When lineage is recorded, evaluate propagation rules (e.g., any column derived from a PII column inherits the PII tag unless explicitly overridden).
- Surface tag propagation events in the audit log.

**Phase that should address it:** Lineage + metadata integration milestone.

---

## Phase-Specific Warnings

| Phase Topic | Likely Pitfall | Mitigation |
|-------------|----------------|------------|
| Execution engine core | Non-atomic state transitions causing duplicate runs | Use `SELECT FOR UPDATE SKIP LOCKED` from day one |
| Scheduler + concurrency | Multi-layer concurrency limits that interact badly | Build a single token system; do not add layers incrementally |
| Partitions + backfill | No resource isolation between backfill and normal runs | Priority queue before backfill submit API |
| Lineage storage design | Adjacency list that times out at 500K+ edges | PostgreSQL recursive CTEs + depth limits + benchmark before UI |
| Lineage capture | Static declarations become stale | Tie lineage version to code hash; detect drift on materialization |
| Field-level lineage | SQL parser fails on dialects and SELECT * | Hybrid: explicit declaration + supplementary parsing with fallback |
| Connector API | Breaking changes destroy third-party connectors | Separate module, semantic versioning, compliance test suite |
| Column access control | Platform proxy bypassed by direct warehouse queries | Use warehouse-native masking, not a proxy |
| Audit log | Append-only by convention, not cryptographically | Hash chain + PostgreSQL row security; design before first write |
| Approval workflows | Human approval becomes a bottleneck | Auto-approve rules + SLA escalation before happy-path approval |
| Go context propagation | Cancelled runs leave warehouse queries running | `ctx.Done()` + warehouse cancellation API in every connector |
| Database migrations | Dirty state on partial failure | Transactional migrations only; separate DDL from data migrations |
| Governance UI | Column masking tested only via platform, not warehouse native | Verify each connector's native masking before building enforcement |
| Overall roadmap | Shipping all features simultaneously | Hard vertical slices; execution engine ships alone first |

---

## Sources

- Dagster issue #25743 — QueuedRunCoordinator concurrency interaction bug (November 2024): https://github.com/dagster-io/dagster/issues/25743
- Dagster issue #15155 — Duplicate runs in backfill: https://github.com/dagster-io/dagster/issues/15155
- Dagster issue #23508 — Global concurrency_key limits not affecting asset materialization (August 2024): https://github.com/dagster-io/dagster/issues/23508
- Dagster Managing Concurrency docs: https://docs.dagster.io/guides/operate/managing-concurrency
- dbt-core discussion #4458 — Column-level lineage SQL parsing challenges: https://github.com/dbt-labs/dbt-core/discussions/4458
- Data Lineage Best Practices 2025 (datadef.io): https://datadef.io/guides/en/data-lineage-best-practices
- Data Lineage: Challenges and Trends 2025 (datacrossroads.nl): https://datacrossroads.nl/2025/10/01/part-1-technological-challenges-data-lineage/
- Data Lineage in 2025 (seemoredata.io): https://seemoredata.io/blog/data-lineage-in-2025-examples-techniques-best-practices/
- Compliance by Design: 18 Tips for Tamper-Proof Audit Logs (Mattermost): https://mattermost.com/blog/compliance-by-design-18-tips-to-implement-tamper-proof-audit-logs/
- OpenMetadata vs DataHub (Atlan 2025): https://atlan.com/openmetadata-vs-datahub/
- Snowflake dynamic data masking pitfalls (k2view): https://www.k2view.com/blog/snowflake-dynamic-data-masking/
- Column-level access control (hoop.dev): https://hoop.dev/blog/column-level-access-control-protecting-sensitive-data-one-field-at-a-time
- Backfilling Data Guide (LakeFS): https://lakefs.io/blog/backfilling-data-foolproof-guide/
- 5 Common Pitfalls in Data Orchestration (Credencys): https://www.credencys.com/blog/5-common-pitfalls-in-data-orchestration-and-how-to-avoid-them/
- golang-migrate dirty state documentation: https://betterstack.com/community/guides/scaling-go/golang-migrate/
- goose vs golang-migrate (Leapcell): https://leapcell.io/blog/goose-vs-gorm-migrations-choosing-the-right-database-migration-tool-for-your-go-project
- HashiCorp go-plugin breaking change (openbao PR #827): https://github.com/openbao/openbao/pull/827
- Go context cancellation and goroutine leaks (2024): https://dev.to/serifcolakel/go-concurrency-mastery-preventing-goroutine-leaks-with-context-timeout-cancellation-best-1lg0
- Rethinking Tamper-Evident Logging (ACM CCS 2025): https://dl.acm.org/doi/10.1145/3719027.3765024
- Automated data governance and bottleneck patterns (OvalEdge 2025): https://www.ovaledge.com/blog/automated-data-governance
- State of Enterprise Data Governance 2025 (Board.org): https://board.org/data/resources/what-we-learned-from-the-2025-state-of-enterprise-data-governance-report/
