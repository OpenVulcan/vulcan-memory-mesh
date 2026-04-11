# 任务目标

基于已确认的 `postaction` 真实执行链路，实施一次“无兼容保留”的职责切割重构，目标如下：

1. 删除 `postaction L1` 中与 whole-session 旧记忆去重耦合的输入与契约。
2. 让 `L1` 只负责当前轮候选提炼与首轮粗过滤信号输出。
3. 让记忆重复 / 替代判断完全下沉到“检索层 + hard dedupe + L2 reviewer”。
4. 删除不再需要的领域字段、端口方法、渲染结构、提示词约束与测试断言。
5. 保证最终 supersede 集合只由 L2 接纳后的 surviving memory nodes 推导。

# 实施步骤

## 1. 删除 L1 的 active memory 输入链路

1. 从 `TurnAnalysisInput` 中删除 `ActiveMemoryNodes`
2. 删除 `TurnAnalysisActiveMemoryNode`
3. 删除 `buildTurnAnalysisInput` 中的 `LoadActiveSessionMemoryNodes`
4. 删除 `renderTurnAnalysisRequest` 中的 `active_memory_nodes`
5. 删除 `renderTurnAnalysisSystemPrompt` 中的 `ACTIVE_MEMORY_RULE`

## 2. 删除 L1 的 supersede 输出契约

1. 从 `TurnAnalysis` 中删除 `SupersededMemoryIDs`
2. 从 L1 prompt 中删除顶层 `superseded_memory_ids`
3. 从 L1 解析器中删除：
   - 顶层 `superseded_memory_ids`
   - `memory_nodes[].supersede_memory_ids`
4. 删除 `validateTurnAnalysis` 中对上述字段的校验

## 3. 收束 supersede 写库路径

1. 保留 `MemoryNodeCandidate.SupersedeMemoryIDs`
2. 保留 L2 reviewer 的 `accepted_candidates[].supersede_memory_ids`
3. 让最终 supersede 集合仅由 surviving memory nodes 的 `SupersedeMemoryIDs` 推导
4. 删除对顶层 `TurnAnalysis.SupersededMemoryIDs` 的依赖

## 4. 删除无用接口与实现

1. 从 `RelationalStore` 中删除 `LoadActiveSessionMemoryNodes`
2. 删除 SQLite / Postgres 对应实现、代理、unsupported 占位
3. 删除测试桩对应方法

## 5. 重写测试与同步文档

1. 重写 `postaction_test.go`
2. 重写 `turn_analyzer_test.go`
3. 重写 `postaction_candidate_review_test.go`
4. 同步更新 `README.md`
5. 同步更新 `docs/post-action-guide_CN.md`

# 验收标准

1. `postaction_l1_main` 请求体中不再包含 `active_memory_nodes`
2. `postaction_l1_main` 输出契约中不再包含 `superseded_memory_ids`
3. `TurnAnalysisInput` 与 `TurnAnalysis` 中不再保留已废弃字段
4. `RelationalStore` 中不再保留 `LoadActiveSessionMemoryNodes`
5. L2 仍能正确回填 `SupersedeMemoryIDs` 到 surviving memory nodes
6. 持久化阶段仍能正确退役旧记忆并清理旧向量
7. `recent_grpc_memory_writes` 排斥能力不回归
8. 通过：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `.\make.ps1 build`

# 执行变更总结

## 1. 核心修复与调整概述

1. 已彻底移除 `postaction L1` 对 `active_memory_nodes` 的输入依赖，L1 现在只基于历史 `details`、当前原始 turn 与 `recent_grpc_memory_writes` 做候选提炼。
2. 已删除 L1 顶层 `superseded_memory_ids` 旧契约，并同步删除领域模型、渲染器、解析器、校验器和日志中的对应字段。
3. 已把最终 supersede 集合的来源收敛为“L2 接纳后仍存活的 `memory_nodes[].SupersedeMemoryIDs`”，不再允许整轮级旧字段继续影响退役写回。
4. 已同步更新 SQLite / PostgreSQL 写回事务、测试用例和中英文文档，保证运行时描述、提示词契约和持久化行为一致。

## 2. 📂文件变更清单

### 新增

1. `docs/completed/20260411/08-postaction-l1-l2-responsibility-refactor.md`
2. `docs/completed/20260411/09-postaction-memory-dedupe-refactor-implementation-plan.md`
3. `docs/completed/20260411/10-postaction-refactor-no-compat-plan.md`
4. `docs/completed/20260411/11-postaction-l1-clean-cut-refactor.md`

### 修改

1. `configs/prompts/default_cn/postaction_l1_main.md`
2. `configs/prompts/default_en/postaction_l1_main.md`
3. `README.md`
4. `docs/post-action-guide_CN.md`
5. `internal/logic/domain/turn_analysis.go`
6. `internal/logic/processor/render.go`
7. `internal/logic/processor/turn_analyzer.go`
8. `internal/logic/processor/turn_analyzer_test.go`
9. `internal/app/ports/ports_memory.go`
10. `internal/app/usecase/postaction_analysis.go`
11. `internal/app/usecase/postaction_candidate_review.go`
12. `internal/app/usecase/postaction_candidate_review_test.go`
13. `internal/app/usecase/postaction_test.go`
14. `internal/adapters/outbound/vldb_sqlite/store.go`
15. `internal/adapters/outbound/vldb_postgres/analysis_store.go`
16. `internal/adapters/outbound/vldb_postgres/helpers.go`
17. `internal/adapters/outbound/vldb_postgres/store.go`
18. `internal/adapters/outbound/vldb_postgres/unsupported.go`

### 删除

1. 无独立文件删除；本次删除的是旧字段、旧端口方法、旧提示词约束与旧测试断言。

## 3. 💻关键代码调整详情

1. 领域模型层删除了 `TurnAnalysisInput.ActiveMemoryNodes`、`TurnAnalysisActiveMemoryNode` 与 `TurnAnalysis.SupersededMemoryIDs`，直接切断 L1 旧职责入口。
2. `buildTurnAnalysisInput` 不再调用 `LoadActiveSessionMemoryNodes`，`renderTurnAnalysisRequest` 也不再输出 `active_memory_nodes`，系统 prompt 同步移除了 `ACTIVE_MEMORY_RULE`。
3. `parseTurnAnalysisResponse` 不再解析 L1 的候选级 / 顶层 supersede 字段，L1 只负责产出 `details / memory_nodes / profile_nodes`。
4. `reviewTurnCandidates` 继续保留 L2 reviewer 的 `accepted_candidates[].supersede_memory_ids`，并把结果回填到 surviving `MemoryNodeCandidate.SupersedeMemoryIDs`。
5. SQLite / PostgreSQL 的 `ApplyTurnAnalysis` 已改成直接从 surviving `analysis.MemoryNodes` 归并 supersede 目标，保证最终退役集合只来自真正存活的新节点。
6. 测试已改写为断言：
   - L1 输入不再含 `active_memory_nodes`
   - L1 输出不再含顶层 `superseded_memory_ids`
   - supersede 只保存在 surviving memory node 上
   - `recent_grpc_memory_writes` 排斥窗口继续有效

## 4. ⚠️遗留问题与注意事项

1. 当前工作区仍存在未跟踪文件 `vmm-local.exe`，本次没有纳入任何提交。
2. `docs/completed/20260411/08~10` 记录了本次改造前的执行链路分析与方案演进，属于过程文档，不代表最终落地契约；最终以 `11-postaction-l1-clean-cut-refactor.md` 和最新代码为准。
