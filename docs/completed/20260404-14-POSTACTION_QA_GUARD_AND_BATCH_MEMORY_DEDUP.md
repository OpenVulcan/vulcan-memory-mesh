# PostAction 问答型噪音抑制与批量记忆去重计划

## 1. 任务目标

解决当前 `PostAction` 在单轮提炼后产生两类额外噪音的问题：

1. 用户只是提出问题，助手基于已有记忆、已有画像或模型自身知识给出回答，但该回答内容又被当成新的 `memory_nodes / profile_nodes` 二次入库；
2. 当前轮次新生成的多条记忆候选，与记忆库里已有内容高度重复时，缺少“高相似召回 + 批量语义判重”闭环，导致重复记忆持续累积。

本轮方案目标是：

1. 在 `PostAction` 首轮 LLM 提炼阶段，显式识别“用户提问型问答”与“用户主动声明型事实”；
2. 对“用户提问，且助手回答内容本质属于用户画像 / 项目画像 / 既有记忆回显”的情况，禁止进入长期记忆；
3. 对同一轮提炼出来的多条记忆候选，按与 `PreCheck` 对等的记忆检索作用域做统一召回；
4. 当同一轮同时存在 `memory_nodes` 与 `profile_nodes` 候选时，尽可能复用同一个 reviewer LLM 一次性完成判断；
5. 对高相似候选执行一次批量 LLM 判重，而不是逐条调用；
6. 为该流程增加可观测的“语义压缩率（Compaction Rate）”指标，衡量问答回显与高重复候选被压缩掉的比例；
7. 对“并非 AI 自身已有能力，而是通过较高成本检索、查询资料、访问网站、工具调用等获得的高价值结果”保留放行通道，避免误杀高价值记忆；
8. 在不破坏现有依赖方向 `adapters -> app -> logic/domain` 的前提下完成改造。

## 2. 现状与问题拆解

### 2.1 当前首轮提炼只做“抽取”，没有足够强的“准入判断”

当前 `turn_analyzer` 负责从 `target_turn` 直接输出：

- `details`
- `memory_nodes`
- `profile_nodes`
- `superseded_memory_ids`

虽然现有 `analyze_turn` prompt 已经有一条规则，禁止把“用户询问 AI 自己画像，助手只是复述/迎合”的内容写回记忆，但覆盖范围仍然偏窄，至少存在以下缺口：

1. 没有把“项目画像 / 项目约束 / 工作约定”的问答型回显一并覆盖；
2. 没有显式区分“用户主动陈述事实”与“助手基于历史记忆回答问题”；
3. 没有结构化输出“该候选是否允许入库、为什么不允许入库”的判定结果；
4. 目前 `ApplyTurnAnalysis` 会对通过解析的 `memory_nodes` 直接落库，因此一旦首轮抽取误把问答回显当成事实，后面没有第二道准入闸门。

### 2.2 当前后置判断天然会走向“多次 LLM 调用”

仓库现状里，画像判断已经有一套独立 reviewer；如果本轮只额外新增记忆判重 reviewer，而不做统一设计，就会出现：

1. 首轮 `analyze_turn` 一次调用；
2. profile reviewer 再调用一次；
3. memory dedupe reviewer 再调用一次。

这样虽然逻辑上可行，但会带来：

1. 单轮 post-action 的模型调用次数增加；
2. memory/profile 对同一轮语义的判断上下文被拆散；
3. 一条事实在 memory 与 profile 两个面向上可能出现不一致结论。

### 2.3 当前“问答型排除”如果定义过粗，会误伤高价值外部检索结果

这次规则不能简单写成“只要用户在提问，就默认不记忆”。因为实际还存在另一类应当放行的高价值内容：

1. 用户虽然是在提问或下达指令；
2. 但助手为了回答该问题，并不是单纯复述已有记忆、已有画像或模型常识；
3. 而是通过较高成本的检索、资料查询、网站访问、工具调用、结果比对、归纳总结之后，产出了新的高价值结论；
4. 这类结果本质上属于“新获得的信息资产”，不应被问答型排除规则直接过滤。

如果不把这条边界提前写清楚，后续实现很容易把“高价值外部调研结果”误归类为普通问答回显，从而损失长期价值。

### 2.4 当前去重只覆盖主动写入，不覆盖 post-action 提炼记忆

仓库当前已有的去重主要体现在：

1. `WriteMemories` 直写路径的 `dedupe_hash` 软幂等；
2. `PreCheck` 召回阶段的“相似度阈值 + 第二层 LLM 采纳”。

但 `PostAction` 的 `memory_nodes` 目前没有一条对应的“高相似候选复核”流程：

1. 只会加载当前 session 的 `active_memory_nodes` 给 `analyze_turn` 做提示；
2. 不会按更广的共享作用域做统一向量/BM25 召回；
3. 不会把“一批新候选 + 每条候选对应的高相似旧记忆”合并成一次批量 LLM 判重；
4. 因此会出现“同义旧记忆已存在，但新 turn 又生成一条近重复新记忆”的问题。

### 2.5 去重搜寻空间必须与记忆召回空间保持对等

当前 `PreCheck` 已有明确的检索作用域策略：

- `team`
- `space`
- `project`

默认值为 `space`，并通过 `ScopeOverride` 进入统一 `MemoryUseCase.Search`。

本轮去重必须遵守以下边界：

1. 不能把 post-action 判重范围写死为 `project`；
2. 必须与 `PreCheck` 的有效检索作用域保持对等；
3. 这里复用的是“共享作用域边界策略”，而不是 compact 边界裁剪语义；
4. 换言之，判重时要看当前有效共享范围内的长期记忆，而不是只看当前 session 或只看当前 project。

## 3. 方案设计

### 3.1 第一层：在 `analyze_turn` 中增加“问答型回显准入判断”

不建议只继续堆叠自然语言提示词规则，而是把“是否允许入库”做成结构化输出，至少覆盖以下判定维度：

1. 当前用户输入类型：
   - `question`
   - `statement`
   - `mixed`
2. 每条新候选的事实来源：
   - `user_asserted`
   - `user_confirmed`
   - `assistant_recalled_memory`
   - `assistant_recalled_profile`
   - `assistant_general_knowledge`
   - `assistant_external_research`
   - `assistant_tool_discovered`
   - `mixed`
3. 每条候选的准入结论：
   - `keep`
   - `drop`
4. 每条候选的拒绝原因：
   - `qa_answer_only`
   - `derived_from_existing_memory`
   - `derived_from_profile_echo`
   - `general_knowledge_answer`
   - `non_durable`

本层的核心原则是：

1. 用户主动陈述、确认、纠正、补充的稳定事实，允许进入长期记忆；
2. 用户只是提问，而助手只是回答“你之前说过什么 / 项目约定是什么 / 你的偏好是什么 / 项目画像是什么”这类内容时，不允许把回答再次写成新记忆；
3. 对属于用户画像或项目画像的回答，只有在用户本轮明确确认、修正或新增时，才允许进入 `profile_nodes`；
4. 对属于一般模型知识的回答，也不能因为当前轮次出现了完整答案就反向写成项目记忆；
5. 如果答案来自非平凡的外部检索、资料查询、网站访问、工具发现或多源归纳，并形成了新的高价值结论，则即使当前轮次表现为“用户提问 -> 助手回答”，也允许进入长期记忆；
6. 这类放行要与普通“回显已有记忆/画像”严格区分，不能借由宽松定义把所有问答型内容重新放开；
7. 放行的外部检索结果必须具备长期业务价值或持久性，例如技术文档摘要、项目背景调研、方案比较结论；临时性状态，如实时天气、系统当前负载、一次性监控值，仍应以 `non_durable` 原因拒绝。

### 3.2 第二层：优先采用“统一 reviewer”同时处理 memory 与 profile 判断

当同一轮首轮提炼后同时存在 `memory_nodes` 与 `profile_nodes` 候选时，优先采用一个统一 reviewer 完成后置判断，而不是拆成两次独立 LLM 调用。

统一 reviewer 的输入建议包含：

1. 当前 turn 的结构化摘要与首轮准入标记；
2. 新 `memory_nodes` 候选；
3. 每条 memory 候选对应的高相似旧记忆列表；
4. 当前活跃 user/project profile 节点；
5. 新 `profile_nodes` 候选；
6. 候选来源中的“是否来自高成本外部检索/工具发现”标记。

统一 reviewer 的输出建议包含：

1. memory 侧：
   - `keep_candidate_indexes`
   - `drop_candidate_indexes`
   - `reason`
2. profile 侧：
   - 延续现有 user/project 分块结构；
   - `accepted_candidates`
   - `invalid_candidate_indexes`
   - `retire_only_node_ids`

执行原则：

1. 若 memory 与 profile 都存在，走一次统一 reviewer；
2. 若只存在其中一侧，也优先复用同一 scene 的单侧输入；
3. 若候选被标记为 `assistant_external_research / assistant_tool_discovered`，reviewer 需要额外判断其是否属于“新获得的高价值信息”，而不是被普通问答回显规则直接排除；
4. 仅在必要时才保留旧的独立 reviewer 作为降级/兼容兜底，而不是主路径默认双调用。

### 3.3 第二层中的 memory 分支：新增 post-action 批量记忆判重评审

对第一层仍然判定为 `keep` 的 `memory_nodes`，新增一条类似画像评审的批量判重链路：

1. 以同一轮全部新记忆候选为输入；
2. 为每条候选构造统一检索 query，优先使用：
   - `abstract`
   - 当 `details` 与 `abstract` 差异明显时，补充 `details`
3. 通过统一记忆检索能力执行向量 + BM25 混合召回；
4. 只保留相似度极高的候选进入批量复核，阈值建议：
   - 默认 `0.90`
   - 且不能低于当前 `precheck` 的 `min_similarity_score`
5. 将“多条新候选 + 每条候选对应的多条高相似旧记忆”一次性提交给 LLM；
6. LLM 按 `candidate_index` 分别给出 `keep / drop` 决策与原因；
7. 只有最终保留的候选才进入 `ApplyTurnAnalysis`。

### 3.4 判重搜寻空间策略

判重检索作用域采用与 `PreCheck` 对等的共享空间策略：

1. 直接复用现有 `team / space / project` 规范化逻辑；
2. 不新增一套与 `PreCheck` 分叉的独立作用域枚举；
3. 应用装配时由 `PostAction` 复用 `cfg.PreCheck.SearchScope`；
4. 判重逻辑只复用共享范围，不复用 `PreCheck` 的 compact 边界裁剪；
5. 即：
   - `team`：在 team 共享层内判重；
   - `space`：在当前 space 共享层内判重；
   - `project`：仅在当前 project 共享层内判重。

### 3.5 结构设计建议

为避免让 `PostActionUseCase` 直接耦合到另一个完整用例，建议围绕“统一 reviewer + 统一检索准备”新增窄接口，而不是直接把 `MemoryUseCase` 硬塞进来：

1. 新增 post-action 专用的候选检索端口，例如：
   - `PostActionMemorySearcher`
2. 新增 post-action 统一候选评审端口，例如：
   - `PostActionCandidateReviewer`
3. 由应用装配层负责把统一记忆检索能力与新的 reviewer 处理器接入 `PostActionUseCase`。

这样可以保持：

1. `PostActionUseCase` 只依赖“检索候选”和“评审决策”两个窄能力；
2. memory 与 profile 尽量复用同一份 reviewer 上下文与一次模型调用；
3. 不把完整 memory RPC 用例逻辑反向耦合进 post-action；
4. 不破坏当前仓库强调的依赖方向。

### 3.6 本轮不做的事情

为控制风险，本轮计划不扩大到以下内容：

1. 不改数据库 schema；
2. 不把“高相似旧记忆”自动标记为 `superseded`；
3. 不在本轮引入跨 `team/space/project` 的新共享级别；
4. 不改变 `PreCheck` 本身的召回行为，只复用其作用域策略；
5. 不把所有重复判断都改成规则引擎，仍以“高相似召回 + LLM 批量判定”为主；
6. 不把“高价值外部检索结果”简单等同于所有工具调用结果，仍需明确高价值与持久性判断。

## 4. 详细执行步骤

1. 扩展 `TurnAnalysis` 输入/输出契约：
   - 为首轮提炼增加“用户输入类型、候选事实来源、候选准入结论、拒绝原因”等结构化字段；
   - 增加“是否来自高成本外部检索/工具发现”的来源标记；
   - 同步更新 `analyze_turn` prompt、渲染器和解析器。
2. 在 `PostActionUseCase.applyImmediateTurnAnalysis` 中新增“首轮准入过滤”：
   - 先过滤掉被判定为问答型回显或一般知识回显的 `memory_nodes / profile_nodes`；
   - 保留明确来自用户陈述、用户确认，或高价值外部检索结果的候选。
3. 新增 post-action 统一候选评审处理器：
   - 优先在一个 reviewer 调用里同时处理 memory/profile；
   - 当 memory 存在时，先准备高相似旧记忆候选；
   - reviewer 返回 memory keep/drop 与 profile accept/invalid/supersede 决策。
4. 在统一 reviewer 的 memory 分支中补齐批量记忆判重：
   - 构造批量 query；
   - 召回每条新候选的高相似旧记忆；
   - 组织单次 LLM 评审请求；
   - 输出逐候选保留/丢弃结果。
5. 复用 `PreCheck` 作用域策略：
   - 统一使用 `team / space / project`；
   - 装配层把 `cfg.PreCheck.SearchScope` 传入 post-action dedupe 配置；
   - 不新增第二套相互独立的 scope 配置来源。
6. 确保最终落库原子性：
   - `ApplyTurnAnalysis` 在最终写入时，需要把筛选后的 `memory_nodes` 与 `profile_nodes` 变更包裹在同一个数据库事务中；
   - 保证统一 reviewer 的联合决策要么一起成功落库，要么一起回滚，避免 memory/profile 出现“脑裂”。
7. 调整应用装配与测试桩：
   - 补齐 `app.go`、`postaction_test.go` 和相关 stub；
   - 确保无 reviewer、无 searcher、空候选等降级路径行为可控。
8. 增加可观测性与指标输出：
   - 为 post-action 记录 `raw_candidates`、`final_stored_nodes`、`compaction_rate`；
   - 明确区分“问答回显压缩”“高重复压缩”“高价值外部结果放行”三类原因；
   - 便于后续对策略误杀或漏放进行观测。
9. 更新文档：
   - `docs/post-action-guide_CN.md`
   - 如涉及行为边界描述，再检查 `README.md`
10. 执行测试：
   - 至少执行仓库要求的最小测试集合；
   - 完成后执行 `go test ./...`。

## 5. 预计影响范围

预计会涉及但不限于以下文件：

- `internal/logic/processor/turn_analyzer.go`
- `internal/logic/processor/render.go`
- `internal/logic/domain/turn_analysis.go`
- `internal/app/usecase/postaction.go`
- `internal/app/app.go`
- `internal/platform/logx/*`（若指标最终走现有日志观测）
- `configs/prompts/default/analyze_turn.md`
- `configs/prompts/qwen3.5-base/analyze_turn.md`
- `configs/prompts/qwen3.5-flash/analyze_turn.md`
- 新增 post-action 记忆批量评审处理器与对应 prompt
- `docs/post-action-guide_CN.md`

## 6. 技术策略与关键细节

### 6.1 为什么不能只靠 `active_memory_nodes`

当前 `active_memory_nodes` 只覆盖当前 session 的活跃记忆，无法满足以下场景：

1. 同一个 project 或 space 里，历史 session 已有同义记忆；
2. 用户级共享记忆已经存在，但当前 project 新会话又重复提炼；
3. 需要根据 `PreCheck` 当前共享边界来决定“这条事实是否已经在有效作用域内存在”。

因此必须补一条“按共享作用域统一检索”的复核路径。

### 6.2 为什么不能逐条 LLM 判重

逐条调用会带来三个问题：

1. 请求次数多，延迟与成本线性放大；
2. 同轮多条候选之间的相互重复关系无法整体感知；
3. 很难保证最终保留集合的全局一致性。

因此应采用“单轮全部候选一次批量评审”的设计。

### 6.3 为什么要给“高价值外部检索结果”单独放行

这类结果与普通问答回显的本质差异在于：

1. 它不是对已有记忆/画像的简单复述；
2. 它也不是模型凭常识直接回答；
3. 它往往依赖额外成本较高的检索、网站访问、资料比对或工具执行；
4. 最终沉淀的是“新获得的信息资产”，而不是“旧信息的再表达”；
5. 但这条绿灯仍受“持久性 / 长期价值”约束，若结果只是实时天气、瞬时系统状态、一次性监控值等临时态数据，仍应以 `non_durable` 拒绝，而不是因为检索成本高就自动放行。

因此本轮规则必须是：

1. 压缩“回显型噪音”；
2. 保留“外部获取且具有持久价值的新信息”。

### 6.4 为什么 memory/profile 判断尽量共用一次 reviewer

统一 reviewer 的价值在于：

1. 同一轮语义只需要建立一次判断上下文；
2. memory 与 profile 可以共享“这是用户声明还是助手回显”的基础结论；
3. 能减少 post-action 链路中的额外模型调用次数；
4. 可以降低“memory 认为应丢弃，但 profile 认为应接纳”这类跨 reviewer 不一致风险。

### 6.5 为什么首轮和二轮都要做

两层解决的是不同问题：

1. 第一层解决“这是不是本来就不该入库的问答型回显”；
2. 第二层解决“这条内容虽然可入库，但与已有长期记忆是否几乎同义，以及 profile 是否真的属于稳定画像”。

如果只做第二层，会把大量本不该进入长期记忆的问题回答也送去判重，增加无意义计算；
如果只做第一层，高重复旧记忆仍然会继续积累。

### 6.6 语义压缩率指标定义

建议新增监控指标：

`Compaction Rate = (Raw_Candidates - Final_Stored_Nodes) / Raw_Candidates`

其中：

1. `Raw_Candidates`：首轮 `analyze_turn` 输出、且通过基础结构校验的原始候选总数；
2. `Final_Stored_Nodes`：经过问答准入过滤、统一 reviewer 判断和高重复判重后，最终实际落库的 `memory_nodes + profile_nodes` 总数。

指标用途：

1. 衡量问答回显与高重复内容被成功压缩的比例；
2. 辅助发现规则过松导致噪音残留，或规则过严导致有效信息损失；
3. 用于回归测试中观察优化是否真正生效。

建议口径：

1. 在典型“问答回显”测试用例中，压缩率应接近 `100%`；
2. 在“高价值外部检索结果”测试用例中，压缩率不应机械追求高值，而应以“保留高价值结果、压缩重复和回显噪音”为准。

## 7. 验收标准

1. 当用户只是询问“项目约定 / 用户偏好 / 画像内容 / 历史记忆内容”且助手只是基于已有信息回答时，不会新增 `memory_nodes` 或 `profile_nodes`；
2. 当用户在提问后明确确认、补充或修正事实时，相关稳定信息仍可正常入库；
3. 当答案属于“高成本外部检索 / 网站访问 / 工具发现 / 多源资料归纳”后形成的新高价值信息时，即使当前轮次表现为问答或指令反馈，也允许正常入库；
4. 当一轮提炼产生多条记忆候选时，会按统一批量流程做高相似旧记忆召回与 LLM 判重，而不是逐条单独调用；
5. 当一轮同时存在 memory/profile 候选时，主路径会尽可能复用同一个 reviewer LLM 调用完成判断；
6. 判重检索范围与 `PreCheck` 当前共享作用域保持对等，不会被硬编码为仅 `project`；
7. 高相似重复候选会被稳定丢弃，低相似但真正新增的信息仍能保留；
8. 在典型“问答回显”测试用例中，`Compaction Rate = (Raw_Candidates - Final_Stored_Nodes) / Raw_Candidates` 应接近 `100%`；
9. 在“高价值外部检索结果”测试用例中，不会因问答型入口而被整体误杀；
10. 统一 reviewer 产出的最终 `memory/profile` 保留结果，在 `ApplyTurnAnalysis` 中会通过同一个数据库事务原子落库，不会出现一侧成功、另一侧失败的分裂状态；
11. 相关单元测试与集成测试通过；
12. `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config` 通过；
13. `go test ./...` 通过。

## 执行变更总结

### 1. 核心修复与调整概述

本次已完成 `PostAction` 的两层噪音压缩改造：

1. 在首轮 `analyze_turn` 输出中增加 `user_input_kind / evidence_source / admission / admission_reason`，让 LLM 对“问答回显是否应继续入库”做结构化判断；
2. 在 `PostActionUseCase` 中新增首轮准入过滤，先压掉问答型回显、画像回显、通识回答和 `non_durable` 的临时外部状态；
3. 新增统一 `review_postaction_candidates` reviewer，把 memory 高重复判重与 profile 准入合并到一次 LLM 调用中执行；
4. 对 post-action 记忆判重补齐“与 `PreCheck` 对等作用域”的高相似召回，默认复用 `space`，支持 `team / project`；
5. 为最终日志增加 `raw_candidates / final_stored_nodes / compaction_rate / admission_drop_count / review_drop_count / external_research_kept_count` 观测字段；
6. 同步更新提示词、中文文档和自动化测试，完成最小测试集与全量 `go test ./...` 回归。

### 2. 📂文件变更清单

新增文件：

1. `internal/logic/domain/postaction_review.go`
2. `internal/logic/processor/postaction_candidate_reviewer.go`
3. `internal/logic/processor/postaction_candidate_reviewer_test.go`
4. `internal/app/usecase/postaction_candidate_review.go`
5. `internal/app/usecase/postaction_candidate_review_test.go`
6. `configs/prompts/default/review_postaction_candidates.md`
7. `configs/prompts/qwen3.5-base/review_postaction_candidates.md`
8. `configs/prompts/qwen3.5-flash/review_postaction_candidates.md`

修改文件：

1. `internal/logic/domain/turn_analysis.go`
2. `internal/logic/processor/turn_analyzer.go`
3. `internal/logic/processor/turn_analyzer_test.go`
4. `internal/app/usecase/postaction.go`
5. `internal/app/usecase/postaction_profiles.go`
6. `internal/app/usecase/postaction_test.go`
7. `internal/app/app.go`
8. `configs/prompts/default/analyze_turn.md`
9. `configs/prompts/qwen3.5-base/analyze_turn.md`
10. `configs/prompts/qwen3.5-flash/analyze_turn.md`
11. `docs/post-action-guide_CN.md`
12. `README.md`

删除文件：

1. 无

### 3. 💻关键代码调整详情

1. 扩展 `TurnAnalysis` 契约：
   - 新增用户输入类型枚举；
   - 新增候选证据来源枚举；
   - 新增首轮准入枚举与拒绝原因枚举；
   - 同步为 memory/profile 候选增加对应字段与校验逻辑。
2. `TurnAnalyzer`：
   - 解析 `user_input_kind`；
   - 解析 memory/profile 的 `evidence_source / admission / admission_reason`；
   - 缺失关键准入字段时直接判定为非法输出，不再保留旧格式兼容默认值。
3. `PostActionUseCase`：
   - 注入 `PostActionMemorySearcher` 与 `PostActionCandidateReviewer`；
   - 在 `applyImmediateTurnAnalysis` 中新增首轮准入过滤与统一 reviewer 主路径；
   - 复用 `cfg.PreCheck.SearchScope` 与相似度阈值进行高相似旧记忆召回；
   - 记录 compaction 相关日志指标；
   - 移除 profile-only reviewer 降级兜底与旧兼容归一路径，缺少统一 reviewer / dedupe searcher 时直接失败。
4. 统一 reviewer：
   - 新增 `review_postaction_candidates` 场景；
   - 一次请求同时携带 memory 相似旧记忆、user/project 活跃画像节点和新候选；
   - 严格要求 memory 候选在 accepted/dropped 中被且仅被分类一次。
5. 测试补齐：
   - 新增首轮准入过滤测试；
   - 新增 shared-scope dedupe 测试；
   - 新增 unified reviewer 单次调用测试；
   - 新增 compaction 日志字段测试；
   - 新增 unified reviewer 处理器请求/响应测试。

### 4. ⚠️遗留问题与注意事项

1. `review_profile_nodes` 旧 post-action reviewer、对应 prompt 文件以及兼容降级路径已全部移除；当前唯一主路径是统一 `review_postaction_candidates` reviewer。
2. `RequiredScenes` 已强制要求 `review_postaction_candidates.md`；系统配置与 `output/configs` 已同步，缺少该场景的 prompt bundle 会在启动时直接失败。
3. memory 与 profile 的最终原子写回仍依赖 `ApplyTurnAnalysis` 存储实现保持单事务提交；当前主链已明确按这一约束执行。
4. 已执行并通过：
   - `.\make.ps1 build`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
