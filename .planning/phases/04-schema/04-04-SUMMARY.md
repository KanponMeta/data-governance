---
phase: 04-schema
plan: 04
subsystem: lineage-schema-capture
tags: [lineage, schema, executor, tracking-io, drift-detection, content-addressed, atomicity]

# Dependency graph
requires:
  - phase: 04-schema
    plan: 03
    provides: connector.SchemaDescriber, asset.MaterializeResult typed fields, asset.CodeHash()

provides:
  - internal/asset/io_tracking.go: TrackingIO decorator + NewTrackingIO (D-04 platform-driven drift)
  - internal/lineage/capture.go: lineage.Writer with SyncStaticEdges + CaptureRun
  - internal/schema/hash.go: HashSchema stable SHA-256 content-addressed hash (D-08)
  - internal/schema/capture.go: schema.Writer with Capture (D-05/D-06)
  - internal/asset/registry.go: OnRegister hook (D-01 static derivation)
  - internal/runtime/executor.go: commitSuccess transactional boundary + trackingIO wiring (D-21)
  - migrations/20260509120000_phase4_lineage_schema.sql: asset_edges_active_unique + column_edges_active_unique partial unique indices

affects:
  - 04-05 (Wave 4 — diff classifier reads schema.change_detected events from this plan)
  - 04-06 (Wave 5 — CTE traversal tests use asset_edges/column_edges written by this plan)
  - 04-07 (Wave 6 — REST catalog reads asset_versions written by this plan)
  - 04-08 (Wave 7 — acceptance tests verify full end-to-end lineage + schema capture)

# Tech tracking
tech-stack:
  added: []  # no new dependencies
  patterns:
    - "TrackingIO decorator: wraps AssetIO without changing the user-facing interface; records Read() calls for D-04 platform-driven drift detection"
    - "Partial unique index ON CONFLICT: asset_edges_active_unique / column_edges_active_unique provide named ON CONFLICT targets for idempotent UPSERT (D-15 soft-retire pattern)"
    - "Per-step BeginTx: commitSuccess wraps lineage + schema writes for a single step; prior successful steps retain their lineage rows even when a later step fails"
    - "uuid.Nil sentinel for first_seen_run_id: SyncStaticEdges inserts nil UUID as placeholder; CaptureRun promotes it to real runID on first materialization"
    - "Non-fatal schema capture: DescribeSchema errors emit schema.capture_failed event and return nil so the run still succeeds (D-08)"
    - "HashSchema canonical form: column sort alphabetically, exclude RowCountEstim/CapturedAt/Comment, preserve PK order"

key-files:
  created:
    - internal/asset/io_tracking.go
    - internal/asset/io_tracking_test.go
    - internal/lineage/capture.go
    - internal/lineage/capture_test.go
    - internal/schema/hash.go
    - internal/schema/hash_test.go
    - internal/schema/capture.go
    - internal/schema/capture_test.go
  modified:
    - internal/asset/registry.go (OnRegister hook field)
    - internal/asset/registry_test.go (TestRegistryOnRegisterHook* tests)
    - internal/runtime/executor.go (LineageWriter/SchemaWriter Deps, trackingIO, commitSuccess)
    - internal/runtime/executor_test.go (TestExecutorWithoutPhase4Writers)
    - migrations/20260509120000_phase4_lineage_schema.sql (active unique indices appended)
    - migrations/atlas.sum (re-hashed)

key-decisions:
  - "Per-step BeginTx (not per-run): commitSuccess wraps lineage+schema for each step independently. A 5-step DAG preserves lineage rows from steps 1-4 even if step 5 fails (operationally most useful). The plan's context.md described wrapping the runs.state UPDATE but that would lose prior successful step lineage on any tx failure."
  - "uuid.Nil sentinel for first_seen_run_id: SyncStaticEdges inserts nil UUID because no runID is available at registration time. CaptureRun's UPDATE promotes it via CASE WHEN first_seen_run_id = '00000000-...' THEN $1 ELSE first_seen_run_id END"
  - "OnRegister releases the write lock before calling the hook: prevents deadlock if the hook calls Registry.Get(). The hook is called with the lock already released — safe because the asset is already committed to the map."
  - "schema.Writer.Capture with nil tx: unit tests pass nil tx to exercise event emission logic without DB. The implementation guards tx != nil before any SQL calls."

# Metrics
duration: 12min
completed: 2026-05-09
---

# Phase 4 Plan 04: Lineage Writer + Schema Writer + Executor Transactional Integration Summary

**Two writers that turn Wave 1's empty tables into a useful lineage + schema audit trail: lineage.Writer (SyncStaticEdges + CaptureRun), schema.Writer (HashSchema + Capture), executor.commitSuccess transactional boundary, and trackingIO D-04 platform-driven drift detection decorator**

## Performance

- **Duration:** ~12 min
- **Started:** 2026-05-09T03:17:15Z
- **Completed:** 2026-05-09T03:29:08Z
- **Tasks:** 4 of 4 (Task 0 + Tasks 1–3)
- **Files modified:** 14 (8 created, 6 modified)

## Accomplishments

### Task 0: TrackingIO Decorator (D-04 platform-driven drift detection)

`internal/asset/io_tracking.go`:
- `TrackingIO` interface embeds `AssetIO` and adds `Observed() []string`
- `NewTrackingIO(inner AssetIO) TrackingIO` wraps any AssetIO
- Records every `Read()` call — even on error (drift cares about intent, not success)
- `Observed()` returns sorted, deduplicated upstream names
- concurrent-safe via `sync.Mutex`; `Write`/`PartitionKey` are pure pass-throughs
- Tests: TestTrackingIORecordsRead, TestTrackingIORecordsOnError, TestTrackingIOEmpty, TestTrackingIOConcurrent, TestTrackingIOWritePassThrough, TestTrackingIOPartitionKeyPassThrough — all pass under -race

### Task 1: Lineage Writer + Registry Hook

`internal/lineage/capture.go`:
- `lineage.Writer{db, events}` with `NewWriter(db, events)`
- `SyncStaticEdges(ctx, asset, codeHash)`:
  - Idempotent UPSERT of `asset_edges` for each declared upstream (D-01)
  - ON CONFLICT ON CONSTRAINT `asset_edges_active_unique` (partial unique index)
  - D-15 soft-retire: UPDATE `superseded_at=NOW()` for removed upstreams
  - Guard: returns error if >256 upstreams (DoS protection T-04-04-03)
  - Returns nil for assets with 0 upstreams (source assets are a no-op)
- `CaptureRun(ctx, tx, runID, asset, result, codeHash, observedUpstreams)`:
  - Step 1: Resolve column lineage source — `result.ColumnLineage` > `a.ColumnLineage()` > `"undeclared"` (D-02)
  - Step 2: UPSERT `column_edges` for each (output, source) pair (D-13/D-15)
  - Step 3: UPDATE `asset_edges` last_seen_* / promote uuid.Nil sentinel to real runID
  - Step 4: UPSERT `asset_versions` (asset, code_hash) — first run inserts; subsequent runs no-op
  - Step 5: D-04 drift detection — `sameSet(observed, declared)` → emit `lineage.drift_detected` + UPDATE `drift_status='pending'`
  - Step 6: Emit `lineage.captured` with full declared/observed context

`internal/asset/registry.go` — `OnRegister func(*Asset) error` hook:
- Called after in-memory registration (lock released before hook call)
- Hook failure propagates to caller but does NOT undo in-memory registration
- nil = no-op (all existing tests unaffected)

Migration amendment: `asset_edges_active_unique` + `column_edges_active_unique` partial unique indices appended to `migrations/20260509120000_phase4_lineage_schema.sql`; `atlas.sum` updated.

### Task 2: Schema Writer (HashSchema + Capture)

`internal/schema/hash.go`:
- `HashSchema(s connector.Schema) string` — 64-char hex SHA-256
- Canonical form: columns sorted alphabetically by Name, excluding RowCountEstim/CapturedAt/Comment
- PrimaryKey list preserved in original order (composite PK order is meaningful)
- Concurrent-safe (pure function; `json.Marshal` + `sha256.Sum256` are goroutine-safe)
- Tests: deterministic (100x), concurrent (50 goroutines), column-order-independent, PK-order-sensitive, volatile-field-ignoring, comment-ignoring, sensitive-field-diffing

`internal/schema/capture.go`:
- `schema.Writer{events}` with `NewWriter(events)`
- `Capture(ctx, tx, runID, asset, result, conn, ref, codeHash)`:
  - Resolution: `connector.SchemaDescriber` > `result.Schema` > unsupported tag
  - Non-fatal: DescribeSchema errors emit `schema.capture_failed`, return nil
  - DoS guards: >10000 columns or 0 columns from descriptor → `schema.capture_failed`
  - Dedup: SELECT latest `schema_hash` → UPDATE `last_seen_*` (unchanged) or INSERT new version (changed)
  - Emits: `schema.captured` + `schema.unchanged` (dedup case) or `schema.change_detected` (new version)
  - nil tx: unit tests pass nil tx; SQL paths guarded by `if tx != nil`

### Task 3: Executor Transactional Integration

`internal/runtime/executor.go`:
- `runtime.Deps` gains `LineageWriter *lineage.Writer` + `SchemaWriter *schema.Writer` (both optional)
- `runStep` wraps `asset.NewAssetIO` with `asset.NewTrackingIO` before `safeMaterialize`
- Success branch replaced with `e.commitSuccess(ctx, runID, a, result, durationMs, tracker.Observed())`
- `commitSuccess()` method:
  - `db.BeginTx(LevelReadCommitted)`
  - `LineageWriter.CaptureRun(tx, observedUpstreams)` if non-nil
  - `SchemaWriter.Capture(tx, conn, ref)` if non-nil
  - `tx.Commit()` → emit `run.step.succeeded` event
  - On error: `defer tx.Rollback()`, return error, run stays 'running'
- nil writers skip gracefully — all existing executor tests pass unchanged

## Architecture Deviation: Per-Step BeginTx

The plan's context (04-RESEARCH.md §3) described wrapping `runs.state UPDATE` (running → succeeded) in the transaction. The implementation uses per-step transactions for lineage/schema writes instead.

**Rationale:** A 5-step DAG where steps 1-4 succeed and step 5 fails would lose the lineage/schema rows from steps 1-4 if we rolled back the per-run transaction. Per-step atomicity preserves the "best-effort lineage capture per successful step" invariant that's operationally most useful. The runs.state UPDATE (running → succeeded) happens in the existing `e.transition()` call after all steps complete, unchanged from Phase 1-3.

**Impact:** If the outer `e.transition()` fails (e.g., reaper resets the run to queued), lineage rows for completed steps are pre-committed but valid (they reference runID which still exists in runs). Re-running will UPSERT idempotently (no duplicate rows).

## uuid.Nil Sentinel Pattern

`SyncStaticEdges` inserts `uuid.Nil` (all-zeros UUID) as `first_seen_run_id` because no runID is available at registration time. When the first materialization runs, `CaptureRun`'s UPDATE promotes it:

```sql
first_seen_run_id = CASE
    WHEN first_seen_run_id = '00000000-0000-0000-0000-000000000000' THEN $1
    ELSE first_seen_run_id
    END
```

This pattern is documented in the code comment and is the same sentinel UUID used by PostgreSQL drivers for zero-value UUIDs.

## Task Commits

| Task | Name | Commit |
|------|------|--------|
| 0 | trackingIO decorator | `6819843` |
| 1 | Lineage writer + registry hook | `29a38db` |
| 2 | Schema writer (HashSchema + Capture) | `d3a0869` |
| 3 | Executor transactional integration | `c525ccf` |

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] `const retireSQL` cannot use runtime string concatenation**
- **Found during:** Task 1 (build verification)
- **Issue:** Go does not allow `const` declarations with runtime function calls; `placeholders(len(ups), 3)` is a runtime expression
- **Fix:** Changed `const retireSQL` to `retireSQL :=` (variable assignment)
- **Files modified:** `internal/lineage/capture.go`
- **Verification:** `go build ./...` exits 0

**2. [Rule 1 - Bug] `noopMaterialize` redeclared in registry_test.go**
- **Found during:** Task 1 (test compilation)
- **Issue:** `asset_test.go` already declares `noopMaterialize`; adding it to `registry_test.go` caused a "redeclared in block" error
- **Fix:** Removed the duplicate from `registry_test.go` (both files are in `package asset`)
- **Files modified:** `internal/asset/registry_test.go`

**3. [Rule 1 - Bug] `Build()` returns `(*Asset, error)` — tests used single return**
- **Found during:** Task 1 (test compilation in lineage package)
- **Issue:** `asset.New(...).Build()` has two return values; capture_test.go used single-return assignment pattern
- **Fix:** Added `buildTestAsset` helper that calls `Build()` and requires no error
- **Files modified:** `internal/lineage/capture_test.go`

**4. [Rule 2 - Missing critical] schema.Writer.Capture nil tx guard for unit tests**
- **Found during:** Task 2
- **Issue:** Unit tests cannot provide a real `*sql.Tx`; the plan's interface shows Capture always receiving a tx. Adding nil guards allows unit test coverage of event emission logic without DB
- **Fix:** Added `if tx != nil { ... }` guard around all SQL operations in Capture
- **Files modified:** `internal/schema/capture.go`

### Architectural Deviation

**Per-step BeginTx vs per-run (documented above):** The plan context described wrapping the runs.state UPDATE. Implementation uses per-step scope. Documented in "Architecture Deviation" section above.

## Known Stubs

None — all implementations are complete. No placeholder values, no hardcoded empty returns in the execution path.

## Threat Flags

None beyond those already documented in the plan's threat_model. The implementation mitigates:
- T-04-04-03: 256 upstream guard in SyncStaticEdges
- T-04-04-05: 10000 column guard in Capture
- T-04-04-07: 0 columns guard in Capture

## Self-Check: PASSED

| Check | Status |
|-------|--------|
| `internal/asset/io_tracking.go` exists with `NewTrackingIO` | PASS |
| `internal/lineage/capture.go` exists with `SyncStaticEdges` + `CaptureRun` | PASS |
| `internal/schema/hash.go` exists with `HashSchema` | PASS |
| `internal/schema/capture.go` exists with `Capture` | PASS |
| `internal/asset/registry.go` has `OnRegister` field | PASS |
| `internal/runtime/executor.go` has `LineageWriter`, `SchemaWriter`, `commitSuccess`, `BeginTx`, `NewTrackingIO` | PASS |
| `migrations/20260509120000_phase4_lineage_schema.sql` has `asset_edges_active_unique` | PASS |
| `atlas.sum` updated | PASS |
| `go build ./...` exits 0 | PASS |
| `go test -race ./internal/asset/...` exits 0 | PASS |
| `go test -race ./internal/lineage/...` exits 0 | PASS |
| `go test -race ./internal/schema/...` exits 0 | PASS |
| `go test -race ./internal/runtime/...` exits 0 | PASS |
| Phase 1/2/3 regression: event/run/connector packages | PASS |
| Commits 6819843, 29a38db, d3a0869, c525ccf exist | PASS |

---
*Phase: 04-schema*
*Completed: 2026-05-09*
