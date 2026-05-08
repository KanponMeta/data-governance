# Phase 4: 血缘与 Schema - Context

**Gathered:** 2026-05-08
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 4 turns the run-execution kernel from Phases 1–3 into a **lineage-aware, Schema-aware** platform. Specifically, after every successful materialization the platform must:

- **Auto-capture asset-level lineage** from `Asset.Upstreams()` (already declared) + emit a per-run `lineage.captured` event so the lineage graph is always present and run-attributed
- **Persist user-declared column-level lineage** ("output column X derives from upstream Z's column Y") supplied either at builder time or via `MaterializeResult`, bound to a stable code-hash so drift is detectable
- **Capture the produced table/column Schema** via a connector capability, hash it, and append a `schema_versions` row when (and only when) the Schema actually changes
- **Diff successive Schemas** and emit structured `schema_changes` rows + an immutable `schema.change_detected` event, classifying each change as breaking or non-breaking
- **Allow operators to acknowledge intentional breaks** (audit trail preserved) so downstream alerting in Phase 5 isn't drowned in expected drops
- **Expose impact analysis** (LINE-06) via a Go package wrapped by both REST and CLI, supporting upstream and downstream traversal of the lineage graph
- **Allow descriptions/owners/tags** on assets, tables, and columns — code-declared defaults plus runtime-mutable overrides via REST
- **Surface a per-column timeline** (META-05) derived from `schema_changes`

What stays *out* of Phase 4: PII tag propagation (Phase 5), OpenLineage event *ingestion* from external tools (v2 ALINE-02), SQL-AST-based column-lineage inference (v2 ALINE-01), the React lineage DAG visualization (Phase 6 LINE-04/LINE-05), warehouse masking sync (Phase 5 RBAC-04), and metadata search/filter (Phase 6 META-04). The Phase 1–3 contracts (event_log RLS-immutability, run lifecycle, claim atomicity, scheduler/sensor daemon, partition keys) are **not modified**, except for additive event_type enum values and the new optional `connector.SchemaDescriber` interface.

</domain>

<decisions>
## Implementation Decisions

### Lineage Capture Model & Drift Detection

- **D-01:** Asset-level lineage (LINE-01) captured **two ways simultaneously**:
  1. **Static derivation** from `Asset.Upstreams()` at registry time, persisted into `asset_edges` (always present, never depends on a run having happened — supports impact analysis on never-yet-materialized assets).
  2. **Run-attributed events** — every successful materialization emits a `lineage.captured` event in `event_log` carrying `(run_id, code_hash, observed_upstreams)`. The event is the audit trail; `asset_edges` is the queryable canonical view. Drift is detected when the run-observed upstreams diverge from the static set (Pitfall #3 mitigation).

- **D-02:** Column-level lineage (LINE-02) declared **two ways**, with runtime taking precedence:
  - **Builder default:** `asset.New("x").ColumnLineage(asset.ColumnLineage{ "out_col": []asset.ColumnRef{{Asset:"upstream", Column:"src"}} })` declares a static map at registration. Queryable before first run; bound to the code_hash; the canonical "intended" lineage.
  - **Runtime override:** `MaterializeResult.ColumnLineage` (typed field, replacing the free-form `Metadata` slot's lineage usage) lets a Materialize function publish per-run lineage — necessary for partitioned assets where the source columns differ by partition.
  - **Resolution:** runtime override wins for a given run row; in absence of a runtime value, the builder default applies. If neither is set, the asset is tagged `column_lineage_undeclared` (LINE-02 is opt-in per Roadmap; we record the gap rather than synthesize anything).

- **D-03:** Code-hash binding uses an **asset definition fingerprint** (NOT a source-code hash):
  - Hash inputs: `(asset.Name, sorted Upstreams, ColumnLineage map normalized to canonical JSON, declared Schema spec if any, declared Description/Owner/Tags)`.
  - Computed at `builder.Register()` (deterministic Go encoding via `encoding/json` with stable map key ordering, then SHA-256).
  - Stored on every `asset_versions` row and copied onto every emitted `lineage.captured` / `schema_versions` / `schema_changes` row. Hash changes ⇒ a new asset_version row is appended.
  - Trade-off: fingerprint catches *declaration* drift (what the user typed). It does NOT catch "the SQL inside Materialize changed but declarations didn't" — Pitfall #3 handles that case via the *separate* drift detection in D-04 (when captured Schema diverges from declared column lineage).

- **D-04:** When the platform detects column-lineage drift (declared column references a column no longer present in captured Schema, or upstream emits a new column not referenced in any declaration), the run **succeeds**:
  - Emit `lineage.drift_detected` event in `event_log` carrying the diff details.
  - Set `asset_versions.drift_status = 'pending'` for surfacing in REST/UI.
  - Drift is informational, not data-correctness-critical (lineage is metadata about how data flows, not the data itself). Aligns with Phase 1 D-09 "fail loudly but don't block production" philosophy. Operator clears drift by updating declarations and re-deploying (which produces a new code_hash and a fresh asset_version row). No auto-update — preserves user intent.

### Schema Capture

- **D-05:** Schema capture (META-01) happens via a new optional **connector capability**:
  ```go
  type SchemaDescriber interface {
      DescribeSchema(ctx context.Context, ref AssetRef) (Schema, error)
  }
  ```
  Source-of-truth is the warehouse/database itself (PostgreSQL `information_schema`, future BigQuery `INFORMATION_SCHEMA.COLUMNS`, Snowflake `SHOW COLUMNS`, S3 Parquet footer). Most accurate — catches DDL changes made out-of-band (DBA ran `ALTER TABLE` directly). The Phase 1 PostgreSQL connector (CONN-01) gains this capability in Phase 4; future connectors implement when they ship Schema-aware features.

- **D-06:** CONN-08 stability is preserved by making `SchemaDescriber` a **separate optional interface**, NOT a new method on `connector.Connector`:
  - Phase 4 type-asserts at runtime: `if d, ok := conn.(connector.SchemaDescriber); ok { ... }`.
  - Connectors that don't implement it: platform falls back to `MaterializeResult.Schema` if the user populated it; otherwise the asset is tagged `schema_capture_unsupported` and META-01/META-02 silently skip. No alert noise for connectors that legitimately can't introspect (e.g., a Kafka producer connector).
  - Pattern matches Go's idiomatic `io.Reader` + `io.WriterTo` capability pairing. Same approach Phase 5 will use for masking/RBAC capabilities.

- **D-07:** Captured Schema is **rich**:
  ```go
  type Schema struct {
      Columns        []Column
      PrimaryKey     []string  // ordered column names
      RowCountEstim  int64     // -1 if connector can't supply
      CapturedAt     time.Time
  }
  type Column struct {
      Name       string
      Type       string  // connector-normalized: "int64", "decimal(10,2)", "timestamptz", "text"
      Nullable   bool
      Default    *string  // pointer so absence != ""
      IsPrimaryKey bool
      Comment    string  // for META-03 seeding
  }
  ```
  Sufficient to detect every breaking-change category in D-09 and to seed META-03 column descriptions.

- **D-08:** Schema captured **every successful run, dedup on hash**:
  - Compute `schema_hash = sha256(canonical_json(Schema))`.
  - If `schema_hash` matches the latest `schema_versions` row for this asset: only `UPDATE last_seen_run_id, last_seen_at` on that row. No new row, no diff event.
  - If hash differs: insert new `schema_versions` row, run diff against previous version, emit `schema_changes` rows + `schema.change_detected` event.
  - Storage cost stays near-zero for stable Schemas (the common case); correctness preserved (META-01 says "after each materialization" — we observe each, just dedup the persistence).
  - DescribeSchema error: log error, emit `schema.capture_failed` event, run succeeds (consistent with D-04 — metadata failures don't fail the data work).

### Schema Diff & Breaking-Change Classification

- **D-09:** Diff classification rules (META-02):
  - **Breaking:** column dropped; type narrowed (e.g., `int64 → int32`, `decimal(10,2) → decimal(8,2)`, `varchar(255) → varchar(64)`); existing column changes from `nullable` to `not null`; primary-key composition changes (column added to / removed from PK, or PK column reordered).
  - **Non-breaking:** column added (regardless of nullability — defaults handle null-on-read in downstream consumers); type widened (e.g., `int32 → int64`); `not null → nullable`; comment change; default-value change.
  - Renames are detected as `(drop, add)` — no heuristic rename detection in v1 (heuristics are risky; if a user wants rename semantics, they ack the drop and add together).
  - Type narrowing/widening uses a connector-supplied compatibility helper (Phase 4 ships the rule for the in-tree PostgreSQL connector; future connectors register their own type lattice). Out-of-rule type changes (e.g., `text → bytea`) default to **breaking** (safe default).

- **D-10:** Operators can **ack intentional breaks** without losing the audit trail:
  - New CLI: `./platform schema ack-break <asset> <change_id> --reason="..."`.
  - REST equivalent: `POST /schema/changes/:id/ack` with body `{reason: "..."}`.
  - Implementation: sets `schema_changes.acknowledged_at`, `acknowledged_by`, `acknowledgement_reason`. Row is NOT deleted — acknowledgement is additive metadata (Phase 1 D-09 immutability extends to schema_changes via the same RLS pattern).
  - Phase 5 alerting filters on `acknowledged_at IS NULL` for "active breaking changes". META-05 timeline shows acknowledged breaks distinctly (UI signal "intentional ✓"). Reason free-text but required (no silent acks).

- **D-11:** Schema-change records live in **a dedicated table + an event_log pointer**:
  - `schema_changes` (queryable structured store): `(id, asset, run_id, code_hash, prev_version_id, new_version_id, change_type ENUM, column_name NULLABLE, prev_type, new_type, prev_nullable, new_nullable, is_breaking BOOL, observed_at, acknowledged_at NULLABLE, acknowledged_by NULLABLE, acknowledgement_reason NULLABLE)`.
  - `event_log` (immutable audit): one `schema.change_detected` event per *diff batch* (per run that produced changes), payload is `{schema_changes_ids: [...]}` — not the full payload (that's in `schema_changes`).
  - Two stores, two purposes, zero payload duplication.
  - `schema_versions(id, asset, code_hash, schema_hash, schema JSONB, captured_at, last_seen_at, last_seen_run_id)` is the third table — full Schema snapshots, pointed to by `schema_changes.{prev,new}_version_id`.

- **D-12:** META-05 column timeline is **derived from `schema_changes`**, no separate `column_history` table:
  - Query: `SELECT * FROM schema_changes WHERE asset=$1 AND column_name=$2 ORDER BY observed_at`.
  - Asset timeline aggregation: `SELECT * FROM schema_changes WHERE asset=$1 ORDER BY observed_at` plus a JOIN to `schema_versions` for snapshot context.
  - Single source of truth, zero sync risk between two tables. If query performance becomes an issue at v1.x volume, a materialized `column_history` rollup can be added later — premature now.

### Lineage Storage & Traversal

- **D-13:** Adjacency tables are **split** by granularity:
  - `asset_edges (id, from_asset, to_asset, code_hash_first, code_hash_latest, first_seen_run_id, first_seen_at, last_seen_run_id, last_seen_at, superseded_at NULLABLE)` — one row per asset→asset edge.
  - `column_edges (id, from_asset, from_column, to_asset, to_column, code_hash_first, code_hash_latest, first_seen_run_id, first_seen_at, last_seen_run_id, last_seen_at, superseded_at NULLABLE)` — one row per (column, column) edge.
  - Asset-level traversal hits the small table (typically <10K rows for a large platform); column-level only consulted when the user opts into column drill-down. Pitfall #4 mitigation by structure.
  - Indices: `(from_asset, from_column)` and `(to_asset, to_column)` on `column_edges`; `(from_asset)` and `(to_asset)` on `asset_edges`. All include `WHERE superseded_at IS NULL` partial-index variants for the hot active-edges path.

- **D-14:** Traversal uses **PostgreSQL `WITH RECURSIVE` with a hard depth cap**:
  - Default depth: **10** (configurable per request via `?depth=N`).
  - Hard ceiling: **25** (request rejected with 400 if `depth>25` — Pitfall #4 DoS mitigation).
  - Bidirectional: same query template, parameterized on `direction='upstream'|'downstream'`, joining the appropriate edge column pair.
  - Result shape: `[{asset, column?, depth, edge_metadata}]` — client assembles the tree.
  - `EXPLAIN ANALYZE` on the recursive CTE captured during planning and re-checked at the end of Phase 4 (ARCHITECTURE.md § lineage).

- **D-15:** Edges are **soft-retired**, never deleted (temporal table pattern):
  - First time `(from, to)` edge observed: `INSERT` with `first_seen_*` and `last_seen_*` set to the current run.
  - Subsequent run sees the same edge: `UPDATE last_seen_run_id, last_seen_at, code_hash_latest`.
  - Run with new code_hash no longer produces an edge that was in a prior run: `UPDATE superseded_at = NOW()`.
  - Active queries: `WHERE superseded_at IS NULL` (default).
  - Point-in-time queries (impact analysis "as of date $T"): `WHERE first_seen_at <= $T AND (superseded_at IS NULL OR superseded_at > $T)`. META-05's "show me the lineage as it was 3 months ago when the incident happened" works for free.
  - No deletes from `asset_edges` / `column_edges` (Phase 1 D-09 immutability extends here).

- **D-16:** Query management splits **sqlc for hot reads, ent for CRUD**:
  - sqlc owns the recursive CTE traversal queries and impact analysis: `internal/lineage/queries/lineage.sql` → generated bindings under `internal/lineage/queries/`. Pattern matches Phase 2's `internal/run/claim.go` (raw SQL for performance-critical SKIP LOCKED) + ent for everything else. Aligns with CLAUDE.md tech stack: "sqlc for high-performance read queries".
  - ent owns row writes: new ent entities `LineageEdge` (asset), `ColumnLineageEdge`, `SchemaVersion`, `SchemaChange`, `AssetVersion`, `AssetMetadata`. Migrations via Atlas as in Phases 1–3.

### Metadata Mutation API (META-03)

- **D-17:** Description / owners / tags settable **two ways, runtime wins on read**:
  - **Builder defaults (code-declared, immutable per code_hash):**
    ```go
    asset.New("orders").
        Description("Daily orders fact table").
        Owner("team-data@").
        Tags("finance", "pii").
        Columns(
            asset.Column("user_id").Description("FK users.id").Tags("pii"),
            asset.Column("total").Description("USD cents"),
        )
    ```
    Stored on `asset_versions` row, fingerprinted into code_hash (D-03) — version-controlled provenance.
  - **Runtime overrides via REST (mutable, governance-team-friendly):**
    - `PATCH /assets/:name/metadata` → `{description, owner, tags}` (replaces or merges per request flag)
    - `PATCH /assets/:name/columns/:col/metadata` → same shape
    - Stored in `asset_metadata` table keyed by `(asset, column NULLABLE)`. Read API: `effective = COALESCE(runtime_value, code_default)`.
  - **Audit trail:** every PATCH emits `metadata.updated` event in `event_log` with `{actor, before, after}` payload. Phase 1 D-09 RLS-immutability ensures retention.
  - Resolves the PROJECT.md core-value tension: data engineers ship sane defaults in code; governance teams self-serve corrections without engineer redeploys.

### OpenLineage Compatibility

- **D-18:** OpenLineage **hybrid**: internal storage native, OL emit on demand:
  - **Internal:** `lineage_edges`, `column_edges`, `schema_versions`, `schema_changes` use our own schema (optimized for our queries — not gated on OL spec churn).
  - **Export:** new CLI `./platform lineage export --asset=... --since=... --format=openlineage` and REST `GET /lineage/export?format=openlineage` translate stored rows into OpenLineage `RunEvent` JSON. Translator lives in `internal/lineage/openlineage/` — small, tested, no runtime dependency on `ThijsKoot/openlineage-go` (LOW credibility per CLAUDE.md). If/when a real Marquez/Datahub user appears we can vendor the spec directly.
  - **Pitfall #3 alignment:** OL recommends events carry `runId` and `eventTime` so consumers can detect staleness. Our run-attributed model (D-01) already produces both — translation is a field-rename, not a redesign.

### Impact Analysis API Surface (LINE-06)

- **D-19:** Impact analysis ships **a Go package + REST + CLI**, all wrapping the same library function:
  - Library: `internal/lineage/impact.Analyze(ctx, query ImpactQuery) (Impact, error)` where `ImpactQuery = {Asset, Column*, Direction, Depth, AsOf*}`.
  - REST: `GET /lineage/impact?asset=...&column=...&direction=downstream&depth=10[&as_of=2026-04-01T00:00:00Z]` — returns paginated tree (cursor on `(asset, column, depth)`).
  - CLI: `./platform impact <asset> [--column=X] [--direction=down|up] [--depth=10] [--as-of=...] [--format=table|json]` — operators get an investigation tool today.
  - Three surfaces, one logic. Phase 6 React UI hits the REST. External alerting / change-management bots also hit the REST (no need to shell out to CLI).

- **D-20:** Impact analysis returns **upstream OR downstream from a single endpoint**:
  - `direction=downstream` (default — "who depends on me?", canonical LINE-06 question).
  - `direction=upstream` (root-cause investigation — "what feeds me?").
  - Same recursive CTE template parameterized on direction; same response shape. Operators investigating an incident almost always want both halves.

### Event Log Additions

- **D-21:** New `event_type` enum values added to extend Phase 1 D-10 / Phase 2 D-18 / Phase 3 D-17:
  - **Lineage:** `lineage.captured`, `lineage.drift_detected`
  - **Schema:** `schema.captured`, `schema.unchanged`, `schema.change_detected`, `schema.capture_failed`
  - **Schema acknowledgement:** `schema.break_acknowledged`
  - **Metadata:** `metadata.updated` (assets and columns share the type; payload disambiguates)
  - All follow the Phase 1 D-09 RLS-immutability rules — append-only, no UPDATE/DELETE permissions for the application DB user.

### Claude's Discretion

- Exact JSONB shape of the `Schema.Columns` array — must round-trip stably (canonical JSON for hash) but field ordering inside `Column` is implementation detail.
- Whether the asset definition fingerprint (D-03) hashes Description/Owner/Tags — leaning yes (changes intent), but a metadata-only edit shouldn't trigger a "lineage drift" alarm. Final call during planning.
- The exact format of `schema_changes.change_type` enum (e.g., `column_dropped`, `column_added`, `type_narrowed`, `type_widened`, `nullable_added`, `nullable_removed`, `pk_changed`) — pick a stable set; the diff function classifies and stops there.
- CLI output format for `./platform impact` and `./platform schema diff` — table vs structured JSON; default to table for human ops, `--format=json` for scripting.
- Whether `AssetVersion` is its own ent entity or a row group implied by `(asset, code_hash)` on a join table. Both work; pick one consistent with Phase 5's likely RBAC binding to asset versions.
- Whether `MaterializeResult.ColumnLineage` is `map[string][]ColumnRef` or a strongly-typed slice — both can encode the same data; map is clearer for the user but slice is friendlier for ent serialization. Lean map; revisit if perf shows up.
- Whether the OpenLineage export endpoint (D-18) lives under `/lineage/export` or `/exports/lineage` — pick one and document.
- Number of breaking-change category enum values exposed publicly — minimum is "breaking | non-breaking | needs_review"; granular categories internal.
- Whether `connector.SchemaDescriber` returns `(Schema, error)` or `(Schema, Diagnostics, error)` — diagnostic channel is nice-to-have, can land in v2 if no real use case appears.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Requirements & Roadmap
- `.planning/REQUIREMENTS.md` — Phase 4 in-scope: LINE-01, LINE-02, LINE-03, LINE-06, META-01, META-02, META-03, META-05 (lines 38–60); deferred: ALINE-01/ALINE-02 (line 109)
- `.planning/ROADMAP.md` §Phase 4 (line 76) — five acceptance criteria + dependency on Phase 3

### Project Context
- `.planning/PROJECT.md` §核心价值 — "下游使用者能信任所用数据、追溯其字段级来源" — drives D-01 (run-attribution) + D-02 (column lineage)
- `.planning/PROJECT.md` §关键决策 — "字段级血缘作为一等特性" — Phase 4 is the place this becomes real
- `.planning/phases/01-infrastructure/01-CONTEXT.md` — Phase 1 decisions: D-09 event_log RLS immutability (extended in D-11/D-21), D-10 event_type enum extension model
- `.planning/phases/02-execution-engine/02-CONTEXT.md` — Phase 2 decisions: D-04 `MaterializeResult.Metadata` lineage hook (Phase 4 promotes to typed `ColumnLineage`), connector interface frozen (CONN-08 motivates D-06 capability pattern)
- `.planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md` — Phase 3 decisions: D-07 sensor `Payload` flows into `MaterializeResult.Metadata` (must coexist with new typed lineage fields); D-09/D-10 partition_key on runs (lineage edges may be partition-scoped — see Phase 3 deferred "partition dependency mapping")

### Research (must-read for Phase 4)
- `.planning/research/PITFALLS.md` §陷阱 3 (line 62) — "字段级血缘仅在声明时捕获" — drives D-03 fingerprint + D-04 drift detection
- `.planning/research/PITFALLS.md` §陷阱 4 (line 89) — "邻接表存储不能扩展" — drives D-13 split tables + D-14 recursive CTE + depth cap
- `.planning/research/PITFALLS.md` §陷阱 12 (line 248) — "血缘图可视化在规模上变得不可用" — depth cap is a Phase 4 backend concern even though UI is Phase 6
- `.planning/research/PITFALLS.md` §陷阱 15 (line 288) — "PII 标签未通过血缘传播" — explicitly Phase 5 work; Phase 4 D-17 puts the tag-storage scaffold in place
- `.planning/research/PITFALLS.md` §阶段特定警告 (line 300+) — Phase 4 row covers lineage capture + storage + field-level
- `.planning/research/ARCHITECTURE.md` — lineage subsystem placement and dependencies
- `.planning/research/STACK.md` — confirms PostgreSQL recursive CTE + sqlc for hot reads (D-16); flags ThijsKoot/openlineage-go as LOW credibility (D-18 hybrid avoids dependency)

### Tech Stack & Conventions
- `CLAUDE.md` §技术栈 §ORM/查询层 — ent for graph schema + sqlc for hot reads — split adopted directly in D-16
- `CLAUDE.md` §技术栈 §血缘捕获 — OpenLineage as event format standard; hybrid emit pattern in D-18
- `CLAUDE.md` §可信度评估 — OpenLineage Go client LOW credibility row → D-18 builds in-house translator

### Phase 1–3 Code (frozen contracts Phase 4 builds on)
- `internal/asset/asset.go` — `MaterializeResult` (D-04 hook); Phase 4 adds typed `ColumnLineage` and (later) `Schema` fields alongside existing `Metadata`
- `internal/asset/io.go` — `AssetIO.PartitionKey()` already exposed (Phase 3 D-09); column-edge rows may carry partition_key
- `internal/asset/builder.go` — extend with `.ColumnLineage(...)`, `.Description(...)`, `.Owner(...)`, `.Tags(...)`, `.Column(...)` builder methods
- `internal/connector/connector.go` — extend in additive way: new `SchemaDescriber` optional interface (D-05/D-06)
- `internal/event/types.go` — add new event_type values (D-21); follow Phase 3 D-17 pattern
- `internal/storage/ent/` — new ent entities for `LineageEdge`, `ColumnLineageEdge`, `SchemaVersion`, `SchemaChange`, `AssetVersion`, `AssetMetadata`
- `migrations/` — new Phase 4 migration adding tables; same Atlas + hand-managed CHECK pattern as Phase 2/3
- `cmd/platform/main.go`, `factories.go` — add `impact`, `schema`, `lineage` subcommands

### External References
- Dagster lineage docs: https://docs.dagster.io/concepts/assets/asset-materializations#asset-lineage — informs D-01 run-attribution model
- dbt-core column-lineage discussion #4458: https://github.com/dbt-labs/dbt-core/discussions/4458 — informs D-02 declared-vs-inferred trade-off (we ship declared; ALINE-01 is v2)
- OpenLineage spec: https://openlineage.io/docs/spec/object-model — D-18 export translator follows this
- PostgreSQL recursive CTE docs: https://www.postgresql.org/docs/16/queries-with.html — D-14 traversal implementation reference
- ent + sqlc dual-ORM precedent (already in use in `internal/run/claim.go`) — D-16 pattern reference

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets (from Phases 1–3)
- **`asset.MaterializeResult`** — currently `{RowsWritten, Metadata map[string]any}`. Phase 4 adds typed `ColumnLineage` and `Schema` fields; `Metadata` stays for sensor `Payload` and connector-specific extras (Phase 3 D-06).
- **`asset.Asset` / `asset.Builder`** — extend with metadata declaration methods (`Description`, `Owner`, `Tags`, `Column`, `ColumnLineage`); existing chain (`Schedule`, `Sensor`, `Partitions`, `Retry`, `Resource`) untouched.
- **`asset.AssetIO.PartitionKey()`** — already populated for partitioned runs; column-edge rows write `partition_key` for partition-scoped lineage indexing.
- **`connector.Connector` interface** — frozen (CONN-08); Phase 4 introduces `connector.SchemaDescriber` as a *separate* optional interface — capability pattern.
- **`event.Writer` / `event.Type`** — reused; `event_type` enum gains new values (D-21) following Phase 3 D-17 pattern (CHECK constraint extension).
- **`storage.Storage` / ent client** — Phase 4 adds six new ent entities and migration for new tables, all via the established ent + Atlas pattern (Phase 1 D-04).
- **`run.ClaimNext` / runs table** — unchanged. New `code_hash`, `lineage_emitted` columns may be added to `runs` for crash-recovery of lineage events; otherwise no impact.
- **PostgreSQL RLS immutability** (Phase 1 D-09) — extends to new `schema_changes` table with the same role grant pattern (event_log model).

### Established Patterns
- **Optional capability interface** — D-06 introduces this pattern for the first time (separate from `connector.Connector`); Phase 5's RBAC/masking will follow.
- **Event log as audit, dedicated table as queryable view** — D-11 establishes the schema_changes pattern; future phases (Phase 5 audit chain, Phase 6 governance inbox) follow the split.
- **sqlc + ent dual-ORM** — already used by `internal/run/claim.go` (raw SQL for SKIP LOCKED) + ent for everything else; Phase 4 formalizes sqlc as the home for recursive-CTE traversals.
- **Soft-retire / temporal table** — D-15 introduces this for lineage edges; Phase 5 RBAC policies will likely follow the same pattern (point-in-time access control).
- **Append-only event log + RLS-immutable** (Phase 1 D-09) — extends to `schema_changes` and `asset_metadata` history rows.
- **Subcommand-per-mode binary** (Phase 2 D-02) — adding `impact`, `schema`, `lineage` subcommands is purely additive.

### Integration Points
- `internal/asset/` — DSL extensions (Description/Owner/Tags/Column/ColumnLineage builder methods)
- `internal/connector/` — add `SchemaDescriber` optional interface; in-tree PostgreSQL connector implements it
- `internal/lineage/` (NEW) — capture writer (called from executor), impact.Analyze library, OpenLineage export translator
- `internal/lineage/queries/` (NEW) — sqlc-managed recursive CTE traversal queries
- `internal/schema/` (NEW) — Schema diff function, breaking-change classifier, schema_versions writer
- `internal/metadata/` (NEW) — AssetMetadata read/write, runtime mutation REST handlers
- `internal/api/` — new REST endpoints under `/lineage/*`, `/schema/*`, `/assets/:name/metadata`, `/assets/:name/columns/:col/metadata`
- `internal/runtime/executor.go` — minor: after successful Materialize, hand off result to lineage capture writer + schema capture writer (synchronous, in same transaction as run row update — preserves Phase 1 D-04 atomicity)
- `cmd/platform/impact.go` (NEW), `cmd/platform/schema.go` (NEW), `cmd/platform/lineage.go` (NEW) — CLI subcommand handlers
- `migrations/2026MMDDHHMMSS_phase4_*.sql` — `asset_edges`, `column_edges`, `schema_versions`, `schema_changes`, `asset_versions`, `asset_metadata` tables; CHECK constraints; RLS grants; partial indices

</code_context>

<specifics>
## Specific Ideas

- **Lineage is metadata, not data correctness.** D-04 drift action is "warn + flag, run succeeds". A failed lineage capture or drift never blocks materialization. The Phase 1 D-09 philosophy applied to a new domain.
- **Two storage paths, two purposes.** D-01 captures lineage statically (asset_edges) AND as run-attributed events. D-11 captures schema changes as queryable rows AND as immutable event_log entries. Same pattern: a queryable canonical view + an immutable audit log. No payload duplication — the event payload points to the structured row by ID.
- **Code-hash binds the *declaration*, not the *implementation*.** D-03 fingerprints what the user typed (name + Upstreams + ColumnLineage + Schema spec + metadata). It does NOT hash MaterializeFunc source — that path is brittle and was deliberately rejected. Drift between declaration and observed reality is what D-04 detects; that's the compensating mechanism.
- **CONN-08 stability is preserved by the optional capability pattern.** D-06 introduces `connector.SchemaDescriber` as a *separate* interface. The base `connector.Connector` interface from Phase 1 does NOT change. Future capabilities (masking — Phase 5; row-count statistics — v2 quality rules) follow the same pattern. This is the precedent.
- **Pitfall #4 mitigation is structural, not just runtime.** D-13 splits asset/column edge tables (so asset traversal hits a small table); D-14 enforces hard depth cap of 25 (DoS prevention); D-15 soft-retires (active edges set stays small even as historical accumulates). All three together — alone, each is insufficient.
- **OpenLineage is an export concern, not a runtime dependency.** D-18 keeps `ThijsKoot/openlineage-go` (LOW credibility per CLAUDE.md) out of the runtime path. Internal storage uses our schema; OL JSON is generated on demand by a translator we own.
- **Metadata is governance-team-mutable.** D-17's runtime PATCH endpoints exist precisely so governance teams don't need engineer redeploys to fix tags or owners. PROJECT.md core value: "数据从业者可以用代码定义、运行并治理数据资产". Governance is a first-class user.
- **Acknowledgement is additive, not destructive.** D-10 ack adds `acknowledged_at` columns; the row stays. Compliance audits later need to know "this break was reviewed and intentional", not "this break never happened".
- **Impact analysis ships in Phase 4, not Phase 6.** D-19 ships REST + CLI (not just CLI) so external alerting/change-management bots have an integration point now. Phase 6 wires the React UI to the same REST.
- **Recursive CTEs use sqlc, not ent.** D-16 codifies the dual-ORM split — performance-critical traversals are hand-written SQL bound by sqlc; row CRUD is ent. Same pattern as Phase 2's `claim.go`.

</specifics>

<deferred>
## Deferred Ideas

- **PII tag propagation through lineage** (Pitfall #15) — Phase 5 work. Phase 4 D-17 stores tags on assets/columns; Phase 5 adds the propagation rule "downstream column inherits parent's PII tag unless explicitly overridden". The storage scaffold lands in Phase 4; the propagation engine in Phase 5.
- **SQL-AST-based column-lineage inference (ALINE-01)** — v2. Pitfall #3 explicitly says SQL parsers fail on dialects, SELECT *, CTEs. Phase 4 ships *declared* column lineage (D-02). Inference comes later, behind a feature flag, with declared values winning on conflict.
- **OpenLineage event ingestion (ALINE-02)** — v2. Phase 4 *exports* OL (D-18); ingestion of OL events from external Spark/Airflow runs is a v2 connector-style work item.
- **Heuristic rename detection** — D-09 explicitly calls renames `(drop, add)`. Heuristic rename detection (same type, position, similar name) is risky and not v1. If a user wants rename semantics, they ack the drop and add as a coordinated pair.
- **Per-asset compatibility policy** (`.Compatibility(StrictBackwardCompatible)`) — D-09 ships the standard rule. Per-asset override considered if a real user produces a case the standard rule misclassifies.
- **Materialized `column_history` rollup** — D-12 derives the timeline from `schema_changes`. If query performance suffers at high asset counts (>10K), a materialized view can be added then.
- **OpenLineage Go client vendoring (`ThijsKoot/openlineage-go`)** — LOW credibility per CLAUDE.md. D-18 builds an in-house translator. If a real Marquez/Datahub user appears, vendor the spec types directly.
- **Schema introspection for connectors that don't natively support it** (S3 without Parquet, Kafka, REST APIs) — D-06's optional capability handles this gracefully (asset tagged `schema_capture_unsupported`). Adding a Phase 4-side schema-inference fallback (sample rows + infer types) is a v1.x feature, not v1.
- **Granular schema_changes change_type enum exposed via REST** — D-09's "Claude's discretion" line. Internal implementation may have 8+ change types; public API exposes minimum {breaking, non-breaking, needs_review}. Granular surfacing is a v1.x UX polish.
- **Per-column statistics in captured Schema** (null count, distinct count, min/max) — Phase 5 data quality rules will need these; Phase 4 adds `RowCountEstim` (D-07) but not column stats. Connectors can optionally provide; Phase 5 formalizes.
- **Partition-aware lineage edges** — D-13 column_edges has room for `partition_key`. Whether lineage edges are partitioned-scoped vs always-aggregated to the asset level is a planning detail. For v1, edges are asset-level (with optional partition_key for diagnostic context); future phases revisit if real users hit cases where the same asset has different lineage per partition.
- **Asset version diff REST endpoint** — `GET /assets/:name/versions/:from/diff/:to` — useful but Phase 4 ships only the storage; the REST diff endpoint is a Phase 6 inbox feature.
- **MaterializeResult schema_capture_supported flag** — Phase 4 type-asserts `connector.SchemaDescriber` at runtime; an explicit per-run flag in MaterializeResult to opt out of capture (e.g., user wrote ad-hoc data, schema irrelevant) is a v1.x polish.

</deferred>

---

*Phase: 04-schema*
*Context gathered: 2026-05-08*
