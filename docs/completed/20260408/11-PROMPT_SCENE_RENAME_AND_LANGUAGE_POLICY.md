# 任务计划：提示词场景重命名与语言输出规则收口

## 任务目标

本轮需要对当前提示词体系做一次收口式清理与重命名，目标如下：

1. 删除当前主运行时未接入的历史残留提示词文件及其对应代码、测试与校验依赖。
2. 保留真正属于 `PreCheck` / `PostAction` / 手工画像指令评审主链路的提示词，并将其场景名统一改成更能表达集成逻辑的命名。
3. 让主提示词入口统一携带“输出语言应跟随当前用户对话语言”的规则，减少模型在中英文或混合语境下输出语言漂移。
4. 同步更新提示词目录、加载校验、运行时装配、测试与必要文档，确保新的提示词契约可以稳定启动并通过回归验证。

## 执行步骤

1. 梳理当前提示词文件、运行时装配点与实际调用链，明确：
   - 哪些提示词已接入主运行时；
   - 哪些只是历史残留；
   - 哪些是“子提示词”但当前实际上没有把提示词正文送入模型。
2. 确定新的场景命名方案，并把仍在使用的主提示词统一重命名为带业务阶段语义的名称。
3. 删除未接入主运行时的历史处理器、提示词文件和相关测试，同时移除不再需要的启动校验项。
4. 调整主运行时代码与测试，让各条链路改用新的场景名加载提示词。
5. 为主提示词统一加入“语言跟随当前用户对话”的输出约束，并在中英双套默认提示词中同步落地。
6. 运行规定测试、补充变更总结，并在完成后归档计划文件。

## 技术选型与实现约束

- 本轮优先保留真正被主运行时调用的提示词，不再为了历史兼容保留未接线的 prompt 文件。
- 若某个“子提示词”当前只是做存在性检查，而不把正文送入 LLM，则应优先移除这层伪依赖，而不是继续维持空转文件。
- 新场景名必须直接体现业务阶段，避免继续使用只能从历史语境里理解的抽象命名。
- 语言输出规则必须强调“跟随当前用户对话主语言”，并兼顾混合语种场景下的稳定决策。
- 代码新增或重写区域需继续遵守仓库现有中英文双语注释规范。

## 验收标准

1. 历史残留提示词文件与对应残留代码已从主仓库契约中移除。
2. 主运行时仍在使用的提示词场景已全部切换到新的命名方案。
3. 主提示词中已加入统一的语言对齐规则，并在默认中英文提示词目录中同步更新。
4. 启动期提示词目录校验与测试已适配新场景名，不再要求已删除的历史提示词文件。
5. 相关测试通过，且必要文档已同步更新。

## 执行变更总结

### 1. 核心修复与调整概述

- 删除了未接入主运行时的残留提示词与处理器，包括 `assemble_context` 伪提示词依赖、`merge_profile` 旧画像合并链路和 `summarize_entry` 历史摘要链路。
- 将主运行时实际保留的提示词统一收口为 5 个主入口：`precheck_l1_main`、`precheck_l2_main`、`postaction_l1_main`、`postaction_l2_main`、`profile_instruction_main`。
- 在 5 个主处理器入口统一追加“输出语言必须跟随当前用户对话主语言”的共享语言规则，避免中英文或混合语境下的输出语言漂移。
- 同步更新了启动期提示词目录校验、应用装配、日志/错误场景名、测试桩、README 与主说明文档，并完成打包态 `output/configs` 的同步。

### 2. 📂文件变更清单

- 新增：
  - `internal/logic/processor/prompt_language_policy.go`
  - `internal/logic/processor/reviewer_test_helpers_test.go`
- 修改：
  - `internal/config/loader.go`
  - `internal/app/app.go`
  - `internal/app/app_test.go`
  - `internal/app/usecase/memory_query.go`
  - `internal/app/usecase/postaction.go`
  - `internal/app/usecase/postaction_candidate_review.go`
  - `internal/app/usecase/postaction_profiles.go`
  - `internal/app/usecase/postaction_queue.go`
  - `internal/app/usecase/postaction_test.go`
  - `internal/app/usecase/profile.go`
  - `internal/config/manager_test.go`
  - `internal/logic/processor/context_assembler.go`
  - `internal/logic/processor/context_assembler_test.go`
  - `internal/logic/processor/intent_extractor.go`
  - `internal/logic/processor/manual_profile_reviewer.go`
  - `internal/logic/processor/manual_profile_reviewer_test.go`
  - `internal/logic/processor/postaction_candidate_reviewer.go`
  - `internal/logic/processor/postaction_candidate_reviewer_test.go`
  - `internal/logic/processor/precheck_memory_reviewer.go`
  - `internal/logic/processor/profile_review_shared.go`
  - `internal/logic/processor/render.go`
  - `internal/logic/processor/turn_analyzer.go`
  - `internal/logic/processor/turn_analyzer_test.go`
  - `README.md`
  - `docs/api-test-guide_CN.md`
  - `docs/grpc-integration-guide_CN.md`
  - `docs/hierarchy-grpc-design_CN.md`
  - `docs/post-action-guide_CN.md`
  - `docs/profile-node-lifecycle_CN.md`
  - `docs/retention-governance-guide_CN.md`
  - `docs/unused-config-parameters_CN.md`
  - `configs/prompts/default_cn/*`
  - `configs/prompts/default_en/*`
  - `configs/prompts/qwen3.5-base/*`
  - `configs/prompts/qwen3.5-flash/*`
- 删除：
  - `internal/logic/processor/entry_summarizer.go`
  - `internal/logic/processor/profile_merger.go`
  - `internal/logic/processor/profile_merger_test.go`
  - 四套提示词目录中的 `assemble_context.md`
  - 四套提示词目录中的 `merge_profile.md`
  - 四套提示词目录中的 `summarize_entry.md`

### 3. 💻关键代码调整详情

- `ContextAssembler` 改为纯确定性拼装，不再伪依赖一份不会发送给模型的 `assemble_context` 提示词。
- `IntentExtractor`、`PreCheckMemoryReviewer`、`TurnAnalyzer`、`PostActionCandidateReviewer`、`ManualProfileReviewer` 全部改为加载新场景名，并在请求前统一追加共享语言策略。
- `PromptLoader.RequiredScenes` 改为只要求 5 个主提示词文件，启动时缺失任一主入口都会直接阻止服务启动。
- 删除旧 `merge_profile` 处理器后，把仍被 post-action / manual reviewer 复用的画像评审索引归一化逻辑沉淀到共享文件，避免公共校验函数随历史链路一起被误删。
- 用例层和日志层的 `InvalidLLMOutputError.Scene`、降级校验、JSON 解码日志判定等全部切换到新的提示词场景名，保证错误排查与实际配置一致。

### 4. ⚠️遗留问题与注意事项

- `docs/daily-reports/` 与 `docs/completed/` 下的历史记录仍保留旧场景名，这是有意保留的历史上下文，不属于当前运行时契约。
- 运行时依赖真实打包布局的测试会读取 `output/configs`；因此本轮除了修改源提示词目录，也必须通过标准构建保持打包态副本同步。
- 当前“语言跟随”规则是通过代码统一注入到 5 个主提示词入口，而不是手工复制进每份 prompt 文件正文；后续若新增新的主 LLM 场景，也要记得复用同一策略。
