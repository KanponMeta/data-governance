package backfill_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/kanpon/data-governance/internal/backfill"
	"github.com/kanpon/data-governance/internal/partition"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParsePartitionSpec exercises all three formats (date range, comma list,
// single key) against all four strategies. Validation-map row.
func TestParsePartitionSpec(t *testing.T) {
	t.Run("date range daily Jan 2024", func(t *testing.T) {
		spec, err := backfill.ParsePartitionSpec(partition.DailyPartitions{}, "2024-01-01:2024-01-31", backfill.DefaultMaxPartitions)
		require.NoError(t, err)
		assert.Len(t, spec.Keys, 31)
		assert.Equal(t, "2024-01-01", spec.Keys[0])
		assert.Equal(t, "2024-01-31", spec.Keys[30])
		assert.Equal(t, "2024-01-01:2024-01-31", spec.Source)
	})

	t.Run("date range daily 2024 leap year (366 days)", func(t *testing.T) {
		spec, err := backfill.ParsePartitionSpec(partition.DailyPartitions{}, "2024-01-01:2024-12-31", backfill.DefaultMaxPartitions)
		require.NoError(t, err)
		assert.Len(t, spec.Keys, 366)
	})

	t.Run("date range monthly Q1 2024", func(t *testing.T) {
		spec, err := backfill.ParsePartitionSpec(partition.MonthlyPartitions{}, "2024-01-01:2024-03-31", backfill.DefaultMaxPartitions)
		require.NoError(t, err)
		assert.Equal(t, []string{"2024-01", "2024-02", "2024-03"}, spec.Keys)
	})

	t.Run("comma list category us,eu,apac", func(t *testing.T) {
		strat := partition.CategoryPartitions{Keys: []string{"us", "eu", "apac"}}
		spec, err := backfill.ParsePartitionSpec(strat, "us,eu,apac", backfill.DefaultMaxPartitions)
		require.NoError(t, err)
		assert.Equal(t, []string{"us", "eu", "apac"}, spec.Keys)
	})

	t.Run("single key 2024-01-15 daily", func(t *testing.T) {
		spec, err := backfill.ParsePartitionSpec(partition.DailyPartitions{}, "2024-01-15", backfill.DefaultMaxPartitions)
		require.NoError(t, err)
		assert.Equal(t, []string{"2024-01-15"}, spec.Keys)
	})
}

// TestParsePartitionSpecCategoryNotDeclared — comma-list with key not in declared keys.
func TestParsePartitionSpecCategoryNotDeclared(t *testing.T) {
	strat := partition.CategoryPartitions{Keys: []string{"us", "eu"}}
	_, err := backfill.ParsePartitionSpec(strat, "us,eu,apac", backfill.DefaultMaxPartitions)
	require.Error(t, err)
	assert.True(t, errors.Is(err, backfill.ErrCategoryKeyNotDeclared),
		"expected ErrCategoryKeyNotDeclared; got: %v", err)
}

// TestMaxPartitionsGuard — exceeding max-partitions returns ErrTooManyPartitions.
// Validation-map row.
func TestMaxPartitionsGuard(t *testing.T) {
	_, err := backfill.ParsePartitionSpec(partition.DailyPartitions{}, "2024-01-01:2024-12-31", 100)
	require.Error(t, err)
	assert.True(t, errors.Is(err, backfill.ErrTooManyPartitions),
		"expected ErrTooManyPartitions; got: %v", err)
	assert.Contains(t, err.Error(), "366")
	assert.Contains(t, err.Error(), "100")
}

// TestParsePartitionSpecEmpty — empty raw spec returns ErrInvalidSpec.
func TestParsePartitionSpecEmpty(t *testing.T) {
	_, err := backfill.ParsePartitionSpec(partition.DailyPartitions{}, "", backfill.DefaultMaxPartitions)
	require.Error(t, err)
	assert.True(t, errors.Is(err, backfill.ErrInvalidSpec))
}

// TestParsePartitionSpecBadDate — "not-a-date:2024-12-31" returns wrapped ErrInvalidSpec
// containing "start date".
func TestParsePartitionSpecBadDate(t *testing.T) {
	_, err := backfill.ParsePartitionSpec(partition.DailyPartitions{}, "not-a-date:2024-12-31", backfill.DefaultMaxPartitions)
	require.Error(t, err)
	assert.True(t, errors.Is(err, backfill.ErrInvalidSpec))
	assert.Contains(t, strings.ToLower(err.Error()), "start date")
}

// TestParsePartitionSpecCategoryInvalidKey — "us/east" rejected by ValidateCategoryKey.
func TestParsePartitionSpecCategoryInvalidKey(t *testing.T) {
	strat := partition.CategoryPartitions{Keys: []string{"us/east"}}
	_, err := backfill.ParsePartitionSpec(strat, "us/east", backfill.DefaultMaxPartitions)
	require.Error(t, err)
	assert.True(t, errors.Is(err, backfill.ErrInvalidSpec),
		"expected wrapped ErrInvalidSpec; got: %v", err)
}

// TestParsePartitionSpecCommaListWithDailyStrategy — "us,eu" with DailyPartitions:
// each item must parse as daily key; "us" fails.
func TestParsePartitionSpecCommaListWithDailyStrategy(t *testing.T) {
	_, err := backfill.ParsePartitionSpec(partition.DailyPartitions{}, "us,eu", backfill.DefaultMaxPartitions)
	require.Error(t, err)
	assert.True(t, errors.Is(err, backfill.ErrInvalidSpec))
}

// TestParsePartitionSpecInvertedRange — end before start propagates from KeysBetween.
func TestParsePartitionSpecInvertedRange(t *testing.T) {
	_, err := backfill.ParsePartitionSpec(partition.DailyPartitions{}, "2024-01-01:2023-12-31", backfill.DefaultMaxPartitions)
	require.Error(t, err)
}
