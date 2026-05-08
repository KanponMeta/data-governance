package partition

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrUnsupportedRangeStrategy is returned by KeysBetween when called with a
// strategy that does not support range expansion (e.g. CategoryPartitions —
// the CLI uses a comma list at parse layer instead).
var ErrUnsupportedRangeStrategy = errors.New("partition: KeysBetween only supports time-based strategies")

// ErrInvalidCategoryKey is returned by ValidateCategoryKey when a category
// key is empty, exceeds 128 chars, or contains '/' (Pitfall 4 — encoding
// ambiguity / path-injection).
var ErrInvalidCategoryKey = errors.New("partition: category key invalid (empty | >128 chars | contains '/')")

// DailyKey returns the UTC date of the day containing t — e.g. "2024-01-15" (D-11).
func DailyKey(t time.Time) string { return t.UTC().Format("2006-01-02") }

// MonthlyKey returns the UTC year-month of the month containing t — e.g. "2024-01" (D-11).
func MonthlyKey(t time.Time) string { return t.UTC().Format("2006-01") }

// WeeklyKey returns the ISO 8601 week key for the week containing t — e.g.
// "2024-W03". Year-boundary cases (2019-12-30 → "2020-W01") and 53-week years
// (2015-W53) are handled by Go stdlib time.Time.ISOWeek().
func WeeklyKey(t time.Time) string {
	year, week := t.UTC().ISOWeek()
	return fmt.Sprintf("%d-W%02d", year, week)
}

// CurrentDailyKey returns the daily key for the partition window (now - offset).
// Default offset 24h aligns with Dagster's "cron fires for the preceding window"
// convention — a daily cron firing at midnight enqueues yesterday's partition.
func CurrentDailyKey(now time.Time, offset time.Duration) string {
	return DailyKey(now.Add(-offset))
}

// ValidateCategoryKey enforces Pitfall 4 — non-empty, ≤128 chars, no '/'.
// Returns ErrInvalidCategoryKey wrapped with the offending key for diagnostics.
func ValidateCategoryKey(key string) error {
	if key == "" || len(key) > 128 || strings.Contains(key, "/") {
		return fmt.Errorf("%w: %q", ErrInvalidCategoryKey, key)
	}
	return nil
}

// KeysBetween generates all partition keys (inclusive) for a time-based
// strategy between start and end (UTC). For CategoryPartitions this returns
// ErrUnsupportedRangeStrategy because the CLI parses comma-list specs at a
// higher layer (D-14, Pattern 8).
//
// start and end are both truncated to start-of-day UTC. An inverted range
// (end < start) returns an explanatory error.
func KeysBetween(strategy PartitionStrategy, start, end time.Time) ([]string, error) {
	startUTC := start.UTC().Truncate(24 * time.Hour)
	endUTC := end.UTC().Truncate(24 * time.Hour)
	if endUTC.Before(startUTC) {
		return nil, fmt.Errorf("partition: KeysBetween: end %s is before start %s", endUTC, startUTC)
	}
	switch strategy.(type) {
	case DailyPartitions:
		days := int(endUTC.Sub(startUTC).Hours()/24) + 1
		keys := make([]string, 0, days)
		for cur := startUTC; !cur.After(endUTC); cur = cur.AddDate(0, 0, 1) {
			keys = append(keys, DailyKey(cur))
		}
		return keys, nil
	case WeeklyPartitions:
		weekStart := isoWeekStart(startUTC)
		var keys []string
		for ; !weekStart.After(endUTC); weekStart = weekStart.AddDate(0, 0, 7) {
			keys = append(keys, WeeklyKey(weekStart))
		}
		return keys, nil
	case MonthlyPartitions:
		cur := time.Date(startUTC.Year(), startUTC.Month(), 1, 0, 0, 0, 0, time.UTC)
		endMonth := time.Date(endUTC.Year(), endUTC.Month(), 1, 0, 0, 0, 0, time.UTC)
		var keys []string
		for ; !cur.After(endMonth); cur = cur.AddDate(0, 1, 0) {
			keys = append(keys, MonthlyKey(cur))
		}
		return keys, nil
	default:
		return nil, fmt.Errorf("%w: strategy=%s", ErrUnsupportedRangeStrategy, strategy.Kind())
	}
}

// isoWeekStart returns the Monday (UTC) starting the ISO week containing t.
// time.Sunday (Go's Weekday=0) maps to ISO 7; the function returns the date
// truncated to start-of-day UTC.
func isoWeekStart(t time.Time) time.Time {
	u := t.UTC()
	weekday := u.Weekday()
	if weekday == time.Sunday {
		weekday = 7 // ISO: Sun=7
	}
	daysFromMonday := int(weekday) - 1
	return u.AddDate(0, 0, -daysFromMonday).Truncate(24 * time.Hour)
}
