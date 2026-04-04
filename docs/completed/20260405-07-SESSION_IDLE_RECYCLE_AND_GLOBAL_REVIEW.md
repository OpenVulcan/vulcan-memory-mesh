# Session Idle 回收、Turn Trash 与全局审核执行计划

## 1. 任务目标

在上一阶段已完成“终态记忆冷回收”的基础上，本阶段继续补齐总计划中仍然缺失的在线治理能力，并在完成后执行一次全局代码审核与最优解修复。具体目标如下：

1. 让 `retention.session_idle_recycle_after` 真正驱动长期空闲 `session` 的自动回收。
2. 让 `retention.turn_keep_extra_turns` 真正参与旧 `turn` 的热窗口判定与回收站迁移。
3. 在不破坏现有 `source_turn_id -> GetTurnDetails` 契约的前提下，实现旧 `turn` 的机会式归档。
4. 在功能补齐后执行全局代码审核，修复阻断风险或明显设计缺口。
5. 完成测试、文档、提交与推送，形成完整闭环。

## 2. 范围边界

### 2.1 本阶段必须完成

1. 新增 `vmm_turn_records_trash`，支持 idle-session 旧 `turn` 迁入回收站。
2. 新增 idle-session 回收逻辑，至少覆盖：
   - 空闲 session 检测；
   - session 级未提级且已失效记忆迁出热表；
   - 超出热窗口且无主表引用的旧 `turn` 迁入回收站。
3. 新增对应的回收站 purge 逻辑。
4. 完成一次全局代码审核，并修复本轮发现的高优先级问题。
5. 提交并推送到远端。

### 2.2 本阶段明确不做

1. 不做任何产品级恢复接口。
2. 不做 `turn` 详情从回收站回读；本阶段继续坚持“仍被主表引用的 turn 不归档”。
3. 不强制升级 PostgreSQL 共享 schema 版本号，避免旧库因缺少自动迁移而无法启动。

## 3. 技术方案

### 3.1 Idle Session 回收规则

1. 候选 `session` 需满足：
   - `session.updated_at <= now - session_idle_recycle_after`
   - 不存在 pending `turn`
2. session 级记忆仅回收：
   - `scope_level = session`
   - `origin_session_id = session.id`
   - `expires_at <= now`
   - `max(last_recalled_at, last_adopted_at, last_reinforced_at, created_at) <= now - session_idle_recycle_after`
3. `turn` 仅回收：
   - 不在最近热窗口 `max(pre_check.history_turns, post_action.session_analysis_history_turns) + turn_keep_extra_turns`
   - `extracted_status != pending`
   - 不存在任何仍留在主表中的 `memory_nodes.source_turn_id = turn_id`
   - 不存在任何仍留在主表中的 `profile_nodes.turn_id = turn_id`

### 3.2 批次语义

1. idle-session 回收使用独立 `recycle_type = session_idle_recycle`
2. 单个 session 生成单个回收批次，便于审计和后续 purge。
3. 一个批次内可以同时包含：
   - `memory_nodes_trash`
   - `memory_context_edges_trash`
   - `turn_records_trash`

### 3.3 Purge 语义

1. `trash_retention` 继续仅表示“进入回收站后的保留时长”。
2. purge 需要覆盖：
   - cold-memory 批次
   - session-idle 批次
3. session-idle 批次 purge 时必须同时删除：
   - `turn_records_trash`
   - `memory_nodes_trash`
   - `memory_context_edges_trash`

### 3.4 审核与修复原则

1. 优先修复会影响功能正确性、稳定性、性能或运行时干扰的问题。
2. 不做“为了开发速度而保留明显债务”的妥协。
3. 若发现计划外的大型架构风险，先在执行总结中记录，并只落地低风险最优解。

## 4. 执行步骤

1. 梳理 turn 与 session 当前读取链路，确认归档前提。
2. 扩展 retention 领域模型与端口，补齐 idle-session 回收与 turn trash 结果结构。
3. 落 PostgreSQL / SQLite 的 `turn_records_trash` 表与相应索引。
4. 实现 idle-session 回收事务与超期 purge。
5. 跑全量测试并做一次全局代码审核。
6. 修复审核发现的问题。
7. 更新文档、补写执行变更总结、提交并推送。

## 5. 验收标准

1. `retention.session_idle_recycle_after` 不再是死配置。
2. `retention.turn_keep_extra_turns` 真正参与 turn 热窗口计算。
3. idle-session 回收不会移动任何仍被热主表记忆或画像引用的 turn。
4. session 级已失效记忆会随 idle-session 回收进入回收站并退出热主表。
5. `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
6. `go test ./internal/adapters/outbound/vldb_sqlite ./internal/adapters/outbound/vldb_postgres`
7. `go test ./...`
8. 完成全局代码审核、修复、提交与推送。

## 6. 风险与注意事项

1. PostgreSQL 当前不支持自动 schema 版本前滚，本阶段只能做向后兼容的增量补表。
2. 若某个 idle session 因旧 turn 仍有主表引用而无可回收内容，应保持 no-op，而不是强行归档。
3. 本阶段仍不提供任何恢复能力；回收站仅作为数据库层防灾缓冲。

## 执行变更总结

### 1. 核心修复与调整概述

1. 补齐 `RetentionStore` 与 `RetentionUseCase` 的第二阶段治理能力，让 `session_idle_recycle_after` 和 `turn_keep_extra_turns` 真正参与运行时决策。
2. 新增 `session_idle_recycle` 批次类型与 `turn_records_trash`，实现长期空闲 session 的机会式压缩：回收过期且长期未强化的 session 级记忆，并把无主表引用的旧 turn 迁入回收站。
3. 把回收站 purge 从“只清 memory trash”扩展为统一 purge `memory_nodes_trash / memory_context_edges_trash / turn_records_trash`。
4. 完成一轮全局代码审核，并把 PostgreSQL idle-session turn 查询中的非必要 `FOR UPDATE` 锁移除，降低复杂查询锁冲突与运行时干扰风险。

### 2. 📂 文件变更清单

修改：

1. `internal/logic/domain/retention.go`
2. `internal/app/ports/interfaces.go`
3. `internal/app/usecase/retention.go`
4. `internal/app/usecase/retention_test.go`
5. `internal/app/app.go`
6. `internal/adapters/outbound/vldb_postgres/helpers.go`
7. `internal/adapters/outbound/vldb_postgres/schema.go`
8. `internal/adapters/outbound/vldb_postgres/turn_store.go`
9. `internal/adapters/outbound/vldb_postgres/retention_store.go`
10. `internal/adapters/outbound/vldb_postgres/retention_store_test.go`
11. `internal/adapters/outbound/vldb_sqlite/store.go`
12. `internal/adapters/outbound/vldb_sqlite/schema_migrations.go`
13. `internal/adapters/outbound/vldb_sqlite/retention_store.go`
14. `internal/adapters/outbound/vldb_sqlite/retention_store_test.go`
15. `README.md`
16. `docs/plan/20260405-07-SESSION_IDLE_RECYCLE_AND_GLOBAL_REVIEW.md`

新增：

1. 无

删除：

1. 无

### 3. 💻 关键代码调整详情

1. 领域模型新增：
   - `RecycleTypeSessionIdle`
   - `RecycleReasonIdleSessionCompact`
   - `SessionIdleRecycleQuery`
   - `SessionIdleRecycleResult`
   - `RetentionTrashPurgeResult`
2. 应用层新增 idle-session 维护接线：
   - `RetentionConfig` 增加 `SessionIdleRecycleAfter / TurnHotWindowSize`
   - worker 在一次维护中按顺序执行：
     - 终态记忆冷回收
     - idle-session 回收
     - 回收站统一 purge
3. PostgreSQL 侧新增：
   - `vmm_turn_records_trash`
   - idle-session 单 session 单事务回收
   - `FOR UPDATE SKIP LOCKED` 的 session 级候选锁定
   - turn 回收时继续坚持“仍被主表记忆/画像引用则不归档”
4. SQLite 侧新增：
   - schema `16 -> 17` 迁移
   - `vmm_turn_records_trash`
   - idle-session 回收脚本
   - 支持在 turn 引用判定里忽略“同批即将删除的 session 级记忆”，避免多等一个扫描周期
5. 测试补齐：
   - usecase 级 worker 参数透传与两段回收验证
   - SQLite idle-session recycle / unified purge SQL 断言
   - PostgreSQL idle-session turn 引用谓词辅助逻辑断言
6. 文档同步：
   - README 的 retention 章节已更新为与真实行为一致

### 4. ⚠️ 遗留问题与注意事项

1. 仍不提供任何产品级恢复能力；回收站只作为数据库层防灾缓冲。
2. PostgreSQL 继续保持向后兼容补表策略，没有提升共享 schema 版本号。
3. SQLite idle-session 回收依赖现有适配器写锁与脚本事务能力，语义正确，但不是细粒度并发模型。
4. 全局审核完成后，当前未发现新的阻断级问题。
5. 验证已完成：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./internal/adapters/outbound/vldb_sqlite ./internal/adapters/outbound/vldb_postgres`
   - `go test ./internal/app/usecase ./internal/adapters/outbound/vldb_postgres ./internal/adapters/outbound/vldb_sqlite`
   - `go test ./...`
   - `go vet ./...`
