# 任务计划：将输出语言规则下沉到主提示词并移除统一注入

## 任务目标

本轮需要把“根据用户当前对话语言决定输出语言”的规则从服务端统一注入逻辑中移除，改为直接写入各主提示词文件本身。重点是提升提示词的可读性、可维护性和可单独优化能力，尤其要让英文提示词明确说明：如果当前 turn 问答以中文为主，输出的总结、精炼信息、原因说明等自由文本字段必须使用中文。

## 执行步骤

1. 检查当前语言规则的注入位置，以及 5 个主提示词文件是否已有等价约束。
2. 在中英文主提示词文件中分别补充显式语言规则，确保规则与各自任务场景相匹配。
3. 对英文提示词强化“中文 turn → 中文输出”的说明，避免仅靠抽象的 dominant language 表述导致模型执行不精确。
4. 移除运行时统一注入逻辑及其调用点，确保实际语言控制完全由提示词文件负责。
5. 补充或更新相关测试，验证主提示词入口不再依赖统一注入函数。
6. 执行仓库要求的最小回归测试与全量测试，并完成标准构建。
7. 在计划文件末尾追加执行变更总结，并按规范归档。

## 技术选型与实现约束

- 本轮需要同步调整 `default_cn` 与 `default_en` 两套主提示词，避免不同语言 bundle 的行为再次漂移。
- 主提示词至少覆盖：`precheck_l1_main`、`precheck_l2_main`、`postaction_l1_main`、`postaction_l2_main`；若 `profile_instruction_main` 也依赖统一注入，则需要一并处理。
- 语言规则必须明确区分“自由文本字段”与“JSON 键 / 枚举值 / 机器字段”，避免误导模型翻译机器可读 token。
- 服务端删除统一注入后，不得留下死代码或无用调用点。

## 验收标准

1. 中英文主提示词文件都已直接包含语言输出规则。
2. 英文提示词已明确要求：若当前 turn 的用户/助手主要使用中文，自由文本输出字段必须使用中文。
3. 运行时统一注入逻辑已移除，主处理器直接使用文件内提示词。
4. 相关测试通过，并完成 `go test ./...` 与 `./make.bat build`。

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已将“根据当前对话主导语言决定自由文本输出语言”的规则直接写入 `default_cn` 与 `default_en` 的 5 个主提示词入口，不再依赖服务端统一追加。
- 已强化英文提示词中的语言约束，明确要求：即使提示词文件本身是英文，只要当前问答或指令以中文为主，生成的总结、精炼信息、归一化文本、原因说明等自由文本字段都必须使用中文。
- 已移除运行时的统一语言规则注入函数及 5 个处理器调用点，并同步修正相关单元测试，验证系统现在只使用文件内提示词规则。

### 2. 📂 文件变更清单

新增：
- 无

修改：
- `configs/prompts/default_cn/precheck_l1_main.md`
- `configs/prompts/default_cn/precheck_l2_main.md`
- `configs/prompts/default_cn/postaction_l1_main.md`
- `configs/prompts/default_cn/postaction_l2_main.md`
- `configs/prompts/default_cn/profile_instruction_main.md`
- `configs/prompts/default_en/precheck_l1_main.md`
- `configs/prompts/default_en/precheck_l2_main.md`
- `configs/prompts/default_en/postaction_l1_main.md`
- `configs/prompts/default_en/postaction_l2_main.md`
- `configs/prompts/default_en/profile_instruction_main.md`
- `internal/logic/processor/intent_extractor.go`
- `internal/logic/processor/precheck_memory_reviewer.go`
- `internal/logic/processor/turn_analyzer.go`
- `internal/logic/processor/postaction_candidate_reviewer.go`
- `internal/logic/processor/manual_profile_reviewer.go`
- `internal/logic/processor/manual_profile_reviewer_test.go`
- `internal/logic/processor/postaction_candidate_reviewer_test.go`
- `internal/logic/processor/turn_analyzer_test.go`

删除：
- `internal/logic/processor/prompt_language_policy.go`

### 3. 💻 关键代码调整详情

- `precheck_l1_main` / `precheck_l2_main`：
  - 直接在提示词中约束 `queries`、`reason` 跟随当前用户问题主导语言输出。
- `postaction_l1_main`：
  - 直接在提示词中约束 `details`、`memory_nodes[].abstract`、`memory_nodes[].details`、`profile_nodes[].content` 跟随 `target_turn` 当前问答主导语言输出。
  - 英文 prompt 明确补充“当前问答以中文为主时必须输出中文”的硬约束。
- `postaction_l2_main`：
  - 直接在提示词中约束 reviewer 的 `reason`、`normalized_content`、`level_reason` 等自由文本字段跟随当前问题与候选内容主导语言输出。
- `profile_instruction_main`：
  - 直接在提示词中约束画像归一化文本、替代/退役原因以及顶层 `reason` 跟随 `instruction` 主导语言输出。
- 处理器初始化链路：
  - 删除 `withMainPromptLanguagePolicy(...)` 调用，改为直接使用文件内提示词。
- 测试：
  - 将原本断言“系统提示词会被统一注入”的测试，改为断言“运行时保留原始提示词内容，不再追加统一规则”。

### 4. ⚠️ 遗留问题与注意事项

- 历史归档文档 `docs/completed/20260408/11-PROMPT_SCENE_RENAME_AND_LANGUAGE_POLICY.md` 仍会提到当时引入过的 `prompt_language_policy.go`，这是历史记录，不影响当前运行时。
- 当前语言控制已经完全依赖提示词文件；后续若继续细调中英文行为，应直接修改各场景 prompt，而不是重新引入共享注入层。
- 本轮已完成并通过以下验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `go test ./...`
  - `.\make.bat build`
