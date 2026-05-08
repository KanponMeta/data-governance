// Package backfill implements the backfill submission service (D-14, D-15, D-16).
//
// It provides three pieces:
//   - ParsePartitionSpec: parses --partitions strings (date range, comma list,
//     single key) against an asset's PartitionStrategy with a max-partitions
//     guard (Pitfall 6).
//   - Submit: mass-enqueues N runs + one backfills row in a single tx with an
//     idempotent ON CONFLICT DO NOTHING that matches the partial unique index
//     from plan 03-01.
//   - GetStatus: aggregates run state counts for a backfill_id.
package backfill

import (
	"fmt"
	"strings"
	"time"

	"github.com/kanpon/data-governance/internal/partition"
)

// DefaultMaxPartitions caps the number of runs created by a single backfill
// submission (Pitfall 6 mitigation). 3650 = 10 years of daily partitions.
// Operators may override via --max-partitions=N at the CLI.
const DefaultMaxPartitions = 3650

// ErrTooManyPartitions is returned when ParsePartitionSpec produces more keys
// than allowed by maxPartitions.
var ErrTooManyPartitions = fmt.Errorf("backfill: too many partitions (max-partitions limit)")

// ErrInvalidSpec is returned for malformed --partitions strings.
var ErrInvalidSpec = fmt.Errorf("backfill: invalid --partitions spec")

// ErrCategoryKeyNotDeclared is returned when a comma-list / single-key spec
// references a key not in CategoryPartitions.Keys.
var ErrCategoryKeyNotDeclared = fmt.Errorf("backfill: category key not declared in asset's CategoryPartitions")

// Spec is the parsed result of --partitions; carries the resolved keys + raw
// user-supplied source for audit (stored verbatim in backfills.partition_spec).
type Spec struct {
	Keys     []string
	Priority string // "critical" | "normal" | "backfill" — caller assigns
	Source   string // raw input — stored in backfills.partition_spec
}

// ParsePartitionSpec parses --partitions input against the asset's
// PartitionStrategy. The three input formats per D-14 are:
//
//  1. Date range:  "2024-01-01:2024-12-31"   → expand via partition.KeysBetween
//  2. Comma list:  "us,eu,apac"              → trim each, validate per-strategy
//  3. Single key:  "2024-01-15" or "us"      → single-element list
//
// maxPartitions caps the resulting Keys length (Pitfall 6). Pass
// DefaultMaxPartitions if you have no operator override.
func ParsePartitionSpec(strategy partition.PartitionStrategy, raw string, maxPartitions int) (Spec, error) {
	source := raw
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Spec{}, fmt.Errorf("%w: empty spec", ErrInvalidSpec)
	}
	if maxPartitions <= 0 {
		maxPartitions = DefaultMaxPartitions
	}

	var (
		keys []string
		err  error
	)
	switch {
	case strings.Contains(raw, ":"):
		keys, err = parseDateRange(strategy, raw)
	case strings.Contains(raw, ","):
		keys, err = parseCommaList(strategy, raw)
	default:
		keys, err = parseSingleKey(strategy, raw)
	}
	if err != nil {
		return Spec{}, err
	}
	if len(keys) > maxPartitions {
		return Spec{}, fmt.Errorf("%w: %d > %d", ErrTooManyPartitions, len(keys), maxPartitions)
	}
	return Spec{Keys: keys, Source: source}, nil
}

// parseDateRange handles the "START:END" format. Both halves must be
// YYYY-MM-DD. Range expansion is delegated to partition.KeysBetween, so
// inverted ranges (end < start) and unsupported strategies (CategoryPartitions)
// surface their existing errors.
func parseDateRange(strategy partition.PartitionStrategy, raw string) ([]string, error) {
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("%w: date range must be START:END", ErrInvalidSpec)
	}
	start, err := time.Parse("2006-01-02", strings.TrimSpace(parts[0]))
	if err != nil {
		return nil, fmt.Errorf("%w: start date %q: %v", ErrInvalidSpec, parts[0], err)
	}
	end, err := time.Parse("2006-01-02", strings.TrimSpace(parts[1]))
	if err != nil {
		return nil, fmt.Errorf("%w: end date %q: %v", ErrInvalidSpec, parts[1], err)
	}
	return partition.KeysBetween(strategy, start, end)
}

// parseCommaList handles "us,eu,apac" (or "2024-01-01,2024-01-02"). Each
// trimmed item is validated against the strategy.
func parseCommaList(strategy partition.PartitionStrategy, raw string) ([]string, error) {
	pieces := strings.Split(raw, ",")
	keys := make([]string, 0, len(pieces))
	for _, p := range pieces {
		k := strings.TrimSpace(p)
		if k == "" {
			continue
		}
		if err := validateKeyForStrategy(strategy, k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: comma list yielded no keys", ErrInvalidSpec)
	}
	return keys, nil
}

// parseSingleKey handles a single trimmed token validated against the strategy.
func parseSingleKey(strategy partition.PartitionStrategy, raw string) ([]string, error) {
	if err := validateKeyForStrategy(strategy, raw); err != nil {
		return nil, err
	}
	return []string{raw}, nil
}

// validateKeyForStrategy ensures a key conforms to the asset's PartitionStrategy.
// Time-based keys (Daily/Weekly/Monthly) are validated via time.Parse format
// strings; CategoryPartitions keys are validated via partition.ValidateCategoryKey
// (Pitfall 4) AND must be present in the strategy's declared key list.
func validateKeyForStrategy(strategy partition.PartitionStrategy, key string) error {
	if strategy == nil {
		return fmt.Errorf("%w: asset has no PartitionStrategy", ErrInvalidSpec)
	}
	switch s := strategy.(type) {
	case partition.DailyPartitions:
		if _, err := time.Parse("2006-01-02", key); err != nil {
			return fmt.Errorf("%w: %q is not a daily key (YYYY-MM-DD)", ErrInvalidSpec, key)
		}
	case partition.WeeklyPartitions:
		// Format YYYY-Wnn — simple structural check; ISO week parsing is
		// non-trivial and the scheduler emits this format directly so a
		// loose match is sufficient for v1.
		if len(key) < 7 || key[4] != '-' || key[5] != 'W' {
			return fmt.Errorf("%w: %q is not a weekly key (YYYY-Wnn)", ErrInvalidSpec, key)
		}
	case partition.MonthlyPartitions:
		if _, err := time.Parse("2006-01", key); err != nil {
			return fmt.Errorf("%w: %q is not a monthly key (YYYY-MM)", ErrInvalidSpec, key)
		}
	case partition.CategoryPartitions:
		if err := partition.ValidateCategoryKey(key); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidSpec, err)
		}
		// Also: key must be in declared list.
		found := false
		for _, declared := range s.Keys {
			if declared == key {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: %q (declared: %v)", ErrCategoryKeyNotDeclared, key, s.Keys)
		}
	default:
		return fmt.Errorf("%w: unsupported strategy %T", ErrInvalidSpec, strategy)
	}
	return nil
}
