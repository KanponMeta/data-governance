---
phase: 5
slug: governance
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-05-09
---

# Phase 5 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (stdlib) + testcontainers-go for Postgres |
| **Config file** | none — Wave 0 installs `internal/governance/testharness/` package |
| **Quick run command** | `go test ./internal/governance/... -run "Test" -short -timeout 60s` |
| **Full suite command** | `go test ./... -timeout 5m` |
| **Estimated runtime** | quick ~15s; full ~3m (live warehouse mocks via httptest) |

---

## Sampling Rate

- **After every task commit:** Run `go test ./internal/governance/... -short -timeout 60s` (the touched package's tests)
- **After every plan wave:** Run `go test ./... -timeout 5m` (full suite)
- **Before `/gsd-verify-work`:** Full suite must be green AND `internal/governance/audit/integration_test.go::TestHashChain_TamperDetection` must pass against a real Postgres testcontainer
- **Max feedback latency:** 60 seconds for quick run

---

## Per-Task Verification Map

> Filled by gsd-planner per plan task. The planner MUST emit one row per task with the linked REQ-ID, threat ref, automated command, and the test file that would prove it (or `❌ W0` if Wave 0 must create it first).

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 5-01-01 | 01 | 0 | RBAC-01 | — | testcontainers Postgres harness boots in <30s | unit | `go test ./internal/governance/testharness/... -run TestPostgresContainer` | ❌ W0 | ⬜ pending |
| 5-01-02 | 01 | 0 | RBAC-01 | — | hash-chain audit fixture seeds genesis row | unit | `go test ./internal/governance/audit/... -run TestGenesis` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

> The planner expands this table to one row per task across all 5 plans. Every requirement (RBAC-01..06, GOV-01..07, QUAL-01..05) MUST appear in at least one row.

---

## Wave 0 Requirements

- [ ] `internal/governance/testharness/postgres_test.go` — testcontainers Postgres helper for governance tests (audit schema, RLS roles, casbin tables)
- [ ] `internal/governance/audit/audit_test.go` — hash-chain genesis + tamper-detection fixtures (RBAC + GOV reqs)
- [ ] `internal/governance/policy/casbin_test.go` — Casbin model + adapter wiring fixture (RBAC reqs)
- [ ] `internal/governance/warehouse/snowflake/mock_test.go` — httptest server emulating Snowflake DDL responses (RBAC-04)
- [ ] `internal/governance/warehouse/bigquery/mock_test.go` — httptest server emulating BigQuery Data Catalog + Tables IAM (RBAC-05)
- [ ] `internal/governance/notify/webhook_test.go` — httptest receiver capturing HMAC-signed deliveries (QUAL-04)
- [ ] `internal/governance/quality/eval_test.go` — null-rate evaluator fixture against testcontainers Postgres (QUAL-01..03)
- [ ] No new framework install — go test stdlib + testcontainers-go already in use from Phase 4

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Snowflake DDM enforcement against a real account | RBAC-04 | Live cloud account credentials cannot be in CI; eventual-consistency window only observable on real Snowflake | UAT: provision sandbox Snowflake account; run `platform policy push --warehouse=snowflake-sandbox`; verify masked column returns `***` for `analyst` role and clear text for `admin` role |
| BigQuery CLS PolicyTag IAM propagation | RBAC-05 | Real GCP project required; IAM propagation window (30s–5min) is environment-dependent | UAT: provision sandbox GCP project; run `platform policy push --warehouse=bq-sandbox`; query masked column as analyst (expect 403) and as admin (expect rows); record observed propagation time |
| Email notification delivery via SMTP | GOV-04 | Requires real SMTP relay credentials; CI uses fake transport | UAT: configure smtp.sandbox.example.com in `notifications.yaml`; trigger an approval; verify reviewer mailbox receives signed email within 30s |
| Audit log export under load | GOV-06 | Streaming + chain re-verification at scale only meaningful on a populated audit_log (>=100k rows) | UAT: seed 100k audit rows via load-gen script; run `platform audit export --format=jsonl --output=/tmp/audit.jsonl`; verify chain re-walk passes and memory stays <512MB |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags (Go test runs are one-shot)
- [ ] Feedback latency < 60s for quick run
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
