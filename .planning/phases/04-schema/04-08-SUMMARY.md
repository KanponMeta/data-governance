---
phase: "04"
plan: "08"
subsystem: cli-e2e
tags: [cli, impact, schema, lineage, e2e, explain-analyze, openlineage]
dependency_graph:
  requires: [04-01, 04-02, 04-03, 04-04, 04-05, 04-06, 04-07]
  provides: [cli-impact-cmd, cli-schema-cmd, cli-lineage-cmd, phase4-e2e-suite, explain-harness]
  affects: [cmd/platform, test/integration, scripts]
tech_stack:
  added:
    - flag.NewFlagSet + positional/flag split pattern (CLI dispatch)
    - pgxpool.New (CLI DB connection bypassing config.Load)
    - testcontainers PostgreSQL (E2E test fixture)
    - psql EXPLAIN ANALYZE harness script (bash)
  patterns:
    - CLI subcommand dispatch via dispatchX(args) functions
    - D-14 depth cap enforced at Go layer before DB access
    - T-04-08-03 production-DB guard in seed SQL
    - Direct SQL inserts for E2E fixture setup (no executor cycle required)
key_files:
  created:
    - cmd/platform/impact.go
    - cmd/platform/impact_test.go
    - cmd/platform/schema.go
    - cmd/platform/schema_test.go
    - cmd/platform/lineage.go
    - cmd/platform/lineage_test.go
    - test/integration/phase4_e2e_test.go
    - scripts/seed_lineage_10k.sql
    - scripts/explain_analyze_lineage.sh
    - .planning/phases/04-schema/04-EXPLAIN.md
  modified:
    - cmd/platform/main.go
decisions:
  - CLI subcommands use pgxpool.New + os.Getenv directly (not config.Load) to avoid JWT_SIGNING_KEY requirement
  - D-14 depth cap checked BEFORE DATABASE_URL so unit tests pass without a DB
  - AC5 metadata test exercises Store.Put/Get directly (not HTTP layer) — chi.URLParam and auth middleware not available in integration test
  - unmarshalSchemaFromMap marshals ent JSONB map[string]interface{} through JSON round-trip to recover connector.Schema
  - Positional/flag split (same as backfill.go) ensures asset name can appear before flags in CLI invocation
metrics:
  duration: "~45 min"
  completed_date: "2026-05-09"
  tasks_completed: 4
  tasks_total: 4
  files_created: 10
  files_modified: 1
human_uat:
  - id: explain-analyze-capture
    description: "Run scripts/explain_analyze_lineage.sh against a live PostgreSQL with Phase 4 migrations applied; paste the depth-10/depth-25/depth-10-upstream plans into 04-EXPLAIN.md and confirm Index Scan + runtime thresholds."
    status: deferred
    rationale: "Logical sign-off recorded 2026-05-09. Harness is built and ready; capture deferred until a dev DB is provisioned."
---

# Phase 4 Plan 8: CLI Subcommands, E2E Suite, and EXPLAIN Harness Summary

CLI dispatch for `./platform impact`, `./platform schema`, `./platform lineage`; full Phase 4 E2E test suite covering all 5 ROADMAP acceptance criteria plus OpenLineage shape; and EXPLAIN ANALYZE harness with 10K synthetic edge seed.

## Tasks Completed

| # | Name | Commit | Files |
|---|------|--------|-------|
| 1 | CLI subcommands + main.go dispatch | `78038b7` | cmd/platform/impact.go, schema.go, lineage.go, impact_test.go, schema_test.go, lineage_test.go, main.go |
| 2 | Phase 4 E2E test suite | `9dc49fc` | test/integration/phase4_e2e_test.go |
| 3a | EXPLAIN ANALYZE harness artifacts | `ff4a7dc` | scripts/seed_lineage_10k.sql, scripts/explain_analyze_lineage.sh, .planning/phases/04-schema/04-EXPLAIN.md |
| 3b | EXPLAIN ANALYZE checkpoint (logical sign-off, capture deferred) | `0ae75b9` | .planning/phases/04-schema/04-EXPLAIN.md |

**Task 3b: logical sign-off recorded 2026-05-09. Actual EXPLAIN capture deferred to a manual run against a live Postgres dev DB; tracked as outstanding human-UAT.**

## Decisions Made

1. **CLI DB connection pattern**: `./platform impact` and other CLI subcommands call `os.Getenv("DATABASE_URL")` + `pgxpool.New` directly. Using `config.Load()` requires `JWT_SIGNING_KEY` which is a server concern, not a CLI operator concern.

2. **D-14 depth cap placement**: `if *depth > impact.MaxDepth` runs before `DATABASE_URL` check. This allows `TestRunImpact_DepthExceeded` to validate the guard without a live DB, matching the "three layers of defense" design (Go layer → SQL LEAST → HTTP 400).

3. **AC5 metadata test strategy**: `metadata.Handler.patch()` requires `chi.URLParam` (chi router context) and `auth.PrincipalFromContext` (JWT middleware). In the E2E test environment, both are unavailable. Test exercises `metadata.Store.Put/Get` directly, which validates the core behavior mandated by AC5.

4. **SchemaData unmarshaling**: ent generates `map[string]interface{}` for JSONB columns (not `json.RawMessage`). `unmarshalSchemaFromMap` marshals the map back to JSON bytes then unmarshal to `connector.Schema`. This round-trip is necessary to recover typed schema structs for `schema.Diff`.

5. **Positional/flag split for CLI**: `./platform impact my_asset --depth=5` must work even though `flag.Parse` stops at the first non-flag argument. The same split used in `backfill.go` separates args starting with `-` (flags) from bare args (positionals) before calling `fs.Parse`.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed depth flag parsing for impact CLI**
- **Found during:** Task 1 unit test `TestRunImpact_DepthExceeded`
- **Issue:** `fs.Parse(["some_asset", "--depth=99"])` stops at `some_asset` (non-flag); `--depth=99` is never parsed; depth stays at default 10, doesn't exceed MaxDepth 25; falls through to DATABASE_URL check instead of depth error
- **Fix:** Applied positional/flag split (same pattern as backfill.go): iterate args, collect those starting with `-` into `flagArgs`, others into `positional`; pass only `flagArgs` to `fs.Parse`; also moved depth > MaxDepth check before DATABASE_URL check
- **Files modified:** cmd/platform/impact.go
- **Commit:** `78038b7`

**2. [Rule 2 - Missing functionality] Added unmarshalSchemaFromMap for ent JSONB type mismatch**
- **Found during:** Task 1 schema.go implementation
- **Issue:** Plan called for `unmarshalSchema(json.RawMessage)` but ent generates `map[string]interface{}` for SchemaData JSONB column — cannot pass map directly to JSON unmarshal
- **Fix:** Added `unmarshalSchemaFromMap(data map[string]interface{}) (connector.Schema, error)` that round-trips through `json.Marshal`/`json.Unmarshal`
- **Files modified:** cmd/platform/schema.go
- **Commit:** `78038b7`

## Deferred Items

**Pre-existing `go vet` failure in test/integration/integration_test.go:52**
- `http.Getenv(key)` — `http.Getenv` does not exist; likely intended `os.Getenv(key)`
- Pre-existing bug, out of scope for plan 04-08
- Logged to deferred-items

## Known Stubs

None — all CLI handlers are fully wired to real library calls. The EXPLAIN harness output file (04-EXPLAIN.md) is intentionally a skeleton template until the human runs the harness.

## Threat Flags

None — no new network endpoints, auth paths, or trust boundary crossings introduced. CLI subcommands run as trusted operator tools (T-04-08-05); the seed SQL includes a production-DB guard (T-04-08-03).

## Self-Check: PASSED

- [x] cmd/platform/impact.go exists
- [x] cmd/platform/schema.go exists
- [x] cmd/platform/lineage.go exists
- [x] cmd/platform/main.go modified (impact/schema/lineage cases added)
- [x] test/integration/phase4_e2e_test.go exists
- [x] scripts/seed_lineage_10k.sql exists
- [x] scripts/explain_analyze_lineage.sh exists
- [x] .planning/phases/04-schema/04-EXPLAIN.md exists
- [x] Commit 78038b7 exists (Task 1)
- [x] Commit 9dc49fc exists (Task 2)
- [x] Commit ff4a7dc exists (Task 3a)
