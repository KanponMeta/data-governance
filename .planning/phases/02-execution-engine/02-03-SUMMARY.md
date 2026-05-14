---
phase: 2
plan: "04"
subsystem: execution-engine
title: "PostgreSQL connector + worker/materialize CLI + stale-run reaper + e2e acceptance test"
one_liner: "Real PostgreSQL connector via pgxpool, worker/materialize CLI subcommands with SIGTERM-safe reaper goroutine, and full e2e testcontainers acceptance test closing ROADMAP criteria 1, 2, 4 and D-14 crash recovery"
tags:
  - postgres
  - connector
  - cli
  - reaper
  - e2e-test
  - crash-recovery
requirements: [ORCH-10, CONN-01]
depends_on: ["02-01", "02-02", "02-03"]
dependency_graph:
  requires:
    - "02-01: asset SDK (Builder, AssetIO, DefinitionRegistry)"
    - "02-02: run lifecycle FSM, ClaimNext, Heartbeat, TransitionForReset, last_heartbeat column"
    - "02-03: runtime.Executor, concurrency.Pool, retry.Policy, connector/config.FactoryRegistry"
  provides:
    - "internal/connector/firstparty/postgres: reference PostgreSQL connector (pgxpool)"
    - "internal/run/reaper.go: StaleRunReaper goroutine (D-14 Option B)"
    - "cmd/platform/worker.go: worker subcommand + reaper spawn"
    - "cmd/platform/materialize.go: materialize subcommand (sync + detach)"
    - "cmd/platform/factories.go: FactoryRegistry initializer for first-party connectors"
    - "testdata/integration/: SDK usage example + config template"
    - "test/integration/e2e_postgres_test.go: full Phase 2 e2e acceptance test"
  affects:
    - "cmd/platform/main.go: added worker + materialize dispatch cases"
    - "internal/concurrency/pool.go: fixed FOR UPDATE on aggregate (pg_advisory_xact_lock)"
    - "internal/asset/registry.go: exported ResetForTest() for external test packages"
tech_stack:
  added:
    - "github.com/testcontainers/testcontainers-go v0.42.0: ephemeral Postgres for connector and e2e tests"
    - "github.com/testcontainers/testcontainers-go/modules/postgres v0.42.0: Postgres module with BasicWaitStrategies"
  patterns:
    - "pgxpool.NewWithConfig for pool lifecycle (D-08 singleton)"
    - "pg_advisory_xact_lock(hashtext(tag)) for concurrency token serialization (replaces broken FOR UPDATE on aggregate)"
    - "sync.WaitGroup + signal.NotifyContext for graceful SIGTERM shutdown in worker"
    - "In-process e2e test (no binary subprocess): build deps directly, queue run via raw SQL, run executor.Run inline"
    - "recordingWriter / eventRecorder in-memory event.Writer for unit test isolation"
key_files:
  created:
    - "internal/connector/firstparty/postgres/postgres.go"
    - "internal/connector/firstparty/postgres/factory.go"
    - "internal/connector/firstparty/postgres/postgres_test.go"
    - "internal/run/reaper.go"
    - "internal/run/reaper_test.go"
    - "cmd/platform/factories.go"
    - "cmd/platform/worker.go"
    - "cmd/platform/materialize.go"
    - "testdata/integration/example_assets.go"
    - "testdata/integration/config.yaml"
    - "test/integration/e2e_postgres_test.go"
  modified:
    - "cmd/platform/main.go (added worker + materialize dispatch)"
    - "internal/concurrency/pool.go (fixed FOR UPDATE on aggregate)"
    - "internal/asset/registry.go (exported ResetForTest)"
    - "go.mod / go.sum (testcontainers dependency)"
decisions:
  - "pg_advisory_xact_lock(hashtext(tag)) replaces FOR UPDATE on aggregate in Pool.Acquire — PostgreSQL does not allow FOR UPDATE with aggregate functions (SQLSTATE 0A000)"
  - "D-14 Option B: StaleRunReaper over River.max_attempts — simpler for single-binary Phase 2; River wiring deferred to Phase 3 when out-of-process job dispatch is required"
  - "In-process e2e test approach — avoids binary compilation in test, faster CI, equally validates the runtime behavior"
  - "auth.TokenIssuer.Verify used in materialize (no standalone ParseToken); signing key from PLATFORM_JWT_SIGNING_KEY env var"
  - "asset.ResetForTest() exported to allow external test packages to reset the global registry between tests"
  - "RetryPolicy.Max=2 for failing asset in integration test — ShouldRetry uses attempt<Max so Max:2 → 2 attempts (1 retry)"
metrics:
  duration: "~55 minutes"
  completed_at: "2026-05-08"
  tasks_completed: 3
  tasks_total: 3
  files_created: 11
  files_modified: 4
---

# Phase 2 Plan 04: PostgreSQL 连接器 + worker/materialize CLI + stale-run reaper + e2e acceptance test 概要

## 已构建内容

### 任务 4.1 — PostgreSQL 连接器 (commit 77e3187)

**internal/connector/firstparty/postgres/postgres.go** — 参考一等连接器 (D-12)。

- `New(ctx, dsn)` 打开带有启动时连接验证的 `pgxpool.Pool` (快速失败)
- `APIVersion()` 返回 `connector.APIVersion` ("v1.0.0") — 编译时断言 `var _ connector.Connector = (*Postgres)(nil)`
- `Ping(ctx, req)` 返回 `ConnectorName="postgres"`, `Capabilities.SupportsSchemaCapture=true`
- `Schema(ctx, req)` 查询 `information_schema.columns` 按 `ordinal_position` 排序; 分割 `schema.table` 标识符
- `Read(ctx, req)` 执行 `SELECT * FROM "schema"."table" [LIMIT n]`; ctx 取消返回 `context.Canceled` (PITFALLS §10)
- `Write(ctx, req)` 构建参数化 `INSERT ... VALUES ($1,$2,...),($N,...)` — 无字符串连接值 (T-02-04-03)
- `Close()` 排出池; 幂等; 后续调用返回 `ErrClosed`
- `quoteIdentifier()` 拒绝包含 `"` 的标识符 (资产名称的 SQL 注入防御)

**internal/connector/firstparty/postgres/factory.go** — `Factory(params) (connector.Connector, error)` 在 `dsn` 缺失时返回 `ErrMissingDSN`; 用 10s 超时构造。

**测试** (11 tests, testcontainers postgres:16-alpine with `BasicWaitStrategies()`):
TestCompileTimeAssertion, TestPing, TestSchemaRoundTrip, TestWriteThenRead, TestReadCtxCancel, TestFactory_MissingDSN, TestFactory_Builds, TestClose, TestClose_Idempotent, TestQuoteIdentifier_RejectQuotes, TestAPIVersion。

### 任务 4.2 — Stale-run reaper (commit 9da434d)

**internal/run/reaper.go** — `StaleRunReaper` 实现 D-14 Option B 崩溃恢复。

**常量:**
- `DefaultReaperStaleAfter = 5 * time.Minute` (30s heartbeat tick 的 10x — 10x 安全边际防止虚假重新排队)
- `DefaultReaperInterval = 60 * time.Second` (组合恢复上限: 约 6 分钟)

**`SweepOnce(ctx) (int64, error)` 语义:**
1. SELECT 候选: `WHERE state IN ('starting','running') AND last_heartbeat < $cutoff`
2. 对于每个候选: `TransitionForReset(from, StateQueued)` 验证 FSM 后向边
3. 原子 UPDATE: `WHERE id=$1 AND state IN (...) AND last_heartbeat < $2` — 如果 0 行受影响,另一个 reaper 或 live heartbeat 赢得了竞争 (幂等)
4. 成功时: 发出 `EventTypeRunCanceled` 与 `Reason="reaper: worker heartbeat lost"`, 增加 reclaimed 计数

**`Run(ctx)` goroutine:** 每 `Interval` tick 一次,直到 ctx 取消。

**测试** (7 tests, uses DATABASE_URL or skips):
TestReaperStaleRunReclaimed, TestReaperStartingState, TestReaperTerminalStateIgnored, TestReaperEventEmitted, TestReaperConcurrentSweepIdempotent, TestReaperGoroutineExitsOnCancel, TestSweepOnce_ReturnsCount。

### 任务 4.3 — CLI 子命令 + 工厂 + e2e 集成测试 (commit 8b4ce6d)

**cmd/platform/factories.go:** `newFactoryRegistry()` 预注册 `"postgres"` factory; plan 02-05 添加更多的一等入口点。

**cmd/platform/worker.go:**
- `runWorker()` → `signal.NotifyContext` → `bootstrap()` → `pool.ReleaseStale(24h)` → 生成 `StaleRunReaper` goroutine (由 `reaperWG` 跟踪) → 声明循环 (500ms 空闲轮询) → `executor.Run` 分发
- `bootstrap(ctx)` 从 `PLATFORM_CONFIG` 加载配置,从 `DATABASE_URL` 打开存储,通过 `FactoryRegistry.BuildAll` 构建连接器注册表,构建 `concurrency.Pool`,构造 `runtime.Executor`
- SIGTERM 关闭: ctx 取消停止声明循环; `defer reaperWG.Wait()` 确保 reaper 在函数返回前退出

**cmd/platform/materialize.go:**
- `runMaterialize(args)` → 标志解析 (`--detach`, `--timeout=30m`) → 认证门禁 → `bootstrap()` → 资产查找 → INSERT run row → 发出 `run.queued` 事件 → detach 或 `waitForRun` 轮询 (500ms tick, 终端状态检查)
- 认证: `PLATFORM_NO_AUTH=1` 绕过; 否则需要 `PLATFORM_SERVICE_TOKEN` 通过 `PLATFORM_JWT_SIGNING_KEY` 的 `auth.TokenIssuer.Verify` 验证
- 失败路径: `os.Exit(1)` 通过 `runMaterialize` 返回非 nil 错误 (通过 `main.go` 分发)

**test/integration/e2e_postgres_test.go** (5 tests, testcontainers, skip-on-no-docker):

| 测试 | 关闭 |
|------|--------|
| TestE2E_PostgresMaterialize | Users_raw→users_clean DAG 执行; 数据落在 users_clean; succeeded 事件; 2 个 step 事件每个 |
| TestE2E_PostgresMaterialize_Failure | step.failed×2 + retry_scheduled×1 + run.failed×1 在 event_log (接受标准 2) |
| TestE2E_TopologicalOrder | users_raw step.started occurred_at < users_clean step.started (接受标准 1) |
| TestE2E_StaleRunReaperRecovery | 过时的 'running' 行 → 在 5s 内变为 queued; run.canceled 事件记录 (D-14 + T-02-04-08) |
| TestE2E_DetachMode | 排队的运行立即插入,返回时 state='queued' |

## D-14 实现说明

D-14 原本描述 "River 处理基础设施故障"。Phase 2 通过 **StaleRunReaper** (Option B) 实现相同的恢复保证:

- **为什么不使用 River.max_attempts:** Phase 2 worker 是单个 Go 二进制文件。将 River 接线为辅助调度器会引入新生命周期 (`river.Client.Start`, `worker.Register`) 和并行持久化路径。reaper 只需要一个 goroutine 和一个 SQL SELECT — 绝对更简单。
- **功能等价:** River 会在调度层重试失败的作业; reaper 在状态层重置卡住的运行 ("重置为 queued,让健康的 worker 重新声明")。对于 Phase 2 的单租户轮询模型,结果是相同的:崩溃的 worker 的运行在约 6 分钟内恢复。
- **推迟,不是放弃:** 当 Phase 3+ 添加进程外连接器子进程时,River 成为硬需求 (跨节点的作业调度)。届时,`factories.go` 是集成 River 的入口点; reaper 继续并行运行。

## Plan 02-05 的钩子

`cmd/platform/factories.go::newFactoryRegistry()` 是一等连接器的单一注册点。Plan 02-05 通过调用 `r.RegisterFactory("mysql", mysql.Factory)` 等添加 `mysql`, `bigquery`, `snowflake`, `s3`, `gcs`, `hdfs`。

## 与计划的偏差

### 自动修复的问题

**1. [规则 1 - Bug] 修复 concurrency.Pool.Acquire 中的 FOR UPDATE 聚合**
- **发现于:** 任务 4.3 (TestE2E_PostgresMaterialize)
- **问题:** `SELECT COALESCE(SUM(weight), 0)::int ... FOR UPDATE` 在 PostgreSQL 中是无效 SQL (`SQLSTATE 0A000: FOR UPDATE 不允许与聚合函数一起使用`)
- **修复:** 用 `SELECT pg_advisory_xact_lock(hashtext($1))` 替换,然后进行聚合查询。事务范围的咨询锁序列化相同 resource_tag 的并发 Acquire 调用,而不触及聚合。
- **修改的文件:** `internal/concurrency/pool.go`
- **提交:** 8b4ce6d

**2. [规则 2 - 缺失关键功能] 导出 asset.ResetForTest()**
- **发现于:** 任务 4.3 (e2e 测试需要重置测试之间的全局注册表)
- **问题:** `resetForTest()` 是包私有的; 外部测试包 (test/integration) 无法调用它
- **修复:** 添加带警告注释的导出 `ResetForTest()` 包装器,说明非并发使用
- **修改的文件:** `internal/asset/registry.go`
- **提交:** 8b4ce6d

**3. [规则 1 - Bug] RetryPolicy.Max 语义 — RegisterFailingAsset 使用 Max:2 表示 2 次尝试**
- **发现于:** 任务 4.3 (TestE2E_PostgresMaterialize_Failure 显示 step.failed×1 而不是 ×2)
- **问题:** `ShouldRetry(attempt, policy)` 返回 `attempt < Max`。对于 Max:1, attempt=1 → `1 < 1 = false` → 无重试 (1 次尝试总计)。Max:2 → 2 次尝试 (1 次重试)。
- **修复:** 将 `RegisterFailingAsset` 改为使用 `Max: 2` 以获得预期的重试序列
- **修改的文件:** `testdata/integration/example_assets.go`
- **提交:** 8b4ce6d

**4. [规则 1 - Bug] materialize.go 使用 auth.TokenIssuer.Verify (没有独立的 ParseToken)**
- **发现于:** 任务 4.3 计划审查
- **问题:** 计划调用 `auth.ParseToken(tok)`,该函数在 auth 包中不存在
- **修复:** 使用 `PLATFORM_JWT_SIGNING_KEY` 环境变量的 `auth.NewTokenIssuer([]byte(signingKey), 0).Verify(tok)`
- **修改的文件:** `cmd/platform/materialize.go`
- **提交:** 8b4ce6d

## 自我检查: 通过

所有文件验证存在:
- internal/connector/firstparty/postgres/postgres.go — 找到
- internal/connector/firstparty/postgres/factory.go — 找到
- internal/run/reaper.go — 找到
- cmd/platform/worker.go — 找到
- cmd/platform/materialize.go — 找到
- cmd/platform/factories.go — 找到
- test/integration/e2e_postgres_test.go — 找到

所有提交验证:
- 77e3187 (任务 4.1: PostgreSQL 连接器) — 找到
- 9da434d (任务 4.2: Stale-run reaper) — 找到
- 8b4ce6d (任务 4.3: CLI 子命令 + e2e 测试) — 找到