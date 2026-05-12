# Milestones

## v1.0 MVP — 2026-05-12

**Phases:** 1-6 | **Plans:** 37 | **Commits:** 247 | **Files:** 616 | **Go LOC:** 113,421 | **TS LOC:** 156

### Key Accomplishments

1. **Platform foundation** — PostgreSQL storage with Atlas migrations, event log with RLS protection, JWT auth with invite flow, stable connector ABI
2. **Asset execution engine** — Go DSL for asset definition, topological DAG execution, 50-goroutine atomic claim, exponential backoff retry, all 7 connectors
3. **Scheduling subsystem** — Cron scheduler with missed-window detection, event sensors with safeEvaluate, time/category partition backfill
4. **Lineage & Schema** — Auto-captured asset/column lineage, recursive CTE impact analysis, Schema diff with breaking-change classification
5. **Governance engine** — Casbin RBAC, hash-chain audit log, Snowflake DDM + BigQuery CLS sync, governance workflow with auto-approval
6. **Web UI + ConnectRPC API** — React SPA embedded via go:embed, asset dashboard, interactive lineage DAG, governance inbox

### Tech Debt

- main.go dependency injection gaps (GovernanceWorkflow, Enforcer, AuthMW, QualityEvaluator unwired)
- Phase 6 stubs: quality trend/alerts (QUAL-06/UI-05), AdminService (UI-07)

### Archive

- [v1.0-ROADMAP.md](./milestones/v1.0-ROADMAP.md)
- [v1.0-REQUIREMENTS.md](./milestones/v1.0-REQUIREMENTS.md)
- [v1.0-MILESTONE-AUDIT.md](./v1.0-MILESTONE-AUDIT.md)