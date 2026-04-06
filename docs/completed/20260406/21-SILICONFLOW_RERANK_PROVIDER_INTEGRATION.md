# 任务计划：接入硅基流动 Rerank 接口

## 1. 任务目标

在现有 rerank 模型与多供应商接入体系中，增加对硅基流动（SiliconFlow）Rerank 接口的支持，确保能够通过配置选择该供应商完成重排请求，并与现有错误处理、路由选择、结果解析和测试体系保持一致。

## 2. 执行步骤

1. 梳理当前仓库中 rerank 模型配置、供应商适配器、调用入口和测试覆盖点，明确新增供应商所需修改的模块边界。
2. 对照硅基流动官方 Rerank 接口文档，确认请求路径、鉴权方式、请求结构、返回结构以及与现有抽象层的映射关系。
3. 在不破坏现有依赖方向和多供应商架构的前提下，实现硅基流动 Rerank 供应商适配逻辑，并补齐必要的配置解析与模型信息传递。
4. 为新增逻辑补充或调整测试，覆盖请求构造、响应解析、错误处理和配置装配路径。
5. 按仓库规范执行相关测试，确认 rerank 供应商扩展未影响已有链路行为。
6. 完成自检后，在本文末尾追加执行变更总结，并将计划文件迁移至 `docs/completed/20260406/`。

## 3. 技术选型与实现原则

- 优先复用现有 rerank provider 抽象与 HTTP 调用基础设施，避免引入额外运行时分支。
- 严格遵守仓库当前的分层规则，保证 `adapters -> app -> logic/domain` 的依赖方向不被破坏。
- 按官方文档对接硅基流动接口，不臆造字段语义；如存在差异，优先通过适配层完成协议转换。
- 新增或调整代码时遵守仓库双语注释规范，重点说明配置入口、请求转换和异常处理的设计意图。

## 4. 验收标准

- 配置层能够识别并正确装配硅基流动 rerank 供应商。
- 运行时能够向硅基流动官方 rerank 接口发起合法请求，并将结果转换为现有领域层所需结构。
- 现有 rerank 相关链路不发生回归，多供应商场景下原有行为保持稳定。
- 相关测试通过，且本次修改涉及的关键模块具备可回归的自动化验证。
- 文档化变更记录完整，计划闭环后按规范迁移归档。

## 执行变更总结

### 1. 核心修复与调整概述

- 已新增 `siliconflow_rerank` 出站适配器，按硅基流动官方 `POST /v1/rerank` 契约完成请求构造、结果解析和结构化错误透传。
- 已将现有 rerank Key 容灾包装器从 DashScope 固定实现抽象为 provider 感知实现，支持 `dashscope` 与 `siliconflow` 共用统一的 key failover / route failover 语义。
- 已扩展 rerank 配置归一化与校验逻辑，使 `rerank.routes[*].provider` 支持 `siliconflow`，并为其补齐默认 endpoint 与默认 model。
- 已同步更新配置示例、设计文档与 README，消除“rerank 仅支持 DashScope”的过时描述。
- 已完成定向测试与 `go test ./...` 全量回归，确认现有链路未被破坏。

### 2. 📂文件变更清单

- 新增：
  - `internal/adapters/outbound/siliconflow_rerank/client.go`
  - `internal/adapters/outbound/siliconflow_rerank/client_test.go`
- 修改：
  - `internal/adapters/outbound/ai_key_failover/classifier.go`
  - `internal/adapters/outbound/ai_key_failover/key_failover_test.go`
  - `internal/adapters/outbound/ai_key_failover/rerank.go`
  - `internal/adapters/outbound/ai_key_failover/rerank_multi_route.go`
  - `internal/app/app.go`
  - `internal/app/app_test.go`
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `configs/base.yaml`
  - `configs/config.yaml`
  - `configs/openai.config.example.yaml`
  - `configs/google_ai_studio.config.example.yaml`
  - `docs/ai-model-failover-design_CN.md`
  - `README.md`
- 删除：
  - 无

### 3. 💻关键代码调整详情

- 在 `siliconflow_rerank` 适配器中新增独立的请求/响应模型，采用顶层 `model/query/documents/top_n/return_documents` 请求结构，并将返回的 `results[index/relevance_score]` 映射回内部文档 ID。
- 在 `ai_key_failover` 中新增 provider 钩子分发逻辑，把“具体客户端构造”和“错误分类器选择”集中到同一入口，避免单路由与多路由装配出现分叉实现。
- 将 DashScope 专属的 rerank HTTP 错误分类逻辑抽取为可复用的结构化/文本回退分类流程，并补充 SiliconFlow 429/Retry-After 场景覆盖。
- 在 `config` 层新增 SiliconFlow provider 归一化逻辑与 provider 级默认值映射，确保空 endpoint/model 时不会错误继承 DashScope 默认值。
- 在 `app` 层将单路由 rerank 构建切换为 provider 感知入口，避免运行时构建仍把非 DashScope provider 当成不支持。

### 4. ⚠️遗留问题与注意事项

- 当前接入优先覆盖硅基流动 rerank 的标准字段：`model / query / documents / top_n / return_documents`；官方可选扩展字段如 `instruction`、`max_chunks_per_doc`、`overlap_tokens` 暂未暴露到仓库配置层。
- 默认 SiliconFlow rerank 模型采用官方文档示例 `BAAI/bge-reranker-v2-m3`；如果后续需要切换到其他硅基流动模型，可直接通过 `rerank.routes[*].model` 覆盖。
- 已执行验证：
  - `go test ./internal/adapters/outbound/ai_key_failover ./internal/adapters/outbound/dashscope_rerank ./internal/adapters/outbound/siliconflow_rerank ./internal/app ./internal/config`
  - `go test ./...`
