# 全局代码审核与记忆详情链路加固执行计划

## 1. 任务目标

本阶段在第 13 阶段完成 PostgreSQL 空闲会话回收聚合加固后，继续执行新一轮全局代码审核，重点关注统一记忆详情查询、主动写入结果解析、回收后详情可见性以及近期多轮修复叠加下的边界一致性，确认是否仍存在真实风险，并对确认存在的问题实施最优修复。

具体目标如下：

1. 复核统一记忆详情、turn 详情、主动写入返回引用与热路径约束之间的交界面。
2. 继续扩展审核面到“详情查询是否泄露冷状态对象”“热路径与详情路径契约是否漂移”“回收后旧引用是否仍可能返回不该暴露的数据”等高风险区域。
3. 对本轮确认存在的真实风险实施低干扰、高稳定性的修复，并补齐必要测试。
4. 完成回归验证、自检、计划归档、提交与推送，保持工程闭环。

## 2. 审核范围

### 2.1 本轮重点覆盖

1. `internal/app/usecase/memory_query.go`
2. 统一记忆详情查询、turn 详情查询与 direct-write / search 返回引用的契约边界
3. 回收链路与详情查询链路之间的冷热状态一致性

### 2.2 本轮不主动扩展

1. 不新增计划外的大型架构重构。
2. 不修改对外接口定义，除非确认存在阻断级错误且有低风险最优解。
3. 不扩大到与当前风险无关的风格性改动。

## 3. 执行策略

1. 基于当前主分支最新状态执行定向深审，优先覆盖最近几轮最容易发生语义漂移的详情与引用解析路径。
2. 只修复能通过代码路径、测试或运行时语义证明的真实问题，不做猜测式改造。
3. 修复方案优先满足：
   - 不改变既有成功路径的对外契约；
   - 不引入额外 provider 往返、后台噪声或锁竞争；
   - 能通过测试长期约束；
   - 对维护者可读、可验证、可继续演进。

## 4. 详细执行步骤

1. 确认仓库基线并建立本阶段计划文件。
2. 深审统一记忆详情与相关引用解析路径，定位真实风险问题。
3. 实施代码修复，并补齐定向回归测试。
4. 运行最少必测集、全量测试与必要静态检查。
5. 对照计划逐项自检，补写执行变更总结。
6. 将计划迁移至 `docs/completed/`，完成 `git commit` 与 `git push`。

## 5. 验收标准

1. 必须明确说明本轮定位到的真实问题与修复原因。
2. 修复后不得让详情链路泄露不应暴露的冷状态对象，也不得破坏正常详情解析与热路径引用。
3. 修复不得引入明显额外开销或运行时干扰。
4. 至少完成以下验证：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`

## 6. 风险与注意事项

1. 本轮仍以功能稳定、效率高、性能强、运行时无干扰为最高优先级。
2. 若发现某项“看似还能继续做、实际上不该做”，必须在总结中明确说明后置或下线理由。
3. 修复必须遵守仓库双语注释、测试约束、计划归档和中文提交规范。

## 执行变更总结

### 1. 核心修复与调整概述

1. 发现并修复了记忆搜索热路径里的竞态泄露风险：`SearchLexicalMemory` 虽然只返回 `active + unexpired` 行，但 `materializeLexicalHits` 回表补全时重新走了不带状态过滤的 `LoadMemoryNodesByIDs`。
2. 这意味着只要 lexical 命中和关系回表之间发生并发 `supersede / expire / recycle`，已退出热路径的记忆仍可能被重新拼回最终搜索结果；`ensureMMRVectors` 的向量回填也存在同类风险。
3. 本轮在 usecase 层新增统一的热路径最终过滤 helper，把 lexical 回表补全和 MMR 向量回填都收敛到同一条 `active + unexpired` 契约上，避免搜索链路重新暴露冷状态记忆。

### 2. 📂文件变更清单

1. 修改：`internal/app/usecase/memory_query.go`
2. 修改：`internal/app/usecase/memory_query_test.go`

### 3. 💻关键代码调整详情

1. 新增 `indexActiveUnexpiredMemoryRowsByID`，统一为热路径按 id 回表结果建立带状态过滤的索引。
2. 将 `materializeLexicalHits` 改为在关系回表后再次执行 `active + unexpired` 过滤，确保 lexical 搜索与最终结果之间不存在竞态泄露窗口。
3. 将 `ensureMMRVectors` 的向量回填也收敛到相同过滤规则，避免已退役记忆仅因“缺向量补全”而重新影响多样性排序。
4. 顺手把 `loadDirectWriteDedupedExistingRows` 改为复用同一 helper，减少热路径状态过滤逻辑重复，实现更稳的一致性收口。

### 4. ⚠️遗留问题与注意事项

1. 本轮没有修改 `GetDetails` 的原始按 id 读取语义，因为对外 `GetMemoryDetails` 已经不再暴露，当前修复重点是“搜索热路径不能重新露出冷状态对象”，不扩大到内部详情原始读模型。
2. 这次修复是 usecase 层的最终防线，不依赖底层适配器“恰好总能在所有回表查询里带上 active 过滤”，能更稳定地抵抗并发状态切换窗口。
3. 已完成验证：
   - `go test ./internal/app/usecase -run "Test(MaterializeLexicalHits|EnsureMMRVectors|MemoryUseCaseSearch)"`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`
