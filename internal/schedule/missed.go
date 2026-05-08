// Package schedule implements the cron scheduler daemon (D-01..D-04).
//
// The package surface:
//   - FireOneSchedule (EXPORTED): single-row tick — selects the next due
//     schedule with FOR UPDATE SKIP LOCKED, inserts a runs row, updates
//     last_fire_at / next_fire_at, and emits schedule.fired (and possibly
//     schedule.missed) events. Production callers (plan 03-06's scheduler
//     subcommand) call this directly so they can interleave sensor evaluation.
//   - Daemon.run (UNEXPORTED): wraps FireOneSchedule in a tick loop with
//     jittered timing for package-internal tests only. Production code does
//     NOT use this method.
//   - UpsertSchedules: idempotent registry → schedules-table sync, called once
//     at daemon start (Open Question 3).
//   - computeNextAndDetectMiss: the LatestOnly missed-window helper (D-04).
//
// Multi-replica safety comes from the same SELECT FOR UPDATE SKIP LOCKED
// primitive used by the run-claim path (D-03). No leader election. No
// advisory locks. No River.
package schedule

import (
	"time"

	"github.com/robfig/cron/v3"
)

// cronParser is the package-level parser. Parser-only usage (D-03) — the
// in-process Cron runner from robfig/cron/v3 is NEVER instantiated.
// Reused across UpsertSchedules and FireOneSchedule.
var cronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// computeNextAndDetectMiss walks forward from lastFiredAt to find the most
// recent window <= now (D-04 LatestOnly). Returns (windowToFire, missedCount).
//
// missedCount is the number of windows skipped while the daemon was down.
// missedCount > 0 is the trigger for the schedule.missed event with
// skipped_count payload.
//
// If lastFiredAt is zero (the asset has never been fired — schedules table row
// freshly inserted with last_fire_at NULL), the function returns the most
// recent past window without flagging "missed". Treating zero as "thousands of
// skipped windows since epoch" would generate a noisy event at every first
// registration; we explicitly suppress that.
//
// If lastFiredAt is in the future relative to now (clock skew or tests),
// behave like "not yet due" — return the next future window after lastFiredAt
// with missedCount=0.
//
// Iteration is bounded by elapsed time / cron period. Worst case after a
// 10-year outage on an hourly schedule: ~87,600 iterations — completes in
// tens of milliseconds (T-03-04-06).
func computeNextAndDetectMiss(sched cron.Schedule, lastFiredAt, now time.Time) (time.Time, int) {
	lastFiredAt = lastFiredAt.UTC()
	now = now.UTC()

	// Treat zero / pre-epoch as "never fired" — fire the most recent past
	// window without missed accounting. Walk forward in cron periods from a
	// point well before `now` and stop at the last candidate <= now.
	if lastFiredAt.IsZero() || lastFiredAt.Before(time.Unix(0, 0)) {
		// Seed walk from one year before `now` — covers the largest plausible
		// cron period (yearly @yearly). For any reasonable cron this loop
		// completes in <1s.
		seed := now.AddDate(-1, 0, 0)
		candidate := sched.Next(seed)
		if candidate.After(now) {
			// First candidate already in the future (cron fires less often than
			// once per year, e.g. "0 0 1 1 *" with seed mid-year). Return it as
			// the next future window with no missed accounting.
			return candidate, 0
		}
		for {
			next := sched.Next(candidate)
			if next.After(now) {
				return candidate, 0
			}
			candidate = next
		}
	}

	candidate := sched.Next(lastFiredAt)
	if candidate.After(now) {
		// Not yet due — the next firing is still in the future.
		return candidate, 0
	}
	missed := 0
	for {
		nextCandidate := sched.Next(candidate)
		if nextCandidate.After(now) {
			return candidate, missed
		}
		missed++
		candidate = nextCandidate
	}
}
