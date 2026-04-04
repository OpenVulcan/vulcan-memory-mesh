# 配置示例补充 PostgreSQL DSN 计划

## 1. 任务目标

为本地配置示例补充 PostgreSQL 组合库相关配置，确保测试数据库可以直接按示例填写：

1. 在 `local.json` 中补充 `postgres` 节点示例
2. 在 `openai.local.example.json` 中补充 `postgres` 节点示例
3. 保持现有 split 模式默认行为不被破坏

## 2. 执行步骤

1. 检查当前配置示例文件位置与现有结构
2. 按当前代码支持的配置字段补充 `postgres` 示例配置
3. 确认配置示例与当前 `storage.mode / storage.combined_provider / postgres.*` 设计一致
4. 自检变更结果，必要时同步补充说明

## 3. 技术策略

- 不修改当前默认运行模式
- 只补充示例字段，不引入与代码不一致的旧命名
- 示例值优先采用本地可直接替换的占位 DSN 形式

## 4. 验收标准

- `local.json` 含有可见的 PostgreSQL DSN 示例
- `openai.local.example.json` 含有可见的 PostgreSQL DSN 示例
- 配置字段名称与当前代码实现保持一致

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已为 `configs/local.json` 和 `configs/openai.local.example.json` 补充 `storage` 与 `postgres` 节点。
- 两个示例文件现在都包含 `dsn` 字段，可直接通过 `VMM_POSTGRES_DSN` 或手工改写配置文件来接入测试数据库。
- 默认模式仍保持为 `split`，避免示例文件变化影响现有本地默认启动行为。

### 2. 📂文件变更清单

- 修改：`configs/local.json`
- 修改：`configs/openai.local.example.json`

### 3. 💻关键代码调整详情

- 在两个示例配置文件中新增：
  - `storage.mode = split`
  - `storage.combined_provider = postgres`
  - `postgres.dsn = ${VMM_POSTGRES_DSN}`
  - 以及与当前实现一致的 `schema / flavor / timeout / 连接池 / 检索参数 / migration_batch_size` 字段
- PostgreSQL 示例字段命名与当前 `internal/config/config.go` 中的 `PostgresConfig` 完全对齐，没有引入旧命名或临时别名。

### 4. ⚠️遗留问题与注意事项

- 当前示例文件只补充了 PostgreSQL 组合库配置入口，默认仍不会自动切到 `combined` 模式；如果你要测试组合库，还需要把 `storage.mode` 改为 `combined`。
- 当前 `flavor` 示例默认是 `paradedb`；如果你的测试数据库是标准 PostgreSQL + `pg_trgm/pgvector` 方案，需要手动改成 `standard`。

### 5. ✅验证记录

- `go test ./internal/config`
