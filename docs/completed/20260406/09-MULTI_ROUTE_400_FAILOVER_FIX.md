# 任务目标

修复多路由 LLM / rerank 在首条 route 返回路由级 `400` 错误时提前中止的问题，确保异构 route 场景仍可继续尝试后续备用 route。

# 执行步骤

1. 审查 `ai_key_failover` 多路由切换逻辑，确认当前 `400` 分类在 route 级容灾中的中止点与影响范围。
2. 调整 route 级切换判定逻辑，使其在“当前 route 已失败但后续 route 仍可能成功”的场景下继续尝试备用 route。
3. 补充针对多路由 `400` 错误切换的单元测试，覆盖 LLM 与 rerank 两条链路。
4. 运行与本次修复直接相关的测试，确认修复不会破坏现有 key 级 failover 与多路由优先级行为。

# 技术选型

- 保持现有 provider 级错误分类器不变，避免把 route 级切换语义和 key 级切换语义混在同一层。
- 在多路由辅助逻辑中单独收敛“是否继续尝试下一个 route”的判定，让 route 级策略明确覆盖 `400` 的异构兼容场景。
- 通过新增精确单测锁定行为，避免未来再次把 route-local `400` 误判为全局终止条件。

# 验收标准

- 当主 route 返回 `400` 且备用 route 可处理同一请求时，LLM 多路由能够切换并成功返回结果。
- 当主 route 返回 `400` 且备用 route 可处理同一请求时，rerank 多路由能够切换并成功返回结果。
- 现有多路由优先级、同优先级顺序、路由耗尽切换等测试继续通过。
- 计划文件在任务完成后补充执行变更总结，并迁移到 `docs/completed/`。

# 执行变更总结

## 1. 核心修复与调整概述

- 修复了多路由 route 级切换逻辑对 `invalid_request` 的处理方式，不再把首条 route 的 `400` 直接视为全局终止条件。
- 保持 provider 级错误分类器与单 route / key 级 failover 逻辑不变，仅在 route 级 `shouldSwitchRoute` 中放宽切换条件，确保异构 provider / model 的备用 route 仍有机会接管请求。
- 同步修正了与新语义冲突的旧单测，并补齐 rerank 的对等回归测试。

## 2. 📂文件变更清单

- 修改：`internal/adapters/outbound/ai_key_failover/multi_route_helpers.go`
- 修改：`internal/adapters/outbound/ai_key_failover/key_failover_test.go`
- 新增并迁移完成计划：`docs/completed/20260406-09-MULTI_ROUTE_400_FAILOVER_FIX.md`

## 3. 💻关键代码调整详情

- `shouldSwitchRoute`
  - 由原先在 `errorClassInvalidRequest` 时停止 route 级容灾，调整为仅在 `errorClassNone` 与 `errorClassUnknown` 时停止。
  - 新增中英文注释，明确说明 route-local `400` 在异构多路由场景下仍可能是“当前 route 不兼容、后续 route 可兼容”的情况。
- `TestLLMMultiRouteClientGenerateContinuesAfterRouteLocalInvalidRequest`
  - 将旧的“400 必停”测试改为“400 可切备用 route”的断言，锁定本次修复目标。
- `TestRerankMultiRouteClientRerankContinuesAfterRouteLocalInvalidRequest`
  - 新增 rerank 对等测试，避免只修复 LLM 而遗漏 rerank 多路由链路。

## 4. ⚠️遗留问题与注意事项

- 当前 route 级策略会把已分类的 `invalid_request` 统一视为“可以继续尝试下一条 route”；这符合异构多路由设计目标，但也意味着真正的全局坏请求会多尝试几条备用 route 后才失败。
- 现有全量测试 `go test ./...` 已通过，说明本次改动未破坏当前仓库其他路径；后续若引入更细粒度的 route 能力差异标记，可再把“可切换的 400”收敛为更精确的分类。
