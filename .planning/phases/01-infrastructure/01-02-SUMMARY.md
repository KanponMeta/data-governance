---
phase: 01-infrastructure
plan: 02
status: complete
tags:
  - postgres
  - ent
  - atlas
  - event-log
  - rls
  - storage

dependency_graph:
  requires:
    - 01-01 (go.mod, module structure, docker-compose)
  provides:
    - internal/storage/ent/schema/{user,invite_token,event_log}.go (ent schemas)
    - internal/storage/ent/ (generated client code)
    - internal/storage/storage.go (Storage interface)
    - internal/storage/postgres.go (PostgreSQL implementation)
    - internal/event/{event,types,writer}.go (EventWriter API)
    - migrations/ (Atlas SQL migrations with RLS)
    - atlas.hcl (Atlas configuration)
  affects:
    - all downstream Phase 1 plans (01-03 auth, 01-04 CI, 01-05 connectors)

tech_stack:
  added:
    - entgo.io/ent v0.14.6 (ORM + codegen)
    - github.com/jackc/pgx/v5 v5.9.2 (PostgreSQL driver)
    - github.com/stretchr/testify v1.11.1 (testing)
    - atlas.hcl (Atlas migration configuration)
  patterns:
    - ent schema with Immutable() and Sensitive() field annotations
    - PostgreSQL RLS (Row Level Security) for append-only enforcement
    - Storage interface pattern for testability
    - Event-sourcing-lite with typed payloads serialized to JSONB

key_files:
  created:
    - internal/storage/ent/schema/user.go (email unique, password_hash sensitive, role/status enums)
    - internal/storage/ent/schema/invite_token.go (token_hash sensitive, email indexed)
    - internal/storage/ent/schema/event_log.go (all fields Immutable(), payload JSONB)
    - internal/storage/ent/ (generated: client.go, user.go, invitetoken.go, eventlog.go, etc.)
    - internal/storage/storage.go (Storage interface: Ping, Ent, DB, WithTx, Close)
    - internal/storage/postgres.go (NewPostgres using pgx/v5/stdlib + ent)
    - internal/storage/postgres_test.go (TestPing, TestEventLogIsAppendOnly, TestWithTxRollsBackOnError)
    - internal/event/event.go (Event struct, Writer interface)
    - internal/event/types.go (7 EventType constants per D-10, typed payloads)
    - internal/event/writer.go (NewWriter, Append with validation, JSONB serialization)
    - internal/event/writer_test.go (TestAppend_RejectsEmptyType, TestAppend_RejectsUnknownType, TestAppend_PersistsAndIsImmutable, TestEventTypeStringsMatchD10)
    - atlas.hcl (local + ci environments, ent provider)
    - migrations/20260506062521_initial.sql (CREATE TABLE user, invite_token, event_log + RLS)
    - migrations/atlas.sum (checksum file)
    - cmd/platform/main.go (start/migrate/healthcheck subcommands)
    - Makefile (migrate, migrate-diff, migrate-lint, migrate-status targets)
  modified:
    - go.mod (go 1.25, replace directive for local module, pgx/v5, testify)
    - go.sum (updated checksums)

decisions:
  - ent Schema annotation used to set singular table names: user, invite_token, event_log (per D-07/D-08)
  - Storage.DB() method exposed for test access to raw *sql.DB (enables RLS verification via raw SQL)
  - event Writer validates event_type against AllPhase1Types() before DB write
  - atlas migrate diff --env local used to generate initial migration; RLS section appended manually
  - platform_app role gets GRANT SELECT,INSERT on event_log; platform_owner role owns all tables
  - No ent edges between User/InviteToken/EventLog (event_log is append-only log, FK coupling undesirable)

metrics:
  duration_minutes: ~45
  completed_date: "2026-05-06T06:10:00Z"
  tasks_completed: 4
  commits: 4
  files_created: 18
  files_modified: 4

deviations:
  - go.mod updated to go 1.25.0 (original 1.22 incompatible with golang.org/x/sync v0.20.0 transitive dep)
  - ariga.io/atlas-provider-ent package not available on proxy; used atlas CLI directly for migration diff
  - github.com/cheikhathch/omitempty removed from go.mod (non-existent repository)
  - ent Schema.Annotations() used with entsql.Annotation{Table: "..."} for custom table names (not annotation.Schema)

---

# Phase 01 Plan 02: 元数据持久层总结

## 一句话总结

PostgreSQL 存储层，使用 ent schema 定义 User/InviteToken/EventLog，带 RLS 强制执行的追加-only event_log 的 Atlas 迁移，以及在 JSONB 中验证并持久化 Phase 1 事件的类型化 EventWriter API。

## 已构建内容

Plan 02 建立所有下游阶段依赖的元数据持久层:

1. **ent schemas** - User (email, password_hash, role, status), InviteToken (token_hash, email, invited_by, expires_at), EventLog (occurred_at, event_type, actor_id, resource_type, resource_id, payload JSONB)，带有适当的字段注解 (Immutable, Sensitive)
2. **Atlas 迁移** - PostgreSQL 16 的声明式迁移，包含幂等角色创建和 RLS 强制执行
3. **Storage 接口** - 可测试性边界，包含 Ping, Ent, DB, WithTx, Close 方法
4. **PostgreSQL 实现** - 使用 pgx/v5/stdlib 驱动的 ent 客户端封装
5. **EventWriter API** - 追加-only 写入器，带有 D-10 事件类型验证和类型化有效载荷序列化到 JSONB
6. **平台命令** - start (发出 platform.started)、migrate (调用 atlas CLI 然后发出 platform.migration_applied)、healthcheck (退出 0)

## 提交

| 提交 | 描述 |
|--------|-------------|
| 37aecab | feat(01-02): add ent schemas for User, InviteToken, EventLog |
| 6b35e7b | feat(01-02): Atlas migration setup with RLS for event_log immutability |
| a216596 | feat(01-02): Storage interface + PostgreSQL implementation + platform commands |
| d470fad | test(01-02): EventWriter tests for validation and immutability |

## RLS 强制执行 (T-02-01 缓解)

event_log 表在数据库级别受到保护:

1. **REVOKE** - `GRANT SELECT, INSERT ON event_log TO platform_app` + `REVOKE UPDATE, DELETE, TRUNCATE ON event_log FROM platform_app`
2. **RLS** - `ALTER TABLE event_log ENABLE ROW LEVEL SECURITY` + `ALTER TABLE event_log FORCE ROW LEVEL SECURITY`
3. **Policies** - `event_log_select` (SELECT USING true) + `event_log_insert` (INSERT WITH CHECK true) - 不存在 UPDATE 或 DELETE 策略

结果: 作为 platform_app 的任何 UPDATE 或 DELETE 尝试返回 SQLSTATE 42501 (insufficient_privilege)。

**验证测试**: `internal/storage/postgres_test.go` 中的 `TestEventLogIsAppendOnly` 创建一个 event_log 行，然后通过原始 SQL 尝试 `UPDATE event_log SET event_type = 'tampered' WHERE id = $1` 和 `DELETE FROM event_log WHERE id = $1`，并断言权限被拒绝错误。

## Schema 设计 (D-07/D-08)

| 表 | 关键字段 | 索引 |
|-------|------------|---------|
| user | email (unique), password_hash (sensitive), role, status | email UNIQUE |
| invite_token | token_hash (sensitive, unique), email, invited_by, expires_at | token_hash UNIQUE, email |
| event_log | occurred_at, event_type, actor_id (nullable), resource_type, resource_id, payload (JSONB) | (event_type, occurred_at), (resource_type, resource_id), occurred_at |

所有 event_log 列标记为 Immutable() - ent 不会为更新操作生成 setter。

## 事件类型枚举 (D-10)

Phase 1 有效 event_type 值:
- `user.registered` - UserRegisteredPayload{UserID, Email}
- `user.invited` - UserInvitedPayload{InviteID, Email, InvitedBy, ExpiresAt}
- `auth.login` - AuthLoginPayload{UserID, UserAgent?, RemoteIP?}
- `auth.logout` - AuthLogoutPayload{UserID}
- `auth.token_expired` - AuthTokenExpiredPayload{UserID}
- `platform.started` - PlatformStartedPayload{Version}
- `platform.migration_applied` - PlatformMigrationAppliedPayload{AppliedAt, AtlasEnv, DurationMs}

## Plan 03 (Auth) 的注意事项

1. **User 实体已存在** - Plan 03 应使用 `storage.Ent().User.Create()` 注册用户，而不是定义新的 User schema
2. **Auth 事件** - Login/logout/token_expired 事件应使用 `EventTypeAuthLogin`/`EventTypeAuthLogout`/`EventTypeAuthTokenExpired` 调用 `event.Writer.Append()`
3. **password_hash** - 已在 User schema 中定义为 Sensitive() 字段；ent 将自动从 String()/Marshal 输出中省略
4. **EventLog 是追加-only** - 应用程序代码不能对 event_log 进行 UPDATE/DELETE；所有事件都是新插入
5. **DB() 方法可用** - 对于 auth 测试中的任何原始 SQL 需求，`storage.DB()` 暴露底层 `*sql.DB`

## 与计划的偏差

### 自动修复的问题

**规则 2 (缺少关键功能): 添加了 Storage.DB() 方法**
- 发现于: Task 3
- 问题: ent 不为 Immutable() 字段生成 setter，因此 RLS UPDATE 测试无法使用 ent UpdateOneID API
- 修复: 在 Storage 接口和 postgresStorage 实现中添加 `DB() *sql.DB` 方法
- 修改的文件: internal/storage/storage.go, internal/storage/postgres.go
- 提交: a216596

**规则 3 (阻塞问题): go.mod golang.org/x/sync 版本与 Go 1.22 不兼容**
- 发现于: Task 1
- 问题: golang.org/x/sync@v0.20.0 需要 Go >= 1.25；go.mod 指定 go 1.22
- 修复: 更新 go.mod 到 go 1.25.0
- 提交: 37aecab

**规则 3 (阻塞问题): ariga.io/atlas-provider-ent 不可访问**
- 发现于: Task 2
- 问题: 在 Go 代理上找不到包；仓库不存在
- 修复: 直接使用 atlas CLI (`atlas migrate diff`) 而不是基于 go run 的 ent provider
- 提交: 6b35e7b

**规则 3 (阻塞问题): github.com/cheikhathch/omitempty 不存在的依赖**
- 发现于: Task 1
- 问题: 在 GitHub 上找不到仓库，阻止 go mod tidy
- 修复: 从 go.mod 中移除无效的间接依赖
- 提交: 37aecab

## 验证结果

| 检查 | 结果 |
|-------|--------|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `grep -q "Sensitive()" internal/storage/ent/schema/user.go` | PASS |
| `grep -q 'field.JSON("payload"' internal/storage/ent/schema/event_log.go` | PASS |
| `grep -c "CREATE TABLE" migrations/20260506062521_initial.sql` | 3 (user, invite_token, event_log) |
| `grep -q "REVOKE UPDATE, DELETE, TRUNCATE ON event_log FROM platform_app"` | PASS |
| `grep -q "ENABLE ROW LEVEL SECURITY" && "FORCE ROW LEVEL SECURITY"` | PASS |
| `grep -q "CREATE POLICY event_log_insert" && "CREATE POLICY event_log_select"` | PASS |
| `grep -q "case \"migrate\":" cmd/platform/main.go` | PASS |
| `grep -q "EventTypePlatformStarted" cmd/platform/main.go` | PASS |
| `grep -q '"atlas", "migrate", "apply"' cmd/platform/main.go` | PASS |
| `grep -q "func NewWriter" internal/event/writer.go` | PASS |

## 自我检查

所有声明已验证:
- 提交存在: 37aecab, 6b35e7b, a216596, d470fad
- 创建的文件: internal/storage/ent/schema/{user,invite_token,event_log}.go, internal/storage/ent/ (generated), internal/storage/{storage,postgres,postgres_test}.go, internal/event/{event,types,writer,writer_test}.go, atlas.hcl, migrations/20260506062521_initial.sql, migrations/atlas.sum, cmd/platform/main.go, Makefile
- RLS 测试: TestEventLogIsAppendOnly 使用原始 SQL UPDATE/DELETE (不是 ent API) 来验证权限拒绝
- 无占位符注释: `grep -q "/* match by ID */" internal/storage/postgres_test.go` 返回 no match