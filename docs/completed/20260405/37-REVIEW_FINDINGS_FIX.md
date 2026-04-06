# Review Findings 修复计划

## 任务目标

修复当前未提交改动中两个已经确认的审查问题，确保：

1. 多层配置文件叠加时，后加载配置中的旧字段 `api_key` 仍然可以按预期覆盖前序层中的 `api_keys`。
2. OpenAI 兼容链路中的 `403` 错误不会被一概误判为单个 Key 失效，避免把共享权限错误错误扩散到整个 Key 池。
3. 修复后相关配置归一化、错误分类和容灾行为均有测试覆盖，并通过仓库规定的验证。

## 执行步骤

1. 审阅 `internal/config/config.go` 的多层配置加载与归一化链路，确认当前 `api_key` / `api_keys` 的合并顺序与覆盖语义。
2. 调整配置归一化逻辑，使高优先级配置层中的旧字段 `api_key` 可以显式压过较低优先级层遗留的 `api_keys` 池。
3. 为配置覆盖回归补充单元测试，覆盖“前层 `api_keys` + 后层 `api_key`”的实际场景。
4. 审阅 `internal/adapters/outbound/ai_key_failover/classifier.go` 中 OpenAI 兼容错误分类逻辑，收敛 `403` 的切 Key 条件。
5. 为 `403` 共享权限错误补充单元测试，确保该场景不会错误熔断整个 Key 池。
6. 运行受影响包测试与 `go test ./...` 做回归验证。
7. 在计划末尾补充执行变更总结，确认与计划一致后迁移到 `docs/completed/`。

## 技术选型及实现原则

- 保持对旧字段 `api_key` 的兼容，但不能让它破坏原有“后加载层优先”的配置叠加语义。
- 优先采用小范围、可解释的归一化修复，不引入额外复杂状态或破坏现有环境变量覆盖规则。
- 对 `403` 分类采取保守策略：只有能明确证明是 Key 级鉴权失败时才切 Key；对共享权限错误应直接暴露原始问题。
- 测试优先覆盖真实回归场景，而不是只验证辅助函数的局部行为。

## 验收标准

- 多层配置加载时，后加载文件中的 `api_key` 能覆盖前层文件中的 `api_keys`。
- OpenAI 兼容 `403` 共享权限错误不会触发整池 Key 轮换或错误冷却。
- 新增测试能够稳定复现并守护上述行为。
- 至少通过相关包测试，并完成一次 `go test ./...`。

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已在分层配置加载阶段补充 AI Key 字段存在性识别，修复“高优先级 `api_key` 无法覆盖低优先级 `api_keys`”的问题。
- 已同步修复相反方向的残留继承风险：后层若显式声明 `api_keys`，也不会再悄悄继承前层旧的 `api_key`。
- 已收敛 OpenAI 兼容 `403` 的错误分类逻辑，只有明确指向坏 Key / 被吊销 Key 的 403 才会触发切 Key；共享权限错误将直接上抛。
- 已补充配置分层与 403 分类的回归测试，并完成全量 `go test ./...` 验证。

### 2. 文件变更清单

- 新增：
  - `docs/plan/20260405-37-REVIEW_FINDINGS_FIX.md`
- 修改：
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `internal/adapters/outbound/ai_key_failover/classifier.go`
  - `internal/adapters/outbound/ai_key_failover/key_failover_test.go`
  - `docs/plan/20260405-37-REVIEW_FINDINGS_FIX.md`
- 删除：
  - 无

### 3. 关键代码调整详情

- 在 `LoadPaths` 中新增分层 AI Key 字段预处理逻辑：
  - 当前层只写 `api_key` 时，先清空旧层遗留的 `api_keys`
  - 当前层只写 `api_keys` 时，先清空旧层遗留的 `api_key`
  - 这样既保留了单层内“旧字段 + 新字段可合并”的兼容行为，也恢复了多层配置“后层优先”的覆盖语义
- 在 `classifyOpenAIError` 中拆分了 `401` 与 `403`：
  - `401` 仍按 Key 级鉴权失败处理
  - `403` 只有在错误内容明确包含 invalid / revoked / disabled key 语义时才切 Key
  - 泛化的模型/组织/项目权限错误改为停止 failover，直接暴露原始问题
- 新增测试覆盖：
  - 后层 `api_key` 覆盖前层 `api_keys`
  - 后层 `api_keys` 覆盖前层 `api_key`
  - 共享 403 权限错误不切 Key
  - Key 级 403 仍可切 Key

### 4. 遗留问题与注意事项

- 本轮修复只收敛了 OpenAI 兼容链路的 `403` 判定；如果未来接入新的兼容 provider，仍需要按其错误语义补充更精细的 Key 级判定。
- 当前分层覆盖修复聚焦于 `llm`、`embedding`、`rerank` 三类 AI Key 字段，符合本轮需求范围。
- 本轮已执行：
  - `go test ./internal/config`
  - `go test ./internal/adapters/outbound/ai_key_failover`
  - `go test ./...`
