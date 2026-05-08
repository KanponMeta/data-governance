package run_test

import (
	"errors"
	"testing"

	"github.com/kanpon/data-governance/internal/run"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTransitionForwardEdges verifies that all valid forward transitions are permitted.
func TestTransitionForwardEdges(t *testing.T) {
	tests := []struct {
		from, to run.State
	}{
		{run.StateQueued, run.StateStarting},
		{run.StateQueued, run.StateCanceled},
		{run.StateStarting, run.StateRunning},
		{run.StateStarting, run.StateFailed},
		{run.StateStarting, run.StateCanceled},
		{run.StateRunning, run.StateSucceeded},
		{run.StateRunning, run.StateFailed},
		{run.StateRunning, run.StateCanceled},
	}
	for _, tt := range tests {
		t.Run(string(tt.from)+"->"+string(tt.to), func(t *testing.T) {
			err := run.Transition(tt.from, tt.to)
			require.NoError(t, err, "expected forward transition to be permitted")
		})
	}
}

// TestTransitionIllegalBackwardEdges verifies that backward transitions are rejected.
func TestTransitionIllegalBackwardEdges(t *testing.T) {
	tests := []struct {
		from, to run.State
		desc     string
	}{
		{run.StateRunning, run.StateQueued, "running->queued is backward"},
		{run.StateSucceeded, run.StateRunning, "succeeded->running is backward (terminal)"},
		{run.StateQueued, run.StateQueued, "no-op self-loop is forbidden"},
		{run.StateStarting, run.StateQueued, "starting->queued requires TransitionForReset"},
		{run.StateFailed, run.StateQueued, "failed->queued is a backward edge"},
		{run.StateCanceled, run.StateQueued, "canceled->queued is a backward edge"},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			err := run.Transition(tt.from, tt.to)
			require.Error(t, err, "expected illegal transition to return error")
			assert.True(t, errors.Is(err, run.ErrIllegalTransition),
				"error must wrap ErrIllegalTransition; got: %v", err)
		})
	}
}

// TestTransitionTerminalStates verifies that terminal states have no outgoing edges.
func TestTransitionTerminalStates(t *testing.T) {
	terminals := []run.State{run.StateSucceeded, run.StateFailed, run.StateCanceled}
	targets := []run.State{run.StateQueued, run.StateStarting, run.StateRunning,
		run.StateSucceeded, run.StateFailed, run.StateCanceled}

	for _, term := range terminals {
		for _, target := range targets {
			t.Run(string(term)+"->"+string(target), func(t *testing.T) {
				err := run.Transition(term, target)
				require.Error(t, err, "terminal state %q should reject all transitions", term)
				assert.True(t, errors.Is(err, run.ErrIllegalTransition))
			})
		}
	}
}

// TestTransitionForReset verifies the reaper's crash-recovery edges are permitted
// ONLY via TransitionForReset, and that normal Transition still rejects them.
func TestTransitionForReset(t *testing.T) {
	t.Run("starting->queued is permitted via TransitionForReset", func(t *testing.T) {
		err := run.TransitionForReset(run.StateStarting, run.StateQueued)
		require.NoError(t, err)
	})
	t.Run("running->queued is permitted via TransitionForReset", func(t *testing.T) {
		err := run.TransitionForReset(run.StateRunning, run.StateQueued)
		require.NoError(t, err)
	})
	t.Run("starting->queued is rejected by normal Transition", func(t *testing.T) {
		err := run.Transition(run.StateStarting, run.StateQueued)
		require.Error(t, err)
		assert.True(t, errors.Is(err, run.ErrIllegalTransition))
	})
	t.Run("running->queued is rejected by normal Transition", func(t *testing.T) {
		err := run.Transition(run.StateRunning, run.StateQueued)
		require.Error(t, err)
		assert.True(t, errors.Is(err, run.ErrIllegalTransition))
	})
	t.Run("queued->queued is rejected by TransitionForReset", func(t *testing.T) {
		err := run.TransitionForReset(run.StateQueued, run.StateQueued)
		require.Error(t, err)
		assert.True(t, errors.Is(err, run.ErrIllegalTransition))
	})
	t.Run("succeeded->queued is rejected by TransitionForReset (only starting/running allowed)", func(t *testing.T) {
		err := run.TransitionForReset(run.StateSucceeded, run.StateQueued)
		require.Error(t, err)
		assert.True(t, errors.Is(err, run.ErrIllegalTransition))
	})
}

// TestIsTerminal verifies that IsTerminal correctly identifies terminal states.
func TestIsTerminal(t *testing.T) {
	terminals := []run.State{run.StateSucceeded, run.StateFailed, run.StateCanceled}
	for _, s := range terminals {
		assert.True(t, run.IsTerminal(s), "expected %q to be terminal", s)
	}
	nonTerminals := []run.State{run.StateQueued, run.StateStarting, run.StateRunning}
	for _, s := range nonTerminals {
		assert.False(t, run.IsTerminal(s), "expected %q to be non-terminal", s)
	}
}

// TestAllStates verifies that AllStates returns all six expected constants.
func TestAllStates(t *testing.T) {
	all := run.AllStates()
	require.Len(t, all, 6, "expected exactly 6 states")
	stateSet := make(map[run.State]struct{}, 6)
	for _, s := range all {
		stateSet[s] = struct{}{}
	}
	for _, expected := range []run.State{
		run.StateQueued, run.StateStarting, run.StateRunning,
		run.StateSucceeded, run.StateFailed, run.StateCanceled,
	} {
		_, ok := stateSet[expected]
		assert.True(t, ok, "AllStates missing %q", expected)
	}
}
