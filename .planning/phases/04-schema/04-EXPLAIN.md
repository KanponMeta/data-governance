# Phase 4 — Recursive CTE EXPLAIN ANALYZE Capture

Status: DEFERRED. The harness (`scripts/seed_lineage_10k.sql` + `scripts/explain_analyze_lineage.sh`) is built and ready to run. The actual capture is deferred to a manual run against a live PostgreSQL dev instance with Phase 4 migrations applied; the result must be pasted into the sections below before final ship.

Logical sign-off recorded by the user on 2026-05-09 to unblock Phase 4 closure. The capture remains an outstanding human-UAT item — surfaced via `04-HUMAN-UAT.md` (if produced by the verifier) and the phase VERIFICATION.md.

## How to Run

```bash
cd /home/developer/.kanpon/code/go/data-governance
export DATABASE_URL="postgres://platform_owner:platform_owner@localhost:5432/platform?sslmode=disable"
# (Ensure platform migrations are applied: ./platform migrate)
bash scripts/explain_analyze_lineage.sh
```

The script will overwrite this file with the actual EXPLAIN ANALYZE output.

## Verification

After running the harness, confirm each item:

- [ ] Index Scan on asset_edges_active_from / asset_edges_active_to (NOT Seq Scan)
      (indicates partial index WHERE superseded_at IS NULL is used — D-13 structural mitigation)
- [ ] Depth-10 runtime < 200ms
      (PITFALLS §4 threshold: 'if depth-10 CTE > 200ms, plan graph-DB migration')
- [ ] Depth-25 runtime < 1000ms
      (acceptable upper bound for the hard-cap edge case — not the hot path)
- [ ] No CTE materialization fence ('CTE Scan' + 'Materialize' in plan output)

Verified by: kanpon (logical approval — actual capture deferred)
Date: 2026-05-09
