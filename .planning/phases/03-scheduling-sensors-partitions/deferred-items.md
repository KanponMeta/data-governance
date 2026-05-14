# Phase 03 — Deferred Items (out-of-scope discoveries)

## 来自 plan 03-03 (priority-claim-and-load-test) — 同时也被 plan 03-05 独立确认

### internal/runtime executor tests fail with "unsupported driver: pgx"

**发现于：** Executor.Run 签名迁移后对 `go test ./internal/runtime/...` 的 Task 2 验证。

**症状：** 六个测试在测试夹具设置阶段失败（executor_test.go 第 101 行：`stent.Open("pgx", dsn)`），错误为 `open ent: unsupported driver: "pgx"`。

**预先存在：** 通过 `git stash` 回滚验证 — 失败发生在父提交 `2f2df38af493af3bbecd1a3f1502c66af9ca1588` 上，早于此计划的签名变更。根本原因是 ent 存储驱动注册：ent 的存储层期望 `postgres` 作为驱动名称，即使 `pgx/v5/stdlib` 在 `database/sql` 中注册为 `pgx`。Phase 2 的 `executor_test.go` 编写时驱动名称为 `postgres` 或两者皆有，后续的驱动注册合并（或库升级）破坏了绑定，但没有人注意到，因为测试被 `DATABASE_URL` 屏蔽。Plan 03-05 独立地在相同的基础提交上重新确认了预先存在。

**为何推迟：** 超出 plan 03-03 的范围 — 症状早于此计划，需要么在 `internal/storage/storage.go` 中重新将 pgx 注册为 `postgres` ent 驱动名称（其他计划会触及），要么重写 executor 测试夹具以使用与 claim 测试相同的 `*sql.DB` 路径（这些测试确实使用 `pgx` 名称工作）。Plan 03-03 的签名变更不会引入或加重失败 — 失败在夹具初始化时显现，在任何签名变更代码路径运行之前。

**Plan 03-03 计划内验证：** 运行时包仍然**构建干净**（`go build ./...` 为绿色），新的 claim_test.go 测试（使用 `*sql.DB` 路径）全部通过。签名迁移通过构建 + claim 测试进行端到端验证；休眠的运行时测试夹具将在未来计划中修复存储驱动问题时恢复（可能在 plan 03-04 / 03-05 当调度器 / 传感器评估器消费 Executor 时）。

**建议修复（未来计划）：**
- 将 `stent.Open("pgx", dsn)` → `stent.Open("postgres", dsn)`（同时也在 `internal/storage/` 中将 pgx 注册为该名称），或
- 用 `internal/run/claim_test.go` 的 `sqlStorage` 存根使用的相同 `*sql.DB` 路径替换 ent 客户端构造，这完全绕过了不需要 ent 的测试。

**受影响的测试（均在夹具阶段失败，非主体）：**
- `TestExecutor_SuccessfulRun`
- `TestExecutor_RetryAndFail`
- `TestExecutor_PanicRecovery`
- `TestExecutor_TopologicalOrder`
- `TestExecutor_ConcurrencyTokenZeroCapacity`
- `TestExecutor_HeartbeatTicks`

**推荐负责人：** Phase 2 负责人 / executor 维护者。