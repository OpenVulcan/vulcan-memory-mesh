# DockDB Schema 版本管理说明（中文）

## 文档目标

这份文档说明当前 VMM 如何在 DockDB 中管理表结构版本，以及后续新增字段、索引或表时应如何升级 schema。

这份说明聚焦：

- `vmm_version` 表的作用
- 当前启动时的 schema 引导流程
- 为什么当前运行时不再保留旧版本兼容迁移
- 后续扩版本时应该如何改代码

## 当前背景

VMM 当前已经取消 SQLite 运行时支持，长期 SQL 数据通过 DockDB 网关落地。

相关代码在：

- [internal/adapters/outbound/vldg_dockdb/store.go](../internal/adapters/outbound/vldg_dockdb/store.go)

当前运行时只接受：

- 当前基线 schema

当前运行时不再负责：

- 自动补齐旧版 `vmm_version` 表字段
- 继续创建历史兼容记忆表
- 从旧 schema 版本逐步迁到最新版本

如果现有 DockDB 里保存的是旧版本 schema：

- 运行时会直接报错
- 需要先人工清理或重建到当前基线结构

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

## 当前基线 schema

当前基线 schema 直接包含这些表：

- `vmm_noise_embeddings`
- `vmm_users`
- `vmm_teams`
- `vmm_spaces`
- `vmm_projects`
- `vmm_sessions`
- `vmm_chat_messages`
- `vmm_memory_entries`

再加上版本表：

- `vmm_version`

当前不再属于主线 schema 的对象：

- 历史兼容记忆表

## 当前启动流程

当前 DockDB 适配器启动时，按下面顺序执行：

1. 先确保 `vmm_version` 表存在
2. 读取 `singleton_id = 1` 的当前 `schema_version`
3. 如果没有版本记录，则视为“全新安装”
4. 全新安装时：
   - 直接执行当前基线 schema
   - 再写入 `vmm_version`
5. 如果已有版本记录且版本等于 `currentSchemaVersion`
   - 直接继续启动
6. 如果已有版本记录但版本不等于 `currentSchemaVersion`
   - 直接报错并拒绝继续兼容启动

可以理解为：

- 新安装：直接引导到当前版本
- 已是最新：不重复跑业务 schema
- 旧版本：直接失败，不做兼容迁移

## 为什么不再保留旧版本兼容迁移

当前策略改成“只接受当前基线 schema”，原因是：

1. 当前仓库已经不再需要历史兼容表
2. 继续保留旧迁移会让运行时逻辑和文档都变复杂
3. 旧版本兼容路径会掩盖实际部署状态，增加排障成本

因此当前要求是：

- 代码里只保留当前基线 schema
- 运行时不再对旧版本做补列、补表或兼容升级

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

### 2. 更新当前基线 schema

把 `currentSchemaSQL` 更新为新的完整基线结构。

也就是说，当前仓库维护的是：

- 一份最新基线

而不是：

- 一串继续向后兼容的历史迁移链

### 3. 调整启动判断

如果你决定引入新版本 `v3`，要同步修改启动逻辑，让它接受：

- `schema_version = 3`

并拒绝：

- 其他旧版本

### 4. 补测试

至少补两类测试：

1. 全新安装时会创建当前基线 schema 并写入版本
2. 旧版本 schema 会被直接拒绝，而不是被兼容升级

当前相关测试在：

- [internal/adapters/outbound/vldg_dockdb/store_test.go](../internal/adapters/outbound/vldg_dockdb/store_test.go)

## 不建议的做法

### 不要重新引入历史兼容表

例如：

- 不要再把历史兼容记忆表加回当前运行时引导脚本

如果确实需要新表：

- 直接按当前业务模型设计新的正式表

### 不要恢复旧版本自动补形状逻辑

例如：

- 不要重新加入给 `vmm_version` 自动补字段的兼容代码

因为这会再次把运行时拉回“混合兼容状态”。

### 不要手动改库但不更新版本常量

如果你直接手动改网关数据库，而不更新 `currentSchemaVersion` 和当前基线 schema：

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

- DockDB：只接受当前基线 schema，并通过 `vmm_version` 标记版本
- LanceDB：启动时做幂等初始化，表已存在时视为成功

## 当前建议

如果你后面要继续扩展长期记忆结构，建议遵守：

1. DockDB 所有结构变化都通过 `schema_version` 管理
2. 当前仓库只维护最新基线，不维护历史兼容迁移链
3. 遇到旧版本库时直接显式失败，不静默兼容
4. 所有新增版本都补单测
5. 文档同步更新这份说明

这样后面无论是增加：

- 新的 session 字段
- 新的消息表
- 摘要表
- 画像表

都可以沿着同一套规则演进。
