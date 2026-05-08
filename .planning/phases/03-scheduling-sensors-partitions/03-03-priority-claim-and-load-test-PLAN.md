---
phase: 3
plan: 03
title: Priority-aware claim ORDER BY + ClaimedRun struct extension + 1000-backfill+50-normal load test
type: execute
wave: 2
depends_on: [01]
requirements: [ORCH-05, ORCH-06, ORCH-07, ORCH-08]
decisions_implemented: [D-13]
files_modified:
  - internal/run/claim.go
  - internal/run/claim_test.go
  - internal/run/priority.go
  - internal/run/priority_test.go
autonomous: true
must_haves:
  truths:
    - "ClaimNext SQL ORDER BY clause is `CASE priority WHEN 'critical' THEN 0 WHEN 'normal' THEN 1 WHEN 'backfill' THEN 2 ELSE 1 END ASC, queued_at ASC`"
    - "ClaimNext SELECT projects partition_key, priority, backfill_id in addition to existing columns"
    - "ClaimedRun struct exposes PartitionKey *string, Priority string, BackfillID *uuid.UUID"
    - "Existing TestClaimAtomicity50Goroutines still passes after ORDER BY change (regression guard)"
    - "TestClaimPriorityOrdering proves: insert 5 backfill + 5 normal + 1 critical → claim order is critical, then 5 normals, then 5 backfills"
    - "TestPriorityClaimLoad proves: 1000 backfill + 50 normal + 50 concurrent claimers → first 50 claims are all 'normal', no duplicate claims, second 50 claims are all 'backfill'"
    - "PriorityOrder(string) int is the single source of truth for the integer mapping (Pitfall 5 — drift prevention)"
  artifacts:
    - path: "internal/run/claim.go"
      provides: "ClaimNext with priority-aware ORDER BY + ClaimedRun struct extension"
      contains: "CASE priority"
    - path: "internal/run/priority.go"
      provides: "Priority enum (critical|normal|backfill) + PriorityOrder() single source of truth"
      contains: "func PriorityOrder"
    - path: "internal/run/claim_test.go"
      provides: "TestClaimAtomicity50Goroutines (existing) + TestClaimPriorityOrdering (new) + TestPriorityClaimLoad (new)"
      contains: "TestClaimPriorityOrdering"
  key_links:
    - from: "internal/run.ClaimNext SELECT"
      to: "runs table partition_key/priority/backfill_id columns (plan 03-01)"
      via: "SELECT id, asset_name, trigger, queued_at, partition_key, priority, backfill_id FROM runs WHERE state='queued' ORDER BY <priority CASE>, queued_at FOR UPDATE SKIP LOCKED LIMIT 1"
      pattern: "SELECT.*partition_key, priority, backfill_id.*FROM runs"
    - from: "internal/run.PriorityOrder Go function"
      to: "claim.go SQL CASE expression"
      via: "Both encode critical=0, normal=1, backfill=2 — drift prevention test asserts agreement"
      pattern: "PriorityOrder.*critical.*normal.*backfill"
---

<objective>
Modify `internal/run/claim.go` to claim runs in priority order (`critical` → `normal` → `backfill`) using a CASE expression in the ORDER BY clause, while preserving the SKIP LOCKED + state-guard atomicity that the Phase 2 50-goroutine test asserts. Extend the `ClaimedRun` struct to expose `partition_key`, `priority`, and `backfill_id` so the executor can pass them through to the runtime. Land two new tests:

1. **TestClaimPriorityOrdering** — small-scale unit test verifying the CASE ORDER BY actually reorders runs.
2. **TestPriorityClaimLoad** — load test with 1000 backfill + 50 normal runs claimed by 50 concurrent goroutines, asserting normal runs claim first, no duplicate claims (SKIP LOCKED preserved), and backfill runs only claim after normal runs are exhausted.

This plan is the **single point of truth** for the priority enum integer mapping (Pitfall 5). A dedicated `PriorityOrder(string) int` function in Go matches the SQL CASE expression; a unit test asserts they agree by enumerating each value.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
This plan implements D-13 layers 2 (priority-then-FIFO claim) — the foundational change that lets backfills coexist with scheduled and on-demand runs without queue-position starvation. Layers 1 (priority column) and 3 (concurrency token tag) live in plans 03-01 (schema) and 03-07 (backfill CLI) respectively.

**Why Wave 2:** This plan modifies `internal/run/claim.go`. It cannot run until plan 03-01 has added the `partition_key`, `priority`, `backfill_id` columns to the `runs` table — otherwise the new SELECT projection fails. depends_on = [01].

**Why parallel with 03-04 and 03-05 in Wave 2:** This plan touches `internal/run/*` only. Plan 03-04 touches `internal/schedule/*`; plan 03-05 touches `internal/sensor/*`. Zero file overlap → safe to run in parallel.

**Why a dedicated PriorityOrder function (Pitfall 5):** The CASE expression in SQL and any future in-memory priority comparison in Go must agree on the integer mapping. By centralizing in `internal/run/priority.go`, future code paths (e.g., logging, observability, in-memory backfill scoring) call the same function. The drift-prevention test (`TestPriorityOrderConsistency`) asserts that `PriorityOrder("critical") < PriorityOrder("normal") < PriorityOrder("backfill")`.

**Why the 50-goroutine atomicity test still passes:** The Phase 2 test (`TestClaimAtomicity50Goroutines`) inserts ONE queued run and asserts exactly one claimer wins. The new ORDER BY is irrelevant when there's only one row to choose from. The SKIP LOCKED + `WHERE state='queued'` + the defense-in-depth UPDATE guard (`WHERE id=$1 AND state='queued'`) all remain unchanged. The test asserts atomicity, not ordering — and atomicity is unchanged. **This plan must explicitly run the test as an acceptance gate.**

**Why the load test is in Wave 2, not Wave 3:** Per the dependency note in the planning context — "priority-aware claim must land BEFORE backfill mass-enqueue is exercised at scale." The load test inserts directly into the `runs` table (no backfill API needed), so it can run as soon as schema (plan 03-01) and claim change (this plan) land. Backfill CLI (plan 03-07) then assumes the priority-aware claim is already verified.

**Pgx dialect note:** ClaimNext currently uses `tx.QueryRowContext` against `*sql.DB` (pgx via stdlib driver). The CASE expression is portable PostgreSQL SQL; no driver-specific syntax. The claim test file already opens a `pgx`-driven connection — same path.

**Frozen interfaces consumed:**
- `internal/storage.Storage` (DB() method only)
- runs table schema with partition_key/priority/backfill_id columns (from plan 03-01)

**Frozen interfaces produced:**
- `internal/run.ClaimedRun` extended struct (consumed by plan 03-04 scheduler enqueue, plan 03-07 backfill CLI for status)
- `internal/run.Priority` constants and `PriorityOrder` function (consumed by plan 03-04, plan 03-05, plan 03-07)

@.planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md
@.planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md
@.planning/phases/03-scheduling-sensors-partitions/03-VALIDATION.md
@internal/run/claim.go
@internal/run/claim_test.go
@migrations/20260507120000_phase2_run_tables.sql
@.planning/phases/02-execution-engine/02-02-SUMMARY.md

<interfaces>
<!-- Existing claim.go surface this plan extends. -->

From internal/run/claim.go (Phase 2 baseline):
```go
package run

var ErrNoQueuedRun = errors.New("run: no queued run available")

type ClaimedRun struct {
    ID        uuid.UUID
    AssetName string
    Trigger   string
    QueuedAt  time.Time
}

// ClaimNext atomically picks one queued run and transitions it to 'starting'.
// Uses SELECT ... FOR UPDATE SKIP LOCKED.
func ClaimNext(ctx context.Context, store storage.Storage, workerID string) (*ClaimedRun, error)

// Heartbeat updates runs.last_heartbeat to NOW().
func Heartbeat(ctx context.Context, store storage.Storage, runID uuid.UUID) error
```

From internal/run/claim_test.go (Phase 2 baseline — extending):
```go
// TestClaimAtomicity50Goroutines — MUST CONTINUE TO PASS after the ORDER BY change.
// Inserts one queued run, spawns 50 goroutines, asserts exactly one wins.
// (Phase 2 acceptance criterion 3.)
func TestClaimAtomicity50Goroutines(t *testing.T)
```

Phase 3 changes (this plan delivers):
```go
// internal/run/priority.go (NEW)
type Priority string
const (
    PriorityCritical Priority = "critical"
    PriorityNormal   Priority = "normal"
    PriorityBackfill Priority = "backfill"
)
func AllPriorities() []Priority
// PriorityOrder is the single source of truth for the integer ordering used by
// claim.go's SQL CASE expression. critical=0, normal=1, backfill=2 (Pitfall 5).
func PriorityOrder(p string) int

// internal/run/claim.go (EXTENDED)
type ClaimedRun struct {
    ID           uuid.UUID
    AssetName    string
    Trigger      string
    QueuedAt     time.Time
    PartitionKey *string     // nil for non-partitioned runs
    Priority     string      // "critical" | "normal" | "backfill"
    BackfillID   *uuid.UUID  // nil for non-backfill runs
}
```
</interfaces>
</context>

<tasks>

<task id="3.3.1" type="auto" tdd="true">
  <name>Task 1: Create internal/run/priority.go — Priority enum + PriorityOrder single source of truth + drift-prevention test</name>
  <files>internal/run/priority.go, internal/run/priority_test.go</files>
  <read_first>
    - internal/run/state.go (existing State enum pattern — mirror this style)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pitfall 5 — Priority Enum Integer Drift
    - .planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md § D-13
  </read_first>
  <behavior>
    - Priority constants exist with exact string values: "critical", "normal", "backfill"
    - AllPriorities() returns a slice of all three values in canonical order (critical, normal, backfill)
    - PriorityOrder("critical") == 0, PriorityOrder("normal") == 1, PriorityOrder("backfill") == 2
    - PriorityOrder("") and PriorityOrder("anything-else") returns 1 (default to "normal" — matches the SQL ELSE 1 branch)
    - TestPriorityOrderConsistency asserts: for every Priority constant, PriorityOrder(string(p)) returns the expected int; the returned ints satisfy critical<normal<backfill
  </behavior>
  <action>
    1. Create `internal/run/priority.go`:
       ```go
       package run

       // Priority enumerates the legal values of the runs.priority column (D-13).
       // The DB-level CHECK constraint in migrations/20260508120000_phase3_runs_columns.sql
       // enforces these same values; this Go enum provides fast-fail and type safety.
       type Priority string

       const (
           PriorityCritical Priority = "critical"
           PriorityNormal   Priority = "normal"
           PriorityBackfill Priority = "backfill"
       )

       // AllPriorities returns every legal value of the runs.priority column.
       func AllPriorities() []Priority {
           return []Priority{PriorityCritical, PriorityNormal, PriorityBackfill}
       }

       // PriorityOrder is the single source of truth for the priority integer mapping
       // used by claim.go's SQL CASE expression (Pitfall 5 — drift prevention).
       //
       //   critical -> 0  (claimed first)
       //   normal   -> 1  (default)
       //   backfill -> 2  (claimed last)
       //
       // Unknown / empty values map to 1 (normal) — matches the SQL ELSE 1 branch,
       // ensuring an unrecognised priority does NOT silently jump ahead of normal runs.
       func PriorityOrder(p string) int {
           switch Priority(p) {
           case PriorityCritical:
               return 0
           case PriorityBackfill:
               return 2
           default:
               return 1 // PriorityNormal and any unrecognised value
           }
       }
       ```
    2. Create `internal/run/priority_test.go`:
       - `TestPriorityOrderConsistency` — table-driven test over all `AllPriorities()`; assert PriorityOrder returns the expected int for each (critical=0, normal=1, backfill=2). Also assert `PriorityOrder("") == 1` and `PriorityOrder("foo") == 1` (default-to-normal).
       - `TestPriorityOrderingMonotonic` — assert `PriorityOrder("critical") < PriorityOrder("normal") < PriorityOrder("backfill")`.
       - `TestAllPrioritiesIsSorted` — assert AllPriorities() returns three elements in canonical order [critical, normal, backfill].
    3. Run `go test ./internal/run/... -run TestPriority -count=1 -timeout 30s` — must pass.
  </action>
  <acceptance_criteria>
    - `grep -q 'type Priority string' internal/run/priority.go`
    - `grep -q 'PriorityCritical Priority = "critical"' internal/run/priority.go`
    - `grep -q 'PriorityNormal   Priority = "normal"' internal/run/priority.go`
    - `grep -q 'PriorityBackfill Priority = "backfill"' internal/run/priority.go`
    - `grep -q 'func PriorityOrder(p string) int' internal/run/priority.go`
    - `grep -q 'func AllPriorities()' internal/run/priority.go`
    - `go test ./internal/run/... -run TestPriorityOrderConsistency -count=1 -timeout 30s` exits 0
    - `go test ./internal/run/... -run TestPriorityOrderingMonotonic -count=1 -timeout 30s` exits 0
  </acceptance_criteria>
  <verify>
    <automated>go test ./internal/run/... -run TestPriority -count=1 -timeout 30s</automated>
  </verify>
  <done>internal/run/priority.go has Priority enum + PriorityOrder; priority_test.go drift-prevention tests pass.</done>
</task>

<task id="3.3.2" type="auto" tdd="true">
  <name>Task 2: Modify internal/run/claim.go — extend ClaimedRun struct + add CASE ORDER BY + add TestClaimPriorityOrdering + verify TestClaimAtomicity50Goroutines still passes</name>
  <files>internal/run/claim.go, internal/run/claim_test.go</files>
  <read_first>
    - internal/run/claim.go (current full file — selectSQL constant, ClaimedRun struct, ClaimNext function)
    - internal/run/claim_test.go (existing TestClaimAtomicity50Goroutines + helpers `openTestDB`, `insertQueuedRun`, `deleteRuns`)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pattern 4 — Priority-Aware Claim SQL (verbatim)
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § Pitfall 1 — Priority ORDER BY breaks SKIP LOCKED guarantee
  </read_first>
  <behavior>
    - ClaimedRun struct has the four existing fields plus PartitionKey *string, Priority string, BackfillID *uuid.UUID
    - ClaimNext SELECT statement projects all 7 columns
    - ClaimNext ORDER BY uses CASE priority expression mapping critical=0, normal=1, backfill=2
    - ClaimNext WHERE clause is unchanged: `WHERE state = 'queued'`
    - SKIP LOCKED retained
    - Defense-in-depth UPDATE guard `WHERE id=$3 AND state='queued'` retained
    - TestClaimAtomicity50Goroutines still passes (regression)
    - TestClaimPriorityOrdering proves: insert 5 backfill + 5 normal + 1 critical, sequentially call ClaimNext 11 times, assert claim order: critical, then 5 normals (in queued_at order), then 5 backfills (in queued_at order)
  </behavior>
  <action>
    1. Edit `internal/run/claim.go`:
       a. Extend the `ClaimedRun` struct:
          ```go
          type ClaimedRun struct {
              ID           uuid.UUID
              AssetName    string
              Trigger      string
              QueuedAt     time.Time
              PartitionKey *string     // nil for non-partitioned runs (D-10)
              Priority     string      // "critical" | "normal" | "backfill" (D-13)
              BackfillID   *uuid.UUID  // nil for non-backfill runs (D-15)
          }
          ```
       b. Replace the existing `selectSQL` constant inside `ClaimNext` with the priority-aware version (verbatim from 03-RESEARCH.md § Pattern 4):
          ```go
          const selectSQL = `
              SELECT id, asset_name, trigger, queued_at, partition_key, priority, backfill_id
              FROM runs
              WHERE state = 'queued'
              ORDER BY
                  CASE priority
                      WHEN 'critical' THEN 0
                      WHEN 'normal'   THEN 1
                      WHEN 'backfill' THEN 2
                      ELSE 1
                  END ASC,
                  queued_at ASC
              FOR UPDATE SKIP LOCKED
              LIMIT 1
          `
          ```
       c. Update the `Scan` call to read three new fields. Use `sql.NullString` for `partition_key`, raw `string` for `priority` (NOT NULL), and `uuid.NullUUID` for `backfill_id`:
          ```go
          var (
              id           uuid.UUID
              assetName    string
              trigger      string
              queuedAt     time.Time
              partitionKey sql.NullString
              priority     string
              backfillID   uuid.NullUUID
          )
          row := tx.QueryRowContext(ctx, selectSQL)
          if err := row.Scan(&id, &assetName, &trigger, &queuedAt, &partitionKey, &priority, &backfillID); err != nil {
              if errors.Is(err, sql.ErrNoRows) {
                  return nil, ErrNoQueuedRun
              }
              return nil, fmt.Errorf("run: select queued: %w", err)
          }
          ```
       d. Build the returned `ClaimedRun` with the new fields:
          ```go
          claimed := &ClaimedRun{
              ID:        id,
              AssetName: assetName,
              Trigger:   trigger,
              QueuedAt:  queuedAt,
              Priority:  priority,
          }
          if partitionKey.Valid {
              s := partitionKey.String
              claimed.PartitionKey = &s
          }
          if backfillID.Valid {
              u := backfillID.UUID
              claimed.BackfillID = &u
          }
          return claimed, nil
          ```
       e. The existing `updateSQL` (`UPDATE runs SET state='starting', claimed_by=$1, claimed_at=$2, last_heartbeat=$2 WHERE id=$3 AND state='queued'`) is **unchanged**. The defense-in-depth state guard remains.
    2. Update any caller of `ClaimedRun{...}` literal constructors in tests / fixtures to populate the new fields explicitly (or zero-value them — defaults are nil/empty/empty which are valid).
    3. Update `cmd/platform/worker.go` line 84 (`slog.Info("worker.run_claimed"...)`) to log the new fields:
       ```go
       slog.Info("worker.run_claimed",
           "run_id", claimed.ID,
           "asset", claimed.AssetName,
           "priority", claimed.Priority,
           "partition_key", derefString(claimed.PartitionKey),
       )
       ```
       Add a small helper `derefString(s *string) string { if s == nil { return "" }; return *s }` to worker.go (or use `cmp.Or` if Go version supports it — but the existing codebase uses Go 1.25, so the helper is fine).
    4. Update `internal/runtime/executor.go` to extract `partitionKey` from the claimed run and forward to `NewAssetIO`:
       Find the line `io := asset.NewAssetIO(self, resolver)` (or equivalent) and change to `io := asset.NewAssetIO(self, resolver, partitionKey)` where `partitionKey` is derived from the run row. Since the executor receives `runID` and `assetName` (not the full ClaimedRun), this requires either:
       - Threading the ClaimedRun down to Executor.Run (cleaner — modify the worker.go call site to pass the claimed.PartitionKey), OR
       - Have Executor.Run query `SELECT partition_key FROM runs WHERE id=$1` once at the top.
       Pick the threading approach: change `Executor.Run` signature to accept `partitionKey string` as an additional parameter, and update the worker.go call site `deps.executor.Run(ctx, claimed.ID, claimed.AssetName, derefString(claimed.PartitionKey))`. This is a 3-line change to the executor.
    5. Extend `internal/run/claim_test.go` with two new tests:
       a. `TestClaimPriorityOrdering(t *testing.T)`:
          ```go
          func TestClaimPriorityOrdering(t *testing.T) {
              db := openTestDB(t)
              defer db.Close()
              defer deleteRuns(t, db, "test-priority-ordering")
              // Insert 5 backfill, 5 normal, 1 critical — all queued, varied queued_at to confirm priority dominates.
              insertWithPriority := func(priority string, queuedAtOffset time.Duration) string {
                  var id string
                  err := db.QueryRowContext(context.Background(),
                      `INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority)
                       VALUES (gen_random_uuid(), $1, 'queued', 'manual', NOW() - $2::interval, $3) RETURNING id`,
                      "test-priority-ordering",
                      fmt.Sprintf("%d milliseconds", queuedAtOffset.Milliseconds()),
                      priority,
                  ).Scan(&id)
                  require.NoError(t, err)
                  return id
              }
              // Insert backfills with OLDEST queued_at to prove priority dominates over FIFO.
              for i := 0; i < 5; i++ { insertWithPriority("backfill", time.Duration(1000-i*10)*time.Millisecond) }
              for i := 0; i < 5; i++ { insertWithPriority("normal",   time.Duration(500-i*10)*time.Millisecond) }
              // Critical inserted with NEWEST queued_at — must still claim first.
              insertWithPriority("critical", 0)

              storage := &sqlStorage{db: db}
              gotPriorities := make([]string, 0, 11)
              for i := 0; i < 11; i++ {
                  c, err := run.ClaimNext(context.Background(), storage, fmt.Sprintf("test-worker-%d", i))
                  require.NoError(t, err)
                  gotPriorities = append(gotPriorities, c.Priority)
              }
              // Expect: 1 critical, then 5 normals, then 5 backfills.
              expected := []string{"critical","normal","normal","normal","normal","normal","backfill","backfill","backfill","backfill","backfill"}
              assert.Equal(t, expected, gotPriorities, "priority ORDER BY did not order claims correctly")
          }
          ```
       b. `TestPriorityClaimLoad(t *testing.T)` (the deferred load test from D-13):
          ```go
          func TestPriorityClaimLoad(t *testing.T) {
              if testing.Short() { t.Skip("load test skipped in -short mode") }
              db := openTestDB(t)
              defer db.Close()
              const asset = "test-priority-load"
              defer deleteRuns(t, db, asset)
              // Bulk insert 1000 backfill + 50 normal in single multi-row VALUES.
              ctx := context.Background()
              for _, batch := range []struct{ count int; priority string }{
                  {1000, "backfill"},
                  {50, "normal"},
              } {
                  // Use VALUES with gen_random_uuid() for each row.
                  values := make([]string, 0, batch.count)
                  args := []any{asset, batch.priority}
                  for i := 0; i < batch.count; i++ {
                      values = append(values, "(gen_random_uuid(), $1, 'queued', 'manual', NOW(), $2)")
                  }
                  q := "INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority) VALUES " + strings.Join(values, ",")
                  _, err := db.ExecContext(ctx, q, args...)
                  require.NoError(t, err)
              }
              storage := &sqlStorage{db: db}

              // Round 1: spawn 50 goroutines, each claims one run. ALL must be 'normal'.
              var wg sync.WaitGroup
              normals := make([]string, 50)
              normalDuplicates := make(map[uuid.UUID]int)
              var mu sync.Mutex
              for i := 0; i < 50; i++ {
                  wg.Add(1)
                  go func(idx int) {
                      defer wg.Done()
                      c, err := run.ClaimNext(ctx, storage, fmt.Sprintf("loader-%d", idx))
                      if err != nil { return }
                      mu.Lock()
                      normals[idx] = c.Priority
                      normalDuplicates[c.ID]++
                      mu.Unlock()
                  }(i)
              }
              wg.Wait()
              for i, p := range normals { assert.Equal(t, "normal", p, "round 1 goroutine %d expected normal, got %q", i, p) }
              for id, n := range normalDuplicates { assert.Equal(t, 1, n, "round 1 duplicate claim: %s claimed %d times", id, n) }

              // Round 2: another 50 goroutines — must ALL claim 'backfill' (no normals left).
              wg = sync.WaitGroup{}
              backfills := make([]string, 50)
              backfillDuplicates := make(map[uuid.UUID]int)
              for i := 0; i < 50; i++ {
                  wg.Add(1)
                  go func(idx int) {
                      defer wg.Done()
                      c, err := run.ClaimNext(ctx, storage, fmt.Sprintf("loader2-%d", idx))
                      if err != nil { return }
                      mu.Lock()
                      backfills[idx] = c.Priority
                      backfillDuplicates[c.ID]++
                      mu.Unlock()
                  }(i)
              }
              wg.Wait()
              for i, p := range backfills { assert.Equal(t, "backfill", p, "round 2 goroutine %d expected backfill, got %q", i, p) }
              for id, n := range backfillDuplicates { assert.Equal(t, 1, n, "round 2 duplicate claim: %s claimed %d times", id, n) }
          }
          ```
       Add necessary imports to claim_test.go: `"strings"`, `"sync"` (already imported), `"github.com/google/uuid"` (already imported).
    6. Run the full claim_test suite to confirm all three tests pass:
       ```bash
       DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable \
         go test ./internal/run/... -count=1 -timeout 300s
       ```
       Both `TestClaimAtomicity50Goroutines` (Phase 2 regression) and the two new tests must pass.
  </action>
  <acceptance_criteria>
    - `grep -q 'PartitionKey \\*string' internal/run/claim.go`
    - `grep -q 'Priority     string' internal/run/claim.go` (or with adjusted spacing)
    - `grep -q 'BackfillID   \\*uuid.UUID' internal/run/claim.go`
    - `grep -q 'CASE priority' internal/run/claim.go`
    - `grep -q "WHEN 'critical' THEN 0" internal/run/claim.go`
    - `grep -q "WHEN 'normal'   THEN 1" internal/run/claim.go`
    - `grep -q "WHEN 'backfill' THEN 2" internal/run/claim.go`
    - `grep -q 'FOR UPDATE SKIP LOCKED' internal/run/claim.go`
    - `grep -q 'WHERE state = .queued.' internal/run/claim.go` (still WHERE state='queued', no WHERE priority)
    - `grep -q 'WHERE id = \\$3 AND state = .queued.' internal/run/claim.go` (defense-in-depth UPDATE guard)
    - `grep -q 'func TestClaimPriorityOrdering' internal/run/claim_test.go`
    - `grep -q 'func TestPriorityClaimLoad' internal/run/claim_test.go`
    - `DATABASE_URL=... go test ./internal/run/... -run TestClaimAtomicity50Goroutines -count=1 -timeout 60s` exits 0 (Phase 2 regression — MANDATORY)
    - `DATABASE_URL=... go test ./internal/run/... -run TestClaimPriorityOrdering -count=1 -timeout 60s` exits 0
    - `DATABASE_URL=... go test ./internal/run/... -run TestPriorityClaimLoad -count=1 -timeout 300s` exits 0
    - `go build ./...` passes after worker.go and executor.go updates
  </acceptance_criteria>
  <verify>
    <automated>DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./internal/run/... -count=1 -timeout 300s</automated>
  </verify>
  <done>claim.go has CASE ORDER BY + extended ClaimedRun + projection of new columns; PriorityOrder + claim SQL agree on integer mapping; TestClaimAtomicity50Goroutines still passes (regression guard); TestClaimPriorityOrdering proves CASE actually reorders; TestPriorityClaimLoad proves no duplicates under 100 goroutines + 1050 rows; worker.go / executor.go updated to consume new fields; build green.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| Multiple worker processes → runs table claim path | Concurrent SELECT FOR UPDATE SKIP LOCKED crosses here; atomicity is the safety property |
| ClaimNext SQL CASE expression → PriorityOrder Go function | Two encodings of the same integer mapping; drift is the threat |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-03-03-01 | Tampering | runs.priority claim ordering — adding a WHERE filter would strand backfill rows (Pitfall 1) | mitigate | Code review checklist: any future change to `WHERE` clause in claim.go MUST keep `WHERE state='queued'` only. Comment in claim.go explicitly forbids `WHERE priority…`. TestPriorityClaimLoad's round 2 (claims after normal exhausted) detects accidental filtering. |
| T-03-03-02 | Tampering | Priority enum integer drift between Go and SQL (Pitfall 5) | mitigate | Single source of truth: `PriorityOrder` function in `internal/run/priority.go`. The SQL CASE expression mirrors it 1:1. `TestPriorityOrderConsistency` enumerates all values; future readers cannot miss the contract. |
| T-03-03-03 | Denial of Service | Sequential scan instead of index scan under load (assumption A2 in 03-RESEARCH.md) | mitigate | Plan 03-01 creates `(state, priority, queued_at)` composite index. TestPriorityClaimLoad uses 1050 rows + 100 goroutines; if performance degrades to seq scan, the 300s timeout will fire and the test fails — the failure mode surfaces as part of CI. |
| T-03-03-04 | Tampering | Existing 50-goroutine atomicity broken by ORDER BY change (regression) | mitigate | Acceptance criterion explicitly re-runs `TestClaimAtomicity50Goroutines`. The SKIP LOCKED + WHERE state='queued' + UPDATE guard `WHERE id=$3 AND state='queued'` are unchanged. Test failure here BLOCKS the plan. |
| T-03-03-05 | Information Disclosure | partition_key/priority/backfill_id leak via slog logs | accept | All three are non-sensitive scheduling metadata, not user data. Log values acceptable. |
| T-03-03-06 | Elevation of Privilege | Caller submits a run with priority='critical' to skip queue | mitigate (defer to caller plans) | DB-level CHECK prevents non-enum values; CHECK is in plan 03-01. Authorization (who may submit critical) is enforced at the CLI layer in plan 03-07 (T-03-07-XX) and at the API layer in Phase 4+. |
</threat_model>

<verification>
- `go build ./...` passes after all caller updates (worker.go, executor.go).
- `DATABASE_URL=... go test ./internal/run/... -count=1 -timeout 300s` exits 0 (covers all four tests: TestClaimAtomicity50Goroutines, TestClaimPriorityOrdering, TestPriorityClaimLoad, TestPriorityOrderConsistency).
- The worker subcommand still claims runs end-to-end (smoke check via existing Phase 2 e2e if available).
- ClaimedRun struct now exposes PartitionKey, Priority, BackfillID; downstream plans (03-04, 03-07) can consume these fields.
</verification>

<success_criteria>
- internal/run/priority.go exists with Priority enum + PriorityOrder + AllPriorities; drift-prevention test passes.
- internal/run/claim.go has the priority-aware ORDER BY (CASE expression) and projects partition_key/priority/backfill_id.
- ClaimedRun struct exposes the three new fields.
- TestClaimAtomicity50Goroutines (Phase 2 acceptance criterion 3) still passes — regression guard met.
- TestClaimPriorityOrdering passes — proves CASE actually reorders claims.
- TestPriorityClaimLoad passes — proves SKIP LOCKED atomicity holds under 100 goroutines + 1050 rows; satisfies D-13 deferred load test obligation.
- worker.go and executor.go updated to thread partition_key through to AssetIO.
- All builds green.
</success_criteria>

<output>
After completion, create `.planning/phases/03-scheduling-sensors-partitions/03-03-SUMMARY.md` documenting:
- Final claim.go SQL (CASE expression — quoted verbatim).
- Confirmation that TestClaimAtomicity50Goroutines still passes.
- Load test runtime (expect ~5-30s for 1050 rows + 100 goroutines).
- Caller-update map: which files outside `internal/run/` were modified to pass partition_key through (worker.go, executor.go).
- Decision-coverage: D-13 layer 2 → which test names cover it (TestClaimPriorityOrdering for correctness, TestPriorityClaimLoad for atomicity at scale).
</output>
