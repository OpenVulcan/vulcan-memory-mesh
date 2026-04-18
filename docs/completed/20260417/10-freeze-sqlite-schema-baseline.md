# 任务计划：冻结 SQLite Schema 基线为 19 并保留未来升级框架

## 任务目标

本次任务目标是把当前 SQLite schema 版本体系收敛到“仅支持基线 19”，移除已经不再需要的旧版本兼容迁移链，同时保留自动升级框架，使未来出现 20 版时可以直接从 19 平滑升级到 20。

## 执行步骤

1. 梳理当前 SQLite schema 版本加载、bootstrap、迁移和 legacy reset 逻辑，明确需要保留与移除的边界。
2. 将迁移框架调整为“单基线模式”：
   - 空库初始化到 19
   - 仅接受 19 为当前受支持版本
   - 小于 19 的库直接报不支持
   - 大于 19 的库继续报版本过新
3. 删除已失效的旧迁移链与 legacy 兼容读写逻辑，同时保留未来追加 `19 -> 20` step 的入口。
4. 同步更新测试与文档，确保行为和约束明确可验证。
5. 运行相关测试，补充执行变更总结并归档计划文件。

## 技术选型与处理原则

- 保持 `currentSchemaVersion = 19` 不变，不回拨版本号。
- 不删除 schema migration 框架本身，只删除已经无意义的历史兼容路径。
- 明确区分“空库初始化”和“旧库升级”，避免把不再支持的旧库静默重建。
- 新增或调整的注释、测试必须遵守仓库双语规范。

## 验收标准

1. 当前代码只支持 SQLite schema 基线 19。
2. 空库仍能自动 bootstrap 到 19。
3. 低于 19 的旧库不会再走自动 reset / 自动迁移，而是返回明确错误。
4. 未来仍可通过追加 step 的方式实现 `19 -> 20` 升级。
5. 相关测试和文档同步更新并通过。

## 执行变更总结

### 1. 核心修复与调整概述

本次调整将 SQLite schema 迁移体系收敛为“基线 19 单点支持”模式：空库仍自动初始化到 19；现存库只有 19 被视为受支持版本；低于 19 的历史库不再尝试自动 reset 或兼容迁移，而是返回明确错误；未来仍可以在现有迁移框架中直接追加 `19 -> 20` 的升级 step。

### 2. 📂文件变更清单

新增：
- 无

修改：
- `internal/adapters/outbound/vldb_sqlite/schema_migrations.go`
- `internal/adapters/outbound/vldb_sqlite/store.go`
- `internal/adapters/outbound/vldb_sqlite/debug_clean.go`
- `internal/adapters/outbound/vldb_sqlite/store_test.go`
- `README.md`

删除：
- 无

### 3. 💻关键代码调整详情

- 在 `schema_migrations.go` 中为迁移计划增加最小支持版本约束，保留 bootstrap 与 future-step 执行框架，同时删除旧的 incremental baseline、legacy reset 与 `vmm_version` 兼容读写逻辑。
- 在 `store.go` 中移除与旧单行版本表相关的常量与 reset SQL，避免常规链路再携带历史版本兼容语义。
- 在 `debug_clean.go` 中独立维护调试清理 SQL，确保调试清理和正式 schema 管理解耦。
- 在测试中补充“空库 bootstrap 到 19”“低于 19 明确拒绝”“未来 19->20 step 仍可执行”等断言，验证当前基线冻结不会破坏后续升级能力。

### 4. ⚠️遗留问题与注意事项

- 当前 `internal/app` 的完整运行时测试在 Windows 环境下仍可能触发上游 `vldb-lancedb` 动态库的 `tokio EnterGuard` 生命周期问题，这属于上游 runtime 稳定性限制，不是本次 SQLite schema 基线冻结引入的回归。
- 后续若引入 SQLite schema 20，只需要在现有迁移框架中追加 `19 -> 20` step，无需重新恢复历史版本兼容链。
