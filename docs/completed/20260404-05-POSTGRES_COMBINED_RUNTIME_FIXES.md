# 任务计划：修复 PostgreSQL 组合库运行时缺陷

## 1. 任务目标

针对本次审查确认的两个有效问题完成修复，确保：

- `storage.mode=combined` 启用后，不再把请求路由到大量返回 `not implemented` 的空壳适配器逻辑
- PostgreSQL 组合库在关键关系写入、查询、画像与 compact 链路上具备可运行的最小完整实现
- 向量检索在执行 ANN 查询前，能够在同一事务内正确应用 `vector_probes` 调优参数
- `split` 模式既有行为不回归，`combined` 模式的最小主链路可用

本次任务聚焦于修复已确认缺陷，不额外扩大到迁移命令、文档站点或新的外部契约改造。

## 2. 执行步骤

1. 梳理 PostgreSQL 组合库当前缺口
   - 对照 `unsupported.go`、`app.go`、`usecase` 调用点识别 combined 模式下真实必经链路
   - 识别可直接复用的 SQLite 语义与当前 PostgreSQL 表结构

2. 补齐关系与画像核心实现
   - 为 `Store` 实现 `AppendTurnRecord`
   - 实现 `LoadPendingSessionTurns`、`LoadRecentSessionTurns`、`LoadRecentSessionHistory`
   - 实现 `LoadActiveSessionMemoryNodes`、`LoadRecentDirectMemoryWrites`
   - 实现 `AdvanceSessionExtractWindow`、`ApplyMemoryAdoption`、`MarkSessionCompacted`
   - 实现画像相关的目标加载、节点查询、渲染画像读取、指令创建/失败回写、手工画像应用

3. 实现事务化分析写回
   - 为 `ApplyTurnAnalysis` 建立显式 PostgreSQL 事务
   - 在同一事务内完成记忆节点写入/更新、上下文边维护、被覆盖记忆状态更新、turn 状态推进
   - 保持异常时整体回滚，避免半写入

4. 修复向量检索参数失效
   - 修改向量检索逻辑，在同一事务内先执行 `SET LOCAL ivfflat.probes = ...`
   - 保证参数设置与随后的 `<=>` 查询共享同一连接与事务上下文
   - 对非法 `vector_probes` 做最小值保护

5. 补充测试与验证
   - 为 combined 模式关键实现补单元测试或集成测试
   - 运行受影响模块测试
   - 运行 `go test ./...`
   - 对照 P1、P2 两项问题逐项自检

## 3. 技术选型

- 以当前 `vmm_*` PostgreSQL 共享表结构为基础补实现，不重新设计表模型
  - 原因：当前 schema 已进入运行时装配路径，优先修复“可运行性”比再次重构模型更重要
- 事务型写回统一使用 `pgxpool.Begin` / `tx.Exec` / `tx.QueryRow`
  - 原因：`ApplyTurnAnalysis`、手工画像应用等流程涉及多表联动，必须确保原子性
- 尽量复用现有领域模型和 SQLite 语义
  - 原因：可以降低 split/combined 语义漂移风险，并减少对上层 usecase 的侵入
- 向量调优参数通过 `SET LOCAL` 注入事务级会话配置
  - 原因：既满足同连接生效要求，又避免污染连接池后续请求

## 4. 验收标准

- combined 模式下应用装配后，不再因为核心接口缺失而在正常请求路径上直接返回 `not implemented`
- `PostAction`、`PreCheck`、`ChatCompact`、画像查询/指令、turn 详情等关键路径依赖的 PostgreSQL 存储方法具备最小可运行实现
- `ApplyTurnAnalysis` 在异常时不会留下半完成的多表写入
- 向量检索会在执行查询前实际应用 `vector_probes`
- 新增或更新测试通过
- `go test ./...` 通过

---

## 执行变更总结

### 1. 核心修复与调整概述

- 移除了 PostgreSQL `Store` 对 `unsupportedOperations` 的空壳依赖，改为由真实实现直接覆盖运行时需要的关系、画像、管理与 compact 链路
- 补齐了 combined 模式下 turn 持久化、近期历史读取、分析结果事务写回、记忆采纳、画像指令、生命周期收敛、项目/用户管理等关键能力
- 修复了向量检索未应用 `postgres.vector_probes` 的问题，改为在同一事务连接中先执行 `SET LOCAL ivfflat.probes = N`，再执行 `<=>` ANN 查询
- 增加了编译期端口断言，避免后续再次出现“组合库被装配到运行时，但接口仍未实现完整”的回归

### 2. 📂 文件变更清单

- 修改：`internal/adapters/outbound/vldb_postgres/store.go`
- 修改：`internal/adapters/outbound/vldb_postgres/helpers.go`
- 修改：`internal/adapters/outbound/vldb_postgres/rows.go`
- 修改：`internal/adapters/outbound/vldb_postgres/vector.go`
- 新增：`internal/adapters/outbound/vldb_postgres/turn_store.go`
- 新增：`internal/adapters/outbound/vldb_postgres/analysis_store.go`
- 新增：`internal/adapters/outbound/vldb_postgres/profile_store.go`
- 新增：`internal/adapters/outbound/vldb_postgres/workspace_admin.go`
- 新增：`internal/adapters/outbound/vldb_postgres/contracts.go`

### 3. 💻 关键代码调整详情

- `AppendTurnRecord`
  - 改为在显式 PostgreSQL 事务中先插入 `vmm_turn_records`，再同步更新 `vmm_sessions.turn_count` 与 `summarize_budget`
- `ApplyTurnAnalysis`
  - 改为单事务写回 turn 细节、用户/项目画像 Blob、长期记忆节点、情境边、画像节点，以及 superseded 记忆状态
  - 返回被覆盖记忆的 `vector_id`，保持上层后续清理逻辑不变
- Session/Turn 读取链路
  - 新增 `LoadPendingSessionTurns`、`LoadRecentSessionTurns`、`LoadRecentSessionHistory`、`LoadTurnsByIDs`、`LoadTurnWindows`
  - 新增 `ListIdlePendingSessions`、`AdvanceSessionExtractWindow`、`MarkSessionCompacted`
- 记忆生命周期链路
  - 新增 `LoadActiveSessionMemoryNodes`、`LoadRecentDirectMemoryWrites`、`ApplyMemoryAdoption`
  - 复用统一记忆模型，保证 PostgreSQL 与 SQLite 的生命周期语义保持一致
- 画像链路
  - 新增 `LoadProfileTargets`、`LoadProfileReviewTargets`、`ListActiveProfileNodes`、`LoadRenderedProfile`
  - 新增 `CreateProfileInstruction`、`FailProfileInstruction`、`ApplyManualProfileInstruction`
  - 新增 `ConvergeExpiredProfileNodes`、`ReplaceRenderedProfiles`
- 管理链路
  - 新增 `EnsureProjectPath`、`DeleteProjectPath`、`MigrateProjectPath`、`DeleteUserRef`
  - 补充项目删除范围统计、用户删除确认码生成与共享画像节点脱钩逻辑
- 向量检索链路
  - `Search` 改为事务内执行 `SET LOCAL ivfflat.probes = <N>`，并对 `VectorProbes < 1` 做最小值保护

### 4. ⚠️ 遗留问题与注意事项

- 当前实现按现有 schema 使用 `ivfflat` 索引；如果未来引入 HNSW 索引，需要新增对应运行时参数，并在查询前切换到 `SET LOCAL hnsw.ef_search`
- 本次修复聚焦于 OSS 本地版 combined 运行时的“可运行性”和“事务一致性”，未扩展到新的迁移命令或额外的运维工具链
- 已完成验证：`go test ./internal/adapters/outbound/vldb_postgres` 与 `go test ./...` 均通过
