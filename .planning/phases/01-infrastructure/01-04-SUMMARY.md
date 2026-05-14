---
phase: 01-infrastructure
plan: 04
status: complete
tags:
  - connector
  - protobuf
  - registry
  - integration
  - ci
  - buf
  - go-plugin

dependency_graph:
  requires:
    - 01-01 (go.mod, module structure, docker-compose)
    - 01-02 (storage layer, ent schemas, event Writer, migrations)
    - 01-03 (TokenIssuer, HashPassword, Config)
    - 01-05 (auth Service, router, platform binary)
  provides:
    - internal/connector/proto/connector.proto (frozen v1 ABI)
    - internal/connector/connector.go (Go interface + types)
    - internal/connector/registry.go (in-process Registry)
    - internal/connector/example_inproc/postgres_stub.go (reference stub)
    - test/integration/integration_test.go (Phase 1 acceptance)
    - .github/workflows/ci.yml (CI pipeline)
  affects:
    - Phase 2 (go-plugin subprocess transport layer)

tech_stack:
  added:
    - connectrpc.com/connect v1.19.2 (protobuf Go plugin)
  patterns:
    - FROZEN ABI comment in proto header
    - APIVersion constant as single source of truth
    - Thread-safe Registry with sync.RWMutex
    - TDD for Registry (RED: tests first, GREEN: implementation)
    - Import boundary verification via `go list` exec in test

key_files:
  created:
    - internal/connector/proto/connector.proto (service ConnectorService, 4 RPCs, all messages)
    - internal/connector/proto/buf.yaml (v2, STANDARD lint, FILE breaking, PACKAGE_DIRECTORY_MATCH disabled)
    - internal/connector/proto/buf.gen.yaml (buf.build/protocolbuffers/go + buf.build/connectrpc/go)
    - internal/connector/version.go (APIVersion = "v1.0.0")
    - internal/connector/connector.go (Connector interface + all types)
    - internal/connector/gen/connector.pb.go (generated)
    - internal/connector/gen/connectorv1connect/connector.connect.go (generated)
    - internal/connector/registry.go (Registry, NewRegistry, Register, Get, List, ErrIncompatibleVersion, ErrAlreadyRegistered, ErrNotFound)
    - internal/connector/registry_test.go (6 test functions, concurrent safety)
    - internal/connector/example_inproc/postgres_stub.go (compile-time assertion, clean import boundary)
    - internal/connector/example_inproc/postgres_stub_test.go (TestStubRegistersAndPings, TestImportBoundary)
    - test/integration/integration_test.go (9 subtests for Phase 1 acceptance)
    - .github/workflows/ci.yml (buf lint, atlas lint, unit + integration tests)
  modified:
    - Makefile (proto-lint, proto-generate, proto-breaking, integration targets)
    - go.mod (added connectrpc.com/connect v1.19.2)
    - go.sum (updated checksums)

decisions:
  - buf PACKAGE_DIRECTORY_MATCH disabled (proto at internal/connector/proto/ not data_governance/connector/v1/)
  - Integration test uses raw SQL UPDATE/DELETE for RLS verification (consistent with plan 02 approach; ent Immutable() prevents UpdateOneID from being generated)
  - Third-party connector import boundary enforced by TestImportBoundary which runs `go list` and greps the import graph

metrics:
  duration_minutes: "~30"
  tasks_completed: 3
  commits: 3
  files_created: 14
  files_modified: 3
  completed_date: "2026-05-06"

requirements:
  CONN-08: "Connector interface defined as versioned protobuf IDL + Go interface — LOCKED at v1.0.0"
  CORE-04: "Connector interface + Registry prove ABI stability for third-party adoption"

deviations: []
auth_gates: []
---

# Phase 01 Plan 04: 连接器接口 + 注册表 + 集成测试总结

## 一句话总结

连接器 ABI 以 protobuf IDL + Go 接口的形式冻结在 v1.0.0，带 APIVersion 强制执行的就地注册表，证明清晰导入边界的示例桩，端到端集成测试覆盖所有 Phase 1 验收标准，保护契约的 CI 流水线。

## 已构建内容

Plan 04 锁定第三方将构建的连接器公共 API surface:

1. **Protobuf IDL** (`internal/connector/proto/connector.proto`) - `data_governance.connector.v1` 包，包含 `ConnectorService` (Ping, Schema, Read, Write RPC)、所有请求/响应消息、Capabilities。文件顶部有 FROZEN 注释。

2. **buf 工具** - `buf.yaml` (v2, STANDARD lint, FILE breaking, PACKAGE_DIRECTORY_MATCH 禁用以允许 `internal/` 布局)、`buf.gen.yaml` (buf.build/protocolbuffers/go + buf.build/connectrpc/go 插件)。生成 `connector.pb.go` 和 `connectorv1connect/connector.connect.go`。

3. **Go 接口** (`internal/connector/connector.go`) - `Connector` 接口精确镜像 proto，包含所有类型: `Capabilities`、`AssetRef`、`Column`、`Row`、请求/响应结构体。

4. **版本常量** (`internal/connector/version.go`) - `APIVersion = "v1.0.0"`，注释中包含版本更新规则。

5. **就地注册表** (`internal/connector/registry.go`) - 线程安全的 `Registry`，包含 `NewRegistry`、`Register` (拒绝不匹配的 APIVersion 并抛出 `ErrIncompatibleVersion`，重复名称抛出 `ErrAlreadyRegistered`)、`Get` (ErrNotFound)、`List` (排序)。

6. **参考桩** (`internal/connector/example_inproc/postgres_stub.go`) - 仅导入 `github.com/kanpon/data-governance/internal/connector`。编译时断言 `var _ connector.Connector = (*PostgresStub)(nil)`。包注释明确记录边界要求。

7. **集成测试** (`test/integration/integration_test.go`) - `TestPhase1AcceptanceCriteria`，包含 9 个子测试: health、admin bootstrap、login JWT、错误密码 401+problem+json、invite、accept-invite、过期令牌 + event_log 验证、RLS 追加-only 强制执行 (原始 SQL)、连接器边界标记。

8. **CI 工作流** (`.github/workflows/ci.yml`) - buf lint、atlas migrate lint (警告模式)、单元测试、带平台启动的集成测试。

## 提交

| 提交 | 描述 |
|--------|-------------|
| 4ac3451 | feat(01-04): connector protobuf IDL + Go interface + buf tooling |
| 4e4b09a | feat(01-04): connector Registry + example in-process stub |
| b59cb30 | feat(01-04): end-to-end integration test + CI workflow |

## 威胁缓解

| 威胁 | 缓解措施 | 文件 |
|--------|------------|------|
| T-04-01 Connector ABI tampering | APIVersion() returns connector.APIVersion; Registry rejects mismatches | registry.go |
| T-04-02 Migration tampering | CI runs atlas migrate lint | ci.yml |
| T-04-03 Connector secrets | Proto comment documents sensitive key convention; Phase 2 will enforce | connector.proto |
| T-04-04 Proto IDL tampering | buf lint + breaking checks in CI | ci.yml |
| T-04-06 Registration repudiation | slog INFO-level logging for audit traceability | registry.go |

## Phase 2 的待解决问题

1. **通过 go-plugin 的子进程传输**: Phase 1 Registry 仅支持就地。Phase 2 必须添加 `hashicorp/go-plugin` 子进程管理，通过 gRPC over stdio 支持非 Go 连接器。

2. **需要添加到 D-10 枚举的连接器生命周期事件**: Phase 1 事件类型 (D-10) 覆盖 auth + platform。Phase 2 应将 `connector.registered`、`connector.ping_failed`、`connector.read_error`、`connector.write_error` 添加到枚举中。

3. **密钥保管库集成**: Phase 1 proto 记录敏感配置密钥应通过 env-var 间接寻址。Phase 2 应通过密钥保管库 (Vault、AWS Secrets Manager 等) 强制执行此操作。

4. **平台服务 proto**: `/grpc` 子树在 Phase 1 中是 net/http 桩。Phase 2 必须定义 `proto/platform/v1/platform.proto` 并用 connect-go 处理程序替换该桩。

## 验证结果

| 检查 | 结果 |
|-------|--------|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test -race -count=1` | PASS (all packages) |
| `buf lint` | PASS |
| `grep "service ConnectorService" internal/connector/proto/connector.proto` | PASS |
| `grep "APIVersion = \"v1.0.0\"" internal/connector/version.go` | PASS |
| `grep "type Connector interface" internal/connector/connector.go` | PASS |
| `grep "var _ connector.Connector" internal/connector/example_inproc/postgres_stub.go` | PASS |
| `TestImportBoundary` in example_inproc | PASS (import graph verified) |
| CI YAML parses as valid YAML | PASS |
| `grep "atlas migrate lint" ci.yml` | PASS |
| `grep "buf lint" ci.yml` | PASS |
| `grep "go test -tags=integration" ci.yml` | PASS |

## 自我检查

所有声明已验证:
- 提交存在: 4ac3451, 4e4b09a, b59cb30
- 创建的文件: internal/connector/{proto/*.{proto,yaml,gen.yaml},connector.go,version.go,registry.go,registry_test.go,gen/*.pb.go,gen/connectorv1connect/*,example_inproc/*.go}, test/integration/integration_test.go, .github/workflows/ci.yml
- 所有来自 PLAN.md 的验收标准已通过 grep + 自动化检查验证
- 集成测试 RLS 检查使用原始 SQL (与 plan 02 一致；ent Immutable() 阻止 UpdateOneID 生成)