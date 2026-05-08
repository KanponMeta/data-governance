---
phase: 3
plan: 05
title: Sensor evaluator — safe Sense() invocation + RunKey/cooldown two-layer dedup + auto-disable on N consecutive failures
type: execute
wave: 2
depends_on: [01, 02]
requirements: [ORCH-06]
decisions_implemented: [D-05, D-06, D-07, D-08, D-12]
files_modified:
  - internal/sensor/daemon.go
  - internal/sensor/daemon_test.go
  - internal/sensor/evaluate.go
  - internal/sensor/evaluate_test.go
  - internal/sensor/registry.go
autonomous: true
must_haves:
  truths:
    - "sensor.Daemon.RunOnce(ctx) is the entry point — selects due sensor rows via SELECT FOR UPDATE SKIP LOCKED, evaluates each via safeEvaluate, and updates state in one transaction per sensor"
    - "safeEvaluate wraps SensorFunc.Sense(ctx) with context.WithTimeout (default = SensorSpec.MinInterval) and defer recover() — panics are converted to errors, not propagated up"
    - "On Fired=true: if RunKey == sensors.last_run_key emit sensor.dedup_skipped (no enqueue); elif NOW() < cooldown_until emit sensor.cooldown_skipped (no enqueue); else INSERT runs row and UPDATE last_run_key/last_fired_at/cooldown_until atomically"
    - "On Sense() error or panic: emit sensor.evaluation_failed event, increment consecutive_failures; if consecutive_failures+1 >= AutoDisableThreshold (default 60) then set disabled_at and emit sensor.disabled — auto-reset to 0 on first successful evaluation"
    - "Sensor with .Partitions(daily) populates runs.partition_key = SensorResult.RunKey if it parses as a daily key, else CurrentDailyKey(now, 24h) (Open Question 1 default)"
    - "UpsertSensors syncs registry asset.Sensors() into the sensors table (idempotent across restarts)"
    - "Tick selects from sensors WHERE disabled_at IS NULL AND (last_evaluated_at IS NULL OR last_evaluated_at + min_interval_seconds * interval '1 second' <= NOW())"
  artifacts:
    - path: "internal/sensor/daemon.go"
      provides: "Daemon.RunOnce(ctx) — sensor tick driver, selects due rows + evaluates + updates state"
      contains: "type Daemon struct"
    - path: "internal/sensor/evaluate.go"
      provides: "safeEvaluate (timeout + panic recovery) + handleResult (dedup + cooldown + enqueue) + handleError (failure counting + auto-disable)"
      contains: "func safeEvaluate"
    - path: "internal/sensor/registry.go"
      provides: "UpsertSensors(ctx, registry): syncs asset.DefinitionRegistry SensorSpec into sensors table"
      contains: "func UpsertSensors"
  key_links:
    - from: "internal/sensor.safeEvaluate"
      to: "user-supplied asset.SensorFunc"
      via: "context.WithTimeout(ctx, spec.MinInterval) + defer recover() wrapper"
      pattern: "context.WithTimeout.*defer.*recover"
    - from: "internal/sensor.handleResult"
      to: "PostgreSQL runs + sensors tables"
      via: "INSERT runs (priority=normal, trigger=sensor, partition_key=...) + UPDATE sensors (last_run_key, last_fired_at, cooldown_until) — same tx"
      pattern: "INSERT INTO runs.*trigger.*sensor"
    - from: "internal/sensor.handleError"
      to: "sensors.consecutive_failures + sensors.disabled_at"
      via: "UPDATE sensors SET consecutive_failures = consecutive_failures + 1, disabled_at = CASE WHEN consecutive_failures + 1 >= $threshold THEN NOW() ELSE disabled_at END"
      pattern: "consecutive_failures \\+ 1 >= "
---

<objective>
Land the sensor evaluation harness: a `Daemon.RunOnce(ctx)` driver that selects due sensors via SELECT FOR UPDATE SKIP LOCKED, calls each user-supplied `Sense(ctx)` function under a timeout-bounded ctx with panic recovery, applies the two-layer dedup (RunKey ⇒ skip, cooldown ⇒ skip), and either enqueues a runs row or records the dedup decision via event. On Sense() error/panic: increment `consecutive_failures` and auto-disable at threshold (D-08).

Like plan 03-04, this delivers the *internal* package; the `./platform scheduler` subcommand that wires both daemons is plan 03-06.

The Daemon exposes `RunOnce(ctx)` rather than `Run(ctx)` because plan 03-06's scheduler subcommand will call `RunOnce` from inside the scheduler tick loop, sharing the goroutine — no separate sensor goroutine pool (D-05: "Sensors run inside the same scheduler subcommand as cron").
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
This plan implements D-05 (sensors share the scheduler subcommand), D-06 (SensorResult contract), D-07 (two-layer dedup: RunKey + cooldown), D-08 (Sense() error → log + retry; N-fail auto-disable), D-12 (Sensor×Partitions composition).

**Why Wave 2:** Depends on plan 03-01 (sensors table) and plan 03-02 (asset.SensorSpec, asset.SensorResult, asset.SensorFunc, partition.PartitionStrategy, partition.CurrentDailyKey). Cannot run before either. depends_on = [01, 02].

**Why parallel with 03-03 and 03-04 in Wave 2:** This plan touches `internal/sensor/*`. Plans 03-03 and 03-04 touch `internal/run/*` and `internal/schedule/*` respectively. Zero file overlap.

**Why RunOnce instead of Run (per-tick driver, not standalone loop):** D-05 mandates that sensors run "inside the same scheduler subcommand as cron, sharing the tick loop." The scheduler Daemon's tick loop (plan 03-04) will call `sensor.Daemon.RunOnce(ctx)` after `schedule.Daemon.tick(ctx)`. This avoids a second timer and shares the SKIP LOCKED safety primitive. **However:** RunOnce should also be testable in isolation, hence its public signature.

**Why timeout = MinInterval (Pitfall 3):** A sensor's `Sense()` could hang on an external HTTP call without timeout. We bound it with `context.WithTimeout(ctx, spec.MinInterval)` because the user explicitly stated "I want this to evaluate at least every MinInterval" — a Sense() call that exceeds MinInterval already violates the user's contract. Documented in evaluate.go comment.

**Why panic recovery is mandatory (Pitfall 2):** A panic in user-supplied Sense() code would kill the goroutine; without recover, the scheduler subcommand silently stops evaluating sensors. `defer recover()` converts the panic into a typed error for the same handleError path as a returned error. Test `TestSensorPanicRecovery` validates this.

**Why two-layer dedup (D-07):** RunKey alone fails when user code has a bug (returns the same key twice for genuinely different events). Cooldown alone fails when user code is intentionally noisy (legitimate same-key events within cooldown). Belt-and-suspenders: BOTH layers must allow the fire. RunKey check first (cheap string compare). Cooldown check second (time compare).

**Why auto-reset consecutive_failures on success (Claude's Discretion + 03-RESEARCH.md A5):** Per CONTEXT.md "auto-reset on success unless test data argues otherwise." Test fixture intentionally fails 5 times, succeeds once, fails 5 more times — confirms each "fail run" is independent (does not accumulate to disable). If we required N successes to reset, a flaky sensor would never auto-disable after self-recovery. Auto-reset is the safer default.

**Why Sensor×Partitions composition uses RunKey-as-partition-key (D-12 + Open Question 1):** When SensorResult.RunKey is set AND the asset has .Partitions, the runs.partition_key is `RunKey` directly **if it parses as a valid key for the strategy**. Otherwise (or RunKey empty), fall back to `partition.CurrentDailyKey(now, 24h)` for daily strategies. This lets the user explicitly target a specific partition per sensor fire. Pin this as a planning decision per 03-RESEARCH.md Open Question 1: "use CurrentKey(strategy, now) (which defaults to previous window). Document this as SensorSpec.DefaultPartitionOffset." Use the simpler convention here: explicit RunKey wins when it validates; else fallback to current.

**Why no separate database advisory lock for sensors (vs schedules):** Same SKIP LOCKED primitive on the `sensors` table works fine. Each sensor row is locked for the duration of its evaluation tx (could be up to MinInterval). Different sensors are not blocked from each other by SKIP LOCKED.

**Frozen interfaces consumed:**
- `internal/asset.DefinitionRegistry`, `Asset.Sensors()`, `Asset.Partitions()` (plan 03-02)
- `internal/asset.SensorSpec`, `SensorResult`, `SensorFunc` (plan 03-02)
- `internal/partition.*` (plan 03-02)
- `internal/event.Writer.Append`, `EventTypeSensor*` constants (plan 03-01)
- `internal/storage.Storage.DB()` (Phase 1)

**Frozen interfaces produced (consumed by plan 03-06 scheduler subcommand):**
- `sensor.Daemon` struct + `RunOnce(ctx)` method
- `sensor.UpsertSensors(ctx, store, registry)` function
- `sensor.AutoDisableThreshold` constant (default 60)

@.planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md
@.planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md
@.planning/phases/03-scheduling-sensors-partitions/03-VALIDATION.md
@internal/asset/asset.go
@internal/asset/registry.go
@internal/event/types.go

<interfaces>
<!-- Plan 03-01 + 03-02 surfaces this plan consumes. -->

From plan 03-01 (sensors table — VERBATIM from ent schema):
```
id UUID, asset_name VARCHAR(256), sensor_name VARCHAR(128), min_interval_seconds INT8 DEFAULT 30,
last_evaluated_at TIMESTAMPTZ NULL, last_fired_at TIMESTAMPTZ NULL, last_run_key VARCHAR(256) NULL,
cooldown_until TIMESTAMPTZ NULL, consecutive_failures INT DEFAULT 0, disabled_at TIMESTAMPTZ NULL,
created_at, updated_at
```

From plan 03-01 (events):
```go
EventTypeSensorEvaluated        = "sensor.evaluated"
EventTypeSensorFired            = "sensor.fired"
EventTypeSensorEvaluationFailed = "sensor.evaluation_failed"
EventTypeSensorDisabled         = "sensor.disabled"
EventTypeSensorCooldownSkipped  = "sensor.cooldown_skipped"
EventTypeSensorDedupSkipped     = "sensor.dedup_skipped"
```

From plan 03-02 (asset SDK):
```go
type SensorSpec struct {
    Name        string
    MinInterval time.Duration  // 0 → 30s default at evaluate time
    Cooldown    time.Duration  // 0 → no cooldown
    Sense       SensorFunc
}
type SensorResult struct {
    Fired   bool
    RunKey  string
    Payload map[string]any
}
type SensorFunc func(ctx context.Context) (SensorResult, error)
func (a *Asset) Sensors() []SensorSpec
func (a *Asset) Partitions() partition.PartitionStrategy
```

This plan produces:
```go
package sensor

const DefaultMinInterval = 30 * time.Second
const AutoDisableThreshold = 60  // consecutive failures before disabled_at is set (D-08)

type Daemon struct {
    Store    storage.Storage
    Registry *asset.DefinitionRegistry
    Events   event.Writer
    // AutoDisable threshold; 0 → DefaultAutoDisable
    DisableAfter int
}

// RunOnce evaluates all currently-due sensors. Returns when no more rows are due.
// Designed to be called from the scheduler subcommand's tick loop.
func (d *Daemon) RunOnce(ctx context.Context) error

func UpsertSensors(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry) error

// safeEvaluate is exported for testability — production callers use evaluateOneSensor.
func safeEvaluate(ctx context.Context, spec asset.SensorSpec, timeout time.Duration) (asset.SensorResult, error)
```
</interfaces>
</context>

<tasks>

<task id="3.5.1" type="auto" tdd="true">
  <name>Task 1: Create internal/sensor/evaluate.go — safeEvaluate (timeout + panic recovery) + handleResult (dedup + cooldown + enqueue) + handleError (failure counting + auto-disable)</name>
  <files>internal/sensor/evaluate.go, internal/sensor/evaluate_test.go</files>
  <read_first>
    - internal/asset/asset.go (SensorSpec, SensorResult, SensorFunc — plan 03-02)
    - internal/event/types.go (EventTypeSensor* constants — plan 03-01)
    - internal/partition/keygen.go (CurrentDailyKey, DailyKey — plan 03-02)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 5 — Sensor Evaluation Harness (verbatim)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pitfall 2, Pitfall 3 — panic recovery + timeout
    - .planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md § D-06, D-07, D-08
  </read_first>
  <behavior>
    - safeEvaluate(ctx, spec, timeout) calls spec.Sense in a goroutine-safe manner with context.WithTimeout(ctx, timeout); panics in spec.Sense are converted to errors via defer recover()
    - safeEvaluate timeout default: max(spec.MinInterval, DefaultMinInterval) when timeout is 0
    - handleResult on Fired=true:
      - If sensorRow.LastRunKey == result.RunKey AND result.RunKey != "" → emit sensor.dedup_skipped, return (no INSERT)
      - Else if sensorRow.CooldownUntil != nil AND NOW() < *sensorRow.CooldownUntil → emit sensor.cooldown_skipped, return (no INSERT)
      - Else: INSERT runs (state='queued', trigger='sensor', priority='normal', partition_key=resolveSensorPartitionKey(...)), UPDATE sensors SET last_run_key, last_fired_at=NOW(), cooldown_until=NOW()+spec.Cooldown, consecutive_failures=0; same tx
    - handleResult on Fired=false: just UPDATE sensors SET last_evaluated_at=NOW(), consecutive_failures=0 (success without fire still resets failure counter)
    - handleError on Sense() error or panic:
      - Emit sensor.evaluation_failed event with error message
      - UPDATE sensors SET consecutive_failures = consecutive_failures + 1, last_evaluated_at = NOW(), disabled_at = (CASE WHEN consecutive_failures+1 >= $threshold THEN NOW() ELSE disabled_at END)
      - If post-update consecutive_failures+1 >= threshold: emit sensor.disabled event
    - resolveSensorPartitionKey(strategy, runKey): if strategy nil → ""; else if RunKey is non-empty AND a syntactically valid key for strategy → RunKey; else fallback to CurrentDailyKey(now, 24h) for daily, WeeklyKey(now-7d) for weekly, MonthlyKey(now-1mo) for monthly, "" for category strategy with empty runKey
  </behavior>
  <action>
    1. Create `internal/sensor/evaluate.go`:
       ```go
       // Package sensor implements the sensor evaluation harness (D-05..D-08).
       package sensor

       import (
           "context"
           "database/sql"
           "errors"
           "fmt"
           "time"

           "github.com/google/uuid"
           "github.com/kanpon/data-governance/internal/asset"
           "github.com/kanpon/data-governance/internal/event"
           "github.com/kanpon/data-governance/internal/partition"
           "github.com/kanpon/data-governance/internal/storage"
       )

       const (
           DefaultMinInterval   = 30 * time.Second
           AutoDisableThreshold = 60  // D-08 default
       )

       // ErrNoDueSensor is returned by evaluateOneSensor when no due sensor row exists.
       var ErrNoDueSensor = errors.New("sensor: no due sensor")

       // safeEvaluate wraps SensorFunc with a timeout-bounded ctx and panic recovery.
       // Pitfall 2: a panic in user code must not crash the daemon.
       // Pitfall 3: an unbounded Sense() call must not block the tick loop.
       //
       // timeout defaults to spec.MinInterval (or DefaultMinInterval if MinInterval==0)
       // when the caller passes 0 — the user has acknowledged that "evaluate at least
       // this often" implies "Sense() must complete within this window."
       func safeEvaluate(ctx context.Context, spec asset.SensorSpec, timeout time.Duration) (result asset.SensorResult, err error) {
           if timeout == 0 {
               timeout = spec.MinInterval
               if timeout < DefaultMinInterval {
                   timeout = DefaultMinInterval
               }
           }
           evalCtx, cancel := context.WithTimeout(ctx, timeout)
           defer cancel()

           defer func() {
               if r := recover(); r != nil {
                   err = fmt.Errorf("sensor %q panic: %v", spec.Name, r)
               }
           }()
           return spec.Sense(evalCtx)
       }

       // sensorRow is the state read from the sensors table for evaluation.
       type sensorRow struct {
           ID                  uuid.UUID
           AssetName           string
           SensorName          string
           MinIntervalSeconds  int64
           LastRunKey          sql.NullString
           CooldownUntil       sql.NullTime
           ConsecutiveFailures int
       }

       // evaluateOneSensor selects the next due sensor with FOR UPDATE SKIP LOCKED, calls safeEvaluate,
       // and applies handleResult or handleError in the same transaction.
       // Returns ErrNoDueSensor when no rows are due.
       func evaluateOneSensor(
           ctx context.Context, store storage.Storage,
           reg *asset.DefinitionRegistry, events event.Writer,
           disableAfter int,
       ) error {
           db := store.DB()
           tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
           if err != nil {
               return fmt.Errorf("sensor.evaluate: begin tx: %w", err)
           }
           defer func() { _ = tx.Rollback() }()

           // Select due sensor: NOT disabled AND (never evaluated OR last_evaluated_at + min_interval <= NOW()).
           const selectSQL = `
               SELECT id, asset_name, sensor_name, min_interval_seconds, last_run_key, cooldown_until, consecutive_failures
               FROM sensors
               WHERE disabled_at IS NULL
                 AND (last_evaluated_at IS NULL
                      OR last_evaluated_at + (min_interval_seconds * interval '1 second') <= NOW())
               ORDER BY last_evaluated_at NULLS FIRST
               FOR UPDATE SKIP LOCKED
               LIMIT 1
           `
           var row sensorRow
           if err := tx.QueryRowContext(ctx, selectSQL).Scan(
               &row.ID, &row.AssetName, &row.SensorName, &row.MinIntervalSeconds,
               &row.LastRunKey, &row.CooldownUntil, &row.ConsecutiveFailures,
           ); err != nil {
               if errors.Is(err, sql.ErrNoRows) {
                   return ErrNoDueSensor
               }
               return fmt.Errorf("sensor.evaluate: select due: %w", err)
           }

           // Resolve SensorSpec from registry.
           a, err := reg.Get(row.AssetName)
           if err != nil || a == nil {
               // Asset definition gone — disable this sensor row.
               return autoDisableOrphan(ctx, tx, events, row.ID, row.AssetName, row.SensorName)
           }
           var spec *asset.SensorSpec
           for i := range a.Sensors() {
               s := a.Sensors()[i]
               if s.Name == row.SensorName {
                   spec = &s
                   break
               }
           }
           if spec == nil {
               return autoDisableOrphan(ctx, tx, events, row.ID, row.AssetName, row.SensorName)
           }

           // Evaluate.
           result, evalErr := safeEvaluate(ctx, *spec, 0)

           if evalErr != nil {
               return handleError(ctx, tx, events, &row, evalErr, disableAfter)
           }

           // Always emit sensor.evaluated (audit trail) — best effort, post-commit.
           defer func() {
               _ = events.Append(context.Background(), event.Event{
                   Type: event.EventTypeSensorEvaluated,
                   OccurredAt: time.Now().UTC(),
                   ResourceType: "sensor",
                   ResourceID:   row.ID.String(),
                   Payload: map[string]any{
                       "asset_name":  row.AssetName,
                       "sensor_name": row.SensorName,
                       "fired":       result.Fired,
                   },
               })
           }()

           if !result.Fired {
               return updateSensorOnNoFire(ctx, tx, &row)
           }
           return handleFired(ctx, tx, events, a, &row, *spec, result)
       }

       // autoDisableOrphan disables a sensor row whose asset/sensor was removed from the registry.
       func autoDisableOrphan(ctx context.Context, tx *sql.Tx, events event.Writer, sensorID uuid.UUID, assetName, sensorName string) error {
           _, err := tx.ExecContext(ctx, `UPDATE sensors SET disabled_at = NOW(), updated_at = NOW() WHERE id = $1`, sensorID)
           if err != nil {
               return fmt.Errorf("sensor.evaluate: disable orphan: %w", err)
           }
           if err := tx.Commit(); err != nil {
               return err
           }
           _ = events.Append(context.Background(), event.Event{
               Type: event.EventTypeSensorDisabled,
               OccurredAt: time.Now().UTC(),
               ResourceType: "sensor",
               ResourceID:   sensorID.String(),
               Payload: map[string]any{"asset_name": assetName, "sensor_name": sensorName, "reason": "orphaned"},
           })
           return nil
       }

       // updateSensorOnNoFire updates last_evaluated_at and resets consecutive_failures (D-08 auto-reset).
       func updateSensorOnNoFire(ctx context.Context, tx *sql.Tx, row *sensorRow) error {
           _, err := tx.ExecContext(ctx,
               `UPDATE sensors SET last_evaluated_at = NOW(), consecutive_failures = 0, updated_at = NOW() WHERE id = $1`,
               row.ID)
           if err != nil {
               return fmt.Errorf("sensor.evaluate: update no-fire: %w", err)
           }
           return tx.Commit()
       }

       // handleFired applies the two-layer dedup (D-07): RunKey check, then cooldown check.
       // If both pass, INSERT runs row + UPDATE sensors row in same tx.
       func handleFired(
           ctx context.Context, tx *sql.Tx, events event.Writer,
           a *asset.Asset, row *sensorRow, spec asset.SensorSpec, result asset.SensorResult,
       ) error {
           now := time.Now().UTC()

           // Layer 1: RunKey dedup.
           if result.RunKey != "" && row.LastRunKey.Valid && row.LastRunKey.String == result.RunKey {
               // Update last_evaluated_at, do NOT enqueue.
               _, _ = tx.ExecContext(ctx,
                   `UPDATE sensors SET last_evaluated_at = NOW(), consecutive_failures = 0, updated_at = NOW() WHERE id = $1`,
                   row.ID)
               if err := tx.Commit(); err != nil {
                   return err
               }
               _ = events.Append(context.Background(), event.Event{
                   Type: event.EventTypeSensorDedupSkipped,
                   OccurredAt: now,
                   ResourceType: "sensor",
                   ResourceID:   row.ID.String(),
                   Payload: map[string]any{"asset_name": row.AssetName, "sensor_name": row.SensorName, "run_key": result.RunKey},
               })
               return nil
           }

           // Layer 2: cooldown.
           if row.CooldownUntil.Valid && now.Before(row.CooldownUntil.Time) {
               _, _ = tx.ExecContext(ctx,
                   `UPDATE sensors SET last_evaluated_at = NOW(), consecutive_failures = 0, updated_at = NOW() WHERE id = $1`,
                   row.ID)
               if err := tx.Commit(); err != nil {
                   return err
               }
               _ = events.Append(context.Background(), event.Event{
                   Type: event.EventTypeSensorCooldownSkipped,
                   OccurredAt: now,
                   ResourceType: "sensor",
                   ResourceID:   row.ID.String(),
                   Payload: map[string]any{"asset_name": row.AssetName, "sensor_name": row.SensorName, "cooldown_until": row.CooldownUntil.Time},
               })
               return nil
           }

           // Pass both layers → enqueue.
           runID := uuid.New()
           partitionKey := resolveSensorPartitionKey(a.Partitions(), result.RunKey, now)
           var pkArg interface{} = nil
           if partitionKey != "" {
               pkArg = partitionKey
           }
           if _, err := tx.ExecContext(ctx,
               `INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority, partition_key)
                VALUES ($1, $2, 'queued', 'sensor', NOW(), 'normal', $3)`,
               runID, row.AssetName, pkArg,
           ); err != nil {
               // Likely partial-unique-index violation — sensor fired for an in-flight partition.
               // Treat as cooldown-skipped (avoid spamming runs table).
               return fmt.Errorf("sensor.handleFired: insert run (likely in-flight collision): %w", err)
           }

           cooldownUntil := now.Add(spec.Cooldown)
           if _, err := tx.ExecContext(ctx,
               `UPDATE sensors
                   SET last_evaluated_at = NOW(),
                       last_fired_at     = NOW(),
                       last_run_key      = $1,
                       cooldown_until    = $2,
                       consecutive_failures = 0,
                       updated_at        = NOW()
                 WHERE id = $3`,
               sql.NullString{String: result.RunKey, Valid: result.RunKey != ""}, cooldownUntil, row.ID,
           ); err != nil {
               return fmt.Errorf("sensor.handleFired: update sensor: %w", err)
           }
           if err := tx.Commit(); err != nil {
               return err
           }
           _ = events.Append(context.Background(), event.Event{
               Type: event.EventTypeSensorFired,
               OccurredAt: now,
               ResourceType: "sensor",
               ResourceID:   row.ID.String(),
               Payload: map[string]any{
                   "asset_name":    row.AssetName,
                   "sensor_name":   row.SensorName,
                   "run_id":        runID.String(),
                   "run_key":       result.RunKey,
                   "partition_key": partitionKey,
                   "payload":       result.Payload,
               },
           })
           return nil
       }

       // handleError increments consecutive_failures and auto-disables at the threshold (D-08).
       // Auto-resets on subsequent successful evaluation (handled in handleFired/updateSensorOnNoFire).
       func handleError(ctx context.Context, tx *sql.Tx, events event.Writer, row *sensorRow, evalErr error, disableAfter int) error {
           threshold := disableAfter
           if threshold <= 0 {
               threshold = AutoDisableThreshold
           }
           const updateSQL = `
               UPDATE sensors
                  SET consecutive_failures = consecutive_failures + 1,
                      last_evaluated_at    = NOW(),
                      updated_at           = NOW(),
                      disabled_at          = CASE
                          WHEN consecutive_failures + 1 >= $1 THEN NOW()
                          ELSE disabled_at
                      END
                WHERE id = $2
               RETURNING consecutive_failures, disabled_at
           `
           var newFailures int
           var disabledAt sql.NullTime
           if err := tx.QueryRowContext(ctx, updateSQL, threshold, row.ID).Scan(&newFailures, &disabledAt); err != nil {
               return fmt.Errorf("sensor.handleError: update: %w", err)
           }
           if err := tx.Commit(); err != nil {
               return err
           }
           _ = events.Append(context.Background(), event.Event{
               Type: event.EventTypeSensorEvaluationFailed,
               OccurredAt: time.Now().UTC(),
               ResourceType: "sensor",
               ResourceID:   row.ID.String(),
               Payload: map[string]any{
                   "asset_name":           row.AssetName,
                   "sensor_name":          row.SensorName,
                   "error":                evalErr.Error(),
                   "consecutive_failures": newFailures,
               },
           })
           if disabledAt.Valid && newFailures >= threshold {
               _ = events.Append(context.Background(), event.Event{
                   Type: event.EventTypeSensorDisabled,
                   OccurredAt: time.Now().UTC(),
                   ResourceType: "sensor",
                   ResourceID:   row.ID.String(),
                   Payload: map[string]any{
                       "asset_name":           row.AssetName,
                       "sensor_name":          row.SensorName,
                       "consecutive_failures": newFailures,
                       "threshold":            threshold,
                   },
               })
           }
           return nil
       }

       // resolveSensorPartitionKey returns runs.partition_key for a sensor-fired run.
       // If RunKey is non-empty AND validates for the strategy, use it; else fall back to current window.
       func resolveSensorPartitionKey(strategy partition.PartitionStrategy, runKey string, now time.Time) string {
           if strategy == nil {
               return ""
           }
           switch s := strategy.(type) {
           case partition.DailyPartitions:
               // Validate runKey format YYYY-MM-DD.
               if runKey != "" {
                   if _, err := time.Parse("2006-01-02", runKey); err == nil {
                       return runKey
                   }
               }
               return partition.CurrentDailyKey(now, 24*time.Hour)
           case partition.WeeklyPartitions:
               // Validate format YYYY-Wnn — fall back to current ISO week.
               return partition.WeeklyKey(now.Add(-7 * 24 * time.Hour))
           case partition.MonthlyPartitions:
               return partition.MonthlyKey(now.AddDate(0, -1, 0))
           case partition.CategoryPartitions:
               // For category, RunKey must be one of the declared keys.
               if runKey != "" {
                   for _, k := range s.Keys {
                       if k == runKey {
                           return runKey
                       }
                   }
               }
               // No fallback for category — return "" (will produce non-partitioned run; caller may treat as error).
               return ""
           }
           return ""
       }
       ```
    2. Create `internal/sensor/evaluate_test.go` with the validation map's required tests:
       - `TestSensorPanicRecovery` — `safeEvaluate(ctx, SensorSpec{Sense: func(ctx context.Context) (SensorResult, error) { panic("boom") }})` returns error containing "panic: boom" and does NOT propagate the panic.
       - `TestSensorTimeoutEnforced` — `Sense` blocks for 5s with `MinInterval=50ms`; `safeEvaluate` returns ctx.DeadlineExceeded within ~100ms.
       - `TestResolveSensorPartitionKey` — DailyPartitions + valid RunKey "2024-01-15" → "2024-01-15"; DailyPartitions + invalid RunKey "foo" → CurrentDailyKey(now, 24h); CategoryPartitions{Keys:[]string{"us"}} + RunKey "us" → "us"; CategoryPartitions + RunKey "eu" (not in keys) → "".
       - `TestSensorRunKeyDedup` (integration) — set up a sensors row with last_run_key="K1"; SensorFunc returns Fired=true, RunKey="K1"; assert no runs row inserted, sensor.dedup_skipped event captured.
       - `TestSensorCooldown` (integration) — sensors row with cooldown_until=NOW()+10min; SensorFunc returns Fired=true, RunKey=""; assert no runs row inserted, sensor.cooldown_skipped event captured.
       - `TestSensorFire` (integration — validation map: TestSensorFire) — sensors row with no last_run_key, no cooldown; SensorFunc returns Fired=true, RunKey="K2"; assert one runs row inserted with trigger='sensor', priority='normal', partition_key="" (no .Partitions); sensor row updated (last_run_key="K2", last_fired_at=NOW()); sensor.fired event captured.
       - `TestSensorAutoDisable` (integration) — set up a sensors row with consecutive_failures=AutoDisableThreshold-1; SensorFunc returns error; assert sensor row's disabled_at != NULL after one evaluation, sensor.disabled event captured.
       - `TestSensorAutoResetOnSuccess` (integration) — sensors row with consecutive_failures=10; SensorFunc returns Fired=false (success, no fire); assert post-evaluation consecutive_failures=0.
       Use a `fakeEventWriter` helper consistent with plan 03-04's pattern.
  </action>
  <acceptance_criteria>
    - `grep -q 'package sensor' internal/sensor/evaluate.go`
    - `grep -q 'func safeEvaluate' internal/sensor/evaluate.go`
    - `grep -q 'context.WithTimeout' internal/sensor/evaluate.go`
    - `grep -q 'recover()' internal/sensor/evaluate.go`
    - `grep -q 'func handleFired' internal/sensor/evaluate.go`
    - `grep -q 'func handleError' internal/sensor/evaluate.go`
    - `grep -q 'last_run_key' internal/sensor/evaluate.go`
    - `grep -q 'cooldown_until' internal/sensor/evaluate.go`
    - `grep -q 'consecutive_failures + 1 >= ' internal/sensor/evaluate.go`
    - `grep -q "trigger='sensor'\\|trigger.*sensor" internal/sensor/evaluate.go` (sensor-triggered run)
    - `grep -q 'EventTypeSensorDedupSkipped' internal/sensor/evaluate.go`
    - `grep -q 'EventTypeSensorCooldownSkipped' internal/sensor/evaluate.go`
    - `grep -q 'EventTypeSensorFired' internal/sensor/evaluate.go`
    - `grep -q 'EventTypeSensorEvaluationFailed' internal/sensor/evaluate.go`
    - `grep -q 'EventTypeSensorDisabled' internal/sensor/evaluate.go`
    - `grep -q 'AutoDisableThreshold' internal/sensor/evaluate.go`
    - `go test ./internal/sensor/... -run TestSensorPanicRecovery -count=1 -timeout 30s` exits 0
    - `go test ./internal/sensor/... -run TestSensorTimeoutEnforced -count=1 -timeout 30s` exits 0
    - `go test ./internal/sensor/... -run TestResolveSensorPartitionKey -count=1 -timeout 30s` exits 0
    - `DATABASE_URL=... go test ./internal/sensor/... -run TestSensorRunKeyDedup -count=1 -timeout 60s` exits 0
    - `DATABASE_URL=... go test ./internal/sensor/... -run TestSensorCooldown -count=1 -timeout 60s` exits 0
    - `DATABASE_URL=... go test ./internal/sensor/... -run TestSensorFire -count=1 -timeout 60s` exits 0
    - `DATABASE_URL=... go test ./internal/sensor/... -run TestSensorAutoDisable -count=1 -timeout 60s` exits 0
  </acceptance_criteria>
  <verify>
    <automated>DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./internal/sensor/... -count=1 -timeout 120s</automated>
  </verify>
  <done>safeEvaluate has timeout + panic recovery; handleFired implements two-layer dedup with proper events; handleError increments consecutive_failures with auto-disable at threshold and auto-reset on success; resolveSensorPartitionKey validates RunKey before using it; all 8 tests pass.</done>
</task>

<task id="3.5.2" type="auto" tdd="true">
  <name>Task 2: Create internal/sensor/daemon.go (RunOnce driver) + internal/sensor/registry.go (UpsertSensors) + tests</name>
  <files>internal/sensor/daemon.go, internal/sensor/daemon_test.go, internal/sensor/registry.go</files>
  <read_first>
    - internal/schedule/registry.go (UpsertSchedules pattern from plan 03-04 — mirror the SELECT-then-INSERT/UPDATE approach)
    - internal/schedule/daemon.go (Daemon.tick pattern — mirror the loop-until-no-rows behavior)
    - internal/sensor/evaluate.go (just created) — evaluateOneSensor signature, ErrNoDueSensor sentinel
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 5
  </read_first>
  <behavior>
    - Daemon.RunOnce(ctx) loops calling evaluateOneSensor until ErrNoDueSensor is returned
    - On other errors (DB transient), log and exit RunOnce — caller's next tick retries
    - UpsertSensors iterates registry.All(), for each Asset.Sensors() spec: SELECT id FROM sensors WHERE asset_name=$1 AND sensor_name=$2; if found UPDATE min_interval_seconds when changed; else INSERT new row with default zero values
    - Daemon.RunOnce returns nil after a clean drain
    - TestDaemonRunOnceDrains — set up 3 due sensor rows; assert RunOnce processes all 3 in one call (each evaluation fires Fired=false, sensors get last_evaluated_at=NOW())
    - TestUpsertSensors — register an asset with one SensorSpec; call UpsertSensors; assert one sensors row inserted; call again with same spec; assert no second row, no error (idempotent)
    - TestUpsertSensorsMinIntervalUpdate — register sensor with MinInterval=30s; UpsertSensors; change registry to MinInterval=60s; UpsertSensors; assert sensors.min_interval_seconds=60
  </behavior>
  <action>
    1. Create `internal/sensor/registry.go`:
       ```go
       package sensor

       import (
           "context"
           "database/sql"
           "errors"
           "fmt"

           "github.com/google/uuid"
           "github.com/kanpon/data-governance/internal/asset"
           "github.com/kanpon/data-governance/internal/storage"
       )

       // UpsertSensors reconciles asset.DefinitionRegistry → sensors table.
       // Idempotent: identical specs cause no update; changed MinInterval is propagated.
       // Removed sensors are NOT deleted from the table — they are left to be evaluated
       // and orphan-disabled by evaluateOneSensor (consistent with schedules behavior).
       func UpsertSensors(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry) error {
           db := store.DB()
           for _, a := range reg.All() {
               specs := a.Sensors()
               for _, spec := range specs {
                   if err := upsertOneSensor(ctx, db, a.Name(), spec); err != nil {
                       return fmt.Errorf("sensor.upsert(%s/%s): %w", a.Name(), spec.Name, err)
                   }
               }
           }
           return nil
       }

       func upsertOneSensor(ctx context.Context, db *sql.DB, assetName string, spec asset.SensorSpec) error {
           minIntervalSec := int64(spec.MinInterval.Seconds())
           if minIntervalSec <= 0 {
               minIntervalSec = int64(DefaultMinInterval.Seconds())
           }
           const selectSQL = `SELECT id, min_interval_seconds FROM sensors WHERE asset_name=$1 AND sensor_name=$2 LIMIT 1`
           var (
               id              uuid.UUID
               existingMinIvl  int64
           )
           err := db.QueryRowContext(ctx, selectSQL, assetName, spec.Name).Scan(&id, &existingMinIvl)
           if err == nil {
               if existingMinIvl == minIntervalSec {
                   return nil // unchanged
               }
               const updateSQL = `UPDATE sensors SET min_interval_seconds=$1, updated_at=NOW() WHERE id=$2`
               _, err = db.ExecContext(ctx, updateSQL, minIntervalSec, id)
               return err
           }
           if !errors.Is(err, sql.ErrNoRows) {
               return err
           }
           const insertSQL = `
               INSERT INTO sensors (id, asset_name, sensor_name, min_interval_seconds, consecutive_failures, created_at, updated_at)
               VALUES (gen_random_uuid(), $1, $2, $3, 0, NOW(), NOW())
           `
           _, err = db.ExecContext(ctx, insertSQL, assetName, spec.Name, minIntervalSec)
           return err
       }
       ```
    2. Create `internal/sensor/daemon.go`:
       ```go
       package sensor

       import (
           "context"
           "errors"
           "log/slog"

           "github.com/kanpon/data-governance/internal/asset"
           "github.com/kanpon/data-governance/internal/event"
           "github.com/kanpon/data-governance/internal/storage"
       )

       type Daemon struct {
           Store        storage.Storage
           Registry     *asset.DefinitionRegistry
           Events       event.Writer
           DisableAfter int  // 0 → AutoDisableThreshold default
       }

       // RunOnce drains the sensor evaluation queue. Designed to be called from the
       // scheduler subcommand's tick loop — the scheduler tick does schedule firing
       // then sensor evaluation, both backed by SKIP LOCKED.
       func (d *Daemon) RunOnce(ctx context.Context) error {
           for {
               if ctx.Err() != nil {
                   return ctx.Err()
               }
               err := evaluateOneSensor(ctx, d.Store, d.Registry, d.Events, d.DisableAfter)
               if errors.Is(err, ErrNoDueSensor) {
                   return nil
               }
               if err != nil {
                   slog.Error("sensor.evaluate_failed", "error", err)
                   return nil  // back off; next tick retries
               }
           }
       }
       ```
    3. Create `internal/sensor/daemon_test.go`:
       - `TestDaemonRunOnceDrains` — set up 3 due sensors (3 SensorSpec with MinInterval=1ns to ensure all due), each Sense returns Fired=false. Call RunOnce. Assert each sensor's last_evaluated_at is set.
       - `TestUpsertSensors` — register asset with SensorSpec; call UpsertSensors; SELECT count from sensors → 1. Call again → still 1.
       - `TestUpsertSensorsMinIntervalUpdate` — registry SensorSpec MinInterval changes between calls; assert min_interval_seconds in DB updated.
       - `TestRunOnceContextCancellation` — start RunOnce with already-canceled ctx, assert returns within 50ms with ctx.Canceled error.
  </action>
  <acceptance_criteria>
    - `grep -q 'type Daemon struct' internal/sensor/daemon.go`
    - `grep -q 'func (d \\*Daemon) RunOnce' internal/sensor/daemon.go`
    - `grep -q 'func UpsertSensors' internal/sensor/registry.go`
    - `grep -q 'INSERT INTO sensors' internal/sensor/registry.go`
    - `DATABASE_URL=... go test ./internal/sensor/... -run TestDaemonRunOnceDrains -count=1 -timeout 60s` exits 0
    - `DATABASE_URL=... go test ./internal/sensor/... -run TestUpsertSensors -count=1 -timeout 60s` exits 0
    - `go build ./...` passes
  </acceptance_criteria>
  <verify>
    <automated>DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./internal/sensor/... -count=1 -timeout 120s</automated>
  </verify>
  <done>internal/sensor package complete with Daemon (RunOnce) + UpsertSensors; idempotent upsert preserves rows on identical specs and updates MinInterval changes; full sensor test suite passes.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| User code (Sense function) → sensor.safeEvaluate | Untrusted user goroutine; timeout + panic recovery is the safety wrapper |
| Sense() result → handleFired enqueue path | RunKey + cooldown + partial unique index combine to prevent duplicate runs |
| Multiple sensor.Daemon replicas → sensors table | SKIP LOCKED multi-replica safety |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-03-05-01 | Denial of Service | Sense() panics and crashes daemon (Pitfall 2) | mitigate | safeEvaluate uses defer recover() to convert panics to errors; TestSensorPanicRecovery validates. |
| T-03-05-02 | Denial of Service | Sense() blocks indefinitely on external HTTP call (Pitfall 3) | mitigate | context.WithTimeout(ctx, spec.MinInterval) bounds every Sense() call. Documented contract: Sense() MUST respect ctx cancellation. TestSensorTimeoutEnforced validates. |
| T-03-05-03 | Denial of Service | Sense() always returns Fired=true with the same RunKey, spamming the runs table | mitigate | Two-layer dedup (D-07): RunKey check + cooldown check. Both must pass before enqueue. TestSensorRunKeyDedup and TestSensorCooldown validate. Additionally the partial unique index on runs (asset_name, partition_key) prevents duplicate in-flight runs. |
| T-03-05-04 | Denial of Service | Buggy Sense() returns error every tick — sensors row pegged in evaluation_failed events forever | mitigate | After AutoDisableThreshold (default 60) consecutive failures, sensor.disabled_at is set; subsequent ticks skip it (`WHERE disabled_at IS NULL` in selectSQL). TestSensorAutoDisable validates. |
| T-03-05-05 | Tampering | Sense() returns RunKey crafted to bypass dedup (e.g., random UUID per call) | accept | This is intended user behavior — a sensor that fires for genuinely distinct events SHOULD use distinct RunKeys. Defense-in-depth: Cooldown layer 2 enforces minimum time between fires; user controls Cooldown. |
| T-03-05-06 | Information Disclosure | SensorResult.Payload may leak credentials via event_log | accept | Per D-06, Payload is opaque user data. Event log is RLS-protected (Phase 1 D-09). Documented: do not put secrets in Payload. |
| T-03-05-07 | Spoofing | One sensor row claimed by two replicas | mitigate | SELECT FOR UPDATE SKIP LOCKED on sensors table — same primitive as schedule firing and run claiming. |
| T-03-05-08 | Tampering | sensors.consecutive_failures incremented via direct SQL | accept | platform_app role has full DML on sensors; same trust model as runs/schedules. Future enhancement: add a Postgres CHECK constraint or trigger to enforce monotone increase, but not Phase 3 work. |
</threat_model>

<verification>
- `go build ./...` passes.
- `DATABASE_URL=... go test ./internal/sensor/... -count=1 -timeout 120s` passes (8+ tests).
- safeEvaluate panic recovery test passes WITHOUT a database (pure unit test).
- TestSensorAutoDisable fires exactly N=AutoDisableThreshold-1 + 1 = threshold-th evaluation and observes disabled_at set + sensor.disabled event emitted.
- Phase 2 50-goroutine atomicity test still passes (regression — sensor changes do not touch runs claim path).
</verification>

<success_criteria>
- internal/sensor package exists with daemon, evaluate, registry components.
- safeEvaluate enforces timeout = max(spec.MinInterval, DefaultMinInterval) and recovers panics.
- handleFired implements RunKey dedup (layer 1) + cooldown dedup (layer 2) + INSERT runs in same tx as UPDATE sensors.
- handleError increments consecutive_failures and sets disabled_at at threshold; auto-resets on success.
- resolveSensorPartitionKey validates RunKey before using as partition_key; falls back to current window for time strategies; rejects unrecognized category keys.
- UpsertSensors idempotently syncs registry → sensors table.
- All sensor tests (panic recovery, timeout, RunKey dedup, cooldown, fire, auto-disable, auto-reset) pass.
</success_criteria>

<output>
After completion, create `.planning/phases/03-scheduling-sensors-partitions/03-05-SUMMARY.md` documenting:
- Final sensor package surface (Daemon, RunOnce, UpsertSensors).
- Two-layer dedup behavior — quote handleFired's check order.
- Auto-disable threshold default (60) and auto-reset semantics.
- Decision-coverage: D-05 (sensors share scheduler subcommand — Daemon.RunOnce), D-06 (SensorResult contract), D-07 (two-layer dedup), D-08 (auto-disable + auto-reset), D-12 (Sensor×Partitions composition via resolveSensorPartitionKey).
</output>
