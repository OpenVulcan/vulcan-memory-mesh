# 全局代码审核与查询稳定性加固执行计划

## 1. 任务目标

本阶段基于第 14 阶段已经完成的记忆搜索热路径竞态收口，继续执行新一轮全局代码审核，重点检查查询链路、回收链路、主动写入链路以及近期多轮修复叠加后的边界一致性，确认是否仍存在影响稳定性、性能或运行时无干扰目标的真实风险，并以最优方案完成修复。

具体目标如下：

1. 复核记忆查询、主动写入、回收维护之间的交界面，确认是否还存在冷热状态不一致、结果重复、错误降级或后台扫描饥饿等问题。
2. 仅修复能够通过代码逻辑、测试或运行时语义证明的真实问题，不做猜测式大改。
3. 在不牺牲稳定性、效率和维护性的前提下，完成必要代码修复与测试补强。
4. 完成回归验证、自检、计划归档、提交与推送，保持工程闭环。

## 2. 审核范围

### 2.1 本轮重点覆盖

1. `internal/app/usecase/memory_query.go`
2. `internal/app/usecase/retention.go`
3. `internal/adapters/outbound/vldb_sqlite/retention_store.go`
4. `internal/adapters/outbound/vldb_postgres/retention_store.go`
5. 最近几轮新增的查询热路径收口逻辑与回收维护逻辑

### 2.2 本轮不主动扩展

1. 不引入计划外的大型架构重构。
2. 不修改对外接口定义，除非确认存在阻断级错误且存在低风险最优解。
3. 不做与当前风险无关的风格性清理。

## 3. 执行策略

1. 基于当前主分支最新状态执行定向深审，优先覆盖最近多轮修复最容易出现语义漂移的查询热路径与回收维护交界面。
2. 若发现真实问题，优先在 usecase 或单一适配器边界内收口，避免扩大改动面。
3. 修复方案必须同时满足：
   - 不破坏既有成功路径契约；
   - 不增加明显额外 provider 往返、锁竞争或后台噪声；
   - 能通过测试长期约束；
   - 便于后续维护者继续理解和演进。

## 4. 详细执行步骤

1. 确认仓库基线并建立本阶段计划文件。
2. 深审查询、主动写入、回收维护等关键链路，定位真实风险问题。
3. 采用最优方案实施修复，并补齐必要测试。
4. 运行最少必测集、全量测试与必要静态检查。
5. 对照计划逐项自检，补写执行变更总结。
6. 将计划迁移至 `docs/completed/`，完成 `git commit` 与 `git push`。

## 5. 验收标准

1. 必须明确说明本轮定位到的真实问题与修复原因。
2. 修复后不得引入新的查询结果泄露、重复写入、后台饥饿或冷热状态不一致问题。
3. 修复不得引入明显额外开销或运行时干扰。
4. 至少完成以下验证：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`

## 6. 风险与注意事项

1. 本轮仍以功能稳定、效率高、性能强、运行时无干扰为最高优先级。
2. 若发现某项原设计应后置或不应继续推进，必须在总结中明确说明原因。
3. 修复必须继续遵守仓库双语注释、测试约束、计划归档和中文提交规范。

## 执行变更总结

### 1. 核心修复与调整概述

1. 本轮全局审核确认了 retention purge 路径存在一个真实的生命周期漏洞：当前实现虽然会删除 `memory_nodes_trash`、`memory_context_edges_trash`、`turn_records_trash`，但只会把 `vmm_recycle_batches` 标记为 `purged`，不会真正删除批次元数据。
2. 在当前产品明确“不提供产品级恢复接口”的前提下，这种做法会让 `recycle_batches` 永久累积，既和“超过保留期后彻底清理”的语义不一致，也会让后台维护台账持续膨胀。
3. 本轮把 PostgreSQL 与 SQLite 的 purge 末尾统一改成“删掉 trash 行后，再删除对应 recycle batch 元数据”，让 purge 真正成为最终删除，同时不改变对外接口和计数返回结构。

### 2. 📂文件变更清单

1. 修改：`internal/adapters/outbound/vldb_postgres/retention_store.go`
2. 修改：`internal/adapters/outbound/vldb_sqlite/retention_store.go`
3. 修改：`internal/adapters/outbound/vldb_postgres/retention_store_test.go`
4. 修改：`internal/adapters/outbound/vldb_sqlite/retention_store_test.go`
5. 修改：`README.md`

### 3. 💻关键代码调整详情

1. PostgreSQL `PurgeExpiredTrash` 不再更新 `purged_at`，而是在完成 trash 删除后直接删除 `vmm_recycle_batches` 对应批次元数据。
2. SQLite `PurgeExpiredTrash` 同步从“更新批次为 purged”改成“直接删除批次行”，确保两套实现保持一致语义。
3. 新增 `buildPostgresRecycleBatchDeleteSQL` 辅助函数与对应单元测试，用于稳定约束 PostgreSQL purge 尾部必须执行批次元数据删除，而不是退回到标记式清理。
4. 更新 SQLite purge 语句测试与 README 文档口径，让“超过 `trash_retention` 后彻底清理”覆盖 trash 表和对应的批次元数据。

### 4. ⚠️遗留问题与注意事项

1. 本轮没有删除 `purged_at` 列本身；它目前会继续保留在 schema 中以兼容已有表结构和迁移成本，但运行时 purge 已不再依赖它保留历史元数据。
2. 当前改动仍保持 `RetentionTrashPurgeResult.BatchIDs` 返回已被彻底删除的批次 id，用于日志与运维观测；这是运行时瞬时结果，不代表数据库中还保留这些批次行。
3. 已完成验证：
   - `go test ./internal/adapters/outbound/vldb_sqlite -run "Test(PurgeExpiredTrashDeletesAllTrashTablesAndBatchMetadata|RecycleIdleSessions|BuildSQLiteIdleSessionCandidateAvailabilityClause)"`
   - `go test ./internal/adapters/outbound/vldb_postgres -run "Test(BuildPostgresRecycleBatchDeleteSQLDeletesMetadata|CollectPostgresIdleSessionRecyclePass|PostgresIdleSessionInspectionBudget|BuildPostgresIdleSessionCandidateAvailabilityClause|BuildPostgresIdleSessionTurnReferenceClause|AppendProtectedSharedMemoryRecycleFilter|SharedRecycleProjectID)"`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`
