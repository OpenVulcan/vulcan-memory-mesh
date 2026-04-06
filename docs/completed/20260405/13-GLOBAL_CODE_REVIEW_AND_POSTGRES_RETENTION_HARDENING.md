# 全局代码审核与 PostgreSQL 回收链路加固执行计划

## 1. 任务目标

本阶段在上一轮完成回收工作器错误路径向量清理收口后，继续执行新一轮全局代码审核，重点复核 PostgreSQL `idle-session recycle` 相关辅助逻辑与批次聚合路径，确认是否仍存在残余饥饿、边界条件或运行时干扰风险，并对确认存在的问题实施最优修复。

具体目标如下：

1. 复核 PostgreSQL `idle-session recycle` 的批次聚合、跳过 no-op session 与候选推进逻辑。
2. 检查最近几轮回收链路修复后，是否还存在“理论支持跳过 no-op、但在小批量限制下仍可能提前停机”的残余窗口。
3. 对确认存在的真实风险实施低干扰、高稳定性的代码修复，并补齐回归测试。
4. 完成全量验证、自检、计划归档、提交与推送，保持工程闭环。

## 2. 审核范围

### 2.1 本轮重点覆盖

1. `internal/adapters/outbound/vldb_postgres/retention_store.go`
2. PostgreSQL `idle-session recycle` 的 pass 聚合、检查预算与 no-op 跳过逻辑
3. PostgreSQL retention 相关单元测试与边界覆盖

### 2.2 本轮不主动扩展

1. 不新增计划外的大型架构重构。
2. 不修改对外接口、配置结构或产品能力。
3. 不扩大到与当前风险无关的风格性改动。

## 3. 执行策略

1. 先定位 PostgreSQL `idle-session recycle` 批次聚合逻辑在 `limit` 很小时是否仍可能发生残余饥饿。
2. 修复时优先选择“有界额外检查预算”而不是无界扫描，兼顾吞吐与后台低干扰。
3. 用 helper 级和流程级测试同时锁住行为，避免后续回归。

## 4. 详细执行步骤

1. 确认仓库基线并建立本阶段计划文件。
2. 深审 PostgreSQL `idle-session recycle` 聚合逻辑，定位真实残余风险。
3. 实施代码修复，并补齐定向回归测试。
4. 运行最少必测集、全量测试与 `go vet`。
5. 对照计划逐项自检，补写执行变更总结。
6. 将计划迁移至 `docs/completed/`，完成 `git commit` 与 `git push`。

## 5. 验收标准

1. 必须明确说明本轮定位到的真实问题与修复原因。
2. 修复后 `limit=1` 且首个候选为 no-op 时，PostgreSQL 聚合逻辑仍能在有界预算内继续推进到后续可回收批次。
3. 修复不得引入无界扫描或显著后台放大量。
4. 至少完成以下验证：
   - `go test ./internal/adapters/outbound/vldb_postgres -run "Test(CollectPostgresIdleSessionRecyclePass|PostgresIdleSessionInspectionBudget|BuildPostgresIdleSessionCandidateAvailabilityClause|BuildPostgresIdleSessionTurnReferenceClause|AppendProtectedSharedMemoryRecycleFilter|SharedRecycleProjectID)"`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`

## 6. 风险与注意事项

1. 本轮仍以功能稳定、效率高、性能强、运行时无干扰为最高优先级。
2. 修复必须保持 PostgreSQL 与 SQLite 两侧治理语义收敛，而不是让某一侧继续残留旧风险。
3. 必须遵守仓库双语注释、测试约束、计划归档和中文提交规范。

## 执行变更总结

### 1. 核心修复与调整概述

1. 发现并修复了 PostgreSQL `collectPostgresIdleSessionRecyclePass` 的残余饥饿窗口：旧实现虽然已支持跳过 no-op session，但检查预算仍然直接等于 `limit`。
2. 这意味着当 `limit=1` 且第一个候选在并发窗口内变成 no-op 时，整轮回收会直接结束，后续真实可回收 session 仍可能被饿死。
3. 本轮将聚合逻辑调整为“批次数限制 + 有界额外检查预算”双约束，在不引入无界扫描的前提下，为少量 race-window no-op 候选保留推进空间。

### 2. 📂文件变更清单

1. 修改：`internal/adapters/outbound/vldb_postgres/retention_store.go`
2. 修改：`internal/adapters/outbound/vldb_postgres/retention_store_test.go`

### 3. 💻关键代码调整详情

1. 为 `collectPostgresIdleSessionRecyclePass` 增加 `postgresIdleSessionInspectionBudget(limit)`，当前采用“`limit + 4`”的有界额外检查预算。
2. 保持结果批次数仍以 `limit` 为上限，只允许在检查侧为少数 no-op 候选多预留少量跳过空间，避免把维护轮放大成无界扫描。
3. 新增 `TestCollectPostgresIdleSessionRecyclePassAllowsBoundedExtraInspection`，验证 `limit=1` 且首个候选为 no-op 时，仍能继续命中下一批真实可回收 session。
4. 新增 `TestPostgresIdleSessionInspectionBudgetAddsBoundedHeadroom`，固定预算策略，防止后续回归为“预算重新等于 limit”。
5. 同步收紧已有 `TestCollectPostgresIdleSessionRecyclePassSkipsNoOpSessions` 的预期，使其与“会继续查到无候选或收满批次”这一真实函数语义对齐。

### 4. ⚠️遗留问题与注意事项

1. 本轮未修改 PostgreSQL `recycleOnePostgresIdleSession` 的事务语义，也未改变外部配置项，仅修正 pass 级推进策略。
2. 额外检查预算当前固定为 `limit + 4`，这是基于“预过滤已存在，额外 no-op 应属少量并发窗口噪声”的保守设计；本轮不继续扩大为更高预算或无界重试。
3. 已完成验证：
   - `go test ./internal/adapters/outbound/vldb_postgres -run "Test(CollectPostgresIdleSessionRecyclePass|PostgresIdleSessionInspectionBudget|BuildPostgresIdleSessionCandidateAvailabilityClause|BuildPostgresIdleSessionTurnReferenceClause|AppendProtectedSharedMemoryRecycleFilter|SharedRecycleProjectID)"`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`
