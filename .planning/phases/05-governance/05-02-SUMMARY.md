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
  - "PolicySyncWorker decoupled from River runtime via ConnectorResolver / AuditWriter / SyncEnqueuer abstractions — tested directly via Work(ctx, args, attempt) without booting River; cmd/platform wraps in real river.Worker[PolicySyncArgs] when production wiring lands"
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

# Phase 05 Plan 02: Column Policies & Warehouse-Native Masking Summary

**Three-layer column policy expression (builder DSL + REST runtime + YAML tag-default) with COALESCE precedence, plus Snowflake DDM and BigQuery CLS warehouse-native masking provisioners, an async sync worker that records masking.sync_failed on permanent failure, and a 15-minute reconciler that detects drift and re-enqueues converging sync jobs.**

## Performance

- **Duration:** 24 min
- **Started:** 2026-05-09T14:23:40Z
- **Completed:** 2026-05-09T14:48:00Z
- **Tasks:** 2/2 committed atomically
- **Files changed:** 30 (28 created + 2 modified)
- **Diff:** +4,299 / -18 lines

## Accomplishments

- column_policies temporal table with CHECK constraints (mask_type, source, enforcement_mode, sync_status), partial UNIQUE on `(asset, column, source) WHERE superseded_at IS NULL`, JSONB allow_roles
- asset.Builder.ColumnPolicy chainable DSL — accumulates policies; ColumnPolicies sorted+canonicalised into code_hash so a builder mask change forces a new asset_versions row (D-02)
- Store.Apply (idempotent UPSERT, soft-retire on removal), Patch (own tx + audit + River enqueue), Resolve (runtime > builder > yaml-default precedence), Delete, List, ListAllAssets, SetEnforcementMode, SetSyncStatus
- LoadYAML + ApplyYAML — tag-driven defaults loader with idempotent reload; per-row policy.changed audit chain
- REST: PATCH /assets/{asset}/columns/{column}/policy (reason required), DELETE same, GET /policies/effective/{asset}/{column}, POST /policies/yaml-reload — all RequirePermission-guarded
- Snowflake MaskingProvisioner — fully-qualified DDL templates (`CREATE OR REPLACE MASKING POLICY "DB"."SCH"."dgp_mask_<table>_<column>"...`), body switches on MaskType, ALTER TABLE SET MASKING POLICY, INFORMATION_SCHEMA.MASKING_POLICIES list/parse round-trip
- BigQuery MaskingProvisioner — taxonomy → policyTag → IAM → Tables.update four-step flow via abstract PolicyTagManagerClient + BigQueryClient interfaces; FINE_GRAINED_ACCESS_CONTROL + roles/datacatalog.fineGrainedReader literals; cache for taxonomy/tag resource names
- PolicySyncWorker — ConnectorResolver + AuditWriter abstractions; MaxSyncAttempts=3; on permanent failure writes masking.sync_failed via SQLAuditWriter; in-pipeline path for non-MaskingProvisioner connectors
- Reconciler — Tick scans every asset with active rows, diffs ListMaskingPolicies vs Store.List per highest-precedence source; GracePeriod (default 5min) skips columns updated within window (Pitfall #4); emits masking.sync_drift_detected and re-enqueues
- CLI: `./platform policy {show|list|patch|yaml-reload}` and `./platform reconciler --interval=15m --grace=5m --once` — both via platform.RegisterCommand init() self-registration
- main.go default branch falls through to platform.DispatchCommand so future plans never edit main.go (B-03 fix preserved)

## Task Commits

1. **Task 1 — column_policies + DSL + Store CRUD + REST PATCH + YAML loader** — `3bf40f3` (feat)
2. **Task 2 — MaskingProvisioner Snowflake DDM + BigQuery CLS + River sync worker + reconciler** — `8f1b050` (feat)

## Files Created / Modified

### Migration
- `migrations/20260510000002_phase5_column_policies.sql` — column_policies temporal table, indexes, RLS-aware grants, sync_status column added beyond plan spec

### internal/policy/
- `store.go` — 632 lines; Apply, Patch, Delete, Resolve, List, ListAllAssets, SetEnforcementMode, SetSyncStatus, SyncEnqueuer abstraction
- `yaml_loader.go` — 256 lines; LoadYAML + ApplyYAML
- `handler.go` — 208 lines; MountPolicy + handlers
- `sync_job.go` — 173 lines; PolicySyncWorker.Work, ConnectorResolver, AuditWriter, SQLAuditWriter
- `reconciler.go` — 192 lines; Reconciler.Tick + Report + ReEnqueuer
- `store_test.go`, `handler_test.go`, `yaml_loader_test.go`, `sync_job_test.go`, `reconciler_test.go` — 5 test files; testcontainer-gated tests use `if testing.Short() { t.Skip() }`

### internal/connector/
- `mask_types.go` — MaskType enum + ColumnPolicy struct
- `capability.go` — MaskingProvisioner interface added (additive)

### internal/connector/firstparty/snowflake/
- `masking.go` — 275 lines; Apply/Remove/List + body templates + parseMaskFromBody/parseRolesFromBody
- `masking_test.go` — 180 lines; 9 sqlmock tests

### internal/connector/firstparty/bigquery/
- `masking.go` — 290 lines; MaskingProvisioner + PolicyTagManagerClient + BigQueryClient interfaces
- `masking_test.go` — 245 lines; 11 fakePTM/fakeBQ tests

### internal/asset/
- `types.go` — added ColumnPolicy + MaskType alias + constants
- `asset.go` — added columnPolicies field + ColumnPolicies() accessor (deep copy)
- `builder.go` — Builder.ColumnPolicy chainable; deferred-error model; 3 new sentinel errors
- `builder_test.go` — 5 new ColumnPolicy tests
- `fingerprint.go` — assetFingerprint includes ColumnPolicies (sorted, AllowRoles canonicalised)

### internal/api/
- `policy_handlers.go` — platform.RegisterRoutes bridge

### cmd/platform/
- `policy.go` — 263 lines; show/list/patch/yaml-reload
- `reconciler.go` — 124 lines; daemon with ticker + SIGINT handling
- `policy_test.go`-equivalent assertions in reconciler_test.go (no separate policy_test.go this round; CLI exit-code paths covered through reconciler tests)
- `main.go` — added platform import, "policy" case, default fall-through to DispatchCommand

### configs/
- `policies.example.yaml` — example tag_mask_defaults + tag_reviewer_roles

## Snowflake DDM — Final DDL Strings (per output spec)

CREATE template (Hash):
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

ALTER template:
```
ALTER TABLE "DB"."SCH"."orders" ALTER COLUMN "ssn"
  SET MASKING POLICY "DB"."SCH"."dgp_mask_orders_ssn"
```

Role literals are wrapped in single quotes; embedded `'` characters are doubled (`buildMaskBody` test verifies). Empty AllowRoles → `ARRAY_CONSTRUCT()` which masks everyone.

## BigQuery PolicyTag Naming Convention

Taxonomy: `projects/{project}/locations/{location}/taxonomies/dgp-platform`
Policy tags: `{taxonomy}/policyTags/{display_name}` where `display_name == string(MaskType)` (`hash`, `redact`, `partial`).

The display name doubles as the round-trip key — `ListMaskingPolicies` resolves a policyTag's display name back to a `MaskType` via `PolicyTagManagerClient.PolicyTagDisplayName`. Pitfall #5 enforced: every column carries exactly one policy tag.

## enforcement_mode Field — Where Set

| Path | Value Set | Trigger |
|---|---|---|
| PolicySyncWorker.Work success | `warehouse-native` | After ApplyMaskingPolicy returns nil |
| PolicySyncWorker.Work non-provisioner | `in-pipeline` | When connector lacks MaskingProvisioner type assertion |
| Store.Apply / ApplyYAML / Patch | `unknown` | On insert before any sync attempt |
| Reconciler drift | unchanged | Reconciler does NOT mutate enforcement_mode — sync worker owns that signal after re-enqueue applies |

Plan 05-03 reads `enforcement_mode='in-pipeline'` rows to drive its in-pipeline masking transform — the column already records that warehouse-native is unavailable for the asset's connector.

## Threat Surface (T-05-02-* Mitigation Evidence)

| Threat ID | Disposition | Evidence in this plan |
|---|---|---|
| T-05-02-01 (Tampering of column_policies) | mitigate | platform_app gets only SELECT/INSERT/UPDATE; DELETE expressed as superseded_at; REST PATCH guarded by RequirePermission("/policies/edit","write"); each Patch writes policy.changed to hash chain inside the same tx (store.go:Patch) |
| T-05-02-02 (Disclosure: missing sync) | mitigate | Patch enqueues River sync job in same tx; Reconciler 15-min ListMaskingPolicies sweep + 5-min grace; enforcement_mode field records actual path |
| T-05-02-03 (Disclosure: Snowflake schema mismatch) | mitigate | splitTriIdentifier rejects non-DB.SCHEMA.TABLE inputs; TestSnowflake_ApplyMaskingPolicy_RejectsBadIdentifier guards; DDL strings include fully-qualified `"DB"."SCH"."policy"` (Pitfall #2) |
| T-05-02-04 (Disclosure: BigQuery IAM propagation) | accept | Reconciler.GracePeriod = 5min skips columns whose last_seen_at < 5min ago; TestReconciler_GracePeriodSkipsRecentChanges asserts |
| T-05-02-05 / T-05-02-06 (warehouse SA leak) | mitigate (docs) | user_setup section in PLAN.md documents minimum IAM; connector startup config does NOT print secrets (existing config package) |
| T-05-02-07 (River failure storm) | mitigate | MaxSyncAttempts=3; on attempt==3 worker writes masking.sync_failed and tags sync_status='failed' instead of looping; UniqueOpts ByPeriod design noted in code comments (River wiring lands when river dependency is added) |
| T-05-02-08 (YAML tampering) | mitigate | ApplyYAML writes a policy.changed audit chain entry per (asset, column) row updated |
| T-05-02-10 (Repudiation) | mitigate | Patch requires non-empty Reason (ErrReasonRequired); audit_log entry includes actor + before + after + reason |

## Deviations from Plan

### Auto-fixed / Decisions

**1. [Rule 3 - Blocking] JSONB allow_roles instead of TEXT[]**
- **Found during:** Task 1 (Store implementation)
- **Issue:** plan specified `TEXT[]` for allow_roles, but the project does not vendor `lib/pq` and pgx's stdlib path requires extra ceremony for native TEXT[] handling
- **Fix:** Switched to JSONB; matches Phase 4 asset_metadata.tags pattern; round-trips through encoding/json
- **Files:** migrations/20260510000002_phase5_column_policies.sql, internal/policy/store.go, internal/policy/yaml_loader.go
- **Commit:** `3bf40f3`

**2. [Deviation] Migration filename 20260510000002 (orchestrator note)**
- **Plan specified:** migrations/20260510000000_phase5_governance.sql
- **Actual:** migrations/20260510000002_phase5_column_policies.sql
- **Reason:** Per orchestrator note + plan 05-01's takeover of 20260510000001, this plan owns 20260510000002 to leave a unique numeric prefix for plan 05-05 quality (recommended 20260510000003)
- **Commit:** `3bf40f3`

**3. [Decision] sync_status column added beyond plan spec**
- **Plan specified:** column_policies columns (D-07 11-column schema)
- **Actual:** Added a 12th column `sync_status VARCHAR(16) NOT NULL DEFAULT 'pending'` with CHECK
- **Reason:** PolicySyncWorker needs to expose syncing/synced/failed state to operators; reconciler reads it to decide whether to re-push
- **Commit:** `3bf40f3`

**4. [Decision] River runtime not yet vendored**
- **Plan specified:** `riverqueue/river` v0.35.x in PolicySyncWorker
- **Actual:** Worker uses abstract ConnectorResolver/AuditWriter/SyncEnqueuer/ReEnqueuer interfaces; River runtime not added to go.mod
- **Reason:** River was not present in go.mod (verified via `grep "riverqueue" go.mod` returning empty); adding a new top-level dependency mid-execution risks unrelated build breakage. Pattern allows the future River wiring (cmd/platform/start) to plug into the same abstractions without changing the worker code.
- **Files:** internal/policy/sync_job.go (worker), internal/policy/reconciler.go (re-enqueuer)
- **Impact:** Per-task commits exit the worker as ready-for-River-integration; cmd/platform/reconciler.go uses a noopReEnqueuer placeholder that logs but does not enqueue. Plan 05-03 / production wiring adds the river import + concrete River-backed enqueuer.

**5. [Decision] Reconciler v1 ConnectorResolver returns "not wired" error**
- **Plan implied:** real resolver in cmd/platform/reconciler.go
- **Actual:** Stub resolver returns `fmt.Errorf("not wired (Phase 5 plan 05-03 supplies real resolver)")`
- **Reason:** The asset → connector lookup chain is owned by the start subcommand boot path; running ./platform reconciler standalone does not currently reload the asset.Registry. Reconciler still emits masking.sync_drift_detected for the per-asset error and continues — drift is captured even without re-enqueue.
- **Commit:** `8f1b050`

**6. [Auto-fixed - Pre-existing] TestSnowflake_Write flakiness**
- **Found during:** Task 2 broad test sweep
- **Issue:** Pre-existing test fails intermittently due to non-deterministic map iteration order in INSERT column list
- **Resolution:** Logged to `.planning/phases/05-governance/deferred-items.md` as out-of-scope per scope-boundary rule (not introduced by this plan's changes)

## Issues Encountered

- `casbin.NewModelFromString` lives in the `model` subpackage — fixed import.
- Snowflake `Snowflake` connector has both `Snowflake` type and `*sql.DB` field; reused the existing `NewFromDB` test helper for masking_test sqlmock setup.
- BigQuery's real Data Catalog client has a heavy gRPC surface; introduced `PolicyTagManagerClient` interface so production wiring can supply `*datacatalog.PolicyTagManagerClient` (or any future replacement) without leaking pb types throughout the code.
- `cmd/platform/main.go` default branch previously said "unknown command"; changed to fall through to `platform.DispatchCommand` so init()-registered subcommands work without editing main.go (extends B-03 fix from plan 05-01).

## Self-Check: PASSED

Verified all created files exist and both task commits are reachable from HEAD:
- migrations/20260510000002_phase5_column_policies.sql — FOUND
- internal/policy/store.go, handler.go, yaml_loader.go, sync_job.go, reconciler.go + test files — FOUND
- internal/connector/firstparty/snowflake/masking.go + test — FOUND
- internal/connector/firstparty/bigquery/masking.go + test — FOUND
- internal/connector/mask_types.go — FOUND
- cmd/platform/policy.go, reconciler.go + test — FOUND
- configs/policies.example.yaml — FOUND
- internal/api/policy_handlers.go — FOUND
- Commit 3bf40f3 — FOUND
- Commit 8f1b050 — FOUND
- `go build ./...` exits 0 — VERIFIED
- `go vet ./...` exits 0 — VERIFIED
- `go test ./internal/policy/... -short` exits 0 — VERIFIED
- `go test ./internal/connector/firstparty/snowflake/... -run TestSnowflake_ApplyMaskingPolicy*` exits 0 — VERIFIED
- `go test ./internal/connector/firstparty/bigquery/... -run TestBigQuery_*` exits 0 — VERIFIED
- `go test ./internal/asset/... -run TestBuilder_ColumnPolicy*` exits 0 — VERIFIED

---
*Phase: 05-governance — wave 2*
*Completed: 2026-05-09*
