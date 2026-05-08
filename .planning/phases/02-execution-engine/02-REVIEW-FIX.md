---
phase: 02-execution-engine
fixed_at: 2026-05-08T00:00:00Z
review_path: .planning/phases/02-execution-engine/02-REVIEW.md
iteration: 1
findings_in_scope: 9
fixed: 7
skipped: 0
already_fixed: 1
addressed_in_other: 1
status: all_fixed
---

# Phase 02: Code Review Fix Report

**Fixed at:** 2026-05-08T00:00:00Z
**Source review:** .planning/phases/02-execution-engine/02-REVIEW.md
**Iteration:** 1

**Summary:**
- Findings in scope: 9 (CR-01, CR-02, CR-03, WR-01 through WR-06)
- Fixed: 7 (CR-01, CR-02, WR-01, WR-02, WR-03, WR-04, WR-06)
- Already fixed before this session: 1 (CR-03)
- Addressed within another finding's fix: 1 (WR-05 resolved within CR-02)
- Skipped: 0

## Fixed Issues

### CR-01: executor.transition silently swallows all DB errors

**Files modified:** `internal/runtime/executor.go`
**Commit:** 4f25104
**Applied fix:** Replaced both `_ = e.transition(...)` calls (failure path line 122, success path line 133) with explicit error checks that log via `slog.Error` when the DB transition fails. The run still proceeds to return the step error (failure path) or nil (success path) — consistent with reviewer intent — but the transition failure is now visible in structured logs rather than silently dropped.

---

### CR-02: materialize subcommand polls before sleeping — ordering bug and ctx timeout mismatch

**Files modified:** `cmd/platform/materialize.go`
**Commit:** f226c5b
**Applied fix:** Restructured `waitForRun` loop to sleep-first (select on ticker/ctx.Done before querying), then check deadline before issuing the DB query. Added a `lastState` variable to carry the last observed state into the timeout error message. Added a `ctx.Err()` check in the query-error path so SIGINT cancellation returns the clean `context.Canceled` error rather than a wrapped DB error. Added a doc comment to `waitForRun` explaining the loop invariants.

---

### WR-01: GCS client construction context lifetime undocumented

**Files modified:** `internal/connector/firstparty/gcs/factory.go`
**Commit:** b374cea
**Applied fix:** Added a code comment immediately above the `context.WithTimeout` call explaining that the construction context is used only for `gcstorage.NewClient` (initial dial) and is not retained by the client for subsequent operations, following the same documentation pattern as the BigQuery factory.

---

### WR-02: Concurrency token re-compete-on-retry behavior undocumented

**Files modified:** `internal/runtime/executor.go`
**Commit:** b593263
**Applied fix:** Added a multi-line NOTE comment above the `releaseAcquired()` call in the resource token failure path explaining that all tokens are released before the retry sleep, that the retrying attempt must re-acquire them on the next iteration competing fairly with new runs, and that this can cause starvation under high load — with a recommendation to revisit in Phase 3 if observed.

---

### WR-03: executor.Run ignores Registry.Get error in the per-step loop

**Files modified:** `internal/runtime/executor.go`
**Commit:** c69600f
**Applied fix:** Replaced `stepAsset, _ := e.deps.Registry.Get(name)` with a proper error check. If `Get` returns an error (asset removed from registry between DAG build and execution), the function returns a descriptive error rather than passing a nil `*asset.Asset` to `runStep` which would cause a nil-pointer panic before `safeMaterialize`'s recovery wrapper.

---

### WR-04: Reaper SweepOnce uses manual rows.Close() instead of defer

**Files modified:** `internal/run/reaper.go`
**Commit:** 880ee05
**Applied fix:** Replaced the manual `_ = rows.Close()` in the scan-error early-return path and the unconditional `_ = rows.Close()` after the loop with a single `defer rows.Close()` immediately after the successful `QueryContext` call. The `rows.Err()` check after the loop is retained. This matches the idiomatic pattern used in all other connectors in the codebase.

---

### WR-06: mysql/snowflake quoteIdentifier has ".." path traversal check that does not apply to SQL identifiers

**Files modified:** `internal/connector/firstparty/mysql/mysql.go`, `internal/connector/firstparty/snowflake/snowflake.go`
**Commit:** 55fc0ce
**Applied fix:** Removed the `strings.Contains(id, "..")` check and its associated error return from both `mysql.quoteIdentifier` and `snowflake.quoteIdentifier`. The backtick/double-quote character rejection (the actual SQL-injection defense) is retained. Updated the doc comment on `mysql.quoteIdentifier` to remove the mention of the path traversal guard.

---

## Already Fixed / Addressed Elsewhere

### CR-03: BigQuery splitIdentifier returns empty project for 2-part identifiers

**Status:** already_fixed (commit 3054983)
**File:** `internal/connector/firstparty/bigquery/bigquery.go:112-114`
**Note:** The `if project == "" { project = b.project }` guard is present in the `Read` method. No action taken; no duplicate commit created.

---

### WR-05: waitForRun does not handle context.Canceled from QueryRowContext cleanly

**Status:** addressed within CR-02 fix (commit f226c5b)
**File:** `cmd/platform/materialize.go`
**Note:** The restructured loop (CR-02) places the `select { case <-ctx.Done(): return ctx.Err() }` before any DB query. On cancellation the select arm fires first, returning the clean `context.Canceled` without a DB round-trip. For the residual case where cancellation arrives mid-query, the explicit `if ctx.Err() != nil { return ctx.Err() }` check on the query error path (lines 121-123) covers the UX concern raised in WR-05. No separate commit required.

---

_Fixed: 2026-05-08T00:00:00Z_
_Fixer: Claude (gsd-code-fixer)_
_Iteration: 1_
