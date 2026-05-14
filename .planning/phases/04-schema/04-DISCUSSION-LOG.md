# Phase 4: 血缘与 Schema - 讨论日志

> **仅作为审计追踪使用。** 请勿将此日志用作规划、研究或执行代理的输入。
> 决策捕获在 CONTEXT.md 中 — 本日志保留所考虑的替代方案。

**日期：** 2026-05-08
**阶段：** 04-schema
**讨论领域：** 血缘捕获 + 列 API、Schema 捕获机制、Schema 差异 + 破坏规则、存储 Schema + 遍历、元数据变更 API (META-03)、OpenLineage 兼容性、影响分析 API 表面 (LINE-06)

---

## 血缘捕获 + 列 API

### 资产级捕获模型

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 两者：静态 + 运行属性 | 在注册时从 `Asset.Upstreams()` 派生基线边，同时每次成功物化发出运行属性的 `lineage.captured` 事件。资产级边在图中是并集；事件提供时间序列 + 漂移检测。 | ✓ |
| 仅静态来自 Upstreams() | 在资产注册时持久化边；重新加载时重新计算。最简单。 | |
| 仅运行属性 | 每次物化发出新的 `(from_asset, to_asset, run_id, code_hash, observed_at)` 事件。 | |

**用户选择：** 两者：静态 + 运行属性
**备注：** 继承 D-01。

### 列级血缘声明 API

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 两者：构建器默认 + Mat. 覆盖 | `.ColumnLineage(...)` 声明静态默认；`MaterializeResult.ColumnLineage` 可在每次运行时覆盖。 | ✓ |
| 仅构建时静态 | 仅 `.ColumnLineage(map[string][]asset.ColumnRef{...})`。纯静态。 | |
| 仅运行时通过 MaterializeResult | 工程师仅填充 `MaterializeResult.ColumnLineage`。 | |

**用户选择：** 两者：构建器默认 + MaterializeResult 覆盖
**备注：** 继承 D-02。解决方案：运行时覆盖在每次运行中获胜；构建器默认在缺席时应用；两者都缺席 ⇒ 标记资产 `column_lineage_undeclared`。

### 代码哈希绑定（陷阱 #3 缓解）

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 资产定义指纹 | 在 `builder.Register()` 时哈希 `(name + sorted Upstreams + ColumnLineage map + Schema spec + metadata)`。*声明的*资产的稳定指纹。 | ✓ |
| MaterializeFunc 的源哈希 | `runtime.FuncForPC` + 嵌入源。函数移动时脆弱。 | |
| 用户管理的资产版本 | `.Version("v3")` 构建器方法；用户手动递增。 | |

**用户选择：** 资产定义指纹
**备注：** 继承 D-03。注意：捕获声明漂移，而非 "Materialize 中 SQL 改变但声明未变" — 该补偿机制是 D-04（通过 captured-Schema 不匹配进行漂移检测）。

### 漂移检测操作

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 警告 + 标记，运行成功 | 发出 `lineage.drift_detected` 事件，设置 `drift_pending`，运行正常完成。 | ✓ |
| 运行失败 | 硬错误 — 强制工程师在流水线再次运行之前更新声明。 | |
| 从观测到的 Schema 自动更新 | 平台覆盖声明以匹配观测到的；擦除用户的意图记录。 | |

**用户选择：** 警告 + 标记，运行成功
**备注：** 继承 D-04。与 Phase 1 D-09 "失败但不要阻塞生产" 保持一致。

---

## Schema 捕获机制

### 主要捕获机制

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 连接器 DescribeSchema() | 添加 `DescribeSchema(ctx, AssetRef) (Schema, error)` 能力。数据源：仓库本身。 | ✓ |
| 显式 MaterializeResult.Schema | 用户的 Materialize 返回 schema。错过带外 DDL。 | |
| 从 connector.Row 推断 | 遍历行，从 Go reflect 综合。空结果、混合类型时脆弱。 | |

**用户选择：** 连接器 DescribeSchema()
**备注：** 继承 D-05。

### CONN-08 向后兼容性策略

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 可选能力接口 | 将 `connector.SchemaDescriber` 定义为独立可选接口；运行时类型断言。 | ✓ |
| 必需方法在 connector.Connector 上 | 硬添加到现有接口。破坏每个现有第三方连接器。 | |
| 版本化接口（Connector + ConnectorV2） | 插件管理器加载两者。为时过早。 | |

**用户选择：** 可选能力接口
**备注：** 继承 D-06。为未来 Phase 5 RBAC/掩码接口建立能力模式。

### 捕获的 Schema 形状

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 丰富：name+type+nullable+default+pk+comment | 每列丰富形状 + 表级（PrimaryKey、RowCountEstimate）。 | ✓ |
| 最小：仅 name+type | 仅 `(name, type)`。最小存储。 | |
| 可插拔：连接器返回原生 + 标准化 | `{NativeSchema: any, NormalizedSchema: Schema}`。加倍复杂性。 | |

**用户选择：** 丰富：name+type+nullable+default+pk+comment
**备注：** 继承 D-07。足以检测 D-09 中每个破坏性变更类别，并为 META-03 列描述提供注释来源。

### 捕获频率

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 每次成功运行，去重哈希 | 每次成功运行运行 DescribeSchema；仅在哈希不同时插入新的 schema_versions 行。 | ✓ |
| 仅首次运行 + 选择加入重新捕获 | 在首次物化时捕获；除非标注 `.RecaptureSchema()`，否则跳过后续。错过带外 DDL。 | |
| 采样：每 N 次运行 + 出错时 | 每 N 次运行 + 失败时捕获。损害 META-01。 | |

**用户选择：** 每次成功运行，去重哈希
**备注：** 继承 D-08。DescribeSchema 错误：日志 + 发出 `schema.capture_failed` 事件，运行仍然成功。

---

## Schema 差异 + 破坏规则

### 破坏性变更分类

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| Drop、narrow、NULL→NOT NULL、PK change | 与大多数仓库 ABI 规则对齐；匹配下游代码实际破坏的内容。 | ✓ |
| 保守：任何变更都是破坏性的 | 简单规则，但每次列添加都会产生警报疲劳。 | |
| 每资产用户定义 | 每个资产声明其兼容性策略。为时过早。 | |

**用户选择：** Drop、narrow、NULL→NOT NULL、PK change
**备注：** 继承 D-09。重命名检测为 `(drop, add)` — v1 中无启发式。类型 narrowing 规则按连接器类型格。

### 意图破坏覆盖机制

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 是：通过 API 确认 + 审计追踪 | 新 CLI/REST `./platform schema ack-break <id> --reason="..."`。设置 `acknowledged_at`/`acknowledged_by`。 | ✓ |
| 是：在代码中声明时 | `.IntentionalBreak("v3-drop-legacy-id", ...)` 构建器方法。重型人体工程学。 | |
| 否 — 每个破坏都是永久记录 | 最干净的数据模型。警报疲劳，治理团队编写过滤 SQL。 | |

**用户选择：** 是：通过 API 确认 + 审计追踪
**备注：** 继承 D-10。确认是附加的 — 行从不删除。理由为纯文本但必填。

### Schema 变更记录存储

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 专用表 + event_log 条目 | `schema_changes` 表用于查询；带负载指针的 `schema.change_detected` 事件。两个存储，无负载重复。 | ✓ |
| 仅 event_log | 将每个差异编码为带完整负载的事件。较慢的 JSON 提取；失去 ack 工作流。 | |
| 仅专用表 | 错过 Phase 1 D-09 审计不可变性承诺。 | |

**用户选择：** 专用表 + event_log 条目
**备注：** 继承 D-11。`schema_versions` 是第三个表 — 完整快照由 `schema_changes.{prev,new}_version_id` 指向。

### META-05 时间线计算

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 从 schema_changes 行派生 | 时间线 = `SELECT * FROM schema_changes WHERE asset=$1 AND column_name=$2`。单一来源。 | ✓ |
| 物化 column_history 表 | 对于非常长的历史更快，但没有证据表明 v1 容量需要。 | |
| 从 schema_versions 快照重构 | 遍历行，查询时差异。已被捕获频率去重设计拒绝。 | |

**用户选择：** 从 schema_changes 行派生
**备注：** 继承 D-12。物化汇总延迟到查询性能数据驱动。

---

## 存储 Schema + 遍历

### 邻接表布局

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 分离：asset_edges + column_edges | 两个表 — 资产遍历命中小表。更清晰的索引。通过结构缓解陷阱 #4。 | ✓ |
| 统一：单个边表，列为 nullable | 一个 `lineage_edges`，资产级为 NULL。陷阱 #4 特别警告此设计。 | |
| 星型：节点 + 带类型列的边 | 最规范化；最便宜的边；每次遍历都添加 JOIN。 | |

**用户选择：** 分离：asset_edges + column_edges
**备注：** 继承 D-13。

### 遍历 API + 安全守卫

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 递归 CTE + 硬深度上限（默认 10，最大 25） | PostgreSQL `WITH RECURSIVE`。双向索引。DoS 防护的硬上限 25。 | ✓ |
| 无上限递归 CTE，应用过滤 | 服务器返回完整闭包；客户端过滤深度。陷阱 #4 特别警告："无深度限制是 DoS 向量"。 | |
| 应用侧 BFS 遍历边表 | 每跳都是网络往返。陷阱 #4 特别推荐递归 CTE。 | |

**用户选择：** 递归 CTE + 硬深度上限（默认 10，最大 25）
**备注：** 继承 D-14。EXPLAIN ANALYZE 在规划期间捕获。

### 重新物化变更血缘时的边版本控制

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 软退休：保留，标记 superseded_at | 时间表模式：`(first_seen_*, last_seen_*, superseded_at)`。活跃查询过滤 `superseded_at IS NULL`；时间点查询按时间范围过滤。 | ✓ |
| 硬替换：每次运行删除并重新插入 | 血缘历史丢失。 | |
| 每代码哈希版本：并行边集 | 每次 API 调用必须指定 code_hash。过度工程。 | |

**用户选择：** 软退休：保留，标记 superseded_at
**备注：** 继承 D-15。Phase 1 D-09 不可变性扩展到血缘边（无 DELETE）。

### 查询管理分离

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| sqlc 用于遍历热读 | `queries/lineage.sql` 中的递归 CTE；sqlc 生成类型安全 Go 绑定。ent 用于 CRUD。匹配 CLAUDE.md 技术栈。 | ✓ |
| ent + 原始 SQL 中的手动递归 CTE | 绕过类型安全。CLAUDE.md 支持 sqlc。 | |
| 纯 ent 遍历辅助函数 | ent 原生不支持递归 CTE。慢。 | |

**用户选择：** sqlc 用于遍历热读
**备注：** 继承 D-16。模式匹配 Phase 2 的 `internal/run/claim.go`。

---

## 元数据变更 API (META-03)

### 设置描述/所有者/标签

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 两者：代码默认 + 运行时可变 | 构建器方法声明绑定到 code_hash 的默认值；REST PATCH 端点允许运行时覆盖。通过 event_log 审计。 | ✓ |
| 仅代码声明 | 最干净的血统，但治理团队无法自助。破坏 PROJECT.md 核心价值。 | |
| 仅运行时 | 最大灵活性，但每个新资产从空开始。无版本控制血统。 | |

**用户选择：** 两者：代码默认 + 运行时可变
**备注：** 继承 D-17。解决 PROJECT.md 紧张关系：工程师交付默认值；治理可以在不重新部署的情况下进行纠正。

---

## OpenLineage 兼容性

### 发送策略

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 混合：内部存储，导出时 OL | 原生内部 Schema；按需通过 `./platform lineage export --format=openlineage` 生成 OL JSON。无运行时依赖 `ThijsKoot/openlineage-go`（低可信度）。 | ✓ |
| 从第一天起 OL 兼容（飞行中） | 向每个事件接收器发送 OpenLineage RunEvent JSON。为时过早的互操作投资。 | |
| 仅内部，收到请求时 OL | 完全跳过 OL。混合实现相同结果，只有一个惰性翻译器。 | |

**用户选择：** 混合：内部存储，导出时 OL
**备注：** 继承 D-18。

---

## 影响分析 API 表面 (LINE-06)

### API 表面

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| REST + CLI，两者包装相同的 Go 包 | `internal/lineage/impact.go` 公开库；REST `GET /lineage/impact`；CLI `./platform impact ...`。三个表面，一个逻辑。 | ✓ |
| 仅 REST | Phase 4 没有 UI；操作员必须 curl。 | |
| 仅 CLI，REST 在 Phase 6 | 外部警报机器人需要 REST。边际范围节省微薄。 | |

**用户选择：** REST + CLI，两者包装相同的 Go 包
**备注：** 继承 D-19。

### 方向处理

| 选项 | 描述 | 选择 |
|--------|-------------|----------|
| 方向参数，单一端点 | `?direction=downstream|upstream` 在 `/lineage/impact` 上。相同的递归 CTE 模板。 | ✓ |
| 两个端点（影响/依赖） | 为相同逻辑加倍表面积。 | |
| 仅影响（下游）；上游是 Phase 6 | 调查故障的操作员几乎总是也想要上游。 | |

**用户选择：** 方向参数，单一端点
**备注：** 继承 D-20。

---

## Claude 的裁量权

（请参阅 CONTEXT.md "Claude 的裁量权" 小节。）

- `Schema.Columns` 的确切 JSONB 形状（哈希的往返稳定性；`Column` 内的字段排序是实现细节）。
- 资产定义指纹是否哈希 Description/Owner/Tags（倾向于是，可能在规划期间修订）。
- 确切的 `schema_changes.change_type` 枚举值。
- `./platform impact` 和 `./platform schema diff` 的 CLI 输出格式。
- `AssetVersion` 是自己的 ent 实体还是由 `(asset, code_hash)` 连接隐含。
- `MaterializeResult.ColumnLineage` Go 类型形状（map vs slice）。
- OL 导出端点是否位于 `/lineage/export` 或 `/exports/lineage`。
- 公开 API 破坏性变更类别的数量。
- `connector.SchemaDescriber` 是否返回 `(Schema, Diagnostics, error)` 而非 `(Schema, error)`。

## 推迟的想法

（请参阅 CONTEXT.md `<deferred>` 部分，包括 PII 标签传播、ALINE-01 SQL 推理、ALINE-02 OL 摄入、启发式重命名检测、每资产兼容性策略、物化 column_history 汇总、非自省连接器的模式推理回退、感知分区的血缘边、资产版本差异 REST 端点。）