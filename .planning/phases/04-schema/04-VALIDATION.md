---
phase: 4
slug: schema
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-05-08
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

> Filled by gsd-planner during plan creation; the planner MUST emit one row per task with the test command and expected file artifact.

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 4-XX-XX | XX | N | REQ-XX | T-4-XX / — | (planner fills) | unit/integration | `(planner fills)` | ✅ / ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

> Wave 0 lands shared test infrastructure before any Phase 4 implementation tasks. Planner must include these in the first wave.

- [ ] `internal/lineage/lineagetest/fixtures.go` — shared lineage fixtures (asset def → expected static edges)
- [ ] `internal/schema/schematest/fixtures.go` — Schema A/B pairs covering every change_type enum value (drop/add/narrow/widen/null-toggle/pk-change)
- [ ] `internal/lineage/lineagetest/recursive_cte_seed.go` — DAG seeder for recursive CTE traversal tests (depths 1, 5, 10, 25, 26)
- [ ] `internal/runtime/executortest/lineage_helpers.go` — testcontainers PostgreSQL helper that loads Phase 4 migrations + Phase 1–3 base schema
- [ ] `migrations/2026MMDDHHMMSS_phase4_*.up.sql` skeleton (empty stubs) — so Atlas roundtrip tests don't fail Wave 0

*If wave 0 unnecessary: "Existing infrastructure covers all phase requirements." — DOES NOT APPLY here, six new tables require fresh fixtures.*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| `EXPLAIN ANALYZE` plan for recursive CTE traversal at depth=10 / depth=25 against a 10K-edge synthetic graph stays within target latency | LINE-06 | Production-shape data isn't in unit tests; synthetic graph must be hand-seeded and inspected | (1) `psql -f scripts/seed_lineage_10k.sql`; (2) `psql -c 'EXPLAIN ANALYZE WITH RECURSIVE ... LIMIT 25'`; (3) record cost in `.planning/phases/04-schema/04-EXPLAIN.md` |
| OpenLineage export hand-validation against published schema | LINE-01 / D-18 | Spec compliance is interpretive; one human pass against `https://openlineage.io/spec/2-0-2/RunEvent.json` is required | `./platform lineage export --asset=demo --since=...  --format=openlineage > /tmp/ol.json && check-jsonschema --schemafile RunEvent.json /tmp/ol.json` |
| `./platform schema diff --asset=X --from=v1 --to=v2` human-readable output | META-02 | Output is for operator consumption; readability is subjective | Run command, eyeball output, paste into PR description |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 180s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
