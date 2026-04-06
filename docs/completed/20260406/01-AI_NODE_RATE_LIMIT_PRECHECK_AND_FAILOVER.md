# AI 轮询节点限额预判与容灾扩展计划

## 任务目标

为当前固定模型的 AI Key 容灾机制增加“轮询节点级”的限额配置与预判能力。每个轮询节点支持配置 `RPM`、`TPM`、`RPD`，运行时在真正发起请求前先根据节点限额做本地可用性判断，必要时提前切换到下一个节点。对于一个节点下的多个 `api_keys`，共享同一套限额配置；如果同平台存在不同 `TPM / RPM / RPD` 配额，应通过配置成两个不同轮询节点来表达，而不是在同一节点内混用。

## 执行步骤

1. 审查现有 `key_failover` 配置结构与运行时 selector，明确“key pool”和“轮询节点”的当前边界。
2. 设计并实现轮询节点配置结构，为 `llm / embedding / rerank` 增加节点级 `RPM / TPM / RPD` 限额字段以及兼容默认写法。
3. 在运行时 selector 中引入节点级请求预算跟踪，支持基于请求前估算进行预判，并在预算不足时提前切换节点。
4. 保持“同一节点下多 key 共用限额配置”的约束，同时保留现有 key 级错误切换逻辑。
5. 补充测试，覆盖：
   - 节点级 `RPM / TPM / RPD` 配置加载与归一化；
   - 预算充足时继续使用当前节点；
   - 预算不足时预先切换到下一个节点；
   - 多 key 共享一个节点预算；
   - `rerank` 故障仍按 `rerank=false` 语义降级。
6. 同步更新配置与设计文档，并执行仓库规定测试与 `go test ./...`。

## 技术选型与实现原则

- 限额配置挂在“轮询节点”而不是单个 key 上；同节点多个 key 只做 key 级容灾，不拆分预算。
- 对同平台但配额不同的情况，通过多个节点表达，每个节点仍保持固定 `provider + endpoint + model`。
- 预判属于本地内存态运行时策略，不做跨重启持久化。
- 请求前预算判断应以保守策略为主，宁可提前切换，也不要把明显超配额的请求打到当前节点。

## 验收标准

- `llm / embedding / rerank` 都能声明轮询节点级 `RPM / TPM / RPD` 配置。
- 多 key 节点会共享同一套限额预算，不会把多个 key 当成多份独立配额。
- 运行时能在发请求前基于预算做预判切换，而不是只在 provider 报错后被动切换。
- 现有 key 级限流 / 鉴权 / 配额错误分类仍正常生效。
- 文档、测试与实现保持一致，并通过规定测试与 `go test ./...`。

## 执行变更总结

### 1. 核心修复与调整概述

- 为 `llm / embedding / rerank` 增加了固定模型前提下的 `nodes` 轮询节点配置，每个节点支持独立的 `rpm / tpm / rpd` 配额。
- 运行时 `ai_key_failover` 从“单 Key 池”升级为“节点级预算预判 + 节点内 Key 级容灾”两层状态机，请求发出前会先做本地预算判断。
- 保留了旧版顶层 `api_key / api_keys` 写法，并把它自动折叠为一个默认节点，避免现有配置全部失效。
- 文档口径同步收敛为“节点不是跨 provider / 跨 model route，而是同一固定模型下的额度组”。

### 2. 📂文件变更清单

- 新增：
  - `internal/adapters/outbound/ai_key_failover/request_cost.go`
- 修改：
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `internal/app/app.go`
  - `internal/app/app_test.go`
  - `internal/adapters/outbound/ai_key_failover/llm.go`
  - `internal/adapters/outbound/ai_key_failover/embedding.go`
  - `internal/adapters/outbound/ai_key_failover/rerank.go`
  - `internal/adapters/outbound/ai_key_failover/state.go`
  - `internal/adapters/outbound/ai_key_failover/selector.go`
  - `internal/adapters/outbound/ai_key_failover/key_failover_test.go`
  - `configs/vmm_config_readme.md`
  - `docs/ai-model-failover-design_CN.md`
  - `README.md`

### 3. 💻关键代码调整详情

- `internal/config/config.go`
  - 新增 `AIRoutingNodeConfig`，并在 `llm / embedding / rerank` 下补充 `rpm / tpm / rpd / nodes`。
  - 新增节点归一化、节点校验、分层配置重置与环境变量覆盖对节点的兼容逻辑。
- `internal/app/app.go`
  - 构建阶段改为把配置层节点映射成运行时 `NodeOptions`，并把启用条件切换为“节点总候选数 > 1”。
- `internal/adapters/outbound/ai_key_failover/`
  - 选择器升级为节点级预算预检查与节点内 Key 轮换并存的结构。
  - 新增请求成本估算器，LLM/Embedding/Rerank 都会在发请求前估算 `Requests/Tokens`，用于 RPM/TPM/RPD 预判。
  - LLM 成功响应后会根据 provider 返回的 `Usage` 回补实际 token 用量，减少纯估算带来的漂移。
- 测试新增覆盖：
  - 节点级 RPM 预切换
  - 节点级 TPM 预切换
  - 同节点多 Key 共享预算
  - 顶层旧写法折叠为默认节点
  - 环境变量覆盖时清空旧节点表

### 4. ⚠️遗留问题与注意事项

- 当前节点级状态仍然是纯内存态，进程重启后会重新从空状态开始计数，这是设计约束，不是缺陷。
- 顶层环境变量目前只支持覆盖默认节点写法；如果未来配置器支持可视化编辑节点列表，建议再统一规划节点级环境变量策略。
- TPM 预判基于保守估算与部分实际 usage 回补；对于不返回 usage 的链路，仍然是估算驱动，不应把它当作 provider 官方账单值。
