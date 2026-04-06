# 任务计划：WRITE_MEMORIES脱敏与PII日志收敛

## 1. 任务目标

本任务需要补齐当前运行时 PII 边界，并收敛不适合生产环境的脱敏求值日志，确保：

1. `WriteMemories` 主动写记忆入口在入库前完成 PII 脱敏。
2. `postaction` 后续分析读取到的 recent direct writes 不会再把明文 PII 重新送入 LLM。
3. PII 引擎的逐命中求值日志不再出现在真实运行时日志中。
4. 调试器与显式调试场景仍保留可观测性，但生产链路默认静默。

## 2. 执行步骤

1. 梳理 `WriteMemories` 从 gRPC 入口到 `MemoryUseCase.Write(...)` 的调用链，确认最合适的脱敏边界。
2. 复核 `internal/platform/pii.Engine` 当前日志输出机制，区分调试器可见行为与真实运行时行为。
3. 在不破坏现有 `precheck` / `postaction` 脱敏边界的前提下，为主动写记忆补齐 PII 脱敏。
4. 调整 PII 引擎日志策略，使逐命中求值日志只在调试器或显式调试模式下出现，默认运行时不输出。
5. 补充或更新相关单元测试与集成测试。
6. 按仓库规则执行最少必要测试，并在闭环后补写执行总结。

## 3. 技术选型与处理原则

- `WriteMemories` 的脱敏应发生在用例层真正消费文本之前，避免把职责分散到存储适配层。
- 主动写记忆与 `postaction` 应尽量复用同一套 `PIIScrubber` 抽象，而不是再引入第二套脱敏实现。
- PII 求值日志属于调试辅助能力，不应默认依附业务运行时 logger。
- 如果需要保留调试信息，应通过显式调试开关或调试器路径触发，而不是让生产配置被动承受额外日志与 I/O 成本。

## 4. 验收标准

满足以下条件视为任务完成：

1. `WriteMemories` 写入库前的 `Abstract` / `Details` 已完成 PII 脱敏。
2. `postaction` 从 `LoadRecentDirectMemoryWrites(...)` 读到的近期开写记忆文本默认已是脱敏后的内容。
3. 默认运行时执行 `Scrub(...)` 时，不再输出 `[PII-EVAL]` 逐命中日志。
4. 调试器路径或显式调试模式仍可观察必要的调试信息。
5. 相关测试通过，且执行总结已补充后归档到 `docs/completed/20260406/`。

## 5. 当前状态

- 状态：已完成
- 结论：`WriteMemories` 已在用例层入库前接入共享 PII 脱敏，PII 引擎逐命中求值日志已收敛为仅调试模式可见，相关测试已通过。

## 6. 执行变更总结

### 1. 核心修复与调整概述

- 在 `MemoryUseCase` 中补齐共享 `PIIScrubber` 注入点，并把 `WriteMemoriesCommand.Items` 放到主动写入流程最前面完成脱敏，确保 direct-write 记忆在进入去重、embedding 与关系持久化前已经替换敏感信息。
- 在 `internal/app/app.go` 中把运行时共享脱敏器同步注入到 `MemoryUseCase`，让 `WriteMemories` 与 `precheck` / `postaction` 共用同一套主运行时规则目录和默认语言配置。
- 调整 `internal/platform/pii/engine.go` 的 `logEvaluation(...)` 行为，取消真实运行时的 `[PII-EVAL]` logger 输出，只在 `DebugMode` 显式开启时把安全摘要打印到调试输出。
- 同步更新 README 与 PII 配置文档，使“主运行时脱敏入口”与“生产日志默认静默、调试器负责细粒度观测”的行为描述与当前实现一致。

### 2. 📂文件变更清单

- 新增：无
- 修改：`internal/app/usecase/pii_redaction.go`
- 修改：`internal/app/usecase/memory_query.go`
- 修改：`internal/app/usecase/memory_query_test.go`
- 修改：`internal/app/app.go`
- 修改：`internal/platform/pii/engine.go`
- 修改：`internal/platform/pii/engine_test.go`
- 修改：`README.md`
- 修改：`docs/pii-validator-config_CN.md`
- 修改：`docs/pii-validator-config_EN.md`
- 删除：无

### 3. 💻关键代码调整详情

- 新增 `scrubWriteMemoryItemsPII(...)`，集中处理 `WriteMemoryItem.Abstract` / `WriteMemoryItem.Details` 的脱敏，避免 direct-write 再各处散落单独 scrub。
- 为 `MemoryUseCase` 增加 `piiScrubber` 字段与 `ConfigurePIIScrubber(...)` 方法，并在 `Write(...)` 中于 `validateWriteMemoriesCommand(...)` 前先替换 `cmd.Items`，这样后续软幂等哈希、embedding 输入、评审候选与持久化文本都天然复用脱敏后的内容。
- 将 `memory.ConfigurePIIScrubber(piiScrubber)` 纳入主运行时装配，避免只有 `precheck` / `postaction` 生效、而 `WriteMemories` 仍留在明文旁路。
- 把 PII 引擎的 `logEvaluation(...)` 改为仅在 `DebugMode` 下向 `debugOutput` / `stdout` 打印安全摘要；默认业务运行时即便传入普通应用 logger，也不会再产生 `[PII-EVAL]` 热路径日志。
- 新增 / 更新测试：
  - `TestMemoryUseCaseWriteScrubsPIIBeforeEmbeddingAndPersistence`
  - `TestEngineKeepsRuntimeLoggerSilentWhenDebugModeOff`
  - `TestEngineDebugModePrintsVerboseTrace`

### 4. ⚠️遗留问题与注意事项

- 本次修复保证“从现在开始经 `WriteMemories` 新写入”的记忆文本会先脱敏；已经存在于库里的历史 direct-write 明文数据不会被自动回填，若要彻底清理旧数据仍需要后续迁移方案。
- 当前收敛的是 PII 引擎的逐命中求值日志；调试器仍可通过 `DebugMode` 看到命中细节与安全摘要，这属于显式调试行为，不再由生产 logger 默认承担。
- 已执行测试：
  - `go test ./internal/app/usecase ./internal/platform/pii ./internal/app ./internal/adapters/inbound/grpcapi ./internal/logic/processor ./internal/platform/textutil ./internal/config`
