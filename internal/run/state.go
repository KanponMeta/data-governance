// Package run provides the run lifecycle state machine, atomic claim mechanism,
// and heartbeat support for the platform's execution engine.
package run

// State enumerates the legal values of the runs.state column.
// The DB-level CHECK constraint in migrations/20260507120000_phase2_run_tables.sql
// enforces these same values; this Go enum provides fast-fail and type safety
// at the application layer.
type State string

const (
	StateQueued    State = "queued"
	StateStarting  State = "starting"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCanceled  State = "canceled"
)

// AllStates lists every legal value of the runs.state column.
// Useful for validation and introspection.
func AllStates() []State {
	return []State{StateQueued, StateStarting, StateRunning, StateSucceeded, StateFailed, StateCanceled}
}
