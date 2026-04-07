# 任务目标

在当前 VMM 本地版运行时中，为 LLM 与向量 Embedding 增加 Google AI Studio 原生对接能力，使用 `google.golang.org/genai` 官方 Go SDK 接入 Google 的 Gemini/Embedding API，并在不破坏现有 `adapters -> app -> logic/domain` 依赖方向的前提下，将其纳入现有多路由与配置体系。

# 详细执行步骤

1. 梳理当前 `openai_native`、`dashscope_rerank`、AI 路由配置、运行时装配与 failover 包装的现状，明确 Google AI Studio 原生接入需要落点的代码位置。
2. 设计 Google AI Studio 适配方案，明确：
   - LLM 与 Embedding 是否分别提供原生客户端；
   - provider 命名与配置形态；
   - 与现有 `llm.routes`、`embedding.*`、`key_failover` 的兼容方式；
   - 是否影响现有 OpenAI-compatible 语义。
3. 引入 `google.golang.org/genai` 依赖，新增 Google AI Studio 原生适配器实现，并保持中英文双语注释规范。
4. 调整配置归一化、校验与运行时装配逻辑，使 Google AI Studio 可作为 LLM 路由 provider 与 Embedding provider 被正常启用。
5. 补充默认配置说明、示例配置和必要文档，明确 Google AI Studio 所需字段与取值说明。
6. 增加或更新相关测试，至少覆盖配置校验、运行时装配与关键客户端行为。
7. 按仓库要求执行必要测试与自检，确认没有破坏现有 OpenAI-compatible 路径。
8. 在任务完成后补充执行变更总结，并迁移到 `docs/completed/20260406/`。

# 技术选型

- 采用 `google.golang.org/genai` 作为唯一官方 SDK 接入 Google AI Studio，避免自行拼接 HTTP 协议带来的维护成本与协议漂移风险。
- 复用当前运行时“配置 -> 路由归一化 -> 运行时装配 -> key_failover 包装”的主链路，尽量以增量方式扩展 provider，而不是再造一套 Google 专属配置体系。
- LLM 与 Embedding 适配器优先保持与当前 `appports.LLMClient`、`appports.EmbeddingClient` 契约一致，降低应用层变更范围。
- 在配置层显式区分“OpenAI-compatible provider”与“Google AI Studio native provider”，避免把 Google 原生路径伪装成 OpenAI-compatible 端点，从而减少后续维护混淆。

# 验收标准

- 运行时可以通过配置启用 Google AI Studio 原生 LLM。
- 运行时可以通过配置启用 Google AI Studio 原生 Embedding。
- 现有 OpenAI-compatible LLM 与 Embedding 路径保持可用，不发生回归。
- 配置校验、运行时装配和必要测试可以覆盖 Google AI Studio 新路径。
- 相关配置说明与示例文件已同步更新。

# 执行变更总结

## 1. 核心修复与调整概述

- 新增 `google_ai_studio` 原生 provider，底层使用 `google.golang.org/genai` 官方 Go SDK 对接 Gemini API。
- 新增 Google AI Studio 原生 `LLMClient` 与 `EmbeddingClient`，并保持与现有 `appports.LLMClient`、`appports.EmbeddingClient` 契约一致。
- 将 `ai_key_failover` 从原先仅支持 OpenAI-compatible 的固定工厂，扩展为按 provider 构建具体客户端与错误分类器。
- 调整配置校验与运行时装配逻辑，使 `llm.routes[].provider` 与 `embedding.provider` 均可使用 `google_ai_studio`。
- 补充 Google AI Studio 的配置帮助、示例配置与设计文档说明，同时保留 `noise_rules` 与 `pii_rules` 既有结构不变。

## 2. 📂文件变更清单

### 新增

- `internal/adapters/outbound/google_ai_studio/client.go`
- `internal/adapters/outbound/google_ai_studio/hints.go`
- `internal/adapters/outbound/google_ai_studio/llm.go`
- `internal/adapters/outbound/google_ai_studio/embedding.go`
- `internal/adapters/outbound/google_ai_studio/llm_test.go`
- `internal/adapters/outbound/google_ai_studio/embedding_test.go`
- `internal/adapters/outbound/ai_key_failover/provider_factory.go`
- `configs/google_ai_studio.config.example.yaml`

### 修改

- `internal/adapters/outbound/ai_key_failover/llm.go`
- `internal/adapters/outbound/ai_key_failover/embedding.go`
- `internal/adapters/outbound/ai_key_failover/llm_multi_route.go`
- `internal/adapters/outbound/ai_key_failover/classifier.go`
- `internal/adapters/outbound/ai_key_failover/key_failover_test.go`
- `internal/app/app.go`
- `internal/app/app_test.go`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `configs/base.yaml`
- `configs/config.yaml`
- `configs/openai.config.example.yaml`
- `README.md`
- `docs/ai-model-failover-design_CN.md`
- `go.mod`
- `go.sum`

## 3. 💻关键代码调整详情

- 在 `google_ai_studio` 适配器中实现懒初始化的 GenAI SDK 客户端，并支持：
  - `GenerateContent` 映射 `SystemPrompt / UserPrompt / ResponseFormat / ProviderHints`
  - `EmbedContent` 映射 `Texts / Dimension / ProviderHints`
  - 从上下文透传 `X-Trace-ID` 与 `X-Client-Request-Id`
- 在 `ai_key_failover` 中新增 provider-aware 工厂层：
  - 允许 OpenAI-compatible 与 Google AI Studio 共用同一套 key 轮换、节点预算与路由 failover 框架
  - 为 Google AI Studio 增加独立错误分类，把 `429 / quota / auth` 等错误接入现有 failover 决策模型
- 在配置层新增 provider 支持：
  - `validateLLMRouteConfigs` 接受 `google_ai_studio`
  - `embedding.provider` 接受 `google_ai_studio`
  - 对 Google AI Studio 放宽 endpoint 必填限制，允许留空使用 SDK 默认 Gemini API 根地址
- 在运行时装配层将 `buildOneLLMRouteClient` 与 `buildEmbedding` 切换为统一的 provider-aware 构建路径，避免后续新增 provider 时重复散落 switch 分支。
- 增加测试覆盖：
  - Google 适配器请求映射与响应解析测试
  - Google 路由级限流切换测试
  - 配置校验与运行时装配接受 Google provider 的测试

## 4. ⚠️遗留问题与注意事项

- 本次只实现了 Google AI Studio 的 `LLM + Embedding` 原生接入，`rerank` 仍保持现状，不支持 Google provider。
- `organization` / `project` 字段在 `google_ai_studio` 模式下当前会被忽略，配置帮助中已明确标注。
- `embedding.dimension` 在 Google 模式下会映射为 Gemini 的 `output_dimensionality`；使用时需要确保所选模型支持该维度，否则上游会返回错误。
- 已执行 `go test ./...` 与 `.\make.bat build`，当前仓库标准构建与全量测试均通过。
