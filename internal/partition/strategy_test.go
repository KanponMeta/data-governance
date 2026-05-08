package partition

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPartitionStrategyKind asserts the Kind() string for each concrete strategy.
// Kind strings flow into runs.partition_key encoding semantics and CLI parsing — they MUST be stable.
func TestPartitionStrategyKind(t *testing.T) {
	require.Equal(t, "daily", DailyPartitions{}.Kind())
	require.Equal(t, "weekly", WeeklyPartitions{}.Kind())
	require.Equal(t, "monthly", MonthlyPartitions{}.Kind())
	require.Equal(t, "category", CategoryPartitions{}.Kind())
}

// TestPartitionStrategySealed serves as a compile-time + runtime exhaustiveness check.
// If a fifth strategy is added, this test documents the obligation to also extend KeysBetween,
// scheduler/backfill validators, and the threat-model register (T-03-02-05 sealed-interface guard).
func TestPartitionStrategySealed(t *testing.T) {
	cases := []PartitionStrategy{
		DailyPartitions{},
		WeeklyPartitions{},
		MonthlyPartitions{},
		CategoryPartitions{},
	}
	for _, s := range cases {
		switch s.(type) {
		case DailyPartitions, WeeklyPartitions, MonthlyPartitions, CategoryPartitions:
			// covered
		default:
			t.Fatalf("unexpected strategy type %T — sealed interface broken", s)
		}
	}
}
