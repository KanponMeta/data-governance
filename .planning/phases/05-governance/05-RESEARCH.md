# Phase 5: 治理引擎 - Research

**Researched:** 2026-05-09
**Domain:** RBAC + 列级访问控制 + 仓库掩码同步 + 治理工作流 + 哈希链审计日志 + 质量规则 + 通知分发
**Confidence:** HIGH（CONTEXT.md 锁定 23 项决策；现有代码库验证 Phase 1–4 模式；外部 API 与库版本通过官方文档与 Go 模块代理双重验证）

---

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions（来自 CONTEXT.md `<decisions>`，逐字保留）

#### RBAC 模型与 Casbin 集成
- **D-01:** Casbin 仅负责 **角色 → API 权限** 映射；列级策略存放在独立的 `column_policies` 表中。Casbin model：标准 RBAC `p = (sub, obj, act)`，`obj` 为资源路径（`/assets/<name>/manage`、`/audit/export`、`/governance/approve`、`/policies/edit`、`/users/admin`），`act ∈ {read, write, manage}`。Casbin Postgres adapter 由本研究阶段决定。Phase 5 引入 Casbin v2.135.x。
- **D-02:** 列策略两路声明、运行时优先（同 Phase 4 D-17）：
  - Builder 默认（代码声明）：`asset.New("orders").ColumnPolicy(asset.ColumnPolicy{Column:"ssn", Mask:asset.MaskHash, AllowRoles:[]string{"pii-analyst"}})`，存放在 `asset_versions` 行，参与 code_hash（D-03）。
  - 运行时覆盖（REST）：`PATCH /assets/:name/columns/:col/policy → {mask, allow_roles, reason}`，存放在 `column_policies` 表（按 `(asset, column)` 唯一）。`effective = COALESCE(runtime_value, code_default)`。
  - 全局 YAML 默认（第三层面）：`policies.yaml` 映射 `tag → mask_default`（如 `pii: hash`）。最低优先级。
  - 读取顺序：runtime > builder > tag-default YAML > unmasked。
  - 审计：每次 PATCH / YAML 重载发出 `policy.changed` 到 **`audit_log`（不是 `event_log`）**，含 `{actor, before, after, reason}`。
- **D-03:** v1 mask 类型枚举且有限：
  - **Hash**（带平台 salt 的 SHA-256）；**Redact**（常量 `"***"`）；**Partial**（首/尾 N 字符可见）。
  - Tokenization、FPE、bucket 推迟至 v1.x。
  - 每种 mask 映射到：管道内（RBAC-05）的 Go 函数；Snowflake DDM 的参数化 `CREATE MASKING POLICY` 模板；BigQuery CLS 的 Data Catalog policy tag + IAM + 数据掩码模板。
- **D-04:** 仓库掩码同步（RBAC-04）= **Push-on-change + Reconcile loop**，两者通过 River：
  - Push：策略变更事务 enqueue `policy_sync` River 任务，调用连接器的 `MaskingProvisioner`（D-05）。失败重试由 River 指数退避；永久失败 emit `masking.sync_failed` audit_log + 告警。
  - Reconcile loop：新 `./platform reconciler` 守护进程（或 scheduler 子命令扩展）每 **15 分钟**（可配置）通过 `ListMaskingPolicies` 拉取仓库现状对比 `column_policies`。漂移触发 `masking.sync_drift_detected` 事件 + 自动 re-push。
  - Pitfall #8：平台**永远不代理查询**——掩码是仓库原生职责，平台是策略真相源。
- **D-05:** 新连接器可选能力 `connector.MaskingProvisioner`（同 Phase 4 D-06 SchemaDescriber 模式）：
  ```go
  type MaskingProvisioner interface {
      ApplyMaskingPolicy(ctx context.Context, asset AssetRef, policy ColumnPolicy) error
      RemoveMaskingPolicy(ctx context.Context, asset AssetRef, column string) error
      ListMaskingPolicies(ctx context.Context, asset AssetRef) ([]ColumnPolicy, error)
  }
  ```
  - Phase 5 仅在 **Snowflake** 与 **BigQuery** 实现。其他连接器（PostgreSQL、MySQL、S3、GCS、HDFS）走管道内掩码路径（D-03）。
  - 运行时类型断言：`if mp, ok := conn.(connector.MaskingProvisioner); ok { ... }`。
  - 具体 Snowflake/BigQuery API 形态由本研究阶段验证（STATE.md flag）。
- **D-06:** **PII 标签传播** 同步运行于 executor 元数据事务内（在 lineage_writer 写完 `column_edges` 之后）：
  - 触发：每个成功物化的 `lineage.captured`（Phase 4 D-01）。
  - 算法：每个输出列沿 `column_edges`（Phase 4 D-13）BFS 上游一跳；若任一源列 `pii=true`（在 `asset_metadata.tags` 中），下游列继承 `pii=true`，除非显式 override 存在。
  - **冲突解决：union**——任一上游 PII ⇒ 下游 PII。
  - **Override 机制：** `asset.New("orders_anonymized").Column("hashed_ssn").TagOverride(asset.TagOverride{Remove:"pii", Reason:"hashed at source via D-03 MaskHash; not reversible"})`。要求非空 `Reason`。Override 发出 `metadata.tag_overridden` 到 audit_log。
  - 同事务保证：不存在下游 PII 列暂时未掩码的窗口。
- **D-07:** `column_policies` 表延续 Phase 4 D-15 软退役/temporal table 模式：
  - 列：`(id, asset, column, mask_type, allow_roles, code_hash_first, code_hash_latest, first_seen_run_id NULLABLE, first_seen_at, last_seen_at, superseded_at NULLABLE, source ENUM(builder|runtime|yaml-default))`。
  - 活跃策略：`WHERE superseded_at IS NULL`。
  - 时间点查询：`WHERE first_seen_at <= $T AND (superseded_at IS NULL OR superseded_at > $T)`。
  - 删除被 RLS 禁止；策略"移除"=设置 `superseded_at = NOW()` + emit `policy.removed`。

#### 治理工作流
- **D-08:** 治理状态住在 **`asset_versions.governance_state`** 列：
  - 枚举：`draft | in_review | active | rejected`，CHECK 约束转换：`draft→in_review`（submit）；`in_review→active`（approve）；`in_review→rejected`（reject）；`rejected→in_review`（resubmit 同 code_hash）；`active→in_review`（admin 强制 re-review，罕见）。
  - 新 code_hash 默认 `draft`（每次代码变更回到 Draft——Pitfall #7 "old approval covers new code" 防护）。
  - 物化门：executor 检查 `governance_state = 'active'`，否则拒绝并 emit `governance.materialization_blocked`。Config flag `governance.gating_enabled` 默认 **false** for v1（保留 dev 工作流，生产显式开启）。
- **D-09:** Reviewer 分派三路并集：
  1. Builder：`asset.New("orders").Reviewers("team-data-gov", "privacy-team")`
  2. Tag-rule YAML：`policies.yaml` 映射 `tag → required_reviewer_roles`（如 `pii: ["privacy-team"]`）
  3. Owner fallback：`asset_metadata.owner` → `team_owners` 配置表 → reviewer role
  - 解析：`(1) ∪ (2) ∪ (3 if (1)∪(2) empty)`。
  - **Quorum：** 默认 1。`asset.New("x").Quorum(asset.QuorumAll)` 要求全员；`Quorum(2)` 要求 2 人。
  - Reviewer pool 在提交时快照到 `governance_reviews` 表——后续添加/移除角色不影响进行中的 review。
- **D-10:** 自动预审批检查管道（Pitfall #7："设计在人工路径之前"）。提交后顺序运行：
  1. **Schema break ack：** 任何未 ack 的 breaking schema_change → BLOCK
  2. **Policy/PII consistency：** 每个 `pii` 标签列必须有 column_policy → 缺失则 BLOCK
  3. **Quality config sanity：** 资产 QualityRule 必须解析、引用现有列 → 损坏则 BLOCK
  4. **Lineage drift：** Phase 4 D-04 `drift_status='pending'` → BLOCK
  5. **PII presence + reviewer：** 任何 `pii` 列 → 禁用 fast-path（必须人工 + privacy-team）
  - 全部通过 + 无 PII + 无 breaking schema_change → 状态直接进入 `active`，emit `governance.auto_approved` + 通知 owner。
  - Builder opt-out：`asset.New("x").RequireHumanReview()`。
- **D-11:** 强制 reject 评论 + SLA 提醒不自动升级：
  - `POST /governance/reviews/:id/reject` 必填 `comment`；CLI 同步必填 `--comment` flag。Approval comment 可选。
  - SLA timer：`governance.review_sla_hours`（默认 48h）。Scheduler tick 扫描 `submitted_at + sla_hours < NOW() AND decided_at IS NULL` → emit `governance.review_sla_breached` + 通知所有 reviewer + owner。**不自动升级**——SOC 2 要求人工证明。
  - 升级 opt-in：`.EscalationRoles(...)` 或全局 YAML，仅在显式配置时执行 + 审计。
- **D-12:** 提交生命周期：
  - `POST /governance/submit {asset, code_hash, reviewers_extra?}` → 创建 `governance_reviews` 行（绑 asset_version_id + submitter_id）+ reviewer pool 快照 + emit `governance.submitted`。
  - `POST /governance/reviews/:id/{approve|reject}` → 原子 update + 状态转换 + emit `governance.{approved|rejected}` + dispatch notification（D-21）。
  - REST + CLI：`./platform governance submit <asset>`、`./platform governance review <id> --approve|--reject [--comment=...]`、`./platform governance status [<asset>]`。

#### 审计哈希链与日志架构
- **D-13:** 审计日志位于 **专用 `audit_log` 表**，与 `event_log` 分离：
  - Schema：`(seq BIGSERIAL PRIMARY KEY, prev_hash BYTEA, self_hash BYTEA, occurred_at TIMESTAMPTZ, event_type TEXT, actor_id, resource_type, resource_id, payload JSONB)`。
  - 哈希构造：`self_hash = SHA-256(seq || prev_hash || occurred_at || event_type || actor_id || resource_type || resource_id || canonical_json(payload))`。Genesis：`prev_hash = bytea(32 zero bytes)`。
  - **独立 Postgres schema** `audit`，独立迁移用户 `audit_migrator`（Pitfall #5）。应用用户 `platform_app` 仅有 INSERT。RLS 禁止 UPDATE/DELETE。仅迁移用户能 DDL。
  - **插入协议：** `audit.WriteEntry(ctx, tx, entry)` 原子 helper。在 caller 事务内 `SELECT MAX(seq) FOR UPDATE` 哨兵行 → 计算 `self_hash` → 插入。并发写入通过 sentinel-row lock 串行化。治理 + 访问控制事件低速率，单锁可接受。
  - **为何与 event_log 分离：** event_log 高速率 run.* 事件；加入哈希链会导致 hot path 串行化。audit_log 小、低速、安全关键。
- **D-14:** Audit-log 内容范围**故意收窄**：
  - **In scope（写入 `audit_log`）：**
    - `policy.changed`、`policy.removed`、`masking.sync_failed`、`masking.sync_drift_detected`
    - `role.created`、`role.deleted`、`role.assigned`、`role.revoked`
    - `governance.submitted`、`governance.approved`、`governance.rejected`、`governance.auto_approved`、`governance.review_sla_breached`、`governance.materialization_blocked`
    - `audit.exported`、`audit.verify_failed`
    - `metadata.tag_overridden`
  - **Out of scope（保留在 `event_log`）：** `run.*`、`schedule.*`、`sensor.*`、`lineage.captured`、`schema.*`、`metadata.updated`（非 PII override 的元数据编辑）。
- **D-15:** 防篡改验证 + 导出（同 Phase 4 D-19 三层包装）：
  - **CLI** `./platform audit verify [--from=<seq>] [--to=<seq>]`——顺序扫描，重新计算每个 `self_hash`，不匹配则失败并打印 mismatch seq。Exit code 0 = 链完整，非零 = 检测到篡改。
  - **REST** `GET /audit/export?from=<ISO>&to=<ISO>&format=json|csv|jsonl`——流式响应（chunked transfer），每行包含 `seq` + `self_hash`。默认 `jsonl`。
  - **CLI** `./platform audit export --from=<ISO> --to=<ISO> --format=jsonl --out=<file>`——同一库函数 wrapper。
  - 三表面包装单一 Go library `internal/audit/{Verify,Export}`。
  - **v1 无后台 reconciler**——按需验证。后台 reconciler 推迟至 v1.x。
  - 导出审计日志本身 emit `audit.exported`（递归到同一链中）。
- **D-16:** GOV-07 retention TTL v1 部分实现：
  - `audit_log.expires_at TIMESTAMPTZ NULL`。全局配置 `audit.retention_default_days`（默认 NULL = infinite，多数合规场景 7-10 年）。允许按 event_type 覆盖。
  - 实际清除机制（特权后台任务 DELETE 过期行）**推迟 v1.x**——v1 仅 schema + 文档化运维 runbook + 在 v1 迁移中预留 purge 用户。
  - 资产数据 TTL（按连接器删除物化数据）**完全推迟 v1.x**。
- **D-17:** S3 Object Lock / WORM 哈希锚定 v1 仅接口：`internal/audit/anchor.Anchor` 接口 stub；v1.x 实现 `S3ObjectLockAnchor`。

#### 质量规则与通知
- **D-18:** 质量规则 DSL = builder 链式强类型。每个规则实现 `asset.QualityRule` 接口（`Name() string`，`Evaluate(ctx, eval QualityEvaluator) (QualityResult, error)`）。v1 三种类型：
  - `asset.NullCheck{Column string, MaxRate float64}` → `COUNT(NULL)/COUNT(*) <= MaxRate`
  - `asset.RangeCheck{Column string, Min, Max float64}` → `MIN(col) >= Min AND MAX(col) <= Max`
  - `asset.SQLAssertion{Name, SQL, Predicate AssertionPredicate}` → 用户 SQL，`${asset}` 插值物化表，Predicate 解释结果（`ScalarEqualsZero`、`ScalarLessThan(N)`、`RowCountIsZero`）
  - 其他类型（UniqueCheck、RegexCheck、FreshnessCheck-as-quality、自定义 Go predicate）推迟 v1.x。
- **D-19:** 质量评估在 executor 同事务内运行，紧随 lineage/schema 之后，独立运行状态列：
  - 顺序：`Materialize succeeded → lineage_writer → schema_writer → quality_evaluator → run.state=succeeded`，全部同 DB 事务。
  - **独立列：** `runs.run_quality_status ENUM(passed, failed, skipped, error)`（无规则资产默认 NULL/skipped）。`runs.state` 保留 Phase 2 D-17 lifecycle 语义——质量失败**不**翻转 `state`。
  - 与 Phase 1 D-09 + Phase 4 D-04 一致："metadata failures don't fail the data work"。
  - **Per-rule 结果：** 新 `quality_results` 表 `(run_id, rule_name, rule_type, status ENUM(passed,failed,error), measured_value, threshold, evaluated_at, error_message NULLABLE)`。`runs.run_quality_status` = 跨规则的最坏值。
  - **失败分发：** `quality.rule_failed` event_log + River 任务派发告警（D-21）。
  - **连接器 evaluator：** `connector.QueryAggregate(ctx, sql) (any, error)` 新稳定能力。不实现的连接器（如纯文件 S3）→ rule 状态 `error` + 原因 "connector does not support aggregate queries"。
- **D-20:** QUAL-04 Freshness SLA 由 **scheduler 子命令** 评估，不是质量规则：
  - Builder：`asset.New("x").FreshnessSLA(asset.FreshnessSLA{MaxLag: 6*time.Hour, ScopeAfterCronFire: true})`。
  - 新 `schedules.last_succeeded_at` 列。Scheduler tick（Phase 3 D-01..04）扩展扫描 `last_succeeded_at + max_lag < NOW()` → emit `sla.breached` + River 派发告警。每 SLA breach window 仅一个告警（dedup `(asset, sla_breach_window_start)`）。
- **D-21:** 通知与告警：
  - **通道（v1）：** webhook（POST JSON）+ SMTP（启动配置自带 host/port/user/password/from）。SES、SendGrid、Slack 推迟 v1.x。
  - **路由 `notifications.yaml`：**
    ```yaml
    rules:
      - match: "governance.submitted"
        webhook: "https://internal/governance"
        email_to: "{reviewer_emails}"
      - match: "quality.rule_failed"
        webhook: "https://internal/alerts"
        email_to: "{owner_email}"
      - match: "sla.breached"
        webhook: "https://pagerduty.example/v2/enqueue"
    ```
  - **派送：** 全部经 River 任务（不阻塞 executor / scheduler / governance handler hot path）。River 原生重试。永久失败 emit `notification.dispatch_failed` event_log + 结构化错误日志。
  - **提交者通知（GOV-04）：** `governance.{approved,rejected}` → notification rule → 提交者邮件（依赖 `users.email`，AUTH-01 已就绪）。

#### 计划分区提示
- **D-22:** 建议分区（planner 可调整）：
  - **05-01 RBAC 基础：** Casbin 集成 + Postgres adapter + roles/users/role_assignments + role-permission CRUD REST/CLI + audit_log 表 + RLS schema + hash-chain writer + verify CLI
  - **05-02 列策略 + 仓库掩码同步：** column_policies 表 + ColumnPolicy DSL + REST PATCH + global YAML loader + MaskingProvisioner + Snowflake DDM impl + BigQuery CLS impl + Push River 任务 + Reconcile loop + sync_failed/drift 事件
  - **05-03 PII 传播 + 非仓库管道掩码：** lineage_writer 扩展 + TagOverride DSL + 非仓库 mask 函数 at AssetIO.Write + 连接器能力断言顺序
  - **05-04 治理工作流：** asset_versions.governance_state + governance_reviews + .Reviewers/.Quorum/.RequireHumanReview/.EscalationRoles DSL + auto pre-approval pipeline + REST + CLI + 物化门
  - **05-05 质量规则 + SLA + 告警：** QualityRule + 三种 rule + executor 事务扩展 + run_quality_status + quality_results + connector.QueryAggregate + FreshnessSLA + scheduler 扩展 + notification dispatcher + River pipeline

#### 事件类型新增
- **D-23:**
  - `event_log` 新增（CHECK 约束附加）：`quality.rule_passed`、`quality.rule_failed`、`quality.rule_error`、`quality.run_evaluated`、`sla.breached`、`sla.recovered`、`notification.dispatched`、`notification.dispatch_failed`、`governance.materialization_blocked`
  - `audit_log` 类型（D-14 范围）：`policy.changed`、`policy.removed`、`masking.sync_failed`、`masking.sync_drift_detected`、`role.created/deleted/assigned/revoked`、`governance.submitted/approved/rejected/auto_approved/review_sla_breached`、`audit.exported/verify_failed`、`metadata.tag_overridden`

### Claude's Discretion（来自 CONTEXT.md `<discretion>`，本研究负责给出建议）
- Casbin Postgres adapter 选型（`casbin/casbin-pg-adapter` v1.5.0 vs `pckhoi/casbin-pgx-adapter/v3` vs in-house thin adapter）
- Snowflake DDM API 调用形态、SQL 模板细节
- BigQuery CLS Data Catalog policy tag taxonomy 结构
- Mask 类型实现细节（Hash salt 管理、Partial 默认长度、Redact 字符集）
- `policies.yaml` 路径与 reload 语义
- Quorum=All 在审批中途 reviewer pool 变化时的语义
- Audit-log JSONL 行 schema 版本化策略
- River 队列拓扑（独立 `policy_sync` 与 `notification` 队列 vs 单队列优先级）
- `notifications.yaml` 模板变量语言
- Reviewer pool 快照持久化形态（denormalized JSON vs 独立 join 表）
- `column_policies` 是否带 `partition_key`（推迟 v1.x）
- Audit verify CLI 输出格式默认值

### Deferred Ideas (OUT OF SCOPE)
（来自 CONTEXT.md `<deferred>`，研究阶段不予探索）
- Asset-data retention TTL 执行（v1 仅 schema）
- External S3 Object Lock 实现（v1 仅接口）
- 后台 tamper-detection reconciler（v1.x）
- Auto SLA 升级（v1.x）
- 额外 mask 类型（Tokenization、FPE、bucketing）
- 额外质量规则类型（UniqueCheck、RegexCheck、custom predicate）
- 额外通知渠道（Slack、SES、SendGrid、MS Teams、PagerDuty events API）
- AGOV-01/02、PLAT-02、自定义 Casbin model 文件
- Audit verify checkpoint 汇总
- 结构化 rejection 原因分类
- OpenLineage-format audit export
- Column-policy partition awareness
</user_constraints>

---

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| RBAC-01 | 管理员可定义命名角色 | D-01 + §1 Casbin enforcer + roles 表；REST `/roles` POST，emit `role.created` audit_log |
| RBAC-02 | 管理员可分配用户到角色 | D-01 + §1 role_assignments 表；REST `/users/:id/roles` PUT，emit `role.assigned` audit_log |
| RBAC-03 | 管理员可定义列级访问策略 | D-02 + §3 column_policies 表 + ColumnPolicy DSL + REST PATCH + global YAML |
| RBAC-04 | 同步策略到 Snowflake DDM 与 BigQuery CLS | D-04/D-05 + §4 MaskingProvisioner + §4.2 Snowflake DDL 模板 + §4.3 BigQuery Data Catalog API + Push River + Reconcile loop |
| RBAC-05 | 非仓库连接器在管道物化时掩码 | D-03/D-05 + §5 AssetIO.Write 包装 + 管道掩码函数（Hash/Redact/Partial）|
| RBAC-06 | 所有数据访问事件、策略变更、用户操作进入哈希链审计日志 | D-13/D-14 + §2 audit schema + sentinel-row 串行化 + canonical JSON + RLS + sentinel-row hash chain writer |
| GOV-01 | 数据工程师可提交资产审核 | D-08/D-12 + §6 governance_reviews + REST/CLI submit |
| GOV-02 | 平台分配审核给治理团队 + 通知 | D-09/D-21 + §6 reviewer pool 解析 + River notification |
| GOV-03 | 审核者携带必填评论批准/驳回；状态转换 | D-08/D-11/D-12 + §6 状态机 + reject 必填 comment + 原子 update |
| GOV-04 | 审核决策通知提交者 | D-21 + §7 notifications.yaml 路由 + River SMTP/webhook |
| GOV-05 | 所有审批决策记录在审计日志 | D-13/D-14 + §2 hash-chain writer + governance.* 事件类型 |
| GOV-06 | 完整审计日志导出为 JSON/CSV | D-15 + §2.4 streaming export + JSONL/CSV serializer + recursive `audit.exported` |
| GOV-07 | 审计日志 retention TTL 配置（v1 部分） | D-16 + §2.5 expires_at + global config + purge runbook |
| QUAL-01 | 工程师定义质量规则（空值率、范围、SQL 断言）| D-18 + §8 NullCheck/RangeCheck/SQLAssertion DSL |
| QUAL-02 | 物化后自动评估全部规则 | D-19 + §8 executor.commitSuccess 扩展 + 同事务 |
| QUAL-03 | 失败标记 run.run_quality_status + UI 显示 | D-19 + §8 quality_results 表 + run_quality_status 列 |
| QUAL-04 | 资产 SLA 阈值（N 小时内物化）| D-20 + §9 FreshnessSLA + scheduler tick 扩展 + last_succeeded_at |
| QUAL-05 | 质量失败/SLA 违反发送告警 | D-21 + §7 River dispatcher + webhook + SMTP |
</phase_requirements>

---

## Project Constraints (from CLAUDE.md)

CLAUDE.md 直接约束本研究：

- **Go 后端**：本阶段所有代码 Go；不引入 Python 运行时依赖
- **PostgreSQL 主存储**：通过 ent + sqlc 双 ORM 模式；新表用 ent；热读用 sqlc
- **Casbin v2.135.x**：CLAUDE.md §授权 锁定为首选 RBAC 库（**已验证**：v2.135.0 确为 Casbin v2 最新发布版）
- **golang-jwt/jwt/v5 v5.3.x**：已在 Phase 1 引入（`go.mod` 验证：v5.3.0）
- **River v0.35.x**：CLAUDE.md §执行引擎 锁定为作业队列（**验证**：当前 v0.35.1，2026-04-26 发布）
- **chi/v5 v5.2.5**：HTTP 路由
- **Atlas + ent**：迁移路径
- **`log/slog`**：所有结构化日志
- **`prometheus/client_golang`**：指标暴露（已在 go.mod，v1.19.1）
- **`gopkg.in/yaml.v3`**：已存在，用于 `policies.yaml` / `notifications.yaml`
- **API 稳定性（CONN-08）**：Phase 5 仅**可加性**扩展 `connector` 接口（新增可选 `MaskingProvisioner`、`QueryAggregate`），不破坏现有
- **单二进制约束**：无外部消息代理；通知通道仅 webhook + SMTP（与 D-21 一致）
- **OpenLineage**：Phase 4 已搭建。Phase 5 治理事件**不**走 OpenLineage（D-14 范围明确排除）
- **中文文档优先**：技术决策文档使用中文，配代码标识符使用英文（同 Phase 4 RESEARCH.md 风格）

---

## Summary

Phase 5 在 Phase 1–4 提供的元数据 + 执行 + 血缘骨架之上构建 **治理引擎**——管理员定义角色与列级策略、平台同步至 Snowflake DDM 与 BigQuery CLS、工程师通过审批工作流提交资产、所有治理操作进入防篡改哈希链审计日志、质量规则在每次物化时自动评估并通过 webhook + SMTP 派发告警。

整个 Phase 是 **5 个建议子计划**（D-22）的累加：05-01 RBAC + 哈希链 → 05-02 列策略 + 仓库掩码同步 → 05-03 PII 传播 → 05-04 治理工作流 → 05-05 质量规则 + SLA + 告警。每个子计划均与既定模式对齐：ent 拥有 CRUD、sqlc 拥有热读 CTE/聚合、Atlas 处理迁移、Phase 4 D-19 三表面（Go library + REST + CLI）、River 处理所有外部 IO 异步化（DDM/CLS sync、webhook 发送、SMTP 发送）、Phase 1 D-09 RLS-immutability 扩展为带 sentinel-row 串行化的哈希链。

技术上最具挑战的四个区域是：
1. **哈希链写入器**：sentinel-row `SELECT … FOR UPDATE` 在 caller 事务内串行化、RFC 8785 JCS canonical JSON、SHA-256(prev || canonical(row))、Postgres 独立 schema + RLS（`platform_app` INSERT-only，UPDATE/DELETE 完全禁止）
2. **Snowflake DDM 同步**：`CREATE OR REPLACE MASKING POLICY` 是 schema-scoped 对象，绑定到列后 `ALTER MASKING POLICY name SET BODY -> ...` 在下次查询时生效（不影响在飞查询）；连接器需要 `APPLY MASKING POLICY ON ACCOUNT` 角色权限
3. **BigQuery CLS 同步**：通过 Data Catalog Taxonomy + Policy Tag + IAM 三层；`cloud.google.com/go/datacatalog/apiv1` PolicyTagManagerClient + BigQuery Tables.update（columns[].policyTags）；表/列限制 1000 个 policy tags，IAM 传播有最终一致性窗口
4. **PII 传播**：必须在 lineage_writer 同事务内同步运行（D-06）——异步会出现下游列暂时未掩码的窗口

**Primary recommendation:** 按 D-22 顺序构建。**05-01 是关键路径**：哈希链一旦写入第一条记录就无法重构（Pitfall #5），必须在第一个治理事件之前定义并测试 canonical JSON + sentinel-row + RLS 三件套。

---

## Standard Stack

### Core (新增依赖)

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/casbin/casbin/v2` | **v2.135.0**（最新；2025-12 系列）[VERIFIED: `go list -m -versions` 2026-05-09，最新发布 v2.135.0] | RBAC enforcer（角色 → API 权限）| CLAUDE.md §授权 锁定首选；多策略后端、role hierarchy、`g(r.sub, p.sub) && keyMatch(r.obj, p.obj)` 匹配开箱即用 [CITED: https://casbin.apache.org/docs/rbac] |
| `github.com/pckhoi/casbin-pgx-adapter/v3` | **v3.2.0**（2024-08，最新）[VERIFIED: `go list -m -versions` 2026-05-09] | Casbin Postgres adapter via pgx/v5 | **推荐选项** — 项目已用 `pgx/v5 v5.9.1`，零 Driver 重复。`casbin/casbin-pg-adapter` v1.5.0（2025-11）使用 `go-pg/v9`，go-pg 已停止维护，会引入第二个 Postgres driver [VERIFIED: https://github.com/casbin/casbin-pg-adapter/blob/master/adapter.go imports `github.com/go-pg/pg/v9`] |
| `github.com/IguteChung/casbin-psql-watcher` | latest（2024-2025）| Casbin watcher via Postgres LISTEN/NOTIFY | **推荐**：v1 单二进制运行时单 enforcer 不需要；但平台架构允许 worker 独立进程（Phase 2/3 既定）→ watcher 在多进程部署下保持策略同步。Phase 5 v1 可仅在 README 提及 + 接口预留，避免过度工程 [CITED: https://github.com/IguteChung/casbin-psql-watcher] |
| `github.com/riverqueue/river` | **v0.35.1**（2026-04-26）[VERIFIED: `go list -m -versions` 2026-05-09] | 异步任务：policy_sync、notification_dispatch | CLAUDE.md §执行引擎 已锁定。原生指数退避、唯一任务、周期任务、跨进程协调 [CITED: https://riverqueue.com/docs/job-retries] |
| `github.com/riverqueue/river/riverdriver/riverpgxv5` | 与 river 配套 | River pgx/v5 driver | River 推荐 driver；与项目现有 pgx/v5 完美匹配 |
| `github.com/gowebpki/jcs` | **latest**（最近发布）| RFC 8785 JSON Canonicalization Scheme | 哈希链 canonical JSON 序列化必需。两个候选：`gowebpki/jcs`（Go-native）vs `lenny321/json-canon`（v0.2.0 2026-02）。**推荐 gowebpki/jcs**——成熟、零依赖 [CITED: https://github.com/gowebpki/jcs] |
| `github.com/wneessen/go-mail` | **latest**（活跃维护）| SMTP 邮件发送 | `gopkg.in/mail.v2` (gomail) 维护停滞；`go-mail` 是现代替代，concurrency-safe，html/template 集成、go-mail 自定义 SMTP 包扩展支持更多 SASL 机制 [CITED: https://pkg.go.dev/github.com/wneessen/go-mail] |
| `cloud.google.com/go/datacatalog` | **v1.31.0** (最新)[VERIFIED: `go list -m -versions` 2026-05-09] | BigQuery CLS：创建/管理 Taxonomy + PolicyTag + IAM bindings | 官方 Google Go SDK；`PolicyTagManagerClient`，包路径 `cloud.google.com/go/datacatalog/apiv1` |

### 已在 go.mod，无需新增

| Library | Version | 用途 |
|---------|---------|-----|
| `github.com/golang-jwt/jwt/v5` | v5.3.0 | JWT signing/verify（auth.Middleware 已用，扩展 claims 支持 `roles []string`）|
| `github.com/jackc/pgx/v5` | v5.9.1 | 主 driver（哈希链 sentinel-row, audit_log 写入）|
| `entgo.io/ent` | v0.14.0 | 新实体：Role, RoleAssignment, ColumnPolicy, GovernanceReview, QualityRule, QualityResult, AuditLogEntry |
| `github.com/sqlc-dev/sqlc` | v1.31.x（tool）| 哈希链 verify 顺序扫描、reviewer 解析、SLA 扫描 |
| `github.com/snowflakedb/gosnowflake` | **v1.19.1**（最新）[VERIFIED: 2026-05-09] | Snowflake DDM DDL 执行（database/sql 模式 ExecContext）|
| `cloud.google.com/go/bigquery` | v1.77.0 | BigQuery 表元数据更新（写 policy tag 到列）|
| `github.com/go-chi/chi/v5` | v5.2.5 | 新路由：`/audit/*`, `/governance/*`, `/policies/*`, `/roles`, `/users/:id/roles` |
| `gopkg.in/yaml.v3` | v3.0.1 | `policies.yaml`、`notifications.yaml` 加载 |
| `github.com/prometheus/client_golang` | v1.19.1 | Counter/Gauge：`audit.verify_failed`, `masking.sync_failed`, `quality.rule_failed`, `governance.review_sla_breached` |
| `crypto/sha256`、`encoding/json`、`log/slog` | stdlib | 哈希计算、payload 序列化、结构化日志 |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `pckhoi/casbin-pgx-adapter/v3` | `casbin/casbin-pg-adapter` v1.5.0 | 官方但用 go-pg/v9（停止维护的 driver），引入双 Postgres driver。**拒绝。** |
| `pckhoi/casbin-pgx-adapter/v3` | 自建 thin adapter on existing pgx pool | 减少一个外部依赖。但 Casbin Adapter 接口 + FilteredAdapter + Watcher 协议需要正确实现；外部库已经 battle-tested。**拒绝**——D-22 估算下，自建 thin adapter 是 plan-level 风险。 |
| Go `gopkg.in/mail.v2` (gomail) | `github.com/wneessen/go-mail` | gomail 维护停滞（GitHub 最近更新很久之前），不再支持现代 SASL/STARTTLS 行为。**拒绝。** |
| `gowebpki/jcs` (RFC 8785) | 简单 `json.Marshal` + 手动排序 keys | 简单实现存在浮点序列化、Unicode 转义、空对象等"边界情况"。RFC 8785 是密码学应用的标准。**拒绝**——哈希链不能容忍序列化变化导致历史 hash 失效。 |
| Push + Reconcile (D-04) | 仅 Push | 静默漂移（DBA 直接修改仓库无法发现）。Pitfall #8 反对。**拒绝。** |
| 同事务 PII 传播 (D-06) | 异步 River job 传播 | 下游列暂时未掩码窗口 → 合规风险。**拒绝。** |
| 同事务质量评估 (D-19) | 异步 River job 评估 | UI 看到 run 完成但 quality pending；告警延迟。**拒绝。** |
| BigQuery 数据掩码（columnDataMaskingExemptionRules）| 仅 PolicyTag + Fine-Grained Reader | 数据掩码是**额外**功能（"To enhance column-level access control, you can optionally use dynamic data masking"）。v1 仅 PolicyTag 拒绝/允许；mask 类型映射到掩码函数推迟到 05-02 detail design [CITED: https://docs.cloud.google.com/bigquery/docs/column-level-security-intro] |

**Installation:**
```bash
go get github.com/casbin/casbin/v2@v2.135.0
go get github.com/pckhoi/casbin-pgx-adapter/v3@v3.2.0
go get github.com/IguteChung/casbin-psql-watcher@latest  # 接口预留；v1 不强依赖
go get github.com/riverqueue/river@v0.35.1
go get github.com/riverqueue/river/riverdriver/riverpgxv5@v0.35.1
go get github.com/gowebpki/jcs@latest
go get github.com/wneessen/go-mail@latest
go get cloud.google.com/go/datacatalog@v1.31.0  # 升级现有 indirect
```

**Version verification（2026-05-09 通过 `go list -m -versions <module>` 验证 + Go 模块代理）：**
- Casbin v2.135.0 确为最新（`go list` 显示完整发布列表停在 v2.135.0；CLAUDE.md "v2.135.x" flagged）
- River v0.35.1 确为最新（2026-04-26 发布）
- gosnowflake v1.19.1 确为最新（与项目 go.mod 一致）
- cloud.google.com/go/datacatalog v1.31.0 是最新（项目当前为 indirect，需升级为 direct）
- pckhoi/casbin-pgx-adapter v3.2.0（最近 stable，符合 pgx/v5）

---

## Architecture Patterns

### Recommended Package Structure (新增于 Phase 5)

```
internal/
├── auth/
│   ├── casbin.go            # NEW: Casbin enforcer 初始化 + 加载 model.conf + Postgres adapter + watcher
│   ├── jwt.go               # 既有：扩展 Claims 增加 Roles []string
│   ├── middleware.go        # 既有：扩展 Permission(action, obj) 中间件，调用 enforcer.Enforce
│   └── service.go           # 既有：扩展 AssignRole / RevokeRole API
├── audit/
│   ├── writer.go            # NEW: WriteEntry(ctx, tx, entry) — sentinel-row FOR UPDATE + canonical JSON + SHA-256 + INSERT
│   ├── canonical.go         # NEW: 包装 gowebpki/jcs.Transform 提供 CanonicalJSON 函数
│   ├── verify.go            # NEW: Verify(ctx, from, to seq) error — 顺序扫描，重新计算每行 hash，第一处不匹配立即返回 mismatch seq
│   ├── export.go            # NEW: Export(ctx, w io.Writer, format) — 流式 JSONL/CSV，含 seq + self_hash
│   ├── types.go             # NEW: AuditEventType enum + 各类 typed payload 结构
│   ├── anchor.go            # NEW: Anchor 接口 stub（D-17）
│   └── retention.go         # NEW: expires_at helper（D-16，仅 schema 字段管理；不实现 purge）
├── policy/
│   ├── store.go             # NEW: column_policies CRUD（ent client）+ COALESCE 解析（runtime > builder > yaml-default）
│   ├── mask.go              # NEW: MaskType enum + Hash/Redact/Partial 实现 + apply(row Row) Row（用于 RBAC-05 管道掩码）
│   ├── yaml_loader.go       # NEW: 加载 policies.yaml — tag→mask 默认 + tag→reviewer 角色（D-09 第 2 路）
│   └── handler.go           # NEW: REST handler PATCH /assets/:name/columns/:col/policy
├── governance/
│   ├── workflow.go          # NEW: Submit / Approve / Reject 业务逻辑 + 状态机转换 CHECK
│   ├── reviewers.go         # NEW: Reviewer pool 解析（builder + tag + owner 三路 + Quorum）
│   ├── auto_approval.go     # NEW: Pre-approval pipeline（5 项检查）
│   ├── pii_propagator.go    # NEW: 沿 column_edges BFS + union 规则 + override 检测（D-06，被 lineage_writer 同事务调用）
│   ├── handler.go           # NEW: REST POST /governance/submit, /governance/reviews/:id/approve|reject
│   ├── sla_scanner.go       # NEW: scheduler tick 调用 — 扫描超 SLA 的 reviews
│   └── service.go           # NEW: 集成所有 governance 子模块的门面
├── quality/
│   ├── rule.go              # NEW: QualityRule 接口 + NullCheck/RangeCheck/SQLAssertion 实现
│   ├── evaluator.go         # NEW: 在 executor.commitSuccess 同事务内评估全部规则
│   ├── store.go             # NEW: quality_results 表 CRUD + run_quality_status 更新
│   ├── freshness.go         # NEW: FreshnessSLA 类型 + scheduler 扫描 helper
│   └── dispatcher.go        # NEW: quality 失败 → enqueue notification River job
├── notification/
│   ├── channel.go           # NEW: Channel 接口（webhook、smtp）+ 两个实现
│   ├── webhook.go           # NEW: HMAC-SHA256 签名 + retry-aware 派送
│   ├── smtp.go              # NEW: go-mail 包装（host/port/user/password/from 启动配置）
│   ├── router.go            # NEW: notifications.yaml 加载 + event-type pattern → channel 路由
│   ├── template.go          # NEW: 简单 {var} 替换（推荐拒绝完整 Go template 引擎以保持可读性）
│   └── worker.go            # NEW: River 工作器：消费 notification.dispatch 任务
├── connector/
│   ├── capability.go        # 扩展：MaskingProvisioner + QueryAggregate 接口（同 D-05/D-19 模式）
│   └── firstparty/
│       ├── snowflake/
│       │   └── masking.go   # NEW: 实现 MaskingProvisioner — CREATE/ALTER/DROP MASKING POLICY DDL
│       └── bigquery/
│           └── masking.go   # NEW: 实现 MaskingProvisioner — Data Catalog Taxonomy + PolicyTag + Tables.update
├── runtime/
│   └── executor.go          # 扩展：commitSuccess 增加 quality_evaluator 调用；runStep 前增加 governance gate
├── lineage/
│   └── capture.go           # 扩展：CaptureRun 内调用 governance.PIIPropagator
├── storage/ent/schema/
│   ├── role.go              # NEW
│   ├── role_assignment.go   # NEW
│   ├── column_policy.go     # NEW（temporal table 模式）
│   ├── governance_review.go # NEW
│   ├── quality_rule.go      # NEW（asset_versions 关联）
│   ├── quality_result.go    # NEW
│   ├── audit_log_entry.go   # NEW（独立 schema "audit"）
│   └── asset_version.go     # 扩展：增加 governance_state 列
├── api/
│   └── routes.go            # 扩展：注册新 chi 路由（带 Casbin 中间件）
cmd/platform/
├── audit.go                 # NEW: ./platform audit verify | export
├── governance.go            # NEW: ./platform governance submit | review | status
├── policy.go                # NEW: ./platform policy show | list
├── role.go                  # NEW: ./platform role create | assign | revoke
├── reconciler.go            # NEW: ./platform reconciler — 15 分钟 tick 调用 MaskingProvisioner.ListMaskingPolicies
└── scheduler.go             # 扩展：tick 调用 quality.FreshnessScanner + governance.SLAScanner
migrations/
└── 20260510000000_phase5_governance.sql
   # 一次性引入：roles, role_assignments, column_policies (temporal),
   # governance_reviews, quality_rules, quality_results,
   # asset_versions.governance_state ALTER, runs.run_quality_status ALTER,
   # schedules.last_succeeded_at ALTER + audit schema + audit_log + audit_sentinel +
   # RLS 关于 audit schema + Casbin policy 表 (casbin_rule)
   # + CHECK 约束扩展（event_log.event_type、audit_log.event_type）
   # + 双角色：audit_migrator（DDL）+ audit_purge（v1.x 准备的清除用户）
```

### Pattern 1: 哈希链写入（D-13）

**Source:** PostgreSQL Wiki + Pitfall #5 + Phase 1 D-09 RLS 扩展。

```go
// internal/audit/writer.go
// Source: based on Pitfall #5 + Phase 1 D-09 RLS pattern
package audit

import (
    "context"
    "crypto/sha256"
    "database/sql"
    "encoding/binary"
    "fmt"
    "time"

    "github.com/google/uuid"
    "github.com/gowebpki/jcs"
)

type Entry struct {
    EventType    AuditEventType
    OccurredAt   time.Time
    ActorID      *uuid.UUID
    ResourceType string
    ResourceID   string
    Payload      any        // 任意 JSON-serializable
    ExpiresAt    *time.Time // D-16 retention
}

// WriteEntry 在 caller 事务内顺序串行化写入哈希链。
// caller 必须已经 BEGIN 并传入 *sql.Tx；commit/rollback 由 caller 负责。
//
// 协议：
// 1. SELECT seq, self_hash FROM audit.audit_sentinel WHERE id=1 FOR UPDATE
//    (sentinel 是初始化迁移插入的单行；FOR UPDATE 串行化所有写入者)
// 2. canonical = jcs.Transform(payloadJSON)
// 3. h = SHA-256(seq+1 || prev.self_hash || ts || event_type || actor || resource_type || resource_id || canonical)
// 4. INSERT INTO audit.audit_log (...) VALUES (...)
// 5. UPDATE audit.audit_sentinel SET seq = seq+1, self_hash = h WHERE id=1
//
// 复杂度：每次写入一个事务级锁。治理 + access-control 速率低（≪ run.* rate）→ 单锁可接受。
func WriteEntry(ctx context.Context, tx *sql.Tx, e Entry) (seq int64, err error) {
    var prevSeq int64
    var prevHash []byte
    if err := tx.QueryRowContext(ctx,
        `SELECT seq, self_hash FROM audit.audit_sentinel WHERE id = 1 FOR UPDATE`,
    ).Scan(&prevSeq, &prevHash); err != nil {
        return 0, fmt.Errorf("audit: lock sentinel: %w", err)
    }
    seq = prevSeq + 1

    payloadJSON, err := encodeCanonical(e.Payload) // jcs.Transform under the hood
    if err != nil {
        return 0, fmt.Errorf("audit: canonicalize: %w", err)
    }

    h := sha256.New()
    var seqBuf [8]byte
    binary.BigEndian.PutUint64(seqBuf[:], uint64(seq))
    h.Write(seqBuf[:])
    h.Write(prevHash)
    var tsBuf [8]byte
    binary.BigEndian.PutUint64(tsBuf[:], uint64(e.OccurredAt.UnixNano()))
    h.Write(tsBuf[:])
    h.Write([]byte(e.EventType))
    if e.ActorID != nil {
        h.Write(e.ActorID[:])
    }
    h.Write([]byte(e.ResourceType))
    h.Write([]byte(e.ResourceID))
    h.Write(payloadJSON)
    selfHash := h.Sum(nil)

    if _, err := tx.ExecContext(ctx, `
        INSERT INTO audit.audit_log
          (seq, prev_hash, self_hash, occurred_at, event_type, actor_id, resource_type, resource_id, payload, expires_at)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10)`,
        seq, prevHash, selfHash, e.OccurredAt, string(e.EventType),
        e.ActorID, e.ResourceType, e.ResourceID, payloadJSON, e.ExpiresAt,
    ); err != nil {
        return 0, fmt.Errorf("audit: insert: %w", err)
    }

    if _, err := tx.ExecContext(ctx, `
        UPDATE audit.audit_sentinel SET seq = $1, self_hash = $2 WHERE id = 1`,
        seq, selfHash,
    ); err != nil {
        return 0, fmt.Errorf("audit: update sentinel: %w", err)
    }
    return seq, nil
}
```

**关键细节：**
- `audit.audit_sentinel` 在 phase5 迁移中插入单行 `(id=1, seq=0, self_hash=bytea(32 zero bytes))`——这是 Genesis。
- `FOR UPDATE` 串行化整个治理事件流。**估算速率：** 单实例治理事件 ≪ 1 事件/秒（人工触发）→ 单锁是 over-engineered 的反面。事实上，sentinel-row 锁已是 Phase 4 D-13 类似 `column_edges` 写入的成熟模式。
- `jcs.Transform` (RFC 8785) 保证跨语言/版本/序列化器的字节级一致——这是哈希链长期验证（10 年合规保留）的前提。简单 `json.Marshal` + 排序 keys 不足以应对浮点 / Unicode 转义边界。

### Pattern 2: 仓库掩码连接器能力（D-05，扩展 Phase 4 D-06）

**Source:** Phase 4 `internal/connector/capability.go` SchemaDescriber 模式。

```go
// internal/connector/capability.go (extension)
// Source: Phase 4 D-06 SchemaDescriber pattern
package connector

import "context"

// MaskingProvisioner 是可选能力（Phase 5 D-05）。
// Snowflake 与 BigQuery 连接器实现此接口；Postgres/MySQL/S3/GCS/HDFS 不实现，
// 由 RBAC-05 管道掩码替代（internal/policy/mask.go 在 AssetIO.Write 时应用）。
type MaskingProvisioner interface {
    ApplyMaskingPolicy(ctx context.Context, asset AssetRef, policy ColumnPolicy) error
    RemoveMaskingPolicy(ctx context.Context, asset AssetRef, column string) error
    ListMaskingPolicies(ctx context.Context, asset AssetRef) ([]ColumnPolicy, error)
}

// QueryAggregate 是可选能力（Phase 5 D-19）。质量规则评估器要求连接器
// 能执行聚合 SQL 返回标量或行计数。Postgres/MySQL/Snowflake/BigQuery 均能实现；
// S3/GCS/HDFS 文件型连接器不能 → 质量规则状态 'error'，告警发出。
type QueryAggregate interface {
    QueryAggregate(ctx context.Context, asset AssetRef, sql string) (any, error)
}
```

### Pattern 3: 三表面 (Phase 4 D-19) — Audit Verify 例

```go
// 单一 Go library function（被 REST + CLI + 内部代码共用）
// internal/audit/verify.go
func Verify(ctx context.Context, db *sql.DB, fromSeq, toSeq int64) (Result, error) { /* ... */ }

// REST handler — 包装 Verify，返回 200 + JSON 报告
// internal/api/audit.go
func handleAuditVerify(w http.ResponseWriter, r *http.Request) { /* ... */ }

// CLI subcommand — 包装 Verify，输出 table 或 --format=json
// cmd/platform/audit.go
func auditVerifyCmd(args []string) { /* ... */ }
```

### Anti-Patterns to Avoid

- **❌ 自建 RBAC：** Casbin 处理边界正确（cycle detection、implicit roles、role inheritance limit）。自建会反复踩 hrekov.com 列出的同样的坑。
- **❌ 在哈希链中包含 run.* 事件：** D-13/D-14 范围明确——hot path 串行化致命。
- **❌ 异步 PII 传播：** D-06 同事务保证不可放弃。
- **❌ 用 `json.Marshal` 做 canonical：** 浮点序列化、空对象、Unicode 转义都会破坏长期 hash 验证。必须用 RFC 8785 JCS。
- **❌ 让 audit_log 与业务表共用迁移用户：** Pitfall #5 明确反对。独立 schema、独立用户、独立备份。
- **❌ 同步 Snowflake DDL 在 policy mutation 事务内：** Phase 4 D-04 + Pitfall #6 既定原则——外部 IO 永远 River 异步化。
- **❌ Quorum=All 在审批中途允许 reviewer pool 重新计算：** D-09 明确快照——这是审计可追溯性的前提。
- **❌ Webhook 派送在 governance handler hot path：** D-21 明确 River 异步——同步 HTTP 派送会让审批 API 延迟增加 100-1000ms 不可控。

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| RBAC enforcement + role hierarchy | 自建 RBAC + 角色图 | `casbin/casbin/v2` v2.135.0 | 角色继承传递闭包、cycle detection、`g(r.sub, p.sub)` 匹配——边界容易写错 |
| Casbin Postgres adapter | 自建 thin adapter | `pckhoi/casbin-pgx-adapter/v3` v3.2.0 | FilteredAdapter + Watcher + 增量 update/remove 协议——battle-tested |
| Async job queue + retries | 自建队列 | `riverqueue/river` v0.35.1 | 事务性 enqueue、指数退避、唯一任务、周期任务、Web UI——已在 CLAUDE.md 锁定 |
| Canonical JSON for hashing | 简单 sorted-keys + json.Marshal | `gowebpki/jcs`（RFC 8785 JCS）| 浮点、Unicode 转义、空对象等边界 |
| JWT signing/verification | 自建 | `golang-jwt/jwt/v5`（已在 go.mod）| 算法兼容性 + 失效 + claims 验证 |
| HMAC webhook 签名 | 自建 + 比较 | stdlib `crypto/hmac` + `crypto/subtle.ConstantTimeCompare` | 时序攻击防御不能用 `==` 比较 |
| Cron 表达式解析 | 自建 | `robfig/cron/v3`（已在 go.mod，Phase 3）| 标准 cron + extended syntax 已 parser |
| SMTP with STARTTLS + SASL | 自建 net/smtp | `wneessen/go-mail` | gomail 已停滞维护；net/smtp 已被 Go 团队 frozen |
| BigQuery Data Catalog API | 自建 REST 客户端 | `cloud.google.com/go/datacatalog` apiv1 | 官方 Google Go SDK，含 PolicyTagManagerClient + IAMPolicyClient + automatic retry/backoff |
| Snowflake DDL execution | 自建 HTTP+OAuth REST | `snowflakedb/gosnowflake` v1.19.1（已在 go.mod）| 已在 Phase 2 用于 Snowflake connector；db.ExecContext 即可执行 DDL |
| 状态机转换 CHECK | 自建 trigger | Postgres CHECK 约束 + ent enum | 既存 Phase 2 D-17 模式 |
| 哈希链 verify 顺序扫描 | 自建 cursor + recompute | sqlc `SELECT ... ORDER BY seq` + Go 循环 | 简单且正确，不需要"图书馆" |

**Key insight:** Phase 5 是"组装阶段"——几乎每个子系统都有一个 battle-tested 库。**唯一**值得自建的是 `internal/audit/writer.go` 的 sentinel-row 哈希链——因为它的协议（`SELECT FOR UPDATE` + canonical + SHA-256 + 双重 INSERT/UPDATE）是 Postgres-specific 业务逻辑，不存在通用库。

---

## Runtime State Inventory

> Phase 5 是 **新功能引入** 而非 rename/refactor，无需运行时状态盘点。本节明确为空。

**所有类别均为空：** 本阶段创建新表、新进程、新 CLI 子命令；不修改任何现有数据/配置/服务名称。

---

## Common Pitfalls

### Pitfall 1: 哈希链 canonical JSON 不稳定 → 历史 hash 失效

**What goes wrong:** 一年后审计员要求验证 5,000 条历史治理记录的哈希链。某条记录的 payload 含浮点（如 `quality_threshold: 0.05`）、Unicode 字符串、嵌套对象。新发布的 Go 标准库 `json.Marshal` 调整了浮点序列化精度——所有历史 self_hash 重新计算结果与存储不匹配。审计判定 "tamper" 而事实是序列化漂移。
**Why it happens:** `json.Marshal` 不是确定性的；map 顺序可变；浮点用 ECMAScript 模式 vs IEEE 754 不同；Unicode 高位字符 escape 策略不同。
**How to avoid:** 用 `gowebpki/jcs.Transform` (RFC 8785 JCS)。在 phase5 第一行 audit_log 写入之前必须正确——后期改造意味着重写所有历史 hash。
**Warning signs:** 相同 payload 不同时间 hash 不一致。CI 增加 "canonical-stability" 测试：固定 5 个 payload，hash 必须稳定 100 次循环 + 不同 Go 版本。

### Pitfall 2: Snowflake masking policy 是 schema-scoped，工程师误以为 account-scoped

**What goes wrong:** 创建 `email_mask` 在 `db1.schema_a`。绑定到 `db2.schema_b.users.email`。运行时报错 "masking policy email_mask does not exist or not authorized"——同名 policy 不存在于 db2.schema_b。
**Why it happens:** [VERIFIED: docs.snowflake.com] Masking policies 是 **schema-scoped identifier**——不同 schema 同名 policy 是不同对象。
**How to avoid:** Phase 5 平台必须在每个 (database, schema) 组合下管理 policy 副本，或选择**单一 governance schema**（如 `governance.policies`）然后跨 schema/db 应用。**推荐**：`MaskingProvisioner.ApplyMaskingPolicy` 实现选用 fully-qualified `<asset_db>.<asset_schema>.<policy_name>`，每次 ALTER 时会自动管 schema 上下文。
**Warning signs:** policy 同步成功但运行时 ALTER COLUMN SET MASKING POLICY 失败 "does not exist"。
**Source:** https://docs.snowflake.com/en/sql-reference/sql/create-masking-policy "schema-scoped identifier"

### Pitfall 3: Snowflake ALTER MASKING POLICY 不影响在飞查询

**What goes wrong:** 紧急合规事件——撤销 ssn 列的 ANALYST 可见性。`ALTER MASKING POLICY ssn_mask SET BODY -> '***'` 立即返回成功。但分析师长时间 SELECT * 仍在跑，看到 SSN 直到查询完成。审计认为 "policy applied immediately"，事实并非如此。
**Why it happens:** [VERIFIED: docs.snowflake.com/en/sql-reference/sql/alter-masking-policy] "Any changes made to the policy rules go into effect when the **next** SQL query that uses the masking policy runs."
**How to avoid:** 文档化此事实给治理团队。紧急情况下，Snowflake 提供 `ABORT_QUERY` SQL 强制停止 in-flight 查询；需要在文档中加入 "紧急吊销" runbook。
**Warning signs:** 合规事件后 SQL 审计日志仍显示已脱敏列被读取。

### Pitfall 4: BigQuery PolicyTag IAM 传播窗口

**What goes wrong:** 平台 push policy → BigQuery API 返回成功。但 IAM 策略实际生效需要 ~30 秒到 5 分钟。这段时间内分析师查询仍按旧权限返回 SSN。
**Why it happens:** [VERIFIED: BigQuery 文档未明确说明窗口；社区报告 30s-5min] BigQuery PolicyTag 写入立即返回，但 IAM 评估有缓存。
**How to avoid:** 不要假设 push 后立即生效。文档化 "新 policy 完全生效需 5 分钟"。Reconcile loop（D-04）在 push 后 1 分钟轮询 ListMaskingPolicies 验证生效。
**Warning signs:** policy push API 200 后，立即查询仍未脱敏。
**Source:** https://docs.cloud.google.com/bigquery/docs/column-level-security-intro

### Pitfall 5: BigQuery 列只能附加 1 个 PolicyTag，policy_tag tree 最深 5 层，每表 1000 个 tags

**What goes wrong:** 设计阶段假设可以"为每列附加多个 tag（pii + financial + region_eu）"。BigQuery 拒绝。
**Why it happens:** [VERIFIED: docs.cloud.google.com] "A column can have only one policy tag" + "A table can have at most 1,000 unique policy tags" + "A policy tag hierarchy can be no more than five levels deep"。
**How to avoid:** Taxonomy 设计：每个 v1 mask type 一个根 tag（hash、redact、partial、unmasked）；具体 (mask_type, allow_roles) 组合作为子 tag。最坏情况下 1 列 = 1 子 tag。每表 < 1000 列 → 限制内。
**Warning signs:** ApplyMaskingPolicy 返回 "tag hierarchy too deep" 或 "duplicate tag on column"。

### Pitfall 6: `casbin/casbin-pg-adapter` v1.5.0 用 go-pg 不是 pgx → 双 Postgres driver

**What goes wrong:** 引入 `casbin/casbin-pg-adapter` 拉入 `go-pg/v9`（项目原本用 pgx/v5）。两个 connection pool、两个监控指标、两套 prepared statement 缓存。
**Why it happens:** 官方 casbin/casbin-pg-adapter 仍依赖 go-pg（go-pg 项目已停止维护，最后版本 2024）。
**How to avoid:** 用 `pckhoi/casbin-pgx-adapter/v3 v3.2.0`——pgx/v5 原生，与项目共用 connection pool。
**Source:** https://github.com/casbin/casbin-pg-adapter/blob/master/adapter.go imports `github.com/go-pg/pg/v9`

### Pitfall 7: webhook 派送时序攻击 (constant-time compare)

**What goes wrong:** 用 `==` 比较 HMAC 签名 → 攻击者通过响应时间 side-channel 反推签名前缀。
**How to avoid:** 必须用 `crypto/subtle.ConstantTimeCompare`。
**Source:** webhooks.fyi best practices 2025

### Pitfall 8: webhook 时间戳重放保护

**What goes wrong:** 攻击者捕获一次合法 webhook，1 小时后重放 → 平台再次执行（如重新 approve 一次审批通知，或重新触发 quality alert）。
**How to avoid:** 签名内容包含 `timestamp.body`；接收方拒绝 timestamp 早于 5 分钟的请求；记录 webhook ID（uuid）+ 7-30 天 TTL 缓存以幂等。**注意：** 本阶段平台是**发送方**而不是接收方——所以这是给接收方的契约文档；平台需要：(1) 签名生成正确；(2) 包含 `X-Platform-Timestamp`、`X-Platform-Signature`、`X-Platform-Webhook-ID` 头；(3) River 重试不重新生成 ID（幂等性键复用）。

### Pitfall 9: River 重试与 webhook 幂等性

**What goes wrong:** River 派送任务超时 → 重试。下游 webhook 接收方收到两次（第一次实际成功但响应丢失；第二次又成功）。
**How to avoid:** webhook 头里固定 `X-Platform-Webhook-ID = <river_job_uuid>`——重试用同一 ID。接收方按此 ID 幂等。

### Pitfall 10: Quality 评估在 same-tx 但连接器 SQL 是新连接

**What goes wrong:** D-19 要求 quality 评估在 executor 元数据 tx 内。但 `connector.QueryAggregate(ctx, sql)` 通常打开一个独立连接 to 仓库（Snowflake/BigQuery）。如果该外部连接超时（30 秒），整个 executor tx 持有锁 30 秒，阻塞其他 step 的 commit。
**How to avoid:** 设置严格 `connector.QueryAggregate` 超时（默认 30 秒，可配）；超时时 → status='error' + alert，不重试。Phase 5 必须为 `connector.QueryAggregate` 添加 ctx 超时强制。

### Pitfall 11: governance.gating_enabled 默认 false 但生产忘记开启

**What goes wrong:** D-08 默认关闭以保留 dev 流程。生产部署忘记开启——治理状态从未真正阻塞物化。SOC2 审计发现 "governance was decorative"。
**How to avoid:** Phase 5 启动时如 `governance.gating_enabled=false`，发出 WARN log + Prometheus gauge `governance_gating_enabled = 0`。生产部署 runbook 强制 step "verify governance.gating_enabled=true"。

### Pitfall 12: 审批 reviewer pool 雪崩——某 reviewer 离职

**What goes wrong:** D-09 reviewer pool 在提交时快照。半年内 in-flight 审批的某 reviewer 离职。审批永久 pending → SLA breach 通知发到已离职邮箱 → 静默 bounce。
**How to avoid:** Phase 5 SLA breach 通知**额外**送给 owner（D-11 已确认） + escalation_roles（D-11 opt-in）。Reviewer 离职时，治理团队可手动 `./platform governance reassign <review-id> <new-reviewer>`——这个 CLI 是 v1 必需。 **建议**：在 D-22 plan 05-04 中加上 reassign 命令。

---

## Code Examples

### 1. Casbin RBAC Model (固定文件 `internal/auth/rbac_model.conf`)

```ini
# Source: https://casbin.apache.org/docs/rbac (canonical)
# Phase 5 D-01: 角色 → API 权限映射
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub) && keyMatch(r.obj, p.obj) && r.act == p.act
```

**示例策略数据（在 `casbin_rule` 表）：**
```
p, role:data-engineer, /assets/*/manage,         write
p, role:data-engineer, /governance/submit,       write
p, role:governance,    /governance/reviews/*,    write
p, role:governance,    /policies/*,              write
p, role:admin,         /users/*,                 manage
p, role:admin,         /audit/export,            read
g, alice@example.com,  role:data-engineer
g, bob@example.com,    role:governance
g, bob@example.com,    role:data-engineer  # role union for users
```

### 2. Snowflake Masking Provisioner（D-05 实现）

```go
// internal/connector/firstparty/snowflake/masking.go
// Source: https://docs.snowflake.com/en/sql-reference/sql/create-masking-policy
//          + https://docs.snowflake.com/en/sql-reference/sql/alter-masking-policy
package snowflake

import (
    "context"
    "fmt"
    "github.com/kanpon/data-governance/internal/connector"
)

// 模板：以 connector.ColumnPolicy.MaskType 切换 body。
const (
    bodyHash    = `CASE WHEN ARRAY_CONTAINS(CURRENT_ROLE()::variant, %s) THEN val ELSE SHA2_HEX(val, 256) END`
    bodyRedact  = `CASE WHEN ARRAY_CONTAINS(CURRENT_ROLE()::variant, %s) THEN val ELSE '***' END`
    bodyPartial = `CASE WHEN ARRAY_CONTAINS(CURRENT_ROLE()::variant, %s) THEN val ELSE LEFT(val,2) || REPEAT('*', LENGTH(val)-4) || RIGHT(val,2) END`
)

func (c *Connector) ApplyMaskingPolicy(ctx context.Context, ref connector.AssetRef, policy connector.ColumnPolicy) error {
    qualified := fmt.Sprintf(`"%s"."%s"."dgp_mask_%s_%s"`, ref.Database, ref.Schema, ref.Table, policy.Column)

    var body string
    switch policy.MaskType {
    case connector.MaskHash:
        body = fmt.Sprintf(bodyHash, allowRolesArray(policy.AllowRoles))
    case connector.MaskRedact:
        body = fmt.Sprintf(bodyRedact, allowRolesArray(policy.AllowRoles))
    case connector.MaskPartial:
        body = fmt.Sprintf(bodyPartial, allowRolesArray(policy.AllowRoles))
    default:
        return fmt.Errorf("snowflake.MaskingProvisioner: unsupported mask type %v", policy.MaskType)
    }

    // CREATE OR REPLACE — idempotent。policy.MaskType 一旦命名稳定，可重复 push 无副作用。
    ddl := fmt.Sprintf(
        `CREATE OR REPLACE MASKING POLICY %s AS (val VARIANT) RETURNS VARIANT -> %s`,
        qualified, body)

    if _, err := c.db.ExecContext(ctx, ddl); err != nil {
        return fmt.Errorf("snowflake: create masking policy %s: %w", qualified, err)
    }

    // ALTER COLUMN SET MASKING POLICY — 同样 idempotent。Snowflake 不允许同列两个 policy；
    // Phase 5 命名策略是 1 列 1 policy（dgp_mask_<table>_<col>）。
    alter := fmt.Sprintf(
        `ALTER TABLE "%s"."%s"."%s" ALTER COLUMN "%s" SET MASKING POLICY %s`,
        ref.Database, ref.Schema, ref.Table, policy.Column, qualified)
    if _, err := c.db.ExecContext(ctx, alter); err != nil {
        return fmt.Errorf("snowflake: alter table set masking: %w", err)
    }
    return nil
}
```

**关键设计：**
- 命名约定 `dgp_mask_<table>_<col>` 一对一映射 → push idempotent → reconcile 简单
- 用 VARIANT 类型避免 STRING/NUMBER 类型签名匹配问题（Snowflake 要求 input/output type 完全一致）
- `CREATE OR REPLACE` 而非 `CREATE IF NOT EXISTS`——前者更新 body；后者保留旧 body
- 连接器 service account 必须有 `APPLY MASKING POLICY ON ACCOUNT TO ROLE <connector_role>` 权限——必须文档化

### 3. BigQuery Masking Provisioner（D-05 实现）

```go
// internal/connector/firstparty/bigquery/masking.go
// Source: cloud.google.com/go/datacatalog/apiv1 PolicyTagManagerClient
//          + cloud.google.com/go/bigquery (Tables.Update)
package bigquery

import (
    "context"
    "fmt"

    datacatalog "cloud.google.com/go/datacatalog/apiv1"
    "cloud.google.com/go/datacatalog/apiv1/datacatalogpb"
    "cloud.google.com/go/bigquery"
    "github.com/kanpon/data-governance/internal/connector"
)

// 设计：
//   1 个 Taxonomy per project, named "dgp-platform"
//   Policy Tag 树：根 -> mask_type 子节点（hash/redact/partial/unmasked）
//   表列 → 单一 policy tag（BigQuery 限制：每列 1 个 tag）
//   IAM binding 在 tag 上：role/datacatalog.fineGrainedReader 给 allow_roles

func (c *Connector) ApplyMaskingPolicy(ctx context.Context, ref connector.AssetRef, policy connector.ColumnPolicy) error {
    // 1. 确保 Taxonomy 存在（idempotent）
    taxonomyName := fmt.Sprintf("projects/%s/locations/%s/taxonomies/%s",
        ref.Project, c.location, "dgp-platform")
    if _, err := c.ptm.GetTaxonomy(ctx, &datacatalogpb.GetTaxonomyRequest{Name: taxonomyName}); err != nil {
        // not exist → create
        _, err := c.ptm.CreateTaxonomy(ctx, &datacatalogpb.CreateTaxonomyRequest{
            Parent: fmt.Sprintf("projects/%s/locations/%s", ref.Project, c.location),
            Taxonomy: &datacatalogpb.Taxonomy{
                DisplayName:           "dgp-platform",
                Description:           "Data Governance Platform managed taxonomy",
                ActivatedPolicyTypes:  []datacatalogpb.Taxonomy_PolicyType{
                    datacatalogpb.Taxonomy_FINE_GRAINED_ACCESS_CONTROL,
                },
            },
        })
        if err != nil {
            return fmt.Errorf("bigquery: create taxonomy: %w", err)
        }
    }

    // 2. 确保 PolicyTag 存在（按 mask_type）
    tagName, err := c.ensurePolicyTag(ctx, taxonomyName, string(policy.MaskType))
    if err != nil {
        return fmt.Errorf("bigquery: ensure policy tag: %w", err)
    }

    // 3. 在 tag 上绑定 IAM (role/datacatalog.fineGrainedReader → allow_roles principals)
    if err := c.bindIAM(ctx, tagName, policy.AllowRoles); err != nil {
        return fmt.Errorf("bigquery: bind iam: %w", err)
    }

    // 4. 更新表列：在 columns[].policyTags 中加入 tagName
    if err := c.attachToColumn(ctx, ref, policy.Column, tagName); err != nil {
        return fmt.Errorf("bigquery: attach to column: %w", err)
    }

    return nil
}
```

**关键约束（D-05 验证项）：**
- Taxonomy `ActivatedPolicyTypes` 必须含 `FINE_GRAINED_ACCESS_CONTROL`，否则 BigQuery 不识别
- 每列只能 1 个 policy tag（[VERIFIED: docs.cloud.google.com]）→ 命名策略：每个 `(asset, column)` 一个 mask_type tag → tag 上 IAM 控制 allow_roles
- Service Account 需要：`bigquery.tables.update` + `datacatalog.taxonomies.create/get` + `datacatalog.policyTags.create/get/setIamPolicy`
- 策略生效有最终一致性窗口（30s-5min；Pitfall #4）→ Reconcile loop 必须验证

### 4. PII Propagator（D-06，同事务调用）

```go
// internal/governance/pii_propagator.go
// Source: D-06 同步 + union 规则；调用自 internal/lineage/capture.go
package governance

import (
    "context"
    "database/sql"
)

// PropagatePII 在 lineage_writer 写入 column_edges 后、tx 内调用。
// 算法：BFS 一跳上游；任一上游列 pii=true 则下游 pii=true，
//       除非显式 override 存在（tag_overridden 在 audit_log 中已检查）。
func PropagatePII(ctx context.Context, tx *sql.Tx, runID string, outputColumns []ColumnRef) error {
    for _, c := range outputColumns {
        // 1. 检查是否有显式 override
        var overrideExists bool
        if err := tx.QueryRowContext(ctx,
            `SELECT EXISTS(SELECT 1 FROM asset_metadata
                           WHERE asset=$1 AND column_name=$2 AND tags ? 'pii_override')`,
            c.Asset, c.Column,
        ).Scan(&overrideExists); err != nil {
            return err
        }
        if overrideExists {
            continue
        }

        // 2. BFS 上游一跳；任一上游有 pii=true 则下游有 pii=true。
        var anyUpstreamPII bool
        if err := tx.QueryRowContext(ctx, `
            SELECT EXISTS (
              SELECT 1 FROM column_edges ce
              JOIN asset_metadata am
                ON am.asset = ce.from_asset AND am.column_name = ce.from_column
              WHERE ce.to_asset = $1 AND ce.to_column = $2
                AND ce.superseded_at IS NULL
                AND am.tags ? 'pii'
            )`,
            c.Asset, c.Column,
        ).Scan(&anyUpstreamPII); err != nil {
            return err
        }

        if anyUpstreamPII {
            // 写 pii=true 到 asset_metadata（如已存在则 merge）
            if _, err := tx.ExecContext(ctx, `
                INSERT INTO asset_metadata (asset, column_name, tags, set_by, set_at)
                VALUES ($1, $2, '{"pii":true}'::jsonb, '00000000-0000-0000-0000-000000000000', NOW())
                ON CONFLICT (asset, column_name)
                DO UPDATE SET tags = asset_metadata.tags || '{"pii":true}'::jsonb`,
                c.Asset, c.Column,
            ); err != nil {
                return err
            }
        }
    }
    return nil
}
```

### 5. Quality Evaluator hook（在 executor.commitSuccess 中）

```go
// internal/runtime/executor.go (扩展 commitSuccess at line ~380)
// 在 SchemaWriter.Capture 后立即调用：
if e.deps.QualityEvaluator != nil {
    conn, ref, _ := e.Resolve(a.Name())
    qstatus, err := e.deps.QualityEvaluator.Evaluate(ctx, tx, runID, a, conn, ref)
    if err != nil {
        // D-19: error → 记录 quality_results 状态 'error'，不 rollback tx
        slog.Error("quality.evaluate", "run_id", runID, "asset", a.Name(), "err", err)
    }
    if _, err := tx.ExecContext(ctx,
        `UPDATE runs SET run_quality_status=$1 WHERE id=$2`, qstatus, runID); err != nil {
        return fmt.Errorf("update run_quality_status: %w", err)
    }
}
```

### 6. Audit Log RLS schema（迁移文件片段）

```sql
-- migrations/20260510000000_phase5_governance.sql
-- 扩展 Phase 1 D-09 RLS 模式到 audit schema。Source: Pitfall #5 + initial migration pattern.

CREATE SCHEMA IF NOT EXISTS audit AUTHORIZATION audit_migrator;

-- audit_migrator 角色（仅迁移用户）
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'audit_migrator') THEN
        CREATE ROLE audit_migrator NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'audit_purge') THEN
        CREATE ROLE audit_purge NOLOGIN;  -- v1.x 准备的清除用户
    END IF;
END $$;

CREATE TABLE audit.audit_log (
    seq           BIGSERIAL PRIMARY KEY,
    prev_hash     BYTEA NOT NULL,
    self_hash     BYTEA NOT NULL,
    occurred_at   TIMESTAMPTZ NOT NULL,
    event_type    TEXT NOT NULL,
    actor_id      UUID NULL,
    resource_type TEXT NOT NULL,
    resource_id   TEXT NOT NULL,
    payload       JSONB NOT NULL,
    expires_at    TIMESTAMPTZ NULL,
    CHECK (event_type IN (
      'policy.changed','policy.removed','masking.sync_failed','masking.sync_drift_detected',
      'role.created','role.deleted','role.assigned','role.revoked',
      'governance.submitted','governance.approved','governance.rejected',
      'governance.auto_approved','governance.review_sla_breached','governance.materialization_blocked',
      'audit.exported','audit.verify_failed','metadata.tag_overridden'
    ))
);

CREATE INDEX audit_log_event_type_occurred_at ON audit.audit_log(event_type, occurred_at);
CREATE INDEX audit_log_resource ON audit.audit_log(resource_type, resource_id);
CREATE INDEX audit_log_expires_at ON audit.audit_log(expires_at) WHERE expires_at IS NOT NULL;

-- Sentinel row：单行串行化哈希链写入
CREATE TABLE audit.audit_sentinel (
    id        SMALLINT PRIMARY KEY DEFAULT 1,
    seq       BIGINT NOT NULL DEFAULT 0,
    self_hash BYTEA NOT NULL DEFAULT decode('0000000000000000000000000000000000000000000000000000000000000000','hex'),
    CHECK (id = 1)  -- 永远只有一行
);
INSERT INTO audit.audit_sentinel (id, seq, self_hash)
VALUES (1, 0, decode('0000000000000000000000000000000000000000000000000000000000000000','hex'))
ON CONFLICT DO NOTHING;

-- 所有权与权限
ALTER SCHEMA audit                  OWNER TO audit_migrator;
ALTER TABLE  audit.audit_log        OWNER TO audit_migrator;
ALTER TABLE  audit.audit_sentinel   OWNER TO audit_migrator;

-- platform_app: INSERT-only on audit_log，UPDATE on sentinel（用于 hash chain 更新）
GRANT USAGE  ON SCHEMA audit                                  TO platform_app;
GRANT SELECT, INSERT ON audit.audit_log                       TO platform_app;
GRANT USAGE  ON SEQUENCE audit.audit_log_seq_seq              TO platform_app;
GRANT SELECT, UPDATE ON audit.audit_sentinel                  TO platform_app;
REVOKE UPDATE, DELETE, TRUNCATE ON audit.audit_log            FROM platform_app;

-- audit_purge: v1.x 才使用，仅 DELETE。v1 部署中不分配该角色给任何用户。
GRANT USAGE   ON SCHEMA audit                                 TO audit_purge;
GRANT DELETE  ON audit.audit_log                              TO audit_purge;
REVOKE INSERT, UPDATE, TRUNCATE ON audit.audit_log            FROM audit_purge;

-- RLS：双重保险
ALTER TABLE audit.audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.audit_log FORCE  ROW LEVEL SECURITY;

CREATE POLICY audit_log_select ON audit.audit_log
  FOR SELECT TO platform_app USING (true);
CREATE POLICY audit_log_insert ON audit.audit_log
  FOR INSERT TO platform_app WITH CHECK (true);
-- 不创建 UPDATE/DELETE policy → 即便意外 GRANT 也无效

CREATE POLICY audit_log_purge_delete ON audit.audit_log
  FOR DELETE TO audit_purge USING (expires_at IS NOT NULL AND expires_at < NOW());
```

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| 自建 RBAC | Casbin | ~2018 onward | 角色继承的边界 case 是雷区——Casbin 已 battle-tested |
| 查询时代理列掩码 | 仓库原生 DDM/CLS（Snowflake/BigQuery）| 2020 onward | Snowflake DDM GA 2020；BigQuery CLS GA 2020；查询时代理被规避（Pitfall #8）|
| `gopkg.in/mail.v2` | `wneessen/go-mail` | 2023 onward | gomail 维护停滞；go-mail 现代 Go 模式 |
| `casbin/casbin-pg-adapter`（go-pg）| `pckhoi/casbin-pgx-adapter/v3`（pgx/v5）| 2024 onward | go-pg 项目无新版本；社区迁移至 pgx/v5 |
| `jwt-go` | `golang-jwt/jwt/v5` | 2022 fork | jwt-go 长期 unmaintained；golang-jwt v5 主动维护 |
| 异步 audit 写入 | sentinel-row 同事务 | 2024 onward (Pitfall #5 共识)| 异步写入会留 commit 空窗，攻击者可在 hash 计算前篡改 |
| 双 ORM (ent + sqlc)| 仍是当前选择 | Phase 1+ | ent 处理图实体；sqlc 处理热读 CTE（Phase 4 D-16 验证）|

**Deprecated/outdated:**
- **`go-pg`**：v9 是最后稳定版（~2024）。新项目应转 pgx/v5。
- **`gomail` (gopkg.in/mail.v2)**：维护停滞。
- **Snowflake password auth**：仍可用但不推荐生产；Snowflake 推动 key-pair / OAuth。Phase 5 v1 startup config 应支持 key-pair 路径。
- **BigQuery legacy SQL**：Phase 5 不使用；所有 SQL 走 standard SQL（默认）。

---

## Assumptions Log

> 本研究中标记 [ASSUMED] 的假设——planner / discuss-phase 应在执行前确认。

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | `IguteChung/casbin-psql-watcher` 是 v1 单二进制部署不必要的（仅多进程才需要）。建议接口预留但不集成。 | Standard Stack | 多进程部署下 RBAC 策略可能 stale 数秒；但 v1 单二进制部署无影响。 |
| A2 | Snowflake `CREATE OR REPLACE MASKING POLICY` body 用 `VARIANT` 输入/输出可处理所有列类型。 | Code Examples §2 | 如果列含 GEOGRAPHY/ARRAY 复杂类型，可能需要类型分派；导致 ApplyMaskingPolicy 报错"input/output types must match"。 |
| A3 | BigQuery 1 列 1 PolicyTag 限制下，Phase 5 用"每个 mask_type 一个 root tag"分类法可工作。 | Code Examples §3 | 若需要"hash + region_eu"组合掩码，需要展开到组合 tag（限于 1000/表）。v1 设计不考虑组合，足够。 |
| A4 | 治理事件速率 ≪ 1/sec，sentinel-row 单锁不会成为瓶颈。 | Code Examples §6 | 若大型组织有数百同时审批 ⇒ 锁竞争；但治理本质是人工节奏，估算正确。 |
| A5 | `connector.QueryAggregate` 默认 30s 超时足够覆盖 Snowflake/BigQuery 简单聚合查询。 | Common Pitfall #10 | 大表 NULL rate 查询可能超 30s；建议改为可配（NullCheck 默认 30s，SQLAssertion 默认 60s）。 |
| A6 | RFC 8785 JCS 的 Go 实现 `gowebpki/jcs` 与 ECMAScript 实现哈希一致。 | Standard Stack | 若不一致，跨语言验证（v2 Python SDK）会失败。建议在 plan 05-01 加入 cross-impl 一致性测试。 |
| A7 | River v0.35.1 的 SQLite driver 仍为预览版——Phase 5 仅用 Postgres driver 即可（CLAUDE.md 确认 SQLite 仅 dev/CI）。 | Standard Stack | 开发模式（SQLite）下 River notification 可能不可用。但 Phase 2 已确立"开发模式不强求"模式。 |
| A8 | go-mail `wneessen/go-mail` SMTP STARTTLS 与 Postfix/Exchange 兼容。 | Standard Stack | 若客户用其他 MTA 与现代 SASL 不兼容，需 fallback 到 plain LOGIN——但 go-mail 已支持。 |
| A9 | Phase 5 不引入新进程（reconciler 可与 scheduler 同进程）——遵循"单二进制"约束。 | Architecture Patterns | 若 reconciler 需长 IO（Snowflake List）阻塞 scheduler tick，需要分离 goroutine（不是分离进程）。 |
| A10 | `snowflakedb/gosnowflake` v1.19.1 `ExecContext` 在 ctx.Cancel 时正确取消 DDL（含 CREATE MASKING POLICY）。 | Code Examples §2 | 历史 issue #767（v1.6.x）有 ctx 不被尊重的报告；v1.19.1 应已修复——但 plan 05-02 应在测试中验证。 |
| A11 | BigQuery PolicyTag IAM 传播窗口 ≤ 5 分钟，Reconcile loop 15 分钟可捕获。 | Common Pitfalls #4 | 若实际 > 5 分钟 → reconciler 报告 false drift。**建议**：reconciler 在 push 后增加"5 分钟宽限期"逻辑。 |

---

## Open Questions

1. **reviewer reassign CLI（Pitfall #12）必需性**
   - What we know: D-09 reviewer pool 在提交快照；reviewer 离职是真实风险。
   - What's unclear: 是否在 Phase 5 v1 必需？还是 v1.x 处理（v1 仅 owner 通知）？
   - Recommendation: Plan 05-04 加入 `./platform governance reassign <id> <new-reviewer>`——一行 SQL update + 审计记录，成本低，避免运维盲区。

2. **Snowflake / BigQuery 鉴权方式选择**
   - What we know: gosnowflake 支持 password / key-pair / OAuth；BigQuery 支持 ADC / SA JSON / Workload Identity。
   - What's unclear: Phase 5 v1 默认推荐哪种？
   - Recommendation: Snowflake **key-pair**（PAT 推动 + 容易轮换）；BigQuery **Workload Identity**（如部署在 GKE）或 **SA JSON**（其他）。文档化两种 + 启动 config 支持。

3. **`policies.yaml` 与 `notifications.yaml` 重新加载语义**
   - What we know: 两个 YAML 文件影响实时治理决策。
   - What's unclear: 文件改动后多久生效？SIGHUP / fsnotify / 仅 restart？
   - Recommendation: v1 用 SIGHUP 触发重载（简单 + 可监控）；fsnotify 推迟 v1.x。

4. **Quorum=All 在审批中途 reviewer 离职 / role 删除的处理**
   - What we know: D-09 快照已确定；reviewer 离职可能阻塞。
   - What's unclear: 是否应支持"快照内 reviewer 已离职"的自动 fallback？
   - Recommendation: v1 不自动 fallback——通过 reassign CLI（见 #1）人工处理。审计可追溯性优于自动化。

5. **Casbin Watcher 在哪些部署形态下必需？**
   - What we know: 单进程 v1 不需要；多进程（如分离 worker + scheduler）需要。
   - What's unclear: Phase 5 v1 部署形态——是 1 个 platform 进程 + N 个 scheduler/worker 进程？
   - Recommendation: 检查 Phase 2/3 worker 设计——如果 worker 进程也运行 chi REST，则需要 watcher 同步 RBAC。**建议** plan 05-01 增加任务 "评估 worker 是否需要 enforcer + watcher"。

---

## Environment Availability

> Phase 5 主要影响代码与数据库结构；外部依赖通过测试时 fixture 满足，生产时由部署者提供。

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| PostgreSQL 16+ | 元数据存储；audit schema；Casbin policy | ✓（既已用于 Phase 1+，testcontainers/postgres v0.42.0 已在 go.mod）| 16+ | — |
| Snowflake 帐号（测试）| 集成测试 Snowflake MaskingProvisioner | ✗（无生产 Snowflake；本地无 emulator）| — | **使用 mock** — gosnowflake 通过 sqlmock + DDL string assertion 测试 ApplyMaskingPolicy；E2E 真实测试推迟到部署后人工验证。 |
| BigQuery 帐号（测试）| 集成测试 BigQuery MaskingProvisioner | ✓（`goccy/bigquery-emulator` v0.6.6 已在 go.mod）| v0.6.6 | bigquery-emulator 不支持 PolicyTag 操作 → MaskingProvisioner 测试需要使用 Cloud Functions 或 mock；E2E 推迟人工验证 |
| SMTP 服务器（测试）| Notification SMTP 通道 | ✓（可起 testcontainers/MailHog）| — | go-mail 有 mock Sender 接口 |
| HTTP 服务器（测试 webhook）| Notification webhook | ✓（stdlib `httptest.Server`）| — | — |
| `riverqueue/river` 工具链 | Migration（River 自带 schema）| 通过 `go install github.com/riverqueue/river/cmd/river@latest` 获得 | v0.35.1 | River 自带 SQL migrations 也可手动嵌入 phase5 主迁移 |

**Missing dependencies with fallback:**
- Snowflake / BigQuery 真实帐号 → mock + emulator + 人工 UAT 阶段验证

**Missing dependencies with no fallback:**
- 无

---

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` + `stretchr/testify` v1.11.1 + `testcontainers-go/modules/postgres` v0.42.0（既有）|
| Config file | 无独立 test config；用 `internal/storage/store_test.go` 模式 |
| Quick run command | `go test -short ./internal/{audit,policy,governance,quality,notification}/... -count=1` |
| Full suite command | `go test ./... -count=1 -timeout=10m`（含 testcontainers postgres + bigquery-emulator）|
| Phase E2E command | `go test -tags=e2e ./internal/runtime/... -count=1`（既有 Phase 4 模式）|

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| RBAC-01 | 创建角色 emit `role.created` audit_log entry | unit + integration | `go test ./internal/auth/... -run TestCreateRole_AuditEntry -count=1` | ❌ Wave 0 |
| RBAC-02 | 用户 → 角色分配持久 + audit | integration | `go test ./internal/auth/... -run TestAssignRole_End2End` | ❌ Wave 0 |
| RBAC-03 | column_policy CRUD + 三层解析正确 | unit | `go test ./internal/policy/... -run TestColumnPolicy_Resolution` | ❌ Wave 0 |
| RBAC-04 (Snowflake) | ApplyMaskingPolicy 生成 DDL string | unit (sqlmock) | `go test ./internal/connector/firstparty/snowflake/... -run TestMasking_ApplyDDL` | ❌ Wave 0 |
| RBAC-04 (BigQuery) | ApplyMaskingPolicy 调用 PolicyTagManagerClient | unit (mock client) | `go test ./internal/connector/firstparty/bigquery/... -run TestMasking_PolicyTag` | ❌ Wave 0 |
| RBAC-04 (Reconcile) | drift 检测 → emit `masking.sync_drift_detected` | integration | `go test ./internal/policy/... -run TestReconcile_Drift` | ❌ Wave 0 |
| RBAC-05 (in-pipeline) | AssetIO.Write 应用 mask 函数 | unit | `go test ./internal/policy/... -run TestMaskApply_Hash_Redact_Partial` | ❌ Wave 0 |
| RBAC-06 (hash chain) | 写 → verify 通过 | unit + integration | `go test ./internal/audit/... -run TestWriteEntry_VerifyChain` | ❌ Wave 0 |
| RBAC-06 (tamper) | 模拟篡改 1 行 → verify 检出 mismatch seq | integration | `go test ./internal/audit/... -run TestVerify_DetectsTamper` | ❌ Wave 0 |
| RBAC-06 (RLS) | platform_app UPDATE/DELETE 被 RLS 拒绝 | integration | `go test ./internal/audit/... -run TestRLS_RejectsUpdate` | ❌ Wave 0 |
| GOV-01/02/03 | submit → review → state transition + comment 必填 reject | integration | `go test ./internal/governance/... -run TestWorkflow_HappyPath` | ❌ Wave 0 |
| GOV-01 (auto-approval) | 自动预审批通过条件 → state=active | unit | `go test ./internal/governance/... -run TestAutoApproval_AllPass` | ❌ Wave 0 |
| GOV-01 (auto-approval block) | PII 列存在 → 走人工 | unit | `go test ./internal/governance/... -run TestAutoApproval_PIIBlocks` | ❌ Wave 0 |
| GOV-02 (reviewer pool) | 三路解析正确 | unit | `go test ./internal/governance/... -run TestReviewerPool_ThreeSource` | ❌ Wave 0 |
| GOV-04 (notification) | 决策 → submitter SMTP 邮件 | integration (MailHog) | `go test ./internal/notification/... -run TestSMTP_DispatchOnDecision` | ❌ Wave 0 |
| GOV-05 (audit trail) | 决策写入 audit_log + hash chain | integration | `go test ./internal/governance/... -run TestDecision_AuditEntry` | ❌ Wave 0 |
| GOV-06 (export) | 流式 JSONL/CSV 导出 + 含 seq + self_hash | integration | `go test ./internal/audit/... -run TestExport_Streaming_VerifiableLines` | ❌ Wave 0 |
| GOV-07 (TTL schema) | expires_at 列写入 + index 存在 | integration | `go test ./internal/audit/... -run TestExpiresAt_SchemaPresence` | ❌ Wave 0 |
| QUAL-01 | NullCheck/RangeCheck/SQLAssertion 评估 | unit | `go test ./internal/quality/... -run TestRules_Evaluate` | ❌ Wave 0 |
| QUAL-02/03 | 物化后 run_quality_status 正确更新 | integration | `go test ./internal/runtime/... -run TestExecutor_QualityHook` | ❌ Wave 0 |
| QUAL-04 | scheduler tick 检出 SLA breach | integration | `go test ./internal/scheduler/... -run TestFreshnessSLA_Breach` | ❌ Wave 0 |
| QUAL-05 (webhook) | quality 失败 → River 派 webhook + HMAC 签名 | integration (httptest)| `go test ./internal/notification/... -run TestWebhook_HMACSigned` | ❌ Wave 0 |
| QUAL-05 (replay safe) | webhook 重发 ID 一致 | integration | `go test ./internal/notification/... -run TestWebhook_IdempotentID` | ❌ Wave 0 |

### Sampling Rate
- **Per task commit:** `go test ./internal/<changed-package>/... -count=1 -short -timeout=2m`
- **Per wave merge:** `go test ./... -count=1 -timeout=10m`（Phase 4 既定全套）
- **Phase gate:** Full suite green + 手动 UAT against 真实 Snowflake/BigQuery 帐号（人工，记录到 05-HUMAN-UAT.md，Phase 4 模式）

### Wave 0 Gaps（plan 05-01 必须先建立）
- [ ] `internal/audit/writertest/fixtures.go` — sentinel-row + canonical JSON 测试支撑
- [ ] `internal/policy/policytest/fixtures.go` — column_policy 三层解析测试支撑
- [ ] `internal/governance/governancetest/fixtures.go` — reviewer pool + auto-approval 测试支撑
- [ ] `internal/quality/qualitytest/fixtures.go` — 三种规则的 mock evaluator
- [ ] `internal/notification/notificationtest/{webhook_server,smtp_server}.go` — httptest + MailHog 接入
- [ ] `internal/connector/firstparty/snowflake/maskingtest/sqlmock_assertions.go` — DDL 字符串断言
- [ ] `internal/connector/firstparty/bigquery/maskingtest/mock_client.go` — PolicyTagManagerClient mock
- [ ] testcontainers helper 扩展：起 Postgres 时自动应用 phase5 迁移
- [ ] River 测试 helper：内存 driver / pgx + River 短路方便 unit test

---

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | yes | golang-jwt/v5 + Casbin（已在 Phase 1 + 本阶段）|
| V3 Session Management | yes | JWT TTL + token expired audit_log（Phase 1 D-09 + 本阶段）|
| V4 Access Control | **核心** | Casbin RBAC（D-01）+ 列级策略（D-02）+ 仓库原生掩码（D-04）+ Casbin policy table 由 audit_migrator 拥有 |
| V5 Input Validation | yes | chi handler 输入 + ent 强类型 + JSON Schema for column_policy PATCH body |
| V6 Cryptography | yes | SHA-256 哈希链（D-13）；HMAC-SHA256 webhook 签名；RFC 8785 JCS 序列化；TLS for SMTP STARTTLS |
| V7 Error Handling & Logging | yes | RLS-immutable audit_log + structured slog 不泄漏密钥 |
| V9 Data Protection | **核心** | 列级掩码（v1 Hash/Redact/Partial）+ 仓库原生 DDM/CLS（Pitfall #8）+ TLS for warehouse connections |
| V13 API Security | yes | chi middleware enforce Casbin + RFC 7807 problem+json 已有 |

### Known Threat Patterns for {Phase 5 stack}

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| audit_log 删除（DBA 入侵 / 配置错误）| Tampering | RLS + 独立 schema + audit_migrator 角色（Pitfall #5）|
| audit_log 修改（同上）| Tampering | 哈希链 + 顺序扫描 verify CLI（D-15）|
| 重放攻击 webhook | Tampering | HMAC + timestamp + Webhook-ID 头（Pitfall #8/9）|
| 时序攻击 HMAC compare | Tampering | crypto/subtle.ConstantTimeCompare（Pitfall #7）|
| Snowflake key-pair 泄漏 → DDM 任意修改 | Tampering | Connector service account 单独权限（仅 APPLY MASKING POLICY）+ 定期轮换 |
| BigQuery SA JSON 泄漏 → IAM 修改 | Tampering | Workload Identity（无 JSON）/ 限定 SA 仅 fineGrainedReader.setIamPolicy |
| 列策略绕过查询代理 | Information Disclosure | Pitfall #8: 仓库原生 DDM/CLS 而非代理（D-04 决策）|
| PII 传播 race | Information Disclosure | 同事务（D-06）— 不存在窗口 |
| Casbin policy 表被入侵 → 角色任意分配 | EoP | 表所有权 audit_migrator + 操作 emit `role.assigned` audit_log + 哈希链 |
| 审批 SLA 暴风（reviewer 离职） | DoS-on-governance | 通知 owner + reassign CLI（建议 #1）|
| 哈希链 sentinel-row lock 竞争 | DoS | 范围窄（D-14）— 治理事件低速 |
| 通知通道 spam（外部 webhook 配错）| DoS-amplification | River 退避 + dead-letter；notifications.yaml 路由严格匹配 |
| 物化绕过 governance gate | EoP | governance.gating_enabled=true（生产强制）+ executor 检查（D-08）|
| canonical JSON 漂移 → hash 失效 | Tampering（false-positive）| RFC 8785 JCS（gowebpki/jcs）+ cross-impl 一致性测试（A6）|

---

## Sources

### Primary (HIGH confidence)
- **Casbin v2 RBAC docs:** https://casbin.apache.org/docs/rbac — RBAC model.conf canonical structure（用于 D-01）
- **Casbin Postgres adapter (官方):** https://github.com/casbin/casbin-pg-adapter — 验证 v1.5.0 用 go-pg/v9
- **Casbin pgx adapter (推荐):** https://github.com/pckhoi/casbin-pgx-adapter — 验证 v3.x 用 pgx/v5
- **Casbin Postgres watcher:** https://github.com/IguteChung/casbin-psql-watcher — Postgres LISTEN/NOTIFY 跨进程同步
- **Snowflake Masking Policies (CREATE):** https://docs.snowflake.com/en/sql-reference/sql/create-masking-policy
- **Snowflake Masking Policies (ALTER):** https://docs.snowflake.com/en/sql-reference/sql/alter-masking-policy — 验证 "next query takes effect"
- **Snowflake Masking Policies (Privileges):** https://docs.snowflake.com/en/user-guide/security-column-ddm-intro — APPLY/OWNERSHIP 权限
- **BigQuery Column-Level Security:** https://docs.cloud.google.com/bigquery/docs/column-level-security-intro — Policy Tags + Data Catalog 关系 + 限制
- **BigQuery Data Catalog (Go):** https://docs.cloud.google.com/data-catalog/docs/samples/data-catalog-ptm-create-taxonomy — Go 创建 taxonomy 示例
- **River Queue:** https://riverqueue.com/docs/job-retries + https://pkg.go.dev/github.com/riverqueue/river — 验证 v0.35.1（2026-04-26）
- **PostgreSQL Audit Trigger Wiki:** https://wiki.postgresql.org/wiki/Audit_trigger — append-only audit 实现模式
- **Tamper-evident audit (AppMaster):** https://appmaster.io/blog/tamper-evident-audit-trails-postgresql — hash chaining 实现
- **RFC 8785 JCS:** https://www.rfc-editor.org/rfc/rfc8785 — JSON Canonicalization Scheme 规范
- **gowebpki/jcs Go 实现:** https://github.com/gowebpki/jcs
- **`internal/auth/middleware.go` 与 `internal/runtime/executor.go`** — 项目内既有代码（Phase 1/4）— 用于扩展点定位

### Secondary (MEDIUM confidence)
- **webhooks.fyi (security):** https://webhooks.fyi/security/replay-prevention + https://webhooks.fyi/security/hmac — 行业最佳实践
- **Hookdeck SHA256 webhook 验证:** https://hookdeck.com/webhooks/guides/how-to-implement-sha256-webhook-signature-verification
- **Mailtrap go-mail 教程（2026）:** https://mailtrap.io/blog/golang-send-email/ — go-mail 优于 gomail 的现代理由
- **wneessen/go-mail:** https://github.com/wneessen/go-mail
- **Bytebase Postgres Audit Logging Guide:** https://www.bytebase.com/blog/postgres-audit-logging/
- **Hrekov Casbin RBAC vs hierarchical:** https://hrekov.com/blog/casbin-rbac-vs-casbin-rbac-hierarchical — 角色继承边界 case
- **Snowflake gosnowflake driver:** https://github.com/snowflakedb/gosnowflake — v1.19.1 验证

### Tertiary (LOW confidence — 标记为待验证)
- BigQuery PolicyTag IAM 传播窗口具体值 30s-5min — 多个社区博客交叉提到，但官方文档未明确写出窗口（A11 假设）
- gosnowflake v1.19.1 ctx.Cancel 在 DDL 上正确取消 — 历史 issue 在 v1.6.x 报告，v1.19 应已修复但未在搜索中显式确认（A10 假设）

### Verified versions（2026-05-09 通过 `go list -m -versions`）
- `github.com/casbin/casbin/v2`：v2.135.0 是最新发布（2025-12 系列）
- `github.com/riverqueue/river`：v0.35.1（2026-04-26）
- `github.com/casbin/casbin-pg-adapter`：v1.5.0（最新）
- `github.com/pckhoi/casbin-pgx-adapter/v3`：v3.2.0
- `github.com/snowflakedb/gosnowflake`：v1.19.1（与项目 go.mod 一致）
- `cloud.google.com/go/datacatalog`：v1.31.0（最新）

---

## Metadata

**Confidence breakdown:**
- Standard Stack: **HIGH** — 库版本均通过 `go list -m -versions` 与 Go 模块代理验证；CLAUDE.md 已锁定大部分库
- Architecture: **HIGH** — 完全延续 Phase 1–4 既定模式（三表面、双 ORM、optional connector capability、temporal table、RLS-immutability、River async）
- Snowflake DDM: **HIGH** — 官方文档双重验证 DDL 形态；ALTER 时机；schema-scoped 限制；privilege 模型
- BigQuery CLS: **MEDIUM** — Policy Tag + Taxonomy 模型 HIGH，IAM 传播窗口具体值 LOW（社区数据）
- 哈希链 + canonical JSON: **HIGH** — Pitfall #5 + RFC 8785 + gowebpki/jcs 三方验证
- 治理工作流: **HIGH** — D-08..D-12 均与 Phase 4 D-03/D-17/D-19 模式直接对应
- 质量规则: **HIGH** — 同事务 D-19 与 Phase 4 D-04 哲学一致；连接器 capability 模式既定
- 通知系统: **MEDIUM** — webhook + SMTP 模式标准；template 语法 + 路由格式仍有 Claude's Discretion 空间
- Common Pitfalls: **HIGH** — 12 个 pitfall 均有官方文档或 PITFALLS.md 引证

**Research date:** 2026-05-09
**Valid until:** 2026-06-09（30 天，技术栈与 API 稳定）。Snowflake/BigQuery API 若有重大更新（罕见），需要重验证 §4.2/§4.3 代码示例。
