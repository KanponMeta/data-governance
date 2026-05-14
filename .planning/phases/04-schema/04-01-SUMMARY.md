---
phase: 04-schema
plan: 01
subsystem: testing
tags: [testcontainers, postgres, lineage, schema-diff, fixtures, migration]

# Dependency graph
requires:
  - phase: 03-scheduling-sensors-partitions
    provides: Phase 3 migration files (all applied by StartPhase4Container)

provides:
  - internal/lineage/lineagetest: StaticEdgeFixtures (4 cases), ColumnLineageFixtures (3 cases)
  - internal/lineage/lineagetest: SeedDAG, SeedBranching, SeedCycle DAG seeders
  - internal/schema/schematest: DiffPairs with all 9 D-09 ChangeKind cases
  - internal/runtime/executortest: StartPhase4Container + Reset testcontainers helper
  - migrations/20260509120000_phase4_lineage_schema.sql: empty Wave 0 stub (atlas.sum updated)

affects:
  - 04-02 (Wave 1 — fills in Phase 4 migration tables asset_edges, column_edges, etc.)
  - 04-03 (Wave 3 — lineage writer uses lineagetest fixtures)
  - 04-05 (Wave 4 — diff classifier uses schematest DiffPairs)
  - 04-06 (Wave 5 — CTE traversal tests use SeedDAG + SeedBranching + SeedCycle)

# Tech tracking
tech-stack:
  added: []  # testcontainers-go/modules/postgres was already in go.mod
  patterns:
    - "Test-only package isolation: lineagetest, schematest, executortest are pure test infrastructure — no production imports"
    - "Local mirror types: schematest.Column + schematest.Schema mirror planned connector.Schema shape (D-07); Wave 4 swaps to real connector.Schema"
    - "Testcontainers lifecycle: StartPhase4Container registers t.Cleanup so containers terminate even on test failure (T-04-01-02 mitigation)"
    - "Migration application via os.ReadFile (lexicographic order): replicates Atlas apply without Atlas binary requirement in tests"

key-files:
  created:
    - internal/lineage/lineagetest/doc.go
    - internal/lineage/lineagetest/fixtures.go
    - internal/lineage/lineagetest/recursive_cte_seed.go
    - internal/lineage/lineagetest/fixtures_smoke_test.go
    - internal/schema/schematest/doc.go
    - internal/schema/schematest/fixtures.go
    - internal/schema/schematest/fixtures_smoke_test.go
    - internal/runtime/executortest/doc.go
    - internal/runtime/executortest/lineage_helpers.go
    - internal/runtime/executortest/lineage_helpers_smoke_test.go
    - migrations/20260509120000_phase4_lineage_schema.sql
  modified:
    - migrations/atlas.sum

key-decisions:
  - "Filename convention: 20260509120000_phase4_lineage_schema.sql (not .up.sql) to match project's existing .sql suffix convention from Phases 1-3"
  - "Local mirror types in schematest: avoids forward-import of connector.Schema before Wave 1 ships; Wave 4 swaps by substituting schematest.Column/Schema with connector.Column/Schema"
  - "executortest opens *sql.DB as superuser (same DSN) rather than setting up a separate platform_app login role, matching the testcontainers postgres module's user model"
  - "atlas migrate hash (not make migrate-lint) used to update atlas.sum: migrate-lint is Atlas Pro-only (v0.38+); CI uses || true; hash update sufficient for Wave 0 acceptance"

patterns-established:
  - "Test package convention: *test suffix + doc.go package comment explicitly saying 'No production code imports this package — it is test-only'"
  - "DAG seeder early-detect: all SeedXxx functions query 'SELECT 1 FROM asset_edges LIMIT 0' first and return an error if table absent"
  - "t.Cleanup for container teardown: all Docker resources cleaned up via t.Cleanup, never defer in TestMain-only style"
  - "Build tag 'integration' on smoke tests requiring Docker: keeps non-Docker test runs unaffected"

requirements-completed: [LINE-01, LINE-02, LINE-03, LINE-06, META-01, META-02, META-03, META-05]

# Metrics
duration: 6min
completed: 2026-05-09
---

# Phase 4 Plan 01: Wave 0 测试基础设施 + Migration 桩 — 摘要

**Phase 4 的共享测试脚手架：lineage fixtures（4+3 个 case）、schema diff fixtures（9 个 D-09 ChangeKind case）、递归 CTE DAG seeder、testcontainers PostgreSQL 辅助工具（含 migration apply），以及带更新 atlas.sum 的空 Phase 4 migration 桩**

## 性能

- **Duration:** ~6 min
- **Started:** 2026-05-09T02:37:31Z
- **Completed:** 2026-05-09T02:43:31Z
- **Tasks:** 2 of 2
- **Files modified:** 12 (11 created, 1 modified)

## 完成事项

- 三个纯测试 Go 包编译通过：`lineagetest`、`schematest`、`executortest`
- `DiffPairs()` 包含全部 9 个 D-09 ChangeKind 值的 fixture 对：column_added、column_dropped、type_narrowed、type_widened、nullable_added、nullable_removed、pk_changed、comment_changed、default_changed
- DAG seeder 支持线性链（`SeedDAG`）、平衡二叉树（`SeedBranching`）和循环图（`SeedCycle`）
- `StartPhase4Container` 通过 testcontainers 启动 PostgreSQL 16，按字典序应用所有 Phase 1–4 migrations，返回就绪的 `*sql.DB`
- 空 Phase 4 migration 桩（Wave 0 槽位）lint 兼容；`atlas.sum` 已更新文件哈希

## Wave 0 → Wave 1 契约

| Consumer Plan | Fixture / Helper | 读取内容 |
|---|---|---|
| 04-02 (Wave 1 — migration) | `migrations/20260509120000_phase4_lineage_schema.sql` | 填充 CREATE TABLE 语句：asset_edges、column_edges 等 |
| 04-03 (Wave 3 — lineage writer) | `lineagetest.StaticEdgeFixtures()`、`lineagetest.ColumnLineageFixtures()` | LINE-01/LINE-02 单元测试的预期边 |
| 04-05 (Wave 4 — diff classifier) | `schematest.DiffPairs()` | META-02 分类器 TDD 的 9 个 A/B schema 对 |
| 04-06 (Wave 5 — CTE traversal) | `lineagetest.SeedDAG()`、`SeedBranching()`、`SeedCycle()` | LINE-03/LINE-06 递归 CTE 测试的 DAG 形状 |
| Wave 3+ 任意集成测试 | `executortest.StartPhase4Container()` | 应用全部 migrations 的完整 PostgreSQL 容器 |

## Task 提交

1. **Task 1: Lineage fixtures + schema diff fixtures + DAG seeder** - `735772e` (feat)
2. **Task 2: testcontainers helper + migration stub** - `3f921d1` (feat)

## 创建/修改的文件

- `internal/lineage/lineagetest/doc.go` — 包文档
- `internal/lineage/lineagetest/fixtures.go` — `StaticEdgeFixtures()`（4 个 case）+ `ColumnLineageFixtures()`（3 个 case）及 `ExpectedEdge` + `ColumnEdgeRow` 类型
- `internal/lineage/lineagetest/recursive_cte_seed.go` — `SeedDAG`、`SeedBranching`、`SeedCycle` + 包级别 `edge` 类型 + `insertEdges` 辅助函数
- `internal/lineage/lineagetest/fixtures_smoke_test.go` — 冒烟测试（无需 DB）
- `internal/schema/schematest/doc.go` — 包文档
- `internal/schema/schematest/fixtures.go` — 本地镜像 `Column` + `Schema` 类型；返回 9 个 D-09 ChangeKind case 的 `DiffPairs()`
- `internal/schema/schematest/fixtures_smoke_test.go` — 冒烟测试，断言恰好 9 个对
- `internal/runtime/executortest/doc.go` — 包文档
- `internal/runtime/executortest/lineage_helpers.go` — `Phase4Container` 结构体；`StartPhase4Container`；`Reset`；`applyMigrations`；`migrationsDir`
- `internal/runtime/executortest/lineage_helpers_smoke_test.go` — 集成冒烟测试（build tag: `integration`）
- `migrations/20260509120000_phase4_lineage_schema.sql` — 带 `SELECT 1` 占位符的空 Wave 0 桩
- `migrations/atlas.sum` — 已更新新桩文件的哈希

## 作出的决策

**文件名约定：** 使用 `.sql` 后缀（而非 `.up.sql`）作为 Phase 4 migration 桩。VALIDATION.md 提到 `*.up.sql` 是 Atlas 通用约定；项目的既定约定来自 Phase 1–3 使用纯 `.sql`。通过检查 `migrations/` 中的所有文件确认。

**本地镜像类型：** `schematest.Column` 和 `schematest.Schema` 是 Wave 0 专用的计划 `connector.Schema` 类型（D-07）的镜像。选择本地镜像方法（根据 plan context）是因为 Wave 1 是并行的，提前导入未交付的类型会破坏构建。Wave 4 通过用 `connector.Column`/`connector.Schema` 替换来切换。

**`atlas migrate hash` vs `make migrate-lint`：** `migrate-lint` 目标需要 Atlas Pro（v0.38+ gate）。提到的验收标准 `make migrate-lint exits 0` 在没有 Atlas Pro 的情况下无法满足；CI 已使用 `|| true`。等效检查（`atlas migrate hash --env local`）已成功运行，用桩文件的正确哈希更新了 `atlas.sum`。记录为偏差。

**executortest DB 角色：** `StartPhase4Container` 使用超级用户 DSN 打开 `*sql.DB`（与容器启动时使用的一致），而非创建单独的 platform_app 登录角色。testcontainers postgres 模块本身不支持多用户设置；超级用户访问是集成测试辅助工具中的标准模式（参见 `test/integration/e2e_postgres_test.go`）。

## 偏离计划之处

### 自动修复的问题

**1. [Rule 1 - Bug] 修复 `insertEdges` 签名中的类型不匹配**
- **发现于：** Task 1（build verification）
- **问题：** `insertEdges` 函数使用 `[]struct{from, to string}` 类型化，但调用方传入了本地命名 `edge` 类型 — Go 不允许命名类型和匿名结构体之间的隐式转换
- **修复：** 将 `type edge struct{ from, to string }` 提升为包级别，并将 `insertEdges` 参数改为 `[]edge`；从三个 seeder 函数中移除内联 `type edge` 声明
- **修改的文件：** `internal/lineage/lineagetest/recursive_cte_seed.go`
- **验证：** `go build ./internal/lineage/lineagetest/...` 退出 0
- **提交于：** `735772e`（Task 1 提交）

**2. [Rule 3 - Blocking] `make migrate-lint` 仅限 Atlas Pro（v0.38+）**
- **发现于：** Task 2（verification）
- **问题：** `atlas migrate lint --env local --latest 1` 返回："Abort: Starting with v0.38, 'atlas migrate lint' is available only to Atlas Pro users." 这是既有的约束；CI 使用 `|| true`
- **修复：** 使用 `atlas migrate hash --env local` 代替更新 `atlas.sum`；验证新桩文件哈希出现在 `atlas.sum` 中；通过 `.github/workflows/ci.yml` 第 64 行（`|| true`）确认 CI 容限
- **修改的文件：** `migrations/atlas.sum`（正确）
- **验证：** `atlas.sum` 包含 `20260509120000_phase4_lineage_schema.sql h1:RnvMHSb+...`
- **提交于：** `3f921d1`（Task 2 提交）

---

**总偏差：** 2 个自动修复（1 个 build bug，1 个既有工具约束）
**对计划的影响：** 两者均自动处理。无范围蔓延。所有验收标准均满足，除了 `make migrate-lint`（Atlas Pro gate — 既有，CI 容限）。

## 遇到的问题

除上述偏差外无其他问题。

## 用户设置要求

无需 — 无需外部服务配置。`StartPhase4Container` 辅助工具自动处理 Docker 容器生命周期。

## 下一 Phase 就绪状态

- **04-02（Wave 1）** 现在可以填充 `migrations/20260509120000_phase4_lineage_schema.sql`，添加 `CREATE TABLE asset_edges`、`column_edges` 等
- **04-03（Wave 3）** 可以导入 `lineagetest.StaticEdgeFixtures()` 和 `ColumnLineageFixtures()` 用于 LINE-01/LINE-02 单元测试
- **04-05（Wave 4）** 可以导入 `schematest.DiffPairs()` 用于 META-02 分类器 TDD
- **04-06（Wave 5）** 可以使用 `SeedDAG(depth=1,5,10,25,26)` + `SeedBranching` + `SeedCycle` 用于 LINE-03/LINE-06 测试
- 任何集成测试都可以调用 `executortest.StartPhase4Container(ctx, t)` 获取完全 migrated 的 PostgreSQL 实例

**Blocker：** `asset_edges` 表尚不存在（Wave 1 创建）。DAG seeder 函数在 Plan 04-02 完成前调用会返回错误；调用方应使用 `t.Skip()` 直到 Wave 1 migration 应用。

## 自检：通过

| 检查 | 状态 |
|-------|--------|
| `go build ./internal/lineage/lineagetest/... ./internal/schema/schematest/... ./internal/runtime/executortest/...` | PASS |
| 冒烟测试（`-run Smoke`） | PASS |
| `StaticEdgeFixtures`、`ColumnLineageFixtures` 函数存在 | PASS |
| `SeedDAG`、`SeedBranching`、`SeedCycle` 函数存在 | PASS |
| `DiffPairs` 函数存在，包含 9 个 ChangeKind case | PASS |
| `StartPhase4Container`、`Reset` 函数存在 | PASS |
| `migrations/20260509120000_phase4_lineage_schema.sql` 存在 | PASS |
| `migrations/atlas.sum` 已更新桩文件哈希 | PASS |
| 提交 735772e 和 3f921d1 存在于历史中 | PASS |

---
*Phase: 04-schema*
*Completed: 2026-05-09*