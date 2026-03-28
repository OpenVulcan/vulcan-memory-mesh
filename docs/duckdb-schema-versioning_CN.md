# DuckDB Schema 版本管理说明（中文）

## 文档目标

这份文档说明当前 VMM 如何在 DuckDB 中管理表结构版本，以及调试阶段的 schema 重置策略。

这份说明聚焦：

- `vmm_version` 表的作用
- 当前启动时如何决定是否重置受管表
- 当前 `PostAction` 相关表的最新基线
- 后续扩版本时应如何同步代码和文档

## 当前背景

VMM 当前已经取消 SQLite 运行时支持，长期 SQL 数据通过 DuckDB 网关落地。

相关代码在：

- [internal/adapters/outbound/vldg_duckdb/store.go](../internal/adapters/outbound/vldg_duckdb/store.go)

当前阶段仍处于调试期，因此策略是：

- 不做旧 schema 兼容迁移
- 不保留历史测试数据
- 当版本不匹配时，直接重置当前受管 DuckDB 表并重建基线

## `vmm_version` 表

当前表结构：

```sql
CREATE TABLE IF NOT EXISTS vmm_version (
  singleton_id INTEGER PRIMARY KEY,
  schema_version INTEGER NOT NULL,
  updated_at VARCHAR NOT NULL
);
```

设计说明：

- 这是一个单例表
- 当前只使用一条记录：
  - `singleton_id = 1`
- `schema_version` 表示当前 DuckDB schema 版本
- `updated_at` 记录最近一次版本重建时间

## 当前 schema 版本

当前代码中的版本常量：

- `currentSchemaVersion = 3`

也就是说，当前运行时认为：

- VMM DuckDB schema 当前版本为 `v3`

## 当前基线 schema

当前基线 schema 直接包含这些表：

- `vmm_noise_embeddings`
- `vmm_users`
- `vmm_teams`
- `vmm_spaces`
- `vmm_projects`
- `vmm_sessions`
- `vmm_turn_records`
- `vmm_memory_entries`

再加上版本表：

- `vmm_version`

当前不再属于主线 schema 的对象：

- 历史兼容记忆表
- `vmm_chat_messages`

## 当前启动流程

当前 DuckDB 适配器启动时，按下面顺序执行：

1. 先确保 `vmm_version` 表存在
2. 读取 `singleton_id = 1` 的当前 `schema_version`
3. 如果没有版本记录：
   - 直接清空当前受管表
   - 重建最新基线 schema
   - 写入 `vmm_version`
4. 如果已有版本记录但版本不等于 `currentSchemaVersion`：
   - 直接清空当前受管表
   - 重建最新基线 schema
   - 覆盖写入 `vmm_version`
5. 如果已有版本记录且版本等于 `currentSchemaVersion`：
   - 直接继续启动

可以理解为：

- 新安装：直接创建当前版本
- 旧测试库：直接重建当前版本
- 已是最新：不重复动当前数据

## 为什么当前采用“重建而不是迁移”

原因是：

1. 当前 PostAction 结构还在调试阶段
2. 历史测试数据没有保留价值
3. 直接重建能避免旧 schema 残留影响调试判断

因此当前要求是：

- 代码里只保留当前基线 schema
- 旧版本不做兼容补丁
- 调试阶段遇到旧结构时直接重建

## 当前 `PostAction` 相关表

### `vmm_sessions`

当前 `vmm_sessions` 负责保存会话级聚合状态，核心字段包括：

- `id`
- `session_key`
- `user_id`
- `team_id`
- `space_id`
- `project_id`
- `turn_count`
- `last_summarized_id`
- `summarize_content`
- `summarize_budget`
- `created_timestamp`
- `updated_timestamp`

其中：

- `session_key` 是客户端传入的业务会话键
- `turn_count` 会在每次成功写入一条 `turn` 后递增
- `last_summarized_id` 为后续宏观总结流程预留
- 时间字段统一使用毫秒时间戳

### `vmm_turn_records`

当前 `vmm_turn_records` 负责保存每次 `PostAction` 清洗后的脱水 turn，核心字段包括：

- `id`
- `session_id`
- `project_id`
- `dehydrated_content`
- `dehydrated_budget`
- `extracted_status`
- `created_timestamp`
- `updated_timestamp`

其中：

- `dehydrated_content` 存的是 JSON
- `dehydrated_budget` 是对脱水 JSON 计算出来的 token 预算
- `extracted_status` 当前使用：
  - `0 = 未提取`
  - `1 = 已提取`

## 后续如何升级 schema

后续如果需要新增字段、索引或表，建议按下面步骤操作。

### 1. 增加版本号

把：

```go
const currentSchemaVersion = 3
```

改成新的版本号。

### 2. 更新当前基线 schema

把 `currentSchemaSQL` 和 `deploy/sql/001_init.sql` 一起更新为新的完整基线结构。

### 3. 更新重置逻辑

如果新增了新的受管表，要同步加入：

- `resetManagedSchemaSQL`

避免旧测试库重建时漏删。

### 4. 补测试

至少补两类测试：

1. 全新安装时会重建并写入当前版本
2. 旧版本 schema 会被重置成当前版本

当前相关测试在：

- [internal/adapters/outbound/vldg_duckdb/store_test.go](../internal/adapters/outbound/vldg_duckdb/store_test.go)

## LanceDB 为什么没有共用这套版本表

当前 `vmm_version` 只管理：

- DuckDB 里的 SQL schema

它不管理：

- LanceDB 表结构

原因是：

- LanceDB 是另一个独立后端
- 它的表存在性和索引状态不由 DuckDB 控制

所以当前策略是：

- DuckDB：通过 `vmm_version` 标记当前 SQL 基线
- LanceDB：启动时做幂等初始化，表已存在时视为成功

## 当前建议

如果你后面要继续扩展长期记忆结构，建议遵守：

1. DuckDB 所有结构变化都通过 `schema_version` 管理
2. 当前仓库只维护最新基线，不维护历史兼容迁移链
3. 调试阶段允许直接重建旧测试库
4. 所有新增版本都补单测
5. 文档同步更新这份说明
