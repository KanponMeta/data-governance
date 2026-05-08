package run

import (
	"errors"
	"fmt"
)

// ErrIllegalTransition is returned when Transition or TransitionForReset is called
// with a state edge that the FSM does not permit.
var ErrIllegalTransition = errors.New("run: illegal state transition")

// legalTransitions enumerates the allowed forward edges of the run lifecycle.
// Reverse transitions (e.g. running → queued) are forbidden in the normal worker
// path. Crash-recovery (plan 02-04 reaper) uses TransitionForReset which permits
// {starting,running} → queued, separately gated.
//
// Terminal states (succeeded, failed, canceled) have empty successor maps —
// any outgoing edge from them is an error.
var legalTransitions = map[State]map[State]struct{}{
	StateQueued:   {StateStarting: {}, StateCanceled: {}},
	StateStarting: {StateRunning: {}, StateFailed: {}, StateCanceled: {}},
	StateRunning:  {StateSucceeded: {}, StateFailed: {}, StateCanceled: {}},
	// Terminal states — no outgoing edges.
	StateSucceeded: {},
	StateFailed:    {},
	StateCanceled:  {},
}

// resetTransitions enumerates the additional edges the stale-run reaper is allowed
// to take (plan 02-04). Splitting these out of legalTransitions keeps the normal
// FSM closed against accidental backward transitions while giving the reaper a
// documented escape hatch.
//
// Plan 02-04's reaper MUST call TransitionForReset (not Transition) to reset
// crashed runs; this naming makes the intent explicit (T-02-02-08 mitigation).
var resetTransitions = map[State]map[State]struct{}{
	StateStarting: {StateQueued: {}},
	StateRunning:  {StateQueued: {}},
}

// Transition validates that going from `from` to `to` is permitted by the FSM.
// The DB-level CHECK constraint guards the value space; this guards the transition graph.
//
// Returns ErrIllegalTransition for any edge that is:
//   - A backward transition (e.g. running → queued)
//   - A self-loop (e.g. queued → queued)
//   - An outgoing edge from a terminal state
//   - Any edge not in legalTransitions
func Transition(from, to State) error {
	successors, ok := legalTransitions[from]
	if !ok {
		return fmt.Errorf("%w: unknown from-state %q", ErrIllegalTransition, from)
	}
	if _, ok := successors[to]; !ok {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}
	return nil
}

// TransitionForReset validates the reaper's crash-recovery transitions
// (starting/running → queued). Use ONLY from the stale-run reaper in plan 02-04.
// Returns ErrIllegalTransition for any edge not in resetTransitions.
//
// The separation from Transition() prevents accidental backward transitions in
// the normal worker FSM path (T-02-02-08 mitigation).
func TransitionForReset(from, to State) error {
	successors, ok := resetTransitions[from]
	if !ok {
		return fmt.Errorf("%w: reset not allowed from %q", ErrIllegalTransition, from)
	}
	if _, ok := successors[to]; !ok {
		return fmt.Errorf("%w: reset %s -> %s", ErrIllegalTransition, from, to)
	}
	return nil
}

// IsTerminal reports whether the state has no outgoing edges in the normal FSM.
// Terminal states are succeeded, failed, and canceled.
func IsTerminal(s State) bool {
	switch s {
	case StateSucceeded, StateFailed, StateCanceled:
		return true
	default:
		return false
	}
}
