# 任务目标

在已经确认 `postaction` 当前真实执行链路之后，进一步收紧实施方案：

1. 不再为“不需要的旧职责 / 旧字段 / 旧接口”保留兼容分支。
2. 直接删除 L1 中与 whole-session 记忆去重强耦合的输入、提示词约束和解析契约。
3. 直接删除仅为旧 L1 supersede 逻辑服务的多余结构，让系统职责重新回到：
   - L1：提炼当前轮候选
   - 检索层 + hard dedupe：召回并过滤相似旧记忆
   - L2：决定 keep / drop / supersede

# 调整原则

本次方案采用“直接删掉无用物”的原则，不再保留中间态兼容：

1. 对后续主链已经没有价值的输入，不保留兼容字段。
2. 对只服务于旧职责的解析逻辑，不保留兼容解析。
3. 对已无实际调用价值的端口方法，不保留空实现或停用实现。
4. 对只验证旧链路的测试，不保留，直接重写为新职责下的测试。

这意味着本次改造会更彻底，但代码会更干净，后续维护成本更低。

# 明确保留与明确删除

## 必须保留

1. `reference_turns`
   - 作用：帮助 L1 理解当前轮的近因上下文
2. `target_turn`
   - 作用：L1 唯一允许提炼的目标 turn
3. `recent_grpc_memory_writes`
   - 作用：绝对排斥工具链已经写入的事实，防止重复提炼
4. `MemoryNodeCandidate.SupersedeMemoryIDs`
   - 作用：L2 评审结果会把 supersede id 回填到 surviving node 上，后续持久化仍需要这个字段
5. 检索层
   - query embedding
   - vector / hybrid / BM25
   - hard dedupe
6. `postaction_l2_main` 的 memory 去重 / supersede 结果

## 直接删除

1. `TurnAnalysisInput.ActiveMemoryNodes`
2. `TurnAnalysisActiveMemoryNode`
3. `RelationalStore.LoadActiveSessionMemoryNodes`
4. SQLite / Postgres 中对应的 `LoadActiveSessionMemoryNodes` 实现与说明
5. `renderTurnAnalysisRequest` 中的 `active_memory_nodes`
6. `renderTurnAnalysisSystemPrompt` 中的 `ACTIVE_MEMORY_RULE`
7. `postaction_l1_main` prompt 中关于：
   - `active_memory_nodes`
   - `superseded_memory_ids`
   - “L1 负责判断旧记忆覆盖/推翻”的全部描述
8. L1 解析契约中的：
   - 顶层 `superseded_memory_ids`
   - `memory_nodes[].supersede_memory_ids`
9. `TurnAnalysis.SupersededMemoryIDs`
10. 依赖顶层 `SupersededMemoryIDs` 的辅助逻辑和日志字段
11. 仅为了旧 L1 supersede 契约存在的测试断言

# 最终目标状态

重构完成后，`postaction` 记忆链路应该收敛为：

1. `Execute`
   - 入库 turn
   - session 入队
2. `buildTurnAnalysisInput`
   - 只加载 `reference_turns`
   - 只加载 `recent_grpc_memory_writes`
   - 不再加载任何 active memory list
3. `postaction_l1_main`
   - 只输出：
     - `user_input_kind`
     - `turn_id`
     - `details`
     - `memory_nodes`
     - `profile_nodes`
   - 不输出任何 supersede 结果
4. admission 粗过滤
5. 检索层为每个候选构建 `similar_memories`
6. hard dedupe 直接拦截极高重复项
7. `postaction_l2_main`
   - 对 surviving candidates 决定 keep / drop / supersede
8. 将 L2 返回的 `supersede_memory_ids` 回填到 surviving memory nodes
9. 持久化阶段从 surviving nodes 上直接推导最终 supersede 集合
10. 写库退役旧记忆并清理旧向量

# 具体修改方案

## 一、领域模型直接清理

涉及文件：

1. `internal/logic/domain/turn_analysis.go`

直接修改：

1. 删除 `TurnAnalysisActiveMemoryNode` 类型
2. 删除 `TurnAnalysisInput.ActiveMemoryNodes`
3. 删除 `TurnAnalysis.SupersededMemoryIDs`
4. 保留 `MemoryNodeCandidate.SupersedeMemoryIDs`

原因：

1. `TurnAnalysisActiveMemoryNode` 只为 L1 的旧去重职责服务，删除后不会影响 L2。
2. 顶层 `SupersededMemoryIDs` 已经是冗余字段，最终 supersede 完全可以从 surviving node 的 `SupersedeMemoryIDs` 推导。

## 二、输入组装直接清理

涉及文件：

1. `internal/app/usecase/postaction_analysis.go`

直接修改：

1. 删除 `LoadActiveSessionMemoryNodes` 调用
2. 删除 `input.ActiveMemoryNodes` 填充逻辑
3. 删除基于 active memory 的日志统计字段

补充调整：

1. `validateTurnAnalysis` 不再校验：
   - `memory_nodes[].supersede_memory_ids`
   - 顶层 `superseded_memory_ids`
2. `validateTurnAnalysis` 只校验：
   - `turn_id`
   - `user_input_kind`
   - `evidence_source`
   - `admission`
   - `admission_reason`

原因：

1. 这些 supersede 约束本来就是依赖 `active_memory_nodes` 才成立。
2. 当 L1 不再承担 supersede 职责时，这些校验继续存在只会制造错误耦合。

## 三、L1 渲染层直接清理

涉及文件：

1. `internal/logic/processor/render.go`

直接修改：

1. 删除 `activeMemoryNodeInput` 本地结构
2. 删除请求体中的 `ActiveMemoryNodes`
3. 删除所有 active memory JSON 渲染逻辑
4. 删除 `ACTIVE_MEMORY_RULE` 替换逻辑
5. 删除相关注释中对 active memory 的职责描述

原因：

1. 只改 domain 不改 renderer，L1 请求体仍会保持旧结构。
2. 要真正收缩 prompt，就必须把渲染层一并切干净。

## 四、L1 prompt 直接重写

涉及文件：

1. `configs/prompts/default_cn/postaction_l1_main.md`
2. `configs/prompts/default_en/postaction_l1_main.md`

直接修改：

1. Input 中删除 `active_memory_nodes`
2. Task 中删除：
   - `superseded_memory_ids`
   - 与旧记忆覆盖判断有关的任务定义
3. Constraints 中删除：
   - `superseded_memory_ids` 只能来自 `active_memory_nodes`
   - 基于旧记忆做覆盖判断的约束
4. Output Format 中直接删除：
   - `superseded_memory_ids`
5. 示例中直接删除：
   - `superseded_memory_ids`
   - `memory_nodes[].supersede_memory_ids`

新的 L1 输出契约应只保留：

1. `user_input_kind`
2. `turn_id`
3. `details`
4. `memory_nodes`
5. `profile_nodes`

原因：

1. 既然 L1 不再负责 supersede，就不该继续输出这个字段。
2. 继续保留空字段只会增加无意义 token 和后续维护负担。

## 五、L1 解析器直接清理

涉及文件：

1. `internal/logic/processor/turn_analyzer.go`
2. `internal/logic/processor/turn_analyzer_test.go`

直接修改：

1. 在 L1 JSON payload 解析结构里删除：
   - `memory_nodes[].supersede_memory_ids`
   - 顶层 `superseded_memory_ids`
2. 删除将 node-level supersede 汇总到 top-level 的逻辑
3. 删除相关单元测试：
   - `TestParseTurnAnalysisResponseMergesCandidateLocalSupersedes`
4. 重写 `TestTurnAnalyzerAnalyze`
   - 不再构造 `ActiveMemoryNodes`
   - 不再断言系统 prompt 包含 `ACTIVE_MEMORY_RULE`
   - 不再断言结果里有 `SupersededMemoryIDs`

原因：

1. 解析器必须和 prompt 新契约完全一致。
2. 不需要的字段就不应继续容忍或解析。

## 六、端口与存储层直接清理

涉及文件：

1. `internal/app/ports/ports_memory.go`
2. `internal/adapters/outbound/vldb_sqlite/store.go`
3. `internal/adapters/outbound/vldb_postgres/analysis_store.go`
4. `internal/adapters/outbound/vldb_postgres/store.go`
5. `internal/adapters/outbound/vldb_postgres/unsupported.go`
6. `internal/app/usecase/postaction_test.go`

直接修改：

1. 从 `RelationalStore` 接口删除 `LoadActiveSessionMemoryNodes`
2. 删除 SQLite 对应实现
3. 删除 Postgres 对应实现 / 代理 / unsupported 占位
4. 删除测试桩里的对应方法

原因：

1. 只要主链完全不再使用它，这个端口方法就是无用物。
2. 保留无用方法只会制造错误暗示，让以后的人误以为它还有业务职责。

## 七、顶层 supersede 合并逻辑直接收敛

涉及文件：

1. `internal/app/usecase/postaction_candidate_review.go`
2. `internal/app/usecase/postaction_analysis.go`
3. `internal/adapters/outbound/vldb_sqlite/store.go`

直接修改：

1. 让最终 supersede 集合完全从 surviving `memory_nodes[].SupersedeMemoryIDs` 推导
2. 删除对 `analysis.SupersededMemoryIDs` 的依赖
3. 删除 `reconcilePostActionSupersededMemoryIDsAfterMemoryFilter`
4. 把 `mergePostActionSupersededMemoryIDs` 改成不再接收 L1 existing supersede 集合
5. `ApplyTurnAnalysis` 改成在写库前先统一收集 surviving memory node 上的 supersede ids，再执行：
   - 标记旧记忆 superseded
   - 清理旧向量

原因：

1. 顶层 supersede 集合本来就只是 candidate-local supersede 的冗余镜像。
2. 当 L1 supersede 被删除后，这个顶层字段已经没有存在价值。

## 八、测试直接重写，不保留旧断言

重点文件：

1. `internal/app/usecase/postaction_test.go`
2. `internal/logic/processor/turn_analyzer_test.go`
3. `internal/app/usecase/postaction_candidate_review_test.go`

直接删除或重写的旧测试关注点：

1. 任何断言 analyzer 输入里包含 `ActiveMemoryNodes`
2. 任何断言 L1 输出里包含 `SupersededMemoryIDs`
3. 任何断言 renderer 会输出 `active_memory_nodes`
4. 任何断言 `ACTIVE_MEMORY_RULE` 会被注入 prompt

必须新增的新测试：

1. L1 输入只包含：
   - `reference_turns`
   - `target_turn`
   - `recent_grpc_memory_writes`
2. L1 请求体不再包含 `active_memory_nodes`
3. L1 输出不再接受 `superseded_memory_ids`
4. L2 返回的 `accepted_candidates[].supersede_memory_ids` 仍可正确：
   - 回填 surviving nodes
   - 在持久化阶段退役旧记忆
5. 长 session 下，即使 store 里存在大量 active memories，也不会再影响 L1 prompt 尺寸

## 九、文档直接对齐，不保留旧术语

涉及文件：

1. `README.md`
2. `docs/post-action-guide_CN.md`

直接修改：

1. 删除任何“L1 参考 active memory 去重 / 覆盖”的说明
2. 明确写清楚：
   - L1 只提炼当前轮候选
   - 检索层 + L2 负责历史记忆重复 / 替代判断

# 实施顺序

## 第一步

先删领域结构和输入组装：

1. `TurnAnalysisInput.ActiveMemoryNodes`
2. `TurnAnalysisActiveMemoryNode`
3. `buildTurnAnalysisInput` 中的 active memory 读取

## 第二步

再删 L1 契约：

1. prompt
2. request renderer
3. response parser
4. validator

## 第三步

再改 supersede 写库路径：

1. 让 supersede 只由 L2 surviving node 推导
2. 删除顶层冗余 supersede 字段与辅助函数

## 第四步

最后删端口 / store / 测试 / 文档中的遗留物

这样顺序的好处是：

1. 先切掉输入
2. 再切掉契约
3. 再切掉冗余合并逻辑
4. 最后统一清扫残留引用

# 验收标准

## 行为标准

1. `postaction_l1_main` 请求体不再包含 `active_memory_nodes`
2. `postaction_l1_main` 输出契约中不再存在 `superseded_memory_ids`
3. `TurnAnalysisInput` 中不再存在 `ActiveMemoryNodes`
4. `RelationalStore` 中不再存在 `LoadActiveSessionMemoryNodes`
5. 最终记忆 supersede 完全由 L2 结果驱动
6. `recent_grpc_memory_writes` 排斥逻辑不回归
7. 画像评审链路不回归

## 测试标准

最少通过：

1. `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`

大改完成前必须通过：

1. `go test ./...`
2. `.\make.ps1 build`

# 风险说明

本方案比保留兼容更激进，风险点主要有两类：

1. 一次性删除字段后，任何遗漏的调用点都会立刻暴露为编译错误。
2. 顶层 supersede 字段删除后，持久化路径必须同步改成从 surviving nodes 推导，否则会直接影响旧记忆退役。

但这类风险是“显性风险”，优点是：

1. 能更快把职责切干净
2. 不会留下后续还要专门再清一次的兼容债务

# 执行变更总结

## 1. 核心修复与调整概述

本次未进入正式编码，而是在上一版具体实施方案基础上，按照“对不需要的东西不保留兼容”的原则，进一步收紧重构方案。核心变化是：

1. 从“先停用、后清理”改为“直接删除无用输入、无用契约、无用接口”
2. 明确删除 L1 的 `active_memory_nodes` 与 `superseded_memory_ids`
3. 明确删除 `RelationalStore.LoadActiveSessionMemoryNodes` 及其实现
4. 明确删除 `TurnAnalysis.SupersededMemoryIDs`，改为由 surviving memory nodes 直接推导最终 supersede 集合

## 2. 📂文件变更清单

新增：

1. `docs/plan/20260411-10-postaction-refactor-no-compat-plan.md`

## 3. 💻关键代码调整详情

本次仍处于方案设计阶段，尚未修改业务代码；但已经把后续实施的关键删除项和重写项精确到了函数与字段级别，重点包括：

1. 删除 L1 输入中的 `ActiveMemoryNodes`
2. 删除 L1 输出中的 `superseded_memory_ids`
3. 删除与旧 L1 supersede 契约耦合的 validator / parser / tests
4. 让最终 supersede 只依赖 L2 回填到 surviving nodes 上的 `SupersedeMemoryIDs`

## 4. ⚠️遗留问题与注意事项

1. 当前仍未开始正式重构，这份文档是更严格的实施方案，不是代码变更结果。
2. 由于本次明确不保留兼容，正式实施时编译期会暴露更多残留引用，必须按计划顺序推进。
3. 如果下一步开始编码，建议直接以本方案为唯一施工依据，不再回退到“保留兼容中间态”的旧方案。
