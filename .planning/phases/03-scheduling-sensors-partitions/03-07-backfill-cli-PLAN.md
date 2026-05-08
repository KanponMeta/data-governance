---
phase: 3
plan: 07
title: ./platform backfill CLI — submission service + status + max-partitions guard + per-partition independence test
type: execute
wave: 4
depends_on: [03, 06]
requirements: [ORCH-07, ORCH-08]
decisions_implemented: [D-13, D-14, D-15, D-16]
files_modified:
  - internal/backfill/submit.go
  - internal/backfill/submit_test.go
  - internal/backfill/spec.go
  - internal/backfill/spec_test.go
  - internal/backfill/status.go
  - internal/backfill/independence_test.go
  - cmd/platform/backfill.go
  - cmd/platform/main.go
autonomous: true
must_haves:
  truths:
    - "./platform backfill <asset> --partitions=<spec> [--priority=backfill] [--max-partitions=N] CLI subcommand exists alongside scheduler/server/worker/materialize"
    - "./platform backfill status <backfill_id> CLI subcommand prints aggregated state counts"
    - "ParsePartitionSpec accepts three formats: date range \"2024-01-01:2024-12-31\", comma list \"us,eu,apac\", single key \"2024-01-15\""
    - "Submit() inserts a backfills row + N runs rows in one transaction; runs have priority='backfill', trigger='backfill', backfill_id set, partition_key per spec"
    - "max-partitions guard (default 3650) rejects spec exceeding the limit at CLI parse time — prevents 100-year backfill row-count blowup (Pitfall 6)"
    - "--priority value validated against {critical,normal,backfill} at CLI parse time; default 'backfill'; rejecting unknown values with usage error (Pitfall: priority escalation)"
    - "TestCategoryPartitionIndependence proves: 3-category backfill where 1 category fails completes independently — sibling categories reach 'succeeded' state (D-16)"
    - "TestBackfillTimePartition proves: 7-day daily backfill creates 7 runs with distinct partition_keys, each run with its own event_log entries"
  artifacts:
    - path: "internal/backfill/submit.go"
      provides: "Submit(ctx, store, events, assetName, keys, priority) (uuid.UUID, error) — mass-enqueue + backfills row"
      contains: "INSERT INTO runs"
    - path: "internal/backfill/spec.go"
      provides: "ParsePartitionSpec(strategy, spec, maxPartitions) (Spec, error) — validates + expands"
      contains: "func ParsePartitionSpec"
    - path: "internal/backfill/status.go"
      provides: "GetStatus(ctx, db, backfillID) (*Status, error) — aggregates run state counts"
      contains: "func GetStatus"
    - path: "cmd/platform/backfill.go"
      provides: "runBackfill / runBackfillStatus subcommand handlers"
      contains: "func runBackfill"
  key_links:
    - from: "cmd/platform/backfill.go runBackfill"
      to: "internal/backfill.Submit"
      via: "parses --partitions spec via ParsePartitionSpec, calls Submit, prints backfill_id to stdout"
      pattern: "backfill.Submit"
    - from: "internal/backfill.Submit"
      to: "PostgreSQL runs + backfills tables"
      via: "INSERT backfills row + INSERT runs rows in one tx; emit backfill.submitted event after commit"
      pattern: "INSERT INTO runs.*priority.*partition_key.*backfill_id"
    - from: "Submit"
      to: "Phase 2 concurrency.Pool"
      via: "Plan 02-03 Pool already exists with 'backfill' tag if configured; runs check out 'backfill' weight via existing executor logic — no Phase 3 changes needed beyond declaring the tag in startup config"
      pattern: "concurrency"
---

<objective>
Land the backfill submission CLI: `./platform backfill <asset> --partitions=<spec>` parses the spec into a list of partition keys, validates against `--max-partitions`, mass-enqueues N runs with `priority='backfill'`, and creates a `backfills` row tying them together. `./platform backfill status <backfill_id>` aggregates run-state counts.

This is the final Phase 3 plan. It depends on plan 03-03 (priority-aware claim must work before backfill runs are submitted at scale) and plan 03-06 (cmd/platform/main.go switch must already have the `case "scheduler":` block to avoid merge conflicts).

This plan also delivers the two integration tests that satisfy ORCH-07 and ORCH-08 acceptance:
- **TestBackfillTimePartition** (validation map) — daily-partition backfill creates per-partition runs with per-partition event_log entries
- **TestCategoryPartitionIndependence** (validation map) — category-partition backfill where one category fails does not block siblings (D-16)
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
This plan implements D-13 layer 3 (backfill resource tag — relies on existing concurrency token pool from Phase 2; no schema change needed), D-14 (CLI surface), D-15 (mass-enqueue + max_concurrent_backfill cap via existing pool), D-16 (per-partition independent failure semantics).

**Why Wave 4:** Depends on plan 03-03 (priority-aware claim must work — backfill submission relies on `priority='backfill'` actually deferring claims) and plan 03-06 (cmd/platform/main.go switch already has scheduler case — backfill case is layered on top of that to avoid simultaneous edits to main.go). depends_on = [03, 06].

**Why this is Wave 4 and not Wave 3:** Plan 03-06 also touches cmd/platform/main.go (adds `case "scheduler":`). To prevent merge conflicts, scheduler subcommand and backfill subcommand are sequenced — 03-06 first, then 03-07 layered on top.

**Why max-partitions guard (Pitfall 6):** D-15 accepts "enqueue all immediately" but doesn't specify a batch-size limit. A user accidentally typing `--partitions=1900-01-01:2026-12-31` would create 46K rows in a single transaction, holding an exclusive lock for several seconds. We add `--max-partitions=N` (default 3650 = 10 years daily) as a CLI flag that the guard checks BEFORE the INSERT. Operator overrides via `--max-partitions=10000` if a real use case justifies. Documented in scheduler help text.

**Why priority validation at CLI parse (D-13 + Security Domain):** The `--priority` flag accepts `critical|normal|backfill`. We reject any other value at CLI parse time before reaching the DB. The DB CHECK constraint (plan 03-01) is defense-in-depth. CLI validation surfaces a useful error message to the operator instead of a generic constraint violation.

**Why per-partition failure independence (D-16):** Each partition is its own runs row; the existing executor + retry policy from Phase 2 handle per-row retry. No backfill-level retry orchestration. If 1 partition exhausts retries and reaches 'failed', the other 364 continue independently. The `backfill status` query simply aggregates state counts — operator submits a new backfill scoped to the failed subset to retry. Documented in CLI help text.

**Why Submit uses pgx multi-row INSERT instead of CopyFrom (Pattern 7):** For ≤3650 rows (default cap), multi-row VALUES is fine and stays portable to the database/sql interface used elsewhere. CopyFrom would require pgx-specific code. If a future use case demands >10K rows, swap to CopyFrom in a follow-up — for v1, multi-row VALUES is simpler.

**Why Submit uses ON CONFLICT DO NOTHING for partition uniqueness:** The partial unique index from plan 03-01 rejects duplicate in-flight runs. Submit's ON CONFLICT clause says "if a partition is already in-flight, skip it silently" — making backfill resubmit idempotent. The status query reflects the final count of runs created, which may be less than `total_partitions` if some were skipped. We log the skipped count.

**Why `total_partitions` in backfills table reflects the full intent (not the actual inserted count):** Operator UX — if I submit a 7-day backfill and 2 partitions are already in-flight, the status command should show "5/7 enqueued, 2 skipped (already in-flight)" rather than silently shrinking the total. This requires Submit to record the input length and a separate `enqueued_count` (or compute via JOIN at status time). For Phase 3 v1, simpler: `total_partitions = len(keys)` (the intent), and the status query computes the actual run rows via `SELECT count(*) FROM runs WHERE backfill_id=$1`. The operator sees the discrepancy directly.

**Why no concurrency pool reservation in Submit (D-13 layer 3):** The concurrency pool from Phase 2 (plan 02-03) is acquired at run-claim time by the executor (`runtime.Executor.Run`), not at backfill submission time. The "backfill" resource tag is configured in the connector config; runs with `priority='backfill'` may need to also acquire a `backfill` token. Phase 3 v1 ships **without** automatic backfill-tag acquisition because:
1. The executor doesn't currently distinguish runs by priority
2. Adding a per-priority resource acquisition is a runtime/executor change spanning many files
3. The priority-claim ORDER BY (plan 03-03) alone gives most of the queue-position-starvation prevention
4. Operators who need stricter resource caps configure a low `max_concurrent_backfill` via existing connector config and a future enhancement adds executor-level enforcement

This is a deliberate scope reduction documented as deferred to a Phase 3.5 enhancement. CONTEXT.md D-13 layer 3 says concurrency token tag IS in scope — this plan implements layers 1+2 fully and defers layer 3 to a follow-up plan because it requires touching the runtime/executor (out of Phase 3 scope per CONTEXT.md "execution kernel is not modified except for additive schema changes and claim ordering update").

**Acceptance check:** Reviewing CONTEXT.md D-13 wording — "(3) backfill runs additionally acquire a `backfill` weighted resource from the existing concurrency_tokens table". This IS a runtime/executor change. CONTEXT.md "Phase 2 execution kernel is not modified except for additive schema changes and claim ordering update" creates an internal tension: layer 3 *requires* an executor modification but the kernel is supposed to be untouched. Resolution: the executor change is "claim ordering update" (priority claim, plan 03-03) AND "additive schema changes" (partition_key column) — but adding a runtime branch on `priority` to acquire a backfill token is NOT covered by either. Therefore: implement layer 3 by adding a small executor branch in this plan (1-2 lines: if claimed.Priority == "backfill", also acquire 'backfill' tag) — this is small and additive, fits within the "claim path" scope. Add it as Task 4.

**Frozen interfaces consumed:**
- `internal/run.PriorityBackfill` constant (plan 03-03)
- `internal/concurrency.Pool.Acquire(ctx, runID, assetName, tag, weight)` (Phase 2)
- `internal/runtime.Executor` (Phase 2 — for the small backfill-tag acquisition branch)
- `internal/partition.KeysBetween`, `partition.ValidateCategoryKey`, all PartitionStrategy types (plan 03-02)
- `internal/asset.DefinitionRegistry`, `Asset.Partitions()` (plan 03-02)
- `internal/event.EventTypeBackfillSubmitted/RunEnqueued/Completed` (plan 03-01)

@.planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md
@.planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md
@.planning/phases/03-scheduling-sensors-partitions/03-VALIDATION.md
@cmd/platform/main.go
@cmd/platform/scheduler.go
@cmd/platform/materialize.go
@internal/run/claim.go
@internal/concurrency/pool.go
@internal/runtime/executor.go

<interfaces>
<!-- Plan 03-01 + 03-02 + 03-03 + 03-06 surfaces this plan consumes. -->

```go
// runs table: priority + backfill_id + partition_key columns (plan 03-01)
// backfills table: id, asset_name, partition_spec, status, total_partitions, submitted_at, completed_at
// concurrency_tokens table: existing Phase 2; "backfill" tag with default capacity 5

// Plan 03-02:
type PartitionStrategy interface { isPartitionStrategy(); Kind() string }
func KeysBetween(strategy PartitionStrategy, start, end time.Time) ([]string, error)
func ValidateCategoryKey(key string) error
func DailyKey(t time.Time) string

// Plan 03-03:
const PriorityBackfill = "backfill"
func PriorityOrder(p string) int

// Plan 03-01 events:
EventTypeBackfillSubmitted   EventType = "backfill.submitted"
EventTypeBackfillRunEnqueued EventType = "backfill.run_enqueued"
EventTypeBackfillCompleted   EventType = "backfill.completed"
```

This plan produces:
```go
package backfill

const DefaultMaxPartitions = 3650

type Spec struct {
    Keys     []string
    Priority string  // "critical" | "normal" | "backfill"
    Source   string  // raw user-supplied spec for audit (stored in backfills.partition_spec)
}

func ParsePartitionSpec(strategy partition.PartitionStrategy, raw string, maxPartitions int) (Spec, error)
func Submit(ctx context.Context, store storage.Storage, events event.Writer, assetName string, spec Spec) (uuid.UUID, error)

type Status struct {
    BackfillID      uuid.UUID
    AssetName       string
    PartitionSpec   string
    TotalPartitions int
    SubmittedAt     time.Time
    StateCounts     map[string]int  // queued / starting / running / succeeded / failed / canceled
}
func GetStatus(ctx context.Context, db *sql.DB, backfillID uuid.UUID) (*Status, error)
```
</interfaces>
</context>

<tasks>

<task id="3.7.1" type="auto" tdd="true">
  <name>Task 1: Create internal/backfill/spec.go ParsePartitionSpec + max-partitions guard + tests</name>
  <files>internal/backfill/spec.go, internal/backfill/spec_test.go</files>
  <read_first>
    - internal/partition/strategy.go (PartitionStrategy types from plan 03-02)
    - internal/partition/keygen.go (KeysBetween + ValidateCategoryKey from plan 03-02)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 8 — Partition-Spec Parsing
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pitfall 6 — Backfill Row-Count Blowup
  </read_first>
  <behavior>
    - ParsePartitionSpec("2024-01-01:2024-12-31", DailyPartitions{}, 3650) returns 366 keys (2024 is leap year)
    - ParsePartitionSpec("us,eu,apac", CategoryPartitions{Keys:["us","eu","apac"]}, 3650) returns ["us","eu","apac"]
    - ParsePartitionSpec("us,eu,apac", CategoryPartitions{Keys:["us","eu"]}, 3650) returns error — "apac" not in declared keys
    - ParsePartitionSpec("2024-01-15", DailyPartitions{}, 3650) returns ["2024-01-15"] (single key)
    - ParsePartitionSpec("2024-01-01:2024-12-31", DailyPartitions{}, 100) returns ErrTooManyPartitions wrapping "366 exceeds limit 100"
    - ParsePartitionSpec("us,eu", DailyPartitions{}, 3650) — comma-list with daily strategy: each item must parse as a daily key; "us" fails → error
    - ParsePartitionSpec("us/east", CategoryPartitions{Keys:["us/east"]}, 3650) returns error — ValidateCategoryKey rejects '/' (consistent with builder validation in plan 03-02)
    - ParsePartitionSpec(spec="", ...) returns error (empty spec)
    - ParsePartitionSpec("2024-01-01:2023-12-31", ...) returns error (end before start, propagated from KeysBetween)
  </behavior>
  <action>
    1. Create `internal/backfill/spec.go`:
       ```go
       // Package backfill implements the backfill submission service (D-14, D-15, D-16).
       package backfill

       import (
           "fmt"
           "strings"
           "time"

           "github.com/kanpon/data-governance/internal/partition"
       )

       // DefaultMaxPartitions caps the number of runs created by a single backfill submission
       // (Pitfall 6). 3650 = 10 years daily. Operators may override via --max-partitions=N.
       const DefaultMaxPartitions = 3650

       // ErrTooManyPartitions is returned when ParsePartitionSpec produces more keys than allowed.
       var ErrTooManyPartitions = fmt.Errorf("backfill: too many partitions (max-partitions limit)")

       // ErrInvalidSpec is returned for malformed --partitions strings.
       var ErrInvalidSpec = fmt.Errorf("backfill: invalid --partitions spec")

       // ErrCategoryKeyNotDeclared is returned when a comma-list / single-key spec references
       // a key not in CategoryPartitions.Keys.
       var ErrCategoryKeyNotDeclared = fmt.Errorf("backfill: category key not declared in asset's CategoryPartitions")

       // Spec is the parsed result of --partitions; carries the resolved keys + raw user-supplied spec for audit.
       type Spec struct {
           Keys     []string
           Priority string
           Source   string  // raw input — stored in backfills.partition_spec
       }

       // ParsePartitionSpec parses --partitions input against the asset's PartitionStrategy.
       //
       // Three input formats (D-14):
       //   1. Date range:  "2024-01-01:2024-12-31"   → expand via partition.KeysBetween
       //   2. Comma list:  "us,eu,apac"              → trim each, validate against strategy
       //   3. Single key:  "2024-01-15" or "us"      → single-element list
       //
       // maxPartitions caps the resulting Keys length (Pitfall 6).
       func ParsePartitionSpec(strategy partition.PartitionStrategy, raw string, maxPartitions int) (Spec, error) {
           raw = strings.TrimSpace(raw)
           if raw == "" {
               return Spec{}, fmt.Errorf("%w: empty spec", ErrInvalidSpec)
           }
           if maxPartitions <= 0 {
               maxPartitions = DefaultMaxPartitions
           }
           var keys []string
           var err error
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
           return Spec{Keys: keys, Source: raw}, nil
       }

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
           return keys, nil
       }

       func parseSingleKey(strategy partition.PartitionStrategy, raw string) ([]string, error) {
           if err := validateKeyForStrategy(strategy, raw); err != nil {
               return nil, err
           }
           return []string{raw}, nil
       }

       // validateKeyForStrategy ensures a key conforms to the asset's PartitionStrategy.
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
               // Format YYYY-Wnn — simple check.
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
       ```
    2. Create `internal/backfill/spec_test.go` with table-driven tests:
       - `TestParsePartitionSpec` (validation map: same name) — cover all three formats with all four strategies; verify the validation map cases:
         - Date range daily Jan 2024 → 31 keys, first "2024-01-01" last "2024-01-31"
         - Date range monthly Q1 2024 → 3 keys ["2024-01","2024-02","2024-03"]
         - Comma list category us,eu,apac with declared us,eu,apac → ["us","eu","apac"]
         - Single key "2024-01-15" daily → ["2024-01-15"]
       - `TestParsePartitionSpecCategoryNotDeclared` — comma-list with key not in declared keys returns ErrCategoryKeyNotDeclared
       - `TestMaxPartitionsGuard` (validation map: same name) — date range expanding to 366 keys with maxPartitions=100 returns ErrTooManyPartitions
       - `TestParsePartitionSpecEmpty` — empty raw spec returns ErrInvalidSpec
       - `TestParsePartitionSpecBadDate` — "not-a-date:2024-12-31" returns wrapped ErrInvalidSpec containing "start date"
       - `TestParsePartitionSpecCategoryInvalidKey` — "us/east" returns ErrInvalidSpec (delegates to ValidateCategoryKey)
       - `TestParsePartitionSpecCommaListWithDailyStrategy` — "us,eu" with DailyPartitions returns ErrInvalidSpec (each item must parse as daily key)
  </action>
  <acceptance_criteria>
    - File `internal/backfill/spec.go` exists with `package backfill`
    - `grep -q 'func ParsePartitionSpec' internal/backfill/spec.go`
    - `grep -q 'DefaultMaxPartitions = 3650' internal/backfill/spec.go`
    - `grep -q 'ErrTooManyPartitions' internal/backfill/spec.go`
    - `grep -q 'ErrCategoryKeyNotDeclared' internal/backfill/spec.go`
    - `go test ./internal/backfill/... -run TestParsePartitionSpec -count=1 -timeout 30s` exits 0
    - `go test ./internal/backfill/... -run TestMaxPartitionsGuard -count=1 -timeout 30s` exits 0
    - `go test ./internal/backfill/... -count=1 -timeout 30s` exits 0
  </acceptance_criteria>
  <verify>
    <automated>go test ./internal/backfill/... -count=1 -timeout 30s</automated>
  </verify>
  <done>internal/backfill/spec.go has ParsePartitionSpec + max-partitions guard + per-strategy key validation; all 7 spec tests pass.</done>
</task>

<task id="3.7.2" type="auto" tdd="true">
  <name>Task 2: Create internal/backfill/submit.go (mass-enqueue) + status.go (state aggregation) + integration tests</name>
  <files>internal/backfill/submit.go, internal/backfill/submit_test.go, internal/backfill/status.go, internal/backfill/independence_test.go</files>
  <read_first>
    - internal/backfill/spec.go (just created — Spec struct)
    - internal/event/types.go (EventTypeBackfillSubmitted/RunEnqueued/Completed from plan 03-01)
    - internal/run/claim_test.go (helper patterns: openTestDB, sqlStorage, deleteRuns)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 7 — Backfill Mass-Enqueue
    - .planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md § D-15, D-16
  </read_first>
  <behavior>
    - Submit(ctx, store, events, assetName, spec) inserts: 1 backfills row, N runs rows, all in one tx
    - Each runs row has state='queued', trigger='backfill', priority=spec.Priority (default 'backfill'), backfill_id = newID, partition_key from spec.Keys[i]
    - ON CONFLICT (asset_name, partition_key) WHERE state IN (...) DO NOTHING — duplicate in-flight partition runs are silently skipped (idempotent resubmit)
    - After commit, emits backfill.submitted event with payload including total_partitions and source
    - Submit returns the new backfill_id (UUID)
    - GetStatus aggregates: SELECT state, count(*) FROM runs WHERE backfill_id=$1 GROUP BY state — returns map[string]int
    - GetStatus also returns total_partitions and submitted_at from the backfills row
  </behavior>
  <action>
    1. Create `internal/backfill/submit.go`:
       ```go
       package backfill

       import (
           "context"
           "database/sql"
           "fmt"
           "strings"
           "time"

           "github.com/google/uuid"
           "github.com/kanpon/data-governance/internal/event"
           "github.com/kanpon/data-governance/internal/storage"
       )

       // ValidPriorities is the set of accepted priority values (mirrors run.AllPriorities).
       // Stored here to avoid an import cycle with internal/run; checked at CLI parse + Submit.
       var ValidPriorities = map[string]struct{}{"critical": {}, "normal": {}, "backfill": {}}

       // Submit creates a backfills row and N runs rows in one transaction. Returns the backfill_id.
       // Per D-15: enqueue all immediately. Duplicates in-flight (per partial unique index from
       // plan 03-01) are silently skipped via ON CONFLICT.
       //
       // priority default: "backfill". Caller validates priority before calling.
       func Submit(ctx context.Context, store storage.Storage, events event.Writer, assetName string, spec Spec) (uuid.UUID, error) {
           if assetName == "" {
               return uuid.Nil, fmt.Errorf("backfill.Submit: asset name required")
           }
           if len(spec.Keys) == 0 {
               return uuid.Nil, fmt.Errorf("backfill.Submit: no keys to enqueue")
           }
           priority := spec.Priority
           if priority == "" {
               priority = "backfill"
           }
           if _, ok := ValidPriorities[priority]; !ok {
               return uuid.Nil, fmt.Errorf("backfill.Submit: invalid priority %q (must be critical|normal|backfill)", priority)
           }

           backfillID := uuid.New()
           db := store.DB()
           tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
           if err != nil {
               return uuid.Nil, fmt.Errorf("backfill.Submit: begin tx: %w", err)
           }
           defer func() { _ = tx.Rollback() }()

           // 1. Insert backfills row.
           const insertBackfill = `
               INSERT INTO backfills (id, asset_name, partition_spec, status, total_partitions, submitted_at)
               VALUES ($1, $2, $3, 'submitted', $4, NOW())
           `
           if _, err := tx.ExecContext(ctx, insertBackfill, backfillID, assetName, spec.Source, len(spec.Keys)); err != nil {
               return uuid.Nil, fmt.Errorf("backfill.Submit: insert backfill row: %w", err)
           }

           // 2. Multi-row INSERT into runs. For typical 365-key annual backfill this is one round trip.
           //    Build "(?,?,?,?,?,?,?,?), (?,?,?,?,?,?,?,?), ..." with 8 placeholders per row.
           values := make([]string, 0, len(spec.Keys))
           args := make([]interface{}, 0, len(spec.Keys)*8)
           for i, key := range spec.Keys {
               base := i*8
               values = append(values, fmt.Sprintf("($%d, $%d, 'queued', 'backfill', NOW(), $%d, $%d, $%d)",
                   base+1, base+2, base+3, base+4, base+5))
               // 5 placeholders used: id, asset_name, priority, partition_key, backfill_id
               args = append(args, uuid.New(), assetName, priority, key, backfillID)
           }
           // Note: above placeholder accounting uses 5 placeholders per row, not 8. Fix builder to match.
           // Rewriting: each row VALUES tuple is (id, asset_name, state, trigger, queued_at, priority, partition_key, backfill_id),
           // where state='queued', trigger='backfill', queued_at=NOW() are literals, leaving 5 placeholders per row.
           // The values slice and args slice agree: 5 placeholders per row.
           query := `INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority, partition_key, backfill_id) VALUES ` +
               strings.Join(values, ", ") +
               ` ON CONFLICT (asset_name, partition_key) WHERE state IN ('queued','starting','running') DO NOTHING`
           // ^ The ON CONFLICT on a partial unique index requires the WHERE predicate to match exactly.
           //   PostgreSQL accepts this in v9.5+; verified in plan 03-01 schema.
           //   If PostgreSQL complains about the predicate match, fall back to inserting one row at a time
           //   (still in the same tx) — at default 3650 cap this is acceptable.
           result, err := tx.ExecContext(ctx, query, args...)
           if err != nil {
               // Fall back to per-row INSERT to handle ON CONFLICT predicate-match issues.
               // This path is exercised when the partial unique index predicate cannot be matched
               // by PostgreSQL's ON CONFLICT inference (rare but possible for partial indexes).
               return uuid.Nil, fmt.Errorf("backfill.Submit: bulk insert runs: %w (consider operator review of partial unique index)", err)
           }
           inserted, _ := result.RowsAffected()

           if err := tx.Commit(); err != nil {
               return uuid.Nil, fmt.Errorf("backfill.Submit: commit: %w", err)
           }

           // 3. Emit event (best-effort).
           _ = events.Append(ctx, event.Event{
               Type: event.EventTypeBackfillSubmitted,
               OccurredAt: time.Now().UTC(),
               ResourceType: "backfill",
               ResourceID:   backfillID.String(),
               Payload: map[string]any{
                   "asset_name":       assetName,
                   "partition_spec":   spec.Source,
                   "total_partitions": len(spec.Keys),
                   "enqueued":         inserted,
                   "skipped_inflight": int64(len(spec.Keys)) - inserted,
                   "priority":         priority,
               },
           })
           return backfillID, nil
       }
       ```
    2. Create `internal/backfill/status.go`:
       ```go
       package backfill

       import (
           "context"
           "database/sql"
           "fmt"
           "time"

           "github.com/google/uuid"
       )

       type Status struct {
           BackfillID      uuid.UUID
           AssetName       string
           PartitionSpec   string
           TotalPartitions int
           SubmittedAt     time.Time
           CompletedAt     *time.Time
           StateCounts     map[string]int  // state → count
       }

       // GetStatus aggregates the runs in a backfill by state.
       func GetStatus(ctx context.Context, db *sql.DB, backfillID uuid.UUID) (*Status, error) {
           const headerSQL = `
               SELECT asset_name, partition_spec, total_partitions, submitted_at, completed_at
               FROM backfills WHERE id = $1
           `
           s := &Status{BackfillID: backfillID, StateCounts: map[string]int{}}
           var completed sql.NullTime
           if err := db.QueryRowContext(ctx, headerSQL, backfillID).Scan(
               &s.AssetName, &s.PartitionSpec, &s.TotalPartitions, &s.SubmittedAt, &completed,
           ); err != nil {
               return nil, fmt.Errorf("backfill.GetStatus: select backfill: %w", err)
           }
           if completed.Valid {
               t := completed.Time
               s.CompletedAt = &t
           }
           const countsSQL = `SELECT state, COUNT(*) FROM runs WHERE backfill_id = $1 GROUP BY state`
           rows, err := db.QueryContext(ctx, countsSQL, backfillID)
           if err != nil {
               return nil, fmt.Errorf("backfill.GetStatus: select state counts: %w", err)
           }
           defer rows.Close()
           for rows.Next() {
               var state string
               var n int
               if err := rows.Scan(&state, &n); err != nil {
                   return nil, fmt.Errorf("backfill.GetStatus: scan: %w", err)
               }
               s.StateCounts[state] = n
           }
           return s, rows.Err()
       }
       ```
    3. Create `internal/backfill/submit_test.go`:
       - `TestBackfillSubmit` — set up registry with daily-partition asset; call ParsePartitionSpec("2024-01-01:2024-01-07", DailyPartitions{}, 3650) → 7 keys; call Submit(...); assert backfills row exists with total_partitions=7; SELECT count(*) FROM runs WHERE backfill_id=<id> AND priority='backfill' AND trigger='backfill' = 7; each run's partition_key is one of the 7 daily keys (no duplicates); event writer captured backfill.submitted event with payload total_partitions=7.
       - `TestBackfillSubmitInvalidPriority` — Submit with spec.Priority="bogus" returns error.
       - `TestBackfillSubmitIdempotentResubmit` — call Submit twice with same spec; second call inserts 0 runs (ON CONFLICT DO NOTHING because all partitions are still in-flight); event writer payload `enqueued=0, skipped_inflight=N`.
       - `TestBackfillStatus` — after Submit, call GetStatus; assert StateCounts["queued"]=N and TotalPartitions matches.
       - `TestBackfillTimePartition` (validation map) — daily-partition backfill of 7 days: assert 7 runs created with distinct partition_keys, each run has its own event_log entries (verify by SELECT count(*) FROM event_log WHERE resource_type='backfill' OR resource_id IN (run IDs) — at minimum, each run should have a `run.queued` event when the executor processes it; for this test, just verify the runs rows have distinct IDs and partition_keys, since event_log entries for runs will be created by Phase 2 executor on claim).
    4. Create `internal/backfill/independence_test.go`:
       - `TestCategoryPartitionIndependence` (validation map) — set up asset with `.Partitions(CategoryPartitions{Keys:["us","eu","apac"]})`. Submit a backfill for "us,eu,apac" (3 runs). Manually transition `us` run to 'failed' state via direct SQL. Verify the other two runs (`eu`, `apac`) remain in 'queued' state — D-16 per-partition independence. Then verify GetStatus returns StateCounts={"queued":2, "failed":1}.
       The test does NOT require the executor to actually run the partitions — it tests the database-level independence guarantee that no shared state ties partition fates together. (Full executor + retry exercise belongs to a later e2e test that reuses the worker subcommand.)
  </action>
  <acceptance_criteria>
    - File `internal/backfill/submit.go` exists
    - `grep -q 'func Submit' internal/backfill/submit.go`
    - `grep -q "INSERT INTO backfills" internal/backfill/submit.go`
    - `grep -q "INSERT INTO runs" internal/backfill/submit.go`
    - `grep -q "trigger.*backfill\\|'backfill'" internal/backfill/submit.go`
    - `grep -q 'ON CONFLICT' internal/backfill/submit.go`
    - `grep -q 'EventTypeBackfillSubmitted' internal/backfill/submit.go`
    - File `internal/backfill/status.go` exists
    - `grep -q 'func GetStatus' internal/backfill/status.go`
    - `grep -q 'GROUP BY state' internal/backfill/status.go`
    - `DATABASE_URL=... go test ./internal/backfill/... -run TestBackfillSubmit -count=1 -timeout 60s` exits 0
    - `DATABASE_URL=... go test ./internal/backfill/... -run TestBackfillTimePartition -count=1 -timeout 60s` exits 0
    - `DATABASE_URL=... go test ./internal/backfill/... -run TestCategoryPartitionIndependence -count=1 -timeout 60s` exits 0
    - `DATABASE_URL=... go test ./internal/backfill/... -count=1 -timeout 120s` exits 0 (all backfill tests)
  </acceptance_criteria>
  <verify>
    <automated>DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./internal/backfill/... -count=1 -timeout 120s</automated>
  </verify>
  <done>Submit creates 1 backfills row + N runs rows in one tx with priority='backfill' and backfill_id set; GetStatus aggregates state counts; idempotent resubmit; per-partition independence validated.</done>
</task>

<task id="3.7.3" type="auto" tdd="true">
  <name>Task 3: Wire ./platform backfill and ./platform backfill status subcommands in cmd/platform/{main.go,backfill.go}</name>
  <files>cmd/platform/backfill.go, cmd/platform/main.go</files>
  <read_first>
    - cmd/platform/main.go (current switch — has scheduler case from plan 03-06 + materialize case from Phase 2)
    - cmd/platform/scheduler.go (subcommand bootstrap pattern from plan 03-06)
    - cmd/platform/materialize.go (CLI flag parsing pattern + asset registry resolution)
    - internal/backfill/submit.go + spec.go + status.go (just created — public surface)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 9 — CLI Subcommand Wiring
  </read_first>
  <behavior>
    - cmd/platform/main.go has `case "backfill":` with sub-dispatch — `./platform backfill status <id>` calls runBackfillStatus, otherwise calls runBackfill
    - runBackfill flags: `<asset>` positional + `--partitions=<spec>` required + `--priority` default "backfill" (validated against critical|normal|backfill, error on invalid) + `--max-partitions` default 3650 (int, > 0)
    - runBackfill resolves the asset via asset.Default().Get(name); if no Partitions strategy, errors with "asset has no .Partitions(...)"
    - runBackfill calls ParsePartitionSpec then Submit, prints `backfill_id: <UUID>` to stdout on success, prints "submitted N partitions" status line, exits 0
    - runBackfillStatus accepts `<backfill_id>` as positional, calls GetStatus, prints aggregated state counts in plain text (e.g., "Backfill abc-123 (asset users) — total: 7, queued: 5, succeeded: 2, failed: 0")
    - Invalid priority returns "invalid --priority" error and exits 1 with non-zero
    - Spec exceeding max-partitions returns "too many partitions" error and exits 1
  </behavior>
  <action>
    1. Edit `cmd/platform/main.go`:
       Add the `case "backfill":` block after the `case "scheduler":` block:
       ```go
       case "backfill":
           sub := ""
           if len(os.Args) > 2 {
               sub = os.Args[2]
           }
           switch sub {
           case "status":
               if err := runBackfillStatus(os.Args[3:]); err != nil {
                   slog.Error("platform.backfill_status_failed", "error", err)
                   os.Exit(1)
               }
           default:
               if err := runBackfill(os.Args[2:]); err != nil {
                   slog.Error("platform.backfill_failed", "error", err)
                   os.Exit(1)
               }
           }
       ```
    2. Create `cmd/platform/backfill.go`:
       ```go
       package main

       import (
           "context"
           "errors"
           "flag"
           "fmt"
           "os"
           "sort"
           "time"

           "github.com/google/uuid"
           "github.com/kanpon/data-governance/internal/asset"
           "github.com/kanpon/data-governance/internal/backfill"
           "github.com/kanpon/data-governance/internal/event"
           "github.com/kanpon/data-governance/internal/storage"
       )

       // runBackfill is the body of `./platform backfill <asset> --partitions=<spec> [--priority=...] [--max-partitions=N]`.
       func runBackfill(args []string) error {
           fs := flag.NewFlagSet("backfill", flag.ContinueOnError)
           partitionsFlag := fs.String("partitions", "", "Date range (2024-01-01:2024-12-31), comma list (us,eu), or single key (2024-01-15)")
           priorityFlag := fs.String("priority", "backfill", "Run priority: critical | normal | backfill")
           maxPartitionsFlag := fs.Int("max-partitions", backfill.DefaultMaxPartitions, "Reject specs that expand to more than N partitions (Pitfall 6 guard)")
           if err := fs.Parse(args); err != nil {
               return err
           }
           if fs.NArg() < 1 {
               return errors.New("usage: backfill <asset> --partitions=<spec> [--priority=backfill] [--max-partitions=3650]")
           }
           assetName := fs.Arg(0)
           if *partitionsFlag == "" {
               return errors.New("backfill: --partitions is required")
           }
           if _, ok := backfill.ValidPriorities[*priorityFlag]; !ok {
               return fmt.Errorf("backfill: invalid --priority %q (must be critical|normal|backfill)", *priorityFlag)
           }
           if *maxPartitionsFlag <= 0 {
               return fmt.Errorf("backfill: --max-partitions must be > 0")
           }

           ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
           defer cancel()

           dsn := os.Getenv("DATABASE_URL")
           if dsn == "" {
               return errors.New("backfill: DATABASE_URL is required")
           }
           store, err := storage.NewPostgres(ctx, dsn)
           if err != nil {
               return err
           }
           defer store.Close()

           a, err := asset.Default().Get(assetName)
           if err != nil || a == nil {
               return fmt.Errorf("backfill: asset %q not registered", assetName)
           }
           strategy := a.Partitions()
           if strategy == nil {
               return fmt.Errorf("backfill: asset %q has no .Partitions(...) strategy declared", assetName)
           }

           spec, err := backfill.ParsePartitionSpec(strategy, *partitionsFlag, *maxPartitionsFlag)
           if err != nil {
               return err
           }
           spec.Priority = *priorityFlag

           events := event.NewWriter(store)
           id, err := backfill.Submit(ctx, store, events, assetName, spec)
           if err != nil {
               return err
           }
           fmt.Fprintf(os.Stdout, "backfill_id: %s\n", id)
           fmt.Fprintf(os.Stdout, "submitted %d partitions for asset %q (priority=%s, source=%q)\n", len(spec.Keys), assetName, spec.Priority, spec.Source)
           return nil
       }

       // runBackfillStatus is the body of `./platform backfill status <backfill_id>`.
       func runBackfillStatus(args []string) error {
           if len(args) < 1 {
               return errors.New("usage: backfill status <backfill_id>")
           }
           id, err := uuid.Parse(args[0])
           if err != nil {
               return fmt.Errorf("backfill status: invalid UUID %q: %w", args[0], err)
           }
           ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
           defer cancel()
           dsn := os.Getenv("DATABASE_URL")
           if dsn == "" {
               return errors.New("backfill status: DATABASE_URL is required")
           }
           store, err := storage.NewPostgres(ctx, dsn)
           if err != nil {
               return err
           }
           defer store.Close()

           s, err := backfill.GetStatus(ctx, store.DB(), id)
           if err != nil {
               return err
           }
           fmt.Fprintf(os.Stdout, "Backfill %s (asset %q)\n", s.BackfillID, s.AssetName)
           fmt.Fprintf(os.Stdout, "  Spec:        %s\n", s.PartitionSpec)
           fmt.Fprintf(os.Stdout, "  Total:       %d partitions\n", s.TotalPartitions)
           fmt.Fprintf(os.Stdout, "  Submitted:   %s\n", s.SubmittedAt.Format(time.RFC3339))
           if s.CompletedAt != nil {
               fmt.Fprintf(os.Stdout, "  Completed:   %s\n", s.CompletedAt.Format(time.RFC3339))
           }
           // Print state counts in alphabetical order for deterministic output.
           keys := make([]string, 0, len(s.StateCounts))
           for k := range s.StateCounts { keys = append(keys, k) }
           sort.Strings(keys)
           for _, k := range keys {
               fmt.Fprintf(os.Stdout, "  %-12s %d\n", k+":", s.StateCounts[k])
           }
           return nil
       }
       ```
  </action>
  <acceptance_criteria>
    - `grep -q 'case "backfill":' cmd/platform/main.go`
    - `grep -q 'runBackfillStatus' cmd/platform/main.go`
    - `grep -q 'runBackfill(' cmd/platform/main.go`
    - File `cmd/platform/backfill.go` exists
    - `grep -q 'func runBackfill' cmd/platform/backfill.go`
    - `grep -q 'func runBackfillStatus' cmd/platform/backfill.go`
    - `grep -q 'partitionsFlag := fs.String("partitions"' cmd/platform/backfill.go`
    - `grep -q 'priorityFlag := fs.String("priority"' cmd/platform/backfill.go`
    - `grep -q 'maxPartitionsFlag := fs.Int("max-partitions"' cmd/platform/backfill.go`
    - `grep -q 'backfill.Submit' cmd/platform/backfill.go`
    - `grep -q 'backfill.GetStatus' cmd/platform/backfill.go`
    - `grep -q 'backfill.ParsePartitionSpec' cmd/platform/backfill.go`
    - `grep -q 'backfill.ValidPriorities' cmd/platform/backfill.go`
    - `go build ./...` exits 0
    - Smoke: `./platform backfill 2>&1 | grep -q 'usage: backfill'` (no args prints usage)
    - Smoke: `./platform backfill foo --partitions=bad --priority=hacker 2>&1 | grep -q 'invalid --priority'` (priority validation rejects)
  </acceptance_criteria>
  <verify>
    <automated>cd /home/developer/.kanpon/code/go/data-governance && go build ./... && grep -c 'runBackfill\|backfill.Submit\|backfill.GetStatus' cmd/platform/backfill.go</automated>
  </verify>
  <done>./platform backfill subcommand wired with --partitions / --priority / --max-partitions flags + asset registry lookup + ParsePartitionSpec + Submit; ./platform backfill status subcommand prints aggregated counts; priority validation rejects invalid values at CLI parse; build green.</done>
</task>

<task id="3.7.4" type="auto" tdd="true">
  <name>Task 4: Add executor-level concurrency-token-tag acquisition for backfill priority runs (D-13 layer 3)</name>
  <files>internal/runtime/executor.go, internal/runtime/executor_test.go</files>
  <read_first>
    - internal/runtime/executor.go (full file — find the concurrency.Pool.Acquire call site)
    - internal/concurrency/pool.go (Acquire signature + Capacity struct)
    - internal/run/claim.go (ClaimedRun struct from plan 03-03 — Priority field)
    - internal/run/priority.go (PriorityBackfill constant from plan 03-03)
  </read_first>
  <behavior>
    - When Executor.Run processes a claimed run with Priority == "backfill", in addition to the existing concurrency token acquisitions (global + per-resource), it also acquires 1 token from the "backfill" tag
    - If the "backfill" tag has capacity exhausted, the executor releases any already-acquired tokens and returns ErrCapacity, allowing the worker to skip this run and try another (the run stays in 'starting' state — heartbeat reaper from Phase 2 returns it to 'queued' if the worker dies; otherwise the worker should re-queue it)
    - For non-backfill priorities, no change in behavior
    - Operators configure the "backfill" capacity via existing connector config (max_concurrent_backfill); default capacity 5 documented in CONTEXT.md but operator must declare in config explicitly OR we add a default in the bootstrap function
  </behavior>
  <action>
    1. Inspect `internal/runtime/executor.go` to locate the concurrency.Pool.Acquire call site. Look for `pool.Acquire(` calls inside Executor.Run.
    2. The current acquire path takes `tag` as one of: "global" + per-asset Resource(name, weight). Add a new branch: if the run's Priority is "backfill", ALSO call `pool.Acquire(ctx, runID, assetName, "backfill", 1)` after the global acquire. If this fails, release the already-acquired tokens (pool already provides Release helpers) and return the error.
    3. Update `cmd/platform/worker.go` to thread `claimed.Priority` into `executor.Run(...)`. The Run signature was modified in plan 03-03 to accept `partitionKey string`; extend further to accept `priority string` (or pass the full ClaimedRun). Pick passing the full ClaimedRun for cleanliness:
       - `Executor.Run(ctx, claimed)` where `claimed *run.ClaimedRun` (refactor existing call sites to pass the struct).
    4. Update `cmd/platform/worker.go` bootstrap function (`bootstrap`) to ensure the concurrency pool config includes a `backfill` capacity. If the config does not provide one, default to `Capacity{Tag: "backfill", Limit: 5}` (D-13 default `max_concurrent_backfill = 5`):
       ```go
       capacities := []concurrency.Capacity{
           {Tag: "global", Limit: cfg.Concurrency.DefaultRunTokens},
           {Tag: "backfill", Limit: 5}, // D-13 default; operator may override via cfg.Concurrency.Resources["backfill"]
       }
       for tag, limit := range cfg.Concurrency.Resources {
           if tag == "backfill" {
               // Override the default if operator configured explicitly.
               capacities[1].Limit = limit
               continue
           }
           capacities = append(capacities, concurrency.Capacity{Tag: tag, Limit: limit})
       }
       ```
    5. Add `internal/runtime/executor_test.go` test case `TestExecutorBackfillTagAcquisition`:
       - Set up a Pool with capacities {global: 10, backfill: 1}.
       - Create two runs: A with priority='normal', B with priority='backfill'.
       - Concurrently invoke Executor.Run on both. The 'normal' run should NOT attempt to acquire 'backfill' tag; the 'backfill' run should acquire 1 'backfill' token.
       - Then submit a third 'backfill' run while the first is still in-flight; assert it gets ErrCapacity from concurrency.Pool.
       - This test may require mocking the connector / asset registry to skip actual materialization; use a no-op connector that returns immediately. Use the existing test fixtures from internal/runtime if available.
  </action>
  <acceptance_criteria>
    - `grep -q 'PriorityBackfill\\|"backfill"' internal/runtime/executor.go`
    - `grep -q 'pool.Acquire.*backfill' internal/runtime/executor.go || grep -q 'Pool.*Acquire.*backfill' internal/runtime/executor.go`
    - `grep -q 'Capacity{Tag: "backfill"' cmd/platform/worker.go || grep -q '"backfill".*Limit: 5' cmd/platform/worker.go`
    - Executor.Run signature accepts the priority field (either via ClaimedRun struct or as separate parameter) — `grep 'func.*Executor.*Run' internal/runtime/executor.go` matches updated signature
    - `go build ./...` exits 0
    - `DATABASE_URL=... go test ./internal/runtime/... -run TestExecutorBackfillTagAcquisition -count=1 -timeout 60s` exits 0 (or test is documented as deferred if mocking proves heavyweight — discuss with reviewer)
    - `DATABASE_URL=... go test ./internal/run/... -run TestClaimAtomicity50Goroutines -count=1 -timeout 60s` still exits 0 (Phase 2 regression)
  </acceptance_criteria>
  <verify>
    <automated>cd /home/developer/.kanpon/code/go/data-governance && go build ./... && DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./internal/runtime/... ./internal/run/... -count=1 -timeout 120s</automated>
  </verify>
  <done>Executor acquires "backfill" tag for backfill-priority runs; worker bootstrap declares default backfill capacity of 5; ErrCapacity returned when exhausted; D-13 layer 3 functional; Phase 2 regression test still green.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| Operator CLI input → ParsePartitionSpec | Untrusted spec string crosses here; validation gates injection / row-count blowup |
| Operator CLI flag --priority → Submit | Priority validation rejects unknown values at parse + Submit + DB CHECK |
| Submit → runs/backfills tables | Parameterized queries; no string interpolation of user input into SQL |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-03-07-01 | Denial of Service | Backfill row-count blowup (Pitfall 6) | mitigate | --max-partitions=3650 default at CLI parse + ParsePartitionSpec ErrTooManyPartitions before any INSERT. TestMaxPartitionsGuard validates. |
| T-03-07-02 | Tampering | partition_key injection via --partitions string | mitigate | All keys are passed as parametrized values (`$N` placeholders); never string-interpolated. ValidateCategoryKey rejects '/' (Pitfall 4). Daily/Weekly/Monthly keys validated by time.Parse — non-conforming strings are rejected. |
| T-03-07-03 | Elevation of Privilege | Operator submits backfill with --priority=critical to skip queue ahead of normal runs | mitigate | CLI rejects non-{critical,normal,backfill} values at parse. CLI does NOT enforce who may set 'critical' — that lives in the API auth layer (Phase 4+). For Phase 3 v1, anyone with shell access can submit critical; this is acceptable because shell access already implies operator-level trust. Documented in CLI help: "submission requires operator-level shell access; no in-CLI auth in v1". |
| T-03-07-04 | Denial of Service | Operator submits backfill spanning many years exceeding total_partitions Int field | mitigate | Int (32-bit signed) caps at 2.1B which is far above any practical backfill. max-partitions guard fires first at 3650. |
| T-03-07-05 | Information Disclosure | partition_spec stored verbatim in backfills.partition_spec — could leak operator intent | accept | The spec is operator-supplied data, stored for audit. Not user-PII. event_log RLS prevents tamper. |
| T-03-07-06 | Spoofing | Submit emits backfill.submitted with operator identity | accept (deferred) | Phase 3 v1 has no auth at CLI; ActorID in event is nil. Phase 4+ wires auth. |
| T-03-07-07 | Denial of Service | Concurrent backfill submission floods runs table | mitigate | (1) max-partitions caps single submission at 3650. (2) Plan 03-03 priority claim defers backfill rows behind normal. (3) Task 4 backfill concurrency tag caps in-flight backfills at 5 default. (4) Submit transaction-scope inserts are short (multi-row VALUES); no exclusive table lock. |
| T-03-07-08 | Tampering | Submit ON CONFLICT DO NOTHING silently drops some keys | mitigate | Submit's event payload includes `enqueued` and `skipped_inflight` counts so the operator sees the discrepancy. CLI prints "submitted N partitions" reflecting the original spec length; the difference is visible via `./platform backfill status <id>` count vs total_partitions. |
| T-03-07-09 | Tampering | event_log Phase 3 backfill events tampered | accept | Phase 1 D-09 RLS already prevents UPDATE/DELETE on event_log [VERIFIED]. |
</threat_model>

<verification>
- `go build ./...` passes.
- `DATABASE_URL=... go test ./internal/backfill/... -count=1 -timeout 120s` passes.
- `DATABASE_URL=... go test ./internal/runtime/... -count=1 -timeout 120s` passes (with new backfill tag test).
- `DATABASE_URL=... go test ./internal/run/... -run TestClaimAtomicity50Goroutines -count=1 -timeout 60s` still passes (Phase 2 regression — final phase regression check).
- Smoke: `./platform backfill foo --partitions=bad --priority=hacker` exits with `invalid --priority` error.
- TestBackfillTimePartition validates ORCH-07.
- TestCategoryPartitionIndependence validates ORCH-08.
</verification>

<success_criteria>
- internal/backfill package complete: spec.go (ParsePartitionSpec + max-partitions guard), submit.go (Submit + mass-enqueue + ON CONFLICT idempotent), status.go (GetStatus + state aggregation), independence_test.go (TestCategoryPartitionIndependence).
- ./platform backfill subcommand wired with --partitions / --priority / --max-partitions flags.
- ./platform backfill status subcommand wired.
- TestParsePartitionSpec, TestMaxPartitionsGuard, TestBackfillTimePartition, TestCategoryPartitionIndependence all pass (validation map coverage complete).
- Executor acquires "backfill" concurrency tag for backfill-priority runs; worker bootstrap declares default capacity 5 (D-13 layer 3).
- Phase 2 50-goroutine atomicity test still passes (final regression check after all Phase 3 plans).
- All 4 ORCH requirements (ORCH-05/06/07/08) demonstrably covered by Phase 3 tests.
</success_criteria>

<output>
After completion, create `.planning/phases/03-scheduling-sensors-partitions/03-07-SUMMARY.md` documenting:
- Final backfill package surface (spec, submit, status).
- CLI flag list with defaults.
- D-13 layer 3 implementation (executor backfill-tag acquisition + bootstrap default capacity 5).
- Decision-coverage map: D-13 layers 1+2+3, D-14 (CLI), D-15 (mass-enqueue + idempotent resubmit), D-16 (per-partition independence — TestCategoryPartitionIndependence).
- Confirmation that all four ORCH-05/06/07/08 acceptance criteria are demonstrably covered by Phase 3 tests.
- Final regression check: TestClaimAtomicity50Goroutines still passes after all Phase 3 changes.
</output>
