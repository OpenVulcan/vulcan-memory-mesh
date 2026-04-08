# 任务计划：增加 LLM 独立输出日志与统计信息

## 任务目标

为当前本地版运行时增加可选的 LLM 独立日志能力。在配置开关开启时，系统需要把每次 LLM 调用的输出内容记录到独立日志文件中，便于排查 JSON 格式错误、模型枚举漂移和输出质量问题。日志文件命名需要在原日志文件命名结构前增加 `LLM-` 前缀，并额外记录模型名称、执行耗时与 tokens 统计信息；默认配置必须关闭。

## 执行步骤

1. 梳理当前日志配置、日志文件命名规则与 LLM 调用链，确认最合适的埋点位置。
2. 在配置结构与 `base.yaml` 中增加 LLM 日志开关，并保证默认关闭。
3. 实现独立 LLM 日志写入能力，确保仅记录模型输出内容，同时补充模型名称、执行时间、tokens 等统计字段。
4. 确保日志文件命名与现有日志结构一致，仅增加 `LLM-` 前缀，并在启动期完成配置装配。
5. 为相关配置与日志逻辑补充测试，执行仓库要求的回归测试与标准构建。
6. 在计划文件末尾补充执行变更总结，并按规范归档。

## 技术选型与实现约束

- 仅记录 LLM 输出内容，不记录完整输入 prompt，避免额外泄露与日志膨胀。
- 独立日志文件应复用现有日志切分/命名约定，避免引入另一套完全不同的轮转策略。
- 日志字段需要足够支撑排障：至少包含场景、模型名、耗时、token 统计、输出正文与时间戳。
- 配置关闭时不得额外产生 LLM 日志文件，也不应影响现有日志性能路径。

## 验收标准

1. `base.yaml` 新增 LLM 日志开关，默认值为关闭。
2. 开关开启后会生成带 `LLM-` 前缀的独立日志文件，并记录模型名称、执行耗时、tokens 统计与输出内容。
3. 开关关闭时不生成 LLM 独立日志，也不影响现有业务日志。
4. 相关配置/日志测试通过，并完成 `go test ./...` 与 `./make.bat build`。

---

## 执行变更总结

### 1. 核心修复与调整概述

- 在运行时配置中新增 `logging.llm_output_enabled` 开关，并保持默认关闭；只有显式开启后才会创建独立的 LLM 输出日志文件。
- 为现有按小时分桶的日志写入器增加了文件名前缀能力，使独立日志能够沿用原有目录结构，并以 `LLM-YYYYMMDDHH.log` 形式落盘。
- 在共享 LLM 响应契约里补充了真实模型名字段，并通过运行时装饰器统一记录 LLM 输出内容、场景、模型名、耗时及 token 用量，覆盖 precheck、postaction 与画像指令等全部处理器链路。

### 2. 📂文件变更清单

- 修改：[D:\projects\VulcanMemoryMesh\configs\base.yaml](D:\projects\VulcanMemoryMesh\configs\base.yaml)
- 新增：[D:\projects\VulcanMemoryMesh\internal\app\llm_output_logging.go](D:\projects\VulcanMemoryMesh\internal\app\llm_output_logging.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\app\app.go](D:\projects\VulcanMemoryMesh\internal\app\app.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\app\app_test.go](D:\projects\VulcanMemoryMesh\internal\app\app_test.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\app\ports\llm.go](D:\projects\VulcanMemoryMesh\internal\app\ports\llm.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\logic\ports\llm.go](D:\projects\VulcanMemoryMesh\internal\logic\ports\llm.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\config\config.go](D:\projects\VulcanMemoryMesh\internal\config\config.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\config\config_test.go](D:\projects\VulcanMemoryMesh\internal\config\config_test.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\platform\logx\file_writer.go](D:\projects\VulcanMemoryMesh\internal\platform\logx\file_writer.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\platform\logx\file_writer_test.go](D:\projects\VulcanMemoryMesh\internal\platform\logx\file_writer_test.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\adapters\outbound\openai_native\llm.go](D:\projects\VulcanMemoryMesh\internal\adapters\outbound\openai_native\llm.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\adapters\outbound\openai_native\llm_test.go](D:\projects\VulcanMemoryMesh\internal\adapters\outbound\openai_native\llm_test.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\adapters\outbound\google_ai_studio\llm.go](D:\projects\VulcanMemoryMesh\internal\adapters\outbound\google_ai_studio\llm.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\adapters\outbound\google_ai_studio\llm_test.go](D:\projects\VulcanMemoryMesh\internal\adapters\outbound\google_ai_studio\llm_test.go)
- 修改：[D:\projects\VulcanMemoryMesh\docs\completed\20260408\13-LLM_OUTPUT_LOGGING.md](D:\projects\VulcanMemoryMesh\docs\completed\20260408\13-LLM_OUTPUT_LOGGING.md)

### 3. 💻关键代码调整详情

- 在 `Config.Logging` 中新增 `LLMOutputEnabled`，同时补充了环境变量覆盖入口 `VMM_LOG_LLM_OUTPUT_ENABLED`，让用户可以通过配置层显式控制该功能。
- 为 `HourlyFileWriter` 增加可选前缀能力，并导出 `NewHourlyPrefixedFileWriter` 供运行时创建 `LLM-` 独立日志流。
- 新增 `llmOutputLoggingClient` 装饰器，在不记录输入 prompt 的前提下，统一记录 `scene`、`response_format`、`model`、`elapsed`、`prompt_tokens`、`completion_tokens`、`total_tokens` 与 `llm_output`。
- 在 OpenAI 与 Google AI Studio 的出站适配器中补齐响应模型名回传，保证独立日志记录的是实际执行模型，而不是推断值。

### 4. ⚠️遗留问题与注意事项

- 当前独立 LLM 日志只记录成功返回的模型输出内容；如果上游请求在 provider 层直接失败且没有任何输出文本，本轮不会额外写入失败体日志。
- 工作区中仍保留了用户自己的 [D:\projects\VulcanMemoryMesh\configs\config.yaml](D:\projects\VulcanMemoryMesh\configs\config.yaml) 本地配置改动；本轮实现不会把它混入后续提交，提交时需要继续显式排除。
