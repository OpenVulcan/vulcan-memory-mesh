# 任务目标

修复本轮代码审查提出的 3 个维护链路问题，确保：

1. `split` 模式下向量重建在 embedding 维度迁移场景失败后，回滚路径不会把 SQLite 与 LanceDB 留在不一致状态。
2. PostgreSQL 的 `ListProjectMemories` 在线调用继续遵循常规 `query_timeout`，维护场景再走独立维护读取超时。
3. `vmm-migrate -migrate split-to-combined` 能正确接入 `maintenance_tool.postgres.*` 维护超时配置。

# 执行步骤

1. 梳理 `vector_rebuild` 的 split 回滚链路，确认当前回滚为何在维度变化时失效。
2. 调整 split 回滚策略：
   - 维持 durable 数据回滚能力；
   - 避免在新维度 LanceDB 表上回灌旧维度向量；
   - 让失败结果明确表达 sidecar 无法自动恢复的原因。
3. 调整 PostgreSQL 记忆读取超时策略：
   - 恢复 `ListProjectMemories` 走常规在线查询预算；
   - 为维护工具补充专用的长超时读取入口；
   - 更新调用方只在维护链路使用该入口。
4. 调整维护迁移配置装配：
   - 让 `split-to-combined` 迁移传递维护读写超时；
   - 保持现有普通运行时超时与维护超时边界清晰。
5. 为上述修复补充/调整测试，覆盖：
   - split 维度迁移失败后的回滚行为；
   - 在线/维护读取超时分离；
   - 迁移命令维护超时装配。

# 技术选型与处理原则

1. 不扩大运行时接口影响面，优先在现有维护端口上补最小必要能力。
2. 不把维护预算继续混入在线公共入口，维护链路使用显式维护方法或显式维护上下文。
3. 对 split 回滚采用“优先保证 durable 一致性，其次尽量恢复 sidecar；若维度已切换导致旧向量无法回灌，则明确报错并避免伪成功”原则。
4. 继续保持现有仓库的中英文双语注释风格，新增或重写的关键函数补充 Why 说明。

# 验收标准

1. `split` 模式 embedding 维度变化时，重建失败不会伪装成已回滚成功；错误信息能够反映 sidecar 无法按旧维度自动恢复。
2. 在线调用 `ListProjectMemories` 时继续使用 `postgres.query_timeout`，维护导出/重建时才使用 `maintenance_tool.postgres.read_timeout`。
3. `split-to-combined` 迁移会把维护读写超时传给 PostgreSQL 维护逻辑。
4. 相关 Go 测试通过，至少覆盖 `cmd/vmm-migrate`、`internal/app`、`internal/adapters/outbound/vldb_postgres`。

# 执行变更总结

## 1. 核心修复与调整概述

- 修复了 `split` 模式向量重建在 embedding 维度变化时的回滚缺陷：当当前 sidecar 表维度已迁到新值而历史向量仍是旧维度时，不再伪装成“已完整回滚”，而是先恢复 durable、清空当前维度 sidecar 表，再明确返回无法自动恢复历史 sidecar 的错误。
- 恢复了 PostgreSQL `ListProjectMemories` 的在线查询预算，新增维护专用项目记忆枚举端口，确保只有维护链路才使用 `maintenance_tool.postgres.read_timeout`。
- 修复了 `vmm-migrate -migrate split-to-combined` 的 PostgreSQL 维护配置装配，补上传递维护读写超时。

## 2. 📂文件变更清单

- 新增：
  - `internal/adapters/outbound/vldb_postgres/memory_store_timeout_test.go`
- 修改：
  - `cmd/vmm-migrate/migrate.go`
  - `cmd/vmm-migrate/migrate_test.go`
  - `internal/app/ports/interfaces.go`
  - `internal/app/vector_rebuild.go`
  - `internal/app/vector_rebuild_test.go`
  - `internal/adapters/outbound/vldb_lancedb/store.go`
  - `internal/adapters/outbound/vldb_postgres/memory_store.go`
  - `internal/adapters/outbound/vldb_sqlite/store.go`
- 文档归档：
  - 当前计划文件将在验证完成后迁移到 `docs/completed/20260407/10-MAINTENANCE_REVIEW_FIXES.md`

## 3. 💻关键代码调整详情

- 在 `vector_rebuild` 中新增历史向量维度探测与当前 sidecar 维度探测逻辑：
  - 若 split 回滚时发现“历史向量维度 != 当前 sidecar 表维度”，先恢复 durable，再重建当前 sidecar 表为空基线，并返回明确错误，避免再把旧维度向量写入新维度表。
- 在 PostgreSQL memory store 中抽出 `listProjectMemoriesWithQueryerAndContextBuilder`：
  - `ListProjectMemories()` 恢复走 `queryContext`
  - `ListProjectMemoriesForMaintenance()` 显式走 `maintenanceReadContext`
- 在维护链路中让 `loadVectorRebuildRecords()` 优先使用 `MaintenanceProjectMemoryLister`，把长超时限定在维护入口。
- 在 `buildPostgresMaintenanceConfig()` 中补上：
  - `MaintenanceReadTimeout`
  - `MaintenanceWriteTimeout`
- 为上述变更补充测试：
  - split 维度变化时的部分回滚报错与 durable 恢复
  - 维护枚举优先使用维护专用端口
  - PostgreSQL 项目记忆在线/维护超时分离
  - 迁移命令维护超时透传

## 4. ⚠️遗留问题与注意事项

- 当 split 模式在“维度已切换”的前提下重建失败时，系统现在会明确报告当前维度 sidecar 无法自动恢复到历史向量；这属于真实限制而非伪成功。此时应修复 embedding 链路后重新执行重建，或回退到历史 embedding 维度配置再恢复运行。
- 本次只修复了审查指出的 3 个问题，没有改动您当前工作区其余未提交功能的总体设计与行为。
- 已执行定向测试：
  - `go test ./cmd/vmm-migrate ./internal/app ./internal/adapters/outbound/vldb_postgres ./internal/adapters/outbound/vldb_sqlite`
  - `go test ./internal/adapters/outbound/vldb_lancedb`
- 已执行全量测试：
  - `go test ./...`
