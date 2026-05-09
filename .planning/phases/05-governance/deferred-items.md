## Deferred — pre-existing test flakiness (out of scope for plan 05-02)

**File:** internal/connector/firstparty/snowflake/snowflake_test.go::TestSnowflake_Write
**Symptom:** Intermittent failure due to non-deterministic map iteration order in INSERT column order.
**Why deferred:** Pre-existing bug not caused by plan 05-02 changes. Should be fixed by sorting the column slice in snowflake.go's Write before building INSERT, or using sqlmock argument matchers that ignore order.
**Found during:** plan 05-02 broad test sweep.
