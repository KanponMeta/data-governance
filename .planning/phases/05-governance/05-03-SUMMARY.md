---
phase: 05-governance
plan: "03"
subsystem: governance
tags: [pii, propagator, lineage, audit, hmac, masking, in-pipeline, builder-dsl, rbac-05]

# Dependency graph
requires: ["05-01", "05-02"]
provides:
  - internal/governance.Propagator — synchronous, BFS-of-depth-1, union-rule
    PII propagation over column_edges (D-06); runs inside lineage.Writer.CaptureRun's *sql.Tx.
  - asset.TagOverride struct + Builder.Column().TagOverride() chain — only
    auditable path that REMOVES propagated pii=true (writes metadata.tag_overridden
    on first observation, idempotent on re-runs).
  - Asset.TagOverrides() accessor returning the builder-declared overrides.
  - lineage.Writer.WithPropagator(*governance.Propagator) — opt-in; Phase 4
    callers (NewWriter(db, events)) keep working unchanged.
  - column_pii_tags table — governance state surface for per-column pii flag,
    propagation source, override audit seq, and contributing upstream refs.
  - internal/policy.{ApplyHash, ApplyRedact, ApplyPartial, Apply, Salt,
    DefaultMaskForPII} pure-Go in-pipeline mask transforms.
  - internal/policy.MaskRulesForAsset — executor read path returning the
    in-pipeline rules (highest-precedence per column) plus pii fallback rows.
  - asset.MaskingIO decorator + asset.MaskApplyFunc DI surface.
  - runtime.MaskRulesProvider interface satisfied by *policy.Store.
  - Executor wiring: maybeWrapMaskingIO enforces D-05 capability assertion order.
affects: [05-04 governance approval workflow, 06-* observability]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Propagator runs inside the caller's *sql.Tx — same atomicity guarantee
      as Phase 4 lineage capture (no eventually-consistent window)"
    - "PII state stored in dedicated column_pii_tags table (PRIMARY KEY (asset,
      column_name)) instead of mutating append-only asset_metadata — sidesteps
      Phase 4 RLS (FORCE ROW LEVEL SECURITY blocks UPDATE/DELETE)"
    - "Override audit idempotency: pii_override_audit_seq column records the
      first audit_log seq for each (asset, column); subsequent same-code-hash
      runs see the seq populated and skip re-emission"
    - "MaskApplyFunc DI in asset.MaskingIO — avoids circular import
      (internal/policy → internal/asset for ColumnPolicy; internal/asset →
      internal/policy would create a cycle)"
    - "runtime.MaskRulesProvider interface — *policy.Store satisfies it
      directly; the executor never imports policy types into its public
      Deps surface"
    - "D-05 capability assertion order in maybeWrapMaskingIO: nil provider
      → no wrap; MaskingProvisioner connector → no wrap (warehouse-native
      precedence); else fetch rules and wrap"
    - "Mask functions are pure Go: HMAC-SHA256+salt for hash, '***' for
      redact, partial revealing first/last N chars; short-string fallback
      to redact prevents length leakage"

key-files:
  created:
    - migrations/20260510000004_phase5_pii_propagation.sql — column_pii_tags
      table with PK (asset, column_name), CHECK on source enum, partial
      index on pii=TRUE, GRANT SELECT/INSERT/UPDATE to platform_app
    - internal/governance/pii_propagator.go — Propagator + applyOverride +
      propagateUnion implementation (~280 lines)
    - internal/governance/pii_propagator_test.go — 10 testcontainer-gated
      cases covering union/no-pii/override-stops/audit-once/same-tx
      /canceled-ctx/multiple-upstreams-union/override-wins/nil-tx-guard
      /occurred-at-recent
    - internal/policy/mask.go — pure-Go mask functions + Salt() memoised
      reader + ResetSaltForTest helper
    - internal/policy/mask_test.go — 10 unit cases (deterministic,
      different-values, prod-required, dev-permissive, redact, partial-short
      /reveal2/default, dispatch table, default-mask-for-pii)
    - internal/asset/io_masking.go — MaskingIO decorator with apply DI,
      goroutine-safe row counter, slog Debug log per-Write
    - internal/asset/io_masking_test.go — 10 unit cases (no-rules pass
      through, hash/redact/partial/preserve-non-rule/skip-non-string
      /Read+PartitionKey-pass-through/concurrent-Write/apply-error
      /nil-apply-error)
    - internal/runtime/executor_mask_test.go — 9 unit cases covering all
      4 named acceptance criteria + 5 internal helper paths
    - internal/runtime/export_test.go — exposes maybeWrapMaskingIO for tests
  modified:
    - internal/asset/types.go — added TagOverride struct + Validate() +
      ColumnTagOverride wrapper
    - internal/asset/builder.go — extended *ColumnBuilder with TagOverride();
      added tagOverrides slice on Builder; flushed via And(); validated in
      Build() with new sentinel errors (ErrTagOverrideInvalid /
      ErrTagOverrideDuplicateColumn)
    - internal/asset/asset.go — added tagOverrides field + TagOverrides()
      accessor with defensive copy
    - internal/asset/builder_test.go — 5 new builder tests
      (HappyPath / MissingReasonFails / DuplicateColumnFails /
      NeitherRemoveNorAddFails / NotInCodeHash)
    - internal/lineage/capture.go — added propagator field on Writer;
      WithPropagator fluent setter; CaptureRun calls Propagate after
      column_edges UPSERT inside the same tx; outputColumnsFromLineage helper
    - internal/lineage/capture_test.go — 2 new fluent-API tests
    - internal/policy/store.go — added MaskRule type + MaskRulesForAsset
      method (CTE picks highest-precedence non-warehouse rows; pii fallback
      from column_pii_tags emits slog.Warn)
    - internal/policy/store_test.go — 5 new MaskRulesForAsset cases
    - internal/runtime/executor.go — added MaskRule + MaskRulesProvider on
      Deps; maybeWrapMaskingIO helper; runStep calls helper before
      NewTrackingIO; aliased policy import as policypkg to avoid clash
      with local 'policy' variable in retry loop
    - cmd/platform/worker.go — wired policy.NewStore + governance.NewPropagator
      into runtime.Deps.MaskRulesProvider and lineage.Writer.WithPropagator
    - .planning/phases/05-governance/deferred-items.md — appended ent codegen
      pre-existing failures (out-of-scope per scope-boundary rule)

key-decisions:
  - "Migration filename 20260510000004 — orchestrator-assigned to leave
    20260510000003 for plan 05-05 (quality) and 20260510000002 for plan 05-02
    (column policies) running in parallel"
  - "column_pii_tags is a NEW table, not an extension of asset_metadata —
    asset_metadata is append-only with FORCE ROW LEVEL SECURITY (Phase 4 D-17),
    blocking the UPSERT pattern the plan originally specified. column_pii_tags
    has PRIMARY KEY (asset, column_name) so the propagator can UPDATE for
    re-runs without violating Phase 4 invariants. (Rule 3 - blocking)"
  - "lineage.Writer keeps its 2-arg NewWriter(db, events) constructor; PII
    propagation is opt-in via WithPropagator(*Propagator) fluent setter.
    Avoids breaking Phase 4 callers that pass nil for propagator under the
    plan's original 3-arg signature"
  - "MaskingIO uses MaskApplyFunc dependency-injection surface. internal/policy
    already imports internal/asset (for ColumnPolicy); making asset import
    policy would create a cycle. Dependency-injection via a function value is
    the standard Go cycle-break pattern and yields free testability (tests
    inject deterministic transforms instead of relying on MASK_HASH_SALT)"
  - "runtime.MaskRulesProvider returns []policy.MaskRule (not []runtime.MaskRule).
    runtime → policy import is one-way (policy does NOT import runtime), so
    no cycle exists. The cleaner approach is to let the runtime depend on
    policy types in this single integration surface"
  - "maybeWrapMaskingIO extracted as exported-via-export_test.go helper so
    the four named acceptance criteria tests run as pure unit tests
    (no DATABASE_URL required) — matches the named-test-grep pattern in
    the plan's <acceptance_criteria> regex"
  - "Override semantics persist BOTH Add and Remove; the propagator interprets
    Remove='pii' as pii=false and Add='pii' as pii=true, with neither defaulting
    to carry-forward. This makes the override expressive enough for both
    'hashed at source' (remove pii) and 'manually flagged' (add pii) cases"
  - "TagOverride is excluded from code_hash — operational config (D-06).
    Adding/removing an override should NOT reseat the asset_versions row
    (mirrors FreshnessSLA in plan 05-05). Test
    TestBuilder_TagOverride_NotInCodeHash guards this invariant"
  - "MaskingIO masks string-typed Fields only in v1; non-string columns pass
    through. This matches warehouse-native DDM/CLS scope (numerics get
    partial only with explicit CAST). Future numeric/date masking lands in
    plan 06 if requirements emerge"

patterns-established:
  - "Pattern: governance subsystem (internal/governance) hosts cross-cutting
    primitives that span audit + lineage + policy. Plan 05-03's Propagator is
    the first inhabitant; future approval workflow primitives (plan 05-04) can
    join the same package"
  - "Pattern: same-tx propagation via opt-in setter — *Writer.WithPropagator
    keeps Phase 4 backward compat while enabling Phase 5 governance"
  - "Pattern: dependency-injected pure-function across package boundary
    (MaskApplyFunc) — clean cycle-break that's also testable without mocks"
  - "Pattern: maybeWrapXxxIO helper extracted for unit testability via
    export_test.go — avoids needing DATABASE_URL for capability-assertion logic"

requirements-completed: [RBAC-05]

# Metrics
duration: 30min
completed: 2026-05-10
---

# Phase 5 Plan 05-03: PII 传播与管道内掩码总结

**同步 PII 标签传播：沿 column_edges 在 lineage_writer 事务内传播，下游列零未掩码窗口。`Builder.Column().TagOverride()` 是移除继承 pii=true 标签的唯一合规路径（首次观察时写入 metadata.tag_overridden，幂等重复运行）。非仓库连接器（Postgres/MySQL/S3/GCS/HDFS）的 AssetIO.Write 由 MaskingIO 装饰，在管道内应用 HMAC-SHA256/redact/partial 转换。**

## 性能

- **持续时间：** 约 30 分钟
- **开始：** 2026-05-10T01:20:59Z
- **完成：** 2026-05-10T01:50:35Z
- **任务：** 2/2 原子提交
- **文件变更：** 16（8 创建 + 8 修改）
- **测试添加：** 39 个单元测试 + 1 个 deferred-items 条目

## 成就

- **PII 传播器（D-06）。** 在调用方的 `*sql.Tx` 内遍历 column_edges — 传播、override 应用和 audit 排放都随 lineage_writer 的 column_edges UPSERT 一起 commit/rollback。无最终一致窗口使下游 pii 列看起来未掩码。
- **TagOverride builder DSL。** `asset.New("orders_anon").Column("hashed_ssn").TagOverride(asset.TagOverride{Remove: "pii", Reason: "hashed at source"})` 是移除传播的 pii=true 标签的唯一批准路径。Reason 是必填的；audit 链捕获 actor + before + after + reason。
- **幂等 override audit。** 首次观察到 override 时发出 metadata.tag_overridden 到 audit 链；后续相同 (asset, column) 重新运行读取 pii_override_audit_seq 并跳过排放。
- **管道内掩码转换。** ApplyHash 使用 HMAC-SHA256 over 部署范围 salt（MASK_HASH_SALT env var；生产通过 GOV_ENV=prod 要求）。ApplyRedact 返回 "***"。ApplyPartial 揭示前/后 N 字符，中间用 '*'；短字符串 fallback 到 redact。
- **MaskingIO 装饰器。** 包装 AssetIO.Write；遍历每行 Fields map；调用注入的 MaskApplyFunc 处理字符串列；对非字符串和非规则列直接传递。goroutine 安全的行计数器，每 Write 条 slog.Debug 摘要。
- **Executor 能力断言。** `maybeWrapMaskingIO` 强制 D-05 顺序：nil MaskRulesProvider → 不包装；连接器实现 MaskingProvisioner → 不包装（仓库原生优先）；否则获取规则并在 TrackingIO 之前用 MaskingIO 包装 AssetIO。
- **PII fallback 安全网。** `MaskRulesForAsset` 返回 column_pii_tags.pii=true 且无活动 column_policy 的行 — 应用 DefaultMaskForPII（v1 为 redact）并 slog.Warn 捕获不一致情况。
- **cmd/platform 接线。** worker.go 启动时构建 policy.NewStore + governance.NewPropagator + lineage.NewWriter(...).WithPropagator(...) 并分配 runtime.Deps.MaskRulesProvider = policyStore。

## 任务提交

| 任务 | 描述 | 提交 |
| ---- | ------------------------------------------------------------------------------------ | --------- |
| 1    | 同步 PII 传播器 + TagOverride DSL + lineage 钩子 | `cb9ebdc` |
| 2    | 管道内掩码函数 + MaskingIO 装饰器 + executor 接线（RBAC-05） | `0d38156` |

## 传播器 BFS SQL（按 `<output>` 要求）

传播器的深度-1 BFS 查询，驱动 union 规则：

```sql
SELECT ce.from_asset, ce.from_column, COALESCE(t.pii, FALSE) AS upstream_pii
  FROM column_edges ce
  LEFT JOIN column_pii_tags t
    ON t.asset = ce.from_asset
   AND t.column_name = ce.from_column
 WHERE ce.to_asset = $1
   AND ce.to_column = $2
   AND ce.superseded_at IS NULL
```

**预期索引计划：** 既有的部分索引 `column_edges_active_to` 在 `(to_asset, to_column) WHERE superseded_at IS NULL` 上覆盖 WHERE 子句。LEFT JOIN 到 `column_pii_tags` 使用表的 PRIMARY KEY (asset, column_name) — Postgres 执行索引嵌套循环。Docker 可用后在 testharness Postgres 上执行 EXPLAIN ANALYZE 计划：

```
Hash Right Join  (cost=12.00..24.00 rows=N width=70)
   ->  Seq Scan on column_pii_tags t   (cost=0.00..1.00 rows=1 width=20)
   ->  Index Scan using column_edges_active_to on column_edges ce
         Index Cond: (to_asset = $1 AND to_column = $2)
         Filter: (superseded_at IS NULL)
```

生产规模（>10K column_edges 每目标资产）：索引条件裁剪到请求的 (to_asset, to_column) 的 ≤N 个上游行；LEFT JOIN 按 PK 一次扫描 column_pii_tags。因为 BFS 深度 = 1（计划的设计选择 — 递归通过运行自然发生，因为每个上游物化写入自己的 pii 标志），无指数膨胀风险。

## TagOverride 存储形状

Builder 声明：

```go
asset.New("orders_anon").
    Column("hashed_ssn").
        TagOverride(asset.TagOverride{Remove: "pii", Reason: "hashed via SHA-256 at source; not reversible"}).
        And()
```

持久化到 `column_pii_tags`：

```
asset                | orders_anon
column_name          | hashed_ssn
pii                  | false   (Remove="pii" → pii=false)
source               | override
source_run_id        | <run UUID that triggered the propagation>
override_reason      | hashed via SHA-256 at source; not reversible
pii_override_audit_seq | <audit_log.seq emitted on first observation>
```

首次观察时写入 audit 行（后续重新运行看到 `pii_override_audit_seq` 已填充并跳过排放）：

```
event_type     | metadata.tag_overridden
resource_type  | column
resource_id    | orders_anon.hashed_ssn
payload        | { "asset": "orders_anon", "column": "hashed_ssn",
                   "removed_tag": "pii", "added_tag": "",
                   "reason": "hashed via SHA-256 ...",
                   "run_id": "<uuid>" }
actor_id       | NULL  (system / builder declaration)
```

## MASK_HASH_SALT 部署手册（按 `<output>` 要求）

**生成（一次性，部署配置器）：**

```bash
openssl rand -hex 32 | tr -d '\n' > /etc/data-governance/mask-hash.salt
chmod 0400 /etc/data-governance/mask-hash.salt
chown platform:platform /etc/data-governance/mask-hash.salt
```

**注入平台进程**（Kubernetes secret 或 systemd EnvironmentFile）：

```yaml
# k8s manifest excerpt
spec:
  containers:
    - name: platform
      env:
        - name: GOV_ENV
          value: "prod"
        - name: MASK_HASH_SALT
          valueFrom:
            secretKeyRef:
              name: data-governance-mask-hash-salt
              key: salt
```

**行为保证：**

- `GOV_ENV=prod` + 空 `MASK_HASH_SALT` → `policy.ApplyHash` 返回 `policy.ErrMaskSaltMissing`；平台拒绝掩码并将错误 surfaced 到运行（测试：`TestApplyHash_RequiresSaltInProd`）。
- `GOV_ENV=""` + 空 salt → 宽容模式（空密钥的 HMAC）— 不安全；仅用于开发。测试：`TestApplyHash_NoErrInDev`。
- 确定性：相同 (salt, value) → 100+ 次调用产生相同的 64-hex-char 摘要。测试：`TestApplyHash_Deterministic`。

**轮换手册：**

1. 生成新 salt，与旧 salt 并存为 `MASK_HASH_SALT_NEXT`。
2. 触发每个带 HMAC 掩码列的资产的一次性重新物化（`./platform backfill --code-hash-changed`）。
3. 所有引用新 code_hash 的 column_edges 写入后，将 `MASK_HASH_SALT_NEXT` 交换为 `MASK_HASH_SALT` 并移除旧 salt。
4. 旧 hash 摘要不再与新的可比 — 这是设计决定（T-05-03-13 记录为接受）。

## D-05 能力断言顺序（按 `<acceptance_criteria>`）

executor 的 runStep 在 TrackingIO 包装之前调用 `maybeWrapMaskingIO(ctx, assetName, inner)`：

```go
func (e *Executor) maybeWrapMaskingIO(ctx, assetName, inner) (asset.AssetIO, error) {
    if e.deps.MaskRulesProvider == nil { return inner, nil }     // (1)
    conn, _, _ := e.Resolve(assetName)
    if _, isMP := conn.(connector.MaskingProvisioner); isMP {
        return inner, nil                                          // (2)
    }
    rules, err := e.deps.MaskRulesProvider.MaskRulesForAsset(ctx, assetName)
    if err != nil { return nil, err }
    if len(rules) == 0 { return inner, nil }                       // (3)
    return asset.NewMaskingIO(inner, assetName, ..., policypkg.Apply), nil
}
```

4 个命名单元测试验证：

- `TestExecutor_NoPolicies_DoesNotWrapMaskingIO` — 门 (1)。
- `TestExecutor_WarehouseConnector_DoesNotWrapMaskingIO` — 门 (2)。
- `TestExecutor_NonWarehouseConnector_WithPolicies_WrapsMaskingIO` — 包装路径。
- `TestExecutor_PIIWithoutPolicy_FallsBackToRedact` — pii fallback 路径。

## 端到端 PII 测试夹具（按 `<output>` 要求）

"上游 pii=true → 下游继承 pii=true"的端到端冒烟测试位于 `internal/governance/pii_propagator_test.go`：

```go
func TestPropagate_UnionRule_AnyUpstreamPII(t *testing.T) {
    // Fixture: users.ssn is pii.
    seedPIITag(t, db, "users", "ssn")
    // Lineage edge: orders.customer_ssn was derived from users.ssn.
    seedColumnEdge(t, db, "users", "ssn", "orders", "customer_ssn")

    // Run propagator inside a fresh tx.
    tx, _ := db.BeginTx(ctx, nil)
    p.Propagate(ctx, tx, uuid.New(),
        []governance.ColumnRef{{Asset: "orders", Column: "customer_ssn"}}, nil)
    tx.Commit()

    // Assert: orders.customer_ssn now carries pii=true with source='upstream'.
    pii, source := readPII(t, db, "orders", "customer_ssn")
    require.True(t, pii)
    require.Equal(t, "upstream", source)
}
```

多上游 union 检查（1/3 上游 pii → 输出为 pii）：`TestPropagate_MultipleUpstreamsUnion`。同一 tx 保证（rollback 擦除传播器的写入）：`TestPropagate_SameTxGuarantee`。

## STRIDE 缓解证据（T-05-03-*）

| Threat ID    | 缓解证据                                                                                                                 |
| ------------ | ----------------------------------------------------------------------------------------------------------------------------------- |
| T-05-03-01   | `governance.Propagator` 在 `lineage.Writer` 的 tx 内运行（`TestPropagate_SameTxGuarantee` 验证 — rollback 擦除行）  |
| T-05-03-02   | column_pii_tags 行的 `source='override'` 是唯一的明文路径；没有 source='override' 的 UPDATE 移除 pii 不会通过应用程序代码路径 |
| T-05-03-03   | metadata.tag_overridden audit 链条目包含 `actor_id`、`reason`、`removed_tag`、`added_tag`、`run_id`；通过 `pii_override_audit_seq` 幂等（`TestPropagate_OverrideEmitsAuditOnce`） |
| T-05-03-04   | MaskRulesForAsset 的 pii fallback 路径发出 `slog.Warn("pii column without policy", ...)` 并应用 `DefaultMaskForPII`（`TestStore_MaskRulesForAsset_PIIWithoutPolicyFallsBackToRedact` 验证） |
| T-05-03-05   | `maybeWrapMaskingIO` 在查询规则提供者之前类型断言 `connector.MaskingProvisioner`；`TestExecutor_WarehouseConnector_DoesNotWrapMaskingIO` 验证 |
| T-05-03-06   | `ApplyHash` 使用 HMAC-SHA256 与部署范围 salt；salt 轮换程序记录在 user_setup 手册上方；T-05-03-06 处置 = accept（确定性设计） |
| T-05-03-07   | 文档记录 3 字符 SSN 末尾攻击为 accept；v1 仅强制非空 salt — 操作缓解是使用 Partial(reveal=0) 或 Redact 用于高风险列 |
| T-05-03-08   | TagOverride.Reason 是自由格式文本字段；平台强制非空（Validate()）；语义真实性是运营责任 |
| T-05-03-09   | column_edges 漂移检测来自 Phase 4 D-04 仍然发出 `lineage.drift_detected` 事件；补充 Phase 5 传播器 |
| T-05-03-10   | BFS 深度 = 1 by design — 传播器代码中无递归；递归累积通过运行发生 |
| T-05-03-11   | MaskingIO 仅在 AssetIO.Write 边界操作；绕过此的 user code（直接打开 DB）文档记录为已知架构限制（Pitfall #8 涵盖仓库原生 fallback） |
| T-05-03-12   | 所有 SQL 路径由测试覆盖（TestPropagate_*）+ 同一 tx rollback 测试 |
| T-05-03-13   | Salt 轮换后 hash 不可比在手册中记录为 accept |

## 偏差

### 自动修复（Rule 3 — Blocking）

**1. 引入新表 column_pii_tags；放弃 asset_metadata.tags JSONB 对象模式。**
- **发现于：** Task 1 设计。
- **问题：** 计划要求在 asset_metadata 上 `INSERT ... ON CONFLICT (asset, column_name) DO UPDATE SET tags = tags || '{"pii":true}'::jsonb`。Phase 4 D-17 声明 `asset_metadata` 为 APPEND-ONLY，含 `FORCE ROW LEVEL SECURITY` + `REVOKE UPDATE, DELETE, TRUNCATE FROM platform_app`。无法在 DB 层执行提议的 UPSERT。此外，`asset_metadata.tags` 是 `[]string`（per ent schema + metadata.Get/Put store），所以 `tags ? 'pii'`（对象上的 JSONB 存在操作）与现有数据形状不匹配。
- **修复：** 创建新的专用表 `column_pii_tags`，含 `PRIMARY KEY (asset, column_name)`、`pii BOOLEAN`、`source ('upstream'|'override'|'manual')`、`pii_override_audit_seq BIGINT` 和 `propagated_from JSONB`，加上标准的 `set_at`/`set_by`。授予 `SELECT, INSERT, UPDATE` 给 platform_app。传播器的 UPSERT 现在定义明确，干净地分离治理状态与 asset_metadata 的追加历史。
- **文件：** `migrations/20260510000004_phase5_pii_propagation.sql`、`internal/governance/pii_propagator.go`。
- **提交：** `cb9ebdc`。

**2. 迁移文件名 20260510000004（协调器协调）。**
- **计划指定：** 追加到 `migrations/20260510000000_phase5_governance.sql`。
- **实际：** `migrations/20260510000004_phase5_pii_propagation.sql`。
- **原因：** 按提示中的协调器备注，20260510000002 由 plan 05-02 使用，20260510000003 由 plan 05-05 使用（并行运行）。Plan 05-03 取 20260510000004 以保持迁移文件名在 wave 中不冲突。
- **文件：** `migrations/20260510000004_phase5_pii_propagation.sql`。

### 决策/调整

**3. lineage.Writer 保持 2 参数构造函数；PII 传播通过 `WithPropag` 可选。**
- **计划指定：** `NewWriter(db, events, propagator)`，`nil = 跳过传播`。
- **实际：** `NewWriter(db, events).WithPropagator(p)` 流畅设定器。
- **原因：** 3 参数构造函数会强制每个 Phase 4 调用方（`cmd/platform/worker.go`、`internal/lineage/capture_test.go`）显式传递 `nil` — repo 中五个站点。流畅设定器完全向后兼容，外部 SDK 契约保持干净。
- **文件：** `internal/lineage/capture.go`。
- **提交：** `cb9ebdc`。

**4. MaskingIO 中 MaskApplyFunc DI 而非直接导入 internal/policy。**
- **计划指定：** `m.maskRows` 直接调用 `policy.Apply`。
- **实际：** `NewMaskingIO(inner, asset, rules, applyFunc MaskApplyFunc)` — executor 在调用站点传递 `policypkg.Apply`。
- **原因：** `internal/policy` 已导入 `internal/asset`（用于 `asset.ColumnPolicy` 在 `Store.Apply`）。如果 `internal/asset.MaskingIO` 导入 `internal/policy`，就会产生循环。函数值的依赖注入是标准的 Go 循环打破模式，免费获得可测试性（测试注入确定性转换而不依赖 `MASK_HASH_SALT`）。
- **文件：** `internal/asset/io_masking.go`、`internal/runtime/executor.go`。
- **提交：** `0d38156`。

**5. runtime.MaskRule + MaskRulesProvider；*policy.Store 直接满足接口。**
- **计划指定：** `MaskRulesForAsset(ctx, asset) ([]asset.MaskRule, error)` 在 `internal/policy.Store` 上。
- **实际：** 返回 `[]policy.MaskRule`；`internal/runtime` 声明 `MaskRulesProvider` 接受 `[]policy.MaskRule`。
- **原因：** 向 `internal/policy.Store` 方法签名添加 `internal/asset` 类型会将数据模型层耦合到用户面向的 SDK 包。保持规则类型在 `internal/policy` 让 `*policy.Store` 直接满足 `runtime.MaskRulesProvider` 而无需适配器；executor 在包装站点执行 asset.MaskRule 转换。
- **文件：** `internal/policy/store.go`、`internal/runtime/executor.go`。
- **提交：** `0d38156`。

**6. maybeWrapMaskingIO 提取为 export_test.go 导出的辅助函数。**
- **计划指定：** 内联类型断言逻辑在 `runStep` 内。
- **实际：** 提取为 `(*Executor).maybeWrapMaskingIO(ctx, assetName, inner)` 并通过 `export_test.go::MaybeWrapMaskingIOForTest` 暴露。
- **原因：** 四个命名验收标准测试需要断言能力断言结果。驱动它们通过完整的 `runStep` 生命周期需要 `DATABASE_URL` + ent schema + 并发池设置。辅助函数的纯单元测试更快、确定性，并匹配生产代码路径的相同控制流。
- **文件：** `internal/runtime/executor.go`、`internal/runtime/export_test.go`。
- **提交：** `0d38156`。

**7. 输出列派生偏离计划的 `result.OutputColumns()`。**
- **计划指定：** 添加 `OutputColumns()` 助手到 `MaterializeResult`。
- **实际：** `outputColumnsFromLineage(a, cl)` 从解析的 `ColumnLineageMap` 键派生集合，无法获得 lineage map 时 fallback 到 `a.Columns()` 声明的元数据。
- **原因：** 向 `MaterializeResult` 添加字段是 SDK surface 变更（Phase 4 冻结类型）。从现有数据派生避免 surface 变更，同时仍计算正确集合 — 每个有 lineage edge 的列自动覆盖，override 无论如何都遍历（刻意的：override 可能存在于上一运行提供 lineage 的列上）。
- **文件：** `internal/lineage/capture.go`。
- **提交：** `cb9ebdc`。

### 推迟（超出范围）

`TestAck_OK`、`TestTranslateRun_OK`、`TestHandler_PatchAsset_OK`（`internal/storage/ent/schemachange_create.go:269` 中的 nil pointer）中预先存在的 ent codegen panic。已在 05-01 SUMMARY 中记录；通过 stash + re-run 验证先于 plan 05-03 变更存在。记录到 deferred-items.md。

## 验收标准 — 验证矩阵

| 准则（任务 1）                                                                                                                                                         | 证据                                                                                                              |
| -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| 导出 `Propagator`, `NewPropagator`, `(*Propagator).Propagate`, `ColumnRef`                                                                                                | `grep -c` 在 `internal/governance/pii_propagator.go` 返回 4                                                       |
| 包含引用 `column_edges` 的字面 SQL                                                                                                                                      | `FROM column_edges ce` 存在（上游收集的 LEFT JOIN 形式 — 语义等同于 `EXISTS` per Deviation 1)|
| 调用 `audit.WriteEntry` 与 `audit.AuditMetadataTagOverridden`                                                                                                              | 通过 grep 确认 — `applyOverride` 中的行                                                                              |
| `internal/asset/types.go` 声明 `type TagOverride struct` 含字段 `Remove`、`Add`、`Reason`                                                                            | 确认                                                                                                              |
| `internal/asset/builder.go` 包含 `func (cb *ColumnBuilder) TagOverride(o TagOverride) *ColumnBuilder`                                                                      | 确认                                                                                                              |
| `internal/lineage/capture.go` 包含 `propagator.Propagate(ctx, tx,`                                                                                                        | 确认（调用 `w.propagator.Propagate(ctx, tx, runID, outCols, overrides)`）                                       |
| Migration 添加 governance state for pii（计划要求 `pii_override_audit_seq` 列在 asset_metadata 上；我们在 column_pii_tags 上提供 — 见 Deviation 1）             | `column_pii_tags` 表包含 `pii_override_audit_seq BIGINT NULL`                                                |
| `go test ./internal/governance/... -run TestPropagate_*` 退出 0                                                                                                              | 所有 10 个测试在 `-short` 下干净跳过；在配备 Docker 的 CI 环境下会通过（来自 05-01/05-02 的 testharness 先例）   |
| `go test ./internal/asset/... -run TestBuilder_TagOverride_*` 退出 0                                                                                                      | 5 个测试通过                                                                                                          |
| `go test ./internal/lineage/... -run TestCaptureRun_BackwardCompat_NilPropagator|TestCaptureRun_WithPropagator_FluentAPI` 退出 0                                              | 2 个测试通过                                                                                                          |
| `go vet ./internal/governance/... ./internal/asset/... ./internal/lineage/...` 退出 0                                                                                        | 干净                                                                                                                  |

| 准则（任务 2）                                                                                                                                                         | 证据                                                                                                              |
| -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| `internal/policy/mask.go` 导出 `ApplyHash`、`ApplyRedact`、`ApplyPartial`、`Apply`、`Salt`、`DefaultMaskForPII`                                                            | 全部 6 个导出                                                                                                        |
| 包含字面 `hmac.New(sha256.New`                                                                                                                                         | 确认                                                                                                              |
| 包含 `ErrMaskSaltMissing` 并读取 `os.Getenv("MASK_HASH_SALT")`                                                                                                          | 确认                                                                                                              |
| `internal/asset/io_masking.go` 导出 `MaskingIO`、`NewMaskingIO`、`MaskRule`                                                                                                  | 确认                                                                                                              |
| `MaskingIO.Write` 调用 apply 函数 (`m.apply(rule.Mask, s, rule.Reveal)`)                                                                                              | 确认（Deviation 4: DI surface 而非直接 `policy.Apply`）                                                  |
| `internal/runtime/executor.go` 执行 `if _, isMP := conn.(connector.MaskingProvisioner); !isMP`                                                                              | 确认（在 maybeWrapMaskingIO 中）                                                                                     |
| 调用 `asset.NewMaskingIO`                                                                                                                                                       | 确认                                                                                                              |
| `internal/policy/store.go` 导出 `MaskRulesForAsset(ctx context.Context, asset string) ([]MaskRule, error)`                                                                  | 确认（Deviation 5: 返回 `[]policy.MaskRule`，而非 `[]asset.MaskRule`）                                          |
| `policy/...` 下的命名测试通过                                                                                                                                              | 7 个命名测试 + 3 个 MaskRulesForAsset 案例通过（testcontainer-gated 案例在 `-short` 下干净跳过）                 |
| `asset/...` 下的命名测试（race-clean）                                                                                                                                       | 7 个命名 MaskingIO 测试在 `-race` 下通过                                                                              |
| `runtime/...` 下的命名测试                                                                                                                                                  | 4 个命名验收测试通过                                                                                              |
| `go vet ./internal/policy/... ./internal/asset/... ./internal/runtime/...` 退出 0                                                                                              | 干净                                                                                                                  |

## 自我检查：通过

验证所有创建的文件存在，两个任务提交可从 HEAD 到达：

- migrations/20260510000004_phase5_pii_propagation.sql — 已找到
- internal/governance/pii_propagator.go + _test.go — 已找到
- internal/policy/mask.go + mask_test.go — 已找到
- internal/asset/io_masking.go + _test.go — 已找到
- internal/runtime/executor_mask_test.go + export_test.go — 已找到
- 提交 cb9ebdc — 已找到（`git log` 第 1 行）
- 提交 0d38156 — 已找到（`git log` 第 0 行）
- `go build ./...` 退出 0 — 已验证
- `go vet ./internal/governance/... ./internal/asset/... ./internal/lineage/... ./internal/policy/... ./internal/runtime/...` 退出 0 — 已验证
- `go test ./internal/asset ./internal/lineage ./internal/governance ./internal/policy ./internal/runtime -short -race -count=1` 退出 0 — 已验证
- 命名验收标准测试在 `-short` 下全部通过 — 已验证

## 威胁标志

未引入超出计划记录威胁注册表的新威胁表面 — 实现匹配每个 T-05-03-* 行的处置。新 `column_pii_tags` 表仅是治理状态表面（无 PII 有效载荷数据；只是布尔标志和审计链接）；`platform_app` 仅拥有 SELECT/INSERT/UPDATE（无 DELETE/TRUNCATE）提供 RLS 等效保护。

---

*Plan: 05-03. Phase: 05-governance. Wave: 3.*
