## Deferred — pre-existing test flakiness (out of scope for plan 05-02)

**File:** internal/connector/firstparty/snowflake/snowflake_test.go::TestSnowflake_Write
**Symptom:** Intermittent failure due to non-deterministic map iteration order in INSERT column order.
**Why deferred:** Pre-existing bug not caused by plan 05-02 changes. Should be fixed by sorting the column slice in snowflake.go's Write before building INSERT, or using sqlmock argument matchers that ignore order.
**Found during:** plan 05-02 broad test sweep.

## Deferred — pre-existing ent codegen panic (out of scope for plan 05-03)

**Files:**
- internal/api/schema_handlers_test.go::TestAck_OK
- internal/lineage/openlineage/translate_test.go::TestTranslateRun_OK
- internal/metadata/handler_test.go::TestHandler_PatchAsset_OK

**Symptom:** `runtime error: invalid memory address or nil pointer dereference` inside `internal/storage/ent/run_create.go::(*RunCreate).defaults` — the ent-generated default function dereferences `runtime.RunFunc` which is nil when the codegen has not been re-run.

**Why deferred:** Pre-existing failure already documented in 05-01 SUMMARY ("Ent codegen pre-existing broken state: git stash showed codegen failed before our changes; did not fix"). Confirmed by stashing plan 05-03 changes and re-running — same panic. Out of scope per scope-boundary rule (not introduced by this plan).

**Fix:** Re-run `go generate ./internal/storage/ent/` (regenerates the runtime defaults). Should be done in a dedicated tooling fix-up commit; touching the ent generator from a feature plan would dilute the diff.

**Found during:** plan 05-03 broad test sweep.
