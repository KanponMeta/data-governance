---
phase: "06-web-ui-api"
plan: "06"
subsystem: "api"
tags: ["connectrpc", "governance", "react", "tanstack-query", "workflow"]

# 依赖图
requires: ["06-01", "05-04"]
provides:
  - "GovernanceService ConnectRPC 处理器（ListReviews, GetReview, ApproveReview, RejectReview）"
  - "/governance 的治理收件箱 React 页面"
  - "ReviewCard 和 ReviewModal 组件"
  - "待处理审查的 20s 轮询（D-17 热屏）"
affects: []

# 技术跟踪
tech-stack:
  added: ["lucide-react"]
  patterns: ["调用 Phase 5 Workflow 服务的 ConnectRPC 治理处理器", "用于待批准/已批准/已拒绝审查的 React 选项卡", "从 fetch 请求的 cookie 中获取 CSRF 令牌"]

key-files:
  created:
    - "web/src/pages/governance/index.tsx — 带选项卡和 ReviewModal 的治理收件箱页面"
    - "web/src/components/ui/dialog.tsx — Radix Dialog 包装器"
    - "web/src/components/ui/textarea.tsx — 带标签样式的 Textarea 组件"
    - "web/src/components/ui/label.tsx — Label 组件"
    - "web/src/components/ui/tabs.tsx — 带状态管理的选项卡"
  modified:
    - "internal/api/connect.go — ConnectDeps 添加 GovernanceWorkflow，实现 ListReviews/GetReview/ApproveReview/RejectReview"
    - "internal/api/router.go — 添加 ConnectGovernanceWorkflow 字段，传递给 mountConnectRPC"
    - "web/src/components/ui/spinner.tsx — 添加 size prop 支持"
    - "web/src/main.tsx — 添加带 /governance 路径的 governanceLayoutRoute"

patterns-established:
  - "GovernanceService 处理器直接调用 governance.Workflow.Approve/Reject（不通过 REST）"
  - "通过 Casbin enforcer.Enforce 在状态更改操作前检查权限"
  - "ReviewModal 发送从 dg_csrf cookie 提取的 X-CSRF-Token 头"

requirements-completed: ["UI-06"]

# 指标
duration: 18min
completed: 2026-05-12
---

# Phase 06 Plan 06: 治理收件箱（UI-06）

**审查请求的治理收件箱：ConnectRPC 处理器 + 带批准/拒绝模态框的 React 页面**

## 性能

- **时长：** 18 分钟（1080 秒）
- **开始：** 2026-05-12T12:17:00Z
- **完成：** 2026-05-12T12:35:00Z
- **任务：** 2
- **修改文件：** 8

## 成就

- 实现了 GovernanceService ConnectRPC 处理器：ListReviews, GetReview, ApproveReview, RejectReview
- 向 ConnectDeps 添加 GovernanceWorkflow 字段以直接访问 Phase 5 工作流服务
- 在 /governance 创建了带待批准/已批准/已拒绝选项卡的治理收件箱 React 页面
- ReviewCard 显示资产名称、提交者、submitted_at、状态徽章
- ReviewModal 提供带必需评论字段的批准/拒绝操作
- 待处理审查的 20s 轮询间隔（D-17 热屏优化）
- 从 GET /v1/me 的 canApprove 权限检查限制收件箱可见性
- 批准/拒绝 fetch 请求中发送 X-CSRF-Token 头

## 任务提交

1. **Task 1: 治理审查 ConnectRPC 处理器** - `d21dd83` (feat)
2. **Task 2: 治理收件箱 React 页面** - `38c968d` (feat)

**计划元数据：** `38c968d` (feat: 添加带批准/拒绝模态框的治理收件箱 React 页面)

## 文件创建/修改

- `internal/api/connect.go` — 向 ConnectDeps 添加 GovernanceWorkflow；ListReviews/GetReview/ApproveReview/RejectReview 实现
- `internal/api/router.go` — 添加 ConnectGovernanceWorkflow 字段，传递给 mountConnectRPC
- `web/src/pages/governance/index.tsx` — 带选项卡和 ReviewModal 的完整治理收件箱页面
- `web/src/main.tsx` — 添加带 /governance 路径的 governanceLayoutRoute
- `web/src/components/ui/dialog.tsx` — Radix Dialog 包装器
- `web/src/components/ui/textarea.tsx` — 带标签样式的 Textarea
- `web/src/components/ui/label.tsx` — Label 组件
- `web/src/components/ui/tabs.tsx` — 带活跃状态管理的选项卡
- `web/src/components/ui/spinner.tsx` — 添加 size prop

## 决策

- ConnectRPC 处理器直接调用 governance.Workflow.Approve/Reject/Get/Status（不通过 REST）以获得类型安全
- 通过 Casbin enforcer.Enforce 在批准/拒绝操作前检查权限
- 拒绝需要评论（D-12 来自 Phase 5）；ErrCommentRequired 映射到 CodeInvalidArgument
- 待处理审查 20s 轮询（D-17）；已批准/已拒绝 60s
- 如果 canApprove=false，治理页面显示"权限拒绝"卡片

## 偏离计划

### 自动修复的问题

**1. [Rule 3 - 阻塞] Spinner 组件缺少 size prop**
- **发现于：** Web 构建验证
- **问题：** `<Spinner size="sm" />` 在 ReviewModal 中因 spinner 不支持 size prop 而导致 TypeScript 失败
- **修复：** 更新 Spinner 接受 size prop，默认 "h-6 w-6"
- **修改文件：** web/src/components/ui/spinner.tsx
- **提交：** 38c968d（任务提交的一部分）

**2. [Rule 3 - 阻塞] connect.NewErrorDetail 错误用法**
- **发现于：** Go 构建验证
- **问题：** `connect.NewErrorDetail("comment is required")` 返回 (val, error) 元组；不能作为单个参数使用
- **修复：** 在 RejectReview 处理器中改为 `errors.New("comment is required")`
- **修改文件：** internal/api/connect.go
- **提交：** d21dd83（任务提交的一部分）

**3. [Rule 3 - 阻塞] GovernanceWorkflow 不在 Deps 结构中**
- **发现于：** Go 构建验证
- **问题：** ConnectDeps 有 GovernanceService 但没有处理器逻辑所需的工作流服务
- **修复：** 向 ConnectDeps 和 router.go Deps 添加 `GovernanceWorkflow *governance.Workflow` 字段
- **修改文件：** internal/api/connect.go, internal/api/router.go
- **提交：** d21dd83（任务提交的一部分）

---

**总偏离：** 3 个自动修复（全部阻塞）
**对计划的影响：** 所有自动修复对于构建成功必要。无范围蔓延。

## 威胁模型合规

| 威胁 ID | 缓解 | 状态 |
|---------|------------|--------|
| T-06-11（审查操作篡改） | CSRF 令牌 + canApprove 权限 + 必需评论 | 已实现 |
| T-06-12（权限提升） | /v1/me 的 canApprove 标志；工作流验证审查池 | 已实现 |

## 验证

- `go build ./internal/api/...` 成功
- `cd web && pnpm run build` 成功
- /governance 的治理收件箱页面（用户有 canApprove 权限时）
- 批准/拒绝需要评论字段（客户端和服务器端验证）

## 下一阶段准备

- GovernanceService ConnectRPC 处理器已完全实现
- 工作流服务集成完成
- 带 20s 轮询的 React 收件箱页面已就绪
- 后续计划无阻碍

---
*Phase: 06-web-ui-api*
*Completed: 2026-05-12*