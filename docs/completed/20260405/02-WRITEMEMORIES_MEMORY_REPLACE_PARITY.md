# 任务目标

在第一阶段 `post-action` 记忆替代闭环的基础上，开启第二阶段实现，重点完成以下目标：

1. 让 `WriteMemories` 复用与 `post-action` 一致的记忆更替语义，避免继续停留在“只有 `dedupe_hash` 软幂等、没有语义级 replace”的状态。
2. 修复当前统一 reviewer 替代链路里剩余的三项稳健性问题：
   - 候选级 supersede 与整轮级 supersede 混用导致的误退役风险；
   - admission filter 或 reviewer 前置裁剪后仍保留 supersede 结果的无替代物退役风险；
   - `similar_memories` 依赖返回顺序回贴，而不是使用 `QueryIndex` 的错配风险。
3. 保持 SQLite 与 PostgreSQL 语义一致，不把第二阶段实现收敛成 PostgreSQL 专用逻辑。
4. 保证本轮修改不破坏现有 `post-action`、`pre-check`、`GetTurnDetails`、记忆衰减、提级与清理配置链路。

# 详细执行步骤

1. 梳理当前 `WriteMemories` 直接写记忆链路，确认其与 `post-action` 在去重、替代、向量写入和关系库落库上的差异点。
2. 为 `WriteMemories` 设计并接入与 `memory_replace_scope` 对齐的语义级替代流程：
   - 复用当前相似记忆检索能力；
   - 明确“丢弃新候选 / 保留并替代旧记忆 / 保留并行补充”三类结果；
   - 保持现有 `dedupe_hash` 作为近窗软幂等兜底，而不是删除它。
3. 调整 `post-action` 替代数据结构，使 supersede 判定从“整轮级列表”收敛到“候选级映射”，避免 reviewer 丢弃某条候选后仍错误退役其对应旧记忆。
4. 修复 admission filter 与 reviewer 前置裁剪后的 supersede 清理逻辑，确保一旦相关记忆候选全部消失，就不会继续退役旧记忆。
5. 修复 `similar_memories` 绑定逻辑，改为基于 `MemoryQueryGroupResult.QueryIndex` 回贴，而不是依赖搜索结果切片顺序。
6. 为上述改动补齐 usecase / processor / config / SQLite / PostgreSQL 相关测试，覆盖顺序乱序、候选裁剪、跨链路一致性和 `WriteMemories` 替代行为。
7. 同步更新文档，至少包含：
   - `README.md`
   - `docs/post-action-guide_CN.md`
   - 如有必要，补充 `WriteMemories` 相关说明

# 技术选型

1. `WriteMemories` 优先复用现有 memory query 与统一替代辅助逻辑，不引入独立的第三套替代状态机。
2. supersede 判定以候选级结构为权威来源；整轮级列表只作为兼容旧分析器输出的过渡数据，不再直接代表最终退役集合。
3. `similar_memories` 一律使用 `QueryIndex` 显式映射，禁止继续依赖结果顺序这一脆弱假设。
4. SQLite 与 PostgreSQL 共用 domain / usecase 契约；存储层只在 SQL 方言细节和索引实现上分叉。

# 验收标准

1. `WriteMemories` 在 `memory_replace_scope` 允许范围内，可以实现：
   - 语义重复时不新增脏记忆；
   - 新事实覆盖旧事实时，旧记忆退出热主表；
   - 并行补充时新旧记忆并存。
2. reviewer 丢弃单个记忆候选时，不会继续错误退役该候选对应的旧记忆。
3. admission filter 或 reviewer 前置裁剪导致相关候选全部消失时，不会留下悬空 supersede。
4. 检索结果乱序或跳过空组时，`similar_memories` 仍能正确绑定到原候选。
5. SQLite 与 PostgreSQL 在本轮新增语义上保持一致。
6. 与本轮相关的测试通过，并且不破坏仓库要求的关键测试集合。

# 执行变更总结

## 1. 核心修复与调整概述

本阶段已经完成 `WriteMemories` 与 `post-action` 的记忆更替语义对齐，并同时收口了三项 reviewer 稳健性问题。

- `analyze_turn` 现在支持候选级 `memory_nodes[].supersede_memory_ids[]`，并继续兼容顶层 `superseded_memory_ids`
- `post-action` 的 supersede 收敛已改成“跟候选走”，不再让被 admission filter 或 reviewer 丢弃的候选继续退役旧记忆
- `similar_memories` 改为按 `MemoryQueryGroupResult.QueryIndex` 回贴，消除了对搜索结果顺序的脆弱依赖
- `WriteMemories` 在原有 `dedupe_hash` 软幂等之后，新增统一 reviewer 语义去重与替代能力
- SQLite / PostgreSQL 都补齐了“主动写入新记忆 + 同事务 supersede 旧记忆 + 事务后删除旧向量”的原子出口

## 2. 📂文件变更清单

新增：

- `internal/app/usecase/memory_replace_scope.go`

修改：

- `configs/prompts/qwen3.5-base/analyze_turn.md`
- `README.md`
- `docs/post-action-guide_CN.md`
- `internal/logic/domain/turn_analysis.go`
- `internal/logic/domain/memory.go`
- `internal/logic/processor/turn_analyzer.go`
- `internal/logic/processor/turn_analyzer_test.go`
- `internal/app/usecase/postaction.go`
- `internal/app/usecase/postaction_candidate_review.go`
- `internal/app/usecase/postaction_candidate_review_test.go`
- `internal/app/usecase/memory_query.go`
- `internal/app/usecase/memory_query_test.go`
- `internal/app/app.go`
- `internal/adapters/outbound/vldb_sqlite/store.go`
- `internal/adapters/outbound/vldb_postgres/memory_store.go`

删除：

- 无

## 3. 💻关键代码调整详情

1. 分析器契约调整
   - `MemoryNodeCandidate` 新增 `SupersedeMemoryIDs`
   - `parseTurnAnalysisResponse` 支持解析候选级 supersede，并把候选级结果并入顶层兼容字段
   - `validateTurnAnalysis` 增加对候选级 supersede id 的 anchor 校验

2. `post-action` 替代收敛调整
   - admission filter 后新增 supersede 清理逻辑：当部分记忆候选被首轮过滤且存活候选没有候选级 supersede 映射时，清空旧的整轮 supersede
   - unified reviewer 合并逻辑改成优先使用“被接纳候选自己的 supersede ids”
   - 只有在 reviewer 未丢弃任何记忆候选、且不存在候选级 supersede 映射时，才保留旧的整轮兼容 supersede
   - `similar_memories` 改为使用 `QueryIndex` 显式回贴

3. `WriteMemories` 语义替代接入
   - 新增 `ConfigureMemoryReplace`
   - `WriteMemories` 保留原有 24 小时 `dedupe_hash` 软幂等
   - 对未命中软幂等的新项，复用统一 reviewer 做语义重复判断
   - reviewer 丢弃且存在可信 `similar_memories` 时，直接返回旧记忆引用
   - reviewer 丢弃但没有可信旧记忆时，降级为继续创建新记忆，避免显式工具写入被静默吞掉

4. 存储层原子出口
   - SQLite 新增 `ApplyDirectMemoryWrite`
   - PostgreSQL 新增 `ApplyDirectMemoryWrite`
   - 两端都支持：
     - 新记忆入库
     - 旧记忆状态切为 `superseded`
     - 提交后返回旧 `vector_id`，供用例层统一删除旧向量

5. 组合根接线
   - `app.go` 现在复用同一个 `PostActionCandidateReviewer`
   - 该 reviewer 同时供 `post-action` 和 `WriteMemories` 使用

6. 测试与验证
   - 新增候选级 supersede 解析测试
   - 新增 admission filter 清理 legacy supersede 测试
   - 新增 reviewer 部分接纳时只保留对应 supersede 的测试
   - 新增 `QueryIndex` 乱序回贴测试
   - 新增 `WriteMemories` 语义去重、语义替代、无可信旧记忆时降级创建三类测试
   - 已执行：
     - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
     - `go test ./internal/adapters/outbound/vldb_sqlite ./internal/adapters/outbound/vldb_postgres`
     - `go test ./...`

## 4. ⚠️遗留问题与注意事项

- 当前 `WriteMemories` 的语义 dedupe 在 reviewer 丢弃且存在多个高相似旧记忆时，会默认复用分数最高的那条旧记忆；如果后续要做更强可解释性，可以再补显式“选中的旧记忆 id”输出位
- 第二阶段已完成 `WriteMemories` 语义替代与 supersede 稳健性修复，但 retention worker、trash 表和最终冷数据清理仍属于后续阶段
- 当前工作区还存在第一阶段未提交的相关改动，本次没有回滚或覆盖这些现有修改
