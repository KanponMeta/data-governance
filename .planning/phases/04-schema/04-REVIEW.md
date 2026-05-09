---
phase: 04-schema
reviewed: 2026-05-09T13:30:00Z
depth: standard
files_reviewed: 76
files_reviewed_list:
  - cmd/platform/impact.go
  - cmd/platform/impact_test.go
  - cmd/platform/lineage.go
  - cmd/platform/lineage_test.go
  - cmd/platform/main.go
  - cmd/platform/schema.go
  - cmd/platform/schema_test.go
  - internal/api/lineage_handlers.go
  - internal/api/lineage_handlers_test.go
  - internal/api/metadata_handlers.go
  - internal/api/schema_handlers.go
  - internal/api/schema_handlers_test.go
  - internal/asset/fingerprint.go
  - internal/asset/fingerprint_test.go
  - internal/asset/io_tracking.go
  - internal/asset/io_tracking_test.go
  - internal/asset/types.go
  - internal/connector/capability.go
  - internal/connector/firstparty/postgres/types_normalize.go
  - internal/connector/firstparty/postgres/types_normalize_test.go
  - internal/connector/schema_types.go
  - internal/connector/schema_types_test.go
  - internal/lineage/capture.go
  - internal/lineage/capture_test.go
  - internal/lineage/impact/analyze.go
  - internal/lineage/impact/analyze_test.go
  - internal/lineage/lineagetest/doc.go
  - internal/lineage/lineagetest/fixtures.go
  - internal/lineage/lineagetest/fixtures_smoke_test.go
  - internal/lineage/lineagetest/recursive_cte_seed.go
  - internal/lineage/openlineage/translate.go
  - internal/lineage/openlineage/translate_test.go
  - internal/lineage/queries/db.go
  - internal/lineage/queries/lineage_integration_test.go
  - internal/lineage/queries/lineage.sql
  - internal/lineage/queries/lineage.sql.go
  - internal/lineage/queries/models.go
  - internal/lineage/queries/querier.go
  - internal/lineage/queries/queries_smoke_test.go
  - internal/metadata/handler.go
  - internal/metadata/handler_test.go
  - internal/metadata/store.go
  - internal/metadata/store_test.go
  - internal/runtime/executortest/doc.go
  - internal/runtime/executortest/lineage_helpers.go
  - internal/runtime/executortest/lineage_helpers_smoke_test.go
  - internal/schema/capture.go
  - internal/schema/capture_test.go
  - internal/schema/classify.go
  - internal/schema/classify_test.go
  - internal/schema/diff.go
  - internal/schema/diff_test.go
  - internal/schema/hash.go
  - internal/schema/hash_test.go
  - internal/schema/lattice_postgres.go
  - internal/schema/lattice_postgres_test.go
  - internal/schema/schematest/doc.go
  - internal/schema/schematest/fixtures.go
  - internal/schema/schematest/fixtures_smoke_test.go
  - internal/schema/writer_diff.go
  - internal/schema/writer_diff_integration_test.go
  - internal/schema/writer_diff_test.go
  - internal/storage/ent/schema/asset_edge.go
  - internal/storage/ent/schema/asset_metadata.go
  - internal/storage/ent/schema/asset_version.go
  - internal/storage/ent/schema/column_edge.go
  - internal/storage/ent/schema/schema_change.go
  - internal/storage/ent/schema/schema_version.go
  - migrations/20260509120000_phase4_lineage_schema.sql
  - migrations/atlas.sum
  - scripts/explain_analyze_lineage.sh
  - scripts/seed_lineage_10k.sql
  - scripts/sqlc-verify.sh
  - sqlc.yaml
  - test/integration/phase4_e2e_test.go
findings:
  critical: 0
  warning: 8
  info: 7
  total: 15
status: issues_found
---

# Phase 4: Code Review Report

**Reviewed:** 2026-05-09T13:30:00Z
**Depth:** standard
**Files Reviewed:** 76
**Status:** issues_found

## Summary

Phase 4 introduces the schema and lineage subsystem (capture writers, recursive CTE traversal,
schema diff/classify, OpenLineage translator, metadata store, and CLI/REST endpoints). Overall
quality is high: parameterized queries are used consistently, depth-cap defense-in-depth is in
place at all three layers (Go-level `MaxDepth`, SQL `LEAST(@max_depth::int, 25)`, HTTP layer),
hand-managed partial indices align with the WHERE clauses in the writers, and the migration is
idempotent (uses `DROP ... IF EXISTS` + `ALTER TABLE ... DROP CONSTRAINT IF EXISTS` patterns).

No SQL injection was found — every dynamic SQL location uses bind parameters with carefully
generated placeholders. No hardcoded secrets. No direct command injection risks (the only
`exec.CommandContext` call is `runMigrate` invoking `atlas migrate apply --env <env>` with
`atlasEnv` validated to a default and fed in as a single argument, not interpolated).

The findings below are concentrated in three areas:

1. **Concurrency / atomicity gaps** — `ackSchemaChange` does an unprotected check-then-update
   (TOCTOU); `CaptureRun` step 3 updates `last_seen_*` for **all** active edges into the asset
   without restricting to the declared upstreams (so concurrent runs of *different* assets that
   share an upstream pattern stomp each other), and the `lineage.drift_detected` event is emitted
   on `event.Writer` (separate connection) inside the run-update transaction — drift state is
   atomic but the audit event is not.
2. **Defensive checks dropped** — `PrincipalFromContext` `ok` flag is discarded in
   `ackSchemaChange`, so a misconfigured route would still write `uuid.Nil` as actor; CLI helper
   `runImpact` argument-splitting treats any leading-`-` token as a flag (would mis-classify a
   positional starting with `-`).
3. **Test fragility / false-confidence patterns** — `cmd/platform/impact_test.go` reimplements
   `strings.Contains` with a buggy substring helper; the `containsAt` helper has redundant
   conditions and would mis-handle some inputs in edge cases, though the calls in this file
   never exercise those edges.

No Critical issues were found. The 8 Warnings are real correctness concerns worth fixing; the 7
Info items are quality/maintainability improvements.

## Warnings

### WR-01: TOCTOU race in `ackSchemaChange` between existence check and update

**File:** `internal/api/schema_handlers.go:41-57`

**Issue:** The handler `Get`s the schema_change row, checks `existing.AcknowledgedAt != nil`,
then issues a separate `UpdateOneID` to set the ack columns. There is no transaction wrapping
these two operations. Two concurrent governance-role callers can both pass the
`AcknowledgedAt == nil` check, then both succeed — the second `UpdateOneID` overwrites the
first ack's `acknowledged_by`/`reason`. D-10 documents this as "ack-once" but the handler
permits a last-writer-wins race, which violates that invariant.

**Fix:** Wrap in a transaction with conditional update, or use a SQL-level guarded UPDATE:

```go
// Option A: conditional UPDATE (returns 0 rows on conflict).
n, err := deps.Ent.SchemaChange.Update().
    Where(schemachange.IDEQ(id), schemachange.AcknowledgedAtIsNil()).
    SetAcknowledgedAt(now).
    SetAcknowledgedBy(principal.UserID).
    SetAcknowledgementReason(body.Reason).
    Save(r.Context())
if err != nil { ... }
if n == 0 {
    writeProblemJSON(w, http.StatusConflict, "already_acknowledged",
        "this schema change was already acknowledged")
    return
}
```

The CLI runSchemaAckBreak in `cmd/platform/schema.go:85-104` has the same TOCTOU pattern and
should be fixed the same way.

### WR-02: `PrincipalFromContext` ok flag dropped — `uuid.Nil` actor on misconfigured route

**File:** `internal/api/schema_handlers.go:38`

**Issue:** `principal, _ := auth.PrincipalFromContext(r.Context())` discards the `ok` flag.
The current router wires `auth.Middleware` + `RequireRole("governance")` ahead of this handler,
so principals are guaranteed in the deployed pipeline. But:
- A future refactor that drops or reorders middleware will silently start writing
  `uuid.Nil` into `acknowledged_by`, `event_log.actor_id`, and the event payload's
  `acknowledged_by` field — a serious audit-trail integrity hole that would not surface in
  unit tests.
- D-10 documents that the ack identity is the canonical accountability record.

**Fix:** Treat missing principal as a programming bug and 401:

```go
principal, ok := auth.PrincipalFromContext(r.Context())
if !ok {
    writeProblemJSON(w, http.StatusUnauthorized, "authentication_required",
        "authentication required")
    return
}
```

This matches the `metadata.Handler.patch` pattern in `internal/metadata/handler.go:85-89`.

### WR-03: `CaptureRun` updates `last_seen_*` for *every* active edge into the asset, not just declared upstreams

**File:** `internal/lineage/capture.go:184-200`

**Issue:** Step 3's UPDATE only filters by `to_asset = $3 AND superseded_at IS NULL`. It rewrites
`last_seen_run_id`, `last_seen_at`, and (when `first_seen_run_id = uuid.Nil`) `first_seen_run_id`
and `first_seen_at` for *all* active edges pointing at the asset, including edges that were
**not** declared in the current run. If the asset is partway through a partial migration where
a stale upstream edge has not yet been retired by `SyncStaticEdges`, this step attributes a
run to an edge the run did not actually consume — corrupting the audit trail and the D-15
point-in-time view.

In particular, the `uuid.Nil` sentinel promotion will set `first_seen_run_id` on a stale edge
on its first observation, which is wrong: the run did not produce that edge.

**Fix:** Restrict the UPDATE to the run's actual contributing set (intersection of declared
upstreams and observed upstreams, or just declared upstreams):

```go
// Only update edges that the run actually attributes to.
ups := a.Upstreams()
if len(ups) > 0 {
    args := make([]any, 0, 3+len(ups))
    args = append(args, runID, now, a.Name())
    for _, u := range ups { args = append(args, u) }
    sql := `UPDATE asset_edges SET last_seen_run_id=$1, last_seen_at=$2, ...
             WHERE to_asset=$3 AND superseded_at IS NULL
               AND from_asset IN (` + placeholders(len(ups), 4) + `)`
    if _, err := tx.ExecContext(ctx, sql, args...); err != nil { ... }
}
```

### WR-04: `lineage.drift_detected` event emission inside run-tx breaks atomicity

**File:** `internal/lineage/capture.go:235-250`

**Issue:** `CaptureRun` runs inside the executor's run-update transaction (`tx *sql.Tx`), but
`w.events.Append(...)` writes via `event.Writer` which uses a *separate* `*sql.DB` connection
(see Phase 1 `event.NewWriter(store)` precedent). The code comment acknowledges this: "The
event.Writer uses its own DB connection (not in our tx)." The implication is that if the
caller's transaction rolls back after `CaptureRun` succeeds, the `lineage.drift_detected`
event remains in `event_log` even though `asset_versions.drift_status` was rolled back to
`'clean'` — a divergence between event log and canonical state.

This is a pre-existing pattern (Phase 1 uses it for `auth.token_expired`), but D-21 explicitly
calls for atomicity between state changes and audit events for Phase 4. The Phase 4 capture
tests are passing only because the transaction commits in the happy path.

**Fix:** Inject an `event.Writer` variant that accepts `tx *sql.Tx`, or buffer events and
emit them after `tx.Commit()` from the executor side. The `schema.Capture` writer at
`internal/schema/capture.go:73-80` has the same pattern — same fix applies.

### WR-05: CLI `runImpact` mis-classifies positional args starting with `-`

**File:** `cmd/platform/impact.go:29-35`

**Issue:** The argument splitter treats any token starting with `-` as a flag:

```go
for _, a := range args {
    if len(a) > 0 && a[0] == '-' {
        flagArgs = append(flagArgs, a)
    } else {
        positional = append(positional, a)
    }
}
```

An asset name that happens to begin with `-` (e.g. an asset literally named `-foo` or a
deliberate adversarial name) would be silently shifted into `flagArgs` and rejected by
`fs.Parse`. The asset name validator on the REST side (`assetNameRE` allows `[a-zA-Z0-9_.-]`,
which permits leading `-`) is more permissive than this splitter — they disagree.

**Fix:** Stop splitting on the first non-flag token (the standard `--` convention works), or
require the asset name to be the *first* positional argument and treat everything else as
flags:

```go
if len(args) == 0 || (len(args[0]) > 0 && args[0][0] == '-') {
    return fmt.Errorf("usage: ./platform impact <asset> ...")
}
assetName := args[0]
if err := fs.Parse(args[1:]); err != nil { ... }
```

The same splitting pattern is used in the existing `runBackfill`; consider unifying.

### WR-06: `column_edges_active_unique` index does not cover non-NULL `partition_key`

**File:** `migrations/20260509120000_phase4_lineage_schema.sql:241-244`

**Issue:** The unique index used as the `ON CONFLICT` target in `CaptureRun`'s column-edge
upsert has the predicate `WHERE superseded_at IS NULL AND partition_key IS NULL`. Any column
edge with a non-NULL `partition_key` is **not covered** by this index, so the upsert's
`ON CONFLICT ON CONSTRAINT column_edges_active_unique` clause will fail with
`there is no unique or exclusion constraint matching the ON CONFLICT specification` for
partition-keyed column edges.

The migration comment acknowledges this: "Partition-aware uniqueness (partition_key IS NOT
NULL case) is a deferred concern". But the writer at `internal/lineage/capture.go:158-179`
has no guard preventing partition-keyed edges from being attempted; if the runtime ever sets
a non-NULL `partition_key` on a `ColumnRef` (the field exists on `ent.ColumnEdge` and on
`asset.MaterializeResult`), the upsert will hard-fail.

**Fix:** Either (a) add a second partial unique index for `partition_key IS NOT NULL`, or (b)
have `CaptureRun` reject column edges with non-nil partition keys until that index lands:

```go
if len(cl) > 0 && /* any ref has partition_key */ {
    return fmt.Errorf("partition-keyed column lineage requires Phase 5 partition uniqueness index")
}
```

### WR-07: `unmarshalSchemaFromMap` round-trip stores schema_data with Go-field-name keys

**File:** `internal/connector/schema_types.go:13-37` and `cmd/platform/schema.go:244-254`

**Issue:** `connector.Schema` and `connector.SchemaColumn` have **no JSON struct tags**.
`schema/capture.go` writes the snapshot via `json.Marshal(connector.Schema)`, producing
`{"Columns":[...],"PrimaryKey":[...],"Name":"...","Type":"...","Nullable":true,"Default":null,"IsPrimaryKey":false,"Comment":""}`
with capitalized keys. Reading back via `unmarshalSchemaFromMap` round-trips through the same
type, so it works *internally*. But:
- The migration calls `schema_data` "the full Schema JSON snapshot (connector.Schema serialized)"
  — third-party tooling (Marquez ingestion, dashboards, debugging via `psql`) will see the
  unconventional CamelCase keys.
- A future change that adds JSON tags to `connector.Schema` would silently break reading
  back of historical schema_versions rows.
- The OpenLineage translator at `internal/lineage/openlineage/translate.go:25-57` defines its
  *own* shape with snake_case-style JSON tags — consistent with the OL spec — confirming the
  inconsistency is internal-only.

**Fix:** Add JSON tags to `connector.Schema` and `connector.SchemaColumn` matching the
schema_data documentation, write a migration to convert existing rows (or pin the wire format
to current Go-name keys via tag aliases), and add a regression test asserting the wire shape.

### WR-08: `runImpact` CLI lacks asset-name validation, REST handler enforces regex

**File:** `cmd/platform/impact.go:58` versus `internal/api/lineage_handlers.go:35-39`

**Issue:** The HTTP handler validates `asset` against `^[a-zA-Z0-9_.\-]{1,256}$`
(`assetNameRE`), and the comment says: "Defense-in-depth: sqlc parameterized queries already
prevent SQL injection, but the regex provides an additional layer of input validation." The
CLI path does **not** apply the same regex — it passes the raw `assetName` directly into
`impact.Analyze`. Any user with shell access can bypass the input validation. This is mostly
defensible (CLI already implies trust), but the asymmetry is surprising and `assetNameRE`
should be exported and reused by the CLI for consistency.

**Fix:** Export `assetNameRE` (e.g. into `internal/lineage/impact` or `internal/asset`) and
apply it in `runImpact` and `runLineageExport`:

```go
if !asset.NameRE.MatchString(assetName) {
    return fmt.Errorf("asset name must match %s", asset.NameRE.String())
}
```

## Info

### IN-01: Test helper `containsAt` reinvents `strings.Contains` poorly

**File:** `cmd/platform/impact_test.go:57-67`

**Issue:** The test file deliberately avoids importing `strings` and rolls its own
`contains`/`containsAt`. The implementation has redundant conditions:

```go
func contains(s, substr string) bool {
    return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}
```

The first check `len(s) >= len(substr)` already covers the empty-`s` case for non-empty
`substr`; the `s == substr` short-circuit is correct but redundant with `containsAt`.
For an empty `substr`, `containsAt` loops `i=0..len(s)` checking `s[0:0] == ""` which would
correctly return true on the first iteration, but the `len(s) > 0` guard would short-circuit
*incorrectly* for the case `s="" substr=""` (returns false; `strings.Contains("", "")` returns true).

**Fix:** Just import `strings` — there's no policy reason to avoid it in a test file:

```go
import "strings"
// ...
if !strings.Contains(msg, "depth") { ... }
```

### IN-02: Empty-arg branches always rejected by integration tests via panic recovery

**File:** `internal/lineage/impact/analyze_test.go:62-89, 119-134, 138-169, 197-211`

**Issue:** Several tests use `defer recover()` to catch the `nopDB` panic as a *success*
indicator that validation passed. This pattern is hard to read and fragile:
- If the DB layer is later updated to return a typed error instead of panicking, the tests
  would silently start passing without ever verifying validation.
- `TestAnalyzeValidDirections` accepts both panic and no-error as success — it cannot
  distinguish a working validator from a missing one.

**Fix:** Use a proper test double that returns a sentinel error, then assert:

```go
type sentinelDB struct{}
var errSentinel = errors.New("reached DB")
func (sentinelDB) Query(...) (pgx.Rows, error) { return nil, errSentinel }

_, err := impact.Analyze(ctx, sentinelDB{}, ...)
require.ErrorIs(t, err, errSentinel) // proves validation passed
```

### IN-03: `runMigrate` doesn't validate `ATLAS_ENV` value

**File:** `cmd/platform/main.go:212-214`

**Issue:** `os.Getenv("ATLAS_ENV")` is passed unchecked into `exec.CommandContext("atlas",
"migrate", "apply", "--env", atlasEnv)`. While `--env <name>` is a single argument and not
interpolated into a shell, an attacker who controls the env can still trigger Atlas behaviors
by selecting any env name from atlas.hcl. Defense in depth: validate against an allowlist
(`local`, `ci`, `prod`) before passing.

**Fix:**

```go
var allowed = map[string]struct{}{"local": {}, "ci": {}, "staging": {}, "prod": {}}
if _, ok := allowed[atlasEnv]; !ok {
    return fmt.Errorf("ATLAS_ENV %q not allowed", atlasEnv)
}
```

### IN-04: `recursive_cte_seed.go` `SeedDAG` ON CONFLICT clause is incorrect for Postgres index syntax

**File:** `internal/lineage/lineagetest/recursive_cte_seed.go:172-173`

**Issue:** `ON CONFLICT (from_asset, to_asset) WHERE superseded_at IS NULL DO NOTHING` —
the `WHERE` clause attached to `ON CONFLICT` is the *index_predicate* (must match a partial
unique index). The migration creates `asset_edges_active_unique` as a partial unique index
on `(from_asset, to_asset) WHERE superseded_at IS NULL`, which matches. So the syntax works
*today*. However, the seeder relies on the existence of that named partial index and would
silently start failing if the migration is changed to use a different unique constraint
(e.g. `ON CONFLICT ON CONSTRAINT asset_edges_active_unique`).

**Fix:** Use the explicit constraint name to align with `lineage.Writer.SyncStaticEdges`
(`ON CONFLICT ON CONSTRAINT asset_edges_active_unique`) so the seeder breaks loudly on
constraint rename:

```go
const stmt = `INSERT INTO asset_edges (...) VALUES (...) 
              ON CONFLICT ON CONSTRAINT asset_edges_active_unique DO NOTHING`
```

### IN-05: `prevPtr any` could shadow nil semantics — minor

**File:** `internal/schema/writer_diff.go:47-50`

**Issue:** The pattern works correctly:
```go
var prevPtr any
if prevVersionID != nil {
    prevPtr = *prevVersionID
}
```
A `var x any` not assigned is the nil interface, which `database/sql` correctly translates to
SQL `NULL`. But future readers may not know this idiom — adding a comment ("nil interface
maps to SQL NULL via database/sql") or using `sql.NullString`/`uuid.NullUUID` for explicit
nullability would make the intent clearer.

### IN-06: `ackSchemaChange` test simulates non-governance role but doesn't assert middleware behavior

**File:** `internal/api/schema_handlers_test.go:146-169`

**Issue:** `TestAck_RequiresGovernanceRole` notes "Handler itself doesn't enforce role;
RequireRole middleware does" and only asserts `rec.Code != 0` (no panic). This test
provides no actual coverage of the role check — it would still pass if the handler accepted
any role. The router-level enforcement should be tested at the router level (a separate
test that builds the full chain and asserts a 403).

**Fix:** Either remove the test (it provides false confidence) or move it to
`router_test.go` and exercise the actual middleware chain.

### IN-07: `seedSchemaChange` and other test helpers swallow context

**File:** `internal/api/schema_handlers_test.go:45-75`, `internal/lineage/openlineage/translate_test.go:23-78`

**Issue:** Several test seeders call `context.Background()` instead of `t.Context()`
(Go 1.22+) or accept a `ctx` arg. If the test times out, the seed query continues to run on
the test DB. Low impact for unit tests against in-memory SQLite, but worth standardizing.

**Fix:** Accept `ctx context.Context` explicitly or use `t.Context()`:

```go
func seedSchemaChange(t *testing.T, ctx context.Context, deps Deps, ...) uuid.UUID { ... }
```

---

_Reviewed: 2026-05-09T13:30:00Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
