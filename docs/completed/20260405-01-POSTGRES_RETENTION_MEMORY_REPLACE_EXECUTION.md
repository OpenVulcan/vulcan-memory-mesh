# 任务目标

基于前一版《SESSION_RECYCLE_BIN_AND_RETENTION_PLAN》设计稿，开始落地第一阶段实现，优先完成以下两条主链：

1. 补齐 `post-action` 的跨 scope 记忆替代闭环，让新记忆可以在 `project / space / team / session` 作用域内真正接管旧记忆，而不是只保留“新候选接受/拒绝”半套决策。
2. 为后续回收链路补齐 `retention` 配置入口与 PostgreSQL 基础存储骨架，至少把配置模型、回收批次表、向量 GC 任务表与最小事务出口准备好。
3. 保证本次实现不破坏现有 `analyze_turn -> superseded_memory_ids`、`SearchMemoryEvents -> source_turn_id -> GetTurnDetails`、session 提级与 Weibull 读时衰减语义。

# 详细执行步骤

1. 先梳理当前 `post-action` 记忆候选评审契约、配置装配点和 PostgreSQL 持久化事务边界，确认需要修改的 prompt、processor、domain、usecase、adapter 与测试范围。
2. 扩展 `review_postaction_candidates` 的记忆评审输出，支持 `accepted_candidates[]` 与 `supersede_memory_ids[]`，并在处理器与用例层把该结构透传出来。
3. 新增 `memory_replace_scope` 配置项，接入 `post-action` 相似记忆召回链路，默认值设为 `project`，并对非法枚举值做校验。
4. 调整 PostgreSQL `ApplyTurnAnalysis`：
   - 保留现有同 session `superseded_memory_ids` 行为；
   - 为新 reviewer 产出的跨 scope 旧记忆替代结果预留统一出口；
   - 第一阶段至少完成“旧记忆退出热主表 + 向量删除任务坐标可收集”的事务路径。
5. 新增 `retention` 配置结构、默认值和校验规则，先把运行期开关、热窗口额外缓存、session 空闲阈值、回收站保留时长接入配置系统。
6. 在 PostgreSQL 侧新增第一阶段最小所需表：
   - `vmm_recycle_batches`
   - `vmm_vector_gc_jobs`
   - 若实现进度允许，再补 `vmm_memory_nodes_trash` 与 `vmm_memory_context_edges_trash`
7. 为上述实现补齐用例与存储测试，重点覆盖：
   - 同 session supersede 不回归；
   - 跨 scope reviewer 替代结构解析；
   - `memory_replace_scope` 默认值与校验；
   - PostgreSQL 新增 schema / 任务表初始化；
   - PostgreSQL 写回时对旧向量清理坐标的稳定收集。

# 技术选型

1. 本次优先落 PostgreSQL 主链，SQLite 暂不做完整同语义实现，但所有新增 domain / config 契约必须为后续 SQLite 对齐保留扩展位。
2. 记忆替代闭环继续沿用“同一事务内切换热主表可见性，事务后异步清理向量”的一致性模型，不直接在请求事务里调用向量删除。
3. `memory_replace_scope` 作为独立配置，不复用 `pre_check.search_scope`，避免在线召回范围与记忆更替范围耦合。
4. `retention` 第一阶段先做配置与 PostgreSQL 基础 schema，不在本次直接把完整 recycle worker 全量落地，避免一次性跨越过大。

# 验收标准

1. 现有 `analyze_turn -> superseded_memory_ids` 路径保持可用，不回归。
2. `review_postaction_candidates` 可以表达“接受新候选并替代哪些旧记忆”的结构化结果。
3. 默认配置下 `memory_replace_scope = project`，并支持 `session / project / space / team`。
4. PostgreSQL 配置加载与 schema 初始化能够识别 `retention` 新增字段与最小基础表。
5. 与本次改动直接相关的测试通过，且不破坏仓库要求的关键测试集合。

---

## 执行变更总结

### 1. 核心修复与调整概述

1. 补齐了 `PostAction` 统一 reviewer 的记忆替代闭环：memory 结果块现在支持 `accepted_candidates[]` 与 `supersede_memory_ids[]`，并在用例层合并进最终 `superseded_memory_ids` 写回链路。
2. 新增独立的 `memory_replace_scope` 配置，不再复用 `pre_check.search_scope`，默认按 `project` 级别执行跨 session 记忆更替，且支持 `session / project / space / team`。
3. 新增 `retention` 配置结构、默认值、环境变量覆盖和校验逻辑，为后续回收站、session 回收与向量 GC 链路准备运行时入口。
4. PostgreSQL 侧补齐了第一阶段最小 schema 骨架，新增回收批次表与向量 GC 任务表，保证后续冷数据治理能沿现有组合库存储继续扩展。
5. 同步更新了 `README.md` 与 `docs/post-action-guide_CN.md`，使仓库文档与当前代码契约保持一致。

### 2. 📂文件变更清单

新增文件：

- `internal/app/usecase/memory_replace_scope.go`

修改文件：

- `README.md`
- `configs/prompts/qwen3.5-base/review_postaction_candidates.md`
- `docs/post-action-guide_CN.md`
- `internal/adapters/outbound/vldb_postgres/helpers.go`
- `internal/adapters/outbound/vldb_postgres/schema.go`
- `internal/app/app.go`
- `internal/app/usecase/memory_query.go`
- `internal/app/usecase/postaction.go`
- `internal/app/usecase/postaction_candidate_review.go`
- `internal/app/usecase/postaction_candidate_review_test.go`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/logic/domain/postaction_review.go`
- `internal/logic/processor/postaction_candidate_reviewer.go`
- `internal/logic/processor/postaction_candidate_reviewer_test.go`

删除文件：

- 无

### 3. 💻关键代码调整详情

1. 统一 reviewer prompt 与 processor 解析逻辑已升级为结构化 memory 评审结果：
   - 支持每个被保留的新记忆候选单独携带 `supersede_memory_ids[]`
   - 保留旧版 `accepted_candidate_indexes` 作为兼容回退输入
2. `PostActionUseCase.reviewTurnCandidates` 现在会：
   - 用 `memory_replace_scope` 控制相似旧记忆召回
   - 在 `session` 作用域下额外下推 `session_id`
   - 校验 reviewer 只能替代该候选真正看过的 `similar_memories.memory_id`
   - 把合法替代结果并入 `analysis.SupersededMemoryIDs`
3. `MemoryQueryCommand` 新增 `SessionID`，并扩展了 `scope_override=session` 的校验与过滤能力。
4. 根配置新增：
   - `memory_replace_scope`
   - `retention.enabled`
   - `retention.recycle_scan_interval`
   - `retention.turn_keep_extra_turns`
   - `retention.session_idle_recycle_after`
   - `retention.trash_retention`
   - `retention.protect_priority_floor`
   - `retention.protect_memory_level_floor`
   - `retention.skip_protected_shared_memories`
5. PostgreSQL schema 第一阶段新增：
   - `vmm_recycle_batches`
   - `vmm_vector_gc_jobs`
   - 对应查询与批次索引
6. 已执行测试：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`

### 4. ⚠️遗留问题与注意事项

1. 本次只完成了 `retention` 的配置入口与 PostgreSQL 基础 schema，完整 recycle worker、trash 表迁移与最终冷数据清理尚未开始实现。
2. PostgreSQL 新增的 `vmm_recycle_batches / vmm_vector_gc_jobs` 当前仍是后续阶段使用的骨架表，本次尚未把回收任务真正入表调度。
3. `memory_replace_scope` 已独立于 `pre_check.search_scope`，后续如调整默认值或支持更多作用域，需要同步修改文档、测试和 reviewer prompt 约束。
