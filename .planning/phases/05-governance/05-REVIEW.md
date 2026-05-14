---
phase: 05-governance
status: partial
source: [05-VERIFICATION.md]
started: 2026-05-10T02:30:00Z
updated: 2026-05-10T02:30:00Z
---

## 当前测试

[等待人工测试]

## 测试项

### 1. Snowflake DDM 端到端推送
预期：PATCH /policies 同步写入 `CREATE OR REPLACE MASKING POLICY` + `ALTER TABLE ALTER COLUMN ... SET MASKING POLICY` 到真实 Snowflake 账户；非允许角色 SELECT 该列返回掩码值（hash/redact/partial）。
人工原因：真实 Snowflake 账户 + 权限（APPLY MASKING POLICY ON ACCOUNT）无法在本地沙箱中执行；sqlmock 单元测试仅覆盖发出的 SQL，不涉及仓库端效果。
结果：[待处理]

### 2. BigQuery CLS 端到端推送
预期：PATCH /policies 同步确保 Data Catalog 分类法 + 策略标签，授予 `roles/datacatalog.fineGrainedReader` 给 AllowRoles，调用 Tables.update 及 policyTags。非细粒度角色 SELECT 该列根据 BigQuery CLS 语义返回 NULL 或失败。
人工原因：需要真实 GCP 服务账号 + Data Catalog API + BigQuery 数据集；fakePTM/fakeBQ 测试仅覆盖请求形状。
结果：[待处理]

### 3. 质量失败时 Webhook + SMTP 告警传递
预期：物化一个 NullCheck 规则超过阈值的资产；验证配置的 webhook 接收方收到带有 `X-Platform-Signature`/`X-Platform-Webhook-ID`/`X-Platform-Timestamp` 头的 POST，且配置的 SMTP 中继收到 STARTTLS 强制邮件及失败摘要。
人工原因：需要运行中的平台进程、webhook 接收方和 SMTP 中继（或测试中继）；httptest 覆盖 webhook 签名但不覆盖通过 JobInserter 队列的端到端派发路径。
结果：[待处理]

### 4. Governance approve→Active 和 reject→Rejected 生命周期，empty-comment reject 返回 400
预期：POST /governance/submit 将 `asset_versions.governance_state` 转为 `in_review`；POST /governance/reviews/{id}/approve 将其转为 `active` 并通知提交者；空 comment 的 POST /governance/reviews/{id}/reject 返回 HTTP 400 ErrCommentRequired；有 comment 则转为 `rejected` 并通知提交者。
人工原因：DB 支持的集成测试 `TestWorkflow_*` 需要 Docker 用于 testharness Postgres testcontainers——Docker 在此沙箱中不可用；建议在声明 SC #2 可投入生产前审查 4 个 commit（32c748d, 24fe99a, 0bd337a, 82f8275）和 executor gate WR-09 fail-closed 修复。
结果：[待处理]

## 摘要

总计：4
通过：0
问题：0
待处理：4
跳过：0
阻塞：0

## 差距

1
