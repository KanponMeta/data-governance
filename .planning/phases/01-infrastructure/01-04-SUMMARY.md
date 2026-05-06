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

# Phase 01 Plan 04: Connector Interface + Registry + Integration Test Summary

## One-liner

Connector ABI frozen at v1.0.0 with protobuf IDL + Go interface, in-process Registry with APIVersion enforcement, example stub proving clean import boundary, end-to-end integration test covering all Phase 1 acceptance criteria, CI pipeline guarding the contract.

## What Was Built

Plan 04 locks the connector public-API surface that third parties will build against:

1. **Protobuf IDL** (`internal/connector/proto/connector.proto`) - `data_governance.connector.v1` package with `ConnectorService` (Ping, Schema, Read, Write RPCs), all request/response messages, Capabilities. FROZEN comment at top of file.

2. **buf tooling** - `buf.yaml` (v2, STANDARD lint, FILE breaking, PACKAGE_DIRECTORY_MATCH disabled to allow `internal/` layout), `buf.gen.yaml` (buf.build/protocolbuffers/go + buf.build/connectrpc/go plugins). Generated `connector.pb.go` and `connectorv1connect/connector.connect.go`.

3. **Go interface** (`internal/connector/connector.go`) - `Connector` interface mirroring proto exactly with all types: `Capabilities`, `AssetRef`, `Column`, `Row`, request/response structs.

4. **Version constant** (`internal/connector/version.go`) - `APIVersion = "v1.0.0"` with bumping rules in comment.

5. **In-process Registry** (`internal/connector/registry.go`) - Thread-safe `Registry` with `NewRegistry`, `Register` (rejects mismatched APIVersion with `ErrIncompatibleVersion`, duplicate name with `ErrAlreadyRegistered`), `Get` (ErrNotFound), `List` (sorted).

6. **Reference stub** (`internal/connector/example_inproc/postgres_stub.go`) - Imports ONLY `github.com/kanpon/data-governance/internal/connector`. Compile-time assertion `var _ connector.Connector = (*PostgresStub)(nil)`. Package comment explicitly documents the boundary requirement.

7. **Integration test** (`test/integration/integration_test.go`) - `TestPhase1AcceptanceCriteria` with 9 subtests: health, admin bootstrap, login JWT, wrong password 401+problem+json, invite, accept-invite, expired token + event_log verification, RLS append-only enforcement (raw SQL), connector boundary marker.

8. **CI workflow** (`.github/workflows/ci.yml`) - buf lint, atlas migrate lint (warn-only), unit tests, integration tests with platform startup.

## Commits

| Commit | Description |
|--------|-------------|
| 4ac3451 | feat(01-04): connector protobuf IDL + Go interface + buf tooling |
| 4e4b09a | feat(01-04): connector Registry + example in-process stub |
| b59cb30 | feat(01-04): end-to-end integration test + CI workflow |

## Threat Mitigation

| Threat | Mitigation | File |
|--------|------------|------|
| T-04-01 Connector ABI tampering | APIVersion() returns connector.APIVersion; Registry rejects mismatches | registry.go |
| T-04-02 Migration tampering | CI runs atlas migrate lint | ci.yml |
| T-04-03 Connector secrets | Proto comment documents sensitive key convention; Phase 2 will enforce | connector.proto |
| T-04-04 Proto IDL tampering | buf lint + breaking checks in CI | ci.yml |
| T-04-06 Registration repudiation | slog INFO-level logging for audit traceability | registry.go |

## Open Questions for Phase 2

1. **Subprocess transport via go-plugin**: Phase 1 Registry is in-process only. Phase 2 must add `hashicorp/go-plugin` subprocess management with gRPC over stdio for non-Go connectors.

2. **Connector lifecycle events to add to D-10 enum**: Phase 1 event types (D-10) cover auth + platform. Phase 2 should add `connector.registered`, `connector.ping_failed`, `connector.read_error`, `connector.write_error` to the enum.

3. **Secrets vault integration**: Phase 1 proto documents that sensitive config keys should be env-var indirection. Phase 2 should enforce this via a secrets vault (Vault, AWS Secrets Manager, etc.).

4. **Platform service protos**: The `/grpc` subtree is a net/http stub in Phase 1. Phase 2 must define `proto/platform/v1/platform.proto` and replace the stub with connect-go handlers.

## Verification Results

| Check | Result |
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

## Self-Check

All claims verified:
- Commits exist: 4ac3451, 4e4b09a, b59cb30
- Files created: internal/connector/{proto/*.{proto,yaml,gen.yaml},connector.go,version.go,registry.go,registry_test.go,gen/*.pb.go,gen/connectorv1connect/*,example_inproc/*.go}, test/integration/integration_test.go, .github/workflows/ci.yml
- All acceptance criteria from PLAN.md verified via grep + automated checks
- Integration test RLS check uses raw SQL (consistent with plan 02; ent Immutable() prevents UpdateOneID generation)
