---
phase: 04-schema
plan: 06
subsystem: lineage-traversal
tags: [sqlc, recursive-cte, lineage, impact-analysis, testcontainers, postgres, depth-cap, cycle-guard]

# Dependency graph
requires:
  - phase: 04-schema
    plan: 02
    provides: asset_edges + column_edges tables, partial indices, cycle-guard CHECK constraints
  - phase: 04-schema
    plan: 04
    provides: edge write path (SyncStaticEdges, CaptureRun) — tables have data
  - phase: 04-schema
    plan: 01
    provides: SeedDAG, SeedBranching, SeedCycle DAG seeders; StartPhase4Container

provides:
  - sqlc.yaml: postgresql engine, lineageq package, pgx/v5 adapter
  - internal/lineage/queries/lineage.sql: TraverseAssetLineage + TraverseColumnLineage recursive CTEs
  - internal/lineage/queries/{db.go,models.go,lineage.sql.go,querier.go}: sqlc-generated Go bindings
  - internal/lineage/impact/analyze.go: impact.Analyze entry point (D-14 depth cap, D-19/D-20 API)
  - internal/lineage/queries/lineage_integration_test.go: 10 integration scenarios

affects:
  - 04-07 (Wave 7 — REST handler calls impact.Analyze; ImpactQuery type must match)
  - 04-08 (Wave 7 — acceptance tests call impact.Analyze via HTTP)

# Tech tracking
tech-stack:
  added:
    - sqlc v1.31.1 (installed as ~/go/bin/sqlc binary; generator-only, not runtime dep)
    - github.com/jackc/pgx/v5/pgtype (pgtype.Timestamptz — already transitive, now directly used)
  patterns:
    - "sqlc pgx/v5 adapter: emit_methods_with_db_argument=true passes DB as second arg to each method"
    - "Generated CASE columns typed as interface{}: sqlc cannot infer concrete type from CASE expressions — asString() helper wraps type assertion"
    - "pgtype.Timestamptz for AsOf: sqlc maps timestamptz param to pgtype.Timestamptz; impact.Analyze wraps *time.Time conversion"
    - "Defense-in-depth for DoS (D-14): 3 independent guards: (1) impact.Analyze Go check depth>25, (2) SQL LEAST(@max_depth::int,25), (3) Wave 7 REST handler HTTP 400"
    - "sqlc-verify CI script: re-runs sqlc generate and fails if git diff is non-empty (mirrors proto-breaking Makefile pattern)"

key-files:
  created:
    - sqlc.yaml
    - internal/lineage/queries/lineage.sql
    - internal/lineage/queries/db.go
    - internal/lineage/queries/models.go
    - internal/lineage/queries/lineage.sql.go
    - internal/lineage/queries/querier.go
    - internal/lineage/queries/queries_smoke_test.go
    - internal/lineage/queries/lineage_integration_test.go
    - internal/lineage/impact/analyze.go
    - internal/lineage/impact/analyze_test.go
    - scripts/sqlc-verify.sh
  modified:
    - Makefile (added sqlc and sqlc-verify targets)

key-decisions:
  - "@column renamed to @col_name in lineage.sql: 'column' is a reserved word in sqlc's PostgreSQL SQL parser; renamed to @col_name which generates ColName in the params struct"
  - "sqlc installed as binary (~/go/bin/sqlc) not via go run: go.mod has a replace directive that blocks go run for external modules with their own replace directives; built sqlc from /tmp/sqlc_build with its own go.mod that includes the required replace directive"
  - "Makefile sqlc target uses PATH expansion to include ~/go/bin; scripts/sqlc-verify.sh also exports PATH so CI works without modifying system PATH"
  - "pgtype.Timestamptz adapter: impact.Analyze converts *time.Time to pgtype.Timestamptz{Time: t, Valid: true} before passing to sqlc-generated params"
  - "interface{} rows from CASE expressions: sqlc types CASE-expression result columns as interface{} when it cannot determine a concrete type; asString() handles string/[]byte/fmt.Sprintf fallback"
  - "pgx.Connect per integration test (not pgxpool): each test gets its own pgx.Conn for simplicity; pool sizing is a production concern"

# Metrics
duration: 10min
completed: 2026-05-09
---

# Phase 4 Plan 06: sqlc 递归 CTE 遍历 + impact.Analyze 库总结

**项目中首次引入 sqlc 工具，双向递归 CTE 遍历 asset_edges 和 column_edges，带环检测、深度上限和时间点 AsOf；impact.Analyze 公共 Go 库封装生成的查询；10 个集成测试覆盖线性链、分支树、环、深度上限、仅活跃/AsOf、列遍历和 SQL 层防御深度**

## Performance

- **Duration:** ~10 min
- **Started:** 2026-05-09T03:53:05Z
- **Completed:** 2026-05-09T04:03:00Z
- **Tasks:** 2 of 2
- **Files modified:** 11 created, 1 modified

## Accomplishments

### Task 1: sqlc 工具 + lineage.sql + 生成的绑定

**项目根目录的 `sqlc.yaml`：**
- Engine: postgresql, queries: `internal/lineage/queries/lineage.sql`, schema: `migrations/`
- Package: `lineageq`, sql_package: `pgx/v5`
- `emit_methods_with_db_argument: true` — DB 作为第二个参数显式传递
- `emit_interface: true` — 生成 Querier 接口用于可测试性

**lineage.sql — 两个命名的递归 CTE：**

```sql
-- name: TraverseAssetLineage :many
WITH RECURSIVE lineage AS (
    -- Base: assets adjacent to @asset in @direction
    SELECT ... FROM asset_edges WHERE ...
    UNION ALL
    SELECT ... FROM asset_edges e JOIN lineage l ON ...
    WHERE l.depth < LEAST(@max_depth::int, 25)  -- D-14 layer 2
      AND NOT (l.path @> ARRAY[next_asset]::text[])  -- cycle guard
)
SELECT DISTINCT asset, depth FROM lineage ORDER BY depth, asset;

-- name: TraverseColumnLineage :many
-- Same template on column_edges with (asset, column) path tuples
```

Key CTE properties:
- **Direction**: `CASE WHEN @direction::text = 'downstream' THEN to_asset ELSE from_asset END` — single query handles both directions
- **Cycle guard**: `NOT (l.path @> ARRAY[next_node]::text[])` — PostgreSQL array containment check accumulates path; prevents infinite loops (Pitfall 2)
- **Depth cap (layer 2)**: `l.depth < LEAST(@max_depth::int, 25)` — SQL-level ceiling even if caller bypasses Go layer
- **Active-edge toggle**: `CASE WHEN @use_as_of::bool THEN ... point-in-time ... ELSE superseded_at IS NULL END` — switches between active-only and point-in-time modes (D-15)

**生成的绑定** (`lineage.sql.go`)：
- `TraverseAssetLineageParams{Direction, Asset, UseAsOf, AsOf pgtype.Timestamptz, MaxDepth int32}`
- `TraverseColumnLineageParams{Direction, Asset, ColName, UseAsOf, AsOf pgtype.Timestamptz, MaxDepth int32}`
- Row types: `TraverseAssetLineageRow{Asset interface{}, Depth int32}` and `TraverseColumnLineageRow{Asset, ColumnName interface{}, Depth int32}` — `interface{}` because sqlc cannot infer concrete types from CASE expressions
- `Querier` interface with both methods for testability

**Makefile targets:**
- `make sqlc` — runs `PATH="${HOME}/go/bin:${PATH}" sqlc generate`
- `make sqlc-verify` — runs `./scripts/sqlc-verify.sh` (re-generates + git diff check)

### Task 2: impact.Analyze + 集成测试

**impact.Analyze API (D-19/D-20):**

```go
func Analyze(ctx context.Context, db lineageq.DBTX, q ImpactQuery) (Impact, error)

type ImpactQuery struct {
    Asset     string      // required
    Column    *string     // nil = asset-level; non-nil = column-level
    Direction string      // "upstream" | "downstream"
    Depth     int         // ≤ 0 → DefaultDepth(10); > MaxDepth(25) → ErrDepthExceeded
    AsOf      *time.Time  // nil = active-edges-only; non-nil = point-in-time
}
```

**Error sentinels:**
- `ErrAssetRequired` — empty Asset
- `ErrInvalidDirection` — not "upstream" or "downstream"
- `ErrDepthExceeded` — depth > MaxDepth (25)

**三防御深度上限链（D-14）：**
1. `impact.Analyze` Go check: `if q.Depth > MaxDepth { return ErrDepthExceeded }` — no DB call made
2. SQL `WHERE l.depth < LEAST(@max_depth::int, 25)` — SQL cap independent of Go layer
3. Wave 7 REST handler `?depth > 25` → HTTP 400 (not yet implemented; deferred to plan 04-07)

**集成测试覆盖矩阵：**

| Test | CTE Path | Fixture | Assertion |
|------|----------|---------|-----------|
| TestRecursiveCTELinearDepth1 | downstream | SeedDAG(1) | 1 node at depth 1 |
| TestRecursiveCTELinearDepth5 | downstream + upstream | SeedDAG(5) | 5 nodes; upstream verified |
| TestRecursiveCTELinearDepth10 | downstream, depth cap | SeedDAG(10) | 10 nodes; depth=5 cap returns 5 |
| TestRecursiveCTECycleSafety | cycle guard path | SeedCycle | 1 node each direction, no loop |
| TestRecursiveCTEDepthCap25 | Go layer cap | SeedDAG(25) | depth=25 OK; depth=26 ErrDepthExceeded |
| TestCTEMaxDepthSQLEnforced | SQL LEAST cap | SeedDAG(30) | maxDepth=26/999 → rows ≤ 25 (D-14 layer 2) |
| TestRecursiveCTEBranching | fan-out | SeedBranching(3) | 14 nodes (all non-root) |
| TestRecursiveCTEActiveOnly | active-only + AsOf toggle | manual + SeedDAG | retired excluded in active mode, included in AsOf mode |
| TestRecursiveCTEColumnTraversal | column_edges CTE | manual column edges | A.col1→B.col2→C.col3; 2 nodes |
| TestRecursiveCTEPerformanceSmoke | latency | SeedDAG(10) | < 1s wall-clock |

## Task Commits

1. **Task 1: sqlc tooling + lineage.sql + generated bindings** - `2eccef2` (feat)
2. **Task 2: impact.Analyze + integration tests** - `88b874e` (feat)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] `@column` renamed to `@col_name` in lineage.sql**
- **Found during:** Task 1 (sqlc generate)
- **Issue:** `column` is a reserved keyword in the sqlc SQL parser; `sqlc generate` returned `syntax error at or near "column"` on line 67 of lineage.sql where `@column::text` appeared
- **Fix:** Renamed all occurrences of `@column` to `@col_name` in lineage.sql (2 occurrences in the WHERE clause of TraverseColumnLineage). The generated params struct field is `ColName` instead of `Column`. The `impact.Analyze` `ImpactQuery.Column *string` field name is unchanged (it's a Go field, not SQL).
- **Files modified:** `internal/lineage/queries/lineage.sql`, `internal/lineage/impact/analyze.go` (uses `ColName`)
- **Impact:** The `TraverseColumnLineageParams` struct has field `ColName string` instead of `Column string`. impact.Analyze correctly maps `*q.Column` to `ColName: *q.Column`.
- **Committed in:** `2eccef2` (Task 1)

**2. [Rule 3 - Blocking] sqlc cannot be installed via `go run` due to `replace` directive in go.mod**
- **Found during:** Task 1 (`make sqlc`)
- **Issue:** `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.0` fails with "The go.mod file for the module providing named packages contains one or more replace directives". The project's go.mod has `replace github.com/cheikhathch/omitempty => ...` which blocks `go run` for external modules that themselves have replace directives (sqlc's go.mod has `replace github.com/go-sql-driver/mysql => ...`).
- **Fix:** Built sqlc binary from a temporary module at `/tmp/sqlc_build/` that includes sqlc's own replace directive. Binary installed to `~/go/bin/sqlc`. Makefile `sqlc` target updated to use `PATH="${HOME}/go/bin:${PATH}" sqlc generate`. `scripts/sqlc-verify.sh` exports `PATH` similarly.
- **Impact:** `make sqlc` and `make sqlc-verify` both work; `make sqlc-verify` exits 0 after generation (idempotent). The installed binary is sqlc v1.31.1 (one patch above the plan's v1.31.0 — same generation output).
- **Committed in:** `2eccef2` (Task 1)

**3. [Rule 2 - Missing functionality] `pgtype.Timestamptz` adapter in `impact.Analyze`**
- **Found during:** Task 2 (understanding generated types)
- **Issue:** The plan specified `AsOf time.Time` in the params. sqlc generates `AsOf pgtype.Timestamptz` in `TraverseAssetLineageParams`. Without adaptation, `time.Time` cannot be passed directly.
- **Fix:** `impact.Analyze` converts `*time.Time` to `pgtype.Timestamptz{Time: *q.AsOf, Valid: true}` before calling sqlc-generated methods. If AsOf is nil, `pgtype.Timestamptz{}` (zero value, `Valid: false`) is passed and `UseAsOf=false` tells SQL to use `superseded_at IS NULL` path.
- **Files modified:** `internal/lineage/impact/analyze.go`
- **Committed in:** `88b874e` (Task 2)

**4. [Rule 2 - Missing functionality] `asString()` type assertion helper for CASE-expression columns**
- **Found during:** Task 2 (reviewing generated row types)
- **Issue:** sqlc types CASE-expression result columns as `interface{}` because it cannot statically determine the concrete type. `TraverseAssetLineageRow.Asset` and `TraverseColumnLineageRow.Asset`, `ColumnName` are all `interface{}`. Without conversion, callers would need to type-assert everywhere.
- **Fix:** Added `asString(v interface{}) string` helper in `analyze.go` that handles `string`, `[]byte`, and generic `fmt.Sprintf("%v", v)` fallback. Used internally in `Analyze` when building `ImpactNode` values.
- **Files modified:** `internal/lineage/impact/analyze.go`
- **Committed in:** `88b874e` (Task 2)

## Known Stubs

None — all recursive CTE paths are wired end-to-end. The impact.Analyze library is a pure Go library with no placeholder values or TODO paths. The integration tests verify all data flows.

## Threat Surface Scan

No new network endpoints introduced by this plan. The impact.Analyze function is a library call — all trust boundary management is left to the Wave 7 REST handler (plan 04-07). The SQL uses parameterized queries throughout (no string interpolation). The sqlc-verify CI script prevents drift between lineage.sql and generated bindings.

| Flag | File | Description |
|------|------|-------------|
| threat_flag: new_sql_query_path | internal/lineage/queries/lineage.sql | Recursive CTE traversal on asset_edges and column_edges; bounded by LEAST(@max_depth::int,25) + cycle guard + active-edge filter. Parameterized queries prevent SQL injection (T-04-06-02). |

## Self-Check: PASSED

| Check | Status |
|-------|--------|
| `sqlc.yaml` exists with `engine: postgresql`, `package: lineageq`, `sql_package: pgx/v5` | PASS |
| `internal/lineage/queries/lineage.sql` has TraverseAssetLineage + TraverseColumnLineage | PASS |
| `grep -c 'LEAST(@max_depth::int, 25)' lineage.sql` = 2 | PASS |
| Cycle guard `NOT (l.path @>` present in lineage.sql | PASS |
| `db.go`, `models.go`, `lineage.sql.go`, `querier.go` generated by sqlc | PASS |
| `scripts/sqlc-verify.sh` exists and is executable | PASS |
| Makefile has `sqlc:` and `sqlc-verify:` targets | PASS |
| `make sqlc-verify` exits 0 (generated files in sync) | PASS |
| `go build ./...` exits 0 | PASS |
| `TestQueriesInterface` passes | PASS |
| `func Analyze` in analyze.go; ErrDepthExceeded, ErrAssetRequired, ErrInvalidDirection | PASS |
| `MaxDepth = 25`, `DefaultDepth = 10` | PASS |
| All unit tests pass (no DB): TestAnalyzeAssetRequired, TestAnalyzeInvalidDirection, TestAnalyzeDepthExceeded | PASS |
| Integration tests (10 scenarios) all pass with Docker/testcontainers | PASS |
| Commit `2eccef2` exists (Task 1) | PASS |
| Commit `88b874e` exists (Task 2) | PASS |

---
*Phase: 04-schema*
*Completed: 2026-05-09*