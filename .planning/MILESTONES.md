# 里程碑

## v1.0 MVP — 2026-05-12

**阶段:** 1-6 | **计划:** 37 | **提交:** 247 | **文件:** 616 | **Go 代码行数:** 113,421 | **TS 代码行数:** 156

### 主要成就

1. **平台基础** — PostgreSQL 存储与 Atlas 迁移，带 RLS 保护的事件日志，带邀请流程的 JWT 认证，稳定的连接器 ABI
2. **资产执行引擎** — Go DSL 资产定义，拓扑 DAG 执行，50 goroutine 原子声明，指数退避重试，全部 7 个连接器
3. **调度子系统** — 带错过窗口检测的 Cron 调度器，带 safeEvaluate 的事件传感器，时间/类别分区回填
4. **血缘与 Schema** — 自动捕获的资产/列血缘，递归 CTE 影响分析，带破坏性变更分类的 Schema diff
5. **治理引擎** — Casbin RBAC，哈希链审计日志，Snowflake DDM + BigQuery CLS 同步，带自动审批的治理工作流
6. **Web UI + ConnectRPC API** — 通过 go:embed 嵌入的 React SPA，资产仪表板，交互式血缘 DAG，治理收件箱

### 技术债务

- main.go 依赖注入缺口 (GovernanceWorkflow、Enforcer、AuthMW、QualityEvaluator 未连接)
- Phase 6 占位符: 质量趋势/告警 (QUAL-06/UI-05)、AdminService (UI-07)

### 归档

- [v1.0-ROADMAP.md](./milestones/v1.0-ROADMAP.md)
- [v1.0-REQUIREMENTS.md](./milestones/v1.0-REQUIREMENTS.md)
- [v1.0-MILESTONE-AUDIT.md](./v1.0-MILESTONE-AUDIT.md)