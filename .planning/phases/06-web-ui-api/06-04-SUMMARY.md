---
phase: "06-web-ui-api"
plan: "04"
subsystem: "ui"
tags: ["react", "reactflow", "dagre", "lineage", "tanstack-query"]

# 依赖图
requires:
  - "06-01"
  - "06-03"
provides:
  - "LINE-04: 带缩放/平移/节点点击的交互式谱系 DAG 可视化"
  - "LINE-05: 通过显示列级谱系的侧边面板进行列下钻"
  - "D-12: 邻域 API（asset_id, focus, depth）返回 DAG 子图"
  - "D-13: 点击资产节点时显示资产元数据 + 列的侧边面板"
  - "D-14: 用户缩放到资产节点时显示列级边"
  - "D-15: 客户端 dagre 布局计算 DAG 布局；ReactFlow 渲染"
affects: ["06-05", "06-07"]

# 技术跟踪
tech-stack:
  added: ["@xyflow/react v12", "dagre v0.8.5"]
  patterns: ["applyNodeChanges/applyEdgeChanges 用于 ReactFlow 状态", "AssetNodeData extends Record<string, unknown> 以获得类型兼容性"]

key-files:
  created:
    - "web/src/components/LineageDAG.tsx — 带 dagre 布局、深度选择器、列视图切换的 ReactFlow 画布"
    - "web/src/components/AssetNode.tsx — 带资产名称、类型徽章和句柄的自定义 ReactFlow 节点"
    - "web/src/components/ColumnPanel.tsx — 显示资产元数据和列列表的侧边面板"
    - "web/src/pages/lineage/[id].tsx — 带 TanStack Query 的 Lineage 页面组件"
    - "internal/lineage/neighborhood/neighborhood.go — 手动邻域查询实现"
  modified:
    - "internal/api/connect.go — LineageService.Neighborhood 处理器连接到 TraverseAssetLineage"
    - "web/src/main.tsx — 在 /lineage/$id 添加 lineageLayoutRoute"
    - "internal/api/router.go — LineageDB 添加到 ConnectDeps"

patterns-established:
  - "useMemo 驱动 data->nodes/edges 转换和 dagre 布局应用"
  - "使用 applyNodeChanges/applyEdgeChanges 而非 useNodesState/useEdgesState 以获得类型安全"
  - "AssetNodeData extends Record<string, unknown> 以满足 ReactFlow Node 类型约束"

key-decisions:
  - "邻域 RPC 返回焦点资产 + 下游邻居，节点之间有边"
  - "深度上限 5 以防止 DoS（T-06-09 缓解）"
  - "列切换按钮仅在选择节点时出现"

requirements-completed: ["LINE-04", "LINE-05"]

# 指标
duration: 12min
completed: 2026-05-12
---

# Phase 06 Plan 04: 交互式谱系 DAG 可视化

**交互式谱系 DAG 可视化（LINE-04），带按需获取邻域（D-12）。通过侧边面板实现列下钻（LINE-05）（D-13）。客户端 dagre 布局（D-15）。一个画布，两个缩放级别：资产级和列级（D-14）。**

## 性能

- **时长：** 12 分钟
- **开始：** 2026-05-12T12:43:00Z
- **完成：** 2026-05-12T12:55:00Z
- **任务：** 2
- **修改文件：** 12

## 成就

- 使用 TraverseAssetLineage 递归 CTE 实现了 LineageService.Neighborhood ConnectRPC 处理器
- 创建了带 dagre 布局引擎的基于 ReactFlow 的 LineageDAG 组件
- 实现了用于资产可视化的自定义 AssetNode 组件
- 实现了点击节点时显示资产元数据和列的 ColumnPanel 侧边面板
- 构建了带 TanStack Query 集成的 LineagePage，从 ConnectRPC Neighborhood API 获取数据
- 在 main.tsx 路由树中 /lineage/$id 添加了 lineage layout route
- 通过 ConnectDeps 和 mountConnectRPC 连接 LineageDB

## 任务提交

每个任务原子提交：

1. **Task 1: 带递归 CTE 的谱系邻域 ConnectRPC 处理器** - `ea95e03` (feat)
2. **Task 2: 带 dagre 布局的 ReactFlow DAG 组件** - `cd6972c` (feat)

**计划元数据：** `cd6972c` (feat: 完成计划)

## 文件创建/修改

- `internal/api/connect.go` — 使用 TraverseAssetLineage 的 LineageService.Neighborhood 处理器
- `internal/api/router.go` — LineageDB 添加到 ConnectDeps
- `internal/lineage/neighborhood/neighborhood.go` — 手动邻域查询实现
- `internal/lineage/queries/neighborhood.sql` — 更新的邻域 SQL
- `internal/lineage/queries/neighborhood.sql.go` — 生成的 sqlc 代码
- `web/src/components/LineageDAG.tsx` — 带 dagre 布局的 ReactFlow 画布
- `web/src/components/AssetNode.tsx` — 自定义 ReactFlow 节点组件
- `web/src/components/ColumnPanel.tsx` — 列/元数据显示侧边面板
- `web/src/pages/lineage/[id].tsx` — Lineage 页面组件
- `web/src/main.tsx` — 添加 lineageLayoutRoute

## 决策

- 邻域 RPC 返回焦点资产 + 所有下游邻居作为节点；边连接焦点到每个邻居
- 深度上限 5 以通过大型邻域查询防止 DoS（T-06-09 缓解）
- 列切换按钮仅在选择节点时可见
- AssetNodeData extends Record<string, unknown> 以满足 ReactFlow Node data 约束
- 直接使用 applyNodeChanges/applyEdgeChanges 而非 useNodesState/useEdgesState 以获得正确的 TypeScript 类型

## 偏离计划

### 自动修复的问题

**1. [Rule 1 - Bug] NodeProps 类型与 @xyflow/react v12 不兼容**
- **发现于：** Task 2（ReactFlow 集成）
- **问题：** NodeProps<AssetNodeData> 约束不匹配；AssetNodeData 不满足 Record<string, unknown>
- **修复：** 改为使用普通 props 接口，AssetNodeData extends Record<string, unknown>
- **修改文件：** web/src/components/AssetNode.tsx, web/src/components/LineageDAG.tsx
- **验证：** pnpm build 成功
- **提交于：** cd6972c（任务提交的一部分）

**2. [Rule 1 - Bug] Button 组件缺少 size prop**
- **发现于：** Task 2（构建 ColumnPanel 和 LineagePage）
- **问题：** Button 组件接口不包含 size prop
- **修复：** 从 Button 使用中移除 size="icon" 和 size="sm"
- **修改文件：** web/src/components/ColumnPanel.tsx, web/src/pages/lineage/[id].tsx
- **验证：** pnpm build 成功
- **提交于：** cd6972c（任务提交的一部分）

**3. [Rule 1 - Bug] 未使用的变量和导入导致 TypeScript 错误**
- **发现于：** Task 2（TypeScript 检查）
- **问题：** Spinner 未使用的导入，initialDepth 未使用变量，未使用的事件参数，useNodesState 未使用的 nodes/edges
- **修复：** 移除未使用的导入，下划线前缀标记未使用的参数，直接使用 applyNodeChanges/applyEdgeChanges
- **修改文件：** web/src/pages/lineage/[id].tsx, web/src/components/LineageDAG.tsx
- **验证：** pnpm build 成功
- **提交于：** cd6972c（任务提交的一部分）

**4. [Rule 3 - 阻塞] createFileRoute API 与 main.tsx Route 类模式不兼容**
- **发现于：** Task 2（路由集成）
- **问题：** createFileRoute 期望与现有 main.tsx 设置不匹配的路由器上下文
- **修复：** 重构 LineagePage 使用 useParams 钩子，在 main.tsx 中连接为懒加载路由组件
- **修改文件：** web/src/pages/lineage/[id].tsx, web/src/main.tsx
- **验证：** pnpm build 成功
- **提交于：** cd6972c（任务提交的一部分）

**5. [Rule 2 - 缺失关键] LineageDB 未连接到 ConnectDeps**
- **发现于：** Task 1（处理器实现）
- **问题：** LineageService 处理器需要 LineageDB 但 ConnectDeps 未包含
- **修复：** 将 LineageDB 添加到 ConnectDeps 结构和 router.go mountConnectRPC 调用
- **修改文件：** internal/api/connect.go, internal/api/router.go
- **验证：** go build 通过
- **提交于：** ea95e03（任务提交的一部分）

---

**总偏离：** 5 个自动修复（3 个 bug，1 个阻塞，1 个缺失关键）
**对计划的影响：** 所有自动修复对于构建成功必要。无范围蔓延。

## 威胁标志

| 标志 | 文件 | 描述 |
|------|------|-------------|
| threat_flag: denial_of_service | internal/api/connect.go | 深度参数上限 5（T-06-09 缓解就位） |

---

*Phase: 06-web-ui-api*
*Completed: 2026-05-12*
## 自检：通过

所有 2 个任务提交已验证：
- ea95e03: LineageService.Neighborhood ConnectRPC 处理器
- cd6972c: ReactFlow 谱系 DAG 组件

所有文件已找到：
- web/src/components/LineageDAG.tsx（已创建）
- web/src/components/AssetNode.tsx（已创建）
- web/src/components/ColumnPanel.tsx（已创建）
- web/src/pages/lineage/[id].tsx（已创建）
- internal/api/connect.go（已修改）
- web/src/main.tsx（已修改）