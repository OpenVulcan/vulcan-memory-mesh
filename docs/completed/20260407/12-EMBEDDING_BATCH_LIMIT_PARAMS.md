# 任务计划：补充向量模型批处理能力参数

## 任务目标

为当前 `embedding` / 向量模型配置增加两项显式能力参数：

1. 批量向量查询支持的最大批量值。
2. 每批内单条数据允许的最大 Tokens。

本次改动不仅要把参数加到配置结构里，还要真正接入运行时执行链路，确保：

- 配置可加载、归一化、校验。
- 运行时批量 embedding 调用会按新参数执行约束。
- 离线向量重建等批处理路径不再依赖硬编码批大小。
- 文档与示例配置同步更新，避免后续继续误用旧约定。

## 详细执行步骤

1. 复核 `EmbeddingConfig`、配置归一化/校验逻辑以及当前 embedding 客户端的批处理实现，确认现有硬编码位置。
2. 为 `embedding` 配置增加两个新参数，并接入：
   - 默认值
   - `Normalize`
   - `Validate`
   - 示例配置与必要文档
3. 调整运行时 embedding 链路：
   - 让 provider 适配器使用新的最大批量参数，而不是继续写死 `10`。
   - 为单条文本增加 token 上限校验，避免超长数据混入一批请求后才在 provider 侧失败。
4. 调整离线向量重建等批处理调用点，让其批次拆分与 embedding 配置保持一致。
5. 补充/更新测试：
   - 配置归一化与校验测试
   - OpenAI / Google embedding 适配器约束测试
   - 向量重建批处理相关测试（如有必要）
6. 运行至少覆盖配置层、embedding 适配层和应用层的定向测试；若影响面较大，则执行全量回归。

## 技术选型与处理原则

- 参数沿用现有 `embedding` 配置树，不额外引入新的并行配置块。
- 运行时限制优先在 embedding 适配层做最后一道防线，避免调用方遗漏时仍能被 provider 侧硬错误击穿。
- 离线批处理路径优先复用同一套配置来源，避免再次出现“在线链路和离线路径各写一套硬编码”的漂移。
- 命名以清晰表达“批上限”和“单条 token 上限”为准，避免和总 TPM / provider hints 混淆。

## 验收标准

1. `embedding` 配置中可以声明新的最大批量值和单条最大 token 值，并通过加载、归一化和校验。
2. OpenAI-compatible embedding 适配器不再把批大小固定写死为 `10`，而是受配置驱动。
3. embedding 请求中若某条文本超过配置允许的最大 token 数，会在本地明确失败，而不是交给上游 provider 随机报错。
4. 向量重建等离线批处理路径会遵循同一套最大批量配置，不再单独写死批大小。
5. 相关测试通过，且不会破坏现有 embedding / 配置契约。

## 执行变更总结

### 1. 核心修复与调整概述

- 为 `embedding` 配置新增 `max_batch_size` 与 `max_input_tokens_per_text` 两个显式参数，并接入默认值、归一化、校验和环境变量覆盖。
- 调整 `ai_key_failover` 的 embedding 包装器，让运行时可以按配置自动拆分超大批次，并在发出请求前先执行单条文本 token 上限校验。
- 同步扩展 OpenAI-compatible 与 Google AI Studio embedding 适配器的本地限制校验，使直接构造 provider 客户端的路径也能遵守同一套模型能力约束。
- 把 `postaction`、主动写记忆和 `vector_rebuild` 的硬编码批次改为受 `embedding.max_batch_size` 驱动，避免运行时与离线维护链路继续漂移。
- 更新配置示例与 AI failover 设计文档，并补齐配置层、包装器、provider 适配器和应用层的测试覆盖。

### 2. 📂 文件变更清单

- 修改：
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `internal/adapters/outbound/ai_key_failover/embedding.go`
  - `internal/adapters/outbound/ai_key_failover/provider_factory.go`
  - `internal/adapters/outbound/ai_key_failover/key_failover_test.go`
  - `internal/adapters/outbound/openai_native/embedding.go`
  - `internal/adapters/outbound/openai_native/embedding_test.go`
  - `internal/adapters/outbound/google_ai_studio/embedding.go`
  - `internal/adapters/outbound/google_ai_studio/embedding_test.go`
  - `internal/app/app.go`
  - `internal/app/usecase/memory_query.go`
  - `internal/app/usecase/postaction.go`
  - `internal/app/vector_rebuild.go`
  - `internal/testutil/realruntime.go`
  - `configs/base.yaml`
  - `configs/config.yaml`
  - `configs/openai.config.example.yaml`
  - `configs/google_ai_studio.config.example.yaml`
  - `docs/ai-model-failover-design_CN.md`
- 新增：
  - 无
- 删除：
  - 无

### 3. 💻 关键代码调整详情

- 在 `EmbeddingConfig` 中新增：
  - `max_batch_size`
  - `max_input_tokens_per_text`
- 在配置归一化中为 `max_batch_size` 补齐默认值 `10`，并在校验阶段强制：
  - `embedding.max_batch_size > 0`
  - `embedding.max_input_tokens_per_text >= 0`
- 在 `applyEnvOverrides` 中新增：
  - `VMM_EMBED_MAX_BATCH_SIZE`
  - `VMM_EMBED_MAX_INPUT_TOKENS_PER_TEXT`
- 在 `ai_key_failover.EmbeddingClient` 中新增统一拆批与 token 校验逻辑：
  - 会先标准化文本输入
  - 会按配置的最大批量自动拆分
  - 会在本地拒绝超过单条 token 上限的文本
  - 每个子批次继续复用现有的 key failover 与预算守卫
- 在 `postaction` / `memory_query` / `vector_rebuild` 中去掉写死的 `10`，改为使用当前配置批宽。
- 在 provider 直连适配器测试中补充了：
  - 批量超限失败
  - 单条 token 超限失败
- 在 `ai_key_failover` 测试中补充了超大 embedding 请求自动拆批行为验证。

### 4. ⚠️ 遗留问题与注意事项

- `max_input_tokens_per_text` 当前采用运行时统一的保守 token 估算器做本地预检查，它不是 provider 官方 tokenizer 的精确值；设计上它用于“提前防守”，而不是做字节级精算。
- 当前默认只把 `max_batch_size` 设为 `10` 以保持历史兼容；`max_input_tokens_per_text` 默认仍为 `0`，表示不开启额外本地限制，需要按实际模型能力自行配置。
- 本轮已执行 `go test ./...`，当前全量通过。
