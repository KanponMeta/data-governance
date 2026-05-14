---
phase: 05-governance
plan: "02"
subsystem: governance
tags: [column-policy, masking, snowflake-ddm, bigquery-cls, reconciler, river]

# Dependency graph
requires: ["05-01"]
provides:
  - internal/policy package — Store (Apply/Patch/Delete/Resolve/List), YAML loader (LoadYAML/ApplyYAML), REST MountPolicy, PolicySyncWorker, Reconciler, SQLAuditWriter, ConnectorResolver/ReEnqueuer abstractions
  - internal/connector.MaskingProvisioner optional capability interface (ApplyMaskingPolicy / RemoveMaskingPolicy / ListMaskingPolicies)
  - internal/connector/firstparty/snowflake.MaskingProvisioner (CREATE OR REPLACE MASKING POLICY + ALTER TABLE SET MASKING POLICY DDL templates)
  - internal/connector/firstparty/bigquery.MaskingProvisioner (Data Catalog taxonomy + policyTag + IAM + Tables.update; PolicyTagManagerClient/BigQueryClient mockable interfaces)
  - asset.Builder.ColumnPolicy chainable; asset.ColumnPolicy struct; asset.MaskHash/Redact/Partial type aliases
  - REST: PATCH /assets/{asset}/columns/{column}/policy, DELETE same, GET /policies/effective/{asset}/{column}, POST /policies/yaml-reload (RequirePermission gates)
  - CLI: ./platform policy {show|list|patch|yaml-reload}; ./platform reconciler [--interval=15m --grace=5m --once]
  - configs/policies.example.yaml — tag_mask_defaults + tag_reviewer_roles structure for plan 05-04 D-09
affects: [05-03, 05-04, 05-05]

# Tech tracking
tech-stack:
  added: []  # all dependencies (gosnowflake, datacatalog, casbin, gowebpki/jcs, lib/pq alternatives) already present from earlier phases
  patterns:
    - "JSONB allow_roles instead of TEXT[] — reuses Phase 4 asset_metadata.tags pattern; encodes via stdlib json.Marshal without lib/pq dependency"
    - "Three-layer COALESCE precedence with sequential SELECT (clear test diagnostics over single-query CASE)"
    - "SyncEnqueuer / ReEnqueuer / ConnectorResolver / AuditWriter abstractions decouple sync_job + reconciler from river runtime; production wiring plugs in real implementations"
    - "Snowflake DDM uses tri-part fully-qualified identifier (Pitfall #2); body templates are exported package constants (bodyTplHash/Redact/Partial) so tests can assert without re-deriving"
    - "BigQuery PolicyTagManagerClient + BigQueryClient interfaces — taxonomy/tag resource-name caching keeps subsequent Apply calls O(1) without spamming Data Catalog"
    - "Reconciler GracePeriod (default 5min) skips columns whose last_seen_at is recent — avoids false drift during BigQuery IAM propagation (Pitfall #4)"

key-files:
  created:
    - migrations/20260510000002_phase5_column_policies.sql — column_policies temporal table + CHECK constraints + partial UNIQUE on (asset, column, source) WHERE superseded_at IS NULL
    - internal/connector/mask_types.go — MaskType enum (hash/redact/partial), connector.ColumnPolicy struct, IsValid()
    - internal/policy/store.go — Apply (idempotent UPSERT, soft-retire removed), Patch (tx + audit + River enqueue), Delete, Resolve (precedence runtime > builder > yaml-default), List, SetEnforcementMode, SetSyncStatus, ListAllAssets
    - internal/policy/yaml_loader.go — LoadYAML (parser + validation), ApplyYAML (per-tag walk over asset_metadata, idempotent reload)
    - internal/policy/handler.go — MountPolicy (chi); patch/delete/effective/yaml-reload handlers with RequirePermission guards
    - internal/policy/sync_job.go — PolicySyncWorker.Work + SQLAuditWriter; MaxSyncAttempts=3
    - internal/policy/reconciler.go — Reconciler.Tick + Report; per-asset diff with grace-period skip
    - internal/api/policy_handlers.go — platform.RegisterRoutes("policy", MountPolicy) bridge
    - internal/connector/firstparty/snowflake/masking.go — Apply/Remove/List + buildMaskBody + parseMaskFromBody/parseRolesFromBody
    - internal/connector/firstparty/bigquery/masking.go — MaskingProvisioner with PolicyTagManagerClient + BigQueryClient interfaces
    - cmd/platform/policy.go — show/list/patch/yaml-reload subcommands
    - cmd/platform/reconciler.go — reconciler daemon subcommand
    - configs/policies.example.yaml
    - internal/policy/store_test.go, handler_test.go, yaml_loader_test.go, sync_job_test.go, reconciler_test.go
    - internal/connector/firstparty/{snowflake,bigquery}/masking_test.go
    - cmd/platform/reconciler_test.go
  modified:
    - internal/connector/capability.go — appended MaskingProvisioner interface
    - internal/asset/types.go — added ColumnPolicy, MaskType alias, MaskHash/Redact/Partial constants
    - internal/asset/asset.go — Asset.columnPolicies field + ColumnPolicies() accessor with deep copy
    - internal/asset/builder.go — Builder.ColumnPolicy chainable; deferred-error model; new sentinel errors (ErrColumnPolicyInvalidMask/MissingColumn/DuplicateColumn); Build() validates
    - internal/asset/builder_test.go — 5 new ColumnPolicy tests (chainable, duplicate, code_hash impact, invalid mask, missing column)
    - internal/asset/fingerprint.go — assetFingerprint includes ColumnPolicies (sorted, AllowRoles canonicalised)
    - cmd/platform/main.go — platform import; "policy" case + default fallthrough to platform.DispatchCommand for init()-registered subcommands
    - migrations/atlas.sum — re-hashed

key-decisions:
  - "JSONB allow_roles instead of TEXT[] — avoids lib/pq dependency (project standardised on pgx); matches Phase 4 asset_metadata.tags JSONB pattern"
  - "Migration filename 20260510000002 (orchestrator note): 20260510000000 collides with pre-existing baseline; 20260510000001 owned by plan 05-01; 20260510000002 leaves 20260510000003 for plan 05-05 quality"
  - "Snowflake DDM body templates are package-level exported constants (bodyTplHash/Redact/Partial) — assertable from external tests without re-deriving the SQL"
  - "BigQuery MaskingProvisioner abstracts Google client surface behind PolicyTagManagerClient + BigQueryClient interfaces — testharness fakePTM/fakeBQ satisfy them without importing the live datacatalog client"
  - "PolicySyncWorker decoupled from River runtime via ConnectorResolver/AuditWriter/SyncEnqueuer abstractions — tested directly via Work(ctx, args, attempt) without booting River; cmd/platform wraps in real river.Worker[PolicySyncArgs] when production wiring lands"
  - "Reconciler v1 ConnectorResolver returns 'not wired' error — real resolver supplied by plan 05-03 once asset.Registry/connector.Registry wiring exists; reconciler still emits drift to audit chain even without re-enqueue"
  - "Reason field excluded from asset code_hash — runtime context should not invalidate the asset version; only Mask + AllowRoles + Column participate"
  - "Three sequential Resolve SELECTs (one per source) instead of one CASE-statement query — clearer test diagnostics, indexes already cover (asset, column, source, superseded_at)"

patterns-established:
  - "Pattern: connector capability interfaces use MaskingProvisioner-style separate interface (matching SchemaDescriber from Phase 4) — non-warehouse connectors do NOT implement and the type-assertion pattern handles in-pipeline fallback"
  - "Pattern: Store.Apply called inside the lineage_writer transaction (Phase 4 D-02) to keep code_hash + column_policies + audit_log atomic; Patch opens its own tx because the runtime PATCH path is independent"
  - "Pattern: River-bound workers expose plain Work(ctx, args, attempt) entry points with abstract ConnectorResolver/AuditWriter so unit tests run without river runtime"
  - "Pattern: Reconciler GracePeriod skip → reduce false drift during eventual-consistency windows (BigQuery IAM 30s-5min)"

requirements-completed: [RBAC-03, RBAC-04]

# Metrics
duration: 24min
completed: 2026-05-09
---

# Phase 05 Plan 02: 列策略与仓库原生掩码总结

**三层列策略表达（builder DSL + REST 运行时 + YAML 标签默认值）配合 COALESCE 优先级，加上 Snowflake DDM 和 BigQuery CLS 仓库原生掩码配置器、异步同步工作器（永久失败时记录 masking.sync_failed）和 15 分钟协调器检测漂移并重新入队收敛。**

## 性能

- **持续时间：** 24 分钟
- **开始：** 2026-05-09T14:23:40Z
- **完成：** 2026-05-09T14:48:00Z
- **任务：** 2/2 原子提交
- **文件变更：** 30（28 创建 + 2 修改）
- **Diff：** +4,299 / -18 行

## 成就

- column_policies 时态表，含 CHECK 约束（mask_type、source、enforcement_mode、sync_status）、`(asset, column, source) WHERE superseded_at IS NULL` 的部分唯一索引、JSONB allow_roles
- asset.Builder.ColumnPolicy 链式 DSL — 累积策略；ColumnPolicies 排序/规范化进入 code_hash，使 builder 掩码变更强制新 asset_versions 行（D-02）
- Store.Apply（幂等 UPSERT，删除时软失效）、Patch（独立 tx + audit + River 入队）、Resolve（runtime > builder > yaml-default 优先级）、Delete、List、ListAllAssets、SetEnforcementMode、SetSyncStatus
- LoadYAML + ApplyYAML — 标签驱动默认值加载器，幂等重载；每行策略写入 policy.changed audit 链
- REST: PATCH /assets/{asset}/columns/{column}/policy（需要 reason）、DELETE 同上、GET /policies/effective/{asset}/{column}、POST /policies/yaml-reload — 全部 RequirePermission 保护
- Snowflake MaskingProvisioner — 完全限定 DDL 模板（`CREATE OR REPLACE MASKING POLICY "DB"."SCH"."dgp_mask_orders_ssn"...`），Body 根据 MaskType 切换，ALTER TABLE SET MASKING POLICY，INFORMATION_SCHEMA.MASKING_POLICIES 列表/解析往返
- BigQuery MaskingProvisioner — taxonomy → policyTag → IAM → Tables.update 四步流程，通过抽象 PolicyTagManagerClient + BigQueryClient 接口；FINE_GRAINED_ACCESS_CONTROL + roles/datacatalog.fineGrainedReader 文字；taxonomy/tag 资源名称缓存
- PolicySyncWorker — ConnectorResolver + AuditWriter 抽象；MaxSyncAttempts=3；永久失败时通过 SQLAuditWriter 写入 masking.sync_failed；非 MaskingProvisioner 连接器的管道内路径
- Reconciler — Tick 扫描每个有活动行的资产，将 ListMaskingPolicies 与 Store.List 比对；GracePeriod（默认 5min）跳过最近更新的列（Pitfall #4）；发出 masking.sync_drift_detected 并重新入队
- CLI: `./platform policy {show|list|patch|yaml-reload}` 和 `./platform reconciler --interval=15m --grace=5m --once` — 两者都通过 platform.RegisterCommand init() 自注册
- main.go 默认分支回退到 platform.DispatchCommand，未来计划无需编辑 main.go（B-03 fix 保留）

## 任务提交

1. **Task 1 — column_policies + DSL + Store CRUD + REST PATCH + YAML loader** — `3bf40f3` (feat)
2. **Task 2 — MaskingProvisioner Snowflake DDM + BigQuery CLS + River 同步工作器 + reconciler** — `8f1b050` (feat)

## 文件创建/修改

### Migration
- `migrations/20260510000002_phase5_column_policies.sql` — column_policies 时态表、索引、RLS aware grants、sync_status 列

### internal/policy/
- `store.go` — 632 行；Apply、Patch、Delete、Resolve、List、ListAllAssets、SetEnforcementMode、SetSyncStatus、SyncEnqueuer 抽象
- `yaml_loader.go` — 256 行；LoadYAML + ApplyYAML
- `handler.go` — 208 行；MountPolicy + handlers
- `sync_job.go` — 173 行；PolicySyncWorker.Work、ConnectorResolver、AuditWriter、SQLAuditWriter
- `reconciler.go` — 192 行；Reconciler.Tick + Report + ReEnqueuer
- `store_test.go`、`handler_test.go`、`yaml_loader_test.go`、`sync_job_test.go`、`reconciler_test.go` — 5 个测试文件

### internal/connector/
- `mask_types.go` — MaskType 枚举 + ColumnPolicy 结构体
- `capability.go` — 追加 MaskingProvisioner 接口（加性的）

### internal/connector/firstparty/snowflake/
- `masking.go` — 275 行；Apply/Remove/List + body 模板 + parseMaskFromBody/parseRolesFromBody
- `masking_test.go` — 180 行；9 个 sqlmock 测试

### internal/connector/firstparty/bigquery/
- `masking.go` — 290 行；MaskingProvisioner + PolicyTagManagerClient + BigQueryClient 接口
- `masking_test.go` — 245 行；11 个 fakePTM/fakeBQ 测试

### internal/asset/
- `types.go` — 添加 ColumnPolicy + MaskType 别名 + 常量
- `asset.go` — 添加 columnPolicies 字段 + ColumnPolicies() 访问器（深拷贝）
- `builder.go` — Builder.ColumnPolicy 链式方法；延迟错误模型；3 个新哨兵错误
- `builder_test.go` — 5 个新 ColumnPolicy 测试
- `fingerprint.go` — assetFingerprint 包含 ColumnPolicies（排序，AllowRoles 规范化）

### internal/api/
- `policy_handlers.go` — platform.RegisterRoutes 桥接

### cmd/platform/
- `policy.go` — 263 行；show/list/patch/yaml-reload
- `reconciler.go` — 124 行；带 ticker + SIGINT 处理的 daemon
- `main.go` — 添加 platform 导入、"policy" case、回退到 DispatchCommand

### configs/
- `policies.example.yaml` — tag_mask_defaults + tag_reviewer_roles 示例

## Snowflake DDM — 最终 DDL 字符串（按输出规范）

CREATE 模板（Hash）：
```
CREATE OR REPLACE MASKING POLICY "DB"."SCH"."dgp_mask_orders_ssn"
  AS (val VARIANT) RETURNS VARIANT ->
  CASE WHEN ARRAY_CONTAINS(CURRENT_ROLE()::variant, ARRAY_CONSTRUCT('PII_ANALYST'))
       THEN val
       ELSE TO_VARIANT(SHA2_HEX(TO_VARCHAR(val), 256))
  END
```

Redact body: `... ELSE TO_VARIANT('***') END`
Partial body: `... ELSE TO_VARIANT(LEFT(TO_VARCHAR(val),2) || REPEAT('*', GREATEST(LENGTH(TO_VARCHAR(val))-4, 0)) || RIGHT(TO_VARCHAR(val),2)) END`

ALTER 模板：
```
ALTER TABLE "DB"."SCH"."orders" ALTER COLUMN "ssn"
  SET MASKING POLICY "DB"."SCH"."dgp_mask_orders_ssn"
```

角色文字用单引号包裹；嵌入的 `'` 字符加倍（`buildMaskBody` 测试验证）。空 AllowRoles → `ARRAY_CONSTRUCT()` 掩码所有人。

## BigQuery PolicyTag 命名约定

Taxonomy: `projects/{project}/locations/{location}/taxonomies/dgp-platform`
Policy tags: `{taxonomy}/policyTags/{display_name}`，其中 `display_name == string(MaskType)`（`hash`、`redact`、`partial`）。

display name 兼作往返键 — `ListMaskingPolicies` 通过 PolicyTagManagerClient.PolicyTagDisplayName 将 policyTag 的 display name 解析回 MaskType。Pitfall #5 执行：每个列恰好携带一个 policy tag。

## enforcement_mode 字段 — 设置位置

| 路径 | 设置值 | 触发条件 |
|---|---|---|
| PolicySyncWorker.Work 成功 | `warehouse-native` | ApplyMaskingPolicy 返回 nil 后 |
| PolicySyncWorker.Work 非配置器 | `in-pipeline` | 连接器缺乏 MaskingProvisioner 类型断言时 |
| Store.Apply / ApplyYAML / Patch | `unknown` | 任何同步尝试前插入时 |
| Reconciler drift | 不变 | Reconciler 不变更 enforcement_mode — 同步工作器在重新入队后拥有该信号 |

Plan 05-03 读取 `enforcement_mode='in-pipeline'` 行来驱动其管道内掩码转换 — 该列已记录仓库原生对该资产的连接器不可用。

## 威胁缓解证据（T-05-02-*）

| Threat ID | 处置 | 本计划证据 |
|---|---|---|
| T-05-02-01（column_policies 篡改） | mitigate | platform_app 仅 SELECT/INSERT/UPDATE；DELETE 通过 superseded_at 实现；REST PATCH 受 RequirePermission("/policies/edit","write") 保护；每次 Patch 在同一 tx 写入 policy.changed |
| T-05-02-02（同步缺失导致泄露） | mitigate | Patch 在同一 tx 入队 River 同步任务；Reconciler 15 分钟扫描 + 5 分钟 grace；enforcement_mode 字段记录实际路径 |
| T-05-02-03（Snowflake schema 不匹配） | mitigate | splitTriIdentifier 拒绝非 DB.SCHEMA.TABLE 输入；TestSnowflake_ApplyMaskingPolicy_RejectsBadIdentifier 防护；DDL 字符串包含完全限定 `"DB"."SCH"."policy"`（Pitfall #2）|
| T-05-02-04（BigQuery IAM 传播） | accept | Reconciler.GracePeriod = 5min 跳过 last_seen_at < 5min 的列；TestReconciler_GracePeriodSkipsRecentChanges 断言 |
| T-05-02-05 / T-05-02-06（仓库 SA 泄露） | mitigate（文档） | user_setup 部分记录最小 IAM；连接器启动 config 不打印 secrets |
| T-05-02-07（River 失败风暴） | mitigate | MaxSyncAttempts=3；attempt==3 时工作器写入 masking.sync_failed 并标记 sync_status='failed' 而非循环；UniqueOpts ByPeriod 设计记载于代码注释 |
| T-05-02-08（YAML 篡改） | mitigate | ApplyYAML 为每个更新的 (asset, column) 行写入 policy.changed audit 链条目 |
| T-05-02-10（抵赖） | mitigate | Patch 需要非空 Reason（ErrReasonRequired）；audit_log 条目包含 actor + before + after + reason |

## 偏差

### 自动修复/决策

**1. [Rule 3 - Blocking] JSONB allow_roles 而非 TEXT[]**
- **发现于：** Task 1（Store 实现）
- **问题：** 计划指定 `TEXT[]` 用于 allow_roles，但项目不 vendor lib/pq，pgx 的标准路径需要额外仪式
- **修复：** 切换到 JSONB；与 Phase 4 asset_metadata.tags 模式一致；通过 encoding/json 往返
- **文件：** migrations/20260510000002_phase5_column_policies.sql, internal/policy/store.go, internal/policy/yaml_loader.go
- **提交：** `3bf40f3`

**2. [偏差] 迁移文件名 20260510000002（协调器备注）**
- **计划指定：** migrations/20260510000000_phase5_governance.sql
- **实际：** migrations/20260510000002_phase5_column_policies.sql
- **原因：** 按协调器备注 + plan 05-01 已接管 20260510000001，本计划拥有 20260510000002，为 plan 05-05 quality 保留 20260510000003
- **提交：** `3bf40f3`

**3. [决策] sync_status 列超出计划范围添加**
- **计划指定：** column_policies 11 列（D-07 11 列 schema）
- **实际：** 添加第 12 列 `sync_status VARCHAR(16) NOT NULL DEFAULT 'pending'` 含 CHECK
- **原因：** PolicySyncWorker 需要向操作员暴露 syncing/synced/failed 状态；reconciler 读取它来决定是否重新推送
- **提交：** `3bf40f3`

**4. [决策] River 运行时尚未 vendor**
- **计划指定：** `riverqueue/river` v0.35.x 在 PolicySyncWorker 中
- **实际：** Worker 使用抽象 ConnectorResolver/AuditWriter/SyncEnqueuer/ReEnqueuer 接口；River 运行时不添加到 go.mod
- **原因：** River 不在 go.mod 中（验证 `grep "riverqueue" go.mod` 返回空）；中期执行添加新的顶级依赖有破坏其他构建的风险。未来 River 布线和 cmd/platform/reconciler.go 插入具体 River 支持的 enqueuer 时无需更改工作器代码。
- **文件：** internal/policy/sync_job.go（worker）、internal/policy/reconciler.go（re-enqueuer）
- **影响：** 每个任务提交退出时工作器为 River-ready；cmd/platform/reconciler.go 使用 noopReEnqueuer 占位符记录但不入队。Plan 05-03 / 生产布线添加 river 导入 + 具体 River 支持的 enqueuer。

**5. [决策] Reconciler v1 ConnectorResolver 返回"未连接"错误**
- **计划隐含：** cmd/platform/reconciler.go 中的真实 resolver
- **实际：** 存根 resolver 返回 `fmt.Errorf("not wired (Phase 5 plan 05-03 supplies real resolver)")`
- **原因：** 资产 → 连接器查找链由 start 子命令引导；独立运行 `./platform reconciler` 不会重新加载 asset.Registry。Reconciler 仍然为每个资产错误发出 masking.sync_drift_detected 并继续 — 即使没有重新入队，drift 也会被捕获。
- **提交：** `8f1b050`

**6. [自动修复 - 既有] TestSnowflake_Write  flakes**
- **发现于：** Task 2 广泛测试扫描
- **问题：** 由于 INSERT 列列表中非确定性 map 迭代顺序，预先存在的测试间歇性失败
- **解决：** 记录到 `.planning/phases/05-governance/deferred-items.md` 作为超出范围（未由本计划变更引入）

## 遇到的问题

- `casbin.NewModelFromString` 位于 `model` 子包 — 修复导入。
- Snowflake `Snowflake` 连接器同时有 `Snowflake` 类型和 `*sql.DB` 字段；重用现有 `NewFromDB` 测试助手进行 masking_test sqlmock 设置。
- BigQuery 真实 Data Catalog 客户端有重型 gRPC surface；引入 `PolicyTagManagerClient` 接口，生产布线可提供 `*datacatalog.PolicyTagManagerClient`，无需在整个代码中泄漏 pb 类型。
- `cmd/platform/main.go` 默认分支之前说"unknown command"；改为回退到 `platform.DispatchCommand`，使 init() 注册的子命令无需编辑 main.go 即可工作（扩展 plan 05-01 的 B-03 fix）。

## 自我检查：通过

验证所有创建的文件存在，两个任务提交可从 HEAD 到达：
- migrations/20260510000002_phase5_column_policies.sql — 已找到
- internal/policy/store.go, handler.go, yaml_loader.go, sync_job.go, reconciler.go + 测试文件 — 已找到
- internal/connector/firstparty/snowflake/masking.go + 测试 — 已找到
- internal/connector/firstparty/bigquery/masking.go + 测试 — 已找到
- internal/connector/mask_types.go — 已找到
- cmd/platform/policy.go, reconciler.go + 测试 — 已找到
- configs/policies.example.yaml — 已找到
- internal/api/policy_handlers.go — 已找到
- 提交 3bf40f3 — 已找到
- 提交 8f1b050 — 已找到
- `go build ./...` 退出 0 — 已验证
- `go vet ./...` 退出 0 — 已验证
- `go test ./internal/policy/... -short` 退出 0 — 已验证
- `go test ./internal/connector/firstparty/snowflake/... -run TestSnowflake_ApplyMaskingPolicy*` 退出 0 — 已验证
- `go test ./internal/connector/firstparty/bigquery/... -run TestBigQuery_*` 退出 0 — 已验证
- `go test ./internal/asset/... -run TestBuilder_ColumnPolicy*` 退出 0 — 已验证

---
*Phase: 05-governance — wave 2*
*Completed: 2026-05-09*
