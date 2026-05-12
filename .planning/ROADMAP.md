# Roadmap: 数据治理平台

## Milestones

- ✅ **v1.0 MVP** — Phases 1-6 (shipped 2026-05-12)
- 🚧 **v1.1 Wiring Fixes** — Fix main.go dependency injection gaps (planned)
- 📋 **v2.0 SDK Ecosystem** — Python SDK, dbt integration (planned)

## Phases

<details>
<summary>✅ v1.0 MVP (Phases 1-6) — SHIPPED 2026-05-12</summary>

- [x] Phase 1: 基础设施 (5/5 plans) — completed 2026-05-06
- [x] Phase 2: 执行引擎 (5/5 plans) — completed 2026-05-08
- [x] Phase 3: 调度、传感器与分区 (7/7 plans) — completed 2026-05-08
- [x] Phase 4: 血缘与 Schema (8/8 plans) — completed 2026-05-09
- [x] Phase 5: 治理引擎 (5/5 plans) — completed 2026-05-10
- [x] Phase 6: Web UI 与 API (7/7 plans) — completed 2026-05-12

See [.planning/milestones/v1.0-ROADMAP.md](./milestones/v1.0-ROADMAP.md) for full phase details.

</details>

### 🚧 v1.1 Wiring Fixes (In Progress)

- [ ] Phase 7: Fix main.go dependency injection (GovernanceWorkflow, Enforcer, AuthMW, QualityEvaluator, GovernanceGatingEnabled)
- [ ] Phase 8: Wire Phase 6 stubs (quality trend, alerts, admin policies)

### 📋 v2.0 SDK Ecosystem (Planned)

- [ ] Phase 9: Python SDK — Python asset definitions registered to Go platform
- [ ] Phase 10: dbt integration — dbt models as first-class assets

## Progress

| Phase | Milestone | Plans Complete | Status | Completed |
|-------|-----------|-----------------|--------|-----------|
| 1. 基础设施 | v1.0 | 5/5 | Complete | 2026-05-06 |
| 2. 执行引擎 | v1.0 | 5/5 | Complete | 2026-05-08 |
| 3. 调度、传感器与分区 | v1.0 | 7/7 | Complete | 2026-05-08 |
| 4. 血缘与 Schema | v1.0 | 8/8 | Complete | 2026-05-09 |
| 5. 治理引擎 | v1.0 | 5/5 | Complete | 2026-05-10 |
| 6. Web UI 与 API | v1.0 | 7/7 | Complete | 2026-05-12 |
| 7. Wiring Fixes | v1.1 | 0/? | Not started | - |
| 8. Stub Wiring | v1.1 | 0/? | Not started | - |
| 9. Python SDK | v2.0 | 0/? | Not started | - |
| 10. dbt Integration | v2.0 | 0/? | Not started | - |

---

*Last updated: 2026-05-12 after v1.0 milestone*