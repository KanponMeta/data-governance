---
phase: "06-web-ui-api"
created: 2026-05-12
status: partial
coverage:
  UI-01: asset dashboard — implemented
  UI-02: run history — implemented
  UI-03: catalog browse — implemented
  UI-04: lineage DAG — implemented
  UI-05: quality alerts — scaffold (stub handlers)
  UI-06: governance inbox — implemented
  UI-07: admin panel — scaffold (UI only, handlers return Unimplemented)
  CORE-04: SPA embedded — implemented
---

# Phase 6: UI Component Specification

## Tech Stack

- **Framework:** React 19 + TypeScript
- **Build:** Vite 6 + pnpm
- **Routing:** TanStack Router v1 (class-based Route pattern, `new Route(...)`)
- **Server State:** TanStack Query v5 (`useQuery`, `useMutation`, `refetchInterval`)
- **Styling:** Tailwind CSS v4 + shadcn/ui components
- **DAG Viz:** ReactFlow v12 (`@xyflow/react`) with dagre layout
- **Charts:** Recharts

## Route Structure

```
/                       → asset dashboard (AssetDashboard)
/assets/$name           → asset detail + run history tab + quality tab
/catalog                → catalog search + browse
/lineage/$id            → interactive lineage DAG
/governance             → governance inbox (permission-gated: canApprove)
/admin                  → admin panel (permission-gated: canManageUsers)
/admin/users            → user management tab
/admin/roles            → role management tab
/admin/policies         → policy management tab
```

## Component Inventory

### Asset Dashboard (`/`)
- `pages/assets/index.tsx` — lazy-loaded, polls every 60s
- `components/AssetCard.tsx` — state badge, quality badge, last materialized time
- `components/RunStatusBadge.tsx` — colored badge per run state
- `components/QualityStatusBadge.tsx` — colored badge per governance state

### Asset Detail (`/assets/$name`)
- `pages/assets/[name]/index.tsx` — asset metadata + tabs
- `pages/assets/[name]/quality.tsx` — quality trend chart + alert list
- `components/RunHistory.tsx` — run list with step expansion
- `components/QualityTrendChart.tsx` — Recharts LineChart, color-coded dots
- `components/AlertList.tsx` — alerts with severity badges, acknowledge action

### Catalog (`/catalog`)
- `pages/catalog/index.tsx` — full catalog browse
- `components/SearchBar.tsx` — Enter key handler
- `components/TagFilter.tsx` — toggle chips
- `components/OwnerSelect.tsx` — dropdown with clear
- `components/SearchResult.tsx` — cards with type badge + highlight

### Lineage DAG (`/lineage/$id`)
- `pages/lineage/[id].tsx` — lineage page with depth selector
- `components/LineageDAG.tsx` — ReactFlow canvas, dagre layout, depth selector
- `components/AssetNode.tsx` — custom node, type badge, handles
- `components/ColumnPanel.tsx` — side panel for column drilldown

### Governance (`/governance`)
- `pages/governance/index.tsx` — tabs: pending/approved/rejected
- `components/ui/dialog.tsx` — approve/reject modal
- `components/ui/textarea.tsx` — comment field
- 20s polling for pending reviews
- CSRF token from `dg_session` cookie (X-CSRF-Token header)

### Admin (`/admin`)
- `pages/admin/index.tsx` — tabbed interface
- `pages/admin/users.tsx` — user table
- `pages/admin/roles.tsx` — role form
- `pages/admin/policies.tsx` — policy form (non-functional — AdminService stubs)

## API Integration

| Page | Endpoint | Protocol | Polling |
|------|----------|----------|---------|
| Asset Dashboard | `/v1/connect/api.v1.AssetService/ListAssets` | ConnectRPC | 60s |
| Run History | `/v1/connect/api.v1.AssetService/ListRuns` | ConnectRPC | 60s |
| Catalog | `GET /v1/catalog/search?q=&tag=&owner=` | REST | 60s |
| Lineage | `/v1/connect/api.v1.LineageService/Neighborhood` | ConnectRPC | on-demand |
| Quality Trend | `GET /v1/quality/trend` | REST | 60s |
| Alerts | `GET /v1/quality/alerts` | REST | 20s |
| Governance | `/v1/connect/api.v1.GovernanceService/ListReviews` | ConnectRPC | 20s |

## Auth / Permissions

- `GET /v1/me` → `{id, email, roles[], permissions: {canApprove, canEditPolicies, canManageUsers}}`
- Cookie: `dg_session` (httpOnly, Secure, SameSite=Strict)
- CSRF: `X-CSRF-Token` header = JWT value from `dg_session` cookie

## Gaps (Not Implemented)

- Quality trend + alerts: handlers are stubs returning empty data
- Admin policy CRUD: AdminService returns `CodeUnimplemented`, ColumnPolicy ent schema missing
- UI-SPEC.md: this document — created after phase execution
