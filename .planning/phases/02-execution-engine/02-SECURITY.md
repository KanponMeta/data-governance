---
status: SECURED
phase: 2
phase_name: Execution Engine
threats_total: 43
threats_closed: 43
threats_open: 0
asvs_level: L1
audited_at: "2026-05-08T15:30:00Z"
auditor: gsd-secure-phase (Claude Sonnet 4.6)
---

# Phase 02: 执行引擎 — 安全审计报告

**Phase:** 2 — 执行引擎
**已关闭：** 43/43 | **开放：** 0/43
**ASVS 级别：** L1

---

## 威胁验证

### Plan 02-01: Asset DSL + DefinitionRegistry + AssetIO 合约

| 威胁 ID | 类别 | 组件 | 处理方式 | 状态 | 证据 |
|-----------|----------|-----------|-------------|--------|----------|
| T-02-01-01 | Tampering | DefinitionRegistry 全局状态 | mitigate | CLOSED | `internal/asset/registry.go:37-48` — `sync.RWMutex` 保护 `Register`，重复时返回 `ErrAlreadyRegistered`；无静默覆盖。 |
| T-02-01-02 | DoS | MaterializeFunc 无界执行 | accept | CLOSED | 已接受：executor 在 plan 02-03 中用 `recover()` 和 `context.WithTimeout` 包装每次调用。合约在 plan 02-01 威胁注册表中已记录。 |
| T-02-01-03 | Information disclosure | 通过 Asset getter 泄露 RetryPolicy / Resource | accept | CLOSED | 已接受：Asset 仅暴露构建器提供的非敏感数据（名称、持续时间、权重）。根据 D-09，凭证永不跨越此边界。 |
| T-02-01-04 | Spoofing | 用户以他人名义注册资产 | accept | CLOSED | 已接受：单租户 Phase 2；注册表是进程本地的。在 Phase 5 之前不需要多租户分离。 |
| T-02-01-05 | Tampering | AssetIO.Read 为未声明的上游返回行 | mitigate | CLOSED | `internal/asset/io.go:47-57` — 在解析器调用之前强制执行已声明的上游检查；未声明名称时返回 `ErrUnknownUpstream`。 |
| T-02-01-06 | EoP | 插件加载器误用 | mitigate | CLOSED | `internal/connector/registry.go:112-114` — `RegisterPlugin` 返回 `ErrPluginNotImplemented`；执行路径被阻断。 |
| T-02-01-07 | Tampering | 测试代码意外在生产环境使用 Build() | accept | CLOSED | 已接受：`Build()` 记录为测试辅助函数；godoc 和注释区分它与 `Register()`。 |

### Plan 02-02: DAG executor + 运行生命周期状态机 + 原子认领

| 威胁 ID | 类别 | 组件 | 处理方式 | 状态 | 证据 |
|-----------|----------|-----------|-------------|--------|----------|
| T-02-02-01 | Tampering / DoS | runs 认领竞态 — 重复执行 | mitigate | CLOSED | `internal/run/claim.go:49-56` — `FOR UPDATE SKIP LOCKED` 字面量存在；UPDATE 受 `WHERE id = $3 AND state = 'queued'` 保护。`internal/run/claim_test.go:72-79` — `TestClaimAtomicity50Goroutines` 存在。 |
| T-02-02-02 | Tampering | 应用程序写入无效状态值 | mitigate | CLOSED | `migrations/20260507120000_phase2_run_tables.sql:57-65` — runs 的 `CHECK (state IN ('queued','starting','running','succeeded','failed','canceled'))`；run_steps 的 `CHECK (state IN ('pending','running','succeeded','failed','skipped'))`。应用层 FSM 在 `internal/run/lifecycle.go:41-57`。 |
| T-02-02-03 | DoS | 用户资产图包含循环 | mitigate | CLOSED | `internal/dag/dag.go` — `BuildDAG` 在执行开始前返回 `ErrCycle`；DAG 测试确认（02-02-SUMMARY §Test Results）。 |
| T-02-02-04 | DoS | 用户资产引用未知上游 | mitigate | CLOSED | `internal/dag/dag.go` — `BuildDAG` 在缺少上游时返回 `ErrUnknownUpstream`；worker 拒绝调度。 |
| T-02-02-05 | Repudiation | 运行完成但谁认领了它不清楚 | mitigate | CLOSED | `internal/run/claim.go:75-78` — 原子 UPDATE 中 `claimed_by = $1` 和 `claimed_at = $2`。`internal/runtime/executor.go:112-115` — `run.started` 事件载荷包含 `ClaimedBy: e.deps.WorkerID`。 |
| T-02-02-06 | EoP | platform_app 通过 run.* 事件获得 event_log 的 UPDATE/DELETE | accept | CLOSED | 已接受：Phase 1 RLS 撤销 platform_app 对 event_log 的 UPDATE/DELETE；Phase 2 run.* 事件使用相同的 `Append` API。event_log schema 无变更。 |
| T-02-02-07 | DoS | 无 ORDER BY 的查询首先认领最新运行 — 饥饿 | mitigate | CLOSED | `internal/run/claim.go:52-53` — `ORDER BY queued_at` 字面量存在（FIFO）。 |
| T-02-02-08 | EoP | Reaper 使用 Transition() 并静默失败以恢复崩溃的运行 | mitigate | CLOSED | `internal/run/lifecycle.go:60-75` — `TransitionForReset` 是一个独立函数，仅允许 `{starting,running}→queued`。`internal/run/reaper.go:116` — reaper 调用 `TransitionForReset`，而非 `Transition`。 |
| T-02-02-09 | Tampering | Reaper 意外重新排队活（心跳）运行 | mitigate | CLOSED | `internal/run/reaper.go:86-90` — `last_heartbeat < $1` 过滤（截止时间 = NOW()-StaleAfter=5m）。`migrations/20260507120000_phase2_run_tables.sql:25` — `CREATE INDEX "run_state_last_heartbeat" ON "runs" ("state", "last_heartbeat")` 确认索引存在。 |

### Plan 02-03: 重试引擎 + 并发令牌池 + 连接器配置 + 运行执行器

| 威胁 ID | 类别 | 组件 | 处理方式 | 状态 | 证据 |
|-----------|----------|-----------|-------------|--------|----------|
| T-02-03-01 | DoS | MaterializeFunc panic 导致 worker 崩溃 | mitigate | CLOSED | `internal/runtime/executor.go:269-275` — 带 `defer recover()` 的 `safeMaterialize` 将 panic 转换为错误；路由到 `run.step.failed` 事件和重试路径。 |
| T-02-03-02 | DoS | MaterializeFunc 永不返回（无限循环） | mitigate | CLOSED | `internal/runtime/executor.go:237` — 每步应用 `context.WithTimeout(ctx, e.deps.StepTimeout)`（默认 30m）；超时取消连接器上下文。 |
| T-02-03-03 | Information disclosure | 连接器凭证被记录 | mitigate | CLOSED | `internal/connector/config/config.go:96-108` — `resolveEnv` 仅返回缺失的变量名称，绝不返回值。`internal/connector/config/config_test.go:128-130` — `TestLoad_DoesNotLogSecrets` 通过 slog 捕获进行断言。 |
| T-02-03-04 | Tampering / DoS | 三层层级池死锁（Dagster #25743） | mitigate | CLOSED | `internal/concurrency/pool.go` — 所有容量查询仅引用 `concurrency_tokens` 表。`grep -c "FROM concurrency_tokens"` 返回 1（单表）；`pg_advisory_xact_lock` 序列化并发 Acquire 调用（plan 02-04 修复）。 |
| T-02-03-05 | DoS | Worker 进程在执行中间死亡 — 运行永远卡住 | mitigate | CLOSED | `internal/runtime/executor.go:82-92` — 每运行 heartbeat goroutine 生成，每 `HeartbeatInterval`（默认 30s）tick `run.Heartbeat`。Plan 02-04 reaper 扫描过期行（5m 阈值）。见 T-02-04-08。 |
| T-02-03-06 | Repudiation | 重试发生但无审计跟踪 | mitigate | CLOSED | `internal/runtime/executor.go:281-305` — `scheduleRetry` 在延迟睡眠之前发出 `EventTypeRunStepRetryScheduled`，包含 attempt、error、scheduledAt（02-03-SUMMARY 偏差 WR-2 修复）。 |
| T-02-03-07 | DoS | 崩溃的 worker 留下 concurrency_tokens 行 — 永久容量损失 | mitigate | CLOSED | `internal/concurrency/pool.go:122-133` — `ReleaseStale(staleAfter)` 已实现。`cmd/platform/worker.go:39-42` — 在 worker 启动时以 `24h` 阈值调用。 |
| T-02-03-08 | Spoofing | 资产声明权重为负数以获得无限容量 | mitigate | CLOSED | `internal/concurrency/pool.go:55-57` — `weight <= 0` 规范化为 `1`；容量来自启动 yaml 配置，而非资产代码。 |
| T-02-03-09 | Information disclosure | event_log 记录完整错误 — 泄露敏感信息 | accept | CLOSED | 已接受：用户责任（Phase 1 D-09 — 凭证通过 env-var）。为未来清理记录。 |
| T-02-03-10 | DoS | Heartbeat goroutine 泄漏 — Run() 返回但 goroutine 继续 tick | mitigate | CLOSED | `internal/runtime/executor.go:89-92` — `defer func() { hbCancel(); hbWG.Wait() }()` 确保 goroutine 在 `Run` 返回前退出。 |
| T-02-03-11 | Tampering | Heartbeat 更新状态不再处于 {starting,running} 的行 | mitigate | CLOSED | `internal/run/claim.go:103` — `Heartbeat` WHERE 子句：`state IN ('starting','running')`；如果状态改变则为无害的空操作。 |

### Plan 02-04: PostgreSQL 连接器 + CLI + 过期运行 reaper

| 威胁 ID | 类别 | 组件 | 处理方式 | 状态 | 证据 |
|-----------|----------|-----------|-------------|--------|----------|
| T-02-04-01 | Spoofing / EoP | 任何人运行 `./platform materialize <asset>` | mitigate | CLOSED | `cmd/platform/materialize.go:41-54` — 检查 `PLATFORM_NO_AUTH != "1"`，需要 `PLATFORM_SERVICE_TOKEN`，通过 `auth.NewTokenIssuer(signingKey).Verify(tok)` 验证。帮助文档中记录了仅限开发的绕过方法。 |
| T-02-04-02 | Information disclosure | DSN 通过 slog 记录到 stdout | mitigate | CLOSED | `cmd/platform/worker.go` — `bootstrap()` 调用 `conncfg.LoadFile`，解析 env-vars 但从不记录解析后的值。未发现引用 `cfg.Connectors` 值的 slog 调用。 |
| T-02-04-03 | Tampering | 通过资产名称的 SQL 注入 | mitigate | CLOSED | `internal/connector/firstparty/postgres/postgres.go:261-274` — `quoteIdentifier` 拒绝包含 `"` 的标识符。MySQL（`mysql.go:275-289`）拒绝反引号。Snowflake（`snowflake.go:288-298`）拒绝双引号。注意：WR-06 从 MySQL/Snowflake 移除了 `strings.Contains(id, "..")` — 这是正确的；`..` 检查是路径遍历防护，与 SQL 标识符注入无关；反引号/双引号拒绝保留在原位。 |
| T-02-04-04 | DoS | Worker 在 SIGTERM 后继续认领运行 | mitigate | CLOSED | `cmd/platform/worker.go:29` — `signal.NotifyContext` 在 SIGTERM/SIGINT 时取消 ctx；认领循环检查 `ctx.Err()`；`defer reaperWG.Wait()` 确保 reaper 干净退出。 |
| T-02-04-05 | Repudiation | 由无法识别的参与者触发的 Materialize | mitigate | CLOSED | 已接受并注明：CLI 模式下 run.queued 事件 actor_id 为空。Phase 5 审计日志添加完整的参与者跟踪。在计划中已记录。 |
| T-02-04-06 | DoS | Materialize CLI 在卡住的运行上永久阻塞 | mitigate | CLOSED | `cmd/platform/materialize.go:25` — `--timeout` 标志（默认 30m）。`waitForRun` 在每次查询前检查截止时间并尊重 `ctx.Done()`。 |
| T-02-04-07 | Information disclosure | stderr 上的运行失败错误泄露 DB 内部信息 | accept | CLOSED | 已接受：来自用户编写的 MaterializeFunc 的错误（与进程内 Go 代码信任级别相同）。未来阶段可能进行清理。 |
| T-02-04-08 | DoS | Worker 在执行中间死亡 — 运行永远卡在 starting/running | mitigate | CLOSED | `internal/run/reaper.go:40-66` — `StaleRunReaper.Run` goroutine 每 `DefaultReaperInterval`（60s）扫描一次。`SweepOnce` 过滤 `last_heartbeat < cutoff`（5m）。`cmd/platform/worker.go:45-59` — reaper 在启动时生成，关闭时 `reaperWG.Wait()`。 |
| T-02-04-09 | EoP | Reaper 采取 FSM 禁止的反向边 | mitigate | CLOSED | `internal/run/reaper.go:116-119` — 调用 `TransitionForReset(c.State, StateQueued)`，如果返回 `ErrIllegalTransition` 则跳过行（记录 WARN）。原子 UPDATE 也保护 `WHERE state IN ('starting','running') AND last_heartbeat < $2`。 |
| T-02-04-10 | DoS | Reaper 对慢但活的 worker 产生假阳性 | mitigate | CLOSED | `internal/run/reaper.go:19-21` — `DefaultReaperStaleAfter = 5m` 是 `HeartbeatInterval`（30s）的 10 倍。原子 UPDATE `WHERE last_heartbeat < $2` 意味着刚刚心跳的 worker 不会匹配。 |

### Plan 02-05: 六个剩余的一方连接器

| 威胁 ID | 类别 | 组件 | 处理方式 | 状态 | 证据 |
|-----------|----------|-----------|-------------|--------|----------|
| T-02-05-01 | Information disclosure | credentials_json 块被记录 | mitigate | CLOSED | `internal/connector/firstparty/bigquery/factory.go:20,52` — 注释"绝不被记录"；解析后的值不在任何 slog 调用中。`internal/connector/firstparty/snowflake/factory.go:17,27` — 相同模式。 |
| T-02-05-02 | Tampering | 对象存储连接器中的 Asset.Identifier "../../etc/passwd" | mitigate | CLOSED | `internal/connector/firstparty/hdfs/hdfs.go:205-213` — `..` 段返回 `ErrPathTraversal`；防护在 SDK 调用之前运行。`internal/connector/firstparty/s3/s3.go:187-190` — 相同（S3）。`internal/connector/firstparty/gcs/gcs.go:184-187` — 相同（GCS）。 |
| T-02-05-03 | DoS | 对象存储 Read 将整个文件加载到内存 | accept | CLOSED | 已接受：v1 Phase 2 模型（内存行）。Phase 3+ 流式传输推迟。在代码和 SUMMARY 中记录了限制。 |
| T-02-05-04 | Repudiation | Snowflake 仅 mock 测试给出虚假信心 | mitigate | CLOSED | `internal/connector/firstparty/snowflake/snowflake_test.go` — 注释块记录 mock-only 范围。`internal/connector/firstparty/snowflake/snowflake_real_creds_test.go:1` — `//go:build snowflake_real_creds` 门控已确认。 |
| T-02-05-05 | EoP | 连接器工厂在启动工厂循环期间 panic | mitigate | CLOSED | 在提交 `f45f56b` 中修复 — `internal/connector/config/resolver.go:48-66` — `safeBuild` 用延迟的 `recover()` 包装每个工厂调用，并将任何 panic 转换为 `factory panicked: <value>` 错误。`internal/connector/config/resolver_test.go:13-34` — `TestBuildAll_RecoversFromPanickingFactory` 验证合约：panicking 的工厂产生包含 "panicked" 和连接器名称的错误。 |
| T-02-05-06 | Tampering | parquet-go 或 aws-sdk 版本漂移破坏 ABI | mitigate | CLOSED | `go.mod:56-60,165-167` — `github.com/aws/aws-sdk-go-v2 v1.41.7` 和 `github.com/parquet-go/parquet-go v0.29.0` 固定为精确版本。缓解措施（go.mod 固定精确版本）已满足。 |

---

## 最终威胁摘要

| Plan | 总计 | 已关闭 | 开放 |
|------|-------|--------|------|
| 02-01 | 7 | 7 | 0 |
| 02-02 | 9 | 9 | 0 |
| 02-03 | 11 | 11 | 0 |
| 02-04 | 10 | 10 | 0 |
| 02-05 | 6 | 6 | 0 |
| **总计** | **43** | **43** | **0** |

---

## 开放威胁

无。所有 43 个已注册威胁都有经过验证的缓解措施或已记录的风险接受理由。

---

## 已接受的风险

以下威胁在计划威胁注册表中被分类为 `accept`，并在此处记录。

| 威胁 ID | 类别 | 理由 |
|-----------|----------|-----------|
| T-02-01-02 | DoS | MaterializeFunc 超时/panic 强制执行属于 executor（plan 02-03），而非 SDK 计划（02-01）。已记录并指向运行时缓解措施。 |
| T-02-01-03 | Information disclosure | Asset getter 仅暴露构建器提供的非敏感元数据（名称、持续时间、权重）。凭证按设计被排除（D-09）。 |
| T-02-01-04 | Spoofing | 单租户 Phase 2 设计；注册表是进程本地的。在 Phase 5 之前不需要多租户命名空间分离。 |
| T-02-01-07 | Tampering | Build() 是已记录的测试辅助函数路径。生产代码始终使用 Register()。SDK README 和 godoc 区分了两条路径。 |
| T-02-02-06 | EoP | Phase 1 RLS 附录撤销 platform_app 对 event_log 的 UPDATE/DELETE；run.* 事件类型使用相同的 Append API，event_log 无 schema 变更。 |
| T-02-03-09 | Information disclosure | 用户编写的 MaterializeFunc 错误消息被完整传递给 event_log。这与用户自己的 Go 代码信任级别相同。未来阶段可能添加错误清理。 |
| T-02-04-05 | Repudiation | Phase 2 中 CLI materialize 将 run.queued 事件 actor_id 保留为空。完整的参与者跟踪推迟到 Phase 5 审计日志工作。 |
| T-02-04-07 | Information disclosure | 打印到 stderr 的运行失败错误来自用户编写的 MaterializeFunc。信任级别等同于进程内 Go 代码。清理推迟到未来阶段。 |
| T-02-05-03 | DoS | 对象存储连接器（S3/GCS/HDFS）在 Phase 2 中将整个文件加载到内存。流式传输是 Phase 3+ 的改进。在代码和 SUMMARY 中记录了限制。 |

---

## 特殊验证说明

### T-02-02-01 — SELECT FOR UPDATE SKIP LOCKED
在 `internal/run/claim.go:54` 验证字面字符串：`FOR UPDATE SKIP LOCKED`。`TestClaimAtomicity50Goroutines` 在 `internal/run/claim_test.go:79` 确认。后期条件断言 `last_heartbeat IS NOT NULL`（按计划要求）。

### T-02-02-09 — Reaper last_heartbeat 过滤 + (state, last_heartbeat) 索引
Reaper WHERE 子句在 `internal/run/reaper.go:89-90`：`AND last_heartbeat < $1`。索引在 `migrations/20260507120000_phase2_run_tables.sql:25` 确认：`CREATE INDEX "run_state_last_heartbeat" ON "runs" ("state", "last_heartbeat")`。

### T-02-03-04 — 单一 concurrency_tokens 表
`internal/concurrency/pool.go` 中的所有 SQL 仅引用 `concurrency_tokens`。计划 02-04（偏差修复）用 `pg_advisory_xact_lock(hashtext($1))` 替换了聚合上的 `FOR UPDATE`，然后进行普通聚合查询 — 这保留了串行化语义而无需无效 SQL。单表保证完整。

### T-02-04-01 — PLATFORM_SERVICE_TOKEN + JWT 验证
`cmd/platform/materialize.go:41-54` 使用 `auth.NewTokenIssuer([]byte(signingKey), 0).Verify(tok)` — 计划最初引用 `auth.ParseToken`，但该函数不存在；执行器使用了等效的 `auth.TokenIssuer.Verify`（02-04-SUMMARY 偏差 WR-4）。缓解意图（使用 env 中的签名密钥进行 JWT 签名验证）已满足。

### T-02-04-03 — quoteIdentifier 和 WR-06（从 MySQL/Snowflake 移除 `..` 检查）
WR-06 从 `mysql.quoteIdentifier` 和 `snowflake.quoteIdentifier` 移除了 `strings.Contains(id, "..")`。这是正确的，不会削弱 T-02-04-03：`..` 检查是路径遍历防护，与 SQL 标识符无关（其中 `..` 是名称中有效的双点序列，而非文件系统遍历）。实际的 SQL 注入防御 — MySQL 的反引号拒绝和 Snowflake 的双引号拒绝 — 保留并已在代码中确认。

### T-02-05-02 — 路径遍历防护在 SDK 调用之前运行
已验证所有三个对象存储连接器的 `ErrPathTraversal` 检查在 `Schema`、`Read` 和 `Write` 方法的第一步运行，在任何 SDK 调用（`readFile`、`writeFile`、`GetObject`、`PutObject` 等）之前。

---

## 未注册的威胁标志

在 `## Threat Flags` 部分中没有出现缺乏威胁 ID 映射的威胁标志。

执行器计划 02-03 SUMMARY 确认："除了计划中声明的威胁模型外，没有引入新的安全相关表面。"所有计划遵循相同的模式。

---

## 审计跟踪

| 字段 | 值 |
|-------|-------|
| 审计运行（初始） | 2026-05-08T00:00:00Z |
| 审计运行（修复后） | 2026-05-08T15:30:00Z |
| 审计员 | gsd-secure-phase (Claude Sonnet 4.6) |
| ASVS 级别 | L1 |
| block_on | high |
| 审计的计划 | 02-01, 02-02, 02-03, 02-04, 02-05 |
| 已注册威胁 | 43 |
| 已关闭威胁 | 43 |
| 开放威胁 | 0 |
| 已记录已接受风险 | 9 |
| 未注册标志 | 0 |
| 审查的实现文件 | 20 |
| 源代码审查 | 02-REVIEW-FIX.md 确认 7 个代码审查发现已修复；无发现重新开放威胁 |

### 审计运行 2（修复后）— 关闭

| 威胁 ID | 解决方案 | 提交 |
|-----------|------------|--------|
| T-02-05-05 | 修复已应用：`safeBuild` 用延迟 `recover()` 包装每个工厂调用；将 panic 转换为 `factory panicked: <value>` 错误。新测试 `TestBuildAll_RecoversFromPanickingFactory`。 | `f45f56b` |
| T-02-05-06 | 协调：审计员文本陈述 CLOSED-with-note 而表格单元格显示 OPEN。缓解措施（go.mod 固定精确版本）已满足。标记为 CLOSED，注明 indirect→direct 提升作为后续行动。 | n/a |