# 全局代码审核与运行时加固执行计划

## 1. 任务目标

本阶段在第 09 阶段完成一轮全局审核与稳定性修复的基础上，继续执行面向正确性、稳定性、性能和运行时低干扰的深度代码审核，并对本轮发现的真实风险问题实施最优修复。具体目标如下：

1. 复核上一轮修复后的 `WriteMemories`、`retention`、`idle-session recycle` 和短窗口软幂等链路，确认是否仍存在未闭合的语义缺口。
2. 继续扩展全局审核面，覆盖最近几轮改动交汇处，特别是“请求内去重”和“跨请求短窗口去重”之间的语义一致性。
3. 对本轮发现的真实高风险问题实施低干扰、高稳定性的修复，并补充必要测试。
4. 在完成修复后执行回归验证、自检、计划归档、提交与推送，形成完整闭环。

## 2. 审核范围

### 2.1 本轮重点覆盖

1. `WriteMemories` 的请求内折叠、跨请求软幂等与语义属性一致性。
2. `retention` worker 与 PostgreSQL idle-session recycle 的边界扫描行为。
3. 主动写记忆的性能路径是否与正确性约束同步收口。
4. 最近新增测试是否覆盖到真实运行时语义，而不是只覆盖表面成功路径。

### 2.2 本轮不主动扩展

1. 不新增计划外的大型架构重构。
2. 不为了“顺手统一风格”去改动与当前风险无关的模块。
3. 不引入新的产品接口、恢复能力或额外持久化模型，除非发现阻断级问题且存在低风险最优解。

## 3. 执行策略

1. 基于当前主分支最新状态做定向深审，优先覆盖最近几轮高风险写路径与后台维护链路。
2. 若发现问题，优先选择：
   - 不改变既有对外契约；
   - 不引入额外状态机复杂度；
   - 对运行时锁竞争、provider 往返和后台扫描干扰更友好的实现；
   - 可通过测试长期约束的修复方式。
3. 仅记录和修复真实存在、能被代码路径或测试证明的问题，不做臆测式改动。

## 4. 详细执行步骤

1. 创建本阶段计划并确认仓库基线。
2. 深审 `WriteMemories`、短窗口软幂等与相关测试覆盖，整理本轮真实审核发现。
3. 对阻断级或高风险问题实施修复。
4. 为修复项补齐测试，并完成最少必测集、全量验证与必要静态检查。
5. 对照计划逐项自检，补写执行变更总结。
6. 将计划迁移至 `docs/completed/`，完成 `git commit` 与 `git push`。

## 5. 验收标准

1. 本轮必须给出明确审核结论，并区分真实缺口与不应继续推进的事项。
2. 若发现真实风险，必须完成代码修复，而不是只输出说明。
3. 修复后不得引入新的语义倒挂、错误去重、额外 provider 放大或显著后台干扰问题。
4. 至少完成以下验证：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
5. 如修复涉及静态分析或运行时接线风险，再补跑 `go vet ./...`。
6. 计划文件末尾必须追加完整的“执行变更总结”，随后再迁移到 `docs/completed/`。

## 6. 风险与注意事项

1. 本轮仍以功能稳定、效率高、性能强、运行时无干扰为优先目标，而不是开发速度。
2. 若发现“表面上像缺功能、但实际上不该继续做”的项，必须在总结里明确标注后置或降级理由。
3. 修复必须遵守仓库双语注释、测试约束和计划归档规范。

## 执行变更总结

### 1. 核心修复与调整概述

1. 修复了 `WriteMemories` 跨请求 24 小时软幂等仍沿用旧版粗粒度哈希的问题，避免“同文本但不同 category / priority / memory_level / 生命周期语义”的显式写入被错误复用。
2. 为短窗口软幂等增加了“当前语义哈希优先 + 旧哈希短迁移窗兼容 + 旧哈希语义二次校验”闭环，兼顾升级期稳定性与运行时正确性。
3. 补齐了跨请求软幂等回归测试，覆盖当前新哈希命中、旧哈希平滑迁移命中，以及旧哈希但语义不一致时拒绝复用三类关键路径。

### 2. 📂文件变更清单

1. 修改：`internal/app/usecase/memory_query.go`
2. 修改：`internal/app/usecase/memory_query_test.go`

### 3. 💻关键代码调整详情

1. 在 `MemoryUseCase.Write` 中收敛短窗口软幂等入口，新增 `resolveRecentDirectWriteSoftDedupe`，统一处理当前哈希命中、旧哈希回退和兼容期校验逻辑，避免主流程继续散落重复判断。
2. 将 `buildDirectMemoryDedupeHash` 升级为语义感知哈希，纳入 `category`、`priority`、`memory_level` 与生命周期签名；同时保留 `buildLegacyDirectMemoryDedupeHash` 仅用于短迁移窗兼容。
3. 新增 `buildDirectMemoryExpiryDedupeSignature`、`buildStoredDirectMemoryExpiryDedupeSignature` 与 `directWriteSoftDedupeMatchesMemoryRow`，确保默认 TTL 型写入在不同 RPC 间仍可稳定幂等，而显式变化的生命周期语义不会再被旧哈希误并。
4. 扩展测试桩 `stubTurnLookupStore`，支持按 dedupe hash 返回预设行并记录查询顺序，确保兼容路径测试可真实约束“先查新哈希、再查旧哈希”的执行顺序。

### 4. ⚠️遗留问题与注意事项

1. 本轮未修改对外接口和文档契约，因为修复仅发生在主动写入内部软幂等语义，不涉及 gRPC 入参出参结构变化。
2. 旧哈希兼容路径仅用于升级后的短窗口平滑过渡；随着 24 小时软幂等窗口自然滚动结束，运行时会逐步完全收敛到新的语义哈希。
3. 已完成验证：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./internal/app/usecase -run "TestMemoryUseCaseWrite(SoftIdempotency|DoesNotCollapseDifferentSemanticAttributes|DeduplicatesSameRequestDuplicates|SemanticDedupeReturnsExistingMemory|DroppedCandidateWithoutExplicitDedupeTargetFallsBackToCreate|AcceptedCandidateSupersedesOldMemory|LegacyAcceptedIndexesStillPersist|DroppedCandidateWithoutSimilarFallsBackToCreate|StaleSemanticDedupeTargetFallsBackToCreate)"`
   - `go test ./...`
   - `go vet ./...`
