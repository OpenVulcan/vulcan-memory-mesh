# 任务目标

本次任务先不直接改代码，先基于当前仓库实现，形成一份可执行、可验证、可回退的 `postaction` 大改方案，目标如下：

1. 彻底厘清 `postaction` 当前从入站、排队、L1 提炼、检索、L2 评审、向量持久化到最终写库的完整执行链路。
2. 在确认现状职责后，设计一套“L1 不再承担 whole-session 记忆去重职责，重复/替代判断统一下沉到检索层 + L2”的重构方案。
3. 控制长生命周期 session 下 `active_memory_nodes` 持续膨胀导致的 prompt 尺寸风险，避免后续分析请求被历史活跃记忆线性拖大。
4. 在不破坏现有 `recent_grpc_memory_writes` 排斥能力、画像评审能力、持久化一致性与向量回滚机制的前提下，给出分步实施方案与验收标准。

# 当前执行过程理解

## 1. 入站接收阶段

当前 `postaction` 首先由 `PostActionUseCase.Execute` 执行：

1. 校验 session / user / project / user_content / assistant_content / timeline。
2. 对文本做 PII 脱敏。
3. 如果是普通单轮问答且没有 timeline，则先经过 `NoiseGate`，被判定为纯噪声则直接丢弃。
4. 将当前轮以标准 `TurnRecord` 形式持久化到关系库。
5. 不阻塞等待 LLM，而是把该 session 放进异步队列。

这一层的职责非常明确：只负责把当前轮可靠入库，并把后续分析工作交给后台，不直接做提炼。

## 2. 异步排队阶段

后台由 `postaction_queue.go` 驱动：

1. 队列以 session 为粒度去重。
2. 同一 session 高频写入时，不会为每个 turn 单独起一个分析任务，而是折叠进同一个 session 工作项。
3. 工作器会加载该 session 当前所有 `pending` turn，按持久化顺序逐条执行后续单轮分析。
4. 如果某条 turn 解码失败，会标记为损坏并跳过，避免无限重试卡死整个 session。

这一层的职责是保证“入库成功”和“后续提炼”解耦，同时尽可能串行、稳定地消费同一个 session 的待处理 turn。

## 3. L1 输入组装阶段

`buildTurnAnalysisInput` 当前会组装 4 类输入给 `postaction_l1_main`：

1. `reference_turns`
   - 来自 `LoadRecentSessionHistory`
   - 只取最近已完成提炼的 turn 精要
   - 会按 `MaxInputTokens` 做预算裁剪
2. `target_turn`
   - 当前正在处理的唯一 turn
   - 会被序列化成紧凑 JSON
3. `active_memory_nodes`
   - 来自 `LoadActiveSessionMemoryNodes`
   - 实际语义不是“最近几轮的记忆”，而是“当前 session 起源的所有 active 且未过期统一记忆”
   - 当前不会按 token 或数量上限裁剪
4. `recent_grpc_memory_writes`
   - 来自 `LoadRecentDirectMemoryWrites`
   - 用于排斥工具链已经直写入库的内容

这里存在当前大改的核心问题：`reference_turns` 已经受预算控制，但 `active_memory_nodes` 没有，会随着长寿命 session 持续增长。

## 4. L1 提炼阶段

`TurnAnalyzer.Analyze` 会调用 `postaction_l1_main`，产出：

1. `details`
2. `memory_nodes`
3. `profile_nodes`
4. `superseded_memory_ids`

随后 `validateTurnAnalysis` 会做严格校验：

1. `turn_id` 必须和当前目标 turn 对齐。
2. `user_input_kind / evidence_source / admission / admission_reason` 必须合法。
3. `memory_nodes[].supersede_memory_ids` 与顶层 `superseded_memory_ids` 只能引用输入中的 `active_memory_nodes.memory_id`。

这意味着当前契约里，L1 不只是“提炼当前轮候选”，还被要求基于全量 `active_memory_nodes` 直接承担一部分重复/替代判断职责。

## 5. 首轮粗过滤阶段

L1 完成后，并不会直接持久化，而是先走 `applyPostActionAdmissionFilter`：

1. 清掉 `admission = drop` 的记忆候选。
2. 清掉 `admission = drop` 的画像候选。
3. 在记忆候选被过滤后，重新对齐顶层 `superseded_memory_ids`，避免后面误退役没有存活候选支撑的旧记忆。

这一层的职责是保留粗粒度的“明显不该进后续阶段”的过滤能力，比如 QA 回显、通识回答、瞬时状态等。

## 6. 检索与硬排重阶段

`reviewTurnCandidates` 不会直接把 L1 结果交给 L2，而是先做“候选 -> 检索证据”扩充：

1. 对每个剩余 `memory_nodes` 生成检索 query。
2. 调用 `MemoryUseCase.Search`：
   - 先做 query embedding
   - 再做向量检索
   - 如启用 hybrid，则融合 lexical / BM25
   - 再做 rerank / decay / context evidence scoring / MMR
3. 为每个候选生成 `similar_memories`
4. 额外保留一份 `hard_dedupe` 候选池
5. 如果命中同 category 且 cosine 足够高的旧记忆，会直接做本地 `hard drop`，这部分甚至不会再送给 L2

因此，真正强力的“历史库里有没有同义旧记忆”判断，本来就已经主要发生在 L1 之后的检索层，而不是完全依赖 L1 的 `active_memory_nodes`。

## 7. L2 联合评审阶段

`postaction_l2_main` 当前做的是一次联合评审：

1. `memory` 部分：
   - 对新记忆候选做 keep / drop
   - drop 时可给 `dedupe_memory_id`
   - accept 时可给 `supersede_memory_ids`
2. `user / project` 部分：
   - 对新画像候选做接纳或判无效
   - 接纳时给规范化内容、优先级、生命周期、`supersede_node_ids`
   - 也可以通过 `retire_only_node_ids` 直接退役旧画像节点

L2 本质上已经是“基于候选 + 相似旧记忆 + 当前画像事实快照”做最终准入与替代决策的主裁判。

## 8. 持久化与回写阶段

只有经过 L2 后仍存活的 `memory_nodes`，才会进入正式向量持久化：

1. `persistMemoryNodeVectors`
   - 对 surviving memory node 的 `abstract` 做 embedding
   - 写入向量库
   - 回填 `vector_id`
2. `ApplyTurnAnalysis`
   - 更新 turn 的 `details`
   - 插入新的 memory/profile 原子节点
   - 把 `superseded_memory_ids` 对应旧记忆改成 superseded
   - 返回 superseded 向量 id，供后续物理删除
3. 成功后推进 `AdvanceSessionExtractWindow`

这说明：

1. L2 之前确实已经发生“为了找相似旧记忆而进行的 query 向量化与检索”。
2. 但新记忆的正式持久化向量，是在 L2 之后才生成。
3. 当前系统里，真正写库生效的 supersede 结果，最终依赖的是 L2 之后合并得到的 supersede 集合。

# 当前问题判断

基于上述链路，当前最核心的问题有 4 个：

## 1. `active_memory_nodes` 会随长寿命 session 持续膨胀

它的加载条件不是“最近几轮”，而是：

1. `origin_session_id = 当前 session`
2. `memory_status = active`
3. 未过期

这会导致长时间、跨多主题 session 的历史活跃记忆持续进入 L1 prompt，且当前没有预算裁剪。

## 2. L1 与“检索层 + L2”职责明显重叠

当前设计里：

1. L1 被要求根据 `active_memory_nodes` 判断重复/覆盖。
2. L1 之后又会重新通过检索召回 `similar_memories`。
3. L2 再根据这些相似旧记忆做最终 keep / drop / supersede。

也就是说，session 级重复判断实际上在两个阶段被做了两次，且后一个阶段明显更强、更接近真实长期库。

## 3. L1 的 supersede 能力与 `active_memory_nodes` 强耦合

只要保留当前校验与 prompt 契约：

1. L1 想输出 supersede，就必须先看到全量 `active_memory_nodes`
2. `validateTurnAnalysis` 也要求 supersede id 必须来自这批输入

这会逼着 L1 持续吞下大量历史记忆，只为了支持 supersede 字段。

## 4. 实际最该保留的是“近因上下文”和“直写排斥”

L1 真正不可丢的是：

1. `reference_turns`
   - 帮助理解当前轮在接着说什么
2. `recent_grpc_memory_writes`
   - 避免工具链已经写入的事实被重复提炼

相比之下，全量 `active_memory_nodes` 对 L1 更像高成本、强耦合、但收益重复的输入。

# 调整原则

本次重构计划遵循以下原则：

1. L1 只做“当前轮候选提炼 + 粗过滤信号输出”，不再承担 whole-session 级重复判断主职责。
2. 检索层 + L2 成为“旧记忆重复 / 替代 / 覆盖”的唯一主裁判。
3. `recent_grpc_memory_writes` 的绝对排斥能力必须保留。
4. 画像评审链路不与本次记忆重构互相耦合，除非必要，不改其判定职责。
5. 所有变更必须优先保证：
   - 持久化正确性
   - supersede 正确性
   - 向量回滚安全
   - prompt 尺寸可控
6. 在大改初期优先保留向后兼容解析能力，避免一次性拆掉所有旧字段导致意外回归。

# 具体调整方案

## 方案总述

目标方案是：

1. L1 不再接收全量 `active_memory_nodes`
2. L1 prompt 不再承担 session 级去重 / supersede 判断职责
3. L2 继续基于检索得到的 `similar_memories` 做最终去重与替代
4. 最终写库的 supersede 集合以 L2 结果为准

## 步骤一：收缩 L1 输入契约

拟修改：

1. `buildTurnAnalysisInput`
   - 停止为 L1 加载 `LoadActiveSessionMemoryNodes`
2. `TurnAnalysisInput`
   - 移除或停用 `ActiveMemoryNodes`
3. `renderTurnAnalysisRequest`
   - 不再把 `active_memory_nodes` 序列化进 `postaction_l1_main` 请求体
4. `renderTurnAnalysisSystemPrompt`
   - 去掉 `ACTIVE_MEMORY_RULE`

预期效果：

1. L1 prompt 尺寸不再和 session 历史活跃记忆量线性耦合。
2. L1 真正只聚焦当前轮与近因上下文。

## 步骤二：收缩 L1 prompt 职责

拟修改：

1. `postaction_l1_main` 中英文 prompt
   - 删除 `active_memory_nodes` 输入说明
   - 删除“基于旧记忆判断重复/覆盖”的任务描述
   - 删除或停用 `superseded_memory_ids` 作为 L1 主要输出职责
2. `validateTurnAnalysis`
   - 不再把 L1 当成 supersede 主裁判
   - 如果保留旧字段兼容解析，则默认要求为空或在无输入时严格拒绝非空输出

预期效果：

1. L1 职责被收敛为“提炼候选事实”。
2. L1 不再需要为了输出 supersede 而吞下全 session 历史记忆。

## 步骤三：强化 L2 的唯一主裁判地位

拟修改：

1. 保留当前检索 -> `similar_memories` -> `hard_dedupe` -> L2 的主链
2. 明确 `postaction_l2_main` 才是：
   - `dedupe_memory_id`
   - `accepted_candidates[].supersede_memory_ids`
   的权威来源
3. 同步补全文档和 prompt 中对该职责边界的描述

预期效果：

1. “相似旧记忆是否已完整覆盖当前候选”统一由检索证据驱动。
2. L2 的 keep / drop / supersede 结果直接成为最终持久化依据。

## 步骤四：校准代码合并逻辑

拟检查并调整：

1. `mergePostActionSupersededMemoryIDs`
2. `reconcilePostActionSupersededMemoryIDsAfterMemoryFilter`
3. `collectAcceptedPostActionCandidateSupersededMemoryIDs`

目标是确保：

1. 当 L1 不再提供 supersede 信息时，L2 输出仍能完整驱动最终 supersede 集合。
2. 不会因为移除 L1 supersede 输入而导致旧记忆无法退役。

## 步骤五：同步测试与文档

必须补的回归点：

1. L1 输入不再包含 `active_memory_nodes`
2. 长 session 不会因历史活跃记忆增长而把 L1 请求无限放大
3. `recent_grpc_memory_writes` 排斥能力不受影响
4. 检索 + L2 仍可正确：
   - 丢弃重复记忆
   - 标记 `dedupe_memory_id`
   - 标记 `supersede_memory_ids`
5. 画像评审链路不回归
6. 落库后 superseded 旧记忆状态和向量清理仍正确

文档同步至少包括：

1. `README.md`
2. `docs/post-action-guide_CN.md`
3. 相关 prompt 说明文档

# 涉及文件清单（预估）

本次正式实施时，预计会涉及以下文件：

1. `internal/app/usecase/postaction_analysis.go`
2. `internal/logic/domain/turn_analysis.go`
3. `internal/logic/processor/render.go`
4. `configs/prompts/default_cn/postaction_l1_main.md`
5. `configs/prompts/default_en/postaction_l1_main.md`
6. `configs/prompts/default_cn/postaction_l2_main.md`
7. `configs/prompts/default_en/postaction_l2_main.md`
8. `internal/app/usecase/postaction_candidate_review.go`
9. `internal/app/usecase/postaction_test.go`
10. `internal/app/usecase/postaction_candidate_review_test.go`
11. `README.md`
12. `docs/post-action-guide_CN.md`

# 技术选型与控制边界

本次方案明确不做以下事情：

1. 不改数据库 schema
2. 不改当前向量检索主链
3. 不改画像生命周期模型
4. 不把重复判断前移到“向量正式持久化之后”

原因是：

1. 当前检索层与 L2 已经具备完成记忆重复/替代判断的能力
2. 真正的问题不在于检索能力缺失，而在于 L1 输入与职责过重、重复

# 验收标准

当后续正式实施完成后，必须满足以下标准：

1. `postaction_l1_main` 请求体不再包含全量 `active_memory_nodes`
2. L1 仍能正确输出：
   - `details`
   - `memory_nodes`
   - `profile_nodes`
   - 基于 `recent_grpc_memory_writes` 的绝对排斥结果
3. 检索层 + L2 仍能正确处理重复记忆与 supersede
4. 长 session 的 prompt 尺寸增长趋势显著收敛
5. 画像评审与最终持久化结果不回归
6. 最少通过：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
7. 作为大改，在合并前还必须通过：
   - `go test ./...`
   - `.\make.ps1 build`

# 风险与回退策略

## 主要风险

1. 如果 L1 supersede 字段被完全移除，但某些边缘路径仍依赖它，可能造成旧记忆退役不完整。
2. 如果只改 prompt、不改校验逻辑，可能出现 L1 输出与校验契约不匹配。
3. 如果只删输入、不补测试，容易把“recent direct writes 排斥”或“画像评审合并”一起带坏。

## 回退策略

1. 初期优先保留解析兼容，但停止 prompt 暴露。
2. 先让 L1 不再使用 `active_memory_nodes`，再视回归情况决定是否彻底删除结构字段。
3. 任何阶段如果发现 L2 supersede 集合无法稳定覆盖原能力，立即退回到“字段保留但不注入 prompt”这一中间态。

# 执行变更总结

## 1. 核心修复与调整概述

本次未直接修改 `postaction` 主链逻辑，而是先完成两件关键前置工作：

1. 先将当前工作区基线提交并推送，固定后续大改的可回溯起点。
2. 在阅读 `postaction` 入站、队列、L1、检索、L2、画像评审、向量持久化与写库代码后，输出一份针对“L1 去 session 级重复职责”的完整重构计划。

当前确认的核心结论是：

1. L1 之后本来就存在更强的“检索 + hard dedupe + L2”重复判断链路。
2. 现有 `active_memory_nodes` 会把 whole-session 活跃记忆持续注入 L1，存在明显 prompt 膨胀风险。
3. 本次正式实施应以“收缩 L1、强化检索层与 L2”为中心，而不是继续在 L1 上追加更多判断逻辑。

## 2. 📂文件变更清单

新增：

1. `docs/plan/20260411-08-postaction-l1-l2-responsibility-refactor.md`

## 3. 💻关键代码调整详情

本次任务为计划设计阶段，未直接改动 `postaction` 代码；但已完成以下关键现状确认：

1. 确认 `active_memory_nodes` 的真实来源是“当前 session 起源、active 且未过期”的统一记忆行，而非最近几轮。
2. 确认 L1 之后存在完整的 query embedding、向量检索、hybrid/BM25、hard dedupe 与 L2 联合评审链路。
3. 确认新记忆的正式向量持久化发生在 L2 之后，而不是之前。
4. 确认当前 L1 prompt 与 `validateTurnAnalysis` 共同把 supersede 责任绑定到了 `active_memory_nodes` 上。

## 4. ⚠️遗留问题与注意事项

1. 当前仅完成计划与现状分析，尚未开始正式重构。
2. 正式实施时必须同步改 prompt、输入组装、校验逻辑、合并逻辑、测试与文档，不能只删 prompt 或只删输入。
3. 当前代码基线已先行提交并推送，提交号为 `d7cd5df`，后续正式改造应以此为基线继续推进。
