package partition

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPartitionKeyGen — Daily/Weekly/Monthly produces correct UTC strings (D-11).
func TestPartitionKeyGen(t *testing.T) {
	ts := time.Date(2024, 1, 15, 12, 30, 45, 0, time.UTC) // Mon Jan 15 2024 — ISO week 3
	require.Equal(t, "2024-01-15", DailyKey(ts))
	require.Equal(t, "2024-W03", WeeklyKey(ts))
	require.Equal(t, "2024-01", MonthlyKey(ts))
}

// TestWeeklyKeyYearBoundary — ISO 8601 year-boundary edge cases (Pattern 6 verification).
func TestWeeklyKeyYearBoundary(t *testing.T) {
	require.Equal(t, "2020-W01", WeeklyKey(time.Date(2019, 12, 30, 0, 0, 0, 0, time.UTC)))
	require.Equal(t, "2015-W01", WeeklyKey(time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC)))
	require.Equal(t, "2015-W53", WeeklyKey(time.Date(2015, 12, 31, 0, 0, 0, 0, time.UTC)))
}

// TestKeysBetween — sub-tests for daily / weekly / monthly / category-rejection.
func TestKeysBetween(t *testing.T) {
	startJan := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	endJan := time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)

	t.Run("daily-31-keys", func(t *testing.T) {
		keys, err := KeysBetween(DailyPartitions{}, startJan, endJan)
		require.NoError(t, err)
		require.Len(t, keys, 31)
		require.Equal(t, "2024-01-01", keys[0])
		require.Equal(t, "2024-01-31", keys[30])
	})

	t.Run("weekly-5-keys-Jan-2024", func(t *testing.T) {
		// Jan 2024: weeks W01 (Jan 1–7), W02, W03, W04, W05 (last day Feb 4) — 5 keys total.
		keys, err := KeysBetween(WeeklyPartitions{}, startJan, endJan)
		require.NoError(t, err)
		require.Equal(t, []string{"2024-W01", "2024-W02", "2024-W03", "2024-W04", "2024-W05"}, keys)
	})

	t.Run("monthly-Q1-2024", func(t *testing.T) {
		end := time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC)
		keys, err := KeysBetween(MonthlyPartitions{}, startJan, end)
		require.NoError(t, err)
		require.Equal(t, []string{"2024-01", "2024-02", "2024-03"}, keys)
	})

	t.Run("category-unsupported", func(t *testing.T) {
		_, err := KeysBetween(CategoryPartitions{Keys: []string{"us", "eu"}}, startJan, endJan)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrUnsupportedRangeStrategy),
			"expected ErrUnsupportedRangeStrategy, got: %v", err)
	})
}

// TestKeysBetweenInvertedRange — start > end is rejected.
func TestKeysBetweenInvertedRange(t *testing.T) {
	start := time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := KeysBetween(DailyPartitions{}, start, end)
	require.Error(t, err)
}

// TestCurrentDailyKey — D-12 default offset 24h aligns with Dagster "previous-window" convention.
func TestCurrentDailyKey(t *testing.T) {
	now := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	require.Equal(t, "2024-01-14", CurrentDailyKey(now, 24*time.Hour))
}

// TestValidateCategoryKey — Pitfall 4 enforcement: empty / >128 chars / contains '/' rejected.
func TestValidateCategoryKey(t *testing.T) {
	require.NoError(t, ValidateCategoryKey("us"))
	require.NoError(t, ValidateCategoryKey("eu-west-1"))

	cases := []string{"", strings.Repeat("x", 129), "us/east"}
	for _, k := range cases {
		err := ValidateCategoryKey(k)
		require.Error(t, err, "expected error for key %q", k)
		require.True(t, errors.Is(err, ErrInvalidCategoryKey),
			"expected ErrInvalidCategoryKey for %q, got: %v", k, err)
	}
}

// TestNonUTCInputProducesUTCKey — D-11 keys always encode the UTC window even when input is in another zone.
func TestNonUTCInputProducesUTCKey(t *testing.T) {
	// Jan 15 02:00 EST = Jan 15 07:00 UTC → "2024-01-15"
	est := time.FixedZone("EST", -5*3600)
	require.Equal(t, "2024-01-15",
		DailyKey(time.Date(2024, 1, 15, 2, 0, 0, 0, est)))

	// Jan 15 01:00 CST (UTC+8) = Jan 14 17:00 UTC → "2024-01-14"
	cst := time.FixedZone("ChinaWest", 8*3600)
	require.Equal(t, "2024-01-14",
		DailyKey(time.Date(2024, 1, 15, 1, 0, 0, 0, cst)))
}
