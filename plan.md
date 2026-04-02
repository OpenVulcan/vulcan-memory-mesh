# VMM Memory Unification Plan

## Goal

This document captures the full implementation plan for the memory-unification work so progress does not depend on transient chat context.

本文用于固化记忆统一化改造的完整实施计划，避免进度依赖临时对话上下文。

The target state is:

- keep `vmm_memory_nodes` as the only durable memory fact table
- absorb and finally remove `vmm_memory_entries`
- support direct AI-written memory over gRPC
- support unified `TYPE + ID` memory references for query and detail lookup
- prevent duplicated memory extraction when tools already wrote memory before post-action LLM runs
- restore pre-check memory adoption on top of the new unified model

目标状态是：

- 保留 `vmm_memory_nodes` 作为唯一长期记忆事实表
- 吸收并最终移除 `vmm_memory_entries`
- 支持 AI 通过 gRPC 直接写入记忆
- 支持基于 `TYPE + ID` 的统一查询与详情引用
- 在 tool 已主动写入记忆后，避免 post-action LLM 重复提炼
- 在新统一模型上恢复 pre-check 的二层采纳流程

## Constraints

- Keep the OSS-local architecture clean: `adapters -> app -> logic/domain`
- Do not reintroduce SaaS-only runtime paths
- Do not reintroduce Postgres-specific storage
- Preserve current build entrypoints and config layout rules
- Keep new code under the repo bilingual-comment convention

约束：

- 保持 OSS 本地版依赖方向清晰：`adapters -> app -> logic/domain`
- 不重新引入 SaaS 专用运行时路径
- 不重新引入 Postgres 存储
- 不破坏当前构建入口和配置目录规则
- 新增代码必须遵守仓库双语注释规范

## Current Status

### Done

1. `analyze_turn` has been rebuilt into a reference-aware single-turn contract.
2. The turn analyzer now supports:
   - `reference_turns`
   - `target_turn`
   - `active_memory_nodes`
   - `recent_grpc_memory_writes`
   - `superseded_memory_ids`
3. A prompt tag renderer has been added for optional rule blocks.
4. `PostAction` runtime mainline has been switched from queued batch extraction to immediate single-turn extraction:
   - append turn
   - analyze turn
   - review profile nodes
   - write vectors
   - apply turn analysis
5. Runtime wiring and tests have been updated for the new immediate-turn path.
6. README and post-action docs have been synchronized with the immediate-turn mainline.
7. `vmm_memory_nodes` has been upgraded into the unified durable memory table on the SQLite mainline.
8. Direct AI memory write RPC, soft idempotency, and unified `MemoryRef(TYPE + ID)` query/detail lookups have landed.
9. Session extraction-window fields plus `recent_grpc_memory_writes` exclusion reads have been connected end-to-end.
10. Workspace memory rebuild / delete / migration flows have been moved off `vmm_memory_entries`.
11. `PreCheck` has been reconnected as a live two-stage flow:
   - first-stage `extract_intent`
   - unified memory recall
   - second-stage `review_precheck_memory`
   - lifecycle write-back only for adopted memory ids
12. Focused test suites and `go test ./...` pass against the current SQLite-first mainline.

已完成：

1. `analyze_turn` 已重做为参考感知型单轮契约
2. 单轮分析器已支持：
   - `reference_turns`
   - `target_turn`
   - `active_memory_nodes`
   - `recent_grpc_memory_writes`
   - `superseded_memory_ids`
3. 已增加 prompt 的 TAG 可选渲染层
4. `PostAction` 运行时主链已从排队批处理切换为即时单轮提炼：
   - 追加 turn
   - 分析 turn
   - 评审画像节点
   - 写入向量
   - 回写 turn analysis
5. 运行时装配与测试已切到新的即时单轮主链
6. README 与 post-action 文档已同步到即时单轮主线

### Still Pending

1. DuckDB still keeps interface-complete unified-memory compatibility stubs for direct-write/query/adoption paths; SQLite is the production-complete mainline.

待完成：

1. 把 `vmm_memory_nodes` 升级成统一记忆表
2. 增加 AI 直接写记忆 RPC 与软幂等
3. 增加统一的 `MemoryRef(TYPE + ID)` 查询/详情协议
4. 增加主动写入排斥窗口字段与关系读取
5. 把迁移与向量重建链路从 `vmm_memory_entries` 切走
6. 移除 `vmm_memory_entries`
7. 接回 pre-check 的二层采纳与生命周期回写
8. 在完整统一后再次同步文档和测试

## Data Model Plan

### Unified Memory Table

`vmm_memory_nodes` will evolve into the only durable memory table.

`vmm_memory_nodes` 将演进为唯一的长期记忆表。

Target fields:

- `id`
- `team_id`
- `space_id`
- `project_id`
- `user_id`
- `origin_session_id`
- `source_turn_id`
- `vector_id`
- `source_kind`
- `scope_level`
- `category`
- `abstract`
- `details`
- `memory_status`
- `priority`
- `memory_level`
- `refresh_weight`
- `status_reason`
- `expires_timestamp`
- `last_recalled_timestamp`
- `last_adopted_timestamp`
- `recalled_count`
- `adopted_count`
- `cross_session_adopted_count`
- `dedupe_hash`
- `created_timestamp`
- `updated_timestamp`

Key semantics:

- `source_turn_id` is nullable and replaces the old mandatory `turn_id`
- `origin_session_id` anchors session-scoped memory and exclusion windows
- `source_kind` identifies:
  - `TURN_EXTRACT`
  - `GRPC_AI_WRITE`
  - `SYSTEM_SEED`
  - `LEGACY_MEMORY_ENTRY`
- `scope_level` identifies:
  - `SESSION`
  - `PROJECT`
  - `USER`
- `memory_status` identifies:
  - `ACTIVE`
  - `SUPERSEDED`
  - `EXPIRED`
  - `DELETED`

关键语义：

- `source_turn_id` 允许为空，替代旧的强绑定 `turn_id`
- `origin_session_id` 用于 session 级记忆和排斥窗口
- `source_kind` 用于区分来源：
  - `TURN_EXTRACT`
  - `GRPC_AI_WRITE`
  - `SYSTEM_SEED`
  - `LEGACY_MEMORY_ENTRY`
- `scope_level` 用于区分作用域：
  - `SESSION`
  - `PROJECT`
  - `USER`
- `memory_status` 用于区分状态：
  - `ACTIVE`
  - `SUPERSEDED`
  - `EXPIRED`
  - `DELETED`

### Session Extraction Window

`vmm_sessions` will gain:

- `last_extract_observed_at`
- `last_extract_completed_at`

These timestamps track which direct AI-written memories have already been exposed to the turn analyzer.

`vmm_sessions` 将新增：

- `last_extract_observed_at`
- `last_extract_completed_at`

这两个时间戳用于标记哪些 AI 主动写入记忆已经被 turn analyzer 观察过。

## RPC Plan

### New RPCs

1. `WriteMemories`
2. `GetMemoryDetails`

### Existing RPC Upgrades

1. `SearchMemoryEvents`
   - return `memory_ref`
   - return optional `source_ref`
   - return `source_kind`
   - return `scope_level`
   - return `category`
   - return `score`
   - return `abstract`
   - return `details_preview`
2. `GetTurnDetails`
   - keep for compatibility
   - internally remains turn-only
3. `PostAction`
   - keep response synchronous with actual write completion
   - update docs/comments away from “background persistence”

### Reference Protocol

Introduce:

```proto
message MemoryRef {
  enum RefType {
    REF_TYPE_UNSPECIFIED = 0;
    REF_TYPE_MEMORY = 1;
    REF_TYPE_TURN = 2;
  }
  RefType type = 1;
  uint64 id = 2;
}
```

Rules:

- memory search returns unified refs
- detail lookup accepts unified refs
- compatibility turn detail path remains available

## Direct Write Plan

Add one direct AI-write pipeline:

1. validate request
2. normalize abstract/details
3. compute `dedupe_hash`
4. find active duplicate within short scope window
5. if duplicate exists:
   - return existing `MemoryRef`
   - optionally bump lightweight counters
6. otherwise:
   - embed abstract
   - write vector row
   - insert unified memory row
   - return new `MemoryRef`

新增 AI 主动写记忆流程：

1. 校验请求
2. 规范化 `abstract/details`
3. 计算 `dedupe_hash`
4. 在短时间范围内查重
5. 如果已有活跃重复项：
   - 返回已有 `MemoryRef`
   - 可选地轻量增加计数
6. 否则：
   - 向量化 abstract
   - 写入向量行
   - 插入统一记忆行
   - 返回新 `MemoryRef`

## Exclusion Window Plan

When post-action analyzes one new turn:

1. capture `analysis_cutoff = now`
2. load direct memories where:
   - `source_kind = GRPC_AI_WRITE`
   - `origin_session_id = current session`
   - `created_timestamp > last_extract_observed_at`
   - `created_timestamp <= analysis_cutoff`
   - `memory_status = ACTIVE`
3. feed them as `recent_grpc_memory_writes`
4. if turn analysis and persistence succeed:
   - advance `last_extract_observed_at`
   - advance `last_extract_completed_at`

当 post-action 分析某个新 turn 时：

1. 先记 `analysis_cutoff = now`
2. 读取满足以下条件的 direct memory：
   - `source_kind = GRPC_AI_WRITE`
   - `origin_session_id = 当前 session`
   - `created_timestamp > last_extract_observed_at`
   - `created_timestamp <= analysis_cutoff`
   - `memory_status = ACTIVE`
3. 把它们作为 `recent_grpc_memory_writes` 输入给单轮分析器
4. 如果本轮分析和持久化成功：
   - 推进 `last_extract_observed_at`
   - 推进 `last_extract_completed_at`

## Lifecycle Plan

Memory lifecycle rules:

- recall alone does not refresh expiry
- only pre-check adoption refreshes weight and expiry
- `SESSION` memory uses idle cleanup instead of 30-day minimum
- `PROJECT` memory minimum lifetime is 30 days
- `USER` memory minimum lifetime is 180 days
- repeated cross-session adoption can upgrade `SESSION -> PROJECT`

记忆生命周期规则：

- 单纯召回不会刷新有效期
- 只有 pre-check 采纳才会刷新权重和有效期
- `SESSION` 级记忆按 idle 清理，不走 30 天保底
- `PROJECT` 级记忆最短 30 天
- `USER` 级记忆最短 180 天
- 跨 session 多次采纳可触发 `SESSION -> PROJECT` 升级

## Execution Order

1. Create this plan document and keep it updated.
2. Upgrade the domain model for unified memory and memory refs.
3. Upgrade storage schemas and relational adapters.
4. Add direct AI-write use case and gRPC surface.
5. Add unified memory detail/query protocol.
6. Connect direct-write exclusion reads into post-action immediate turn analysis.
7. Migrate workspace maintenance flows off `vmm_memory_entries`.
8. Remove `vmm_memory_entries` from schema and logic.
9. Reconnect pre-check adoption on top of the new model.
10. Update docs, run focused tests, then run `go test ./...`.

执行顺序：

1. 先创建并维护这份计划文档
2. 升级统一记忆与统一引用的领域模型
3. 升级存储表结构和关系适配器
4. 增加 AI 直接写记忆用例与 gRPC 接口
5. 增加统一记忆详情/查询协议
6. 把 direct-write 排斥窗口真正接到即时 turn 分析
7. 把 workspace 维护链路从 `vmm_memory_entries` 切走
8. 从表结构和逻辑中移除 `vmm_memory_entries`
9. 在新模型上接回 pre-check 的采纳流程
10. 更新文档，跑定向测试，再跑 `go test ./...`

## Working Log

- 2026-04-02: plan document created
- 2026-04-02: immediate-turn `analyze_turn` contract and post-action mainline already completed before this file
- 2026-04-02: unified memory RPC surface, SQLite unified table migration, exclusion-window persistence, and `PreCheck` two-stage adoption were connected end-to-end
- 2026-04-02: focused tests plus `go test ./...` passed after the live pre-check reconnection

工作日志：

- 2026-04-02：创建计划文档
- 2026-04-02：在此文件创建前，已完成即时单轮 `analyze_turn` 契约与 post-action 主链改造
