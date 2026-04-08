## 任务目标

针对 post-action 的统一 reviewer 提示词进行收紧，优先修正“同 project 不同 session 的明显语义重复内容仍被接纳为新记忆”的问题。在不修改代码流程与阈值逻辑的前提下，先通过提示词强化“高相似旧记忆优先复用、非实质新增不得新建”的判定规则。

## 执行步骤

1. 审查当前 `review_postaction_candidates.md` 中与记忆去重相关的规则，定位容易让模型把“措辞更完整/总结更顺”误判为“新增稳定记忆”的表述。
2. 基于现有日志样本，补充更明确的去重规则，重点约束以下场景：
   - 已有高相似旧记忆时，默认应丢弃新候选；
   - 仅是改写、压缩、扩写、重排、并列合并、措辞润色时，不得视为新增；
   - 跨 session 但同 project 的语义重复，仍应按重复处理；
   - 只有明确新增旧记忆中不存在的稳定事实时，才允许 keep。
3. 保持提示词输出契约不变，不调整 JSON 结构与字段命名，避免影响现有解析链路。
4. 自检修改后的提示词，确认其与当前代码逻辑、日志观察结论一致，没有引入互相冲突的指令。

## 技术选型与策略

- 本次仅调整系统默认提示词文件 `configs/prompts/default/review_postaction_candidates.md`。
- 不同步调整 `analyze_turn.md`，因为当前问题在日志中已能确认旧记忆被成功召回，优先收紧最终 reviewer 的裁决规则更直接。
- 不修改运行时代码与阈值，便于单独观察提示词调整带来的行为变化。

## 验收标准

1. 提示词继续兼容当前 `review_postaction_candidates` 的输入输出格式。
2. 规则中明确要求模型在高相似旧记忆存在时优先 `drop + dedupe_memory_id`，而不是仅因“更完整表达”就 `keep`。
3. 规则中明确限制“措辞变化、结构重排、并列汇总、跨 session 重复”不能单独构成新增记忆依据。
4. 修改完成后，计划文件补充执行变更总结并归档到 `docs/completed/20260408/`。

## 执行变更总结

### 1. 核心修复与调整概述

- 已收紧 `post-action` 统一 reviewer 的记忆去重提示词，明确要求在高相似旧记忆已存在时优先执行去重，而不是因为“表达更完整”“总结更顺”就接纳为新记忆。
- 已补充对“同 project 不同 session 的重复事实”“多条旧记忆合并覆盖新候选”“外部检索但结果重复旧记忆”等场景的明确约束，降低 reviewer 将重复事实误判为新增稳定记忆的概率。

### 2. 📂文件变更清单

- 新增：`docs/plan/20260408-03-POSTACTION_PROMPT_DEDUPE_TUNING.md`
- 修改：`configs/prompts/default/review_postaction_candidates.md`
- 删除：无

### 3. 💻关键代码调整详情

- 本次未调整 Go 代码与存储逻辑，仅修改 reviewer 系统提示词。
- 在 `review_postaction_candidates.md` 中新增了“只有明确新增旧记忆中不存在的稳定事实时才可 keep”的判断门槛。
- 在 `review_postaction_candidates.md` 中新增了“措辞变化、结构重排、摘要化、跨 session 来源差异不构成新增事实”的明确反例。
- 在 `review_postaction_candidates.md` 中新增了“多条旧记忆合并即可覆盖新候选时也应 drop”的规则，并要求尽量回填 `dedupe_memory_id`。

### 4. ⚠️遗留问题与注意事项

- 本次仅调整提示词，尚未增加写库前硬排重，因此如果 reviewer 仍然误判，运行时仍可能产生新的重复记忆。
- 当前尚未增加 reviewer 输入输出的详细调试日志，后续若还出现重复写入，建议补充该链路日志以便确认模型具体裁决过程。
- 本次为 prompt-only 调整，未执行自动化测试；建议后续结合真实重复样本继续观察 `review_drop_count`、`superseded_memory_count` 与新增 memory 数量的变化。
