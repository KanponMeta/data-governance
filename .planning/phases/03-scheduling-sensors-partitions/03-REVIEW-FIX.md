---
phase: 03-scheduling-sensors-partitions
padded_phase: "03"
fixed_at: 2026-05-08T10:20:00Z
review_path: .planning/phases/03-scheduling-sensors-partitions/03-REVIEW.md
iteration: 1
findings_in_scope: 4
fixed: 4
skipped: 0
status: all_fixed
---

# Phase 03: 代码审查修复报告

**修复时间：** 2026-05-08T10:20:00Z
**来源审查：** .planning/phases/03-scheduling-sensors-partitions/03-REVIEW.md
**迭代：** 1

**摘要：**
- 范围内的发现（WR-01 到 WR-04）：4
- 已修复：4
- 已跳过：0

## 已修复的问题

### WR-01: `UpsertSchedules` 在多副本启动时产生重复行的竞态条件

**修改的文件：** `internal/schedule/registry.go`
**提交：** 0b45bb2
**应用的修复：** 将 SELECT 然后 INSERT/UPDATE 序列包装在 `SERIALIZABLE` 事务中。在 SERIALIZABLE 隔离级别下，两个副本同时 SELECT"未找到"后不能同时 INSERT——一个事务会因序列化失败而中止，调用方可以重试。第 44 行的注释已更新以解释竞态条件和补救措施。

### WR-02: `sensor.upsertOneSensor` 与 WR-01 存在相同的的多副本启动竞态条件

**修改的文件：** `internal/sensor/registry.go`
**提交：** 0b45bb2（与 WR-01 相同的原子提交）
**应用的修复：** 与 WR-01 相同的模式：`upsertOneSensor` 内的 SELECT/INSERT 现在包装在 `SERIALIZABLE` 事务中。该函数现在正确提交或返回错误，并添加了描述性错误包装器。

### WR-03: 调度器 `shutdownCtx` 已创建但从未使用

**修改的文件：** `cmd/platform/scheduler.go`
**提交：** d07079b
**应用的修复：** 将 `_ = shutdownCtx`（死代码）替换为实际调用 `runOneTick(shutdownCtx)`。在 SIGTERM 时，调度器现在在携带配置的 `PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT` 的新上下文上排出任何进行中的 tick，使关闭超时行为与其文档意图一致。

### WR-04: `computeNextAndDetectMiss` 为时钟偏移返回一个未来窗口；`FireOneSchedule` 中没有保护

**修改的文件：** `internal/schedule/fire.go`
**提交：** 8281ab3
**应用的修复：** 在 `computeNextAndDetectMiss` 之后立即添加保护，检查 `windowToFire.After(now)`。当为 true 时（时钟偏移场景），将调度行 `next_fire_at` 前滚到 `windowToFire` 并提交事务而不插入 runs 行，保持语义正确性并防止记录未来窗口分区 key。

---

_修复时间：2026-05-08T10:20:00Z_
_修复者：Claude (gsd-code-fixer)_
_迭代：1_