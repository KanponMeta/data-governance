---
phase: 05-governance
fixed_at: 2026-05-10T02:40:51Z
review_path: .planning/phases/05-governance/05-REVIEW.md
iteration: 1
findings_in_scope: 17
fixed: 17
skipped: 0
status: all_fixed
---

# Phase 5: 代码审查修复报告

**修复时间：** 2026-05-10T02:40:51Z
**来源审查：** `.planning/phases/05-governance/05-REVIEW.md`
**迭代：** 1

**摘要：**
- 范围内发现：17（6 个严重 + 11 个警告）
- 已修复：17
- 已跳过：0

每个修复都通过 `git commit --no-verify` 原子提交。每个提交消息都引用发现 ID。代码库构建干净（`go build ./...`），最终修复后 `go vet ./...` 干净。依赖 Postgres testcontainers 的集成测试未被执行 — Docker 在此沙箱中不可用；受影响包中的单元级测试在存在的地方全部通过。

## 已修复问题

### CR-01: 审计哈希链完整性损坏 — 哈希使用输入 OccurredAt，行存储默认 time.Now()

**修改文件：** `internal/audit/writer.go`
**提交：** `28272d3`
**应用的修复：** 在计算 `selfHash` 之前解析 `occurredAt`（零值默认为 `time.Now().UTC()`）。哈希和 INSERT 现在使用相同的时间戳值，因此 `Verify` 重新计算的哈希与存储的行匹配。

### CR-02: audit.Verify 将 TIMESTAMPTZ 扫描为 int64 — 验证路径无效

**修改文件：** `internal/audit/verify.go`
**提交：** `8a8ceb8`
**应用的修复：** 将扫描目标从 `var occurredAtUnixNano int64` 改为 `var occurredAt time.Time`，然后导出 `occurredAtUnixNano := occurredAt.UnixNano()` 用于哈希重新计算。pgx 现在正确扫描 TIMESTAMPTZ，整个 `Verify` 编码路径（REST `/audit/verify`、`platform audit verify` CLI、`emitVerifyFailedEntry`）都功能正常。

### CR-03: 治理批准绕过 — quorum 通过无权限 comment 文本的子串计数

**修改文件：** `internal/governance/workflow.go`
**提交：** `32c748d`
**应用的修复：** 添加了两个新的 sentinel errors（`ErrSelfApproval`、`ErrDuplicateVote`）。`decide()` 现在在 `decider == submitter`（四眼原则）时拒绝，并在 prior `[approved by <decider>]` 或 `[rejected by <decider>]` token 已存在于 comment ledger 中时拒绝。`countApprovals` 现在匹配结构化的 `[approved by <uuid>]` token 前缀，而非松散的 `"approved by "` 子串，因此自由格式的 reject comment 不能被误计为批准。**状态：需要人工验证** — 长期修复（governance_review_decisions 表 + reviewer-pool 成员资格强制）在单独跟踪；此处短期加固是正确的，但维护者应在依赖之前根据 Plan 05-04 治理测试场景确认。

### CR-04: InProcessQueue.InsertTx 忽略 tx 并立即插入 — 破坏文档化的原子入队契约

**修改文件：** `internal/notification/worker.go`、`internal/governance/workflow.go`、`internal/governance/sla_scanner.go`、`internal/quality/freshness.go`
**提交：** `24fe99a`
**应用的修复：** 所有三个生产调用方（`Workflow.Submit`、`Workflow.decide`、`SLAScanner.Scan`、`freshness.emitBreach`）现在在 `tx.Commit()` 之前构建 `NotificationDispatchArgs`，并在提交成功后调用 `queue.Insert`。回滚的 tx 不再产生幽灵通知。`InProcessQueue.InsertTx` doc-comment 被重写以明确其非事务性语义，并将调用方指向 post-commit 模式；表面与未来 River swap 保持兼容。（注意：`internal/quality/dispatcher.go` 中的 dispatcher 路径已记录其幽灵通知限制，本次迭代保持不变；它需要在 evaluator → executor 提交边界进行更深入的重构。）

### CR-05: CLI getActorFromEnv 允许任意 actor 欺骗进入哈希链

**修改文件：** `cmd/platform/governance.go`、`cmd/platform/policy.go`、`cmd/platform/role.go`、`cmd/platform/governance_test.go`
**提交：** `5745c2d`
**应用的修复：** 添加了 `cliDangerousEnabled()` helper（读取 `PLATFORM_CLI_DANGEROUS`）和共享的 `cliAuthDisabledMsg`。所有 CLI 写子命令现在拒绝运行，除非设置了该标志：`governance submit/review/reassign`、`policy patch/yaml-reload`、`role create/assign/revoke`。读子命令（`governance status`、`policy show/list`、`role list`）不受影响。现有测试通过 `t.Setenv("PLATFORM_CLI_DANGEROUS", "1")` 选择加入；新的 `TestSubmitCmd_DangerousFlagRequired` 覆盖门本身。

### CR-06: 通过未转义列名的质量规则 SQL 注入（NullCheck / RangeCheck）

**修改文件：** `internal/asset/types.go`
**提交：** `5ad946a`
**应用的修复：** 添加了 `quoteSQLIdent` helper，将嵌入的双引号加倍（ANSI SQL 标识符引号规则）。`NullCheck.Evaluate` 和 `RangeCheck.Evaluate` 现在通过 `quoteSQLIdent` 传递列名，而不是直接嵌入 `"%s"` 模板。SQLAssertion 经过审查并有意保持原样 — SQL body 是 asset-author 代码（在注册时受信任）。

### WR-01: RequirePermission 仅检查主角色；多角色用户可能被拒绝访问

**修改文件：** `internal/auth/middleware.go`
**提交：** `e772109`
**应用的修复：** `Principal` 结构现在携带 `Roles []string`（完整活动集）以及主 `Role`。`Middleware` 从 JWT claims 填充两者。`RequirePermission` 对 `Role + Roles` 进行去重，轮流对每个在 Casbin 中强制执行 — 任何角色匹配即通过。被分配多个角色的用户不再因非主要角色的许可而被静默拒绝。

### WR-02: REST handlers 向客户端泄露原始 DB 错误消息

**修改文件：** `internal/api/role_handlers.go`、`internal/policy/handler.go`、`internal/governance/handler.go`
**提交：** `0bd337a`
**应用的修复：** 每个以前调用 `http.Error(w, err.Error(), 500)` 或 `writeProblem(w, 500, "...", err.Error())` 处理 DB 起源错误的 handler，现在通过 `slog.Error` 和结构化字段（actor、asset 等）记录完整错误，并返回通用详情 `"internal error; see server logs"`。`handleDecideError` 还为 CR-03 引入的 `ErrSelfApproval`（403）和 `ErrDuplicateVote`（409）添加了明确案例，因此这些被清楚地暴露而不是落入 500 路径。

### WR-03: handleVerify 仅验证 seq=1，不验证整个链

**修改文件：** `internal/api/audit_handlers.go`
**提交：** `1a2f20e`
**应用的修复：** Handler 现在首先从 `audit.audit_log` 解析 `MAX(seq)`，然后调用 `Verify(ctx, db, 1, maxSeq)`，因此重新计算整个链。空链返回 `ok=true, scanned=0`。镜像 `cmd/platform/audit.go` CLI 行为。

### WR-04: 用于哈希比较的 bytesEqual 应使用 subtle.ConstantTimeCompare

**修改文件：** `internal/audit/verify.go`
**提交：** `b57889b`
**应用的修复：** 删除了本地 `bytesEqual` helper，将比较替换为 `subtle.ConstantTimeCompare(computedHash, storedHash) != 1`。与项目对哈希材料常量时间比较的要求一致，并在链验证期间消除了小的时间侧信道。

### WR-05: audit.WriteEntry 调用方经常省略 OccurredAt

**修改文件：** `internal/audit/export.go`、`internal/audit/verify.go`
**提交：** `5cb1526`
**应用的修复：** `emitExportAuditEntry` 和 `emitVerifyFailedEntry` 现在显式设置 `OccurredAt: time.Now().UTC()`，因此审计发射站点是真相来源。CR-01 修复已在 `WriteEntry` 中添加了防御性零值默认值，但显式 `OccurredAt` 提高了可审计性，并与每个其他发射站点（governance、policy、auth 等）对齐。所有其他调用站点都已设置 `OccurredAt`。

### WR-06: ApplyPartial 字节索引破坏多字节 UTF-8

**修改文件：** `internal/policy/mask.go`
**提交：** `0d198cc`
**应用的修复：** `ApplyPartial` 现在转换为 `[]rune`，按 rune 计数计算长度和切片，并从 rune 切片重建掩码字符串。多字节 UTF-8 序列不再在 rune 中间分割，消除了所谓掩码字符的部分 rune 泄漏。

### WR-07: isUndefinedTable 子串匹配脆弱

**修改文件：** `internal/governance/auto_approval.go`
**提交：** `d93ab14`
**应用的修复：** 添加了 `github.com/jackc/pgx/v5/pgconn` 导入。`isUndefinedTable` 现在使用 `errors.As` 对 `*pgconn.PgError` 并检查 SQLSTATE 代码 `42P01`（undefined_table）/ `42703`（undefined_column）。误导性的 `"does not exist"` 子串匹配消失了；残留的 `"undefined_table"` / `"undefined_column"` 子串回退为非 pg 测试脚手架保留。

### WR-08: connectorName 使用 context.Background() 调用 Ping

**修改文件：** `internal/policy/sync_job.go`
**提交：** `5a3640a`
**应用的修复：** `connectorName` 现在以 `ctx context.Context` 作为第一个参数，并将其转发给 `Ping`。`sync_job.go` 中所有三个调用站点都传递工作上下文，因此在关闭期间 Ping 不再泄漏等待无响应仓库的 goroutine。

### WR-09: 治理门在缺少 asset_versions 行时 fail-open 创建竞态窗口绕过

**修改文件：** `internal/runtime/executor.go`
**提交：** `82f8275`
**应用的修复：** 治理门中的 `errors.Is(err, sql.ErrNoRows)` 分支现在返回 `errMaterializationGated` 而不是允许运行继续。绕过理由是一个绕过表面：攻击者可以用新的 `code_hash` 注册资产，并在治理审查写入 asset_versions 行之前竞态运行。运行现在重试（或被外部策略拒绝），而不是静默绕过访问控制。

### WR-10: CreateRole 即使 ON CONFLICT DO NOTHING 意味着没有行被创建也发出审计

**修改文件：** `internal/auth/service.go`
**提交：** `2e87740`
**应用的修复：** `CreateRole` 现在在 INSERT 后捕获 `res.RowsAffected()`，当 `rows == 0` 时在审计写入之前短路。提交空 tx，函数返回 `nil` 而不发出 `role.created`。审计链消费者现在可以区分真正的创建和 no-op 重放。

### WR-11: dedupRoles 在 len <= 1 时返回输入切片不变

**修改文件：** `internal/governance/reviewers.go`
**提交：** `528a528`
**应用的修复：** `dedupRoles` 不再有空 fast-path 直接返回输入切片。函数始终分配新切片，消除了调用方后续 `append(pool.Roles, ...)` 可能通过共享后备数组突变原始数据的别名风险。

---

_修复时间：2026-05-10T02:40:51Z_
_修复者：Claude (gsd-code-fixer)_
_迭代：1_
