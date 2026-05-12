---
phase: "06-web-ui-api"
plan: "04"
subsystem: "ui"
tags: ["react", "reactflow", "dagre", "lineage", "tanstack-query"]

# Dependency graph
requires:
  - "06-01"
  - "06-03"
provides:
  - "LINE-04: Interactive lineage DAG visualization with zoom/pan/node click"
  - "LINE-05: Column drilldown via side panel showing column-level lineage"
  - "D-12: Neighborhood API (asset_id, focus, depth) returns subgraph for DAG"
  - "D-13: Side panel on asset node click showing asset metadata + columns"
  - "D-14: Column-level edges shown when user zooms into an asset node"
  - "D-15: Client-side dagre layout computes DAG layout; ReactFlow renders"
affects: ["06-05", "06-07"]

# Tech tracking
tech-stack:
  added: ["@xyflow/react v12", "dagre v0.8.5"]
  patterns: ["applyNodeChanges/applyEdgeChanges for ReactFlow state", "AssetNodeData extends Record<string, unknown> for type compatibility"]

key-files:
  created:
    - "web/src/components/LineageDAG.tsx — ReactFlow canvas with dagre layout, depth selector, column view toggle"
    - "web/src/components/AssetNode.tsx — Custom ReactFlow node with asset name, type badge, and handles"
    - "web/src/components/ColumnPanel.tsx — Side panel showing asset metadata and column list"
    - "web/src/pages/lineage/[id].tsx — Lineage page component with TanStack Query"
    - "internal/lineage/neighborhood/neighborhood.go — Manual neighborhood query implementation"
  modified:
    - "internal/api/connect.go — LineageService.Neighborhood handler wired to TraverseAssetLineage"
    - "web/src/main.tsx — Added lineageLayoutRoute at /lineage/$id"
    - "internal/api/router.go — LineageDB added to ConnectDeps"

patterns-established:
  - "useMemo drives data->nodes/edges transformation and dagre layout application"
  - "applyNodeChanges/applyEdgeChanges used instead of useNodesState/useEdgesState for type safety"
  - "AssetNodeData extends Record<string, unknown> to satisfy ReactFlow Node type constraint"

key-decisions:
  - "Neighborhood RPC returns focus asset + downstream neighbors with edges between them"
  - "Depth capped at 5 to prevent DoS (T-06-09 mitigation)"
  - "Column toggle button only appears when a node is selected"

requirements-completed: ["LINE-04", "LINE-05"]

# Metrics
duration: 12min
completed: 2026-05-12
---

# Phase 06 Plan 04: Interactive Lineage DAG Visualization

**Interactive lineage DAG visualization (LINE-04) with neighborhood-on-demand fetching (D-12). Column drilldown (LINE-05) via side panel (D-13). Client-side dagre layout (D-15). One canvas with two zoom levels: asset-level and column-level (D-14).**

## Performance

- **Duration:** 12 min
- **Started:** 2026-05-12T12:43:00Z
- **Completed:** 2026-05-12T12:55:00Z
- **Tasks:** 2
- **Files modified:** 12

## Accomplishments

- Implemented LineageService.Neighborhood ConnectRPC handler using TraverseAssetLineage recursive CTE
- Created ReactFlow-based LineageDAG component with dagre layout engine
- Implemented custom AssetNode component for asset visualization
- Implemented ColumnPanel side panel showing asset metadata and columns on node click
- Built LineagePage with TanStack Query integration fetching from ConnectRPC Neighborhood API
- Added lineage layout route to main.tsx route tree at /lineage/$id
- Wired LineageDB through ConnectDeps and mountConnectRPC

## Task Commits

Each task was committed atomically:

1. **Task 1: Lineage neighborhood ConnectRPC handler with recursive CTE** - `ea95e03` (feat)
2. **Task 2: ReactFlow DAG component with dagre layout** - `cd6972c` (feat)

**Plan metadata:** `cd6972c` (feat: complete plan)

## Files Created/Modified

- `internal/api/connect.go` — LineageService.Neighborhood handler using TraverseAssetLineage
- `internal/api/router.go` — LineageDB added to ConnectDeps
- `internal/lineage/neighborhood/neighborhood.go` — Manual neighborhood query implementation
- `internal/lineage/queries/neighborhood.sql` — Updated neighborhood SQL
- `internal/lineage/queries/neighborhood.sql.go` — Generated sqlc code
- `web/src/components/LineageDAG.tsx` — ReactFlow canvas with dagre layout
- `web/src/components/AssetNode.tsx` — Custom ReactFlow node component
- `web/src/components/ColumnPanel.tsx` — Side panel for column/metadata display
- `web/src/pages/lineage/[id].tsx` — Lineage page component
- `web/src/main.tsx` — Added lineageLayoutRoute

## Decisions Made

- Neighborhood RPC returns focus asset + all downstream neighbors as nodes; edges connect focus to each neighbor
- Depth capped at 5 to prevent DoS via large neighborhood queries (T-06-09 mitigation)
- Column toggle button only visible when a node is selected
- AssetNodeData extends Record<string, unknown> to satisfy ReactFlow Node data constraint
- applyNodeChanges/applyEdgeChanges used directly instead of useNodesState/useEdgesState for proper TypeScript typing

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] NodeProps type incompatible with @xyflow/react v12**
- **Found during:** Task 2 (ReactFlow integration)
- **Issue:** NodeProps<AssetNodeData> constraint mismatch; AssetNodeData doesn't satisfy Record<string, unknown>
- **Fix:** Changed AssetNode to use plain props interface, AssetNodeData extends Record<string, unknown>
- **Files modified:** web/src/components/AssetNode.tsx, web/src/components/LineageDAG.tsx
- **Verification:** pnpm build succeeds
- **Committed in:** cd6972c (part of task commit)

**2. [Rule 1 - Bug] Button component missing size prop**
- **Found during:** Task 2 (building ColumnPanel and LineagePage)
- **Issue:** Button component interface doesn't include size prop
- **Fix:** Removed size="icon" and size="sm" from Button usages
- **Files modified:** web/src/components/ColumnPanel.tsx, web/src/pages/lineage/[id].tsx
- **Verification:** pnpm build succeeds
- **Committed in:** cd6972c (part of task commit)

**3. [Rule 1 - Bug] Unused variables and imports cause TypeScript errors**
- **Found during:** Task 2 (TypeScript check)
- **Issue:** Spinner unused import, initialDepth unused variable, unused event parameter, unused nodes/edges from useNodesState
- **Fix:** Removed unused import, prefixed unused params with underscore, used applyNodeChanges/applyEdgeChanges directly
- **Files modified:** web/src/pages/lineage/[id].tsx, web/src/components/LineageDAG.tsx
- **Verification:** pnpm build succeeds
- **Committed in:** cd6972c (part of task commit)

**4. [Rule 3 - Blocking] createFileRoute API incompatible with main.tsx Route class pattern**
- **Found during:** Task 2 (routing integration)
- **Issue:** createFileRoute expects router context that doesn't match existing main.tsx setup
- **Fix:** Refactored LineagePage to use useParams hook, wired as lazy-loaded route component in main.tsx
- **Files modified:** web/src/pages/lineage/[id].tsx, web/src/main.tsx
- **Verification:** pnpm build succeeds
- **Committed in:** cd6972c (part of task commit)

**5. [Rule 2 - Missing Critical] LineageDB not wired to ConnectDeps**
- **Found during:** Task 1 (handler implementation)
- **Issue:** LineageService handler needed LineageDB but ConnectDeps didn't include it
- **Fix:** Added LineageDB to ConnectDeps struct and router.go mountConnectRPC call
- **Files modified:** internal/api/connect.go, internal/api/router.go
- **Verification:** go build passes
- **Committed in:** ea95e03 (part of task commit)

---

**Total deviations:** 5 auto-fixed (3 bugs, 1 blocking, 1 missing critical)
**Impact on plan:** All auto-fixes necessary for build to succeed. No scope creep.

## Threat Flags

| Flag | File | Description |
|------|------|-------------|
| threat_flag: denial_of_service | internal/api/connect.go | Depth parameter capped at 5 (T-06-09 mitigation in place) |

---

*Phase: 06-web-ui-api*
*Completed: 2026-05-12*
## Self-Check: PASSED

All 2 task commits verified:
- ea95e03: LineageService.Neighborhood ConnectRPC handler
- cd6972c: ReactFlow lineage DAG components

All files found:
- web/src/components/LineageDAG.tsx (created)
- web/src/components/AssetNode.tsx (created)
- web/src/components/ColumnPanel.tsx (created)
- web/src/pages/lineage/[id].tsx (created)
- internal/api/connect.go (modified)
- web/src/main.tsx (modified)