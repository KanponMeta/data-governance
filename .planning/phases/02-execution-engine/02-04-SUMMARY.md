---
phase: 2
plan: "04"
subsystem: execution-engine
title: "PostgreSQL connector + worker/materialize CLI + stale-run reaper + e2e acceptance test"
one_liner: "Real PostgreSQL connector via pgxpool, worker/materialize CLI subcommands with SIGTERM-safe reaper goroutine, and full e2e testcontainers acceptance test closing ROADMAP criteria 1, 2, 4 and D-14 crash recovery"
tags:
  - postgres
  - connector
  - cli
  - reaper
  - e2e-test
  - crash-recovery
requirements: [ORCH-10, CONN-01]
depends_on: ["02-01", "02-02", "02-03"]
dependency_graph:
  requires:
    - "02-01: asset SDK (Builder, AssetIO, DefinitionRegistry)"
    - "02-02: run lifecycle FSM, ClaimNext, Heartbeat, TransitionForReset, last_heartbeat column"
    - "02-03: runtime.Executor, concurrency.Pool, retry.Policy, connector/config.FactoryRegistry"
  provides:
    - "internal/connector/firstparty/postgres: reference PostgreSQL connector (pgxpool)"
    - "internal/run/reaper.go: StaleRunReaper goroutine (D-14 Option B)"
    - "cmd/platform/worker.go: worker subcommand + reaper spawn"
    - "cmd/platform/materialize.go: materialize subcommand (sync + detach)"
    - "cmd/platform/factories.go: FactoryRegistry initializer for first-party connectors"
    - "testdata/integration/: SDK usage example + config template"
    - "test/integration/e2e_postgres_test.go: full Phase 2 e2e acceptance test"
  affects:
    - "cmd/platform/main.go: added worker + materialize dispatch cases"
    - "internal/concurrency/pool.go: fixed FOR UPDATE on aggregate (pg_advisory_xact_lock)"
    - "internal/asset/registry.go: exported ResetForTest() for external test packages"
tech_stack:
  added:
    - "github.com/testcontainers/testcontainers-go v0.42.0: ephemeral Postgres for connector and e2e tests"
    - "github.com/testcontainers/testcontainers-go/modules/postgres v0.42.0: Postgres module with BasicWaitStrategies"
  patterns:
    - "pgxpool.NewWithConfig for pool lifecycle (D-08 singleton)"
    - "pg_advisory_xact_lock(hashtext(tag)) for concurrency token serialization (replaces broken FOR UPDATE on aggregate)"
    - "sync.WaitGroup + signal.NotifyContext for graceful SIGTERM shutdown in worker"
    - "In-process e2e test (no binary subprocess): build deps directly, queue run via raw SQL, run executor.Run inline"
    - "recordingWriter / eventRecorder in-memory event.Writer for unit test isolation"
key_files:
  created:
    - "internal/connector/firstparty/postgres/postgres.go"
    - "internal/connector/firstparty/postgres/factory.go"
    - "internal/connector/firstparty/postgres/postgres_test.go"
    - "internal/run/reaper.go"
    - "internal/run/reaper_test.go"
    - "cmd/platform/factories.go"
    - "cmd/platform/worker.go"
    - "cmd/platform/materialize.go"
    - "testdata/integration/example_assets.go"
    - "testdata/integration/config.yaml"
    - "test/integration/e2e_postgres_test.go"
  modified:
    - "cmd/platform/main.go (added worker + materialize dispatch)"
    - "internal/concurrency/pool.go (fixed FOR UPDATE on aggregate)"
    - "internal/asset/registry.go (exported ResetForTest)"
    - "go.mod / go.sum (testcontainers dependency)"
decisions:
  - "pg_advisory_xact_lock(hashtext(tag)) replaces FOR UPDATE on aggregate in Pool.Acquire — PostgreSQL does not allow FOR UPDATE with aggregate functions (SQLSTATE 0A000)"
  - "D-14 Option B: StaleRunReaper over River.max_attempts — simpler for single-binary Phase 2; River wiring deferred to Phase 3 when out-of-process job dispatch is required"
  - "In-process e2e test approach — avoids binary compilation in test, faster CI, equally validates the runtime behavior"
  - "auth.TokenIssuer.Verify used in materialize (no standalone ParseToken); signing key from PLATFORM_JWT_SIGNING_KEY env var"
  - "asset.ResetForTest() exported to allow external test packages to reset the global registry between tests"
  - "RetryPolicy.Max=2 for failing asset in integration test — ShouldRetry uses attempt<Max so Max:2 → 2 attempts (1 retry)"
metrics:
  duration: "~55 minutes"
  completed_at: "2026-05-08"
  tasks_completed: 3
  tasks_total: 3
  files_created: 11
  files_modified: 4
---

# Phase 2 Plan 04: PostgreSQL connector + worker/materialize CLI + stale-run reaper + e2e acceptance test Summary

## What Was Built

### Task 4.1 — PostgreSQL connector (commit 77e3187)

**internal/connector/firstparty/postgres/postgres.go** — Reference first-party connector (D-12).

- `New(ctx, dsn)` opens a `pgxpool.Pool` with connectivity verification on startup (fast-fail)
- `APIVersion()` returns `connector.APIVersion` ("v1.0.0") — compile-time assertion `var _ connector.Connector = (*Postgres)(nil)`
- `Ping(ctx, req)` returns `ConnectorName="postgres"`, `Capabilities.SupportsSchemaCapture=true`
- `Schema(ctx, req)` queries `information_schema.columns` ordered by `ordinal_position`; splits `schema.table` identifiers
- `Read(ctx, req)` executes `SELECT * FROM "schema"."table" [LIMIT n]`; ctx cancellation returns `context.Canceled` (PITFALLS §10)
- `Write(ctx, req)` builds parameterized `INSERT ... VALUES ($1,$2,...),($N,...)` — no string concatenation of values (T-02-04-03)
- `Close()` drains the pool; idempotent; subsequent calls return `ErrClosed`
- `quoteIdentifier()` rejects identifiers containing `"` (SQL injection defense for asset names)

**internal/connector/firstparty/postgres/factory.go** — `Factory(params) (connector.Connector, error)` returns `ErrMissingDSN` when `dsn` is absent; constructs with 10s timeout.

**Tests** (11 tests, testcontainers postgres:16-alpine with `BasicWaitStrategies()`):
TestCompileTimeAssertion, TestPing, TestSchemaRoundTrip, TestWriteThenRead, TestReadCtxCancel, TestFactory_MissingDSN, TestFactory_Builds, TestClose, TestClose_Idempotent, TestQuoteIdentifier_RejectQuotes, TestAPIVersion.

### Task 4.2 — Stale-run reaper (commit 9da434d)

**internal/run/reaper.go** — `StaleRunReaper` implements D-14 Option B crash recovery.

**Constants:**
- `DefaultReaperStaleAfter = 5 * time.Minute` (10x the 30s heartbeat tick — 10x safety margin prevents spurious re-queues)
- `DefaultReaperInterval = 60 * time.Second` (combined recovery upper bound: ~6 minutes)

**`SweepOnce(ctx) (int64, error)` semantics:**
1. SELECT candidates: `WHERE state IN ('starting','running') AND last_heartbeat < $cutoff`
2. For each candidate: `TransitionForReset(from, StateQueued)` validates the FSM backward edge
3. Atomic UPDATE: `WHERE id=$1 AND state IN (...) AND last_heartbeat < $2` — if 0 rows affected, another reaper or a live heartbeat won the race (idempotent)
4. On success: emit `EventTypeRunCanceled` with `Reason="reaper: worker heartbeat lost"`, increment reclaimed count

**`Run(ctx)` goroutine:** ticks every `Interval` until ctx canceled.

**Tests** (7 tests, uses DATABASE_URL or skips):
TestReaperStaleRunReclaimed, TestReaperStartingState, TestReaperTerminalStateIgnored, TestReaperEventEmitted, TestReaperConcurrentSweepIdempotent, TestReaperGoroutineExitsOnCancel, TestSweepOnce_ReturnsCount.

### Task 4.3 — CLI subcommands + factories + e2e integration test (commit 8b4ce6d)

**cmd/platform/factories.go:** `newFactoryRegistry()` pre-registers `"postgres"` factory; entry point for plan 02-05 to add six more.

**cmd/platform/worker.go:** 
- `runWorker()` → `signal.NotifyContext` → `bootstrap()` → `pool.ReleaseStale(24h)` → spawn `StaleRunReaper` goroutine (tracked by `reaperWG`) → claim loop (500ms idle poll) → `executor.Run` dispatch
- `bootstrap(ctx)` loads config from `PLATFORM_CONFIG`, opens storage from `DATABASE_URL`, builds connector registry via `FactoryRegistry.BuildAll`, builds `concurrency.Pool`, constructs `runtime.Executor`
- SIGTERM shutdown: ctx cancellation stops claim loop; `defer reaperWG.Wait()` ensures reaper exits before function returns

**cmd/platform/materialize.go:**
- `runMaterialize(args)` → flag parse (`--detach`, `--timeout=30m`) → auth gate → `bootstrap()` → asset lookup → INSERT run row → emit `run.queued` event → detach OR `waitForRun` poll (500ms tick, terminal state check)
- Auth: `PLATFORM_NO_AUTH=1` bypasses; otherwise requires `PLATFORM_SERVICE_TOKEN` validated by `auth.TokenIssuer.Verify` with `PLATFORM_JWT_SIGNING_KEY`
- Failure path: `os.Exit(1)` via `runMaterialize` returning non-nil error (via `main.go` dispatch)

**test/integration/e2e_postgres_test.go** (5 tests, testcontainers, skip-on-no-docker):

| Test | Closes |
|------|--------|
| TestE2E_PostgresMaterialize | Users_raw→users_clean DAG executes; data lands in users_clean; succeeded event; 2 step events each |
| TestE2E_PostgresMaterialize_Failure | step.failed×2 + retry_scheduled×1 + run.failed×1 in event_log (acceptance criterion 2) |
| TestE2E_TopologicalOrder | users_raw step.started occurred_at < users_clean step.started (acceptance criterion 1) |
| TestE2E_StaleRunReaperRecovery | Stale 'running' row → queued within 5s; run.canceled event recorded (D-14 + T-02-04-08) |
| TestE2E_DetachMode | Queued run inserted immediately, state='queued' on return |

## D-14 Implementation Note

D-14 originally described "River handles infrastructure faults". Phase 2 implements the same recovery guarantee via **StaleRunReaper** (Option B):

- **Why not River.max_attempts:** Phase 2 worker is a single Go binary. Wiring River as a secondary dispatcher introduces a new lifecycle (`river.Client.Start`, `worker.Register`) and a parallel persistence path. The reaper requires only a goroutine and one SQL SELECT — strictly simpler.
- **Functional equivalence:** River would retry failed jobs at the dispatch layer; the reaper resets stuck runs at the state layer ("reset to queued, let a healthy worker re-claim"). For Phase 2's single-tenant polling model, the result is identical: a crashed worker's run is recovered within ~6 minutes.
- **Deferred, not abandoned:** When Phase 3+ adds out-of-process connector subprocesses, River becomes a hard requirement (job dispatch across nodes). At that point, `factories.go` is the entry point to integrate River; the reaper continues alongside.

## Hooks for Plan 02-05

`cmd/platform/factories.go::newFactoryRegistry()` is the single registration point for first-party connectors. Plan 02-05 adds `mysql`, `bigquery`, `snowflake`, `s3`, `gcs`, `hdfs` by calling `r.RegisterFactory("mysql", mysql.Factory)` etc.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed FOR UPDATE on aggregate in concurrency.Pool.Acquire**
- **Found during:** Task 4.3 (TestE2E_PostgresMaterialize)
- **Issue:** `SELECT COALESCE(SUM(weight), 0)::int ... FOR UPDATE` is invalid SQL in PostgreSQL (`SQLSTATE 0A000: FOR UPDATE is not allowed with aggregate functions`)
- **Fix:** Replaced with `SELECT pg_advisory_xact_lock(hashtext($1))` before the aggregate query. Transaction-scoped advisory lock serializes concurrent Acquire calls for the same resource_tag without touching the aggregate.
- **Files modified:** `internal/concurrency/pool.go`
- **Commit:** 8b4ce6d

**2. [Rule 2 - Missing critical functionality] Exported asset.ResetForTest()**
- **Found during:** Task 4.3 (e2e test needs to reset global registry between tests)
- **Issue:** `resetForTest()` is package-private; external test packages (test/integration) cannot call it
- **Fix:** Added exported `ResetForTest()` wrapper with warning comment about non-concurrent use
- **Files modified:** `internal/asset/registry.go`
- **Commit:** 8b4ce6d

**3. [Rule 1 - Bug] RetryPolicy.Max semantic — RegisterFailingAsset uses Max:2 for 2 attempts**
- **Found during:** Task 4.3 (TestE2E_PostgresMaterialize_Failure showed step.failed×1 instead of ×2)
- **Issue:** `ShouldRetry(attempt, policy)` returns `attempt < Max`. With Max:1, attempt=1 → `1 < 1 = false` → no retry (1 attempt total). Max:2 → 2 attempts (1 retry).
- **Fix:** Changed `RegisterFailingAsset` to use `Max: 2` to get the expected retry sequence
- **Files modified:** `testdata/integration/example_assets.go`
- **Commit:** 8b4ce6d

**4. [Rule 1 - Bug] materialize.go uses auth.TokenIssuer.Verify (no standalone ParseToken)**
- **Found during:** Task 4.3 plan review
- **Issue:** Plan called `auth.ParseToken(tok)` which does not exist in the auth package
- **Fix:** Used `auth.NewTokenIssuer([]byte(signingKey), 0).Verify(tok)` with `PLATFORM_JWT_SIGNING_KEY` env var
- **Files modified:** `cmd/platform/materialize.go`
- **Commit:** 8b4ce6d

## Self-Check: PASSED

All files verified present:
- internal/connector/firstparty/postgres/postgres.go — FOUND
- internal/connector/firstparty/postgres/factory.go — FOUND
- internal/run/reaper.go — FOUND
- cmd/platform/worker.go — FOUND
- cmd/platform/materialize.go — FOUND
- cmd/platform/factories.go — FOUND
- test/integration/e2e_postgres_test.go — FOUND

All commits verified:
- 77e3187 (Task 4.1: PostgreSQL connector) — FOUND
- 9da434d (Task 4.2: Stale-run reaper) — FOUND
- 8b4ce6d (Task 4.3: CLI subcommands + e2e tests) — FOUND
