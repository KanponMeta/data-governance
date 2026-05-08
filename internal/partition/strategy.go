// Package partition defines partition strategies and partition-key generation
// for Phase 3 of the data-governance platform (D-09, D-11).
//
// A partition strategy describes how an asset's runs are sliced. Each
// concrete strategy implements the sealed PartitionStrategy interface; new
// strategies require explicit support in KeysBetween, the scheduler/backfill
// validators, and the threat-model register (T-03-02-05).
//
// Time-based keys (DailyKey/WeeklyKey/MonthlyKey) always encode the UTC
// start-of-window per D-11; the optional TZ field on a strategy spec affects
// only cron alignment and display, not the persisted partition_key value.
//
// Weekly partitioning uses ISO 8601 weeks (Mon-Sun) — Go stdlib
// time.Time.ISOWeek() implements RFC 5545 / ISO 8601 correctly, including
// year-boundary edge cases such as 2019-12-30 → "2020-W01" and 53-week years
// such as 2015-W53.
package partition

import "time"

// PartitionStrategy is a sealed interface — only types declared in this
// package implement it via the unexported isPartitionStrategy() method.
// New strategies require explicit support in KeysBetween and the
// scheduler/backfill validators (D-09, T-03-02-05).
type PartitionStrategy interface {
	isPartitionStrategy()
	// Kind returns a stable string identifier — "daily" | "weekly" | "monthly" | "category".
	Kind() string
}

// DailyPartitions: one partition per UTC calendar day starting at Start (D-09 + D-11).
// TZ is optional ("UTC" default); it affects cron alignment only — partition keys are UTC.
type DailyPartitions struct {
	Start time.Time
	TZ    string
}

func (DailyPartitions) isPartitionStrategy() {}
func (DailyPartitions) Kind() string         { return "daily" }

// WeeklyPartitions: one partition per ISO 8601 week (Mon-Sun) starting Monday
// of the week containing Start. Keys formatted as "YYYY-Www" (D-11).
type WeeklyPartitions struct {
	Start time.Time
	TZ    string
}

func (WeeklyPartitions) isPartitionStrategy() {}
func (WeeklyPartitions) Kind() string         { return "weekly" }

// MonthlyPartitions: one partition per UTC calendar month starting from the
// month of Start. Keys formatted as "YYYY-MM" (D-11).
type MonthlyPartitions struct {
	Start time.Time
	TZ    string
}

func (MonthlyPartitions) isPartitionStrategy() {}
func (MonthlyPartitions) Kind() string         { return "monthly" }

// CategoryPartitions: one partition per user-supplied static category key (D-09).
// Keys must be non-empty, ≤128 chars, and not contain '/' — enforced by
// ValidateCategoryKey at builder time (Pitfall 4 mitigation).
type CategoryPartitions struct {
	Keys []string
}

func (CategoryPartitions) isPartitionStrategy() {}
func (CategoryPartitions) Kind() string         { return "category" }
