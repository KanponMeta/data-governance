package run

// Priority enumerates the legal values of the runs.priority column (D-13).
// The DB-level CHECK constraint in
// migrations/20260508120000_phase3_runs_columns.sql enforces these same
// values; this Go enum provides fast-fail and type safety at the
// application layer (mirrors the State enum / runs.state pattern).
type Priority string

const (
	PriorityCritical Priority = "critical"
	PriorityNormal   Priority = "normal"
	PriorityBackfill Priority = "backfill"
)

// AllPriorities returns every legal value of the runs.priority column in
// canonical order (critical, normal, backfill). Useful for table-driven
// tests, validation, and introspection.
func AllPriorities() []Priority {
	return []Priority{PriorityCritical, PriorityNormal, PriorityBackfill}
}

// PriorityOrder is the SINGLE SOURCE OF TRUTH for the priority integer
// mapping used by ClaimNext's SQL CASE expression in claim.go (Pitfall 5
// — drift prevention).
//
//	critical -> 0  (claimed first)
//	normal   -> 1  (default; FIFO within tier)
//	backfill -> 2  (claimed last)
//
// Unknown / empty values map to 1 (normal) — matches the SQL ELSE 1 branch
// in the CASE expression. This means an unrecognised priority does NOT
// silently jump ahead of normal runs (which would be a privilege escalation),
// nor get stranded behind backfills (which would be a denial of service).
//
// The CASE expression in internal/run/claim.go MUST mirror this mapping
// 1:1; the unit test TestPriorityOrderConsistency catches drift by
// enumerating every Priority constant.
func PriorityOrder(p string) int {
	switch Priority(p) {
	case PriorityCritical:
		return 0
	case PriorityBackfill:
		return 2
	default:
		// PriorityNormal and any unrecognised value.
		return 1
	}
}
