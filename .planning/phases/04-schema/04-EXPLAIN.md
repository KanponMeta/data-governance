# Phase 4 — 递归 CTE EXPLAIN ANALYZE 捕获

状态：已延期。辅助工具（`scripts/seed_lineage_10k.sql` + `scripts/explain_analyze_lineage.sh`）已构建并可运行。实际捕获延期至在应用 Phase 4 migrations 的实时 PostgreSQL dev 实例上手动运行；结果必须粘贴到以下部分，然后才能最终交付。

逻辑签收已由用户在 2026-05-09 记录以解除 Phase 4 关闭阻塞。捕获仍然是待处理的人工-UAT 项目 — 通过 `04-HUMAN-UAT.md`（如果验证者产生）和 phase VERIFICATION.md 浮出水面。

## 如何运行

```bash
cd /home/developer/.kanpon/code/go/data-governance
export DATABASE_URL="postgres://platform_owner:platform_owner@localhost:5432/platform?sslmode=disable"
# (确保 platform migrations 已应用：./platform migrate)
bash scripts/explain_analyze_lineage.sh
```

脚本将用实际的 EXPLAIN ANALYZE 输出覆盖此文件。

## 验证

运行辅助工具后，确认每个项目：

- [ ] asset_edges_active_from / asset_edges_active_to 上的 Index Scan（不是 Seq Scan）
      （表示使用了部分索引 WHERE superseded_at IS NULL — D-13 结构缓解措施）
- [ ] Depth-10 运行时 < 200ms
      （PITFALLS §4 阈值：'如果 depth-10 CTE > 200ms，计划 graph-DB 迁移'）
- [ ] Depth-25 运行时 < 1000ms
      （可接受的硬上限边缘情况 — 不是热路径）
- [ ] 无 CTE 物化屏障（计划输出中无 'CTE Scan' + 'Materialize'）

验证者：kanpon（逻辑批准 — 实际捕获延期）
日期：2026-05-09