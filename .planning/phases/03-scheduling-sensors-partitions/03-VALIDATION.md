---
phase: 3
slug: scheduling-sensors-partitions
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-05-08
---

# Phase 3 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.
> Source-of-truth: `03-RESEARCH.md` § Validation Architecture.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go testing package (stdlib) + `testify` v1.11.1 (already in `go.mod`) |
| **Config file** | none — `DATABASE_URL` env var required for integration tests (mirrors Phase 2 `internal/run/claim_test.go` pattern) |
| **Quick run command** | `go test ./internal/partition/... ./internal/run/... -count=1 -timeout 30s` |
| **Full suite command** | `DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./internal/... ./cmd/... -count=1 -timeout 300s` |
| **Estimated runtime** | ~120s full suite (load test adds ~60s) |

---

## Sampling Rate

- **After every task commit:** Run `go test ./internal/partition/... ./internal/run/... -count=1 -timeout 30s`
- **After every plan wave:** Run `DATABASE_URL=... go test ./internal/... -count=1 -timeout 120s`
- **Before `/gsd-verify-work`:** Full suite must be green (including the 1000-backfill+50-normal load test)
- **Max feedback latency:** 30s for the per-commit quick run; 120s for the per-wave full suite

---

## Per-Task Verification Map

> Plan / Wave / Task IDs are placeholders until the planner assigns them. The mapping below is keyed on requirement → behavior → automated command. The planner MUST attach these commands to the corresponding `<automated>` blocks during plan generation.

| Plan (TBD) | Wave (TBD) | Requirement | Decision Ref | Behavior Under Test | Test Type | Automated Command | File Exists | Status |
|------------|------------|-------------|--------------|---------------------|-----------|-------------------|-------------|--------|
| TBD | 1 | ORCH-05 | D-01..D-04 | Cron-scheduled asset auto-fires at next scheduled time after daemon start | Integration | `DATABASE_URL=... go test ./internal/schedule/... -run TestScheduler -v` | ❌ Wave 0 | ⬜ pending |
| TBD | 1 | ORCH-05 | D-04 | Missed-window LatestOnly recovery emits `schedule.missed` with correct skip count | Unit | `go test ./internal/schedule/... -run TestMissedWindowLatestOnly` | ❌ Wave 0 | ⬜ pending |
| TBD | 1 | ORCH-05 | D-03 | Invalid cron expression returns error at builder time, not runtime | Unit | `go test ./internal/asset/... -run TestScheduleInvalidCron` | ❌ Wave 0 | ⬜ pending |
| TBD | 2 | ORCH-06 | D-05, D-06 | Sensor fires materialization when `Sense()` returns `Fired=true` | Integration | `DATABASE_URL=... go test ./internal/sensor/... -run TestSensorFire` | ❌ Wave 0 | ⬜ pending |
| TBD | 2 | ORCH-06 | D-07 | RunKey dedup prevents second enqueue for same key | Unit | `go test ./internal/sensor/... -run TestSensorRunKeyDedup` | ❌ Wave 0 | ⬜ pending |
| TBD | 2 | ORCH-06 | D-07 | Cooldown window prevents enqueue regardless of RunKey | Unit | `go test ./internal/sensor/... -run TestSensorCooldown` | ❌ Wave 0 | ⬜ pending |
| TBD | 2 | ORCH-06 | D-08 | Panic in `Sense()` is recovered; `consecutive_failures` incremented | Unit | `go test ./internal/sensor/... -run TestSensorPanicRecovery` | ❌ Wave 0 | ⬜ pending |
| TBD | 2 | ORCH-06 | D-08 | After N consecutive failures sensor `disabled_at` set; `sensor.disabled` event emitted | Unit | `go test ./internal/sensor/... -run TestSensorAutoDisable` | ❌ Wave 0 | ⬜ pending |
| TBD | 1 | ORCH-07 | D-09, D-11 | DailyKey/WeeklyKey/MonthlyKey produce correct UTC ISO-8601 strings | Unit | `go test ./internal/partition/... -run TestPartitionKeyGen` | ❌ Wave 0 | ⬜ pending |
| TBD | 1 | ORCH-07 | D-11 | ISO week edge case: 2019-12-30 → `2020-W01` | Unit | `go test ./internal/partition/... -run TestWeeklyKeyYearBoundary` | ❌ Wave 0 | ⬜ pending |
| TBD | 1 | ORCH-07 | D-09 | `KeysBetween(daily, 2024-01-01, 2024-01-31)` returns 31 keys; weekly/monthly variants verified | Unit | `go test ./internal/partition/... -run TestKeysBetween` | ❌ Wave 0 | ⬜ pending |
| TBD | 3 | ORCH-07 | D-10, D-15 | Time-partitioned backfill: each partition is its own run with its own `event_log` entries | Integration | `DATABASE_URL=... go test ./internal/backfill/... -run TestBackfillTimePartition` | ❌ Wave 0 | ⬜ pending |
| TBD | 3 | ORCH-08 | D-09, D-16 | CategoryPartitions: each category is independent; one failure does not block siblings | Integration | `DATABASE_URL=... go test ./internal/backfill/... -run TestCategoryPartitionIndependence` | ❌ Wave 0 | ⬜ pending |
| TBD | 1 | ORCH-07 | D-10 | Partial unique index on `(asset_name, partition_key) WHERE state IN ('queued','starting','running')` rejects duplicate in-flight partition runs | Integration | `DATABASE_URL=... go test ./internal/partition/... -run TestPartitionUniqueConstraint` | ❌ Wave 0 | ⬜ pending |
| TBD | 1 | ORCH-07 / ORCH-08 | D-13 | Priority-aware claim: normal runs claimed before backfill runs (CASE ORDER BY) | Integration | `DATABASE_URL=... go test ./internal/run/... -run TestClaimPriorityOrdering` | ❌ Wave 0 | ⬜ pending |
| TBD | 1 | ORCH-04 (Phase 2 regression) | D-13 | **50-goroutine claim atomicity test** — must continue passing after priority ORDER BY change | Integration | `DATABASE_URL=... go test ./internal/run/... -run TestClaimAtomicity50Goroutines` | ✅ `internal/run/claim_test.go` | ⬜ pending |
| TBD | 3 | ORCH-07 / ORCH-08 (deferred) | D-13 | **1000-backfill + 50-normal priority-claim load test** — `normal` claimed first, no duplicate claims under SKIP LOCKED | Load | `DATABASE_URL=... go test ./internal/run/... -run TestPriorityClaimLoad -timeout 300s` | ❌ Wave 0 | ⬜ pending |
| TBD | 1 | ORCH-05/06/07/08 | D-17 | All Phase 3 `event_type` enum values accepted by `event.Writer` | Unit | `go test ./internal/event/... -run TestAllPhase3EventTypes` | ❌ Wave 0 | ⬜ pending |
| TBD | 3 | ORCH-07 / ORCH-08 | D-14 | Backfill CLI spec parsing: date range, comma list, single key | Unit | `go test ./internal/backfill/... -run TestParsePartitionSpec` | ❌ Wave 0 | ⬜ pending |
| TBD | 3 | ORCH-07 / ORCH-08 | D-14, D-15 | Backfill row-count guard rejects spec exceeding `--max-partitions` | Unit | `go test ./internal/backfill/... -run TestMaxPartitionsGuard` | ❌ Wave 0 | ⬜ pending |
| TBD | 2 | ORCH-05 / ORCH-06 | D-01..D-08 | Scheduler subcommand graceful-shutdown drains in-flight ticks within configured timeout | Integration | `DATABASE_URL=... go test ./cmd/platform/... -run TestSchedulerGracefulShutdown` | ❌ Wave 0 | ⬜ pending |

*Status legend:* ⬜ pending · ✅ green · ❌ red · ⚠️ flaky

---

## Wave 0 Requirements

**Wave 0** (must run before any implementation tasks): create test stubs that surface MISSING references so downstream tasks have a place to anchor `<automated>` verifications.

- [ ] `internal/partition/keygen_test.go` — covers ORCH-07 + ISO-week year-boundary edge case
- [ ] `internal/partition/strategy_test.go` — Daily/Weekly/Monthly/Category strategy contracts
- [ ] `internal/schedule/fire_test.go` — covers ORCH-05 tick logic + missed-window recovery
- [ ] `internal/schedule/missed_test.go` — `schedule.missed` event with skip count
- [ ] `internal/sensor/evaluate_test.go` — covers ORCH-06 dedup + panic recovery + auto-disable
- [ ] `internal/backfill/submit_test.go` — covers ORCH-07/08 + D-14 spec parsing + max-partitions guard
- [ ] `internal/backfill/independence_test.go` — category/time partition independence under failure
- [ ] `internal/run/claim_test.go` — **EXISTS** (Phase 2). Extend with `TestClaimPriorityOrdering` and `TestPriorityClaimLoad`. Existing `TestClaimAtomicity50Goroutines` MUST keep passing.
- [ ] `internal/event/types_test.go` — Phase 3 EventType enum value coverage
- [ ] `internal/asset/builder_test.go` — Schedule/Sensor/Partitions chained builder methods (extend existing test file)
- [ ] `cmd/platform/scheduler_test.go` — graceful shutdown
- [ ] `migrations/2026MMDDHHMMSS_phase3_*.sql` — schedules, sensors, backfills CREATE TABLE + ALTER TABLE runs (partition_key, priority, backfill_id) + partial unique index + CHECK constraints + role grants

*Framework install:* none — `testify` already pinned in `go.mod`.

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Operator UX of `./platform backfill` CLI output (status, IDs, failure modes) | ORCH-07 / ORCH-08 (D-14) | Output formatting and copy/paste ergonomics are subjective | Run `./platform backfill assets.users --partitions=2024-01-01:2024-01-07`; confirm `backfill_id` is copy-pasteable; run `./platform backfill status <id>`; confirm progress aggregation reads cleanly |
| Schedule restart behavior under multi-hour daemon outage | ORCH-05 (D-04) | Requires manipulating wall clock or daemon downtime > tick interval; hard to fake reliably in CI | Stop scheduler with running schedule; advance system clock past 3 missed windows; restart scheduler; confirm only 1 catch-up run enqueued and `schedule.missed{skipped: 2}` emitted |

---

## Validation Sign-Off

- [ ] All Phase 3 tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references in the verification map
- [ ] No watch-mode flags in any test command
- [ ] Feedback latency < 30s (quick) / < 120s (full suite excluding load test) / < 300s (load test)
- [ ] `nyquist_compliant: true` set in frontmatter once planner has wired every Wave/Task ID

**Approval:** pending
