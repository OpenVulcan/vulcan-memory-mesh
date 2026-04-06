# Scratchpad 审查问题修复计划

## 任务目标

修复最近 4 份计划相关代码审查中识别出的 3 个真实问题，确保 DWM scratchpad 链路在稳定性、一致性和调试可维护性上达到可长期维护的状态。

本次要解决的问题：

1. SQLite scratchpad 的批量写入/删除虽声明为整批原子，但当前实现仍由多个独立 RPC 组成，存在部分落库后返回失败的风险。
2. PostgreSQL `CreateScratchpadPlan` 在“首条计划并发写入”场景下仍可能撞上唯一键冲突，无法真正复用同计划并发赢家。
3. PostgreSQL `DebugCleanManagedSchema` 未清理最近主线已引入的 recycle / trash / vector GC 表，导致调试清库语义不完整。

## 执行步骤

1. 收敛 SQLite scratchpad 修改路径：
   - 让 `UpsertScratchpadItems`
   - `DeleteScratchpadItems`
   - `CleanScratchpad`
   - `DeleteExpiredScratchpadSessions`
   都进入单脚本事务语义。
2. 收敛 PostgreSQL scratchpad 计划首写流程：
   - 改为“先尝试插入，再在冲突后回读并判定”
   - 避免无行时 `SELECT ... FOR UPDATE` 锁不住未来插入的问题。
3. 收敛 PostgreSQL debug-clean 受管表清单：
   - 覆盖 recycle / trash / vector_gc_jobs 等主线表。
4. 为关键修复补回归测试。
5. 运行最小必测、相关定向测试、全量测试和 `go vet`。
6. 追加执行变更总结，并按规范迁移到 `docs/completed/`。

## 技术方案

### 一、SQLite 原子性修复

最佳修复方向不是继续堆叠 `execBatch + exec`，而是把一个 scratchpad 变更批次收敛成一个显式事务脚本：

- `BEGIN IMMEDIATE`
- 批量节点写入 / 删除
- 计划时间戳刷新
- `COMMIT`

这样：

1. 节点变更与 `plans.updated_timestamp` 刷新具有真正的单批一致性。
2. 调用方看到的“整批成功 / 整批失败”与数据库事实一致。
3. `Clean` 与过期 GC 也能共享同一套事务思路，避免局部删成功、父行删失败的中间态。

### 二、PostgreSQL 并发首写修复

最佳修复方向是把 `CreateScratchpadPlan` 改成：

1. 事务内先执行 `INSERT ... ON CONFLICT DO NOTHING RETURNING ...`
2. 如果插入成功，直接返回新行
3. 如果插入未成功，再 `SELECT ... FOR UPDATE` 读取现存行
4. 若现存行与输入计划名忽略大小写一致，则复用它
5. 若不一致，则返回计划冲突

这样能修复“首条 plan 不存在时，`FOR UPDATE` 无法锁住未来插入”的天然竞态。

### 三、DebugClean 覆盖范围修复

PostgreSQL 的 debug-clean 需要统一抽成受管表名列表，并覆盖：

- recycle batch / recycle jobs
- trash tables
- vector gc jobs
- scratchpad tables
- 其余主线长期表

这样后续新增受管表时，清库逻辑也更容易同步维护。

## 验收标准

1. SQLite scratchpad 批量写入/删除/清理路径变为单事务脚本，不再由多个独立写 RPC 组成。
2. PostgreSQL 首次并发创建同一 scratchpad plan 时，后到者能复用先到者，而不是撞唯一键失败。
3. PostgreSQL debug-clean 能覆盖 recycle / trash / vector_gc_jobs 等当前主线表。
4. 新增或更新测试可以稳定覆盖本次修复的关键行为。
5. 通过：
   - `go test ./internal/adapters/outbound/vldb_sqlite ./internal/adapters/outbound/vldb_postgres`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`

## 风险与注意事项

1. SQLite 事务脚本需要使用安全的 SQL 字面量构造，不能引入未转义文本。
2. PostgreSQL 首写修复必须保持现有计划守卫语义不变，不能把“不同 plan”错误放宽成静默复用。
3. debug-clean 表清单收口时，不能漏掉最近 retention 主线已落地的表。

## 执行变更总结

### 1. 核心修复与调整概述

- 已将 SQLite scratchpad 的 upsert、delete、clean 与过期 GC 路径统一收敛为单条显式事务脚本，确保节点写入/删除与计划时间戳刷新、父计划删除具备真实的整批原子性。
- 已将 PostgreSQL scratchpad 首写流程改为“先尝试 `INSERT ... ON CONFLICT DO NOTHING RETURNING`，失败后回读复用赢家”的并发收敛策略，修复首条计划并发写入撞唯一键的问题。
- 已补齐 PostgreSQL debug-clean 的受管表名单，覆盖 scratchpad、recycle、trash 与 vector GC 主线表，并新增 helper 级回归测试。

### 2. 📂 文件变更清单

#### 修改

- `internal/adapters/outbound/vldb_sqlite/scratchpad.go`
- `internal/adapters/outbound/vldb_sqlite/scratchpad_test.go`
- `internal/adapters/outbound/vldb_postgres/scratchpad.go`
- `internal/adapters/outbound/vldb_postgres/debug_clean.go`

#### 新增

- `internal/adapters/outbound/vldb_postgres/scratchpad_test.go`

### 3. 💻 关键代码调整详情

- SQLite scratchpad：
  - 删除了 `execBatch + exec`、`delete + exec` 这类多 RPC 组合路径。
  - 新增 `buildSQLiteScratchpadUpsertScript`、`buildSQLiteScratchpadDeleteScript`、`buildSQLiteScratchpadCleanScript`、`buildSQLiteScratchpadGCDeleteScript`，统一通过 `BEGIN IMMEDIATE ... COMMIT` 保证单批一致性。
  - `UpsertScratchpadItems` 现在先加载已有节点 id，再为新 key 分配连续 id，最后一次性提交原子脚本。
- PostgreSQL scratchpad：
  - `CreateScratchpadPlan` 改为先插入、后回读，避免“空范围 `FOR UPDATE` 锁不住未来插入”的竞态。
  - 新增 `buildPostgresScratchpadPlanInsertSQL`，把并发首写的核心 SQL 提炼为可回归 helper。
- PostgreSQL debug-clean：
  - 新增 `postgresManagedTableNamesForDebugClean`，集中管理受管表清单。
  - 清单已覆盖 `vmm_memory_nodes_trash`、`vmm_memory_context_edges_trash`、`vmm_turn_records_trash`、`vmm_recycle_jobs`、`vmm_recycle_batches`、`vmm_vector_gc_jobs` 等主线表。
- 回归测试：
  - SQLite 新增对 upsert/delete/GC 单事务脚本的断言。
  - PostgreSQL 新增对 scratchpad 首写 SQL 冲突守卫与 debug-clean 表覆盖范围的 helper 级测试。
  - 已通过：
    - `go test ./internal/adapters/outbound/vldb_sqlite ./internal/adapters/outbound/vldb_postgres`
    - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
    - `go test ./...`
    - `go vet ./...`

### 4. ⚠️ 遗留问题与注意事项

- 本轮仅修复审查发现的 3 个问题，没有改动 scratchpad gRPC 契约或外部文档。
- SQLite 的事务原子性目前通过显式脚本落地，后续若底层网关改为支持真正的参数化多语句事务 RPC，可以再评估是否切回更强类型的事务接口。
