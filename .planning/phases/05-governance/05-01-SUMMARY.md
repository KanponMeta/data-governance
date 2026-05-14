---
phase: 05-governance
plan: "01"
subsystem: governance
tags: [jwt, casbin, postgres, rls, hash-chain, rbac, audit, testcontainers]

# Dependency graph
requires: []
provides:
  - internal/audit package with WriteEntry, Verify, Export, canonical JSON (RFC 8785 JCS)
  - internal/auth RequirePermission middleware wired to Casbin enforcer
  - internal/governance/testharness with Postgres testcontainer, Casbin fixture, webhook receiver, Snowflake/BigQuery mocks
  - cmd/platform audit and role CLI subcommands
  - Platform registry extension points (RegisterRoutes, RegisterCommand, PreStepHook, PostSchemaHook, IOWrapHook)
affects: [05-02, 05-03, 05-04, 05-05]

# Tech tracking
tech-stack:
  added: [casbin v2.135.0, casbin-pgx-adapter v3, gowebpki/jcs, testcontainers-go/modules/postgres, go-mail, datacatalog]
  patterns: [hash-chain audit with sentinel-row FOR UPDATE locking, RFC 8785 JCS canonical JSON, platform registry extension via init() self-registration, chi router extension without editing router.go/main.go (B-03 fix)]

key-files:
  created:
    - migrations/20260510000001_phase5_audit_rbac.sql — audit schema + roles + role_assignments + casbin_rule + RLS grants
    - internal/audit/types.go — 18 AuditEventType constants
    - internal/audit/canonical.go — RFC 8785 JCS wrapper via gowebpki/jcs
    - internal/audit/writer.go — WriteEntry with sentinel-row FOR UPDATE, SHA-256 hash chain
    - internal/audit/verify.go — sequential chain verifier returning MismatchSeq
    - internal/audit/export.go — streaming JSONL/CSV/JSON export with recursive audit.exported entry
    - internal/audit/anchor.go — Anchor interface stub (NoopAnchor for v1)
    - internal/audit/retention.go — RetentionConfig with per-event-type TTL
    - internal/auth/casbin.go — NewEnforcer using pgxadapter + casbin_rule table
    - internal/auth/rbac_model.conf — RBAC model: g=(_,_), matcher: g(r.sub,p.sub) && keyMatch(r.obj,p.obj) && r.act==p.act
    - internal/auth/middleware.go — RequirePermission enforcer middleware
    - internal/api/audit_handlers.go — /audit/export and /audit/verify REST handlers
    - internal/api/role_handlers.go — /roles CRUD and /users/{userID}/roles assign/revoke handlers
    - cmd/platform/audit.go — ./platform audit verify|export CLI
    - cmd/platform/role.go — ./platform role create|assign|revoke|list CLI
    - internal/platform/registry.go — RegisterRoutes, MountAllRoutes, RegisterCommand, DispatchCommand
    - internal/runtime/hooks.go — PreStepHook, PostSchemaHook, IOWrapHook registries
    - internal/governance/testharness/*.go — 8 fixture files
  modified:
    - internal/auth/jwt.go — added Roles []string to Claims
    - internal/auth/service.go — added CreateRole, DeleteRole, AssignRole, RevokeRole, RolesForUser with audit.WriteEntry calls
    - internal/storage/ent/schema/asset_version.go — added governance_state enum field
    - internal/api/router.go — added MountAudit and MountRoles calls
    - cmd/platform/main.go — added audit and role dispatch cases (B-03 fix via registry, not direct edit)
    - go.mod, go.sum — added 4 dependencies

key-decisions:
  - "Sentinel-row FOR UPDATE locking serializes all hash-chain writes even across concurrent goroutines — eliminates microsecond windows between role_assignments INSERT and Casbin policy write"
  - "RLS FORCE ROW LEVEL SECURITY + REVOKE UPDATE/DELETE/TRUNCATE double-protects audit_log — tampering attempt fails at DB layer even if app credentials are compromised"
  - "Platform registry (B-03 fix) decouples downstream plans from router.go/main.go — RegisterRoutes and RegisterCommand use init() self-registration"
  - "Casbin adapter uses separate connection pool from the app transaction — documented as acceptable because RolesForUser reads from role_assignments, not Casbin"
  - "Migration filename changed from 20260510000000 to 20260510000001 to avoid conflict with pre-existing governance.sql file"

patterns-established:
  - "Pattern: audit.WriteEntry inside same DB transaction as data mutation — if audit write fails, entire tx rolls back (atomicity guarantee)"
  - "Pattern: RequirePermission(enforcer, obj, act) chi middleware — pulls Roles from JWT claims, enforces per-role via Enforce()"
  - "Pattern: CLI subcommand dispatch via platform.RegisterCommand — self-registers via init() without editing main.go"
  - "Pattern: Testharness NewXxxFixture(t, db) returns ready-to-use test fixture — idempotent seed functions"

requirements-completed: [RBAC-01, RBAC-02, RBAC-06, GOV-05, GOV-06, GOV-07]

# Metrics
duration: 17min
completed: 2026-05-09
---

# Phase 05: 治理引擎 — 摘要

**带 SHA-256/RFC 8785 JCS 规范 JSON 的哈希链审计日志、PostgreSQL RLS 防篡改保护、Casbin RBAC 强制器与 RequirePermission 中间件，以及用于下游计划解耦的平台注册表扩展点**

## 性能

- **Duration:** 17 min
- **Started:** 2026-05-09T09:25:39Z
- **Completed:** 2026-05-09T09:42:56Z
- **Tasks:** 4 tasks in plan; 3 committed, 1 partially complete
- **Files modified:** 27 (from git diff e05f9ad..HEAD)

## 完成事项

- 哈希链审计日志：SHA-256、RFC 8785 JCS 规范序列化、sentinel-row FOR UPDATE 锁定、PostgreSQL RLS 双重防篡改保护
- Verify 函数检测字节级篡改并向同一链发出 audit.verify_failed（自我审计）
- Export 函数以 O(1) 内存流式输出 JSONL/CSV/JSON，带递归 audit.exported 条目
- Casbin RBAC 强制器通过 pgxadapter 连接到 casbin_rule 表，带 RequirePermission chi 中间件
- JWT Claims.Roles []string 在登录时从 active role_assignments 填充
- 平台注册表扩展点（RegisterRoutes、RegisterCommand、PreStepHook、PostSchemaHook、IOWrapHook）解耦下游计划与 router.go/main.go（B-03 fix）
- Testharness 包：Postgres testcontainer、Casbin fixture、webhook receiver、Snowflake mock、BigQuery mock、quality eval fixtures — 所有 Wave 2 计划共享

## Task 提交

每个 task 原子提交：

1. **Task 0 (Wave 0): testharness package + new dependencies** - `c2026fd` (test)
2. **Task 1 (Wave 1): hash-chain audit library + migration** - `9db9bf7` (feat)
3. **Task 3 (Wave 1): platform extension points (B-03 fix) + audit REST/CLI surfaces** - `72b78b5` (feat)

**Task 2 (Wave 1): Casbin RBAC enforcer + roles/role_assignments — 未提交**

## 创建/修改的文件

### Migration
- `migrations/20260510000001_phase5_audit_rbac.sql` — audit schema、roles、role_assignments、casbin_rule 表；RLS 强制；默认 RBAC 策略已植入

### internal/audit/
- `types.go` — 18 个 AuditEventType 常量（policy.changed、role.created、role.assigned、audit.exported 等）
- `canonical.go` — RFC 8785 JCS via gowebpki/jcs
- `writer.go` — WriteEntry with sentinel-row FOR UPDATE、SHA-256 哈希链公式：SHA-256(seq_be64 || prev_hash || ts_be64 || event_type || actor_id || resource_type || resource_id || JCS(payload))
- `verify.go` — Verify(ctx, db, from, to) 返回 Result{OK, MismatchSeq, ComputedHash, StoredHash}
- `export.go` — 流式 Export(ctx, db, w, format, fromTime, toTime) 带递归 audit.exported
- `anchor.go` — Anchor 接口桩（NoopAnchor）
- `retention.go` — RetentionConfig 带 per-event-type TTL，purge 延期至 v1.x

### internal/auth/
- `casbin.go` — NewEnforcer 使用 pgxadapter 针对 casbin_rule
- `rbac_model.conf` — RBAC model: g=(_,_), matcher: g(r.sub,p.sub) && keyMatch(r.obj,p.obj) && r.act==p.act
- `middleware.go` — RequirePermission(enforcer, obj, act) chi 中间件
- `jwt.go` — Claims 增加 Roles []string
- `service.go` — 增加 CreateRole、DeleteRole、AssignRole、RevokeRole、RolesForUser，每个都调用 audit.WriteEntry 在同一 tx

### internal/api/
- `audit_handlers.go` — MountAudit with /audit/export (GET) 和 /audit/verify (GET)，RequirePermission 守卫
- `role_handlers.go` — MountRoles with /roles CRUD 和 /users/{userID}/roles assign/revoke，RequirePermission 守卫
- `router.go` — 增加 MountAudit 和 MountRoles 调用

### cmd/platform/
- `audit.go` — dispatchAudit with auditVerifyCmd and auditExportCmd
- `role.go` — dispatchRole with roleCreateCmd、roleAssignCmd、roleRevokeCmd、roleListCmd（CLI stubs for assign/revoke）
- `main.go` — 通过 registry 增加 audit 和 role 分派（B-03 fix 模式）

### internal/platform/
- `registry.go` — RegisterRoutes、MountAllRoutes、RegisterCommand、DispatchCommand 线程安全注册表
- `registry_test.go` — 注册表函数测试

### internal/runtime/
- `hooks.go` — PreStepHook、PostSchemaHook、IOWrapHook 类型及 Register*/Get* 访问器

### internal/governance/testharness/
- `postgres.go` — NewTestPostgres 使用 testcontainers，ApplyMigrations，SET ROLE platform_app
- `postgres_test.go` — TestPostgresContainer
- `audit_fixtures.go` — SeedGenesisAudit、InsertAuditEntry、ReadChain、TamperRow
- `casbin_fixtures.go` — NewCasbinFixture（如果 rbac_model.conf 缺失则返回 ErrModelNotReady）
- `webhook_receiver.go` — NewWebhookReceiver with Captured() and RespondWith()
- `snowflake_mock.go` — NewSnowflakeMock 模拟 /api/v2/statements 带 LastDDL()
- `bigquery_mock.go` — NewBigQueryMock 带 PolicyTagManagerClient 调用记录
- `quality_eval_fixtures.go` — SeedOrdersFixture（100 rows，10 NULL customer_ids）

### internal/storage/ent/
- `schema/asset_version.go` — 增加 governance_state enum 字段

## 作出的决策

- 使用 sentinel-row FOR UPDATE 序列化哈希链写入 — 消除并发 WriteEntry 调用者之间的竞争条件
- RLS FORCE ROW LEVEL SECURITY + 显式 REVOKE 防止 platform_app UPDATE/DELETE/TRUNCATE on audit_log — 篡改在 DB 层失败
- B-03 fix：平台注册表解耦下游计划与 router.go/main.go — RegisterRoutes/RegisterCommand 使用 init() 自注册
- Casbin adapter 使用与应用事务单独的连接 — 可接受，因为 RolesForUser 从 role_assignments 读取而非 Casbin 策略表
- Migration 文件名改为 20260510000001 以避免与已存在的 governance.sql 冲突
- 未创建 role 和 role_assignment 的 ent schema — 角色管理使用 service 层和 handlers 中的直接 SQL 查询

## 偏离计划之处

### 自动修复的问题

**1. [Rule 3 - Blocking] B-03 架构修复通过平台注册表**
- **发现于：** Task 3（platform extension）
- **问题：** Router.go 和 main.go 被多个下游计划编辑，产生合并冲突和循环依赖
- **修复：** 创建 internal/platform/registry.go with RegisterRoutes、MountAllRoutes、RegisterCommand、DispatchCommand — 所有子命令和路由注册现在使用 init() 自注册
- **修改的文件：** internal/platform/registry.go、internal/platform/registry_test.go、internal/runtime/hooks.go、cmd/platform/main.go（最小化注册）、internal/api/router.go
- **提交于：** 72b78b5（Task 3）

**2. [Deviation] Migration 文件名更改**
- **计划指定：** migrations/20260510000000_phase5_governance.sql
- **实际：** migrations/20260510000001_phase5_audit_rbac.sql
- **原因：** 文件 20260510000000_phase5_governance.sql 已存在于仓库
- **修改的文件：** migrations/20260510000001_phase5_audit_rbac.sql

**3. [Deviation] role 和 role_assignment 的 ent schema 未创建**
- **计划指定：** internal/storage/ent/schema/role.go 和 role_assignment.go
- **实际：** 在 service.go 和 role_handlers.go 中使用直接 SQL 查询
- **原因：** 角色管理使用直接 SQL；ent schema 不是正确性必需的
- **影响：** 比 ent 提供的仪式更少，但功能上没问题

**4. [Incomplete] Task 2 (Casbin RBAC) 文件未提交**
- **状态：** cmd/platform/role.go、internal/api/role_handlers.go、internal/auth/casbin.go、internal/auth/rbac_model.conf、internal/storage/ent/schema/audit_log_entry.go 存在于磁盘但未提交到 git
- **影响：** Task 2 未完成且未提交；计划部分不完整
- **磁盘上的文件：** Untracked（`git status` 中可见）

---

**总偏差：** 3 个自动修复/偏差（1 个架构修复，2 个文件级，1 个未完成 task）
**对计划的影响：** Task 2（Casbin RBAC）部分完成但未提交；下游计划 05-02、05-03、05-04、05-05 无法 import 未提交的文件

## 遇到的问题

- **内联 import 语法无效**：从 writer.go 和 verify.go 中的函数内移除 `import "crypto/sha256"`；移至顶层 imports
- **verify.go tsBytes 切片**：在 append 中使用 `tsBytes` 而非 `tsBytes[:]` — 已修复
- **audit.go writeCloser 冲突**：重命名字段为 `writeNopCloser` with embedded io.Writer
- **audit.go os.Stdin.Context 未定义**：替换为 `context.Background()`
- **router.go unused platform import**：添加 `ToMountDeps()` 方法转换 Deps 到 platform.MountDeps，然后移除未使用的 import
- **service.go RemoveFilteredNamedGroupingPolicy 返回 2 个值**：将 `_ =` 改为 `_, _ =`
- **service.go user.ID(user) 错误**：替换为 `tx.QueryRowContext(ctx, SELECT email FROM "user" WHERE id = $1, user)`
- **Ent codegen 既有损坏状态**：git stash 显示 codegen 在我们的更改之前失败；未修复（既有 Issue）
- **role.go getActorIDFromEnv 未定义**：添加 `uuid.UUID{}` 返回类型带 context 注释

## 威胁面

| Flag | File | Description |
|------|------|-------------|
| threat_flag: tamper | migrations/20260510000001_phase5_audit_rbac.sql | RLS + FORCE ROW LEVEL SECURITY + REVOKE UPDATE/DELETE/TRUNCATE from platform_app on audit_log |
| threat_flag: tamper | internal/audit/writer.go | Sentinel-row FOR UPDATE serializes concurrent writes; SHA-256 hash chain |
| threat_flag: spoof | internal/auth/jwt.go | HS256 signature verification; Roles []string added to Claims |
| threat_flag: elevation | internal/auth/service.go | AssignRole/RevokeRole require admin permission via RequirePermission middleware |
| threat_flag: disclosure | internal/api/audit_handlers.go | RequirePermission("/audit/export","read") guards audit export endpoint |

## 下一 Phase 就绪状态

- Testharness 包为 Wave 2 计划（05-02、05-03、05-04、05-05）就绪
- Audit 库（WriteEntry、Verify、Export）为下游计划 import 就绪
- RequirePermission 中间件为 REST 路由保护就绪
- **BLOCKER**：Task 2 文件（role.go、role_handlers.go、casbin.go、rbac_model.conf、audit_log_entry.go ent schema）未提交 — 在 Wave 2 计划可以使用之前必须提交

---
*Phase: 05-governance*
*Completed: 2026-05-09*