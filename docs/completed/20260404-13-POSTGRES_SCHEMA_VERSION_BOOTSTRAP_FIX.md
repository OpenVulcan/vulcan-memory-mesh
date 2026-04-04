# PostgreSQL 组合库版本初始化与校验修复计划

## 1. 任务目标

修复 PostgreSQL 组合库 `vmm_schema_versions` 的初始化与使用缺陷，确保：

1. 首次建库成功后会写入当前版本号，而不是仅在调试迁移时写入；
2. 启动阶段会消费版本元数据，至少具备基础一致性校验能力；
3. 版本常量与版本键定义归属于正式 schema 语义层，而不是挂在调试迁移文件中。

## 2. 问题现状

当前代码存在以下问题：

1. `postgres_combined` 与 `postgres_combined_search_<flavor>` 只在 `debug-migrate` 导入完成后写入；
2. 正常启动路径虽然会创建 `vmm_schema_versions`，但不会补写初始版本；
3. 启动路径也不会读取版本表做兼容性判断；
4. 当前 schema 版本常量定义在 `internal/adapters/outbound/vldb_postgres/debug_migrate.go`，职责归属不合理。

## 3. 执行步骤

1. 抽离 PostgreSQL 组合库的共享版本常量与版本键辅助逻辑；
2. 在正式启动 bootstrap 路径中增加：
   - schema version 表初始化；
   - 当前版本兼容性检查；
   - 缺失版本记录的自动补写；
3. 调整调试迁移逻辑，复用新的共享版本写入逻辑；
4. 增加纯单元测试，覆盖版本键和版本校验辅助逻辑；
5. 执行相关测试与全量回归；
6. 回填执行总结并归档计划。

## 4. 技术策略

### 4.1 版本语义

- `postgres_combined`：共享物理表结构版本；
- `postgres_combined_search_<flavor>`：当前 flavor 搜索布局版本。

### 4.2 本轮修复范围

- 允许“版本缺失”场景自动补写当前版本，适用于初始建库和历史 v1 库回填；
- 对“版本高于当前运行时代码”与“版本低于当前运行时代码但尚无 migration runner”的情况，采用显式失败；
- 不在本轮实现完整 PostgreSQL migration runner。

## 5. 验收标准

1. 首次启动 PostgreSQL 组合库后，`vmm_schema_versions` 中存在当前共享版本与当前 flavor 搜索版本；
2. 调试迁移不再独占版本写入逻辑；
3. 如果未来版本不一致，启动阶段能返回明确错误，而不是静默继续；
4. 新增测试通过；
5. `go test ./...` 通过。

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已修复 PostgreSQL 组合库“首次建库不写版本、只有调试迁移才写版本”的问题。
- 已将版本语义从 `debug_migrate.go` 抽离到正式共享模块，避免正式 schema 版本定义依附于调试迁移文件。
- 已在启动 bootstrap 中加入版本兼容校验：
  - 缺失版本记录时允许继续启动，并在成功完成 schema/bootstrap 后自动补写当前版本；
  - 若已记录版本高于当前运行时代码，启动直接失败；
  - 若已记录版本低于当前运行时代码，因尚无 PostgreSQL migration runner，启动直接失败。
- 已让调试迁移路径复用同一套共享版本写入逻辑，不再维护一套重复常量。

### 2. 📂 文件变更清单

- 新增：`internal/adapters/outbound/vldb_postgres/schema_version.go`
- 新增：`internal/adapters/outbound/vldb_postgres/schema_version_test.go`
- 修改：`internal/adapters/outbound/vldb_postgres/schema.go`
- 修改：`internal/adapters/outbound/vldb_postgres/debug_migrate.go`

### 3. 💻 关键代码调整详情

- `schema_version.go`
  - 新增 `currentCombinedSchemaVersion` 与 `currentCombinedSearchSchemaVersion` 的共享定义；
  - 新增 `searchSchemaVersionComponent(flavor)`；
  - 新增 tracked schema 组件枚举、版本校验、缺失版本补写和强制写回逻辑。
- `schema.go`
  - 将 schema bootstrap 顺序调整为：
    1. 创建 schema/version table；
    2. 校验已存在版本是否与当前运行时代码兼容；
    3. 创建其余共享表、索引、种子与搜索索引；
    4. 启动成功后补写缺失版本记录。
- `debug_migrate.go`
  - 删除迁移文件内部自带的版本常量与搜索版本键函数；
  - 迁移后恢复版本记录改为统一走共享 `writeTrackedSchemaVersions`。
- `schema_version_test.go`
  - 覆盖版本组件集合、搜索版本键生成，以及“缺失允许 / 漂移报错”的纯单元测试。

### 4. ⚠️遗留问题与注意事项

- 当前仅完成“版本初始化 + 版本漂移 fail-fast”，尚未实现真正的 PostgreSQL migration runner。
- 这意味着：
  - 缺失版本记录的历史 v1 库会被自动补写；
  - 但未来若代码把版本提升到 `2`，旧库会被明确拒绝启动，直到真正实现迁移逻辑。
- 搜索版本目前仍只跟踪当前 flavor；如果未来需要在 flavor 切换时主动清理旧 flavor 残留索引，还需要单独补一轮索引收敛策略。

### 5. ✅ 验证记录

- `go test ./internal/adapters/outbound/vldb_postgres ./internal/config ./internal/app ./internal/app/usecase`
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
- `go test ./...`
- 真实冒烟验证：
  - 使用全新测试 schema 启动 combined 模式；
  - 成功观察到 `vmm_schema_versions` 自动写入：
    - `postgres_combined=1`
    - `postgres_combined_search_paradedb=1`
