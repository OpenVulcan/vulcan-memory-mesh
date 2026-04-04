# 全局代码审核与正确性加固执行计划

## 1. 任务目标

本阶段在第 10 阶段完成跨请求软幂等语义修复后，继续执行一轮新的全局代码审核，重点关注最近多轮改动叠加后是否仍存在正确性、稳定性、性能或运行时干扰风险，并对确认存在的问题实施最优修复。具体目标如下：

1. 复核 `WriteMemories`、`retention`、`post-action`、`idle-session recycle` 等最近高频改动链路，确认是否存在新的边界漏洞或语义倒挂。
2. 扩展审核面到“兼容逻辑”“回退路径”“批处理路径”“后台扫描路径”等容易被多轮修复叠加影响的区域。
3. 对本轮确认存在的真实风险实施低干扰、高稳定性的代码修复，并补齐回归测试。
4. 完成全量验证、自检、计划归档、提交与推送，保持工程闭环。

## 2. 审核范围

### 2.1 本轮重点覆盖

1. `WriteMemories` 的软幂等、语义替代、向量写入与持久化收口。
2. `retention` 后台维护器、session idle recycle 与冷状态回收链路。
3. `post-action` 与 direct-write 共享 reviewer 语义边界。
4. 最近新增测试是否真正约束了运行时语义，而不是仅覆盖表面成功路径。

### 2.2 本轮不主动扩展

1. 不新增计划外的大型架构重构。
2. 不为了统一代码风格而扩大无关改动面。
3. 不引入新的产品接口、恢复能力或额外持久化模型，除非出现阻断级缺陷且存在低风险最优解。

## 3. 执行策略

1. 先基于当前主分支最新状态执行定向深审，优先覆盖近几轮刚落地的高风险链路。
2. 只对能够通过代码路径、测试或运行时语义证明的真实问题做修复，不做推测式改造。
3. 修复方案必须优先满足：
   - 不改变既有对外契约；
   - 不增加后台噪声、锁竞争或 provider 放大；
   - 能通过测试长期约束；
   - 对后续维护者可读且可验证。

## 4. 详细执行步骤

1. 创建本阶段计划并确认仓库基线。
2. 执行新一轮全局审核，定位真实风险问题。
3. 对确认问题实施修复，并补齐必要测试。
4. 运行最少必测集、全量测试与必要静态检查。
5. 对照计划逐项自检，补写执行变更总结。
6. 将计划迁移至 `docs/completed/`，完成 `git commit` 与 `git push`。

## 5. 验收标准

1. 本轮必须输出明确的审核结论，并区分真实缺口与无需继续推进的事项。
2. 若发现真实风险，必须完成代码修复，而不是只写说明。
3. 修复后不得引入新的语义错乱、后台扫描放大、错误去重或显著运行时干扰。
4. 至少完成以下验证：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
5. 如涉及静态分析、后台维护器或复杂回退路径，再补跑 `go vet ./...`。
6. 计划文件末尾必须追加完整“执行变更总结”，随后再迁移至 `docs/completed/`。

## 6. 风险与注意事项

1. 本轮仍以功能稳定、效率高、性能强、运行时无干扰为最高优先级，而不是开发速度。
2. 若发现某项“看似还能继续做、实际上不该做”，必须在总结中明确说明后置或下线理由。
3. 修复必须遵守仓库双语注释、测试约束、计划归档和中文提交规范。

## 执行变更总结

### 1. 核心修复与调整概述

1. 发现并修复了 SQLite `idle-session recycle` 的候选选择漏洞：旧实现只按“空闲且无 pending turn”选最老 `limit` 个 session，没有像 PostgreSQL 一样先过滤“是否真的存在可回收内容”。
2. 该问题会导致最老的一批 no-op session 在 `limit` 较小时长期占住扫描窗口，后面实际有陈旧 session 记忆或可归档旧 turn 的 session 无法被回收，形成后台维护饥饿。
3. 本轮为 SQLite 补齐与 PostgreSQL 同级的候选可回收性预过滤，并新增运行时级与 helper 级回归测试，确保最老 no-op session 不再阻塞后续真正可回收的批次。

### 2. 📂文件变更清单

1. 修改：`internal/adapters/outbound/vldb_sqlite/retention_store.go`
2. 修改：`internal/adapters/outbound/vldb_sqlite/retention_store_test.go`

### 3. 💻关键代码调整详情

1. 在 `RecycleIdleSessions` 的 session 选择 SQL 中新增 `buildSQLiteIdleSessionCandidateAvailabilityClause` 预过滤，要求候选 session 必须至少满足以下任一条件：
   - 存在可回收的过期 session 级 active 记忆；
   - 存在超出热窗口、且没有 memory/profile 引用的旧 turn。
2. 保持修复策略为“查询前过滤”，而不是简单扩大扫描窗口或多轮跳过重试，避免额外放大单次维护轮的查询范围和后台干扰。
3. 新增 `TestRecycleIdleSessionsSkipsNoOpOldestSessions`，模拟 `limit=1` 且最老 session 为 no-op 的场景，约束运行时必须直接挑出后续可回收 session。
4. 新增 `TestBuildSQLiteIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows`，固定住 SQLite 可回收性预过滤谓词的两条存在性分支与 profile 引用保护条件。

### 4. ⚠️遗留问题与注意事项

1. 本轮未扩展产品接口或配置项，修复仅发生在 SQLite retention 内部候选筛选逻辑，对外契约保持不变。
2. PostgreSQL 侧此前已具备同类预过滤能力，因此这次主要是补齐 SQLite 与 PostgreSQL 的语义一致性，而不是引入新的治理策略。
3. 已完成验证：
   - `go test ./internal/adapters/outbound/vldb_sqlite -run "Test(RecycleIdleSessions|BuildSQLiteIdleSessionCandidateAvailabilityClause)"`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`
