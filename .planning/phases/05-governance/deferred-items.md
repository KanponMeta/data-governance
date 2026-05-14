## Deferred — pre-existing test flakiness (out of scope for plan 05-02)

**文件：** internal/connector/firstparty/snowflake/snowflake_test.go::TestSnowflake_Write
**症状：** 由于 INSERT 列顺序中非确定性 map 迭代顺序导致的间歇性失败。
**为何推迟：** 非 plan 05-02 变更导致的预先存在 bug。应在 snowflake.go 的 Write 中对列切片排序后再构建 INSERT，或使用忽略顺序的 sqlmock 参数匹配器来修复。
**发现于：** plan 05-02 广泛测试扫描。

## Deferred — pre-existing testcontainer flakiness (out of scope for plan 05-04)

**文件：** internal/governance/testharness/postgres.go::NewTestPostgres
**症状：** `postgres not ready: failed to connect ... read: connection reset by peer / unexpected EOF` 在此主机上 100% 可复现（Linux 6.17 / Docker 29.4 / testcontainers-go v0.42.0）。pgx 池 ping 在 Postgres 容器的 TCP 监听器完成初始化之前发生 — 没有重试循环。
**为何推迟：** 非 plan 05-04 变更导致的预先存在 bug。05-01 中提交的相同 TestPostgresContainer 在此主机上同样失败。所有 Phase 5 计划通过 `if testing.Short() { t.Skip() }` 短路 DB 支持的测试。
**建议修复：** 向 NewTestPostgres 添加 `pingWithRetry` 帮助程序，在宣布容器未就绪之前循环退避约 30 秒。或者提高 `postgres.WithStrategy(wait.ForLog(...))` 超时。
**发现于：** plan 05-04 广泛测试扫描。
## Deferred — pre-existing ent codegen panic (out of scope for plan 05-03)

**文件：**
- internal/api/schema_handlers_test.go::TestAck_OK
- internal/lineage/openlineage/translate_test.go::TestTranslateRun_OK
- internal/metadata/handler_test.go::TestHandler_PatchAsset_OK

**症状：** `runtime error: invalid memory address or nil pointer dereference` 位于 `internal/storage/ent/run_create.go::(*RunCreate).defaults` 内 — ent 生成的默认函数取消引用 `runtime.RunFunc`，当 codegen 未重新运行时该值为 nil。

**为何推迟：** 预先存在的失败已在 05-01 SUMMARY 中记录（"Ent codegen 预先存在的损坏状态：git stash 显示 codegen 在我们的变更之前失败；未修复"）。通过暂存 plan 05-03 变更并重新运行确认 — 相同 panic。根据范围边界规则（未由此计划引入），超出范围。

**修复：** 重新运行 `go generate ./internal/storage/ent/`（重新生成运行时默认值）。应在专门的工具修复提交中完成；从功能计划中触及 ent 生成器会淡化差异。

**发现于：** plan 05-03 广泛测试扫描。