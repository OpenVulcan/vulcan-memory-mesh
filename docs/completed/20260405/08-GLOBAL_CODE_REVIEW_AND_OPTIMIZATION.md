# 全局代码审核与最优修复执行计划

## 1. 任务目标

本阶段在上一轮功能交付已完成并推送的基础上，执行一次面向稳定性、性能、正确性和运行时干扰控制的全局代码审核，并按最优方案修复真实风险问题。具体目标如下：

1. 复核当前总计划中已落地的 `memory replace`、`retention`、`session idle recycle` 与 `turn trash` 相关实现，确认是否仍存在逻辑缺口或回归风险。
2. 在不引入额外架构扰动、不牺牲兼容性的前提下，修复本轮审核中发现的真实问题。
3. 为修复项补齐必要测试，确保问题被长期约束，而不是一次性“看起来通过”。
4. 对照计划完成自检，补写执行变更总结，并完成提交与推送。

## 2. 审核范围

### 2.1 本轮重点覆盖

1. `post-action` / `WriteMemories` 的记忆替代与去重链路。
2. `retention` 后台维护器与 `session idle recycle` 事务实现。
3. PostgreSQL / SQLite 双存储语义是否保持一致。
4. 热路径约束是否被冷数据治理逻辑误伤。
5. 最近几轮新增文档与配置说明是否与真实行为一致。

### 2.2 本轮不主动扩展

1. 不新增计划外的大型架构重构。
2. 不为了“代码更漂亮”而调整与当前任务无关的模块。
3. 不引入新的运行时入口、恢复接口或额外持久化模型，除非审核发现这是阻断级缺口且有低风险最优解。

## 3. 执行策略

1. 先确认仓库实际状态与计划基线，避免重复修复已解决的问题。
2. 从生命周期热路径与最近引入的 retention 逻辑开始做定向深审，再扩展到相关调用链和测试面。
3. 仅记录真实存在、能够被代码路径证明的问题，不做臆测式“找问题”。
4. 若发现问题，优先选择：
   - 不改变对外契约；
   - 不增加无收益状态机复杂度；
   - 对性能和锁竞争更友好的实现；
   - PostgreSQL 与 SQLite 行为尽量对齐的方案。

## 4. 详细执行步骤

1. 核对仓库状态、计划序号与当前主分支基线。
2. 梳理 `retention`、`memory replace`、`WriteMemories`、`turn/session recycle` 相关主链代码与测试。
3. 输出本轮真实审核发现，区分阻断级、高风险和一般改进项。
4. 对阻断级或高风险问题实施修复，并同步补测试与必要文档。
5. 运行最少必测集与全量测试，必要时追加 `go vet`。
6. 对照计划逐项自检，补写执行变更总结。
7. 将计划迁移至 `docs/completed/`，完成 `git commit` 与 `git push`。

## 5. 验收标准

1. 本轮审核结论可明确说明“当前还缺什么、哪些是真的缺、哪些不应继续推进”。
2. 若发现真实问题，必须完成代码修复，而不是只输出审计结论。
3. 修复后不得引入新的运行时干扰、锁放大或语义倒挂问题。
4. 至少完成以下验证：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
5. 如修复涉及静态分析可见风险，再补跑 `go vet ./...`。
6. 计划文件末尾必须追加完整的“执行变更总结”，随后再迁移到 `docs/completed/`。

## 6. 风险与注意事项

1. 审核优先级以功能稳定、效率高、性能强、运行时无干扰为准，不以开发速度为先。
2. 若发现“看似未完成但实际不该做”的事项，需要在总结里明确降级或后置理由，避免计划继续虚高。
3. 所有修复必须尊重当前仓库双语注释与测试约束，不得为了赶进度跳过。

## 执行变更总结

### 1. 核心修复与调整概述

1. 修复了 `WriteMemories` 的批内重复写入缺口：同一 RPC 内重复的相同主动记忆项现在会先折叠成一次真实写入，避免重复 embedding、重复向量写入和重复长期记忆行。
2. 修复了 PostgreSQL `idle-session recycle` 的候选饥饿问题：最老但实际无可回收内容的 session 不会再卡死后续真正可回收的批次。
3. 为上述两类风险分别补齐了用例测试和 PostgreSQL 辅助逻辑测试，确保后续改动不会静默回归。
4. 完成最少必测集、全量测试和 `go vet`，确认本轮修复没有引入新的编译、语义或静态分析问题。

### 2. 📂 文件变更清单

修改：

1. `internal/app/usecase/memory_query.go`
2. `internal/app/usecase/memory_query_test.go`
3. `internal/adapters/outbound/vldb_postgres/retention_store.go`
4. `internal/adapters/outbound/vldb_postgres/retention_store_test.go`
5. `docs/completed/20260405-08-GLOBAL_CODE_REVIEW_AND_OPTIMIZATION.md`

新增：

1. 无

删除：

1. 无

### 3. 💻 关键代码调整详情

1. `WriteMemories` 批内去重：
   - 新增同请求内基于 `dedupe_hash` 的折叠逻辑；
   - 同一请求里重复的写入项现在共享一次 reviewer / embedding / vector / persistence 流程；
   - 结果回填时保留原始顺序，首条作为真实创建结果，后续重复项标记为 `Deduped=true` 并复用同一 `memory ref`。
2. PostgreSQL idle-session 回收稳健性：
   - 新增 `collectPostgresIdleSessionRecyclePass`，允许单次扫描跳过 no-op session 而继续处理后续可回收批次；
   - `recycleOnePostgresIdleSession` 新增当前扫描内的 `excludedSessionIDs` 约束，避免同一轮里反复锁到同一个空候选；
   - 新增 `buildPostgresIdleSessionCandidateAvailabilityClause`，在 session 候选层就要求存在“可回收的陈旧 session 记忆或旧 turn”，从源头减少空扫描和锁干扰。
3. 测试补齐：
   - 新增 `TestMemoryUseCaseWriteDeduplicatesSameRequestDuplicates`；
   - 新增 `TestCollectPostgresIdleSessionRecyclePassSkipsNoOpSessions`；
   - 新增 `TestBuildPostgresIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows`。

### 4. ⚠️ 遗留问题与注意事项

1. 本轮没有继续扩展新的回收策略或接口，仍保持当前对外契约不变。
2. PostgreSQL idle-session 候选预过滤已经足以解决当前饥饿问题，但其查询复杂度高于最初版本；后续若数据量显著上升，可再结合运行数据评估是否需要专门索引或进一步分层扫描策略。
3. `WriteMemories` 的批内折叠当前沿用既有 `dedupe_hash` 语义；若未来产品要把“同文本但不同类别/优先级”视为不同写入对象，需要另行调整去重键设计，而不是在本轮静默改变契约。
4. 验证已完成：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./internal/app/usecase ./internal/adapters/outbound/vldb_postgres`
   - `go test ./...`
   - `go vet ./...`
