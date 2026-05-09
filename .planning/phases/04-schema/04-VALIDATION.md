---
phase: 4
slug: schema
status: ready
nyquist_compliant: true
wave_0_complete: true
created: 2026-05-08
updated: 2026-05-09
---

# Phase 4 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (stdlib) + testcontainers-go for integration |
| **Config file** | none — go.mod handles all deps |
| **Quick run command** | `go test ./internal/lineage/... ./internal/schema/... ./internal/metadata/... ./internal/asset/...` |
| **Full suite command** | `go test ./... -tags=integration` |
| **Estimated runtime** | ~30s quick, ~3 min full (testcontainers PostgreSQL spin-up dominates) |

---

## Sampling Rate

- **After every task commit:** Run quick command (unit tests for the touched package)
- **After every plan wave:** Run full suite command
- **Before `/gsd-verify-work`:** Full suite must be green
- **Max feedback latency:** 30 seconds (quick); 3 minutes (full)

---

## Per-Task Verification Map

> One row per task across plans 04-01..04-08. Automated Command is the `<verify><automated>` block from each task. Status is updated as plans execute.

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 4.1.1 | 04-01 | 1 | LINE-01/02/03/06 + META-01/02/03/05 | T-04-01-01 | Test fixtures are non-runtime; D-09 ChangeKind enumerated | unit | `go build ./internal/lineage/lineagetest/... ./internal/schema/schematest/... && go test ./internal/lineage/lineagetest/... ./internal/schema/schematest/... -run Smoke -count=1 -timeout 30s` | ✅ W0 | ⬜ pending |
| 4.1.2 | 04-01 | 1 | LINE-01/02/03/06 + META-01/02/03/05 | T-04-01-02 | testcontainers helper has Cleanup hook; migration stub passes lint | unit + integration | `go build ./internal/runtime/executortest/... && make migrate-lint && grep -c 'Phase 4 lineage + schema migration' migrations/20260509120000_phase4_lineage_schema.sql` | ✅ W0 | ⬜ pending |
| 4.2.1 | 04-02 | 2 | LINE-01/02/03 + META-01/02/05 (D-07/11/13/15) | T-04-02-01 | ent entities + migration adopts immutable + soft-retire fields | unit | `go build ./... && grep -c 'CREATE TABLE' migrations/20260509120000_phase4_lineage_schema.sql` | ✅ W0 | ⬜ pending |
| 4.2.2 | 04-02 | 2 | D-09 / D-21 RLS-immutability extension | T-04-02-02 | event_log CHECK constraint extended; partial unique indices | unit | `make migrate-lint && grep -c "'lineage.captured'\|'schema.captured'\|'metadata.updated'" migrations/20260509120000_phase4_lineage_schema.sql` | ✅ W0 | ⬜ pending |
| 4.2.3 | 04-02 | 2 | D-07 (connector.Schema) + D-21 (event types) | — | Schema/Column types compile; AllKnownTypes covers Phase 4 | unit | `go build ./... && go test ./internal/event/... -run TestAllPhase4EventTypes -count=1 -timeout 30s && go test ./internal/connector/... -run TestSchemaTypeShape -count=1 -timeout 30s` | ✅ W0 | ⬜ pending |
| 4.3.1 | 04-03 | 3 | LINE-02 / META-03 (D-02/03/17) | T-04-03-01 | Builder DSL extensions + ColumnLineage + CodeHash | unit | `go build ./... && go test ./internal/asset/... -count=1 -timeout 60s` | ✅ W0 | ⬜ pending |
| 4.3.2 | 04-03 | 3 | D-03 fingerprint stability | — | Fingerprint deterministic + concurrent-safe | unit (-race) | `go build ./... && go test -race ./internal/asset/... -count=1 -timeout 60s` | ✅ W0 | ⬜ pending |
| 4.3.3 | 04-03 | 3 | META-01 (D-05 SchemaDescriber) | T-04-03-02 | Postgres SchemaDescriber via information_schema (no string interpolation) | unit + integration | `go build ./... && go test ./internal/connector/firstparty/postgres/... -count=1 -timeout 60s` | ✅ W0 | ⬜ pending |
| 4.4.0 | 04-04 | 4 | D-04 platform-driven drift | T-04-04-02 | trackingIO records all reads regardless of result; concurrent-safe | unit (-race) | `go build ./... && go test -race ./internal/asset/... -run TestTrackingIO -count=1 -timeout 30s` | ✅ W0 | ⬜ pending |
| 4.4.1 | 04-04 | 4 | LINE-01/02 (D-01/02/04/15) | T-04-04-01 / T-04-04-03 | SyncStaticEdges idempotent + soft-retire; CaptureRun observed-vs-declared drift | unit + integration | `go build ./... && make migrate-lint && go test ./internal/lineage/... ./internal/asset/... -run 'TestSyncStaticEdgesNoUpstreams\|TestRegistryOnRegister' -count=1 -timeout 30s` | ✅ W0 | ⬜ pending |
| 4.4.2 | 04-04 | 4 | META-01 (D-05/06/08) | T-04-04-05 / T-04-04-07 | HashSchema deterministic + ignore volatile; SchemaDescriber capability fallback | unit (-race) | `go build ./... && go test -race ./internal/schema/... -run 'TestHashSchema\|TestCaptureUnsupported\|TestCaptureDescriberError' -count=1 -timeout 30s` | ✅ W0 | ⬜ pending |
| 4.4.3 | 04-04 | 4 | D-21 transactional boundary | T-04-04-06 | trackingIO wired in executor; commitSuccess uses BeginTx + Rollback on lineage error | integration (-race) | `go build ./... && go test -race ./internal/runtime/... -count=1 -timeout 60s` | ✅ W0 | ⬜ pending |
| 4.5.1 | 04-05 | 5 | META-02 (D-09) | — | Diff + IsWideningPostgres + Classify pure-Go; out-of-lattice → breaking safe default | unit (-race) | `go build ./... && go test -race ./internal/schema/... -run 'TestDiff\|TestIsWideningPostgres\|TestClassify' -count=1 -timeout 30s` | ✅ W0 | ⬜ pending |
| 4.5.2 | 04-05 | 5 | META-02 (D-11) audit-pointer pattern | T-04-05-01 | WriteSchemaChanges INSERTs one row per change; tx atomic with schema_versions | integration (-race) | `go build ./... && go test -race ./internal/schema/... -count=1 -timeout 60s` | ✅ W0 | ⬜ pending |
| 4.6.1 | 04-06 | 6 | LINE-03 / LINE-06 (D-14/16) | T-04-06-02 / T-04-06-07 | sqlc parameterized queries; LEAST(@max_depth::int, 25) SQL-level cap; cycle guard | unit + tooling | `make sqlc && make sqlc-verify && go build ./...` | ✅ W0 | ⬜ pending |
| 4.6.2 | 04-06 | 6 | LINE-06 (D-19/20) | T-04-06-01 / T-04-06-03 / T-04-06-05 | impact.Analyze depth-cap (Go layer); SQL cap independently enforced (TestCTEMaxDepthSQLEnforced) | unit + integration | `go build ./... && go test -race ./internal/lineage/impact/... -count=1 -timeout 30s` | ✅ W0 | ⬜ pending |
| 4.7.1 | 04-07 | 7 | META-03 (D-17/D-21) | T-04-07-04 / T-04-07-06 | Metadata store INSERT-only; tag-cap MaxTags=64; RequireRole gate | unit | `go build ./... && go test -race ./internal/metadata/... -count=1 -timeout 60s && go test -race ./internal/event/... -run TestAllKnownTypes -count=1 -timeout 30s` | ✅ W0 | ⬜ pending |
| 4.7.2 | 04-07 | 7 | META-02 (D-10/11/12) | T-04-07-03 / T-04-07-07 | Schema-ack only mutates ack columns; reason required; already-acked → 409 | unit | `go test -race ./internal/api/... -run 'TestAck\|TestListChanges' -count=1 -timeout 60s` | ✅ W0 | ⬜ pending |
| 4.7.3 | 04-07 | 7 | LINE-01 / LINE-06 (D-18/19/20) | T-04-07-01 / T-04-07-02 / T-04-07-05 | depth_exceeded → 400; assetNameRE; OL translator point-in-time predicate (D-15) | unit | `go test -race ./internal/lineage/openlineage/... ./internal/api/... -run 'TestTranslate\|TestImpact\|TestExport\|TestRunEvent' -count=1 -timeout 60s` | ✅ W0 | ⬜ pending |
| 4.7.4 | 04-07 | 7 | All Phase 4 REST surfaces | T-04-07-08 / T-04-07-10 | RequireRole on writes; routes mounted under JWT group | unit + boot | `go build ./... && go test -race ./internal/api/... -run 'TestRouter\|TestNewRouter' -count=1 -timeout 60s` | ✅ W0 | ⬜ pending |
| 4.8.1 | 04-08 | 8 | All Phase 4 CLI surfaces (D-10/14/18/19/20) | — | CLI subcommand dispatch + flag validation paths (no DB needed) | unit (-race) | `go build ./... && go test -race ./cmd/platform/... -run 'TestRun(Impact\|SchemaAckBreak\|SchemaDiff\|LineageExport)\|TestDispatch(Schema\|Lineage)' -count=1 -timeout 60s` | ✅ W0 | ⬜ pending |
| 4.8.2 | 04-08 | 8 | All five ROADMAP AC + OpenLineage round-trip | T-04-08-02 | testcontainers full-stack tests for AC1–AC5 + OL shape | integration (-race) | `go build -tags=integration ./... && go test -tags=integration -race ./test/integration/... -run TestPhase4 -count=1 -timeout 10m` | ✅ W0 | ⬜ pending |
| 4.8.3 | 04-08 | 8 | D-14 EXPLAIN ANALYZE harness | T-04-08-03 | Production-DB guard in seed SQL; harness `set -euo pipefail`; INSERT INTO asset_edges present | tooling | `test -f scripts/seed_lineage_10k.sql && test -f scripts/explain_analyze_lineage.sh && test -x scripts/explain_analyze_lineage.sh && test -f .planning/phases/04-schema/04-EXPLAIN.md && bash -n scripts/explain_analyze_lineage.sh && grep -q "current_database" scripts/seed_lineage_10k.sql && grep -q "INSERT INTO asset_edges" scripts/seed_lineage_10k.sql && grep -q "set -euo pipefail" scripts/explain_analyze_lineage.sh` | ✅ W0 | ⬜ pending |
| 4.8.4 | 04-08 | 8 | D-14 EXPLAIN ANALYZE human review | — | Index Scan confirmed; runtime under thresholds; human-signed | manual + integration check | `test -f .planning/phases/04-schema/04-EXPLAIN.md && grep -q "Verified by:" .planning/phases/04-schema/04-EXPLAIN.md && ! grep -q "Verified by: (pending)" .planning/phases/04-schema/04-EXPLAIN.md && grep -q "Index Scan\|Seq Scan" .planning/phases/04-schema/04-EXPLAIN.md` | ⏳ post-W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

**Sampling continuity:** Every implementation task has `<automated>` verify or runs after Wave 0 (04-01) provides fixtures. No 3-task gaps without automation. Task 4.8.4 is the single human-verify checkpoint per VALIDATION manual-only verifications.

---

## Wave 0 Requirements

> Wave 0 (= plan 04-01) lands shared test infrastructure before any Phase 4 implementation tasks.

- [x] `internal/lineage/lineagetest/fixtures.go` — shared lineage fixtures (asset def → expected static edges) — **04-01 task 1**
- [x] `internal/schema/schematest/fixtures.go` — Schema A/B pairs covering every change_type enum value (drop/add/narrow/widen/null-toggle/pk-change) — **04-01 task 1**
- [x] `internal/lineage/lineagetest/recursive_cte_seed.go` — DAG seeder for recursive CTE traversal tests (depths 1, 5, 10, 25, 26) — **04-01 task 1**
- [x] `internal/runtime/executortest/lineage_helpers.go` — testcontainers PostgreSQL helper that loads Phase 4 migrations + Phase 1–3 base schema — **04-01 task 2**
- [x] `migrations/20260509120000_phase4_*.up.sql` skeleton (empty stubs) — so Atlas roundtrip tests don't fail Wave 0 — **04-01 task 2** (`migrations/20260509120000_phase4_lineage_schema.sql`)

*Wave 0 is COMPLETE: plan 04-01 ships fixtures + helpers; plan 04-04 task 0 additionally lands the trackingIO decorator that the executor needs in plan 04-04 task 3 (D-04 platform-driven drift detection).*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| `EXPLAIN ANALYZE` plan for recursive CTE traversal at depth=10 / depth=25 against a 10K-edge synthetic graph stays within target latency | LINE-06 | Production-shape data isn't in unit tests; synthetic graph must be hand-seeded and inspected | (1) `psql -f scripts/seed_lineage_10k.sql`; (2) `bash scripts/explain_analyze_lineage.sh`; (3) inspect `.planning/phases/04-schema/04-EXPLAIN.md` for Index Scan + runtime thresholds; sign "Verified by:" line. Captured by task 4.8.4 (checkpoint:human-verify). |
| OpenLineage export hand-validation against published schema | LINE-01 / D-18 | Spec compliance is interpretive; one human pass against `https://openlineage.io/spec/2-0-2/RunEvent.json` is required | `./platform lineage export --asset=demo --since=...  --format=openlineage > /tmp/ol.json && check-jsonschema --schemafile RunEvent.json /tmp/ol.json` |
| `./platform schema diff --asset=X --from=v1 --to=v2` human-readable output | META-02 | Output is for operator consumption; readability is subjective | Run command, eyeball output, paste into PR description |

---

## Validation Sign-Off

- [x] All tasks have `<automated>` verify or Wave 0 dependencies
- [x] Sampling continuity: no 3 consecutive tasks without automated verify
- [x] Wave 0 covers all MISSING references (04-01 + 04-04 task 0)
- [x] No watch-mode flags
- [x] Feedback latency < 180s (quick suite < 30s, full suite < 5m)
- [x] `nyquist_compliant: true` set in frontmatter

**Approval:** ready-for-execution
