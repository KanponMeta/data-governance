---
phase: 02-execution-engine
fixed_at: 2026-05-08T00:00:00Z
review_path: .planning/phases/02-execution-engine/02-REVIEW.md
iteration: 1
findings_in_scope: 9
fixed: 7
skipped: 0
already_fixed: 1
addressed_in_other: 1
status: all_fixed
---

# Phase 02: 代码审查修复报告

**修复时间:** 2026-05-08T00:00:00Z
**来源审查:** .planning/phases/02-execution-engine/02-REVIEW.md
**迭代:** 1

**概要:**
- 范围内的发现: 9 (CR-01, CR-02, CR-03, WR-01 到 WR-06)
- 已修复: 7 (CR-01, CR-02, WR-01, WR-02, WR-03, WR-04, WR-06)
- 此会话前已修复: 1 (CR-03)
- 在其他修复中处理: 1 (WR-05 在 CR-02 修复中解决)
- 跳过: 0

## 已修复的问题

### CR-01: executor.transition 静默吞掉所有 DB 错误

**修改的文件:** `internal/runtime/executor.go`
**提交:** 4f25104
**应用的修复:** 将两个 `_ = e.transition(...)` 调用 (失败路径第 122 行, 成功路径第 133 行) 替换为显式错误检查，在 DB 转换失败时通过 `slog.Error` 记录。运行仍然继续返回 step 错误 (失败路径) 或 nil (成功路径) — 与审查者意图一致 — 但转换失败现在在结构化日志中可见，而不是被静默丢弃。

---

### CR-02: materialize 子命令在 sleep 之前轮询 — 排序错误和 ctx 超时不匹配

**修改的文件:** `cmd/platform/materialize.go`
**提交:** f226c5b
**应用的修复:** 重构 `waitForRun` 循环为 sleep-first (在查询之前 select ticker/ctx.Done)，然后在发出 DB 查询之前检查 deadline。添加 `lastState` 变量以将最后观察到的状态携带到超时错误消息中。在查询错误路径中添加 `ctx.Err()` 检查，以便 SIGINT 取消返回干净的 `context.Canceled` 错误，而不是包装的 DB 错误。添加文档注释到 `waitForRun` 解释循环不变量。

---

### WR-01: GCS 客户端构造上下文生命周期未文档化

**修改的文件:** `internal/connector/firstparty/gcs/factory.go`
**提交:** b374cea
**应用的修复:** 在 `context.WithTimeout` 调用上方添加代码注释,说明构造上下文仅用于 `gcstorage.NewClient` (初始拨号)，不随客户端保留以供后续操作使用，遵循与 BigQuery factory 相同的文档模式。

---

### WR-02: 并发令牌重新竞争-重试行为未文档化

**修改的文件:** `internal/runtime/executor.go`
**提交:** b593263
**应用的修复:** 在资源令牌失败路径的 `releaseAcquired()` 调用上方添加多行 NOTE 注释，说明所有令牌在重试 sleep 之前释放，重试的尝试必须在下一次迭代中与新运行公平竞争重新获取它们，这可能导致高负载下的饥饿 — 如果观察到，建议在 Phase 3 重新审视。

---

### WR-03: executor.Run 在每步循环中忽略 Registry.Get 错误

**修改的文件:** `internal/runtime/executor.go`
**提交:** c69600f
**应用的修复:** 将 `stepAsset, _ := e.deps.Registry.Get(name)` 替换为正确的错误检查。如果 `Get` 返回错误 (资产在 DAG 构建和执行之间从注册表中移除)，函数返回描述性错误，而不是传递 nil `*asset.Asset` 到 `runStep`，这将在 `safeMaterialize` 的 recovery wrapper 之前导致 nil 指针 panic。

---

### WR-04: Reaper SweepOnce 使用手动 rows.Close() 而不是 defer

**修改的文件:** `internal/run/reaper.go`
**提交:** 880ee05
**应用的修复:** 将扫描错误早期返回路径中的手动 `_ = rows.Close()` 和循环后无条件的 `_ = rows.Close()` 替换为在成功的 `QueryContext` 调用后立即使用单个 `defer rows.Close()`。保留循环后的 `rows.Err()` 检查。这与代码库中所有其他连接器使用的惯用模式一致。

---

### WR-06: mysql/snowflake quoteIdentifier 有不适用于 SQL 标识符的 ".." 路径遍历检查

**修改的文件:** `internal/connector/firstparty/mysql/mysql.go`, `internal/connector/firstparty/snowflake/snowflake.go`
**提交:** 55fc0ce
**应用的修复:** 从 `mysql.quoteIdentifier` 和 `snowflake.quoteIdentifier` 中移除 `strings.Contains(id, "..")` 检查及其关联的错误返回。保留反引号/双引号字符拒绝 (实际的 SQL 注入防御)。更新 `mysql.quoteIdentifier` 的文档注释，移除路径遍历守卫的提及。

---

## 已修复 / 在其他地方处理

### CR-03: BigQuery splitIdentifier 对 2 部分标识符返回空项目

**状态:** already_fixed (commit 3054983)
**文件:** `internal/connector/firstparty/bigquery/bigquery.go:112-114`
**注意:** `if project == "" { project = b.project }` 守卫存在于 `Read` 方法中。未采取任何行动;未创建重复提交。

---

### WR-05: waitForRun 未干净地处理 QueryRowContext 的 context.Canceled

**状态:** 在 CR-02 修复中处理 (commit f226c5b)
**文件:** `cmd/platform/materialize.go`
**注意:** 重构后的循环 (CR-02) 在任何 DB 查询之前放置 `select { case <-ctx.Done(): return ctx.Err() }`。在取消时,select 分支首先触发,返回干净的 `context.Canceled`,无需 DB 往返。对于在查询中途到达取消的剩余情况,查询错误路径上显式的 `if ctx.Err() != nil { return ctx.Err() }` 检查 (第 121-123 行) 涵盖了 WR-05 中提出的 UX 关注点。不需要单独的提交。

---

_修复时间: 2026-05-08T00:00:00Z_
_修复者: Claude (gsd-code-fixer)_
_迭代: 1_