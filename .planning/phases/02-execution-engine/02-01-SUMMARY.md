---
phase: 2
plan: 01
title: Asset DSL + DefinitionRegistry + AssetIO contract
status: complete
completed: "2026-05-08T03:12:00Z"
duration: ~15m
tasks_completed: 2
tasks_total: 2
files_created: 8
files_modified: 2
commits:
  - a4d063f
  - b53b69a
subsystem: asset-sdk
tags: [asset, builder, registry, io, connector, dsl]
dependency_graph:
  requires: []
  provides:
    - internal/asset: Asset value type, Builder DSL, DefinitionRegistry, AssetIO, ConnectorResolver, RetryPolicy, Resource
    - internal/connector: RegisterInProcess, RegisterPlugin (stub), ErrPluginNotImplemented
  affects:
    - "02-02: DAG executor consumes Asset.Upstreams(), Asset.MaterializeFn(), Builder.Build()"
    - "02-03: Retry engine consumes Asset.RetryPolicy(), Asset.Resources()"
    - "02-04: CLI calls asset.Default().Get(name)"
tech_stack:
  added: []
  patterns:
    - "Functional builder DSL with *Builder chaining (D-01)"
    - "Process-global singleton via Default() with test-only resetForTest() (D-05)"
    - "Defensive-copy accessors on Asset (opaque struct, private fields)"
    - "Sentinel error variables + errors.Is() throughout"
key_files:
  created:
    - internal/asset/asset.go
    - internal/asset/builder.go
    - internal/asset/registry.go
    - internal/asset/io.go
    - internal/asset/retry.go
    - internal/asset/asset_test.go
    - internal/asset/registry_test.go
    - internal/asset/builder_test.go
  modified:
    - internal/connector/registry.go
    - internal/connector/registry_test.go
decisions:
  - "Build() returns *Asset without committing to Default() — test-friendly path consumed by 02-02 DAG tests"
  - "ConnectorResolver interface chosen over direct connector.Registry reference — allows plan 02-03 to inject config-aware resolver without coupling io.go to registry internals"
  - "resetForTest() kept unexported — accessible only from package-internal tests (package asset), preventing misuse in other packages"
  - "AssetIO.Read enforces declared-upstream check at runtime (T-02-01-05 mitigation) — prevents lineage gaps before Phase 4"
requirements: [ORCH-01, ORCH-02]
---

# Phase 2 计划 01：Asset DSL + DefinitionRegistry + AssetIO 契约总结

**一句话描述：** 功能型构建器 DSL（`asset.New().Upstream().Connector().Materialize().Register()`），包含进程级 DefinitionRegistry、AssetIO 连接器抽象接口，以及用于进程内加载的 connector.Registry 扩展。

## 导出的 API 表面

### internal/asset

| 标识符 | 类型 | 描述 |
|---|---|---|
| `New(name string) *Builder` | func | Asset 定义 DSL 入口点 |
| `Builder` | type | 通过链式调用累积 asset 配置；通过 Register() 提交或通过 Build() 验证 |
| `Builder.Upstream(names ...string) *Builder` | method | 可变参数上游依赖声明 |
| `Builder.Connector(name string) *Builder` | method | 按名称绑定 asset 到连接器（在物化时解析） |
| `Builder.Materialize(fn MaterializeFunc) *Builder` | method | 注册用户转换函数 |
| `Builder.Retry(p RetryPolicy) *Builder` | method | 覆盖平台默认重试策略 |
| `Builder.Resource(name string, weight int) *Builder` | method | 附加命名资源约束（weight 默认为 1，如果 ≤0） |
| `Builder.Build() (*Asset, error)` | method | 验证并返回 *Asset，**不提交**到 Default() registry — 供计划 02-02 DAG 测试使用的测试路径 |
| `Builder.Register() error` | method | 验证并提交到 Default() registry |
| `Asset` | type | 不可变运行时表示；所有字段私有，通过方法访问 |
| `Asset.Name() string` | method | 唯一 asset 标识符 |
| `Asset.Upstreams() []string` | method | 上游名称的防御副本 |
| `Asset.ConnectorName() string` | method | 绑定连接器名称 |
| `Asset.MaterializeFn() MaterializeFunc` | method | 用户提供的转换函数 |
| `Asset.RetryPolicy() RetryPolicy` | method | 每个 asset 的重试配置 |
| `Asset.Resources() []Resource` | method | 资源约束的防御副本 |
| `MaterializeFunc` | type | `func(ctx context.Context, io AssetIO) (MaterializeResult, error)` |
| `MaterializeResult` | type | `{RowsWritten int64, Metadata map[string]any}` |
| `RetryPolicy` | type | `{Max int, InitialDelay, MaxDelay time.Duration, JitterPct int}` |
| `RetryPolicy.IsZero() bool` | method | 当策略未设置时为真（引擎应用平台默认） |
| `DefaultRetryPolicy() RetryPolicy` | func | 零值策略 |
| `Resource` | type | `{Name string, Weight int}` |
| `AssetIO` | interface | `Read(ctx, upstream) ([]connector.Row, error)` + `Write(ctx, rows) (int64, error)` |
| `ConnectorResolver` | interface | `Resolve(assetName) (connector.Connector, connector.AssetRef, error)` — 计划 02-03 实现 |
| `NewAssetIO(self, resolver) AssetIO` | func | 构造运行时 AssetIO；DAG executor 每步构建一个 |
| `DefinitionRegistry` | type | 线程安全的 asset registry |
| `NewDefinitionRegistry() *DefinitionRegistry` | func | 构造空 registry |
| `DefinitionRegistry.Register(*Asset) error` | method | 添加 asset；重复时返回 ErrAlreadyRegistered |
| `DefinitionRegistry.Get(name) (*Asset, error)` | method | 按名称查找；不存在时返回 ErrNotFound |
| `DefinitionRegistry.List() []string` | method | 所有名称，按字母顺序排序 |
| `Default() *DefinitionRegistry` | func | 进程级单例 (D-05) |
| `ErrAlreadyRegistered` | var | 哨兵：重复注册 |
| `ErrNotFound` | var | 哨兵：asset 不在 registry 中 |
| `ErrMissingMaterialize` | var | 哨兵：Build/Register 时未调用 Materialize |
| `ErrMissingConnector` | var | 哨兵：Build/Register 时未调用 Connector |
| `ErrEmptyName` | var | 哨兵：New("") 空名称 |
| `ErrUnknownUpstream` | var | 哨兵：AssetIO.Read 时未声明的上游 |

### internal/connector（扩展）

| 标识符 | 类型 | 描述 |
|---|---|---|
| `Registry.RegisterInProcess(name, impl) error` | method | 第一方进程内连接器加载 (D-07) |
| `Registry.RegisterPlugin(name, pluginPath) error` | method | 延迟：返回 ErrPluginNotImplemented |
| `ErrPluginNotImplemented` | var | 哨兵：v1 中 plugin 加载器未实现 (D-07) |

## 测试覆盖总结

**internal/asset：** 19 个测试，覆盖：
- Asset 访问器正确性 + 防御副本行为
- RetryPolicy.IsZero()、DefaultRetryPolicy()
- MaterializeResult、Resource 字段访问
- DefinitionRegistry Register/Get/List（成功、ErrAlreadyRegistered、ErrNotFound、排序列表）
- Default() 单例一致性 + resetForTest() 清除状态
- Builder 完整链式注册
- 可变参数 Upstream()
- 顺序无关的链式调用
- Resource weight=0 默认为 1
- ErrMissingMaterialize、ErrMissingConnector
- AssetIO.Read：运行时强制声明的上游检查 (T-02-01-05)
- AssetIO.Write：委托给连接器
- Build() 返回 *Asset 但不注册（确认 ErrNotFound）
- Build() 验证错误（ErrEmptyName、ErrMissingMaterialize、ErrMissingConnector）
- Build() 和 Register() 产生等效的 *Asset 字段
- 重复 Register 返回 ErrAlreadyRegistered

**internal/connector：** 新增 3 个测试：
- RegisterInProcess 成功路径
- RegisterInProcess ErrAlreadyRegistered
- RegisterPlugin 返回 ErrPluginNotImplemented（确认 errors.Is）

## 下游计划的开放钩子

| 计划 | 消费的 API | 用途 |
|------|-------------|------|
| 02-02 (DAG executor) | `Builder.Build()`、`Asset.Upstreams()`、`Asset.MaterializeFn()` | 构建测试 asset 而不污染全局 registry；遍历依赖图；每步调用 MaterializeFunc |
| 02-03 (Retry + concurrency) | `Asset.RetryPolicy()`、`Asset.Resources()`、`ConnectorResolver` 接口 | 针对 connector.Registry + 启动配置实现 ConnectorResolver；token 池获取使用 Resources() |
| 02-04 (CLI + PG connector) | `asset.Default().Get(name)`、`connector.Registry.RegisterInProcess()` | `materialize <asset>` CLI 查找 asset；PostgreSQL 连接器通过 RegisterInProcess 注册 |

## 与计划的偏差

无 — 计划按书面完全执行。

唯一实现注意事项：`go build ./...` 因预存在的 `go.sum` 中 `ariga.io/atlas` MySQL 内部条目（`golang.org/x/mod/semver`）而失败。这与此计划无关 — `internal/asset` 和 `internal/connector` 包构建干净。预存在的构建问题超出此计划范围。

## 已知的存根

- `connector.Registry.RegisterPlugin`：按设计故意返回 `ErrPluginNotImplemented`（D-07 延迟）。在 godoc 中有文档。计划 02-05 或未来阶段添加 hashicorp/go-plugin 实现。
- `AssetIO` 实现将所有行缓冲在内存中（无流式）。性能细节不在用户契约中 — 计划 02-03 可能在需要时优化。
- `ConnectorResolver` 接口已定义但在此计划中未实现 — 计划 02-03 针对 connector.Registry + 启动配置实现它。

## 威胁标志

无 — 未引入新的网络端点、认证路径或信任边界。所有已识别的威胁（T-02-01-01 到 T-02-01-07）均按计划威胁模型中的规定处理。

## 自检：通过

所有 8 个创建的文件的文件确认存在于磁盘上。
任务提交确认：`a4d063f`（任务 1）、`b53b69a`（任务 2）。