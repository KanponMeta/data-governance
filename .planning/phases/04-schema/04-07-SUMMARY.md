# Phase 4 Plan 07 执行总结

**执行时间：** 2026-05-09
**实际耗时：** 60 分钟
**计划类型：** execute / wave-7
**依赖：** 04-04, 04-05, 04-06

## 任务完成情况

| 任务 | 名称 | 提交 | 文件 |
|------|------|------|------|
| 1 | 元数据存储 + 处理器 | `b9ae6d0` | internal/metadata/store.go, store_test.go, handler.go, handler_test.go, api/metadata_handlers.go |
| 2 | Schema 变更确认 + 时间线处理器 | `a081493` | internal/api/schema_handlers.go, schema_handlers_test.go, Deps 扩展 |
| 3 | OpenLineage 转换器 + Lineage REST 处理器 | `ea72963` | internal/lineage/openlineage/translate.go, translate_test.go, api/lineage_handlers.go |
| 4 | 路由布线 + 启动 | `c144eaf` | internal/api/router.go, cmd/platform/main.go |

## 新增文件清单

| 文件 | 说明 |
|------|------|
| internal/metadata/store.go | 元数据存储（INSERT-only，COALESCE 读取逻辑） |
| internal/metadata/store_test.go | 存储单元测试（5 个测试） |
| internal/metadata/handler.go | chi HTTP 处理器（PatchAsset, PatchColumn, Get） |
| internal/metadata/handler_test.go | 处理器单元测试（6 个测试） |
| internal/lineage/openlineage/translate.go | OpenLineage RunEvent 转换器 |
| internal/lineage/openlineage/translate_test.go | 转换器单元测试（6 个测试） |
| internal/api/schema_handlers.go | Schema 变更确认和时间线处理器 |
| internal/api/schema_handlers_test.go | Schema 处理器测试（8 个测试） |
| internal/api/lineage_handlers.go | 影响分析和导出处理器 |
| internal/api/lineage_handlers_test.go | Lineage 处理器测试（7 个测试） |
| internal/api/metadata_handlers.go | 元数据处理器薄胶水层 |

## 修改文件清单

| 文件 | 说明 |
|------|------|
| internal/api/router.go | Deps 扩展 + Phase 4 路由挂载 |
| internal/event/types.go | MetadataUpdatedPayload 结构体 |
| internal/auth/middleware.go | ContextWithPrincipal + TestPrincipalKey 辅助函数 |
| cmd/platform/main.go | Phase 4 启动布线（pgxpool, OLTranslator, Ent） |

## API 路由汇总

| 方法 | 路径 | 认证 | 说明 |
|------|------|------|------|
| GET | /v1/lineage/impact | JWT | 影响分析（任意已认证用户） |
| GET | /v1/lineage/export | JWT | OpenLineage 导出（任意已认证用户） |
| GET | /v1/schema/changes | JWT | Schema 变更时间线（任意已认证用户） |
| GET | /v1/assets/{name}/metadata | JWT | 读取资产元数据（任意已认证用户） |
| POST | /v1/schema/changes/{id}/ack | JWT + governance | 确认 Schema 变更 |
| PATCH | /v1/assets/{name}/metadata | JWT + governance | 更新资产级元数据 |
| PATCH | /v1/assets/{name}/columns/{col}/metadata | JWT + governance | 更新列级元数据 |

## Deps 扩展

```go
type Deps struct {
    Auth           *auth.Service
    Issuer         *auth.TokenIssuer
    Storage        storage.Storage
    Events         event.Writer
    Version        string
    // Phase 4 新增字段:
    Ent            *ent.Client              // schema-ack 变更、元数据存储
    LineageDB      lineageq.DBTX             // 影响分析处理器 (sqlc client)
    OLTranslator   openlineage.Translator   // 导出处理器
}
```

## 实施决策记录

1. **Deps 扩展时机**：在 Task 2 提交中扩展（非 Task 4），避免 schema_handlers.go 无法编译
2. **LineageDB 类型**：`lineageq.DBTX` 而非 `*lineageq.Queries`，因为 `impact.Analyze` 需要原始 DB 连接
3. **OLTranslator 复用**：复用 `store.Ent()` 避免二次 `ent.Open()` 调用
4. **Nil LineageDB 处理**：返回 503 而非 panic，保证开发模式和单元测试安全
5. **ThijsKoot 未引入**：CLAUDE.md 标记为 LOW 可信度；RunEvent 结构体足够小，可内联实现
6. **ContextWithPrincipal 导出**：从 auth 包导出，支持跨包处理器测试

## 威胁缓解状态

| 威胁 ID | 类别 | 组件 | 处置 | 缓解措施 |
|---------|------|------|------|----------|
| T-04-07-01 | D (DoS) | GET /v1/lineage/impact?depth=N | mitigate | 硬上限 25 在 handler 层强制执行；depth>25 返回 HTTP 400 |
| T-04-07-02 | T (篡改) | GET /v1/lineage/impact?asset=… | mitigate | 资产名称验证正则 `^[a-zA-Z0-9_.\-]{1,256}$` |
| T-04-07-03 | E (提权) | POST /v1/schema/changes/:id/ack | mitigate | RequireRole("governance") 中间件 |
| T-04-07-04 | E (提权) | PATCH /v1/assets/:name/metadata | mitigate | RequireRole("governance") 中间件 |
| T-04-07-05 | I (信息泄露) | GET /v1/lineage/export | mitigate | JWT 保护组内挂载 |
| T-04-07-06 | D (DoS) | PATCH with massive tags array | mitigate | 1MB body limit + MaxTags=64 |

## 下一步

Phase 4 Plan 08（Wave 8 — CLI 子命令 + E2E 测试套件 + EXPLAIN ANALYZE 工具）是 Phase 4 的最终计划。
