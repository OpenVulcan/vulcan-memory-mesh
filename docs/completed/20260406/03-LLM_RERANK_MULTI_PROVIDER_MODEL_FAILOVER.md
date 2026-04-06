# LLM 与 Rerank 多 Provider / 多 Model 容灾扩展计划

## 任务目标

在保持 `embedding` 仍然只支持“固定模型下多 Key 容灾”的前提下，为 `llm` 与 `rerank` 扩展多 `provider`、多 `model` 的容灾能力。新的运行时需要支持在单条能力链路中按配置声明多个候选路由，当当前路由不可用时，按既定顺序或策略切换到下一个 `provider + endpoint + model` 组合。

## 执行步骤

1. 审查当前 `config`、`app` 装配层以及 `ai_key_failover` 的职责边界，确认现有实现中哪些部分默认假设了“固定 provider / 固定 model”。
2. 设计 `llm` 与 `rerank` 的多路由配置结构与兼容策略，确保旧配置仍可自动归一化为单路由写法。
3. 实现 `llm` 与 `rerank` 的多路由运行时包装器，使其在路由级失败时支持切换到不同 `provider / model`，同时保持单路由内部仍沿用现有 key failover 逻辑。
4. 明确 `embedding` 不参与多模型 / 多 provider 容灾，并保持当前固定模型约束不变。
5. 补充测试，覆盖：
   - `llm` 多 provider / 多 model 路由归一化与校验；
   - `rerank` 多 provider / 多 model 路由归一化与降级；
   - 单路由内部 key failover 与多路由切换的组合行为；
   - `embedding` 仍拒绝多模型容灾配置。
6. 同步更新中文文档，明确三类能力的容灾边界差异，并执行规定测试与全量 `go test ./...`。

## 技术选型与实现原则

- `llm` 与 `rerank` 的路由级容灾应独立于单路由内部的 key failover；先选路由，再在该路由内部选 key。
- 旧版单配置写法必须继续可用；未显式声明多路由时，运行时自动折叠为单路由。
- `embedding` 明确保持固定 `provider + endpoint + model + dimension`，不引入路由级切换。
- 配置、实现、测试与文档必须统一表达“LLM / rerank 可多路由，embedding 不可多路由”的边界。

## 验收标准

- `llm` 与 `rerank` 支持显式声明多个 `provider + endpoint + model` 路由，并能在失败时切换。
- 单路由内部仍保留已有的 key 轮换、额度预判与冷却行为。
- `embedding` 继续只支持固定模型下多 key 容灾，不会混入多模型 / 多 provider 路由。
- 旧版单路由配置保持兼容，归一化与校验行为明确。
- 文档、测试与实现对齐，并通过仓库要求测试与 `go test ./...`。

## 执行变更总结

### 1. 核心修复与调整概述

- 为 `llm` 与 `rerank` 新增显式 `routes` 配置结构，支持在不同 `provider + endpoint + model` 之间按声明顺序做路由级容灾。
- 保留单条 route 内部原有的 `nodes + key_failover` 机制，使路由级切换与 key 级切换分层协作。
- 明确 `embedding` 继续只支持固定模型下的节点 / 多 key 容灾，不引入 `routes`。
- 同步修正文档口径，明确 `nodes` 表示同一条固定模型 route 下的配额档位分组，而 `routes` 才表示 provider/model 级切换。

### 2. 📂文件变更清单

- 新增：
  - `internal/adapters/outbound/ai_key_failover/errors.go`
  - `internal/adapters/outbound/ai_key_failover/multi_route_helpers.go`
  - `internal/adapters/outbound/ai_key_failover/llm_multi_route.go`
  - `internal/adapters/outbound/ai_key_failover/rerank_multi_route.go`
- 修改：
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `internal/app/app.go`
  - `internal/app/app_test.go`
  - `internal/adapters/outbound/ai_key_failover/selector.go`
  - `internal/adapters/outbound/ai_key_failover/state.go`
  - `internal/adapters/outbound/ai_key_failover/key_failover_test.go`
  - `README.md`
  - `configs/vmm_config_readme.md`
  - `docs/ai-model-failover-design_CN.md`
- 删除：
  - 无

### 3. 💻关键代码调整详情

- 在 `config.go` 中为 `LLMConfig` 与 `RerankConfig` 增加 `routes`，并补充对应的归一化、字段裁剪、默认值与校验逻辑。
- 在 `app.go` 中为 `llm` 与 `rerank` 增加 route 级运行时装配；当存在多条 route 时，改为构建多路由包装器。
- 在 `ai_key_failover` 中新增 `LLMMultiRouteClient` 与 `RerankMultiRouteClient`，实现“先选 route，再在 route 内部做 key failover”的双层切换。
- 为单路由 Key 池耗尽增加类型化错误，保证外层 route 包装器可以准确识别“当前 route 已无可用候选”并继续切换。
- 新增与补充测试，覆盖 route 归一化、显式 route 校验、路由级切换、按模型精确选路，以及无效请求不应盲目扩散到其他 route 的行为。

### 4. ⚠️遗留问题与注意事项

- 当前 `rerank.routes[*].provider` 的运行时内置支持仍只有 `dashscope`；本次先把多 route 框架与配置结构打通，后续如新增 provider，只需补适配器与构建分支。
- 环境变量覆盖目前仍主要覆盖旧版顶层单 route 字段；如果后续需要通过环境变量直接管理 `routes`，需单独设计 route 级覆盖约定。
- 已执行：
  - `go test ./internal/adapters/outbound/ai_key_failover ./internal/config ./internal/app`
  - `go test ./...`
