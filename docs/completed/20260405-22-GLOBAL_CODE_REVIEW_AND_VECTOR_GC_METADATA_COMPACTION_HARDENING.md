# 全局代码审核与 Vector GC 元数据压缩加固执行计划

## 1. 任务目标

本阶段继续执行全局代码审核，重点检查上一轮补齐的 `vector_gc_jobs` 重试闭环在“任务成功完成后”的元数据生命周期是否仍存在持续累积风险，确认是否会把补偿队列本身重新演化成新的无界台账，并以最优方式完成修复。

具体目标如下：

1. 复核 `vector_gc_jobs` 成功完成路径，确认是否仍通过“标记完成但保留行”的方式结束任务。
2. 若确认存在无界元数据增长风险，则收敛为“成功即删除”的最小实现，避免把重试队列本身变成新的长期存储负担。
3. 在不破坏当前重试语义、失败补偿和幂等性的前提下，完成 SQLite / PostgreSQL 两侧实现与测试补强。
4. 完成回归验证、自检、执行总结、计划归档、提交与推送。

## 2. 审核范围

### 2.1 本轮重点覆盖

1. `internal/adapters/outbound/vldb_sqlite/vector_gc_jobs.go`
2. `internal/adapters/outbound/vldb_postgres/vector_gc_jobs.go`
3. `internal/adapters/outbound/vldb_sqlite/retention_store_test.go`
4. `internal/adapters/outbound/vldb_postgres/retention_store_test.go`
5. 与 retention 向量 GC 成功完成路径相关的说明和测试

### 2.2 本轮不主动扩展

1. 不改动 `vector_gc_jobs` 的失败重试语义。
2. 不改动 retention 调度周期、claim 租约和重试延迟参数。
3. 不扩展到与当前问题无关的回收链路。

## 3. 执行策略

1. 先确认现状：成功完成的向量 GC 任务是否只是更新 `completed_*` 字段而继续保留在表里。
2. 若确认存在持续积累风险，则优先采用“完成即删行”的方式收口，而不是再引入额外 purge 阶段。
3. 修复必须同时满足：
   - 失败任务仍可继续重试；
   - 成功任务不再占用长期表空间；
   - 不增加新的后台噪声或扫描步骤；
   - 对当前 SQLite / PostgreSQL 适配器保持实现简单且幂等。

## 4. 详细执行步骤

1. 确认仓库基线并建立本阶段计划文件。
2. 审核向量 GC 成功完成路径，定位真实风险。
3. 调整 SQLite / PostgreSQL 完成逻辑为成功即删除，并补齐必要测试。
4. 运行定向测试、最少必测集、全量测试与静态检查。
5. 对照计划逐项自检，补写执行变更总结。
6. 将计划迁移至 `docs/completed/`，完成 `git commit` 与 `git push`。

## 5. 验收标准

1. 必须明确说明本轮定位到的真实问题与修复原因。
2. 修复后，成功完成的 `vector_gc_jobs` 不得继续在主表里累积。
3. 修复不得破坏失败重试能力或引入新的运行时干扰。
4. 至少完成以下验证：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`

## 6. 风险与注意事项

1. 本轮仍以功能稳定、效率高、性能强、运行时无干扰为最高优先级。
2. 保留现有 schema，不新增计划外字段或新表。
3. 完成即删行后，成功任务不再提供表内审计历史；这符合当前仓库“重试队列只承担补偿功能、不承担长期台账功能”的方向。

## 执行变更总结

### 1. 核心修复与调整概述

1. 本轮全局审核确认了一个真实但比较隐蔽的治理问题：上一轮刚接入的 `vector_gc_jobs` 重试闭环在任务成功后仍采用“标记完成但保留行”的模式结束任务。
2. 这会把原本只应该承担短期补偿职责的重试队列再次演化成长期台账。随着 retention 长期运行，`vector_gc_jobs` 本身会不断累积成功任务行，形成新的元数据增长点，和此前对 `recycle_batches` 做过的“不要保留永续台账”治理方向不一致。
3. 本轮修复把成功完成路径统一收敛成“成功即删行”，保留失败任务的重试能力，但不再为成功任务长期占用主表空间，从而让 `vector_gc_jobs` 重新回到“瞬时补偿队列”的角色。

### 2. 📂文件变更清单

1. 新增：`docs/plan/20260405-22-GLOBAL_CODE_REVIEW_AND_VECTOR_GC_METADATA_COMPACTION_HARDENING.md`
2. 修改：`internal/adapters/outbound/vldb_sqlite/vector_gc_jobs.go`
3. 修改：`internal/adapters/outbound/vldb_sqlite/retention_store_test.go`
4. 修改：`internal/adapters/outbound/vldb_postgres/vector_gc_jobs.go`
5. 修改：`internal/adapters/outbound/vldb_postgres/retention_store_test.go`

### 3. 💻关键代码调整详情

1. SQLite 侧把 `CompleteVectorGCJobs` 从“更新 `completed_timestamp`”改成“直接删除对应任务行”，并保留现有 `RetryVectorGCJobs` 的失败重试路径不变。
2. PostgreSQL 侧同步把 `CompleteVectorGCJobs` 改成删除任务行，并新增 `buildPostgresDeleteCompletedVectorGCJobsSQL` 辅助函数，方便以后继续通过无数据库测试约束 SQL 语义。
3. SQLite 测试从断言“完成路径会写 `completed_timestamp`”改成断言“完成路径会执行 `DELETE FROM vmm_vector_gc_jobs`”。
4. PostgreSQL 测试新增 `TestBuildPostgresDeleteCompletedVectorGCJobsSQLDeletesRows`，确保成功完成路径不会再退回为 `UPDATE completed_at` 风格的永续台账实现。

### 4. ⚠️遗留问题与注意事项

1. 本轮只压缩成功任务元数据；失败任务依然会继续保留在队列表中等待后续重试，这是当前闭环所需的必要状态。
2. 由于 schema 已经包含 `completed_at / completed_timestamp` 字段，本轮没有额外做 schema 清理；这些字段目前属于兼容残留，不影响运行时正确性。
3. 已完成验证：
   - `go test ./internal/adapters/outbound/vldb_sqlite -run "Test(SQLiteVectorGCJobsClaimRetryAndComplete|EnqueueVectorGCJobsPersistsRetryRows)"`
   - `go test ./internal/adapters/outbound/vldb_postgres -run "Test(BuildPostgresDeleteCompletedVectorGCJobsSQLDeletesRows|BuildPostgresRecycleBatchDeleteSQLDeletesMetadata)"`
   - `go test ./internal/app/usecase -run "TestRetentionUseCase"`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`
