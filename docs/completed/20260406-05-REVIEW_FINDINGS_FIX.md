# 任务目标

修复本轮代码审查指出的两个问题，确保：

1. 多路由 AI 容灾在单个 provider 的内部 HTTP 超时场景下，仍能继续切换到后续 route。
2. 当显式声明 `llm.routes` / `rerank.routes` 时，现有顶层配置与环境变量覆盖能力不会失效，仍然符合仓库既有“后加载覆盖前加载、环境变量优先”的契约。

# 详细执行步骤

1. 梳理当前多路由失败中止条件
   - 检查 `shouldAbortRouteFailover` 与错误分类器之间的边界。
   - 区分“调用方 context 已取消/超时”与“provider 自身请求超时”两类错误。
2. 修复 route failover 中止逻辑
   - 仅在外层请求上下文真实失效时停止 route failover。
   - 保留 provider 超时、公共故障走后续 route 的能力。
3. 梳理 route 模式下的配置覆盖链路
   - 检查 `buildLLM` / `buildReranker`、`ProviderRoutes()`、`LoadPaths()`、`applyEnvOverrides()` 的协作关系。
   - 明确顶层 legacy 字段在 route 模式下的覆盖语义。
4. 修复 route 模式下的顶层覆盖失效问题
   - 让高优先级顶层字段在 route 模式启用时仍能影响最终运行时装配，且不破坏已有 route 自包含语义。
   - 同步处理 `llm` 与 `rerank` 两条链路。
5. 补充与更新测试
   - 增加 route failover 超时场景测试。
   - 增加 route 模式下顶层配置层覆盖与环境变量覆盖测试。
6. 执行验证
   - 运行与本次改动相关的 Go 测试。
   - 对照审查意见逐项确认修复闭环。

# 技术选型

1. 继续复用现有 `failureDecision` 与 provider classifier，不引入新的容灾状态模型。
2. 通过最小范围调整 route 中止条件与 route 解析逻辑完成修复，避免扩大到整体配置模型重构。
3. 通过测试锁定以下两个回归面：
   - provider 内部超时不应阻断 route failover；
   - route 模式不应吞掉顶层高优先级覆盖。

# 验收标准

1. provider 内部超时错误出现时，多路由客户端能够继续尝试后续 route。
2. 外层调用 context 真实取消或超时时，route failover 仍会立即停止。
3. 显式声明 `llm.routes` / `rerank.routes` 时，顶层高优先级配置覆盖与环境变量覆盖仍然生效。
4. 新增/更新测试通过，且没有引入与本次修复直接相关的新失败。

# 执行变更总结

## 1. 核心修复与调整概述

本次已完成两项审查问题修复：

1. 修正多路由容灾的中止条件，避免把 provider 自身包装出来的 `context.DeadlineExceeded` 误判成外层调用已超时，从而提前终止后续 route failover。
2. 为 `llm.routes` 与 `rerank.routes` 补回“显式顶层 legacy 字段覆盖”能力，让更高优先级配置层与环境变量在 route 模式下仍能影响最终运行时装配，同时避免 `DefaultLocal()` 默认值反向污染显式 routes。

## 2. 📂文件变更清单

新增：

1. `docs/plan/20260406-05-REVIEW_FINDINGS_FIX.md`

修改：

1. `internal/adapters/outbound/ai_key_failover/multi_route_helpers.go`
2. `internal/adapters/outbound/ai_key_failover/key_failover_test.go`
3. `internal/config/config.go`
4. `internal/config/config_test.go`
5. `README.md`
6. `configs/vmm_config_readme.md`
7. `docs/ai-model-failover-design_CN.md`

删除：

1. 无

## 3. 💻关键代码调整详情

1. 调整 `shouldAbortRouteFailover`
   - 现在仅当外层调用 `ctx.Err()` 真实变成 `context.Canceled` / `context.DeadlineExceeded` 时才停止 route failover。
   - provider 自身 HTTP client 包装出来的超时错误会继续进入分类逻辑，并允许切换下一条 route。
2. 在配置层新增显式覆盖跟踪
   - 为 `LLMConfig` 与 `RerankConfig` 增加内部使用的 legacy route override presence 标记。
   - 在 `LoadPaths()` 的分层解析过程中记录哪些顶层 legacy 字段是“当前更高优先级层显式声明”的。
   - 当同层显式写入 `routes` 时，先清空更早层遗留的 legacy 覆盖标记，避免旧层顶层字段反向覆盖新层 routes。
3. 在环境变量覆盖阶段同步打标
   - 让 `VMM_LLM_*` / `VMM_RERANK_*` 的 legacy 顶层环境变量覆盖在 route 模式下继续生效。
4. 在 `ProviderRoutes()` 中重新应用显式顶层覆盖
   - 仅把“被显式写入过的”顶层 legacy 字段叠加到显式 routes。
   - 未显式写入的 fallback 默认值不会自动覆盖 routes。
   - 对 `api_key / api_keys / nodes` 采用整体替换节点池策略；对单独预算字段采用节点预算覆盖策略。
5. 补充回归测试
   - 增加“包装后的 route 超时仍继续 failover”的测试。
   - 增加“后加载配置层 legacy 顶层字段仍能覆盖显式 routes”的测试。
   - 增加“环境变量 legacy 顶层字段仍能覆盖显式 routes”的测试。

## 4. ⚠️遗留问题与注意事项

1. 本次兼容的是“显式写入的顶层 legacy 字段”对显式 routes 的运行时覆盖，不代表重新允许 route 依赖顶层默认值补全缺失字段。
2. `embedding` 仍然保持固定 provider / endpoint / model / dimension，不在本次 route 覆盖修复范围内。
3. 本次验证已执行：
   - `go test ./internal/config ./internal/adapters/outbound/ai_key_failover`
   - `go test ./internal/app -run 'TestBuildLLMUsesMultiRouteWrapper|TestBuildRerankerUsesMultiRouteWrapper|TestLoadPathsLatestLegacyFieldsOverrideExplicitRoutes|TestApplyEnvOverridesLegacyRouteFieldsOverrideExplicitRoutes|TestLLMMultiRouteClientGenerateContinuesAfterWrappedRouteTimeout'`
