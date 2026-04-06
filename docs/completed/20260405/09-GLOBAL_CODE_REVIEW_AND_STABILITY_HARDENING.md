# 全局代码审核与稳定性加固执行计划

## 1. 任务目标

本阶段在第 08 阶段完成一轮全局审核与风险修复的基础上，继续执行新一轮面向正确性、稳定性、性能与运行时干扰控制的深度代码审核，并对本轮发现的真实问题实施最优修复。具体目标如下：

1. 复核上一轮修复后的 `WriteMemories`、`retention`、`idle-session recycle`、`memory replace` 与组合根接线实现，确认是否仍存在新暴露的边界风险。
2. 继续扩展全局审核面，覆盖近期多轮改动交汇处，特别是配置语义、运行时接线、存储语义一致性与后台维护链路。
3. 对本轮发现的真实风险问题实施低干扰、高稳定性的修复，并补充必要测试。
4. 在完成修复后执行回归验证、自检、计划归档、提交与推送，形成完整闭环。

## 2. 审核范围

### 2.1 本轮重点覆盖

1. `WriteMemories`、`post-action` 与统一 reviewer 之间的语义一致性。
2. `retention` worker、`idle-session recycle`、`trash purge` 的边界行为与饥饿/误回收风险。
3. `app` 组合根的配置接线、历史窗口语义和文档口径一致性。
4. PostgreSQL / SQLite 双存储在新增治理能力上的语义漂移风险。
5. 最近新增测试是否充分覆盖真实运行时边界。

### 2.2 本轮不主动扩展

1. 不新增计划外的大型架构重构。
2. 不为了“顺手优化”而改动与当前风险无关的模块。
3. 不引入恢复能力、额外管理接口或新持久化模型，除非发现阻断级问题且存在低风险最优解。

## 3. 执行策略

1. 先基于当前主分支最新状态做定向深审，优先覆盖最近几轮高风险链路。
2. 若发现问题，优先选择：
   - 不改变既有对外契约；
   - 不引入额外状态机复杂度；
   - 对运行时锁竞争、扫描成本和后台干扰更友好的实现；
   - 可通过测试长期约束的修复方式。
3. 仅记录和修复真实存在、能被代码路径或测试证明的问题，不做臆测式改动。

## 4. 详细执行步骤

1. 创建本阶段计划并确认仓库基线。
2. 深审最近高风险链路与配置接线，整理本轮真实审核发现。
3. 对阻断级或高风险问题实施修复。
4. 为修复项补齐测试，并完成最少必测集与全量验证。
5. 对照计划逐项自检，补写执行变更总结。
6. 将计划迁移至 `docs/completed/`，完成 `git commit` 与 `git push`。

## 5. 验收标准

1. 本轮必须给出明确审核结论，并区分真实缺口与不应继续推进的事项。
2. 若发现真实风险，必须完成代码修复，而不是只输出说明。
3. 修复后不得引入新的运行时锁放大、批处理饥饿、语义倒挂或显著性能回退问题。
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

1. 修复了 `WriteMemories` 批内折叠键过粗的问题，避免把“同文本但不同 `category / priority / memory_level / expires_at`”的合法显式写入错误折叠成一条。
2. 顺带把主动写记忆的新建路径优化成“单请求统一批量 embedding，再逐条落向量和关系行”，降低多条显式写入时的 provider 往返次数与延迟。
3. 修复了 PostgreSQL idle-session 回收跳过 no-op session 后缺少总检查上限的问题，避免一次维护周期无界扫描大量空候选、放大后台干扰。
4. 为上述问题补齐测试并完成最少必测集、全量测试与 `go vet`，确认本轮修复没有引入新的编译或运行时回归。

### 2. 📂 文件变更清单

修改：

1. `internal/app/usecase/memory_query.go`
2. `internal/app/usecase/memory_query_test.go`
3. `internal/adapters/outbound/vldb_postgres/retention_store.go`
4. `internal/adapters/outbound/vldb_postgres/retention_store_test.go`
5. `docs/completed/20260405-09-GLOBAL_CODE_REVIEW_AND_STABILITY_HARDENING.md`

新增：

1. 无

删除：

1. 无

### 3. 💻 关键代码调整详情

1. `WriteMemories` 批内折叠修正：
   - 新增单请求内专用的 `buildDirectMemoryRequestCollapseKey`；
   - 该键保留 `category / priority / memory_level / expires_at` 等语义属性，不再直接复用只适合短窗口软幂等的 `dedupe_hash`；
   - 这样同文本但不同持久化语义的显式写入不会被静默合并。
2. 主动写记忆性能优化：
   - 新增 `prepareDirectWriteCreateVectors`；
   - 所有仍需新建的主动写入项会先统一批量 embedding；
   - 后续逐条持久化时直接复用预计算向量，减少 provider 调用次数。
3. PostgreSQL idle-session 回收稳健性修正：
   - `collectPostgresIdleSessionRecyclePass` 增加 `inspectedSessionCount` 上限；
   - 允许在单轮里跳过少量 no-op session，但不再无界扫描整个冷集合；
   - 保留“继续处理后续真实可回收批次”的收益，同时把后台干扰控制在配置批次范围内。
4. 测试补齐：
   - 新增“同请求内重复写入仍应折叠”的断言保留；
   - 新增“同文本但不同语义属性不得折叠”的回归测试；
   - 调整 PostgreSQL idle-session 跳过 no-op session 的测试，使其同时验证“能继续处理后续批次”与“不会无界多扫”。

### 4. ⚠️ 遗留问题与注意事项

1. 本轮没有继续扩展新的产品接口或回收能力，仍保持既有对外契约不变。
2. `WriteMemories` 的短窗口软幂等仍沿用既有 `dedupe_hash` 设计；本轮只修正“单请求内重复折叠”不能复用该粗粒度键的问题，没有改变历史存储语义。
3. PostgreSQL idle-session 回收现在会在单次维护里限制候选检查上限；如果未来冷集合极大且前部长期堆积 no-op session，可再结合运行数据评估是否需要更强的候选物化或专项索引策略。
4. 验证已完成：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./internal/app/usecase ./internal/adapters/outbound/vldb_postgres`
   - `go test ./...`
   - `go vet ./...`
