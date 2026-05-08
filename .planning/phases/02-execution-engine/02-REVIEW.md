---
phase: 02-execution-engine
reviewed: 2026-05-08T00:00:00Z
depth: standard
files_reviewed: 41
files_reviewed_list:
  - cmd/platform/factories.go
  - cmd/platform/main.go
  - cmd/platform/materialize.go
  - cmd/platform/worker.go
  - internal/asset/asset.go
  - internal/asset/builder.go
  - internal/asset/io.go
  - internal/asset/registry.go
  - internal/asset/retry.go
  - internal/concurrency/pool.go
  - internal/connector/config/config.go
  - internal/connector/config/resolver.go
  - internal/connector/firstparty/bigquery/bigquery.go
  - internal/connector/firstparty/bigquery/factory.go
  - internal/connector/firstparty/conformance/conformance.go
  - internal/connector/firstparty/gcs/factory.go
  - internal/connector/firstparty/gcs/gcs.go
  - internal/connector/firstparty/hdfs/factory.go
  - internal/connector/firstparty/hdfs/hdfs.go
  - internal/connector/firstparty/mysql/factory.go
  - internal/connector/firstparty/mysql/mysql.go
  - internal/connector/firstparty/postgres/factory.go
  - internal/connector/firstparty/postgres/postgres.go
  - internal/connector/firstparty/s3/factory.go
  - internal/connector/firstparty/s3/s3.go
  - internal/connector/firstparty/snowflake/factory.go
  - internal/connector/firstparty/snowflake/snowflake.go
  - internal/connector/registry.go
  - internal/dag/dag.go
  - internal/event/types.go
  - internal/retry/policy.go
  - internal/run/claim.go
  - internal/run/lifecycle.go
  - internal/run/reaper.go
  - internal/run/state.go
  - internal/runtime/executor.go
  - internal/storage/ent/schema/concurrency_token.go
  - internal/storage/ent/schema/run.go
  - internal/storage/ent/schema/run_step.go
  - migrations/20260507120000_phase2_run_tables.sql
  - migrations/20260507121500_phase2_concurrency_tokens.sql
findings:
  critical: 3
  warning: 6
  info: 4
  total: 13
status: issues_found
---

# Phase 02: Code Review Report

**Reviewed:** 2026-05-08T00:00:00Z
**Depth:** standard
**Files Reviewed:** 41
**Status:** issues_found

## Summary

Phase 2 introduces the execution engine: asset registry, DAG-based topological execution, concurrency token pool, per-asset retry policy, run lifecycle FSM, heartbeat/reaper for crash recovery, and seven first-party connectors (postgres, mysql, snowflake, s3, gcs, hdfs, bigquery). The overall architecture is sound and idiomatic Go. Locking is consistent across registries and connectors, and the advisory-lock approach for concurrency token acquisition is appropriate.

Three critical issues require attention before merging:

1. A read-after-cancel race condition in the executor's `transition` helper silently ignores all DB write errors.
2. The `materialize` subcommand polls inside the loop _before_ sleeping, meaning the first state check can consume the run ID it just inserted — a logical ordering bug combined with an unchecked context leak.
3. The BigQuery `splitIdentifier` returns an empty project string for 2-part identifiers then uses it in a backtick-quoted SQL query, producing an invalid query silently.

Six warnings cover unhandled error returns, a token-release ordering issue that can starve capacity during retries, and missing goroutine lifetime guarantees in tests.

---

## Critical Issues

### CR-01: `executor.transition` silently swallows all DB errors

**File:** `internal/runtime/executor.go:318-319`

**Issue:** `transition` is called and its error return is discarded with `_ =` at three call sites (lines 122, 133, 137). When the `UPDATE runs SET state` query fails — network error, constraint violation, or the run was already moved by the reaper — the executor continues as if the transition succeeded. For the failure path (line 122), this means the run row stays in `running` while the caller returns an error, leaving the row orphaned in a non-terminal state that the reaper will eventually re-queue as if the worker crashed.

```go
// executor.go lines 122-123 (failure path)
_ = e.transition(ctx, runID, run.StateRunning, run.StateFailed)
e.appendEvent(ctx, runID, event.EventTypeRunFailed, ...)
```

The success path at line 133 has the same problem. If the DB update fails the row silently stays `running` instead of moving to `succeeded`.

**Fix:** Propagate the error from `transition` in all call sites. For the terminal transitions (succeeded/failed), the current run cannot continue either way so wrap and return:

```go
// In the step failure branch (around line 122):
if terr := e.transition(ctx, runID, run.StateRunning, run.StateFailed); terr != nil {
    slog.Error("executor.transition_failed", "run_id", runID, "to", "failed", "error", terr)
}

// In the success path (around line 133):
if terr := e.transition(ctx, runID, run.StateRunning, run.StateSucceeded); terr != nil {
    slog.Error("executor.transition_failed", "run_id", runID, "to", "succeeded", "error", terr)
}
```

At a minimum, errors must be logged; ideally the run terminal transition error is returned so the worker loop can decide whether to retry the claim.

---

### CR-02: `materialize` subcommand inserts run then polls immediately — ordering bug and ctx timeout mismatch

**File:** `cmd/platform/materialize.go:34, 96-124`

**Issue — timeout context created before insert:** The outer context is created with `timeout + 30s` at line 34, but the separate `waitForRun` deadline is computed from `time.Now()` at line 97. Between line 34 and line 97, `bootstrap` + the DB insert + the event write can consume several seconds. The `timeout` variable is then passed directly to `waitForRun`. In practice this is not a bug per se, but the 30-second pad on the outer context is not clearly reasoned: if `timeout` = 30 minutes and bootstrap takes 10 seconds, the poll loop runs for 30 minutes, then hits its deadline, but the outer `ctx` does not expire for another 29 minutes and 50 seconds. The extra 30s guard is therefore misleading.

**Issue — first poll reads stale or zero state:** `waitForRun` calls `QueryRowContext` on the very first loop iteration (line 103) _before_ the `select` sleep. The run was just inserted as `'queued'`. The worker picks it up asynchronously; during the poll window the state is legitimately `queued`. The switch statement at lines 107-115 does not have a `case "queued":` or `case "starting":` or `case "running":` arm — those fall through to the `time.Now().After(deadline)` check. This is intentional but there is a subtle bug: the `deadline` check at line 116 uses `time.Now().After(deadline)` _after_ the query, meaning the very first iteration never sleeps even when the run has not finished. The loop first polls, then checks the deadline, then _waits_ in the `select`. The correct structure should: check deadline _before_ polling (to handle a zero-budget timeout), then poll, then sleep. As written, with `timeout = 0`, it issues one DB query before returning.

```
for {
    /* poll */  ← fires on first iteration
    /* switch */
    if time.Now().After(deadline) { return timeout err }
    select { case <-ticker.C: /* sleep 500ms */ }
}
```

**Fix:** Move the deadline check before the query, or restructure so the first iteration also waits 500ms (consistent with stated "poll every 500ms" behavior). Also add explicit handling for non-terminal states to avoid misleading logs:

```go
for {
    select {
    case <-ticker.C:
    case <-ctx.Done():
        return ctx.Err()
    }
    if time.Now().After(deadline) {
        return fmt.Errorf("materialize: timeout (last state=%s)", lastState)
    }
    if err := deps.store.DB().QueryRowContext(ctx, stateSQL, runID).Scan(&state, &errMsg); err != nil {
        return fmt.Errorf("materialize: poll state: %w", err)
    }
    // ... switch ...
}
```

---

### CR-03: BigQuery `splitIdentifier` returns empty project for 2-part identifiers, then uses it in query

**File:** `internal/connector/firstparty/bigquery/bigquery.go:107-111, 198-212`

**Issue:** `splitIdentifier` returns `("", dataset, table, nil)` when the identifier is `"dataset.table"` (2 parts, line 207). The `Read` method at line 111 uses the returned `project` value in a format string:

```go
q := fmt.Sprintf("SELECT * FROM `%s`.`%s`.`%s`", project, dataset, table)
```

When `project` is empty this produces `` SELECT * FROM ``.`dataset`.`table` `` — an invalid BigQuery SQL query. The BigQuery client will return an error, but the error message will be opaque (a BigQuery API error about invalid SQL), not a clear "project required" message.

In contrast, `Write` and `Schema` discard the returned `project` entirely (line 159, line 79) and use only `dataset` and `table`, which is correct for those paths. The inconsistency means `Read` is broken for 2-part identifiers while `Write` and `Schema` work.

**Fix:** Either enforce that 3-part identifiers are required for `Read` (returning a clear error), or fall back to the connector's stored `b.project` when the parsed project is empty:

```go
// In Read, after splitIdentifier:
if project == "" {
    project = b.project
}
q := fmt.Sprintf("SELECT * FROM `%s`.`%s`.`%s`", project, dataset, table)
```

Apply the same fix symmetrically in `Write` and `Schema` for consistency, even though they currently ignore the project and rely on the BigQuery client's default project context.

---

## Warnings

### WR-01: GCS client is not closed by `NewClientFromOptions` caller in `Factory`

**File:** `internal/connector/firstparty/gcs/factory.go:36-40`

**Issue:** `Factory` calls `NewClientFromOptions` with a `context.WithTimeout` that it cancels (`defer cancel()`). The resulting `*gcstorage.Client` is then passed to `New(client, bucket, format)`. The GCS client performs connection establishment lazily but the context cancellation races with the first real operation if the factory's 10-second timeout fires. More concretely: if `New(client, ...)` returns the `*GCS` struct and then `cancel()` fires before the first `Ping` or `Read`, the underlying client may have its context cancelled depending on how the GCS SDK uses it internally.

This is the same pattern used in the BigQuery factory. For connectors that use the GCS/BQ libraries, the construction context is only for the `NewClient` call itself, not the ongoing connection — this is correct for the GCS SDK. However, it is worth noting that both factories pass the construction timeout context to `NewClient`, which the SDK uses only for initial dial. The concern is minor for GCS since the SDK does not hold the context. Rating: warning because the pattern is consistent but worth documenting.

**Fix:** Add a code comment to `Factory` explicitly documenting that the construction context is only used for `NewClient`, not retained, following the same pattern as the BQ factory:

```go
// ctx is used only for gcstorage.NewClient (initial dial); the client itself
// does not retain ctx. Subsequent operations use the per-request context.
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
```

---

### WR-02: Concurrency token release on retry does not release ALL acquired tags atomically — capacity starvation risk

**File:** `internal/runtime/executor.go:176-231`

**Issue:** In `runStep`, when a resource-level token acquisition fails (after the global token was already acquired), `releaseAcquired()` is called to drop all acquired tags before retrying. This is correct. However, the `Release` call in `concurrency.Pool` uses a simple `DELETE WHERE run_id = $1 AND resource_tag = $2` without any locking — it is not in the same advisory-lock transaction used by `Acquire`. The sequence:

1. Acquire `global` → row inserted
2. Acquire `resource_a` → fails (capacity exhausted)
3. `releaseAcquired()` → deletes `global` row
4. Retry: Acquire `global` again (new row inserted)

Between steps 3 and 4, another concurrent run could acquire the freed `global` slot. This is not a correctness bug (the behavior is correct), but it means the retrying run sees additional contention that would not exist if it held the token across the retry sleep. The current design makes a retrying run re-compete for all tokens on every attempt, which can cause starvation under high load.

**Fix (design-level):** Document the "re-compete on retry" behavior explicitly in a code comment in `runStep` so future engineers do not assume the token is held across retries. Consider a token reservation pattern (pre-check without insert) for Phase 3 if starvation becomes observable.

```go
// NOTE: All tokens are released before the retry sleep so other runs can proceed.
// The retrying attempt must re-acquire tokens on the next loop iteration,
// competing fairly with new runs. This means high contention can starve retrying
// runs; revisit in Phase 3 if starvation is observed in production.
releaseAcquired()
```

---

### WR-03: `executor.Run` ignores `Registry.Get` error in the per-step loop

**File:** `internal/runtime/executor.go:119`

```go
stepAsset, _ := e.deps.Registry.Get(name)
```

**Issue:** `e.deps.Registry.Get(name)` can return an error (and a `nil` *asset.Asset) if the asset was somehow removed from the registry between `buildSubgraph` building the DAG and the per-step loop executing. If `Get` fails and returns `nil`, `runStep` receives a nil `*asset.Asset` and will panic when it calls `a.RetryPolicy()` (line 170), `a.Name()` (line 186), or `a.Resources()` (line 201). This panic is recovered by `safeMaterialize`, but the step never reaches that function — the panic occurs in `runStep` itself before the user function is called, and is NOT wrapped by `safeMaterialize`.

**Fix:** Check the error and return it:

```go
stepAsset, err := e.deps.Registry.Get(name)
if err != nil {
    return fmt.Errorf("executor: step %q not found during execution: %w", name, err)
}
```

---

### WR-04: Reaper `SweepOnce` closes `rows` manually then calls `rows.Err()` — double-close risk

**File:** `internal/run/reaper.go:101, 107-109`

**Issue:** Inside the scan loop, if `rows.Scan` fails, the code calls `_ = rows.Close()` explicitly (line 101) and returns early. After the loop, line 107 calls `_ = rows.Close()` unconditionally, followed by `rows.Err()` at line 108. Calling `Close()` twice on `*sql.Rows` is explicitly documented as safe in Go's `database/sql` package, so this is not a crash risk. However, the manual `rows.Close()` in the error path at line 101 combined with the unconditional `_ = rows.Close()` at line 107 is confusing and the pattern differs from the idiomatic `defer rows.Close()` used everywhere else in the codebase (postgres, mysql, snowflake connectors all use `defer rows.Close()`).

More importantly: when `rows.Scan` fails at line 100, the code immediately returns without calling `rows.Err()`. The error from `Scan` is returned, but any prior row-iteration error accumulated in `rows.Err()` is dropped. This is a minor correctness issue.

**Fix:** Use `defer rows.Close()` immediately after the `QueryContext` call (idiomatic pattern), and check `rows.Err()` once after the loop:

```go
rows, err := r.Store.DB().QueryContext(ctx, selectSQL, cutoff)
if err != nil {
    return 0, fmt.Errorf("reaper: select stale: %w", err)
}
defer rows.Close()

var candidates []staleRunRow
for rows.Next() {
    var row staleRunRow
    var stateStr string
    if err := rows.Scan(&row.ID, &stateStr, &row.AssetName); err != nil {
        return 0, fmt.Errorf("reaper: scan stale: %w", err)
    }
    row.State = State(stateStr)
    candidates = append(candidates, row)
}
if err := rows.Err(); err != nil {
    return 0, fmt.Errorf("reaper: iterate stale: %w", err)
}
```

---

### WR-05: `waitForRun` in `materialize.go` does not handle `context.Canceled` from `QueryRowContext`

**File:** `cmd/platform/materialize.go:103-104`

**Issue:** When `ctx` is canceled (SIGINT during synchronous wait), `QueryRowContext` will return `ctx.Err()` wrapped in a `*sql.ErrConnDone` or `context.Canceled`. The current code at line 104 wraps it as `"materialize: poll state: %w"` and returns. The `select` at line 119 also catches `ctx.Done()` and returns `ctx.Err()`, but the DB query runs before the `select`, so a canceled context during query will produce the wrapped error rather than the clean `context.Canceled`. This makes the exit message unnecessarily noisy in normal SIGINT shutdown. This is a minor UX issue rather than a correctness bug.

**Fix:** After the query error, check `ctx.Err()` before wrapping:

```go
if err := deps.store.DB().QueryRowContext(ctx, stateSQL, runID).Scan(&state, &errMsg); err != nil {
    if ctx.Err() != nil {
        return ctx.Err()
    }
    return fmt.Errorf("materialize: poll state: %w", err)
}
```

---

### WR-06: `mysql.quoteIdentifier` path-traversal check uses `strings.Contains(id, "..")` which triggers on legitimate names

**File:** `internal/connector/firstparty/mysql/mysql.go:280-282`

**Issue:** The path-traversal guard `strings.Contains(id, "..")` fires when an identifier contains two consecutive dots. A MySQL database named `db..schema` is indeed invalid, but the check also matches valid names like `"my..table"` in a concatenated `"db.my..table"` identifier — though again, double-dots in MySQL table names are not legal. The real risk is a false positive: a user who names a column `"e.g.."` (trailing dot) or schema `"schema.."` would get an opaque error about path traversal instead of a clear "illegal identifier" message.

The Postgres connector does not have this check (correct for SQL), and the S3/GCS/HDFS connectors check segment-by-segment for `".."` (also correct). Only MySQL and Snowflake have the `strings.Contains(id, "..")` string-level check. This is a conservative measure that could produce confusing error messages.

**Fix:** The MySQL identifier quoting already rejects backticks. The `".."` check is borrowed from path-traversal defenses for file-system connectors and does not apply cleanly to SQL identifiers. Remove it from `mysql.quoteIdentifier` and `snowflake.quoteIdentifier`, or replace with a clearer "consecutive dots not allowed" message:

```go
// In quoteIdentifier:
// Remove: if strings.Contains(id, "..") { ... }
// The backtick rejection is sufficient for SQL injection defense.
```

---

## Info

### IN-01: `envVarPattern` in config loader only matches uppercase variable names

**File:** `internal/connector/config/config.go:27`

```go
envVarPattern = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)
```

**Issue:** The regex only matches `${UPPER_CASE}` placeholders. POSIX allows lowercase env var names (`$HOME`, `$user`, etc.). While the project convention is uppercase env vars (enforced by the regex comment "valid env var name"), this will silently leave lowercase `${lower_case_var}` placeholders unreplaced rather than reporting them as missing. Users who write `${dsn}` instead of `${DSN}` will get a confusing error: the literal string `${dsn}` will be passed to the YAML decoder and then to the connector factory, which will fail with a connector-specific error.

**Fix:** Either extend the regex to match lowercase (POSIX convention), or add a post-resolve check that warns when any `${...}` pattern remains in the resolved string (indicating an unmatched placeholder):

```go
// After resolveEnv, scan for unresolved placeholders (catches lowercase mismatches):
if remaining := regexp.MustCompile(`\$\{[^}]+\}`).FindString(resolved); remaining != "" {
    return nil, fmt.Errorf("config: unresolved placeholder %q (env var names must be UPPER_CASE)", remaining)
}
```

---

### IN-02: `asset.ResetForTest` is exported and mutates a package-level variable without sync protection

**File:** `internal/asset/registry.go:85-89`

```go
func ResetForTest() { defaultRegistry = NewDefinitionRegistry() }
```

**Issue:** The exported `ResetForTest` function replaces `defaultRegistry` (a package-level `var`) without holding any lock. If called concurrently with a `Register` or `Get` call that is reading `defaultRegistry`, this is a data race. The comment at line 88 says "NOT safe for concurrent test use — use only from TestMain or serially," which is correct documentation. However, the integration test at `test/integration/e2e_postgres_test.go:273-274` calls `asset.ResetForTest()` inside individual test functions that run serially (`TestE2E_PostgresMaterialize`, etc.). These tests are currently sequential, so there is no race in practice. The risk is a future `t.Parallel()` call that would introduce the race silently.

**Fix:** Add the `t.Setenv`-style pattern or use a `sync/atomic.Pointer` for `defaultRegistry`, or add a package-init mutex. At minimum, add a test-time `go vet`/`-race` note in the function comment:

```go
// ResetForTest is safe only when called serially (e.g. from TestMain or t.Cleanup without
// t.Parallel). It is intentionally not protected by a mutex — tests using t.Parallel()
// MUST NOT call this function.
func ResetForTest() { defaultRegistry = NewDefinitionRegistry() }
```

---

### IN-03: `run_steps` table has no foreign key to `runs`

**File:** `migrations/20260507120000_phase2_run_tables.sql:27-46`

**Issue:** The `run_steps.run_id` column references `runs.id` semantically but has no `FOREIGN KEY` constraint in the migration DDL. Orphaned step rows (referencing a deleted or non-existent run) can accumulate without DB-level enforcement. The ent schema (`run_step.go:23`) also does not declare an edge to `Run`, so ent does not generate the FK either.

This is a data-integrity gap: if a run is deleted (manually or via a future cleanup job), its `run_steps` rows are silently left behind. Similarly, a bug that writes to `run_steps` with a non-existent `run_id` would succeed silently.

**Fix:** Add the foreign key in the migration:

```sql
ALTER TABLE run_steps
  ADD CONSTRAINT run_steps_run_id_fkey
  FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE;
```

And declare the edge in the ent schema:

```go
func (RunStep) Edges() []ent.Edge {
    return []ent.Edge{
        edge.From("run", Run.Type).Ref("steps").Field("run_id").Required().Immutable(),
    }
}
```

---

### IN-04: `conformance.go` `CtxCancel` test asserts `err != nil` which always passes even without cancellation

**File:** `internal/connector/firstparty/conformance/conformance.go:79`

```go
require.Truef(t, errors.Is(err, context.Canceled) || err != nil,
    "expected ctx.Canceled or any error, got %v", err)
```

**Issue:** The assertion `errors.Is(err, context.Canceled) || err != nil` is equivalent to just `err != nil` because `errors.Is(err, context.Canceled)` implies `err != nil`. The test therefore passes as long as the connector returns ANY error after context cancellation — including unrelated errors like "object not found" or "bucket empty". A connector that ignores context cancellation entirely and returns a business error would pass this test, masking a real bug.

**Fix:** Strengthen the assertion. The connector should return `context.Canceled` (or a wrapping of it) when the context is cancelled before the operation completes:

```go
// The connector must respect context cancellation.
// Accept context.Canceled, context.DeadlineExceeded, or a wrapping of either.
cancelErr := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
require.Truef(t, cancelErr,
    "connector should return context.Canceled/DeadlineExceeded on ctx cancel, got: %v", err)
```

---

_Reviewed: 2026-05-08T00:00:00Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
