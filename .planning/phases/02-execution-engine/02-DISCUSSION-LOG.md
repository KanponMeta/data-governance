# Phase 2: 执行引擎 - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-05-07
**Phase:** 02-execution-engine
**Areas discussed:** 资产定义 DSL 与加载方式, 首方连接器封装方式, Phase 2 阶段切分, 重试 + 并发 token 池设计粒度

---

## Gray Area Selection

| Option | Description | Selected |
|--------|-------------|----------|
| 资产定义 DSL 与加载方式 | 用户怎么写资产代码，和平台二进制如何关联 | ✓ |
| 首方连接器封装方式 | 7 个一方连接器 in-process vs go-plugin vs 子命令 | ✓ |
| Phase 2 阶段切分 | 范围太大，是否拆分阶段 | ✓ |
| 重试 + 并发 token 池设计粒度 | ORCH-04 + ORCH-09，避免 Dagster 死锁 | ✓ |

**User selected:** all four.

---

## 资产定义 DSL 与加载方式

### Q1: 数据工程师怎么在 Go 代码中定义一个资产？

| Option | Description | Selected |
|--------|-------------|----------|
| 函数式 builder | asset.New("users_clean").Upstream("users_raw").Materialize(fn).Register() | ✓ |
| struct + interface | 用户实现 Asset 接口，方法风格 | |
| 声明式结构体 | asset.Definition{...} 字段填值 | |

**User's choice:** 函数式 builder (recommended)
**Notes:** 链式调用、可读性、可生成资产（循环中创建）、与 Dagster 心智模型一致。

### Q2: 用户代码与平台二进制的关系？

| Option | Description | Selected |
|--------|-------------|----------|
| 用户编译自己的二进制 | 链接 SDK，处理交付 | ✓ |
| 用户类型、平台提供 main | 平台 CLI 生成 main.go | |
| Plugin 动态加载 (.so) | 限制太多，CLAUDE.md 排除 | |

**User's choice:** 用户编译自己的二进制 (recommended)
**Notes:** Go 静态语言特性决定 — 不能像 Python 那样丢进目录加载。

### Q3: 资产定义如何与连接器绑定？

| Option | Description | Selected |
|--------|-------------|----------|
| 引用 connector 名 | asset.Connector("postgres-prod")，启动时从 config 加载 | ✓ |
| 代码中直接配置连接器 | 连接信息与代码耦合 | |
| 资产名称隐含映射 | "postgres.public.users" 前缀匹配 | |

**User's choice:** 资产在代码中引用 connector 名 (recommended)
**Notes:** 凭证与代码解耦；配置可提交到 git，secrets 走环境变量。

### Q4: Materialize 签名如何传递输入/输出？

| Option | Description | Selected |
|--------|-------------|----------|
| Inputs/output AssetIO | AssetIO 提供 Read/Write 简化 API | ✓ |
| 手动调 connector API | 显式但样板代码多 | |
| 纯 SQL 表达式 | dbt 风格，仅 SQL 上下文 | |

**User's choice:** Inputs 传表名，输出返回表名 (recommended)
**Notes:** 隐藏 connector 调用复杂度，让用户专注业务逻辑。

### Q5: 用户编译自己二进制后，运行时架构？

| Option | Description | Selected |
|--------|-------------|----------|
| 单二进制多模式 | ./myproject server / worker / materialize | ✓ |
| 双进程 - Dagster 代码位置 | gRPC code-location 服务器 | |
| 用户 SDK 嵌入平台 | 平台是库 | |

**User's choice:** 单二进制多模式 (recommended)
**Notes:** Go 静态链接的核心优势 — 不需要 Dagster 在 Python 中被迫使用的 RPC 双进程模型。

### Q6: 资产是否需要返回结果元数据？

| Option | Description | Selected |
|--------|-------------|----------|
| MaterializeResult 结构体 | RowsWritten + Metadata map | ✓ |
| 仅 error | 平台从事件日志推导 | |
| 完整资产快照 | 包含 row count/schema/lineage | |

**User's choice:** 返回 MaterializeResult 结构体 (recommended)
**Notes:** Phase 2 hook，Phase 4 血缘扩展利用 Metadata。

---

## 首方连接器封装方式

### Q1: 7 个一方连接器怎么运行？

| Option | Description | Selected |
|--------|-------------|----------|
| In-process 编进平台二进制 | 同进程实现 connector 接口 | ✓ |
| 全部走 go-plugin 子进程 | 一致性但启动慢 | |
| 混合：默认 in-process 可切换 | 运维复杂 | |
| 独立子命令二进制 spawn | 中间方案 | |

**User's choice:** In-process 编进平台二进制 (recommended)
**Notes:** Phase 1 已经有 example_inproc 模式；启动快、调试好；go-plugin 留给第三方。

### Q2: Connector 的连接生命周期？

| Option | Description | Selected |
|--------|-------------|----------|
| 全局单例 + 连接池 | 进程级生命周期 | ✓ |
| 每次运行重新建立 | 干净但开销高 | |
| Long-running + idle 超时 | 复杂 | |

**User's choice:** 单例 + 连接池 (recommended)
**Notes:** 简单，匹配 v1 单机部署约束。

### Q3: 连接器凭证存放在哪里？

| Option | Description | Selected |
|--------|-------------|----------|
| config 文件 + env var 间接 | 配置可提交，secrets 在 env | ✓ |
| Postgres 内部表 + 加密 | 需自建 KMS 抽象 | |
| Vault / 外部 secrets | 增加部署依赖 | |

**User's choice:** config 文件 + 环境变量间接 (recommended)
**Notes:** 与 Phase 1 connector.proto 推荐的约定一致。

### Q4: 云连接器集成测试怎么在 CI 跑？

| Option | Description | Selected |
|--------|-------------|----------|
| 本地 emulator/fake | testcontainers + LocalStack + bigquery-emulator + fake-gcs | ✓ |
| Skip + nightly 真凭证 | 依赖付费资源 | |
| 混合：本地+云 mock 接口 | 限制性集成保证 | |

**User's choice:** 本地 emulator/fake (recommended)
**Notes:** CI 不需云凭证；Snowflake 若无可用 mock 则降级为 nightly + 真凭证（已记录在 deferred）。

---

## Phase 2 阶段切分

### Q1: Phase 2 如何切分？

| Option | Description | Selected |
|--------|-------------|----------|
| Plan 内部分组，不动 ROADMAP | 5 个 plan 内部增量交付 | ✓ |
| 拆为小数阶段 2.0 + 2.1 | 独立验收 | |
| 单阶段单 plan 一次交付 | 不推荐 | |

**User's choice:** Plan 内部分组 (recommended)
**Notes:** 与 Phase 1 五个 plan 的节奏一致；保持单阶段单验收报告。

### Q2: 连接器交付顺序？

| Option | Description | Selected |
|--------|-------------|----------|
| PG 领先，验证架构后批量其他 | 验证 connector 接口的 reference impl | ✓ |
| PG + MySQL 同时 | 双 SQL 验证抽象 | |
| 按使用频率分组 | ROI 排序 | |

**User's choice:** PG 领先 (recommended)
**Notes:** 验收标准 4 直接锁定 PG；其他 6 是同接口的备选实现。

### Q3: Phase 2 验收标准完成衡量？

| Option | Description | Selected |
|--------|-------------|----------|
| 5 条都需 PASS | 严格按领域验收 | ✓ |
| 拆给 Phase 2.0 + 2.1 | 验收随阶段切分 | |

**User's choice:** 5 条 ROADMAP 验收都需 PASS (recommended)
**Notes:** 严格执行 ROADMAP；选项 1 + 2 已经为这个标准做了铺垫。

---

## 重试 + 并发 token 池设计粒度

### Q1: 重试逻辑在哪一层实现？

| Option | Description | Selected |
|--------|-------------|----------|
| 引擎层 + 复用 River 进程重试 | 业务/基础双层 | ✓ |
| 全部交给 River | 业务+平台共用计数 | |
| 全部在引擎 | River 仅入队 | |

**User's choice:** 引擎层 + 复用 River 进程重试 (recommended)
**Notes:** River 处理 worker 崩溃；引擎处理业务错误，retry budget 不被冲走。

### Q2: 重试策略在哪里声明？

| Option | Description | Selected |
|--------|-------------|----------|
| 资产级 Builder 选项 | asset.Retry(...) | ✓ |
| 全局默认 + 粗粒度 override | 表达力差 | |
| Materialize 内手动 | 事件日志看不到 | |

**User's choice:** 资产级 Builder 选项 (recommended)
**Notes:** 平台默认 + 资产 override，事件日志可见。

### Q3: 并发 token 池设计粒度？

| Option | Description | Selected |
|--------|-------------|----------|
| 全局统一池 + 资源标签 | 单表，多维度从同池借还 | ✓ |
| 仅全局 max_concurrent_runs | 表达力不足 | |
| 分层池 | 正是 PITFALLS #2 警告的形态 | |

**User's choice:** 全局统一池 + 资源标签 (recommended)
**Notes:** PROJECT.md 关键决策直接锁定此项；避免 Dagster issue #25743。

### Q4: Run claiming 如何避免重复？

| Option | Description | Selected |
|--------|-------------|----------|
| SKIP LOCKED + 状态枚举 CHECK | 双层保护 + 50-goroutine 测试 | ✓ |
| River 原生事务性作业 | Run 状态保护仍需应用层 | |

**User's choice:** SELECT FOR UPDATE SKIP LOCKED + CHECK 约束 (recommended)
**Notes:** 验收标准 3 直接对应；测试用例锁定。

---

## Claude's Discretion

- AssetIO 内部实现 (流式/批/缓冲)
- River 队列拓扑 (单队列 vs 优先级队列)
- concurrency_tokens 表索引细节
- MaterializeResult.Metadata 是否在 Phase 2 引入类型化常量
- yaml vs toml 配置文件
- materialize CLI 同步 vs 异步默认行为 (默认同步 + --detach)
- Snowflake mock 选择或降级 nightly

## Deferred Ideas

- 第三方 go-plugin 连接器加载器实现（接口预留，scaffolding 等首个真实需求）
- Vault / KMS 凭证集成（v2）
- 异步 materialize CLI 唯一模式（v1.x 打磨）
- OpenLineage 事件发送（Phase 4）
- 调度/传感器/分区/回填（Phase 3，token 池资源标签为其预留）
- 血缘提取（Phase 4）
- Schema 自动捕获（Phase 4）
