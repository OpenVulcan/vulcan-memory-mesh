# 任务计划：POSTACTION重复脱敏修复

## 1. 任务目标

本任务需要修复 `postaction` 链路中因 PII 已在入口完成脱敏后，后续分析输入再次执行脱敏而产生的重复处理问题，确保：

1. `postaction` 在入口处完成一次且仅一次请求级 PII 脱敏。
2. 后续入库、排队推进、单轮分析与 reviewer 流程消费的都是入口已脱敏后的文本。
3. 不再对同一条 `postaction` 链内数据做重复脱敏，避免额外规则替换副作用与无效性能损耗。
4. 文档、测试与当前实现保持一致，避免后续维护者误判脱敏边界。

## 2. 执行步骤

1. 复核 `postaction` 当前入口、落库、队列解码、单轮分析输入与 reviewer 调用链路，确认重复脱敏的具体位置。
2. 调整 `postaction` 用例层实现，移除链路内重复脱敏逻辑，并保留入口处的主脱敏边界。
3. 补充或更新测试，证明：
   - 入库内容已经是脱敏后的文本；
   - 后续从库中读回并推进到分析器 / reviewer 的内容仍然是脱敏文本；
   - 不再依赖额外二次脱敏来维持正确性。
4. 同步更新相关文档，使 `postaction` 的 PII 行为描述为“入口一次脱敏，后续复用已脱敏数据”。
5. 执行测试验证并整理执行总结。

## 3. 技术选型与处理原则

- `postaction` 的 PII 主边界保持在 `Execute(...)` 入口，不把脱敏职责分散到后续多个阶段。
- 后续分析与 reviewer 继续消费已脱敏的持久化数据，而不是重新对同一链路文本做二次脱敏。
- 若某个后续路径确实仍可能接触非本链路写入的历史数据，需要与“本次修复 postaction 重复脱敏”严格区分，避免过度扩大改动范围。
- 优先保持现有构造与装配方式稳定，减少对非目标模块的干扰。

## 4. 验收标准

满足以下条件视为任务完成：

1. `postaction` 入口处仍保留 PII 脱敏。
2. `postaction` 链内不再对同一批已脱敏文本执行重复脱敏。
3. 测试能证明入库文本与后续推进文本均为脱敏后的内容。
4. 文档已改为准确描述当前 `postaction` 的单次脱敏边界。
5. 计划文件补充执行变更总结后归档至 `docs/completed/20260406/`。

## 5. 当前状态

- 状态：已完成
- 结论：`postaction` 已改为在入口执行一次 PII 脱敏，后续分析与 reviewer 复用已脱敏持久化数据，不再对同链路文本重复脱敏。

## 6. 执行变更总结

### 1. 核心修复与调整概述

- 移除了 `postaction` 在 `buildTurnAnalysisInput(...)` 返回前的重复 PII 脱敏，保留 `Execute(...)` 入口作为该链路唯一的请求级脱敏边界。
- 保持 `postaction` 入库、队列推进、单轮分析输入组装都继续消费入口已脱敏并已持久化的文本，避免重复规则替换造成潜在副作用和无效性能损耗。
- 同步把 `post-action` 中文文档修正为“入口一次脱敏，后续复用已脱敏持久化数据”的真实行为描述。

### 2. 📂文件变更清单

- 修改：`internal/app/usecase/postaction.go`
- 修改：`internal/app/usecase/pii_redaction.go`
- 修改：`internal/app/usecase/pii_redaction_test.go`
- 修改：`internal/app/usecase/postaction_test.go`
- 修改：`docs/post-action-guide_CN.md`
- 新增：`docs/plan/20260406-15-POSTACTION重复脱敏修复.md`（待归档）

### 3. 💻关键代码调整详情

- 在 `internal/app/usecase/postaction.go` 中，`buildTurnAnalysisInput(...)` 现在直接返回组装后的 `TurnAnalysisInput`，不再对当前 turn、历史引用、活跃记忆和 recent direct writes 再做一轮 usecase 级 scrub。
- 在 `internal/app/usecase/pii_redaction.go` 中删除 `scrubTurnAnalysisInputPII(...)`，收敛 `postaction` 的 PII 职责边界，只保留入口命令对象的脱敏辅助逻辑。
- 在 `internal/app/usecase/postaction_test.go` 中把原“分析前再次脱敏”测试改为“复用已脱敏持久化数据且不触发额外 scrub”，并通过 `countingPIIScrubber` 断言组装分析输入阶段不会再次调用脱敏器。

### 4. ⚠️遗留问题与注意事项

- 本次修复只收敛了 `postaction` 这条链路内的重复脱敏，不代表仓库中所有其他写入口都已完成同样的单边界治理。
- 若后续要把“所有会写入长期存储并再次喂给 LLM 的文本都只在写入前脱敏一次”升级为仓库级统一契约，还需要继续梳理 `WriteMemories`、手工画像指令等其他入口。
- 已执行测试：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
