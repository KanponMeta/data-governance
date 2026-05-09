---
phase: 04-schema
plan: 02
subsystem: lineage-schema-foundation
tags: [ent, migration, atlas, lineage, schema-versioning, metadata, event-types, connector]

# Dependency graph
requires:
  - phase: 04-schema
    plan: 01
    provides: migrations/20260509120000_phase4_lineage_schema.sql empty stub + atlas.sum

provides:
  - migrations/20260509120000_phase4_lineage_schema.sql: 6 CREATE TABLE + hand-managed appendix (partial indices, CHECK constraints, RLS grants, event_log enum extension)
  - internal/storage/ent/schema/asset_edge.go: AssetEdge ent entity
  - internal/storage/ent/schema/column_edge.go: ColumnEdge ent entity
  - internal/storage/ent/schema/schema_version.go: SchemaVersion ent entity
  - internal/storage/ent/schema/schema_change.go: SchemaChange ent entity
  - internal/storage/ent/schema/asset_version.go: AssetVersion ent entity (drift_status mutable; all other fields immutable)
  - internal/storage/ent/schema/asset_metadata.go: AssetMetadata ent entity (append-only, RLS protected)
  - internal/connector/schema_types.go: connector.Schema + connector.SchemaColumn (D-07 rich capture shape)
  - internal/event/types.go: 8 Phase 4 EventType constants (D-21) + extended AllKnownTypes() (37 total)

affects:
  - 04-03 (Wave 3 — lineage writer uses asset_edges/column_edges tables)
  - 04-04 (Wave 3 — schema writer uses schema_versions/schema_changes tables)
  - 04-05 (Wave 4 — diff classifier uses SchemaChange.change_type constraint)
  - 04-06 (Wave 5 — CTE traversal queries asset_edges with partial indices)
  - 04-07 (Wave 6 — metadata PATCH handler uses asset_metadata table)
  - 04-08 (Wave 7 — acceptance tests verify event_log_event_type_check covers Phase 4 types)

# Tech tracking
tech-stack:
  added: []  # no new dependencies; all Phase 4 ent entities use existing entgo.io/ent v0.14.0
  patterns:
    - "Hand-managed SQL appendix: ent generates CREATE TABLE; partial indices + CHECK constraints + RLS appended to same migration file (established Phase 1-3 pattern)"
    - "Soft-retire edges: superseded_at NULLABLE; partial index WHERE superseded_at IS NULL for hot read path (D-15)"
    - "ent.Immutable() field defense: asset_versions and schema_changes have Immutable() on all non-mutable fields; Wave 6 ack mutation is the only UPDATE path for schema_changes"
    - "connector package placement for Schema types: avoids asset->schema->connector->asset import cycle (Pitfall 4)"
    - "AllKnownTypes() cumulative pattern: Phase 4 types appended after Phase 3 block; min count asserted in tests"

key-files:
  created:
    - internal/storage/ent/schema/asset_edge.go
    - internal/storage/ent/schema/column_edge.go
    - internal/storage/ent/schema/schema_version.go
    - internal/storage/ent/schema/schema_change.go
    - internal/storage/ent/schema/asset_version.go
    - internal/storage/ent/schema/asset_metadata.go
    - internal/connector/schema_types.go
    - internal/connector/schema_types_test.go
  modified:
    - migrations/20260509120000_phase4_lineage_schema.sql (stub replaced with full DDL)
    - migrations/atlas.sum (hash updated)
    - internal/event/types.go (8 new constants + AllKnownTypes extended)
    - internal/event/types_test.go (TestAllPhase4EventTypes added)
    - internal/storage/ent/*.go (ent-regenerated: client.go, tx.go, mutation.go, predicate.go, runtime.go, hook.go + 6x entity files each)

key-decisions:
  - "Hand-written migration SQL (not atlas migrate diff): ariga.io/atlas-provider-ent has no go-get endpoint in this environment (same constraint as Plan 04-01); migration DDL written by hand matching exact Atlas output format (timestamptz, uuid, character varying(N), jsonb)"
  - "Tasks 1+2 committed together: the plan separates CREATE TABLE (Task 1) and hand-managed appendix (Task 2) but since atlas migrate diff was unavailable, the full migration was authored in one pass and committed atomically as e9f82df"
  - "asset_version UNIQUE(asset, code_hash) via ent index.Fields().Unique(): plan noted this as preferred approach vs hand-managed SQL; used ent's built-in unique multi-field index support"
  - "drift_status mutable on AssetVersion: only field without Immutable() because lineage drift detection (D-04) updates it post-insert without inserting a new row"
  - "schema_changes acknowledgement columns mutable: acknowledged_at/by/reason lack Immutable() per D-10; all other schema_change fields are immutable; DB-level: REVOKE DELETE/TRUNCATE; column-update restriction is app-layer (Wave 6 ent mutation)"

# Metrics
duration: 25min
completed: 2026-05-09
---

# Phase 4 Plan 02: Phase 4 Schema Migration + 6 Ent Entities + Schema/Event Types Summary

**Hand-authored Phase 4 PostgreSQL migration (6 CREATE TABLE + partial indices + CHECK constraints + RLS + event_log enum extension), 6 ent entity schemas with ent codegen, connector.Schema/SchemaColumn D-07 types, and 8 Phase 4 D-21 EventType constants with test coverage**

## Performance

- **Duration:** ~25 min
- **Started:** 2026-05-09T10:35:00Z
- **Completed:** 2026-05-09T11:00:00Z
- **Tasks:** 3 of 3 (Tasks 1+2 committed atomically, Task 3 separately)
- **Files modified:** 62 (6 new ent schema files, 2 new type files, 2 new test files, ~48 ent-generated files, 2 migration files, 2 event type files)

## Accomplishments

### Migration (Tasks 1+2)

`migrations/20260509120000_phase4_lineage_schema.sql` now contains:

**6 CREATE TABLE statements:**
| Table | Purpose | Key columns |
|-------|---------|-------------|
| `asset_edges` | Asset-to-asset lineage adjacency (D-13) | from_asset, to_asset, code_hash_first/latest, first/last_seen_run_id, superseded_at |
| `column_edges` | Column-to-column lineage adjacency (D-13) | from_asset/column, to_asset/column, partition_key, superseded_at |
| `schema_versions` | Full schema snapshots deduplicated by hash (D-11) | asset, code_hash, schema_hash, schema_data JSONB, last_seen_at |
| `schema_changes` | Per-diff breaking-change audit trail (D-09/D-11) | change_type, is_breaking, acknowledged_at/by/reason |
| `asset_versions` | Code-level asset snapshots per code_hash (D-17) | code_hash UNIQUE per asset, column_lineage JSONB, drift_status |
| `asset_metadata` | Runtime override metadata (D-17 append-only) | asset, column_name (nullable), set_by, set_at |

**Hand-managed appendix:**
- 4 partial indices `WHERE superseded_at IS NULL`: `asset_edges_active_from`, `asset_edges_active_to`, `column_edges_active_from`, `column_edges_active_to`
- CHECK `asset_edges_no_self`: `from_asset != to_asset` (D-13 Pitfall 2)
- CHECK `column_edges_no_self`: prevents same column→itself
- CHECK `schema_changes_change_type_check`: 9 internal change_type values
- CHECK `asset_versions_drift_status_check`: `clean|pending|acknowledged`
- Role grants: `platform_owner` ownership; `platform_app` SELECT/INSERT/UPDATE on edges/versions; REVOKE DELETE/TRUNCATE on schema_changes and asset_metadata
- RLS on `asset_metadata`: ENABLE + FORCE ROW LEVEL SECURITY, SELECT + INSERT policies only (mirrors event_log D-09 pattern)
- `event_log_event_type_check` extended: DROP+ADD includes all 37 event types (Phase 1: 7, Phase 2: 9, Phase 3: 13, Phase 4: 8)

### Ent Entities (Task 1)

All 6 ent schema files generated cleanly; `go generate ./internal/storage/ent/...` and `go build ./...` succeed.

| Entity | Table | Field count | Notable |
|--------|-------|-------------|---------|
| AssetEdge | asset_edges | 10 | All fields Immutable() except code_hash_latest, last_seen_run_id, last_seen_at, superseded_at |
| ColumnEdge | column_edges | 13 | Same as AssetEdge + from_column, to_column, partition_key |
| SchemaVersion | schema_versions | 8 | schema_data JSON Immutable(); only last_seen_at/run_id mutable |
| SchemaChange | schema_changes | 17 | acknowledged_at/by/reason mutable; all others Immutable() |
| AssetVersion | asset_versions | 9 | drift_status mutable; all others Immutable(); UNIQUE(asset,code_hash) index |
| AssetMetadata | asset_metadata | 8 | set_by/set_at Immutable(); description/owner/tags mutable (for UI display) |

### Types (Task 3)

**connector.Schema (D-07):**
```go
type Schema struct {
    Columns       []SchemaColumn
    PrimaryKey    []string
    RowCountEstim int64     // -1 if connector cannot supply
    CapturedAt    time.Time
}
type SchemaColumn struct {
    Name, Type, Comment string
    Nullable, IsPrimaryKey bool
    Default *string  // nil = no default
}
```

**Phase 4 EventType constants (D-21):**
- `lineage.captured`, `lineage.drift_detected`
- `schema.captured`, `schema.unchanged`, `schema.change_detected`, `schema.capture_failed`, `schema.break_acknowledged`
- `metadata.updated`

`AllKnownTypes()` now returns 37 entries (7 Phase 1 + 9 Phase 2 + 13 Phase 3 + 8 Phase 4).

## Task Commits

1. **Task 1+2: 6 ent entities + full Phase 4 migration** - `e9f82df` (feat)
2. **Task 3: connector.Schema types + Phase 4 EventType constants + tests** - `d1f1444` (feat)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] `atlas migrate diff` unavailable: ariga.io/atlas-provider-ent has no go-get endpoint**
- **Found during:** Task 1 (Step 8)
- **Issue:** `atlas.hcl` references `ariga.io/atlas-provider-ent` to load ent schema, but the package has no go-get meta tags and cannot be fetched. Same constraint existed in Plan 04-01 (`make migrate-lint` was Atlas Pro-only)
- **Fix:** Hand-authored the migration DDL in the exact Atlas output format, matching column types (`character varying(N)`, `timestamptz`, `jsonb`, `uuid`, `text`) from Phase 1-3 migrations. Tasks 1+2 were combined into a single file write + commit since the separation was predicated on atlas generating Task 1 output that Task 2 would append to
- **Verification:** `go build ./...` exits 0; all 6 CREATE TABLE present (`grep -c 'CREATE TABLE'` returns 6); all hand-managed appendix elements verified via grep
- **Committed in:** `e9f82df` (Tasks 1+2)

**2. [Rule 1 - Decision] Tasks 1+2 committed atomically instead of separately**
- **Found during:** Task 1 execution
- **Issue:** The plan splits Task 1 (Atlas-generated CREATE TABLE) and Task 2 (hand-managed appendix) into separate commits. Since Task 1 could not be Atlas-generated, both were authored together
- **Fix:** Combined into a single `e9f82df` commit covering the full migration file content
- **Impact:** None — downstream plans only depend on the final migration file content, not the commit split

### Not Applicable

- `make migrate-lint` skipped (Atlas Pro gate, pre-existing — documented in 04-01-SUMMARY.md)
- `make migrate-apply` skipped (no live local PostgreSQL; migration syntax verified against Phase 1-3 patterns)
- `runs.code_hash` / `runs.lineage_emitted` columns: NOT added. 04-CONTEXT.md §3 explicitly states "lineage_emitted column on runs: NOT needed if capture is in the same tx." Rationale: keeping the schema minimal; Wave 3 will add if same-tx approach is insufficient

## Threat Surface Scan

| Flag | File | Description |
|------|------|-------------|
| threat_flag: new_tables | migrations/20260509120000_phase4_lineage_schema.sql | 6 new tables with data at trust boundary platform_app→PostgreSQL. Mitigated: RLS on asset_metadata; REVOKE DELETE/TRUNCATE on schema_changes; all other tables grant SELECT/INSERT/UPDATE only (no DELETE) |
| threat_flag: new_event_types | internal/event/types.go | 8 new event_type values extend the event_log_event_type_check constraint. Drift risk (T-04-02-05) between Go AllKnownTypes() and DB CHECK mitigated by: shipped simultaneously in same plan; Plan 04-08 acceptance test TestEventTypeConsistency will verify DB CHECK vs AllKnownTypes() match |

## Known Stubs

None — this plan is pure schema/type foundation. No business logic, no data rendering, no placeholder values.

## Self-Check

| Check | Status |
|-------|--------|
| `go build ./...` exits 0 | PASS |
| `grep -c 'CREATE TABLE' migration` = 6 | PASS |
| 6 ent schema files exist with correct struct names | PASS |
| `internal/connector/schema_types.go` exports Schema + SchemaColumn | PASS |
| `internal/event/types.go` has all 8 Phase 4 constants | PASS |
| `AllKnownTypes()` returns >= 37 (tested) | PASS |
| `go test ./internal/event/... -run TestAllPhase4EventTypes` exits 0 | PASS |
| `go test ./internal/connector/... -run TestSchemaTypeShape` exits 0 | PASS |
| Commit e9f82df exists | PASS |
| Commit d1f1444 exists | PASS |
| Phase 3 regression: `go test ./internal/event/... -run TestAllPhase3EventTypes` | PASS |
| atlas.sum updated with new migration hash | PASS |

## Self-Check: PASSED

---
*Phase: 04-schema*
*Completed: 2026-05-09*
