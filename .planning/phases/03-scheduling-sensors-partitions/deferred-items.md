# Phase 3 Deferred Items

Out-of-scope discoveries logged during plan execution. Items here are NOT
addressed by their discovering plan; they should be triaged in a future cycle.

## Pre-existing test failures

### internal/runtime executor tests fail with "unsupported driver: pgx"

- **Discovered during:** plan 03-05 execution (verified pre-existing on master at base commit `2f2df38`)
- **Symptom:** `TestExecutor_*` tests open ent with `pgx` driver name, but ent's
  `dialect.Postgres` expects driver name `"postgres"` for stdlib registration.
- **Files:** `internal/runtime/executor_test.go` (multiple tests)
- **Cause:** Test infrastructure mismatch — likely needs a `pgxstdlib.RegisterConnConfig`
  or a switch to `lib/pq` driver name in the test bootstrap.
- **Scope:** Pre-existing on master; outside the scope of phase 03 plans.
- **Recommended owner:** Phase 2 owner / executor maintainer.
