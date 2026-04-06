# 全局代码审核与 Retention 向量 GC 重试加固执行计划

## 1. 任务目标

本阶段继续执行全局代码审核，重点检查冷数据回收链路在“关系回收成功、向量删除失败”这一跨存储边界上的一致性闭环，确认是否存在会导致长期孤儿向量累积、最终永久失去清理坐标的真实风险，并以最优方式完成修复。

具体目标如下：

1. 复核 retention 维护器在冷记忆回收、idle-session 回收后的向量清理行为，确认失败时是否仅记录日志而没有持久化重试闭环。
2. 充分复用仓库中已经存在的 `vmm_vector_gc_jobs` 表结构，避免再引入新的 schema 设计或额外后台组件。
3. 将失败向量删除收口为低干扰、可重试、可渐进执行的后台治理流程，避免 SQLite 分离向量库存储留下永久孤儿向量。
4. 补齐用例层与存储层测试，完成回归验证、自检、执行总结、计划归档、提交与推送。

## 2. 审核范围

### 2.1 本轮重点覆盖

1. `internal/app/usecase/retention.go`
2. `internal/app/usecase/retention_test.go`
3. `internal/app/ports/interfaces.go`
4. `internal/logic/domain/retention.go`
5. `internal/adapters/outbound/vldb_sqlite/retention_store.go`
6. `internal/adapters/outbound/vldb_sqlite/retention_store_test.go`
7. `internal/adapters/outbound/vldb_postgres/retention_store.go`
8. `internal/adapters/outbound/vldb_postgres/retention_store_test.go`

### 2.2 本轮不主动扩展

1. 不修改 retention 总体调度周期与现有回收策略。
2. 不引入新的外部依赖、额外进程或独立任务系统。
3. 不改动与本轮风险无关的 post-action / memory-query 主链。

## 3. 执行策略

1. 先确认现状：回收成功后若 `vector.DeleteByIDs` 失败，是否只记日志、不入重试台账，且后续 purge 会让清理坐标永久丢失。
2. 修复时优先复用已存在但尚未接线的 `vmm_vector_gc_jobs`，避免扩大 schema 变更面。
3. 失败向量删除必须满足：
   - 关系回收成功后才允许入队；
   - 重试必须有界、渐进、低噪声；
   - 对 PostgreSQL 组合库存储保持零干扰；
   - 对 SQLite 分离向量库提供真实持久化补偿。

## 4. 详细执行步骤

1. 确认仓库基线并建立本阶段计划文件。
2. 审核 retention 向量清理失败路径与现有 `vector_gc_jobs` 表结构，定位真实风险。
3. 在 domain / port / usecase / SQLite / PostgreSQL 适配器中补齐向量 GC 入队、领取、完成与重试能力。
4. 调整 retention 维护器：先尝试即时删除，失败则入队；每轮维护再消费一批待重试任务。
5. 补充用例层和存储层回归测试。
6. 运行定向测试、最少必测集、全量测试与静态检查。
7. 对照计划逐项自检，补写执行变更总结。
8. 将计划迁移至 `docs/completed/`，完成 `git commit` 与 `git push`。

## 5. 验收标准

1. 必须明确说明本轮定位到的真实问题与修复原因。
2. 修复后，retention 回收成功但向量删除失败时，必须存在持久化重试闭环，不能只剩日志。
3. 修复不得引入新的热路径阻塞、无界重试、重复删除风暴或明显运行时干扰。
4. 至少完成以下验证：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`

## 6. 风险与注意事项

1. 本轮仍以功能稳定、效率高、性能强、运行时无干扰为最高优先级。
2. 只复用现有 `vector_gc_jobs` 表，不新增计划外 schema。
3. 任何失败重试逻辑都必须保持幂等，避免同一 `vector_id` 被无限重复入队或重复删除。

## 执行变更总结

### 1. 核心修复与调整概述

1. 本轮全局审核确认了一个真实的一致性漏洞：retention 在冷记忆回收或 idle-session 回收成功后，会立即调用 `vector.DeleteByIDs` 清理旁路向量；但一旦删除失败，当前实现只记录日志，不会把失败任务持久化。
2. 这在 SQLite 分离向量库存储下是有后果的。因为关系回收已经提交、热表行已删除，而后续 `purge` 又会继续清掉回收站和批次元数据；如果失败向量没有进入持久化重试队列，系统最终会永久失去这些 `vector_id` 的清理坐标，形成长期孤儿向量。
3. 本轮修复直接复用仓库里已经存在但未接线的 `vmm_vector_gc_jobs` 表，把 retention 的失败向量删除收口成“即时尝试删除，失败则入队；后续维护轮次再领取一批任务执行补偿；成功后标记完成，失败后释放租约并重试”的低干扰闭环。
4. 同时补充了 SQLite 存储层和 retention 用例层测试，验证入队、领取、重试、完成、以及即时删除失败后的补偿路径都已连通。

### 2. 📂文件变更清单

1. 新增：`docs/plan/20260405-21-GLOBAL_CODE_REVIEW_AND_RETENTION_VECTOR_GC_RETRY_HARDENING.md`
2. 新增：`internal/adapters/outbound/vldb_sqlite/vector_gc_jobs.go`
3. 新增：`internal/adapters/outbound/vldb_postgres/vector_gc_jobs.go`
4. 修改：`internal/logic/domain/retention.go`
5. 修改：`internal/app/ports/interfaces.go`
6. 修改：`internal/app/usecase/retention.go`
7. 修改：`internal/app/usecase/retention_test.go`
8. 修改：`internal/adapters/outbound/vldb_sqlite/retention_store_test.go`

### 3. 💻关键代码调整详情

1. 在 `internal/logic/domain/retention.go` 新增 `VectorGCJobTypeRetentionRecycle`、`VectorGCJobEnqueueQuery` 和 `VectorGCJobRecord`，把重试任务抽象为共享领域模型。
2. 扩展 `RetentionStore` 端口，新增向量 GC 的四个能力：入队、领取、完成、重试；这样用例层可以在不感知底层 SQL 方言的前提下驱动补偿逻辑。
3. 在 `internal/app/usecase/retention.go` 中新增持久化重试闭环：
   - 即时删除成功则直接结束；
   - 即时删除失败则把 `vector_id` 批量写入 `vector_gc_jobs`；
   - 每轮维护会额外领取一批到期任务；
   - 删除成功则标记完成；
   - 删除失败则递增尝试次数、设置下一次运行时间并释放领取租约。
4. 为了降低运行时噪声，本轮没有在“失败后已成功入队”场景再重复输出第二条同因错误日志，只保留真正的删除失败日志和必要的入队失败日志。
5. 在 SQLite 侧实现了实际的 `vector_gc_jobs` 读写脚本，并新增测试覆盖：
   - 入队脚本会写入持久化重试行；
   - 领取逻辑会推进 `next_run` 形成租约；
   - 重试失败会递增 `attempt_count` 并释放租约；
   - 成功后会标记 `completed_timestamp`。
6. PostgreSQL 侧补齐了同语义的实现，保持组合库存储也能满足统一接口和后续扩展要求；由于 PostgreSQL 当前 `DeleteByIDs` 是 no-op，这条能力在现运行时基本保持零干扰。

### 4. ⚠️遗留问题与注意事项

1. 这次修复优先解决的是 retention 回收后的向量孤儿风险；`post-action` 或 direct-write 其他旁路删除失败路径仍然保持各自原有补偿方式，并未在本轮统一迁入同一个重试队列。
2. `idle-session` 回收可能聚合多个批次，因此向量 GC 任务只会在“结果确实对应单一批次”时保留 `batch_id` 锚点；多批次聚合会退回 `0`，避免错误归属到某个具体批次。
3. 当前重试节奏采用固定、平稳的延迟窗口而不是指数退避，目的是优先控制实现复杂度与运行时噪声；如果未来出现更长时间的向量库故障，再评估是否需要基于 `attempt_count` 做更保守的退避策略。
4. 已完成验证：
   - `go test ./internal/app/usecase -run "TestRetentionUseCase"`
   - `go test ./internal/adapters/outbound/vldb_sqlite -run "Test(EnqueueVectorGCJobsPersistsRetryRows|SQLiteVectorGCJobsClaimRetryAndComplete|RecycleColdMemoriesCopiesRowsIntoTrashAndDeletesFTS|RecycleIdleSessionsMovesStaleSessionRowsIntoTrash|PurgeExpiredTrashDeletesAllTrashTablesAndBatchMetadata)"`
   - `go test ./internal/adapters/outbound/vldb_postgres -run "Test(BuildPostgresRecycleBatchDeleteSQLDeletesMetadata|CollectPostgresIdleSessionRecyclePass|BuildPostgresIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows|AppendProtectedSharedMemoryRecycleFilterAppendsPredicate|SharedRecycleProjectIDCollapsesMixedProjectBatches)"`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`
