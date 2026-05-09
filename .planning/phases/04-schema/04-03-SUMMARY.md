---
phase: 04-schema
plan: 03
subsystem: asset-dsl + fingerprint + connector-capability
tags: [builder-dsl, column-lineage, code-hash, fingerprint, schema-describer, postgres, capability-pattern]

# Dependency graph
requires:
  - phase: 04-schema
    plan: 02
    provides: connector.Schema + connector.SchemaColumn types (D-07); ent entities for Wave 4 writer

provides:
  - internal/asset/types.go: ColumnRef, ColumnLineageMap, ColumnMeta — shared by Builder + MaterializeResult
  - internal/asset/asset.go: MaterializeResult typed fields (ColumnLineage, Schema) + Asset Phase 4 accessors (Description, Owner, Tags, Columns, ColumnLineage, CodeHash)
  - internal/asset/builder.go: Description, Owner, Tags, Column, ColumnLineage builder methods + ColumnBuilder type + ErrInvalidColumnRef
  - internal/asset/fingerprint.go: ComputeCodeHash — deterministic SHA-256 canonical JSON hash (D-03); populated by Build() and Register()
  - internal/connector/capability.go: SchemaDescriber optional interface (D-05/D-06); separate file — base Connector interface bytes-identical
  - internal/connector/firstparty/postgres/types_normalize.go: normalizePostgresType (16 PG type mappings)
  - internal/connector/firstparty/postgres/postgres.go: Postgres.DescribeSchema implementation (D-07)

affects:
  - 04-04 (Wave 3 — schema writer will type-assert conn.(connector.SchemaDescriber))
  - 04-05 (Wave 4 — diff classifier reads SchemaColumn.Type in normalized form)
  - 04-06 (Wave 5 — CTE traversal reads Asset.ColumnLineage() for column-edge recording)
  - 04-07 (Wave 6 — REST handler exposes Asset.Description/Owner/Tags/Columns)
  - 04-08 (Wave 7 — acceptance tests verify fingerprint stability + CONN-08 invariant)

# Tech tracking
tech-stack:
  added:
    - "crypto/sha256 (stdlib) — SHA-256 fingerprint for D-03"
    - "encoding/json (stdlib) — canonical JSON marshaling for deterministic hash"
    - "sort (stdlib) — pre-sort collections before hashing for order-invariance"
  patterns:
    - "Additive-only builder DSL: Phase 2/3 methods (Upstream, Connector, Materialize, Retry, Resource, Schedule, Sensor, Partitions) are unchanged; Phase 4 methods append after existing chain"
    - "Defensive copies: Tags, Columns, ColumnLineage accessors all return copies to prevent caller mutation"
    - "Fingerprint canonicalization: pre-sort upstreams, tags, columns (by name), column tags, ColumnLineage refs (by Asset+Column); encoding/json sorts map keys in Go 1.12+"
    - "Optional capability pattern: connector.SchemaDescriber is a separate interface in capability.go; runtime detection via type assertion conn.(connector.SchemaDescriber); ok=false for connectors that don't implement"
    - "Compile-time assertion: var _ connector.SchemaDescriber = (*Postgres)(nil) in postgres.go"
    - "Integration test without build tag: TestDescribeSchemaIntegration uses testcontainers (same pattern as existing postgres tests) — runs if Docker available, skips if not"

key-files:
  created:
    - internal/asset/types.go
    - internal/asset/fingerprint.go
    - internal/asset/fingerprint_test.go
    - internal/connector/capability.go
    - internal/connector/firstparty/postgres/types_normalize.go
    - internal/connector/firstparty/postgres/types_normalize_test.go
  modified:
    - internal/asset/asset.go (MaterializeResult typed fields + Asset struct Phase 4 fields + Phase 4 accessors)
    - internal/asset/builder.go (Phase 4 methods + ColumnBuilder + ErrInvalidColumnRef + Build() fingerprint + T-04-03-05 validation)
    - internal/asset/builder_test.go (Phase 4 TDD tests appended)
    - internal/connector/firstparty/postgres/postgres.go (DescribeSchema + compile-time assertion)
    - internal/connector/firstparty/postgres/postgres_test.go (SchemaDescriber capability + integration tests)

key-decisions:
  - "Build() populates codeHash (not only Register()): test callers that use Build() directly also get a hash; invalid assets (return before end of Build()) never get a hash"
  - "ColumnLineage map defensive copy in both Builder and Asset accessors: Builder.ColumnLineage deep-copies on set; Asset.ColumnLineage deep-copies on read — two-layer protection against caller mutation"
  - "Integration test without explicit build tag file: TestDescribeSchemaIntegration uses the same skip-if-no-testDSN pattern as existing postgres tests rather than a separate build-tagged file — consistent with codebase convention"
  - "Golden hash pinned: 1ff892702afda232e57d98686b3f1c1cdcd3a4c50d71b0d79dd70b60ed99f431 for 'orders' example chain from 04-RESEARCH.md (fingerprint canonicalization must not change without updating this)"
  - "ErrInvalidColumnRef added (T-04-03-05): Build() rejects ColumnLineageMap entries with empty Asset or Column — validates at declaration time, not write time"

# Metrics
duration: 35min
completed: 2026-05-09
---

# Phase 4 Plan 03: Builder DSL Extensions + Code-Hash Fingerprint + SchemaDescriber Capability Summary

**Column-level lineage builder DSL, deterministic SHA-256 asset fingerprint, optional connector.SchemaDescriber capability interface, and PostgreSQL DescribeSchema implementation with type normalization. Last plan touching user-facing API surface before Waves 4-7 build internal subsystems.**

## Performance

- **Duration:** ~35 min
- **Started:** 2026-05-09T11:05:00Z
- **Completed:** 2026-05-09T11:40:00Z
- **Tasks:** 3 of 3
- **Files modified:** 11 (6 created, 5 modified)

## Accomplishments

### Task 1: Builder DSL + Types + MaterializeResult

**New types (`internal/asset/types.go`):**
```go
type ColumnRef struct { Asset, Column string }
type ColumnLineageMap map[string][]ColumnRef
type ColumnMeta struct { Name, Description string; Tags []string }
```

**Builder method signatures (additive — Phase 2/3 methods unchanged):**
```go
func (b *Builder) Description(desc string) *Builder
func (b *Builder) Owner(owner string) *Builder
func (b *Builder) Tags(tags ...string) *Builder
func (b *Builder) ColumnLineage(cl ColumnLineageMap) *Builder
func (b *Builder) Column(name string) *ColumnBuilder

type ColumnBuilder struct { /* private */ }
func (cb *ColumnBuilder) Description(desc string) *ColumnBuilder
func (cb *ColumnBuilder) Tags(tags ...string) *ColumnBuilder
func (cb *ColumnBuilder) And() *Builder
```

**MaterializeResult extensions (backward-compatible):**
```go
type MaterializeResult struct {
    RowsWritten   int64
    ColumnLineage ColumnLineageMap  // nil = use builder default (D-02)
    Schema        *connector.Schema // nil = rely on SchemaDescriber (D-06)
    Metadata      map[string]any    // retained for sensor Payload coexistence (Phase 3)
}
```

**Asset Phase 4 accessors:**
```go
func (a *Asset) Description() string
func (a *Asset) Owner() string
func (a *Asset) Tags() []string           // defensive copy
func (a *Asset) Columns() []ColumnMeta    // defensive copy
func (a *Asset) ColumnLineage() ColumnLineageMap  // defensive deep copy
func (a *Asset) CodeHash() string
```

### Task 2: Code-Hash Fingerprint (D-03)

**Fields included in fingerprint:**
| Field | Rationale |
|-------|-----------|
| name | Core identity |
| upstreams | Data shape dependency |
| description | Governance metadata (per D-03 resolution: metadata edits ARE versioned) |
| owner | Governance metadata |
| tags | Governance metadata |
| columns | Column declarations = data shape |
| column_lineage | Column-level lineage = data shape |

**Fields excluded from fingerprint:**
| Field | Rationale |
|-------|-----------|
| connector_name | Binding vs declaration — moving asset between connectors should not invalidate lineage |
| materialize_fn | Go function values are not hashable; D-04 covers implementation drift |
| retry_policy | Orchestration concern, not data shape |
| schedule / sensors / partitions | Orchestration concerns, not data shape |

**Canonicalization:**
- `upstreams`: pre-sorted alphabetically
- `tags`: pre-sorted alphabetically
- `columns`: sorted by Name; each column's Tags sorted alphabetically
- `column_lineage` values: sorted by (Asset, Column); map keys auto-sorted by encoding/json (Go 1.12+)
- encoding/json marshals struct fields in declaration order (stable)

**Pinned golden hash for 'orders' example chain:**
```
1ff892702afda232e57d98686b3f1c1cdcd3a4c50d71b0d79dd70b60ed99f431
```
(SHA-256 of: `{"name":"orders","upstreams":[],"description":"Daily orders fact table","owner":"team-data@example.com","tags":["finance","pii"],"columns":[{"name":"total","description":"USD cents"},{"name":"user_id","description":"FK users.id","tags":["pii"]}],"column_lineage":{"user_id":[{"asset":"payments","column":"payer_id"}]}}`)

### Task 3: SchemaDescriber Capability + PostgreSQL Implementation

**connector.SchemaDescriber interface (`internal/connector/capability.go`):**
```go
type SchemaDescriber interface {
    DescribeSchema(ctx context.Context, ref AssetRef) (Schema, error)
}
```

**Wave 4 runtime detection pattern:**
```go
if d, ok := conn.(connector.SchemaDescriber); ok {
    schema, err := d.DescribeSchema(ctx, ref)
    // ... use schema ...
} else {
    // tag schema_capture_unsupported
}
```

**normalizePostgresType mapping table shipped:**

| PostgreSQL data_type | Normalized form |
|---------------------|----------------|
| smallint | int16 |
| integer | int32 |
| bigint | int64 |
| real | float32 |
| double precision | float64 |
| boolean | bool |
| text | text |
| character varying(N) | varchar(N) |
| character varying | varchar |
| character(N) | char(N) |
| numeric(p,s) | decimal(p,s) |
| numeric(p) | decimal(p) |
| numeric | decimal |
| timestamp with time zone | timestamptz |
| timestamp without time zone | timestamp |
| date | date |
| jsonb | jsonb |
| json | json |
| uuid | uuid |
| bytea | bytea |
| (unknown) | ?:rawType |

No deviations from 04-RESEARCH.md §6 table.

**PostgreSQL DescribeSchema implementation:**
- Queries `information_schema.columns` with `$1, $2` parameters (schemaName, tableName)
- Fetches column comments via `col_description(format('%s.%s', schema, table)::regclass::oid, ordinal_position::int)`
- Fetches PK via `information_schema.table_constraints JOIN key_column_usage WHERE constraint_type = 'PRIMARY KEY'`
- Returns columns in `ordinal_position` order (Wave 4 hashSchema() sorts alphabetically)
- `RowCountEstim: -1` (Phase 5 quality work adds pg_class.reltuples query)

**CONN-08 confirmation:** `git diff internal/connector/connector.go` returns empty. The Connector interface is bytes-identical to its Phase 1 definition.

## Task Commits

1. **Task 1: Builder DSL + types + MaterializeResult** - `bec3994` (feat)
2. **Task 2: Fingerprint + Build() integration + stability tests** - `46901b9` (feat)
3. **Task 3: SchemaDescriber + postgres DescribeSchema + type normalization** - `2c44e78` (feat)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Security] Added ErrInvalidColumnRef + Build() validation for empty ColumnRef.Asset/Column (T-04-03-05)**
- **Found during:** Task 1 threat model review
- **Issue:** The threat model explicitly marks T-04-03-05 as `mitigate` — ColumnRef with empty Asset or Column should be rejected at Build() time
- **Fix:** Added `ErrInvalidColumnRef` sentinel error + validation loop in `Build()` after partition validation
- **Files modified:** `internal/asset/builder.go`
- **Committed in:** `bec3994` (Task 1)

**2. [Rule 1 - Bug] stubConnector in test needed full Connector interface implementation**
- **Found during:** Task 3 (RED phase — build failed)
- **Issue:** `type stubConnector struct{}` inside the test was declared with `var _ connector.Connector = (*stubConnector)(nil)` but had no methods — Go rejected the compile
- **Fix:** Promoted to package-level `stubNoSchemaDescriber` type with all 5 Connector interface methods implemented (APIVersion, Ping, Schema, Read, Write)
- **Files modified:** `internal/connector/firstparty/postgres/postgres_test.go`
- **Committed in:** `2c44e78` (Task 3)

**3. [Rule 1 - Bug] `//go:build integration` directive inside function doc comment was misinterpreted**
- **Found during:** Task 3 (test compilation)
- **Issue:** `//go:build integration` inside a function's doc comment was parsed by the Go toolchain as a misplaced build directive (`misplaced compiler directive` error)
- **Fix:** Replaced with plain prose comment `// Build tag "integration" required; run with: go test -tags=integration`; the test uses the existing `if testDSN == ""` skip pattern consistent with the rest of the postgres test suite
- **Files modified:** `internal/connector/firstparty/postgres/postgres_test.go`
- **Committed in:** `2c44e78` (Task 3)

## Known Stubs

None — all Phase 4 user-facing API surface is fully implemented. `RowCountEstim: -1` is documented as intentional (Phase 5 adds pg_class.reltuples query for quality work).

## Threat Surface Scan

No new network endpoints, auth paths, file access patterns, or schema changes at trust boundaries were introduced in this plan. All additions are:
- In-process type definitions and builder DSL (asset package)
- Read-only SQL queries against `information_schema` (no DDL/DML)
- Optional interface expansion (no new mandatory contracts)

## Self-Check: PASSED

| Check | Status |
|-------|--------|
| `internal/asset/types.go` exists with ColumnRef, ColumnLineageMap, ColumnMeta | PASS |
| `internal/asset/fingerprint.go` exists with ComputeCodeHash | PASS |
| `internal/connector/capability.go` exists with SchemaDescriber interface | PASS |
| `internal/connector/firstparty/postgres/types_normalize.go` exists | PASS |
| `go build ./...` exits 0 | PASS |
| `go test -race ./internal/asset/... -count=1 -timeout 60s` exits 0 | PASS |
| `go test ./internal/connector/firstparty/postgres/... -count=1 -timeout 120s` exits 0 | PASS |
| `git diff internal/connector/connector.go` returns empty (CONN-08) | PASS |
| Type case count >= 14 in types_normalize.go | PASS (14 cases) |
| var _ connector.SchemaDescriber = (*Postgres)(nil) compiles | PASS |
| Golden hash pinned in fingerprint_test.go | PASS |
| Commits bec3994, 46901b9, 2c44e78 exist | PASS |

---
*Phase: 04-schema*
*Completed: 2026-05-09*
