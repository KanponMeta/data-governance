---
phase: 03-scheduling-sensors-partitions
plan: 03
subsystem: run-engine
tags: [run, claim, priority, skip-locked, postgres, executor, signature, load-test, pitfall-1, pitfall-5]

# Dependency graph
requires:
  - phase: 03-scheduling-sensors-partitions/01-schema-events-foundation
    provides: runs.partition_key/priority/backfill_id columns + CHECK + (state, priority, queued_at) composite index — required by both the SELECT projection and the priority-aware ORDER BY
  - phase: 03-scheduling-sensors-partitions/02-asset-dsl-and-partitions
    provides: asset.NewAssetIO third-arg (partitionKey) constructor — runStep now passes the claimed partition_key through to user MaterializeFn via io.PartitionKey()
  - phase: 02-execution-engine
    provides: ClaimNext SKIP LOCKED + atomicity primitives, Executor.Run + heartbeatLoop, TestClaimAtomicity50Goroutines (Phase 2 acceptance criterion 3 — REGRESSION GUARD)
provides:
  - "internal/run.Priority enum + AllPriorities() + PriorityOrder(string) int — SINGLE SOURCE OF TRUTH for the priority integer mapping (Pitfall 5)"
  - "internal/run.ClaimedRun extended with PartitionKey *string, Priority string, BackfillID *uuid.UUID"
  - "internal/run.ClaimNext priority-aware ORDER BY (CASE priority...) — preserves SKIP LOCKED + WHERE state='queued' + UPDATE-state-guard (Pitfall 1 mitigation)"
  - "internal/runtime.Executor.Run(ctx context.Context, claimed *run.ClaimedRun) error — FROZEN signature; plan 03-07 will not change again"
  - "internal/runtime.runStep(ctx, runID, asset, topoOrder, partitionKey) — partition_key threaded into asset.NewAssetIO"
  - "TestClaimPriorityOrdering (correctness: 11 mixed-priority rows claim in tier order)"
  - "TestPriorityClaimLoad (atomicity at scale: 1000 backfill + 50 normal × 100 concurrent claimers, no duplicates, no Pitfall-1 stranding)"
  - "TestPriorityOrderConsistency / TestPriorityOrderingMonotonic / TestAllPrioritiesIsSorted (Pitfall 5 drift-prevention unit tests)"
affects: [03-04-scheduler-tick-loop, 03-05-sensor-evaluator, 03-06-scheduler-subcommand, 03-07-backfill-cli]

# Tech tracking
tech-stack:
  added: []  # Pure additive — no new third-party deps; reuses sql.NullString and uuid.NullUUID
  patterns:
    - "Single-migration signature change: Executor.Run takes *run.ClaimedRun once; plan 03-07 only READS new fields (claimed.Priority for layer-3 token tag) — avoids dual-migration breaks"
    - "SQL CASE expression mirrors Go PriorityOrder() integer mapping 1:1 — drift-prevention test enumerates AllPriorities() to assert agreement"
    - "Priority-aware ORDER BY uses CASE in ORDER BY (priority is a non-indexable expression at the column level) — composite index (state, priority, queued_at) from plan 03-01 backs the scan"
    - "Pitfall 1 is mitigated by code comment + test: WHERE clause stays 'WHERE state=queued' ONLY; priority belongs in ORDER BY"
    - "Defense-in-depth UPDATE guard 'WHERE id=$3 AND state=queued' kept — atomicity contract unchanged"

key-files:
  created:
    - "internal/run/priority.go (Priority enum + AllPriorities + PriorityOrder)"
    - "internal/run/priority_test.go (3 drift-prevention tests)"
    - ".planning/phases/03-scheduling-sensors-partitions/deferred-items.md (out-of-scope ent driver issue)"
  modified:
    - "internal/run/claim.go (ClaimedRun extended; SELECT projects 7 cols; ORDER BY CASE priority)"
    - "internal/run/claim_test.go (added insertWithPriority helper, TestClaimPriorityOrdering, TestPriorityClaimLoad, claimRound + claimRoundResult helpers)"
    - "internal/runtime/executor.go (Executor.Run signature → (ctx, *run.ClaimedRun); runStep takes partitionKey; nil-claim guard)"
    - "internal/runtime/executor_test.go (5 call sites updated to pass *ClaimedRun)"
    - "cmd/platform/worker.go (call site updated; logs priority + partition_key; derefString helper added)"
    - "test/integration/e2e_postgres_test.go (3 integration test call sites updated)"

key-decisions:
  - "Single signature migration for Executor.Run — change once in 03-03 to (ctx, *ClaimedRun); plan 03-07 will only READ new fields. This eliminates the dual-migration risk where intermediate (ctx, runID, assetName, partitionKey) and final (ctx, claimed) would both update callers and break each other."
  - "PriorityOrder unknown/empty value → 1 (normal) — matches SQL CASE ELSE 1 branch. Choice: an unrecognised priority should NOT silently jump ahead of normal runs (privilege escalation) NOR get stranded behind backfills (DoS). Defaulting to 'normal' is the safe middle ground."
  - "ORDER BY CASE expression preferred over a generated column: CASE is portable, requires no schema migration beyond plan 03-01's existing index, and pairs with the (state, priority, queued_at) composite index. If load tests ever show seq scan, the fallback is documented in 03-RESEARCH.md A2 (add a generated priority_order column)."
  - "ClaimedRun struct grew by 3 nullable/value fields — sql.NullString for partition_key (DB column is NULLable), bare string for priority (DB column is NOT NULL with default 'normal'), uuid.NullUUID for backfill_id (DB column is NULLable)."
  - "Pitfall 1 mitigation is doubled: code comment in claim.go forbids adding 'WHERE priority …'; TestPriorityClaimLoad's round 2 (claims AFTER normals are exhausted) detects accidental filtering by failing if backfill rows are not claimed."
  - "TestPriorityClaimLoad runs eagerly (not behind testing.Short) — 130ms is cheap enough that the load coverage is part of every CI run. The test's name signals it's a load profile to operators reading test output."

patterns-established:
  - "SQL CASE expression + Go function pair as drift-prevention: encode the constant in BOTH places, then add a unit test that enumerates the enum values to confirm they agree — catches future readers who edit one but not the other"
  - "Frozen signature comment block in executor.Run docstring — explicitly tells the next plan author 'do not change this signature again' with rationale"
  - "Concurrent test helper (claimRound + claimRoundResult): encapsulates the goroutine + WaitGroup + dedup-map + error-channel boilerplate for any future round-of-N-claims tests"
  - "Priority-aware claim test with INVERTED queued_at — backfills get the OLDEST timestamps so a FIFO-only implementation would claim them first; priority must dominate to pass"

requirements-completed: [ORCH-05, ORCH-06, ORCH-07, ORCH-08]
decisions-implemented: [D-13]

# Metrics
duration: ~7min
completed: 2026-05-08
---

# Phase 3 Plan 03: Priority-Aware Claim + Load Test Summary

**ClaimNext now claims runs in tier order — `critical` → `normal` → `backfill` — without losing the SKIP LOCKED atomicity that the Phase 2 50-goroutine test asserts. The `Executor.Run` signature is frozen at `(ctx, *run.ClaimedRun)` for the rest of Phase 3.**

## Performance

- **Duration:** ~7 min (08:44:32Z → 08:51:35Z)
- **Started:** 2026-05-08T08:44:32Z
- **Completed:** 2026-05-08T08:51:35Z
- **Tasks:** 2 (Task 1 strict TDD RED→GREEN; Task 2 atomic multi-file change)
- **Files created:** 3 (priority.go, priority_test.go, deferred-items.md)
- **Files modified:** 6 source files (claim.go, claim_test.go, executor.go, executor_test.go, worker.go, e2e_postgres_test.go)

## Final claim.go SQL (verbatim)

```go
const selectSQL = `
    SELECT id, asset_name, trigger, queued_at, partition_key, priority, backfill_id
    FROM runs
    WHERE state = 'queued'
    ORDER BY
        CASE priority
            WHEN 'critical' THEN 0
            WHEN 'normal'   THEN 1
            WHEN 'backfill' THEN 2
            ELSE 1
        END ASC,
        queued_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
`
```

The CASE integer mapping (`critical=0, normal=1, backfill=2`, default `1`) mirrors `internal/run/priority.go::PriorityOrder` exactly. `TestPriorityOrderConsistency` enumerates `AllPriorities()` and asserts each value's integer.

## Phase 2 Regression Guard — TestClaimAtomicity50Goroutines

**Status: STILL PASSES** after the ORDER BY change.

```
=== RUN   TestClaimAtomicity50Goroutines
--- PASS: TestClaimAtomicity50Goroutines (0.046s)
```

**Why this works:** the test inserts ONE queued run and asserts exactly one of 50 concurrent goroutines wins. The new ORDER BY only matters when multiple rows are eligible — with a single row, the ordering is irrelevant. SKIP LOCKED + `WHERE state='queued'` + the defense-in-depth UPDATE guard `WHERE id=$3 AND state='queued'` are all unchanged. Atomicity is unchanged.

## Load test runtime

`TestPriorityClaimLoad` inserts 1000 backfill + 50 normal queued runs and runs two rounds of 50 concurrent goroutines each:

```
=== RUN   TestPriorityClaimLoad
--- PASS: TestPriorityClaimLoad (0.13s)
```

**130 milliseconds** for 1050-row insert + 100 concurrent claims — well under the 300s plan budget. Per round assertions:

- Round 1: all 50 claims have `priority='normal'` (no starvation by 1000 older backfill rows). Zero duplicate claim IDs (SKIP LOCKED holds at scale).
- Round 2: all 50 claims have `priority='backfill'` (Pitfall 1 regression check — no `WHERE priority…` filter has been smuggled in). Zero duplicate claim IDs.

The composite index `run_state_priority_queued_at` from plan 03-01 backs the scan; no seq scan observed at this row count.

## TestClaimPriorityOrdering result

11 mixed-priority rows (5 backfill with OLDEST queued_at, 5 normal in middle, 1 critical NEWEST):

```
expected := []string{
    "critical",
    "normal","normal","normal","normal","normal",
    "backfill","backfill","backfill","backfill","backfill",
}
```

PASS. The CASE expression dominates over queued_at — even though backfills have the oldest timestamps, they claim LAST.

## Caller-update map (signature migration)

| File | Lines updated | Old signature → New signature |
|------|---------------|-------------------------------|
| `internal/runtime/executor.go` | line 78–104 (definition) + line 123 (runStep call) + line 184 (runStep signature) + line 244 (NewAssetIO call) | `Run(ctx, runID uuid.UUID, assetName string)` → `Run(ctx context.Context, claimed *run.ClaimedRun)`; `runStep` gains `partitionKey string` |
| `cmd/platform/worker.go` | line 84–95 | `deps.executor.Run(ctx, claimed.ID, claimed.AssetName)` → `deps.executor.Run(ctx, claimed)`; slog logs priority + partition_key; `derefString` helper added |
| `internal/runtime/executor_test.go` | lines 234, 284, 337, 383, 444, 476 (5 call sites) | each `exec.Run(ctx, claimed.ID, "<asset>")` → `exec.Run(ctx, claimed)` |
| `test/integration/e2e_postgres_test.go` | lines 302, 345, 385 (3 call sites) | each `setup.executor.Run(ctx, claimed.ID, claimed.AssetName)` → `setup.executor.Run(ctx, claimed)` |
| `cmd/platform/materialize.go` | NONE — does not call `executor.Run` directly (uses queued-row + state-poll pattern) | — |

## FROZEN signature contract

```go
// FINAL FORM for Phase 3 — plan 03-07 will NOT change this again.
func (e *Executor) Run(ctx context.Context, claimed *run.ClaimedRun) error
```

Plan 03-07 (backfill CLI / D-13 layer 3) needs `claimed.Priority == "backfill"` to gate the layer-3 concurrency-token-pool tag acquisition. Reading `claimed.Priority` requires NO signature change because the field is already on the struct.

The docstring for `Executor.Run` carries a `FROZEN SIGNATURE` block citing this rationale so future plan authors do not break callers a second time.

## Decision-coverage Map (D-13)

| D-13 Layer | Implementation | Test(s) covering |
|------------|----------------|------------------|
| Layer 1: priority column + CHECK | (delivered by plan 03-01) | (plan 03-01 verified via `\d runs`) |
| **Layer 2: priority-then-FIFO claim** | `selectSQL` CASE expression in `internal/run/claim.go`; `PriorityOrder` in `internal/run/priority.go` | **TestClaimPriorityOrdering** (correctness — proves CASE actually reorders); **TestPriorityClaimLoad** (atomicity at scale — proves SKIP LOCKED + no Pitfall 1 stranding); **TestPriorityOrderConsistency** + **TestPriorityOrderingMonotonic** + **TestAllPrioritiesIsSorted** (Pitfall 5 drift-prevention unit tests) |
| Layer 3: backfill resource tag | (deferred to plan 03-07; reads `claimed.Priority`) | (plan 03-07 will add) |

## Threat Surface Coverage

| Threat ID | Status | Evidence |
|-----------|--------|----------|
| T-03-03-01 (Pitfall 1: WHERE filter strands backfills) | mitigated | Code comment in claim.go forbids `WHERE priority…`; `TestPriorityClaimLoad` round 2 detects accidental filtering by failing if backfill rows do not claim after normals are exhausted |
| T-03-03-02 (Pitfall 5: Priority enum integer drift) | mitigated | `PriorityOrder` in priority.go is single source of truth; SQL CASE mirrors 1:1; `TestPriorityOrderConsistency` enumerates every `AllPriorities()` value |
| T-03-03-03 (DoS: seq scan instead of index scan under load) | mitigated | Plan 03-01's `(state, priority, queued_at)` composite index backs the scan; `TestPriorityClaimLoad` finishes in 130ms for 1050 rows + 100 goroutines (300s timeout would fire if scan degraded) |
| T-03-03-04 (Regression: 50-goroutine atomicity broken by ORDER BY change) | mitigated | `TestClaimAtomicity50Goroutines` re-run as plan acceptance gate — passes |
| T-03-03-05 (Information disclosure via slog) | accepted | partition_key/priority/backfill_id are non-sensitive scheduling metadata |
| T-03-03-06 (Privilege escalation: caller submits priority='critical') | mitigated (defer) | DB-level CHECK from plan 03-01 prevents non-enum values; authorization (who may submit) is plan 03-07 / Phase 4 work |

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — Blocking] `test/integration/e2e_postgres_test.go` had 3 call sites still using the Phase 2 signature**

- **Found during:** Final `go vet ./...` after Task 2 commit.
- **Issue:** Plan's task 2 read_first list mentioned `internal/runtime/executor_test.go` as the test fixture file but did not enumerate `test/integration/e2e_postgres_test.go`, which also calls `setup.executor.Run(...)` in three different test functions. `go vet` failed with `too many arguments in call to setup.executor.Run`.
- **Fix:** Identical mechanical replacement to the runtime test fixture: `setup.executor.Run(ctx, claimed.ID, claimed.AssetName)` → `setup.executor.Run(ctx, claimed)`. Pure call-site update; no test logic changed.
- **Files modified:** `test/integration/e2e_postgres_test.go` (lines 302, 345, 385)
- **Commit:** `80c48e6`
- **Verification:** `go vet ./...` clean; `go build ./...` clean.

### Out-of-scope (logged for later)

**1. [DEFERRED] internal/runtime executor tests fail at fixture init with "open ent: unsupported driver: pgx"**

- **Pre-existing:** Verified by `git stash` rewinding the executor signature change — failures occur on the parent commit `2f2df38af493af3bbecd1a3f1502c66af9ca1588` BEFORE this plan, so it is NOT introduced by 03-03.
- **Root cause:** `internal/runtime/executor_test.go` line 101 does `stent.Open("pgx", dsn)` while ent's pgx adapter expects the driver name `"postgres"`. Driver name disagreement, not a signature issue.
- **Why deferred:** Out of plan 03-03's scope (does not block any plan-03-03 verification). Documented in `.planning/phases/03-scheduling-sensors-partitions/deferred-items.md` for the next phase plan to fix.
- **Plan 03-03 verification path:** the `internal/run/claim_test.go` route (which uses a plain `*sql.DB` via `sqlStorage`, sidestepping ent) covers all signature-change correctness end-to-end. Build is green; vet is green; the plan-defined test set passes.

### Other minor adjustments

- **Combined Task 2 RED/GREEN into a single commit** (`7e023b8`) instead of two separate commits. Rationale: the ClaimedRun struct extension, the SQL CASE rewrite, the Executor.Run signature change, AND every caller update must land together to keep `go build ./...` green; splitting would have produced an intermediate broken-build commit that cannot bisect cleanly. Task 1 still uses strict TDD RED→GREEN (separate commits `4f350ab` and `654fe90`).
- **Added a nil-claim guard `if claimed == nil` at the top of `Executor.Run`** (Rule 2 — missing critical functionality / defensive programming for the new pointer parameter). The plan's behavior contract did not call this out, but the new pointer-receiving signature opens a nil-deref panic vector that the old `(uuid.UUID, string)` signature could not have. Cost zero scope creep; pure correctness.

## Task Commits

| Task | Phase | Commit | Description |
|------|-------|--------|-------------|
| 1 | RED   | `4f350ab` | test(03-03): add failing tests for run.PriorityOrder + Priority enum (Pitfall 5) |
| 1 | GREEN | `654fe90` | feat(03-03): add Priority enum + PriorityOrder single source of truth (D-13, Pitfall 5) |
| 2 | atomic | `7e023b8` | feat(03-03): priority-aware ClaimNext + extend ClaimedRun + Executor.Run takes *ClaimedRun (D-13 layer 2) |
| – | fix | `80c48e6` | fix(03-03): update integration test executor.Run callers to (ctx, *ClaimedRun) |

Verify with `git log --oneline 2f2df38..HEAD`.

## Self-Check: PASSED

**Created files exist:**
- FOUND: internal/run/priority.go
- FOUND: internal/run/priority_test.go
- FOUND: .planning/phases/03-scheduling-sensors-partitions/deferred-items.md

**Modified files updated:**
- FOUND: internal/run/claim.go (ClaimedRun struct + SELECT 7 cols + CASE priority ORDER BY)
- FOUND: internal/run/claim_test.go (TestClaimPriorityOrdering + TestPriorityClaimLoad + helpers)
- FOUND: internal/runtime/executor.go (Run signature → (ctx, *run.ClaimedRun); runStep takes partitionKey)
- FOUND: internal/runtime/executor_test.go (5 call sites updated)
- FOUND: cmd/platform/worker.go (Run call updated; derefString helper)
- FOUND: test/integration/e2e_postgres_test.go (3 call sites updated)

**Commits exist:**
- FOUND: 4f350ab (Task 1 RED)
- FOUND: 654fe90 (Task 1 GREEN — priority.go)
- FOUND: 7e023b8 (Task 2 atomic — claim + executor + callers + tests)
- FOUND: 80c48e6 (integration test fix)

**Build & test verification:**
- `go build ./...` → green
- `go vet ./...` → clean
- `DATABASE_URL=postgres://platform:platform@localhost:5432/platform?sslmode=disable go test ./internal/run/... -count=1 -timeout 300s` → all 17 tests pass
- `TestClaimAtomicity50Goroutines` (Phase 2 regression guard) → PASS
- `TestClaimPriorityOrdering` (correctness) → PASS
- `TestPriorityClaimLoad` (atomicity at scale, 130ms for 1050 rows + 100 claimers) → PASS
- `TestPriorityOrderConsistency` / `TestPriorityOrderingMonotonic` / `TestAllPrioritiesIsSorted` (Pitfall 5 drift-prevention) → PASS

## Next Plan Readiness

- **Plan 03-04 (scheduler tick loop)** can now consume the priority-aware claim path: schedule fires INSERT runs with default `priority='normal'` (DB column default) and the claim path will tier them above any pending backfill rows automatically — no scheduler-side priority awareness needed.
- **Plan 03-05 (sensor evaluator)** likewise: sensor-fired runs INSERT with default `priority='normal'` and naturally claim above backfill traffic.
- **Plan 03-06 (scheduler subcommand)** has the full claim primitive ready; scheduler/sensor goroutines just emit INSERTs with the correct priority value.
- **Plan 03-07 (backfill CLI / D-13 layer 3)** will:
  1. INSERT mass-enqueue rows with `priority='backfill'` and `backfill_id` set (claim path will already isolate them);
  2. READ `claimed.Priority` in the layer-3 acquisition path WITHOUT changing `Executor.Run`'s signature — the FROZEN signature contract is precisely what enables this.

---

*Phase: 03-scheduling-sensors-partitions*
*Plan: 03 (priority-claim and load test)*
*Completed: 2026-05-08*
