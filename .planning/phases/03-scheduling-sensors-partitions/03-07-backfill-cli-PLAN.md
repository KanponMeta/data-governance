---
phase: 03-scheduling-sensors-partitions
plan: 07
title: ./platform backfill CLI — 提交服务 + 状态 + max-partitions 守卫 + 每分区独立性测试
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
  - cmd/platform/worker.go
  - internal/runtime/executor.go
  - internal/runtime/executor_test.go
autonomous: true
must_haves:
  truths:
    - "./platform backfill <asset> --partitions=<spec> [--priority=backfill] [--max-partitions=N] CLI 子命令与 scheduler/server/worker/materialize 并存"
    - "./platform backfill status <backfill_id> CLI 子命令打印聚合状态计数"
    - "ParsePartitionSpec 接受三种格式：日期范围 \"2024-01-01:2024-12-31\"、逗号列表 \"us,eu,apac\"、单个 key \"2024-01-15\""
    - "Submit() 在一个事务中插入 backfills 行 + N 个 runs 行；runs 具有 priority='backfill'、trigger='backfill'、backfill_id 已设置、每个规范的 partition_key"
    - "Submit() 多行 INSERT 每行使用 5 个占位符（id、asset_name、priority、partition_key、backfill_id）— 基础索引 `i*5`，而不是 `i*8`"
    - "Submit() ON CONFLICT 谓词与计划 03-01 中的部分唯一索引完全匹配：ON CONFLICT (asset_name, partition_key) WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL DO NOTHING"
    - "max-partitions 守卫（默认 3650）在 CLI 解析时拒绝超过限制的规范——防止 100 年回填行数爆炸（陷阱 6）"
    - "--priority 值在 CLI 解析时针对 {critical,normal,backfill} 验证；默认 'backfill'；用用法错误拒绝未知值（陷阱：优先级提升）"
    - "Executor 从 ClaimedRun 结构体（按计划 03-03 的最终签名已存在）读取 claimed.Priority，并为 backfill-priority 运行获取 `backfill` 并发标签——此计划中无 Executor.Run 签名更改"
    - "cmd/platform/worker.go 引导声明默认 backfill 容量 {Tag: \"backfill\", Limit: 5}（D-13 第 3 层默认值）"
    - "TestExecutorBackfillTagAcquisition 无条件通过——使用内联最小桩连接器；无逃生舱口，无\"如果模拟证明重量级则延期\""
    - "TestCategoryPartitionIndependence 证明：1 个类别失败的 3 类别回填独立完成——兄弟类别达到 'succeeded' 状态（D-16）"
    - "TestBackfillTimePartition 证明：7 天每日回填创建 7 个具有不同 partition_key 的运行，每个运行有自己的 event_log 条目"
  artifacts:
    - path: "internal/backfill/submit.go"
      provides: "Submit(ctx, store, events, assetName, keys, priority) (uuid.UUID, error) — 批量入队 + backfills 行"
      contains: "INSERT INTO runs"
    - path: "internal/backfill/spec.go"
      provides: "ParsePartitionSpec(strategy, spec, maxPartitions) (Spec, error) — 验证 + 展开"
      contains: "func ParsePartitionSpec"
    - path: "internal/backfill/status.go"
      provides: "GetStatus(ctx, db, backfillID) (*Status, error) — 聚合运行状态计数"
      contains: "func GetStatus"
    - path: "cmd/platform/backfill.go"
      provides: "runBackfill / runBackfillStatus 子命令处理器"
      contains: "func runBackfill"
    - path: "cmd/platform/worker.go"
      provides: "Worker 引导声明默认 backfill 容量 5"
      contains: "Tag: \"backfill\""
    - path: "internal/runtime/executor.go"
      provides: "Executor 读取 claimed.Priority 并获取 backfill 标签（无签名更改——使用计划 03-03 的 *run.ClaimedRun）"
      contains: "claimed.Priority"
  key_links:
    - from: "cmd/platform/backfill.go runBackfill"
      to: "internal/backfill.Submit"
      via: "通过 ParsePartitionSpec 解析 --partitions 规范，调用 Submit，打印 backfill_id 到 stdout"
      pattern: "backfill.Submit"
    - from: "internal/backfill.Submit"
      to: "PostgreSQL runs + backfills 表"
      via: "INSERT backfills 行 + 在一个 tx 中 INSERT runs 行；提交后发出 backfill.submitted 事件"
      pattern: "INSERT INTO runs.*priority.*partition_key.*backfill_id"
    - from: "internal/backfill.Submit ON CONFLICT 谓词"
      to: "migrations/20260508120000_phase3_runs_columns.sql 部分唯一索引谓词"
      via: "谓词必须完全匹配：WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL"
      pattern: "WHERE state IN \\('queued','starting','running'\\) AND partition_key IS NOT NULL"
    - from: "internal/runtime.Executor.Run"
      to: "Phase 2 concurrency.Pool"
      via: "如果 claimed.Priority == \"backfill\"，除了全局+资源 token 外，还 pool.Acquire(ctx, runID, assetName, \"backfill\", 1)"
      pattern: "claimed\\.Priority.*backfill"
  - from: "cmd/platform/worker.go 引导"
    to: "internal/concurrency.Pool capacities"
    via: "除非 cfg.Concurrency.Resources[\"backfill\"] 覆盖，否则追加 Capacity{Tag: \"backfill\", Limit: 5} 默认值"
    pattern: "Tag: \"backfill\".*Limit: 5"
---

<objective>
实现回填提交 CLI：`./platform backfill <asset> --partitions=<spec>` 将规范解析为分区 key 列表，根据 `--max-partitions` 验证，批量入队 N 个 `priority='backfill'` 的运行，并创建将它们绑在一起的 `backfills` 行。`./platform backfill status <backfill_id>` 聚合运行状态计数。

这是最后一个 Phase 3 计划。它依赖于计划 03-03（优先级感知认领必须在回填运行大规模提交之前工作；Executor.Run 已从 03-03 接受 `*run.ClaimedRun` — 此计划不再次更改签名）和计划 03-06（cmd/platform/main.go switch 必须已有 `case "scheduler":` 块以避免合并冲突）。

此计划还提供满足 ORCH-07 和 ORCH-08 验收的两个集成测试：
- **TestBackfillTimePartition**（验证映射）— 每日分区回填创建每分区运行，每分区 event_log 条目
- **TestCategoryPartitionIndependence**（验证映射）— 一个类别失败时不阻塞兄弟的类别分区回填（D-16）

**签名稳定性：** 计划 03-03 将 `Executor.Run(ctx, claimed *run.ClaimedRun) error` 设置为最终 Phase 3 签名。此计划仅在执行器体内添加对 `claimed.Priority` 的读取以驱动第 3 层 token 标签获取——无签名更改，无对 worker.go 或 materialize.go 的调用站点更改（那些已按 03-03 传递 `*run.ClaimedRun`）。
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
此计划实现 D-13 第 3 层（backfill 资源标签——依赖于 Phase 2 的现有并发 token 池；无需 schema 更改）、D-14（CLI 表面）、D-15（批量入队 + 通过现有池限制 max_concurrent_backfill）、D-16（每分区独立失败语义）。

**为什么 Wave 4：** 依赖于计划 03-03（优先级感知认领必须工作——回填提交依赖于 `priority='backfill'` 实际延迟认领；ClaimedRun 结构体必须携带 Priority 字段；Executor.Run 已接受 `*run.ClaimedRun`）和计划 03-06（cmd/platform/main.go switch 已有 scheduler case — backfill case 分层在其上以避免同时编辑 main.go）。depends_on = [03, 06]。

**为什么这是 Wave 4 而不是 Wave 3：** 计划 03-06 也编辑 cmd/platform/main.go（添加 `case "scheduler":`）。为防止合并冲突，scheduler 子命令和 backfill 子命令按顺序排列——03-06 先，然后 03-07 分层在上面。

**为什么 max-partitions 守卫（陷阱 6）：** D-15 接受"立即入队全部"但未指定批大小限制。意外输入 `--partitions=1900-01-01:2026-12-31` 的用户会在单个事务中创建 46K 行，持有独占锁数秒。我们添加 `--max-partitions=N`（默认 3650 = 10 年每日）作为 CLI 标志，守卫在 INSERT 之前检查。如果实际用例需要，操作员可通过 `--max-partitions=10000` 覆盖。记录在调度器帮助文本中。

**为什么在 CLI 解析时验证优先级（D-13 + 安全领域）：** `--priority` 标志接受 `critical|normal|backfill`。我们在 CLI 解析时拒绝任何其他值，然后才到达 DB。DB CHECK 约束（计划 03-01）是纵深防御。CLI 验证向操作员提供有用的错误消息而不是通用的约束违规。

**为什么每分区失败独立性（D-16）：** 每个分区是自己的 runs 行；现有的执行器 + Phase 2 重试策略处理每行重试。如果 1 个分区耗尽重试并达到 'failed'，其他 364 个独立继续。`backfill status` 查询简单地聚合状态计数——操作员提交一个新的限定于失败子集的回填以重试。记录在 CLI 帮助文本中。

**为什么 Submit 使用 pgx 多行 INSERT 而不是 CopyFrom（模式 7）：** 对于 ≤3650 行（默认限制），多行 VALUES 很好，并与其他地方使用的 database/sql 接口保持可移植性。CopyFrom 需要 pgx 特定代码。如果未来的用例需要 >10K 行，在后续交换为 CopyFrom — 对于 v1，多行 VALUES 更简单。

**为什么多行 INSERT 每行使用 5 个占位符（不是 8）：** 每个 `runs` 行有 8 列：id、asset_name、state、trigger、queued_at、priority、partition_key、backfill_id。其中三列是 SQL 字面量（`state='queued'`、trigger='backfill'、`NOW()` for queued_at）。这留下**每行 5 个参数占位符**：id、asset_name、priority、partition_key、backfill_id。值构建器必须使用 `base := i*5`（不是 `i*8`）——否则第二行的占位符指向 args 切片之后，PostgreSQL 返回参数绑定错误。

**为什么 ON CONFLICT 谓词必须与部分索引谓词完全匹配：** 计划 03-01 创建 `run_partition_inflight_unique ON runs (asset_name, partition_key) WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL`。PostgreSQL ON CONFLICT 推理要求冲突目标上的 WHERE 子句与部分索引谓词**逐 token 匹配**（空格不敏感但运算符/字面量敏感）。如果我们从 Submit 的 ON CONFLICT 中省略 `AND partition_key IS NOT NULL`，PostgreSQL 响应：`ERROR: there is no unique or exclusion constraint matching the ON CONFLICT specification`。应用程序代码和索引必须一致。

**为什么 Submit 对分区唯一性使用 ON CONFLICT DO NOTHING：** 计划 03-01 的部分唯一索引拒绝重复的进行中运行。Submit 的 ON CONFLICT 子句说"如果分区已在进行中，静默跳过"——使回填重新提交幂等。状态查询反映最终创建的运行数，可能少于 `total_partitions`（如果某些被跳过）。我们记录跳过的计数。

**为什么 backfills 表中的 `total_partitions` 反映完整意图（而非实际插入计数）：** 操作员 UX — 如果我提交 7 天回填而 2 个分区已在进行中，状态命令应显示"5/7 入队，2 个跳过（已在进行中）"而不是静默缩小总数。这需要 Submit 记录输入长度和单独的 `enqueued_count`（或在状态时通过 JOIN 计算）。对于 Phase 3 v1，更简单：`total_partitions = len(keys)`（意图），状态查询通过 `SELECT count(*) FROM runs WHERE backfill_id=$1` 计算实际运行行。操作员直接看到差异。

**为什么 D-13 第 3 层适合此计划（执行器分支在 claimed.Priority）：** 计划 03-03 已将 `Executor.Run` 更改为接受 `*run.ClaimedRun`。结构体已携带 `Priority string`。此计划在现有执行器体内添加一个微小的 `if claimed.Priority == "backfill"` 分支，除全局 + 每资源 token 外还获取一个额外的 `backfill` 标签。**无签名更改。** Worker.go 和 materialize.go 保持不变（来自计划 03-03）。此计划在 `internal/backfill/*` 之外进行的唯一更新是：
- `internal/runtime/executor.go` — 添加优先级驱动的获取分支（并在失败路径上匹配释放）。
- `cmd/platform/worker.go` — 在引导容量切片中声明默认 `Capacity{Tag: "backfill", Limit: 5}`。

**消耗的冻结接口：**
- `internal/run.ClaimedRun.Priority`（计划 03-03 — 已扩展结构体）
- `internal/run.PriorityBackfill` 常量（计划 03-03）
- `internal/runtime.Executor.Run(ctx, claimed *run.ClaimedRun)`（计划 03-03 最终签名 — 此计划不变）
- `internal/concurrency.Pool.Acquire(ctx, runID, assetName, tag, weight)`（Phase 2）
- `internal/partition.KeysBetween`、`partition.ValidateCategoryKey`、所有 PartitionStrategy 类型（计划 03-02）
- `internal/asset.DefinitionRegistry`、`Asset.Partitions()`（计划 03-02）
- `internal/event.EventTypeBackfillSubmitted/RunEnqueued/Completed`（计划 03-01）

@.planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md
@.planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md
@.planning/phases/03-scheduling-sensors-partitions/03-VALIDATION.md
@cmd/platform/main.go
@cmd/platform/scheduler.go
@cmd/platform/materialize.go
@cmd/platform/worker.go
@internal/run/claim.go
@internal/concurrency/pool.go
@internal/runtime/executor.go
@migrations/20260508120000_phase3_runs_columns.sql

<interfaces>
<!-- 计划 03-01 + 03-02 + 03-03 + 03-06 暴露此计划消耗的接口。 -->

```go
// runs 表：priority + backfill_id + partition_key 列（计划 03-01）
// backfills 表：id、asset_name、partition_spec、status、total_partitions、submitted_at、completed_at
// concurrency_tokens 表：现有 Phase 2；"backfill" 标签默认容量 5

// 计划 03-02：
type PartitionStrategy interface { isPartitionStrategy(); Kind() string }
func KeysBetween(strategy PartitionStrategy, start, end time.Time) ([]string, error)
func ValidateCategoryKey(key string) error
func DailyKey(t time.Time) string

// 计划 03-03（FROZEN — 此计划不更改）：
const PriorityBackfill = "backfill"
func PriorityOrder(p string) int
type ClaimedRun struct {
    ID uuid.UUID; AssetName string; Trigger string; QueuedAt time.Time;
    PartitionKey *string; Priority string; BackfillID *uuid.UUID
}
func (e *Executor) Run(ctx context.Context, claimed *run.ClaimedRun) error  // ← FROZEN

// 计划 03-01 事件：
EventTypeBackfillSubmitted   EventType = "backfill.submitted"
EventTypeBackfillRunEnqueued EventType = "backfill.run_enqueued"
EventTypeBackfillCompleted   EventType = "backfill.completed"
```

此计划产生：
```go
package backfill

const DefaultMaxPartitions = 3650

type Spec struct {
    Keys     []string
    Priority string  // "critical" | "normal" | "backfill"
    Source   string  // 原始用户提供的规范用于审计（存储在 backfills.partition_spec）
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
  <name>任务 1：创建 internal/backfill/spec.go ParsePartitionSpec + max-partitions 守卫 + 测试</name>
  <files>internal/backfill/spec.go, internal/backfill/spec_test.go</files>
  <read_first>
    - internal/partition/strategy.go（来自计划 03-02 的 PartitionStrategy 类型）
    - internal/partition/keygen.go（来自计划 03-02 的 KeysBetween + ValidateCategoryKey）
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § 模式 8 — 分区规范解析
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § 陷阱 6 — 回填行数爆炸
  </read_first>
  <behavior>
    - ParsePartitionSpec("2024-01-01:2024-12-31", DailyPartitions{}, 3650) 返回 366 个 key（2024 是闰年）
    - ParsePartitionSpec("us,eu,apac", CategoryPartitions{Keys:["us","eu","apac"]}, 3650) 返回 ["us","eu","apac"]
    - ParsePartitionSpec("us,eu,apac", CategoryPartitions{Keys:["us","eu"]}, 3650) 返回错误 — "apac" 未在声明的 key 中
    - ParsePartitionSpec("2024-01-15", DailyPartitions{}, 3650) 返回 ["2024-01-15"]（单个 key）
    - ParsePartitionSpec("2024-01-01:2024-12-31", DailyPartitions{}, 100) 返回 ErrTooManyPartitions 包装 "366 exceeds limit 100"
    - ParsePartitionSpec("us,eu", DailyPartitions{}, 3650) — 带每日策略的逗号列表：每个项目必须解析为每日 key；"us" 失败 → 错误
    - ParsePartitionSpec("us/east", CategoryPartitions{Keys:["us/east"]}, 3650) 返回错误 — ValidateCategoryKey 拒绝 '/'（与计划 03-02 中构建器验证一致）
    - ParsePartitionSpec(spec="", ...) 返回错误（空规范）
    - ParsePartitionSpec("2024-01-01:2023-12-31", ...) 返回错误（结束在开始之前，从 KeysBetween 传播）
  </behavior>
  <action>
    1. 创建 `internal/backfill/spec.go`：
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
    2. 创建 `internal/backfill/spec_test.go`，包含表驱动测试：
       - `TestParsePartitionSpec`（验证映射：同名）— 覆盖所有四种策略的三种格式；验证验证映射用例：
         - 日期范围每日 2024 年 1 月 → 31 个 key，首个 "2024-01-01"，末个 "2024-01-31"
         - 日期范围每月 2024 年 Q1 → 3 个 key ["2024-01","2024-02","2024-03"]
         - 逗号列表类别 us,eu,apac，声明 us,eu,apac → ["us","eu","apac"]
         - 单个 key "2024-01-15" 每日 → ["2024-01-15"]
       - `TestParsePartitionSpecCategoryNotDeclared` — 逗号列表与未在声明 key 中的 key 返回 ErrCategoryKeyNotDeclared
       - `TestMaxPartitionsGuard`（验证映射：同名）— 扩展为 366 个 key 且 maxPartitions=100 的日期范围返回 ErrTooManyPartitions
       - `TestParsePartitionSpecEmpty` — 空原始规范返回 ErrInvalidSpec
       - `TestParsePartitionSpecBadDate` — "not-a-date:2024-12-31" 返回包含 "start date" 的包装 ErrInvalidSpec
       - `TestParsePartitionSpecCategoryInvalidKey` — "us/east" 返回 ErrInvalidSpec（委托给 ValidateCategoryKey）
       - `TestParsePartitionSpecCommaListWithDailyStrategy` — 带 DailyPartitions 的 "us,eu" 返回 ErrInvalidSpec（每个项目必须解析为每日 key）
  </action>
  <acceptance_criteria>
    - 文件 `internal/backfill/spec.go` 存在且为 `package backfill`
    - `grep -q 'func ParsePartitionSpec' internal/backfill/spec.go`
    - `grep -q 'DefaultMaxPartitions = 3650' internal/backfill/spec.go`
    - `grep -q 'ErrTooManyPartitions' internal/backfill/spec.go`
    - `grep -q 'ErrCategoryKeyNotDeclared' internal/backfill/spec.go`
    - `go test ./internal/backfill/... -run TestParsePartitionSpec -count=1 -timeout 30s` 退出 0
    - `go test ./internal/backfill/... -run TestMaxPartitionsGuard -count=1 -timeout 30s` 退出 0
    - `go test ./internal/backfill/... -count=1 -timeout 30s` 退出 0
  </acceptance_criteria>
  <verify>
    <automated>go test ./internal/backfill/... -count=1 -timeout 30s</automated>
  </verify>
  <done>internal/backfill/spec.go 包含 ParsePartitionSpec + max-partitions 守卫 + 每策略 key 验证；所有 7 个 spec 测试通过。</done>
</task>

<task id="3.7.2" type="auto" tdd="true">
  <name>任务 2：创建 internal/backfill/submit.go（正确 5 占位符构建器的批量入队 + 匹配 ON CONFLICT 谓词）+ status.go（状态聚合）+ 集成测试</name>
  <files>internal/backfill/submit.go, internal/backfill/submit_test.go, internal/backfill/status.go, internal/backfill/independence_test.go</files>
  <read_first>
    - internal/backfill/spec.go（刚刚创建 — Spec 结构体）
    - internal/event/types.go（来自计划 03-01 的 EventTypeBackfillSubmitted/RunEnqueued/Completed）
    - internal/run/claim_test.go（辅助模式：openTestDB、sqlStorage、deleteRuns）
    - migrations/20260508120000_phase3_runs_columns.sql（验证部分唯一索引谓词为 `WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL`）
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § 模式 7 — 回填批量入队
    - .planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md § D-15、D-16
  </read_first>
  <behavior>
    - Submit(ctx, store, events, assetName, spec) 插入：1 个 backfills 行，N 个 runs 行，全部在一个 tx 中
    - 每个 runs 行有 state='queued'、trigger='backfill'、priority=spec.Priority（默认 'backfill'）、backfill_id = newID、partition_key 来自 spec.Keys[i]
    - 多行 VALUES 构建器每行使用**5 个占位符**（`base := i*5`），用于：id、asset_name、priority、partition_key、backfill_id。3 个字面量列（state='queued'、trigger='backfill'、queued_at=NOW()）不是占位符。
    - ON CONFLICT 子句：`ON CONFLICT (asset_name, partition_key) WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL DO NOTHING` — 谓词与计划 03-01 的部分唯一索引完全匹配（空格不敏感，运算符/字面量敏感）。如果没有 `AND partition_key IS NOT NULL`，PostgreSQL 返回"no unique or exclusion constraint matching"。
    - 提交后，发出带有包括 total_partitions 和 source 的有效负载的 backfill.submitted 事件
    - Submit 返回新的 backfill_id（UUID）
    - GetStatus 聚合：SELECT state, count(*) FROM runs WHERE backfill_id=$1 GROUP BY state — 返回 map[string]int
    - GetStatus 还从 backfills 行返回 total_partitions 和 submitted_at
  </behavior>
  <action>
    1. 交叉检查 `migrations/20260508120000_phase3_runs_columns.sql`（计划 03-01）— 部分唯一索引附录必须包含：
       ```sql
       CREATE UNIQUE INDEX run_partition_inflight_unique
         ON runs (asset_name, partition_key)
         WHERE state IN ('queued','starting','running')
           AND partition_key IS NOT NULL;
       ```
       （计划 03-01 已逐字指定此谓词。如果没有，停止并报告 — 下面的应用程序代码依赖于此确切谓词。）
    2. 创建 `internal/backfill/submit.go`：
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
       //
       // Multi-row INSERT layout: 8 columns total (id, asset_name, state, trigger, queued_at,
       //   priority, partition_key, backfill_id). 3 of those are SQL literals (state='queued',
       //   trigger='backfill', queued_at=NOW()), so 5 placeholders per row. Use base := i*5.
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

           // 2. Multi-row INSERT into runs.
           //    8 columns, 3 literal: state='queued', trigger='backfill', queued_at=NOW().
           //    5 placeholders per row: id, asset_name, priority, partition_key, backfill_id.
           //    base := i*5 — DO NOT use i*8 (the 3 literal columns are NOT placeholders).
           values := make([]string, 0, len(spec.Keys))
           args := make([]interface{}, 0, len(spec.Keys)*5)
           for i, key := range spec.Keys {
               base := i * 5
               values = append(values, fmt.Sprintf("($%d, $%d, 'queued', 'backfill', NOW(), $%d, $%d, $%d)",
                   base+1, base+2, base+3, base+4, base+5))
               args = append(args, uuid.New(), assetName, priority, key, backfillID)
           }
           // ON CONFLICT predicate MUST exactly match the partial unique index from plan 03-01:
           //   WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL
           // PostgreSQL ON CONFLICT inference rejects mismatched predicates with:
           //   ERROR: there is no unique or exclusion constraint matching the ON CONFLICT specification
           query := `INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority, partition_key, backfill_id) VALUES ` +
               strings.Join(values, ", ") +
               ` ON CONFLICT (asset_name, partition_key) WHERE state IN ('queued','starting','running') AND partition_key IS NOT NULL DO NOTHING`
           result, err := tx.ExecContext(ctx, query, args...)
           if err != nil {
               return uuid.Nil, fmt.Errorf("backfill.Submit: bulk insert runs: %w", err)
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
    3. 创建 `internal/backfill/status.go`：
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
    4. 创建 `internal/backfill/submit_test.go`：
       - `TestBackfillSubmit` — 设置带有每日分区资产的注册表；调用 ParsePartitionSpec("2024-01-01:2024-01-07", DailyPartitions{}, 3650) → 7 个 key；调用 Submit(...)；断言 backfills 行存在且 total_partitions=7；SELECT count(*) FROM runs WHERE backfill_id=<id> AND priority='backfill' AND trigger='backfill' = 7；每个运行的 partition_key 是 7 个每日 key 之一（无重复）；事件写入器捕获带有有效负载 total_partitions=7 的 backfill.submitted 事件。
       - `TestBackfillSubmitInvalidPriority` — 使用 spec.Priority="bogus" 的 Submit 返回错误。
       - `TestBackfillSubmitIdempotentResubmit` — 使用相同规范两次调用 Submit；第二次调用插入 0 个运行（ON CONFLICT DO NOTHING，因为所有分区仍在进行中）；事件写入器有效负载 `enqueued=0, skipped_inflight=N`。
       - `TestBackfillStatus` — Submit 后调用 GetStatus；断言 StateCounts["queued"]=N 且 TotalPartitions 匹配。
       - `TestBackfillTimePartition`（验证映射）— 7 天每日分区回填：断言创建 7 个具有不同 partition_key 的运行，每个运行有自己的 event_log 条目（通过 SELECT count(*) FROM event_log WHERE resource_type='backfill' OR resource_id IN (run IDs) 验证——至少，每个运行在执行器处理它时应该有一个 `run.queued` 事件；对于此测试，只需验证运行行具有不同的 ID 和 partition_key，因为运行的事件日志条目将由 Phase 2 执行器在认领时创建）。
    5. 创建 `internal/backfill/independence_test.go`：
       - `TestCategoryPartitionIndependence`（验证映射）— 设置带有 `.Partitions(CategoryPartitions{Keys:["us","eu","apac"]})` 的资产。提交 "us,eu,apac" 的回填（3 个运行）。通过直接 SQL 将 `us` 运行转换为 'failed' 状态。验证其他两个运行（`eu`、`apac`）保持在 'queued' 状态 — D-16 每分区独立性。然后验证 GetStatus 返回 StateCounts={"queued":2, "failed":1}。
       测试不需要执行器实际运行分区 — 它测试数据库级别的独立性保证，没有共享状态将分区命运绑在一起。（完整的执行器 + 重试练习属于以后重用 worker 子命令的 e2e 测试。）
  </action>
  <acceptance_criteria>
    - 文件 `internal/backfill/submit.go` 存在
    - `grep -q 'func Submit' internal/backfill/submit.go`
    - `grep -q "INSERT INTO backfills" internal/backfill/submit.go`
    - `grep -q "INSERT INTO runs" internal/backfill/submit.go`
    - `grep -q "trigger.*backfill\\|'backfill'" internal/backfill/submit.go`
    - `grep -E 'base := i\\*5' internal/backfill/submit.go` 匹配（占位符构建器每行 5 个，不是 8）
    - `! grep -E 'base := i\\*8' internal/backfill/submit.go`（无遗留 `i*8` 算术 — 该 bug 会在运行时产生参数绑定错误）
    - `grep -q "AND partition_key IS NOT NULL DO NOTHING" internal/backfill/submit.go`（ON CONFLICT 谓词与计划 03-01 的部分唯一索引完全匹配）
    - `grep -q "ON CONFLICT (asset_name, partition_key)" internal/backfill/submit.go`
    - `grep -q "WHERE state IN ('queued','starting','running')" internal/backfill/submit.go`
    - `grep -q 'EventTypeBackfillSubmitted' internal/backfill/submit.go`
    - 文件 `internal/backfill/status.go` 存在
    - `grep -q 'func GetStatus' internal/backfill/status.go`
    - `grep -q 'GROUP BY state' internal/backfill/status.go`
    - `DATABASE_URL=... go test ./internal/backfill/... -run TestBackfillSubmit -count=1 -timeout 60s` 退出 0
    - `DATABASE_URL=... go test ./internal/backfill/... -run TestBackfillTimePartition -count=1 -timeout 60s` 退出 0
    - `DATABASE_URL=... go test ./internal/backfill/... -run TestCategoryPartitionIndependence -count=1 -timeout 60s` 退出 0
    - `DATABASE_URL=... go test ./internal/backfill/... -count=1 -timeout 120s` 退出 0（所有 backfill 测试）
  </acceptance_criteria>
  <verify>
    <automated>DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./internal/backfill/... -count=1 -timeout 120s</automated>
  </verify>
  <done>Submit 在一个 tx 中创建 1 个 backfills 行 + N 个 runs 行，priority='backfill' 且 backfill_id 已设置；多行 INSERT 使用每行 5 占位符算术（base := i*5）；ON CONFLICT 谓词与计划 03-01 的部分唯一索引完全匹配（包括 `AND partition_key IS NOT NULL`）；GetStatus 聚合状态计数；幂等重新提交；每分区独立性已验证。</done>
</task>

<task id="3.7.3" type="auto" tdd="true">
  <name>任务 3：在 cmd/platform/{main.go,backfill.go} 中连接 ./platform backfill 和 ./platform backfill status 子命令</name>
  <files>cmd/platform/backfill.go, cmd/platform/main.go</files>
  <read_first>
    - cmd/platform/main.go（当前 switch — 有来自计划 03-06 的 scheduler case + 来自 Phase 2 的 materialize case）
    - cmd/platform/scheduler.go（来自计划 03-06 的子命令引导模式）
    - cmd/platform/materialize.go（CLI 标志解析模式 + 资产注册表解析）
    - internal/backfill/submit.go + spec.go + status.go（刚刚创建 — 公共表面）
    - .planning/phases/03-scheduling-sensors-partitions/03-RESEARCH.md § 模式 9 — CLI 子命令连接
  </read_first>
  <behavior>
    - cmd/platform/main.go 有 `case "backfill":` 带子调度 — `./platform backfill status <id>` 调用 runBackfillStatus，否则调用 runBackfill
    - runBackfill 标志：`<asset>` 位置参数 + `--partitions=<spec>` 必需 + `--priority` 默认 "backfill"（针对 critical|normal|backfill 验证，错误时无效） + `--max-partitions` 默认 3650（int，> 0）
    - runBackfill 通过 asset.Default().Get(name) 解析资产；如果没有 Partitions 策略，错误显示"asset has no .Partitions(...)"
    - runBackfill 调用 ParsePartitionSpec 然后 Submit，成功时打印 `backfill_id: <UUID>` 到 stdout，打印"submitted N partitions"状态行，退出 0
    - runBackfillStatus 接受 `<backfill_id>` 作为位置参数，调用 GetStatus，打印纯文本中的聚合状态计数（例如，"Backfill abc-123 (asset users) — total: 7, queued: 5, succeeded: 2, failed: 0"）
    - 无效优先级返回"invalid --priority"错误并以非零退出 1
    - 超过 max-partitions 的规范返回"too many partitions"错误并退出 1
  </behavior>
  <action>
    1. 编辑 `cmd/platform/main.go`：
       在 `case "scheduler":` 块之后添加 `case "backfill":` 块：
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
    2. 创建 `cmd/platform/backfill.go`：
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
    - 文件 `cmd/platform/backfill.go` 存在
    - `grep -q 'func runBackfill' cmd/platform/backfill.go`
    - `grep -q 'func runBackfillStatus' cmd/platform/backfill.go`
    - `grep -q 'partitionsFlag := fs.String("partitions"' cmd/platform/backfill.go`
    - `grep -q 'priorityFlag := fs.String("priority"' cmd/platform/backfill.go`
    - `grep -q 'maxPartitionsFlag := fs.Int("max-partitions"' cmd/platform/backfill.go`
    - `grep -q 'backfill.Submit' cmd/platform/backfill.go`
    - `grep -q 'backfill.GetStatus' cmd/platform/backfill.go`
    - `grep -q 'backfill.ParsePartitionSpec' cmd/platform/backfill.go`
    - `grep -q 'backfill.ValidPriorities' cmd/platform/backfill.go`
    - `go build ./...` 退出 0
    - Smoke: `./platform backfill 2>&1 | grep -q 'usage: backfill'`（无参数打印用法）
    - Smoke: `./platform backfill foo --partitions=bad --priority=hacker 2>&1 | grep -q 'invalid --priority'`（优先级验证拒绝）
  </acceptance_criteria>
  <verify>
    <automated>cd /home/developer/.kanpon/code/go/data-governance && go build ./... && grep -c 'runBackfill\|backfill.Submit\|backfill.GetStatus' cmd/platform/backfill.go</automated>
  </verify>
  <done>./platform backfill 子命令已连接，带有 --partitions / --priority / --max-partitions 标志 + 资产注册表查找 + ParsePartitionSpec + Submit；./platform backfill status 子命令打印聚合计数；优先级验证在 CLI 解析时拒绝无效值；构建通过。</done>
</task>

<task id="3.7.4" type="auto" tdd="true">
  <name>任务 4：为 backfill priority 运行添加执行器级并发 token 标签获取（D-13 第 3 层）— 使用来自现有 *run.ClaimedRun 签名的 claimed.Priority；在 worker 引导中声明默认 backfill 容量</name>
  <files>internal/runtime/executor.go, internal/runtime/executor_test.go, cmd/platform/worker.go</files>
  <read_first>
    - internal/runtime/executor.go（完整文件 — 注意：签名已接受 `claimed *run.ClaimedRun` 按计划 03-03；在 runStep 约 193 行附近找到现有的 `pool.Acquire(... "global", 1)` 调用点）
    - internal/concurrency/pool.go（Acquire 签名 + Capacity 结构体）
    - internal/run/claim.go（在计划 03-03 中扩展的 ClaimedRun 结构体 — Priority 字段是 `string`）
    - internal/run/priority.go（来自计划 03-03 的 PriorityBackfill 常量）
    - cmd/platform/worker.go（当前引导函数 — 第 133-139 行从 cfg.Concurrency 构建 capacities 切片）
  </read_first>
  <behavior>
    - 当 Executor.Run 处理具有 Priority == "backfill" 的已认领运行时，除了现有的并发 token 获取（全局 + 每资源）外，还从 "backfill" 标签获取 1 个 token
    - 如果 "backfill" 标签容量耗尽，执行器释放任何已获取的 token，要么安排重试（如果策略允许），要么返回 ErrCapacity 给 worker 处理。运行在重试睡眠期间保持在 'starting' 状态。
    - 对于非 backfill 优先级，行为不变
    - 操作员通过现有连接器配置（cfg.Concurrency.Resources["backfill"]）配置 "backfill" 容量；如果操作员未明确设置，默认容量 5 在 worker 引导中声明
    - **无 Executor.Run 签名更改** — 计划 03-03 已安装 `Run(ctx, claimed *run.ClaimedRun)` 作为最终形式。此任务仅在现有函数体内添加对 `claimed.Priority` 的读取。
    - **对 worker.go ClaimNext/Run 调用站点无更改** — 已按计划 03-03 传递 `claimed`。
    - TestExecutorBackfillTagAcquisition 是无条件的 — 它使用内联最小桩连接器（`_test.go` 中的私有结构体），因此测试不依赖于重量级测试基础设施。验收标准是测试以退出代码 0 通过；无逃生舱口。
  </behavior>
  <action>
    1. 检查 `internal/runtime/executor.go`。Phase 2 基线 + 计划 03-03 签名是：
       ```go
       func (e *Executor) Run(ctx context.Context, claimed *run.ClaimedRun) error {
           runID := claimed.ID
           assetName := claimed.AssetName
           // ...
       }
       ```
       在 `runStep`（从 Run 调用）内，找到约 193 行的现有全局获取：
       ```go
       if err := e.deps.Pool.Acquire(ctx, runID, a.Name(), "global", 1); err != nil { ... }
       acquired = append(acquired, "global")
       ```
       将 `priority` 字符串从已认领运行引入 runStep — 更改 `runStep` 签名以接受 `priority string`：
       ```go
       func (e *Executor) runStep(ctx context.Context, runID uuid.UUID, a *asset.Asset, topoOrder int, partitionKey string, priority string) error {
       ```
       在 `Run` 中，现有调用 `e.runStep(ctx, runID, stepAsset, i, partitionKey)`（03-03 之后）变为 `e.runStep(ctx, runID, stepAsset, i, partitionKey, claimed.Priority)`。
       在 `runStep` 中全局 token 获取之后立即且在每资源获取循环之前，添加：
       ```go
       // D-13 layer 3: backfill-priority runs additionally acquire a "backfill" token.
       // Capacity defaults to 5 (worker.go bootstrap) — caps in-flight backfill runs.
       if priority == "backfill" {
           if err := e.deps.Pool.Acquire(ctx, runID, a.Name(), "backfill", 1); err != nil {
               releaseAcquired()
               if !retry.ShouldRetry(attempt, policy) {
                   e.appendEvent(ctx, runID, event.EventTypeRunStepFailed, event.RunStepFailedPayload{
                       AssetName: a.Name(), Attempt: attempt, Error: err.Error(),
                   })
                   return fmt.Errorf("executor: step %q failed to acquire backfill token (retries exhausted): %w", a.Name(), err)
               }
               e.scheduleRetry(ctx, runID, a, attempt, err, policy)
               continue
           }
           acquired = append(acquired, "backfill")
       }
       ```
       这镜像了现有的每资源获取模式。
    2. 编辑 `cmd/platform/worker.go` `bootstrap` 函数 — 更改 capacities 切片以声明默认 `backfill` 容量（如果 cfg.Concurrency.Resources 未提供）：
       替换现有块（第 133-139 行）：
       ```go
       capacities := []concurrency.Capacity{
           {Tag: "global", Limit: cfg.Concurrency.DefaultRunTokens},
       }
       for tag, limit := range cfg.Concurrency.Resources {
           capacities = append(capacities, concurrency.Capacity{Tag: tag, Limit: limit})
       }
       ```
       为：
       ```go
       capacities := []concurrency.Capacity{
           {Tag: "global", Limit: cfg.Concurrency.DefaultRunTokens},
       }
       // D-13 layer 3 default: 5 concurrent backfill runs unless operator overrides.
       backfillSet := false
       for tag, limit := range cfg.Concurrency.Resources {
           if tag == "backfill" {
               backfillSet = true
           }
           capacities = append(capacities, concurrency.Capacity{Tag: tag, Limit: limit})
       }
       if !backfillSet {
           capacities = append(capacities, concurrency.Capacity{Tag: "backfill", Limit: 5})
       }
       ```
    3. 添加 `internal/runtime/executor_test.go` 测试用例 `TestExecutorBackfillTagAcquisition`：
       - 使用内联最小桩连接器（对此测试文件私有）。这避免了对重量级测试基础设施的任何依赖：
         ```go
         // Inline stub — satisfies the connector.Connector interface with no-op Materialize.
         // Defined locally in this test to keep the test self-contained.
         type stubConnector struct{}

         func (stubConnector) Read(ctx context.Context, ref connector.AssetRef) ([]connector.Row, error) {
             return nil, nil
         }
         func (stubConnector) Write(ctx context.Context, ref connector.AssetRef, rows []connector.Row) (int64, error) {
             return 0, nil
         }
         // Add any other methods required by the connector.Connector interface — copy from internal/connector/connector.go.
         // The connector returns immediately so each Executor.Run completes quickly.
         ```
         （阅读 `internal/connector/connector.go` 确认确切的接口；桩必须满足所有方法。如果接口有 `Close()` 或类似方法，返回 nil。）
       - 设置测试：
         ```go
         func TestExecutorBackfillTagAcquisition(t *testing.T) {
             if os.Getenv("DATABASE_URL") == "" { t.Skip("requires DATABASE_URL") }
             db := openTestDB(t) // helper from claim_test.go pattern
             defer db.Close()
             store := &sqlStorage{db: db}

             // Build a Pool with capacities {global: 10, backfill: 1}.
             pool := concurrency.NewPool(store, []concurrency.Capacity{
                 {Tag: "global", Limit: 10},
                 {Tag: "backfill", Limit: 1},
             })

             // Register a no-op asset wired to the stub connector.
             reg := asset.NewRegistry()
             a, err := asset.New("test-backfill-tag").
                 Connector("stub").
                 Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
                     // Block briefly to keep the token held while the second goroutine tries.
                     time.Sleep(200 * time.Millisecond)
                     return asset.MaterializeResult{}, nil
                 }).
                 Build()
             require.NoError(t, err)
             require.NoError(t, reg.Register(a))

             connReg := connector.NewRegistry()
             require.NoError(t, connReg.Register("stub", stubConnector{}))

             events := event.NewWriter(store)
             exec := runtime.NewExecutor(runtime.Deps{
                 Store:        store,
                 Events:       events,
                 Registry:     reg,
                 ConnectorReg: connReg,
                 Pool:         pool,
                 WorkerID:     "test",
             })

             // Insert two backfill-priority runs in 'starting' state (post-claim) directly.
             insertStartingRun := func(priority string) uuid.UUID {
                 id := uuid.New()
                 _, err := db.Exec(
                     `INSERT INTO runs (id, asset_name, state, trigger, queued_at, claimed_by, claimed_at, last_heartbeat, priority)
                      VALUES ($1, 'test-backfill-tag', 'starting', 'backfill', NOW(), 'test', NOW(), NOW(), $2)`,
                     id, priority,
                 )
                 require.NoError(t, err)
                 return id
             }
             defer db.Exec(`DELETE FROM runs WHERE asset_name='test-backfill-tag'`)
             defer db.Exec(`DELETE FROM concurrency_tokens WHERE asset_name='test-backfill-tag'`)

             id1 := insertStartingRun("backfill")
             id2 := insertStartingRun("backfill")

             // Run id1 in a goroutine — should acquire the single backfill token and hold it ~200ms.
             errCh := make(chan error, 2)
             go func() {
                 errCh <- exec.Run(context.Background(), &run.ClaimedRun{
                     ID: id1, AssetName: "test-backfill-tag", Trigger: "backfill", Priority: "backfill",
                 })
             }()
             // Briefly wait so id1 has acquired the backfill token.
             time.Sleep(50 * time.Millisecond)

             // Run id2 with a short retry policy so the test does not hang for minutes — capacity error returns quickly.
             ctx2, cancel := context.WithTimeout(context.Background(), 1*time.Second)
             defer cancel()
             err2 := exec.Run(ctx2, &run.ClaimedRun{
                 ID: id2, AssetName: "test-backfill-tag", Trigger: "backfill", Priority: "backfill",
             })
             // id2 should fail to acquire the backfill token (capacity is 1 and id1 is holding it).
             // The error message includes "backfill token".
             assert.Error(t, err2, "second backfill run should fail to acquire backfill token while first holds it")
             assert.Contains(t, err2.Error(), "backfill token", "error should mention backfill token")

             // Wait for id1 to complete cleanly.
             require.NoError(t, <-errCh)
         }
         ```
       - 测试在 <2 秒内运行。backfill token 容量 1 + 200ms 保持的桩 Materialize 足以确定性地产生容量冲突。
       - 所有必需的测试依赖项（concurrency.NewPool、asset.NewRegistry、asset.New、connector.NewRegistry、event.NewWriter、runtime.NewExecutor）已从 Phase 2 存在；除了内联 stubConnector 外不需要重量级模拟。
    4. 针对本地 DB 运行测试：
       ```bash
       DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable \
         go test ./internal/runtime/... -run TestExecutorBackfillTagAcquisition -count=1 -timeout 60s
       ```
       预期：PASS。验收是无条件的 — 如果失败，修复它；不要标记为延期。
  </action>
  <acceptance_criteria>
    - `grep -q '"backfill"' internal/runtime/executor.go`（优先级字面量存在于新分支中）
    - `grep -E 'priority *== *"backfill"' internal/runtime/executor.go` 匹配（第 3 层获取分支）
    - `grep -E 'Pool\\.Acquire\\(.*"backfill"' internal/runtime/executor.go` 匹配（backfill 标签的实际 Acquire 调用）
    - `grep -q 'func (e \\*Executor) Run(ctx context.Context, claimed \\*run.ClaimedRun) error' internal/runtime/executor.go`（签名未从计划 03-03 更改；此计划中无进一步迁移）
    - `grep -q 'Tag: "backfill", Limit: 5' cmd/platform/worker.go`（引导默认容量存在）
    - `grep -q 'func TestExecutorBackfillTagAcquisition' internal/runtime/executor_test.go`
    - `grep -q 'type stubConnector struct' internal/runtime/executor_test.go`（内联最小桩存在 — 不依赖重量级基础设施）
    - `go build ./...` 退出 0
    - `DATABASE_URL=... go test ./internal/runtime/... -run TestExecutorBackfillTagAcquisition -count=1 -timeout 60s` 退出 0（无条件的 — 无逃生舱口）
    - `DATABASE_URL=... go test ./internal/run/... -run TestClaimAtomicity50Goroutines -count=1 -timeout 60s` 仍然退出 0（Phase 2 回归）
  </acceptance_criteria>
  <verify>
    <automated>cd /home/developer/.kanpon/code/go/data-governance && go build ./... && DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./internal/runtime/... -run TestExecutorBackfillTagAcquisition -count=1 -timeout 60s && DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/data_governance?sslmode=disable go test ./internal/run/... -run TestClaimAtomicity50Goroutines -count=1 -timeout 60s</automated>
  </verify>
  <done>Executor 通过现有 *run.ClaimedRun 参数为 backfill-priority 运行获取 "backfill" 标签（自计划 03-03 以来无签名更改）；worker 引导声明默认 backfill 容量 5；ErrCapacity 在耗尽时返回；D-13 第 3 层功能；TestExecutorBackfillTagAcquisition 使用内联最小桩连接器无条件通过 — 无逃生舱口；Phase 2 回归测试仍然通过。</done>
</task>

</tasks>

<threat_model>
## 信任边界

| 边界 | 描述 |
|----------|-------------|
| 操作员 CLI 输入 → ParsePartitionSpec | 不受信任的规范字符串在此跨越；验证阻止注入 / 行数爆炸 |
| 操作员 CLI 标志 --priority → Submit | 优先级验证在解析时拒绝未知值 + Submit + DB CHECK |
| Submit → runs/backfills 表 | 参数化查询；用户输入不进行字符串插值到 SQL |

## STRIDE 威胁注册

| 威胁 ID | 类别 | 组件 | 处置 | 缓解计划 |
|-----------|----------|-----------|-------------|-----------------|
| T-03-07-01 | 拒绝服务 | 回填行数爆炸（陷阱 6） | 缓解 | --max-partitions=3650 默认在 CLI 解析 + ParsePartitionSpec ErrTooManyPartitions 在任何 INSERT 之前。TestMaxPartitionsGuard 验证。 |
| T-03-07-02 | 篡改 | 通过 --partitions 字符串注入 partition_key | 缓解 | 所有 key 作为参数化值传递（`$N` 占位符）；从不字符串插值。ValidateCategoryKey 拒绝 '/'（陷阱 4）。Daily/Weekly/Monthly key 通过 time.Parse 验证 — 不符合的字符串被拒绝。 |
| T-03-07-03 | 权限提升 | 操作员提交 --priority=critical 的回填以跳过队列中的普通运行 | 缓解 | CLI 在解析时拒绝非 {critical,normal,backfill} 值。CLI 不强制谁可以设置 'critical' — 这在 API 授权层（Phase 4+）。对于 Phase 3 v1，任何有 shell 访问权限的人都可以提交 critical；这是可接受的，因为 shell 访问已经意味着操作员级信任。记录在 CLI 帮助中："提交需要操作员级 shell 访问；v1 中无 CLI 内身份验证"。 |
| T-03-07-04 | 拒绝服务 | 操作员提交跨越多年的回填超出 total_partitions Int 字段 | 缓解 | Int（32 位有符号）上限为 21 亿，远远超过任何实际回填。max-partitions 守卫首先在 3650 触发。 |
| T-03-07-05 | 信息泄露 | partition_spec 原样存储在 backfills.partition_spec 中 — 可能泄露操作员意图 | 接受 | 规范是操作员提供的数据，存储用于审计。不是用户 PII。event_log RLS 防止篡改。 |
| T-03-07-06 | 欺骗 | Submit 发出带有操作员身份的 backfill.submitted | 接受（延期） | Phase 3 v1 在 CLI 无身份验证；事件中的 ActorID 为 nil。Phase 4+ 连接身份验证。 |
| T-03-07-07 | 拒绝服务 | 并发回填提交淹没 runs 表 | 缓解 | (1) max-partitions 将单次提交限制在 3650。(2) 计划 03-03 优先级认领将回填行推迟到普通之后。(3) 任务 4 backfill 并发标签将在飞行中回填限制为默认 5。(4) Submit 事务范围插入是短的（多行 VALUES）；无独占表锁。 |
| T-03-07-08 | 篡改 | Submit ON CONFLICT DO NOTHING 静默丢弃某些 key | 缓解 | Submit 的事件有效负载包括 `enqueued` 和 `skipped_inflight` 计数，因此操作员看到差异。CLI 打印"submitted N partitions"反映原始规范长度；差异可通过 `./platform backfill status <id>` 计数与 total_partitions 可见。 |
| T-03-07-09 | 篡改 | event_log Phase 3 回填事件被篡改 | 接受 | Phase 1 D-09 RLS 已阻止 UPDATE/DELETE on event_log [已验证]。 |
| T-03-07-10 | 篡改 | Submit 与计划 03-01 中部分唯一索引之间的 ON CONFLICT 谓词漂移 | 缓解 | 此计划的 ON CONFLICT WHERE 子句与部分索引谓词逐字匹配。验收标准明确 grep `AND partition_key IS NOT NULL DO NOTHING`。如果谓词漂移（任何一方更新而另一方不更新），PostgreSQL 快速失败并显示"no unique or exclusion constraint matching"。集成测试 TestBackfillSubmit 练习此路径 — 谓词不匹配表现为测试失败。 |
| T-03-07-11 | 篡改 | 多行 INSERT 占位符算术使用错误的步幅（i*8 vs i*5） | 缓解 | 验收标准 `grep -E 'base := i\\*5'` 匹配；`grep -E 'base := i\\*8'` 必须不匹配。TestTestBackfillSubmit 提交 ≥2 行，因此 off-by-N 步幅 bug 立即表现为参数绑定错误。 |
</threat_model>

<verification>
- `go build ./...` 通过。
- `DATABASE_URL=... go test ./internal/backfill/... -count=1 -timeout 120s` 通过。
- `DATABASE_URL=... go test ./internal/runtime/... -count=1 -timeout 120s` 通过（带新的 backfill 标签测试）。
- `DATABASE_URL=... go test ./internal/run/... -run TestClaimAtomicity50Goroutines -count=1 -timeout 60s` 仍然通过（Phase 2 回归 — 最终阶段回归检查）。
- Smoke: `./platform backfill foo --partitions=bad --priority=hacker` 退出并显示 `invalid --priority` 错误。
- TestBackfillTimePartition 验证 ORCH-07。
- TestCategoryPartitionIndependence 验证 ORCH-08。
- TestExecutorBackfillTagAcquisition 验证 D-13 第 3 层无条件。
</verification>

<success_criteria>
- internal/backfill 包完整：spec.go（ParsePartitionSpec + max-partitions 守卫）、submit.go（Submit + 批量入队 + ON CONFLICT 幂等 + 正确的 5 占位符构建器 + 匹配的 ON CONFLICT 谓词）、status.go（GetStatus + 状态聚合）、independence_test.go（TestCategoryPartitionIndependence）。
- ./platform backfill 子命令连接了 --partitions / --priority / --max-partitions 标志。
- ./platform backfill status 子命令已连接。
- TestParsePartitionSpec、TestMaxPartitionsGuard、TestBackfillTimePartition、TestCategoryPartitionIndependence 全部通过（验证映射覆盖完整）。
- Executor 读取 claimed.Priority 并为 backfill-priority 运行获取 "backfill" 并发标签，而不更改 Run 签名（计划 03-03 的 `Run(ctx, *run.ClaimedRun)` 保持最终形式）。
- Worker 引导声明默认 backfill 容量为 5，除非 cfg.Concurrency.Resources["backfill"] 覆盖（D-13 第 3 层默认值）。
- TestExecutorBackfillTagAcquisition 使用内联最小桩连接器无条件通过（无逃生舱口，无"如果模拟证明重量级则延期"）。
- Phase 2 50 goroutine 原子性测试仍然通过（所有 Phase 3 更改后的最终回归检查）。
- 所有 4 个 ORCH 需求（ORCH-05/06/07/08）明确由 Phase 3 测试覆盖。
</success_criteria>

<output>
完成后，创建 `.planning/phases/03-scheduling-sensors-partitions/03-07-SUMMARY.md` 记录：
- 最终 backfill 包表面（spec、submit、status）。
- 带有默认值的 CLI 标志列表。
- 确认的多行 INSERT 占位符算术（`base := i*5`，不是 `i*8`）。
- ON CONFLICT 谓词逐字引用并确认与计划 03-01 部分唯一索引匹配。
- D-13 第 3 层实现：执行器读取 `claimed.Priority`（自 03-03 以来无签名更改）并获取 "backfill" 标签；引导默认容量 5。
- 决策覆盖映射：D-13 第 1+2+3 层、D-14（CLI）、D-15（批量入队 + 幂等重新提交）、D-16（每分区独立性 — TestCategoryPartitionIndependence）。
- 确认所有四个 ORCH-05/06/07/08 验收标准明确由 Phase 3 测试覆盖。
- 最终回归检查：TestClaimAtomicity50Goroutines 在所有 Phase 3 更改后仍然通过。
- TestExecutorBackfillTagAcquisition 通过 — D-13 第 3 层已确认。
</output>