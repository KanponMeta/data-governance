---
phase: 03-scheduling-sensors-partitions
plan: 02
subsystem: sdk
tags: [asset, builder, dsl, partition, sensor, schedule, cron, robfig, sealed-interface]

# Dependency graph
requires:
  - phase: 02-execution-engine
    provides: asset.Builder accumulator pattern, asset.Asset immutable type, asset.AssetIO interface, asset.MaterializeFunc/Result types, internal/connector
  - phase: 03-scheduling-sensors-partitions/01-schema-events-foundation
    provides: schema-only foundation — no compile-time API consumed by this plan; Wave 1 parallel-safe (zero file overlap)
provides:
  - "asset.ScheduleSpec / SensorSpec / SensorResult / SensorFunc Go types (D-06)"
  - "asset.Builder.Schedule(cronExpr) / .Sensor(spec) / .Partitions(strategy) chained methods (D-03, D-06, D-09, D-12)"
  - "asset.Asset.Schedule() / Sensors() / Partitions() accessor methods"
  - "asset.AssetIO.PartitionKey() string method + NewAssetIO partitionKey constructor arg (D-09, D-10)"
  - "Build()-time cron validation via robfig/cron/v3 parser-only (Pitfall 1, T-03-02-01)"
  - "Build()-time SensorSpec validation: name required, Sense required, MinInterval ≥ 0 (D-06, T-03-02-03)"
  - "Build()-time CategoryPartitions key validation via partition.ValidateCategoryKey (Pitfall 4, T-03-02-02)"
  - "internal/partition package: PartitionStrategy sealed interface (T-03-02-05), DailyPartitions/WeeklyPartitions/MonthlyPartitions/CategoryPartitions structs"
  - "internal/partition: DailyKey/WeeklyKey/MonthlyKey UTC ISO-8601 key generation (D-11)"
  - "internal/partition: KeysBetween range expansion (Daily/Weekly/Monthly), CategoryPartitions returns ErrUnsupportedRangeStrategy"
  - "internal/partition: CurrentDailyKey(now, offset) — Dagster previous-window convention (default 24h)"
  - "internal/partition: ValidateCategoryKey enforces non-empty / ≤128 chars / no '/' (Pitfall 4)"
affects: [03-03-claim-priority-scheduler-daemon, 03-04-sensor-evaluator, 03-05-cron-loop, 03-06-backfill-cli, 03-07-integration-tests]

# Tech tracking
tech-stack:
  added:
    - "github.com/robfig/cron/v3 v3.0.1 (parser-only per D-03 — never instantiates cron.Cron runner)"
  patterns:
    - "Sealed interface via unexported isPartitionStrategy() method (T-03-02-05) — third parties cannot implement; KeysBetween default branch returns ErrUnsupportedRangeStrategy as defense-in-depth"
    - "Build()-time fail-fast validation deferred from setter methods — invalid cron / sensor / category never reaches scheduler daemon (Pitfall 1)"
    - "Cron parser as package-level var initialised once (cron.NewParser with Minute|Hour|Dom|Month|Dow|Descriptor flags) — reused across all .Schedule() validations"
    - "Defensive copy on accessor methods (Asset.Sensors() / Resources() / Upstreams()) — preserves immutability of registered Asset"
    - "TDD RED → GREEN per task: failing test committed first, implementation in follow-up commit (3 task pairs = 6 commits)"

key-files:
  created:
    - "internal/partition/strategy.go (PartitionStrategy interface + 4 concrete strategy structs)"
    - "internal/partition/strategy_test.go (Kind() + sealed-interface exhaustiveness test)"
    - "internal/partition/keygen.go (DailyKey/WeeklyKey/MonthlyKey/KeysBetween/CurrentDailyKey/ValidateCategoryKey + isoWeekStart helper + ErrUnsupportedRangeStrategy/ErrInvalidCategoryKey sentinels)"
    - "internal/partition/keygen_test.go (7 test functions covering UTC encoding, ISO week edges, range expansion, validation)"
    - "internal/asset/io_test.go (TestAssetIOPartitionKeyDefault + TestAssetIOPartitionKeySet)"
  modified:
    - "internal/asset/asset.go (Phase 3 types: ScheduleSpec/SensorSpec/SensorResult/SensorFunc; Asset struct + 3 accessor methods)"
    - "internal/asset/builder.go (cronParser package var; 5 new error sentinels; Schedule/Sensor/Partitions methods; Build() validation block)"
    - "internal/asset/io.go (AssetIO interface + PartitionKey() method; assetIO.partitionKey field; NewAssetIO third arg)"
    - "internal/asset/builder_test.go (11 new Phase 3 tests: schedule, sensor, partition, orthogonal-composition; existing NewAssetIO call sites updated)"
    - "internal/runtime/executor.go (NewAssetIO call updated to pass \"\" — partition_key wiring lands in plan 03-03+)"
    - "go.mod / go.sum (robfig/cron/v3 v3.0.1 added as direct dependency; go mod tidy normalised indirect block)"

key-decisions:
  - "Cron validation at Build()/Register() (fail-fast) NOT at runtime — invalid expressions never reach scheduler daemon (Pitfall 1, T-03-02-01)"
  - "PartitionStrategy is a sealed interface via unexported isPartitionStrategy() method — third parties cannot add strategies; KeysBetween default branch returns ErrUnsupportedRangeStrategy as defense-in-depth (T-03-02-05)"
  - "WeeklyKey delegates to Go stdlib time.Time.ISOWeek() — RFC 5545 compliant since Go 1.0; year-boundary cases (2019-12-30 → 2020-W01) and 53-week years (2015-W53) work without custom logic (Pattern 6)"
  - "CurrentDailyKey default offset 24h matches Dagster's previous-window convention — operators expect 'today's daily run' to process yesterday's data (Open Question 1 in 03-RESEARCH.md)"
  - "CategoryPartitions validation deferred to Builder.Build() (not setter) per D-12 orthogonal-composition contract — keeps method ordering irrelevant"
  - ".Partitions() last-wins semantics (no error on second call) — keeps API minimal; future evolution path unblocked"
  - "Time-partition keys store UTC ISO-8601 strings (D-11); TZ on the partition spec is for cron alignment / display only — DST landmines avoided by construction"
  - "robfig/cron/v3 used parser-only per D-03; cron.NewParser package-level var; cron.Cron runner is NEVER instantiated"

patterns-established:
  - "Phase 3 SDK extension pattern: append types to asset.go, append validation rules to builder.go Build(), keep Phase 2 errors first"
  - "Threat-model-driven validation: each builder Build() check has an explicit T-03-02-* threat ID in the error sentinel comment"
  - "TDD RED → GREEN per task with two separate commits per task — clean bisect surface"

requirements-completed: [ORCH-05, ORCH-06, ORCH-07, ORCH-08]
decisions-implemented: [D-03, D-06, D-09, D-11, D-12]

# Metrics
duration: ~12min
completed: 2026-05-08
---

# Phase 3 Plan 02: Asset DSL Extensions and Partitions Summary

**稳定的 Phase 3 SDK 表面：`.Schedule(cron)` / `.Sensor(spec)` / `.Partitions(strategy)` 链式构建器方法，以及 `internal/partition` 包和 `AssetIO.PartitionKey()` 访问器——所有下游 Phase 3 计划（scheduler tick loop、sensor evaluator、backfill CLI）的编译时 API 已冻结。**

## Performance

- **Duration:** ~12 min
- **Started:** 2026-05-08T08:27:00Z (approx)
- **Completed:** 2026-05-08T08:39:00Z
- **Tasks:** 3 (all autonomous; all followed strict TDD RED→GREEN)
- **Files created:** 5 (4 partition + 1 io_test)
- **Files modified:** 6 source files (asset.go, builder.go, io.go, builder_test.go, executor.go, go.mod/go.sum)

## Accomplishments

- **internal/partition 包**已创建，包含密封的 `PartitionStrategy` 接口和四个具体策略（`DailyPartitions`、`WeeklyPartitions`、`MonthlyPartitions`、`CategoryPartitions`）；六个导出函数（`DailyKey`、`WeeklyKey`、`MonthlyKey`、`KeysBetween`、`CurrentDailyKey`、`ValidateCategoryKey`）；两个错误 sentinel（`ErrUnsupportedRangeStrategy`、`ErrInvalidCategoryKey`）。
- **9 个分区测试**覆盖 UTC 编码、ISO 周年末边界情况（2019-12-30 → "2020-W01"、2015-12-31 → "2015-W53"）、范围扩展（31 个 daily / 5 个 weekly / 3 个 monthly）、倒置范围拒绝、category 策略拒绝、非 UTC 输入 → UTC 键，以及 category 键验证规则。
- **`asset` 包扩展**了 `ScheduleSpec`/`SensorSpec`/`SensorResult`/`SensorFunc` 类型；`Asset.Schedule()`/`Sensors()`/`Partitions()` 访问器（含防御性拷贝）；`Builder.Schedule()`/`Sensor()`/`Partitions()` 链式方法。
- **Build()-time 验证**针对所有 Phase 3 输入：`cronParser.Parse()` 用于 cron 表达式、name/Sense/MinInterval 守卫用于传感器、`partition.ValidateCategoryKey` 用于 category 键。所有错误都包装类型 sentinel（`ErrInvalidCron`、`ErrSensorNameRequired`、`ErrSensorFuncRequired`、`ErrSensorMinIntervalNegative`、`ErrPartitionInvalidKey`）。
- **`AssetIO.PartitionKey() string`** 已添加到接口；`NewAssetIO` 构造函数将 `partitionKey` 作为第三个参数；`internal/asset/builder_test.go` 中的现有调用点（×2）和 `internal/runtime/executor.go` 中的调用点（×1）已更新为传递 `""`，直到计划 03-03+ 将 partition_key 从 claimed runs 接入。
- **`robfig/cron/v3 v3.0.1`** 添加为直接依赖（D-03——仅解析器；`cron.Cron` runner 永不实例化）。
- **11 个新的 asset 包测试：** `TestSchedule{Accepted, InvalidCron, Every}`、`TestSensor{Accepted, EmptyName, NilSense, NegativeMinInterval}`、`TestPartitions{DailyAccepted, CategoryInvalidKey, CategoryOversizeKey, LastWins}`、`TestOrthogonalComposition`、`TestAssetIOPartitionKey{Default, Set}`。**全部通过**，与现有的 9 个 Phase 2 builder 测试一起——零回归。

## Task Commits

每个任务原子提交——RED 测试提交后跟 GREEN 实现提交：

| Task | Description                                                          | RED commit | GREEN commit |
| ---- | -------------------------------------------------------------------- | ---------- | ------------ |
| 1    | internal/partition package — strategies + UTC keygen                  | `c8eb0be`  | `453063f`    |
| 2    | Schedule/Sensor/Partitions DSL + cron parser validation              | `f229a78`  | `cfd4c1b`    |
| 3    | AssetIO.PartitionKey() + NewAssetIO signature change                  | `def9eec`  | `9ebfede`    |

Verify with `git log --oneline aa830da..HEAD`.

## Decision-Coverage Map

| Decision | Covered by                                            | Test name(s)                                                                                  |
| -------- | ----------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| **D-03** (robfig/cron/v3 parser-only)             | `cronParser` var in `builder.go`; `Build()` Parse()  | `TestScheduleAccepted`, `TestScheduleInvalidCron`, `TestScheduleEvery`                        |
| **D-06** (SensorSpec / SensorResult / SensorFunc)| `asset.go` types + `Builder.Sensor()` + Build() guards | `TestSensorAccepted`, `TestSensorEmptyName`, `TestSensorNilSense`, `TestSensorNegativeMinInterval` |
| **D-09** (single .Partitions(strategy) DSL)       | `Builder.Partitions()` + sealed PartitionStrategy interface | `TestPartitionsDailyAccepted`, `TestPartitionsCategoryInvalidKey`, `TestPartitionsCategoryOversizeKey`, `TestPartitionsLastWins`, `TestPartitionStrategyKind`, `TestPartitionStrategySealed` |
| **D-11** (UTC ISO-8601 keys)                       | `DailyKey`/`WeeklyKey`/`MonthlyKey` in `keygen.go`     | `TestPartitionKeyGen`, `TestWeeklyKeyYearBoundary`, `TestNonUTCInputProducesUTCKey`           |
| **D-12** (orthogonal method composition)         | All builder methods return `*Builder`; Build() validates regardless of order | `TestOrthogonalComposition`                                                                   |

## Final Public API Surface

### `internal/asset` package

```go
// Phase 3 types (asset.go):
type ScheduleSpec struct { CronExpr string; TZ string }
type SensorResult struct { Fired bool; RunKey string; Payload map[string]any }
type SensorFunc func(ctx context.Context) (SensorResult, error)
type SensorSpec struct { Name string; MinInterval, Cooldown time.Duration; Sense SensorFunc }

// Asset accessors (asset.go):
func (a *Asset) Schedule() *ScheduleSpec
func (a *Asset) Sensors() []SensorSpec
func (a *Asset) Partitions() partition.PartitionStrategy

// Builder methods (builder.go):
func (b *Builder) Schedule(cronExpr string) *Builder
func (b *Builder) Sensor(spec SensorSpec) *Builder
func (b *Builder) Partitions(strategy partition.PartitionStrategy) *Builder

// Error sentinels (builder.go):
var ErrInvalidCron, ErrSensorNameRequired, ErrSensorFuncRequired,
    ErrSensorMinIntervalNegative, ErrPartitionInvalidKey error

// AssetIO extension (io.go):
type AssetIO interface {
    Read(ctx context.Context, upstream string) ([]connector.Row, error)
    Write(ctx context.Context, rows []connector.Row) (int64, error)
    PartitionKey() string  // Phase 3 addition
}
func NewAssetIO(self *Asset, resolver ConnectorResolver, partitionKey string) AssetIO
```

### `internal/partition` package

```go
// strategy.go:
type PartitionStrategy interface {
    isPartitionStrategy()  // sealed marker
    Kind() string
}
type DailyPartitions   struct { Start time.Time; TZ string }
type WeeklyPartitions  struct { Start time.Time; TZ string }
type MonthlyPartitions struct { Start time.Time; TZ string }
type CategoryPartitions struct { Keys []string }

// keygen.go:
var ErrUnsupportedRangeStrategy, ErrInvalidCategoryKey error
func DailyKey(t time.Time) string
func WeeklyKey(t time.Time) string
func MonthlyKey(t time.Time) string
func KeysBetween(strategy PartitionStrategy, start, end time.Time) ([]string, error)
func CurrentDailyKey(now time.Time, offset time.Duration) string
func ValidateCategoryKey(key string) error
```

## Threat Surface Coverage

The plan's `<threat_model>` register is fully addressed by this plan's deliverables:

| Threat ID                        | Status     | Evidence                                                                                  |
| -------------------------------- | ---------- | ----------------------------------------------------------------------------------------- |
| T-03-02-01 (malformed cron DOS)  | mitigated  | `cronParser.Parse()` in `Build()`; `TestScheduleInvalidCron` enforces                     |
| T-03-02-02 (category key tampering) | mitigated | `partition.ValidateCategoryKey` enforces ≤128 / no '/'; `TestPartitionsCategoryInvalidKey` and `TestPartitionsCategoryOversizeKey` enforce |
| T-03-02-03 (sensor busy-loop DOS) | mitigated | MinInterval < 0 rejected in Build(); `TestSensorNegativeMinInterval` enforces             |
| T-03-02-04 (SensorResult.Payload disclosure) | accept | Documented; Phase 4 lineage hook will treat as untrusted                              |
| T-03-02-05 (third-party PartitionStrategy spoof) | mitigated | Sealed interface via unexported `isPartitionStrategy()`; `TestPartitionStrategySealed` is the exhaustiveness guard; KeysBetween default branch returns `ErrUnsupportedRangeStrategy` as defense-in-depth |
| T-03-02-06 (ISO-week edge cases)   | mitigated  | Delegate to Go stdlib `time.Time.ISOWeek()`; `TestWeeklyKeyYearBoundary` covers 2019-W01 / 2015-W53 |
| T-03-02-07 (partitionKey error-message disclosure) | accept | partitionKey is non-sensitive metadata; documented in code                          |

## Deviations from Plan

**None — plan executed exactly as written.**

The plan's task structure, test names, behavior rules, action steps, and acceptance criteria all matched the implementation 1:1. The only minor adjustment was running `go mod tidy` after `go get` to promote `robfig/cron/v3` from `// indirect` to a direct dependency line — this is a normal Go module hygiene step, not a deviation from plan intent (the plan's acceptance criterion `grep github.com/robfig/cron/v3 v3.0.1 go.mod` passes either way).

I added `TestSensorNegativeMinInterval` and `TestPartitionsCategoryOversizeKey` beyond the plan's enumerated tests — both enforce threat-model items (T-03-02-03 and T-03-02-02 respectively) that the plan's `<behavior>` rules called out but did not name a test for explicitly. Cost zero scope creep; pure threat-mitigation evidence.

## Issues Encountered

- The plan's `<output>` section names the SUMMARY file `03-02-SUMMARY.md`, while the orchestrator-provided objective specifies `03-02-asset-dsl-and-partitions-SUMMARY.md`. Followed the orchestrator path (matches Phase 3 plan-01 file naming convention `03-01-schema-events-foundation-SUMMARY.md`).
- `go mod tidy` initially marked `robfig/cron/v3` as `// indirect` despite a direct import in `internal/asset/builder.go`. A second invocation (after running tests that pulled in additional transitive build constraints) promoted it to direct. No code change needed.

## Self-Check: PASSED

**Created files exist:**
- FOUND: internal/partition/strategy.go
- FOUND: internal/partition/strategy_test.go
- FOUND: internal/partition/keygen.go
- FOUND: internal/partition/keygen_test.go
- FOUND: internal/asset/io_test.go

**Modified files updated:**
- FOUND: internal/asset/asset.go (Phase 3 types + accessors)
- FOUND: internal/asset/builder.go (cronParser, error sentinels, 3 new methods, Build() validation)
- FOUND: internal/asset/io.go (AssetIO.PartitionKey() + NewAssetIO partitionKey arg)
- FOUND: internal/asset/builder_test.go (11 new tests + 2 NewAssetIO call updates)
- FOUND: internal/runtime/executor.go (NewAssetIO call updated)
- FOUND: go.mod (robfig/cron/v3 v3.0.1 direct)

**Commits exist:**
- FOUND: c8eb0be (Task 1 RED — partition tests)
- FOUND: 453063f (Task 1 GREEN — partition impl)
- FOUND: f229a78 (Task 2 RED — DSL tests)
- FOUND: cfd4c1b (Task 2 GREEN — DSL impl)
- FOUND: def9eec (Task 3 RED — io tests)
- FOUND: 9ebfede (Task 3 GREEN — io impl)

**Build & test pass:**
- `go build ./...` → green
- `go test ./internal/asset/... -count=1 -timeout 30s` → ok
- `go test ./internal/partition/... -count=1 -timeout 30s` → ok
- `go test ./internal/runtime/... -count=1 -timeout 60s` → ok (no regressions from NewAssetIO signature change)
- `go vet ./...` → clean

## Next Plan Readiness

- **Plan 03-03 (claim priority + scheduler daemon)** can now consume the frozen SDK surface: scheduler will read `Asset.Schedule()` / `Asset.Sensors()` / `Asset.Partitions()`, parse cron exprs (already validated), and use `partition.CurrentDailyKey()` + `partition.KeysBetween()` for partition selection. `runs.partition_key` from plan 03-01 is the storage; `AssetIO.PartitionKey()` from this plan is the read-side accessor — both ready.
- **Plan 03-04 (sensor evaluator)** has the `SensorSpec`/`SensorResult` types it needs; the daemon can now `range a.Sensors()` and call `spec.Sense(ctx)` directly.
- **Plan 03-05 (cron loop)** can re-use the same `cronParser` package var pattern (or its own equivalent in `internal/schedule/`); cron expressions stored in `schedules.cron_expr` are already validated at definition time.
- **Plan 03-06 (backfill CLI)** has `partition.KeysBetween` + `partition.ValidateCategoryKey` ready for `--partitions` spec parsing; `Asset.Partitions()` provides the strategy resolution per Pitfall 4.
- **Plan 03-07 (integration tests)** has all builder DSL inputs ready for end-to-end fixtures (schedules, sensors, partitions, backfills) without further SDK changes.

---

*Phase: 03-scheduling-sensors-partitions*
*Plan: 02 (asset DSL + partitions)*
*Completed: 2026-05-08*
