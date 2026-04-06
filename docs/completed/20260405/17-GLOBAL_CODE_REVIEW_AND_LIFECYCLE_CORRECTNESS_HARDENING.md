# 全局代码审核与生命周期正确性加固执行计划

## 1. 任务目标

本阶段基于第 16 阶段已经完成的统一检索分数契约收口，继续执行新一轮全局代码审核，重点检查记忆生命周期、检索结果解释、回收维护与近期多轮修复叠加后的边界一致性，确认是否仍存在会影响功能稳定性、性能、正确性或运行时无干扰目标的真实风险，并以最优方案完成修复。

具体目标如下：

1. 复核统一检索、生命周期更新、回收维护之间的交界面，确认是否仍存在冷热状态不一致、统计漂移、错误排序、错误回收或后台副作用等问题。
2. 仅修复能够通过代码逻辑、测试或运行时语义证明的真实问题，不做猜测式重构。
3. 在不牺牲稳定性、效率和可维护性的前提下，完成必要代码修复与测试补强。
4. 完成回归验证、自检、计划归档、提交与推送，保持工程闭环。

## 2. 审核范围

### 2.1 本轮重点覆盖

1. `internal/app/usecase/memory_query.go`
2. `internal/app/usecase/precheck.go`
3. `internal/app/usecase/retention.go`
4. `internal/adapters/outbound/vldb_sqlite/retention_store.go`
5. `internal/adapters/outbound/vldb_postgres/retention_store.go`
6. 与生命周期读时行为、检索解释和回收统计相关的测试与文档

### 2.2 本轮不主动扩展

1. 不引入计划外的大型架构重构。
2. 不修改对外接口定义，除非确认存在阻断级错误且存在低风险最优解。
3. 不做与当前风险无关的风格性清理。

## 3. 执行策略

1. 基于当前主分支最新状态执行定向深审，优先覆盖最近多轮修复叠加后最容易发生语义漂移的生命周期与回收边界。
2. 若发现真实问题，优先在 usecase 或单一适配器边界内收口，避免扩大改动面。
3. 修复方案必须同时满足：
   - 不破坏既有成功路径契约；
   - 不增加明显额外 provider 往返、锁竞争或后台噪声；
   - 能通过测试长期约束；
   - 便于后续维护者继续理解和演进。

## 4. 详细执行步骤

1. 确认仓库基线并建立本阶段计划文件。
2. 深审统一检索、生命周期更新与回收维护关键链路，定位真实风险问题。
3. 采用最优方案实施修复，并补齐必要测试。
4. 运行最少必测集、全量测试与必要静态检查。
5. 对照计划逐项自检，补写执行变更总结。
6. 将计划迁移至 `docs/completed/`，完成 `git commit` 与 `git push`。

## 5. 验收标准

1. 必须明确说明本轮定位到的真实问题与修复原因。
2. 修复后不得引入新的生命周期漂移、检索错误、冷热状态不一致或后台维护副作用。
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

1. 本轮全局审核确认了一个生命周期正确性漏洞：`ApplyMemoryAdoption` 在 SQLite 和 PostgreSQL 两端都只检查 `memory_status == active`，没有在采纳事务开始时重新确认“该行此刻是否仍未过期”。
2. 这意味着某条记忆如果在检索完成后、pre-check 采纳回写开始前刚好越过过期点，当前实现仍会把它继续强化并延长有效期，相当于把已经退出热路径的冷状态记忆悄悄写活。
3. 本轮把“active + unexpired” 判定上收成共享领域 helper，并让查询层热路径、SQLite 生命周期回写、PostgreSQL 生命周期回写都复用同一套判定，确保采纳只能强化在当前时刻仍属于热路径的记忆。

### 2. 📂文件变更清单

1. 修改：`internal/logic/domain/memory.go`
2. 新增：`internal/logic/domain/memory_test.go`
3. 修改：`internal/app/usecase/memory_query.go`
4. 修改：`internal/adapters/outbound/vldb_sqlite/store.go`
5. 修改：`internal/adapters/outbound/vldb_sqlite/store_test.go`
6. 修改：`internal/adapters/outbound/vldb_postgres/analysis_store.go`

### 3. 💻关键代码调整详情

1. 在领域层新增 `MemoryNodeRecordIsActiveUnexpiredAt`，把 `active + unexpired` 热路径契约变成统一可复用判定。
2. 将查询用例里的 `memoryNodeRecordIsActiveUnexpiredAt` 改为复用共享领域 helper，避免查询层与存储层出现两套逐渐漂移的热状态定义。
3. 将 SQLite `ApplyMemoryAdoption` 与 PostgreSQL `ApplyMemoryAdoption` 的采纳写回过滤条件收紧为“当前时刻仍然 active 且未过期”，阻止过期边界上的 active 行被采纳事务重新写活。
4. 新增领域层生命周期判定测试，以及 SQLite 行为测试 `TestStoreApplyMemoryAdoptionSkipsExpiredActiveRows`，验证刚刚过期的 active 记忆不会触发实际写回脚本。

### 4. ⚠️遗留问题与注意事项

1. 本轮没有改变“active 但 expires_at 已过期”这类历史脏数据本身的清理节奏；它们仍由现有 retention / 热路径过滤负责退出可见面。本次修复补的是“生命周期回写不能把它们重新强化”的事务边界防线。
2. PostgreSQL 侧没有新增专门的适配器行为测试，而是通过共享领域判定与 SQLite 事务行为测试共同约束；原因是 PostgreSQL 当前适配器测试基线主要是纯辅助函数层，本轮优先保持低干扰收口。
3. 已完成验证：
   - `go test ./internal/logic/domain ./internal/adapters/outbound/vldb_sqlite -run "Test(MemoryNodeRecordIsActiveUnexpiredAt|StoreApplyMemoryAdoptionSkipsExpiredActiveRows|EvolveAdoptedMemoryRecordStrengthensLifecycle)"`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`
