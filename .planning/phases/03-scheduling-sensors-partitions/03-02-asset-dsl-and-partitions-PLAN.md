---
phase: 3
plan: 02
title: Asset DSL extensions (.Schedule/.Sensor/.Partitions) + AssetIO.PartitionKey + partition keygen
type: execute
wave: 1
depends_on: []
requirements: [ORCH-05, ORCH-06, ORCH-07, ORCH-08]
decisions_implemented: [D-03, D-06, D-09, D-11, D-12]
files_modified:
  - internal/asset/asset.go
  - internal/asset/builder.go
  - internal/asset/builder_test.go
  - internal/asset/io.go
  - internal/asset/io_test.go
  - internal/partition/strategy.go
  - internal/partition/strategy_test.go
  - internal/partition/keygen.go
  - internal/partition/keygen_test.go
  - go.mod
  - go.sum
autonomous: true
must_haves:
  truths:
    - ".Schedule(cronExpr) returns *Builder, validates cronExpr via robfig/cron/v3 parser at Build()/Register() time, rejects invalid expressions with a wrapped error"
    - ".Sensor(SensorSpec{...}) returns *Builder and accumulates SensorSpec in the asset"
    - ".Partitions(strategy) returns *Builder, accepts a value implementing partition.PartitionStrategy"
    - "Method order is irrelevant — .Schedule.Sensor.Partitions composes orthogonally per D-12"
    - "AssetIO has PartitionKey() string method that returns the partition for the current run (\"\" if non-partitioned)"
    - "DailyKey(t)/WeeklyKey(t)/MonthlyKey(t) produce UTC ISO-8601 strings: 2024-01-15, 2024-W03, 2024-01"
    - "WeeklyKey for time.Date(2019,12,30,...) returns \"2020-W01\" (ISO week year-boundary)"
    - "KeysBetween(DailyPartitions{}, 2024-01-01, 2024-01-31) returns 31 keys"
    - "Category partition key with len > 128 or containing '/' is rejected at builder time"
  artifacts:
    - path: "internal/asset/builder.go"
      provides: "Schedule, Sensor, Partitions chained builder methods"
      contains: "func (b *Builder) Schedule"
    - path: "internal/asset/asset.go"
      provides: "ScheduleSpec, SensorSpec, SensorResult, SensorFunc types + Asset accessor methods"
      contains: "type SensorSpec struct"
    - path: "internal/asset/io.go"
      provides: "AssetIO.PartitionKey() string + assetIO.partitionKey field + NewAssetIO accepts partitionKey"
      contains: "PartitionKey() string"
    - path: "internal/partition/strategy.go"
      provides: "PartitionStrategy interface + Daily/Weekly/Monthly/Category structs"
      contains: "type PartitionStrategy interface"
    - path: "internal/partition/keygen.go"
      provides: "DailyKey, WeeklyKey, MonthlyKey, KeysBetween, CurrentDailyKey functions"
      contains: "func WeeklyKey"
  key_links:
    - from: "internal/asset/builder.go .Schedule(expr)"
      to: "github.com/robfig/cron/v3 NewParser"
      via: "validate expr at Build()/Register() time using cron.NewParser(...).Parse(expr)"
      pattern: "cron.NewParser"
    - from: "internal/asset/io.go AssetIO interface"
      to: "internal/asset/io.go assetIO struct partitionKey field"
      via: "NewAssetIO(self, resolver, partitionKey) constructor passes partitionKey through"
      pattern: "func NewAssetIO"
    - from: "internal/partition/keygen.go WeeklyKey"
      to: "Go stdlib time.ISOWeek"
      via: "year, week := t.UTC().ISOWeek(); return fmt.Sprintf(\"%d-W%02d\", year, week)"
      pattern: "ISOWeek"
---

<objective>
Add the user-facing SDK surface for Phase 3: chained builder methods `.Schedule(cron)`, `.Sensor(spec)`, `.Partitions(strategy)`; the `SensorSpec`/`SensorResult`/`SensorFunc` types; the `partition.PartitionStrategy` interface plus four concrete strategies (`DailyPartitions`, `WeeklyPartitions`, `MonthlyPartitions`, `CategoryPartitions`); the partition-key generation algorithms (`DailyKey`, `WeeklyKey`, `MonthlyKey`, `KeysBetween`, `CurrentDailyKey`); the `AssetIO.PartitionKey() string` accessor.

This plan is the **stable public API** that all downstream Phase 3 plans (scheduler tick loop, sensor evaluator, backfill CLI) consume. It introduces `github.com/robfig/cron/v3` as a new go.mod dependency, used parser-only per D-03.

Method composition is orthogonal (D-12): users may chain any subset of `.Schedule().Sensor().Partitions()` in any order; method validation defers to `Build()`.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
This plan implements D-03 (robfig/cron/v3 parser-only — no Cron runner), D-06 (SensorSpec/SensorResult/SensorFunc surface), D-09 (`.Partitions(spec)` builder + four strategies), D-11 (UTC ISO-8601 keys), D-12 (orthogonal method composition).

**Why Wave 1, parallel with 03-01:** This plan touches `internal/asset/*` and the new `internal/partition/*` package, plus go.mod for the cron dep. Plan 03-01 touches `internal/storage/ent/schema/`, `migrations/`, and `internal/event/types.go`. Zero file overlap — both safe to run in parallel.

**Why fail-fast at Build() for cron expressions:** Per Pitfall 1 in 03-RESEARCH.md Security Domain, an invalid cron expression at runtime would crash the scheduler daemon. By validating at `Builder.Build()` / `Builder.Register()`, we catch malformed expressions during asset definition (compile-time-ish) instead of at the next scheduler tick. The parser is already invoked there; storing the parsed `cron.Schedule` is unnecessary because the daemon re-parses each tick from the DB-stored string.

**Why partitionKey is a per-run input, not a per-asset configuration:** A single asset definition can produce many partitioned runs (one per partition). `AssetIO.PartitionKey()` returns the key for the currently-executing run, populated from `runs.partition_key` by the executor. The asset's `Partitions(...)` declaration tells the platform how many partitions exist; per-run `partitionKey` selects which one is being materialized.

**Why ISO weeks (Mon-Sun):** Discretion call per CONTEXT § Claude's Discretion — we pick ISO weeks because Go stdlib `time.Time.ISOWeek()` implements ISO 8601, the format `2024-W03` is unambiguous, and DST/locale concerns are sidestepped. Documented in keygen.go package doc.

**Why CurrentDailyKey defaults to "previous window" (yesterday):** Open Question 1 in 03-RESEARCH.md — Dagster convention; matches operator expectation that "today's daily run" processes yesterday's data. Configurable via `offset` parameter; default `24*time.Hour`.

**robfig/cron/v3 parser-only usage (D-03):** We import `github.com/robfig/cron/v3` purely for `cron.NewParser(...).Parse(expr)` to validate at builder time and `cron.Schedule.Next(t)` for the scheduler daemon (plan 03-04). We do NOT instantiate `cron.New()` — its built-in runner would compete with the database-coordinated tick loop.

**Frozen interfaces consumed:**
- `internal/asset.Builder` accumulator pattern (Phase 2 D-01 frozen)
- `internal/asset.Asset` immutable runtime type (Phase 2 frozen)
- `internal/asset.AssetIO` interface (Phase 2 D-04 frozen — extending only)

**Frozen interfaces produced (consumed by plans 03-04, 03-05, 03-06, 03-07):**
- `asset.SensorSpec`, `asset.SensorResult`, `asset.SensorFunc` types
- `partition.PartitionStrategy` interface + four concrete types
- `partition.DailyKey`, `partition.WeeklyKey`, `partition.MonthlyKey`, `partition.KeysBetween`, `partition.CurrentDailyKey`
- `asset.AssetIO.PartitionKey() string` method

@.planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md
@.planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md
@.planning/phases/03-scheduling-sensors-partitions/03-VALIDATION.md
@internal/asset/asset.go
@internal/asset/builder.go
@internal/asset/io.go
@internal/asset/builder_test.go

<interfaces>
<!-- Existing builder/asset/io surfaces this plan extends. Executor uses these directly — no exploration needed. -->

From internal/asset/asset.go (Phase 2 frozen):
```go
type MaterializeFunc func(ctx context.Context, io AssetIO) (MaterializeResult, error)
type MaterializeResult struct { RowsWritten int64; Metadata map[string]any }
type Resource struct { Name string; Weight int }
type Asset struct {
    name, connectorName string
    upstreams []string
    materializeFn MaterializeFunc
    retryPolicy RetryPolicy
    resources []Resource
    // Phase 3 additions in this plan:
    schedule   *ScheduleSpec
    sensors    []SensorSpec
    partitions PartitionStrategy
}
func (a *Asset) Name() string
func (a *Asset) Upstreams() []string
func (a *Asset) ConnectorName() string
func (a *Asset) MaterializeFn() MaterializeFunc
func (a *Asset) RetryPolicy() RetryPolicy
func (a *Asset) Resources() []Resource
```

From internal/asset/builder.go (Phase 2 frozen):
```go
type Builder struct{ a *Asset }
func New(name string) *Builder
func (b *Builder) Upstream(names ...string) *Builder
func (b *Builder) Connector(name string) *Builder
func (b *Builder) Materialize(fn MaterializeFunc) *Builder
func (b *Builder) Retry(p RetryPolicy) *Builder
func (b *Builder) Resource(name string, weight int) *Builder
func (b *Builder) Build() (*Asset, error)
func (b *Builder) Register() error
// Phase 3 additions: Schedule, Sensor, Partitions
```

From internal/asset/io.go (Phase 2 frozen — extending):
```go
type AssetIO interface {
    Read(ctx context.Context, upstream string) ([]connector.Row, error)
    Write(ctx context.Context, rows []connector.Row) (int64, error)
    // Phase 3 addition: PartitionKey() string
}
type ConnectorResolver interface {
    Resolve(assetName string) (connector.Connector, connector.AssetRef, error)
}
func NewAssetIO(self *Asset, resolver ConnectorResolver) AssetIO
// Phase 3 change: NewAssetIO(self, resolver, partitionKey string) AssetIO
```

Phase 3 new types (verbatim from D-06 + D-09):
```go
// asset.go
type ScheduleSpec struct {
    CronExpr string
    TZ       string  // optional, defaults to "UTC"; affects cron alignment, not partition key encoding
}
type SensorResult struct {
    Fired   bool
    RunKey  string
    Payload map[string]any
}
type SensorFunc func(ctx context.Context) (SensorResult, error)
type SensorSpec struct {
    Name        string
    MinInterval time.Duration  // default 30s
    Cooldown    time.Duration  // default 0 (off)
    Sense       SensorFunc
}

// partition/strategy.go
type PartitionStrategy interface {
    isPartitionStrategy()  // sealed marker — only this package's types implement
    Kind() string          // "daily" | "weekly" | "monthly" | "category"
}
type DailyPartitions struct { Start time.Time; TZ string }
type WeeklyPartitions struct { Start time.Time; TZ string }
type MonthlyPartitions struct { Start time.Time; TZ string }
type CategoryPartitions struct { Keys []string }

// partition/keygen.go
func DailyKey(t time.Time) string
func WeeklyKey(t time.Time) string
func MonthlyKey(t time.Time) string
func KeysBetween(strategy PartitionStrategy, start, end time.Time) ([]string, error)
func CurrentDailyKey(now time.Time, offset time.Duration) string  // default offset 24h
```
</interfaces>
</context>

<tasks>

<task id="3.2.1" type="auto" tdd="true">
  <name>Task 1: Create internal/partition package — strategies + key generation + tests</name>
  <files>internal/partition/strategy.go, internal/partition/strategy_test.go, internal/partition/keygen.go, internal/partition/keygen_test.go</files>
  <read_first>
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 6 — Partition Key Generation (full code blocks)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 8 — Partition-Spec Parsing (KeysBetween + isoWeekStart)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pitfall 4 — Partition Key Encoding Ambiguity (validation rules)
    - .planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md § D-09, D-11
  </read_first>
  <behavior>
    - DailyKey(time.Date(2024,1,15,12,30,0,0,time.UTC)) returns "2024-01-15"
    - WeeklyKey(time.Date(2024,1,15,0,0,0,0,time.UTC)) returns "2024-W03" (Monday Jan 15 2024 is in ISO week 3)
    - WeeklyKey(time.Date(2019,12,30,0,0,0,0,time.UTC)) returns "2020-W01" (year-boundary edge case)
    - WeeklyKey(time.Date(2015,12,31,0,0,0,0,time.UTC)) returns "2015-W53" (53-week year edge case)
    - MonthlyKey(time.Date(2024,1,15,0,0,0,0,time.UTC)) returns "2024-01"
    - All keys produced from a non-UTC input still encode the UTC window (e.g., a time in EST is converted to UTC before extraction)
    - KeysBetween(DailyPartitions{}, 2024-01-01, 2024-01-31) returns exactly 31 keys, in ascending date order, first "2024-01-01" last "2024-01-31"
    - KeysBetween(WeeklyPartitions{}, 2024-01-01, 2024-01-31) returns 5 keys ("2024-W01" through "2024-W05")
    - KeysBetween(MonthlyPartitions{}, 2024-01-01, 2024-03-31) returns ["2024-01","2024-02","2024-03"]
    - KeysBetween(CategoryPartitions{Keys:[]string{"us","eu"}}, ...) returns error (use comma list at CLI parse layer instead)
    - CurrentDailyKey(time.Now(), 24*time.Hour) returns DailyKey of yesterday
    - DailyPartitions{}, WeeklyPartitions{}, MonthlyPartitions{}, CategoryPartitions{} all implement PartitionStrategy
    - PartitionStrategy is a sealed interface — only types in this package can implement it (via unexported `isPartitionStrategy()` method)
  </behavior>
  <action>
    1. Create `internal/partition/strategy.go`:
       ```go
       // Package partition defines partition strategies and key generation for Phase 3 (D-09, D-11).
       package partition

       import "time"

       // PartitionStrategy is a sealed interface — only types declared in this package
       // implement it. New strategies require explicit support in KeysBetween and the
       // scheduler/backfill validators (D-09).
       type PartitionStrategy interface {
           isPartitionStrategy()
           // Kind returns a stable string identifier — "daily", "weekly", "monthly", "category".
           Kind() string
       }

       // DailyPartitions: one partition per UTC calendar day starting at Start (D-09 + D-11).
       type DailyPartitions struct {
           Start time.Time
           TZ    string // optional; "UTC" default; affects cron alignment, not key encoding
       }
       func (DailyPartitions) isPartitionStrategy() {}
       func (DailyPartitions) Kind() string { return "daily" }

       // WeeklyPartitions: one partition per ISO 8601 week (Mon-Sun) starting Mon of the week containing Start.
       type WeeklyPartitions struct{ Start time.Time; TZ string }
       func (WeeklyPartitions) isPartitionStrategy() {}
       func (WeeklyPartitions) Kind() string { return "weekly" }

       // MonthlyPartitions: one partition per UTC calendar month starting from the month of Start.
       type MonthlyPartitions struct{ Start time.Time; TZ string }
       func (MonthlyPartitions) isPartitionStrategy() {}
       func (MonthlyPartitions) Kind() string { return "monthly" }

       // CategoryPartitions: one partition per user-supplied static category key (D-09).
       // Keys must be unique, non-empty, ≤128 chars, and contain no '/' (Pitfall 4).
       type CategoryPartitions struct{ Keys []string }
       func (CategoryPartitions) isPartitionStrategy() {}
       func (CategoryPartitions) Kind() string { return "category" }
       ```
    2. Create `internal/partition/strategy_test.go`:
       - `TestPartitionStrategyKind` — assert `(DailyPartitions{}).Kind() == "daily"`, etc. for all four.
       - `TestPartitionStrategySealed` — compile-time check via type switch in test that exhaustively matches all four; if a fifth strategy is added later this test will document the obligation.
    3. Create `internal/partition/keygen.go` with the verbatim functions from 03-RESEARCH.md § Pattern 6 + § Pattern 8:
       ```go
       package partition

       import (
           "fmt"
           "strings"
           "time"
       )

       // ErrUnsupportedRangeStrategy is returned by KeysBetween when called with a strategy
       // that does not support range expansion (e.g. CategoryPartitions — use comma list instead).
       var ErrUnsupportedRangeStrategy = fmt.Errorf("partition: KeysBetween only supports time-based strategies")

       // ErrInvalidCategoryKey is returned by validation when a category key is empty,
       // exceeds 128 chars, or contains '/' (Pitfall 4 — encoding ambiguity).
       var ErrInvalidCategoryKey = fmt.Errorf("partition: category key invalid (empty | >128 chars | contains '/')")

       func DailyKey(t time.Time) string   { return t.UTC().Format("2006-01-02") }
       func MonthlyKey(t time.Time) string { return t.UTC().Format("2006-01") }
       func WeeklyKey(t time.Time) string {
           year, week := t.UTC().ISOWeek()
           return fmt.Sprintf("%d-W%02d", year, week)
       }
       // CurrentDailyKey returns the daily key for the partition window (now - offset).
       // Default offset 24h aligns with Dagster convention "cron fires for the preceding window".
       func CurrentDailyKey(now time.Time, offset time.Duration) string {
           return DailyKey(now.Add(-offset))
       }

       // ValidateCategoryKey enforces Pitfall 4 — non-empty, ≤128 chars, no '/'.
       func ValidateCategoryKey(key string) error {
           if key == "" || len(key) > 128 || strings.Contains(key, "/") {
               return fmt.Errorf("%w: %q", ErrInvalidCategoryKey, key)
           }
           return nil
       }

       // KeysBetween generates all partition keys (inclusive) for a time-based strategy
       // between start and end (UTC). For CategoryPartitions this returns ErrUnsupportedRangeStrategy
       // because the CLI parses comma-list specs at a higher layer.
       func KeysBetween(strategy PartitionStrategy, start, end time.Time) ([]string, error) {
           startUTC := start.UTC().Truncate(24 * time.Hour)
           endUTC   := end.UTC().Truncate(24 * time.Hour)
           if endUTC.Before(startUTC) {
               return nil, fmt.Errorf("partition: KeysBetween: end %s is before start %s", endUTC, startUTC)
           }
           switch strategy.(type) {
           case DailyPartitions:
               keys := make([]string, 0, int(endUTC.Sub(startUTC).Hours()/24)+1)
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
       func isoWeekStart(t time.Time) time.Time {
           u := t.UTC()
           weekday := u.Weekday()
           if weekday == time.Sunday { weekday = 7 } // ISO: Sun=7
           daysFromMonday := int(weekday) - 1
           return u.AddDate(0, 0, -daysFromMonday).Truncate(24 * time.Hour)
       }
       ```
    4. Create `internal/partition/keygen_test.go` with the following test functions covering the validation map:
       - `TestPartitionKeyGen` — Daily/Weekly/Monthly key produces correct UTC strings. Use `time.Date(2024, 1, 15, 12, 30, 45, 0, time.UTC)` and assert "2024-01-15", "2024-W03", "2024-01".
       - `TestWeeklyKeyYearBoundary` — assert WeeklyKey for 2019-12-30, 2015-01-01, 2015-12-31 returns "2020-W01", "2015-W01", "2015-W53" respectively.
       - `TestKeysBetween` — sub-tests for daily (31 keys for January 2024), weekly (5 keys), monthly (3 keys for Q1 2024), and CategoryPartitions returning ErrUnsupportedRangeStrategy.
       - `TestKeysBetweenInvertedRange` — start > end returns error.
       - `TestCurrentDailyKey` — assert CurrentDailyKey(time.Date(2024,1,15,...), 24*time.Hour) == "2024-01-14".
       - `TestValidateCategoryKey` — valid "us" passes; "" / strings.Repeat("x",129) / "us/east" all return ErrInvalidCategoryKey.
       - `TestNonUTCInputProducesUTCKey` — `time.Date(2024,1,15,2,0,0,0, time.FixedZone("EST", -5*3600))` (Jan 15 2:00 EST = Jan 15 7:00 UTC) → DailyKey returns "2024-01-15". Then `time.Date(2024,1,15,1,0,0,0, time.FixedZone("ChinaWest", 8*3600))` (Jan 15 01:00 CST = Jan 14 17:00 UTC) → DailyKey returns "2024-01-14".
  </action>
  <acceptance_criteria>
    - `grep -q 'type PartitionStrategy interface' internal/partition/strategy.go`
    - `grep -q 'type DailyPartitions struct' internal/partition/strategy.go`
    - `grep -q 'type WeeklyPartitions struct' internal/partition/strategy.go`
    - `grep -q 'type MonthlyPartitions struct' internal/partition/strategy.go`
    - `grep -q 'type CategoryPartitions struct' internal/partition/strategy.go`
    - `grep -q 'isPartitionStrategy()' internal/partition/strategy.go`
    - `grep -q 'func DailyKey(t time.Time) string' internal/partition/keygen.go`
    - `grep -q 'func WeeklyKey' internal/partition/keygen.go`
    - `grep -q 'ISOWeek()' internal/partition/keygen.go`
    - `grep -q 'func KeysBetween' internal/partition/keygen.go`
    - `grep -q 'func ValidateCategoryKey' internal/partition/keygen.go`
    - `go test ./internal/partition/... -run TestPartitionKeyGen -count=1 -timeout 30s` exits 0
    - `go test ./internal/partition/... -run TestWeeklyKeyYearBoundary -count=1 -timeout 30s` exits 0
    - `go test ./internal/partition/... -run TestKeysBetween -count=1 -timeout 30s` exits 0
    - `go test ./internal/partition/... -count=1 -timeout 30s` exits 0 (full package suite)
  </acceptance_criteria>
  <verify>
    <automated>go test ./internal/partition/... -count=1 -timeout 30s</automated>
  </verify>
  <done>internal/partition package exists with strategy + keygen files; all 7 test functions pass; sealed interface enforced.</done>
</task>

<task id="3.2.2" type="auto" tdd="true">
  <name>Task 2: Add ScheduleSpec, SensorSpec, SensorResult, SensorFunc types + Asset accessor methods + Schedule/Sensor/Partitions builder methods + cron validation</name>
  <files>internal/asset/asset.go, internal/asset/builder.go, internal/asset/builder_test.go, go.mod, go.sum</files>
  <read_first>
    - internal/asset/asset.go (current Asset struct + accessor methods)
    - internal/asset/builder.go (current Builder accumulator + Build/Register methods)
    - internal/asset/builder_test.go (existing test patterns)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 1 — robfig/cron/v3 parser-only usage
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 10 — Builder DSL Extension
    - .planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md § D-03, D-06, D-09, D-12
  </read_first>
  <behavior>
    - asset.New("foo").Connector("postgres").Materialize(fn).Schedule("0 0 * * *").Build() succeeds; Asset.Schedule() returns non-nil ScheduleSpec
    - asset.New("foo")...Schedule("not a valid cron").Build() returns wrapped error containing "invalid cron expression"
    - asset.New("foo")...Sensor(SensorSpec{Name:"...", MinInterval: 30*time.Second, Sense: senseFn}).Build() succeeds; Asset.Sensors() returns slice with one entry
    - Sensor with empty Name returns ErrSensorNameRequired at Build time
    - Sensor with nil Sense returns ErrSensorFuncRequired at Build time
    - asset.New("foo")...Partitions(partition.DailyPartitions{}).Build() succeeds; Asset.Partitions() returns the strategy
    - At most one partition strategy per asset — calling .Partitions twice overwrites (last wins, no error)
    - Method order does not matter — chaining .Schedule().Sensor().Partitions() in any order all yield the same Asset
    - For CategoryPartitions, builder Build() validates each key via partition.ValidateCategoryKey and returns wrapped error on first invalid key
  </behavior>
  <action>
    1. Run `go get github.com/robfig/cron/v3@v3.0.1` to add the dependency. Verify `go.mod` has `github.com/robfig/cron/v3 v3.0.1` and `go.sum` updated.
    2. Edit `internal/asset/asset.go` — add Phase 3 type declarations BEFORE the `Asset struct`:
       ```go
       // ScheduleSpec is the user-facing cron schedule attached via Builder.Schedule (D-03, D-12).
       type ScheduleSpec struct {
           CronExpr string
           TZ       string  // optional; defaults to "UTC" when empty; affects cron firing wall-clock alignment only
       }

       // SensorResult is returned by SensorFunc to indicate whether the sensor fired (D-06).
       // RunKey is the dedup key compared against sensors.last_run_key (layer 1).
       // Payload is a Phase 4 lineage hook — flows into MaterializeResult.Metadata of the triggered run.
       type SensorResult struct {
           Fired   bool
           RunKey  string
           Payload map[string]any
       }

       // SensorFunc is the user-supplied evaluation closure (D-06).
       type SensorFunc func(ctx context.Context) (SensorResult, error)

       // SensorSpec attaches an event sensor to an asset via Builder.Sensor (D-06).
       // MinInterval defaults to 30s when zero; Cooldown defaults to 0 (off, opt-in).
       type SensorSpec struct {
           Name        string
           MinInterval time.Duration
           Cooldown    time.Duration
           Sense       SensorFunc
       }
       ```
       Add fields to `Asset struct`:
       ```go
       schedule   *ScheduleSpec
       sensors    []SensorSpec
       partitions partition.PartitionStrategy
       ```
       Add accessors:
       ```go
       // Schedule returns the cron schedule attached via .Schedule(...). Nil if none.
       func (a *Asset) Schedule() *ScheduleSpec { return a.schedule }

       // Sensors returns a defensive copy of the attached SensorSpec list.
       func (a *Asset) Sensors() []SensorSpec { return append([]SensorSpec(nil), a.sensors...) }

       // Partitions returns the partition strategy attached via .Partitions(...). Nil if none.
       func (a *Asset) Partitions() partition.PartitionStrategy { return a.partitions }
       ```
       Add import `"time"` and `"github.com/kanpon/data-governance/internal/partition"` to asset.go.
    3. Edit `internal/asset/builder.go`:
       a. Add error sentinels:
          ```go
          ErrInvalidCron        = errors.New("asset: invalid cron expression")
          ErrSensorNameRequired = errors.New("asset: SensorSpec.Name is required")
          ErrSensorFuncRequired = errors.New("asset: SensorSpec.Sense is required")
          ErrSensorMinIntervalNegative = errors.New("asset: SensorSpec.MinInterval must be ≥ 0")
          ErrPartitionInvalidKey = errors.New("asset: CategoryPartitions key invalid")
          ```
       b. Add a package-level cron parser variable (D-03 — parser only):
          ```go
          // cronParser is initialised once; parser-only usage (D-03) — the in-process Cron runner is NEVER instantiated.
          var cronParser = cron.NewParser(
              cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
          )
          ```
          Add import `"github.com/robfig/cron/v3"`.
       c. Add three chained builder methods:
          ```go
          // Schedule attaches a cron expression to the asset (ORCH-05, D-03, D-12).
          // Validation is deferred to Build()/Register() — invalid expressions surface there.
          func (b *Builder) Schedule(cronExpr string) *Builder {
              b.a.schedule = &ScheduleSpec{CronExpr: cronExpr}
              return b
          }

          // Sensor appends a SensorSpec to the asset (ORCH-06, D-06, D-12).
          // Multiple .Sensor calls are cumulative.
          func (b *Builder) Sensor(spec SensorSpec) *Builder {
              b.a.sensors = append(b.a.sensors, spec)
              return b
          }

          // Partitions attaches a partition strategy (ORCH-07/08, D-09, D-12).
          // At most one strategy per asset — successive calls overwrite (last wins).
          func (b *Builder) Partitions(strategy partition.PartitionStrategy) *Builder {
              b.a.partitions = strategy
              return b
          }
          ```
       d. Extend `Build()` to validate Phase 3 fields AFTER the existing Materialize/Connector checks (so existing error semantics are preserved):
          ```go
          if b.a.schedule != nil {
              if _, err := cronParser.Parse(b.a.schedule.CronExpr); err != nil {
                  return nil, fmt.Errorf("%w: %q: %v (asset %q)", ErrInvalidCron, b.a.schedule.CronExpr, err, b.a.name)
              }
          }
          for _, s := range b.a.sensors {
              if s.Name == "" {
                  return nil, fmt.Errorf("%w (asset %q)", ErrSensorNameRequired, b.a.name)
              }
              if s.Sense == nil {
                  return nil, fmt.Errorf("%w (asset %q sensor %q)", ErrSensorFuncRequired, b.a.name, s.Name)
              }
              if s.MinInterval < 0 {
                  return nil, fmt.Errorf("%w (asset %q sensor %q): %s", ErrSensorMinIntervalNegative, b.a.name, s.Name, s.MinInterval)
              }
          }
          if cp, ok := b.a.partitions.(partition.CategoryPartitions); ok {
              for _, k := range cp.Keys {
                  if err := partition.ValidateCategoryKey(k); err != nil {
                      return nil, fmt.Errorf("%w: %v (asset %q)", ErrPartitionInvalidKey, err, b.a.name)
                  }
              }
          }
          ```
          Add imports `"github.com/kanpon/data-governance/internal/partition"`.
    4. Extend `internal/asset/builder_test.go` with new test functions:
       - `TestScheduleAccepted` — `New("foo").Connector("c").Materialize(fn).Schedule("0 0 * * *").Build()` returns no error; `Asset.Schedule().CronExpr == "0 0 * * *"`.
       - `TestScheduleInvalidCron` — `Schedule("not a valid cron").Build()` returns error matching `ErrInvalidCron`. (This satisfies VALIDATION.md `TestScheduleInvalidCron`.)
       - `TestScheduleEvery` — `Schedule("@every 30s").Build()` succeeds (descriptor support).
       - `TestSensorAccepted` — single SensorSpec accepted; `Asset.Sensors()` returns slice of length 1.
       - `TestSensorEmptyName` — `Sensor(SensorSpec{Sense: fn}).Build()` returns ErrSensorNameRequired.
       - `TestSensorNilSense` — `Sensor(SensorSpec{Name:"x"}).Build()` returns ErrSensorFuncRequired.
       - `TestPartitionsDailyAccepted` — `.Partitions(partition.DailyPartitions{}).Build()` succeeds; `Asset.Partitions().Kind() == "daily"`.
       - `TestPartitionsCategoryInvalidKey` — `.Partitions(partition.CategoryPartitions{Keys: []string{"us/east"}}).Build()` returns ErrPartitionInvalidKey.
       - `TestPartitionsLastWins` — `.Partitions(daily).Partitions(weekly).Build()` results in `Asset.Partitions().Kind() == "weekly"`.
       - `TestOrthogonalComposition` — chain `.Schedule("0 0 * * *").Sensor(spec).Partitions(daily)` AND `.Partitions(daily).Sensor(spec).Schedule("0 0 * * *")`; assert resulting Assets have same Schedule(), Sensors(), Partitions() (D-12).
       - Use a no-op `MaterializeFunc` and a connector name "test" in helper functions consistent with existing builder_test.go patterns.
  </action>
  <acceptance_criteria>
    - `grep -q 'type ScheduleSpec struct' internal/asset/asset.go`
    - `grep -q 'type SensorSpec struct' internal/asset/asset.go`
    - `grep -q 'type SensorResult struct' internal/asset/asset.go`
    - `grep -q 'type SensorFunc func' internal/asset/asset.go`
    - `grep -q 'func (a \\*Asset) Schedule()' internal/asset/asset.go`
    - `grep -q 'func (a \\*Asset) Sensors()' internal/asset/asset.go`
    - `grep -q 'func (a \\*Asset) Partitions()' internal/asset/asset.go`
    - `grep -q 'func (b \\*Builder) Schedule(cronExpr string) \\*Builder' internal/asset/builder.go`
    - `grep -q 'func (b \\*Builder) Sensor(spec SensorSpec) \\*Builder' internal/asset/builder.go`
    - `grep -q 'func (b \\*Builder) Partitions(strategy partition.PartitionStrategy) \\*Builder' internal/asset/builder.go`
    - `grep -q 'cron.NewParser' internal/asset/builder.go`
    - `grep -q 'ErrInvalidCron' internal/asset/builder.go`
    - `grep -q 'github.com/robfig/cron/v3 v3.0.1' go.mod`
    - `go test ./internal/asset/... -run TestScheduleInvalidCron -count=1 -timeout 30s` exits 0
    - `go test ./internal/asset/... -run TestOrthogonalComposition -count=1 -timeout 30s` exits 0
    - `go test ./internal/asset/... -count=1 -timeout 30s` exits 0
  </acceptance_criteria>
  <verify>
    <automated>go test ./internal/asset/... -run TestScheduleInvalidCron -count=1 -timeout 30s</automated>
  </verify>
  <done>asset package has ScheduleSpec/SensorSpec/SensorResult/SensorFunc types + Asset accessors; Builder has Schedule/Sensor/Partitions methods; cron validation at Build() time; orthogonal composition test passes; robfig/cron/v3 added to go.mod.</done>
</task>

<task id="3.2.3" type="auto" tdd="true">
  <name>Task 3: Extend AssetIO with PartitionKey() string + update NewAssetIO + add tests</name>
  <files>internal/asset/io.go, internal/asset/io_test.go</files>
  <read_first>
    - internal/asset/io.go (current AssetIO interface + assetIO struct + NewAssetIO)
    - internal/asset/asset_test.go (existing AssetIO test patterns if any)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 10 — `internal/asset/io.go` extension
  </read_first>
  <behavior>
    - AssetIO interface declares PartitionKey() string method
    - assetIO struct has partitionKey string field
    - NewAssetIO(self, resolver, partitionKey) populates the struct field
    - io.PartitionKey() returns the partitionKey string passed at construction
    - For non-partitioned runs, callers pass partitionKey="" and io.PartitionKey() returns ""
    - All EXISTING call sites of NewAssetIO compile after the signature change (executor in plan 03-XX will be updated to pass partition_key from claimed run; this plan updates only the asset package surface and any internal call sites within asset/)
  </behavior>
  <action>
    1. Edit `internal/asset/io.go`:
       a. Extend the `AssetIO` interface:
          ```go
          type AssetIO interface {
              Read(ctx context.Context, upstream string) ([]connector.Row, error)
              Write(ctx context.Context, rows []connector.Row) (int64, error)
              // PartitionKey returns the partition key for the currently-executing run (D-09, D-10).
              // Returns "" for non-partitioned runs (runs.partition_key IS NULL).
              PartitionKey() string
          }
          ```
       b. Extend `assetIO` struct:
          ```go
          type assetIO struct {
              self         *Asset
              resolver     ConnectorResolver
              partitionKey string
          }
          ```
       c. Update `NewAssetIO` signature:
          ```go
          // NewAssetIO constructs the runtime AssetIO for an asset run. The DAG executor
          // (internal/runtime) builds one AssetIO per step and passes it to MaterializeFunc.
          // partitionKey is the value of runs.partition_key for the current run (D-10);
          // pass "" for non-partitioned assets.
          func NewAssetIO(self *Asset, resolver ConnectorResolver, partitionKey string) AssetIO {
              return &assetIO{self: self, resolver: resolver, partitionKey: partitionKey}
          }
          ```
       d. Add the method:
          ```go
          func (io *assetIO) PartitionKey() string { return io.partitionKey }
          ```
       e. The `Read` and `Write` methods are unchanged — they ignore partitionKey for now (Phase 4 lineage will use it to index reads/writes).
    2. Update any callers of NewAssetIO **inside `internal/asset/*` and `internal/asset/*_test.go`**:
       - Search via `grep -rn "NewAssetIO(" internal/asset/` and add `, ""` as the third argument to each existing call.
       - DO NOT touch `internal/runtime/*` or any other package in this plan — that wiring lives in plan 03-04 (scheduler) or a downstream executor-update plan; the executor must be updated separately. The build will FAIL outside `internal/asset/` until the executor is updated. To keep this plan green, **also** update any direct caller in `internal/runtime/` that does not go through a test fixture, by adding `, ""` as the third argument. (This is a pure mechanical fix — no logic change. Use grep to find all occurrences.)
    3. Run `grep -rn "NewAssetIO(" --include='*.go' .` to find every caller, then update each to pass `""` as the third arg. Confirm the codebase still builds.
    4. Create or extend `internal/asset/io_test.go`:
       - `TestAssetIOPartitionKeyDefault` — `io := NewAssetIO(a, fakeResolver, ""); assert.Equal(t, "", io.PartitionKey())`.
       - `TestAssetIOPartitionKeySet` — `io := NewAssetIO(a, fakeResolver, "2024-01-15"); assert.Equal(t, "2024-01-15", io.PartitionKey())`.
       - Use a minimal `fakeResolver` stub that satisfies the `ConnectorResolver` interface (return errors from Resolve since these tests don't exercise Read/Write).
  </action>
  <acceptance_criteria>
    - `grep -q 'PartitionKey() string' internal/asset/io.go`
    - `grep -q 'partitionKey string' internal/asset/io.go`
    - `grep -q 'func NewAssetIO(self \\*Asset, resolver ConnectorResolver, partitionKey string) AssetIO' internal/asset/io.go`
    - `grep -q 'func (io \\*assetIO) PartitionKey() string { return io.partitionKey }' internal/asset/io.go`
    - `grep -rn 'NewAssetIO(' --include='*.go' . | grep -v 'partitionKey string)' | grep -v -E 'NewAssetIO\\([^)]*,[^)]*,[^)]*\\)'` returns no rows (all callers updated to pass three args)
    - `go build ./...` passes
    - `go test ./internal/asset/... -run TestAssetIOPartitionKey -count=1 -timeout 30s` exits 0
  </acceptance_criteria>
  <verify>
    <automated>go build ./... && go test ./internal/asset/... -count=1 -timeout 30s</automated>
  </verify>
  <done>AssetIO interface has PartitionKey(); NewAssetIO accepts partitionKey string; all callers updated; tests pass; build green.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| User code → asset.Builder | User-supplied cron expressions, sensor names, category keys cross here at definition time |
| asset.Builder → robfig/cron/v3 parser | Cron expression validation crosses here; parser is the trust enforcement point |
| asset.Builder → partition.ValidateCategoryKey | Category key validation crosses here; rejects '/' and length>128 |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-03-02-01 | Denial of Service | Malformed cron expression in user code | mitigate | `Builder.Build()` calls `cronParser.Parse(expr)` and returns wrapped ErrInvalidCron. Asset registration fails fast — daemon never sees a bad expression (Pitfall 1). Test `TestScheduleInvalidCron` enforces. |
| T-03-02-02 | Tampering | Category partition key with '/' or oversized | mitigate | `partition.ValidateCategoryKey` enforces ≤128 chars + no '/'; called by `Builder.Build()` for CategoryPartitions. Tested in `TestPartitionsCategoryInvalidKey` and `TestValidateCategoryKey` (Pitfall 4). |
| T-03-02-03 | Denial of Service | SensorSpec with negative MinInterval causing busy-loop | mitigate | `Builder.Build()` rejects MinInterval < 0; tested in builder_test. Daemon defaults zero to 30s at evaluation time (plan 03-05). |
| T-03-02-04 | Information Disclosure | SensorResult.Payload may carry user-controlled data | accept | Payload is opaque `map[string]any` per D-06; downstream lineage hook (Phase 4) will treat as untrusted. No new trust boundary in this plan. |
| T-03-02-05 | Spoofing | Anyone can implement PartitionStrategy interface | mitigate | Sealed interface — `isPartitionStrategy()` is unexported. Third parties cannot add strategies; KeysBetween defaults to ErrUnsupportedRangeStrategy for unknown types (defense in depth). |
| T-03-02-06 | Tampering | partition.WeeklyKey ISO-week edge cases | mitigate | Delegate to Go stdlib `time.Time.ISOWeek()` (RFC 5545 compliant since Go 1.0); tested with year-boundary fixtures (2019-12-30 → "2020-W01", 2015-12-31 → "2015-W53"). |
| T-03-02-07 | Information Disclosure | partitionKey leakage via error messages | accept | partitionKey is non-sensitive metadata (asset partition identifier), not user data. Acceptable to include in error context. |
</threat_model>

<verification>
- `go build ./...` passes after the AssetIO signature change is propagated.
- `go test ./internal/asset/... -count=1 -timeout 30s` exits 0.
- `go test ./internal/partition/... -count=1 -timeout 30s` exits 0.
- `grep github.com/robfig/cron/v3 go.mod` returns the line with v3.0.1.
- Composing methods in any order produces the same Asset (TestOrthogonalComposition).
</verification>

<success_criteria>
- internal/partition package exists with PartitionStrategy interface (sealed), four concrete strategies, and DailyKey/WeeklyKey/MonthlyKey/KeysBetween/CurrentDailyKey/ValidateCategoryKey.
- internal/asset extends Asset with schedule/sensors/partitions fields and accessors (Schedule/Sensors/Partitions).
- internal/asset adds ScheduleSpec, SensorSpec, SensorResult, SensorFunc types.
- Builder has Schedule/Sensor/Partitions chained methods; Build() validates cron via robfig/cron/v3, rejects invalid sensor specs, validates category keys.
- AssetIO has PartitionKey() string method; NewAssetIO accepts partitionKey string.
- All Phase 3 builder tests pass (cron invalid, sensor errors, partition validation, orthogonal composition).
- robfig/cron/v3 v3.0.1 added to go.mod.
- All existing tests still pass; build green.
</success_criteria>

<output>
After completion, create `.planning/phases/03-scheduling-sensors-partitions/03-02-SUMMARY.md` documenting:
- Final asset package SDK surface (Schedule/Sensor/Partitions methods + types)
- Final partition package surface (interface + 4 strategies + 6 functions)
- AssetIO signature change and propagation
- robfig/cron/v3 dependency confirmed at v3.0.1
- Decision-coverage map: D-03 / D-06 / D-09 / D-11 / D-12 → which test names cover each
</output>
