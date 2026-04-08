# 任务计划：Default Prompt 英文版落地

## 任务目标

在本轮实现中，为当前 `configs/prompts/default/` 下的默认提示词补齐一套英文版文件，并统一存放到 `configs/prompts/default_en/` 目录，确保：

1. 英文版与现有 `default` 目录保持同名文件结构一致。
2. 每份提示词的字段约束、JSON 输出契约、枚举值和关键规则不发生语义漂移。
3. 英文版只做语言层翻译与必要措辞顺滑，不引入新的业务规则。
4. 相关说明文档与任务记录同步沉淀，便于后续切换 prompt 路由时直接复用。

## 执行步骤

1. 盘点 `configs/prompts/default/` 下现有提示词文件，确认需要翻译的文件列表与主要使用场景。
2. 逐份阅读中文/当前默认提示词，提炼其结构、输出格式、枚举说明和禁止项，确保翻译时不改动契约。
3. 在 `configs/prompts/default_en/` 下创建同名英文版提示词文件，保持 Markdown 结构与示例布局一致。
4. 自检英文版内容，重点检查：
   - JSON 字段名与示例不被误改；
   - 枚举值仍使用系统要求的原始字符串；
   - 禁止项、优先级规则、输出格式要求未遗漏。
5. 将本轮变更写入计划总结，并按仓库规则归档到 `docs/completed/20260408/`。

## 技术选型与实现约束

- 本轮只新增 `default_en` 英文提示词目录，不改动现有代码路由逻辑。
- 英文版应尽量贴近当前默认提示词的业务含义，避免“自由改写”导致后续模型行为偏移。
- 保留原始 Markdown 层级、代码块、JSON 示例和字段命名。
- 若发现某些提示词中存在明显不一致或歧义，只记录在总结中，不在本轮擅自扩展业务规则。

## 验收标准

1. `configs/prompts/default_en/` 下存在与 `configs/prompts/default/` 完全对应的同名英文提示词文件。
2. 每份英文提示词都保留原有结构、字段约束、枚举规则与输出格式说明。
3. 本轮变更不修改现有 `default` 提示词内容，也不引入代码行为变更。
4. 计划文件完成总结后成功归档至 `docs/completed/20260408/`。

## 执行变更总结

### 1. 核心修复与调整概述

本轮已为默认提示词目录补齐完整英文版实现，并落在 `configs/prompts/default_en/`。处理方式以“结构镜像 + 契约保真”为原则，保持原有文件命名、Markdown 层级、JSON 输出格式、枚举原值与关键规则不变，只对说明文字、示例文案和约束描述进行了英文表达落地。

### 2. 📂文件变更清单

新增：

1. `configs/prompts/default_en/analyze_turn.md`
2. `configs/prompts/default_en/assemble_context.md`
3. `configs/prompts/default_en/extract_intent.md`
4. `configs/prompts/default_en/merge_profile.md`
5. `configs/prompts/default_en/review_postaction_candidates.md`
6. `configs/prompts/default_en/review_precheck_memory.md`
7. `configs/prompts/default_en/review_profile_instruction.md`
8. `configs/prompts/default_en/summarize_entry.md`

修改：

1. `docs/plan/20260408-09-DEFAULT_EN_PROMPTS.md`

删除：

1. 无

### 3. 💻关键代码调整详情

1. 为 `analyze_turn`、`extract_intent`、`review_postaction_candidates`、`review_profile_instruction` 等复杂提示词补齐英文版内容，完整保留动态标签、字段约束、枚举限制与输出格式。
2. 保留所有系统依赖的原始 JSON key 与 enum 值，例如 `keep`、`drop`、`user_asserted`、`question` 等，避免英文版引入契约偏移。
3. 对 `default` 与 `default_en` 两个目录的文件集合进行了逐项对齐检查，确认当前 8 份提示词文件一一对应，无缺失、无多余文件。

### 4. ⚠️遗留问题与注意事项

1. 本轮仅新增英文提示词文件，尚未改动运行时 prompt 路由选择逻辑；是否在特定语言场景自动切换到 `default_en`，需在后续实现中单独处理。
2. 英文版当前以“提示词英文化、契约不变”为目标；如果后续要针对英文模型进一步优化措辞风格或检索语句风格，建议在真实样本验证后再做定向迭代。
3. 本轮未运行代码测试，原因是本次变更仅涉及新增提示词 Markdown 文件与计划文档，不涉及 Go 代码、配置装配或运行时逻辑变更。
