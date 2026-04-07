# 任务计划：analyze_turn JSON 解码失败原始返回日志补全

## 一、任务目标

修复当前 `analyze_turn` 发生 `json decode failed` 时日志只输出结构化错误文本、却丢失模型原始返回内容的问题，确保运维排障时可以直接在运行日志中看到导致解码失败的原始 LLM 输出。

## 二、技术方案

### 2.1 梳理错误链路

确认 `turn_analyzer` 在 JSON 解析失败时会返回带 `Raw` 字段的 `InvalidLLMOutputError`，并定位 post-action 同步/异步链路当前只记录 `err`、没有透传 `Raw` 的日志点。

### 2.2 补充原始返回日志输出

在 post-action 失败日志中增加针对 `InvalidLLMOutputError` 的识别：

1. 仅当 `Scene == "analyze_turn"` 且 `Message == "json decode failed"` 时输出原始返回。
2. 原样写入日志，避免再做二次裁剪或结构化重编码，便于复现真实解析失败现场。
3. 保持其他错误场景的日志行为不变，避免无关 payload 扩散。

### 2.3 测试补齐

增加回归测试，验证：

1. 当 `turn_analyzer` 返回 `json decode failed` 的 `InvalidLLMOutputError` 时，日志中会包含原始返回内容。
2. 非该场景的普通错误不会误带出原始载荷。

## 三、执行步骤

1. 检查 `turn_analyzer`、post-action 同步链路与队列链路中的错误日志位置。
2. 增加 `json decode failed` 原始返回日志输出。
3. 补充日志回归测试。
4. 运行 post-action / processor / grpc 相关测试确认无回归。

## 四、验收标准

1. `invalid llm output for analyze_turn: json decode failed` 出现时，日志中能看到模型原样返回。
2. 其他错误场景不会被额外污染。
3. 相关测试通过。

## 执行变更总结

### 1. 核心修复与调整概述

本次修复在 post-action 队列失败日志中补齐了 `analyze_turn` 的诊断信息：

1. 为 post-action 分析器端口增加 `AnalyzeModel()`，让运行时能够稳定记录本次使用的模型标识。
2. 在排队分析失败日志里默认输出 `model` 字段。
3. 当错误属于 `analyze_turn` 的 `json decode failed` 时，额外把 `InvalidLLMOutputError.Raw` 以原样文本输出到日志中的 `llm_raw_output` 字段，便于直接查看模型返回体。
4. 保持其他错误路径不追加原始返回，避免无关日志膨胀。

### 2. 📂文件变更清单

新增：无

修改：

1. `internal/app/usecase/postaction.go`
2. `internal/app/usecase/postaction_queue.go`
3. `internal/app/usecase/postaction_test.go`
4. `internal/logic/processor/turn_analyzer.go`

删除：无

### 3. 💻关键代码调整详情

1. 扩展 `PostActionTurnAnalyzer` 接口，新增 `AnalyzeModel()` 作为失败日志的模型来源。
2. 在 `processor.TurnAnalyzer` 中实现 `AnalyzeModel()`，返回当前 `analyze_turn` 配置模型。
3. 在 `processQueuedTurns` 的失败日志路径中新增 `appendQueuedTurnAnalysisFailureLogFields`，统一补齐模型字段，并仅在 `analyze_turn/json decode failed` 时写出原始 LLM 返回。
4. 新增回归测试 `TestPostActionUseCaseLogsRawAnalyzeTurnJSONDecodeFailure`，验证日志同时包含错误摘要、模型字段与原样返回体。

### 4. ⚠️遗留问题与注意事项

1. 当前日志里的 `model` 来自 `TurnAnalyzer` 的配置模型标识，不是 provider SDK 回传的二次解析模型名；若后续需要精确记录路由最终命中的 provider/model，需扩展 LLM 响应契约。
2. 原始返回体仅在 `analyze_turn/json decode failed` 场景下输出，其他结构化校验错误仍维持现状。
3. 已完成验证：
   - `go test ./internal/app/usecase ./internal/logic/processor ./internal/adapters/inbound/grpcapi ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
