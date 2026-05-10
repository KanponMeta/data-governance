## Deferred — pre-existing test flakiness (out of scope for plan 05-02)

**File:** internal/connector/firstparty/snowflake/snowflake_test.go::TestSnowflake_Write
**Symptom:** Intermittent failure due to non-deterministic map iteration order in INSERT column order.
**Why deferred:** Pre-existing bug not caused by plan 05-02 changes. Should be fixed by sorting the column slice in snowflake.go's Write before building INSERT, or using sqlmock argument matchers that ignore order.
**Found during:** plan 05-02 broad test sweep.

## Deferred — pre-existing testcontainer flakiness (out of scope for plan 05-04)

**File:** internal/governance/testharness/postgres.go::NewTestPostgres
**Symptom:** `postgres not ready: failed to connect ... read: connection reset by peer / unexpected EOF` 100% reproducible on this host (Linux 6.17 / Docker 29.4 / testcontainers-go v0.42.0). The pgx pool ping happens before the Postgres container's TCP listener finishes initialising — there is no retry loop.
**Why deferred:** Pre-existing bug not caused by plan 05-04 changes. The same TestPostgresContainer that was committed in 05-01 fails identically on this host. All Phase 5 plans short-circuit DB-backed tests via `if testing.Short() { t.Skip() }`.
**Recommended fix:** Add a `pingWithRetry` helper to NewTestPostgres that loops with backoff for ~30s before declaring the container unready. Alternatively raise the `postgres.WithStrategy(wait.ForLog(...))` timeout.
**Found during:** plan 05-04 broad test sweep.
