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

# Phase 06: 代码审查报告

**审查时间:** 2026-05-12T00:00:00Z
**深度:** standard
**审查文件数:** 45
**状态:** 发现问题

## 摘要

审查了 Phase 6 Web UI 与 API 交付物的 45 个文件。代码库整体结构良好，chi 用于 HTTP 路由、connect-go 用于 RPC、React 配合 TanStack Query 作为前端、ReactFlow 用于血缘可视化。没有发现严重的安全漏洞或 bug。发现了 3 个警告和 6 个信息级别的问题，主要与未完成的桩实现相关（在注释中已说明）。

## 警告

### WR-01: fetchQualityTrend 是一个返回空数据的桩

**文件:** `internal/api/quality_handlers.go:68-85`
**问题:** `fetchQualityTrend` 函数是一个桩实现，始终返回空结果（`[]QualityTrendPoint{}, 0, nil`）。实际的 ent 查询被注释掉了，因此质量趋势数据将始终为空。
**修复:**
```go
// Implement when ent queries are ready:
// runs, err := deps.Ent.Run.Query().
//   Where(run.AssetName(asset)).
//   Where(run.FinishedAtNEQ(nil)).
//   Order(ent.Desc(run.FieldFinishedAt)).
//   Limit(limit).
//   All(ctx)
```

### WR-02: ListAlertsHandler 和 AcknowledgeAlertHandler 是桩

**文件:** `internal/api/quality_handlers.go:98-143`
**问题:** 两个处理器（`listAlertsHandler` 和 `acknowledgeAlertHandler`）都是桩实现，在没有任何实际数据库查询的情况下返回空数据。警报永远不会填充或确认。
**修复:**
```go
// Replace stub with actual ent queries when quality_alerts schema is ready:
// alerts, err := deps.Ent.QualityAlert.Query().
//   Where(qualityalert.Acknowledged(false)).
//   Order(ent.Desc(qualityalert.FieldCreatedAt)).
//   Limit(50).All(ctx)
```

### WR-03: CSRF 验证和登录处理器之间的 Cookie 名称不匹配

**文件:** `internal/auth/csrf.go:21` 和 `internal/api/auth_handlers.go:135`
**问题:** `DefaultCSRFConfig()` 使用 cookie 名称 `"dg_session"`（第 21 行），但在 `auth_handlers.go:135` 中登录处理器设置的 cookie 名称为 `Name: "dg_session"`。但是，登录中设置的 cookie 值是 `out.Token`（JWT 访问令牌），而不是单独的 CSRF 令牌。CSRF 中间件将 cookie 值与 `X-CSRF-Token` 请求头进行比较，两者都设置为 JWT。这造成了一种情况，即 CSRF 令牌就是 JWT，这可能不是预期行为。

注意：基于注释（D-23、T-06-02），这种模式看起来是有意为之，但如果 JWT 用于 CSRF，则可能存在安全风险。CSRF 令牌理想情况下应该与认证令牌分开。
**修复:** 考虑使用单独的令牌进行认证和 CSRF 保护，或记录为什么在此上下文中 JWT 可以接受作为 CSRF 令牌。

## 信息

### IN-01: Admin ConnectRPC 处理器是未实现的桩

**文件:** `internal/api/connect_admin.go:27-65`
**问题:** 所有 `AdminService` 处理器方法（`ListUsers`、`AssignRole`、`RemoveRole`、`ListRoles`、`CreateRole`、`DeleteRole`、`ListPolicies`、`CreatePolicy`、`UpdatePolicy`、`DeletePolicy`）都返回 `connect.CodeUnimplemented`。这些在注释中被标记为桩。
**修复:** 在后续计划中实现 AdminService 处理器（如注释中所述）。

### IN-02: Tabs 组件使用 React.cloneElement 可能导致额外重新渲染

**文件:** `web/src/components/ui/tabs.tsx:21, 41`
**问题:** Tabs/TabsList 组件使用 `React.cloneElement` 向子组件传递 props，这绕过了正常的 React 协调机制，可能导致意外行为。该模式功能正常，但可以重构为使用基于上下文的方法。
**修复:**
```tsx
// Use React context for Tabs state instead of cloneElement
const TabsContext = React.createContext<{
  value?: string
  onValueChange?: (v: string) => void
}>({})
```

### IN-03: React 组件中缺少错误边界

**文件:** `web/src/main.tsx`（以及所有页面组件）
**问题:** 没有定义 React 错误边界。组件渲染树中未捕获的错误将使整个应用程序崩溃，而不是优雅降级。
**修复:** 添加错误边界组件包装路由级内容。

### IN-04: Governance 页面 CSRF 令牌提取使用了错误的 cookie 名称

**文件:** `web/src/pages/governance/index.tsx:109-112`
**问题:** 代码查找 `dg_csrf` cookie，但后端设置的是 `dg_session` cookie（internal/api/auth_handlers.go:135）。CSRF 令牌提取应改用 `dg_session`。
**修复:**
```tsx
// Get CSRF token from cookie - use correct cookie name
const csrfToken = document.cookie
  .split('; ')
  .find(row => row.startsWith('dg_session='))
  ?.split('=')[1] || ''
```

### IN-05: 目录页面中引用了缺失的搜索栏和过滤器组件

**文件:** `web/src/pages/catalog/index.tsx:3-7`
**问题:** 导入了 `SearchBar`、`SearchResult`、`TagFilter` 和 `OwnerSelect` 组件，但代码库中不存在这些组件。目录页面将无法编译/运行。
**修复:** 创建缺失的组件，或移除导入（如果稍后添加）。

### IN-06: main.tsx 在单个文件中同时包含路由定义和页面组件

**文件:** `web/src/main.tsx`
**问题:** 该文件有 368 行，混合了路由树定义和多个页面组件（AssetDashboardPage、AssetCardPage、AssetDetailPage、RunHistoryPage 等）。这违反了单一职责原则，使文件更难维护。
**修复:** 拆分为 `web/src/pages/` 目录下的单独文件，每个文件一个页面组件（如 admin 子页面已做的那样）。

---

_审查时间: 2026-05-12T00:00:00Z_
_审查者: Claude (gsd-code-reviewer)_
_深度: standard_
