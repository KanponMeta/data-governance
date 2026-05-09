# Phase 4: 血缘与 Schema - Research

**Researched:** 2026-05-08
**Domain:** Lineage capture, Schema diff/versioning, impact analysis, metadata mutation — all in-process Go, PostgreSQL storage
**Confidence:** HIGH (codebase verified, decisions locked in 04-CONTEXT.md, patterns confirmed from Phases 1-3)

---

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions
- **D-01** Asset-level lineage captured two ways: static derivation into `asset_edges` at registry time + run-attributed `lineage.captured` event per successful materialization. Drift detected when run-observed upstreams diverge from static set.
- **D-02** Column-level lineage declared two ways; runtime `MaterializeResult.ColumnLineage` wins over builder default. Undeclared tagged `column_lineage_undeclared`, never synthesized.
- **D-03** Code-hash = SHA-256 of canonical JSON of `(asset.Name, sorted Upstreams, ColumnLineage, declared Schema spec, declared Description/Owner/Tags)`. Computed at `builder.Register()`.
- **D-04** Lineage/column drift detected → run succeeds, emit `lineage.drift_detected`, set `asset_versions.drift_status='pending'`. No auto-update.
- **D-05** Schema captured via optional `connector.SchemaDescriber` interface (new, separate from `Connector`). Source-of-truth is the warehouse itself.
- **D-06** CONN-08 preserved: `SchemaDescriber` is a separate optional interface. Runtime type-assert `if d, ok := conn.(connector.SchemaDescriber); ok`. Fallback: `MaterializeResult.Schema` if set; else tag `schema_capture_unsupported`.
- **D-07** Schema struct: `{Columns []Column, PrimaryKey []string, RowCountEstim int64, CapturedAt time.Time}`. Column: `{Name, Type, Nullable bool, Default *string, IsPrimaryKey bool, Comment string}`.
- **D-08** Schema dedup by hash: if hash matches latest `schema_versions` row, UPDATE `last_seen_*` only. If changed: insert new row, diff, emit `schema_changes` + `schema.change_detected`.
- **D-09** Breaking: column dropped; type narrowed; existing nullable→not-null; PK composition changed. Non-breaking: column added; type widened; not-null→nullable; comment/default change. Renames = (drop, add). Out-of-lattice type changes default breaking.
- **D-10** Ack: `schema_changes.acknowledged_at/by/reason` added (never deleted). CLI: `./platform schema ack-break <asset> <change_id> --reason="..."`. REST: `POST /schema/changes/:id/ack`.
- **D-11** `schema_changes` table (queryable) + `schema.change_detected` event_log pointer. `schema_versions` for full Schema snapshots.
- **D-12** META-05 timeline = `SELECT * FROM schema_changes WHERE asset=$1 AND column_name=$2 ORDER BY observed_at`. No separate `column_history` table.
- **D-13** Split adjacency tables: `asset_edges` (asset→asset) and `column_edges` (column→column). Partial indices `WHERE superseded_at IS NULL`.
- **D-14** Traversal: PostgreSQL `WITH RECURSIVE`, default depth 10, hard ceiling 25 (400 if >25).
- **D-15** Edges soft-retired: `superseded_at` set when edge no longer present. Never deleted.
- **D-16** sqlc owns recursive CTE traversals (`internal/lineage/queries/`). ent owns CRUD for new entities.
- **D-17** Metadata: builder defaults stored on `asset_versions`, runtime overrides in `asset_metadata`. Read: `COALESCE(runtime, code_default)`. PATCH emits `metadata.updated` event.
- **D-18** OpenLineage hybrid: internal storage native, export on demand via in-house translator (`internal/lineage/openlineage/`). No runtime dependency on ThijsKoot/openlineage-go.
- **D-19** Impact analysis: `internal/lineage/impact.Analyze(ctx, ImpactQuery) (Impact, error)` + REST `GET /lineage/impact` + CLI `./platform impact`.
- **D-20** Same CTE template parameterized on `direction='upstream'|'downstream'`.
- **D-21** New event_types: `lineage.captured`, `lineage.drift_detected`, `schema.captured`, `schema.unchanged`, `schema.change_detected`, `schema.capture_failed`, `schema.break_acknowledged`, `metadata.updated`. All follow Phase 1 D-09 RLS-immutability.

### Claude's Discretion
- Exact JSONB field ordering inside `Column` (must round-trip stably for hashing).
- Whether D-03 fingerprint includes Description/Owner/Tags (leaning yes; research below recommends YES with a metadata-only flag to suppress drift alarm for pure-metadata changes).
- Exact `schema_changes.change_type` enum values (recommend: `column_added`, `column_dropped`, `type_narrowed`, `type_widened`, `nullable_added`, `nullable_removed`, `pk_changed`, `comment_changed`, `default_changed`).
- CLI output format for `./platform impact` and `./platform schema diff` — default table, `--format=json`.
- Whether `AssetVersion` is its own ent entity or a row group by `(asset, code_hash)`.
- Whether `MaterializeResult.ColumnLineage` is `map[string][]ColumnRef` or typed slice (recommend map).
- Whether OL export endpoint lives at `/lineage/export` or `/exports/lineage` (recommend `/lineage/export`).
- Number of breaking-change categories exposed publicly (minimum `breaking|non_breaking|needs_review`; granular internal).
- Whether `SchemaDescriber` returns `(Schema, error)` or `(Schema, Diagnostics, error)` (recommend the simpler form for v1).

### Deferred Ideas (OUT OF SCOPE)
- PII tag propagation through lineage (Phase 5).
- SQL-AST-based column-lineage inference (ALINE-01, v2).
- OpenLineage event ingestion from external tools (ALINE-02, v2).
- Heuristic rename detection (D-09 calls renames drop+add).
- Per-asset compatibility policy override.
- Materialized `column_history` rollup (D-12: derive from schema_changes).
- ThijsKoot/openlineage-go vendoring (D-18: in-house translator).
- Schema inference fallback for non-introspectable connectors.
- Granular change_type exposed via REST (internal only).
- Per-column statistics (Phase 5 data quality).
- Partition-aware lineage edges at column granularity (v1: asset-level with optional partition_key context).
- Asset version diff REST endpoint (Phase 6).
- MaterializeResult schema_capture_supported flag (v1.x polish).
</user_constraints>

---

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| LINE-01 | Auto-capture asset→asset lineage edges from declared upstreams | D-01/D-13/D-15: static derivation at registry + run-attributed event; Section §3 lineage writer hook in executor |
| LINE-02 | Column-level lineage declared in code, version-bound to code_hash | D-02/D-03: builder DSL + MaterializeResult.ColumnLineage typed field; Section §5 builder DSL |
| LINE-03 | Lineage stored as adjacency table, traversable via recursive CTE | D-13/D-14/D-16: split tables, sqlc CTE queries; Section §4 CTE patterns |
| LINE-06 | Impact analysis API: upstream + downstream traversal | D-19/D-20: impact.Analyze library + REST + CLI; Section §4 impact API |
| META-01 | Schema captured at every materialization | D-05/D-06/D-08: SchemaDescriber capability + dedup on hash; Section §6 schema capture |
| META-02 | Schema diff with breaking/non-breaking classification | D-09/D-11: classifier rules + schema_changes table; Section §7 diff classifier |
| META-03 | Descriptions/owners/tags settable via code or REST | D-17: asset_metadata table + PATCH endpoints; Section §8 metadata API |
| META-05 | Per-column timeline from schema_changes | D-12: derived query, no separate table; Section §7 timeline derivation |
</phase_requirements>

---

## Summary

Phase 4 extends the execution kernel with three interlocking subsystems: **lineage capture** (asset and column graph edges), **Schema versioning** (capture, diff, breaking-change classification, acknowledgement), and **metadata mutation** (descriptions/owners/tags). All three write synchronously in the run-update transaction and use the Phase 1 event_log as an immutable audit pointer while storing queryable state in dedicated tables.

The implementation pattern is consistent across all three subsystems: ent entities own row CRUD; sqlc owns the performance-critical read queries (recursive CTE traversal, impact analysis, timeline aggregation); Atlas handles migrations following the hand-managed appendix pattern from Phases 1-3. No new runtime Go dependencies are needed — the existing `encoding/json`, `crypto/sha256`, `pgx/v5`, `ent`, and `sqlc` are sufficient.

The most technically involved areas are: (1) the canonical-JSON fingerprint for D-03 code-hash stability, (2) the recursive CTE traversal with bidirectional direction parameter (D-14/D-20), (3) the breaking-change classifier type lattice for PostgreSQL types (D-09), and (4) wiring the lineage/schema capture writers into `executor.runStep` at the correct transactional boundary (D-21 crash-recovery guarantee).

**Primary recommendation:** Build in wave order — Migration + ent entities → Builder DSL extensions → Lineage capture hook in executor → Schema capture → Diff classifier → Impact API → Metadata REST → CLI subcommands → OpenLineage export.

---

## Standard Stack

### Core (no new dependencies required)

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `entgo.io/ent` | v0.14.0 (go.mod) | New ent entities for 6 Phase 4 tables | Already in-use; graph-entity model maps directly to lineage/schema/metadata |
| `github.com/sqlc-dev/sqlc` | v1.31.x (tool) | Recursive CTE traversal queries, impact analysis reads | Already the pattern for hot reads (claim.go precedent); generates typed Go from SQL |
| `crypto/sha256` + `encoding/json` | stdlib | Code-hash fingerprint (D-03) + schema hash (D-08) | Zero new deps; deterministic with sorted keys |
| `pgx/v5` | v5.9.1 (go.mod) | Raw SQL execution for lineage writer txn | Already the primary driver |
| `ariga.io/atlas` | indirect in go.mod | Phase 4 migration file generation | Established pattern; applies hand-managed appendix |
| `github.com/jackc/pgx/v5` | v5.9.1 | Recursive CTE execution (sqlc-generated code uses pgx rows) | Already wired |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `log/slog` | stdlib | Structured logging for lineage/schema capture errors | Non-fatal capture failures (D-04, D-08) |
| `encoding/json` | stdlib | OpenLineage export translator (D-18) JSON marshaling | `/lineage/export` endpoint |
| `github.com/go-chi/chi/v5` | v5.2.5 | New REST routes `/lineage/*`, `/schema/*`, `/assets/:name/metadata` | Already router |

### No New Runtime Dependencies

Phase 4 deliberately adds **zero new `require` entries to go.mod**:
- Code-hash: `encoding/json` + `crypto/sha256` (stdlib) [VERIFIED: codebase uses both already]
- OpenLineage export: hand-written struct marshaling (D-18 explicitly avoids ThijsKoot/openlineage-go) [VERIFIED: CLAUDE.md LOW credibility flag]
- Schema diff: pure Go logic, no external library needed
- Recursive CTE: sqlc-generated pgx bindings, no graph library

---

## Architecture Patterns

### Recommended Package Structure (new in Phase 4)

```
internal/
├── lineage/
│   ├── capture.go          # LineageWriter — called from executor after successful step
│   ├── impact.go           # impact.Analyze(ctx, ImpactQuery) (Impact, error)
│   ├── openlineage/
│   │   └── translate.go    # RunEvent translator; no external dep
│   └── queries/
│       ├── lineage.sql     # sqlc source: recursive CTE + edge upserts
│       └── db.go           # sqlc-generated Go bindings (do not edit)
├── schema/
│   ├── capture.go          # SchemaWriter — calls SchemaDescriber, deduplicates by hash
│   ├── diff.go             # Diff(prev, next Schema) []SchemaChange
│   ├── classifier.go       # Classify(change SchemaChange, lattice TypeLattice) BreakKind
│   └── lattice_postgres.go # PostgreSQL type widening/narrowing rules
├── metadata/
│   ├── store.go            # AssetMetadata read/write (COALESCE logic)
│   └── handler.go          # chi REST handlers for PATCH /assets/:name/metadata
├── storage/ent/schema/
│   ├── asset_version.go    # NEW ent schema
│   ├── asset_edge.go       # NEW ent schema
│   ├── column_edge.go      # NEW ent schema
│   ├── schema_version.go   # NEW ent schema
│   ├── schema_change.go    # NEW ent schema
│   └── asset_metadata.go   # NEW ent schema
└── ...
cmd/platform/
├── impact.go               # NEW: ./platform impact subcommand
├── schema.go               # NEW: ./platform schema ack-break subcommand
└── lineage.go              # NEW: ./platform lineage export subcommand
migrations/
└── 2026MMDDHHMMSS_phase4_lineage_schema.sql  # asset_edges, column_edges, schema_versions, schema_changes, asset_versions, asset_metadata + RLS + indices
```

### Pattern 1: ent Entity Registration (no client breakage)

New ent schemas register by adding files to `internal/storage/ent/schema/` and re-running `go generate`. The existing `client.go` is regenerated — no manual edit.

```go
// internal/storage/ent/schema/asset_edge.go
// Source: established ent pattern from internal/storage/ent/schema/run.go
package schema

import (
    "entgo.io/ent"
    "entgo.io/ent/dialect/entsql"
    "entgo.io/ent/schema"
    "entgo.io/ent/schema/field"
    "entgo.io/ent/schema/index"
    "github.com/google/uuid"
)

type AssetEdge struct{ ent.Schema }

func (AssetEdge) Annotations() []schema.Annotation {
    return []schema.Annotation{entsql.Annotation{Table: "asset_edges"}}
}

func (AssetEdge) Fields() []ent.Field {
    return []ent.Field{
        field.UUID("id", uuid.UUID{}).Default(uuid.New),
        field.String("from_asset").NotEmpty().MaxLen(256).Immutable(),
        field.String("to_asset").NotEmpty().MaxLen(256).Immutable(),
        field.String("code_hash_first").NotEmpty().MaxLen(64).Immutable(),
        field.String("code_hash_latest").NotEmpty().MaxLen(64),
        field.UUID("first_seen_run_id", uuid.UUID{}).Immutable(),
        field.Time("first_seen_at").Immutable(),
        field.UUID("last_seen_run_id", uuid.UUID{}),
        field.Time("last_seen_at"),
        field.Time("superseded_at").Optional().Nillable(),
    }
}

func (AssetEdge) Indexes() []ent.Index {
    return []ent.Index{
        index.Fields("from_asset"),
        index.Fields("to_asset"),
        // NOTE: partial index WHERE superseded_at IS NULL must be hand-managed
        // in the SQL appendix (ent has no WHERE-clause index support).
    }
}
```

**Key insight:** ent regeneration after adding new schema files does NOT touch existing entity code — it only adds new `*_create.go`, `*_query.go` etc. files and regenerates `client.go` to expose the new entity builders. [VERIFIED: ent documentation pattern, consistent with existing schema/]

### Pattern 2: sqlc Dual-ORM Split (D-16 precedent from claim.go)

`internal/run/claim.go` uses raw SQL for `SELECT FOR UPDATE SKIP LOCKED` while ent handles Run entity CRUD elsewhere. Phase 4 follows the same split for lineage traversal:

```sql
-- internal/lineage/queries/lineage.sql
-- Source: D-14, D-20 decisions; PostgreSQL WITH RECURSIVE docs

-- name: TraverseAssetLineage :many
WITH RECURSIVE lineage AS (
    -- base: the starting asset
    SELECT
        CASE WHEN @direction::text = 'downstream'
             THEN to_asset
             ELSE from_asset END AS asset,
        0 AS depth,
        ARRAY[CASE WHEN @direction::text = 'downstream'
                   THEN from_asset
                   ELSE to_asset END] AS path
    FROM asset_edges
    WHERE CASE WHEN @direction::text = 'downstream'
               THEN from_asset = @asset::text
               ELSE to_asset   = @asset::text END
      AND superseded_at IS NULL

    UNION ALL

    -- recursive: follow the graph one hop
    SELECT
        CASE WHEN @direction::text = 'downstream'
             THEN e.to_asset
             ELSE e.from_asset END,
        l.depth + 1,
        l.path || CASE WHEN @direction::text = 'downstream'
                        THEN e.to_asset
                        ELSE e.from_asset END
    FROM asset_edges e
    JOIN lineage l ON (
        CASE WHEN @direction::text = 'downstream'
             THEN e.from_asset = l.asset
             ELSE e.to_asset   = l.asset END
    )
    WHERE e.superseded_at IS NULL
      AND l.depth < @max_depth::int   -- hard cap from caller (never > 25)
      AND NOT (l.path @> ARRAY[
              CASE WHEN @direction::text = 'downstream'
                   THEN e.to_asset
                   ELSE e.from_asset END
          ])  -- cycle guard
)
SELECT DISTINCT asset, depth FROM lineage ORDER BY depth, asset;
```

**sqlc yaml config addition** (`sqlc.yaml`):
```yaml
  - path: internal/lineage/queries/lineage.sql
    queries: internal/lineage/queries/lineage.sql
    schema:  migrations/
    engine:  postgresql
    gen:
      go:
        package: lineageq
        out:     internal/lineage/queries/
```

**Critical note:** sqlc v1.31.x supports `@param::type` casting and `ARRAY` operations for PostgreSQL. The `@>` operator (array contains) requires `::text[]` cast on the array literal. [ASSUMED — sqlc array operator support; verify with `sqlc generate` during Wave 0]

### Pattern 3: Code-Hash Fingerprint (D-03)

Canonical-JSON encoding must be **deterministic across Go versions and map iteration**. `encoding/json` does NOT guarantee stable map key ordering by default for `map[string]any`. The solution is to marshal a struct with explicit field ordering, never a raw map.

```go
// internal/asset/fingerprint.go
package asset

import (
    "crypto/sha256"
    "encoding/json"
    "fmt"
    "sort"
)

// assetFingerprint is the canonical struct whose JSON is hashed (D-03).
// Field order in this struct IS the stable ordering — encoding/json marshals
// struct fields in declaration order, not sorted. Map fields must be pre-sorted.
type assetFingerprint struct {
    Name         string                       `json:"name"`
    Upstreams    []string                     `json:"upstreams"`    // caller must sort before passing
    ColumnLineage map[string][]columnRefFingerprint `json:"column_lineage,omitempty"` // keys iterated in sorted order via sortedColumnLineage()
    SchemaSpec   *schemaSpecFingerprint       `json:"schema_spec,omitempty"`
    Description  string                       `json:"description,omitempty"` // see Discretion note
    Owner        string                       `json:"owner,omitempty"`
    Tags         []string                     `json:"tags,omitempty"`  // caller must sort
}

type columnRefFingerprint struct {
    Asset  string `json:"asset"`
    Column string `json:"column"`
}

type schemaSpecFingerprint struct {
    Columns []string `json:"columns"` // sorted column names if user declared schema spec
}

// ComputeCodeHash returns the SHA-256 hex of the canonical JSON fingerprint.
// Upstreams, Tags, and ColumnLineage keys are sorted by the caller BEFORE passing.
func ComputeCodeHash(fp assetFingerprint) string {
    // For ColumnLineage map: iterate keys in sorted order to produce stable JSON.
    // encoding/json marshals struct fields in order, but map keys in sorted order
    // only as of Go 1.12+ for map[string]T. Verify this holds for nested slices.
    b, err := json.Marshal(fp)
    if err != nil {
        panic(fmt.Sprintf("asset: fingerprint marshal: %v", err)) // struct — cannot fail
    }
    h := sha256.Sum256(b)
    return fmt.Sprintf("%x", h)
}
```

**Go 1.12+ guarantee:** `encoding/json` marshals `map[string]T` keys in sorted order [VERIFIED: Go stdlib encoding/json source — sort.Strings applied before marshal]. This means `map[string][]ColumnRef` in `assetFingerprint.ColumnLineage` will produce stable JSON. No custom sorting needed for the map itself, but the `[]ColumnRef` slices and `Upstreams []string` must be pre-sorted by the caller at `builder.Register()`.

**Discretion resolution:** Include Description/Owner/Tags in the fingerprint. Rationale: a code_hash change signals "the developer's declared intent changed." A metadata-only edit producing a new code_hash is acceptable — it creates a new `asset_versions` row (audit trail of the change) and does NOT trigger `lineage.drift_detected` (drift detection compares observed-upstreams vs declared-upstreams, not code_hash deltas). The fingerprint including metadata means the governance team's "who owns this?" question is version-controlled.

### Pattern 4: Optional Capability Interface (D-06 — first instance of this pattern)

```go
// internal/connector/connector.go (additive — CONN-08 preserved)

// SchemaDescriber is an optional capability interface (D-05/D-06). Connectors
// that can introspect the output table after a materialization implement this.
// Phase 4 type-asserts at runtime; the base Connector interface is NOT modified.
// Phase 5 will add SchemamaskerCapability and RBACCapability following this pattern.
type SchemaDescriber interface {
    // DescribeSchema returns the current Schema of the asset's output table as
    // observed from the warehouse. Called after a successful materialization.
    // Errors are non-fatal (D-08: schema capture failure emits event, run succeeds).
    DescribeSchema(ctx context.Context, ref AssetRef) (asset.Schema, error)
}
```

**Type assertion pattern in executor (D-06)**:
```go
// internal/runtime/executor.go — after result, runErr := safeMaterialize(...)
// Only called when runErr == nil (successful materialization).

if schemaWriter != nil { // injected into Executor.Deps
    conn, _, _ := e.deps.ConnectorReg.Get(a.ConnectorName())
    ref := connector.AssetRef{Identifier: a.Name()}
    if err := schemaWriter.Capture(ctx, runID, a, result, conn, ref); err != nil {
        // Non-fatal: log error, emit schema.capture_failed event (D-08)
        slog.Warn("executor.schema_capture_failed", "asset", a.Name(), "error", err)
    }
}
```

**Note on import cycle:** `connector.SchemaDescriber` references `asset.Schema`. To avoid an import cycle (`connector` importing `asset`), move the `Schema` / `Column` types to a new shared package `internal/schema/types.go` OR keep them in `connector` and have `asset.Builder` produce a `connector.Schema` in D-03 fingerprint. Recommend: keep Schema types in `connector` package (already has `Column`, `SchemaResponse`), and Phase 4 promotes `connector.SchemaResponse` to the richer `connector.Schema` struct (D-07). [ASSUMED — import graph; verify with `go build ./...` after type moves]

### Pattern 5: Migration SQL Appendix (hand-managed, following Phase 1-3 convention)

The Phase 4 migration file follows the established pattern: ent-generated table DDL first, then a `-- ===== Hand-managed =====` block for partial indices, CHECK constraints, RLS grants.

```sql
-- migrations/2026MMDDHHMMSS_phase4_lineage_schema.sql (partial example)

-- === ent-generated tables ===
CREATE TABLE "asset_edges" ( ... );
CREATE TABLE "column_edges" ( ... );
CREATE TABLE "schema_versions" ( ... );
CREATE TABLE "schema_changes" ( ... );
CREATE TABLE "asset_versions" ( ... );
CREATE TABLE "asset_metadata" ( ... );

-- ===== Hand-managed: partial indices (ent has no WHERE-clause support) =====

-- D-13 active-edges hot path
CREATE INDEX IF NOT EXISTS asset_edges_active_from
  ON asset_edges (from_asset) WHERE superseded_at IS NULL;
CREATE INDEX IF NOT EXISTS asset_edges_active_to
  ON asset_edges (to_asset) WHERE superseded_at IS NULL;

CREATE INDEX IF NOT EXISTS column_edges_active_from
  ON column_edges (from_asset, from_column) WHERE superseded_at IS NULL;
CREATE INDEX IF NOT EXISTS column_edges_active_to
  ON column_edges (to_asset, to_column) WHERE superseded_at IS NULL;

-- ===== Hand-managed: RLS for schema_changes (D-10/D-21 immutability) =====
ALTER TABLE schema_changes OWNER TO platform_owner;
GRANT SELECT, INSERT ON schema_changes TO platform_app;
REVOKE UPDATE, DELETE, TRUNCATE ON schema_changes FROM platform_app;
-- Allow UPDATE only for the ack columns (acknowledged_at, acknowledged_by, reason).
-- Implementation: a separate UPDATE-only policy scoped to those columns.
-- Because PostgreSQL RLS column-level UPDATE policies are not trivial, the simpler
-- approach is: the ack operation goes through a dedicated DB function (or a separate
-- ent mutation that only touches ack fields) executed as platform_owner.
-- Alternative (recommended): allow platform_app UPDATE WHERE acknowledged_at IS NULL
-- on only the ack columns via a SECURITY DEFINER function. See §9 crash-recovery notes.

ALTER TABLE schema_versions OWNER TO platform_owner;
GRANT SELECT, INSERT ON schema_versions TO platform_app;
REVOKE UPDATE, DELETE, TRUNCATE ON schema_versions FROM platform_app;
-- Exception: UPDATE last_seen_run_id, last_seen_at is needed for D-08 dedup.
-- Resolution: allow UPDATE for platform_app but only enforce immutability via
-- application-layer ent mutation (no RLS needed for the dedup columns since
-- they are not audit-sensitive). OR use a SECURITY DEFINER function.
-- Recommended: allow SELECT/INSERT/UPDATE on schema_versions for platform_app
-- (the immutability concern is on schema_changes rows, not version snapshots).

ALTER TABLE asset_edges OWNER TO platform_owner;
GRANT SELECT, INSERT, UPDATE ON asset_edges TO platform_app;
-- UPDATE needed for last_seen_* and superseded_at (D-15 soft-retire pattern).
-- No DELETE ever; no RLS needed beyond app-layer convention.

ALTER TABLE asset_metadata OWNER TO platform_owner;
GRANT SELECT, INSERT, UPDATE ON asset_metadata TO platform_app;

-- ===== Hand-managed: event_log RLS extension for new event types =====
-- D-21 new event_type values are valid CHECK constraint additions.
-- The existing event_log CHECK constraint (from Phase 1) must be extended.
-- Pattern: DROP old CHECK, ADD new CHECK with all known types.
ALTER TABLE event_log DROP CONSTRAINT IF EXISTS event_log_event_type_check;
ALTER TABLE event_log ADD CONSTRAINT event_log_event_type_check
  CHECK (event_type IN (
    -- existing Phase 1-3 types (copy verbatim) ...
    'user.registered', 'user.invited', 'auth.login', 'auth.logout',
    'auth.token_expired', 'platform.started', 'platform.migration_applied',
    'run.queued', 'run.started', 'run.step.started', 'run.step.succeeded',
    'run.step.failed', 'run.step.retry_scheduled', 'run.succeeded',
    'run.failed', 'run.canceled',
    'schedule.fired', 'schedule.missed', 'schedule.paused', 'schedule.resumed',
    'sensor.evaluated', 'sensor.fired', 'sensor.evaluation_failed',
    'sensor.disabled', 'sensor.cooldown_skipped', 'sensor.dedup_skipped',
    'backfill.submitted', 'backfill.run_enqueued', 'backfill.completed',
    -- Phase 4 additions (D-21)
    'lineage.captured', 'lineage.drift_detected',
    'schema.captured', 'schema.unchanged', 'schema.change_detected',
    'schema.capture_failed', 'schema.break_acknowledged',
    'metadata.updated'
  ));
```

**CRITICAL NOTE on event_log CHECK extension:** The existing `internal/event/types.go` uses an in-memory `AllKnownTypes()` slice that the writer validates against at runtime. Phase 4 must add the new constants AND update `AllKnownTypes()`. The migration CHECK constraint DROP+ADD is idempotent and safe in PostgreSQL (DDL is transactional). [VERIFIED: Phase 3 migration 20260508120000_phase3_runs_columns.sql uses same DROP CONSTRAINT IF EXISTS / ADD CONSTRAINT pattern]

### Anti-Patterns to Avoid

- **Using `map[string]any` iteration for canonical JSON hashing.** Go 1.12+ guarantees sorted keys for `map[string]T`, but only for top-level maps. Nested maps also get sorted. Confirmed safe.
- **Putting RLS DELETE block on schema_versions.** The `last_seen_at` dedup UPDATE (D-08) requires UPDATE access. Only `schema_changes` (the immutable ack trail) needs RLS immutability.
- **Adding lineage/schema capture OUTSIDE the run-update transaction.** D-21 explicitly says synchronous in same tx. If the lineage write fails, the entire run-update tx rolls back and the run stays in state `running` for the reaper to re-queue (crash recovery path). Do NOT capture lineage in a separate tx after the run update commits.
- **Modifying `connector.Connector` interface.** CONN-08 is frozen. `SchemaDescriber` is a separate interface. Any addition to `Connector` breaks all existing third-party connectors.

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Recursive graph traversal | Custom Go BFS/DFS | PostgreSQL `WITH RECURSIVE` (sqlc) | DB does it in one round-trip; custom BFS requires N+1 queries or loading entire graph |
| Canonical JSON for hashing | Custom marshaler | `encoding/json` struct marshaling + pre-sorted slices | Go stdlib guarantees stable output for structs and `map[string]T` (sorted keys) |
| Schema version storage | Rolling single row | `schema_versions` append-only table | Historical comparisons, point-in-time lineage ("as of 3 months ago") |
| Breaking-change detection | String matching heuristics | Typed type-lattice with explicit narrowing/widening rules | Edge cases: `varchar(255)` → `text` (widening), `decimal(10,4)` → `decimal(8,2)` (narrowing on both precision and scale) |
| Column timeline | Separate `column_history` table | `SELECT FROM schema_changes WHERE asset=$1 AND column_name=$2` | D-12: single source of truth; separate table means sync risk |
| OpenLineage JSON building | Import ThijsKoot/openlineage-go | In-house struct + `json.Marshal` | LOW credibility per CLAUDE.md; spec is simple enough (RunEvent ~15 fields) |

**Key insight:** The PostgreSQL recursive CTE is the right level of abstraction for lineage traversal in v1. It handles cycles (via `path @> ARRAY[...]` check), depth caps (via `depth < max_depth`), and bidirectional queries via parameterized join direction — all in one SQL template. Custom BFS in Go would require loading all edges into memory first (N+1 reads) or building a graph library abstraction that adds complexity without improving performance at v1 scale (<10K edges typical).

---

## Section §3: Executor Integration (LINE-01, LINE-02 hook points)

### Transaction Boundary (D-21 Crash Recovery)

The lineage capture writer and schema capture writer must execute **inside the same database transaction that updates `runs.state = 'succeeded'`**. This is the atomicity guarantee: if either write fails, the run stays in state `running` and the reaper re-queues it.

Current executor flow in `runStep`:
```
safeMaterialize() → result, runErr
appendEvent(RunStepSucceeded)
return nil
```

Phase 4 extended flow (after successful materialize, before returning nil):
```
safeMaterialize() → result, runErr (== nil)
[PHASE 4 HOOK: lineageWriter.Capture(ctx, tx, runID, asset, result)]
[PHASE 4 HOOK: schemaWriter.Capture(ctx, tx, runID, asset, result, conn, ref)]
appendEvent(RunStepSucceeded)
runs UPDATE state='succeeded' in same tx
tx.Commit()
```

**Implementation decision:** The run-state transition currently uses raw `ExecContext` on `store.DB()` without an explicit transaction (see `executor.transition`). Phase 4 needs to wrap `[lineage write + schema write + run state update]` in a single `BeginTx`. This requires adding a `db *sql.DB` or `pgxpool *pgxpool.Pool` to `Executor.Deps` and creating an explicit transaction in `runStep` after the materialize succeeds.

**Lineage_emitted column on runs:** Not needed if capture is in the same tx. If capture fails, tx rolls back and the run never reaches `succeeded`. The reaper re-queues it and the next attempt re-captures. [VERIFIED: consistent with D-21 "events emit synchronously in run-update transaction"]

### Static Edge Derivation (D-01 — at registry time)

`asset_edges` rows for the static upstreams must be inserted when an asset is first registered (or when the code_hash changes), not only on successful runs. This means the `LineageWriter.SyncStaticEdges(ctx, asset)` function is called from `DefinitionRegistry.Register()` rather than from the executor.

```go
// internal/lineage/capture.go
type LineageWriter struct {
    db *sql.DB // or pgxpool.Pool
}

// SyncStaticEdges upserts asset_edges for all declared upstreams.
// Called at registry time (D-01 static derivation).
func (w *LineageWriter) SyncStaticEdges(ctx context.Context, a *asset.Asset, codeHash string) error {
    // For each upstream in a.Upstreams():
    //   INSERT INTO asset_edges (from_asset, to_asset, code_hash_first, code_hash_latest, ...)
    //   ON CONFLICT (from_asset, to_asset) DO UPDATE
    //     SET code_hash_latest = EXCLUDED.code_hash_latest,
    //         last_seen_at = NOW()
    //     WHERE superseded_at IS NULL
    // Any edge NOT in the new upstreams: UPDATE SET superseded_at = NOW()
    return nil
}

// CaptureRun writes the run-attributed lineage event (D-01) within the provided tx.
func (w *LineageWriter) CaptureRun(ctx context.Context, tx *sql.Tx, runID uuid.UUID, a *asset.Asset, result asset.MaterializeResult, codeHash string) error {
    // 1. Write column_edges from result.ColumnLineage (runtime override) or a.ColumnLineage() (builder default)
    // 2. Detect drift: observed upstreams vs asset.Upstreams() → if differ, emit lineage.drift_detected
    // 3. Emit lineage.captured event in event_log (inside same tx via eventWriter)
    return nil
}
```

---

## Section §4: Recursive CTE Traversal (LINE-03, LINE-06)

### Complete sqlc Query Template

The bidirectional traversal uses a single query template parameterized on `direction`. The approach uses PostgreSQL boolean expressions in CASE statements — no dynamic SQL, fully preparable.

```sql
-- Downstream: who depends on @asset (impact of a change to @asset)?
-- Upstream:   what feeds @asset (root-cause investigation)?
-- max_depth:  caller enforces; must be <= 25 (400 Bad Request if exceeded)

-- For point-in-time queries (D-15, D-20 "as_of" parameter):
-- Replace `WHERE superseded_at IS NULL` with:
--   WHERE (first_seen_at <= @as_of AND (superseded_at IS NULL OR superseded_at > @as_of))
```

### EXPLAIN ANALYZE Notes (D-14 planning note)

For the active-edges hot path (`WHERE superseded_at IS NULL`), PostgreSQL will use the partial index `asset_edges_active_from` or `asset_edges_active_to`. The recursive step hits the same index. Expected plan: **Index Scan** (not Seq Scan) on the partial index, with a **Hash Join** or **Nested Loop** for the recursive CTE materialization step.

Red flags requiring investigation:
- Seq scan on `asset_edges` when `superseded_at IS NULL` clause is present → partial index not being used → run `ANALYZE asset_edges` or add explicit `WHERE superseded_at IS NULL` to index creation
- CTE materialization fence: PostgreSQL 12+ treats CTEs as optimization fences by default when they contain `UNION ALL`. For Phase 4, `MATERIALIZED` is acceptable (the CTE is small at v1 scale); can add `NOT MATERIALIZED` hint later if needed
- At >50K edges, consider adding `asset_edges(from_asset, to_asset) WHERE superseded_at IS NULL` covering index for the join column [ASSUMED — performance threshold; requires load testing to confirm]

### Impact API Surface (D-19, D-20)

```go
// internal/lineage/impact.go
package lineage

type ImpactQuery struct {
    Asset     string
    Column    *string    // nil = asset-level traversal only
    Direction string     // "upstream" | "downstream"
    Depth     int        // default 10; max 25
    AsOf      *time.Time // nil = current active edges
}

type ImpactNode struct {
    Asset  string
    Column *string
    Depth  int
}

type Impact struct {
    Root  ImpactQuery
    Nodes []ImpactNode
}

func Analyze(ctx context.Context, q *lineageq.Queries, query ImpactQuery) (Impact, error) {
    if query.Depth > 25 {
        return Impact{}, fmt.Errorf("lineage: depth %d exceeds hard limit 25", query.Depth)
    }
    if query.Depth <= 0 {
        query.Depth = 10
    }
    // Call sqlc-generated TraverseAssetLineage or TraverseColumnLineage
    // depending on whether query.Column is nil.
    ...
}
```

---

## Section §5: Builder DSL Extensions (LINE-02, META-03)

### Adding Methods Without Breaking Existing Call Sites

`Builder` is a fluent chain. New methods return `*Builder`. Existing call sites (`New("x").Upstream("y").Connector("z").Materialize(fn).Register()`) are unaffected because the new methods are purely additive.

```go
// internal/asset/builder.go additions

// ColumnRef declares a column-level lineage source (D-02).
type ColumnRef struct {
    Asset  string `json:"asset"`
    Column string `json:"column"`
}

// ColumnLineageMap maps output column name → source column references.
type ColumnLineageMap map[string][]ColumnRef

// ColumnLineage sets the builder-default column-level lineage (D-02).
// Runtime MaterializeResult.ColumnLineage overrides this per-run.
func (b *Builder) ColumnLineage(cl ColumnLineageMap) *Builder {
    b.a.columnLineage = cl
    return b
}

// Description sets the asset's human-readable description (D-17).
func (b *Builder) Description(desc string) *Builder {
    b.a.description = desc
    return b
}

// Owner sets the asset's declared owner (team email or group name) (D-17).
func (b *Builder) Owner(owner string) *Builder {
    b.a.owner = owner
    return b
}

// Tags sets the asset's declared tags (D-17). Multiple .Tags calls are NOT cumulative
// (last wins); use variadic to set all tags at once.
func (b *Builder) Tags(tags ...string) *Builder {
    b.a.tags = append([]string(nil), tags...) // defensive copy
    return b
}

// Column starts a column-level metadata declaration chain (D-17).
// Returns a *ColumnBuilder that scopes subsequent calls to the named column.
func (b *Builder) Column(name string) *ColumnBuilder {
    return &ColumnBuilder{asset: b, name: name}
}

// ColumnBuilder provides fluent column-level metadata (D-17).
type ColumnBuilder struct {
    asset *Builder
    name  string
}

func (cb *ColumnBuilder) Description(desc string) *ColumnBuilder {
    // Append to b.a.columnMeta[name]
    return cb
}

func (cb *ColumnBuilder) Tags(tags ...string) *ColumnBuilder {
    return cb
}

// And returns to the parent Builder chain.
func (cb *ColumnBuilder) And() *Builder { return cb.asset }
```

**Example usage (D-17):**
```go
asset.New("orders").
    Upstream("payments").
    Connector("postgres-prod").
    Description("Daily orders fact table").
    Owner("team-data@example.com").
    Tags("finance", "pii").
    Column("user_id").Description("FK users.id").Tags("pii").And().
    Column("total").Description("USD cents").And().
    ColumnLineage(asset.ColumnLineageMap{
        "user_id": {{Asset: "payments", Column: "payer_id"}},
    }).
    Materialize(fn).
    Register()
```

### MaterializeResult Typed Fields (D-02, Phase 2 D-04 migration)

```go
// internal/asset/asset.go — Phase 4 addition

// MaterializeResult extended for Phase 4 (D-02).
// ColumnLineage: runtime override for column-level lineage (nil = use builder default).
// Schema: optional schema snapshot provided by the Materialize function itself
//         (used as fallback if SchemaDescriber is not implemented by connector).
// Metadata: kept as-is for sensor Payload (Phase 3 D-06) and connector-specific extras.
type MaterializeResult struct {
    RowsWritten   int64
    ColumnLineage ColumnLineageMap // nil = use builder default (D-02)
    Schema        *Schema          // nil = rely on SchemaDescriber (D-06 fallback)
    Metadata      map[string]any   // retained for sensor Payload coexistence (Phase 3 D-06)
}
```

**Backward compatibility:** All existing `MaterializeResult{RowsWritten: n, Metadata: m}` call sites continue to compile — new fields have zero values (nil maps), which are the correct defaults for "use builder defaults" and "rely on connector capability."

---

## Section §6: Schema Capture (META-01)

### PostgreSQL Connector SchemaDescriber Implementation

The existing `postgres.Postgres.Schema()` method (verified in `postgres.go`) already queries `information_schema.columns`. Phase 4 promotes this to the richer `SchemaDescriber` interface:

```go
// internal/connector/firstparty/postgres/postgres.go addition

// Compile-time assertion: Postgres satisfies SchemaDescriber.
var _ connector.SchemaDescriber = (*Postgres)(nil)

// DescribeSchema implements connector.SchemaDescriber (D-05).
// Queries information_schema.columns + pg_constraint for PK detection.
func (p *Postgres) DescribeSchema(ctx context.Context, ref connector.AssetRef) (connector.Schema, error) {
    schemaName, tableName, err := splitIdentifier(ref.Identifier)
    if err != nil {
        return connector.Schema{}, err
    }

    const colQuery = `
        SELECT
            c.column_name,
            c.data_type,
            c.character_maximum_length,
            c.numeric_precision,
            c.numeric_scale,
            c.is_nullable,
            c.column_default,
            c.ordinal_position,
            col_description(
                (c.table_schema||'.'||c.table_name)::regclass::oid,
                c.ordinal_position
            ) AS comment
        FROM information_schema.columns c
        WHERE c.table_schema = $1 AND c.table_name = $2
        ORDER BY c.ordinal_position
    `

    const pkQuery = `
        SELECT kcu.column_name
        FROM information_schema.table_constraints tc
        JOIN information_schema.key_column_usage kcu
          ON tc.constraint_name = kcu.constraint_name
         AND tc.table_schema = kcu.table_schema
        WHERE tc.constraint_type = 'PRIMARY KEY'
          AND tc.table_schema = $1 AND tc.table_name = $2
        ORDER BY kcu.ordinal_position
    `
    // ... execute both queries, combine into connector.Schema
}
```

**Type normalization (D-07 "connector-normalized" types):** PostgreSQL's `information_schema.data_type` returns SQL standard names (`character varying`, `integer`, `timestamp with time zone`, etc.). The normalizer maps these to platform-standard types used in the Schema struct:

| PG `data_type` | Normalized |
|----------------|-----------|
| `integer` | `int32` |
| `bigint` | `int64` |
| `smallint` | `int16` |
| `character varying(N)` | `varchar(N)` |
| `text` | `text` |
| `boolean` | `bool` |
| `numeric(p,s)` | `decimal(p,s)` |
| `timestamp with time zone` | `timestamptz` |
| `timestamp without time zone` | `timestamp` |
| `date` | `date` |
| `jsonb` | `jsonb` |
| `uuid` | `uuid` |
| `double precision` | `float64` |
| `real` | `float32` |

[ASSUMED — normalization table completeness; may need additions for types encountered in practice]

### Schema Hash for Dedup (D-08)

```go
// internal/schema/capture.go

func hashSchema(s connector.Schema) string {
    // Canonical JSON of only the schema structure (not CapturedAt, RowCountEstim).
    type canonicalCol struct {
        Name         string  `json:"name"`
        Type         string  `json:"type"`
        Nullable     bool    `json:"nullable"`
        Default      *string `json:"default,omitempty"`
        IsPrimaryKey bool    `json:"is_pk"`
    }
    type canonicalSchema struct {
        Columns    []canonicalCol `json:"columns"` // in ordinal order (from DescribeSchema)
        PrimaryKey []string       `json:"pk"`
    }
    // Marshal and SHA-256
}
```

**Why exclude RowCountEstim and CapturedAt from hash:** These fields are volatile (row count changes frequently; time always differs). Hashing only structural fields (columns, types, nullability, defaults, PK) gives stable dedup — only actual schema changes trigger a new `schema_versions` row.

---

## Section §7: Schema Diff Classifier (META-02, META-05)

### Go Data Model for change_type

```go
// internal/schema/diff.go

type ChangeKind int

const (
    ChangeColumnAdded    ChangeKind = iota
    ChangeColumnDropped
    ChangeTypeNarrowed
    ChangeTypeWidened
    ChangeNullableAdded  // not null → nullable (non-breaking)
    ChangeNullableRemoved // nullable → not null (BREAKING)
    ChangePKModified
    ChangeCommentChanged
    ChangeDefaultChanged
)

type SchemaChange struct {
    ColumnName   *string    // nil for PK-level changes
    Kind         ChangeKind
    IsBreaking   bool
    PrevType     *string
    NewType      *string
    PrevNullable *bool
    NewNullable  *bool
    PrevDefault  *string
    NewDefault   *string
}

// Diff returns the ordered list of changes between prev and next schemas.
func Diff(prev, next connector.Schema) []SchemaChange { ... }
```

### Type Lattice (PostgreSQL-specific, D-09)

The type lattice is a pure Go function. Future connectors register their own lattice by implementing a `TypeCompatibilityChecker` interface.

```go
// internal/schema/lattice_postgres.go

// IsWideningPostgres returns true if old→new is a widening (non-breaking) change
// for PostgreSQL types, false if narrowing (breaking), and an error if the relationship
// is unknown (default: breaking per D-09).
func IsWideningPostgres(oldType, newType string) (isWidening bool, known bool) {
    // Widening rules (examples):
    //   int16  → int32    ✓
    //   int32  → int64    ✓
    //   float32 → float64 ✓
    //   varchar(64) → varchar(255) ✓
    //   varchar(N) → text ✓ (text is unbounded, widening)
    //   decimal(8,2) → decimal(10,2) ✓ (wider precision, same scale)
    // Narrowing rules:
    //   int64  → int32    ✗
    //   varchar(255) → varchar(64) ✗
    //   decimal(10,4) → decimal(8,2) ✗ (narrower precision AND scale)
    //   decimal(10,4) → decimal(10,2) ✗ (same precision, narrower scale)
    // Unknown (default breaking):
    //   text → bytea     → known=false (heterogeneous type change)
    //   int32 → uuid     → known=false
}
```

**Implementation notes:**
- Parse `varchar(N)` and `decimal(p,s)` with regexp to extract numeric parameters.
- `numeric` without parameters = unbounded precision → wider than any `decimal(p,s)`.
- Cross-family type changes (int → text, bool → int) are always `known=false` → breaking.
- The lattice is a value receiver function, not a global map, so it is thread-safe by construction.

### Timeline Derivation (META-05, D-12)

```sql
-- META-05: column timeline for a given asset + column
-- Source: D-12 decision; schema_changes table
SELECT
    sc.id,
    sc.change_type,
    sc.is_breaking,
    sc.prev_type,
    sc.new_type,
    sc.prev_nullable,
    sc.new_nullable,
    sc.observed_at,
    sc.acknowledged_at,
    sc.acknowledged_by,
    sc.acknowledgement_reason,
    sv.schema_hash AS new_schema_hash
FROM schema_changes sc
JOIN schema_versions sv ON sc.new_version_id = sv.id
WHERE sc.asset = $1
  AND sc.column_name = $2
ORDER BY sc.observed_at;
```

---

## Section §8: Metadata REST API (META-03)

### Endpoints

```
PATCH /assets/:name/metadata
  Body: {"description": "...", "owner": "...", "tags": ["a","b"]}
  Query: ?merge=true (merge tags) | ?merge=false (replace, default)
  Auth: JWT required; governance team role
  Response: 200 {"effective": {...merged effective metadata...}}
  Side effect: INSERT INTO asset_metadata; emit metadata.updated event

PATCH /assets/:name/columns/:col/metadata
  Same shape, scoped to column

GET /assets/:name/metadata
  Response: {"code_default": {...}, "runtime_override": {...}, "effective": {...}}
```

### AssetMetadata Table

```sql
-- asset_metadata stores runtime overrides (D-17)
CREATE TABLE "asset_metadata" (
    "id"          uuid NOT NULL,
    "asset"       character varying(256) NOT NULL,
    "column_name" character varying(256) NULL,  -- NULL = asset-level
    "description" text NULL,
    "owner"       character varying(256) NULL,
    "tags"        jsonb NULL,  -- stored as JSON array for flexibility
    "set_by"      uuid NOT NULL,  -- actor user_id
    "set_at"      timestamptz NOT NULL,
    PRIMARY KEY ("id")
);
CREATE INDEX asset_metadata_asset_col ON asset_metadata (asset, column_name);
```

**Read logic (COALESCE):** The GET handler loads the latest `asset_metadata` row for `(asset, column)` and the `asset_versions` row for the current code_hash. Effective value = `COALESCE(runtime.field, code_default.field)`.

**Note on UPDATE vs INSERT:** D-17 says runtime overrides are mutable. The simplest model is always INSERT (append history). The GET returns the MAX(set_at) row. If governance teams want a full audit history of metadata changes, all rows are retained. This avoids UPDATE which would complicate the RLS model.

---

## Section §9: OpenLineage Export Translator (D-18)

### Minimum Required Fields (OpenLineage RunEvent v1.x)

```go
// internal/lineage/openlineage/translate.go

// RunEvent is the minimal OpenLineage RunEvent for export (D-18).
// Source: https://openlineage.io/docs/spec/object-model
type RunEvent struct {
    EventType string    `json:"eventType"` // "COMPLETE"
    EventTime string    `json:"eventTime"` // RFC 3339
    Run       OLRun     `json:"run"`
    Job       OLJob     `json:"job"`
    Inputs    []OLDataset `json:"inputs"`
    Outputs   []OLDataset `json:"outputs"`
    Producer  string    `json:"producer"` // "https://github.com/kanpon/data-governance"
    SchemaURL string    `json:"schemaURL"` // "https://openlineage.io/spec/1-0-5/OpenLineage.json"
}

type OLRun struct {
    RunID    string            `json:"runId"` // our run UUID
    Facets   map[string]any    `json:"facets,omitempty"`
}

type OLJob struct {
    Namespace string `json:"namespace"` // platform instance identifier
    Name      string `json:"name"`      // asset name
    Facets    map[string]any `json:"facets,omitempty"`
}

type OLDataset struct {
    Namespace string `json:"namespace"` // connector identifier
    Name      string `json:"name"`      // asset name / table name
    Facets    map[string]any `json:"facets,omitempty"` // columnLineage facet here
}
```

**Field mapping (our model → OL):**
| Our field | OL field |
|-----------|---------|
| `run.id` (UUID) | `run.runId` |
| `run.finished_at` | `eventTime` |
| `asset.Name` | `job.name` |
| `asset.Upstreams()` | `inputs[].name` |
| `asset.Name` | `outputs[0].name` |
| `result.ColumnLineage` | `outputs[0].facets.columnLineage.fields` |
| `code_hash` | `run.facets.processing_engine.version` |
| `"COMPLETE"` | `eventType` (always COMPLETE for successful runs) |

The translator is ~100 lines of pure Go, zero external dependencies. [VERIFIED: OL spec reviewed at https://openlineage.io/docs/spec/object-model]

---

## Common Pitfalls

### Pitfall 1: Event_type CHECK constraint drift between DB and Go
**What goes wrong:** New event types added to `internal/event/types.go` but the migration CHECK constraint not updated (or vice versa). Writer validates against `AllKnownTypes()` in memory; DB enforces CHECK constraint. If they diverge, events succeed in one but fail in the other.
**Why it happens:** Two places to update.
**How to avoid:** Test `TestEventTypeConsistency` that loads `AllKnownTypes()` and verifies every value is present in the migration's CHECK constraint string (or query `information_schema.check_constraints` in integration test).
**Warning signs:** `event_log` insert fails with constraint violation for valid-seeming event type.

### Pitfall 2: Recursive CTE cycle on self-referential assets
**What goes wrong:** An asset accidentally declares itself as upstream (user error). Recursive CTE loops infinitely without cycle guard.
**Why it happens:** PostgreSQL recursive CTEs do not auto-detect cycles.
**How to avoid:** The cycle guard `NOT (path @> ARRAY[...])` in the CTE anchors correct behavior. Also: add a `CHECK (from_asset != to_asset)` constraint on `asset_edges` to prevent self-edges at the DB level.
**Warning signs:** CTE query never returns (or returns after hitting max_depth with unexpected results).

### Pitfall 3: Ent regeneration breaks custom code in generated files
**What goes wrong:** Developer hand-edits a generated ent file (e.g., `client.go`). `go generate` overwrites it.
**Why it happens:** Ent generates all files in the `ent/` directory except schema files.
**How to avoid:** Never edit files outside `internal/storage/ent/schema/`. All customization goes in schema files via `entsql.Annotation` or in the hand-managed SQL appendix. Established pattern in this codebase. [VERIFIED: existing schema files use entsql.Annotation for table naming]

### Pitfall 4: Import cycle when Schema types shared between connector and asset
**What goes wrong:** `connector.SchemaDescriber` returns `asset.Schema` → `connector` imports `asset` → `asset` already imports `connector` → cycle.
**Why it happens:** Phase 4 introduces the `asset.Schema` type (D-07) in the `asset` package to be part of `MaterializeResult`. If `connector.SchemaDescriber` also returns `asset.Schema`, there's an import cycle.
**How to avoid:** Keep `Schema` / `Column` types in the `connector` package (they already have `Column`, `SchemaResponse`). Phase 4 extends `connector.SchemaResponse` to the richer `connector.Schema` struct. `MaterializeResult.Schema` is typed as `*connector.Schema` (asset imports connector, which it already does in `io.go`). [VERIFIED: `internal/asset/io.go` already imports `internal/connector`]

### Pitfall 5: Schema hash instability from field ordering
**What goes wrong:** Two schema captures with identical structure produce different hashes because column order differs between `DescribeSchema` calls.
**Why it happens:** `information_schema.columns` returns columns in `ordinal_position` order, which is deterministic for the same table. But if someone does `ALTER TABLE ADD COLUMN` then `DROP COLUMN` + `ADD COLUMN`, ordinal positions may shift.
**How to avoid:** The canonical schema JSON for hashing orders columns by `name` (alphabetical), NOT by `ordinal_position`. Include ordinal_position only in the stored JSONB for display purposes.
**Warning signs:** Repeated materializations of an unchanged table produce new `schema_versions` rows with different hashes.

### Pitfall 6: RLS conflict with schema_versions dedup UPDATE
**What goes wrong:** Applying Phase 1-style immutability RLS (`REVOKE UPDATE ON schema_versions FROM platform_app`) breaks the D-08 dedup path which needs `UPDATE last_seen_run_id, last_seen_at`.
**Why it happens:** Conflating "immutable audit data" with "operational metadata that legitimately updates."
**How to avoid:** Only `schema_changes` (the ack rows) needs strict immutability. `schema_versions` allows UPDATE for the dedup columns. The `schema_changes.acknowledged_at` columns use a SECURITY DEFINER function for the ack operation, which executes as `platform_owner` (bypasses RLS). [VERIFIED: PostgreSQL RLS model; Phase 1 migration demonstrates the pattern]

---

## Code Examples

### Verified Pattern: Raw SQL + ent split (from claim.go)

```go
// Source: internal/run/claim.go — established precedent for Phase 4 D-16
// Raw SQL for performance-critical SKIP LOCKED; ent for entity CRUD.
// Phase 4 follows this exact split: sqlc for CTE traversals, ent for edge/version writes.

tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
defer func() { _ = tx.Rollback() }()
// ... raw SQL operations ...
if err := tx.Commit(); err != nil { return fmt.Errorf("run: commit claim: %w", err) }
```

### Verified Pattern: ent schema with hand-managed appendix

```go
// Source: internal/storage/ent/schema/run.go — hand-managed comment
// ent generates DDL for fields; partial/conditional indexes and CHECK constraints
// go in the SQL appendix because ent has no native support.
// Phase 4 new entities follow the same convention.
```

### Verified Pattern: Migration partial index

```sql
-- Source: migrations/20260508120000_phase3_runs_columns.sql
-- Same pattern Phase 4 uses for asset_edges WHERE superseded_at IS NULL
CREATE UNIQUE INDEX run_partition_inflight_unique
  ON runs (asset_name, partition_key)
  WHERE state IN ('queued','starting','running')
    AND partition_key IS NOT NULL;
```

### Verified Pattern: RLS for immutable table

```sql
-- Source: migrations/20260506062521_initial.sql
-- Phase 4 applies same pattern to schema_changes (ack-additive, not fully immutable)
GRANT SELECT, INSERT ON event_log TO platform_app;
REVOKE UPDATE, DELETE, TRUNCATE ON event_log FROM platform_app;
ALTER TABLE event_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE event_log FORCE ROW LEVEL SECURITY;
CREATE POLICY event_log_select ON event_log FOR SELECT TO platform_app USING (true);
CREATE POLICY event_log_insert ON event_log FOR INSERT TO platform_app WITH CHECK (true);
```

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| SQL-parser-based column lineage inference | Declared column lineage (user annotates) | ALINE-01 deferred to v2 | Phase 4 ships only declared; SQL parsing is v2 |
| Single `lineage_edges` table mixing asset+column | Split `asset_edges` + `column_edges` | D-13 | Asset traversal hits smaller table; column drill-down on demand |
| `map[string]any` Metadata hook for lineage | Typed `ColumnLineage ColumnLineageMap` field | Phase 4 | Type safety; ent serialization; no string-key guessing |
| ThijsKoot/openlineage-go runtime dependency | In-house OL translator | D-18 | Zero LOW-credibility runtime deps |
| `connector.Schema()` returns basic Column list | `SchemaDescriber.DescribeSchema()` returns rich Schema with PK, defaults, comments | Phase 4 | Enables full breaking-change classification per D-09 |

**Deprecated/outdated:**
- Using `MaterializeResult.Metadata["lineage"]` to pass lineage (Phase 2 D-04 hook): Phase 4 replaces with typed `MaterializeResult.ColumnLineage` field. Existing code using the Metadata key works but lineage capture ignores it (typed field wins). Document migration note for users.

---

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | `encoding/json` marshals `map[string]T` with sorted keys for all nested map levels | §3, Code-hash | Non-deterministic fingerprint → false drift detection on every run |
| A2 | sqlc v1.31.x supports `@param::type` casting and `@>` array-contains operator in PostgreSQL mode | §4 CTE | CTE query fails to generate; need alternative array syntax |
| A3 | PostgreSQL partial indices are used by the query planner for the CTE recursive step | §4 EXPLAIN | Seq scan instead of index scan at v1 scale (acceptable but slower) |
| A4 | Import cycle does not exist if Schema types live in `connector` package (asset already imports connector) | §5 Pattern 4 | Compile failure; requires new shared package `internal/schemadefs` |
| A5 | ent v0.14.0 correctly generates Atlas-compatible migrations for 6 new entities without collision | §2 ent Pattern | Migration drift; need `go generate` + `atlas migrate diff` to verify |
| A6 | PostgreSQL `col_description()` function available for column comment extraction | §6 SchemaDescriber | Comment field always empty; acceptable fallback (non-breaking) |
| A7 | `information_schema.columns` is accessible from the `platform_app` role | §6 SchemaDescriber | Schema capture fails for all assets; add explicit GRANT if needed |

---

## Open Questions (RESOLVED)

1. **Fingerprint includes metadata (Description/Owner/Tags)?**
   - What we know: D-03 says "leaning yes"; CONTEXT.md lists as Claude's Discretion.
   - What's unclear: Does a governance-team PATCH to owner/tags trigger a code_hash change and thus a new `asset_versions` row? If yes, do drift alarms fire?
   - RESOLVED: **Include Description/Owner/Tags in the D-03 fingerprint.** Governance-team self-serve edits are version-controlled provenance — a PATCH that changes owner/tags should produce a new `asset_versions` row with a fresh `code_hash`. This is the expected and desired outcome (it preserves the audit trail). Drift alarm (`lineage.drift_detected`) is **decoupled** from the code_hash change: drift fires only on upstream divergence between observed (instrumented `AssetIO.Read`) and declared (`Asset.Upstreams()`) — never on code_hash change alone. Therefore metadata-only edits create new asset versions without triggering drift noise.

2. **AssetVersion as own ent entity vs implicit `(asset, code_hash)` grouping?**
   - What we know: D-16 lists `AssetVersion` as an ent entity; CONTEXT.md "Claude's Discretion" says pick one.
   - RESOLVED: **AssetVersion is its own ent entity** with fields `(id, asset, code_hash, description, owner, tags, column_lineage JSONB, drift_status, created_at)`. Phase 5 RBAC will bind policies to specific asset versions; a foreign-key reference to an explicit `asset_versions.id` is cleaner than a composite `(asset, code_hash)` key. Plan 04-02 creates the ent entity; plans 04-04 (CaptureRun) and 04-07 (metadata read API) consume it.

3. **Schema_changes ack UPDATE pattern — SECURITY DEFINER or allow UPDATE?**
   - What we know: `schema_changes` should be immutable except for ack columns; RLS blocks UPDATE for platform_app.
   - RESOLVED: **Allow UPDATE on schema_changes for platform_app, restricted by application logic.** The ent mutation in plan 04-07 (and CLI in plan 04-08) only calls `Set{AcknowledgedAt,AcknowledgedBy,AcknowledgementReason}` — non-ack columns are never touched. Column-level UPDATE policies in PostgreSQL require PG 15+ `FOR UPDATE OF (col)` which is non-portable. Compile-time mutation surface (only three Set methods exist on the ack code path) plus DB grants `SELECT, INSERT, UPDATE` (no DELETE/TRUNCATE) form the two-layer defense. The Wave 2 `TestSchemaChangesAckOnly` test asserts the mutation surface is bounded to ack columns.

---

## Environment Availability

Step 2.6: SKIPPED (Phase 4 is code/config/migration changes only; all external dependencies — PostgreSQL, pgx/v5, ent, sqlc — are already verified in the existing development environment from Phases 1-3).

---

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | `testing` stdlib + `testify` v1.11.1 (already in go.mod) + `testcontainers-go` for integration |
| Config file | None needed (existing pattern: no separate config file, test flags inline) |
| Quick run command | `go test ./internal/lineage/... ./internal/schema/... ./internal/metadata/... -v -timeout 30s` |
| Full suite command | `go test ./... -timeout 5m` (includes integration tests with testcontainers) |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| LINE-01 | Asset edges inserted on registry + run-attributed event emitted | unit | `go test ./internal/lineage/... -run TestSyncStaticEdges -v` | ❌ Wave 0 |
| LINE-01 | Edge soft-retire when upstream removed (superseded_at set) | unit | `go test ./internal/lineage/... -run TestEdgeSoftRetire -v` | ❌ Wave 0 |
| LINE-02 | Builder ColumnLineage stored in asset_versions and queryable | unit | `go test ./internal/asset/... -run TestColumnLineageBuilder -v` | ❌ Wave 0 |
| LINE-02 | Runtime MaterializeResult.ColumnLineage wins over builder default | unit | `go test ./internal/lineage/... -run TestColumnLineageResolution -v` | ❌ Wave 0 |
| LINE-03 | Recursive CTE returns correct downstream nodes at depth 3 | integration | `go test ./internal/lineage/... -run TestRecursiveCTE -v -tags integration` | ❌ Wave 0 |
| LINE-06 | impact.Analyze returns full upstream+downstream graph | integration | `go test ./internal/lineage/... -run TestImpactAnalyze -v -tags integration` | ❌ Wave 0 |
| LINE-06 | depth>25 rejected with error | unit | `go test ./internal/lineage/... -run TestImpactDepthCap -v` | ❌ Wave 0 |
| META-01 | SchemaDescriber type-assertion succeeds for postgres connector | unit | `go test ./internal/connector/... -run TestSchemaDescriberCapability -v` | ❌ Wave 0 |
| META-01 | Schema hash dedup: second identical capture updates last_seen_at only | integration | `go test ./internal/schema/... -run TestSchemaHashDedup -v -tags integration` | ❌ Wave 0 |
| META-02 | Diff(prev, next) correctly classifies breaking vs non-breaking | unit | `go test ./internal/schema/... -run TestDiffClassifier -v` | ❌ Wave 0 |
| META-02 | Type lattice: widening int32→int64 = non-breaking | unit | `go test ./internal/schema/... -run TestTypeLattice -v` | ❌ Wave 0 |
| META-03 | PATCH /assets/:name/metadata stored and effective on GET | integration | `go test ./internal/metadata/... -run TestMetadataPatch -v -tags integration` | ❌ Wave 0 |
| META-05 | Column timeline query returns ordered schema_changes | integration | `go test ./internal/schema/... -run TestColumnTimeline -v -tags integration` | ❌ Wave 0 |

### Unit Test Boundaries (no DB, pure Go logic)

- **Fingerprint stability:** `TestCodeHashStability` — same asset definition → same hash across 100 calls, different goroutines
- **Diff classifier:** `TestDiffClassifier` — table-driven test covering all 9 ChangeKind values with examples
- **Type lattice:** `TestTypeLattice` — matrix test: (int32→int64)=widening, (varchar(255)→varchar(64))=narrowing, (text→bytea)=unknown/breaking
- **RLS rejection:** `TestEventLogRLS` (already exists pattern from Phase 1) — platform_app cannot DELETE from event_log; same test extended for schema_changes
- **Recursive CTE cycle guard:** `TestCyclicLineageGuard` — asset A→B→A does not infinite loop (use mock sqlc DB)
- **Capability fallback:** `TestSchemaCaptureFallback` — connector without SchemaDescriber falls back to MaterializeResult.Schema, then to `schema_capture_unsupported` tag
- **ColumnLineage resolution:** `TestColumnLineageResolution` — runtime override wins; builder default used when runtime is nil; undeclared → `column_lineage_undeclared` tag

### Integration Test Boundaries (testcontainers-go PostgreSQL)

- **Executor → lineage writer → asset_edges:** Start a test asset registry with two assets (A upstream of B), materialize B, assert `asset_edges` row exists with correct `from_asset=A, to_asset=B`. Checks atomicity (both run-state update and edge insert in same tx).
- **Executor → schema capture → schema_versions:** Materialize asset backed by postgres test container; assert `schema_versions` row inserted with correct column list. Second materialization of same schema: assert no new row, only `last_seen_at` updated.
- **Impact API end-to-end:** Create 5-asset chain A→B→C→D→E via sqlc insert; call `impact.Analyze(ctx, {Asset:"C", Direction:"downstream", Depth:10})`; assert result contains D and E but not A or B.

### E2E Acceptance Scenarios (ROADMAP Phase 4 criteria)

| Criterion | E2E Test Description |
|-----------|---------------------|
| AC-1: Asset edges auto-recorded after materialization | Materialize asset with 2 upstreams; `GET /lineage/impact?asset=upstream_a&direction=downstream` returns target asset in response |
| AC-2: Column lineage declared and version-bound | Register asset with `.ColumnLineage(...)`, materialize; query `asset_versions` row; assert `column_lineage` JSONB matches declaration and `code_hash` matches computed fingerprint |
| AC-3: Impact analysis traverses full graph | 5-asset chain; `impact.Analyze` for middle asset downstream returns 3 nodes at correct depths |
| AC-4: Schema captured, diffed, breaking changes recorded | Create table, materialize; drop column; materialize again; assert `schema_changes` row with `is_breaking=true` and `schema.change_detected` event in event_log |
| AC-5: Metadata settable via API | `PATCH /assets/:name/metadata {description: "test"}; GET /assets/:name/metadata` returns `effective.description = "test"` |

### Sampling Rate
- **Per task commit:** `go test ./internal/lineage/... ./internal/schema/... ./internal/metadata/... -v -timeout 30s`
- **Per wave merge:** `go test ./... -timeout 5m`
- **Phase gate:** Full suite green before `/gsd-verify-work`

### Wave 0 Gaps (must create before implementation)

- [ ] `internal/lineage/capture_test.go` — covers LINE-01 (SyncStaticEdges, CaptureRun)
- [ ] `internal/lineage/impact_test.go` — covers LINE-06 (Analyze, depth cap)
- [ ] `internal/lineage/queries/lineage_test.go` — covers LINE-03 (CTE correctness, cycle guard)
- [ ] `internal/schema/diff_test.go` — covers META-02 (Diff, classifier, lattice)
- [ ] `internal/schema/capture_test.go` — covers META-01 (hash dedup, SchemaDescriber fallback)
- [ ] `internal/metadata/handler_test.go` — covers META-03 (PATCH/GET endpoints)
- [ ] `internal/asset/fingerprint_test.go` — covers D-03 (hash stability, ColumnLineage resolution)
- [ ] Framework install: `go get github.com/sqlc-dev/sqlc/cmd/sqlc@latest` (tool, not runtime dep; already in Makefile or CI)

---

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | no — no new auth paths | — |
| V3 Session Management | no — no new sessions | — |
| V4 Access Control | yes — metadata PATCH endpoints require governance-team role | JWT middleware (existing); future Phase 5 Casbin |
| V5 Input Validation | yes — `depth` parameter DoS vector; asset names in SQL | Depth cap ≤25 enforced in `impact.Analyze`; parameterized queries throughout |
| V6 Cryptography | yes — SHA-256 for code_hash and schema_hash | `crypto/sha256` stdlib; never hand-rolled |

### Known Threat Patterns

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Impact API depth bomb (`?depth=9999`) | DoS | Hard cap 25; return 400 Bad Request above |
| Asset name injection in CTE (user-supplied string in `@asset` parameter) | Tampering | sqlc parameterized queries; no string interpolation |
| Metadata PATCH without authorization | Elevation of Privilege | JWT required; Phase 5 Casbin for role check; Phase 4 uses existing JWT middleware |
| Schema_changes ack by unauthorized user | Tampering | REST `POST /schema/changes/:id/ack` requires governance-team role (JWT claim check) |
| Large ColumnLineage map causing memory pressure during fingerprint | DoS | Reasonable size limit (e.g., 1000 columns max) enforced in builder validation |

---

## Sources

### Primary (HIGH confidence)
- `internal/run/claim.go` — verified sqlc+ent dual-ORM split pattern (D-16 precedent)
- `internal/storage/ent/schema/run.go` + `event_log.go` — verified ent schema patterns with entsql.Annotation
- `migrations/20260506062521_initial.sql` — verified RLS/GRANT pattern for immutable tables (D-09 extension)
- `migrations/20260508120000_phase3_runs_columns.sql` — verified hand-managed appendix pattern (CHECK constraints, partial indices)
- `internal/connector/firstparty/postgres/postgres.go` — verified `information_schema.columns` query for SchemaDescriber base
- `go.mod` — verified all dependency versions (ent v0.14.0, pgx v5.9.1, sqlc tool pattern)
- `internal/asset/builder.go`, `asset.go`, `io.go` — verified existing Builder chain, MaterializeResult, AssetIO interface
- `internal/event/types.go` — verified event_type enum extension pattern (AllKnownTypes + constants)
- `internal/runtime/executor.go` — verified runStep hook points for Phase 4 lineage/schema capture integration
- Go stdlib `encoding/json` — guaranteed sorted map keys for `map[string]T` since Go 1.12 [CITED: https://pkg.go.dev/encoding/json]
- OpenLineage object model spec [CITED: https://openlineage.io/docs/spec/object-model]
- PostgreSQL recursive CTE docs [CITED: https://www.postgresql.org/docs/16/queries-with.html]

### Secondary (MEDIUM confidence)
- `.planning/research/ARCHITECTURE.md` §2.3 — PostgreSQL adjacency table + recursive CTE recommendation verified against this codebase's design
- `.planning/research/PITFALLS.md` §3,4,12,15 — Phase 4 pitfall catalogue, confirmed applicable
- `.planning/phases/04-schema/04-CONTEXT.md` — 21 locked decisions, confirmed no contradictions with codebase state

### Tertiary (LOW confidence)
- sqlc `@>` array operator support in v1.31.x for PostgreSQL — [ASSUMED A2] needs `sqlc generate` validation during Wave 0
- Import cycle analysis for `connector.Schema` type placement — [ASSUMED A4] needs `go build ./...` to confirm

---

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — all dependencies verified in go.mod; no new deps needed
- Architecture patterns: HIGH — all patterns verified against existing codebase (claim.go, schema files, migrations)
- Pitfalls: HIGH — derived from Phase 1-3 codebase patterns + PITFALLS.md (itself HIGH credibility)
- CTE query specifics: MEDIUM — structure verified against PG docs; sqlc binding details need Wave 0 validation
- Type lattice completeness: MEDIUM — common types covered; edge cases may appear during implementation

**Research date:** 2026-05-08
**Valid until:** 2026-06-08 (stable domain; ent/sqlc versions pinned in go.mod)
