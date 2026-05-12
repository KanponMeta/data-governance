---
phase: "06-web-ui-api"
plan: "06"
subsystem: "api"
tags: ["connectrpc", "governance", "react", "tanstack-query", "workflow"]

# Dependency graph
requires: ["06-01", "05-04"]
provides:
  - "GovernanceService ConnectRPC handlers (ListReviews, GetReview, ApproveReview, RejectReview)"
  - "Governance inbox React page at /governance"
  - "ReviewCard and ReviewModal components"
  - "20s polling for pending reviews (D-17 hot screen)"
affects: []

# Tech tracking
tech-stack:
  added: ["lucide-react"]
  patterns: ["ConnectRPC governance handlers calling Phase 5 Workflow service", "React tabs for pending/approved/rejected reviews", "CSRF token from cookie in fetch requests"]

key-files:
  created:
    - "web/src/pages/governance/index.tsx — Governance inbox page with tabs and ReviewModal"
    - "web/src/components/ui/dialog.tsx — Radix Dialog wrapper"
    - "web/src/components/ui/textarea.tsx — Labeled textarea component"
    - "web/src/components/ui/label.tsx — Label component"
    - "web/src/components/ui/tabs.tsx — Tabs with state management"
  modified:
    - "internal/api/connect.go — Added GovernanceWorkflow to ConnectDeps, implemented ListReviews/GetReview/ApproveReview/RejectReview"
    - "internal/api/router.go — Added ConnectGovernanceWorkflow field, pass to mountConnectRPC"
    - "web/src/components/ui/spinner.tsx — Added size prop support"
    - "web/src/main.tsx — Added governanceLayoutRoute with /governance path"

patterns-established:
  - "GovernanceService handlers call governance.Workflow.Approve/Reject directly (not via REST)"
  - "Permission checked via Casbin enforcer.Enforce before state-changing operations"
  - "ReviewModal sends X-CSRF-Token header extracted from dg_csrf cookie"

requirements-completed: ["UI-06"]

# Metrics
duration: 18min
completed: 2026-05-12
---

# Phase 06 Plan 06: Governance Inbox (UI-06)

**Governance inbox for review requests: ConnectRPC handlers + React page with approve/reject modal**

## Performance

- **Duration:** 18 min (1080 seconds)
- **Started:** 2026-05-12T12:17:00Z
- **Completed:** 2026-05-12T12:35:00Z
- **Tasks:** 2
- **Files modified:** 8

## Accomplishments

- Implemented GovernanceService ConnectRPC handlers: ListReviews, GetReview, ApproveReview, RejectReview
- Added GovernanceWorkflow field to ConnectDeps to access Phase 5 workflow service directly
- Created governance inbox React page at /governance with pending/approved/rejected tabs
- ReviewCard shows asset name, submitter, submitted_at, status badge
- ReviewModal provides approve/reject actions with required comment field
- 20s polling interval for pending reviews (D-17 hot screen optimization)
- canApprove permission check from GET /v1/me gates inbox visibility
- X-CSRF-Token header sent with approve/reject fetch requests

## Task Commits

1. **Task 1: Governance review ConnectRPC handlers** - `d21dd83` (feat)
2. **Task 2: Governance inbox React page** - `38c968d` (feat)

**Plan metadata:** `38c968d` (feat: add governance inbox React page with approve/reject modal)

## Files Created/Modified

- `internal/api/connect.go` — Added GovernanceWorkflow to ConnectDeps; ListReviews/GetReview/ApproveReview/RejectReview implementations
- `internal/api/router.go` — Added ConnectGovernanceWorkflow field, pass to mountConnectRPC
- `web/src/pages/governance/index.tsx` — Full governance inbox page with tabs and ReviewModal
- `web/src/main.tsx` — Added governanceLayoutRoute with /governance path
- `web/src/components/ui/dialog.tsx` — Radix Dialog wrapper
- `web/src/components/ui/textarea.tsx` — Textarea with label styling
- `web/src/components/ui/label.tsx` — Label component
- `web/src/components/ui/tabs.tsx` — Tabs with active state management
- `web/src/components/ui/spinner.tsx` — Added size prop

## Decisions Made

- ConnectRPC handlers call governance.Workflow.Approve/Reject/Get/Status directly (not REST) for type safety
- Permission check via Casbin enforcer.Enforce before approve/reject operations
- Comment required for reject (D-12 from Phase 5); ErrCommentRequired mapped to CodeInvalidArgument
- 20s polling for pending reviews (D-17); 60s for approved/rejected
- Governance page shows "permission denied" card if canApprove=false

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Spinner component lacked size prop**
- **Found during:** Web build verification
- **Issue:** `<Spinner size="sm" />` in ReviewModal failed TypeScript because spinner didn't support size prop
- **Fix:** Updated Spinner to accept size prop with default "h-6 w-6"
- **Files modified:** web/src/components/ui/spinner.tsx
- **Commit:** 38c968d (part of task commit)

**2. [Rule 3 - Blocking] connect.NewErrorDetail wrong usage**
- **Found during:** Go build verification
- **Issue:** `connect.NewErrorDetail("comment is required")` returned (val, error) tuple; couldn't use as single arg
- **Fix:** Changed to `errors.New("comment is required")` in RejectReview handler
- **Files modified:** internal/api/connect.go
- **Commit:** d21dd83 (part of task commit)

**3. [Rule 3 - Blocking] GovernanceWorkflow not in Deps struct**
- **Found during:** Go build verification
- **Issue:** ConnectDeps had GovernanceService but not the actual Workflow service needed for handler logic
- **Fix:** Added GovernanceWorkflow *governance.Workflow field to ConnectDeps and router.go Deps
- **Files modified:** internal/api/connect.go, internal/api/router.go
- **Commit:** d21dd83 (part of task commit)

---

**Total deviations:** 3 auto-fixed (all blocking)
**Impact on plan:** All auto-fixes necessary for build to succeed. No scope creep.

## Threat Model Compliance

| Threat ID | Mitigation | Status |
|-----------|------------|--------|
| T-06-11 (Review action tampering) | CSRF token + canApprove permission + comment required | Implemented |
| T-06-12 (Elevation of Privilege) | canApprove flag from /v1/me; workflow validates reviewer pool | Implemented |

## Verification

- `go build ./internal/api/...` succeeds
- `cd web && pnpm run build` succeeds
- Governance inbox page at /governance (when user has canApprove permission)
- Approve/reject require comment field (validated client and server side)

## Next Phase Readiness

- GovernanceService ConnectRPC handlers are fully implemented
- Workflow service integration complete
- React inbox page with 20s polling ready
- No blockers for subsequent plans

---
*Phase: 06-web-ui-api*
*Completed: 2026-05-12*