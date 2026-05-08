package schedule

import (
	"testing"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMissedWindowLatestOnly exercises the LatestOnly missed-window logic
// (D-04). When several cron windows have elapsed since the last fire, the
// scheduler must fire only the most recent window and report the count of
// skipped windows in the second return value.
func TestMissedWindowLatestOnly(t *testing.T) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	hourly, err := parser.Parse("0 * * * *")
	require.NoError(t, err)

	// Case 1: lastFiredAt = 2026-01-01 00:00:00 UTC, now = 2026-01-01 03:30:00 UTC.
	// Windows 01:00, 02:00, 03:00 are all <= now. The scheduler should fire 03:00
	// and report 2 skipped windows (01:00 and 02:00).
	lastFired := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 1, 1, 3, 30, 0, 0, time.UTC)
	wnd, missed := computeNextAndDetectMiss(hourly, lastFired, now)
	assert.Equal(t, time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC), wnd, "should fire the most recent past window (03:00)")
	assert.Equal(t, 2, missed, "two windows skipped (01:00, 02:00)")

	// Case 2: lastFiredAt = zero time → never fired. Return the most-recent past
	// window with missed=0 (no avalanche of "missed" events at first registration).
	wnd, missed = computeNextAndDetectMiss(hourly, time.Time{}, now)
	assert.Equal(t, 0, missed, "zero lastFiredAt must report 0 skipped windows")
	assert.False(t, wnd.IsZero(), "must return a concrete window when lastFiredAt is zero")
	assert.False(t, wnd.After(now), "returned window must not be in the future")

	// Case 3: lastFiredAt = now-30s on 1-hour schedule → not yet due. The function
	// must return the *next future* window with missed=0.
	notDue := now.Add(-30 * time.Second)
	wnd, missed = computeNextAndDetectMiss(hourly, notDue, now)
	assert.Equal(t, 0, missed, "no missed windows when not yet due")
	assert.True(t, wnd.After(now), "returned window must be in the future when not yet due")

	// Case 4: lastFiredAt = now (just fired) → next future window, missed=0.
	wnd, missed = computeNextAndDetectMiss(hourly, now, now)
	assert.Equal(t, 0, missed)
	assert.True(t, wnd.After(now))
}
