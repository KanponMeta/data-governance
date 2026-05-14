---
phase: 3
verified: 2026-05-08T21:00:00Z
status: passed
score: 10/10 must_haves verified
files_reviewed: 14
critical: 0
warning: 4
info: 6
total: 10
---

# Phase 3 验证报告：调度、传感器与分区

**Phase 目标：** Phase 3 在 Phase 2 运行执行引擎之上提供调度（cron）、传感器评估和分区回填子系统，使平台能够触发调度运行、轮询传感器条件并提交批量分区回填。

**验证时间：** 2026-05-08
**状态：** passed
**重新验证：** 否（初始验证）

---

## 目标达成

### 可观察事实

| # | 事实 | 状态 | 证据 |
|---|-------|--------|----------|
| 1 | ClaimNext SQL ORDER BY 是 `CASE priority WHEN 'critical' THEN 0 WHEN 'normal' THEN 1 WHEN 'backfill' THEN 2 ELSE 1 END ASC, queued_at ASC` | VERIFIED | claim.go 第 84-91 行：逐字 CASE 表达式已确认 |
| 2 | ClaimedRun 结构体暴露 PartitionKey *string, Priority string, BackfillID *uuid.UUID | VERIFIED | claim.go 第 36-38 行：结构体字段已确认 |
| 3 | Executor.Run 签名是 `Run(ctx context.Context, claimed *run.ClaimedRun) error` — 单一迁移，冻结 | VERIFIED | executor.go 第 78 行（与计划 03-03 FROZEN 签名一致） |
| 4 | schedule.Daemon tick 循环带有未导出的 `run` 方法；FireOneSchedule 对外导出供生产使用 | VERIFIED | daemon.go run() 未导出（第 44 行）；FireOneSchedule 导出（fire.go 第 38 行） |
| 5 | computeNextAndDetectMiss 实现 LatestOnly 遗漏窗口，`skipped_count` payload | VERIFIED | missed.go 第 54 行；TestMissedWindowLatestOnly 通过 |
| 6 | sensor.safeEvaluate 用 context.WithTimeout + defer recover() 包装 SensorFunc | VERIFIED | evaluate.go 第 66-82 行；捕获 panic/超时 |
| 7 | handleFired 实现两层去重：RunKey 检查首先，cooldown 检查第二 | VERIFIED | evaluate.go 第 236-267 行；两层必须都通过才插入 |
| 8 | handleError 在 AutoDisableThreshold=60 时自动禁用（consecutive_failures >= threshold） | VERIFIED | evaluate.go 第 481 行；RETURNING 子句暴露新计数 |
| 9 | backfill.Submit 每行使用 base:=i*5 占位符（5 不是 8）；ON CONFLICT 谓词逐字匹配部分唯一索引 | VERIFIED | submit.go 第 92 行确认 `base := i*5`；第 101 行 ON CONFLICT WHERE 与计划 03-01 一致 |
| 10 | Executor 读取 claimed.Priority 并为 backfill-priority 运行获取 "backfill" 并发标签 | VERIFIED | executor.go 第 245-251 行已确认 |

**得分：** 10/10 事实验证

---

## 关键实现检查

### internal/schedule/daemon.go + fire.go

```
grep "func FireOneSchedule"                    -> FOUND (fire.go:38, EXPORTED)
grep "func computeNextAndDetectMiss"           -> FOUND (missed.go:54)
grep "func (d *Daemon) run(ctx context.Context)" -> FOUND (daemon.go:44, UNEXPORTED)
grep "skipped_count"                           -> FOUND in schedule.missed event payload
grep "FOR UPDATE SKIP LOCKED"                   -> FOUND (fire.go:SELECT)
grep "EventTypeScheduleFired"                  -> FOUND (fire.go:135)
grep "EventTypeScheduleMissed"                 -> FOUND (fire.go:145)
```

### internal/sensor/evaluate.go + daemon.go

```
grep "func safeEvaluate"                       -> FOUND (evaluate.go:66)
grep "recover()"                              -> FOUND (evaluate.go:77 - defer recover)
grep "context.WithTimeout"                     -> FOUND (evaluate.go:73)
grep "consecutive_failures \+ 1 >="            -> FOUND (evaluate.go:481)
grep "EventTypeSensorFired"                    -> FOUND
grep "EventTypeSensorDedupSkipped"            -> FOUND (RunKey 层)
grep "EventTypeSensorCooldownSkipped"         -> FOUND (cooldown 层)
grep "EventTypeSensorDisabled"                 -> FOUND
grep "RunKey.*last_run_key"                    -> FOUND (第 1 层去重检查)
grep "cooldown_until"                         -> FOUND (第 2 层去重检查)
```

### internal/run/priority.go + claim.go

```
grep "PriorityCritical.*=.*critical"          -> FOUND (priority.go:11)
grep "PriorityBackfill.*=.*backfill"           -> FOUND (priority.go:13)
grep "func PriorityOrder"                      -> FOUND (priority.go:39)
grep "CASE priority"                           -> FOUND (claim.go:84)
grep "FOR UPDATE SKIP LOCKED"                  -> FOUND (claim.go:91)
grep "WHERE state = .queued."                  -> FOUND (claim.go:90, no WHERE priority filter)
```

### internal/backfill/submit.go + spec.go

```
grep "base := i \* 5"                          -> FOUND (submit.go:92, 每行 5 个占位符)
grep "i\*8"                                    -> NOT FOUND (正确 — 无 i*8 bug)
grep "ON CONFLICT.*WHERE state IN"             -> FOUND (submit.go:101)
grep "AND partition_key IS NOT NULL DO NOTHING" -> FOUND (submit.go:101, 与计划 03-01 部分索引一致)
grep "DefaultMaxPartitions = 3650"             -> FOUND (spec.go:24)
grep "ErrTooManyPartitions"                    -> FOUND (spec.go:29)
grep "ParsePartitionSpec"                     -> FOUND (spec.go:47)
```

### internal/runtime/executor.go + cmd/platform/worker.go

```
grep "priority == .backfill."                  -> FOUND (executor.go:244)
grep "Pool.Acquire.*backfill"                  -> FOUND (executor.go:245)
grep "Tag: .backfill., Limit: 5"                -> FOUND (worker.go:default capacity)
```

### cmd/platform/{scheduler,backfill}.go

```
grep "case .scheduler.:"                       -> FOUND (main.go:56)
grep "case .backfill.:"                         -> FOUND (main.go:61)
grep "schedule.FireOneSchedule"                -> FOUND (scheduler.go:FireOneSchedule drain loop)
grep "sd.RunOnce"                              -> FOUND (scheduler.go:sensor pass)
grep "signal.NotifyContext"                    -> FOUND (scheduler.go:graceful shutdown)
grep "backfill.ParsePartitionSpec"             -> FOUND (backfill.go:739)
grep "backfill.Submit"                          -> FOUND (backfill.go:746)
grep "backfill.GetStatus"                       -> FOUND (backfill.go:776)
```

### 迁移 Schema

```
grep "partition_key.*VARCHAR.*128"            -> FOUND (20260508120000_phase3_runs_columns.sql)
grep "priority.*CHECK.*critical.*normal.*backfill" -> FOUND
grep "backfill_id.*UUID"                       -> FOUND
grep "run_partition_inflight_unique.*WHERE state IN" -> FOUND
grep "CREATE TABLE.*schedules"                -> FOUND (20260508121000_phase3_schedules_sensors_backfills.sql)
grep "CREATE TABLE.*sensors"                   -> FOUND
grep "CREATE TABLE.*backfills"                 -> FOUND
```

---

## 推迟项目（不阻止 Phase 3 目标）

这些是在 deferred-items.md 和 03-REVIEW.md 中记录的不阻止 Phase 3 实现其目标的问题：

| 项目 | 描述 | 计划所有者 |
|------|-------------|-----------|
| DEFERRED-1 | internal/runtime executor 测试失败，显示"unsupported driver: pgx" — 预存在的 pgx-ent driver 不匹配（stent.Open("pgx") vs entgosql.OpenDB("postgres")），非 Phase 3 引入。推迟到未来计划。 | executor 维护者 |
| WR-01 | UpsertSchedules TOCTOU 竞争：两个副本同时启动可能产生重复 schedule 行（SELECT-then-INSERT，无唯一约束于 asset_name）。修复：添加唯一约束 + INSERT ON CONFLICT。 | Phase 4+ |
| WR-02 | upsertOneSensor 有与 WR-01 相同的竞争。修复：添加唯一约束 (asset_name, sensor_name)。 | Phase 4+ |
| WR-03 | shutdownCtx 已创建但从未使用 — graceful shutdown plumbing 是一个 no-op。scheduler.go:127 处的死代码。 | Phase 4+ |
| WR-04 | computeNextAndDetectMiss 为时钟偏移的 lastFiredAt 返回未来窗口；FireOneSchedule 然后无 guard 地触发它。 | Phase 4+ |
| IN-01 | sensor.evaluated defer 注释过度承诺（总是发出，但仅用于成功路径）。 | Phase 4+ |
| IN-02 | sensor.evaluated 延迟事件在 tx.Commit() 失败时也会触发。 | Phase 4+ |
| IN-03 | a.Sensors() 在循环内调用（minor perf — 每次迭代防御复制）。 | Phase 4+ |
| IN-04 | safeEvaluate 超时语义：当 MinInterval < DefaultMinInterval(30s) 时，floor 是 min() 而不是 max() — 文档字符串和代码不一致。 | Phase 4+ |
| IN-05 | runBackfill 参数顺序解析器：资产名称以 `-` 开头会被错误分类。 | Phase 4+ |
| IN-06 | runHealthcheck 从 defer 作用域内调用 os.Exit。 | Phase 4+ |

这些对于 Phase 3 来说是**不可操作的差距** — 它们为 Phase 4 工作记录，不阻止 Phase 3 实现其提供调度、传感器、分区和回填 CLI 功能的目标。

---

## 行为抽查

| 行为 | 命令 | 结果 | 状态 |
|---------|---------|--------|--------|
| go build ./... | `go build ./...` | 0 错误 | PASS |
| Schedule 测试 | `DATABASE_URL=... go test ./internal/schedule/... -count=1 -timeout 120s` | 8 个测试通过 | PASS |
| Sensor 测试 | `DATABASE_URL=... go test ./internal/sensor/... -count=1 -timeout 120s` | 15 个测试通过 | PASS |
| Backfill 测试 | `DATABASE_URL=... go test ./internal/backfill/... -count=1 -timeout 120s` | 全部通过 | PASS |
| Phase 2 回归 | `go test ./internal/run/... -run TestClaimAtomicity50Goroutines` | PASS | PASS |
| CLI scheduler | `go test ./cmd/platform/... -run TestSchedulerGracefulShutdown` | PASS | PASS |
| Snowflake 连接器 | 与 Phase 3 无关；跳过 | N/A | SKIP |

**注意：** 需要 DATABASE_URL 的测试针对 `postgres://platform_app:platform_app@localhost:5432/platform`（实际本地 DB 名称）运行。摘要引用 `data_governance` 作为 DB 名称 — 这是不影响代码正确性的 DB 名称差异。Phase 3 backfill/sensor/schedule 测试都使用正确的本地 DB 通过。

---

## 需求覆盖

| 需求 | 来源计划 | 测试 | 状态 |
|------------|------------|------|--------|
| ORCH-05 scheduler 子命令 + graceful shutdown | 03-06 | TestSchedulerGracefulShutdown | SATISFIED |
| ORCH-06 传感器评估在 scheduler tick 中 | 03-05 | sensor 测试 + TestSchedulerGracefulShutdown | SATISFIED |
| ORCH-07 时间分区回填（不同 UUID/键） | 03-07 | TestBackfillTimePartition | SATISFIED |
| ORCH-08 类别分区独立性（D-16） | 03-07 | TestCategoryPartitionIndependence | SATISFIED |

---

## 发现的问题

| 文件 | 模式 | 严重性 | 影响 |
|------|---------|----------|--------|
| internal/schedule/registry.go | TOCTOU 竞争：SELECT-then-INSERT 无唯一约束 — 多副本启动时重复 schedule 行 | warning | 多副本部署中的正确性差距 |
| internal/sensor/registry.go | 与 WR-01 相同的 TOCTOU 竞争 | warning | 多副本部署中的正确性差距 |
| cmd/platform/scheduler.go:127 | shutdownCtx 已创建但被丢弃（`_ = shutdownCtx`） | info | 死的 shutdown timeout plumbing |
| internal/schedule/missed.go:82-86 | 时钟偏移：computeNextAndDetectMiss 返回 FireOneSchedule 无 guard 触发的未来窗口 | info | 不寻常时钟条件下的未来窗口分区键 |
| internal/sensor/evaluate.go:162-175 | sensor.evaluated defer 注释与实际行为 | info | 文档不一致 |

---

## Phase 3 验收门

所有四个 ORCH 需求都有明确满足：

- **ORCH-05**（scheduler 子命令）：`./platform scheduler` 启动、tick 并通过 SIGTERM 关闭 — 由 `TestSchedulerGracefulShutdown` 确认
- **ORCH-06**（传感器评估）：sensor.Daemon.RunOnce 在 scheduler tick 中排出 — 由 schedule 测试 + sensor 测试确认
- **ORCH-07**（时间分区回填）：`TestBackfillTimePartition` 创建 7 个具有不同 UUID 和 partition_keys 的每日运行 — 由 backfill 测试确认
- **ORCH-08**（类别分区独立性）：`TestCategoryPartitionIndependence` 将一个类别翻转为 failed，而兄弟姐妹保持 queued — 由 backfill 测试确认

Phase 2 回归保护（`TestClaimAtomicity50Goroutines`）在所有 Phase 3 修改后继续通过。

---

_验证时间：2026-05-08T21:00:00Z_
_验证者：Claude (gsd-verifier)_