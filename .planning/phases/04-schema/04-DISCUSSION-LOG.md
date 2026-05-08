# Phase 4: 血缘与 Schema - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-05-08
**Phase:** 04-schema
**Areas discussed:** Lineage capture + column API, Schema capture mechanism, Schema diff + breaking rules, Storage schema + traversal, Metadata mutation API (META-03), OpenLineage compatibility, Impact analysis API surface (LINE-06)

---

## Lineage capture + column API

### Asset-level capture model

| Option | Description | Selected |
|--------|-------------|----------|
| Both: static + run-attributed | Derive baseline edges from `Asset.Upstreams()` at registration AND emit a run-attributed `lineage.captured` event per run. Asset-level edges in graph are the union; events give time-series + drift detection. | ✓ |
| Static-only from Upstreams() | Persist edges once when assets register; recompute on registry reload. Simplest. | |
| Run-attributed only | Every materialization emits fresh `(from_asset, to_asset, run_id, code_hash, observed_at)` event. | |

**User's choice:** Both: static + run-attributed
**Notes:** Carried D-01.

### Column-level lineage declaration API

| Option | Description | Selected |
|--------|-------------|----------|
| Both: builder default + Mat. override | `.ColumnLineage(...)` declares static default; `MaterializeResult.ColumnLineage` may override per-run. | ✓ |
| Builder-time static only | Only `.ColumnLineage(map[string][]asset.ColumnRef{...})`. Pure static. | |
| Runtime via MaterializeResult only | Engineers populate `MaterializeResult.ColumnLineage` only. | |

**User's choice:** Both: builder default + MaterializeResult override
**Notes:** Carried D-02. Resolution: runtime override wins per run; builder default applies if absent; both absent ⇒ tag asset `column_lineage_undeclared`.

### Code-hash binding (Pitfall #3 mitigation)

| Option | Description | Selected |
|--------|-------------|----------|
| Asset definition fingerprint | Hash `(name + sorted Upstreams + ColumnLineage map + Schema spec + metadata)` at builder.Register(). Stable fingerprint of the *declared* asset. | ✓ |
| Source hash of MaterializeFunc | `runtime.FuncForPC` + embed source. Brittle when functions move. | |
| User-managed asset version | `.Version("v3")` builder method; user manually bumps. | |

**User's choice:** Asset definition fingerprint
**Notes:** Carried D-03. Note: catches declaration drift, not "SQL inside Materialize changed but declarations unchanged" — that compensating mechanism is D-04 (drift via captured-Schema mismatch).

### Drift detection action

| Option | Description | Selected |
|--------|-------------|----------|
| Warn + flag, run succeeds | Emit `lineage.drift_detected` event, set `drift_pending`, run completes normally. | ✓ |
| Fail the run | Hard error — forces engineer to update declarations before pipeline runs again. | |
| Auto-update from observed Schema | Platform overwrites declarations to match observed; erases user's intent record. | |

**User's choice:** Warn + flag, run succeeds
**Notes:** Carried D-04. Aligns with Phase 1 D-09 "fail loudly but don't block production".

---

## Schema capture mechanism

### Primary capture mechanism

| Option | Description | Selected |
|--------|-------------|----------|
| Connector DescribeSchema() | Add `DescribeSchema(ctx, AssetRef) (Schema, error)` capability. Source of truth: warehouse itself. | ✓ |
| Explicit MaterializeResult.Schema | User's Materialize returns the schema. Misses out-of-band DDL. | |
| Infer from connector.Row | Walk rows, synthesize from Go reflect. Brittle on empty results, mixed types. | |

**User's choice:** Connector DescribeSchema()
**Notes:** Carried D-05.

### CONN-08 backwards-compatibility strategy

| Option | Description | Selected |
|--------|-------------|----------|
| Optional capability interface | Define `connector.SchemaDescriber` as separate optional interface; type-assert at runtime. | ✓ |
| Required method on connector.Connector | Hard-add to existing interface. Breaks every existing third-party connector. | |
| Versioned interface (Connector + ConnectorV2) | Plugin manager loads either. Premature. | |

**User's choice:** Optional capability interface
**Notes:** Carried D-06. Establishes capability pattern for future Phase 5 RBAC/masking interfaces.

### Captured Schema shape

| Option | Description | Selected |
|--------|-------------|----------|
| Rich: name+type+nullable+default+pk+comment | Per-column rich shape + table-level (PrimaryKey, RowCountEstimate). | ✓ |
| Minimal: name+type only | Just `(name, type)`. Smallest storage. | |
| Pluggable: connector returns native + normalized | `{NativeSchema: any, NormalizedSchema: Schema}`. Doubles complexity. | |

**User's choice:** Rich: name+type+nullable+default+pk+comment
**Notes:** Carried D-07. Sufficient to detect every breaking-change category in D-09 and seed META-03 column descriptions from comments.

### Capture frequency

| Option | Description | Selected |
|--------|-------------|----------|
| Every successful run, dedup on hash | Run DescribeSchema every successful run; only insert new schema_versions row if hash differs. | ✓ |
| Only on first run + opt-in re-capture | Capture on first materialization; skip subsequent unless `.RecaptureSchema()` annotated. Misses out-of-band DDL. | |
| Sampled: every Nth run + on-error | Capture every Nth run + on failure. Compromises META-01. | |

**User's choice:** Every successful run, dedup on hash
**Notes:** Carried D-08. DescribeSchema error: log + emit `schema.capture_failed` event, run still succeeds.

---

## Schema diff + breaking rules

### Breaking-change classification

| Option | Description | Selected |
|--------|-------------|----------|
| Drop, narrow, NULL→NOT NULL, PK change | Aligns with most warehouse ABI rules; matches what downstream code actually breaks on. | ✓ |
| Conservative: any change is breaking | Easy rule, but alert fatigue from every column addition. | |
| User-defined per asset | Each asset declares its compatibility policy. Premature. | |

**User's choice:** Drop, narrow, NULL→NOT NULL, PK change
**Notes:** Carried D-09. Renames detected as `(drop, add)` — no heuristic in v1. Type narrowing rules per connector type-lattice.

### Intentional break override mechanism

| Option | Description | Selected |
|--------|-------------|----------|
| Yes: ack-via-API + audit trail | New CLI/REST `./platform schema ack-break <id> --reason="..."`. Sets `acknowledged_at`/`acknowledged_by`. | ✓ |
| Yes: at declaration in code | `.IntentionalBreak("v3-drop-legacy-id", ...)` builder method. Heavy ergonomics. | |
| No — every break is permanent record | Cleanest data model. Alert fatigue, governance teams write filter SQL. | |

**User's choice:** Yes: ack-via-API + audit trail
**Notes:** Carried D-10. Ack is additive — row never deleted. Reason free-text but required.

### Schema-change record storage

| Option | Description | Selected |
|--------|-------------|----------|
| Dedicated table + event_log entries | `schema_changes` table for queries; `schema.change_detected` event with payload pointer. Two stores, no payload duplication. | ✓ |
| event_log only | Encode every diff as event with full payload. Slower JSON-extraction; loses ack workflow. | |
| Dedicated table only | Misses Phase 1 D-09 audit-immutability promise. | |

**User's choice:** Dedicated table + event_log entries
**Notes:** Carried D-11. `schema_versions` is third table — full snapshots pointed to by `schema_changes.{prev,new}_version_id`.

### META-05 timeline computation

| Option | Description | Selected |
|--------|-------------|----------|
| Derive from schema_changes rows | Timeline = `SELECT * FROM schema_changes WHERE asset=$1 AND column_name=$2`. Single source. | ✓ |
| Materialized column_history table | Faster for very long histories, but no evidence of need at v1 volume. | |
| Reconstruct from schema_versions snapshots | Walk rows, diff at query time. Already rejected by capture-frequency dedup design. | |

**User's choice:** Derive from schema_changes rows
**Notes:** Carried D-12. Materialized rollup deferred until query perf data motivates.

---

## Storage schema + traversal

### Adjacency table layout

| Option | Description | Selected |
|--------|-------------|----------|
| Split: asset_edges + column_edges | Two tables — asset traversal hits small table. Cleaner indices. Pitfall #4 mitigation by structure. | ✓ |
| Unified: single edges table with column nullable | One `lineage_edges` with NULL for asset-level. Pitfall #4 specifically warns this design. | |
| Star: nodes + edges with type column | Most normalized; cheapest edges; adds JOIN to every traversal. | |

**User's choice:** Split: asset_edges + column_edges
**Notes:** Carried D-13.

### Traversal API + safety guards

| Option | Description | Selected |
|--------|-------------|----------|
| Recursive CTE + hard depth cap (default 10, max 25) | PostgreSQL `WITH RECURSIVE`. Bidirectional indexed. Hard ceiling 25 for DoS prevention. | ✓ |
| Recursive CTE no cap, app filters | Server returns full closure; client filters depth. Pitfall #4 explicitly warns: 'no depth limit is a DoS vector'. | |
| Application-side BFS over edges table | Each hop is network round-trip. Pitfall #4 explicitly recommends recursive CTE. | |

**User's choice:** Recursive CTE + hard depth cap (default 10, max 25)
**Notes:** Carried D-14. EXPLAIN ANALYZE captured during planning.

### Edge versioning when re-materialization changes lineage

| Option | Description | Selected |
|--------|-------------|----------|
| Soft-retire: keep, mark superseded_at | Temporal table pattern: `(first_seen_*, last_seen_*, superseded_at)`. Active queries filter `superseded_at IS NULL`; point-in-time queries filter on time range. | ✓ |
| Hard-replace: delete and reinsert per run | Lineage history lost. | |
| Version per code_hash: parallel edge sets | Every API call must specify code_hash. Over-engineered. | |

**User's choice:** Soft-retire: keep, mark superseded_at
**Notes:** Carried D-15. Phase 1 D-09 immutability extends to lineage edges (no DELETE).

### Query management split

| Option | Description | Selected |
|--------|-------------|----------|
| sqlc for traversal hot reads | Recursive CTEs in `queries/lineage.sql`; sqlc generates type-safe Go bindings. ent for CRUD. Matches CLAUDE.md tech stack. | ✓ |
| ent + manual recursive CTE in raw SQL | Bypasses type safety. CLAUDE.md endorses sqlc. | |
| Pure ent traversal helpers | Ent doesn't natively support recursive CTEs. Slow. | |

**User's choice:** sqlc for traversal hot reads
**Notes:** Carried D-16. Pattern matches Phase 2's `internal/run/claim.go`.

---

## Metadata mutation API (META-03)

### Setting description / owners / tags

| Option | Description | Selected |
|--------|-------------|----------|
| Both: code default + runtime mutable | Builder methods declare defaults bound to code_hash; REST PATCH endpoints allow runtime overrides. Audit via event_log. | ✓ |
| Code-declared only | Cleanest provenance, but governance teams cannot self-serve. Breaks PROJECT.md core value. | |
| Runtime only | Maximum flexibility, but every fresh asset starts empty. No version-controlled provenance. | |

**User's choice:** Both: code default + runtime mutable
**Notes:** Carried D-17. Resolves PROJECT.md tension: engineers ship defaults; governance corrects without redeploys.

---

## OpenLineage compatibility

### Emission strategy

| Option | Description | Selected |
|--------|-------------|----------|
| Hybrid: internal storage, OL on export | Native internal schema; OL JSON generated on demand by `./platform lineage export --format=openlineage`. No runtime dependency on `ThijsKoot/openlineage-go` (LOW credibility). | ✓ |
| OL-compat from day one (in-flight) | Emit OpenLineage RunEvent JSON to per-event sink. Premature interop investment. | |
| Internal only, no OL until requested | Skip OL entirely. Hybrid achieves same outcome with one lazy translator. | |

**User's choice:** Hybrid: internal storage, OL on export
**Notes:** Carried D-18.

---

## Impact analysis API surface (LINE-06)

### API surface

| Option | Description | Selected |
|--------|-------------|----------|
| REST + CLI, both wrap same Go package | `internal/lineage/impact.go` exposes library; REST `GET /lineage/impact`; CLI `./platform impact ...`. Three surfaces, one logic. | ✓ |
| REST only | Phase 4 has no UI; operators must curl. | |
| CLI only, REST in Phase 6 | External alerting bots need REST. Marginal scope savings. | |

**User's choice:** REST + CLI, both wrap same Go package
**Notes:** Carried D-19.

### Direction handling

| Option | Description | Selected |
|--------|-------------|----------|
| Direction parameter, single endpoint | `?direction=downstream|upstream` on `/lineage/impact`. Same recursive CTE template. | ✓ |
| Two endpoints (impact / dependencies) | Doubles surface area for the same logic. | |
| Impact only (downstream); upstream is Phase 6 | Operators investigating failures almost always want upstream too. | |

**User's choice:** Direction parameter, single endpoint
**Notes:** Carried D-20.

---

## Claude's Discretion

(See CONTEXT.md "Claude's Discretion" subsection for the full list.)

- Exact JSONB shape of `Schema.Columns` (round-trip stability for hash; field ordering inside `Column` is implementation detail).
- Whether the asset definition fingerprint hashes Description/Owner/Tags (leaning yes, may revise during planning).
- Exact `schema_changes.change_type` enum values.
- CLI output format for `./platform impact` and `./platform schema diff`.
- Whether `AssetVersion` is its own ent entity or implied by `(asset, code_hash)` join.
- `MaterializeResult.ColumnLineage` Go type shape (map vs slice).
- Whether OL export endpoint lives at `/lineage/export` or `/exports/lineage`.
- Number of public-API breaking-change categories.
- Whether `connector.SchemaDescriber` returns `(Schema, Diagnostics, error)` instead of `(Schema, error)`.

## Deferred Ideas

(See CONTEXT.md `<deferred>` section for the full list, including PII tag propagation, ALINE-01 SQL inference, ALINE-02 OL ingestion, heuristic rename detection, per-asset compatibility policy, materialized column_history rollup, schema-inference fallback for non-introspectable connectors, partition-aware lineage edges, asset version diff REST endpoint.)
