## 任务目标

修复本轮代码审查指出的两个问题，确保：

1. LLM 多路由容灾在不同模型名之间也能真正发生，而不是被主路由模型名固定住。
2. live runtime 测试夹具能够正确识别仅通过 `nodes[].api_keys` 声明 Key 的合法 LLM 路由配置。

## 详细执行步骤

1. 审查当前 `internal/app/app.go` 中 LLM 处理器装配方式，确认哪些处理器把主模型名固定写入 `LLMRequest.Model`，以及这对 `LLMMultiRouteClient` 的路由筛选造成的影响。
2. 设计并实现兼容修复：
   - 保留提示词选择与诊断所需的主模型语义；
   - 取消会阻断多路由容灾的“请求级强制固定模型”行为；
   - 确保单路由与多路由场景都保持兼容。
3. 修复 `internal/testutil/realruntime.go` 的 live fixture 完整性判断逻辑，使其兼容 node-only 的合法 LLM route。
4. 补充或调整测试，覆盖：
   - 不同模型名的多路由场景不会因处理器固定模型而失去容灾能力；
   - live runtime fixture 对 node-only route 的识别行为正确。
5. 运行相关测试并进行闭环验证，确认修复未引入新的回归。

## 技术选型及策略

- 优先采用最小侵入、语义清晰的修复方案，避免为了通过当前审查而破坏现有 prompt 选择、模型诊断或单路由兼容行为。
- 若处理器内部必须区分“提示词主模型”和“真正发请求的路由模型”，则通过显式参数语义拆分处理，而不是继续复用一个字段承担两种职责。
- live fixture 的修复必须与运行时真实支持的配置契约保持一致，不能在测试层额外收窄合法配置范围。

## 验收标准

1. `internal/app/app.go` 中不再因为固定 `PrimaryModel` 而阻断不同模型名之间的多路由容灾。
2. `internal/testutil/realruntime.go` 能接受仅通过 `nodes[].api_keys` 配置 Key 的合法 LLM route。
3. 相关新增或调整测试通过，且至少通过与本次改动相关的 Go 测试。
4. 修复完成后，在本文末尾追加「执行变更总结」，再将文件迁移到 `docs/completed/`。

## 执行变更总结

### 1. 核心修复与调整概述

- 已修复 `internal/app/app.go` 中处理器统一固定 `PrimaryModel` 的问题：现在提示词仍沿用主模型进行 prompt 路由，但处理器发起的 LLM 请求在多路由模式下不再把模型名硬编码进 `LLMRequest.Model`，从而允许 `LLMMultiRouteClient` 在不同模型名的 route 之间继续容灾切换。
- 已修复 `internal/testutil/realruntime.go` 中 live runtime fixture 只接受顶层 `api_keys` 的问题：现在会从顶层 Key 池或节点级 `nodes[].api_keys` 中提取首个可用 Key，兼容 node-only 的合法配置形态。
- 顺手补齐了 embedding fixture 的同类 Key 提取逻辑，避免同样的 node-only 形态在测试基座里再次被误判。

### 2. 📂文件变更清单

- 修改：`internal/app/app.go`
- 修改：`internal/app/app_test.go`
- 修改：`internal/testutil/realruntime.go`
- 新增：`internal/testutil/realruntime_test.go`
- 修改：`docs/plan/20260406-07-MULTI_ROUTE_FAILOVER_REVIEW_FIX.md`

### 3. 💻关键代码调整详情

- 在 `internal/app/app.go` 中新增 `routeFailoverAwareLLMClient` 与 `adaptLLMForProcessorRoutes`：
  - 单路由场景保持原样，不改变处理器请求模型；
  - 多路由场景下清空处理器发起请求中的 `Model` 字段，只保留主模型用于 prompt 目录选择；
  - 这样既不破坏现有 prompt 路由，又不会把多路由容灾错误地锁死在主模型上。
- 在 `newApplication` 中把 `llm` 拆分成：
  - `llmPromptModel`：继续用于 prompt 选择；
  - `processorLLM`：专供处理器调用的 LLM 客户端包装器。
- 在 `internal/testutil/realruntime.go` 中新增 `firstRoutingAPIKey`：
  - 统一从顶层 `api_keys` 或节点级 `nodes[].api_keys` 提取首个可用 Key；
  - live fixture 对 LLM 与 embedding 都改为基于该逻辑判定配置是否完整并构造真实客户端。
- 在测试侧补充了两类回归校验：
  - `internal/app/app_test.go`：验证多路由时会清空处理器请求模型，单路由时仍保留；
  - `internal/testutil/realruntime_test.go`：验证顶层 Key 池、node-only Key 池、缺失 Key 三种形态的提取行为。

### 4. ⚠️遗留问题与注意事项

- 当前修复聚焦于“运行时处理器请求不要阻断多路由容灾”与“live fixture 正确识别合法 Key 声明形态”，未进一步改动 prompt 路由策略；也就是说，多路由场景下 prompt 仍以主模型目录为准，这是当前实现下最稳妥且兼容性最好的方案。
- 已执行 `go test ./internal/app ./internal/testutil` 与 `go test ./...`，均已通过。
