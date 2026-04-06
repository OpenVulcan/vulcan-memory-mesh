# PostgreSQL Scratchpad 自动升级补齐计划

## 任务目标

补齐 PostgreSQL 组合库存储路径下的 schema 自动升级能力，解决当前“仅允许版本完全一致、旧版本直接启动失败”的缺口。

本次目标聚焦：

1. 为 PostgreSQL 建立最小可维护的增量升级框架。
2. 将 scratchpad 双表与相关索引正式纳入 PostgreSQL 的显式 schema 版本升级链。
3. 保持新库启动仍可直接 bootstrap 到最新版本。
4. 保持旧库在检测到可升级版本时自动升级，而不是报错退出。
5. 补齐测试与文档说明，明确这次升级相对 plan30 的补充关系。

## 背景与本次相对 plan30 的变化

`20260405-30-DWM_SCRATCHPAD_CONTRACT_ALIGNMENT.md` 已经完成了 scratchpad 功能本身，但 PostgreSQL 侧仍存在一个真实缺口：

- scratchpad 表会被 `ensureSchema()` 的 `CREATE TABLE IF NOT EXISTS` 自动补齐；
- 但 PostgreSQL 的 schema version 机制仍会在检测到“已记录版本 < 当前目标版本”时直接报错；
- 这意味着它还不是一套真正的“自动升级”机制。

因此本计划不重复实现 scratchpad CRUD，而是只处理 PostgreSQL 自动升级缺口。

## 执行步骤

1. 梳理 PostgreSQL 当前 schema version 启动链路。
2. 设计 PostgreSQL 组合库的最小增量迁移框架。
3. 将共享 schema 版本从当前版本提升一代，并定义 `1 -> 2` 的 scratchpad 升级步骤。
4. 调整启动流程：
   - 先初始化版本表；
   - 再执行可升级版本的迁移步骤；
   - 最后执行当前版本的幂等 schema ensure；
   - 补写/更新版本记录。
5. 为迁移框架补单元测试，覆盖：
   - 空版本 bootstrap；
   - 旧版本自动升级；
   - 无迁移路径时报错；
   - 更新后版本号正确推进。
6. 更新 README 或相关说明文档，明确 PostgreSQL 现在支持 scratchpad 相关自动升级。
7. 运行最小必测与全量测试，确保没有回归。

## 技术方案

### 一、版本策略

PostgreSQL 当前共享 schema 版本从 `1` 提升到 `2`。

版本 `2` 的定义：

- 将 `vmm_scratchpad_plans`
- `vmm_scratchpad_nodes`
- 以及相关索引

正式纳入“显式版本升级语义”，不再仅仅依赖当前大而全的 `ensureSchema()` 隐式补表。

### 二、迁移框架

新增 PostgreSQL 侧的最小迁移描述结构，类似 SQLite 的组件化思想，但保持实现更轻量：

- 组件键：继续沿用 `postgres_combined`
- 版本步进：先只支持 `1 -> 2`
- 每一步迁移必须：
  - 幂等
  - 可重复执行
  - 不依赖外部人工介入

### 三、启动顺序

PostgreSQL 启动链路调整为：

1. `ensureSchemaVersionTable`
2. 校验“是否出现比当前运行时更高的不可能版本”
3. 对低于当前版本、且存在迁移路径的组件执行自动升级
4. 执行当前版本全量 `ensureSchema()` 幂等补齐
5. 写回最新 schema version

### 四、兼容性原则

本次只解决：

- PostgreSQL 共享 schema 从 `1 -> 2` 的自动升级

不在本次范围内处理：

- 任意历史版本的大跨度升级
- 未定义迁移路径时的自动推断
- scratchpad 之外的额外版本跳变

若未来继续有 PostgreSQL schema 演进，应在本次框架上继续追加新 step。

## 验收标准

1. PostgreSQL 旧库记录版本为 `1` 时，启动不会再因“automatic postgres migration is not implemented yet”失败。
2. PostgreSQL 启动后能够自动推进到最新 schema 版本。
3. 新库仍能直接 bootstrap 到最新版本。
4. scratchpad 相关表和索引在旧库升级后可用。
5. 测试通过：
   - `go test ./internal/adapters/outbound/vldb_postgres`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`

## 风险与注意事项

1. 迁移步骤必须保持幂等，避免旧库反复启动时重复失败。
2. 不能把 PostgreSQL 迁移逻辑写成 scratchpad 专属临时代码，必须沉淀成可继续扩展的框架。
3. 不能影响现有新库 bootstrap 语义。
4. 若发现仓库内对 PostgreSQL schema version 有其他隐藏假设，需要同步修正测试与注释。

## 执行变更总结

### 1. 核心修复与调整概述

本轮已补齐 PostgreSQL 组合库存储的自动升级能力，解决了“tracked schema 版本落后时直接失败、无法自动升级”的缺口。当前 PostgreSQL 共享 schema 版本已从 `1` 提升到 `2`，并正式引入显式的 `1 -> 2` 升级步骤，把 scratchpad 双表及其索引纳入可追踪的升级链。

同时，启动流程已从“仅校验版本”调整为“版本表初始化 -> 可升级版本迁移 -> 当前 schema 幂等补齐 -> 版本回填”，因此旧库在版本可迁移时将自动前进，而不会再因为 `automatic postgres migration is not implemented yet` 被阻断。

### 2. 📂 文件变更清单

新增文件：

- `internal/adapters/outbound/vldb_postgres/schema_migrations.go`

修改文件：

- `internal/adapters/outbound/vldb_postgres/schema.go`
- `internal/adapters/outbound/vldb_postgres/schema_version.go`
- `internal/adapters/outbound/vldb_postgres/schema_version_test.go`
- `README.md`

### 3. 💻 关键代码调整详情

1. 新增 PostgreSQL 迁移框架：
   - 引入 `trackedSchemaMigrationStep`
   - 引入 `trackedSchemaMigrationSteps`
   - 引入 `resolveTrackedSchemaMigrationPath`
   - 引入 `applyTrackedSchemaMigrations`
   以上逻辑用于把旧版本升级路径显式化，而不是继续依赖启动失败提示。

2. 共享 schema 版本升级：
   - `currentCombinedSchemaVersion` 从 `1` 提升为 `2`
   - 当前新增显式迁移步骤：
     - `postgres_combined: 1 -> 2`
   - 本次升级内容为：
     - `vmm_scratchpad_plans`
     - `vmm_scratchpad_nodes`
     - 以及 scratchpad 相关索引

3. 启动顺序调整：
   - `ensureSchema()` 现在先执行 `applyTrackedSchemaMigrations()`
   - 再执行当前版本的全量 `ensureSchema` 幂等补齐
   - 最后通过 `backfillTrackedSchemaVersions()` 写回缺失版本记录

4. 提取 scratchpad DDL 复用：
   - 新增 `scratchpadPlansTableDDL()`
   - 新增 `scratchpadNodesTableDDL()`
   - 新增 `scratchpadIndexDDLs()`
   - 避免 bootstrap 与 migration 各自维护两套 scratchpad DDL

5. 测试更新：
   - 调整 PostgreSQL schema version 单元测试，使“旧版本可迁移”成为合法语义
   - 新增迁移路径解析测试，覆盖：
     - 存在 `1 -> 2` 升级路径
     - 能顺序构造路径
     - 缺失路径时确定性报错

### 4. ⚠️ 遗留问题与注意事项

1. 当前 PostgreSQL 自动升级仅覆盖已显式声明的步骤；本轮只实现了共享 schema `1 -> 2`。
2. 搜索组件版本 `postgres_combined_search_*` 目前仍为 `1`，因此本轮没有新增对应迁移步骤。
3. 后续如果 PostgreSQL schema 再演进，必须继续在 `trackedSchemaMigrationSteps()` 中追加新 step，而不是重新退回“版本落后直接报错”的策略。
4. 本轮没有引入任何 scratchpad 之外的新 DDL 变更，因此升级面严格控制在既有功能缺口内。
