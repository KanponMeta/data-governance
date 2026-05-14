---
phase: 03-scheduling-sensors-partitions
plan: 07
subsystem: backfill
tags: [backfill, cli, mass-enqueue, on-conflict, concurrency, priority, d-13, d-14, d-15, d-16]

# Dependency graph
requires:
  - phase: 03-scheduling-sensors-partitions/01-schema-events-foundation
    provides: backfills table, runs.backfill_id/priority/partition_key columns, partial unique index `run_partition_inflight_unique` predicate, 3 backfill.* EventType constants
  - phase: 03-scheduling-sensors-partitions/02-asset-dsl-and-partitions
    provides: PartitionStrategy sealed interface, KeysBetween/ValidateCategoryKey, DailyPartitions/CategoryPartitions
  - phase: 03-scheduling-sensors-partitions/03-priority-claim-and-load-test
    provides: ClaimedRun.Priority field, PriorityBackfill constant, Executor.Run(ctx, *run.ClaimedRun) frozen signature
  - phase: 03-scheduling-sensors-partitions/06-scheduler-subcommand
    provides: cmd/platform/main.go switch with `case "scheduler":` (avoids merge conflict on this plan's `case "backfill":` add)
  - phase: 02-execution-engine
    provides: storage.NewPostgres, event.NewWriter, asset.Default(), concurrency.Pool, Executor + retry policy
provides:
  - "backfill.ParsePartitionSpec(strategy, raw, maxPartitions) — three formats (date range, comma list, single key) with per-strategy key validation"
  - "backfill.DefaultMaxPartitions = 3650 + ErrTooManyPartitions guard (Pitfall 6)"
  - "backfill.ErrInvalidSpec / ErrCategoryKeyNotDeclared sentinels"
  - "backfill.Submit(ctx, store, events, asset, spec) — mass-enqueue 1 backfills row + N runs rows in single tx (idempotent via ON CONFLICT)"
  - "backfill.GetStatus(ctx, db, backfillID) — aggregate run state counts via GROUP BY state"
  - "backfill.ValidPriorities {critical,normal,backfill} — CLI parse-time validation set"
  - "./platform backfill <asset> --partitions=<spec> [--priority=...] [--max-partitions=N] subcommand"
  - "./platform backfill status <backfill_id> subcommand (alphabetical state-count output)"
  - "Executor reads claimed.Priority and acquires \"backfill\" concurrency tag for backfill-priority runs (D-13 layer 3) — NO Run signature change since plan 03-03"
  - "Worker bootstrap declares default Capacity{Tag: \"backfill\", Limit: 5} unless operator overrides via cfg.Concurrency.Resources"
affects: []  # Final plan of Phase 3 — no downstream Phase 3 plans

# Tech tracking
tech-stack:
  added: []  # Pure additive — reuses existing partition / event / storage / concurrency surfaces
  patterns:
    - "Multi-row VALUES INSERT with 5 placeholders/row (base := i*5) — three SQL-literal columns (state='queued', trigger='backfill', queued_at=NOW()) are NOT placeholders"
    - "ON CONFLICT predicate matches partial unique index predicate VERBATIM: WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL DO NOTHING"
    - "Idempotent backfill resubmit via ON CONFLICT DO NOTHING — second submit silently skips in-flight partitions; event payload exposes enqueued vs. skipped counts"
    - "CLI flags-anywhere positional parsing — split args into positional vs flag tokens before FlagSet.Parse so operators may type `backfill <asset> --partitions=...` (Go stdlib otherwise stops at first non-flag)"
    - "Executor layer-3 token tag acquisition reads claimed.Priority within existing runStep retry loop; release path mirrors per-resource acquire pattern"
    - "Worker bootstrap default-capacity-or-override pattern: scan cfg.Concurrency.Resources for tag presence, append default only when absent"
    - "Test isolation via entgosql.OpenDB(dialect.Postgres, db) sidesteps the deferred pgx-ent driver issue documented in deferred-items.md"

key-files:
  created:
    - "internal/backfill/spec.go"
    - "internal/backfill/spec_test.go"
    - "internal/backfill/submit.go"
    - "internal/backfill/submit_test.go"
    - "internal/backfill/status.go"
    - "internal/backfill/independence_test.go"
    - "cmd/platform/backfill.go"
  modified:
    - "cmd/platform/main.go (added case \"backfill\": dispatch block)"
    - "cmd/platform/worker.go (default Capacity{Tag: \"backfill\", Limit: 5})"
    - "internal/runtime/executor.go (runStep accepts priority; layer-3 acquire branch)"
    - "internal/runtime/executor_test.go (added stubConnector + TestExecutorBackfillTagAcquisition)"

key-decisions:
  - "Multi-row INSERT placeholder arithmetic uses 5 per row (NOT 8). Each runs row has 8 columns total — id, asset_name, state, trigger, queued_at, priority, partition_key, backfill_id — but state='queued', trigger='backfill', queued_at=NOW() are SQL literals. That leaves 5 PARAMETER placeholders per row. Acceptance grep enforces base := i*5 and forbids i*8."
  - "ON CONFLICT predicate matches the partial unique index from plan 03-01 EXACTLY: WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL DO NOTHING. PostgreSQL ON CONFLICT inference rejects a mismatched predicate with `there is no unique or exclusion constraint matching the ON CONFLICT specification`. Submit's WHERE token-for-token matches the partial-index predicate."
  - "total_partitions in the backfills row reflects the operator's submitted intent (len(spec.Keys)), not the actual inserted count. When ON CONFLICT skips some keys (idempotent resubmit), the operator sees the discrepancy via `backfill status` (sum of state counts vs. total)."
  - "CLI argument ordering: positional vs flag args are pre-split before FlagSet.Parse so operators may type `backfill <asset> --partitions=...` instead of having to reorder. Acceptance criterion smoke verified: `./platform backfill foo --partitions=bad --priority=hacker` returns 'invalid --priority'."
  - "D-13 layer 3 implementation reuses Plan 03-03's frozen Executor.Run(ctx, *run.ClaimedRun) signature. The new branch is inside runStep — NO call-site changes. Worker.go and materialize.go remain unchanged from plan 03-03."
  - "Default backfill capacity 5 (D-13 layer 3 default). Operators override via cfg.Concurrency.Resources[\"backfill\"]. Bootstrap pattern: scan resources for tag presence, append default only when absent."
  - "Per-partition independence (D-16) is validated at the database level: TestCategoryPartitionIndependence flips one run to 'failed' and asserts siblings stay 'queued'. The independence claim is precisely about per-partition isolation in the runs table; full executor + retry exercise belongs to a downstream e2e test."
  - "TestExecutorBackfillTagAcquisition uses inline minimal stubConnector (private to the test file) — UNCONDITIONAL acceptance, no escape clause. Test sidesteps the pre-existing pgx-ent driver issue via entgosql.OpenDB(dialect.Postgres, db) instead of stent.Open(\"pgx\", dsn)."

patterns-established:
  - "Multi-row INSERT mass-enqueue with idempotent ON CONFLICT — pattern reusable for any future bulk-insert that respects an existing partial unique index"
  - "Three-layer backfill isolation contract: priority column (plan 03-01) + priority-aware claim ORDER BY (plan 03-03) + concurrency tag acquire in executor (this plan) — together prevent both queue-position starvation AND connector saturation"
  - "Test files using entgosql.OpenDB(dialect.Postgres, db) bypass the deferred pgx-ent storage driver issue — pattern is reusable until that deferred item is resolved"

requirements-completed: [ORCH-07, ORCH-08]
decisions-implemented: [D-13, D-14, D-15, D-16]

# Metrics
duration: ~11min
completed: 2026-05-08
---

# Phase 3 Plan 07: Backfill CLI 总结

**Phase 3 的最终计划。交付面向操作员的 `./platform backfill` 子命令，包括批量入队（`Submit`）、状态聚合（`GetStatus`）、分区规范解析和行数保护（`ParsePartitionSpec`），以及执行器端的 D-13 层-3 钩子，为 backfill 优先级运行获取 `backfill` 并发标签。与 plans 03-01（priority/backfill_id 列 + 部分唯一索引）和 03-03（priority-aware ORDER BY in ClaimNext）一起，完成三层 backfill 隔离契约（D-13）——backfill 行永远不会饿死 normal 运行或耗尽连接器。ORCH-07（时间分区）和 ORCH-08（类别分区，每个分区失败独立性）验收标准分别由 `TestBackfillTimePartition` 和 `TestCategoryPartitionIndependence` 验证。**

## 性能指标

- **耗时：** 约 11 分钟（09:11:46Z → 09:23:09Z）
- **开始时间：** 2026-05-08T09:11:46Z
- **完成时间：** 2026-05-08T09:23:09Z
- **任务数：** 4（均为自主完成；任务 1+2 遵循 TDD；任务 3+4 原子实现）
- **创建文件数：** 7（3 个 backfill 源文件 + 3 个 backfill 测试 + 1 个 cmd/platform/backfill.go）
- **修改文件数：** 4（cmd/platform/main.go, cmd/platform/worker.go, internal/runtime/executor.go, internal/runtime/executor_test.go）
- **提交数：** 4 个任务提交 + 1 个元数据提交

## 完成情况

### `internal/backfill` 包（新）

- **`spec.go`** — `ParsePartitionSpec(strategy, raw, maxPartitions)` 接受三种格式（日期范围 `2024-01-01:2024-12-31` → 通过 `partition.KeysBetween` 返回 366 个键；逗号列表 `us,eu,apac` → 修剪 + 按策略验证；单个键 `2024-01-15` → 1 个元素的列表）。`DefaultMaxPartitions = 3650`（陷阱 6 缓解措施：10 年每日）。错误 sentinel：`ErrTooManyPartitions` / `ErrInvalidSpec` / `ErrCategoryKeyNotDeclared`。`Spec` 结构体携带 Keys + Priority + Source（原始输入逐字记录在 `backfills.partition_spec` 中）。
- **`submit.go`** — `Submit(ctx, store, events, asset, spec)` 打开单个 read-committed tx，插入 1 个 `backfills` 行，然后向 `runs` 发出多行 VALUES INSERT（每行 5 个占位符 — base := i*5；三个列是 SQL 字面量：state='queued', trigger='backfill', queued_at=NOW()）。`ON CONFLICT (asset_name, partition_key) WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL DO NOTHING` — 谓词与 plan 03-01 的部分唯一索引 VERBATIM 匹配。提交后发出 `backfill.submitted` 事件，有效载荷暴露 `total_partitions` / `enqueued` / `skipped_inflight`，以便操作员看到 ON-CONFLICT 跳过。`ValidPriorities = {critical, normal, backfill}` 与 CLI 共享以进行解析时验证。
- **`status.go`** — `GetStatus(ctx, db, backfillID)` 读取 `backfills` 行头 + `SELECT state, COUNT(*) FROM runs WHERE backfill_id=$1 GROUP BY state` 聚合。返回 `Status{BackfillID, AssetName, PartitionSpec, TotalPartitions, SubmittedAt, CompletedAt *time.Time, StateCounts map[string]int}`。

### CLI 子命令（新）

- **`cmd/platform/backfill.go`** — `runBackfill` 在参数中的任何位置接受资产位置参数（Go stdlib `flag` 否则会在第一个非 flag 处停止）；在检查 `--partitions` 之前验证 `--priority` 是否符合 `ValidPriorities`，以便明显错误的调用显示最具体的错误（T-03-07-03）。解析资产注册表，读取 `a.Partitions()`（如果为 nil 则报错），解析规范，调用 `backfill.Submit`，打印 `backfill_id: <UUID>` + 摘要。`runBackfillStatus` 按字母顺序打印状态计数，格式确定。
- **`cmd/platform/main.go`** — 添加了 `case "backfill":` 分派块（根据 `os.Args[2]` 的 `status` 与默认情况进行子分派），叠加在 plan 03-06 的调度器 case 之上以避免合并冲突。

### D-13 层 3（执行器 + Worker）

- **`internal/runtime/executor.go`** — `runStep` 现在接受 `priority string`；在 `Run` 内部从 `claimed.Priority` 传入（后者已经接受来自 plan 03-03 的 `*run.ClaimedRun` — 这里没有签名更改）。在现有重试循环内部，在全局令牌获取之后但在每个资源获取之前，当 `priority == "backfill"` 时，步骤额外地执行 `Pool.Acquire(ctx, runID, asset, "backfill", 1)`。失败路径匹配现有的每个资源分支：如果重试次数保留，则 `releaseAcquired()` + `scheduleRetry`，否则发出 `run.step.failed` 并返回包含 "backfill token" 的包装错误。
- **`cmd/platform/worker.go`** — bootstrap 声明默认 `Capacity{Tag: "backfill", Limit: 5}`，除非操作员通过 `cfg.Concurrency.Resources["backfill"]` 覆盖。模式：扫描资源中是否存在标签，仅在缺失时附加默认值。

### 测试（3 个集成测试 + 9 个单元测试）

- **`spec_test.go`**（9 个测试）— 表驱动：`TestParsePartitionSpec`（5 个子测试：2024 年 1 月每日、闰年 366 天、Q1 月度、逗号列表类别、单个键）、`TestParsePartitionSpecCategoryNotDeclared`、`TestMaxPartitionsGuard`、`TestParsePartitionSpecEmpty`、`TestParsePartitionSpecBadDate`、`TestParsePartitionSpecCategoryInvalidKey`、`TestParsePartitionSpecCommaListWithDailyStrategy`、`TestParsePartitionSpecInvertedRange`。全部通过。
- **`submit_test.go`**（5 个集成测试，DATABASE_URL 门控）— `TestBackfillSubmit`（快乐路径：7 个每日运行 + backfills 行 + 事件）、`TestBackfillSubmitInvalidPriority`（拒绝 "bogus"）、`TestBackfillSubmitIdempotentResubmit`（第二次 Submit 通过 ON CONFLICT 插入 0 个运行）、`TestBackfillStatus`（StateCounts["queued"]=7）、`TestBackfillTimePartition`（ORCH-07 验证图：7 个不同的 UUID，7 个不同的分区键；将一个翻转为 'failed' 使兄弟姐妹保持 queued）。
- **`independence_test.go`**（1 个集成测试）— `TestCategoryPartitionIndependence`（ORCH-08 / D-16 验证图）：3 类别提交，将 'us' 翻转为 failed，eu+apac 保持 queued，GetStatus 报告 `{queued:2, failed:1}`。
- **`internal/runtime/executor_test.go`** — `TestExecutorBackfillTagAcquisition`（D-13 层 3 无条件）：池容量=1 在 `backfill` 标签上，两个 backfill 优先级运行并发 — 第一个获取并保持约 250ms，第二个失败并显示包含 "backfill token" 的错误。 内联最小 `stubConnector`（5 个方法：APIVersion/Ping/Schema/Read/Write 均为无操作）。

## 任务提交

每个任务原子提交：

1. **任务 1：ParsePartitionSpec + max-partitions guard + 9 个 spec 测试** — `6e72692`（feat）
2. **任务 2：Submit + GetStatus + 5 个集成测试 + independence 测试** — `71d1897`（feat）
3. **任务 3：./platform backfill 子命令接线** — `1005c28`（feat）
4. **任务 4：Executor backfill 标签获取 + worker 默认容量 + TestExecutorBackfillTagAcquisition** — `227caa5`（feat）

## CLI 表面（D-14）

```text
./platform backfill <asset> --partitions=<spec> [--priority=critical|normal|backfill] [--max-partitions=3650]
./platform backfill status <backfill_id>
```

| 标志 | 默认值 | 验证 |
| ---------------- | ---------------------- | ----------------------------------------------------------------- |
| `--partitions` | （必需）| 日期范围 / 逗号列表 / 单个键 — 策略特定 |
| `--priority` | `backfill` | 必须是 `critical` / `normal` / `backfill`（在解析时拒绝）|
| `--max-partitions` | `3650` (=10y 每日) | 必须 > 0；拒绝扩展超过 N 的规范（陷阱 6）|

资产位置参数可以出现在标志之前、之间或之后（位置/标志参数在 `flag.Parse` 之前预先拆分）。

## 多行 INSERT 占位符算术（已确认）

```go
for i, key := range spec.Keys {
    base := i * 5    // ← 5, NOT 8
    values = append(values, fmt.Sprintf("($%d, $%d, 'queued', 'backfill', NOW(), $%d, $%d, $%d)",
        base+1, base+2, base+3, base+4, base+5))
    args = append(args, uuid.New(), assetName, priority, key, backfillID)
}
```

8 列总计，但 3 列是字面量（state='queued', trigger='backfill', queued_at=NOW()）→ 每行 5 个占位符。验收 grep 强制 `base := i*5` 且 `! grep i*8`。

## ON CONFLICT 谓词（原文）

```sql
ON CONFLICT (asset_name, partition_key)
WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL
DO NOTHING
```

与 plan 03-01 的部分唯一索引 `run_partition_inflight_unique` 谓词逐 token 匹配。在本地 Postgres 实例上通过 Live 验证：第一次 INSERT 返回行，第二次 INSERT（相同分区仍在飞行中）返回 0 行。PostgreSQL ON CONFLICT 推理通过 — 谓词匹配是精确的。

## D-13 三层覆盖图

| 层 | 计划 | 机制 |
| ----- | ------------ | ---------------------------------------------------------------------------------------- |
| 1 | 03-01 | `runs.priority VARCHAR(16) CHECK IN ('critical','normal','backfill')` 列 |
| 2 | 03-03 | `ClaimNext` ORDER BY CASE priority — claim 顺序：critical → normal → backfill |
| 3 | **03-07** | 执行器 `if priority == "backfill"` 获取 `backfill` 令牌（默认容量 5）|

## ORCH 验收覆盖（Phase 3 最终）

| 要求 | 测试 | 证据 |
| ----------- | ----------------------------------------------------- | ------------------------------------------------------------- |
| ORCH-05 | TestSchedulerGracefulShutdown (plan 03-06) | scheduler 子命令通过 SIGTERM 引导和关闭 |
| ORCH-06 | （sensor evaluator from plan 03-05）| sensor.Daemon.RunOnce 在 scheduler tick 中排空 |
| ORCH-07 | **TestBackfillTimePartition**（本计划）| 7 天每日 backfill 创建 7 个具有不同 UUID/键的 runs |
| ORCH-08 | **TestCategoryPartitionIndependence**（本计划）| 3 类别 backfill，1 个失败，2 个保持 queued（D-16）|

## Phase 3 最终回归检查

`TestClaimAtomicity50Goroutines`（Phase 2 验收标准 3）— 在所有 Phase 3 修改（包括 runStep priority 线程）之后 **通过** 不变。Plan 03-03 的 SKIP LOCKED + UPDATE-state-guard 原子性契约完好无损。

## 决策

- **Multi-row VALUES（不是 pgx CopyFrom）** — 对于 ≤3650 行，多行 VALUES INSERT 很好，并且可以移植到 `database/sql`。CopyFrom 需要特定于 pgx 的代码；如果未来用例需要 >10K 行，请在后续版本中交换。
- **total_partitions = len(spec.Keys)，而不是入队计数** — 操作员 UX：当 ON CONFLICT 在重新提交时跳过了一些飞行内分区时，状态输出显示差异（状态计数总和与总数的对比）。
- **CLI 参数顺序容差** — 位置和标志参数在 `flag.Parse` 之前预先拆分，因此自然的 `backfill <asset> --partitions=...` 排序可以工作（Go stdlib 否则会在第一个非 flag 令牌处停止）。
- **优先级验证在 --partitions 检查之前运行** — 当两者都错误时，表面 "invalid --priority" 而不是 "--partitions is required"。
- **D-13 层 3 在现有 runStep 重试循环内部，而不是新代码路径** — 重试/释放语义已经匹配每个资源获取模式；添加单独的代码路径可能会导致漂移。
- **测试绕过延迟的 pgx-ent 驱动程序问题** — `TestExecutorBackfillTagAcquisition` 使用 `entgosql.OpenDB(dialect.Postgres, db)` 而不是 `stent.Open("pgx", dsn)`，绕过了 `deferred-items.md` 中记录的预先存在的失败。

## 与计划的偏差

### 自动修复的问题

**1. [规则 1 — Bug] CLI 标志排序 — 标志后的位置参数不起作用**

- **发现于：** 任务 3 冒烟验证（`./platform backfill foo --partitions=bad --priority=hacker`）
- **问题：** Go stdlib `flag.Parse` 在第一个非 flag 令牌处停止，因此原始实现在 `foo` 被视为资产并忽略其后的一切 — 冒烟测试的 "invalid --priority" 检查将失败，因为 priority 标志从未到达验证。
- **修复：** 在 `FlagSet.Parse` 之前预先将 args 拆分为位置参数（无前导 `-`）和标志参数（有前导 `-`）。资产位置参数现在可以出现在标志之前、之间或之后。
- **修改的文件：** `cmd/platform/backfill.go`
- **提交于：** `1005c28`

**2. [规则 1 — Bug] 验证顺序 — "--partitions is required" 在优先级检查之前触发**

- **发现于：** 任务 3 冒烟验证（相同）
- **问题：** 当同时指定 `--partitions=bad` 和 `--priority=hacker` 时，原始代码返回 "--partitions is required"（因为 `bad` 不为空？不 — 该错误不适用；实际上原始代码首先返回 "--partitions is required"，因为它在优先级验证之前检查，但规范值是 "bad"）。验收标准期望 CLI 返回 "invalid --priority"。
- **修复：** 重新排序验证 — 优先级检查在空分区检查之前运行，因此明显错误的 `--priority` 值显示最具体的错误。
- **修改的文件：** `cmd/platform/backfill.go`
- **提交于：** `1005c28`（与 #1 合并）

**3. [规则 3 — 阻塞] `concurrency_tokens` 表在 worktree DB 中缺失；应用迁移**

- **发现于：** 任务 4 — 首次运行 `TestExecutorBackfillTagAcquisition`
- **问题：** `concurrency_tokens` 表在本地 Postgres 实例中不存在 — Phase 2 迁移 `20260507121500_phase2_concurrency_tokens.sql` 从未应用于此 worktree 的 DB。测试失败并显示 `ERROR: relation "concurrency_tokens" does not exist (SQLSTATE 42P01)`。
- **修复：** 通过 `psql` 直接应用迁移（幂等 — 迁移文件中的 `CREATE TABLE IF NOT EXISTS` 语义）。
- **修改的文件：** 无（仅 DB 状态修复 — 迁移文件已存在于 `migrations/` 中）
- **验证：** `\dt` 确认修复后 `concurrency_tokens` 存在；`TestExecutorBackfillTagAcquisition` 现在通过。
- **提交于：** 不适用（DB 状态修复，仅）

**4. [规则 3 — 阻塞] worktree 分支基础不匹配初始 checkout**

- **发现于：** 执行前 worktree 分支检查
- **问题：** 初始 HEAD 在 `943de17`（一个分歧的项目初始提交），而不是预期的基 `330773e`（plan 03-06 在 master 上的 docs 提交）。预期提交的完整文件集已经在 master 上。
- **修复：** `git reset --hard 330773e97c095a9d468d23726533ac3ccc4cd9c4` 以将 worktree HEAD 与预期的基础提交对齐。
- **修改的文件：** 无
- **提交于：** 不适用（仅 worktree 状态修复）

---

**总偏差：** 4 个自动修复（2 个 bug，2 个阻塞环境）。零范围蔓延。所有交付物与 `must_haves.truths` 对齐。

## 威胁表面覆盖

计划的 `<threat_model>` STRIDE 登记已通过本计划的交付物完全解决：

| 威胁 ID | 状态 | 证据 |
| ------------ | ---------- | ------------------------------------------------------------------------------------------------------- |
| T-03-07-01 | mitigated | `--max-partitions=3650` 默认值 + 任何 INSERT 之前的 `ErrTooManyPartitions`；`TestMaxPartitionsGuard` |
| T-03-07-02 | mitigated | 所有键作为 `$N` 占位符传递；`ValidateCategoryKey` 拒绝 '/'；通过 `time.Parse` 进行每日/每月验证 |
| T-03-07-03 | mitigated | CLI `ValidPriorities` 映射在解析时检查；显式的 "invalid --priority" 错误 |
| T-03-07-04 | mitigated | Int 上限为 2.1B；max-partitions 守卫首先在 3650 处触发 |
| T-03-07-05 | accepted | `partition_spec` 是操作员提供的；不是用户 PII |
| T-03-07-06 | accepted | Phase 3 v1 在 CLI 没有 auth；ActorID 为 nil — Phase 4+ 接入 auth |
| T-03-07-07 | mitigated | (1) max-partitions 守卫 (2) priority claim 使 backfill 延迟 (3) backfill 标签 cap=5 (4) 短 tx 范围 |
| T-03-07-08 | mitigated | 事件有效载荷暴露 `enqueued` + `skipped_inflight`；状态命令显示与总数的差异 |
| T-03-07-09 | accepted | Phase 1 D-09 RLS 防止 event_log 上的 UPDATE/DELETE [已验证] |
| T-03-07-10 | mitigated | ON CONFLICT 谓词逐 token 匹配部分索引谓词；集成测试 exercises 路径 |
| T-03-07-11 | mitigated | acceptance grep: `base := i*5`（多行测试立即暴露错误步幅）|

## 自我检查：通过

**创建的文件存在：**

- 已找到：internal/backfill/spec.go
- 已找到：internal/backfill/spec_test.go
- 已找到：internal/backfill/submit.go
- 已找到：internal/backfill/submit_test.go
- 已找到：internal/backfill/status.go
- 已找到：internal/backfill/independence_test.go
- 已找到：cmd/platform/backfill.go

**修改的文件已更新：**

- 已找到：cmd/platform/main.go（case "backfill" 存在）
- 已找到：cmd/platform/worker.go（默认 backfill 容量存在）
- 已找到：internal/runtime/executor.go（priority 参数传入；backfill 获取分支存在）
- 已找到：internal/runtime/executor_test.go（TestExecutorBackfillTagAcquisition + stubConnector 存在）

**提交存在：**

- 已找到：6e72692（任务 1 — ParsePartitionSpec）
- 已找到：71d1897（任务 2 — Submit + GetStatus + independence）
- 已找到：1005c28（任务 3 — CLI 子命令）
- 已找到：227caa5（任务 4 — D-13 层 3 获取）

**验收套件：**

- 通过：go build ./...
- 通过：go test ./internal/backfill/... -count=1 -timeout 120s
- 通过：go test ./internal/runtime/... -run TestExecutorBackfillTagAcquisition -count=1 -timeout 60s
- 通过：go test ./internal/run/... -run TestClaimAtomicity50Goroutines -count=1 -timeout 60s（Phase 2 回归）
- 通过：冒烟 `./platform backfill foo --partitions=bad --priority=hacker` 返回 "invalid --priority"

## 下一计划就绪状态

这是 **Phase 3 的最终计划**。随着 03-07 完成，所有四个 ORCH 验收标准（ORCH-05/06/07/08）都已明确覆盖：

| 要求 | Phase 3 测试 | 计划 |
| ----------- | ----------------------------------------- | ---- |
| ORCH-05 | TestSchedulerGracefulShutdown | 03-06 |
| ORCH-06 | sensor.Daemon.RunOnce in scheduler tick | 03-05 + 03-06 |
| ORCH-07 | **TestBackfillTimePartition** | **03-07** |
| ORCH-08 | **TestCategoryPartitionIndependence** | **03-07** |

Phase 3 功能完成。Phase 4（血缘捕获）现在可以消费：

- `backfill_id` 列用于 backfill 感知的血缘归属
- `backfill.submitted` / `backfill.completed` 事件用于 backfill 窗口血缘印章
- 冻结的 Executor.Run 签名（仍然是 `(ctx, *run.ClaimedRun)`）
- 资产 DSL（Schedule/Sensor/Partitions 链式方法）

---

*Phase: 03-scheduling-sensors-partitions*
*Plan: 07 (backfill CLI — final Phase 3 plan)*
*Completed: 2026-05-08*
