package run_test

import (
	"testing"

	"github.com/kanpon/data-governance/internal/run"
	"github.com/stretchr/testify/assert"
)

// TestPriorityOrderConsistency is the drift-prevention test for Pitfall 5.
// It enumerates every Priority constant via AllPriorities() and asserts
// PriorityOrder returns the expected integer for each. It also asserts
// the empty / unrecognised input default-to-normal contract (matches the
// SQL ELSE 1 branch in claim.go's CASE expression).
func TestPriorityOrderConsistency(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{string(run.PriorityCritical), 0},
		{string(run.PriorityNormal), 1},
		{string(run.PriorityBackfill), 2},
		{"", 1},     // empty defaults to normal
		{"foo", 1},  // unrecognised defaults to normal — matches SQL ELSE 1
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, run.PriorityOrder(c.in),
			"PriorityOrder(%q) want %d", c.in, c.want)
	}
}

// TestPriorityOrderingMonotonic asserts the canonical order
// critical < normal < backfill is preserved by PriorityOrder.
// If a future developer reshuffles the integer mapping, this fails loudly.
func TestPriorityOrderingMonotonic(t *testing.T) {
	c := run.PriorityOrder(string(run.PriorityCritical))
	n := run.PriorityOrder(string(run.PriorityNormal))
	b := run.PriorityOrder(string(run.PriorityBackfill))
	assert.Less(t, c, n, "critical must order before normal")
	assert.Less(t, n, b, "normal must order before backfill")
}

// TestAllPrioritiesIsSorted asserts AllPriorities() returns the canonical
// three values in the expected order [critical, normal, backfill].
func TestAllPrioritiesIsSorted(t *testing.T) {
	got := run.AllPriorities()
	assert.Equal(t, []run.Priority{
		run.PriorityCritical,
		run.PriorityNormal,
		run.PriorityBackfill,
	}, got)
}
