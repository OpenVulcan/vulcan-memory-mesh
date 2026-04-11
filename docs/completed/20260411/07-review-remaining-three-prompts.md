# 任务计划：检查其余三个主提示词是否存在职责偏移或冗余约束

## 任务目标

对当前主运行时剩余三个主提示词进行系统检查：

- `postaction_l1_main`
- `postaction_l2_main`
- `profile_instruction_main`

重点判断它们是否存在以下问题：

1. 提示词职责与后台真实处理逻辑不一致；
2. 某些输出字段仅是提示词要求，但后台没有真实解析或没有业务意义；
3. 规则描述过重、过散、过于内部实现导向，可能影响模型稳定性与速度；
4. 输出格式、顺序或字段解释可能误导模型，导致判断质量下降。

## 详细执行步骤

1. 读取三个中英文 prompt 的当前内容，确认其角色、输入、规则、输出格式。
2. 对照对应的处理器、解析器、usecase 和测试，确认每个关键字段是否被真实消费。
3. 分别评估：
   - 角色描述是否贴合模型实际职责；
   - 输入描述是否存在不必要的内部术语；
   - 输出字段是否有真实后台意义；
   - 是否存在可压缩但不应丢失的关键判断锚点。
4. 形成结构化审查结论：
   - 明确指出每个 prompt 的主要问题与严重性；
   - 对没有问题或只有轻微问题的 prompt 明确说明；
   - 给出后续优化建议，但不在本轮直接修改代码。

## 技术选型与处理原则

- 本轮以“审查和结论输出”为主，不直接落地修改。
- 结论必须基于当前仓库代码真实行为，而不是只根据 prompt 文案主观判断。
- 优先关注“职责是否偏移”和“字段是否有后台意义”，因为这两类问题最容易让 prompt 看似复杂但实际无效。

## 验收标准

- 三个 prompt 都有明确审查结论。
- 每个关键字段都能说明其后台是否真实有意义。
- 若发现问题，结论中能指出具体文件与位置，并给出简明优化方向。

## 执行变更总结

### 1. 核心修复与调整概述

本轮未直接修改源码或提示词正文，而是完成了其余三个主提示词的“职责-契约-后端消费”对照审查：

- `postaction_l1_main`：整体职责与后端一致，但存在一个契约缺口：提示词只要求输出顶层 `superseded_memory_ids`，没有把后端真实支持且后续流程会利用的 `memory_nodes[].supersede_memory_ids` 明确暴露给模型，可能削弱候选级替代映射精度。
- `postaction_l2_main`：核心分类规则与解析器一致，但 `memory.reason`、`user.reason`、`project.reason` 目前只具有诊断/返回文本意义，不参与后续业务决策或持久化判断；继续强制模型生成这三段文字会增加耗时。
- `profile_instruction_main`：与后端真实作用基本一致，`level_reason`、`supersede_nodes[].reason`、`retired_nodes[].reason`、顶层 `reason` 都有实际落点；仅存在轻微可优化项，例如角色描述仍偏内部术语、纯 JSON 红线不如其他 prompt 明确。

### 2. 📂文件变更清单

新增：

- `docs/plan/20260411-07-review-remaining-three-prompts.md`（本文件，后续迁移到 completed）

修改：

- `docs/plan/20260411-07-review-remaining-three-prompts.md`（追加执行变更总结）

删除：

- 无

### 3. 💻关键代码调整详情

本轮未调整业务代码；仅进行了代码与提示词的交叉核查。关键核查结论如下：

- `postaction_l1_main` 对应解析器真实支持 `memory_nodes[].supersede_memory_ids`，但当前提示词未将该字段纳入任务说明或输出格式。
- `postaction_l2_main` 的块级 `reason` 会被解析进入结构体，但在记忆去重、画像接纳、生命周期计算、持久化写回中均不参与决策。
- `profile_instruction_main` 的 `level_reason` 会落到新画像节点的 `LevelReason`，`supersede_nodes[].reason` 的首个非空值会进入新节点 `StatusReason`，`retired_nodes[].reason` 会进入退役结果，顶层 `reason` 会返回给 RPC 的 `review_reason`。

### 4. ⚠️遗留问题与注意事项

- 若后续要优化 `postaction_l2_main` 以提速，优先目标应是收紧或移除块级 `reason` 输出，而不是动结构化判定字段。
- 若后续要优化 `postaction_l1_main` 的替代精度，应补齐 `memory_nodes[].supersede_memory_ids` 的显式提示与示例，避免只依赖顶层 `superseded_memory_ids`。
- `profile_instruction_main` 当前不建议做大幅删减；它的示例和字段说明确实承载了“部分冲突拆解、跨领域拆分、同领域聚合”这些关键判断逻辑。
