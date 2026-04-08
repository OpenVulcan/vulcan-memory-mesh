# 任务计划：补充 PostAction 提示词对格式化调试请求的排除规则

## 任务目标

本轮需要补充 `postaction_l1_main` 提示词中的负例约束，避免模型把“原样输出、引用、排版、脚注、格式调试”这类一次性输出请求误判成可长期入库的记忆或画像。

## 执行步骤

1. 检查当前中英文 `postaction_l1_main` 提示词对 `user_asserted`、`admission_reason` 和临时输出任务的约束是否存在明显空缺。
2. 在中英文提示词中补充针对“格式化/引用/原样输出/展示既有记忆 ID”的排除规则。
3. 确保补充后的规则与现有 `qa_answer_only`、`derived_from_existing_memory`、`non_durable` 等判定不冲突。
4. 同步标准构建产物中的提示词目录，并验证构建成功。
5. 在计划文件中记录执行总结并归档。

## 技术选型与实现约束

- 本轮只修改 `postaction_l1_main` 主提示词，不改代码逻辑。
- 规则文案需要兼顾“直接返回空数组”与“必要时可输出 `admission=\"drop\"` 候选”的两种允许行为。
- 中英文提示词必须保持语义一致，避免不同语言 bundle 出现不同判定标准。

## 验收标准

1. 中英文 `postaction_l1_main` 都已明确排除格式化/引用/原样输出类临时请求。
2. 规则已明确“提及既有 memory_id / turn_id / 脚注标记不构成新记忆”。
3. 标准构建成功，`output/configs` 中的提示词副本已同步更新。

---

## 执行变更总结

### 1. 核心修复与调整概述

- 在中英文 `postaction_l1_main` 主提示词中补充了“格式化/引用/原样输出/脚注/版式调试”类临时请求的排除规则，避免模型把一次性输出任务误判成可长期入库的记忆或画像。
- 收紧了 `user_asserted` 的适用边界，明确只有长期稳定事实、长期偏好、长期约束、长期项目规则才允许走该来源判定。
- 明确规定“仅提及既有 `memory_id` / `turn_id` / 脚注标记 / 输出模板”不构成新增长期事实，并要求在这类场景下优先返回空结果或 `admission="drop"`。

### 2. 📂文件变更清单

- 修改：[D:\projects\VulcanMemoryMesh\configs\prompts\default_cn\postaction_l1_main.md](D:\projects\VulcanMemoryMesh\configs\prompts\default_cn\postaction_l1_main.md)
- 修改：[D:\projects\VulcanMemoryMesh\configs\prompts\default_en\postaction_l1_main.md](D:\projects\VulcanMemoryMesh\configs\prompts\default_en\postaction_l1_main.md)
- 修改：[D:\projects\VulcanMemoryMesh\docs\completed\20260408\12-POSTACTION_PROMPT_FORMATTING_EXCLUSION.md](D:\projects\VulcanMemoryMesh\docs\completed\20260408\12-POSTACTION_PROMPT_FORMATTING_EXCLUSION.md)

### 3. 💻关键代码调整详情

- 在中文提示词中新增临时输出任务负例，覆盖原样输出、引用、换行调整、Markdown 标记、脚注、`memory_id` / `turn_id` 展示、示例文本与显示调试等场景。
- 在英文提示词中同步新增完全等价的负例规则，确保 `default_cn` 与 `default_en` 的判定标准一致。
- 对提示词约束进行了优先级补充，要求这类临时请求通常返回空 `details` 与空节点数组；若必须显式保留候选，也只能以 `admission="drop"` 且优先 `non_durable` 的方式输出。

### 4. ⚠️遗留问题与注意事项

- 本轮只修正了 `postaction_l1_main` 的首轮分析约束，没有引入基于 `🧠` 前缀或 `[^🧠...]` 标记的句子级过滤；该部分仍建议放在下一轮单独处理。
- 提示词调整会改善这类误判，但最终效果仍依赖具体模型的枚举遵从性与指令执行稳定性，后续需要结合真实回包继续观察。
