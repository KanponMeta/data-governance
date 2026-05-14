---
phase: 5
slug: governance
status: ready
nyquist_compliant: true
wave_0_complete: false
created: 2026-05-09
updated: 2026-05-09
---

# Phase 5 — 验证策略

> 每个阶段的验证合同，用于执行期间的反馈采样。

---

## 测试基础设施

| 属性 | 值 |
|------|-----|
| **框架** | go test (stdlib) + testcontainers-go（Postgres）+ httptest.Server（webhook）+ sqlmock（Snowflake DDL）|
| **Wave 0 安装** | `internal/governance/testharness/` 包在 plan 05-01 task 0 创建 |
| **快速运行命令** | `go test ./internal/{audit,policy,governance,quality,notification,auth}/... -short -timeout 60s` |
| **完整套件命令** | `go test ./... -timeout 5m` |
| **竞态感知** | `go test -race ./internal/asset/... ./internal/audit/... ./internal/policy/...`（装饰器并发）|
| **预计运行时长** | quick ~25s; full ~3.5m（testcontainers Postgres + warehouse mocks）|

---

## 采样率

- **每个 task commit 后：** 运行 `go test ./internal/<changed-package>/... -short -timeout 60s`。
- **每个 plan wave 后：** 运行 `go test ./... -timeout 5m`。在 Wave 1 plan 05-01（审计链并发写入）和 Wave 3 plan 05-03（MaskingIO 并发 Write）上进行竞态测试。
- **在 `/gsd-verify-work` 之前：**
  - 完整套件绿色
  - `go test ./internal/audit/... -run TestVerify_DetectsTamper -count=1` 必须通过
  - `go test ./internal/runtime/... -run "TestExecutor_Quality_FailingNullCheck_SetsFailed_RunStateStillSucceeded" -count=1` 必须通过
  - `go test ./internal/governance/... -run "TestPropagate_OverrideEmitsAuditOnce" -count=1` 必须通过
- **最大反馈延迟：** 快速运行 60 秒。

---

## 每任务验证地图

> 跨所有 5 个 plan 的每个 task 一行。每个需求（RBAC-01..06, GOV-01..07, QUAL-01..05）至少出现在一行中。状态 `❌ W0` = 文件由 Wave 0 创建（plan 05-01 task 0）；`❌` = 文件尚不存在；`⬜` = 待处理；`✅` = 绿色。

| Task ID | Plan | Wave | Requirement(s) | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|----------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 5-01-00 | 01 | 0 | (test infra) | T-05-01-* | testharness package + Postgres testcontainer + Casbin/Snowflake/BigQuery/webhook fixtures boot | unit | `go test ./internal/governance/testharness/... -run TestPostgresContainer -short -timeout 60s` | ❌ W0 | ⬜ pending |
| 5-01-01 | 01 | 1 | RBAC-06, GOV-05, GOV-06, GOV-07 | T-05-01-01,02,03,04,06,08,09,10,14 | hash-chain audit log RLS-protected; sentinel-row serialised writes; tamper detection; streaming export with recursive audit.exported entry | unit + integration | `go test ./internal/audit/... -run "TestWriteEntry_HappyPath\|TestWriteEntry_Concurrent\|TestWriteEntry_RLSDeniesUpdate\|TestVerify_HappyPath\|TestVerify_DetectsTamper\|TestVerify_EmitsAuditVerifyFailed\|TestExport_JSONL_StreamingRecursiveAuditEntry\|TestCanonicalJSON_DeterministicAcross100Iterations" -count=1` | ❌ | ⬜ pending |
| 5-01-01b | 01 | 1 | GOV-06 (CLI) | T-05-01-06 | CLI verify exits 0 on intact, 2 on tamper; CLI export streams JSONL with seq+self_hash | integration | `go test ./cmd/platform/... -run "TestAuditVerifyCmd_HappyPath\|TestAuditVerifyCmd_Tamper\|TestAuditExportCmd_JSONL" -count=1` | ❌ | ⬜ pending |
| 5-01-02 | 01 | 1 | RBAC-01, RBAC-02 | T-05-01-04,05,11,12 | Casbin RBAC enforcer + roles/role_assignments tables; AssignRole writes role.assigned audit AND casbin_rule row in same tx | unit + integration | `go test ./internal/auth/... -run "TestCreateRole_AuditEntry\|TestAssignRole_End2End\|TestAssignRole_AuditFailureRollsBack\|TestRolesForUser_RespectsRevoked\|TestRequirePermission_AllowsByRole" -count=1` | ❌ | ⬜ pending |
| 5-01-02b | 01 | 1 | RBAC-01, RBAC-02 (REST + CLI) | T-05-01-05,11 | REST + CLI surfaces wrap auth.Service library | integration | `go test ./internal/api/... -run "TestRoleHandler\|TestCreateRoleHandler\|TestAssignRoleHandler\|TestRevokeRoleHandler" -count=1 && go test ./cmd/platform/... -run "TestRole" -count=1` | ❌ | ⬜ pending |
| 5-02-01 | 02 | 2 | RBAC-03 | T-05-02-01,02,10 | column_policies temporal table + 3-source DSL/REST/YAML + Resolve precedence; PATCH writes policy.changed audit + River enqueue | unit + integration | `go test ./internal/policy/... -run "TestStore_Apply_Idempotent\|TestStore_Apply_SoftRetiresRemoved\|TestStore_Patch_RuntimeOverridesBuilder\|TestStore_Patch_WritesAuditEntry\|TestStore_Patch_RequiresReason\|TestStore_Resolve_PrecedenceOrder" -count=1` | ❌ | ⬜ pending |
| 5-02-01b | 02 | 2 | RBAC-03 (DSL + REST + CLI) | T-05-02-01 | Builder ColumnPolicy chain + REST PATCH + CLI dispatch | unit + integration | `go test ./internal/asset/... -run "TestBuilder_ColumnPolicy_Chainable\|TestBuilder_ColumnPolicy_DuplicateColumnFails\|TestBuilder_ColumnPolicy_AffectsCodeHash" -count=1 && go test ./internal/api/... -run "TestPatchPolicyHandler_201_RecordsAudit\|TestPatchPolicyHandler_400_OnMissingReason\|TestPatchPolicyHandler_403_OnInsufficientRole\|TestEffectiveHandler_ReturnsResolvedPrecedence" -count=1 && go test ./cmd/platform/... -run "TestPolicy" -count=1` | ❌ | ⬜ pending |
| 5-02-02 | 02 | 2 | RBAC-04 | T-05-02-02,03,04,05,06,07,12 | Snowflake ApplyMaskingPolicy generates fully-qualified DDL; BigQuery ApplyMaskingPolicy creates Taxonomy with FINE_GRAINED_ACCESS_CONTROL + PolicyTag + IAM + Tables.update; River sync worker; Reconciler 5min grace period | unit (sqlmock + mock client) + integration | `go test ./internal/connector/firstparty/snowflake/... -run "TestSnowflake_ApplyMaskingPolicy_Hash_DDL\|TestSnowflake_ApplyMaskingPolicy_Redact\|TestSnowflake_ApplyMaskingPolicy_Partial\|TestSnowflake_RemoveMaskingPolicy_UNSET_then_DROP\|TestSnowflake_ListMaskingPolicies_ParsesNamePrefix\|TestSnowflake_ApplyMaskingPolicy_ContextCancellation_Returns" -count=1` | ❌ | ⬜ pending |
| 5-02-02b | 02 | 2 | RBAC-04 (BigQuery) | T-05-02-04,06 | BigQuery PolicyTag + IAM bindings | unit | `go test ./internal/connector/firstparty/bigquery/... -run "TestBigQuery_ApplyMaskingPolicy_CreatesTaxonomyIfMissing\|TestBigQuery_ApplyMaskingPolicy_ReusesExistingTaxonomy\|TestBigQuery_ApplyMaskingPolicy_AttachesPolicyTagToColumn\|TestBigQuery_ApplyMaskingPolicy_BindsIAM_OnAllowRoles" -count=1` | ❌ | ⬜ pending |
| 5-02-02c | 02 | 2 | RBAC-04 (sync + reconcile) | T-05-02-02,07,12 | River sync job retries; permanent failure writes masking.sync_failed audit; Reconciler emits drift + re-pushes within grace window | integration | `go test ./internal/policy/... -run "TestPolicySyncWorker_AppliesAndUpdatesEnforcementMode\|TestPolicySyncWorker_NonProvisionerConnector_SetsInPipeline\|TestPolicySyncWorker_PermanentFailure_WritesAuditAndDispatchesAlert\|TestReconciler_NoDriftWhenAligned\|TestReconciler_DriftEmitsAuditAndReEnqueues\|TestReconciler_GracePeriodSkipsRecentChanges" -count=1 && go test ./cmd/platform/... -run "TestReconcilerCmd_OnceFlag_RunsSingleTick\|TestReconcilerCmd_HonoursContextCancel" -count=1` | ❌ | ⬜ pending |
| 5-03-01 | 03 | 3 | (extends RBAC-03 via PII propagation; supports RBAC-05 enforcement decision) | T-05-03-01,02,03,12 | PII propagator runs in lineage_writer's tx; union rule; TagOverride emits metadata.tag_overridden once per asset_version | unit + integration | `go test ./internal/governance/... -run "TestPropagate_UnionRule_AnyUpstreamPII\|TestPropagate_NoUpstreamPII_NoChange\|TestPropagate_OverrideStopsPropagation\|TestPropagate_OverrideEmitsAuditOnce\|TestPropagate_SameTxGuarantee\|TestPropagate_RespectsCanceledContext\|TestPropagate_MultipleUpstreamsUnion" -count=1` | ❌ | ⬜ pending |
| 5-03-01b | 03 | 3 | RBAC-03 (DSL) | T-05-03-08 | TagOverride DSL chained on ColumnBuilder; reason required | unit | `go test ./internal/asset/... -run "TestBuilder_TagOverride_HappyPath\|TestBuilder_TagOverride_MissingReasonFails\|TestBuilder_TagOverride_DuplicateColumnFails\|TestBuilder_TagOverride_NeitherRemoveNorAddFails" -count=1 && go test ./internal/lineage/... -run "TestCaptureRun_CallsPropagator_WhenSet\|TestCaptureRun_BackwardCompat_NilPropagator\|TestCaptureRun_PropagatorErrorRollsBackTx" -count=1` | ❌ | ⬜ pending |
| 5-03-02 | 03 | 3 | RBAC-05 | T-05-03-04,05,06,07,11 | Mask functions HMAC-SHA256+salt; MaskingIO decorator wraps non-warehouse AssetIO.Write; warehouse-native connector skips wrapping; pii fallback redact | unit + integration + race | `go test ./internal/policy/... -run "TestApplyHash_Deterministic\|TestApplyHash_DifferentValuesDifferentHashes\|TestApplyHash_RequiresSaltInProd\|TestApplyRedact_AlwaysReturnsThreeStars\|TestApplyPartial_ShortValue_FullyRedacted\|TestApplyPartial_LongValue_Reveal2\|TestApply_DispatchByMaskType\|TestStore_MaskRulesForAsset_OnlyInPipelineRows\|TestStore_MaskRulesForAsset_PIIWithoutPolicyFallsBackToRedact\|TestStore_MaskRulesForAsset_WarehouseNativeRowsExcluded" -count=1 && go test ./internal/asset/... -run "TestMaskingIO_NoRules_PassesThrough\|TestMaskingIO_HashesSSNColumn\|TestMaskingIO_RedactsEmail\|TestMaskingIO_PartialEmail_Reveals2\|TestMaskingIO_PreservesNonRuleColumns\|TestMaskingIO_ReadAndPartitionKey_PassThrough\|TestMaskingIO_Concurrent_Write" -race -count=1` | ❌ | ⬜ pending |
| 5-03-02b | 03 | 3 | RBAC-05 (executor wiring) | T-05-03-05,11 | Executor runStep wraps MaskingIO inside TrackingIO when conn is non-warehouse and asset has policies | integration | `go test ./internal/runtime/... -run "TestExecutor_NoPolicies_DoesNotWrapMaskingIO\|TestExecutor_WarehouseConnector_DoesNotWrapMaskingIO\|TestExecutor_NonWarehouseConnector_WithPolicies_WrapsMaskingIO\|TestExecutor_PIIWithoutPolicy_FallsBackToRedact" -count=1` | ❌ | ⬜ pending |
| 5-04-01 | 04 | 2 | GOV-01, GOV-02 | T-05-04-01,03,08,10 | governance_reviews schema + 3-source reviewer pool + 5-check auto-approval pipeline + builder DSL (Reviewers/Quorum/RequireHumanReview/EscalationRoles) | unit + integration | `go test ./internal/asset/... -run "TestBuilder_Reviewers_Accumulate\|TestBuilder_Quorum_DefaultIs1\|TestBuilder_RequireHumanReview_Toggles\|TestBuilder_EscalationRoles_Accumulate\|TestBuilder_GovernanceConfig_NotInCodeHash" -count=1 && go test ./internal/governance/... -run "TestResolveReviewers_BuilderOnly\|TestResolveReviewers_YamlTagRules\|TestResolveReviewers_OwnerFallback_OnlyWhenEmpty\|TestResolveReviewers_UnionOfBuilderAndYaml\|TestResolveReviewers_Dedups\|TestResolveReviewers_QuorumAllPreserved\|TestAutoApproval_AllPass_Approves\|TestAutoApproval_UnacknowledgedSchemaBreak_Blocks\|TestAutoApproval_PIIWithoutPolicy_Blocks\|TestAutoApproval_BrokenQualityRule_Blocks\|TestAutoApproval_LineageDriftPending_Blocks\|TestAutoApproval_PIIPresent_RequiresHuman\|TestAutoApproval_RequireHumanReview_ForcesHuman_EvenWhenAllPass" -count=1` | ❌ | ⬜ pending |
| 5-04-02 | 04 | 2 | GOV-01, GOV-02, GOV-03, GOV-04 | T-05-04-01,02,04,05,07,11,12,13 | Workflow Submit/Approve/Reject/Reassign in tx; reject requires comment; SLA scanner; executor materialization gate; CLI parity | integration | `go test ./internal/governance/... -run "TestWorkflow_Submit_AutoApprovedPath\|TestWorkflow_Submit_HumanReviewPath\|TestWorkflow_Submit_BlockedPath\|TestWorkflow_Approve_HappyPath\|TestWorkflow_Reject_RequiresComment\|TestWorkflow_Reject_HappyPath\|TestWorkflow_ResubmitAfterReject\|TestWorkflow_Reassign_RotatesReviewerPool\|TestWorkflow_Approve_QuorumAll_PartialDoesNotFlip\|TestSLAScanner_NoBreaches_WhenAllRecent\|TestSLAScanner_OneBreachAfterSLA\|TestSLAScanner_DoesNotReEmit\|TestSLAScanner_NotifiesReviewersAndOwner" -count=1` | ❌ | ⬜ pending |
| 5-04-02b | 04 | 2 | GOV-01..04 (REST + CLI) | T-05-04-04,07 | REST handlers mounted with RequirePermission; executor gate; CLI dispatchGovernance | integration | `go test ./internal/runtime/... -run "TestExecutor_GatingDisabled_AllowsRun_EvenWhenStateIsDraft\|TestExecutor_GatingEnabled_StateActive_AllowsRun\|TestExecutor_GatingEnabled_StateDraft_BlocksAndEmits\|TestExecutor_GatingEnabled_StateRejected_BlocksAndEmits" -count=1 && go test ./internal/governance/... -run "TestSubmitHandler_201_AutoApprovedPath\|TestSubmitHandler_201_HumanReviewPath\|TestApproveHandler_200_FlipsToActive\|TestRejectHandler_400_OnEmptyComment\|TestRejectHandler_200_FlipsToRejected\|TestReassignHandler_200\|TestStatusHandler_200\|TestSubmitHandler_403_OnInsufficientRole" -count=1 && go test ./cmd/platform/... -run "TestSubmitCmd_HappyPath\|TestReviewCmd_RejectRequiresComment\|TestStatusCmd\|TestReassignCmd_HappyPath" -count=1` | ❌ | ⬜ pending |
| 5-05-01 | 05 | 2 | QUAL-01, QUAL-02, QUAL-03 | T-05-05-07,08,09,14 | QualityRule DSL (NullCheck/RangeCheck/SQLAssertion) + connector.QueryAggregate + Postgres impl + executor commitSuccess hook + run_quality_status independent column | unit + integration | `go test ./internal/quality/... -run "TestNullCheck_Pass_WhenRateBelowThreshold\|TestNullCheck_Fail_WhenRateAboveThreshold\|TestNullCheck_Error_WhenQueryFails\|TestRangeCheck_PassesWithinBounds\|TestRangeCheck_FailsBelowMin\|TestRangeCheck_FailsAboveMax\|TestSQLAssertion_ScalarEqualsZero_Pass_When0\|TestSQLAssertion_ScalarLessThan_Fail_WhenAbove\|TestSQLAssertion_RowCountIsZero_Pass_WhenEmpty\|TestSQLAssertion_AssetSubstitution\|TestStore_Persist_HappyPath\|TestStore_History_OrderedDesc" -count=1 && go test ./internal/connector/firstparty/postgres/... -run "TestPostgres_QueryAggregate_HappyPath\|TestPostgres_QueryAggregate_ContextTimeout\|TestPostgres_QueryAggregate_NoRows\|TestPostgres_QueryAggregate_MultiColumn" -count=1` | ❌ | ⬜ pending |
| 5-05-01b | 05 | 2 | QUAL-01..03 (executor) | T-05-05-08,14 | commitSuccess invokes Evaluator after schema_writer; quality failure does NOT flip run.state | integration | `go test ./internal/asset/... -run "TestBuilder_QualityRule_Chainable\|TestBuilder_QualityRule_DuplicateNameFails\|TestBuilder_QualityRule_InCodeHash" -count=1 && go test ./internal/runtime/... -run "TestExecutor_Quality_NoRules_SetsSkipped\|TestExecutor_Quality_PassingNullCheck_SetsPassed\|TestExecutor_Quality_FailingNullCheck_SetsFailed_RunStateStillSucceeded\|TestExecutor_Quality_NonAggregateConnector_SetsError\|TestExecutor_Quality_FailureDoesNotRollbackLineage\|TestExecutor_Quality_RuleTimeout_SetsError" -count=1` | ❌ | ⬜ pending |
| 5-05-02 | 05 | 2 | QUAL-04, QUAL-05 | T-05-05-01,02,04,05,06,10,12,13 | FreshnessSLA scanner + notification subsystem (webhook+SMTP+River worker); HMAC signing; stable WebhookID; STARTTLS | unit + integration | `go test ./internal/asset/... -run "TestBuilder_FreshnessSLA_Stores\|TestBuilder_FreshnessSLA_RejectsZeroDuration\|TestBuilder_FreshnessSLA_NotInCodeHash" -count=1 && go test ./internal/quality/... -run "TestScanner_NoBreach_WhenLastSucceededRecent\|TestScanner_Breach_WhenStale\|TestScanner_NeverRun_BreachAfterCreatedAtPlusMaxLag\|TestScanner_DedupBy_FreshnessBreachEmittedAt\|TestScanner_EmitsSLABreachEvent_AndEnqueuesNotification" -count=1 && go test ./internal/schedule/... -run "TestDaemon_FreshnessScanner_Invoked\|TestDaemon_NoScanner_NoOp" -count=1` | ❌ | ⬜ pending |
| 5-05-02b | 05 | 2 | QUAL-05 (notification) | T-05-05-01,02,04,05,06,10,12 | Webhook HMAC + ID stable + SMTP STARTTLS + Router glob match + worker dispatched/dispatch_failed events | unit + integration | `go test ./internal/notification/... -run "TestWebhook_Send_HappyPath_200\|TestWebhook_Send_HMACSignatureCorrect\|TestWebhook_Send_500ReturnsError\|TestWebhook_Send_RespectsContextTimeout\|TestWebhook_Send_StableWebhookIDAcrossCalls\|TestWebhook_HMAC_ConstantTimeCompare\|TestSMTP_Send_HappyPath\|TestSMTP_Send_AuthFailure_ReturnsError\|TestSMTP_Send_RespectsTLSMandatory\|TestSMTP_Send_BuildsMultipartHTML\|TestWorker_DispatchesViaWebhookAndSMTP_OnMatch\|TestWorker_NoRuleMatch_NoOp\|TestWorker_PartialFailure_LogsAndRetries\|TestWorker_FinalFailureEmitsDispatchFailed\|TestWorker_StableWebhookIDAcrossRetries" -count=1` | ❌ | ⬜ pending |

*状态：⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

**需求覆盖矩阵：**

| Req ID | Plan | Task |
|--------|------|------|
| RBAC-01 | 05-01 | Task 2 (Casbin RBAC + roles) |
| RBAC-02 | 05-01 | Task 2 (role_assignments) |
| RBAC-03 | 05-02 | Task 1 (column_policies) |
| RBAC-04 | 05-02 | Task 2 (Snowflake DDM + BigQuery CLS sync) |
| RBAC-05 | 05-03 | Task 2 (in-pipeline MaskingIO) |
| RBAC-06 | 05-01 | Task 1 (hash chain audit_log) |
| GOV-01 | 05-04 | Task 2 (Submit) |
| GOV-02 | 05-04 | Task 1+2 (reviewer pool + notify) |
| GOV-03 | 05-04 | Task 2 (Approve/Reject + comment) |
| GOV-04 | 05-04 | Task 2 (notification dispatch on decision) |
| GOV-05 | 05-01 | Task 1 (audit_log writes for governance.* events; populated by 05-04) |
| GOV-06 | 05-01 | Task 1 (audit verify + export) |
| GOV-07 | 05-01 | Task 1 (audit_log.expires_at + audit_purge role; v1 schema only per D-16) |
| QUAL-01 | 05-05 | Task 1 (NullCheck/RangeCheck/SQLAssertion DSL) |
| QUAL-02 | 05-05 | Task 1 (executor commitSuccess hook) |
| QUAL-03 | 05-05 | Task 1 (run_quality_status + quality_results) |
| QUAL-04 | 05-05 | Task 2 (FreshnessSLA scanner) |
| QUAL-05 | 05-05 | Task 2 (notification subsystem) |

---

## Wave 0 需求

- [ ] `internal/governance/testharness/postgres.go` — testcontainers Postgres helper，应用于所有 phase 1–5 迁移（audit schema, RLS roles）
- [ ] `internal/governance/testharness/audit_fixtures.go` — `SeedGenesisAudit`, `InsertAuditEntry`, `ReadChain`, `TamperRow` helpers，用于哈希链测试
- [ ] `internal/governance/testharness/casbin_fixtures.go` — Casbin enforcer fixture wiring `casbin-pgx-adapter/v3`
- [ ] `internal/governance/testharness/snowflake_mock.go` — httptest server 模拟 `/api/v2/statements`，捕获 DDL 字符串
- [ ] `internal/governance/testharness/bigquery_mock.go` — 进程内 `PolicyTagManagerClient` fake，记录 CreateTaxonomy/CreatePolicyTag/SetIamPolicy
- [ ] `internal/governance/testharness/webhook_receiver.go` — httptest receiver，在 POST 上捕获 HMAC-SHA256 X-Platform-Signature 头
- [ ] `internal/governance/testharness/quality_eval_fixtures.go` — seed `fixtures.orders` 表（100 行，customer_id 中 10 个 NULL），用于 null-rate evaluator 测试
- [ ] go.mod 添加：`casbin/casbin/v2 v2.135.0`, `pckhoi/casbin-pgx-adapter/v3 v3.2.0`, `gowebpki/jcs latest`, `wneessen/go-mail latest`, `cloud.google.com/go/datacatalog v1.31.0`
- [ ] 占位符 `migrations/20260510000000_phase5_governance.sql` 仅包含 `CREATE SCHEMA audit AUTHORIZATION audit_migrator;`，使 testcontainer 测试通过（Plan 05-01 Task 1 补充其余内容）

---

## 人工验证项

| 行为 | 需求 | 人工原因 | 测试说明 |
|----------|-------------|------------|-------------------|
| 在真实账户上强制执行 Snowflake DDM | RBAC-04 | 云账户凭证无法在 CI 中；只能在线上 Snowflake 观察最终一致性窗口 | UAT：配置沙箱 Snowflake 账户；运行 `./platform policy show <asset>` 然后通过 REST PATCH 策略；以 `analyst` 角色验证掩码列返回 `***`，以 `admin` 角色返回明文；记录传播时间 |
| BigQuery CLS PolicyTag IAM 传播 | RBAC-04 | 需要真实 GCP 项目；IAM 传播窗口（30s–5min）因环境而异 | UAT：配置沙箱 GCP 项目；PATCH 策略；以 analyst 身份查询掩码列（预期 403）并以 admin 身份查询（预期 rows）；记录观察到的传播时间 |
| 通过 SMTP 发送电子邮件通知 | GOV-04, QUAL-05 | 需要真实 SMTP 中继凭证；CI 使用假 SMTP transport | UAT：在启动配置中配置 smtp.sandbox.example.com；触发审批；验证审核人邮箱在 30 秒内收到签名邮件 |
| 审计日志导出（100k+ 行）下加载 | GOV-06 | 只有在填充的 audit_log 上，流式 + 链重新验证才有意义 | UAT：通过 load-gen 脚本 seed 100k audit 行；运行 `./platform audit export --format=jsonl --out=/tmp/audit.jsonl`；验证链重新遍历通过且内存保持在 <512MB |
| 针对真实 BigQuery 的 Reconciler 5 分钟宽限期 | RBAC-04 | IAM 传播时间只能在真实 GCP 上观察 | UAT：PATCH 策略；在 5–10 分钟内观察 reconciler 日志；断言在宽限期窗口内没有误报 drift_detected 事件发出 |
| MASK_HASH_SALT 轮换运行手册 | RBAC-05 | Salt 轮换使所有历史哈希失效；需要部署级编排 | UAT：记录并在 staging 数据集上执行 deploy + re-materialize 循环；验证轮换前的行仍可用旧 salt 读取；轮换后的行使用新 salt |

---

## 验证签收

- [x] 所有任务都有 `<automated>` verify 或 Wave 0 依赖项
- [x] 采样连续性：每个任务都有自动化覆盖；没有连续 3 个任务没有自动化 verify
- [x] Wave 0 涵盖所有 MISSING 引用（testharness package 安装）
- [x] 无 watch-mode 标志（Go test 运行是单次）
- [x] 反馈延迟 quick run < 60s
- [x] `nyquist_compliant: true` 设置在 frontmatter 中
- [x] 每个需求（RBAC-01..06, GOV-01..07, QUAL-01..05 = 18 个 ID）至少被一个任务覆盖

**审批：** 准备执行