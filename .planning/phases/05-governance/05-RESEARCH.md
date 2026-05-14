# 阶段 5：治理引擎 - 研究

**研究日期：** 2026-05-09
**领域：** RBAC + 列级访问控制 + 仓库掩码同步 + 治理工作流 + 哈希链审计日志 + 质量规则 + 通知分发
**置信度：** 高（CONTEXT.md 锁定 23 项决策；现有代码库验证阶段 1-4 模式；外部 API 和库版本已通过官方文档和 Go 模块代理验证）

---

<user_constraints>
## 用户约束（来自 CONTEXT.md）

### 锁定决策（来自 CONTEXT.md `<decisions>`，逐字保留）

#### RBAC 模型和 Casbin 集成
- **D-01：** Casbin 仅负责 **角色 → API 权限** 映射；列级策略存储在独立的 `column_policies` 表中。Casbin 模型：标准 RBAC `p = (sub, obj, act)`，其中 `obj` 是资源路径（`/assets/<name>/manage`、`/audit/export`、`/governance/approve`、`/policies/edit`、`/users/admin`），`act ∈ {read, write, manage}`。Casbin Postgres 适配器由此研究阶段决定。阶段 5 引入 Casbin v2.135.x。
- **D-02：** 双向列策略声明，运行时优先（与阶段 4 D-17 相同）：
  - 构建器默认（代码声明）：`asset.New("orders").ColumnPolicy(asset.ColumnPolicy{Column:"ssn", Mask:asset.MaskHash, AllowRoles:[]string{"pii-analyst"}})`，存储在 `asset_versions` 行，参与 code_hash（D-03）。
  - 运行时覆盖（REST）：`PATCH /assets/:name/columns/:col/policy → {mask, allow_roles, reason}`，存储在 `column_policies` 表（唯一索引为 `(asset, column)`）。`effective = COALESCE(runtime_value, code_default)`。
  - 全局 YAML 默认（第三层）：`policies.yaml` 映射 `tag → mask_default`（例如 `pii: hash`）。最低优先级。
  - 读取顺序：运行时 > 构建器 > 标签默认 YAML > 未掩码。
  - 审计：每次 PATCH / YAML 重载都会向 **`audit_log`（不是 `event_log`）** 发送 `policy.changed`，包含 `{actor, before, after, reason}`。
- **D-03：** v1 掩码类型已枚举且有限：
  - **Hash**（SHA-256 + 平台盐）；**Redact**（常量 `"***"`）；**Partial**（首尾 N 个字符可见）。
  - Tokenization、FPE、bucket 推迟到 v1.x。
  - 每种掩码类型映射到：管道内（RBAC-05）Go 函数；Snowflake DDM 参数化 `CREATE MASKING POLICY` 模板；BigQuery CLS 数据目录策略标签 + IAM + 数据掩码模板。
- **D-04：** 仓库掩码同步（RBAC-04）= **变更时推送 + 协调循环**，均通过 River：
  - 推送：策略变更事务入队 `policy_sync` River 任务，调用连接器的 `MaskingProvisioner`（D-05）。失败重试通过 River 指数退避；永久失败发送 `masking.sync_failed` 到 audit_log + 告警。
  - 协调循环：新的 `./platform reconciler` 守护进程（或调度器子命令扩展）每 **15 分钟**（可配置）通过 `ListMaskingPolicies` 拉取仓库当前状态，并与 `column_policies` 比较。漂移发送 `masking.sync_drift_detected` 事件 + 自动重新推送。
  - 陷阱 #8：平台 **永不代理查询**—掩码是仓库原生责任，平台是策略的事实来源。
- **D-05：** 新连接器可选能力 `connector.MaskingProvisioner`（与阶段 4 D-06 SchemaDescriber 相同的模式）：
  ```go
  type MaskingProvisioner interface {
      ApplyMaskingPolicy(ctx context.Context, asset AssetRef, policy ColumnPolicy) error
      RemoveMaskingPolicy(ctx context.Context, asset AssetRef, column string) error
      ListMaskingPolicies(ctx context.Context, asset AssetRef) ([]ColumnPolicy, error)
  }
  ```
  - 阶段 5 仅在 **Snowflake** 和 **BigQuery** 上实现。其他连接器（PostgreSQL、MySQL、S3、GCS、HDFS）使用管道内掩码路径（D-03）。
  - 运行时类型断言：`if mp, ok := conn.(connector.MaskingProvisioner); ok { ... }`。
  - 此研究阶段验证的特定 Snowflake/BigQuery API 形状（STATE.md 标志）。
- **D-06：** **PII 标签传播** 在执行器元数据事务内同步运行（在 lineage_writer 写入 `column_edges` 之后）：
  - 触发器：每次成功的 materialize `lineage.captured`（阶段 4 D-01）。
  - 算法：对每个输出列，沿 `column_edges`（阶段 4 D-13）向上游 BFS 一跳；如果任何源列有 `pii=true`（在 `asset_metadata.tags` 中），下游列继承 `pii=true`，除非存在显式覆盖。
  - **冲突解决：并集**—任何上游 PII ⇒ 下游 PII。
  - **覆盖机制：** `asset.New("orders_anonymized").Column("hashed_ssn").TagOverride(asset.TagOverride{Remove:"pii", Reason:"hashed at source via D-03 MaskHash; not reversible"})`。需要非空 `Reason`。覆盖向 audit_log 发送 `metadata.tag_overridden`。
  - 相同事务保证：不存在下游 PII 列暂时未掩码的窗口。
- **D-07：** `column_policies` 表延续阶段 4 D-15 软退休/时间表模式：
  - 列：`(id, asset, column, mask_type, allow_roles, code_hash_first, code_hash_latest, first_seen_run_id NULLABLE, first_seen_at, last_seen_at, superseded_at NULLABLE, source ENUM(builder|runtime|yaml-default))`。
  - 活动策略：`WHERE superseded_at IS NULL`。
  - 时间点查询：`WHERE first_seen_at <= $T AND (superseded_at IS NULL OR superseded_at > $T)`。
  - 删除被 RLS 禁止；策略"移除" = 设置 `superseded_at = NOW()` + 发送 `policy.removed`。

#### 治理工作流
- **D-08：** 治理状态位于 **`asset_versions.governance_state`** 列：
  - 枚举：`draft | in_review | active | rejected`。CHECK 约束控制转换：`draft→in_review`（提交）；`in_review→active`（批准）；`in_review→rejected`（拒绝）；`rejected→in_review`（重新提交相同 code_hash）；`active→in_review`（管理员强制重新审查，罕见）。
  - 新 code_hash 默认为 `draft`—每次代码更改都会返回草稿—陷阱 #7"旧批准覆盖新代码"保护。
  - 物化门控：执行器检查 `governance_state = 'active'`，否则拒绝并发送 `governance.materialization_blocked`。配置标志 `governance.gating_enabled` 默认为 **false** 用于 v1（保留开发工作流，生产环境显式启用）。
- **D-09：** 审查者分配来自三个来源（并集）：
  1. 构建器：`asset.New("orders").Reviewers("team-data-gov", "privacy-team")`
  2. 标签规则 YAML：`policies.yaml` 映射 `tag → required_reviewer_roles`（例如 `pii: ["privacy-team"]`）
  3. 所有者回退：`asset_metadata.owner` → `team_owners` 配置表 → 审查者角色
  - 解析：`(1) ∪ (2) ∪ (3 如果 (1)∪(2) 为空)`。
  - **法定人数：** 默认 1。`asset.New("x").Quorum(asset.QuorumAll)` 需要全部；`Quorum(2)` 需要 N 中的 2。
  - 审查者池在提交时快照到 `governance_reviews` 表—随后添加/删除角色不影响进行中的审查。
- **D-10：** 自动批准检查管道（陷阱 #7："设计先于人工路径"）。在提交后按顺序运行：
  1. **架构破坏确认：** 任何未确认的破坏性 schema_change → 阻止
  2. **策略/PII 一致性：** 每个有 `pii` 标签的列必须有 column_policy → 缺失阻止
  3. **质量配置 sanity：** 资产 QualityRule 必须解析并引用现有列 → 损坏阻止
  4. **血缘漂移：** 阶段 4 D-04 `drift_status='pending'` → 阻止
  5. **PII 存在 + 审查者：** 任何有 `pii` 标签的列 → 禁用快速路径（需要人工 + privacy-team）
  - 全部通过 + 无 PII + 无破坏性 schema_change → 状态直接进入 `active`，发送 `governance.auto_approved` + 通知所有者。
  - 构建器选择退出：`asset.New("x").RequireHumanReview()`。
- **D-11：** 强制拒绝评论 + SLA 告警，无自动升级：
  - `POST /governance/reviews/:id/reject` 需要非空 `comment`；CLI 同步需要 `--comment` 标志。批准评论可选。
  - SLA 计时器：`governance.review_sla_hours`（默认 48h）。调度器 tick 扫描 `submitted_at + sla_hours < NOW() AND decided_at IS NULL` → 发送 `governance.review_sla_breached` + 通知所有审查者 + 所有者。**不自动升级**—SOC 2 需要人工认证。
  - 升级选择加入：`.EscalationRoles(...)` 或全局 YAML，仅在明确配置 + 审计时执行。
- **D-12：** 提交生命周期：
  - `POST /governance/submit {asset, code_hash, reviewers_extra?}` → 创建 `governance_reviews` 行（链接到 asset_version_id + submitter_id）+ 审查者池快照 + 发送 `governance.submitted`。
  - `POST /governance/reviews/:id/{approve|reject}` → 原子更新 + 状态转换 + 发送 `governance.{approved|rejected}` + 分发通知（D-21）。
  - REST + CLI：`./platform governance submit <asset>`、`./platform governance review <id> --approve|--reject [--comment=...]`、`./platform governance status [<asset>]`。

#### 审计哈希链和日志架构
- **D-13：** 审计日志位于 **专用 `audit_log` 表**，与 `event_log` 分开：
  - 模式：`(seq BIGSERIAL PRIMARY KEY, prev_hash BYTEA, self_hash BYTEA, occurred_at TIMESTAMPTZ, event_type TEXT, actor_id, resource_type, resource_id, payload JSONB)`。
  - 哈希构造：`self_hash = SHA-256(seq || prev_hash || occurred_at || event_type || actor_id || resource_type || resource_id || canonical_json(payload))`。创世：`prev_hash = bytea(32 zero bytes)`。
  - **独立 Postgres 模式** `audit`，独立迁移用户 `audit_migrator`（陷阱 #5）。应用用户 `platform_app` 只有 INSERT。RLS 禁止 UPDATE/DELETE。只有迁移用户可以 DDL。
  - **插入协议：** `audit.WriteEntry(ctx, tx, entry)` 原子辅助函数。在调用者事务内：`SELECT MAX(seq) FOR UPDATE` 哨兵行 → 计算 `self_hash` → 插入。通过哨兵行锁序列化并发写入。治理 + 访问控制事件低频，单锁可接受。
  - **为什么与 event_log 分开：** event_log 有高频 run.* 事件；添加到哈希链会序列化热路径。audit_log 小而低频、安全关键。
- **D-14：** 审计日志内容范围 **刻意缩小**：
  - **在范围内（写入 `audit_log`）：**
    - `policy.changed`、`policy.removed`、`masking.sync_failed`、`masking.sync_drift_detected`
    - `role.created`、`role.deleted`、`role.assigned`、`role.revoked`
    - `governance.submitted`、`governance.approved`、`governance.rejected`、`governance.auto_approved`、`governance.review_sla_breached`、`governance.materialization_blocked`
    - `audit.exported`、`audit.verify_failed`
    - `metadata.tag_overridden`
  - **不在范围内（留在 `event_log`）：** `run.*`、`schedule.*`、`sensor.*`、`lineage.captured`、`schema.*`、`metadata.updated`（非 PII 覆盖元数据编辑）。
- **D-15：** 防篡改验证 + 导出（与阶段 4 D-19 三层包装器相同）：
  - **CLI** `./platform audit verify [--from=<seq>] [--to=<seq>]`—顺序扫描，重新计算每个 `self_hash`，首次不匹配失败并打印不匹配 seq。退出代码 0 = 链完整，非零 = 检测到篡改。
  - **REST** `GET /audit/export?from=<ISO>&to=<ISO>&format=json|csv|jsonl`—流式响应（分块传输），每行包含 `seq` + `self_hash`。默认 `jsonl`。
  - **CLI** `./platform audit export --from=<ISO> --to=<ISO> --format=jsonl --out=<file>`—相同的库函数包装器。
  - 围绕单个 Go 库 `internal/audit/{Verify, Export}` 的三层包装器。
  - **v1 无后台协调器**—按需验证。后台协调器推迟到 v1.x。
  - 审计日志本身的导出发送 `audit.exported`（递归到同一链中）。
- **D-16：** GOV-07 保留 TTL v1 部分实现：
  - `audit_log.expires_at TIMESTAMPTZ NULL`。全局配置 `audit.retention_default_days`（默认 NULL = 无限；大多数合规场景 7-10 年）。允许每个 event_type 覆盖。
  - 实际清除机制（特权后台任务 DELETE 过期行）**推迟到 v1.x**—v1 仅有模式 + 文档化操作手册 + v1 迁移中保留的清除用户。
  - 资产数据 TTL（通过连接器删除物化数据）**完全推迟到 v1.x**。
- **D-17：** S3 Object Lock / WORM 哈希锚定 v1 接口仅有：`internal/audit/anchor.Anchor` 接口存根；v1.x 实现 `S3ObjectLockAnchor`。

#### 质量规则和通知
- **D-18：** 质量规则 DSL = 构建器链式强类型。每条规则实现 `asset.QualityRule` 接口（`Name() string`、`Evaluate(ctx, eval QualityEvaluator) (QualityResult, error)`）。v1 三种类型：
  - `asset.NullCheck{Column string, MaxRate float64}` → `COUNT(NULL)/COUNT(*) <= MaxRate`
  - `asset.RangeCheck{Column string, Min, Max float64}` → `MIN(col) >= Min AND MAX(col) <= Max`
  - `asset.SQLAssertion{Name, SQL, Predicate AssertionPredicate}` → 用户 SQL，`${asset}` 插值物化表，Predicate 解释结果（`ScalarEqualsZero`、`ScalarLessThan(N)`、`RowCountIsZero`）
  - 其他类型（UniqueCheck、RegexCheck、FreshnessCheck-as-quality、自定义 Go 谓词）推迟到 v1.x。
- **D-19：** 质量评估在执行器事务内运行，紧接血缘/架构之后，具有独立状态列：
  - 序列：`Materialize 成功 → lineage_writer → schema_writer → quality_evaluator → run.state=succeeded`，全部在同一 DB 事务中。
  - **独立列：** `runs.run_quality_status ENUM(passed, failed, skipped, error)`（没有规则的资产默认为 NULL/skipped）。`runs.state` 保留阶段 2 D-17 生命周期语义—质量失败 **不会** 翻转 `state`。
  - 与阶段 1 D-09 + 阶段 4 D-04 一致："元数据失败不会使数据工作失败"。
  - **每规则结果：** 新建 `quality_results` 表 `(run_id, rule_name, rule_type, status ENUM(passed,failed,error), measured_value, threshold, evaluated_at, error_message NULLABLE)`。`runs.run_quality_status` = 所有规则中最差的。
  - **失败分发：** `quality.rule_failed` event_log + River 任务分发告警（D-21）。
  - **连接器评估器：** `connector.QueryAggregate(ctx, sql) (any, error)` 新的稳定能力。不实现它的连接器（例如纯文件 S3）→ 规则状态 `error` + 原因 "connector does not support aggregate queries"。
- **D-20：** QUAL-04 Freshness SLA 由 **调度器子命令** 评估，不是质量规则：
  - 构建器：`asset.New("x").FreshnessSLA(asset.FreshnessSLA{MaxLag: 6*time.Hour, ScopeAfterCronFire: true})`。
  - 新建 `schedules.last_succeeded_at` 列。调度器 tick（阶段 3 D-01..04）扩展扫描 `last_succeeded_at + max_lag < NOW()` → 发送 `sla.breached` + River 分发告警。每个 SLA 破坏窗口一次告警（在 `(asset, sla_breach_window_start)` 上去重）。
- **D-21：** 通知和告警：
  - **渠道（v1）：** webhook（POST JSON）+ SMTP（启动配置自包含 host/port/user/password/from）。SES、SendGrid、Slack 推迟到 v1.x。
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
  - **分发：** 全部通过 River 任务（不阻塞执行器/调度器/治理处理器热路径）。River 原生重试。永久失败发送 `notification.dispatch_failed` 到 event_log + 结构化错误日志。
  - **提交者通知（GOV-04）：** `governance.{approved,rejected}` → 通知规则 → 提交者电子邮件（取决于 `users.email`，AUTH-01 已就绪）。

#### 计划分区建议
- **D-22：** 建议分区（规划者可调整）：
  - **05-01 RBAC 基础：** Casbin 集成 + Postgres 适配器 + roles/users/role_assignments + 角色权限 CRUD REST/CLI + audit_log 表 + RLS 模式 + 哈希链写入器 + verify CLI
  - **05-02 列策略 + 仓库掩码同步：** column_policies 表 + ColumnPolicy DSL + REST PATCH + 全局 YAML 加载器 + MaskingProvisioner + Snowflake DDM 实现 + BigQuery CLS 实现 + 推送 River 任务 + 协调循环 + sync_failed/drift 事件
  - **05-03 PII 传播 + 非仓库管道掩码：** lineage_writer 扩展 + TagOverride DSL + AssetIO.Write 上的非仓库掩码函数 + 连接器能力断言顺序
  - **05-04 治理工作流：** asset_versions.governance_state + governance_reviews + .Reviewers/.Quorum/.RequireHumanReview/.EscalationRoles DSL + 自动预批准管道 + REST + CLI + 物化门控
  - **05-05 质量规则 + SLA + 告警：** QualityRule + 三条规则 + 执行器事务扩展 + run_quality_status + quality_results + connector.QueryAggregate + FreshnessSLA + 调度器扩展 + 通知分发器 + River 管道

#### 事件类型新增
- **D-23：**
  - 新增 `event_log`（追加 CHECK 约束）：`quality.rule_passed`、`quality.rule_failed`、`quality.rule_error`、`quality.run_evaluated`、`sla.breached`、`sla.recovered`、`notification.dispatched`、`notification.dispatch_failed`、`governance.materialization_blocked`
  - `audit_log` 类型（D-14 范围）：`policy.changed`、`policy.removed`、`masking.sync_failed`、`masking.sync_drift_detected`、`role.created/deleted/assigned/revoked`、`governance.submitted/approved/rejected/auto_approved/review_sla_breached`、`audit.exported/verify_failed`、`metadata.tag_overridden`

### Claude 的决定权（来自 CONTEXT.md `<discretion>`，此研究负责提供建议）
- Casbin Postgres 适配器选择（`casbin/casbin-pg-adapter` v1.5.0 vs `pckhoi/casbin-pgx-adapter/v3` vs 自建薄适配器）
- Snowflake DDM API 调用模式、SQL 模板细节
- BigQuery CLS 数据目录策略标签分类法结构
- 掩码类型实现细节（Hash 盐管理、Partial 默认长度、Redact 字符集）
- `policies.yaml` 路径和重载语义
- 审批过程中审查者池变化时 Quorum=All 语义
- 审计日志 JSONL 行模式版本控制策略
- River 队列拓扑（独立的 `policy_sync` 和 `notification` 队列 vs 单队列带优先级）
- `notifications.yaml` 模板变量语言
- 审查者池快照持久化形状（反规范化 JSON vs 独立连接表）
- `column_policies` 是否包含 `partition_key`（推迟到 v1.x）
- 审计 verify CLI 输出格式默认

### 推迟的想法（超出范围）
（来自 CONTEXT.md `<deferred>`，此研究阶段不探索）
- 资产数据保留 TTL 执行（仅 v1 模式）
- 外部 S3 Object Lock 实现（仅 v1 接口）
- 后台篡改检测协调器（v1.x）
- 自动 SLA 升级（v1.x）
- 其他掩码类型（Tokenization、FPE、bucketing）
- 其他质量规则类型（UniqueCheck、RegexCheck、自定义谓词）
- 其他通知渠道（Slack、SES、SendGrid、MS Teams、PagerDuty 事件 API）
- AGOV-01/02、PLAT-02、自定义 Casbin 模型文件
- 审计验证检查点摘要
- 结构化拒绝原因分类
- OpenLineage 格式审计导出
- 列策略分区感知
</user_constraints>

---

<phase_requirements>
## 阶段需求

| ID | 描述 | 研究支持 |
|----|-------------|------------------|
| RBAC-01 | 管理员可以定义命名角色 | D-01 + §1 Casbin 执行器 + roles 表；REST `/roles` POST，发送 `role.created` 到 audit_log |
| RBAC-02 | 管理员可以将用户分配到角色 | D-01 + §1 role_assignments 表；REST `/users/:id/roles` PUT，发送 `role.assigned` 到 audit_log |
| RBAC-03 | 管理员可以定义列级访问策略 | D-02 + §3 column_policies 表 + ColumnPolicy DSL + REST PATCH + 全局 YAML |
| RBAC-04 | 将策略同步到 Snowflake DDM 和 BigQuery CLS | D-04/D-05 + §4 MaskingProvisioner + §4.2 Snowflake DDL 模板 + §4.3 BigQuery Data Catalog API + 推送 River + 协调循环 |
| RBAC-05 | 非仓库连接器在 materialize 期间在管道内掩码 | D-03/D-05 + §5 AssetIO.Write 包装 + 管道掩码函数（Hash/Redact/Partial） |
| RBAC-06 | 所有数据访问事件、策略变更、用户操作进入哈希链审计日志 | D-13/D-14 + §2 审计模式 + 哨兵行序列化 + 规范 JSON + RLS + 哨兵行哈希链写入器 |
| GOV-01 | 数据工程师可以提交资产供审查 | D-08/D-12 + §6 governance_reviews + REST/CLI 提交 |
| GOV-02 | 平台分配审查者到治理团队 + 通知 | D-09/D-21 + §6 审查者池解析 + River 通知 |
| GOV-03 | 审查者批准/拒绝，强制评论；状态转换 | D-08/D-11/D-12 + §6 状态机 + 拒绝强制评论 + 原子更新 |
| GOV-04 | 审查者决定通知提交者 | D-21 + §7 notifications.yaml 路由 + River SMTP/webhook |
| GOV-05 | 所有批准决策记录在审计日志中 | D-13/D-14 + §2 哈希链写入器 + governance.* 事件类型 |
| GOV-06 | 完整审计日志导出为 JSON/CSV | D-15 + §2.4 流式导出 + JSONL/CSV 序列化器 + 递归 `audit.exported` |
| GOV-07 | 审计日志保留 TTL 配置（v1 部分） | D-16 + §2.5 expires_at + 全局配置 + 清除手册 |
| QUAL-01 | 工程师定义质量规则（空值率、范围、SQL 断言） | D-18 + §8 NullCheck/RangeCheck/SQLAssertion DSL |
| QUAL-02 | 在 materialize 后自动评估所有规则 | D-19 + §8 executor.commitSuccess 扩展 + 相同事务 |
| QUAL-03 | 失败标记 run.run_quality_status + UI 显示 | D-19 + §8 quality_results 表 + run_quality_status 列 |
| QUAL-04 | 资产 SLA 阈值（物化在 N 小时内） | D-20 + §9 FreshnessSLA + 调度器 tick 扩展 + last_succeeded_at |
| QUAL-05 | 质量失败/SLA 破坏发送告警 | D-21 + §7 River 分发器 + webhook + SMTP |
</phase_requirements>

---

## 项目约束（来自 CLAUDE.md）

CLAUDE.md 直接约束此研究：

- **Go 后端：** 此阶段所有代码都是 Go；不引入 Python 运行时依赖
- **PostgreSQL 主存储：** 通过 ent + sqlc 双 ORM 模式；新表使用 ent；热读使用 sqlc
- **Casbin v2.135.x：** CLAUDE.md §Authorization 锁定为首选 RBAC 库（**已验证：** v2.135.0 确实是 Casbin v2 最新发布）
- **golang-jwt/jwt/v5 v5.3.x：** 已在阶段 1 引入（`go.mod` 已验证：v5.3.0）
- **River v0.35.x：** CLAUDE.md §Execution Engine 锁定为作业队列（**已验证：** 当前 v0.35.1，发布于 2026-04-26）
- **chi/v5 v5.2.5：** HTTP 路由
- **Atlas + ent：** 迁移路径
- **`log/slog`：** 所有结构化日志
- **`prometheus/client_golang`：** 指标暴露（已在 go.mod，v1.19.1）
- **`gopkg.in/yaml.v3`：** 已存在，用于 `policies.yaml` / `notifications.yaml`
- **API 稳定性（CONN-08）：** 阶段 5 仅**附加地**扩展 `connector` 接口（新可选 `MaskingProvisioner`、`QueryAggregate`），不破坏现有接口
- **单二进制约束：** 无外部消息代理；通知渠道仅限 webhook + SMTP（与 D-21 一致）
- **OpenLineage：** 阶段 4 已设置。阶段 5 治理事件不通过 OpenLineage（D-14 范围明确排除）
- **中文文档优先级：** 技术决策文档使用中文，代码标识符使用英文（与阶段 4 RESEARCH.md 相同风格）

---

## 摘要

阶段 5 在阶段 1-4 提供的元数据 + 执行 + 血缘骨架之上构建**治理引擎**—管理员定义角色和列策略，平台同步到 Snowflake DDM 和 BigQuery CLS，工程师通过审批工作流提交资产，所有治理操作进入防篡改哈希链审计日志，质量规则在每次物化时自动评估并通过 webhook + SMTP 分发告警。

整个阶段是 **5 个推荐子计划**（D-22）的积累：05-01 RBAC + 哈希链 → 05-02 列策略 + 仓库掩码同步 → 05-03 PII 传播 → 05-04 治理工作流 → 05-05 质量规则 + SLA + 告警。每个子计划都与既定模式一致：ent 拥有 CRUD，sqlc 拥有热读 CTE/聚合，Atlas 处理迁移，阶段 4 D-19 三层（Go 库 + REST + CLI），River 处理所有外部 IO 异步（DDM/CLS 同步、webhook 发送、SMTP 发送），阶段 1 D-09 RLS-不可变性扩展到哈希链与哨兵行序列化。

技术上，四个最具挑战性的领域是：
1. **哈希链写入器：** 哨兵行 `SELECT … FOR UPDATE` 在调用者事务内序列化，RFC 8785 JCS 规范 JSON，SHA-256(prev || canonical(row))，Postgres 独立模式 + RLS（`platform_app` 仅 INSERT，完全禁止 UPDATE/DELETE）
2. **Snowflake DDM 同步：** `CREATE OR REPLACE MASKING POLICY` 是模式作用域对象；绑定到列后，`ALTER MASKING POLICY name SET BODY -> ...` 在下次查询时生效（不影响进行中的查询）；连接器需要 `APPLY MASKING POLICY ON ACCOUNT` 角色权限
3. **BigQuery CLS 同步：** 通过 Data Catalog Taxonomy + Policy Tag + IAM 三层；`cloud.google.com/go/datacatalog/apiv1` PolicyTagManagerClient + BigQuery Tables.update（columns[].policyTags）；表/列限制 1000 个策略标签，IAM 传播有最终一致性窗口
4. **PII 传播：** 必须在 lineage_writer 事务内同步运行（D-06）— 异步会创建下游列暂时未掩码的窗口

**主要建议：** 按 D-22 顺序构建。**05-01 在关键路径上：** 一旦第一条记录写入哈希链，就无法重建（陷阱 #5）— 必须在第一个治理事件之前定义和测试。

---

## 标准技术栈

### 核心（新依赖）

| 库 | 版本 | 用途 | 为什么是标准 |
|---------|---------|---------|-------------|
| `github.com/casbin/casbin/v2` | **v2.135.0**（最新；2025-12 系列）[已验证：`go list -m -versions` 2026-05-09，最新发布 v2.135.0] | RBAC 执行器（角色 → API 权限） | CLAUDE.md §Authorization 锁定首选；多策略后端、角色层次结构、`g(r.sub, p.sub) && keyMatch(r.obj, p.obj)` 匹配开箱即用 [引用：https://casbin.apache.org/docs/rbac] |
| `github.com/pckhoi/casbin-pgx-adapter/v3` | **v3.2.0**（2024-08，最新）[已验证：`go list -m -versions` 2026-05-09] | 通过 pgx/v5 的 Casbin Postgres 适配器 | **推荐选项** — 项目已使用 `pgx/v5 v5.9.1`，零驱动重复。`casbin/casbin-pg-adapter` v1.5.0（2025-11）使用 `go-pg/v9`，go-pg 已 EOL，会引入第二个 Postgres 驱动 [已验证：https://github.com/casbin/casbin-pg-adapter/blob/master/adapter.go 导入 `github.com/go-pg/pg/v9`] |
| `github.com/IguteChung/casbin-psql-watcher` | 最新（2024-2025） | 通过 Postgres LISTEN/NOTIFY 的 Casbin 监视器 | **推荐：** v1 单二进制运行时单个执行器不需要；但平台架构允许工作进程作为独立进程（阶段 2/3 已建立）→ 监视器保持策略跨多进程同步。阶段 5 v1 可以在 README 中提及 + 保留接口，避免过度工程 [引用：https://github.com/IguteChung/casbin-psql-watcher] |
| `github.com/riverqueue/river` | **v0.35.1**（2026-04-26）[已验证：`go list -m -versions` 2026-05-09] | 异步任务：policy_sync、notification_dispatch | CLAUDE.md §Execution Engine 已锁定。原指指数退避、唯一任务、周期任务、跨进程协调 [引用：https://riverqueue.com/docs/job-retries] |
| `github.com/riverqueue/river/riverdriver/riverpgxv5` | 与 river 配对 | River pgx/v5 驱动 | River 推荐的驱动；与项目现有 pgx/v5 完全匹配 |
| `github.com/gowebpki/jcs` | **最新**（最近发布） | RFC 8785 JSON 规范化方案 | 哈希链规范 JSON 序列化必需。两个候选：`gowebpki/jcs`（Go 原生）vs `lenny321/json-canon`（v0.2.0 2026-02）。**推荐 gowebpki/jcs**—成熟、零依赖 [引用：https://github.com/gowebpki/jcs] |
| `github.com/wneessen/go-mail` | **最新**（活跃维护） | SMTP 邮件发送 | `gopkg.in/mail.v2`（gomail）维护停滞；`go-mail` 是现代替代方案，并发安全，html/template 集成，go-mail 自定义 SMTP 包扩展支持更多 SASL 机制 [引用：https://pkg.go.dev/github.com/wneessen/go-mail] |
| `cloud.google.com/go/datacatalog` | **v1.31.0**（最新）[已验证：`go list -m -versions` 2026-05-09] | BigQuery CLS：创建/管理 Taxonomy + PolicyTag + IAM 绑定 | 官方 Google Go SDK；`PolicyTagManagerClient`，包路径 `cloud.google.com/go/datacatalog/apiv1` |

### 已在 go.mod 中，无需新依赖

| 库 | 版本 | 用途 |
|---------|---------|---------|
| `github.com/golang-jwt/jwt/v5` | v5.3.0 | JWT 签名/验证（auth.Middleware 已使用，扩展声明添加 `roles []string`） |
| `github.com/jackc/pgx/v5` | v5.9.1 | 主驱动（哈希链哨兵行、audit_log 写入） |
| `entgo.io/ent` | v0.14.0 | 新实体：Role、RoleAssignment、ColumnPolicy、GovernanceReview、QualityRule、QualityResult、AuditLogEntry |
| `github.com/sqlc-dev/sqlc` | v1.31.x（工具） | 哈希链验证顺序扫描、审查者解析、SLA 扫描 |
| `github.com/snowflakedb/gosnowflake` | **v1.19.1**（最新）[已验证：2026-05-09] | Snowflake DDM DDL 执行（database/sql 模式 ExecContext） |
| `cloud.google.com/go/bigquery` | v1.77.0 | BigQuery 表元数据更新（写入策略标签到列） |
| `github.com/go-chi/chi/v5` | v5.2.5 | 新路由：`/audit/*`、`/governance/*`、`/policies/*`、`/roles`、`/users/:id/roles` |
| `gopkg.in/yaml.v3` | v3.0.1 | `policies.yaml`、`notifications.yaml` 加载 |
| `github.com/prometheus/client_golang` | v1.19.1 | 计数器/ Gauge：`audit.verify_failed`、`masking.sync_failed`、`quality.rule_failed`、`governance.review_sla_breached` |
| `crypto/sha256`、`encoding/json`、`log/slog` | 标准库 | 哈希计算、有效载荷序列化、结构化日志 |

### 考虑的替代方案

| 而不是 | 可以使用 | 权衡 |
|------------|-----------|----------|
| `pckhoi/casbin-pgx-adapter/v3` | `casbin/casbin-pg-adapter` v1.5.0 | 官方但使用 go-pg/v9（EOL 驱动），引入双重 Postgres 驱动。**拒绝。** |
| `pckhoi/casbin-pgx-adapter/v3` | 在现有 pgx 池上自建薄适配器 | 减少一个外部依赖。但 Casbin Adapter 接口 + FilteredAdapter + Watcher 协议需要正确实现；外部库已经过战斗测试。**拒绝**—在计划估计下，自建薄适配器是计划级风险。 |
| Go `gopkg.in/mail.v2`（gomail） | `github.com/wneessen/go-mail` | gomail 维护停滞（上次 GitHub 更新是很久前），不再支持现代 SASL/STARTTLS 行为。**拒绝。** |
| `gowebpki/jcs`（RFC 8785） | 简单 `json.Marshal` + 手动键排序 | 简单实现在浮点序列化、Unicode 转义、空对象"边缘情况"上有问题。RFC 8785 是加密应用的标准。**拒绝**—哈希链不能容忍序列化变更导致历史哈希失效。 |
| 推送 + 协调（D-04） | 仅推送 | 静默漂移（DBA 直接修改仓库，无法检测）。陷阱 #8 反对。**拒绝。** |
| 相同事务 PII 传播（D-06） | 异步 River 任务传播 | 下游列暂时未掩码窗口 → 合规风险。**拒绝。** |
| 相同事务质量评估（D-19） | 异步 River 作业评估 | UI 看到运行完成但质量待定；告警延迟。**拒绝。** |
| BigQuery 数据掩码（columnDataMaskingExemptionRules） | 仅 PolicyTag + Fine-Grained Reader | 数据掩码是**附加**功能（"为增强列级访问控制，您可以选择使用动态数据掩码"）。v1 仅 PolicyTag 拒绝/允许；掩码类型映射到掩码函数推迟到 05-02 详细设计 [引用：https://docs.cloud.google.com/bigquery/docs/column-level-security-intro] |

**安装：**
```bash
go get github.com/casbin/casbin/v2@v2.135.0
go get github.com/pckhoi/casbin-pgx-adapter/v3@v3.2.0
go get github.com/IguteChung/casbin-psql-watcher@latest  # 接口保留；v1 不强依赖
go get github.com/riverqueue/river@v0.35.1
go get github.com/riverqueue/river/riverdriver/riverpgxv5@v0.35.1
go get github.com/gowebpki/jcs@latest
go get github.com/wneessen/go-mail@latest
go get cloud.google.com/go/datacatalog@v1.31.0  # 升级现有 indirect
```

**版本验证（2026-05-09 通过 `go list -m -versions <module>` + Go 模块代理）：**
- Casbin v2.135.0 确实是最新的（`go list` 显示完整发布列表在 v2.135.0 停止；CLAUDE.md "v2.135.x" 已标记）
- River v0.35.1 确实是最新的（2026-04-26 发布）
- gosnowflake v1.19.1 确实是最新的（匹配项目 go.mod）
- cloud.google.com/go/datacatalog v1.31.0 是最新的（项目当前为 indirect，需要升级为 direct）
- pckhoi/casbin-pgx-adapter v3.2.0（最新稳定版，匹配 pgx/v5）

---

## 架构模式

### 建议的包结构（阶段 5 新增）

```
internal/
├── auth/
│   ├── casbin.go            # 新增：Casbin 执行器初始化 + 加载 model.conf + Postgres 适配器 + 监视器
│   ├── jwt.go               # 现有：扩展 Claims 添加 Roles []string
│   ├── middleware.go        # 现有：扩展 Permission(action, obj) 中间件，调用 enforcer.Enforce
│   └── service.go           # 现有：扩展 AssignRole / RevokeRole API
├── audit/
│   ├── writer.go            # 新增：WriteEntry(ctx, tx, entry) — 哨兵行 FOR UPDATE + 规范 JSON + SHA-256 + INSERT
│   ├── canonical.go         # 新增：围绕 gowebpki/jcs.Transform 的包装器，提供 CanonicalJSON 函数
│   ├── verify.go            # 新增：Verify(ctx, from, to seq) error — 顺序扫描，重新计算每行哈希，首次不匹配立即返回
│   ├── export.go            # 新增：Export(ctx, w io.Writer, format) — 流式 JSONL/CSV，包含 seq + self_hash
│   ├── types.go             # 新增：AuditEventType 枚举 + 各种类型化有效载荷结构
│   ├── anchor.go            # 新增：Anchor 接口存根（D-17）
│   └── retention.go         # 新增：expires_at 辅助函数（D-16，仅保留模式；无清除实现）
├── policy/
│   ├── store.go             # 新增：column_policies CRUD（ent 客户端）+ COALESCE 解析（运行时 > 构建器 > yaml-default）
│   ├── mask.go              # 新增：MaskType 枚举 + Hash/Redact/Partial 实现 + apply(row Row) Row（用于 RBAC-05 管道内掩码）
│   ├── yaml_loader.go       # 新增：加载 policies.yaml — tag→mask 默认 + tag→reviewer 角色（D-09 第二路径）
│   └── handler.go           # 新增：REST 处理器 PATCH /assets/:name/columns/:col/policy
├── governance/
│   ├── workflow.go          # 新增：提交/批准/拒绝业务逻辑 + 状态机转换 CHECK
│   ├── reviewers.go         # 新增：审查者池解析（构建器 + 标签 + 所有者三路径 + Quorum）
│   ├── auto_approval.go     # 新增：预批准管道（5 项检查）
│   ├── pii_propagator.go   # 新增：沿 column_edges BFS + 并集规则 + 覆盖检测（D-06，在 lineage_writer 同一事务中调用）
│   ├── handler.go           # 新增：REST POST /governance/submit、/governance/reviews/:id/approve|reject
│   ├── sla_scanner.go      # 新增：调度器 tick 调用 — 扫描超过 SLA 的审查
│   └── service.go           # 新增：整合所有治理子模块的门面
├── quality/
│   ├── rule.go              # 新增：QualityRule 接口 + NullCheck/RangeCheck/SQLAssertion 实现
│   ├── evaluator.go         # 新增：在 executor.commitSuccess 同一事务内评估所有规则
│   ├── store.go             # 新增：quality_results 表 CRUD + run_quality_status 更新
│   ├── freshness.go         # 新增：FreshnessSLA 类型 + 调度器扫描辅助函数
│   └── dispatcher.go        # 新增：质量失败 → 入队 notification River 作业
├── notification/
│   ├── channel.go           # 新增：Channel 接口（webhook、smtp）+ 两个实现
│   ├── webhook.go           # 新增：HMAC-SHA256 签名 + 重试感知分发
│   ├── smtp.go              # 新增：go-mail 包装器（host/port/user/password/from 启动配置）
│   ├── router.go            # 新增：notifications.yaml 加载 + 事件类型模式 → 渠道路由
│   ├── template.go          # 新增：简单 {var} 替换（建议拒绝完整 Go 模板引擎以保持可读性）
│   └── worker.go            # 新增：River worker：消费 notification.dispatch 任务
├── connector/
│   ├── capability.go        # 扩展：MaskingProvisioner + QueryAggregate 接口（与 D-05/D-19 相同的模式）
│   └── firstparty/
│       ├── snowflake/
│       │   └── masking.go   # 新增：实现 MaskingProvisioner — CREATE/ALTER/DROP MASKING POLICY DDL
│       └── bigquery/
│           └── masking.go   # 新增：实现 MaskingProvisioner — Data Catalog Taxonomy + PolicyTag + Tables.update
├── runtime/
│   └── executor.go          # 扩展：commitSuccess 添加 quality_evaluator 调用；runStep 在治理门控前添加
├── lineage/
│   └── capture.go           # 扩展：CaptureRun 调用 governance.PIIPropagator
├── storage/ent/schema/
│   ├── role.go             # 新增
│   ├── role_assignment.go   # 新增
│   ├── column_policy.go     # 新增（时间表模式）
│   ├── governance_review.go # 新增
│   ├── quality_rule.go      # 新增
│   ├── quality_result.go  # 新增
│   ├── audit_log_entry.go # 新增（独立模式 "audit"）
│   └── asset_version.go   # 扩展：添加 governance_state 列
├── api/
│   └── routes.go           # 扩展：注册新 chi 路由（带 Casbin 中间件）
cmd/platform/
├── audit.go                 # 新增：./platform audit verify | export
├── governance.go            # 新增：./platform governance submit | review | status
├── policy.go               # 新增：./platform policy show | list
├── role.go                 # 新增：./platform role create | assign | revoke
├── reconciler.go          # 新增：./platform reconciler — 15 分钟 tick 调用 MaskingProvisioner.ListMaskingPolicies
└── scheduler.go            # 扩展：tick 调用 quality.FreshnessScanner + governance.SLAScanner
migrations/
└── 20260510000000_phase5_governance.sql
   # 一次性引入：roles、role_assignments、column_policies（时间）、
   # governance_reviews、quality_rules、quality_results、
   # asset_versions.governance_state ALTER、runs.run_quality_status ALTER、
   # schedules.last_succeeded_at ALTER + audit 模式 + audit_log + audit_sentinel +
   # RLS on audit 模式 + Casbin 策略表（casbin_rule）
   # + CHECK 约束扩展（event_log.event_type、audit_log.event_type）
   # + 双重角色：audit_migrator（DDL）+ audit_purge（清除用户保留给 v1.x）
```

### 模式 1：哈希链写入器（D-13）

**来源：** PostgreSQL Wiki + 陷阱 #5 + 阶段 1 D-09 RLS 扩展。

```go
// internal/audit/writer.go
// 来源：基于陷阱 #5 + 阶段 1 D-09 RLS 模式
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
    Payload      any        // 任何 JSON 可序列化
    ExpiresAt    *time.Time // D-16 保留
}

// WriteEntry 在调用者事务内将写入序列化到哈希链。
// 调用者必须已经调用 BEGIN 并传入 *sql.Tx；调用者负责 commit/rollback。
//
// 协议：
// 1. SELECT seq, self_hash FROM audit.audit_sentinel WHERE id=1 FOR UPDATE
//    （哨兵是初始化迁移插入的单行；FOR UPDATE 序列化所有写入者）
// 2. canonical = jcs.Transform(payloadJSON)
// 3. h = SHA-256(seq+1 || prev.self_hash || ts || event_type || actor || resource_type || resource_id || canonical)
// 4. INSERT INTO audit.audit_log (...) VALUES (...)
// 5. UPDATE audit.audit_sentinel SET seq = seq+1, self_hash = h WHERE id=1
//
// 复杂度：每次写入一次事务级锁。治理 + 访问控制频率低（≪ 运行.* 频率）→ 单锁可接受。
func WriteEntry(ctx context.Context, tx *sql.Tx, e Entry) (seq int64, err error) {
    var prevSeq int64
    var prevHash []byte
    if err := tx.QueryRowContext(ctx,
        `SELECT seq, self_hash FROM audit.audit_sentinel WHERE id = 1 FOR UPDATE`,
    ).Scan(&prevSeq, &prevHash); err != nil {
        return 0, fmt.Errorf("audit: lock sentinel: %w", err)
    }
    seq = prevSeq + 1

    payloadJSON, err := encodeCanonical(e.Payload) // 底层使用 jcs.Transform
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
- `audit.audit_sentinel` 在 phase5 迁移中插入单行 `(id=1, seq=0, self_hash=bytea(32 zero bytes))`—这是创世块。
- `FOR UPDATE` 序列化整个治理事件流。**估计频率：** 单实例治理事件 ≪ 1 事件/秒（人类触发）→ 单锁是过度工程。实际上，哨兵行锁已经是与阶段 4 D-13 中 `column_edges` 写入类似的既定模式。
- `jcs.Transform`（RFC 8785）保证跨语言/版本/序列化器的字节级一致性—这是长期哈希验证（10 年合规保留）的先决条件。简单的 `json.Marshal` + 排序键对于浮点 / Unicode 转义边缘情况是不够的。

### 模式 2：仓库掩码连接器能力（D-05，扩展阶段 4 D-06）

**来源：** 阶段 4 `internal/connector/capability.go` SchemaDescriber 模式。

```go
// internal/connector/capability.go（扩展）
// 来源：阶段 4 D-06 SchemaDescriber 模式
package connector

import "context"

// MaskingProvisioner 是一个可选能力（阶段 5 D-05）。
// Snowflake 和 BigQuery 连接器实现此接口；PostgreSQL/MySQL/S3/GCS/HDFS 不实现，
// 由 RBAC-05 管道内掩码替代（internal/policy/mask.go 在 AssetIO.Write 时应用）。
type MaskingProvisioner interface {
    ApplyMaskingPolicy(ctx context.Context, asset AssetRef, policy ColumnPolicy) error
    RemoveMaskingPolicy(ctx context.Context, asset AssetRef, column string) error
    ListMaskingPolicies(ctx context.Context, asset AssetRef) ([]ColumnPolicy, error)
}

// QueryAggregate 是一个可选能力（阶段 5 D-19）。质量规则评估器需要
// 可以执行返回标量或行计数的聚合 SQL 的连接器。PostgreSQL/MySQL/Snowflake/BigQuery
// 都可以实现；S3/GCS/HDFS 基于文件的连接器不能 → 质量规则状态 'error'，原因 "connector does not support aggregate queries"。
type QueryAggregate interface {
    QueryAggregate(ctx context.Context, asset AssetRef, sql string) (any, error)
}
```

### 模式 3：三层（阶段 4 D-19）— 审计验证示例

```go
// 单一 Go 库函数（REST + CLI + 内部代码共享）
// internal/audit/verify.go
func Verify(ctx context.Context, db *sql.DB, fromSeq, toSeq int64) (Result, error) { /* ... */ }

// REST 处理器 — 包装 Verify，返回 200 + JSON 报告
// internal/api/audit.go
func handleAuditVerify(w http.ResponseWriter, r *http.Request) { /* ... */ }

// CLI 子命令 — 包装 Verify，输出表或 --format=json
// cmd/platform/audit.go
func auditVerifyCmd(args []string) { /* ... */ }
```

### 应避免的反模式

- **❌ 自建 RBAC：** Casbin 正确处理边缘情况（循环检测、隐式角色、角色继承限制）。自建会反复遇到 hrekov.com 上列出的相同陷阱。
- **❌ 将 run.* 事件包含在哈希链中：** D-13/D-14 范围明确—热路径序列化将是致命的。
- **❌ 异步 PII 传播：** D-06 相同事务保证不能放弃。
- **❌ 使用 `json.Marshal` 进行规范序列化：** 浮点序列化、空对象、Unicode 转义都会破坏长期哈希验证。必须使用 RFC 8785 JCS。
- **❌ 在 audit_log 和业务表之间共享迁移用户：** 陷阱 #5 明确反对。独立模式、独立用户、独立备份。
- **❌ 在策略变更事务中同步执行 Snowflake DDL：** 阶段 4 D-04 + 陷阱 #6 确立的原则—外部 IO 始终通过 River 异步。
- **❌ 允许 Quorum=All 在审批中途重新计算审查者池：** D-09 明确快照—这是审计可追溯性的先决条件。
- **❌ 在治理处理器热路径中同步分发 webhook：** D-21 明确 River 异步—同步 HTTP 分发会给批准 API 增加 100-1000ms不可预测延迟。

---

## 不要自己动手

| 问题 | 不要构建 | 使用 | 为什么 |
|---------|-------------|-------------|----------|
| RBAC 执行 + 角色层次结构 | 自建 RBAC + 角色图 | `casbin/casbin/v2` v2.135.0 | 角色继承边缘情况是雷区—Casbin 经过战斗测试 |
| Casbin Postgres 适配器 | 自建薄适配器 | `pckhoi/casbin-pgx-adapter/v3` v3.2.0 | FilteredAdapter + Watcher + 增量更新/删除协议—经过战斗测试 |
| 异步作业队列 + 重试 | 自建队列 | `riverqueue/river` v0.35.1 | 事务性入队、指数退避、唯一任务、周期任务、Web UI—已在 CLAUDE.md 中锁定 |
| 哈希规范化 JSON | 简单排序键 + json.Marshal | `gowebpki/jcs`（RFC 8785 JCS） | 浮点、Unicode 转义、空对象边缘情况 |
| JWT 签名/验证 | 自建 | `golang-jwt/jwt/v5`（已在 go.mod） | 算法兼容性 + 过期 + 声明验证 |
| HMAC webhook 签名 | 自建 + 比较 | 标准库 `crypto/hmac` + `crypto/subtle.ConstantTimeCompare` | 计时攻击防御不能使用 `==` 比较 |
| Cron 表达式解析 | 自建 | `robfig/cron/v3`（已在 go.mod，阶段 3） | 标准 cron + 扩展语法已解析 |
| SMTP + STARTTLS + SASL | 自建 net/smtp | `wneessen/go-mail` | gomail 维护停滞；net/smtp 被 Go 团队冻结 |
| BigQuery Data Catalog API | 自建 REST 客户端 | `cloud.google.com/go/datacatalog` apiv1 | 官方 Google Go SDK，包含 PolicyTagManagerClient + IAMPolicyClient + 自动重试/退避 |
| Snowflake DDL 执行 | 自建 HTTP+OAuth REST | `snowflakedb/gosnowflake` v1.19.1（已在 go.mod） | 阶段 2 已用于 Snowflake 连接器；db.ExecContext 可以执行 DDL |
| 状态机转换 CHECK | 自建触发器 | Postgres CHECK 约束 + ent 枚举 | 现有阶段 2 D-17 模式 |
| 哈希链验证顺序扫描 | 自建游标 + 重新计算 | sqlc `SELECT ... ORDER BY seq` + Go 循环 | 简单正确，不需要"库" |

**关键洞察：** 阶段 5 是一个"组装阶段"—几乎每个子系统都有经过战斗测试的库。**唯一**值得自建的是 `internal/audit/writer.go` 的哨兵行哈希链—因为其协议（`SELECT FOR UPDATE` + 规范 + SHA-256 + 双 INSERT/UPDATE）是 Postgres 特定的业务逻辑，没有通用库。

---

## 运行时状态清单

> 阶段 5 是**新功能引入**而不是重命名/重构，不需要运行时状态清单。本节明确为空。

**所有类别为空：** 此阶段创建新表、新进程、新 CLI 子命令；不修改任何现有数据/配置/服务名称。

---

## 常见陷阱

### 陷阱 1：哈希链规范 JSON 不稳定 → 历史哈希失效

**出问题的地方：** 一年后审计员要求验证 5,000 条历史治理记录的哈希链。某条记录的有效载荷包含浮点数（例如 `quality_threshold: 0.05`）、Unicode 字符串、嵌套对象。新的 Go 标准库 `json.Marshal` 调整了浮点序列化精度—所有历史 self_hash 重新计算都与存储的不匹配。审计员判断"篡改"，而实际上是序列化漂移。
**为什么发生：** `json.Marshal` 不是确定性的；映射顺序可能不同；浮点数使用 ECMAScript 模式 vs IEEE 754 不同；Unicode 高位字符转义策略不同。
**如何避免：** 使用 `gowebpki/jcs.Transform`（RFC 8785 JCS）。必须在 phase5 第一次 audit_log 写入之前正确；改造意味着重写所有历史哈希。
**警告迹象：** 相同有效载荷在不同时间产生不同哈希。CI 应添加"规范稳定性"测试：固定 5 个有效载荷，哈希必须在 100 次迭代 + 不同 Go 版本中稳定。

### 陷阱 2：Snowflake 掩码策略是模式作用域，工程师误以为是账户作用域

**出问题的地方：** 在 `db1.schema_a` 中创建 `email_mask`。绑定到 `db2.schema_b.users.email`。运行并报错"掩码策略 email_mask 不存在或未授权"—同名列策略在 db2.schema_b 中不存在。
**为什么发生：** [已验证：docs.snowflake.com] 掩码策略是**模式作用域标识符**—不同模式中的同名列策略是不同的对象。
**如何避免：** 阶段 5 平台必须在每个（数据库、模式）组合下管理策略副本，或者选择**单一治理模式**（例如 `governance.policies`）并应用跨模式/数据库。**推荐：** `MaskingProvisioner.ApplyMaskingPolicy` 实现使用完全限定的 `<asset_db>.<asset_schema>.<policy_name>`，ALTER 自动管理模式上下文。
**警告迹象：** 策略同步成功但运行时 ALTER COLUMN SET MASKING POLICY 失败"不存在"。
**来源：** https://docs.snowflake.com/en/sql-reference/sql/create-masking-policy "schema-scoped identifier"

### 陷阱 3：Snowflake ALTER MASKING POLICY 不影响进行中的查询

**出问题的地方：** 紧急合规事件—撤销 ANALYST 对 ssn 列的可见性。`ALTER MASKING POLICY ssn_mask SET BODY -> '***'` 立即返回成功。但分析师的长期运行 `SELECT *` 仍然显示 SSN 直到查询完成。审计员认为"策略立即生效"，实际上并没有。
**为什么发生：** [已验证：docs.snowflake.com/en/sql-reference/sql/alter-masking-policy] "对策略规则所做的任何更改在**下次**使用该掩码策略的 SQL 查询运行时生效。"
**如何避免：** 为治理团队记录此事实。在紧急情况下，Snowflake 提供 `ABORT_QUERY` SQL 强制停止进行中的查询；需要将"紧急撤销"手册添加到文档中。
**警告迹象：** 合规事件后，SQL 审计日志仍然显示已掩码列被读取。
**来源：** https://docs.snowflake.com/en/sql-reference/sql/alter-masking-policy

### 陷阱 4：BigQuery PolicyTag IAM 传播窗口

**出问题的地方：** 平台推送策略 → BigQuery API 返回成功。但 IAM 策略实际上需要约 30 秒到 5 分钟才生效。在此窗口期间，分析师查询仍然根据旧权限返回 SSN。
**为什么发生：** [已验证：BigQuery 文档未明确说明窗口；社区报告 30 秒-5 分钟] BigQuery PolicyTag 写入立即返回，但 IAM 评估有缓存。
**如何避免：** 不要假设推送立即生效。文档化"新策略在 5 分钟后完全生效"。协调循环（D-04）在推送后 1 分钟轮询 ListMaskingPolicies 以验证效果。
**警告迹象：** 策略推送 API 200，立即查询仍然未掩码。
**来源：** https://docs.cloud.google.com/bigquery/docs/column-level-security-intro

### 陷阱 5：BigQuery 列只能附加 1 个 PolicyTag，policy_tag 树最多 5 层，每表 1000 个标签

**出问题的地方：** 设计阶段假设"每列可以附加多个标签（pii + financial + region_eu）"。BigQuery 拒绝。
**为什么发生：** [已验证：docs.cloud.google.com] "列只能有一个策略标签" + "表最多可以有 1,000 个唯一策略标签" + "策略标签层次结构最多五层深"。
**如何避免：** 分类法设计：每种 v1 掩码类型一个根标签（hash、redact、partial、unmasked）；特定（mask_type、allow_roles）组合作为子标签。最坏情况：1 列 = 1 子标签。<1000 列/表 → 在限制内。
**警告迹象：** ApplyMaskingPolicy 返回"标签层次结构太深"或"列上重复标签"。

### 陷阱 6：`casbin/casbin-pg-adapter` v1.5.0 使用 go-pg 而不是 pgx → 双重 Postgres 驱动

**出问题的地方：** 引入 `casbin/casbin-pg-adapter` 会引入 `go-pg/v9`（项目最初使用 pgx/v5）。两个连接池、两个监控指标、两个预处理语句缓存。
**为什么发生：** 官方 casbin/casbin-pg-adapter 仍然依赖 go-pg（go-pg 项目 EOL，最后版本 2024）。
**如何避免：** 使用 `pckhoi/casbin-pgx-adapter/v3 v3.2.0`—原生 pgx/v5，与项目连接池共享。
**来源：** https://github.com/casbin/casbin-pg-adapter/blob/master/adapter.go 导入 `github.com/go-pg/pg/v9`

### 陷阱 7：webhook 分发计时攻击（恒定时间比较）

**出问题的地方：** 使用 `==` 比较 HMAC 签名 → 攻击者从响应时间侧通道推断签名前缀。
**如何避免：** 必须使用 `crypto/subtle.ConstantTimeCompare`。
**来源：** webhooks.fyi 2025 最佳实践

### 陷阱 8：webhook 时间戳重放保护

**出问题的地方：** 攻击者捕获一个合法 webhook，一小时后重放 → 平台重新执行（例如重新批准批准通知、重新触发质量告警）。
**如何避免：** 签名内容包含 `timestamp.body`；接收方拒绝时间戳超过 5 分钟的请求；记录 webhook ID（uuid）+ 7-30 天 TTL 缓存用于幂等性。**注意：** 此阶段平台是**发送者**而不是接收者—所以这是接收者合同文档；平台需要：（1）签名生成正确；（2）包含 `X-Platform-Timestamp`、`X-Platform-Signature`、`X-Platform-Webhook-ID` 头；（3）River 重试不重新生成 ID（幂等性密钥重用）。

### 陷阱 9：River 重试和 webhook 幂等性

**出问题的地方：** River 分发任务超时 → 重试。下游 webhook 接收者得到两次（第一次实际成功但响应丢失；第二次也成功）。
**如何避免：** webhook 头有固定 `X-Platform-Webhook-ID = <river_job_uuid>`—重试使用相同 ID。接收者在此 ID 上幂等。

### 陷阱 10：质量评估在同一 tx 但连接器 SQL 是新连接

**出问题的地方：** D-19 要求在执行器元数据 tx 中进行质量评估。但 `connector.QueryAggregate(ctx, sql)` 通常打开到仓库（Snowflake/BigQuery）的新连接。如果那个外部连接超时（30 秒），整个执行器 tx 持有锁 30 秒，阻止其他步骤提交。
**如何避免：** 为 `connector.QueryAggregate` 设置严格超时（默认 30 秒，可配置）；超时时 → status='error' + 告警，不重试。阶段 5 必须为 `connector.QueryAggregate` 添加 ctx 超时强制。

### 陷阱 11：governance.gating_enabled 默认为 false 但生产环境忘记启用

**出问题的地方：** D-08 默认为关闭以保留开发工作流。生产部署忘记启用—治理状态永远不会实际阻止 materialize。SOC2 审计员发现"治理是装饰性的"。
**如何避免：** 阶段 5 启动时如果 `governance.gating_enabled=false` 则记录 WARN + Prometheus gauge `governance_gating_enabled = 0`。生产手册要求步骤"验证 governance.gating_enabled=true"。

### 陷阱 12：批准审查者池雪崩—一个审查者辞职

**出问题的地方：** D-09 审查者池在提交时快照。六个月后，进行中批准的审查者辞职。批准永久待定 → SLA 破坏通知发送到辞职邮件 → 静默退回。
**如何避免：** 阶段 5 SLA 破坏通知**额外**发送给所有者（D-11 确认）+ escalation_roles（D-11 选择加入）。当审查者辞职时，治理团队可以手动 `./platform governance reassign <review-id> <new-reviewer>`—此 CLI 是 v1 必需的。**建议：** 在 D-22 计划 05-04 中添加 reassign 命令。

---

## 代码示例

### 1. Casbin RBAC 模型（固定文件 `internal/auth/rbac_model.conf`）

```ini
# 来源：https://casbin.apache.org/docs/rbac（规范）
# 阶段 5 D-01：角色 → API 权限映射
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

**示例策略数据（在 `casbin_rule` 表中）：**
```
p, role:data-engineer, /assets/*/manage,         write
p, role:data-engineer, /governance/submit,       write
p, role:governance,    /governance/reviews/*,    write
p, role:governance,    /policies/*,              write
p, role:admin,         /users/*,                 manage
p, role:admin,         /audit/export,            read
g, alice@example.com,  role:data-engineer
g, bob@example.com,    role:governance
g, bob@example.com,    role:data-engineer  # 用户角色并集
```

### 2. Snowflake 掩码配置器（D-05 实现）

```go
// internal/connector/firstparty/snowflake/masking.go
// 来源：https://docs.snowflake.com/en/sql-reference/sql/create-masking-policy
//          + https://docs.snowflake.com/en/sql-reference/sql/alter-masking-policy
package snowflake

import (
    "context"
    "fmt"
    "github.com/kanpon/data-governance/internal/connector"
)

// 模板：根据 connector.ColumnPolicy.MaskType 切换 body
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

    // CREATE OR REPLACE — 幂等。一旦掩码类型命名稳定，可以重复推送而无副作用。
    ddl := fmt.Sprintf(
        `CREATE OR REPLACE MASKING POLICY %s AS (val VARIANT) RETURNS VARIANT -> %s`,
        qualified, body)

    if _, err := c.db.ExecContext(ctx, ddl); err != nil {
        return fmt.Errorf("snowflake: create masking policy %s: %w", qualified, err)
    }

    // ALTER COLUMN SET MASKING POLICY — 也是幂等的。Snowflake 不允许同一列上有两个策略；
    // 阶段 5 命名策略是 1 列 1 策略（dgp_mask_<table>_<col>）。
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
- 命名约定 `dgp_mask_<table>_<col>` 是 1:1 → 推送是幂等的 → 协调简单
- 使用 VARIANT 类型避免 STRING/NUMBER 类型签名匹配问题（Snowflake 要求输入/输出类型完全匹配）
- 使用 `CREATE OR REPLACE` 而不是 `CREATE IF NOT EXISTS`—前者更新 body；后者保持旧 body
- 连接器服务账户必须具有 `APPLY MASKING POLICY ON ACCOUNT TO ROLE <connector_role>` 权限—必须记录

### 3. BigQuery 掩码配置器（D-05 实现）

```go
// internal/connector/firstparty/bigquery/masking.go
// 来源：cloud.google.com/go/datacatalog/apiv1 PolicyTagManagerClient
//          + cloud.google.com/go/bigquery（Tables.Update）
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
//   每个项目一个 Taxonomy，名为 "dgp-platform"
//   策略标签树：root -> mask_type 子节点（hash/redact/partial/unmasked）
//   表列 → 单个策略标签（BigQuery 限制：每列 1 个标签）
//   标签上的 IAM 绑定：role/datacatalog.fineGrainedReader → allow_roles

func (c *Connector) ApplyMaskingPolicy(ctx context.Context, ref connector.AssetRef, policy connector.ColumnPolicy) error {
    // 1. 确保 Taxonomy 存在（幂等）
    taxonomyName := fmt.Sprintf("projects/%s/locations/%s/taxonomies/%s",
        ref.Project, c.location, "dgp-platform")
    if _, err := c.ptm.GetTaxonomy(ctx, &datacatalogpb.GetTaxonomyRequest{Name: taxonomyName}); err != nil {
        // 不存在 → 创建
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

    // 3. 在标签上绑定 IAM（role/datacatalog.fineGrainedReader → allow_roles）
    if err := c.bindIAM(ctx, tagName, policy.AllowRoles); err != nil {
        return fmt.Errorf("bigquery: bind iam: %w", err)
    }

    // 4. 更新表列：在 columns[].policyTags 中添加 tagName
    if err := c.attachToColumn(ctx, ref, policy.Column, tagName); err != nil {
        return fmt.Errorf("bigquery: attach to column: %w", err)
    }

    return nil
}
```

**关键约束（D-05 验证）：**
- Taxonomy `ActivatedPolicyTypes` 必须包含 `FINE_GRAINED_ACCESS_CONTROL`，否则 BigQuery 不识别
- 每列只能有 1 个策略标签（[已验证：docs.cloud.google.com]）→ 命名策略：每 `(asset, column)` 一个 mask_type 标签 → 标签 IAM 控制 allow_roles
- 服务账户需要：`bigquery.tables.update` + `datacatalog.taxonomies.create/get` + `datacatalog.policyTags.create/get/setIamPolicy`
- 策略生效有最终一致性窗口（30 秒-5 分钟；陷阱 #4）→ 协调循环必须验证

### 4. PII 传播器（D-06，相同事务调用）

```go
// internal/governance/pii_propagator.go
// 来源：D-06 同步 + 并集规则；从 internal/lineage/capture.go 调用
package governance

import (
    "context"
    "database/sql"
)

// PropagatePII 在 lineage_writer 写入 column_edges 后，在 tx 内调用。
// 算法：向上游 BFS 一跳；如果任何上游列有 pii=true，下游有 pii=true，
//       除非存在显式覆盖（tag_overridden 已在 audit_log 中检查）。
func PropagatePII(ctx context.Context, tx *sql.Tx, runID string, outputColumns []ColumnRef) error {
    for _, c := range outputColumns {
        // 1. 检查显式覆盖
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

        // 2. 向上游 BFS 一跳；如果任何上游有 pii=true，下游有 pii=true。
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
            // 将 pii=true 写入 asset_metadata（如果已存在则合并）
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

### 5. 质量评估器钩子（在 executor.commitSuccess 中）

```go
// internal/runtime/executor.go（扩展 commitSuccess 在约第 380 行）
// 在 SchemaWriter.Capture 后立即调用：
    if e.deps.QualityEvaluator != nil {
        conn, ref, _ := e.Resolve(a.Name())
        qstatus, err := e.deps.QualityEvaluator.Evaluate(ctx, tx, runID, a, conn, ref)
        if err != nil {
            // D-19：错误 → 记录 quality_results 状态 'error'，不回滚 tx
            slog.Error("quality.evaluate", "run_id", runID, "asset", a.Name(), "err", err)
        }
        if _, err := tx.ExecContext(ctx,
            `UPDATE runs SET run_quality_status=$1 WHERE id=$2`, qstatus, runID); err != nil {
            return fmt.Errorf("update run_quality_status: %w", err)
        }
    }
```

### 6. 审计日志 RLS 模式（迁移文件摘录）

```sql
-- migrations/20260510000000_phase5_governance.sql
-- 扩展阶段 1 D-09 RLS 模式到 audit 模式。来源：陷阱 #5 + 初始迁移模式。

CREATE SCHEMA IF NOT EXISTS audit AUTHORIZATION audit_migrator;

-- audit_migrator 角色（仅迁移用户）
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'audit_migrator') THEN
        CREATE ROLE audit_migrator NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'audit_purge') THEN
        CREATE ROLE audit_purge NOLOGIN;  -- 清除用户保留给 v1.x
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

-- 哨兵行：序列化哈希链写入的单个行
CREATE TABLE audit.audit_sentinel (
    id        SMALLINT PRIMARY KEY DEFAULT 1,
    seq       BIGINT NOT NULL DEFAULT 0,
    self_hash BYTEA NOT NULL DEFAULT decode('0000000000000000000000000000000000000000000000000000000000000000','hex'),
    CHECK (id = 1)  -- 总是恰好一行
);
INSERT INTO audit.audit_sentinel (id, seq, self_hash)
VALUES (1, 0, decode('0000000000000000000000000000000000000000000000000000000000000000','hex'))
ON CONFLICT DO NOTHING;

-- 所有权和权限
ALTER SCHEMA audit                  OWNER TO audit_migrator;
ALTER TABLE  audit.audit_log        OWNER TO audit_migrator;
ALTER TABLE  audit.audit_sentinel   OWNER TO audit_migrator;

-- platform_app：仅 INSERT on audit_log，在 sentinel 上 UPDATE（用于哈希链更新）
GRANT USAGE  ON SCHEMA audit                                  TO platform_app;
GRANT SELECT, INSERT ON audit.audit_log                       TO platform_app;
GRANT USAGE  ON SEQUENCE audit.audit_log_seq_seq              TO platform_app;
GRANT SELECT, UPDATE ON audit.audit_sentinel                  TO platform_app;
REVOKE UPDATE, DELETE, TRUNCATE ON audit.audit_log            FROM platform_app;

-- audit_purge：仅在 v1.x 使用，仅 DELETE。v1 部署中不分配给任何用户。
GRANT USAGE   ON SCHEMA audit                                 TO audit_purge;
GRANT DELETE ON audit.audit_log                              TO audit_purge;
REVOKE INSERT, UPDATE, TRUNCATE ON audit.audit_log            FROM audit_purge;

-- RLS：双重保险
ALTER TABLE audit.audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.audit_log FORCE  ROW LEVEL SECURITY;

CREATE POLICY audit_log_select ON audit.audit_log
  FOR SELECT TO platform_app USING (true);
CREATE POLICY audit_log_insert ON audit.audit_log
  FOR INSERT TO platform_app WITH CHECK (true);
-- 不创建 UPDATE/DELETE 策略 → 即使意外 GRANT 也无效

CREATE POLICY audit_log_purge_delete ON audit.audit_log
  FOR DELETE TO audit_purge USING (expires_at IS NOT NULL AND expires_at < NOW());
```

---

## 当前技术水平

| 旧方法 | 当前方法 | 何时变更 | 影响 |
|--------------|------------------|--------------|--------|
| 自建 RBAC | Casbin | ~2018 起 | 角色继承边缘情况是雷区—Casbin 经过战斗测试 |
| 查询时代理列掩码 | 仓库原生 DDM/CLS（Snowflake/BigQuery） | 2020 起 | Snowflake DDM GA 2020；BigQuery CLS GA 2020；查询时代理会被绕过（陷阱 #8） |
| `gopkg.in/mail.v2` | `wneessen/go-mail` | 2023 起 | gomail 维护停滞；go-mail 现代 Go 模式 |
| `casbin/casbin-pg-adapter`（go-pg） | `pckhoi/casbin-pgx-adapter/v3`（pgx/v5） | 2024 起 | go-pg 项目无新版本；社区迁移到 pgx/v5 |
| `jwt-go` | `golang-jwt/jwt/v5` | 2022 分支 | jwt-go 长期无人维护；golang-jwt v5 活跃维护 |
| 异步审计写入 | 哨兵行相同事务 | 2024 起（陷阱 #5 共识） | 异步写入留下提交窗口，攻击者可以在哈希计算前篡改 |
| 双 ORM（ent + sqlc） | 仍然是当前选择 | 阶段 1+ | ent 处理图形实体；sqlc 处理热读 CTE（阶段 4 D-16 已验证） |

**已弃用/过时：**
- **`go-pg`：** v9 是最后稳定版（~2024）。新项目应迁移到 pgx/v5。
- **`gomail`（gopkg.in/mail.v2）：** 维护停滞。
- **Snowflake 密码认证：** 仍然可用但不推荐用于生产；Snowflake 推动密钥对 / OAuth。阶段 5 v1 启动配置应支持密钥对路径。
- **BigQuery 旧版 SQL：** 阶段 5 不使用；所有 SQL 使用标准 SQL（默认）。

---

## 假设日志

> 此研究中标记为 [ASSUMED] 的假设 — 规划者 / discuss-phase 应在执行前确认。

| # | 声明 | 部分 | 如果错误的风险 |
|---|-------|---------|---------------|
| A1 | `IguteChung/casbin-psql-watcher` 对 v1 单二进制部署不必要（仅多进程需要）。建议接口保留但不集成。 | 标准技术栈 | 多进程部署中 RBAC 策略可能几秒钟内不新鲜；对 v1 单二进制无影响。 |
| A2 | Snowflake `CREATE OR REPLACE MASKING POLICY` body 使用 `VARIANT` 输入/输出来处理所有列类型。 | 代码示例 §2 | 如果列包含 GEOGRAPHY/ARRAY 复杂类型，可能需要类型分发；导致 ApplyMaskingPolicy 错误"输入/输出类型必须匹配"。 |
| A3 | 在 BigQuery 每列 1 个 PolicyTag 限制下，阶段 5 使用"每种 mask_type 一个根标签"分类法，可以工作。 | 代码示例 §3 | 如果需要"hash + region_eu"组合掩码，需要扩展组合标签（限制 1000/表）。v1 设计不考虑组合，足够。 |
| A4 | 治理事件频率 ≪ 1/秒，哨兵行单锁不会成为瓶颈。 | 代码示例 §6 | 如果大型组织有数百个同时批准 ⇒ 锁竞争；但治理本质上是人类节奏的，估计正确。 |
| A5 | `connector.QueryAggregate` 默认 30 秒超时对 Snowflake/BigQuery 简单聚合查询足够。 | 常见陷阱 #10 | 大表空值率查询可能超过 30 秒；建议使其可配置（NullCheck 默认 30 秒，SQLAssertion 默认 60 秒）。 |
| A6 | RFC 8785 JCS Go 实现 `gowebpki/jcs` 与 ECMAScript 实现哈希一致。 | 标准技术栈 | 如果不一致，跨语言验证（v2 Python SDK）将失败。建议在计划 05-01 中添加跨实现一致性测试。 |
| A7 | River v0.35.1 SQLite 驱动仍然是预览版—阶段 5 仅使用 Postgres 驱动（CLAUDE.md 确认 SQLite 仅用于开发/CI）。 | 标准技术栈 | River 通知在开发模式下可能不可用（SQLite）。但阶段 2 已建立"开发模式不强制"模式。 |
| A8 | go-mail `wneessen/go-mail` SMTP STARTTLS 与 Postfix/Exchange 兼容。 | 标准技术栈 | 如果客户使用其他 MTA 有现代 SASL 不兼容，需要回退到纯 LOGIN；但 go-mail 支持。 |
| A9 | 阶段 5 不引入新进程（协调器可以与调度器在同一进程中运行）—遵循"单二进制"约束。 | 架构模式 | 如果协调器需要长时间 IO（Snowflake List）阻塞调度器 tick，需要单独 goroutine（不是单独进程）。 |
| A10 | `snowflakedb/gosnowflake` v1.19.1 `ExecContext` 尊重 DDL 上的 ctx.Cancel（包括 CREATE MASKING POLICY）。 | 代码示例 §2 | 历史问题 #767（v1.6.x）有报告 ctx 不被尊重；v1.19.1 应该已修复—但计划 05-02 应在测试中验证。 |
| A11 | BigQuery PolicyTag IAM 传播窗口 ≤ 5 分钟，协调循环 15 分钟可以捕获。 | 常见陷阱 #4 | 如果实际 > 5 分钟 ⇒ 协调器报告假漂移。**建议：** 协调器在推送后添加"5 分钟宽限期"逻辑。 |

---

## 开放问题

1. **审查者重新分配 CLI（陷阱 #12）必要性**
   - 我们知道的：D-09 审查者池在提交时快照；审查者辞职是真实风险。
   - 不清楚的：阶段 5 v1 需要？还是 v1.x（v1 仅所有者通知）？
   - 建议：计划 05-04 添加 `./platform governance reassign <id> <new-reviewer>`—一个 SQL 更新 + 审计记录，低成本，避免运营盲点。

2. **Snowflake / BigQuery 认证方法选择**
   - 我们知道的：gosnowflake 支持密码 / 密钥对 / OAuth；BigQuery 支持 ADC / SA JSON / 工作负载身份。
   - 不清楚的：阶段 5 v1 默认推荐？
   - 建议：Snowflake **密钥对**（PAT 驱动 + 易于轮换）；BigQuery **工作负载身份**（如果部署在 GKE 上）或 **SA JSON**（其他）。记录两者 + 启动配置支持两者。

3. **`policies.yaml` 和 `notifications.yaml` 重载语义**
   - 我们知道的：两个 YAML 文件都影响实时治理决策。
   - 不清楚的：文件更改后多长时间生效？SIGHUP / fsnotify / 仅重启？
   - 建议：v1 使用 SIGHUP 触发重载（简单 + 可监控）；fsnotify 推迟到 v1.x。

4. **审批中途审查者辞职 / 角色删除时 Quorum=All 处理**
   - 我们知道的：D-09 快照已建立；审查者辞职可能阻塞。
   - 不清楚的：应该"快照审查者已辞职"自动回退？
   - 建议：v1 不自动回退—通过重新分配 CLI 处理（见 #1）。审计可追溯性优于自动化。

5. **Casbin Watcher 需要哪种部署形式？**
   - 我们知道的：单进程 v1 不需要；多进程（例如独立 worker + 调度器）需要。
   - 不清楚的：阶段 5 v1 部署形式——是 1 个平台进程 + N 个调度器/worker 进程吗？
   - 建议：检查阶段 2/3 worker 设计——如果 worker 进程也运行 chi REST，需要 watcher 进行 RBAC 同步。**建议：** 计划 05-01 添加任务"评估 worker 是否需要执行器 + watcher"。

---

## 环境可用性

> 阶段 5 主要影响代码和数据库结构；外部依赖通过测试夹具提供，生产环境由部署者提供。

| 依赖 | 需要者 | 可用 | 版本 | 回退 |
|-------------|------------|-----------|---------|----------|
| PostgreSQL 16+ | 元数据存储；audit 模式；Casbin 策略 | ✓（已在阶段 1+ 使用，testcontainers/postgres v0.42.0 已在 go.mod） | 16+ | — |
| Snowflake 账户（测试） | Snowflake MaskingProvisioner 集成测试 | ✗（无生产 Snowflake；无本地模拟器） | — | **使用 mock**—通过 sqlmock + DDL String 断言测试 ApplyMaskingPolicy；E2E 真实测试推迟到部署后人工验证。 |
| BigQuery 账户（测试） | BigQuery MaskingProvisioner 集成测试 | ✓（`goccy/bigquery-emulator` v0.6.6 已在 go.mod） | v0.6.6 | bigquery-emulator 不支持 PolicyTag 操作 → MaskingProvisioner 测试需要 Cloud Functions 或 mock；E2E 推迟到人工验证 |
| SMTP 服务器（测试） | 通知 SMTP 渠道 | ✓（可以启动 testcontainers/MailHog） | — | go-mail 有 mock Sender 接口 |
| HTTP 服务器（测试 webhook） | 通知 webhook | ✓（标准库 `httptest.Server`） | — | — |
| `riverqueue/river` 工具链 | 迁移（River 有自己的模式） | 可通过 `go install github.com/riverqueue/river/cmd/river@latest` 获取 | v0.35.1 | River 自己的 SQL 迁移也可以嵌入 phase5 主迁移中 |

**缺失依赖及回退：**
- Snowflake / BigQuery 真实账户 → mock + 模拟器 + 人工 UAT 阶段验证

**无回退的缺失依赖：**
- 无

---

## 验证架构

### 测试框架
| 属性 | 值 |
|----------|---|
| 框架 | Go 标准库 `testing` + `stretchr/testify` v1.11.1 + `testcontainers-go/modules/postgres` v0.42.0（现有） |
| 配置文件 | 无独立测试配置；使用 `internal/storage/store_test.go` 模式 |
| 快速运行命令 | `go test -short ./internal/{audit,policy,governance,quality,notification}/... -count=1` |
| 完整套件命令 | `go test ./... -count=1 -timeout=10m`（包括 testcontainers postgres + bigquery-emulator） |
| 阶段 E2E 命令 | `go test -tags=e2e ./internal/runtime/... -count=1`（现有阶段 4 模式） |

### 阶段需求 → 测试映射

| 需求 ID | 行为 | 测试类型 | 自动命令 | 文件存在？ |
|--------|----------|-----------|-------------------|---------------|
| RBAC-01 | 创建角色发送 `role.created` audit_log 条目 | 单元 + 集成 | `go test ./internal/auth/... -run TestCreateRole_AuditEntry -count=1` | ❌ Wave 0 |
| RBAC-02 | 用户 → 角色分配持久化 + 审计 | 集成 | `go test ./internal/auth/... -run TestAssignRole_End2End` | ❌ Wave 0 |
| RBAC-03 | column_policy CRUD + 三层解析正确 | 单元 | `go test ./internal/policy/... -run TestColumnPolicy_Resolution` | ❌ Wave 0 |
| RBAC-04（Snowflake） | ApplyMaskingPolicy 生成 DDL 字符串 | 单元（sqlmock） | `go test ./internal/connector/firstparty/snowflake/... -run TestMasking_ApplyDDL` | ❌ Wave 0 |
| RBAC-04（BigQuery） | ApplyMaskingPolicy 调用 PolicyTagManagerClient | 单元（mock 客户端） | `go test ./internal/connector/firstparty/bigquery/... -run TestMasking_PolicyTag` | ❌ Wave 0 |
| RBAC-04（协调） | 漂移检测 → 发送 `masking.sync_drift_detected` | 集成 | `go test ./internal/policy/... -run TestReconcile_Drift` | ❌ Wave 0 |
| RBAC-05（管道内） | AssetIO.Write 应用掩码函数 | 单元 | `go test ./internal/policy/... -run TestMaskApply_Hash_Redact_Partial` | ❌ Wave 0 |
| RBAC-06（哈希链） | 写入 → 验证通过 | 单元 + 集成 | `go test ./internal/audit/... -run TestWriteEntry_VerifyChain` | ❌ Wave 0 |
| RBAC-06（篡改） | 模拟篡改 1 行 → 验证检测到不匹配 seq | 集成 | `go test ./internal/audit/... -run TestVerify_DetectsTamper` | ❌ Wave 0 |
| RBAC-06（RLS） | platform_app UPDATE/DELETE 被 RLS 拒绝 | 集成 | `go test ./internal/audit/... -run TestRLS_RejectsUpdate` | ❌ Wave 0 |
| GOV-01/02/03 | 提交 → 审查 → 状态转换 + 评论强制拒绝 | 集成 | `go test ./internal/governance/... -run TestWorkflow_HappyPath` | ❌ Wave 0 |
| GOV-01（自动批准） | 自动批准通过条件 → state=active | 单元 | `go test ./internal/governance/... -run TestAutoApproval_AllPass` | ❌ Wave 0 |
| GOV-01（自动批准阻止） | PII 列存在 → 进入人工 | 单元 | `go test ./internal/governance/... -run TestAutoApproval_PIIBlocks` | ❌ Wave 0 |
| GOV-02（审查者池） | 三源解析正确 | 单元 | `go test ./internal/governance/... -run TestReviewerPool_ThreeSource` | ❌ Wave 0 |
| GOV-04（通知） | 决定 → 提交者 SMTP 邮件 | 集成（MailHog） | `go test ./internal/notification/... -run TestSMTP_DispatchOnDecision` | ❌ Wave 0 |
| GOV-05（审计跟踪） | 决定写入 audit_log + 哈希链 | 集成 | `go test ./internal/governance/... -run TestDecision_AuditEntry` | ❌ Wave 0 |
| GOV-06（导出） | 流式 JSONL/CSV 导出 + 包含 seq + self_hash | 集成 | `go test ./internal/audit/... -run TestExport_Streaming_VerifiableLines` | ❌ Wave 0 |
| GOV-07（TTL 模式） | expires_at 列写入 + 索引存在 | 集成 | `go test ./internal/audit/... -run TestExpiresAt_SchemaPresence` | ❌ Wave 0 |
| QUAL-01 | NullCheck/RangeCheck/SQLAssertion 评估 | 单元 | `go test ./internal/quality/... -run TestRules_Evaluate` | ❌ Wave 0 |
| QUAL-02/03 | materialize 后 run_quality_status 正确 | 集成 | `go test ./internal/runtime/... -run TestExecutor_QualityHook` | ❌ Wave 0 |
| QUAL-04 | 调度器 tick 检测 SLA 破坏 | 集成 | `go test ./internal/scheduler/... -run TestFreshnessSLA_Breach` | ❌ Wave 0 |
| QUAL-05（webhook） | 质量失败 → River 分发 webhook + HMAC 签名 | 集成（httptest） | `go test ./internal/notification/... -run TestWebhook_HMACSigned` | ❌ Wave 0 |
| QUAL-05（重放安全） | webhook 重发 ID 一致 | 集成 | `go test ./internal/notification/... -run TestWebhook_IdempotentID` | ❌ Wave 0 |

### 采样率
- **每次任务提交：** `go test ./internal/<changed-package>/... -count=1 -short -timeout=2m`
- **每次 wave 合并：** `go test ./... -count=1 -timeout=10m`（阶段 4 完整套件）
- **阶段门控：** 完整套件绿色 + 针对真实 Snowflake/BigQuery 账户的人工 UAT（人工，记录到 05-HUMAN-UAT.md，阶段 4 模式）

### Wave 0 差距（计划 05-01 必须首先建立）
- [ ] `internal/audit/writertest/fixtures.go` — 哨兵行 + 规范 JSON 测试支持
- [ ] `internal/policy/policytest/fixtures.go` — column_policy 三层解析测试支持
- [ ] `internal/governance/governancetest/fixtures.go` — 审查者池 + 自动批准测试支持
- [ ] `internal/quality/qualitytest/fixtures.go` — 三种规则类型的 mock 评估器
- [ ] `internal/notification/notificationtest/{webhook_server,smtp_server}.go` — httptest + MailHog 钩子
- [ ] `internal/connector/firstparty/snowflake/maskingtest/sqlmock_assertions.go` — DDL 字符串断言
- [ ] `internal/connector/firstparty/bigquery/maskingtest/mock_client.go` — PolicyTagManagerClient mock
- [ ] testcontainers 辅助函数扩展：启动 Postgres 时自动应用 phase5 迁移
- [ ] River 测试辅助函数：内存驱动 / pgx + River 快捷方式方便单元测试

---

## 安全领域

### 适用的 ASVS 类别

| ASVS 类别 | 适用 | 标准控制 |
|---------------|---------|-----------------|
| V2 身份验证 | 是 | golang-jwt/v5 + Casbin（已在阶段 1 + 此阶段） |
| V3 会话管理 | 是 | JWT TTL + 令牌过期 audit_log（阶段 1 D-09 + 此阶段） |
| V4 访问控制 | **核心** | Casbin RBAC（D-01）+ 列级策略（D-02）+ 仓库原生掩码（D-04）+ Casbin 策略表由 audit_migrator 拥有 |
| V5 输入验证 | 是 | chi 处理器输入 + ent 强类型 + column_policy PATCH body 的 JSON Schema |
| V6 密码学 | 是 | SHA-256 哈希链（D-13）；HMAC-SHA256 webhook 签名；RFC 8785 JCS 序列化；SMTP STARTTLS 的 TLS |
| V7 错误处理和日志 | 是 | RLS-不可变 audit_log + 结构化 slog 不泄露密钥 |
| V9 数据保护 | **核心** | 列级掩码（v1 Hash/Redact/Partial）+ 仓库原生 DDM/CLS（陷阱 #8）+ 仓库连接的 TLS |
| V13 API 安全 | 是 | chi 中间件强制 Casbin + RFC 7807 problem+json 已存在 |

### {阶段 5 技术栈} 的已知威胁模式

| 模式 | STRIDE | 标准缓解 |
|---------|--------|--------------------|
| audit_log 删除（DBA 入侵/配置错误） | Tampering | RLS + 独立模式 + audit_migrator 角色（陷阱 #5） |
| audit_log 修改（相同） | Tampering | 哈希链 + 顺序扫描验证 CLI（D-15） |
| Webhook 重放攻击 | Tampering | HMAC + 时间戳 + Webhook-ID 头（陷阱 #8/9） |
| 计时攻击 HMAC 比较 | Tampering | crypto/subtle.ConstantTimeCompare（陷阱 #7） |
| Snowflake 密钥对泄露 → DDM 任意修改 | Tampering | 连接器服务账户独立权限（仅 APPLY MASKING POLICY）+ 定期轮换 |
| BigQuery SA JSON 泄露 → IAM 修改 | Tampering | 工作负载身份（无 JSON）/ 限制 SA 仅 fineGrainedReader.setIamPolicy |
| 列策略绕过通过查询代理 | Information Disclosure | 陷阱 #8：仓库原生 DDM/CLS 不是代理（D-04 决策） |
| PII 传播竞态 | Information Disclosure | 相同事务（D-06）— 无窗口 |
| Casbin 策略表入侵 → 任意角色分配 | EoP | 表所有权 audit_migrator + 操作发送 `role.assigned` audit_log + 哈希链 |
| 批准 SLA 雪崩（审查者辞职） | DoS-on-governance | 通知所有者 + 重新分配 CLI（建议 #1） |
| 哈希链哨兵行锁竞争 | DoS | 窄范围（D-14）— 治理事件低频 |
| 通知渠道垃圾邮件（外部 webhook 配置错误） | DoS-amplification | River 退避 + 死信；notifications.yaml 路由严格匹配 |
| 物化绕过治理门控 | EoP | governance.gating_enabled=true（生产必需）+ 执行器检查（D-08） |
| 规范 JSON 漂移 → 哈希失效 | Tampering（误报） | RFC 8785 JCS（gowebpki/jcs）+ 跨实现一致性测试（A6） |

---

## 来源

### 主要（高置信度）
- **Casbin v2 RBAC 文档：** https://casbin.apache.org/docs/rbac — RBAC model.conf 规范结构（用于 D-01）
- **Casbin Postgres 适配器（官方）：** https://github.com/casbin/casbin-pg-adapter — 验证 v1.5.0 使用 go-pg/v9
- **Casbin pgx 适配器（推荐）：** https://github.com/pckhoi/casbin-pgx-adapter — 验证 v3.x 使用 pgx/v5
- **Casbin Postgres 监视器：** https://github.com/IguteChung/casbin-psql-watcher — Postgres LISTEN/NOTIFY 跨进程同步
- **Snowflake 掩码策略（CREATE）：** https://docs.snowflake.com/en/sql-reference/sql/create-masking-policy
- **Snowflake 掩码策略（ALTER）：** https://docs.snowflake.com/en/sql-reference/sql/alter-masking-policy — 验证"下次查询生效"
- **Snowflake 掩码策略（Privileges）：** https://docs.snowflake.com/en/sql-reference/sql/create-masking-policy — APPLY/OWNERSHIP 权限
- **BigQuery 列级安全：** https://docs.cloud.google.com/bigquery/docs/column-level-security-intro — 策略标签 + 数据目录关系 + 限制
- **BigQuery Data Catalog（Go）：** https://docs.cloud.google.com/data-catalog/docs/samples/data-catalog-ptm-create-taxonomy — Go taxonomy 创建示例
- **River 队列：** https://riverqueue.com/docs/job-retries + https://pkg.go.dev/github.com/riverqueue/river — 验证 v0.35.1（2026-04-26）
- **PostgreSQL 审计触发器 Wiki：** https://wiki.postgresql.org/wiki/Audit_trigger — 追加只读审计实现模式
- **防篡改审计（AppMaster）：** https://appmaster.io/blog/tamper-evident-audit-trails-postgresql — 哈希链接实现
- **RFC 8785 JCS：** https://www.rfc-editor.org/rfc/rfc8785 — JSON 规范化方案规范
- **gowebpki/jcs Go 实现：** https://github.com/gowebpki/jcs
- **`internal/auth/middleware.go` 和 `internal/runtime/executor.go`** — 现有项目代码（阶段 1/4）— 用于扩展点位置

### 次要（中置信度）
- **webhooks.fyi（安全）：** https://webhooks.fyi/security/replay-prevention + https://webhooks.fyi/security/hmac — 行业最佳实践
- **Hookdeck SHA256 webhook 验证：** https://hookdeck.com/webhooks/guides/how-to-implement-sha256-webhook-signature-verification
- **Mailtrap go-mail 教程（2026）：** https://mailtrap.io/blog/golang-send-email/ — go-mail 优于 gomail 的原因
- **wneessen/go-mail：** https://github.com/wneessen/go-mail
- **Bytebase Postgres 审计日志指南：** https://www.bytebase.com/blog/postgres-audit-logging/
- **Hrekov Casbin RBAC vs 层次结构：** https://hrekov.com/blog/casbin-rbac-vs-casbin-rbac-hierarchical — 角色继承边缘情况
- **Snowflake gosnowflake 驱动：** https://github.com/snowflakedb/gosnowflake — v1.19.1 已验证

### 第三（低置信度 — 标记需验证）
- BigQuery PolicyTag IAM 传播窗口具体值 30 秒-5 分钟—多个社区博客交叉引用，但官方文档未明确说明窗口（A11 假设）
- gosnowflake v1.19.1 ctx.Cancel 正确取消 DDL—v1.6.x 历史问题有报告，v1.19 应该已修复但未在搜索中明确确认（A10 假设）

### 验证的版本（2026-05-09 通过 `go list -m -versions`）
- `github.com/casbin/casbin/v2`：v2.135.0 是最新发布（2025-12 系列）
- `github.com/riverqueue/river`：v0.35.1（2026-04-26）
- `github.com/casbin/casbin-pg-adapter`：v1.5.0（最新）
- `github.com/pckhoi/casbin-pgx-adapter/v3`：v3.2.0
- `github.com/snowflakedb/gosnowflake`：v1.19.1（匹配项目 go.mod）
- `cloud.google.com/go/datacatalog`：v1.31.0（最新）

---

## 元数据

**置信度分解：**
- 标准技术栈：**高** — 库版本全部通过 `go list -m -versions` 和 Go 模块代理验证；CLAUDE.md 已锁定大多数库
- 架构：**高** — 完全延续阶段 1-4 既定模式（三层、双 ORM、可选连接器能力、时间表、RLS-不可变性、River 异步）
- Snowflake DDM：**高** — 官方文档双重验证 DDL 形状；ALTER 时机；模式作用域约束；权限模型
- BigQuery CLS：**中** — 策略标签 + Taxonomy 模型高，IAM 传播窗口具体值低（社区数据）
- 哈希链 + 规范 JSON：**高** — 陷阱 #5 + RFC 8785 + gowebpki/jcs 三重验证
- 治理工作流：**高** — D-08..D-12 直接对应阶段 4 D-03/D-17/D-19 模式
- 质量规则：**高** — 相同事务 D-19 与阶段 4 D-04 理念一致；连接器能力模式已建立
- 通知系统：**中** — webhook + SMTP 模式标准；模板语法 + 路由格式仍有 Claude 决定权空间
- 常见陷阱：**高** — 12 个陷阱全部有官方文档或 PITFALLS.md 引用

**研究日期：** 2026-05-09
**有效期：** 2026-06-09（30 天；技术栈和 API 稳定）。如果 Snowflake/BigQuery API 有任何重大更新（罕见）— 需要重新验证 §4.2/§4.3 代码示例。
