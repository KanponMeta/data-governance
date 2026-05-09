---
phase: 04-schema
plan: 05
subsystem: schema-diff-classifier
tags: [schema, diff, classifier, breaking-change, lattice, writer, capture, postgresql]

# Dependency graph
requires:
  - phase: 04-schema
    plan: 01
    provides: schematest.DiffPairs() — 9 ChangeKind fixture pairs
  - phase: 04-schema
    plan: 02
    provides: schema_changes table DDL + SchemaChange ent entity + connector.Schema types
  - phase: 04-schema
    plan: 04
    provides: schema.Writer.Capture + HashSchema + schema_versions INSERT/dedup path

provides:
  - internal/schema/diff.go: ChangeKind enum (9 values) + SchemaChange struct + Diff() pure-Go function
  - internal/schema/lattice_postgres.go: IsWideningPostgres() + parseVarchar + parseDecimal helpers
  - internal/schema/classify.go: Classify(SchemaChange, latticeFn) → (changeType string, isBreaking bool)
  - internal/schema/writer_diff.go: WriteSchemaChanges(ctx, tx, ...) → []uuid.UUID
  - internal/schema/capture.go: extended Capture to call Diff + WriteSchemaChanges + emit audit-pointer payload

affects:
  - 04-06 (CTE traversal — schema_changes rows now exist for timeline derivation, META-05)
  - 04-07 (REST endpoints — schema_changes rows readable via timeline query)
  - 04-08 (acceptance tests — TestCaptureWithDiff + schema_changes rows verifiable end-to-end)

# Tech tracking
tech-stack:
  added: []  # no new dependencies; uses database/sql, uuid, regexp, strconv from stdlib + go.mod
  patterns:
    - "Provisional ChangeKind pattern: Diff emits ChangeTypeWidened for all type changes; Classify resolves to type_widened or type_narrowed via lattice (avoids Diff needing lattice dependency)"
    - "Lattice function pointer in Classify: enables per-connector lattices in Phase 5+ without changing Classify signature"
    - "prevVersionData struct in capture.go: bundles id+hash+schema for the single latestQuery that replaces separate queries"
    - "uuidsToStrings helper: converts []uuid.UUID to []string for JSON-serializable event payloads"
    - "T-04-05-01 unmarshal guard: json.Unmarshal failure on schema_data treated as missing prev (hasPrev=false), avoiding diff on corrupt data"

key-files:
  created:
    - internal/schema/diff.go
    - internal/schema/diff_test.go
    - internal/schema/lattice_postgres.go
    - internal/schema/lattice_postgres_test.go
    - internal/schema/classify.go
    - internal/schema/classify_test.go
    - internal/schema/writer_diff.go
    - internal/schema/writer_diff_test.go
    - internal/schema/writer_diff_integration_test.go
  modified:
    - internal/schema/capture.go (latestQuery extended to SELECT schema_data; WriteSchemaChanges call + audit-pointer payload)
    - migrations/20260506062521_initial.sql (Rule 1 fix: quoted 'user' reserved keyword)

key-decisions:
  - "Provisional ChangeTypeWidened from Diff: Diff never needs to import the lattice; the provisional kind is resolved in Classify. This keeps Diff pure set-comparison logic."
  - "Decimal widening rule: np >= op AND ns >= os (both precision AND scale must be >=). scale=4 has MORE fractional digits than scale=2, so decimal(10,2) → decimal(10,4) is widening."
  - "First-capture behavior: when hasPrev=false (no prior schema_versions row), WriteSchemaChanges is called with empty changes, returns nil IDs, schema.change_detected emits schema_changes_ids=[] — this is correct (no diff to record on first capture)."
  - "schema_changes_ids test: verified the LAST schema.change_detected event (not the first) has non-empty IDs — first capture legitimately has empty IDs."
  - "Migration reserved-word fix: ALTER TABLE user → ALTER TABLE 'user' (pre-existing bug in initial.sql; blocked all integration tests using StartPhase4Container)"

# Metrics
duration: 17min
completed: 2026-05-09
---

# Phase 4 Plan 05: Schema Diff Classifier + Breaking-Change Writer Summary

**Schema diff classifier (Diff + IsWideningPostgres + Classify) and writer (WriteSchemaChanges) wired into schema.Writer.Capture, with all 9 schematest fixture pairs verified end-to-end through Diff → Classify → schema_changes DB rows + audit-pointer event payloads**

## Performance

- **Duration:** ~17 min
- **Started:** 2026-05-09T11:32:40Z
- **Completed:** 2026-05-09T11:49:35Z
- **Tasks:** 2 of 2
- **Files modified:** 11 (9 created, 2 modified)

## Accomplishments

### Task 1: Pure-Go Diff + Lattice + Classify

**`schema.Diff(prev, next connector.Schema) []SchemaChange`**

Column-by-column set comparison producing an ordered list of changes:
1. `column_added` (columns in next but not prev)
2. `column_dropped` (columns in prev but not next)
3. In-place attribute changes per column (sorted alphabetically): type change (provisional `ChangeTypeWidened`) → nullable → default → comment
4. `pk_changed` if PrimaryKey slice differs

Renames produce drop+add pairs per CONTEXT.md (no heuristic rename detection).

**`schema.IsWideningPostgres(oldType, newType) (isWidening, known bool)`**

PostgreSQL type lattice for Phase 4:

| Family | Examples | Rule |
|--------|---------|------|
| Integer | int16 < int32 < int64 | `newRank >= oldRank` |
| Float | float32 < float64 | `newRank >= oldRank` |
| String | varchar(N1) < varchar(N2) if N2>=N1; varchar(*) < text | N2>=N1 or new=text |
| Decimal | decimal(p1,s1) → decimal(p2,s2) | `p2>=p1 AND s2>=s1` (both precision AND scale) |
| Cross-family | int→text, decimal→varchar, bool→int | `known=false` |

**Decimal scale rule confirmed:** `np >= op AND ns >= os` (both must be >=).
- `decimal(10,2) → decimal(10,4)`: s2(4) >= s1(2) → widening ✓ (more fractional digits)
- `decimal(10,4) → decimal(10,2)`: s2(2) < s1(4) → narrowing ✓ (fewer fractional digits)

**`schema.Classify(c SchemaChange, lattice func(string,string)(bool,bool)) (string, bool)`**

| Kind (provisional from Diff) | Lattice result | DB change_type | is_breaking |
|------------------------------|----------------|----------------|-------------|
| ChangeColumnAdded | n/a | column_added | false |
| ChangeColumnDropped | n/a | column_dropped | true |
| ChangeTypeWidened | widening=true, known=true | type_widened | false |
| ChangeTypeWidened | widening=false, known=true | type_narrowed | true |
| ChangeTypeWidened | known=false (cross-family) | type_narrowed | true (D-09 safe default) |
| ChangeNullableAdded | n/a | nullable_added | false |
| ChangeNullableRemoved | n/a | nullable_removed | true |
| ChangePKChanged | n/a | pk_changed | true |
| ChangeCommentChanged | n/a | comment_changed | false |
| ChangeDefaultChanged | n/a | default_changed | false |

### Task 2: WriteSchemaChanges + Capture Integration

**`schema.WriteSchemaChanges(ctx, tx, runID, asset, codeHash, prevVersionID, newVersionID, changes) ([]uuid.UUID, error)`**

- INSERTs one `schema_changes` row per `SchemaChange` in the supplied tx (atomic with `schema_versions` INSERT — D-11)
- Calls `Classify(c, IsWideningPostgres)` per change to determine `change_type` + `is_breaking`
- `column_name = ""` maps to DB NULL (PK-level changes)
- `prevVersionID = nil` maps to DB NULL (first capture)
- Returns `[]uuid.UUID` for audit-pointer payload on `schema.change_detected` event

**`schema.Writer.Capture` upgrade (D-11 audit pointer):**

```
latestQuery: SELECT id, schema_hash, schema_data FROM schema_versions ...
```

New flow on hash difference:
1. INSERT schema_versions (existing)
2. `changes = Diff(prev.schema, captured)` (if hasPrev)
3. `changeIDs, err = WriteSchemaChanges(ctx, tx, ..., changes)` — within same tx
4. Emit `schema.change_detected` with:
   ```json
   {"asset": "...", "schema_hash": "...", "new_version_id": "...", "code_hash": "...",
    "schema_changes_ids": ["uuid1", "uuid2", ...], "prev_version_id": "..."}
   ```

### 9 Schematest Fixtures: All Pass

| Fixture | Expected ChangeKind | is_breaking | Result |
|---------|---------------------|-------------|--------|
| column_added | column_added | false | PASS |
| column_dropped | column_dropped | true | PASS |
| type_narrowed | type_narrowed | true | PASS |
| type_widened | type_widened | false | PASS |
| nullable_added | nullable_added | false | PASS |
| nullable_removed | nullable_removed | true | PASS |
| pk_changed | pk_changed | true | PASS |
| comment_changed | comment_changed | false | PASS |
| default_changed | default_changed | false | PASS |

## Task Commits

1. **Task 1: Diff + IsWideningPostgres + Classify** - `8c24bc6` (feat)
2. **Task 2: WriteSchemaChanges + Capture integration** - `de8c84e` (feat)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed `ALTER TABLE user` reserved-keyword syntax error in initial migration**
- **Found during:** Task 2 (integration test execution)
- **Issue:** `migrations/20260506062521_initial.sql` lines 61 and 66 used `ALTER TABLE user` and `GRANT ... ON user` without quoting `user`, which is a PostgreSQL reserved keyword. This caused `ERROR: syntax error at or near "user"` in `applyMigrations`, blocking ALL integration tests that call `StartPhase4Container`.
- **Fix:** Quoted `user` as `"user"` in both statements
- **Files modified:** `migrations/20260506062521_initial.sql`
- **Verification:** `go test -tags=integration ./internal/runtime/executortest/...` exits 0
- **Committed in:** `de8c84e` (Task 2)

**2. [Rule 1 - Bug] Test checked first schema.change_detected event instead of last**
- **Found during:** Task 2 (integration TestCaptureWithDiff failure)
- **Issue:** `TestCaptureWithDiff` checked `changeDetectedEvents[0]` for `schema_changes_ids`. The FIRST `schema.change_detected` event is emitted on initial capture (no prev), and legitimately has `schema_changes_ids: []`. The SECOND event (on diff capture) has the non-empty IDs.
- **Fix:** Changed test to check `changeDetectedEvents[len-1]` (last event) and asserted `len >= 2`
- **Files modified:** `internal/schema/writer_diff_integration_test.go`
- **Verification:** `TestCaptureWithDiff` passes
- **Committed in:** `de8c84e` (Task 2)

**3. [Rule 1 - Bug] sqlmock unit tests needed Begin/Rollback expectations**
- **Found during:** Task 2 (unit test execution)
- **Issue:** `go-sqlmock` requires explicit `ExpectBegin()` and `ExpectRollback()` calls before the test calls `db.Begin()` and `tx.Rollback()`. Without them, mock reports "unexpected call to Begin"
- **Fix:** Added `mock.ExpectBegin()` + `mock.ExpectRollback()` to both unit test cases
- **Files modified:** `internal/schema/writer_diff_test.go`
- **Committed in:** `de8c84e` (Task 2)

### Design Notes

- **First-capture behavior:** When there is no prior `schema_versions` row (`hasPrev=false`), `WriteSchemaChanges` is called with empty `changes`, returns nil IDs, and `schema.change_detected` emits `schema_changes_ids: []`. This is correct — there is no prev schema to diff against on first capture.
- **Unmarshal failure guard:** If `json.Unmarshal(rawSchemaData, &prev.schema)` fails (T-04-05-01), `hasPrev` is set to false and treated as first capture, avoiding diff on corrupt data.

## Threat Surface Scan

No new network endpoints, auth paths, file access patterns, or schema changes introduced. All DB writes use parameterized SQL (`$1...$14`). Existing threat mitigations from plan 04-05 threat register applied as implemented.

## Known Stubs

None — all functionality is fully wired. `schema_changes_ids` in `schema.change_detected` payload are real inserted UUIDs from DB.

## Self-Check: PASSED

| Check | Status |
|-------|--------|
| `go build ./...` exits 0 | PASS |
| `go test -race ./internal/schema/... -run 'TestDiff|TestIsWideningPostgres|TestClassify'` exits 0 | PASS |
| `go test ./internal/schema/... -run TestClassifyAllSchemaTestFixtures -v` shows 9 sub-tests PASS | PASS |
| `go test -race ./internal/schema/... -run TestWriteSchemaChangesEmpty` exits 0 | PASS |
| `go test -tags=integration -race ./internal/schema/...` exits 0 | PASS |
| `go test -race ./internal/event/... ./internal/run/... ./internal/asset/... ./internal/connector/...` exits 0 | PASS |
| `grep -q 'func Diff(prev, next connector.Schema)' internal/schema/diff.go` | PASS |
| `grep -q 'func IsWideningPostgres' internal/schema/lattice_postgres.go` | PASS |
| `grep -q 'func Classify' internal/schema/classify.go` | PASS |
| `grep -q 'func WriteSchemaChanges' internal/schema/writer_diff.go` | PASS |
| `grep -q 'WriteSchemaChanges' internal/schema/capture.go` | PASS |
| `grep -q 'schema_changes_ids' internal/schema/capture.go` | PASS |
| `grep -q 'Diff(prev.schema, captured)' internal/schema/capture.go` | PASS |
| Commit 8c24bc6 exists | PASS |
| Commit de8c84e exists | PASS |

---
*Phase: 04-schema*
*Completed: 2026-05-09*
