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

# Phase 2 Plan 01: Asset DSL + DefinitionRegistry + AssetIO contract Summary

**One-liner:** Functional builder DSL (`asset.New().Upstream().Connector().Materialize().Register()`) with process-global DefinitionRegistry, AssetIO connector-abstraction interface, and connector.Registry extensions for in-process loading.

## Exported API Surface

### internal/asset

| Identifier | Kind | Description |
|---|---|---|
| `New(name string) *Builder` | func | Entry point for asset definition DSL |
| `Builder` | type | Accumulates asset config via chaining; committed with Register() or validated with Build() |
| `Builder.Upstream(names ...string) *Builder` | method | Variadic upstream dependency declaration |
| `Builder.Connector(name string) *Builder` | method | Bind asset to connector by name (resolved at materialize-time) |
| `Builder.Materialize(fn MaterializeFunc) *Builder` | method | Register user transformation function |
| `Builder.Retry(p RetryPolicy) *Builder` | method | Override platform default retry policy |
| `Builder.Resource(name string, weight int) *Builder` | method | Attach named resource constraint (weight defaults to 1 if ≤0) |
| `Builder.Build() (*Asset, error)` | method | Validate and return *Asset WITHOUT registering — test path for plan 02-02 |
| `Builder.Register() error` | method | Validate and commit to Default() registry |
| `Asset` | type | Immutable runtime representation; all fields private, accessed via methods |
| `Asset.Name() string` | method | Unique asset identifier |
| `Asset.Upstreams() []string` | method | Defensive copy of upstream names |
| `Asset.ConnectorName() string` | method | Bound connector name |
| `Asset.MaterializeFn() MaterializeFunc` | method | User-supplied transform function |
| `Asset.RetryPolicy() RetryPolicy` | method | Per-asset retry config |
| `Asset.Resources() []Resource` | method | Defensive copy of resource constraints |
| `MaterializeFunc` | type | `func(ctx context.Context, io AssetIO) (MaterializeResult, error)` |
| `MaterializeResult` | type | `{RowsWritten int64, Metadata map[string]any}` |
| `RetryPolicy` | type | `{Max int, InitialDelay, MaxDelay time.Duration, JitterPct int}` |
| `RetryPolicy.IsZero() bool` | method | True when policy is unset (engine applies platform default) |
| `DefaultRetryPolicy() RetryPolicy` | func | Zero-value policy |
| `Resource` | type | `{Name string, Weight int}` |
| `AssetIO` | interface | `Read(ctx, upstream) ([]connector.Row, error)` + `Write(ctx, rows) (int64, error)` |
| `ConnectorResolver` | interface | `Resolve(assetName) (connector.Connector, connector.AssetRef, error)` — plan 02-03 implements |
| `NewAssetIO(self, resolver) AssetIO` | func | Construct runtime AssetIO; DAG executor builds one per step |
| `DefinitionRegistry` | type | Thread-safe asset registry |
| `NewDefinitionRegistry() *DefinitionRegistry` | func | Construct empty registry |
| `DefinitionRegistry.Register(*Asset) error` | method | Add asset; ErrAlreadyRegistered on duplicate |
| `DefinitionRegistry.Get(name) (*Asset, error)` | method | Lookup by name; ErrNotFound if absent |
| `DefinitionRegistry.List() []string` | method | All names, alphabetically sorted |
| `Default() *DefinitionRegistry` | func | Process-global singleton (D-05) |
| `ErrAlreadyRegistered` | var | Sentinel: duplicate registration |
| `ErrNotFound` | var | Sentinel: asset not in registry |
| `ErrMissingMaterialize` | var | Sentinel: Build/Register without Materialize call |
| `ErrMissingConnector` | var | Sentinel: Build/Register without Connector call |
| `ErrEmptyName` | var | Sentinel: New("") with empty name |
| `ErrUnknownUpstream` | var | Sentinel: AssetIO.Read with undeclared upstream |

### internal/connector (extensions)

| Identifier | Kind | Description |
|---|---|---|
| `Registry.RegisterInProcess(name, impl) error` | method | First-party in-process connector loading (D-07) |
| `Registry.RegisterPlugin(name, pluginPath) error` | method | Deferred: returns ErrPluginNotImplemented |
| `ErrPluginNotImplemented` | var | Sentinel: plugin loader not implemented in v1 (D-07) |

## Test Coverage Summary

**internal/asset:** 19 tests covering:
- Asset accessor correctness + defensive copy behavior
- RetryPolicy.IsZero(), DefaultRetryPolicy()
- MaterializeResult, Resource field access
- DefinitionRegistry Register/Get/List (success, ErrAlreadyRegistered, ErrNotFound, sorted list)
- Default() singleton identity + resetForTest() clears state
- Builder full chain registration
- Variadic Upstream()
- Order-independent chaining
- Resource weight=0 defaulting to 1
- ErrMissingMaterialize, ErrMissingConnector
- AssetIO.Read: declared vs undeclared upstream enforcement (T-02-01-05)
- AssetIO.Write: delegation to connector
- Build() returns *Asset without registering (ErrNotFound confirmed)
- Build() validation errors (ErrEmptyName, ErrMissingMaterialize, ErrMissingConnector)
- Build() and Register() produce equivalent *Asset fields
- Duplicate Register returns ErrAlreadyRegistered

**internal/connector:** 3 new tests added:
- RegisterInProcess success path
- RegisterInProcess ErrAlreadyRegistered
- RegisterPlugin returns ErrPluginNotImplemented (errors.Is confirmed)

## Open Hooks for Downstream Plans

| Plan | Consumed API | Usage |
|------|-------------|-------|
| 02-02 (DAG executor) | `Builder.Build()`, `Asset.Upstreams()`, `Asset.MaterializeFn()` | Build test assets without polluting global registry; traverse dependency graph; invoke MaterializeFunc per step |
| 02-03 (Retry + concurrency) | `Asset.RetryPolicy()`, `Asset.Resources()`, `ConnectorResolver` interface | Implement ConnectorResolver against connector.Registry + startup config; token pool acquisition uses Resources() |
| 02-04 (CLI + PG connector) | `asset.Default().Get(name)`, `connector.Registry.RegisterInProcess()` | `materialize <asset>` CLI looks up asset; PostgreSQL connector registers via RegisterInProcess |

## Deviations from Plan

None — plan executed exactly as written.

The only implementation note: `go build ./...` fails on a pre-existing `go.sum` entry for `ariga.io/atlas` MySQL internals (`golang.org/x/mod/semver`). This is unrelated to this plan — the `internal/asset` and `internal/connector` packages build cleanly. The pre-existing build issue is out of scope for this plan.

## Known Stubs

- `connector.Registry.RegisterPlugin`: returns `ErrPluginNotImplemented` intentionally (D-07 deferred). Documented in godoc. Plan 02-05 or a future phase adds the hashicorp/go-plugin implementation.
- `AssetIO` implementation buffers all rows in memory (no streaming). Performance detail not in user contract — plan 02-03 may optimize if needed.
- `ConnectorResolver` interface is defined but not implemented in this plan — plan 02-03 implements it against connector.Registry + startup config.

## Threat Flags

None — no new network endpoints, auth paths, or trust boundaries introduced. All identified threats (T-02-01-01 through T-02-01-07) are addressed as specified in the plan's threat model.

## Self-Check: PASSED

All 8 created files confirmed present on disk.
Task commits confirmed: `a4d063f` (Task 1), `b53b69a` (Task 2).
