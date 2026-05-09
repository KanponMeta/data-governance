# Phase 5: 治理引擎 - Context

**Gathered:** 2026-05-09
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 5 turns the lineage-aware platform from Phase 4 into a **governed** platform. After Phase 5 the platform must:

- **RBAC + 列级访问控制 (RBAC-01..06)** — admins define roles, assign users, declare column-level masking policies; the platform synchronizes masking to **Snowflake Dynamic Data Masking** and **BigQuery Column-Level Security** via warehouse-native APIs (Pitfall #8 — never proxy queries); for non-warehouse connectors (PostgreSQL, MySQL, S3, GCS, HDFS) the platform applies masking at the materialization-write boundary (RBAC-05).
- **PII tag propagation through lineage (Pitfall #15)** — when a column is tagged PII, the propagation engine traverses `column_edges` (Phase 4 D-13) and inherits the tag onto every downstream column unless an explicit override with reason is recorded.
- **Asset governance workflow (GOV-01..05)** — assets carry a state machine (Draft → InReview → Active / Rejected) bound to `asset_versions` (Phase 4 D-03 code-hash); engineers submit, configured reviewers approve/reject with mandatory comments on rejection; auto-pre-approval (Pitfall #7) fast-tracks low-risk submissions; submitters and reviewers receive notifications.
- **Tamper-proof hash-chain audit log (RBAC-06 + GOV-05)** — every governance + access-control operation produces a row in a **separate `audit_log` table** with `seq, prev_hash, self_hash` columns, locked down by PostgreSQL RLS (extending the Phase 1 D-09 immutability pattern to a dedicated, separately-administered table). The chain is verifiable via CLI and exportable for SOC2/GDPR audits.
- **Quality rules at materialization (QUAL-01..05)** — engineers declare rules on the asset builder (NullCheck, RangeCheck, SQLAssertion); the executor evaluates them in the same transaction as lineage/schema capture; failures populate a new `run_quality_status` column (independent of `run.state`, consistent with Phase 4 D-04 "metadata failures don't block data work"); breaches dispatch alerts via webhook or SMTP.
- **Audit-log retention TTL (GOV-07, partial)** — `audit_log.expires_at` infrastructure ships in v1; the actual deletion mechanism + asset data TTL deferred to v1.x.

What stays **out** of Phase 5: Web UI for governance inbox / role management / column-policy editor (Phase 6 UI-06/UI-07), row-level security (v2 AGOV-01), automatic PII classification by pattern matching (v2 AGOV-02), SSO/OIDC (v2 PLAT-02), asset-data retention TTL execution (v1.x), external hash-anchoring to S3 Object Lock (interface reserved, v1.x implements). The Phase 1–4 contracts (event_log RLS-immutability, run lifecycle, scheduler/sensor daemon, lineage capture, schema diff) are **not modified**, only **additively extended** via new event_type values and the new `MaskingProvisioner` optional connector capability.

</domain>

<decisions>
## Implementation Decisions

### RBAC Model & Casbin Integration

- **D-01:** Casbin handles **role → API permission** mapping only; column-level policy lives in a **dedicated `column_policies` table** outside Casbin.
  - Casbin model: standard RBAC `p = (sub, obj, act)` where `obj` is a resource path (`/assets/<name>/manage`, `/audit/export`, `/governance/approve`, `/policies/edit`, `/users/admin`) and `act` ∈ {read, write, manage}.
  - Casbin Postgres adapter (specific choice — `casbin/casbin-pg-adapter` v3 vs `memwey/casbin-sqlx-adapter` — picked by planner/researcher) persists policy rules. Phase 5 imports the Casbin v2.135.x package per `CLAUDE.md` §技术栈 §授权.
  - Why split: Snowflake DDM and BigQuery CLS need to sync **rows** of column policies (not Casbin enforcer evaluations). Encoding column dimensions inside Casbin's RBAC model creates impedance mismatch with warehouse APIs. Casbin's strength is the role-graph; column-policy is a join table that the platform owns and projects to warehouses.

- **D-02:** Column policies are declared **two ways, runtime wins on read**, mirroring Phase 4 D-17:
  - **Builder default (code-declared):**
    ```go
    asset.New("orders").
        ColumnPolicy(asset.ColumnPolicy{
            Column: "ssn", Mask: asset.MaskHash, AllowRoles: []string{"pii-analyst"},
        }).
        ColumnPolicy(asset.ColumnPolicy{
            Column: "email", Mask: asset.MaskPartial, AllowRoles: []string{"customer-ops"},
        })
    ```
    Stored on `asset_versions` row, fingerprinted into code_hash (Phase 4 D-03).
  - **Runtime override (REST):**
    - `PATCH /assets/:name/columns/:col/policy` → `{mask, allow_roles, reason}` (governance team self-serve).
    - Stored in `column_policies` table keyed by `(asset, column)`. `effective = COALESCE(runtime_value, code_default)`.
  - **Global YAML default (third surface):** `policies.yaml` maps `tag → mask_default` (e.g., `"pii": "hash"`). Applies when no explicit policy exists at builder or runtime level — the lowest-precedence layer.
  - Resolution order on read: runtime override > builder default > tag-default YAML > unmasked.
  - **Audit trail:** every PATCH and every YAML reload emits `policy.changed` to **`audit_log` (NOT `event_log`)** with `{actor, before, after, reason}`. See D-13.

- **D-03:** Mask types in v1 are **enumerated and finite** — Hash (SHA-256 with platform salt), Redact (constant `"***"`), Partial (last-N-chars or first-N-chars revealed). Tokenization, format-preserving encryption, and value-range bucketing are v1.x. Each mask type maps to:
  - In-pipeline (RBAC-05): a Go function applied at `AssetIO.Write` boundary to mask outgoing rows for non-warehouse targets.
  - Snowflake DDM: a `CREATE MASKING POLICY` SQL template parameterized by mask type.
  - BigQuery CLS: a Data Catalog policy tag with attached IAM policy + a `SAFE_CAST(NULL ...)` masking function template.

- **D-04:** Warehouse masking sync (RBAC-04) uses **Push-on-change + Reconcile loop**, both running through River (per `CLAUDE.md` §技术栈, River is the in-tree job queue):
  - **Push:** policy mutation transaction enqueues a `policy_sync` River job with `(asset, column, target_connector)`. The job calls the connector's `MaskingProvisioner` capability (D-05) to ALTER MASKING POLICY (Snowflake) or update Data Catalog policy tag (BigQuery). Failures retry via River's native exponential backoff; permanent failures emit `masking.sync_failed` audit-log entry + alert.
  - **Reconcile loop:** new `./platform reconciler` daemon (or scheduler subcommand extension) every **15 minutes** (configurable) pulls warehouse state via the connector's `ListMaskingPolicies` capability and diffs against `column_policies`. Drift triggers `masking.sync_drift_detected` event + an automatic re-push.
  - Pitfall #8 alignment: the platform never proxies queries — masking is a warehouse-native concern, the platform is the policy source-of-truth that synchronizes outward.

- **D-05:** New optional connector capability — `connector.MaskingProvisioner` — follows Phase 4 D-06 pattern:
  ```go
  type MaskingProvisioner interface {
      ApplyMaskingPolicy(ctx context.Context, asset AssetRef, policy ColumnPolicy) error
      RemoveMaskingPolicy(ctx context.Context, asset AssetRef, column string) error
      ListMaskingPolicies(ctx context.Context, asset AssetRef) ([]ColumnPolicy, error)
  }
  ```
  - In-scope implementations in Phase 5: **Snowflake** (DDM) and **BigQuery** (CLS via Data Catalog policy tags). Other Phase 2 connectors (PostgreSQL, MySQL, S3, GCS, HDFS) do NOT implement this capability — they go through the in-pipeline mask path (D-03).
  - Type assertion at runtime: `if mp, ok := conn.(connector.MaskingProvisioner); ok { ... }`. Connectors that don't implement: in-pipeline masking applies for that asset's writes.
  - Snowflake DDM API specifics, BigQuery Data Catalog API specifics, and the exact SQL templates are **planner/researcher discretion** — `STATE.md` flagged "Snowflake and BigQuery masking provisioning API calls need validation before designing PolicyStore sync interface". Phase researcher must validate against current API surface before implementation.

- **D-06:** **PII tag propagation** runs **synchronously** inside the executor's metadata transaction, after lineage_writer has written `column_edges`:
  - Trigger: every `lineage.captured` event (Phase 4 D-01) from a successful materialization.
  - Algorithm: for each output column, BFS upstream via `column_edges` (Phase 4 D-13) one hop at a time; if any source column carries `pii=true` (in `asset_metadata.tags`), inherit `pii=true` on the output column unless an explicit override exists.
  - **Conflict resolution: union** — any upstream PII column => downstream is PII. Conservative default; intersection rule is rejected because it can lose PII status when only one of multiple upstreams was sensitive.
  - **Override mechanism:** explicit code-level escape hatch:
    ```go
    asset.New("orders_anonymized").
        Column("hashed_ssn").
            TagOverride(asset.TagOverride{Remove: "pii", Reason: "hashed at source via D-03 MaskHash; not reversible"})
    ```
    Override requires non-empty `Reason`. Override emits `metadata.tag_overridden` to **audit_log** (D-13) with `{actor, asset, column, removed_tag, reason}`. Runtime overrides via REST PATCH (Phase 4 D-17) follow the same rule.
  - Propagation runs in the same transaction as lineage capture — no eventually-consistent window where downstream PII columns appear unmasked.

- **D-07:** `column_policies` table follows Phase 4 D-15 **soft-retire / temporal table** pattern:
  - Columns: `(id, asset, column, mask_type, allow_roles, code_hash_first, code_hash_latest, first_seen_run_id NULLABLE, first_seen_at, last_seen_at, superseded_at NULLABLE, source ENUM(builder|runtime|yaml-default))`.
  - Active policies: `WHERE superseded_at IS NULL`.
  - Point-in-time queries (audit "what policy was active on date $T?"): `WHERE first_seen_at <= $T AND (superseded_at IS NULL OR superseded_at > $T)` — same pattern as Phase 4 lineage edges.
  - Deletes are forbidden by RLS; policy "removal" sets `superseded_at = NOW()` plus emits `policy.removed` to audit_log.

### Governance Workflow

- **D-08:** Governance state lives on **`asset_versions.governance_state`** column (extends Phase 4 D-03):
  - Enum: `draft | in_review | active | rejected`. CHECK constraint enforces valid transitions:
    - `draft → in_review` (submit)
    - `in_review → active` (approve)
    - `in_review → rejected` (reject)
    - `rejected → in_review` (resubmit same code_hash with comments)
    - `active → in_review` (admin can force re-review; rare)
  - Default for newly-registered code_hash: `draft`. New code_hash always starts in `draft` (resets governance — Pitfall #7 prevents "old approval covers new code").
  - Materialization gate: executor checks `asset_versions.governance_state = 'active'` before running. `draft` and `in_review` runs are rejected with `governance.materialization_blocked` event. (Backward compat for Phase 1–4 development assets: a config flag `governance.gating_enabled` defaults to **false** for v1; production opts in. This avoids breaking dev workflows.)

- **D-09:** Reviewer assignment is **three-source**, union-based:
  1. **Builder declaration:** `asset.New("orders").Reviewers("team-data-gov", "privacy-team")` — engineer-declared (default contributor).
  2. **Tag-rule YAML:** `policies.yaml` maps `tag → required_reviewer_roles` (e.g., `"pii": ["privacy-team"]`). Auto-applied when any column has the tag.
  3. **Owner fallback:** `asset_metadata.owner` (Phase 4 D-17) implies a reviewer-team mapping via a config table `team_owners` (e.g., `team-data@` → role `team-data-leads`). Used when neither (1) nor (2) yields a reviewer.
  - Resolution: union of (1) ∪ (2) ∪ (3 if (1)∪(2) is empty). Each role contributes one reviewer to the candidate pool.
  - **Quorum:** default 1 (first reviewer to act decides). Per-asset override: `asset.New("x").Quorum(asset.QuorumAll)` requires every reviewer in the candidate pool to approve. `Quorum(2)` requires any 2. Quorum=1 is the v1 default for usability.
  - Reviewer-pool persistence: snapshot taken at submission time into `governance_reviews` table — adding/removing roles later does not retroactively change in-flight reviews.

- **D-10:** Auto pre-approval (Pitfall #7 — designed before the human path):
  - On `submit`, run a fixed check pipeline before assigning to humans:
    1. **Schema break ack:** any unacknowledged breaking schema_change for this asset_version → BLOCK (cannot auto-approve).
    2. **Policy/PII consistency:** every column tagged `pii` has a `column_policy` (builder, runtime, or YAML default) → if missing, BLOCK.
    3. **Quality config sanity:** if asset has any `QualityRule(...)`, the rules parse and reference existing columns → if broken, BLOCK.
    4. **Lineage drift:** Phase 4 D-04 `drift_status = 'pending'` → BLOCK.
    5. **PII presence + reviewer:** if any column carries `pii` tag, fast-path is disabled — must go human (`privacy-team`).
  - All checks pass + no PII columns + no breaking schema change in this version → state goes directly to `active` with `governance.auto_approved` event in audit_log + a notification to the owner. Builder can opt out: `asset.New("x").RequireHumanReview()` forces human path regardless.
  - Result: low-risk schema-stable assets ship without human latency; risky assets get the focused attention.

- **D-11:** **Mandatory rejection comment + SLA reminders without auto-escalation:**
  - `POST /governance/reviews/:id/reject` requires non-empty `comment` field; CLI mirrors via flag. Approval `comment` optional.
  - SLA timer: configurable `governance.review_sla_hours` (default 48h). Scheduler tick scans `governance_reviews` where `submitted_at + sla_hours < NOW()` and `decided_at IS NULL` → emit `governance.review_sla_breached` audit-log entry + send notification to **all assigned reviewers** (re-ping) + notify the asset owner. Does **not** auto-escalate to a backup reviewer pool — escalation is opt-in (per-asset `.EscalationRoles(...)` or global YAML), audit-log captures the escalation event when it does happen.
  - Why no auto-decision: SOC 2 requires human attestation for governance gates; auto-approving a stalled review breaks that.

- **D-12:** Submission lifecycle — submission, review records, notifications:
  - `POST /governance/submit` with `{asset, code_hash, reviewers_extra?}` creates a `governance_reviews` row tied to (asset_version_id, submitter_id), captures the reviewer-pool snapshot (D-09), and emits `governance.submitted` to audit_log.
  - Reviewer decision: `POST /governance/reviews/:id/{approve|reject}` — atomic update on `governance_reviews` + transition `asset_versions.governance_state` + emit `governance.{approved|rejected}` to audit_log + dispatch notification (D-21).
  - REST + CLI symmetry per Phase 4 D-19: `./platform governance submit <asset>`, `./platform governance review <id> --approve|--reject [--comment=...]`, `./platform governance status [<asset>]`.

### Audit Hash Chain & Log Architecture

- **D-13:** Audit log lives in a **dedicated `audit_log` table**, separate from `event_log`:
  - Schema: `(seq BIGSERIAL PRIMARY KEY, prev_hash BYTEA, self_hash BYTEA, occurred_at TIMESTAMPTZ, event_type TEXT, actor_id, resource_type, resource_id, payload JSONB)`.
  - Hash construction: `self_hash = SHA-256(seq || prev_hash || occurred_at || event_type || actor_id || resource_type || resource_id || canonical_json(payload))`. Genesis row: `prev_hash = bytea(32 zero bytes)`.
  - **Separate Postgres schema** (`audit`) with separate migration user (`audit_migrator`) per Pitfall #5 ("单独的表、单独的 Schema、单独的备份策略"). Application user (`platform_app`) has INSERT-only on `audit.audit_log`. RLS enforces no UPDATE/DELETE for `platform_app` (extends Phase 1 D-09 RLS pattern). Only the migration user can DDL.
  - **Insert protocol:** atomic helper `audit.WriteEntry(ctx, tx, entry)` — selects `MAX(seq)` (FOR UPDATE on a sentinel row) inside the caller's transaction, computes `self_hash`, inserts. Concurrent writers serialize via the sentinel-row lock. Throughput estimate: governance + access-control events are low-rate (≪ run.* rate) so single-lock serialization is acceptable.
  - **Why separate from `event_log`:** event_log is high-rate run-attributed events (run.step.*, schedule.*, lineage.captured, schema.captured) — adding hash chain there would force serialization on the hot path. audit_log is small, low-rate, security-critical.

- **D-14:** Audit-log content scope is **narrow**:
  - **In scope (these events go to `audit_log`):**
    - `policy.changed`, `policy.removed`, `masking.sync_failed`, `masking.sync_drift_detected`
    - `role.created`, `role.deleted`, `role.assigned`, `role.revoked`
    - `governance.submitted`, `governance.approved`, `governance.rejected`, `governance.auto_approved`, `governance.review_sla_breached`, `governance.materialization_blocked`
    - `audit.exported`, `audit.verify_failed`
    - `metadata.tag_overridden` (PII override per D-06)
  - **Out of scope (stay in `event_log`):** `run.*`, `schedule.*`, `sensor.*`, `lineage.captured`, `schema.*`, `metadata.updated` (non-PII-override metadata edits stay in event_log).
  - Rationale: hash chain protects "who changed access controls" — the SOC2/GDPR-attestable surface. The full event stream remains queryable via event_log without paying the chain's serialization cost.

- **D-15:** Tamper-detection + export surfaces (Phase 4 D-19 three-layer pattern):
  - **CLI** `./platform audit verify [--from=<seq>] [--to=<seq>]` — sequential scan, recompute each `self_hash`, fail loudly with `seq` of mismatch. Exit code 0 = chain intact, non-zero = tamper detected. Suitable for SOC 2 auditor demos / CI invariant checks. Operators run on cadence.
  - **REST** `GET /audit/export?from=<ISO>&to=<ISO>&format=json|csv|jsonl` — streaming response (chunked transfer for large ranges), each row includes `seq` and `self_hash` so downstream consumers can re-verify externally. Format default `jsonl` (one JSON object per line, ideal for log shippers).
  - **CLI** `./platform audit export --from=<ISO> --to=<ISO> --format=jsonl --out=<file>` — wrapper around the same library function.
  - All three surfaces wrap a single Go library `internal/audit/{Verify,Export}` per Phase 4 D-19.
  - **No background reconciler in v1** — verification is on-demand. Background reconciler is a v1.x feature. (User chose CLI+REST over CLI+reconciler+REST during discuss; reconciler is mostly belt-and-suspenders, CLI satisfies the audit story.)
  - Exporting the audit log itself emits `audit.exported` (recursively into the same chain) per Pitfall #5 "记录日志的日志".

- **D-16:** **GOV-07 Retention TTL is partial in v1:** audit-log retention infrastructure ships; asset-data retention deferred.
  - `audit_log` schema includes `expires_at TIMESTAMPTZ NULL`. Global config `audit.retention_default_days` (default `infinite` / NULL — most compliance scenarios are 7–10 years). Per-event-type override allowed.
  - The actual purge mechanism (a privileged background job that DELETEs expired rows) is **deferred to v1.x** — schema-only in v1, with documented runbook for ops to run a manual purge if needed. Privileged purge user (separate from `platform_app`) is provisioned in v1 migrations.
  - Asset-data TTL (auto-deleting materialized data after N days) is **deferred to v1.x** entirely. The complexity (per-connector deletion, lineage update, governance approval for the deletion itself) does not fit in v1 budget.

- **D-17:** External hash anchoring (S3 Object Lock / WORM) — **interface-only in v1:**
  - Pitfall #5 calls this "可选" / "对于合规级部署". v1 ships a `internal/audit/anchor.Anchor` interface stub; no implementation. v1.x adds `S3ObjectLockAnchor` that periodically writes `(seq, self_hash)` pairs to an immutable S3 bucket.
  - Why: implementation requires ops-level S3 + IAM + retention-lock configuration that is out of scope for a single-binary v1 release.

### Quality Rules & Notifications

- **D-18:** Quality rule DSL is **builder-chained, strongly-typed**:
  - Every rule type implements `asset.QualityRule` interface (`Name() string`, `Evaluate(ctx, eval QualityEvaluator) (QualityResult, error)`). v1 ships three concrete types:
    - `asset.NullCheck{Column string, MaxRate float64}` — `COUNT(NULL) / COUNT(*) <= MaxRate` (QUAL-01 explicit "空值率").
    - `asset.RangeCheck{Column string, Min, Max float64}` — `MIN(col) >= Min AND MAX(col) <= Max` (QUAL-01 explicit "范围边界").
    - `asset.SQLAssertion{Name string, SQL string, Predicate AssertionPredicate}` — runs user-supplied SQL where `${asset}` interpolates to the materialized table; `Predicate` interprets the result (e.g., `ScalarEqualsZero`, `ScalarLessThan(N)`, `RowCountIsZero`) (QUAL-01 explicit "自定义 SQL 断言").
  - Builder usage:
    ```go
    asset.New("orders").
        QualityRule(asset.NullCheck{Column:"customer_id", MaxRate: 0.0}).
        QualityRule(asset.RangeCheck{Column:"amount", Min: 0, Max: 1e9}).
        QualityRule(asset.SQLAssertion{Name:"no_dup_orders",
            SQL:"SELECT COUNT(*) - COUNT(DISTINCT order_id) FROM ${asset}",
            Predicate: asset.ScalarEqualsZero,
        })
    ```
  - Other rule types (UniqueCheck, RegexCheck, FreshnessCheck-as-quality, custom Go predicate) are v1.x — the `QualityRule` interface is a stable extension point.

- **D-19:** Quality evaluation runs **in the same executor transaction as lineage/schema capture, after both**, with an independent run status column:
  - Sequencing in `internal/runtime/executor.runStep`: Materialize succeeds → lineage_writer → schema_writer → **quality_evaluator** → run.state = succeeded. All four in one DB transaction.
  - **Independent column:** `runs.run_quality_status ENUM(passed, failed, skipped, error)` (default NULL until evaluation completes for assets with no rules → `skipped`). `runs.state` retains its Phase 2 D-17 lifecycle semantics — quality failure does **not** flip `state` to `failed`. Materialization succeeded, the run is "succeeded but failed quality".
  - Why: aligns with Phase 1 D-09 "metadata failures don't fail the data work" + Phase 4 D-04 same philosophy. Conflating quality with materialization breaks Phase 2 retry semantics (quality flap shouldn't trigger materialization retry) and creates UX confusion ("the run failed but my data is in the warehouse?").
  - **Per-rule outcomes:** new `quality_results` table `(run_id, rule_name, rule_type, status ENUM(passed,failed,error), measured_value, threshold, evaluated_at, error_message NULLABLE)`. One row per rule. Aggregate `runs.run_quality_status` is the worst across rules.
  - **Failure dispatch:** quality failures emit `quality.rule_failed` event_log + enqueue an alert dispatch River job (D-21).
  - **Connector evaluator:** `connector.QueryAggregate(ctx, sql) (any, error)` is added as a stable connector capability for SQLAssertion + NullCheck + RangeCheck. Connectors that don't implement (e.g., file-only S3) → quality rules `error` status with reason "connector does not support aggregate queries"; alert sent so operators know.

- **D-20:** **QUAL-04 Freshness SLA** is evaluated by the **scheduler subcommand**, not as a quality rule:
  - Builder: `asset.New("x").FreshnessSLA(asset.FreshnessSLA{MaxLag: 6 * time.Hour, ScopeAfterCronFire: true})`. `MaxLag` measured from the schedule's last fire time (cron-aligned) or from the last successful run (sensor-/manual-triggered assets).
  - New `schedules.last_succeeded_at` column. Scheduler tick (Phase 3 D-01..04) extends to also scan assets with FreshnessSLA where `last_succeeded_at + max_lag < NOW()` → emit `sla.breached` event_log + enqueue alert dispatch (D-21). One alert per SLA breach window (dedup on `(asset, sla_breach_window_start)`).
  - Why scheduler not quality-rule: quality rules evaluate when materialization completes — but SLA must fire when materialization **fails to happen**. Scheduler is the only subsystem that knows about expected fires.

- **D-21:** Notification & alert delivery:
  - **Channels (v1):** webhook (POST JSON to user-configured URL) + SMTP (bring-your-own — startup config holds `smtp.host/port/user/password/from`). SES, SendGrid, Slack — **deferred to v1.x** (kept lean for single-binary v1; users can fan webhooks to Slack themselves).
  - **Routing config:** `notifications.yaml` maps event-type patterns to channel + recipient list:
    ```yaml
    rules:
      - match: "governance.submitted"
        webhook: "https://internal/governance"
        email_to: "{reviewer_emails}"     # template var resolved per-event
      - match: "quality.rule_failed"
        webhook: "https://internal/alerts"
        email_to: "{owner_email}"
      - match: "sla.breached"
        webhook: "https://pagerduty.example/v2/enqueue"
    ```
  - **Delivery:** all dispatches go through River jobs — non-blocking on the executor / scheduler / governance handler hot paths. River's native retry (exponential backoff) handles transient failures. Permanent failures emit `notification.dispatch_failed` event_log + log structured error.
  - **Submitter notifications (GOV-04):** governance decision emits `governance.{approved,rejected}` → notification rule routes to submitter's email. Requires `users.email` (Phase 1 AUTH-01) — which is already in place.

### Plan Partitioning Hint

- **D-22:** Phase 5 is large. Suggested plan partitioning (planner may refine, same as Phase 2 D-11 / Phase 4 multi-plan model):
  - **05-01 RBAC基础:** Casbin integration + Postgres adapter + roles/users/role_assignments tables + role-permission CRUD REST/CLI + audit_log table + RLS schema + hash-chain writer + verify CLI.
  - **05-02 列策略 + 仓库掩码同步:** column_policies table + ColumnPolicy DSL + REST PATCH + global YAML loader + MaskingProvisioner capability + Snowflake DDM impl + BigQuery CLS impl + Push-on-change River job + Reconcile loop subcommand + sync_failed/drift events.
  - **05-03 PII 传播 + 非仓库物化时掩码:** propagator extension to lineage_writer + TagOverride DSL + non-warehouse mask functions at AssetIO.Write + connector capability assertion order.
  - **05-04 治理工作流:** asset_versions.governance_state column + governance_reviews table + .Reviewers/.Quorum/.RequireHumanReview/.EscalationRoles DSL + auto-pre-approval check pipeline + REST + CLI + materialization gate.
  - **05-05 质量规则 + SLA + 告警:** QualityRule interface + NullCheck/RangeCheck/SQLAssertion + executor transaction extension + run_quality_status + quality_results table + connector.QueryAggregate capability + FreshnessSLA + scheduler extension + notification dispatcher + River job pipeline.
  - Each plan ends with integration tests against real Postgres + at least one warehouse emulator (for 05-02) per Phase 2 D-10 testcontainers convention.

### Event Log & Audit Log Type Additions

- **D-23:** New `event_log.event_type` values (Phase 4 D-21 pattern; CHECK constraint extended additively):
  - **Quality:** `quality.rule_passed`, `quality.rule_failed`, `quality.rule_error`, `quality.run_evaluated`
  - **SLA:** `sla.breached`, `sla.recovered`
  - **Notifications:** `notification.dispatched`, `notification.dispatch_failed`
  - **Materialization gating:** `governance.materialization_blocked`
  
  New `audit_log` event types (per D-14 scope):
  - **Policy/Access:** `policy.changed`, `policy.removed`, `masking.sync_failed`, `masking.sync_drift_detected`
  - **Roles:** `role.created`, `role.deleted`, `role.assigned`, `role.revoked`
  - **Governance:** `governance.submitted`, `governance.approved`, `governance.rejected`, `governance.auto_approved`, `governance.review_sla_breached`
  - **Audit-on-audit:** `audit.exported`, `audit.verify_failed`
  - **PII override:** `metadata.tag_overridden`

### Claude's Discretion

- Casbin Postgres adapter — concrete library choice between `casbin/casbin-pg-adapter` v3, `memwey/casbin-sqlx-adapter`, or building a thin in-house adapter on existing `pgxpool`. Researcher should pick based on maintenance status at planning time.
- Snowflake DDM API call shape (account-level vs schema-level masking policies) and exact DDL templates — **researcher must validate against current Snowflake API** before committing (STATE.md flagged).
- BigQuery Column-Level Security via Data Catalog policy tag taxonomy structure — researcher validates current Cloud SDK + IAM binding shape.
- Mask types (Hash/Redact/Partial) implementation details — Hash salt management (per-deployment vs per-asset), Partial reveal length default, charset for Redact placeholder.
- `policies.yaml` file path/location convention (alongside startup config? separate `policies/` directory?) and reload semantics (signal-based, watch-based, or restart-only).
- Quorum=All semantics when reviewer pool changes during a review (snapshot at submit vs live recomputation) — D-09 says snapshot, but edge cases (reviewer leaves company) may warrant escalation_roles override.
- Audit-log export `jsonl` line schema versioning — first-line metadata header vs implicit schema versioning. Lean toward `_meta` first row.
- River queue configuration — separate `policy_sync` and `notification` queue names vs single shared queue with priority. Phase 2 default was one queue; revisit if backfill priority interactions appear.
- `notifications.yaml` template variable language (Go template / handlebars-like / simple `{var}` substitution). Lean toward simple `{var}` substitution.
- Reviewer-pool snapshot persistence shape — denormalized JSON column on `governance_reviews` vs separate `governance_review_reviewers` table. Either works; denorm is simpler, normalized supports per-reviewer status tracking.
- Whether `column_policies` rows carry `partition_key` (Phase 4 D-13's column_edges has it) — defer to v1.x; v1 column policies are asset-level.
- Audit verify CLI output format (table for human / JSON for CI) — default to table with `--format=json` flag, mirroring Phase 4 `./platform impact` UX.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Requirements & Roadmap
- `.planning/REQUIREMENTS.md` — Phase 5 in-scope: RBAC-01..06 (lines 64–69), GOV-01..07 (lines 73–79), QUAL-01..05 (lines 47–51); deferred: AGOV-01/AGOV-02 (lines 116–117), PLAT-02 SSO (line 122)
- `.planning/ROADMAP.md` §Phase 5 — five acceptance criteria + dependency on Phase 4

### Project Context
- `.planning/PROJECT.md` §核心价值 — "下游使用者能信任所用数据... 清楚地知道谁有权访问" — drives D-01 + D-04 + D-06
- `.planning/PROJECT.md` §关键决策 — "字段级血缘作为一等特性" + "v1 开源（自托管）" — D-04 single-binary masking sync, D-21 SMTP-only
- `.planning/STATE.md` §Blockers/Concerns — "Phase 5 (Warehouse-native masking sync): Snowflake and BigQuery masking provisioning API calls need validation" — drives D-04/D-05 researcher validation step

### Prior-phase decisions Phase 5 builds on
- `.planning/phases/01-infrastructure/01-CONTEXT.md` — D-09 event_log RLS-immutability (extended to audit_log in D-13); D-10 event_type enum extension model (D-23)
- `.planning/phases/02-execution-engine/02-CONTEXT.md` — D-04 `MaterializeResult` (extended in D-19 with quality results); D-17 `runs.state` CHECK constraint (D-19 adds `run_quality_status` separately, NOT a new state); D-18 event_type extension precedent
- `.planning/phases/03-scheduling-sensors-partitions/03-CONTEXT.md` — D-01..D-04 scheduler architecture (D-20 SLA evaluation extends this); River queue usage precedent (D-04, D-21)
- `.planning/phases/04-schema/04-CONTEXT.md` — D-03 code_hash + asset_versions (D-08 extends with governance_state); D-06 optional connector capability pattern (D-05 follows for MaskingProvisioner); D-13 split tables for hot reads (column_policies follows); D-15 soft-retire / temporal table (D-07 column_policies follows); D-17 metadata three-layer (builder default + runtime PATCH + tag default — D-02 column policies follows exactly); D-19 Go+REST+CLI three-surface library (D-15 audit verify/export, D-12 governance follow); D-21 event_type additions

### Research (must-read for Phase 5)
- `.planning/research/PITFALLS.md` §陷阱 5 — "审计日志可查询但不防篡改" — drives D-13 (separate table + RLS + hash chain) + D-14 (scope) + D-15 (verify CLI) + D-17 (anchoring deferred but interface)
- `.planning/research/PITFALLS.md` §陷阱 7 — "审批工作流成为同步瓶颈" — drives D-10 (auto pre-approval first) + D-11 (SLA reminder + comment requirement) + D-12 (review API)
- `.planning/research/PITFALLS.md` §陷阱 8 — "列级访问控制没有覆盖所有查询路径" — drives D-04 (warehouse-native sync, never proxy) + D-05 (MaskingProvisioner) + STATE.md API-validation flag
- `.planning/research/PITFALLS.md` §陷阱 15 — "PII 敏感性标签未通过血缘传播" — drives D-06 (sync propagation + union + override)
- `.planning/research/PITFALLS.md` §阶段特定警告 — Phase 5 rows: 列访问控制, 审计日志, 审批工作流, 治理 UI
- `.planning/research/FEATURES.md` §数据质量 — quality DSL + materialization-time evaluation (D-18, D-19); §访问控制 — RBAC + immutable audit log (D-01, D-13); §列级安全执行 — column policies + warehouse sync + downstream inheritance (D-02, D-04, D-06); §治理工作流 — submission, review, comments (D-08..D-12); §合规与审计追踪 — hash chain + GDPR/SOC2 export + retention TTL (D-13..D-16)
- `.planning/research/STACK.md` — Casbin v2.135.x; River for job queue; pgx for Postgres; ent for CRUD entities (Phase 4 D-16 dual-ORM rule applies — sqlc only for hot reads, none expected in Phase 5)

### Tech Stack & Conventions
- `CLAUDE.md` §技术栈 §授权 — Casbin v2.135.x, golang-jwt v5.3.x (already in go.mod via Phase 1)
- `CLAUDE.md` §技术栈 §执行引擎 — River v0.35.x for job queue (D-04 push job, D-21 notification dispatch, D-15 audit export)
- `CLAUDE.md` §技术栈 §可观测性 — log/slog for structured logging (notification + sync failures); Prometheus metrics counters for `audit.verify_failed`, `masking.sync_failed`, `quality.rule_failed`, `governance.review_sla_breached`
- `CLAUDE.md` §可信度评估 — Casbin v2.135.x (HIGH); ThijsKoot/openlineage-go (LOW — Phase 4 D-18 in-house translator already established; Phase 5 audit export does NOT use OpenLineage)

### External References (researcher must validate at planning time)
- Snowflake Dynamic Data Masking docs (current API surface): https://docs.snowflake.com/en/user-guide/security-column-ddm-intro
- BigQuery Column-Level Security + Data Catalog policy tags: https://cloud.google.com/bigquery/docs/column-level-security
- Pitfall #5 reference: https://mattermost.com/blog/compliance-by-design-18-tips-to-implement-tamper-proof-audit-logs/
- Pitfall #5 reference: https://dl.acm.org/doi/10.1145/3719027.3765024 (Tamper-evident logs ACM CCS 2025)
- Pitfall #8 reference: https://hoop.dev/blog/column-level-access-control-protecting-sensitive-data-one-field-at-a-time
- Pitfall #8 reference: https://www.k2view.com/blog/snowflake-dynamic-data-masking/
- Casbin Go docs (RBAC + Postgres adapter): https://casbin.org/docs/rbac and https://github.com/casbin/casbin-pg-adapter

### Phase 1–4 Code (frozen contracts Phase 5 builds on)
- `internal/auth/` — JWT, password, middleware, service. Phase 5 extends with `roles`, `role_assignments` tables + Casbin enforcer initialization in `auth.Service`.
- `internal/event/types.go` — `EventType` enum + `AllKnownTypes`. Phase 5 adds new event types per D-23 (additive only). New audit-log event types live in `internal/audit/types.go` — separate enum.
- `internal/event/writer.go` — `event.Writer` reused for non-audit events (quality.*, sla.*, notification.*, governance.materialization_blocked). New `internal/audit/writer.go` for hash-chained audit_log writes (separate library).
- `internal/connector/capability.go` — Phase 4 introduced `SchemaDescriber`. Phase 5 adds `MaskingProvisioner` (D-05) and `QueryAggregate` (for QualityRule SQLAssertion / NullCheck / RangeCheck) following the same pattern.
- `internal/connector/firstparty/` — Snowflake + BigQuery connectors gain `MaskingProvisioner`; PostgreSQL connector gains `QueryAggregate` (already has SQL execution, just exposes it as capability).
- `internal/asset/builder.go` — extend with `.ColumnPolicy(...)`, `.QualityRule(...)`, `.FreshnessSLA(...)`, `.Reviewers(...)`, `.Quorum(...)`, `.RequireHumanReview()`, `.EscalationRoles(...)`. Phase 4 already added `.Column(...).TagOverride(...)`.
- `internal/asset/fingerprint.go` — Phase 4 D-03 fingerprint. Phase 5 column_policies are NOT in code_hash (they're enforcement detail, not lineage); QualityRule definitions ARE in code_hash (rule changes = new code_hash = new governance review per D-08).
- `internal/storage/ent/` — new ent entities: `Role`, `RoleAssignment`, `ColumnPolicy`, `QualityRule`, `QualityResult`, `GovernanceReview`, `AuditLogEntry` (separate schema), `FreshnessSLABreach` (or merge into events). Migrations via Atlas as in Phases 1–4.
- `internal/lineage/` — Phase 4 lineage_writer + column_edges. Phase 5 PII propagator (D-06) lives in `internal/governance/pii_propagator.go`, called from lineage_writer's transaction.
- `internal/runtime/executor.go:368` — Phase 4 lineage capture hook. Phase 5 adds quality_evaluator hook after schema_writer (D-19) + materialization gate before runStep (D-08) + in-pipeline mask hook at AssetIO.Write boundary (D-03 RBAC-05).
- `cmd/platform/{materialize,lineage,schema,impact}.go` — existing subcommands. Phase 5 adds `audit.go`, `governance.go`, `policy.go`, `role.go`, `reconciler.go`.
- `migrations/` — new migration `20260510_phase5_governance.sql`: roles, role_assignments, column_policies, governance_reviews, quality_rules, quality_results, audit schema + audit_log + RLS grants + sentinel sequence row. Same Atlas + hand-managed CHECK pattern.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets (from Phases 1–4)
- **`internal/auth/`** — JWT middleware, user service, password handling. Phase 5 adds role-graph on top: roles → permissions via Casbin; users → roles via `role_assignments`. JWT claims gain `roles []string` for Casbin enforcer input.
- **`event.Writer` / `event.EventType` enum** — reused for non-audit events. New `internal/audit.Writer` is parallel, NOT shared, to avoid hash-chain serialization on event_log hot path.
- **`connector.Connector` + `connector.SchemaDescriber`** — frozen + Phase 4 capability. Phase 5 adds two more optional capabilities (`MaskingProvisioner`, `QueryAggregate`) using the same `interface assertion at use site` pattern (Phase 4 D-06).
- **`asset.Builder` + builder method chain** — extend additively. Existing `.Description/.Owner/.Tags/.Column/.ColumnLineage/.Schedule/.Sensor/.Partitions/.Retry/.Resource` chain unchanged. New methods: `.ColumnPolicy/.QualityRule/.FreshnessSLA/.Reviewers/.Quorum/.RequireHumanReview/.EscalationRoles`.
- **`asset_versions` ent entity (Phase 4)** — extend with `governance_state` enum column. Existing `code_hash` + `drift_status` unchanged.
- **`asset_metadata` ent entity (Phase 4 D-17)** — already has `tags JSONB`. PII tag `"pii"` is the v1 conventional tag name. PII override mechanism (D-06) writes to this table with `tag_override_reason`.
- **`column_edges` (Phase 4 D-13)** — PII propagator's traversal substrate. No schema change in Phase 5 — propagator just queries it.
- **`runs` table (Phase 2 D-17 + Phase 3 D-10)** — extend with `run_quality_status ENUM` column. `state` column unchanged.
- **`schedules` table (Phase 3 D-02)** — extend with `last_succeeded_at` column for SLA detection (D-20).
- **`event_log` (Phase 1 D-09)** — RLS-immutable. Phase 5 adds new event types per D-23 (CHECK constraint extension). Hash-chain audit goes to **separate** `audit.audit_log`, NOT this table.
- **River job queue** — already in use for Phase 2 retries / Phase 3 scheduler. Phase 5 adds `policy_sync` and `notification_dispatch` job kinds. River's transactional enqueue (per `CLAUDE.md` 选用理由) ensures policy mutations + their sync jobs commit atomically.

### Established Patterns
- **Optional capability interface (Phase 4 D-06):** Phase 5 adds `MaskingProvisioner` + `QueryAggregate` exactly the same way — separate interfaces, runtime type-assert, capability gracefully absent.
- **Three-layer config: builder default + runtime PATCH + global YAML default (Phase 4 D-17 metadata):** Phase 5 column policies (D-02) follow this exactly. Same precedence rule on read.
- **Soft-retire / temporal table (Phase 4 D-15):** Phase 5 column_policies (D-07) and role_assignments (implicit) follow same pattern — point-in-time queries for audit attestation come for free.
- **Three-surface library (Phase 4 D-19):** Phase 5 audit verify/export (D-15) and governance API (D-12) ship as Go package + REST + CLI wrapping single library. External integrations hit REST; operators get CLI for investigation; UI in Phase 6 hits the same REST.
- **Append-only event log + RLS-immutable (Phase 1 D-09):** Phase 5 audit_log extends with cryptographic hash chain on top of the same RLS pattern, in a separate Postgres schema.
- **Subcommand-per-mode binary (Phase 2 D-02):** Phase 5 adds `governance`, `audit`, `policy`, `role`, `reconciler` subcommands. Reconciler may share process with `scheduler` if planner judges complexity — both run lightweight tick loops over Postgres.
- **River-backed async work (Phase 2/3):** Phase 5 uses River for non-blocking policy_sync (D-04) and notification_dispatch (D-21). Critical: NEVER block the materialization transaction or the policy-mutation transaction on external API calls.

### Integration Points
- `internal/auth/` — add Casbin enforcer; role/role_assignment CRUD; permission middleware on REST routes.
- `internal/audit/` (NEW) — hash-chain writer with sentinel-row serialization; verify scanner; export streaming; chain anchor interface stub.
- `internal/policy/` (NEW) — column_policy CRUD; mask-type registry (Hash/Redact/Partial); YAML loader for tag→mask defaults.
- `internal/governance/` (NEW) — submission API; auto-pre-approval check pipeline; review CRUD; reviewer-pool resolution; PII propagator (called from lineage_writer).
- `internal/quality/` (NEW) — QualityRule interface; built-in rule types (NullCheck, RangeCheck, SQLAssertion); evaluator orchestrator; quality_results writer; alert dispatcher (delegates to notification subsystem).
- `internal/notification/` (NEW) — channel registry (webhook, smtp); template renderer; River job worker for dispatch.
- `internal/connector/` — extend `MaskingProvisioner` + `QueryAggregate` interfaces. Snowflake + BigQuery connectors gain MaskingProvisioner; PostgreSQL gains QueryAggregate (others as needed).
- `internal/runtime/executor.go` — three new hook points:
  - **Pre-runStep:** materialization gate (D-08) — check `governance_state = active` (skip if `governance.gating_enabled = false`).
  - **Post-schema_writer in same tx:** quality_evaluator (D-19) → run_quality_status update + quality.rule_failed event + notification job enqueue.
  - **Within AssetIO.Write:** in-pipeline mask application for non-warehouse connectors (D-03 RBAC-05) — wrapping `connector.Connector.Write` with a column-policy-aware row transformer.
- `internal/lineage/writer.go` (Phase 4) — call PII propagator (D-06) after column_edges INSERT, same transaction.
- `internal/scheduler/` (Phase 3) — extend tick loop to scan FreshnessSLA breaches (D-20), enqueue alert dispatch.
- `internal/api/` — new REST endpoints: `/audit/{verify,export}`, `/governance/{submit,reviews}`, `/policies/columns`, `/roles`, `/users/:id/roles`, `/policies/masking-sync/status`.
- `cmd/platform/{audit,governance,policy,role,reconciler}.go` (NEW) — CLI subcommand handlers.
- `migrations/20260510_phase5_governance.sql` — schema changes; CHECK constraints; RLS grants on audit schema; partial indices on column_policies/governance_reviews.

</code_context>

<specifics>
## Specific Ideas

- **Hash chain belongs in a separate table, separate Postgres schema, separate migration user.** Pitfall #5 is unambiguous: "永远不要将审计记录存储在与业务数据相同的表中。单独的表、单独的 Schema、单独的备份策略。" v1 honors this — audit_log lives in schema `audit`, owned by `audit_migrator`, INSERT-only for `platform_app`. event_log stays for high-rate platform events.
- **Hash chain scope is narrow on purpose.** Including run.* / schedule.* events in the chain would force serialization on the platform's hot path. v1 chains the 12 governance + access-control event types only — the SOC2/GDPR-attestable surface — and accepts that "did this run happen?" remains in event_log without cryptographic guarantees.
- **Casbin manages roles; column policies are platform-owned.** Casbin's strength is role hierarchies and API-permission mapping; warehouse masking sync needs row-level data the platform owns and projects to Snowflake/BigQuery. Mixing the two — putting columns inside Casbin's enforcer — creates impedance mismatch with the warehouse APIs.
- **Warehouse-native masking is the only correct execution.** Pitfall #8 is explicit: queries that bypass the platform (Snowflake worksheet, BigQuery console, JDBC) must still see masked values. The platform NEVER proxies queries — it provisions warehouse masking policies and owns drift detection. Non-warehouse connectors get in-pipeline masking at write time as the second-best alternative; this is documented as a known limitation, not a gap.
- **PII propagation is union-default with explicit override.** Any-upstream-PII => downstream-PII is the safer rule. Override requires `Reason:` non-empty and goes to the audit_log hash chain — engineers are accountable for declaring "this column is hashed at source, not reversible".
- **Auto-approval comes first, human review is the fallback.** Pitfall #7's data: 2025 industry surveys show governance bottlenecks rose 10% YoY due to manual approvals. v1 ships auto-approval check pipeline ON BY DEFAULT — schema-stable, non-PII, quality-rule-clean assets bypass humans. Risky assets get human attention they actually deserve.
- **Quality is governance, not data correctness.** Quality failures populate `run_quality_status` (independent column), not `runs.state`. Materialization succeeded; the data is in the warehouse; quality is a downstream alert. This aligns with Phase 1 D-09 + Phase 4 D-04 philosophy: metadata failures don't block data work. Conflating the two breaks Phase 2 retry semantics.
- **SLA breaches fire from scheduler, not from rule evaluation.** "Materialization didn't happen on time" can only be detected by the subsystem that knows what was expected — the scheduler. Quality rules evaluate completed runs; SLA evaluates absent runs.
- **Single binary stays single binary.** v1 ships webhook + bring-your-own SMTP only. SES, SendGrid, Slack are deferred to v1.x — users can fan webhooks to those services themselves. This honors PROJECT.md "v1 仅支持开源自托管" + CLAUDE.md "自包含" constraint.
- **Three-surface API for every feature.** Audit verify/export, governance submit/review, role/policy management — every one is Go package + REST + CLI wrapping a single library function (Phase 4 D-19). Phase 6 React UI plugs into REST; ops use CLI; external integrations use either.
- **Connector capability assertion order:** when an asset writes to a target, the platform checks: (1) does target implement `MaskingProvisioner`? if yes, masking is the warehouse's job (RBAC-04); (2) else apply in-pipeline mask at AssetIO.Write boundary (RBAC-05). This decision is made per-(asset, connector) at registration time and persisted into `column_policies.enforcement_mode` for clarity.
- **Reconciler is non-negotiable for warehouse sync.** Push-only would silently drift when DBAs or platform engineers modify warehouse state directly. Reconcile loop (default 15 min) catches this — Pitfall #8's "执行必须在所有数据端点保持一致" guarantee.

</specifics>

<deferred>
## Deferred Ideas

- **Asset-data retention TTL execution (GOV-07 partial):** v1 ships audit_log TTL infrastructure only. Asset materialized data deletion (per-connector, lineage-aware, governance-approved) is v1.x. Schema field `expires_at` reserved on `asset_versions` but unused.
- **External hash anchoring (S3 Object Lock / WORM) (Pitfall #5 optional):** v1 ships interface stub. Real implementation requires ops-level S3 + IAM + retention configuration that doesn't fit single-binary v1.
- **Background tamper-detection reconciler:** v1 has only on-demand CLI verify + REST export. Continuous-scan daemon is v1.x.
- **Auto SLA escalation (Pitfall #7 optional half):** v1 sends SLA reminders to backup-reviewer roles but does NOT auto-escalate state. SLA timer and escalation_roles config exist; auto-promote-to-second-level-reviewer is v1.x once we have real-world latency data.
- **Additional mask types:** Tokenization, format-preserving encryption, value-range bucketing — v1 ships Hash/Redact/Partial only. Type registry is open-ended; v1.x adds.
- **Additional quality rule types:** UniqueCheck, RegexCheck, FreshnessCheck-as-quality (separate from FreshnessSLA), custom Go predicate. v1 ships NullCheck/RangeCheck/SQLAssertion. Interface is stable; v1.x adds.
- **Additional notification channels:** Slack, SES, SendGrid, MS Teams, PagerDuty events API. v1 ships webhook + SMTP. v1.x adds — webhook target is sufficient stopgap (users can fan to any system).
- **PII tag classification by pattern matching (AGOV-02):** Auto-tag columns based on regex/Bloomfilter ML inference. v2 — Phase 5 v1 requires explicit tags only.
- **Row-level security (AGOV-01):** Row predicates per role. v2 — column-level is the v1 boundary.
- **SSO / OIDC (PLAT-02):** v2 — local users + invited-by-email is v1.
- **Per-asset compatibility policy / RBAC override (Phase 4 deferred):** still v1.x.
- **Custom Casbin model files (.conf) for projects with non-RBAC needs:** v1 ships fixed RBAC model. Project-customized policy models is v1.x once a real user produces a non-RBAC requirement.
- **Materialized aggregations of `audit_log` for read performance:** v1 audit verify is sequential scan; if chains exceed 10M+ rows, a checkpoint summary table makes sense. v1.x feature.
- **Reviewer per-decision rationale taxonomy (rejected_for_pii / rejected_for_quality / rejected_for_schema):** v1 ships free-text comment. Structured rejection reasons + analytics is v1.x.
- **OpenLineage-format audit export:** Out of scope — audit_log is access-control attestation, not lineage events. Phase 4 D-18 OL export covers lineage; Phase 5 audit export is JSONL/CSV with `seq` + `self_hash`.
- **Column-policy partition awareness:** Phase 4's column_edges has `partition_key`; column_policies in v1 are asset-level only. v1.x if real users want different policies per partition.
- **Reviewed Todos (not folded):** None — `gsd-tools todo match-phase 5` returned 0 matches.

</deferred>

---

*Phase: 05-governance*
*Context gathered: 2026-05-09*
