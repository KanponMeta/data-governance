---
status: partial
phase: 05-governance
source: [05-VERIFICATION.md]
started: 2026-05-10T02:30:00Z
updated: 2026-05-10T02:30:00Z
---

## Current Test

[awaiting human testing]

## Tests

### 1. Snowflake DDM end-to-end push
expected: PATCH /policies sync writes `CREATE OR REPLACE MASKING POLICY` + `ALTER TABLE ALTER COLUMN ... SET MASKING POLICY` against a real Snowflake account; SELECT against the column from a non-allowed role returns the masked value (hash/redact/partial).
why_human: Real Snowflake account + privileges (APPLY MASKING POLICY ON ACCOUNT) cannot be exercised from the local sandbox; sqlmock unit tests cover only the SQL emitted, not warehouse-side effect.
result: [pending]

### 2. BigQuery CLS end-to-end push
expected: PATCH /policies sync ensures Data Catalog taxonomy + policy tag, grants `roles/datacatalog.fineGrainedReader` to AllowRoles, calls Tables.update with policyTags. SELECT against the column from a non-fine-grained role returns NULL or fails per BigQuery CLS semantics.
why_human: Real GCP service account + Data Catalog API + BigQuery dataset are required; fakePTM/fakeBQ tests cover only the request shape.
result: [pending]

### 3. Webhook + SMTP alert delivery on quality failure
expected: Materialize an asset with a NullCheck rule whose threshold is exceeded; verify the configured webhook receiver gets a POST with `X-Platform-Signature`/`X-Platform-Webhook-ID`/`X-Platform-Timestamp` headers AND the configured SMTP relay receives a STARTTLS-mandatory mail with the failure summary.
why_human: Requires a running platform process, a webhook receiver, and an SMTP relay (or test relay); httptest covers webhook signing but not the end-to-end dispatch path through the JobInserter queue.
result: [pending]

### 4. Governance approve→Active and reject→Rejected lifecycle, reject-with-empty-comment 400
expected: POST /governance/submit transitions `asset_versions.governance_state` to `in_review`; POST /governance/reviews/{id}/approve transitions it to `active` and the submitter receives a notification; POST /governance/reviews/{id}/reject with an empty comment returns HTTP 400 ErrCommentRequired; with a comment, transitions to `rejected` and notifies the submitter.
why_human: The DB-backed integration test `TestWorkflow_*` requires Docker for testharness Postgres testcontainers — Docker is unavailable in this sandbox; reviewing 4 commits (32c748d, 24fe99a, 0bd337a, 82f8275) and the executor gate WR-09 fail-closed fix is recommended before declaring SC #2 production-ready.
result: [pending]

## Summary

total: 4
passed: 0
issues: 0
pending: 4
skipped: 0
blocked: 0

## Gaps
