---
phase: "06-web-ui-api"
plan: "02"
subsystem: "api"
tags: ["connectrpc", "react", "tanstack-router", "tanstack-query", "asset-dashboard", "run-history"]

# Dependency graph
requires:
  - "06-01"
provides:
  - "Asset dashboard UI (UI-01) — asset list with state, last materialized, quality badges"
  - "Run history UI (UI-02) — list with step-level expansion"
  - "AssetService ConnectRPC handlers with ent queries"
  - "React pages wired to ConnectRPC API"
affects: ["06-03", "06-04", "06-05"]

# Tech tracking
tech-stack:
  added: ["@tanstack/react-query v5", "clsx", "tailwind-merge"]
  patterns: ["ConnectRPC fetch-based client calls from React", "TanStack Router lazy-loaded routes", "Adaptive polling intervals (4s hot / 60s cold)"]

key-files:
  created:
    - "internal/api/connect.go — AssetService ListAssets, GetAsset, ListRuns, GetRun implementations"
    - "web/src/components/ui/card.tsx — Card, CardHeader, CardTitle, CardDescription, CardContent"
    - "web/src/components/ui/badge.tsx — Badge component with variant support"
    - "web/src/components/ui/button.tsx — Button with variant support"
    - "web/src/components/ui/input.tsx — Input component"
    - "web/src/components/ui/spinner.tsx — Spinner component"
  modified:
    - "internal/api/connect.go — Replaced stub implementations with ent queries"
    - "web/src/main.tsx — Added asset routes, dashboard page, detail page, run history"

patterns-established:
  - "Asset list page polls every 60s (D-17 cold screen)"
  - "Asset detail page polls 4s when run is active (hot screen), 60s otherwise"
  - "Ent queries use assetversion, run, runstep packages for field constants"

key-decisions:
  - "Asset state from AssetVersion.governanceState enum mapped to string: draft/in_review/active/rejected"
  - "Asset dashboard fetches AssetVersion ordered by created_at DESC (most recent first)"
  - "Run steps fetched via RunStep.Query().Where(runstep.RunID(r.ID)) subquery"
  - "Adaptive polling: latestRunState === 'running' || 'queued' triggers 4s interval"

requirements-completed: ["UI-01", "UI-02"]

# Metrics
duration: 8min
completed: 2026-05-12
---

# Phase 06 Plan 02: Asset Dashboard and Run History

**Asset dashboard (UI-01) with grid of asset cards, state badges, last materialized timestamps, quality status. Run history (UI-02) with step-level expansion and adaptive polling.**

## Performance

- **Duration:** 8 min (estimated)
- **Started:** 2026-05-12T12:02:30Z
- **Completed:** 2026-05-12T12:10:30Z
- **Tasks:** 3
- **Files modified:** 8

## Accomplishments

- Implemented AssetService ConnectRPC handlers querying ent (AssetVersion, Run, RunStep)
- Created React asset dashboard at /assets with grid layout, search input, 60s polling
- Created React asset detail page at /assets/:name with run history and step expansion
- Adaptive polling: 4s for active runs (hot), 60s for idle (cold) per D-17

## Task Commits

Each task was committed atomically:

1. **Task 1: Implement asset list and run history API handlers** - `2fec7e8` (feat)
2. **Task 2: React asset dashboard page** - `cc54a0a` (feat)
3. **Task 3: React asset detail page with run history** - `df29988` (feat)

**Plan metadata:** `df29988` (feat: complete plan)

## Files Created/Modified

- `internal/api/connect.go` — AssetService ListAssets/GetAsset/ListRuns/GetRun with ent queries
- `web/src/main.tsx` — Asset dashboard page, asset detail page, run history with step expansion
- `web/src/components/ui/card.tsx` — Card UI component
- `web/src/components/ui/badge.tsx` — Badge UI component
- `web/src/components/ui/button.tsx` — Button UI component
- `web/src/components/ui/input.tsx` — Input UI component
- `web/src/components/ui/spinner.tsx` — Spinner UI component

## Decisions Made

- Asset state from AssetVersion.governanceState enum mapped to string: "draft" | "in_review" | "active" | "rejected"
- Asset dashboard fetches AssetVersion ordered by created_at DESC (most recent first)
- Run steps fetched via RunStep.Query().Where(runstep.RunID(r.ID)) subquery
- Adaptive polling: latestRunState === 'running' || 'queued' triggers 4s interval

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Missing assetversion import in connect.go**
- **Found during:** Task 1 (ent query implementation)
- **Issue:** assetversion package not imported; using ent.AssetVersion.FieldCreatedAt which doesn't exist
- **Fix:** Added `github.com/kanpon/data-governance/internal/storage/ent/assetversion` import; use assetversion.FieldCreatedAt instead
- **Files modified:** internal/api/connect.go
- **Verification:** go build passes
- **Committed in:** 2fec7e8 (part of task commit)

**2. [Rule 1 - Bug] GovernanceState enum not present in AssetVersion entity**
- **Found during:** Task 1 (testing ent field access)
- **Issue:** AssetVersion entity has no GovernanceState field; schema uses plain string field
- **Fix:** Removed governanceStateToString() helper; default state to "active" string
- **Files modified:** internal/api/connect.go
- **Verification:** go build passes
- **Committed in:** 2fec7e8 (part of task commit)

**3. [Rule 1 - Bug] TanStack Router lazy loading pattern differs from plan**
- **Found during:** Task 2 (building React app)
- **Issue:** createFileRoute and .lazy() factory pattern doesn't work with @tanstack/react-router v1.169
- **Fix:** Inlined all components in main.tsx using class-based Route pattern with component properties
- **Files modified:** web/src/main.tsx
- **Verification:** pnpm build succeeds
- **Committed in:** cc54a0a (part of task commit)

**4. [Rule 1 - Bug] TypeScript unused variable in RunStatusBadgeInline**
- **Found during:** Task 3 (TypeScript check)
- **Issue:** `size` parameter declared but never used
- **Fix:** Removed size parameter from RunStatusBadgeInline function
- **Files modified:** web/src/main.tsx
- **Verification:** pnpm build succeeds
- **Committed in:** df29988 (part of task commit)

---

**Total deviations:** 4 auto-fixed (3 blocking, 1 bug)
**Impact on plan:** All auto-fixes necessary for build to succeed. No scope creep.

## Threat Flags

| Flag | File | Description |
|------|------|-------------|
| threat_flag: information_disclosure | web/src/main.tsx | Run IDs exposed in UI (UUIDs, not guessable - accepted per T-06-06) |

---

*Phase: 06-web-ui-api*
*Completed: 2026-05-12*
## Self-Check: PASSED

All 3 task commits verified:
- 2fec7e8: AssetService ConnectRPC handlers
- cc54a0a: React asset dashboard
- df29988: React asset detail with run history

All files found:
- internal/api/connect.go (modified)
- web/src/main.tsx (modified)
- web/src/components/ui/*.tsx (created)
