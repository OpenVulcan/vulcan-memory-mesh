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
