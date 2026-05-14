---
phase: 02-execution-engine
reviewed: 2026-05-08T00:00:00Z
depth: standard
files_reviewed: 41
files_reviewed_list:
  - cmd/platform/factories.go
  - cmd/platform/main.go
  - cmd/platform/materialize.go
  - cmd/platform/worker.go
  - internal/asset/asset.go
  - internal/asset/builder.go
  - internal/asset/io.go
  - internal/asset/registry.go
  - internal/asset/retry.go
  - internal/concurrency/pool.go
  - internal/connector/config/config.go
  - internal/connector/config/resolver.go
  - internal/connector/firstparty/bigquery/bigquery.go
  - internal/connector/firstparty/bigquery/factory.go
  - internal/connector/firstparty/conformance/conformance.go
  - internal/connector/firstparty/gcs/factory.go
  - internal/connector/firstparty/gcs/gcs.go
  - internal/connector/firstparty/hdfs/factory.go
  - internal/connector/firstparty/hdfs/hdfs.go
  - internal/connector/firstparty/mysql/factory.go
  - internal/connector/firstparty/mysql/mysql.go
  - internal/connector/firstparty/postgres/factory.go
  - internal/connector/firstparty/postgres/postgres.go
  - internal/connector/firstparty/s3/factory.go
  - internal/connector/firstparty/s3/s3.go
  - internal/connector/firstparty/snowflake/factory.go
  - internal/connector/firstparty/snowflake/snowflake.go
  - internal/connector/registry.go
  - internal/dag/dag.go
  - internal/event/types.go
  - internal/retry/policy.go
  - internal/run/claim.go
  - internal/run/lifecycle.go
  - internal/run/reaper.go
  - internal/run/state.go
  - internal/runtime/executor.go
  - internal/storage/ent/schema/concurrency_token.go
  - internal/storage/ent/schema/run.go
  - internal/storage/ent/schema/run_step.go
  - migrations/20260507120000_phase2_run_tables.sql
  - migrations/20260507121500_phase2_concurrency_tokens.sql
findings:
  critical: 3
  warning: 6
  info: 4
  total: 13
status: issues_found
---

# Phase 02 代码审查报告

**审查时间：** 2026-05-08T00:00:00Z
**深度：** standard
**审查的文件：** 41
**状态：** issues_found

## 总结

Phase 2 引入了执行引擎：资产 registry、基于 DAG 的拓扑执行、并发 token 池、每个资产重试策略、运行生命周期 FSM、用于崩溃恢复的 heartbeat/reaper，以及七个第一方连接器（postgres、mysql、snowflake、s3、gcs、hdfs、bigquery）。整体架构合理且符合 Go 习惯。registry 和连接器的锁定是一致的，并发 token 获取的建议锁方法是适当的。

三个关键问题需要在合并前关注：

1. executor 的 `transition` 辅助方法中存在读后取消竞争条件，静默忽略所有 DB 写入错误。
2. `materialize` 子命令在循环内 sleep 前轮询，意味着第一个状态检查可能消耗它刚插入的运行 ID——一个结合了未检查上下文泄漏的逻辑排序错误。
3. BigQuery `splitIdentifier` 对 2 部分标识符返回空项目字符串，然后将其用于带反引号的 SQL 查询，产生静默的无效查询。

六个警告涵盖未处理的错误返回、token 释放排序问题（在重试期间可能饥饿容量），以及测试中缺少 goroutine 生命周期保证。

---

## 关键问题

### CR-01: `executor.transition` 静默吞掉所有 DB 错误

**文件：** `internal/runtime/executor.go:318-319`

**问题：** `transition` 被调用，其错误返回值在三个调用点（第 122、133、137 行）用 `_ =` 丢弃。当 `UPDATE runs SET state` 查询失败时——网络错误、约束冲突，或运行已被 reaper 移动——executor 继续执行，好像转换成功了一样。对于失败路径（第 122 行），这意味着运行行停留在 `running`，而调用者返回错误，使行困在非终止状态，reaper 最终会将其重新排队，就像 worker 崩溃了一样。

```go
// executor.go 第 122-123 行（失败路径）
_ = e.transition(ctx, runID, run.StateRunning, run.StateFailed)
e.appendEvent(ctx, runID, event.EventTypeRunFailed, ...)
```

第 133 行的成功路径有相同问题。如果 DB 更新失败，行静默停留在 `running` 而不是移动到 `succeeded`。

**修复：** 在所有调用点传播 `transition` 的错误。对于终止转换（succeeded/failed），当前运行无论哪种方式都无法继续，所以包装并返回：

```go
// 在步骤失败分支（约第 122 行）：
if terr := e.transition(ctx, runID, run.StateRunning, run.StateFailed); terr != nil {
    slog.Error("executor.transition_failed", "run_id", runID, "to", "failed", "error", terr)
}

// 在成功路径（约第 133 行）：
if terr := e.transition(ctx, runID, run.StateRunning, run.StateSucceeded); terr != nil {
    slog.Error("executor.transition_failed", "run_id", runID, "to", "succeeded", "error", terr)
}
```

至少必须记录错误；理想情况下返回运行终止转换错误，以便 worker 循环决定是否重试声明。

---

### CR-02: `materialize` 子命令插入运行后立即轮询 — 排序错误和 ctx 超时不匹配

**文件：** `cmd/platform/materialize.go:34, 96-124`

**问题 — 在插入前创建超时上下文：** 外层上下文在第 34 行用 `timeout + 30s` 创建，但单独的 `waitForRun` 截止日期在第 97 行从 `time.Now()` 计算。在第 34 行和第 97 行之间，`bootstrap` + DB 插入 + 事件写入可能消耗几秒钟。然后 `timeout` 变量直接传递给 `waitForRun`。在实践中这不是一个错误，但外层上下文的 30 秒填充不是明确合理的：如果 `timeout` = 30 分钟而 bootstrap 消耗 10 秒，轮询循环运行 30 分钟，然后达到其截止日期，但外层 `ctx` 在另外 29 分 50 秒后才过期。因此额外的 30 秒保护是误导性的。

**问题 — 第一次轮询读取过时或零状态：** `waitForRun` 在第一次循环迭代（第 103 行）的 `select` sleep **之前**调用 `QueryRowContext`。运行刚作为 `'queued'` 插入。Worker 异步获取它；在轮询窗口期间状态合法是 `queued`。第 107-115 行的 switch 语句没有 `case "queued":` 或 `case "starting":` 或 `case "running":` 分支——这些落入 `time.Now().After(deadline)` 检查。这是故意的，但有一个微妙错误：`deadline` 检查在第 116 行在查询之后使用 `time.Now().After(deadline)` **之后**，意味着第一次迭代即使运行未完成也不会 sleep。正确的结构应该是：在轮询**之前**检查截止日期（处理零预算超时），然后轮询，然后 sleep。按原样，`timeout = 0` 时，在返回前发出一 DB 查询。

```
for {
    /* poll */  ← 在第一次迭代时触发
    /* switch */
    if time.Now().After(deadline) { return timeout err }
    select { case <-ticker.C: /* sleep 500ms */ }
}
```

**修复：** 将截止日期检查移到查询之前，或重构使第一次迭代也等待 500ms（与声明的"每 500ms 轮询"行为一致）。还添加对非终止状态的明确处理以避免误导性日志：

```go
for {
    select {
    case <-ticker.C:
    case <-ctx.Done():
        return ctx.Err()
    }
    if time.Now().After(deadline) {
        return fmt.Errorf("materialize: timeout (last state=%s)", lastState)
    }
    if err := deps.store.DB().QueryRowContext(ctx, stateSQL, runID).Scan(&state, &errMsg); err != nil {
        return fmt.Errorf("materialize: poll state: %w", err)
    }
    // ... switch ...
}
```

---

### CR-03: BigQuery `splitIdentifier` 对 2 部分标识符返回空项目，然后用于查询

**文件：** `internal/connector/firstparty/bigquery/bigquery.go:107-111, 198-212`

**问题：** `splitIdentifier` 在标识符为 `"dataset.table"`（2 部分，第 207 行）时返回 `("", dataset, table, nil)`。第 111 行的 `Read` 方法将返回的 `project` 值用于格式化字符串：

```go
q := fmt.Sprintf("SELECT * FROM `%s`.`%s`.`%s`", project, dataset, table)
```

当 `project` 为空时，这产生 `` SELECT * FROM ``.`dataset`.`table` `` — 一个无效的 BigQuery SQL 查询。BigQuery 客户端将返回错误，但错误消息将是不透明的（关于无效 SQL 的 BigQuery API 错误），而不是清晰的"需要项目"消息。

相比之下，`Write` 和 `Schema` 完全丢弃返回的 `project`（第 159 行、第 79 行），只使用 `dataset` 和 `table`，对这些路径是正确的。不一致性意味着 `Read` 对 2 部分标识符失效，而 `Write` 和 `Schema` 工作。

**修复：** 要么强制 `Read` 需要 3 部分标识符（返回清晰错误），要么在解析的项目为空时回退到连接器存储的 `b.project`：

```go
// 在 Read 中，splitIdentifier 之后：
if project == "" {
    project = b.project
}
q := fmt.Sprintf("SELECT * FROM `%s`.`%s`.`%s`", project, dataset, table)
```

对 `Write` 和 `Schema` 应用相同的修复以保持一致性，即使它们当前忽略项目并依赖 BigQuery 客户端的默认项目上下文。

---

## 警告

### WR-01: GCS 客户端在 `Factory` 中未被 `NewClientFromOptions` 调用者关闭

**文件：** `internal/connector/firstparty/gcs/factory.go:36-40`

**问题：** `Factory` 调用 `NewClientFromOptions`，带有它取消的 `context.WithTimeout`（`defer cancel()`）。 resulting `*gcstorage.Client` 然后传递给 `New(client, bucket, format)`。GCS 客户端延迟建立连接，但如果 factory 的 10 秒超时在第一次真实操作之前触发，则上下文取消与第一次操作竞争。更具体地说：如果 `New(client, ...)` 返回 `*GCS` 结构体，然后 `cancel()` 在第一次 `Ping` 或 `Read` 之前触发，底层客户端可能已取消其上下文，取决于 GCS SDK 内部如何使用它。

这与 BigQuery factory 使用的模式相同。对于使用 GCS/BQ 库的连接器，构造上下文仅用于 `NewClient` 调用本身，不保留连接——这对 GCS SDK 是正确的。然而，值得注意的是两个 factory 都将构造超时上下文传递给 `NewClient`，SDK 仅用于初始拨号。问题是 GCS 的 minor，因为 SDK 不保留上下文。评级：警告，因为模式一致但值得记录。

**修复：** 在 `Factory` 中添加代码注释，明确记录构造上下文仅用于 `NewClient`（初始拨号），不保留，随后操作使用每请求上下文：

```go
// ctx is used only for gcstorage.NewClient (initial dial); the client itself
// does not retain ctx. Subsequent operations use the per-request context.
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
```

---

### WR-02: 重试时并发 token 释放不会原子释放所有获取的标签 — 容量饥饿风险

**文件：** `internal/runtime/executor.go:176-231`

**问题：** 在 `runStep` 中，当资源级 token 获取失败后（全局 token 已获取），调用 `releaseAcquired()` 删除所有获取的标签后再重试。这是正确的。但是 `concurrency.Pool` 中的 `Release` 调用使用简单的 `DELETE WHERE run_id = $1 AND resource_tag = $2`，没有任何锁定——它不在 `Acquire` 使用的同一建议锁事务中。序列：

1. 获取 `global` → 插入行
2. 获取 `resource_a` → 失败（容量耗尽）
3. `releaseAcquired()` → 删除 `global` 行
4. 重试：再次获取 `global`（插入新行）

在步骤 3 和 4 之间，另一个并发运行可以获取释放的 `global` 槽。这不是正确性错误（行为是正确的），但意味着重试运行看到额外的竞争，如果在重试 sleep 时持有 token 就不存在。当前设计使重试运行在每次尝试时重新竞争所有 token，这在高负载下可能导致饥饿。

**修复（设计级）：** 在 `runStep` 的代码注释中明确记录"重试时重新竞争"行为，以便未来工程师不会假设 token 在重试之间保留。如果在 Phase 3 中饥饿变得可观察，考虑 token 预留模式（预检查而不插入）：

```go
// NOTE: All tokens are released before the retry sleep so other runs can proceed.
// The retrying attempt must re-acquire tokens on the next loop iteration,
// competing fairly with new runs. This means high contention can starve retrying
// runs; revisit in Phase 3 if starvation is observed in production.
releaseAcquired()
```

---

### WR-03: `executor.Run` 在每步循环中忽略 `Registry.Get` 错误

**文件：** `internal/runtime/executor.go:119`

```go
stepAsset, _ := e.deps.Registry.Get(name)
```

**问题：** `e.deps.Registry.Get(name)` 可以在资产在 `buildSubgraph` 构建 DAG 和每步循环执行之间的某个时候从 registry 中移除时返回错误（和 `nil` *asset.Asset）。如果 `Get` 失败并返回 `nil`，`runStep` 收到 `nil` *asset.Asset，当调用 `a.RetryPolicy()`（第 170 行）、`a.Name()`（第 186 行）或 `a.Resources()`（第 201 行）时会 panic。这个 panic 被 `safeMaterialize` 恢复，但步骤永远不会到达该函数——panic 发生在 `runStep` 本身内部，在用户函数被调用之前，不由 `safeMaterialize` 包装。

**修复：** 检查错误并返回：

```go
stepAsset, err := e.deps.Registry.Get(name)
if err != nil {
    return fmt.Errorf("executor: step %q not found during execution: %w", name, err)
}
```

---

### WR-04: Reaper `SweepOnce` 手动关闭 `rows` 然后调用 `rows.Err()` — double-close 风险

**文件：** `internal/run/reaper.go:101, 107-109`

**问题：** 在扫描循环内部，如果 `rows.Scan` 失败，代码在第 101 行显式调用 `_ = rows.Close()` 并提前返回。循环后，第 107 行无条件调用 `_ = rows.Close()`，然后是第 108 行的 `rows.Err()`。在 Go 的 `database/sql` 包中，对 `*sql.Rows` 调用两次 `Close()` 明确记录为是安全的，所以这不是崩溃风险。但是错误路径中手动 `rows.Close()`（第 101 行）与循环后无条件的 `_ = rows.Close()`（第 107 行）的组合是令人困惑的，模式与代码库中其他地方的惯用 `defer rows.Close()` 不同（postgres、mysql、snowflake 连接器都使用 `defer rows.Close()`）。

更重要的是：当 `rows.Scan` 在第 100 行失败时，代码立即返回而不调用 `rows.Err()`。`Scan` 的错误被返回，但之前在 `rows.Err()` 中累积的任何行迭代错误被丢弃。这是一个 minor 正确性问题。

**修复：** 在 `QueryContext` 调用后立即使用 `defer rows.Close()`（惯用模式），然后在循环后检查 `rows.Err()` 一次：

```go
rows, err := r.Store.DB().QueryContext(ctx, selectSQL, cutoff)
if err != nil {
    return 0, fmt.Errorf("reaper: select stale: %w", err)
}
defer rows.Close()

var candidates []staleRunRow
for rows.Next() {
    var row staleRunRow
    var stateStr string
    if err := rows.Scan(&row.ID, &stateStr, &row.AssetName); err != nil {
        return 0, fmt.Errorf("reaper: scan stale: %w", err)
    }
    row.State = State(stateStr)
    candidates = append(candidates, row)
}
if err := rows.Err(); err != nil {
    return 0, fmt.Errorf("reaper: iterate stale: %w", err)
}
```

---

### WR-05: `materialize.go` 中的 `waitForRun` 不处理 `QueryRowContext` 的 `context.Canceled`

**文件：** `cmd/platform/materialize.go:103-104`

**问题：** 当 `ctx` 被取消（同步等待期间的 SIGINT）时，`QueryRowContext` 将返回 `ctx.Err()`，包装在 `*sql.ErrConnDone` 或 `context.Canceled` 中。当前代码在第 104 行将其包装为 `"materialize: poll state: %w"` 并返回。第 119 行的 `select` 也捕获 `ctx.Done()` 并返回 `ctx.Err()`，但 DB 查询在 `select` 之前运行，所以查询期间的取消上下文会产生包装错误而不是干净的 `context.Canceled`。这在正常 SIGINT 关闭时使退出消息不必要的嘈杂。这是一个 minor UX 问题而不是正确性错误。

**修复：** 在查询错误后，检查 `ctx.Err()` 然后再包装：

```go
if err := deps.store.DB().QueryRowContext(ctx, stateSQL, runID).Scan(&state, &errMsg); err != nil {
    if ctx.Err() != nil {
        return ctx.Err()
    }
    return fmt.Errorf("materialize: poll state: %w", err)
}
```

---

### WR-06: `mysql.quoteIdentifier` 路径遍历检查触发合法名称

**文件：** `internal/connector/firstparty/mysql/mysql.go:280-282`

**问题：** 路径遍历防护 `strings.Contains(id, "..")` 在标识符包含两个连续点时触发。名为 `db..schema` 的 MySQL 数据库确实是无效的，但检查也会匹配像 `"my..table"` 这样的有效名称在一个连接的 `"db.my..table"` 标识符中——尽管话虽如此，MySQL 表名中的双点在法律上是不允许的。真正风险是误报：命名列为 `"e.g.."`（尾随点）或 schema `"schema.."` 的用户会得到关于路径遍历的不透明错误，而不是清晰的"非法标识符"消息。

Postgres 连接器没有此检查（对 SQL 正确），S3/GCS/HDFS 连接器逐段检查 `".."`（也是正确的）。只有 MySQL 和 Snowflake 有 `strings.Contains(id, "..")` 字符串级检查。这是一个保守措施，可能产生令人困惑的错误消息。

**修复：** MySQL 标识符引用已经拒绝反引号。`".."` 检查借用了文件系统连接器的路径遍历防御，不适用于 SQL 标识符。从 `mysql.quoteIdentifier` 和 `snowflake.quoteIdentifier` 中删除它，或替换为更清晰的"不允许连续点"消息：

```go
// In quoteIdentifier:
// Remove: if strings.Contains(id, "..") { ... }
// The backtick rejection is sufficient for SQL injection defense.
```

---

## 信息

### IN-01: config loader 中的 `envVarPattern` 仅匹配大写变量名

**文件：** `internal/connector/config/config.go:27`

```go
envVarPattern = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)
```

**问题：** 正则表达式仅匹配 `${UPPER_CASE}` 占位符。POSIX 允许小写 env var 名称（`$HOME`、`$user` 等）。虽然项目约定是大写 env vars（由正则表达式注释"有效 env var 名称"强制），但这将静默保留小写 `${lower_case_var}` 占位符不替换，而不是报告它们为缺失。写 `${dsn}` 而不是 `${DSN}` 的用户会得到令人困惑的错误：字面字符串 `${dsn}` 将传递给 YAML 解码器，然后传递给连接器 factory，这将失败并带有连接器特定的错误。

**修复：** 要么扩展正则表达式以匹配小写（POSIX 约定），要么添加后解决检查，警告当任何 `${...}` 模式保留在解析字符串中时（表示未匹配的占位符）：

```go
// After resolveEnv, scan for unresolved placeholders (catches lowercase mismatches):
if remaining := regexp.MustCompile(`\$\{[^}]+\}`).FindString(resolved); remaining != "" {
    return nil, fmt.Errorf("config: unresolved placeholder %q (env var names must be UPPER_CASE)", remaining)
}
```

---

### IN-02: `asset.ResetForTest` 被导出并在无 sync 保护的情况下改变包级变量

**文件：** `internal/asset/registry.go:85-89`

```go
func ResetForTest() { defaultRegistry = NewDefinitionRegistry() }
```

**问题：** 导出的 `ResetForTest` 函数替换 `defaultRegistry`（包级 `var`），而不持有任何锁。如果与调用 `Register` 或 `Get` 读取 `defaultRegistry` 的调用并发调用，这是数据竞争。第 88 行的注释说"NOT safe for concurrent test use — use only from TestMain or serially,"，这是正确的文档。但是 `test/integration/e2e_postgres_test.go:273-274` 中的集成测试在单独运行测试函数内调用 `asset.ResetForTest()`（`TestE2E_PostgresMaterialize` 等）。这些测试当前是顺序的，所以在实践中没有竞争。风险是未来的 `t.Parallel()` 调用会静默引入竞争。

**修复：** 添加 `t.Setenv` 风格模式或使用 `sync/atomic.Pointer` 用于 `defaultRegistry`，或添加包初始化互斥锁。至少在函数注释中添加测试时间 `go vet`/`-race` 注意：

```go
// ResetForTest is safe only when called serially (e.g. from TestMain or t.Cleanup without
// t.Parallel). It is intentionally not protected by a mutex — tests using t.Parallel()
// MUST NOT call this function.
func ResetForTest() { defaultRegistry = NewDefinitionRegistry() }
```

---

### IN-03: `run_steps` 表没有到 `runs` 的外键

**文件：** `migrations/20260507120000_phase2_run_tables.sql:27-46`

**问题：** `run_steps.run_id` 列语义引用 `runs.id`，但在迁移 DDL 中没有 `FOREIGN KEY` 约束。可以累积孤立步骤行（引用已删除或不存在运行的），没有 DB 级执行。如果运行被删除（手动或通过未来清理 job），其 `run_steps` 行被静默留下。类似地，写入 `run_steps` 并带有不存在 `run_id` 的错误会在不报告的情况下成功。

这是数据完整性差距。修复：在迁移中添加外键：

```sql
ALTER TABLE run_steps
  ADD CONSTRAINT run_steps_run_id_fkey
  FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE;
```

并在 ent schema 中声明边：

```go
func (RunStep) Edges() []ent.Edge {
    return []ent.Edge{
        edge.From("run", Run.Type).Ref("steps").Field("run_id").Required().Immutable(),
    }
}
```

---

### IN-04: `conformance.go` `CtxCancel` 测试断言 `err != nil` 即使没有取消也总是通过

**文件：** `internal/connector/firstparty/conformance/conformance.go:79`

```go
require.Truef(t, errors.Is(err, context.Canceled) || err != nil,
    "expected ctx.Canceled or any error, got %v", err)
```

**问题：** 断言 `errors.Is(err, context.Canceled) || err != nil` 等同于 just `err != nil`，因为 `errors.Is(err, context.Canceled)` 意味着 `err != nil`。因此，只要连接器在上下文取消后返回任何错误，测试就通过——包括"对象未找到"或"bucket 空"等无关错误。完全忽略上下文取消并返回业务错误的连接器将通过此测试，掩盖真正的错误。

**修复：** 加强断言。连接器应在上下文取消后完成操作时返回 `context.Canceled`（或其包装）：

```go
// The connector must respect context cancellation.
// Accept context.Canceled, context.DeadlineExceeded, or a wrapping of either.
cancelErr := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
require.Truef(t, cancelErr,
    "connector should return context.Canceled/DeadlineExceeded on ctx cancel, got: %v", err)
```

---

_Reviewed: 2026-05-08T00:00:00Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_