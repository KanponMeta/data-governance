# Phase 5: 治理引擎 - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-05-09
**Phase:** 05-governance
**Areas discussed:** Audit hash chain & log architecture; RBAC + column policy + warehouse masking + PII propagation; Governance workflow + auto pre-approval; Quality rule DSL + evaluation hook + alert delivery

---

## Gray Area Selection

**Question:** Phase 5 范围很大（RBAC + 治理工作流 + 质量规则 + 审计哈希链）。要讨论哪些灰色区？(可多选)

| Option | Description | Selected |
|--------|-------------|----------|
| 审计哈希链与日志架构 | (必须最先决定 — Pitfall #5) 表归属、哈希链范围、tamper 验证、外部锚定、retention TTL | ✓ |
| RBAC 列级策略 + 仓库掩码同步 + PII 传播 | (RBAC-01..06 + Pitfall #8 + #15) Casbin model、列策略 DSL、Snowflake DDM/BigQuery CLS 同步、PII 传播时机 | ✓ |
| 治理工作流状态机与自动预审批 | (GOV-01..05 + Pitfall #7) 状态机、reviewer 分派、auto pre-approval、SLA 升级 | ✓ |
| 质量规则 DSL + 评估钩子 + 告警通道 | (QUAL-01..05 + GOV-04) DSL 形态、评估时机、SLA、webhook/email | ✓ |

---

## Area 1: 审计哈希链与日志架构

### Q1.1: Audit hash-chain 写入哪张表？（与现有 event_log 的关系）

| Option | Description | Selected |
|--------|-------------|----------|
| 独立 audit_log 表 | 新建独立 audit_log，含哈希链列 (seq, prev_hash, self_hash, prev_seq)。event_log 不变。Pitfall #5 "单独的表、单独的 Schema、单独的备份策略"。 | ✓ |
| 扩展现有 event_log 表 | 给 event_log 加 seq/prev_hash/self_hash 列。代价：全部事件参与哈希计算，串行化拖累 run 吞吐量。 | |
| 双写 (event_log + audit_log) | 治理事件同时写两张表。代价：重复存储，增加路径复杂度。 | |

**User's choice:** 独立 audit_log 表
**Notes:** Pitfall #5 explicit guidance + protects event_log hot path from hash-chain serialization.

### Q1.2: audit_log 的哈希链覆盖哪些操作？

| Option | Description | Selected |
|--------|-------------|----------|
| 仅治理 + 访问控制操作 | policy.changed, role.assigned/revoked, governance.{submitted,approved,rejected}, audit.exported, masking.sync_failed。范围窄，符合 SOC2/GDPR 关注人类决策。 | ✓ |
| 所有 actor-driven 操作 | 加入 metadata.updated、schema.break_acknowledged 等人为调整。 | |
| 全部事件 | 连 run.step.succeeded 都进哈希链。完备但过重；Pitfall #5 仅为"访问事件"设计。 | |

**User's choice:** 仅治理 + 访问控制操作
**Notes:** Narrow scope keeps chain serialization off the run hot path while still covering SOC2/GDPR attestable events.

### Q1.3: 防篡改验证机制 + 审计日志导出接口如何提供？

| Option | Description | Selected |
|--------|-------------|----------|
| CLI verify + REST 导出 | ./platform audit verify [--from=<seq>] + ./platform audit export + GET /audit/export 流式 (JSONL/CSV)。无后台 reconciler。Phase 4 D-19 三层包装同一 library。 | ✓ |
| CLI + 后台 reconciler + REST 导出 | 双保险：reconciler 默认 5 分钟扫描；CLI 供人工/CI 调用。 | |
| 仅 CLI | 最简，无 REST 流式导出。 | |

**User's choice:** CLI verify + REST 导出
**Notes:** On-demand verify satisfies SOC2 demos; background reconciler deferred as v1.x belt-and-suspenders.

### Q1.4: GOV-07 保留策略 (TTL) 在 v1 的范围？

| Option | Description | Selected |
|--------|-------------|----------|
| v1 仅实现 audit_log 保留接口 | audit_log retention TTL 可配，asset 数据 TTL 推迟。合规场景十年保留是主流。 | ✓ |
| v1 同时实现资产+审计日志保留 | 完整 GOV-07。范围较大，~1 plan 工作量。 | |
| v1 均不实现 | 接口预留，全部推 v1.x。 | |

**User's choice:** v1 仅实现 audit_log 保留接口
**Notes:** Compromise — audit retention is compliance-critical, asset data retention is per-connector + lineage-aware complexity that doesn't fit v1.

---

## Area 2: RBAC 列级策略 + 仓库掩码同步 + PII 传播

### Q2.1: Casbin 权限模型与列策略表之间的分工？

| Option | Description | Selected |
|--------|-------------|----------|
| Casbin RBAC 负责角色，column_policy 表独立 | Casbin model p={sub,obj,act}, obj=资源路径。列级掩码独立 column_policies 表，由平台同步到仓库（Casbin 范围之外）。 | ✓ |
| Casbin RBAC+ABAC 统一模型 | p={sub, obj, act, attrs} 含 column 属性；Casbin enforcer 授权列级访问。代价：Casbin 不能直接驱动仓库掩码同步。 | |
| 全自研 RBAC + column_policy | 不引入 Casbin。CLAUDE.md 选 Casbin 是首选。不推荐。 | |

**User's choice:** Casbin RBAC 负责角色，column_policy 表独立
**Notes:** Avoids impedance mismatch between Casbin enforcer and warehouse SQL; column_policy stays platform-owned for sync.

### Q2.2: 列策略在哪里、怎么表达？

| Option | Description | Selected |
|--------|-------------|----------|
| 代码级 DSL + 运行时 REST PATCH 两轨 | builder ColumnPolicy + REST PATCH /assets/<name>/columns/<col>/policy + global YAML for tag→mask defaults。运行时覆盖代码默认（同 P4 D-17）。 | ✓ |
| 仅 REST API 运行时配置 | 治理团队从 UI/REST 完全控制。代价：工程师需调用 API 才能设默认。 | |
| 仅 builder DSL | 全代码化，无运行时调整。代价：微调一个列要重部署。 | |

**User's choice:** 代码级 DSL + 运行时 REST PATCH 两轨
**Notes:** Mirrors Phase 4 D-17 metadata pattern — engineer defaults, governance overrides.

### Q2.3: 仓库掩码同步策略（RBAC-04）？

| Option | Description | Selected |
|--------|-------------|----------|
| Push-on-change + Reconcile loop | 策略变更后 River 任务调用 Snowflake DDM / BigQuery CLS API；后台 reconciler 默认 15 分钟拉取仓库现状对比 column_policies，发现漂移发 policy.sync_drift_detected。 | ✓ |
| 仅 reconcile loop | 简单实现；延迟较高。 | |
| 仅 push | 实时但漂移检测弱；同事务 RPC 处理仓库不可达/超时复杂。 | |

**User's choice:** Push-on-change + Reconcile loop 双保险
**Notes:** Pitfall #8 + STATE.md flag — needs API validation by researcher; River task isolates main path.

### Q2.4: PII tag 沿血缘传播、冲突解决与覆盖机制？

| Option | Description | Selected |
|--------|-------------|----------|
| 同步在 lineage capture 后 + union 冲突 + 显式覆盖 | executor 物化后 lineage_writer 写入 column_edges 后同事务跳发 propagator；上游任一列 PII=true 则下游继承（union）；override 通过 builder TagOverride 含必填 reason；metadata.tag_overridden 进 audit_log。 | ✓ |
| 同步传播 + intersection 冲突 | 所有上游都 PII 才传播。 | |
| 后台 reconciler 周期扫描 | 低优先级传播，延迟高，合规 race。 | |

**User's choice:** 同步在 lineage capture 后 + union 冲突 + 显式覆盖
**Notes:** Conservative default + audit-trail override + same-transaction guarantee = compliance-safe.

---

## Area 3: 治理工作流状态机与自动预审批

### Q3.1: 资产治理状态机存在哪里？

| Option | Description | Selected |
|--------|-------------|----------|
| asset_versions.governance_state 列 | P4 asset_versions 加 governance_state (Draft, InReview, Active, Rejected)，与 code_hash 绑定，每次代码变更新版本默认 Draft。 | ✓ |
| 独立 governance_requests 表 | 每次提交一个 request 行；与 asset_versions 解耦。 | |
| Asset 全局状态（不绑 version） | 简单但代码新版本被默认视为 Active → 治理漏护。 | |

**User's choice:** asset_versions.governance_state 列
**Notes:** Natural binding to code_hash; new code = new version = back to Draft.

### Q3.2: Reviewer 分派机制（谁来审批）？

| Option | Description | Selected |
|--------|-------------|----------|
| 代码 + tag 规则 + owner 三路 | (1) builder Reviewers; (2) global YAML 含 tag "pii" 默认需 "privacy-team"; (3) fallback 从 asset_metadata.owner 推 reviewer role。union 三路 + per-asset Quorum(N)。 | ✓ |
| 仅 global YAML 规则 | 治理团队全局配置 tag→reviewer。简洁但不灵活。 | |
| Round-robin governance team pool | 纯轮询 pool，不考虑 PII/合规上下文。 | |

**User's choice:** 代码 + tag 规则 + owner 三路
**Notes:** Mirrors Phase 4 D-17 three-source pattern; quorum=1 default for usability.

### Q3.3: 自动预审批策略（Pitfall #7："先于人工审批设计"）？

| Option | Description | Selected |
|--------|-------------|----------|
| 默认开启 auto-pre-approval check | 提交触发检查集：schema break 未 ack BLOCK；PII tag/policy 不一致 BLOCK；质量配置不全 BLOCK；通过且无 PII 无 breaking change 走 fast-path 自动批准。.RequireHumanReview() 可强制人工。 | ✓ |
| 全人工（无 auto-approval） | Pitfall #7 明说人工审批几乎总是成为瓶颈。 | |
| 默认自动过，仅高风险走人工 | 默认 auto-approve，仅 PII 资产或显式 RequireReview 走人工。代价：忘记打 PII 的资产逃避审查。 | |

**User's choice:** 默认开启 auto-pre-approval check
**Notes:** Auto-approval pipeline is OFF only when blocked by checks; engineering velocity preserved without compromising governance.

### Q3.4: 必填评论 + SLA 升级（Pitfall #7："计时器 + 升级路径"）？

| Option | Description | Selected |
|--------|-------------|----------|
| 拒绝必填 reason + SLA 提醒不自动升级 | Reject 必填 reason；超 N 小时（默认 48）发 governance.review_sla_breached 事件 + 通知 backup reviewer，但不自动推进状态。 | ✓ |
| 同 A + 超 SLA 自动升级到二级审批人 | 自动推进状态。代价：escalation pool 配置 + 状态机复杂。 | |
| 仅必填 reason + SLA 提醒事件 | 不发额外通知，依赖运维仪表盘。 | |

**User's choice:** 拒绝必填 reason + SLA 提醒不自动升级
**Notes:** SOC 2 requires human attestation; auto-approving stalled reviews breaks compliance posture.

---

## Area 4: 质量规则 DSL + 评估钩子 + 告警通道

### Q4.1: 质量规则 DSL 的形态？

| Option | Description | Selected |
|--------|-------------|----------|
| Builder chained 强类型规则 | asset.New("x").QualityRule(NullCheck{...}).QualityRule(RangeCheck{...}).QualityRule(SQLAssertion{...})。每种规则一个 typed struct 实现 QualityRule 接口。v1 仅交三种。 | ✓ |
| 单一 .QualityRules([]Rule{...}) 列表 | 一次传入。不太符合现有 chain 风格。 | |
| 独立 yaml 规则文件 | 治理团队从文件配置。代价：工程师在代码里看不到规则。 | |

**User's choice:** Builder chained 强类型规则
**Notes:** Mirrors Phase 2 D-04 builder pattern; 3 starter rule types cover QUAL-01.

### Q4.2: 质量规则评估时机 + 失败语义？

| Option | Description | Selected |
|--------|-------------|----------|
| 同事务在 lineage/schema 后 + 独立 run_quality_status 列 | executor.runStep 物化成功 → lineage → schema → quality_evaluator 同事务。Run 表加 run_quality_status (passed/failed/skipped)，run.state 仍是 succeeded。一致 P1 D-09 + P4 D-04 哲学。 | ✓ |
| 质量失败则 run 失败 | run.state=failed，走 retry。代价：质量轻微违例触发物化重试。 | |
| 异步 River job 评估 | run 立即完成，quality 后台异步。代价：UI 看到 run 完成但 quality pending；告警延迟。 | |

**User's choice:** 同事务在 lineage/schema 后 + 独立 run_quality_status 列
**Notes:** "Materialization succeeded but quality failed" is the right semantic; aligns with metadata-don't-block-data philosophy.

### Q4.3: QUAL-04 SLA 阈值（"在计划后 N 小时内物化"）在哪里评估？

| Option | Description | Selected |
|--------|-------------|----------|
| scheduler 扩展检查 | asset.FreshnessSLA{MaxLag} → scheduler tick 扫 last_succeeded_at + MaxLag < now → sla.breached → 告警。同 P3 scheduler 架构。 | ✓ |
| 独立 SLA daemon | 单独进程。代价：多一个 daemon。 | |
| SLA 作为质量规则的一种 | asset.SLACheck{...}。代价：物化后判定者错过运行未发生时的 SLA 违反。 | |

**User's choice:** scheduler 扩展检查
**Notes:** Only the scheduler knows what *should* have happened; quality rules only see what *did* happen.

### Q4.4: 告警分发通道（QUAL-05 + GOV-04）？

| Option | Description | Selected |
|--------|-------------|----------|
| Webhook + bring-your-own SMTP 两个内置 | webhook (POST JSON to URL) + SMTP-only (启动配置 SMTP host/port/credentials)。River job 异步派送重试。SES/SendGrid/Slack v1.x。 | ✓ |
| Webhook + SES + SendGrid + Slack | 一次交付多通道。代价：多外部 SDK 依赖。 | |
| 仅 webhook | 邮件 v1.x。代价：GOV-04 提交者收邮件可能不够。 | |

**User's choice:** Webhook + bring-your-own SMTP 两个内置
**Notes:** Aligns with single-binary v1 constraint; users can fan webhook → Slack/PagerDuty themselves.

---

## Claude's Discretion

Items not asked about in detail; planner / researcher decides during planning:
- Mask types v1 implementation details (Hash salt management, Partial reveal length defaults).
- Snowflake DDM API call shape — researcher must validate against current Snowflake API per STATE.md flag.
- BigQuery CLS via Data Catalog policy tag taxonomy — researcher validates current Cloud SDK + IAM binding shape.
- Casbin Postgres adapter concrete library choice (`casbin/casbin-pg-adapter` v3 vs `memwey/casbin-sqlx-adapter` vs in-house).
- Quorum semantics when reviewer pool changes during a review (snapshot at submit vs live recomputation).
- Audit-log export `jsonl` line schema versioning.
- River queue topology (separate `policy_sync` and `notification` queues vs single shared).
- `notifications.yaml` template variable language.
- Reviewer-pool snapshot persistence shape (denormalized JSON vs separate join table).
- column_policies partition_key support — defer to v1.x; v1 is asset-level.
- Audit verify CLI output format default (table vs JSON).

## Deferred Ideas

Captured to prevent scope creep into Phase 5; tracked in CONTEXT.md `<deferred>`:
- Asset-data retention TTL execution (v1.x; schema-only in v1).
- External hash anchoring (S3 Object Lock) — interface stub only in v1.
- Background tamper-detection reconciler — v1.x.
- Auto SLA escalation — v1.x.
- Additional mask types (Tokenization, FPE, bucketing).
- Additional quality rules (UniqueCheck, RegexCheck, custom predicates).
- Additional notification channels (Slack, SES, SendGrid, MS Teams, PagerDuty events API).
- PII auto-classification (AGOV-02) — v2.
- Row-level security (AGOV-01) — v2.
- SSO/OIDC (PLAT-02) — v2.
- Custom Casbin model files for non-RBAC needs.
- Audit verify checkpoint summaries for read performance.
- Structured rejection reason taxonomy.
- OpenLineage-format audit export (out of scope).
- Column-policy partition awareness.
