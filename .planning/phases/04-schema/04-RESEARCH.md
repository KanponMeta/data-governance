# Phase 4 研究报告

**阶段：** 04-schema
**审查时间：** 2026-05-09
**状态：** 已审查

## 概述

Phase 4 研究阶段涵盖 schema 和 lineage 子系统的技术调研，为后续执行计划提供技术基础。

## 技术决策研究

### 1. Builder DSL 扩展策略

**问题：** 如何在不影响 Phase 2/3 现有链式调用 API 的情况下，为 Phase 4 添加 Description、Owner、Tags、Column、ColumnLineage 等元数据字段？

**研究结论：**
- 采用"纯加法"扩展策略：现有 Phase 2/3 方法（Upstream、Connector、Materialize、Retry 等）保持不变
- Phase 4 方法（Description、Owner、Tags、Column 等）追加到现有链之后
- 防御性复制：Tags、Columns、ColumnLineage 访问器都返回副本，防止调用方意外修改

**文件：** `internal/asset/builder.go`

### 2. Code-Hash 指纹算法

**问题：** 如何为每个资产计算确定性、加密安全的指纹，用于版本绑定和变更检测？

**研究结论：**
- 使用 SHA-256 哈希（`crypto/sha256` 标准库）
- 规范化处理：
  - `upstreams`：预排序（字母顺序）
  - `tags`：预排序
  - `columns`：按 Name 排序；每列的 Tags 排序
  - `column_lineage` 值：按 (Asset, Column) 排序
- 使用 `encoding/json` 进行确定性 JSON 序列化（Go 1.12+ 自动排序 map 键）
- 包含的字段：name、upstreams、description、owner、tags、columns、column_lineage
- 排除的字段：connector_name、materialize_fn、retry_policy、schedule、sensors、partitions

**黄金哈希（已固定）：**
```
1ff892702afda232e57d98686b3f1c1cdcd3a4c50d71b0d79dd70b60ed99f431
```
对应输入：
```json
{"name":"orders","upstreams":[],"description":"Daily orders fact table","owner":"team-data@example.com","tags":["finance","pii"],"columns":[{"name":"total","description":"USD cents"},{"name":"user_id","description":"FK users.id","tags":["pii"]}],"column_lineage":{"user_id":[{"asset":"payments","column":"payer_id"}]}}
```

**文件：** `internal/asset/fingerprint.go`

### 3. SchemaDescriber 能力接口

**问题：** 如何让连接器可选地提供 DescribeSchema 功能，而不强制所有连接器实现？

**研究结论：**
- 使用 Go 的"可选接口"模式
- `connector.SchemaDescriber` 作为独立接口定义在 `capability.go`
- 运行时通过类型断言检测：`conn.(connector.SchemaDescriber)`
- 未实现的连接器返回 `ok=false`，不导致错误
- 编译时断言：`var _ connector.SchemaDescriber = (*Postgres)(nil)`

**接口定义：**
```go
type SchemaDescriber interface {
    DescribeSchema(ctx context.Context, ref AssetRef) (Schema, error)
}
```

**文件：** `internal/connector/capability.go`

### 4. PostgreSQL 类型规范化

**问题：** PostgreSQL 有多种等效类型表示（如 `character varying(N)` 和 `varchar(N)`），如何统一？

**研究结论：**
- 创建 `normalizePostgresType` 函数，将 PostgreSQL 类型映射到规范化形式
- 16 种类型映射（smallint→int16、integer→int32、bigint→int64 等）
- 保留原始类型作为后缀（如 `varchar(N)` → `varchar(N)`）

**文件：** `internal/connector/firstparty/postgres/types_normalize.go`

### 5. OpenLineage 转换器设计

**问题：** 如何将内部 lineage 模型转换为 OpenLineage RunEvent 格式，而不引入外部依赖？

**研究结论：**
- 内部实现 `RunEvent` 结构体，零外部依赖
- 不 vendoring ThijsKoot/openlineage-go（可信度低）
- 字段映射：
  - `runs.id` → `Run.RunID`
  - `runs.finished_at` → `EventTime`
  - `runs.asset_name` → `Job.Name`
  - asset_edges → `Inputs[]`
  - `runs.asset_name` → `Outputs[0]`
  - column_edges → `columnLineage facet`
- 时间点谓词：`FirstSeenAtLTE(run.StartedAt) AND (SupersededAtIsNil OR SupersededAtGT(run.StartedAt))`

**文件：** `internal/lineage/openlineage/translate.go`

### 6. Metadata 存储设计

**问题：** 如何实现元数据的审计跟踪和覆盖机制？

**研究结论：**
- 仅 INSERT 模式：`asset_metadata` 行永不 UPDATE
- GET 读取：`MAX(set_at)` 通过 `ORDER BY set_at DESC LIMIT 1` 获取最新值
- COALESCE 读取解析：`runtime_override` 字段优先于 `code_default`
- 支持标签合并（`Merge=true`）vs 替换（`Merge=false`）

**文件：** `internal/metadata/store.go`

### 7. CTE 递归遍历性能

**问题：** 递归 CTE 的 EXPLAIN ANALYZE 性能基准是什么？

**研究结论：**
- Depth-10 阈值：< 200ms（PITFALLS §4）
- Depth-25 阈值：< 1000ms（硬上限查询的可接受上限）
- 应使用 Index Scan（而非 Seq Scan）
- 无 CTE 物化 fence 警告

**验证工具：** `scripts/explain_analyze_lineage.sh`

## 技术栈清单

| 组件 | 技术 | 版本 | 用途 |
|------|------|------|------|
| 哈希 | `crypto/sha256` | stdlib | SHA-256 指纹 |
| JSON | `encoding/json` | stdlib | 确定性序列化 |
| 排序 | `sort` | stdlib | 预排序集合 |
| DB 池 | `pgxpool` | v5 | CLI DB 连接 |

## 风险评估

| 风险 | 严重程度 | 缓解措施 |
|------|----------|----------|
| 指纹规范化算法变更导致哈希不兼容 | 低 | 黄金哈希固定在测试中 |
| 可选接口实现不一致 | 低 | 编译时断言 |
| CTE 性能不达标 | 中 | EXPLAIN ANALYZE 验证 |

---
*Phase: 04-schema*
*Reviewed: 2026-05-09*
