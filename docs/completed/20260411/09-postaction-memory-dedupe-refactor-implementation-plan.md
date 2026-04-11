# 任务目标

在已经确认 `postaction` 当前真实执行链路的前提下，形成一份可以直接指导编码实施的“具体修改方案”。本方案不再停留在原则层，而是细化到：

1. 要改哪些职责边界。
2. 每个阶段改哪些文件与函数。
3. 哪些字段立即移除，哪些字段先保留兼容。
4. 哪些测试要删改，哪些回归用例必须新增。
5. 实施顺序如何安排，才能在大改过程中保持系统可编译、可验证、可回退。

# 现状与改造目标

## 当前现状

当前 `postaction` 链路中，L1 既承担“当前轮候选提炼”，又被要求基于 `active_memory_nodes` 做 whole-session 级旧记忆重复 / 覆盖判断。

但从实际代码看：

1. L1 之后已经存在更强的相似记忆检索链路：
   - query embedding
   - 向量检索
   - hybrid / BM25
   - hard dedupe
   - L2 联合评审
2. 最终真正生效的 keep / drop / supersede 结果，本来就主要依赖检索层与 L2。
3. `active_memory_nodes` 当前又是全量 session 活跃记忆，不是最近几轮，且没有 prompt 预算裁剪。

因此，当前系统的主要问题不是“缺少重复判断能力”，而是“L1 输入过重且与后续检索 / L2 职责重复”。

## 改造目标

本次重构的明确目标是：

1. L1 只负责“针对 `target_turn` 提炼候选 + 给出首轮粗过滤信号”。
2. L1 不再承担基于 whole-session 记忆池的重复 / supersede 主判断职责。
3. 检索层 + L2 成为旧记忆重复、替代、覆盖的唯一主裁判。
4. `recent_grpc_memory_writes` 的绝对排斥能力继续保留。
5. 通过移除 L1 的 `active_memory_nodes` 注入，消除长生命周期 session 导致的 prompt 膨胀风险。

# 具体实施方案

## 第一阶段：先完成职责切割，不动数据库 schema

这一阶段不修改表结构，只调整运行时输入、prompt 与校验逻辑。

### 1.1 收缩领域输入模型

目标：

1. `TurnAnalysisInput` 不再把 `ActiveMemoryNodes` 作为 L1 必备输入。
2. 保留 `ReferenceTurns`
3. 保留 `TargetTurn`
4. 保留 `RecentGRPCMemoryWrites`

涉及文件：

1. `internal/logic/domain/turn_analysis.go`

具体改法：

1. 从 `TurnAnalysisInput` 中删除 `ActiveMemoryNodes` 字段。
2. 保留 `TurnAnalysisActiveMemoryNode` 类型暂不立即删除，进入过渡兼容期。
3. `TurnAnalysis` 中保留：
   - `MemoryNodes`
   - `ProfileNodes`
   - `SupersededMemoryIDs`
4. `MemoryNodeCandidate.SupersedeMemoryIDs` 保留。

原因：

1. 当前最终写库和 L2 合并逻辑仍使用 `SupersedeMemoryIDs`。
2. 先切断 L1 输入，再决定是否彻底删除中间类型，风险最低。

### 1.2 收缩 L1 输入组装

目标：

1. `buildTurnAnalysisInput` 不再加载 `LoadActiveSessionMemoryNodes`
2. L1 只接收近因上下文与直写排斥信息

涉及文件：

1. `internal/app/usecase/postaction_analysis.go`

具体改法：

1. 删除：
   - `activeMemoryNodes, err := u.store.LoadActiveSessionMemoryNodes(...)`
2. 删除 `input.ActiveMemoryNodes` 的填充逻辑。
3. 保留：
   - `LoadRecentSessionHistory`
   - `LoadRecentDirectMemoryWrites`
4. 调整日志字段：
   - `active_memory_count` 不再记录或改成固定 `0`

原因：

1. 这是切断 L1 prompt 膨胀的核心一步。
2. 不改这一处，后面 prompt 改了也无法真正降载。

### 1.3 收缩 L1 请求渲染

目标：

1. `renderTurnAnalysisRequest` 不再序列化 `active_memory_nodes`
2. `renderTurnAnalysisSystemPrompt` 不再渲染 `ACTIVE_MEMORY_RULE`

涉及文件：

1. `internal/logic/processor/render.go`

具体改法：

1. 删除 request body 中的：
   - `ActiveMemoryNodes []activeMemoryNodeInput`
2. 删除 `activeMemoryNodeInput` 本地结构。
3. 删除 `input.ActiveMemoryNodes` 的遍历与 JSON 输出。
4. 删除 `ACTIVE_MEMORY_RULE` 的替换逻辑。
5. 保留：
   - `REFERENCE_RULE`
   - `DIRECT_WRITE_EXCLUSION_RULE`

原因：

1. 仅删除领域字段还不够，渲染层必须同步切掉。
2. 这一步完成后，L1 prompt 才会真正变小。

### 1.4 收缩 L1 prompt 职责

目标：

1. `postaction_l1_main` 不再负责 session 级重复 / supersede 判断
2. L1 prompt 只聚焦“当前轮候选提炼”

涉及文件：

1. `configs/prompts/default_cn/postaction_l1_main.md`
2. `configs/prompts/default_en/postaction_l1_main.md`

具体改法：

1. Input 说明里删除：
   - `active_memory_nodes`
2. Task 说明里删除：
   - “如果 target_turn 明确覆盖、推翻或使旧记忆失效，则通过 superseded_memory_ids 返回对应 memory_id”
3. Constraints 里删除：
   - `superseded_memory_ids` 只能来自 `active_memory_nodes`
   - 对 `active_memory_nodes` 的任何依赖描述
4. Output Format 中：
   - 可以暂时保留 `superseded_memory_ids` 字段，但示例改为空数组
   - 明确说明默认应返回空数组

原因：

1. 先把 L1 职责彻底降回“提炼器”。
2. 保留空字段兼容，比直接删 key 更稳。

## 第二阶段：调整 L1 校验逻辑，切断对 active-memory 的硬依赖

### 2.1 调整 `validateTurnAnalysis`

目标：

1. 不再要求 L1 的 supersede id 来自输入里的 active memory。
2. 将 L1 的 `SupersededMemoryIDs` 视为兼容保留字段，而非主判断输出。

涉及文件：

1. `internal/app/usecase/postaction_analysis.go`

具体改法：

1. 删除构建 `activeMemoryIDs` 的校验逻辑。
2. 删除：
   - `memory_nodes[].supersede_memory_ids` 必须出现在 activeMemoryIDs
   - `superseded_memory_ids` 必须出现在 activeMemoryIDs
3. 新约束改为：
   - 若 L1 仍输出 `SupersedeMemoryIDs` / `SupersededMemoryIDs`，只做去重与非零归一
   - 不再作为强约束校验失败来源

原因：

1. 如果不改校验，L1 去掉 `active_memory_nodes` 后会立刻和现有契约冲突。
2. 这是本次职责切割中最关键的代码位之一。

### 2.2 明确 L1 supersede 字段的兼容策略

目标：

1. 让 L1 supersede 字段进入“兼容可读、默认不用”的状态。

策略：

1. 第一版改造不删除解析字段：
   - `parseTurnAnalysisResponse` 继续支持 `memory_nodes[].supersede_memory_ids`
   - 继续支持顶层 `superseded_memory_ids`
2. 但 prompt 不再要求模型输出这些字段。
3. merge 逻辑继续接受这些字段，以便回滚时可以快速恢复旧行为。

原因：

1. 大改时保留向后兼容解析最安全。
2. 这能避免一次性拆太多导致连锁回归。

## 第三阶段：强化“检索层 + L2”作为唯一主裁判

### 3.1 不改现有检索主链，只明确其权威性

目标：

1. 保持当前 `buildPostActionMemoryReviewCandidates` / `MemoryUseCase.Search` / hard dedupe / L2 路径不变。
2. 明确它才是 session 级历史重复与替代的唯一主判断链。

涉及文件：

1. `configs/prompts/default_cn/postaction_l2_main.md`
2. `configs/prompts/default_en/postaction_l2_main.md`
3. `internal/app/usecase/postaction_candidate_review.go`

具体改法：

1. 在 L2 prompt 中强化：
   - 新记忆是否重复，只看该候选自己的 `similar_memories`
   - supersede / dedupe 的判断以 `similar_memories` 为准
2. 在文档和注释中明确：
   - L2 是“旧记忆重复 / 替代”的最终主裁判

原因：

1. 当前系统本来就已经这样工作，只是 L1 prompt 还在背部分旧职责。
2. 这一步属于“让契约描述和代码现实重新对齐”。

### 3.2 保持 hard dedupe 位置不变

目标：

1. 不把 hard dedupe 前移或后移。

理由：

1. 当前 hard dedupe 在 reviewer 前执行，能直接拦掉极高重复候选，节省 L2 token。
2. 这部分与 L1 去职责没有冲突，应该保留。

## 第四阶段：校准 supersede 合并逻辑

### 4.1 明确最终 supersede 集合来源

目标：

1. 最终 `analysis.SupersededMemoryIDs` 以 L2 接纳结果合并为主。

涉及文件：

1. `internal/app/usecase/postaction_candidate_review.go`

重点函数：

1. `mergePostActionSupersededMemoryIDs`
2. `reconcilePostActionSupersededMemoryIDsAfterMemoryFilter`
3. `collectAcceptedPostActionCandidateSupersededMemoryIDs`

具体改法：

1. 保留当前 L2 -> accepted candidates -> `supersede_memory_ids` 合并路径。
2. 重新审视 `existing []uint64` 的来源：
   - 在 L1 不再承担 supersede 主职责后，`existing` 只应作为兼容输入
   - 正常新链路下，最终 supersede 主要来自 L2 accepted candidates
3. 确认以下场景仍正确：
   - L2 accept 且 supersede -> 正确退役旧记忆
   - L2 accept 但不 supersede -> 不误退役
   - L2 drop -> 不误退役

原因：

1. 真正影响数据库里旧记忆状态的就是这里。
2. 如果这段没校准，前面的职责重构就可能失去意义。

## 第五阶段：端口与存储层处理策略

### 5.1 第一版不立即删除 `LoadActiveSessionMemoryNodes` 端口

目标：

1. 降低本次改动 blast radius。

涉及文件：

1. `internal/app/ports/ports_memory.go`

策略：

1. 第一版保留 `RelationalStore.LoadActiveSessionMemoryNodes`
2. 只是不再由 `buildTurnAnalysisInput` 调用
3. 等重构稳定后，再单独评估是否彻底删除：
   - 端口方法
   - SQLite / Postgres 实现
   - 测试桩

原因：

1. 当前该方法仍被测试桩和少量说明文本引用。
2. 先停用、后清理，比一轮里同时删接口和逻辑更稳。

### 5.2 存储层 SQL 本轮不动

目标：

1. 不在第一轮重构里修改 `LoadActiveSessionMemoryNodes` 的 SQL 语义。

原因：

1. 本轮问题不是 SQL 错，而是这批数据不该继续喂给 L1。
2. 停用比重写查询更直接，也更可控。

# 具体文件级改动清单

## 必改文件

1. `internal/logic/domain/turn_analysis.go`
2. `internal/app/usecase/postaction_analysis.go`
3. `internal/logic/processor/render.go`
4. `configs/prompts/default_cn/postaction_l1_main.md`
5. `configs/prompts/default_en/postaction_l1_main.md`
6. `configs/prompts/default_cn/postaction_l2_main.md`
7. `configs/prompts/default_en/postaction_l2_main.md`
8. `internal/app/usecase/postaction_candidate_review.go`

## 高概率要同步修改的测试文件

1. `internal/app/usecase/postaction_test.go`
2. `internal/app/usecase/postaction_candidate_review_test.go`
3. `internal/logic/processor/turn_analyzer_test.go`

## 高概率要同步修改的文档文件

1. `README.md`
2. `docs/post-action-guide_CN.md`

# 测试修改方案

## 需要删除或改写的旧断言

### 1. `postaction_test.go`

当前存在直接断言 `input.ActiveMemoryNodes` 的测试，需要改写：

1. `TestBuildTurnAnalysisInputReusesScrubbedStoredData`
   - 目前断言 active memory anchor 会进入输入
   - 改成只断言：
     - `ReferenceTurns`
     - `TargetTurn`
     - `RecentGRPCMemoryWrites`
2. `TestPostActionUseCaseProcessesQueuedTurnsAsynchronously`
   - 目前断言 analyzer 收到了 active memory anchor
   - 改成断言 analyzer 不再收到 active memory，但后续仍可借助检索 / reviewer 完成 supersede

### 2. `turn_analyzer_test.go`

1. `renderTurnAnalysisRequest` 相关测试要改：
   - 不再生成 `active_memory_nodes`
   - `ACTIVE_MEMORY_RULE` 删除后，对应 TAG 测试要同步变化

## 必须新增的回归测试

### 1. L1 输入收缩测试

新增用例目标：

1. `buildTurnAnalysisInput` 不再调用 `LoadActiveSessionMemoryNodes`
2. `renderTurnAnalysisRequest` 输出中不包含 `active_memory_nodes`

### 2. L1 兼容 supersede 空输出测试

新增用例目标：

1. 当 L1 返回空 `superseded_memory_ids` 时，整条链路仍正常。
2. 后续由 L2 提供 supersede 仍能成功退役旧记忆。

### 3. 检索 + L2 主裁判测试

新增用例目标：

1. L1 不再看到 active memories
2. 仍能通过 `similar_memories` + L2：
   - drop 重复候选
   - accept 新候选
   - supersede 旧记忆

### 4. 长 session 输入收敛测试

新增用例目标：

1. 即使 store 里存在大量 active session memory nodes
2. L1 输入大小也不再随着这些节点增长

# 实施顺序

## Step 1

先改领域模型与输入构造：

1. `TurnAnalysisInput`
2. `buildTurnAnalysisInput`
3. `renderTurnAnalysisRequest`

要求：

1. 先让代码层不再向 L1 传 active memory
2. 保持可编译

## Step 2

再改 prompt：

1. `postaction_l1_main`
2. `postaction_l2_main`

要求：

1. prompt 契约和代码输入保持一致

## Step 3

再改校验与合并逻辑：

1. `validateTurnAnalysis`
2. supersede merge 相关函数

要求：

1. 切断 L1 对 active memory 的强依赖
2. 确保最终退役结果仍正确

## Step 4

最后改测试与文档：

1. 重写旧断言
2. 补新增回归用例
3. 更新文档说明职责边界

# 验收标准

## 功能标准

1. `postaction_l1_main` 请求体中不再出现 `active_memory_nodes`
2. L1 仍能正确产出 `details / memory_nodes / profile_nodes`
3. `recent_grpc_memory_writes` 的绝对排斥逻辑不回归
4. 检索层 + L2 仍能正确完成：
   - dedupe
   - `dedupe_memory_id`
   - `supersede_memory_ids`
5. 旧记忆 supersede 后状态与向量清理仍正确
6. 画像评审链路不回归

## 测试标准

最少通过：

1. `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`

作为大改，最终还必须通过：

1. `go test ./...`
2. `.\make.ps1 build`

# 风险控制与中间态策略

## 中间态策略

本次改造采用“先停用，后清理”的策略：

1. 先停止 L1 使用 `active_memory_nodes`
2. 先停止 prompt 暴露该字段
3. 暂不立刻删除所有相关类型、端口与解析字段
4. 等主链稳定后，再做第二轮清理

## 这样做的原因

1. 能显著降低一次性大删字段导致的编译与回归风险。
2. 能把本次真正核心的收益先拿到：
   - prompt 收敛
   - 职责切割
   - 逻辑去重

# 推荐实施拆分

建议实际编码时拆成两个提交：

## 提交一

主题：

1. 收缩 L1 输入与 prompt
2. 调整校验逻辑
3. 让主链重新稳定通过测试

## 提交二

主题：

1. 清理遗留注释 / 文档 / 可选兼容结构
2. 追加长 session 收敛测试
3. 补足职责边界文档

这样做的好处是：

1. 第一提交先拿到主要行为变化。
2. 第二提交再做收尾清理，便于回滚与定位问题。

# 执行变更总结

## 1. 核心修复与调整概述

本次未开始正式编码，而是基于前一份现状分析，进一步沉淀出一份“可以直接进入实施”的具体修改方案。与上一份计划相比，这次新增了：

1. 明确的分阶段改造顺序
2. 文件级修改清单
3. 测试改写与新增用例矩阵
4. 中间态兼容策略

## 2. 📂文件变更清单

新增：

1. `docs/plan/20260411-09-postaction-memory-dedupe-refactor-implementation-plan.md`

## 3. 💻关键代码调整详情

本次是实施方案设计阶段，尚未修改业务代码；但已经明确后续编码时的核心落点：

1. `TurnAnalysisInput` 从“4 类输入”收敛为“3 类输入”
2. `buildTurnAnalysisInput` 停止加载 `LoadActiveSessionMemoryNodes`
3. `renderTurnAnalysisRequest` 不再输出 `active_memory_nodes`
4. `validateTurnAnalysis` 不再对 L1 supersede 强依赖 active memory 输入
5. 最终 supersede 结果以检索层 + L2 为主

## 4. ⚠️遗留问题与注意事项

1. 当前方案尚未正式实施，工作区还未进入具体编码阶段。
2. 本次重构属于职责切割型大改，正式动手时必须一并修改 prompt、输入组装、校验、测试和文档，不能只改其中一层。
3. 为控制风险，推荐采用“先停用 active_memory_nodes 对 L1 的注入，再保留兼容结构观察一轮”的中间态策略。
