---
phase: 05-governance
reviewed: 2026-05-10T02:06:36Z
depth: quick
files_reviewed: 91
files_reviewed_list:
  - cmd/platform/audit.go
  - cmd/platform/governance.go
  - cmd/platform/governance_test.go
  - cmd/platform/main.go
  - cmd/platform/policy.go
  - cmd/platform/reconciler.go
  - cmd/platform/reconciler_test.go
  - cmd/platform/role.go
  - cmd/platform/scheduler.go
  - cmd/platform/worker.go
  - configs/notifications.example.yaml
  - configs/policies.example.yaml
  - internal/api/audit_handlers.go
  - internal/api/governance_handlers.go
  - internal/api/policy_handlers.go
  - internal/api/role_handlers.go
  - internal/api/router.go
  - internal/asset/asset.go
  - internal/asset/builder.go
  - internal/asset/builder_test.go
  - internal/asset/fingerprint.go
  - internal/asset/io_masking.go
  - internal/asset/io_masking_test.go
  - internal/asset/quality_builder_test.go
  - internal/asset/types.go
  - internal/audit/anchor.go
  - internal/audit/canonical.go
  - internal/audit/export.go
  - internal/audit/retention.go
  - internal/audit/types.go
  - internal/audit/verify.go
  - internal/audit/writer.go
  - internal/auth/casbin.go
  - internal/auth/jwt.go
  - internal/auth/middleware.go
  - internal/auth/rbac_model.conf
  - internal/auth/service.go
  - internal/connector/capability.go
  - internal/connector/firstparty/bigquery/masking.go
  - internal/connector/firstparty/postgres/query_aggregate.go
  - internal/connector/firstparty/snowflake/masking.go
  - internal/connector/mask_types.go
  - internal/event/event.go
  - internal/event/types.go
  - internal/governance/auto_approval.go
  - internal/governance/auto_approval_test.go
  - internal/governance/handler.go
  - internal/governance/handler_test.go
  - internal/governance/pii_propagator.go
  - internal/governance/pii_propagator_test.go
  - internal/governance/reviewers.go
  - internal/governance/reviewers_test.go
  - internal/governance/sla_scanner.go
  - internal/governance/sla_scanner_test.go
  - internal/governance/workflow.go
  - internal/governance/workflow_test.go
  - internal/lineage/capture.go
  - internal/lineage/capture_test.go
  - internal/notification/channel.go
  - internal/notification/router.go
  - internal/notification/smtp.go
  - internal/notification/template.go
  - internal/notification/webhook.go
  - internal/notification/worker.go
  - internal/platform/registry.go
  - internal/policy/handler.go
  - internal/policy/mask.go
  - internal/policy/mask_test.go
  - internal/policy/reconciler.go
  - internal/policy/store.go
  - internal/policy/store_test.go
  - internal/policy/sync_job.go
  - internal/policy/yaml_loader.go
  - internal/quality/dispatcher.go
  - internal/quality/evaluator.go
  - internal/quality/freshness.go
  - internal/quality/rule.go
  - internal/quality/store.go
  - internal/runtime/executor.go
  - internal/runtime/executor_mask_test.go
  - internal/runtime/executor_test.go
  - internal/runtime/export_test.go
  - internal/runtime/hooks.go
  - internal/runtime/quality_executor_test.go
  - internal/schedule/daemon_freshness_test.go
  - internal/schedule/daemon.go
  - internal/storage/ent/schema/asset_version.go
  - internal/storage/ent/schema/governance_review.go
  - migrations/20260510000001_phase5_audit_rbac.sql
  - migrations/20260510000002_phase5_column_policies.sql
  - migrations/20260510000003_phase5_quality.sql
  - migrations/20260510000004_phase5_pii_propagation.sql
  - migrations/20260510000005_phase5_governance_workflow.sql
findings:
  critical: 6
  warning: 11
  info: 6
  total: 23
status: issues_found
---

# Phase 5: 代码审查报告

**审查时间：** 2026-05-10T02:06:36Z
**深度：** quick
**审查文件数：** 91
**状态：** issues_found

## 摘要

Phase 5 引入了平台的安全/治理骨干：哈希链审计日志、Casbin RBAC、列级掩码（Snowflake DDM、BigQuery CLS、管道内）、PII 传播、治理审批工作流、quality DSL 和通知。许多地方可以看到防御性实践——`crypto/subtle.ConstantTimeCompare` 用于 HMAC 验证有文档说明，`WithValidMethods` JWT allowlist 防止 `alg=none`，掩码 DDL 使用 Postgres 风格的标识符引用（`""`），Casbin 在每个 governance/audit/policy 端点上强制 RBAC，`audit.audit_log` 上启用了 RLS，`crypto/rand`（而非 `math/rand`）用于邀请令牌。

然而，几个 CRITICAL 错误威胁着阶段的核心保证：

1. **审计哈希链完整性被时间戳不匹配破坏**——哈希计算与行插入之间的时间戳不一致（`internal/audit/writer.go`）。每个调用者传递零 `OccurredAt`（`emitVerifyFailedEntry`、`emitExportAuditEntry` 及许多 SLA-scanner / sync-job 路径都是这种情况）的条目将存储一个与存储时间戳不匹配的哈希——导致 `audit.Verify` 在干净链上报告篡改检测。
2. **`audit.Verify` 无法运行**：它将 `occurred_at`（`TIMESTAMPTZ`）扫描到 `int64` 变量中。pgx 将在第一行拒绝此操作，因此验证路径（以及 `/audit/verify` REST 端点、`audit verify` CLI）都是非功能性的。
3. **治理审批绕过**：`Workflow.Approve` 通过字符串搜索先前评论中的 `"approved by "` 来计算审批数——单个决策者可以重复自我批准，或者包含该子字符串的 `Reject` 评论将被计为批准。没有检查决策者是否属于审核者池、决策者不是提交者（无四人 eyes），或者决策者尚未投票。这破坏了迁移声称的整个 SOC 2 治理姿态。
4. **进程内通知队列静默破坏事务性入队**：`InsertTx` 忽略提供的 `*sql.Tx` 并立即插入，与文档化的契约矛盾。当源事务回滚时通知仍会触发。
5. **CLI 在无认证情况下接受 `ACTOR_ID` env var** 用于治理提交/批准/拒绝。任何具有 shell 访问权限的人都可以决定任意用户 UUID 的审核，且审计链将欺骗的 UUID 记录为合法操作者。
6. **质量规则 SQL 通过 `fmt.Sprintf` 构建**（`internal/asset/types.go` 中的 `NullCheck`、`RangeCheck`），列名嵌入在 `"..."` 中但未转义嵌入的引号——包含 `"` 的列名将突破并注入 SQL 到数据仓库。

警告集中在：处理程序中的错误掩盖（原始 DB 错误泄露到客户端）、治理门 fail-open 竞争窗口、`ApplyPartial` 中的字节 vs rune 索引、错误消息子字符串匹配检测"表缺失"、省略 `OccurredAt` 的 audit `WriteEntry` 调用，以及 PII 传播器代码路径中未使用的 `havePrior`。Info 项目涵盖死代码、幻数以及 `==` 而非 `errors.Is`。

## 严重问题

### CR-01: 审计哈希链完整性破坏——哈希使用输入 `OccurredAt`，行存储默认值 `time.Now()`

**文件：** `internal/audit/writer.go:53-71`
**问题：** `computeSelfHash` 在第 56-59 行应用零值默认值之前使用 `e.OccurredAt` 调用。当调用者调用 `WriteEntry` 而不设置 `OccurredAt`（常见——见 `emitExportAuditEntry`、`emitVerifyFailedEntry`、重试失败路径）时，哈希使用 `(0).UnixNano()` 计算，而行使用 `time.Now().UTC()` INSERT。`Verify` 从存储的时间戳重新计算并报告每个此类行的链不匹配。

**修复：** 在哈希计算之前设置时间戳默认值：
```go
// 4. Insert the audit row.
occurredAt := e.OccurredAt
if occurredAt.IsZero() {
    occurredAt = time.Now().UTC()
}
// ... actorID / expiresAt prep ...

// 3. Compute self_hash using the SAME occurredAt that will be persisted.
seq = prevSeq + 1
selfHash := computeSelfHash(seq, prevHash, occurredAt, e.EventType, e.ActorID, e.ResourceType, e.ResourceID, payloadBytes)
```

### CR-02: `audit.Verify` 将 `TIMESTAMPTZ` 扫描到 `int64`——验证路径非功能性

**文件：** `internal/audit/verify.go:55, 61, 94`
**问题：** 第 55 行声明 `var occurredAtUnixNano int64`。第 61 行将 `occurred_at`（根据迁移 20260510000001 第 28 行的 `TIMESTAMPTZ` 列）扫描到该 int64。pgx 将用扫描错误拒绝第一行，因此整个 `Verify` 函数立即失败。这意味着 `/audit/verify` REST 端点、`platform audit verify` CLI 和递归 `emitVerifyFailedEntry` 自我审计都是死代码路径。audit 包没有覆盖此代码路径的测试，这就是为什么这个错误没有被捕获。

**修复：** 扫描到 `time.Time` 并转换：
```go
var occurredAt time.Time
if err := rows.Scan(&seq, &rowPrevHash, &storedHash, &occurredAt, &eventType, &actorID, &resourceType, &resourceID, &payload); err != nil {
    return Result{Err: fmt.Errorf("verify: scan row %d: %w", seq, err)}, nil
}
// ... pass occurredAt.UnixNano() to computeSelfHashFromRow:
computedHash := computeSelfHashFromRow(seq, prevHash, occurredAt.UnixNano(), eventType, actorStr, resourceType, resourceID, payload)
```

同时为 `audit.Verify` 添加单元测试，针对新构建的链——`internal/audit/` 中没有任何测试是根本原因。

### CR-03: 治理审批绕过——法定人数通过非特权评论文本的子字符串计数

**文件：** `internal/governance/workflow.go:316, 331-347, 573-584`
**问题：** `decide()` 通过向行的 `comment` 列追加 `"[approved by <uuid>]"` 或 `"[rejected by <uuid>]"` 来构建投票账本。然后 `countApprovals` 扫描该文本查找 `"approved by "`。三种失败模式：

1. **自我批准/重复批准**：没有检查决策者已经投票。具有 `RequirePermission("/governance/reviews/*", "write")` 的用户可以对自己 `POST /approve` `quorum-1` 次以将行翻转为已批准。
2. **提交者 == 决策者**：无四人 eyes 检查。提交者可以批准自己的审核（假设他们同时持有 engineer 和 governance 权限，或者只检查 governance 权限）。
3. **子字符串冲突**：包含字面 `approved by ` 的自由格式 `Reject` 评论（例如，审核者因为"应该先由 privacy-team 批准"而拒绝）将计入下一次调用的批准计数。
4. **未检查池成员资格**：任何持有 governance 权限的用户都可以决定任何审核——甚至是专门路由到 `pool.Roles=['privacy-team']` 的审核。

**修复：** 用结构化 `governance_review_decisions` 表替换字符串账本（每 (review_id, decider_id) 一行，唯一键），并在 `decide()` 上设置以下限制：
- decider_id 不在已投票集中
- decider_id 与 submitter_id 不同（可配置）
- 决策者的角色与 `pool.Roles` 相交
- 批准行数 >= 有效法定人数

在该 schema 落地之前，至少在 `decide()` 中验证 `decider != submitter` 并拒绝来自同一决策者的第二次投票：
```go
if decider == submitterID {
    return Review{}, errors.New("governance: decider cannot be submitter (four-eyes)")
}
if strings.Contains(currentComment, "by "+decider.String()+"]") {
    return Review{}, errors.New("governance: decider already voted on this review")
}
```

### CR-04: `InProcessQueue.InsertTx` 忽略 tx 并立即插入——破坏文档化的原子入队契约

**文件：** `internal/notification/worker.go:253-260`
**问题：** `InsertTx` 文档注释说"我们在生产调用者中有意在 tx.Commit() 之后 Insert"，但代码无条件调用 `Insert` 忽略提供的 `*sql.Tx`。调用者使用 `q.InsertTx(ctx, tx, args)` 在 `tx.Commit()` 之前（见 `internal/governance/workflow.go:232`、`internal/governance/sla_scanner.go:148`、`internal/quality/freshness.go:141`）。如果 `tx.Commit()` 失败，审计行 + 治理行 + sla_breach_emitted_at 更新都会回滚，但通知已经被排队并将发送——为从未持久化的转换发出幻像"审核已提交"/"SLA 违反"事件。

**修复：** (a) 通过注册 `tx.Commit` 钩子缓冲参数直到提交后（Go 的 `database/sql` 没有这样的钩子，所以显式包装提交）或 (b) 文档化进程内队列是尽力而为的，让调用者在提交后转向 `Insert`：
```go
// Production callers: insert AFTER tx.Commit succeeds.
if err := tx.Commit(); err != nil { return err }
_ = w.queue.Insert(ctx, args) // post-commit
```
长期：切换到 River，其 `InsertTx` 本机遵守 tx（这正是选择 River 要填补的差距）。

### CR-05: CLI `getActorFromEnv` 允许任意操作者欺骗到哈希链

**文件：** `cmd/platform/governance.go:289-296`, `cmd/platform/policy.go:190-251`
**问题：** CLI 从 env 中读取 `ACTOR_ID` 无需任何认证，并将其作为 `Submit / Approve / Reject / Reassign` 的操作者传递——这些调用将 `actor_id` 写入哈希链审计日志。任何具有 shell 访问权限的人都可以发送 `ACTOR_ID=<arbitrary-uuid> ./platform governance review <id> --approve`，链将把冒名顶替记录为合法的。提交/拒绝/批准处理程序在审计有效载荷级别无法区分 CLI 调用和服务器内部操作者。

**修复：** 要求 CLI 用户进行身份验证（`platform login` 发出 JWT，然后将 JWT 存储在 `~/.platform/credentials.json`）；从已验证的 JWT 中读取用户 UUID，绝不从 env 读取。在那之前，用合成操作者标记每个 CLI 起源的审计条目，带有 `payload.cli=true` 属性，以便链读者可以将这些行隔离：
```go
func getActorFromEnv() (uuid.UUID, error) {
    return uuid.Nil, errors.New("CLI auth not yet implemented; use REST API")
}
```

并将 governance/role/policy CLI 子命令隐藏在构建标签或 env 功能标志（`PLATFORM_CLI_DANGEROUS=1`）后面，这样生产部署不会意外暴露绕过。

### CR-06: 质量规则 SQL 注入——`NullCheck` / `RangeCheck` 中未转义的列名

**文件：** `internal/asset/types.go:193-195, 240-242`
**问题：** `NullCheck.Evaluate` 通过 `fmt.Sprintf("... \"%s\" IS NULL ... FROM %s", n.Column, eval.AssetTable())` 构建 SQL。列名包装在 `"..."` 中但 `n.Column` 中的引号不会被双写。声明为 `email"; DROP TABLE x; --` 的列名将关闭标识符引号并注入。`RangeCheck.Evaluate`（第 240 行）存在相同缺陷。`eval.AssetTable()` 也是原始连接的——其来源是特定于连接器的，但 `connector.QualifiedTable(ref)` 不会转义。

虽然列名通常来自受信任的资产声明，但这仍然是一个注入面——返回攻击者控制的列名（例如，来自上游目录发现）的第三方连接器将损害数据仓库。SQLAssertion 更宽松（第 302 行：`strings.ReplaceAll(s.SQL, "${asset}", eval.AssetTable())`）。

**修复：** 转义标识符并验证/清理：
```go
func quoteIdent(s string) string {
    return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
// NullCheck.Evaluate
sqlText := fmt.Sprintf(
    `SELECT COUNT(*)::float8 AS total, COUNT(*) FILTER (WHERE %s IS NULL)::float8 AS nulls FROM %s`,
    quoteIdent(n.Column), eval.AssetTable())
```
并在构建时在 `Column` 上添加标识符验证器（例如，`regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)`），以便格式错误的名称在注册时失败，而不是在数据仓库执行时失败。

## 警告

### WR-01: `RequirePermission` 仅检查主要角色；多角色用户可能被拒绝他们应该拥有的访问权限

**文件：** `internal/auth/middleware.go:128-150`
**问题：** 注释声称"从已认证 Principal 的 JWT 中提取所有角色"，但代码仅检查 `p.Role`（单数，主要角色）。`Claims.Roles []string` 字段由 `Login` 填充（根据 `jwt.go:34`），但在 enforcement 时从不查阅。同时分配了 `data-engineer` 和 `governance` 的用户将仅强制执行主要角色——破坏了多角色设计。

**修复：** 迭代角色：
```go
roles := []string{p.Role}
// (extract additional roles from principal — currently Principal.Roles is not in the struct)
for _, r := range roles {
    if allowed, _ := enforcer.Enforce("role:"+r, obj, act); allowed {
        next.ServeHTTP(w, r); return
    }
}
writeForbidden(w, ...)
```
并扩展 `Principal` 以携带来自 JWT 的完整角色集。

### WR-02: REST 处理程序向客户端泄露原始 DB 错误消息（信息泄露）

**文件：** `internal/api/role_handlers.go:50, 62, 83, 109, 128`; `internal/policy/handler.go:85, 109, 128, 145, 151`; `internal/governance/handler.go:118, 197, 238`
**问题：** 许多处理程序执行 `http.Error(w, err.Error(), 500)` 或 `writeProblem(w, 500, "Internal Server Error", err.Error())`。DB 错误包括 schema 名称、表名、约束名和 pgx 包装文本——这些对攻击者映射系统很有用。例如，`assignRolesHandler` 如果请求者发送未知角色，会揭示 `roles.name` 上存在 FK。

**修复：** 在服务器端记录完整错误；仅向客户端返回通用详情：
```go
slog.Error("assign_role failed", "actor", actor.UserID, "err", err)
writeProblem(w, 500, "Internal Server Error", "internal error; see server logs")
```

### WR-03: `handleVerify` 仅验证 seq=1，而非整个链

**文件：** `internal/api/audit_handlers.go:73`
**问题：** 调用 `audit.Verify(ctx, db, 1, 0)`——`to=0`。在 `Verify` 内部，`if to < from { to = from }` 将 `to` 限制为 1，因此端点仅扫描一行。操作员调用 `/audit/verify` 即使后面的行被篡改也会获得虚假的"OK"。

**修复：** 首先查找 `MAX(seq)`（正如 `cmd/platform/audit.go:60-67` 已经正确做的那样），然后调用 `Verify(ctx, db, 1, maxSeq)`。

### WR-04: 用于哈希比较的 `bytesEqual` 应该使用 `subtle.ConstantTimeCompare`

**文件：** `internal/audit/verify.go:96, 126-136`
**问题：** 本地定义的 `bytesEqual` 不使用常量时间比较。虽然审计哈希不是凭证，但在链验证期间暴露时序差异可能让篡改攻击者探测哪个前缀匹配。此外，项目在其他地方明确要求 `subtle.ConstantTimeCompare` 用于 HMAC 检查——为保持一致性，审计哈希比较应使用它。

**修复：**
```go
import "crypto/subtle"
// ...
if subtle.ConstantTimeCompare(computedHash, storedHash) != 1 {
    // mismatch
}
```

### WR-05: `audit.WriteEntry` 调用者经常省略 `OccurredAt`，加重 CR-01

**文件：** `internal/audit/export.go:220-230`; `internal/audit/verify.go:176-186`; `internal/policy/sync_job.go:113-126`
**问题：** 多个调用站点构造 `audit.Entry{ EventType: ..., ResourceType: ..., Payload: ... }` 而不设置 `OccurredAt`。结合 CR-01，每个这样的行都会破坏链。即使 CR-01 修复后，每个审计发出站点也应明确设置 `OccurredAt = time.Now().UTC()`，以便可以将"调用者是否始终提供时间戳"的测试变成 vet 规则。

**修复：** 添加防御性检查：
```go
func WriteEntry(ctx context.Context, tx *sql.Tx, e Entry) (int64, error) {
    if e.OccurredAt.IsZero() {
        e.OccurredAt = time.Now().UTC()
    }
    // ... rest of WriteEntry uses e.OccurredAt for both hash + insert
}
```
并更新所有调用站点以明确设置 `OccurredAt`。

### WR-06: `ApplyPartial` 字节索引破坏多字节 UTF-8

**文件：** `internal/policy/mask.go:88-97`
**问题：** `len(value)`、`value[:reveal]`、`value[len(value)-reveal:]` 都按字节索引。对于 UTF-8 字符串，这可以在 rune 中间序列处分割并产生无效 UTF-8 的掩码输出，或暴露比预期更多的字符（例如，`reveal=2` 的 3 字节字符暴露 3 字节 rune 的 2 字节）。

**修复：** 转换为 `[]rune`：
```go
func ApplyPartial(value string, reveal int) string {
    if reveal <= 0 { reveal = 2 }
    runes := []rune(value)
    if len(runes) <= 2*reveal+1 {
        return ApplyRedact(value)
    }
    mid := strings.Repeat("*", len(runes)-2*reveal)
    return string(runes[:reveal]) + mid + string(runes[len(runes)-reveal:])
}
```

### WR-07: `isUndefinedTable` 子字符串匹配脆弱

**文件：** `internal/governance/auto_approval.go:372-380`
**问题：** 通过 `strings.Contains(err.Error(), "does not exist")` 检测"表不存在"。这依赖于区域设置（Postgres 翻译消息）、版本相关，并且匹配假阳性——例如，"value 'pii' does not exist in enum" 的约束错误将静默作为"表缺失"吞下。风险：合法查询错误降级为"fail-open，不需要策略"，让提交在应该出错时自动批准。

**修复：** 使用 pgx 错误代码：
```go
import "github.com/jackc/pgx/v5/pgconn"
func isUndefinedTable(err error) bool {
    var pe *pgconn.PgError
    if errors.As(err, &pe) {
        return pe.Code == "42P01" || pe.Code == "42703" // undefined_table | undefined_column
    }
    return false
}
```

### WR-08: `connectorName` 使用 `context.Background()` 调用 `Ping`——忽略调用者取消

**文件：** `internal/policy/sync_job.go:140-149`
**问题：** 当 River 关闭或 worker 被取消时，`Work` 立即返回，但如果它为日志行调用 `connectorName`，该辅助函数会使用后台上下文启动 `Ping`——可能对无响应的数据仓库无限期挂起。`Work` 已经在返回；日志调用泄漏 goroutine。

**修复：** 通过工作上下文：
```go
func connectorName(ctx context.Context, c connector.Connector) string {
    resp, err := c.Ping(ctx, connector.PingRequest{})
    // ...
}
```
并更新四个调用站点。

### WR-09: 治理门在缺少 `asset_versions` 行时 fail-open 创建竞争窗口绕过

**文件：** `internal/runtime/executor.go:290-294`
**问题：** 当 `governance_state` 查询返回 `sql.ErrNoRows` 时，门"允许运行继续（D-09 fail-open）"。注释 justification 为"首次注册期间的竞争"。但这正是攻击者会瞄准的绕过：使用从未见过的 `code_hash` 注册资产，立即加入运行，门允许在无需治理审核的情况下进行物化。

**修复：** Fail-closed + 重试——注册竞争预计在毫秒内解决，因此短暂 sleep + 重试比跳过门更合理：
```go
case errors.Is(err, sql.ErrNoRows):
    return fmt.Errorf("step %q: %w (asset_version not yet registered; retry shortly)", a.Name(), errMaterializationGated)
```

### WR-10: `CreateRole` 即使 ON CONFLICT DO NOTHING 意味着没有创建行时也发出审计

**文件：** `internal/auth/service.go:330-356`
**问题：** `INSERT ... ON CONFLICT (name) DO NOTHING` 与无条件 `audit.WriteEntry` 配对。当重复的 `CreateRole` 调用命中现有角色时，链记录一个针对已存在行的 `role.created` 事件。审计链消费者无法区分"首次创建"和"无操作重放"。

**修复：** 在发出审计之前检查 `RowsAffected`：
```go
res, err := tx.ExecContext(ctx, `INSERT INTO roles ... ON CONFLICT ...`, ...)
if err != nil { return err }
n, _ := res.RowsAffected()
if n == 0 { /* skip audit; role already exists */ return nil }
```

### WR-11: `dedupRoles` 在 len <= 1 时返回输入切片未修改，允许别名 mutation

**文件：** `internal/governance/reviewers.go:139-153`
**问题：** 当 `len(in) <= 1` 时直接返回 `in`。调用者（`Submit`、`Reassign`）持有此切片，随后可能通过 `append(pool.Roles, ...)` 突变 `pool.Roles`。如果调用者的源切片有备用容量，mutation 将影响原始切片。微妙的别名错误。

**修复：** 始终复制：
```go
out := make([]string, 0, len(in))
seen := make(map[string]struct{}, len(in))
for _, r := range in {
    if _, ok := seen[r]; ok { continue }
    seen[r] = struct{}{}
    out = append(out, r)
}
return out
```

## Info

### IN-01: `err == sql.ErrNoRows` 应该是 `errors.Is(err, sql.ErrNoRows)`

**文件：** `internal/governance/pii_propagator.go:129, 301`
**问题：** 直接比较在错误被包装时失败。项目在其他地方使用 `errors.Is`。目前直接比较有效，因为直接调用者是 `tx.QueryRowContext().Scan`，它返回未包装的 `sql.ErrNoRows`，但一致性很重要。

**修复：** `case errors.Is(err, sql.ErrNoRows):`

### IN-02: `err == ErrTokenExpired` 应该是 `errors.Is(err, ErrTokenExpired)`

**文件：** `internal/auth/middleware.go:74`
**问题：** 与 IN-01 相同——直接 `==` 今天有效只是因为 `jwt.go` 直接返回 `ErrTokenExpired`。如果未来调用者用 `%w` 包装错误，比较将静默失败。

**修复：** `if errors.Is(err, ErrTokenExpired) {`

### IN-03: 最大上游数的幻数 256 应该是命名常量

**文件：** `internal/lineage/capture.go:77-79`
**问题：** `len(ups) > 256` 内联重复一个常量。命名常量提高可发现性。

**修复：**
```go
const maxDeclaredUpstreams = 256 // DoS guard: real assets have <50 upstreams
if len(ups) > maxDeclaredUpstreams { ... }
```

### IN-04: `havePrior` 被声明但仅被 SetXxx 样式分支使用，从未被 `applyOverride` 中的 INSERT 路径使用

**文件：** `internal/governance/pii_propagator.go:118-185`
**问题：** 变量仅在 `sql.ErrNoRows` case 中设置为 `false`，在 default 中设置为 `true`。它的唯一读者是第 162 行的 `if !havePrior` 分支。阅读函数，死代码感觉是 `havePrior` 是冗余的——相同的信号在 `priorAuditSeq.Valid` 中用于审计发出决策。微小但值得重构。

**修复：** 删除该变量；直接使用 `if errors.Is(err, sql.ErrNoRows)` 分支。

### IN-05: `Reassign` 中注释掉的/"死"event_log 条目

**文件：** `internal/governance/workflow.go:478-481`
**问题：** 块注释"event_log entry (no hash-chain write; this is operational, not access-control)"后面没有代码。要么实现要么删除。迁移将 `governance.reviewer_reassigned` 添加到 event_log CHECK 约束，因此事件类型已被提供但从未发出——操作员对重新分配操作没有可观测性。

**修复：** 追加事件：
```go
_ = w.events.Append(ctx, event.Event{
    Type:         event.EventTypeGovernanceReviewerReassigned,
    ResourceType: "governance_review",
    ResourceID:   reviewID.String(),
    Payload: map[string]any{"actor": actor.String(), "old": oldPool, "new": newPool},
})
```

### IN-06: 通知规则模式内联解析；考虑带有 `Match(eventType)` 方法的 Pattern 类型

**文件：** `internal/notification/router.go:109-122`
**问题：** `matchPattern` 是一个自由函数，在每次调用时重新解析相同的模式。对于具有 N 条规则和 M 条事件的配置，这是 N*M 次解析。今天开销微不足道（M=10/秒，N=10），但容易清理。

**修复：** 在 `NewRouter` 时预编译为类型化 `Pattern`（精确/通配符/前缀）并存储在 `RuleConfig` 上。

---
_审查时间：2026-05-10T02:06:36Z_
_审查者：Claude (gsd-code-reviewer)_
_深度：quick_
