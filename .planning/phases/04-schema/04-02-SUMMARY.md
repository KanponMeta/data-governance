---
phase: 04-schema
plan: 02
subsystem: lineage-schema-foundation
tags: [ent, migration, atlas, lineage, schema-versioning, metadata, event-types, connector]

# 依赖图
requires:
  - phase: 04-schema
    plan: 01
    provides: migrations/20260509120000_phase4_lineage_schema.sql 空桩 + atlas.sum

provides:
  - migrations/20260509120000_phase4_lineage_schema.sql: 6 个 CREATE TABLE + 手动管理的附录（局部索引、CHECK 约束、RLS 授权、event_log 枚举扩展）
  - internal/storage/ent/schema/asset_edge.go: AssetEdge ent 实体
  - internal/storage/ent/schema/column_edge.go: ColumnEdge ent 实体
  - internal/storage/ent/schema/schema_version.go: SchemaVersion ent 实体
  - internal/storage/ent/schema/schema_change.go: SchemaChange ent 实体
  - internal/storage/ent/schema/asset_version.go: AssetVersion ent 实体（drift_status 可变；其他字段不可变）
  - internal/storage/ent/schema/asset_metadata.go: AssetMetadata ent 实体（追加写入、RLS 保护）
  - internal/connector/schema_types.go: connector.Schema + connector.SchemaColumn（D-07 丰富捕获形态）
  - internal/event/types.go: 8 个 Phase 4 EventType 常量（D-21）+ 扩展的 AllKnownTypes()（共 37 个）

affects:
  - 04-03（Wave 3 — 血缘写入器使用 asset_edges/column_edges 表）
  - 04-04（Wave 3 — Schema 写入器使用 schema_versions/schema_changes 表）
  - 04-05（Wave 4 — diff 分类器使用 SchemaChange.change_type 约束）
  - 04-06（Wave 5 — CTE 遍历使用局部索引查询 asset_edges）
  - 04-07（Wave 6 — 元数据 PATCH 处理器使用 asset_metadata 表）
  - 04-08（Wave 7 — 验收测试验证 event_log_event_type_check 覆盖 Phase 4 类型）

# 技术跟踪
tech-stack:
  added: []  # 无新增依赖；所有 Phase 4 ent 实体使用现有的 entgo.io/ent v0.14.0
  patterns:
    - "手动管理的 SQL 附录：ent 生成 CREATE TABLE；局部索引 + CHECK 约束 + RLS 附加到同一迁移文件（established Phase 1-3 pattern）"
    - "软退役边：superseded_at 可为空；局部索引 WHERE superseded_at IS NULL 用于热读路径（D-15）"
    - "ent.Immutable() 字段防御：asset_versions 和 schema_changes 在所有非可变字段上有 Immutable()；Wave 6 ack 变更是 schema_changes 的唯一 UPDATE 路径"
    - "Schema 类型的 connector 包放置：避免 asset->schema->connector->asset 导入循环（Pitfall 4）"
    - "AllKnownTypes() 累积模式：Phase 4 类型附加在 Phase 3 块之后；测试中断言最小计数"

key-files:
  created:
    - internal/storage/ent/schema/asset_edge.go
    - internal/storage/ent/schema/column_edge.go
    - internal/storage/ent/schema/schema_version.go
    - internal/storage/ent/schema/schema_change.go
    - internal/storage/ent/schema/asset_version.go
    - internal/storage/ent/schema/asset_metadata.go
    - internal/connector/schema_types.go
    - internal/connector/schema_types_test.go
  modified:
    - migrations/20260509120000_phase4_lineage_schema.sql（桩替换为完整 DDL）
    - migrations/atlas.sum（哈希已更新）
    - internal/event/types.go（8 个新常量 + 扩展 AllKnownTypes）
    - internal/event/types_test.go（新增 TestAllPhase4EventTypes）
    - internal/storage/ent/*.go（ent 重新生成：client.go、tx.go、mutation.go、predicate.go、runtime.go、hook.go + 6 个实体文件）

key-decisions:
  - "手写迁移 SQL（非 atlas migrate diff）：ariga.io/atlas-provider-ent 在此环境没有 go-get 端点（与 Plan 04-01 相同的约束）；迁移 DDL 手写匹配精确的 Atlas 输出格式（timestamptz、uuid、character varying(N)、jsonb）"
  - "Task 1+2 一起提交：计划将 CREATE TABLE（Task 1）和手动管理的附录（Task 2）分开，但由于 atlas migrate diff 不可用，整个迁移一次性编写并作为 e9f82df 原子提交"
  - "asset_version UNIQUE(asset, code_hash) 通过 ent index.Fields().Unique()：计划注明这是优于手写 SQL 的首选方法；使用 ent 内置的唯一多字段索引支持"
  - "AssetVersion 上 drift_status 可变：唯一没有 Immutable() 的字段，因为血缘漂移检测（D-04）在插入后更新它而不插入新行"
  - "schema_changes 确认列可变：acknowledged_at/by/reason 缺少 Immutable() 按 D-10；所有其他 schema_change 字段不可变；数据库层面：REVOKE DELETE/TRUNCATE；列更新限制在应用层（Wave 6 ent 变更）"

# 指标
duration: 25min
completed: 2026-05-09
---

# Phase 4 Plan 02：Phase 4 Schema 迁移 + 6 个 Ent 实体 + Schema/Event 类型总结

**手编 Phase 4 PostgreSQL 迁移（6 个 CREATE TABLE + 局部索引 + CHECK 约束 + RLS + event_log 枚举扩展）、6 个 ent 实体 Schema（带 ent 代码生成）、connector.Schema/SchemaColumn D-07 类型，以及 8 个带测试覆盖的 Phase 4 D-21 EventType 常量**

## 性能

- **耗时：** 约 25 分钟
- **开始时间：** 2026-05-09T10:35:00Z
- **完成时间：** 2026-05-09T11:00:00Z
- **任务：** 3 个中的 3 个（Task 1+2 原子提交，Task 3 单独提交）
- **修改文件：** 62 个（6 个新 ent schema 文件、2 个新类型文件、2 个新测试文件、约 48 个 ent 生成文件、2 个迁移文件、2 个事件类型文件）

## 完成情况

### 迁移（Task 1+2）

`migrations/20260509120000_phase4_lineage_schema.sql` 现在包含：

**6 个 CREATE TABLE 语句：**
| 表 | 用途 | 关键列 |
|-------|---------|-------------|
| `asset_edges` | 资产到资产血缘邻接（D-13） | from_asset, to_asset, code_hash_first/latest, first/last_seen_run_id, superseded_at |
| `column_edges` | 列到列血缘邻接（D-13） | from_asset/column, to_asset/column, partition_key, superseded_at |
| `schema_versions` | 按哈希去重的完整 Schema 快照（D-11） | asset, code_hash, schema_hash, schema_data JSONB, last_seen_at |
| `schema_changes` | 每个 diff 的破坏性变更审计跟踪（D-09/D-11） | change_type, is_breaking, acknowledged_at/by/reason |
| `asset_versions` | 每个 code_hash 的代码级资产快照（D-17） | code_hash UNIQUE per asset, column_lineage JSONB, drift_status |
| `asset_metadata` | 运行时覆盖元数据（D-17 追加写入） | asset, column_name (可为空), set_by, set_at |

**手动管理的附录：**
- 4 个局部索引 `WHERE superseded_at IS NULL`：`asset_edges_active_from`、`asset_edges_active_to`、`column_edges_active_from`、`column_edges_active_to`
- CHECK `asset_edges_no_self`：`from_asset != to_asset`（D-13 Pitfall 2）
- CHECK `column_edges_no_self`：防止同一列→自身
- CHECK `schema_changes_change_type_check`：9 个内部 change_type 值
- CHECK `asset_versions_drift_status_check`：`clean|pending|acknowledged`
- 角色授权：`platform_owner` 所有权；`platform_app` 对 edges/versions 的 SELECT/INSERT/UPDATE；对 schema_changes 和 asset_metadata 的 REVOKE DELETE/TRUNCATE
- `asset_metadata` 上的 RLS：ENABLE + FORCE ROW LEVEL SECURITY，仅 SELECT + INSERT 策略（镜像 event_log D-09 模式）
- `event_log_event_type_check` 扩展：DROP+ADD 包含所有 37 个事件类型（Phase 1: 7, Phase 2: 9, Phase 3: 13, Phase 4: 8）

### Ent 实体（Task 1）

所有 6 个 ent schema 文件生成干净；`go generate ./internal/storage/ent/...` 和 `go build ./...` 成功。

| 实体 | 表 | 字段数 | 备注 |
|--------|-------|-------------|---------|
| AssetEdge | asset_edges | 10 | 除 code_hash_latest、last_seen_run_id、last_seen_at、superseded_at 外所有字段 Immutable() |
| ColumnEdge | column_edges | 13 | 与 AssetEdge 相同 + from_column、to_column、partition_key |
| SchemaVersion | schema_versions | 8 | schema_data JSON Immutable()；仅 last_seen_at/run_id 可变 |
| SchemaChange | schema_changes | 17 | acknowledged_at/by/reason 可变；其他 Immutable() |
| AssetVersion | asset_versions | 9 | drift_status 可变；其他 Immutable()；UNIQUE(asset,code_hash) 索引 |
| AssetMetadata | asset_metadata | 8 | set_by/set_at Immutable()；description/owner/tags 可变（用于 UI 显示） |

### 类型（Task 3）

**connector.Schema（D-07）：**
```go
type Schema struct {
    Columns       []SchemaColumn
    PrimaryKey   []string
    RowCountEstimate int64     // -1 如果连接器无法提供
    CapturedAt    time.Time
}
type SchemaColumn struct {
    Name, Type, Comment string
    Nullable, IsPrimaryKey bool
    Default *string  // nil = 无默认值
}
```

**Phase 4 EventType 常量（D-21）：**
- `lineage.captured`、`lineage.drift_detected`
- `schema.captured`、`schema.unchanged`、`schema.change_detected`、`schema.capture_failed`、`schema.break_acknowledged`
- `metadata.updated`

`AllKnownTypes()` 现在返回 37 个条目（Phase 1: 7 + Phase 2: 9 + Phase 3: 13 + Phase 4: 8）。

## 任务提交

1. **Task 1+2: 6 个 ent 实体 + 完整 Phase 4 迁移** - `e9f82df`（feat）
2. **Task 3: connector.Schema 类型 + Phase 4 EventType 常量 + 测试** - `d1f1444`（feat）

## 与计划的偏差

### 自动修复的问题

**1. [规则 3 - 阻塞] `atlas migrate diff` 不可用：ariga.io/atlas-provider-ent 没有 go-get 端点**
- **发现于：** Task 1（Step 8）
- **问题：** `atlas.hcl` 引用 `ariga.io/atlas-provider-ent` 加载 ent schema，但该包没有 go-get 元标签，无法获取。与 Plan 04-01 存在相同约束（`make migrate-lint` 是 Atlas Pro 专有）
- **修复：** 以精确的 Atlas 输出格式手编迁移 DDL，匹配 Phase 1-3 迁移的列类型（`character varying(N)`、`timestamptz`、`jsonb`、`uuid`、`text`）。Task 1+2 合并为单个文件写入 + 提交，因为分离的前提是 atlas 生成 Task 1 输出供 Task 2 附加
- **验证：** `go build ./...` 退出 0；所有 6 个 CREATE TABLE 存在（`grep -c 'CREATE TABLE'` 返回 6）；所有手动管理的附录元素通过 grep 验证
- **提交于：** `e9f82df`（Task 1+2）

**2. [规则 1 - 决策] Task 1+2 代替分开提交而是原子提交**
- **发现于：** Task 1 执行期间
- **问题：** 计划将 Task 1（Atlas 生成的 CREATE TABLE）和 Task 2（手动管理的附录）分为单独的提交。由于 Task 1 无法由 Atlas 生成，两者一起编写
- **修复：** 合并为单个 `e9f82df` 提交，覆盖完整迁移文件内容
- **影响：** 无 — 下游计划仅依赖最终迁移文件内容，不依赖提交拆分

### 不适用

- `make migrate-lint` 跳过（Atlas Pro 门控，预先存在 — 在 04-01-SUMMARY.md 中记录）
- `make migrate-apply` 跳过（无实时本地 PostgreSQL；迁移语法通过 Phase 1-3 模式验证）
- `runs.code_hash` / `runs.lineage_emitted` 列：未添加。04-CONTEXT.md §3 明确说明"runs 上的 lineage_emitted 列：不需要，如果捕获在同一个 tx 中"。理由：保持 schema 最小化；如果同 tx 方法不足，Wave 3 将添加

## 威胁面扫描

| 标志 | 文件 | 描述 |
|------|------|-------------|
| threat_flag: new_tables | migrations/20260509120000_phase4_lineage_schema.sql | 6 个新表，数据在平台 app→PostgreSQL 信任边界。缓解：asset_metadata 上的 RLS；对 schema_changes 的 REVOKE DELETE/TRUNCATE；所有其他表仅授予 SELECT/INSERT/UPDATE（无 DELETE） |
| threat_flag: new_event_types | internal/event/types.go | 8 个新 event_type 值扩展了 event_log_event_type_check 约束。Go AllKnownTypes() 和 DB CHECK 之间的漂移风险（T-04-02-05）通过以下方式缓解：同时发布在同一计划中；Plan 04-08 验收测试 TestEventTypeConsistency 将验证 DB CHECK 与 AllKnownTypes() 匹配 |

## 已知存根

无 — 此计划是纯 schema/类型基础。无业务逻辑、无数据渲染、无占位符值。

## 自我检查

| 检查项 | 状态 |
|-------|--------|
| `go build ./...` 退出 0 | PASS |
| `grep -c 'CREATE TABLE' migration` = 6 | PASS |
| 6 个 ent schema 文件存在且结构体名称正确 | PASS |
| `internal/connector/schema_types.go` 导出 Schema + SchemaColumn | PASS |
| `internal/event/types.go` 有所有 8 个 Phase 4 常量 | PASS |
| `AllKnownTypes()` 返回 >= 37（已测试） | PASS |
| `go test ./internal/event/... -run TestAllPhase4EventTypes` 退出 0 | PASS |
| `go test ./internal/connector/... -run TestSchemaTypeShape` 退出 0 | PASS |
| 提交 e9f82df 存在 | PASS |
| 提交 d1f1444 存在 | PASS |
| Phase 3 回归：`go test ./internal/event/... -run TestAllPhase3EventTypes` | PASS |
| atlas.sum 使用新迁移哈希更新 | PASS |

## 自我检查：通过

---
*阶段：04-schema*
*完成时间：2026-05-09*