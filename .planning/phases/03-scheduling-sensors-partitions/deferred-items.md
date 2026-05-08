# Phase 03 — Deferred Items (out-of-scope discoveries)

## From plan 03-03 (priority-claim-and-load-test) — also independently confirmed by plan 03-05

### internal/runtime executor tests fail with "unsupported driver: pgx"

**Discovered during:** Task 2 verification of `go test ./internal/runtime/...` after Executor.Run signature migration.

**Symptom:** Six tests fail at the test fixture setup (line 101 of executor_test.go: `stent.Open("pgx", dsn)`) with `open ent: unsupported driver: "pgx"`.

**Pre-existing:** Verified via `git stash` rewind — the failures occur on the parent commit `2f2df38af493af3bbecd1a3f1502c66af9ca1588` BEFORE this plan's signature change. Root cause is the ent storage driver registration: ent's storage layer expects `postgres` as the driver name even when `pgx/v5/stdlib` is registered as `pgx` in `database/sql`. Phase 2's `executor_test.go` was authored when the driver name was either `postgres` or both, and a subsequent driver-registration consolidation (or library upgrade) broke the binding without anyone noticing because the tests are gated behind `DATABASE_URL`. Plan 03-05 independently re-confirmed pre-existence on the same base commit.

**Why deferred:** Out of plan 03-03's scope — the symptom predates this plan and would require either re-registering pgx as the `postgres` ent driver name in `internal/storage/storage.go` (touched by other plans) or rewriting the executor test fixtures to use the same `*sql.DB` path as the claim tests (which DO work with the `pgx` name). Plan 03-03's signature change does NOT introduce or worsen the failure — the failure manifests at fixture init, before any signature-change code path runs.

**Plan 03-03 in-plan verification:** the runtime package still **builds clean** (`go build ./...` is green), and the new claim_test.go tests (which use the `*sql.DB` path) all pass. The signature migration is verified end-to-end through builds + the claim tests; the dormant runtime test fixtures will be revived when the storage driver issue is fixed in a future plan (likely plan 03-04 / 03-05 when the scheduler / sensor evaluator consume Executor).

**Suggested fix (future plan):**
- Change `stent.Open("pgx", dsn)` → `stent.Open("postgres", dsn)` (after also registering pgx under that name in `internal/storage/`), OR
- Replace the ent client construction with the same `*sql.DB` path used by `internal/run/claim_test.go`'s `sqlStorage` stub, which sidesteps ent entirely for tests that don't need it.

**Affected tests (all fail at fixture, not body):**
- `TestExecutor_SuccessfulRun`
- `TestExecutor_RetryAndFail`
- `TestExecutor_PanicRecovery`
- `TestExecutor_TopologicalOrder`
- `TestExecutor_ConcurrencyTokenZeroCapacity`
- `TestExecutor_HeartbeatTicks`

**Recommended owner:** Phase 2 owner / executor maintainer.
