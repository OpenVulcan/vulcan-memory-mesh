# 任务计划：Prompt 语言路由与目录校验重构

## 任务目标

本轮需要对当前提示词目录选择逻辑做一次收口式重构，目标如下：

1. 默认提示词目录改为 `configs/prompts/default_en/`。
2. 现有中文默认提示词目录从 `configs/prompts/default/` 迁移为 `configs/prompts/default_cn/`。
3. 配置层新增显式提示词目录选择能力，例如 `prompt_language` 或等价字段，用于手动指定应使用的提示词目录族。
4. 一旦通过配置显式指定提示词目录，服务端启动时必须校验对应目录及其所需文件全部存在；缺少任意目录或文件时必须直接报错并拒绝启动。
5. 彻底移除当前“根据模型名称匹配提示词目录”的逻辑，不再保留 `prompts.routes` 语义链路。
6. 同步更新配置、加载器、运行时装配、测试与必要文档，确保新规则闭环。

## 执行步骤

1. 梳理当前提示词加载链路，明确以下模块的职责与耦合点：
   - `internal/config/config.go`
   - `internal/config/loader.go`
   - `internal/config/manager.go`
   - `internal/app/app.go`
   - `internal/logic/processor/*`
2. 设计新的配置模型，明确：
   - 默认提示词目录族如何表达；
   - 显式指定目录时如何覆盖默认值；
   - 启动阶段如何统一做目录与文件完整性校验。
3. 调整提示词目录结构与加载逻辑：
   - 将现有中文默认提示词迁移到 `default_cn`；
   - 将英文默认提示词作为新的默认目录；
   - 删除模型名到提示词目录的匹配逻辑与相关配置校验。
4. 修改运行时装配代码，确保所有 processor 均基于新的显式目录选择结果加载 prompt，而不是继续传入模型名做二次路由。
5. 补齐或更新相关测试，覆盖：
   - 默认使用 `default_en`
   - 显式切换到 `default_cn`
   - 指定目录缺失或文件缺失时启动失败
   - 旧 `prompts.routes` 配置不再生效或被拒绝
6. 同步更新文档与计划总结，最终归档到 `docs/completed/20260408/`。

## 技术选型与实现约束

- 本轮不再保留“模型到提示词目录”的动态匹配机制，避免继续维护无收益的分支路径。
- 提示词目录选择优先由配置显式决定；未配置时必须稳定落到英文默认目录。
- 目录完整性校验必须覆盖运行时实际需要的全部 prompt 文件，不能只检查目录存在。
- 代码新增或重写区域需遵守仓库现有中英文双语注释规范。
- 任何涉及配置契约变化的实现，必须同步更新配置样例与说明文档。

## 验收标准

1. 中文默认提示词目录完成迁移并可通过新配置显式选择。
2. 未显式配置时，系统默认使用英文提示词目录。
3. 服务端启动时会校验目标提示词目录与所需 prompt 文件；缺失时直接失败。
4. 代码中不再存在基于模型名匹配提示词目录的运行时逻辑。
5. 相关测试通过，且文档已同步更新。

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已把默认提示词目录从 `default` 切换为 `default_en`，并把原中文默认提示词完整迁移到 `default_cn`。
- 已新增显式提示词包选择配置 `prompts.prompt_language`，支持内建语言别名与自定义目录名。
- 已彻底移除原先基于模型名匹配提示词目录的运行时逻辑，不再保留 `prompts.routes` 配置语义。
- 已把启动期提示词校验收敛为“默认英文提示词包必须存在 + 当前选中提示词包必须完整存在”，缺少任意必需文件时直接拒绝启动。
- 已同步更新打包配置、README 说明与相关测试，并通过标准构建刷新 `output/configs`，保证真实运行时夹具与仓库配置保持一致。

### 2. 📂文件变更清单

新增：

- `internal/config/prompt_bundle.go`
- `configs/prompts/default_cn/analyze_turn.md`
- `configs/prompts/default_cn/assemble_context.md`
- `configs/prompts/default_cn/extract_intent.md`
- `configs/prompts/default_cn/merge_profile.md`
- `configs/prompts/default_cn/review_postaction_candidates.md`
- `configs/prompts/default_cn/review_precheck_memory.md`
- `configs/prompts/default_cn/review_profile_instruction.md`
- `configs/prompts/default_cn/summarize_entry.md`

修改：

- `README.md`
- `cmd/vmm-local/main.go`
- `configs/base.yaml`
- `internal/app/app.go`
- `internal/app/app_test.go`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/config/loader.go`
- `internal/config/loader_test.go`
- `internal/config/manager.go`
- `internal/config/manager_test.go`
- `internal/testutil/realruntime.go`
- `internal/testutil/realruntime_test.go`

删除：

- `configs/prompts/default/analyze_turn.md`
- `configs/prompts/default/assemble_context.md`
- `configs/prompts/default/extract_intent.md`
- `configs/prompts/default/merge_profile.md`
- `configs/prompts/default/review_postaction_candidates.md`
- `configs/prompts/default/review_precheck_memory.md`
- `configs/prompts/default/review_profile_instruction.md`
- `configs/prompts/default/summarize_entry.md`

### 3. 💻关键代码调整详情

- 在 `internal/config/prompt_bundle.go` 中新增提示词包规范化逻辑，统一把 `default/en/english` 映射到 `default_en`，把 `zh/zh-cn/cn/chinese` 映射到 `default_cn`，同时保留自定义目录名直通能力。
- 在 `internal/config/config.go` 中把 `PromptConfig` 从旧的 `routes` 路由映射改为单值 `prompt_language`；同时新增对旧 `prompts.routes` 的显式拒绝逻辑，避免旧配置被静默吞掉。
- 在 `internal/config/loader.go` 中删除旧的 prompt route 归一化与校验链路，并把系统提示词基线校验改为检查 `default_en` 目录。
- 在 `internal/config/manager.go` 中重写 `PromptManager`：运行时只解析一套显式选中的提示词包，不再根据模型名二次匹配目录；并增加“选中目录缺失或文件不全即启动失败”的完整性校验。
- 在 `internal/app/app.go` 中保留处理器按业务层级选择主 LLM 模型的能力，但彻底解除“处理器传入模型名用于提示词目录匹配”的耦合，确保模型选择仅用于 LLM 请求，不再参与 prompt 目录路由。
- 在 `cmd/vmm-local/main.go` 与 `internal/testutil/realruntime.go` 中统一改为使用 `cfg.Prompts.PromptLanguage` 构建 `PromptManager`。
- 在 `configs/base.yaml` 与 `README.md` 中补充新的显式提示词选择说明、默认英文策略和旧 `prompts.routes` 废弃提示。
- 通过 `.\make.bat build` 同步刷新 `output/configs`，解决真实运行时测试夹具继续读取旧打包 prompt 目录的问题。

### 4. ⚠️遗留问题与注意事项

- 当前仓库仍保留 `qwen3.5-base`、`qwen3.5-flash` 等目录，但它们现在只作为“可手动指定的自定义提示词包”存在，不再被模型名自动匹配。
- 旧版历史文档与已归档计划中仍会提到 `prompts.routes` 或 `configs/prompts/default`，这些属于历史记录，不代表当前运行时契约。
- 本轮只完成提示词目录选择与校验收口，尚未实现基于前端语言或响应内容自动切换提示词的更细粒度策略。

### 验证结果

- 已执行：`go test ./internal/config ./internal/app ./internal/testutil ./internal/logic/processor`
- 已执行：`go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
- 已执行：`go test ./...`
- 已执行：`.\make.bat build`
