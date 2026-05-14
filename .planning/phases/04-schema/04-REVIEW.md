# Phase 4: 代码审查报告

**审查时间：** 2026-05-09T13:30:00Z
**深度：** standard
**文件审查数：** 76
**状态：** issues_found

## 概述

Phase 4 引入了 schema 和 lineage 子系统（capture writers、递归 CTE 遍历、schema diff/classify、OpenLineage translator、metadata store 和 CLI/REST 端点）。整体质量很高：参数化查询被一致使用、三层防御深度cap（Go 层 `MaxDepth`、SQL `LEAST(@max_depth::int, 25)`、HTTP 层）都已到位、手动管理的部分索引与 writers 中的 WHERE 子句对齐、migration 是幂等的（使用 `DROP ... IF EXISTS` + `ALTER TABLE ... DROP CONSTRAINT IF EXISTS` 模式）。

未发现 SQL 注入 — 每个动态 SQL 位置都使用绑定参数和精心生成的占位符。无硬编码密钥。无直接命令注入风险（唯一的 `exec.CommandContext` 调用是 `runMigrate` 调用 `atlas migrate apply --env <env>`，其中 `atlasEnv` 被验证为默认值并作为单个参数传入，而非插值）。

以下发现集中在三个领域：

1. **并发/原子性缺口** — `ackSchemaChange` 进行无保护的 check-then-update（TOCTOU）；`CaptureRun` 步骤 3 为**所有**活跃边更新 `last_seen_*`（不限于声明的上游），因此不同资产并发运行会相互踩踏；`lineage.drift_detected` 事件在 run-update 事务内的 `event.Writer`（独立连接）上发出 — drift 状态是原子的，但审计事件不是。
2. **防御检查被丢弃** — `PrincipalFromContext` 的 `ok` 标志在 `ackSchemaChange` 中被丢弃，因此配置错误的路由仍会写入 `uuid.Nil` 作为 actor；CLI helper `runImpact` 参数拆分将任何以 `-` 开头的 token 视为 flag（会将 `-` 开头的位置参数错误分类）。
3. **测试脆弱性/假信心模式** — `cmd/platform/impact_test.go` 用有 bug 的 substring helper 重新实现了 `strings.Contains`；`containsAt` helper 有冗余条件，会在边缘情况下处理错误，尽管此文件中的调用从未触及这些边缘。

未发现 Critical 问题。8 个 Warning 是值得修复的真实正确性问题；7 个 Info 项目是质量/可维护性改进。

## Warnings

### WR-01: `ackSchemaChange` 中存在 Check 和 Update 之间的 TOCTOU 竞争

**文件：** `internal/api/schema_handlers.go:41-57`

**问题：** handler `Get` 了 schema_change 行，检查 `existing.AcknowledgedAt != nil`，然后发出单独的 `UpdateOneID` 来设置 ack 列。这两个操作没有事务包装。两个并发 governance-role 调用者都可能通过 `AcknowledgedAt == nil` 检查，然后都成功 — 第二个 `UpdateOneID` 会覆盖第一个 ack 的 `acknowledged_by`/`reason`。D-10 记录为 "ack-once"，但 handler 允许最后写入者胜出的竞争，违反了该不变量。

**修复：** 在事务中包装条件更新，或使用 SQL 级别的守卫 UPDATE：

```go
// Option A: conditional UPDATE (returns 0 rows on conflict).
n, err := deps.Ent.SchemaChange.Update().
    Where(schemachange.IDEQ(id), schemachange.AcknowledgedAtIsNil()).
    SetAcknowledgedAt(now).
    SetAcknowledgedBy(principal.UserID).
    SetAcknowledgementReason(body.Reason).
    Save(r.Context())
if err != nil { ... }
if n == 0 {
    writeProblemJSON(w, http.StatusConflict, "already_acknowledged",
        "this schema change was already acknowledged")
    return
}
```

CLI runSchemaAckBreak 在 `cmd/platform/schema.go:85-104` 中有相同的 TOCTOU 模式，应该以相同方式修复。

### WR-02: `PrincipalFromContext` ok 标志被丢弃 — 错误配置的路由上 `uuid.Nil` actor

**文件：** `internal/api/schema_handlers.go:38`

**问题：** `principal, _ := auth.PrincipalFromContext(r.Context())` 丢弃了 `ok` 标志。当前路由器在处理程序前接入 `auth.Middleware` + `RequireRole("governance")`，因此在部署的管道中 principal 是有保证的。但：
- 未来重构如果删除或重新排序中间件，将静默开始写入 `uuid.Nil` 到 `acknowledged_by`、`event_log.actor_id` 和事件 payload 的 `acknowledged_by` 字段 — 这是一个严重的审计跟踪完整性漏洞，不会出现在单元测试中。
- D-10 记录 ack 身份是规范的问责记录。

**修复：** 将缺失的 principal 视为编程错误和 401：

```go
principal, ok := auth.PrincipalFromContext(r.Context())
if !ok {
    writeProblemJSON(w, http.StatusUnauthorized, "authentication_required",
        "authentication required")
    return
}
```

这匹配了 `internal/metadata/handler.go:85-89` 中的 `metadata.Handler.patch` 模式。

### WR-03: `CaptureRun` 为资产的**所有**活跃边更新 `last_seen_*`，而不仅仅是声明的上游

**文件：** `internal/lineage/capture.go:184-200`

**问题：** 步骤 3 的 UPDATE 仅按 `to_asset = $3 AND superseded_at IS NULL` 过滤。它重写**所有**指向资产的活跃边的 `last_seen_run_id`、`last_seen_at`（以及 `first_seen_run_id = uuid.Nil` 时的 `first_seen_run_id` 和 `first_seen_at`），包括当前运行**未声明**的边。如果资产处于部分迁移过程中，其中陈旧的上游边尚未被 `SyncStaticEdges` 停用，此步骤会将运行归因于运行实际未使用的边 — 污染了审计跟踪和 D-15 时间点视图。

特别是，`uuid.Nil` 哨兵提升会在第一次观察时在陈旧边上设置 `first_seen_run_id`，这是错误的：运行没有产生该边。

**修复：** 将 UPDATE 限制为运行的实际贡献集（声明上游和观察上游的交集，或仅声明上游）：

```go
// Only update edges that the run actually attributes to.
ups := a.Upstreams()
if len(ups) > 0 {
    args := make([]any, 0, 3+len(ups))
    args = append(args, runID, now, a.Name())
    for _, u := range ups { args = append(args, u) }
    sql := `UPDATE asset_edges SET last_seen_run_id=$1, last_seen_at=$2, ...
             WHERE to_asset=$3 AND superseded_at IS NULL
               AND from_asset IN (` + placeholders(len(ups), 4) + `)`
    if _, err := tx.ExecContext(ctx, sql, args...); err != nil { ... }
}
```

### WR-04: `lineage.drift_detected` 事件在 run-tx 内发出破坏了原子性

**文件：** `internal/lineage/capture.go:235-250`

**问题：** `CaptureRun` 在执行器的 run-update 事务（`tx *sql.Tx`）内运行，但 `w.events.Append(...)` 通过 `event.Writer` 写入，后者使用*独立*的 `*sql.DB` 连接（参见 Phase 1 `event.NewWriter(store)` 先例）。代码注释承认这一点："The event.Writer uses its own DB connection (not in our tx)." 含义是如果调用者的事务在 `CaptureRun` 成功后回滚，`lineage.drift_detected` 事件仍留在 `event_log` 中，即使 `asset_versions.drift_status` 已回滚到 `'clean'` — 事件日志和规范状态之间的分歧。

这是一个既有的模式（Phase 1 对 `auth.token_expired` 使用它），但 D-21 明确要求状态变更和审计事件之间的原子性。Phase 4 capture 测试仅在 happy path 中事务提交时通过。

**修复：** 注入接受 `tx *sql.Tx` 的 `event.Writer` 变体，或在 `tx.Commit()` 后从执行器侧缓冲事件并发出。`internal/schema/capture.go:73-80` 处的 `schema.Capture` writer 有相同的模式 — 相同的修复适用。

### WR-05: CLI `runImpact` 错误分类以 `-` 开头的位置参数

**文件：** `cmd/platform/impact.go:29-35`

**问题：** 参数拆分器将任何以 `-` 开头的 token 视为 flag：

```go
for _, a := range args {
    if len(a) > 0 && a[0] == '-' {
        flagArgs = append(flagArgs, a)
    } else {
        positional = append(positional, a)
    }
}
```

如果资产名称恰好以 `-` 开头（例如 literally 名为 `-foo` 的资产或故意的对抗性名称），它会被静默移入 `flagArgs` 并被 `fs.Parse` 拒绝。REST 端的资产名称验证器（`assetNameRE` 允许 `[a-zA-Z0-9_.-]`，允许前导 `-`）比此拆分器更宽松 — 两者不一致。

**修复：** 在第一个非 flag token 处停止拆分（标准 `--` 约定），或要求资产名称是*第一个*位置参数，其余视为 flag：

```go
if len(args) == 0 || (len(args[0]) > 0 && args[0][0] == '-') {
    return fmt.Errorf("usage: ./platform impact <asset> ...")
}
assetName := args[0]
if err := fs.Parse(args[1:]); err != nil { ... }
```

相同的拆分模式用于现有的 `runBackfill`；考虑统一。

### WR-06: `column_edges_active_unique` 索引不覆盖非 NULL `partition_key`

**文件：** `migrations/20260509120000_phase4_lineage_schema.sql:241-244`

**问题：** 用作 `CaptureRun` 列边 upsert 的 `ON CONFLICT` 目标的唯一索引有谓词 `WHERE superseded_at IS NULL AND partition_key IS NULL`。任何带有非 NULL `partition_key` 的列边**不被**此索引覆盖，因此 upsert 的 `ON CONFLICT ON CONSTRAINT column_edges_active_unique` 子句将失败，错误为 `there is no unique or exclusion constraint matching the ON CONFLICT specification`。

migration 注释承认这一点："Partition-aware uniqueness (partition_key IS NOT NULL case) is a deferred concern"。但 `internal/lineage/capture.go:158-179` 中的 writer 没有guard 阻止 partition-keyed 边被尝试；如果运行时在 `ColumnRef` 上设置非 NULL `partition_key`（字段存在于 `ent.ColumnEdge` 和 `asset.MaterializeResult`），upsert 将硬失败。

**修复：** (a) 为 `partition_key IS NOT NULL` 添加第二个部分唯一索引，或 (b) 让 `CaptureRun` 拒绝带有非 nil partition key 的列边，直到该索引落地：

```go
if len(cl) > 0 && /* any ref has partition_key */ {
    return fmt.Errorf("partition-keyed column lineage requires Phase 5 partition uniqueness index")
}
```

### WR-07: `unmarshalSchemaFromMap` 往返以 Go 字段名密钥存储 schema_data

**文件：** `internal/connector/schema_types.go:13-37` 和 `cmd/platform/schema.go:244-254`

**问题：** `connector.Schema` 和 `connector.SchemaColumn` **没有 JSON 结构标签**。`schema/capture.go` 通过 `json.Marshal(connector.Schema)` 写入快照，产生 `{"Columns":[...],"PrimaryKey":[...],"Name":"...","Type":"...","Nullable":true,"Default":null,"IsPrimaryKey":false,"Comment":""}` 带有大写密钥。通过 `unmarshalSchemaFromMap` 往返读取会通过相同类型回传，因此*内部*工作正常。但：
- migration 调用 `schema_data` "full Schema JSON snapshot (connector.Schema serialized)" — 第三方工具（Marquez 导入、仪表板、通过 `psql` 调试）会看到非传统 CamelCase 密钥。
- 未来为 `connector.Schema` 添加 JSON 标签的更改会静默破坏历史 schema_versions 行的回读。
- `internal/lineage/openlineage/translate.go:25-57` 中的 OpenLineage translator 定义了*自己的*带 snake_case 风格 JSON 标签的形状 — 与 OL 规范一致 — 确认不一致是内部问题。

**修复：** 为 `connector.Schema` 和 `connector.SchemaColumn` 添加与 schema_data 文档匹配的 JSON 标签，编写迁移以转换现有行（或通过标签别名将线格式固定到当前 Go 名称密钥），并添加回归测试断言线形状。

### WR-08: `runImpact` CLI 缺少资产名称验证，REST handler 执行 regex

**文件：** `cmd/platform/impact.go:58` vs `internal/api/lineage_handlers.go:35-39`

**问题：** HTTP handler 根据 `^[a-zA-Z0-9_.\-]{1,256}$`（`assetNameRE`）验证 `asset`，注释说："Defense-in-depth: sqlc parameterized queries already prevent SQL injection, but the regex provides an additional layer of input validation." CLI 路径**不**应用相同的 regex — 它将原始 `assetName` 直接传入 `impact.Analyze`。任何拥有 shell 访问权限的用户都可以绕过输入验证。这在大多数情况下是合理的（CLI 已经意味着信任），但不对称令人惊讶，`assetNameRE` 应该被导出并在 CLI 中重用以保持一致性。

**修复：** 导出 `assetNameRE`（例如到 `internal/lineage/impact` 或 `internal/asset`）并在 `runImpact` 和 `runLineageExport` 中应用：

```go
if !asset.NameRE.MatchString(assetName) {
    return fmt.Errorf("asset name must match %s", asset.NameRE.String())
}
```

## Info

### IN-01: 测试辅助 `containsAt` 糟糕地重新发明了 `strings.Contains`

**文件：** `cmd/platform/impact_test.go:57-67`

**问题：** 测试文件故意避免导入 `strings` 并滚动自己的 `contains`/`containsAt`。实现有冗余条件：

```go
func contains(s, substr string) bool {
    return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}
```

第一个检查 `len(s) >= len(substr)` 已经覆盖了非空 `substr` 的空 `s` 情况；`s == substr` 短路是正确的但与 `containsAt` 冗余。对于空 `substr`，`containsAt` 循环 `i=0..len(s)` 检查 `s[0:0] == ""`，这会在第一次迭代中正确返回 true，但 `len(s) > 0` guard 会在情况 `s="" substr=""` 上错误地短路（返回 false；`strings.Contains("", "")` 返回 true）。

**修复：** 只需导入 `strings` — 在测试文件中没有政策原因避免它：

```go
import "strings"
// ...
if !strings.Contains(msg, "depth") { ... }
```

### IN-02: 空参数分支通过 panic recovery 被集成测试始终拒绝

**文件：** `internal/lineage/impact/analyze_test.go:62-89, 119-134, 138-169, 197-211`

**问题：** 多个测试使用 `defer recover()` 来将 `nopDB` panic 作为*成功*指标，即验证通过。这种模式难以阅读且脆弱：
- 如果 DB 层后来被更新为返回类型化错误而非 panic，测试将静默开始通过而永远不验证验证。
- `TestAnalyzeValidDirections` 接受 panic 和无错误两种情况作为成功 — 无法区分工作验证器和缺失的验证器。

**修复：** 使用返回哨兵错误的适当测试 double，然后断言：

```go
type sentinelDB struct{}
var errSentinel = errors.New("reached DB")
func (sentinelDB) Query(...) (pgx.Rows, error) { return nil, errSentinel }

_, err := impact.Analyze(ctx, sentinelDB{}, ...)
require.ErrorIs(t, err, errSentinel) // proves validation passed
```

### IN-03: `runMigrate` 不验证 `ATLAS_ENV` 值

**文件：** `cmd/platform/main.go:212-214`

**问题：** `os.Getenv("ATLAS_ENV")` 被未经检查地传入 `exec.CommandContext("atlas", "migrate", "apply", "--env", atlasEnv)`。虽然 `--env <name>` 是单个参数且不会插值到 shell，但控制环境的攻击者仍可通过选择 atlas.hcl 中的任何 env 名称来触发 Atlas 行为。防御深度：在传递前根据允许列表（`local`、`ci`、`prod`）验证。

**修复：**

```go
var allowed = map[string]struct{}{"local": {}, "ci": {}, "staging": {}, "prod": {}}
if _, ok := allowed[atlasEnv]; !ok {
    return fmt.Errorf("ATLAS_ENV %q not allowed", atlasEnv)
}
```

### IN-04: `recursive_cte_seed.go` `SeedDAG` ON CONFLICT 子句对 Postgres 索引语法不正确

**文件：** `internal/lineage/lineagetest/recursive_cte_seed.go:172-173`

**问题：** `ON CONFLICT (from_asset, to_asset) WHERE superseded_at IS NULL DO NOTHING` — 附加到 `ON CONFLICT` 的 `WHERE` 子句是*index_predicate*（必须匹配部分唯一索引）。migration 创建 `asset_edges_active_unique` 作为 `(from_asset, to_asset) WHERE superseded_at IS NULL` 上的部分唯一索引，是匹配的。因此语法在*今天*工作。但 seeder 依赖该命名部分索引的存在，如果 migration 改为使用不同的唯一约束（例如 `ON CONFLICT ON CONSTRAINT asset_edges_active_unique`），将静默开始失败。

**修复：** 使用显式约束名称与 `lineage.Writer.SyncStaticEdges` 对齐（`ON CONFLICT ON CONSTRAINT asset_edges_active_unique DO NOTHING`），以便 seeder 在约束重命名时大声中断：

```go
const stmt = `INSERT INTO asset_edges (...) VALUES (...)
              ON CONFLICT ON CONSTRAINT asset_edges_active_unique DO NOTHING`
```

### IN-05: `prevPtr any` 可能隐藏 nil 语义 — 次要

**文件：** `internal/schema/writer_diff.go:47-50`

**问题：** 模式工作正确：
```go
var prevPtr any
if prevVersionID != nil {
    prevPtr = *prevVersionID
}
```
未分配的 `var x any` 是 nil 接口，`database/sql` 正确地将其转换为 SQL `NULL`。但未来读者可能不知道这个习惯 — 添加注释（"nil interface 通过 database/sql 映射到 SQL NULL"）或使用 `sql.NullString`/`uuid.NullUUID` 来获得明确的可空性会使意图更清晰。

### IN-06: `ackSchemaChange` 测试模拟非 governance 角色但不断言中间件行为

**文件：** `internal/api/schema_handlers_test.go:146-169`

**问题：** `TestAck_RequiresGovernanceRole` 注释 "Handler itself doesn't enforce role; RequireRole middleware does" 并且仅断言 `rec.Code != 0`（无 panic）。此测试不提供对角色检查的实际覆盖 — 如果 handler 接受任何角色，它仍会通过。路由器级别的 enforcement 应在路由器级别测试（构建完整链并断言 403 的单独测试）。

**修复：** 删除测试（它提供假信心）或将其移至 `router_test.go` 并执行实际的中间件链。

### IN-07: `seedSchemaChange` 和其他测试辅助函数吞掉 context

**文件：** `internal/api/schema_handlers_test.go:45-75`, `internal/lineage/openlineage/translate_test.go:23-78`

**问题：** 多个测试 seeders 调用 `context.Background()` 而非 `t.Context()`（Go 1.22+）或接受 `ctx` 参数。如果测试超时，seed 查询继续在测试 DB 上运行。对内存 SQLite 的单元测试影响低，但值得标准化。

**修复：** 显式接受 `ctx context.Context` 或使用 `t.Context()`：

```go
func seedSchemaChange(t *testing.T, ctx context.Context, deps Deps, ...) uuid.UUID { ... }
```

---

_审查时间：2026-05-09T13:30:00Z_
_审查者：Claude (gsd-code-reviewer)_
_深度：standard_
