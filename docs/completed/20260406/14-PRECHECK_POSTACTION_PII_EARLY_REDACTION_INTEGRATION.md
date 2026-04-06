# 任务计划：PRECHECK_POSTACTION_PII前置脱敏接入

## 1. 任务目标

本任务需要把 PII 脱敏能力前置接入到 `precheck` 与 `postaction` 的首环节，确保：

1. 请求内容在进入 LLM 分析前已完成 PII 脱敏。
2. 请求内容在进入长期记忆写库链路前已完成 PII 脱敏。
3. 不破坏当前入站清洗、MessageNormalizer、NoiseGate 与 post-action 既有职责边界。
4. 配置、规则加载、运行时装配与测试覆盖保持一致。

## 2. 执行步骤

1. 梳理 `cmd/vmm-local` 到 `internal/app/app.go` 的运行时装配链路，明确 `precheck` 与 `postaction` 的实际调用入口。
2. 梳理入站清洗层、标准化层、precheck 用例、postaction 用例、写库前过滤链路的职责边界。
3. 选择最早且职责合理的 PII 接入点，使脱敏发生在：
   - `precheck` 的首环节
   - `postaction` 的首环节
4. 在不破坏现有清洗职责划分的前提下，引入 PII 引擎装配与调用。
5. 补充针对 `precheck` / `postaction` / 存库前行为的测试。
6. 运行仓库要求的相关测试，必要时执行更大范围验证。

## 3. 技术选型与处理原则

- PII 脱敏必须在业务用例真正消费文本之前执行，而不是放在日志或持久化适配层事后处理。
- 入站清洗仍负责媒体/base64/`<think>` 标签等结构性净化；PII 只负责敏感内容替换，不篡改现有职责。
- 若 `precheck` 与 `postaction` 的首环节文本入口不同，应分别在各自首环节显式调用，而不是依赖隐式副作用。
- 装配方式优先复用现有 `config.PromptLayout` 中的 `SystemPIIRulesDir()` / `UserPIIRulesDir()` 与 `cfg.PII.DefaultLanguage`。

## 4. 验收标准

满足以下条件视为任务完成：

1. `precheck` 首环节在进入 LLM 分析前使用 PII 脱敏后的文本。
2. `postaction` 首环节在进入 LLM 分析及后续写库链路前使用 PII 脱敏后的文本。
3. 主运行时真正装配了 PII 引擎，不再仅限 `vmm-pii-tester`。
4. 测试能证明敏感内容不会以明文继续进入分析或写库链路。
5. 完成变更总结并归档到 `docs/completed/20260406/`。

## 5. 当前状态

- 状态：已完成
- 结论：PII 已前置接入 `precheck` / `postaction` 首环节，主运行时已实际装配共享脱敏器，测试与文档同步已完成。

## 6. 执行变更总结

### 1. 核心修复与调整概述

- 在 `internal/app/app.go` 中新增运行时 PII 脱敏器装配，复用 `SystemPIIRulesDir()` / `UserPIIRulesDir()` 与 `cfg.PII.DefaultLanguage`，让主运行时真正消费这套规则。
- 在 `internal/app/usecase/precheck.go` 中把当前请求、最近 turn 窗口、召回候选与 reviewer/assembler 入口统一纳入 PII 脱敏边界，确保进入第一层/第二层 LLM 与最终注入上下文的内容都已脱敏。
- 在 `internal/app/usecase/postaction.go` 中把 canonical payload、raw replay payload、噪声门输入、关系库存储文本和单轮分析器输入统一接入 PII 脱敏，保证进入写库链与 LLM 提炼链之前已完成敏感信息替换。
- 同步更新 README、PII 配置说明、post-action / noise-gate 中文文档与“未接入主运行时参数清单”，使当前仓库文档与真实运行时行为一致。

### 2. 📂文件变更清单

- 新增：`internal/app/usecase/pii_redaction.go`
- 新增：`internal/app/usecase/pii_redaction_test.go`
- 修改：`internal/app/app.go`
- 修改：`internal/app/usecase/precheck.go`
- 修改：`internal/app/usecase/precheck_test.go`
- 修改：`internal/app/usecase/postaction.go`
- 修改：`internal/app/usecase/postaction_test.go`
- 修改：`README.md`
- 修改：`docs/post-action-guide_CN.md`
- 修改：`docs/noise-gate-guide_CN.md`
- 修改：`docs/pii-validator-config_CN.md`
- 修改：`docs/pii-validator-config_EN.md`
- 修改：`docs/unused-config-parameters_CN.md`

### 3. 💻关键代码调整详情

- 新增 `PIIScrubber` 用例层接口与共享脱敏辅助函数，覆盖：
  - pre-check 最近 turn 脱敏
  - pre-check 召回候选 / reviewer 输入 / fallback 日志脱敏
  - post-action 命令对象 canonical/raw 双副本脱敏
  - post-action 单轮分析输入中的目标 turn、参考历史、活跃记忆锚点、recent direct write 脱敏
- 通过 `ConfigurePIIScrubber(...)` 把共享脱敏器注入到 `PreCheckUseCase` 与 `PostActionUseCase`，避免修改现有构造签名导致大面积测试改动。
- 在 `internal/app/app.go` 中增加 `runtimePIIScrubber` 与 `buildPIIScrubber(...)`，把 `internal/platform/pii.Engine` 绑定成主运行时默认语言脱敏器。
- 新增两组回归测试：
  - `TestPreCheckExecuteScrubsPIIBeforeIntentReviewAndAssembly`
  - `TestPostActionExecuteScrubsPIIBeforeNoiseGateAndPersistence`
  - `TestBuildTurnAnalysisInputScrubsPIIBeforeAnalyzer`

### 4. ⚠️遗留问题与注意事项

- 当前这次接入保证“从本次请求进入主业务用例开始”的文本会先被脱敏；历史库中已经存在的旧数据不会被批量回填，需要后续如有需要再单独设计迁移/重建方案。
- gRPC 入口层仍保留原有 raw / cleaned 收据日志分层；这次没有调整传输层日志策略，而是把业务用例后续进入写库与 LLM 的文本边界先收紧。
- 已执行：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config ./internal/app`
  - `go test ./...`
