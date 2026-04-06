# AI Key 级容灾实现计划

## 任务目标

在当前仓库中正式落地 AI Key 级容灾能力，实现以下目标：

1. `llm`、`embedding`、`rerank` 支持在固定 `provider + endpoint + model` 前提下配置多个 API Key。
2. 运行时在限流、配额、鉴权等 key 级错误下，自动切换到下一个 key。
3. 对失败 key 做内存态冷却与恢复，不做持久化。
4. 不引入多 provider / 多 model / 多 route 容灾。
5. 保持上层接口与业务链路不变。

## 详细执行步骤

1. 扩展配置结构：
   - 为 `llm / embedding / rerank` 增加 `api_keys` 与 `key_failover`。
   - 增加 `api_key` 到 `api_keys` 的归一化逻辑。
   - 增加环境变量覆盖与配置校验。
2. 新增 key-only failover 包装层：
   - 设计 key 状态结构与选择器。
   - 设计统一错误分类。
   - 实现 `LLMClient / EmbeddingClient / RerankerClient` 的 key failover 包装器。
3. 调整组合根：
   - 在 `internal/app/app.go` 中装配新的 key failover 包装器。
4. 补充或增强测试：
   - `internal/config`
   - `internal/app`
   - 新增 failover 包装层测试
5. 同步文档：
   - 更新配置说明文档
   - 如有必要更新示例配置
6. 自检并运行测试。
7. 在计划文件末尾追加执行变更总结。
8. 完成后迁移到 `docs/completed/`。

## 技术选型

### 配置层

- 继续沿用当前 `Config` 结构。
- 在三类 AI 配置上扩展：
  - `APIKeys []string`
  - `KeyFailover KeyFailoverConfig`

### 运行时层

- 新增 `internal/adapters/outbound/ai_key_failover/`。
- 该层只做：
  - key 选择
  - key 冷却
  - key 恢复
  - key 级错误分类

### 风险控制

- 只允许同一配置下切换 key。
- 400 / bad request / 参数错误不切 key。
- 5xx / timeout / network 默认不切 key。
- 429 / quota / 401 / 403 / invalid key 才触发 key 切换。

## 验收标准

1. `llm / embedding / rerank` 都能配置多个 key。
2. 旧配置只写 `api_key` 时仍兼容。
3. key failover 包装器已接入组合根。
4. 限流 / 配额 / 鉴权错误可触发 key 切换。
5. `embedding` 不支持切模型或切 provider。
6. 测试通过，文档同步完成。

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已在配置层正式引入 `api_keys` 与 `key_failover`，并保留旧 `api_key` 的兼容写法。
- 已实现新的 `internal/adapters/outbound/ai_key_failover/` 包装层，为 `llm / embedding / rerank` 提供固定模型下的 API Key 级容灾。
- 已在组合根中接入新的 key failover 包装器，运行时现在会基于固定 `provider + endpoint + model` 做 key 轮换。
- 已明确将容灾动作收敛为 key 级：
  - 429 / quota / 401 / 403 会切 key
  - 400 / 5xx / timeout 默认不切 key
- 已同步更新配置说明和示例配置。

### 2. 📂文件变更清单

- 新增：`internal/adapters/outbound/ai_key_failover/state.go`
- 新增：`internal/adapters/outbound/ai_key_failover/classifier.go`
- 新增：`internal/adapters/outbound/ai_key_failover/selector.go`
- 新增：`internal/adapters/outbound/ai_key_failover/llm.go`
- 新增：`internal/adapters/outbound/ai_key_failover/embedding.go`
- 新增：`internal/adapters/outbound/ai_key_failover/rerank.go`
- 新增：`internal/adapters/outbound/ai_key_failover/key_failover_test.go`
- 修改：`internal/config/config.go`
- 修改：`internal/config/config_test.go`
- 修改：`internal/app/app.go`
- 修改：`internal/app/app_test.go`
- 修改：`configs/vmm_config_readme.md`
- 修改：`configs/local.json`
- 修改：`configs/openai.local.example.json`

### 3. 💻关键代码调整详情

- 在 `internal/config/config.go` 中新增：
  - `KeyFailoverConfig`
  - `LLMConfig.APIKeys`
  - `EmbeddingConfig.APIKeys`
  - `RerankConfig.APIKeys`
  - `api_key -> api_keys` 归一化逻辑
  - 多 key 环境变量覆盖逻辑
- 在 `internal/adapters/outbound/ai_key_failover/` 中新增：
  - 选择器与状态机
  - OpenAI-compatible / DashScope 错误分类
  - `LLMClient / EmbeddingClient / RerankerClient` 包装实现
- 在 `internal/app/app.go` 中调整：
  - `buildLLM`
  - `buildEmbedding`
  - `buildReranker`
  使其统一装配 key failover 包装器
- 在 `embedding` 包装器中强制固定：
  - `model`
  - `dimension`
  防止运行时误切模型或误切维度

### 4. ⚠️遗留问题与注意事项

- 当前 `key_failover` 的 `RespectRetryAfter` 与 `ProbeAfterCooldown` 默认由 `DefaultLocal()` 提供；如果未来要支持“显式配置为 false 并与默认值精确区分”，建议后续改为指针布尔或显式 presence 方案。
- 当前对 `5xx / timeout / network` 采取保守策略：默认不切 key，这符合本轮设计收敛目标，但如果未来要支持可选的更激进策略，需要额外配置开关。
- 本轮已运行：
  - `go test ./internal/config ./internal/app ./internal/adapters/outbound/ai_key_failover`
  - `go test ./...`
