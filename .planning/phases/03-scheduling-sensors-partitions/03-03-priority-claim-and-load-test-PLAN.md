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
  - internal/runtime/executor.go
  - cmd/platform/worker.go
  - cmd/platform/materialize.go
autonomous: true
must_haves:
  truths:
    - "ClaimNext SQL ORDER BY clause is `CASE priority WHEN 'critical' THEN 0 WHEN 'normal' THEN 1 WHEN 'backfill' THEN 2 ELSE 1 END ASC, queued_at ASC`"
    - "ClaimNext SELECT projects partition_key, priority, backfill_id in addition to existing columns"
    - "ClaimedRun struct exposes PartitionKey *string, Priority string, BackfillID *uuid.UUID"
    - "Executor.Run signature is `Run(ctx context.Context, claimed *run.ClaimedRun) error` — single migration, no intermediate signature"
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
    - path: "internal/runtime/executor.go"
      provides: "Executor.Run takes *run.ClaimedRun (final signature, set ONCE in this plan)"
      contains: "claimed *run.ClaimedRun"
  key_links:
    - from: "internal/run.ClaimNext SELECT"
      to: "runs table partition_key/priority/backfill_id columns (plan 03-01)"
      via: "SELECT id, asset_name, trigger, queued_at, partition_key, priority, backfill_id FROM runs WHERE state='queued' ORDER BY <priority CASE>, queued_at FOR UPDATE SKIP LOCKED LIMIT 1"
      pattern: "SELECT.*partition_key, priority, backfill_id.*FROM runs"
    - from: "internal/run.PriorityOrder Go function"
      to: "claim.go SQL CASE expression"
      via: "Both encode critical=0, normal=1, backfill=2 — drift prevention test asserts agreement"
      pattern: "PriorityOrder.*critical.*normal.*backfill"
    - from: "cmd/platform/worker.go ClaimNext call site"
      to: "internal/runtime.Executor.Run(ctx, claimed)"
      via: "passes the entire *run.ClaimedRun struct (priority, partition_key, backfill_id all reachable downstream)"
      pattern: "executor.Run\\(ctx, claimed\\)"
---

<objective>
修改 `internal/run/claim.go` 以优先级顺序（`critical` → `normal` → `backfill`）claim runs，使用 CASE 表达式的 ORDER BY 子句，同时保留 Phase 2 50-goroutine 测试所验证的 SKIP LOCKED + state-guard 原子性。扩展 `ClaimedRun` 结构以暴露 `partition_key`、`priority` 和 `backfill_id`，使 executor 可以将它们传递到运行时。落地两个新测试：

1. **TestClaimPriorityOrdering** — 小规模单元测试，验证 CASE ORDER BY 实际重新排序 runs。
2. **TestPriorityClaimLoad** — 负载测试，包含 1000 个 backfill + 50 个 normal runs，由 50 个并发 goroutines claim，断言 normal runs 首先被 claim，无重复 claim（SKIP LOCKED 保留），backfill runs 仅在 normal runs 耗尽后才被 claim。

本计划是优先级枚举整数映射的**单一真实来源**（Pitfall 5）。Go 中专门的 `PriorityOrder(string) int` 函数与 SQL CASE 表达式匹配；一个单元测试通过枚举每个值来断言它们一致。

**Executor.Run 的单一签名迁移：** 计划 03-03 设置 `Executor.Run(ctx context.Context, claimed *run.ClaimedRun) error` 作为最终签名。计划 03-07（backfill CLI）将只读取附加字段（例如 `claimed.Priority == "backfill"` 用于第三层 token tag）；它不会再次更改签名。这避免了双迁移问题，即由中间签名更新的调用者在后续计划更改时中断。
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
本计划实现了 D-13 层 2（优先级然后 FIFO claim）——允许 backfills 与 scheduled 和 on-demand runs 共存而不造成队列位置饥饿的基础变更。第 1 层（priority 列）和第 3 层（concurrency token tag）分别在计划 03-01（schema）和 03-07（backfill CLI）中。

**为何 Wave 2：** 本计划修改 `internal/run/claim.go`。它必须等到计划 03-01 将 `partition_key`、`priority`、`backfill_id` 列添加到 `runs` 表后才能运行——否则新的 SELECT 投影会失败。depends_on = [01]。

**为何与 03-04 和 03-05 在 Wave 2 并行：** 本计划仅涉及 `internal/run/*`、`internal/runtime/*` 和 `cmd/platform/{worker.go,materialize.go}`。计划 03-04 涉及 `internal/schedule/*`；计划 03-05 涉及 `internal/sensor/*`。零文件重叠 → 可安全并行运行。

**为何使用专门的 PriorityOrder 函数（Pitfall 5）：** SQL 中的 CASE 表达式和 Go 中任何未来的内存优先级比较必须就整数映射达成一致。通过集中在 `internal/run/priority.go` 中，未来的代码路径（例如日志、可观察性、内存 backfill 评分）调用相同的函数。漂移预防测试（`TestPriorityOrderConsistency`）断言 `PriorityOrder("critical") < PriorityOrder("normal") < PriorityOrder("backfill")`。

**为何 50-goroutine 原子性测试仍然通过：** Phase 2 测试（`TestClaimAtomicity50Goroutines`）插入一个排队 run 并断言恰好一个 claimer 获胜。当只有一行可供选择时，新的 ORDER BY 无关。SKIP LOCKED + `WHERE state='queued'` + 防御性深度 UPDATE 守卫（`WHERE id=$1 AND state='queued'`）均保持不变。测试断言原子性，而非排序——原子性未改变。**本计划必须明确将测试作为验收门槛运行。**

**为何负载测试在 Wave 2，而非 Wave 3：** per 规划上下文中的依赖注释——"优先级感知 claim 必须先于 backfill 大量入队在规模上被验证"。负载测试直接插入 `runs` 表（无需 backfill API），因此可以在 schema（计划 03-01）和 claim 变更（本计划）落地后立即运行。Backfill CLI（计划 03-07）随后假设优先级感知 claim 已过验证。

**为何 Executor.Run 接受 *run.ClaimedRun（单一签名迁移）：** 添加 `partition_key`（计划 03-03）和基于 `priority` 的 token 获取（计划 03-07 D-13 层 3）都需要 executor 端访问 ClaimNext 填充的字段。最简洁的方式是整个 `*run.ClaimedRun` 结构向下传递。本计划执行单一签名变更（`Run(ctx, runID, assetName) → Run(ctx, claimed)`）并更新所有调用者（`cmd/platform/worker.go`、`cmd/platform/materialize.go`）。计划 03-07 然后仅读取 `claimed.Priority` 来控制 backfill-tag 获取——不再进一步更改签名。这消除了双重迁移风险，其中 03-03 的中间 `(ctx, runID, assetName, partitionKey)` 和 03-07 的最终 `(ctx, claimed)` 都更新调用者并相互中断。

**Pgx 方言注释：** ClaimNext 目前对 `*sql.DB`（pgx via stdlib driver）使用 `tx.QueryRowContext`。CASE 表达式是便携式 PostgreSQL SQL；无驱动程序特定语法。claim 测试文件已经打开了 `pgx` 驱动的连接——相同的路径。

**已冻结的接口：**
- `internal/storage.Storage`（仅 DB() 方法）
- 带有 partition_key/priority/backfill_id 列的 runs 表 schema（来自计划 03-01）

**已冻结的接口：**
- `internal/run.ClaimedRun` 扩展结构（由计划 03-04 scheduler 入队、计划 03-07 backfill CLI 用于状态消费）
- `internal/run.Priority` 常量和 `PriorityOrder` 函数（由计划 03-04、03-05、03-07 消费）
- `internal/runtime.Executor.Run(ctx, *run.ClaimedRun) error` 最终签名（由计划 03-07 第三层获取消费，不再更改签名）

@.planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md
@.planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md
@.planning/phases/03-scheduling-sensors-partitions/03-VALIDATION.md
@internal/run/claim.go
@internal/run/claim_test.go
@internal/runtime/executor.go
@cmd/platform/worker.go
@cmd/platform/materialize.go
@migrations/20260507120000_phase2_run_tables.sql
@.planning/phases/02-execution-engine/02-02-SUMMARY.md

<interfaces>
<!-- 本计划扩展的现有 claim.go 表面。 -->

From internal/run/claim.go (Phase 2 基线):
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

From internal/runtime/executor.go (Phase 2 基线——在本计划中更改):
```go
// Current signature:
func (e *Executor) Run(ctx context.Context, runID uuid.UUID, assetName string) error
// New signature this plan installs (FINAL — plan 03-07 does NOT change again):
func (e *Executor) Run(ctx context.Context, claimed *run.ClaimedRun) error
```

From cmd/platform/worker.go (Phase 2 基线——在本计划中更新):
```go
// Current call site (line 85):
execErr := deps.executor.Run(ctx, claimed.ID, claimed.AssetName)
// New call site this plan installs (FINAL):
execErr := deps.executor.Run(ctx, claimed)
```

From internal/run/claim_test.go (Phase 2 基线——扩展中):
```go
// TestClaimAtomicity50Goroutines — MUST CONTINUE TO PASS after the ORDER BY change.
// Inserts one queued run, spawns 50 goroutines, asserts exactly one wins.
// (Phase 2 acceptance criterion 3.)
func TestClaimAtomicity50Goroutines(t *testing.T)
```

Phase 3 变更（本计划交付）:
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
  <name>Task 2: Modify internal/run/claim.go — extend ClaimedRun struct + add CASE ORDER BY + change Executor.Run signature ONCE to (ctx, *run.ClaimedRun) + update worker.go/materialize.go callers + add TestClaimPriorityOrdering + verify TestClaimAtomicity50Goroutines still passes</name>
  <files>internal/run/claim.go, internal/run/claim_test.go, internal/runtime/executor.go, cmd/platform/worker.go, cmd/platform/materialize.go</files>
  <read_first>
    - internal/run/claim.go (current full file — selectSQL constant, ClaimedRun struct, ClaimNext function)
    - internal/run/claim_test.go (existing TestClaimAtomicity50Goroutines + helpers `openTestDB`, `insertQueuedRun`, `deleteRuns`)
    - internal/runtime/executor.go (current Executor.Run signature: `Run(ctx, runID uuid.UUID, assetName string) error` at line 78)
    - cmd/platform/worker.go (current call site at line 85: `deps.executor.Run(ctx, claimed.ID, claimed.AssetName)`)
    - cmd/platform/materialize.go (any call to executor.Run that must be updated)
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
    - Executor.Run signature changed ONCE (and only once, in this plan) from `Run(ctx, runID, assetName)` to `Run(ctx, claimed *run.ClaimedRun)`. Plan 03-07 will NOT change this signature again — it only reads `claimed.Priority`.
    - All callers of executor.Run (cmd/platform/worker.go, cmd/platform/materialize.go, runtime/executor_test.go fixtures) updated to pass *run.ClaimedRun
    - asset.NewAssetIO is called with `derefString(claimed.PartitionKey)` as the third argument (per plan 03-02 task 3 NewAssetIO signature)
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
    2. Edit `internal/runtime/executor.go` — change the `Executor.Run` signature ONCE (final form for Phase 3):
       a. Change the signature from
          ```go
          func (e *Executor) Run(ctx context.Context, runID uuid.UUID, assetName string) error {
          ```
          to
          ```go
          func (e *Executor) Run(ctx context.Context, claimed *run.ClaimedRun) error {
              runID := claimed.ID
              assetName := claimed.AssetName
              // partitionKey is "" for non-partitioned runs (claimed.PartitionKey == nil).
              partitionKey := ""
              if claimed.PartitionKey != nil {
                  partitionKey = *claimed.PartitionKey
              }
          ```
       b. Inside `runStep` (or wherever `asset.NewAssetIO(a, e)` is called — line 239 in current source), the call must be updated to pass partitionKey through. Since `runStep` does not currently receive partitionKey, thread it through `runStep(ctx, runID, stepAsset, i, partitionKey)`. Update the call site in `Run` accordingly. The new `runStep` signature:
          ```go
          func (e *Executor) runStep(ctx context.Context, runID uuid.UUID, a *asset.Asset, topoOrder int, partitionKey string) error {
              // ...
              io := asset.NewAssetIO(a, e, partitionKey)  // ← plan 03-02 task 3 added third arg
              // ...
          }
          ```
       c. The import `"github.com/google/uuid"` is no longer strictly required for `Run` itself (since `runID` comes from `claimed.ID`), but it remains imported because `runStep`, `transition`, `appendEvent` still use `uuid.UUID`. No import change needed.
    3. Edit `cmd/platform/worker.go`:
       a. Change line 85 from `execErr := deps.executor.Run(ctx, claimed.ID, claimed.AssetName)` to `execErr := deps.executor.Run(ctx, claimed)`.
       b. Update the slog.Info at line 84 to log the new fields:
          ```go
          slog.Info("worker.run_claimed",
              "run_id", claimed.ID,
              "asset", claimed.AssetName,
              "priority", claimed.Priority,
              "partition_key", derefString(claimed.PartitionKey),
          )
          ```
       c. Add a small helper at the bottom of worker.go:
          ```go
          // derefString returns *s, or "" if s is nil.
          func derefString(s *string) string {
              if s == nil {
                  return ""
              }
              return *s
          }
          ```
    4. Edit `cmd/platform/materialize.go` — find any call to `executor.Run(...)` and update it to pass a synthesized `*run.ClaimedRun`. The `materialize` subcommand creates an ad-hoc run row (not via ClaimNext), so it must construct a ClaimedRun by-hand:
       ```go
       claimed := &run.ClaimedRun{
           ID:        runID,        // freshly inserted run row
           AssetName: assetName,
           Trigger:   "manual",
           QueuedAt:  time.Now().UTC(),
           Priority:  "normal",
           // PartitionKey nil (manual materialize is non-partitioned for v1)
           // BackfillID nil
       }
       if err := exec.Run(ctx, claimed); err != nil { ... }
       ```
       (If materialize.go does not currently call executor.Run — e.g., it uses a different code path — skip this sub-step. Run `grep -n "executor.Run\\|exec.Run" cmd/platform/*.go` to find ALL callers and update each.)
    5. Update test fixtures in `internal/runtime/executor_test.go` (if any) — find every `e.Run(ctx, runID, assetName)` invocation and replace with `e.Run(ctx, &run.ClaimedRun{ID: runID, AssetName: assetName, Priority: "normal"})`. Also import `"github.com/kanpon/data-governance/internal/run"` if not already.
    6. Update any caller of `ClaimedRun{...}` literal constructors in tests / fixtures to populate the new fields explicitly (or zero-value them — defaults are nil/empty/empty which are valid).
    7. Extend `internal/run/claim_test.go` with two new tests:
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
    8. Run the full claim_test suite to confirm all three tests pass:
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
    - `grep -q 'func (e \\*Executor) Run(ctx context.Context, claimed \\*run.ClaimedRun) error' internal/runtime/executor.go`
    - `grep -q 'deps.executor.Run(ctx, claimed)' cmd/platform/worker.go`
    - `! grep -q 'deps.executor.Run(ctx, claimed.ID, claimed.AssetName)' cmd/platform/worker.go` (old call site removed)
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
  <done>claim.go has CASE ORDER BY + extended ClaimedRun + projection of new columns; PriorityOrder + claim SQL agree on integer mapping; TestClaimAtomicity50Goroutines still passes (regression guard); TestClaimPriorityOrdering proves CASE actually reorders; TestPriorityClaimLoad proves no duplicates under 100 goroutines + 1050 rows; Executor.Run signature changed ONCE to (ctx, *run.ClaimedRun) — plan 03-07 will not change it again; worker.go / materialize.go / runtime tests updated to consume new field; build green.</done>
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
- `go build ./...` passes after all caller updates (worker.go, materialize.go, executor.go).
- `DATABASE_URL=... go test ./internal/run/... -count=1 -timeout 300s` exits 0 (covers all four tests: TestClaimAtomicity50Goroutines, TestClaimPriorityOrdering, TestPriorityClaimLoad, TestPriorityOrderConsistency).
- `DATABASE_URL=... go test ./internal/runtime/... -count=1 -timeout 120s` still passes (executor signature change does not break existing tests once fixtures updated).
- The worker subcommand still claims runs end-to-end (smoke check via existing Phase 2 e2e if available).
- ClaimedRun struct now exposes PartitionKey, Priority, BackfillID; downstream plans (03-04, 03-07) can consume these fields.
- Executor.Run final signature `Run(ctx, *run.ClaimedRun) error` — plan 03-07 will not change it again.
</verification>

<success_criteria>
- internal/run/priority.go exists with Priority enum + PriorityOrder + AllPriorities; drift-prevention test passes.
- internal/run/claim.go has the priority-aware ORDER BY (CASE expression) and projects partition_key/priority/backfill_id.
- ClaimedRun struct exposes the three new fields.
- Executor.Run signature changed ONCE to `Run(ctx context.Context, claimed *run.ClaimedRun) error` — final form for Phase 3.
- worker.go and materialize.go updated to pass `*run.ClaimedRun` to Executor.Run; partition_key threaded into AssetIO via NewAssetIO third arg.
- TestClaimAtomicity50Goroutines (Phase 2 acceptance criterion 3) still passes — regression guard met.
- TestClaimPriorityOrdering passes — proves CASE actually reorders claims.
- TestPriorityClaimLoad passes — proves SKIP LOCKED atomicity holds under 100 goroutines + 1050 rows; satisfies D-13 deferred load test obligation.
- All builds green.
</success_criteria>

<output>
After completion, create `.planning/phases/03-scheduling-sensors-partitions/03-03-SUMMARY.md` documenting:
- Final claim.go SQL (CASE expression — quoted verbatim).
- Confirmation that TestClaimAtomicity50Goroutines still passes.
- Load test runtime (expect ~5-30s for 1050 rows + 100 goroutines).
- Caller-update map: which files outside `internal/run/` were modified to pass *ClaimedRun (worker.go, materialize.go, runtime/executor.go).
- Final Executor.Run signature documented as STABLE for the rest of Phase 3 — plan 03-07 must NOT change it again.
- Decision-coverage: D-13 layer 2 → which test names cover it (TestClaimPriorityOrdering for correctness, TestPriorityClaimLoad for atomicity at scale).
</output>
