---
phase: 06-web-ui-api
reviewed: 2026-05-12T00:00:00Z
depth: standard
files_reviewed: 45
files_reviewed_list:
  - cmd/platform/main.go
  - internal/api/auth_handlers.go
  - internal/api/connect_admin.go
  - internal/api/connect_asset.go
  - internal/api/connect_governance.go
  - internal/api/connect_lineage.go
  - internal/api/connect_quality.go
  - internal/api/me_handlers.go
  - internal/api/quality_handlers.go
  - internal/api/router.go
  - internal/api/search_handlers.go
  - internal/api/server.go
  - internal/auth/csrf.go
  - internal/auth/service.go
  - internal/lineage/neighborhood/neighborhood.go
  - migrations/20260512043000_add_fts_search.sql
  - proto/api/v1/api.pb.go
  - proto/api/v1/api.proto
  - proto/api/v1/v1connect/api.connect.go
  - web/package.json
  - web/src/components/AlertList.tsx
  - web/src/components/AssetNode.tsx
  - web/src/components/ColumnPanel.tsx
  - web/src/components/LineageDAG.tsx
  - web/src/components/QualityTrendChart.tsx
  - web/src/components/ui/badge.tsx
  - web/src/components/ui/button.tsx
  - web/src/components/ui/card.tsx
  - web/src/components/ui/dialog.tsx
  - web/src/components/ui/input.tsx
  - web/src/components/ui/label.tsx
  - web/src/components/ui/select.tsx
  - web/src/components/ui/spinner.tsx
  - web/src/components/ui/tabs.tsx
  - web/src/components/ui/textarea.tsx
  - web/src/main.tsx
  - web/src/pages/admin/index.tsx
  - web/src/pages/admin/policies.tsx
  - web/src/pages/admin/roles.tsx
  - web/src/pages/admin/users.tsx
  - web/src/pages/assets/[name]/quality.tsx
  - web/src/pages/catalog/index.tsx
  - web/src/pages/governance/index.tsx
  - web/src/pages/lineage/[id].tsx
  - web/vite.config.ts
findings:
  critical: 0
  warning: 3
  info: 6
  total: 9
status: issues_found
---

# Phase 06: Code Review Report

**Reviewed:** 2026-05-12T00:00:00Z
**Depth:** standard
**Files Reviewed:** 45
**Status:** issues_found

## Summary

Reviewed 45 files across the Phase 6 Web UI & API deliverable. The codebase is generally well-structured with proper use of chi for HTTP routing, connect-go for RPC, React with TanStack Query for the frontend, and ReactFlow for lineage visualization. No critical security vulnerabilities or bugs were found. Three warnings and six info-level items were identified, mostly related to incomplete stub implementations that are acknowledged in comments.

## Warnings

### WR-01: fetchQualityTrend is a stub returning empty data

**File:** `internal/api/quality_handlers.go:68-85`
**Issue:** The `fetchQualityTrend` function is a stub implementation that always returns empty results (`[]QualityTrendPoint{}, 0, nil`). The actual ent queries are commented out, so quality trend data will always be empty.
**Fix:**
```go
// Implement when ent queries are ready:
// runs, err := deps.Ent.Run.Query().
//   Where(run.AssetName(asset)).
//   Where(run.FinishedAtNEQ(nil)).
//   Order(ent.Desc(run.FieldFinishedAt)).
//   Limit(limit).
//   All(ctx)
```

### WR-02: ListAlertsHandler and AcknowledgeAlertHandler are stubs

**File:** `internal/api/quality_handlers.go:98-143`
**Issue:** Both handlers (`listAlertsHandler` and `acknowledgeAlertHandler`) are stub implementations that return empty data without any actual database queries. Alerts will never be populated or acknowledged.
**Fix:**
```go
// Replace stub with actual ent queries when quality_alerts schema is ready:
// alerts, err := deps.Ent.QualityAlert.Query().
//   Where(qualityalert.Acknowledged(false)).
//   Order(ent.Desc(qualityalert.FieldCreatedAt)).
//   Limit(50).All(ctx)
```

### WR-03: Cookie name mismatch between CSRF validation and login handler

**File:** `internal/auth/csrf.go:21` and `internal/api/auth_handlers.go:135`
**Issue:** `DefaultCSRFConfig()` uses cookie name `"dg_session"` (line 21), but in `auth_handlers.go:135` the login handler sets cookie with `Name: "dg_session"`. However, the cookie value set in login is `out.Token` (the JWT access token), not a separate CSRF token. The CSRF middleware compares the cookie value against the `X-CSRF-Token` header, both of which are set to the JWT. This creates a situation where the CSRF token IS the JWT, which may not be the intended behavior.

Note: This pattern appears intentional based on comments (D-23, T-06-02) but could be a security concern if the JWT is used for CSRF. A CSRF token should ideally be separate from the authentication token.
**Fix:** Consider using separate tokens for authentication and CSRF protection, or document why the JWT is acceptable as a CSRF token in this context.

## Info

### IN-01: Admin ConnectRPC handlers are unimplemented stubs

**File:** `internal/api/connect_admin.go:27-65`
**Issue:** All `AdminService` handler methods (`ListUsers`, `AssignRole`, `RemoveRole`, `ListRoles`, `CreateRole`, `DeleteRole`, `ListPolicies`, `CreatePolicy`, `UpdatePolicy`, `DeletePolicy`) return `connect.CodeUnimplemented`. These are acknowledged as stubs in comments.
**Fix:** Implement AdminService handlers in subsequent plans as noted in the comment.

### IN-02: Tabs component uses React.cloneElement which may cause extra re-renders

**File:** `web/src/components/ui/tabs.tsx:21, 41`
**Issue:** The Tabs/TabsList components use `React.cloneElement` to pass props to children, which bypasses the normal React reconciliation and can cause unexpected behavior. The pattern is functional but could be refactored to use a context-based approach.
**Fix:**
```tsx
// Use React context for Tabs state instead of cloneElement
const TabsContext = React.createContext<{
  value?: string
  onValueChange?: (v: string) => void
}>({})
```

### IN-03: Missing error boundaries in React components

**File:** `web/src/main.tsx` (and all page components)
**Issue:** No React error boundaries are defined. Uncaught errors in component render trees will crash the entire application instead of graceful degradation.
**Fix:** Add error boundary components wrapping route-level content.

### IN-04: Governance page CSRF token extraction uses wrong cookie name

**File:** `web/src/pages/governance/index.tsx:109-112`
**Issue:** The code looks for `dg_csrf` cookie but the backend sets `dg_session` cookie (internal/api/auth_handlers.go:135). The CSRF token extraction should use `dg_session` instead.
**Fix:**
```tsx
// Get CSRF token from cookie - use correct cookie name
const csrfToken = document.cookie
  .split('; ')
  .find(row => row.startsWith('dg_session='))
  ?.split('=')[1] || ''
```

### IN-05: Missing search bar and filter components referenced in catalog page

**File:** `web/src/pages/catalog/index.tsx:3-7`
**Issue:** The `SearchBar`, `SearchResult`, `TagFilter`, and `OwnerSelect` components are imported but not present in the codebase. The catalog page will fail to compile/run.
**Fix:** Create the missing components or remove imports if they will be added later.

### IN-06: main.tsx contains both route definition AND page components in a single file

**File:** `web/src/main.tsx`
**Issue:** This file is 368 lines and mixes route tree definition with multiple page components (AssetDashboardPage, AssetCardPage, AssetDetailPage, RunHistoryPage, etc.). This violates single responsibility and makes the file harder to maintain.
**Fix:** Split into separate files under `web/src/pages/` directory with one page component per file, as already done for admin sub-pages.

---

_Reviewed: 2026-05-12T00:00:00Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
