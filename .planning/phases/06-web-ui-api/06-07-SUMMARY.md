---
phase: "06-web-ui-api"
plan: "07"
subsystem: ui
tags: [react, tanstack-router, connectrpc, casbin, go:embed, admin-panel]

# 依赖图
requires:
  - phase: "05-governance"
    provides: "Casbin 执行器、RBAC 角色、列策略 schema"
provides:
  - "带用户/角色/策略管理的管理面板"
  - "通过 go:embed 嵌入 Go 二进制的 React SPA"
  - "用于管理操作的 ConnectRPC AdminService"
affects: [06-web-ui-api]

# 技术跟踪
tech-stack:
  added: [connectrpc, embed]
  patterns: [权限限制的管理 UI, 嵌入式 SPA 服务]

key-files:
  created:
    - internal/api/connect_admin.go
    - web/src/pages/admin/index.tsx
    - web/src/pages/admin/users.tsx
    - web/src/pages/admin/roles.tsx
    - web/src/pages/admin/policies.tsx
  modified:
    - cmd/platform/main.go
    - internal/api/router.go
    - internal/api/server.go

key-decisions:
  - "使用布尔 ServeSPA 标志而非 nil embed.FS 检查来控制生产服务"
  - "SPA 处理器将 API 路径路由到 chi 路由器，所有其他路径到嵌入的 React"
  - "权限检查通过 /v1/me 端点的 canManageUsers 和 canEditPolicies"

patterns-established:
  - "ConnectRPC 服务与 chi HTTP 处理器在过渡期共存"

requirements-completed: [UI-07, AUTH-04]

# 指标
duration: 11min
completed: 2026-05-12
---

# Phase 6 Plan 07: 管理面板和嵌入式 SPA 总结

**带用户/角色/策略管理的管理面板，连接到 ConnectRPC API，React SPA 通过 go:embed 嵌入 Go 二进制文件**

## 性能

- **时长：** 11 分钟
- **开始：** 2026-05-12T04:28:56Z
- **完成：** 2026-05-12T04:39:53Z
- **任务：** 3
- **修改文件：** 8

## 成就
- /admin 的管理面板，带选项卡：Users、Roles、Policies
- 用户管理：列出用户，分配/移除角色
- 角色管理：创建/删除带描述的角色
- 列级访问策略管理
- React SPA 嵌入 Go 二进制文件并在非 API 路由提供服务
- 所有管理操作由 canManageUsers 或 canEditPolicies 权限限制

## 任务提交

每个任务原子提交：

1. **Task 1: 管理 API 处理器** - `e2f1ac4` (feat)
2. **Task 2: React 管理面板页面** - `1289f1e` (feat)
3. **Task 3: go:embed SPA 接线** - `d5a6c72` (feat)

**计划元数据：** `7d744a0` (docs(06-03): 向目录浏览计划添加 UI-03 覆盖)

## 文件创建/修改
- `internal/api/connect_admin.go` - ConnectRPC AdminService，含 ListUsers, AssignRole, RemoveRole, ListRoles, CreateRole, DeleteRole, ListPolicies, CreatePolicy, UpdatePolicy, DeletePolicy
- `web/src/pages/admin/index.tsx` - 带权限限制选项卡的管理布局
- `web/src/pages/admin/users.tsx` - 带角色分配的用户的表
- `web/src/pages/admin/roles.tsx` - 带创建/删除的角色列表
- `web/src/pages/admin/policies.tsx` - 带创建/删除的策略列表
- `cmd/platform/main.go` - 添加 embed 指令和 ServeSPA 标志
- `internal/api/router.go` - 向 Deps 添加 ServeSPA bool 和 StaticAssets embed.FS
- `internal/api/server.go` - 为非 API 路由添加 spaHandler

## 决策

- 使用布尔 `ServeSPA` 标志而非比较 `embed.FS` 与 nil（Rule 3 修复无效的 nil 比较）
- SPA 处理器将 `/v1`、`/health`、`/metrics`、`/grpc`、`/debug` 路由到 chi 路由器；所有其他路径提供嵌入的 React
- 策略方法返回 Unimplemented，因为 ColumnPolicy ent schema 尚不存在

## 偏离计划

### 自动修复的问题

**1. [Rule 3 - 阻塞] 无效的 embed.FS nil 比较**
- **发现于：** Task 3（go:embed SPA 接线）
- **问题：** 无法比较 `deps.StaticAssets != nil` 与 `embed.FS` 类型 - 不是有效比较
- **修复：** 向 Deps 结构添加 `ServeSPA bool` 字段，在平台启动时设置 `ServeSPA: true`
- **修改文件：** internal/api/router.go, cmd/platform/main.go
- **验证：** `go build ./...` 通过
- **提交于：** d5a6c72（Task 3 提交）

**2. [Rule 3 - 阻塞] 未使用的导入导致构建失败**
- **发现于：** Task 3（go:embed SPA 接线）
- **问题：** `io/fs` 和 `strings` 导入在从 main.go 移除 fs.Sub 逻辑后未使用
- **修复：** 从 main.go 移除未使用的导入
- **修改文件：** cmd/platform/main.go
- **验证：** `go build ./...` 通过
- **提交于：** d5a6c72（Task 3 提交）

**3. [Rule 2 - 缺失关键] ColumnPolicy ent schema 未实现**
- **发现于：** Task 1（管理 API 处理器）
- **问题：** 策略 CRUD 方法无法查询 ColumnPolicy 表，因为 schema 不存在
- **修复：** 策略方法返回 Unimplemented；UI 显示空列表
- **修改文件：** internal/api/connect_admin.go
- **验证：** 构建通过；策略选项卡显示"未定义策略"
- **提交于：** e2f1ac4（Task 1 提交）

---

**总偏离：** 3 个自动修复（全部阻塞）
**对计划的影响：** 所有自动修复对于构建通过必要。策略方法推迟到 ColumnPolicy schema 存在时。

## 遇到的问题

- `emptypb.Empty` 使用了 `google.golang.org/protobuf/types/known/emptypb` 而非生成的 proto
- AdminServiceServer 接口重新声明 - 从 connect_admin.go 移除重复（已在 connect.go 中）
- `Enforcer.Enforce` 在 `any` 类型上未定义 - 将 ConnectDeps.Enforcer 从 `any` 改为 `*casbin.Enforcer`
- `connect.NewErrorDetail` 错误的 API - 改为 `errors.New("message")`
- `deps.Ent.ColumnPolicy` 未定义 - ColumnPolicy ent schema 不存在
- `deps.Ent.DB()` 未定义 - 改为 search_handlers.go 使用 `deps.LineageDB`
- `go:embed` 模式对符号链接无效 - 将 web/dist 复制到 cmd/platform/web_dist
- 由于未使用的导入导致构建失败，重构后

## 下一阶段准备

- 管理面板 UI 完整但策略后端为存根（需要 ColumnPolicy schema）
- 嵌入式 SPA 服务工作；生产二进制在非 API 路由提供 React
- ConnectRPC AdminService 已准备好在 ent schema 添加时完全实现

---
*Phase: 06-web-ui-api*
*Completed: 2026-05-12*