---
phase: 03-scheduling-sensors-partitions
padded_phase: "03"
fixed_at: 2026-05-08T10:20:00Z
review_path: .planning/phases/03-scheduling-sensors-partitions/03-REVIEW.md
iteration: 1
findings_in_scope: 4
fixed: 4
skipped: 0
status: all_fixed
---

# Phase 03: Code Review Fix Report

**Fixed at:** 2026-05-08T10:20:00Z
**Source review:** .planning/phases/03-scheduling-sensors-partitions/03-REVIEW.md
**Iteration:** 1

**Summary:**
- Findings in scope (WR-01 through WR-04): 4
- Fixed: 4
- Skipped: 0

## Fixed Issues

### WR-01: `UpsertSchedules` race produces duplicate rows under multi-replica startup

**Files modified:** `internal/schedule/registry.go`
**Commit:** 0b45bb2
**Applied fix:** Wrapped the SELECT-then-INSERT/UPDATE sequence inside a `SERIALIZABLE` transaction. Under SERIALIZABLE isolation, two replicas that concurrently SELECT "not found" cannot both INSERT — one transaction aborts with a serialisation failure that the caller can retry. The comment at line 44 was updated to explain the race and the remedy.

### WR-02: `sensor.upsertOneSensor` has the same multi-replica startup race as WR-01

**Files modified:** `internal/sensor/registry.go`
**Commit:** 0b45bb2 (same atomic commit as WR-01)
**Applied fix:** Same pattern as WR-01: the SELECT/INSERT inside `upsertOneSensor` is now wrapped in a `SERIALIZABLE` transaction. The function now properly commits or returns errors, with descriptive error wrappers added.

### WR-03: Scheduler `shutdownCtx` is created but never used

**Files modified:** `cmd/platform/scheduler.go`
**Commit:** d07079b
**Applied fix:** Replaced `_ = shutdownCtx` (dead code) with an actual call to `runOneTick(shutdownCtx)`. On SIGTERM, the scheduler now drains any in-flight tick on a fresh context carrying the configured `PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT`, making the shutdown timeout behaviour match its documented intent.

### WR-04: `computeNextAndDetectMiss` returns a future window for clock-skew; no guard in `FireOneSchedule`

**Files modified:** `internal/schedule/fire.go`
**Commit:** 8281ab3
**Applied fix:** Added a guard immediately after `computeNextAndDetectMiss` that checks `windowToFire.After(now)`. When true (clock-skew scenario), the schedule row's `next_fire_at` is rolled forward to `windowToFire` and the transaction commits without inserting a runs row, preserving semantic correctness and preventing a future-window partition key from being recorded.

---

_Fixed: 2026-05-08T10:20:00Z_
_Fixer: Claude (gsd-code-fixer)_
_Iteration: 1_