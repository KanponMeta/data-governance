---
phase: 2
plan: 01
title: Asset DSL + DefinitionRegistry + AssetIO 合约
type: execute
wave: 1
depends_on: []
requirements: [ORCH-01, ORCH-02]
files_modified:
  - internal/asset/asset.go
  - internal/asset/builder.go
  - internal/asset/registry.go
  - internal/asset/io.go
  - internal/asset/retry.go
  - internal/asset/asset_test.go
  - internal/asset/registry_test.go
  - internal/asset/builder_test.go
  - internal/connector/registry.go
  - internal/connector/registry_test.go
autonomous: true
must_haves:
  truths:
    - "Data engineer can write `asset.New(\"users_clean\").Upstream(\"users_raw\").Connector(\"postgres-prod\").Materialize(fn).Register()` in Go code"
    - "Calling Register() twice with the same asset name returns ErrAlreadyRegistered, not silent overwrite"
    - "DefinitionRegistry.Get(name) returns the registered Asset; Get on unknown name returns ErrNotFound"
    - "AssetIO.Read(upstreamName) returns rows produced by the named upstream connector without the user importing connector package"
    - "Retry policy declared on builder is retrievable via Asset.RetryPolicy() (engine reads this in plan 02-03)"
    - "connector.Registry.RegisterInProcess(name, impl) accepts an in-process Connector while RegisterPlugin reservation exists for Phase 2 deferred plugin path"
    - "Builder.Build() validates inputs and returns *Asset WITHOUT registering — used by tests in plan 02-02 (DAG) to construct assets without polluting the global Default() registry"
  artifacts:
    - path: "internal/asset/asset.go"
      provides: "Asset value type + DefinitionRegistry interface"
      contains: "type Asset struct"
    - path: "internal/asset/builder.go"
      provides: "asset.New + chained Builder methods Upstream/Connector/Materialize/Retry/Resource/Build/Register"
      contains: "func New("
    - path: "internal/asset/registry.go"
      provides: "process-global DefinitionRegistry with Get/List/Register"
      contains: "type DefinitionRegistry"
    - path: "internal/asset/io.go"
      provides: "AssetIO interface with Read(asset string) and Write(rows) helpers; MaterializeResult type"
      contains: "type AssetIO interface"
    - path: "internal/asset/retry.go"
      provides: "RetryPolicy struct (Max, InitialDelay, MaxDelay, JitterPct)"
      contains: "type RetryPolicy struct"
    - path: "internal/connector/registry.go"
      provides: "RegisterInProcess + RegisterPlugin (stub) methods on Registry"
      contains: "func (r *Registry) RegisterInProcess"
  key_links:
    - from: "user code"
      to: "internal/asset.Builder"
      via: "asset.New(...)..Register()"
      pattern: "asset\\.New\\("
    - from: "internal/asset.Builder.Register"
      to: "internal/asset.DefinitionRegistry"
      via: "process-global Default() registry"
      pattern: "Default\\(\\)\\.Register"
    - from: "internal/asset.AssetIO"
      to: "internal/connector.Connector"
      via: "AssetIO delegates Read/Write to the named connector through the registry"
      pattern: "connector\\.(Read|Write)"
    - from: "internal/dag.dag_test.go (plan 02-02)"
      to: "internal/asset.Builder.Build()"
      via: "DAG tests construct *Asset via Build() without committing to global registry"
      pattern: "\\.Build\\(\\)"
---

<objective>
为 Phase 2 建立面向用户的 SDK 边界: 功能构建器 DSL (`asset.New().Upstream().Connector().Materialize().Register()`)、进程级 DefinitionRegistry、对用户代码隐藏连接器调用的 AssetIO 合约,以及用于进程内加载的 connector.Registry 扩展。此计划引入了平台为外部消费导出的第一个包 — 从此以后其 API 稳定性很重要。
</objective>

<context>
此计划实现决策 D-01 (功能构建器)、D-04 (Materialize 签名 + AssetIO)、D-05 (运行时注册表,无代码生成)、D-07 (RegisterInProcess),并为 D-15 (构建器上的重试策略) + D-16 (构建器上的资源标签) 暴露钩子,以便 plan 02-02 和 02-03 可以读取它们。

**为什么这是 Wave 1 计划:** 不依赖于运行生命周期或令牌池 — 仅依赖于 Phase 1 冻结的 `connector.Connector` 接口。Plan 02-02 (DAG executor) 消费 `Asset.Upstreams()` 和 `Asset.MaterializeFn()`; plan 02-03 (重试/并发) 消费 `Asset.RetryPolicy()` 和 `Asset.Resources()`; plan 02-04 (CLI) 调用 `asset.Default().Get(name)` 按名称查找资产。

**为什么是新的 SDK 包,而不是 pkg/:** Phase 1 D-04 选择仅使用 `internal/` 布局 (无 `pkg/`)。对于 Phase 2,用户二进制导入是 `github.com/kanpon/data-governance/internal/asset` — 这是可以接受的,因为用户使用 `replace github.com/kanpon/data-governance => ../platform` (D-02) 编译他们的二进制文件。重新导出到公共路径 是 v2 改进; SDK 合约才是重要的。

**通过名称进行连接器绑定 (D-03):** Asset 存储连接器名称 (字符串),而不是 Connector 结构。解析在物化时通过 connector.Registry 进行,plan 02-03 的配置加载器从以相同名称键控的 yaml 配置填充它。

**使用的冻结接口:**

```go
// 来自 internal/connector/connector.go (冻结 v1.0.0)
type Connector interface {
  APIVersion() string
  Ping(ctx, PingRequest) (PingResponse, error)
  Schema(ctx, SchemaRequest) (SchemaResponse, error)
  Read(ctx, ReadRequest) (ReadResponse, error)
  Write(ctx, WriteRequest) (WriteResponse, error)
}
```

@.planning/phases/02-execution-engine/02-CONTEXT.md
@.planning/research/ARCHITECTURE.md
@internal/connector/connector.go
@internal/connector/registry.go
@internal/connector/example_inproc/postgres_stub.go
</context>

<tasks>

<task id="1.1" type="auto" tdd="true">
  <name>任务 1: Asset 值类型 + RetryPolicy + DefinitionRegistry 骨架</name>
  <files>internal/asset/asset.go, internal/asset/retry.go, internal/asset/registry.go, internal/asset/asset_test.go, internal/asset/registry_test.go</files>
  <read_first>
    - .planning/phases/02-execution-engine/02-CONTEXT.md (D-01, D-04, D-05, D-15, D-16)
    - internal/connector/connector.go (冻结接口 — Asset 按名称引用连接器)
    - internal/connector/registry.go (现有 Register/Get/List 形状; DefinitionRegistry 的镜像约定)
    - internal/storage/ent/schema/event_log.go (内部包结构示例)
  </read_first>
  <behavior>
    - Test 1 (asset_test.go): `Asset` 结构暴露 Name(), Upstreams() []string, ConnectorName() string, MaterializeFn() MaterializeFunc, RetryPolicy() RetryPolicy, Resources() []Resource — 所有方法都返回构造时提供的值。
    - Test 2 (asset_test.go 中嵌入的 retry_test.go): `RetryPolicy{Max: 0}.IsZero()` 返回 true。`DefaultRetryPolicy()` 返回 Max=0, InitialDelay=0, MaxDelay=0, JitterPct=0 (引擎在 IsZero 时使用平台默认值)。
    - Test 3 (registry_test.go): `DefinitionRegistry.Register(asset)` 成功; 第二次使用相同名称 Register 返回 `ErrAlreadyRegistered`。Get(name) 返回资产。Get(未知) 返回 ErrNotFound。List() 返回按字母顺序排序的名称。
    - Test 4 (registry_test.go): `Default()` 在调用中返回相同的单例 (按 D-05 的进程级)。Reset() (仅测试辅助函数) 清除它。
  </behavior>
  <action>
    创建 `internal/asset/asset.go` 定义:
    ```go
    package asset

    import "context"

    // MaterializeFunc 是用户提供的转换函数。AssetIO 包装连接器调用 (D-04)。
    type MaterializeFunc func(ctx context.Context, io AssetIO) (MaterializeResult, error)

    // MaterializeResult 是 Materialize 调用的返回值。
    // RowsWritten 是有业务意义的行数; Metadata 是 Phase 4 lineage 钩子 (D-04)。
    type MaterializeResult struct {
        RowsWritten int64
        Metadata    map[string]any
    }

    // Resource 附加具有权重的命名资源约束 (D-16)。Plan 02-03 在从全局 concurrency_tokens 表检出令牌时读取这些。
    type Resource struct {
        Name   string // 例如 "postgres-prod"
        Weight int    // 默认 1; 每次获取消耗的令牌数
    }

    // Asset 是用户定义资产的不变运行时表示。
    // 仅通过 asset.New(...).Register() 构造。Builder 写入; Asset 读取。
    type Asset struct {
        name           string
        upstreams      []string
        connectorName  string
        materializeFn  MaterializeFunc
        retryPolicy    RetryPolicy
        resources      []Resource
    }

    func (a *Asset) Name() string                  { return a.name }
    func (a *Asset) Upstreams() []string           { return append([]string(nil), a.upstreams...) }
    func (a *Asset) ConnectorName() string         { return a.connectorName }
    func (a *Asset) MaterializeFn() MaterializeFunc { return a.materializeFn }
    func (a *Asset) RetryPolicy() RetryPolicy      { return a.retryPolicy }
    func (a *Asset) Resources() []Resource         { return append([]Resource(nil), a.resources...) }
    ```

    创建 `internal/asset/retry.go` 定义:
    ```go
    package asset

    import "time"

    // RetryPolicy 是每个资产的重试配置 (D-15)。InitialDelay 指数增长至 MaxDelay;
    // JitterPct (0..100) 随机化每个延迟。
    type RetryPolicy struct {
        Max          int
        InitialDelay time.Duration
        MaxDelay     time.Duration
        JitterPct    int
    }

    // IsZero 报告策略是否未设置,应应用平台默认值。
    func (r RetryPolicy) IsZero() bool {
        return r.Max == 0 && r.InitialDelay == 0 && r.MaxDelay == 0 && r.JitterPct == 0
    }

    // DefaultRetryPolicy 返回零值策略,用作引擎回退,当资产省略 Retry(...) 且启动配置中的平台级默认也未设置时。
    func DefaultRetryPolicy() RetryPolicy { return RetryPolicy{} }
    ```

    创建 `internal/asset/registry.go` 定义:
    ```go
    package asset

    import (
        "errors"
        "fmt"
        "sort"
        "sync"
    )

    var (
        ErrAlreadyRegistered = errors.New("asset: already registered")
        ErrNotFound          = errors.New("asset: not found")
    )

    // DefinitionRegistry 是进程级资产注册表 (D-05)。
    // Builder.Register() 调用 Default().Register(asset); worker / materialize
    // 子命令通过 Default().List() 枚举。
    type DefinitionRegistry struct {
        mu     sync.RWMutex
        assets map[string]*Asset
    }

    func NewDefinitionRegistry() *DefinitionRegistry {
        return &DefinitionRegistry{assets: make(map[string]*Asset)}
    }

    func (r *DefinitionRegistry) Register(a *Asset) error {
        if a == nil || a.name == "" {
            return fmt.Errorf("asset: register requires non-empty Name")
        }
        r.mu.Lock()
        defer r.mu.Unlock()
        if _, exists := r.assets[a.name]; exists {
            return fmt.Errorf("%w: %q", ErrAlreadyRegistered, a.name)
        }
        r.assets[a.name] = a
        return nil
    }

    func (r *DefinitionRegistry) Get(name string) (*Asset, error) {
        r.mu.RLock()
        defer r.mu.RUnlock()
        a, ok := r.assets[name]
        if !ok {
            return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
        }
        return a, nil
    }

    func (r *DefinitionRegistry) List() []string {
        r.mu.RLock()
        defer r.mu.RUnlock()
        names := make([]string, 0, len(r.assets))
        for n := range r.assets {
            names = append(names, n)
        }
        sort.Strings(names)
        return names
    }

    var defaultRegistry = NewDefinitionRegistry()

    // Default 返回资产 New(...).Register() 写入的进程级注册表。
    func Default() *DefinitionRegistry { return defaultRegistry }

    // resetForTest 替换默认注册表。仅供测试使用。
    func resetForTest() { defaultRegistry = NewDefinitionRegistry() }
    ```

    在 `internal/asset/asset_test.go` 和 `internal/asset/registry_test.go` 中创建测试,覆盖上述行为。使用 `testify/require` (已在 go.mod 中)。
  </action>
  <verify>
    <automated>cd /home/developer/.kanpon/code/go/data-governance && go test ./internal/asset/... -run 'TestAsset|TestRegistry|TestRetryPolicy' -count=1 -v</automated>
  </verify>
  <acceptance_criteria>
    - File `internal/asset/asset.go` exists and `grep -q "type Asset struct" internal/asset/asset.go` succeeds
    - File `internal/asset/asset.go` contains `type MaterializeFunc func(ctx context.Context, io AssetIO) (MaterializeResult, error)` (verify via `grep -q "type MaterializeFunc func(ctx context.Context, io AssetIO) (MaterializeResult, error)" internal/asset/asset.go`)
    - File `internal/asset/asset.go` contains `type MaterializeResult struct` with fields `RowsWritten int64` and `Metadata    map[string]any`
    - File `internal/asset/asset.go` contains `type Resource struct` with fields `Name   string` and `Weight int`
    - File `internal/asset/retry.go` contains `type RetryPolicy struct` with exactly fields `Max int`, `InitialDelay time.Duration`, `MaxDelay     time.Duration`, `JitterPct    int`
    - File `internal/asset/registry.go` contains `var ErrAlreadyRegistered`, `var ErrNotFound`, `func Default() *DefinitionRegistry`
    - `go test ./internal/asset/... -run 'TestAsset|TestRegistry|TestRetryPolicy' -count=1` exits 0
    - `go vet ./internal/asset/...` exits 0
  </acceptance_criteria>
  <done>Asset / RetryPolicy / DefinitionRegistry types exist; tests pass; package compiles cleanly with go vet.</done>
</task>

<task id="1.2" type="auto" tdd="true">
  <name>Task 2: Functional Builder + Build() + AssetIO interface + connector.Registry.RegisterInProcess</name>
  <files>internal/asset/builder.go, internal/asset/io.go, internal/asset/builder_test.go, internal/connector/registry.go, internal/connector/registry_test.go</files>
  <read_first>
    - internal/asset/asset.go (just created — Builder writes private fields)
    - internal/asset/registry.go (just created — Builder.Register() calls Default().Register())
    - internal/connector/registry.go (current file — adding RegisterInProcess + RegisterPlugin stub)
    - internal/connector/registry_test.go (existing test patterns to extend)
    - internal/connector/example_inproc/postgres_stub.go (reference Connector implementation for AssetIO test fakes)
  </read_first>
  <behavior>
    - Test 1 (builder_test.go): `asset.New("users_clean").Upstream("users_raw").Connector("postgres-prod").Materialize(fn).Register()` returns nil; Default().Get("users_clean") returns the asset; Upstreams() == ["users_raw"]; ConnectorName() == "postgres-prod".
    - Test 2: Variadic Upstream — `New("a").Upstream("b", "c", "d")` produces Upstreams() == ["b","c","d"].
    - Test 3: Builder method chaining is order-independent — `New("a").Materialize(fn).Upstream("b").Connector("c").Retry(p).Resource("r1", 2).Register()` works.
    - Test 4: `Resource("postgres-prod", 0)` defaults Weight to 1 (planner-applied default per D-16).
    - Test 5: Registering without Materialize returns an error (`ErrMissingMaterialize`).
    - Test 6: Registering without Connector returns ErrMissingConnector.
    - Test 7: AssetIO interface has Read(ctx, upstream string) ([]connector.Row, error) and Write(ctx, rows []connector.Row) (int64, error). NewAssetIO(asset, registry, upstreamConnectorMap) constructs an AssetIO that delegates to connector.Registry.Get(name).Read/Write.
    - Test 8 (registry_test.go in connector pkg): `connector.Registry.RegisterInProcess("pg", impl)` is equivalent to existing Register but the method name documents intent. `RegisterPlugin(name, pluginPath)` returns ErrPluginNotImplemented (D-07: deferred until first third-party connector ships).
    - Test 9 (builder_test.go — Build()): `New("a").Connector("c").Materialize(fn).Build()` returns (*Asset, nil); the returned Asset's Name()=="a" and Default().Get("a") returns ErrNotFound (proving Build does NOT register).
    - Test 10 (builder_test.go — Build() validation): `New("").Connector("c").Materialize(fn).Build()` returns (nil, error wrapping a validation error); `New("a").Build()` returns (nil, ErrMissingMaterialize); `New("a").Materialize(fn).Build()` returns (nil, ErrMissingConnector).
    - Test 11 (builder_test.go — Register uses Build): `Register()` after a successful chain produces the same *Asset shape as `Build()` (Name, Upstreams, ConnectorName, RetryPolicy, Resources all equal); only difference is Register also commits to Default().
  </behavior>
  <action>
    Create `internal/asset/builder.go`:
    ```go
    package asset

    import (
        "errors"
        "fmt"
    )

    var (
        ErrMissingMaterialize = errors.New("asset: Materialize(fn) is required before Register/Build")
        ErrMissingConnector   = errors.New("asset: Connector(name) is required before Register/Build")
        ErrEmptyName          = errors.New("asset: New(name) requires non-empty name")
    )

    // Builder accumulates configuration before Register() commits to the global registry.
    // Construct only via New(name). Methods return *Builder for chaining (D-01).
    type Builder struct {
        a *Asset
    }

    // New starts a new Asset definition. Name must be non-empty and unique within the registry.
    func New(name string) *Builder {
        return &Builder{a: &Asset{name: name}}
    }

    // Upstream appends one or more upstream asset names (variadic per D-01).
    // The DAG executor (plan 02-02) reads Asset.Upstreams() to build the dependency graph.
    func (b *Builder) Upstream(names ...string) *Builder {
        b.a.upstreams = append(b.a.upstreams, names...)
        return b
    }

    // Connector binds the asset to a connector by name (D-03). Resolution happens at
    // materialize time via connector.Registry.Get(name).
    func (b *Builder) Connector(name string) *Builder {
        b.a.connectorName = name
        return b
    }

    // Materialize registers the user transformation function (D-04 signature).
    func (b *Builder) Materialize(fn MaterializeFunc) *Builder {
        b.a.materializeFn = fn
        return b
    }

    // Retry overrides the platform default retry policy for this asset (D-15).
    func (b *Builder) Retry(p RetryPolicy) *Builder {
        b.a.retryPolicy = p
        return b
    }

    // Resource attaches a named resource constraint (D-16). Weight defaults to 1 if zero.
    func (b *Builder) Resource(name string, weight int) *Builder {
        if weight <= 0 {
            weight = 1
        }
        b.a.resources = append(b.a.resources, Resource{Name: name, Weight: weight})
        return b
    }

    // Build validates the accumulated configuration and returns the *Asset WITHOUT
    // committing it to the process-global Default() registry.
    //
    // This is the test-friendly construction path: plan 02-02 DAG tests build assets
    // via Build() so they can mutate the in-test graph without polluting the global
    // singleton across test cases. Production code paths use Register() instead.
    //
    // Returns nil + error when:
    //   - name is empty (ErrEmptyName)
    //   - Materialize was not called (ErrMissingMaterialize)
    //   - Connector was not called (ErrMissingConnector)
    func (b *Builder) Build() (*Asset, error) {
        if b.a.name == "" {
            return nil, fmt.Errorf("%w", ErrEmptyName)
        }
        if b.a.materializeFn == nil {
            return nil, fmt.Errorf("%w (asset %q)", ErrMissingMaterialize, b.a.name)
        }
        if b.a.connectorName == "" {
            return nil, fmt.Errorf("%w (asset %q)", ErrMissingConnector, b.a.name)
        }
        return b.a, nil
    }

    // Register validates and commits to the process-global Default() registry.
    // It delegates validation to Build() so the contract is identical: any chain that
    // Build() accepts, Register() accepts, and vice versa.
    func (b *Builder) Register() error {
        a, err := b.Build()
        if err != nil {
            return err
        }
        return Default().Register(a)
    }
    ```

    Create `internal/asset/io.go`:
    ```go
    package asset

    import (
        "context"
        "fmt"

        "github.com/kanpon/data-governance/internal/connector"
    )

    // AssetIO is the user-facing IO contract (D-04). User Materialize functions call
    // io.Read(upstreamName) to read upstream rows and io.Write(rows) to write the asset's
    // own rows — the connector resolution and pooling lives behind this interface.
    type AssetIO interface {
        // Read reads the rows of the named upstream asset using its bound connector.
        // Returns ErrUnknownUpstream if the upstream is not declared in the asset's Upstreams().
        Read(ctx context.Context, upstream string) ([]connector.Row, error)

        // Write writes rows to the asset's own connector target.
        // Returns the connector-reported RowsWritten count.
        Write(ctx context.Context, rows []connector.Row) (int64, error)
    }

    var ErrUnknownUpstream = fmt.Errorf("asset: upstream not declared")

    // ConnectorResolver maps an asset name to the connector instance that materializes it.
    // Plan 02-03 implements this against connector.Registry + the startup config.
    type ConnectorResolver interface {
        Resolve(assetName string) (connector.Connector, connector.AssetRef, error)
    }

    // NewAssetIO constructs the runtime AssetIO for an asset run. The DAG executor (plan 02-02)
    // builds one AssetIO per step and passes it to MaterializeFunc.
    func NewAssetIO(self *Asset, resolver ConnectorResolver) AssetIO {
        return &assetIO{self: self, resolver: resolver}
    }

    type assetIO struct {
        self     *Asset
        resolver ConnectorResolver
    }

    func (io *assetIO) Read(ctx context.Context, upstream string) ([]connector.Row, error) {
        // Enforce that user reads only declared upstreams (catches typos at runtime).
        declared := false
        for _, u := range io.self.upstreams {
            if u == upstream {
                declared = true
                break
            }
        }
        if !declared {
            return nil, fmt.Errorf("%w: %q (declared: %v)", ErrUnknownUpstream, upstream, io.self.upstreams)
        }
        c, ref, err := io.resolver.Resolve(upstream)
        if err != nil {
            return nil, fmt.Errorf("asset: resolve upstream %q: %w", upstream, err)
        }
        resp, err := c.Read(ctx, connector.ReadRequest{Asset: ref})
        if err != nil {
            return nil, fmt.Errorf("asset: connector read %q: %w", upstream, err)
        }
        return resp.Rows, nil
    }

    func (io *assetIO) Write(ctx context.Context, rows []connector.Row) (int64, error) {
        c, ref, err := io.resolver.Resolve(io.self.name)
        if err != nil {
            return 0, fmt.Errorf("asset: resolve self %q: %w", io.self.name, err)
        }
        resp, err := c.Write(ctx, connector.WriteRequest{Asset: ref, Rows: rows})
        if err != nil {
            return 0, fmt.Errorf("asset: connector write %q: %w", io.self.name, err)
        }
        return resp.RowsWritten, nil
    }
    ```

    Edit `internal/connector/registry.go` adding (do NOT remove the existing Register method — keep it as the underlying primitive):
    ```go
    // ErrPluginNotImplemented is returned by RegisterPlugin until Phase 2 third-party
    // plugin scaffolding ships (D-07: interface reserved, implementation deferred).
    var ErrPluginNotImplemented = errors.New("connector: plugin loader not implemented in v1")

    // RegisterInProcess registers an in-process Connector implementation under name.
    // It is the first-party connector loading path (D-06, D-07). Equivalent to Register
    // for now; the separate name documents intent and pairs with RegisterPlugin.
    func (r *Registry) RegisterInProcess(name string, c Connector) error {
        return r.Register(name, c)
    }

    // RegisterPlugin is reserved for hashicorp/go-plugin subprocess loading (D-07).
    // Phase 2 keeps the method shape stable; the implementation is deferred until the
    // first real third-party connector ships, so this returns ErrPluginNotImplemented.
    func (r *Registry) RegisterPlugin(name string, pluginPath string) error {
        return fmt.Errorf("%w (name=%q path=%q)", ErrPluginNotImplemented, name, pluginPath)
    }
    ```

    Add tests in `internal/asset/builder_test.go` for builder chaining + validation + Build(), and extend `internal/connector/registry_test.go` to cover RegisterInProcess (success path) and RegisterPlugin (returns ErrPluginNotImplemented).

    Build()-specific tests in builder_test.go MUST cover (mapping to behaviors 9-11):
    - `TestBuilder_Build_ReturnsAssetWithoutRegistering`: assert Build() returns *Asset, then assert `Default().Get(name)` returns ErrNotFound (via errors.Is).
    - `TestBuilder_Build_ValidationErrors`: assert Build() returns (nil, ErrMissingMaterialize) when Materialize unset; (nil, ErrMissingConnector) when Connector unset; (nil, ErrEmptyName) when New("").
    - `TestBuilder_Build_AndRegister_AreEquivalent`: chain1.Build() and chain2.Register() produce *Assets with identical Name/Upstreams/ConnectorName/RetryPolicy/Resources fields; only difference is registry presence.

    Use `testify/require` for assertions and `t.Cleanup(asset.resetForTest)` (export resetForTest as a package-internal helper accessible from tests in the same package, OR expose via export_test.go). Pick the package-internal approach: builder_test.go is in `package asset` so it sees resetForTest directly.
  </action>
  <verify>
    <automated>cd /home/developer/.kanpon/code/go/data-governance && go test ./internal/asset/... ./internal/connector/... -count=1 -v && go vet ./internal/asset/... ./internal/connector/...</automated>
  </verify>
  <acceptance_criteria>
    - `grep -q "func New(name string) \*Builder" internal/asset/builder.go` succeeds
    - `grep -q "func (b \*Builder) Upstream(names \.\.\.string) \*Builder" internal/asset/builder.go` succeeds
    - `grep -q "func (b \*Builder) Materialize(fn MaterializeFunc) \*Builder" internal/asset/builder.go` succeeds
    - `grep -q "func (b \*Builder) Retry(p RetryPolicy) \*Builder" internal/asset/builder.go` succeeds
    - `grep -q "func (b \*Builder) Resource(name string, weight int) \*Builder" internal/asset/builder.go` succeeds
    - `grep -q "func (b \*Builder) Build() (\*Asset, error)" internal/asset/builder.go` succeeds (REQUIRED — plan 02-02 DAG tests depend on this)
    - `grep -q "func (b \*Builder) Register() error" internal/asset/builder.go` succeeds
    - `grep -q "ErrEmptyName" internal/asset/builder.go` succeeds
    - `grep -q "type AssetIO interface" internal/asset/io.go` succeeds
    - `grep -q "Read(ctx context.Context, upstream string) (\[\]connector.Row, error)" internal/asset/io.go` succeeds
    - `grep -q "func (r \*Registry) RegisterInProcess" internal/connector/registry.go` succeeds
    - `grep -q "func (r \*Registry) RegisterPlugin" internal/connector/registry.go` succeeds
    - `grep -q "ErrPluginNotImplemented" internal/connector/registry.go` succeeds
    - `go test ./internal/asset/... ./internal/connector/... -count=1` exits 0
    - At least one test exercises the full builder chain `New().Upstream().Connector().Materialize().Register()` and asserts it succeeds; another asserts ErrMissingMaterialize and ErrMissingConnector
    - At least one test asserts `connector.Registry.RegisterPlugin(...)` returns an error wrapping ErrPluginNotImplemented (use `errors.Is`)
    - At least three Build()-specific tests exist (verify with `grep -c "TestBuilder_Build" internal/asset/builder_test.go` returns ≥3): Build returns *Asset without registering, Build returns ErrMissingMaterialize/ErrMissingConnector/ErrEmptyName, Build and Register produce equivalent *Asset
    - Build() test for non-registration uses `errors.Is(err, asset.ErrNotFound)` to confirm Default().Get(name) cannot find the built asset
  </acceptance_criteria>
  <done>Builder chain compiles and registers; Build() returns *Asset without committing to Default(); AssetIO interface defined; connector.Registry has RegisterInProcess + RegisterPlugin stub; all tests pass.</done>
</task>

</tasks>

<verification>
端到端 SDK 边界烟雾测试 (两个任务后执行器运行):

```bash
cd /home/developer/.kanpon/code/go/data-governance
go test ./internal/asset/... ./internal/connector/... -count=1
go vet ./internal/asset/... ./internal/connector/...
go build ./...
```

手动合约审查 (尚无代码路径,仅形状):
- 阅读 internal/asset/asset.go 并确认 Asset 是不透明的 (私有字段,仅访问器方法)。
- 阅读 internal/asset/builder.go 并确认链式方法都返回 *Builder。
- 阅读 internal/asset/builder.go 并确认 Build() 返回 (*Asset, error),并被 Register() 消费 (即 Register 的主体调用 b.Build())。
- 阅读 internal/asset/io.go 并确认 AssetIO 接口使用 connector.Row (Phase 1 冻结类型)。
- 确认 `go doc -all github.com/kanpon/data-governance/internal/asset` 列出 New, Builder, Builder.Build, Asset, AssetIO, RetryPolicy, Resource, MaterializeResult, ErrAlreadyRegistered, ErrNotFound, ErrMissingMaterialize, ErrMissingConnector, ErrEmptyName。
</verification>

<threat_model>

## 信义边界

| 边界 | 描述 |
|----------|-------------|
| user-code → asset SDK | 用户提供的 MaterializeFunc 不可信; 可能 panic、泄漏 goroutine 或永远阻塞 |
| asset SDK → connector.Registry | Asset 按名称引用连接器; 不匹配的名称解析为 ErrNotFound,而不是静默失败 |
| asset.Default() 全局状态 | 进程级注册表 — 并发 init() 函数的 Register 竞争 |

## STRIDE 威胁寄存器

| 威胁 ID | 类别 | 组件 | 处理方式 | 缓解计划 |
|-----------|----------|-----------|-------------|-----------------|
| T-02-01-01 | 篡改 | DefinitionRegistry 全局状态 | 缓解 | sync.RWMutex 保护 map; Register 在重复时返回 ErrAlreadyRegistered (无静默覆盖)。任务 1.1 中的测试 3 涵盖并发 Register 隔离。 |
| T-02-01-02 | DoS | MaterializeFunc 无界执行 | 接受 | Plan 02-02 在每个 MaterializeFunc 调用中包装 `recover()` 和 ctx-with-timeout。Phase 2 plan-01 仅定义合约; 运行时强制属于 executor。 |
| T-02-01-03 | 信息泄露 | 通过 Asset getter 泄露 RetryPolicy / Resource | 接受 | Asset 仅暴露构建器提供的非秘密数据 (名称、持续时间、权重)。没有凭证穿过此边界; D-09 将凭证保存在连接器配置中。 |
| T-02-01-04 | 欺骗 | 用户以他人名称注册资产 | 接受 | 按 Phase 2 设计为单租户; 注册表是进程本地的,不需要多租户分离。 |
| T-02-01-05 | 篡改 | AssetIO.Read 返回未声明上游的行 | 缓解 | io.Read 强制上游在 Asset.Upstreams() 中声明,否则返回 ErrUnknownUpstream。测试 7 涵盖这一点。防止 Phase 4 中的 lineage 差距。 |
| T-02-01-06 | EoP | 插件加载器误用 | 缓解 | RegisterPlugin 在 Phase 2 中返回 ErrPluginNotImplemented; 意外执行路径已关闭。 |
| T-02-01-07 | 篡改 | 测试代码意外在生产路径中使用 Build() | 接受 | Build() 是 plan 02-02 DAG 构造的文档化测试辅助函数。捕获绕过全局注册表的生产路径的测试超出范围; SDK README 和 godoc 都注明"生产代码路径使用 Register()"。 |

</threat_model>

<success_criteria>

- [ ] `internal/asset` 包存在,文件: asset.go, builder.go, registry.go, io.go, retry.go (+ tests)
- [ ] Builder 链: `asset.New(name).Upstream(...).Connector(name).Materialize(fn).Retry(p).Resource(name,w).Register()` 编译和工作
- [ ] `Builder.Build() (*Asset, error)` 存在并返回经验证的 *Asset,而不提交到 Default() (由 plan 02-02 DAG 测试消费)
- [ ] DefinitionRegistry 进程级单例; Default() 返回相同实例; Register/Get/List 强制字母顺序和唯一性
- [ ] AssetIO 接口委托给 ConnectorResolver; 强制执行声明的上游检查
- [ ] connector.Registry 有 RegisterInProcess (工作) 和 RegisterPlugin (根据 D-07 返回 ErrPluginNotImplemented)
- [ ] internal/asset/... 和 internal/connector/... 中的所有测试通过 `-count=1`
- [ ] `go vet ./internal/asset/... ./internal/connector/...` exit 0
- [ ] `go build ./...` 整个模块成功

</success_criteria>

<output>
完成后,创建 `.planning/phases/02-execution-engine/02-01-SUMMARY.md` 文档:
- 最终导出的 API 表面 (类型和函数,包括 Builder.Build)
- 测试覆盖总结
- Plan 02-02 / 02-03 / 02-04 将消费的开放钩子 (Asset getter、用于测试的 Builder.Build、AssetIO 接口、ConnectorResolver 接口)
- 与计划的任何偏差及理由
</output>