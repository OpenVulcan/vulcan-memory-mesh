# 全局代码审核与回收链路稳定性加固执行计划

## 1. 任务目标

本阶段在第 11 阶段修复 SQLite `idle-session recycle` 候选饥饿问题后，继续执行一轮新的全局代码审核，重点关注 retention、direct-write、post-action 与共享回退路径之间是否仍存在正确性、稳定性、性能或运行时干扰风险，并对确认存在的问题实施最优修复。具体目标如下：

1. 复核 `retention`、`WriteMemories`、`post-action`、`idle-session recycle` 等最近多轮持续演化链路，确认是否还存在新的边界漏洞。
2. 继续扩展审核面到“回退路径”“批处理路径”“后台维护路径”“跨适配器语义一致性”等高风险区域。
3. 对本轮确认存在的真实风险实施低干扰、高稳定性的修复，并补齐必要测试。
4. 完成回归验证、自检、计划归档、提交与推送，保持工程闭环。

## 2. 审核范围

### 2.1 本轮重点覆盖

1. `retention` 工作器与 PostgreSQL / SQLite 两套适配器的空闲 session、冷记忆与 trash purge 语义一致性。
2. `WriteMemories` 与 `post-action` 的回退、兼容与批处理收敛路径。
3. 近期测试桩和回归用例是否真正约束了运行时行为，而不是只覆盖表面成功路径。
4. 最近多轮修复之间是否存在“某一端已加固、另一端仍残留旧风险”的不对称问题。

### 2.2 本轮不主动扩展

1. 不新增计划外的大型架构重构。
2. 不为统一风格而扩大与当前风险无关的改动面。
3. 不引入新的产品接口、恢复能力或额外持久化模型，除非出现阻断级缺陷且存在低风险最优解。

## 3. 执行策略

1. 基于当前主分支最新状态执行定向深审，优先覆盖最近几轮反复改动的共享逻辑与后台维护路径。
2. 只修复能通过代码路径、测试或运行时语义证明的真实问题，不做推测式改造。
3. 修复方案必须优先满足：
   - 不改变既有对外契约；
   - 不增加后台扫描放大、锁竞争或 provider 往返；
   - 能通过测试长期约束；
   - 对后续维护者可读、可验证、可继续演进。

## 4. 详细执行步骤

1. 创建本阶段计划并确认仓库基线。
2. 执行新一轮全局审核，定位真实风险问题。
3. 对确认问题实施修复，并补齐必要测试。
4. 运行最少必测集、全量测试与必要静态检查。
5. 对照计划逐项自检，补写执行变更总结。
6. 将计划迁移至 `docs/completed/`，完成 `git commit` 与 `git push`。

## 5. 验收标准

1. 本轮必须输出明确审核结论，并区分真实缺口与无需继续推进的事项。
2. 若发现真实风险，必须完成代码修复，而不是只写说明。
3. 修复后不得引入新的语义错乱、后台噪声放大、错误去重或显著运行时干扰。
4. 至少完成以下验证：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
5. 如涉及后台维护器、适配器 SQL 语义或复杂回退路径，再补跑 `go vet ./...`。
6. 计划文件末尾必须追加完整“执行变更总结”，随后再迁移至 `docs/completed/`。

## 6. 风险与注意事项

1. 本轮仍以功能稳定、效率高、性能强、运行时无干扰为最高优先级，而不是开发速度。
2. 若发现某项“看似还能继续做、实际上不该做”，必须在总结中明确说明后置或下线理由。
3. 修复必须遵守仓库双语注释、测试约束、计划归档和中文提交规范。

## 执行变更总结

### 1. 核心修复与调整概述

1. 发现并修复了 `RetentionUseCase.runMaintenance` 的一致性风险：旧实现即使关系回收返回错误，也仍会继续清理 `RecycledVectorIDs`，这会把“底层适配器恰好在错误时返回零值”的偶然前提写死在运行时里。
2. 该风险一旦在未来适配器返回“部分结果 + 错误”时触发，会导致关系库事务失败但向量已被删除，形成冷热数据不一致，且恢复成本高。
3. 本轮将向量清理严格收敛为“仅在关系回收成功提交后执行”，并补齐带部分结果错误返回的回归测试，锁死这一边界。

### 2. 📂文件变更清单

1. 修改：`internal/app/usecase/retention.go`
2. 修改：`internal/app/usecase/retention_test.go`

### 3. 💻关键代码调整详情

1. 调整 `runMaintenance` 中两条向量清理桥接逻辑：
   - `RecycleColdMemories` 返回错误时不再调用 `cleanupVectors`
   - `RecycleIdleSessions` 返回错误时不再调用 `cleanupIdleSessionVectors`
2. 保持成功路径行为不变，仍然在关系回收成功后执行 best-effort 向量清理，因此不会影响正常回收吞吐。
3. 扩展 `fakeRetentionStore`，让测试可以模拟“带部分结果的错误返回”，避免未来有人无意间重新引入“错误时照样删向量”的风险。
4. 新增两条回归测试：
   - `TestRetentionUseCaseRunMaintenanceSkipsColdRecycleVectorCleanupOnError`
   - `TestRetentionUseCaseRunMaintenanceSkipsIdleSessionVectorCleanupOnError`

### 4. ⚠️遗留问题与注意事项

1. 本轮未改变 `RetentionStore` 接口与外部配置，只修正了工作器内部对错误返回的处理策略，对外契约保持不变。
2. 当前修复假设“向量删除仍然是 best-effort”，因此成功路径在向量删除失败时依旧只记录日志、不回滚关系回收；这是现有架构下的既有策略，本轮未扩大改动范围。
3. 已完成验证：
   - `go test ./internal/app/usecase -run "TestRetentionUseCase"`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`
