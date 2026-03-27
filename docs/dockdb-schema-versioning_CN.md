# DockDB Schema 版本管理说明（中文）

## 文档目标

这份文档说明当前 VMM 如何在 DockDB 中管理表结构版本，以及后续新增字段、索引或表时应如何升级 schema。

这份说明聚焦：

- `vmm_version` 表的作用
- 当前启动时的迁移流程
- 为什么不再每次启动无脑执行全量建表
- 后续扩版本时应该如何改代码

## 当前背景

VMM 当前已经取消 SQLite 运行时支持，长期 SQL 数据通过 DockDB 网关落地。

相关代码在：

- [internal/adapters/outbound/vldg_dockdb/store.go](../internal/adapters/outbound/vldg_dockdb/store.go)

过去的做法是：

- 启动时直接执行整段 schema DDL
- 使用 `CREATE TABLE IF NOT EXISTS ...`
- 使用 `CREATE INDEX IF NOT EXISTS ...`

这个做法虽然简单，但有两个问题：

1. 每次启动都会重复执行整段建表脚本
2. 当后续需要结构升级时，缺少明确的版本管理入口

为了解决这个问题，当前版本引入了：

- `vmm_version`

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
- `schema_version` 表示当前 DockDB schema 版本
- `updated_at` 记录最近一次版本更新的时间

## 当前 schema 版本

当前代码中的版本常量：

- `currentSchemaVersion = 2`

也就是说，当前运行时认为：

- VMM DockDB schema 当前版本为 `v2`

## 当前启动流程

当前 DockDB 适配器启动时，按下面顺序执行：

1. 先确保 `vmm_version` 表存在
2. 读取 `singleton_id = 1` 的当前 `schema_version`
3. 如果没有版本记录，则视为 `0`
4. 从 `当前版本 + 1` 开始逐条执行迁移
5. 每成功执行一条迁移，就回写一次 `vmm_version`

可以理解为：

- 新安装：`0 -> 1`
- 已是最新：不重复跑已有业务 schema
- 后续升级：例如 `1 -> 2 -> 3`

## 当前 `v1` 迁移内容

当前 `v1` schema 包括这些表：

- `vmm_memories`
- `vmm_noise_embeddings`

这些对象目前放在：

- `schemaV1SQL`

## 当前 `v2` 迁移内容

当前 `v2` schema 追加了新的层级和会话存储表：

- `vmm_users`
- `vmm_teams`
- `vmm_spaces`
- `vmm_projects`
- `vmm_sessions`
- `vmm_chat_messages`
- `vmm_memory_entries`

这些对象目前放在：

- `schemaV2SQL`

## 为什么还保留 `CREATE TABLE IF NOT EXISTS`

虽然现在已经有版本表，但 `v1` 迁移脚本里仍然使用：

- `CREATE TABLE IF NOT EXISTS`
- `CREATE INDEX IF NOT EXISTS`

原因是：

1. 新安装时可以直接执行
2. 迁移脚本本身更稳
3. 即使某次网关侧部分对象已存在，也更不容易因为重复创建失败

但注意：

- 版本表的意义不是完全替代 `IF NOT EXISTS`
- 而是避免每次启动都重复跑整套 schema

## 后续如何升级 schema

后续如果需要新增字段、索引或表，建议按下面步骤操作。

### 1. 增加版本号

把：

```go
const currentSchemaVersion = 2
```

改成：

```go
const currentSchemaVersion = 3
```

### 2. 增加新的迁移函数或 SQL 片段

例如新增：

```go
const schemaV2SQL = `
ALTER TABLE ...
CREATE INDEX ...
`
```

### 3. 在 `applySchemaMigration` 里增加对应分支

例如：

```go
func (s *Store) applySchemaMigration(ctx context.Context, version int) error {
	switch version {
	case 1:
		return s.exec(ctx, schemaV1SQL)
	case 2:
		return s.exec(ctx, schemaV2SQL)
	default:
		return fmt.Errorf("unsupported dockdb schema version: %d", version)
	}
}
```

### 4. 补测试

至少补两类测试：

1. 新安装从 `0` 直接迁到最新版本
2. 已有旧版本时只执行缺失迁移，不重复执行旧迁移

当前相关测试在：

- [internal/adapters/outbound/vldg_dockdb/store_test.go](../internal/adapters/outbound/vldg_dockdb/store_test.go)

## 不建议的做法

### 不要直接修改旧版本 SQL 的语义

例如：

- 不要在已经发布后的 `schemaV1SQL` 里随意改表结构语义

更稳的做法是：

- 保留 `v1`
- 新增 `v2`
- 通过新迁移逐步升级

### 不要跳过版本号直接改线上结构

如果你直接手动改网关数据库，而不更新 `currentSchemaVersion` 和迁移逻辑：

- 后续启动无法准确判断当前 schema 状态
- 不利于团队协作和问题追踪

## LanceDB 为什么没有共用这套版本表

当前 `vmm_version` 只管理：

- DockDB 里的 SQL schema

它不管理：

- LanceDB 表结构

原因是：

- LanceDB 是另一个独立后端
- 它的表存在性和索引状态不由 DockDB 控制

所以当前策略是：

- DockDB：走 `vmm_version` 迁移表
- LanceDB：启动时做幂等初始化，表已存在时视为成功

## 当前建议

如果你后面要继续扩展长期记忆结构，建议遵守：

1. DockDB 所有结构变化都通过 `schema_version` 管理
2. 每次只做前向迁移，不回写旧版本语义
3. 所有新增迁移都补单测
4. 文档同步更新这份说明

这样后面无论是增加：

- 新的 session 字段
- 新的消息表
- 摘要表
- 画像表

都可以沿着同一套模式演进。
