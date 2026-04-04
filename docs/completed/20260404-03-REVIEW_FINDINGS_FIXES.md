# 任务计划：修复未提交改动中的审查问题

## 1. 任务目标

针对当前未提交改动中已确认的 3 个有效审查问题完成修复，确保：

- SQLite schema 版本低于 `14` 的本地历史库仍可顺利启动，不会因为迁移链断裂而失败
- `ChatCompact` 记录 compact 边界时不会因为并发追加 turn 而写入过期边界
- 向量表重建时不会把已经过期的长期记忆重新灌回 LanceDB，避免召回结果被无效候选污染

本次修复只解决上述审查问题，不扩大接口契约与业务语义范围。

## 2. 执行步骤

1. 修复 SQLite 迁移链
   - 检查当前 `schema_migrations.go` 的迁移入口与历史版本回退逻辑
   - 为 `14` 之前的历史版本补齐兼容处理，保证旧本地库不会因缺失中间迁移步骤而阻塞启动
   - 保持当前增量迁移框架可继续追加后续版本

2. 修复 compact 边界并发窗口
   - 调整 `MarkSessionCompacted` 的加锁范围
   - 保证“读取 session 最新 turn”与“写入 compact 边界”处于同一串行化区间
   - 避免 post-action 并发写入导致边界落后

3. 修复向量重建的数据源筛选
   - 调整 `ListProjectMemories` 的查询条件
   - 只返回 active 且未过期的长期记忆行
   - 保持向量重建结果与正常查询期的有效数据口径一致

4. 补充或更新测试
   - 为旧 schema 版本兼容路径补测试
   - 为 compact 边界并发窗口相关行为补测试或收紧已有测试
   - 为向量重建过滤过期数据补测试

5. 验证
   - 运行受影响模块测试
   - 运行 `go test ./...`
   - 对照 3 个审查问题逐项确认已闭环

## 3. 技术选型

- 对旧 schema 版本采用“显式兼容旧版 + 必要时安全重建到当前版本”的方式
  - 原因：当前迁移链从 `14 -> 15` 开始，历史更老的本地库缺少完整增量链；直接报错会把本地升级变成阻塞故障
- 对 compact 边界更新采用同一把写锁包住“读取最新 turn + 写入 session 边界”
  - 原因：当前 `AppendTurnRecord` 已用同一把锁串行化 turn 追加，只要读取也纳入同一临界区即可消除竞态窗口
- 向量重建继续以 SQLite 主记忆表为事实源，但查询条件与运行时 recall 一致
  - 原因：重建后的 LanceDB 内容应与当前有效长期记忆集合保持一致，不能把已过期行重新带回 ANN 候选池

## 4. 验收标准

- 旧版 SQLite 本地库在 schema 版本 `< 14` 时不再因“无迁移路径”导致启动失败
- `MarkSessionCompacted` 不再存在先读最新 turn、后加锁写边界的竞态窗口
- 向量表重建不会回灌 expired 的长期记忆
- 新增或更新测试通过
- `go test ./...` 通过

## 执行变更总结

### 1. 核心修复与调整概述

- 为 SQLite 增量迁移框架补上了 `14` 之前历史版本的兼容恢复路径：当检测到旧版本地库版本早于当前显式增量迁移基线时，不再直接报“无迁移路径”，而是按旧行为重建到当前 schema 并继续写入新版本号。
- 调整了 `MarkSessionCompacted` 的临界区范围，将“读取 session 最新 turn”与“写入 compact 边界”合并到同一把写锁内，消除了与 `AppendTurnRecord` 并发时可能写入过期边界的问题。
- 收紧了 `ListProjectMemories` 的查询口径，使向量表重建仅回放 active 且未过期的长期记忆，避免把已经过期的数据重新灌入 LanceDB。
- 补充了对应回归测试，覆盖旧 schema 恢复、compact 边界加锁顺序、向量重建过滤过期数据三条修复路径。

### 2. 📂 文件变更清单

- 修改：
  - `internal/adapters/outbound/vldb_sqlite/schema_migrations.go`
  - `internal/adapters/outbound/vldb_sqlite/session_compact.go`
  - `internal/adapters/outbound/vldb_sqlite/store.go`
  - `internal/adapters/outbound/vldb_sqlite/store_test.go`
  - `docs/plan/20260404-03-REVIEW_FINDINGS_FIXES.md`
- 新增：
  - 无
- 删除：
  - 无

### 3. 💻 关键代码调整详情

- 在 `schema_migrations.go` 中新增 `legacySQLiteIncrementalMigrationBaseline`、`requiresLegacySQLiteReset` 与 `resetLegacySQLiteSchemaToCurrent`，让旧版 SQLite 本地库在版本早于显式迁移基线时走“受控重建到当前 schema”的兼容分支，而不是落入缺失迁移路径报错。
- 在 `session_compact.go` 中把 `writeMu` 加锁位置前移到 `MAX(id)` 查询之前，确保最新 turn 读取与 compact 边界更新之间没有竞态窗口。
- 在 `store.go` 的 `ListProjectMemories` 中复用 `buildActiveUnexpiredMemoryCondition`，让向量重建数据源与运行时有效记忆筛选规则保持一致。
- 在 `store_test.go` 中新增 3 组测试：
  - 旧版 `<14` schema 自动恢复测试
  - `MarkSessionCompacted` 在释放写锁前不会提前读取最新 turn 的并发顺序测试
  - `ListProjectMemories` 会携带未过期过滤条件的 SQL 断言测试

### 4. ⚠️ 遗留问题与注意事项

- 对于 `14` 之前的历史本地库，本次恢复路径沿用旧行为语义，即优先保证本地启动兼容，而不是尝试为未知旧结构补齐完整的逐版本无损迁移。
- 本次修复没有改动 gRPC 契约，也没有扩大 `ChatCompact` / `PreCheck` 的外部接口语义，仅修复内部启动与召回一致性问题。
- 已完成验证：
  - `go test ./internal/adapters/outbound/vldb_sqlite ./internal/app`
  - `go test ./...`
