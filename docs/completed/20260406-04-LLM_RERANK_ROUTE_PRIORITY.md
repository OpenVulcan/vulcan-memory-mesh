# LLM 与 Rerank 路由 Priority 扩展计划

## 任务目标

为 `llm` 与 `rerank` 的 provider / model 级 `routes` 增加 `priority` 概念。运行时在每次选择路由时，都应优先使用当前“可用且优先级最高”的 route；当高优先级 route 不可用时，再自动降级到较低优先级 route。`embedding` 不参与这次扩展，继续保持固定模型下的多 key / 多节点容灾。

## 执行步骤

1. 审查当前 `llm.routes` 与 `rerank.routes` 的配置结构、归一化逻辑以及运行时 route 选择顺序，确认 `priority` 的接入点。
2. 为 `LLMRouteConfig` 与 `RerankRouteConfig` 增加 `priority` 字段，并补充归一化、必要校验与文档说明。
3. 调整 `LLMMultiRouteClient` 与 `RerankMultiRouteClient` 的 route 选择逻辑，使其按 `priority` 从高到低选择；同优先级下保持声明顺序稳定。
4. 保持 `embedding` 不新增 route priority，也不改变其当前固定模型容灾语义。
5. 补充测试，覆盖：
   - route priority 的归一化与保留；
   - 高优先级 route 可用时优先命中；
   - 高优先级 route 不可用时降级到低优先级 route；
   - 同优先级下仍保持原声明顺序。
6. 更新中文文档，并执行定向测试与全量 `go test ./...`。

## 技术选型与实现原则

- `priority` 只作用于 `llm.routes` 与 `rerank.routes`，不作用于 `embedding`。
- `priority` 数值越大，优先级越高。
- route 的健康与预算判断仍由各自 route 内部的 `nodes + key_failover` 决定；`priority` 只影响“先尝试哪条 route”。
- 当多个 route `priority` 相同时，必须保持配置文件中的原始声明顺序，避免运行时出现不稳定漂移。

## 验收标准

- `llm.routes[*].priority` 与 `rerank.routes[*].priority` 可被正确读取并参与运行时选路。
- 高优先级 route 可用时，请求优先走高优先级 route。
- 高优先级 route 不可用时，运行时能继续降级到更低优先级 route。
- `embedding` 的配置结构和运行时行为保持不变。
- 测试、实现、文档一致，并通过仓库要求测试与 `go test ./...`。

## 执行变更总结

### 1. 核心修复与调整概述

- 为 `llm.routes` 与 `rerank.routes` 增加 `priority` 字段，用于表达 provider / model 级 route 的优先级。
- 调整多路由运行时选择顺序：每次请求都会优先尝试当前“可用且 `priority` 更高”的 route；当高优先级 route 不可用时，再继续降级到低优先级 route。
- 保持同优先级 route 的声明顺序稳定，避免运行时出现不可预期的调度漂移。
- 明确 `embedding` 不引入 route priority，也不改变其固定模型下的节点 / 多 key 容灾语义。

### 2. 📂文件变更清单

- 修改：
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `internal/app/app.go`
  - `internal/adapters/outbound/ai_key_failover/llm_multi_route.go`
  - `internal/adapters/outbound/ai_key_failover/rerank_multi_route.go`
  - `internal/adapters/outbound/ai_key_failover/key_failover_test.go`
  - `README.md`
  - `configs/vmm_config_readme.md`
  - `docs/ai-model-failover-design_CN.md`
- 新增：
  - 无
- 删除：
  - 无

### 3. 💻关键代码调整详情

- 在 `LLMRouteConfig` 与 `RerankRouteConfig` 中新增 `priority` 字段，使配置层可以直接表达 route 优先级。
- 在 `app.go` 的多路由装配逻辑中，把配置中的 `priority` 透传到运行时 route options。
- 在 `LLMMultiRouteClient` 与 `RerankMultiRouteClient` 的构建阶段使用稳定排序，按 `priority` 从高到低重排 route；当 `priority` 相同时，保留原始声明顺序。
- 补充测试，验证：
  - priority 字段会被归一化保留；
  - 高优先级 route 会优先命中；
  - 同优先级 route 仍按声明顺序工作；
  - rerank 与 llm 的路由级优先级语义一致。

### 4. ⚠️遗留问题与注意事项

- 当前 `priority` 只作用于 `llm.routes` 与 `rerank.routes`，不会影响 `nodes`，也不会影响 `embedding`。
- `priority` 只改变“先尝试哪条 route”，并不替代 route 内部的 `nodes + key_failover` 预算判断与错误分类逻辑。
- 已执行：
  - `go test ./internal/adapters/outbound/ai_key_failover ./internal/config ./internal/app`
  - `go test ./...`
