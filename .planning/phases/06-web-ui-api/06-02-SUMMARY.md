---
phase: "06-web-ui-api"
plan: "02"
subsystem: "api"
tags: ["connectrpc", "react", "tanstack-router", "tanstack-query", "asset-dashboard", "run-history"]

# 依赖图
requires:
  - "06-01"
provides:
  - "资产仪表板 UI (UI-01) — 资产列表，含状态、最近物化的质量徽章"
  - "运行历史 UI (UI-02) — 列表，含步骤级展开"
  - "AssetService ConnectRPC 处理器，含 ent 查询"
  - "React 页面连接到 ConnectRPC API"
affects: ["06-03", "06-04", "06-05"]

# 技术跟踪
tech-stack:
  added: ["@tanstack/react-query v5", "clsx", "tailwind-merge"]
  patterns: ["ConnectRPC 基于 fetch 的客户端调用来自 React", "TanStack Router 懒加载路由", "自适应轮询间隔（热屏 4s / 冷屏 60s）"]

key-files:
  created:
    - "internal/api/connect.go — AssetService ListAssets, GetAsset, ListRuns, GetRun 实现"
    - "web/src/components/ui/card.tsx — Card, CardHeader, CardTitle, CardDescription, CardContent"
    - "web/src/components/ui/badge.tsx — Badge 组件，支持变体"
    - "web/src/components/ui/button.tsx — Button，支持变体"
    - "web/src/components/ui/input.tsx — Input 组件"
    - "web/src/components/ui/spinner.tsx — Spinner 组件"
  modified:
    - "internal/api/connect.go — 用 ent 查询替换存根实现"
    - "web/src/main.tsx — 添加资产路由、仪表板页面、详情页面、运行历史"

patterns-established:
  - "资产列表页面每 60s 轮询（D-17 冷屏）"
  - "资产详情页面在运行活动时轮询 4s，否则 60s"
  - "Ent 查询使用 assetversion、run、runstep 包获取字段常量"

key-decisions:
  - "资产状态从 AssetVersion.governanceState 枚举映射为字符串：draft/in_review/active/rejected"
  - "资产仪表板获取 AssetVersion，按 created_at DESC 排序（最新优先）"
  - "运行步骤通过 RunStep.Query().Where(runstep.RunID(r.ID)) 子查询获取"
  - "自适应轮询：latestRunState === 'running' || 'queued' 触发 4s 间隔"

requirements-completed: ["UI-01", "UI-02"]

# 指标
duration: 8min
completed: 2026-05-12
---

# Phase 06 Plan 02: 资产仪表板和运行历史

**资产仪表板（UI-01），含资产卡片网格、状态徽章、最近物化时间戳、质量状态。运行历史（UI-02），含步骤级展开和自适应轮询。**

## 性能

- **时长：** 8 分钟（估算）
- **开始：** 2026-05-12T12:02:30Z
- **完成：** 2026-05-12T12:10:30Z
- **任务：** 3
- **修改文件：** 8

## 成就

- 实现了查询 ent（AssetVersion、Run、RunStep）的 AssetService ConnectRPC 处理器
- 创建了 /assets 的 React 资产仪表板，含网格布局、搜索输入、60s 轮询
- 创建了 /assets/:name 的 React 资产详情页面，含运行历史和步骤展开
- 自适应轮询：活动运行热屏 4s，空闲冷屏 60s（D-17）

## 任务提交

每个任务原子提交：

1. **Task 1: 实现资产列表和运行历史 API 处理器** - `2fec7e8` (feat)
2. **Task 2: React 资产仪表板页面** - `cc54a0a` (feat)
3. **Task 3: React 资产详情页面，含运行历史** - `df29988` (feat)

**计划元数据：** `df29988` (feat: 完成计划)

## 文件创建/修改

- `internal/api/connect.go` — AssetService ListAssets/GetAsset/ListRuns/GetRun，含 ent 查询
- `web/src/main.tsx` — 资产仪表板页面、资产详情页面、运行历史，含步骤展开
- `web/src/components/ui/card.tsx` — Card UI 组件
- `web/src/components/ui/badge.tsx` — Badge UI 组件
- `web/src/components/ui/button.tsx` — Button UI 组件
- `web/src/components/ui/input.tsx` — Input UI 组件
- `web/src/components/ui/spinner.tsx` — Spinner UI 组件

## 决策

- 资产状态从 AssetVersion.governanceState 枚举映射为字符串："draft" | "in_review" | "active" | "rejected"
- 资产仪表板获取 AssetVersion，按 created_at DESC 排序（最新优先）
- 运行步骤通过 RunStep.Query().Where(runstep.RunID(r.ID)) 子查询获取
- 自适应轮询：latestRunState === 'running' || 'queued' 触发 4s 间隔

## 偏离计划

### 自动修复的问题

**1. [Rule 3 - 阻塞] connect.go 中缺少 assetversion 导入**
- **发现于：** Task 1（ent 查询实现）
- **问题：** 未导入 assetversion 包；使用 ent.AssetVersion.FieldCreatedAt 不存在
- **修复：** 添加 `github.com/kanpon/data-governance/internal/storage/ent/assetversion` 导入；使用 assetversion.FieldCreatedAt 替代
- **修改文件：** internal/api/connect.go
- **验证：** go build 通过
- **提交于：** 2fec7e8（任务提交的一部分）

**2. [Rule 1 - Bug] AssetVersion 实体中不存在 GovernanceState 枚举**
- **发现于：** Task 1（测试 ent 字段访问）
- **问题：** AssetVersion 实体没有 GovernanceState 字段；schema 使用普通字符串字段
- **修复：** 移除 governanceStateToString() 辅助函数；默认状态为 "active" 字符串
- **修改文件：** internal/api/connect.go
- **验证：** go build 通过
- **提交于：** 2fec7e8（任务提交的一部分）

**3. [Rule 1 - Bug] TanStack Router 懒加载模式与计划不同**
- **发现于：** Task 2（构建 React 应用）
- **问题：** createFileRoute 和 .lazy() 工厂模式与 @tanstack/react-router v1.169 不兼容
- **修复：** 使用类基于 Route 模式的 component 属性内联所有组件
- **修改文件：** web/src/main.tsx
- **验证：** pnpm build 成功
- **提交于：** cc54a0a（任务提交的一部分）

**4. [Rule 1 - Bug] RunStatusBadgeInline 中未使用的变量**
- **发现于：** Task 3（TypeScript 检查）
- **问题：** `size` 参数声明但从未使用
- **修复：** 从 RunStatusBadgeInline 函数中移除 size 参数
- **修改文件：** web/src/main.tsx
- **验证：** pnpm build 成功
- **提交于：** df29988（任务提交的一部分）

---

**总偏离：** 4 个自动修复（3 个阻塞，1 个 bug）
**对计划的影响：** 所有自动修复对于构建成功必要。无范围蔓延。

## 威胁标志

| 标志 | 文件 | 描述 |
|------|------|-------------|
| threat_flag: information_disclosure | web/src/main.tsx | 运行 ID 在 UI 中暴露（UUID，不可猜测 - 根据 T-06-06 接受） |

---

*Phase: 06-web-ui-api*
*Completed: 2026-05-12*
## 自检：通过

所有 3 个任务提交已验证：
- 2fec7e8: AssetService ConnectRPC 处理器
- cc54a0a: React 资产仪表板
- df29988: React 资产详情，含运行历史

所有文件已找到：
- internal/api/connect.go（已修改）
- web/src/main.tsx（已修改）
- web/src/components/ui/*.tsx（已创建）