# 全局代码审核与召回稳定性加固执行计划

## 1. 任务目标

本阶段基于第 17 阶段已经完成的采纳回写生命周期收口，继续执行新一轮全局代码审核，重点检查统一召回、候选合并、生命周期更新、回收维护以及近期多轮修复叠加后的边界一致性，确认是否仍存在会影响功能稳定性、性能、正确性或运行时无干扰目标的真实风险，并以最优方案完成修复。

具体目标如下：

1. 复核统一召回、候选合并、生命周期更新与回收维护之间的交界面，确认是否仍存在冷热状态不一致、结果重复、统计漂移、错误排序或后台副作用等问题。
2. 仅修复能够通过代码逻辑、测试或运行时语义证明的真实问题，不做猜测式重构。
3. 在不牺牲稳定性、效率和可维护性的前提下，完成必要代码修复与测试补强。
4. 完成回归验证、自检、计划归档、提交与推送，保持工程闭环。

## 2. 审核范围

### 2.1 本轮重点覆盖

1. `internal/app/usecase/memory_query.go`
2. `internal/app/usecase/precheck.go`
3. `internal/adapters/outbound/vldb_sqlite/store.go`
4. `internal/adapters/outbound/vldb_postgres/analysis_store.go`
5. 与召回排序、候选合并、生命周期边界相关的测试与文档

### 2.2 本轮不主动扩展

1. 不引入计划外的大型架构重构。
2. 不修改对外接口定义，除非确认存在阻断级错误且存在低风险最优解。
3. 不做与当前风险无关的风格性清理。

## 3. 执行策略

1. 基于当前主分支最新状态执行定向深审，优先覆盖最近多轮修复叠加后最容易发生语义漂移的召回与生命周期边界。
2. 若发现真实问题，优先在 usecase 或单一适配器边界内收口，避免扩大改动面。
3. 修复方案必须同时满足：
   - 不破坏既有成功路径契约；
   - 不增加明显额外 provider 往返、锁竞争或后台噪声；
   - 能通过测试长期约束；
   - 便于后续维护者继续理解和演进。

## 4. 详细执行步骤

1. 确认仓库基线并建立本阶段计划文件。
2. 深审统一召回、候选合并、生命周期更新与回收维护关键链路，定位真实风险问题。
3. 采用最优方案实施修复，并补齐必要测试。
4. 运行最少必测集、全量测试与必要静态检查。
5. 对照计划逐项自检，补写执行变更总结。
6. 将计划迁移至 `docs/completed/`，完成 `git commit` 与 `git push`。

## 5. 验收标准

1. 必须明确说明本轮定位到的真实问题与修复原因。
2. 修复后不得引入新的召回错误、冷热状态不一致、候选合并漂移或后台维护副作用。
3. 修复不得引入明显额外开销或运行时干扰。
4. 至少完成以下验证：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`

## 6. 风险与注意事项

1. 本轮仍以功能稳定、效率高、性能强、运行时无干扰为最高优先级。
2. 若发现某项原设计应后置或不应继续推进，必须在总结中明确说明原因。
3. 修复必须继续遵守仓库双语注释、测试约束、计划归档和中文提交规范。

## 执行变更总结

### 1. 核心修复与调整概述

1. 本轮全局审核确认了一个 SQLite 生命周期写回并发漏洞：`ApplyMemoryAdoption` 之前是“先读候选记忆行，再获取 `writeMu` 执行写回”，这会让它在并发窗口里基于过期快照做强化更新。
2. 如果在读取和真正写回之间，另一条写路径已经完成 supersede、回收或计数递增，旧实现仍可能把较早读取到的状态重新写回，从而覆盖更晚的生命周期状态，破坏“单进程写串行化”的设计目标。
3. 本轮把 SQLite 采纳写回调整为“先拿写锁，再做整段读-改-写”，让候选读取也处于同一串行临界区；同时补上并发测试，锁定“读阶段必须位于写锁内”的行为边界。

### 2. 📂文件变更清单

1. 修改：`internal/adapters/outbound/vldb_sqlite/store.go`
2. 修改：`internal/adapters/outbound/vldb_sqlite/store_test.go`

### 3. 💻关键代码调整详情

1. 将 SQLite `ApplyMemoryAdoption` 的 `writeMu.Lock()` 前移到候选行加载之前，使整段生命周期读-改-写都运行在同一把适配器写锁内。
2. 保留现有“过期 active 行不再强化”的过滤逻辑，但把它放在已串行化的最新快照之上，避免并发窗口里基于旧快照继续决策。
3. 新增 `TestStoreApplyMemoryAdoptionLocksBeforeReading`，通过在测试线程提前持有 `writeMu` 并观察查询是否提前发出，验证采纳流程必须等到写锁释放后才允许开始读取候选行。
4. 保留并继续验证 `TestStoreApplyMemoryAdoptionSkipsExpiredActiveRows`，确保“不过期才可强化”和“先锁再读”两条防线同时成立。

### 4. ⚠️遗留问题与注意事项

1. 本轮修复聚焦 SQLite，因为 PostgreSQL 采纳写回原本就通过事务内 `SELECT ... FOR UPDATE` 在数据库侧串行化读取与更新，不存在同类“先读后锁”的单进程快照窗口。
2. 当前修复目标是保证单进程内适配器写路径的一致性；如果未来引入多进程共享同一 SQLite 后端，仍需额外评估进程间串行化与 RPC 网关级事务语义。
3. 已完成验证：
   - `go test ./internal/adapters/outbound/vldb_sqlite -run "Test(StoreApplyMemoryAdoptionSkipsExpiredActiveRows|StoreApplyMemoryAdoptionLocksBeforeReading|EvolveAdoptedMemoryRecordStrengthensLifecycle)"`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`
