# 任务目标

修复 PostgreSQL 维护链路中“长事务”和“长读取”仍沿用过短请求超时的问题，确保向量维度重建、维护导出以及基于 durable 事实源的重建读取在大数据量场景下具备更稳健的超时预算，同时不放大普通在线请求的查询超时。

# 执行步骤

1. 复核当前 PostgreSQL 组合库存储中 `bootstrapContext`、`queryContext` 与维护链路的实际绑定关系，区分启动、普通请求、维护长事务、维护长读取四类语义。
2. 设计并实现“维护长事务超时地板”与“维护读取超时地板”两个独立上下文派生方法，避免继续复用在线请求超时。
3. 将向量维度重建等长事务切换到维护长事务上下文，并将 `ListProjectMemories` 这类维护/管理重建事实源切换到维护读取上下文。
4. 视一致性需要，同步评估并修复同类的一次性迁移事务上下文，避免维护工具内部语义分裂。
5. 增加定向测试，验证新上下文地板与调用点行为符合预期。

# 技术选型

- 保留 `queryContext` 作为在线/常规数据库调用的请求级超时入口。
- 保留 `bootstrapContext` 作为启动期 schema / 索引初始化入口。
- 新增 PostgreSQL 维护专用上下文：
  - 维护读取：为大批量 durable 导出和向量重建事实源提供更宽松但有限的读取超时。
  - 维护长事务：为向量维度重建、全量导入等长事务提供显著高于启动期的超时预算。
- 采用 Go 单元测试直接校验上下文截止时间地板，避免引入真实慢查询。

# 验收标准

1. 向量维度重建不再复用 `30s` 的启动期超时地板。
2. `ListProjectMemories` 不再复用默认 `5s` 的在线查询超时。
3. 普通在线请求仍继续使用原有 `queryContext`，不被本次维护优化放大。
4. 定向测试通过，并能清楚说明第 2 条与第 3 条采用不同超时地板的原因。

# 执行变更总结

## 1. 核心修复与调整概述

- 为 PostgreSQL 组合库存储新增“维护读取超时地板”和“维护长事务超时地板”，把维护链路与在线请求、启动链路的上下文预算彻底拆开。
- 将向量维度重建和快照导入切换到维护长事务上下文，避免继续继承 `30s` 的启动期事务预算。
- 将 `ListProjectMemories` 切换到维护读取上下文，使项目级 durable memory 导出不再受默认 `5s` 在线查询超时限制。
- 增加上下文超时地板测试，并完成 PostgreSQL 定向测试、迁移命令测试及全仓回归测试。

## 2. 📂文件变更清单

### 修改

- `internal/adapters/outbound/vldb_postgres/helpers.go`
- `internal/adapters/outbound/vldb_postgres/memory_store.go`
- `internal/adapters/outbound/vldb_postgres/vector_dimension_migration.go`
- `internal/adapters/outbound/vldb_postgres/debug_migrate.go`

### 新增

- `internal/adapters/outbound/vldb_postgres/context_timeout_test.go`

## 3. 💻关键代码调整详情

- 在 `Store` 上新增 `maintenanceReadContext`，以 `30s` 作为维护读取最小超时预算；当配置的 `QueryTimeout` 更大时，仍保留更大的值。
- 在 `Store` 上新增 `maintenanceWriteContext`，以 `10min` 作为维护长事务最小超时预算，专门覆盖向量维度重建、快照导入等重型维护事务。
- 抽取 `queryMemoryNodesWithContextBuilder`，让记忆查询调用点可以按语义选择上下文策略；`ListProjectMemories` 使用维护读取策略，其余常规路径仍继续走 `queryContext`。
- 将 `RunVectorDimensionMigration` 与 `importManagedSnapshot` 的事务上下文切换到维护长事务策略，避免大数据量场景下在 DDL、向量回填与索引重建过程中被过早取消。
- 通过上下文截止时间测试验证两类地板值行为，确保“只放宽维护链路，不放宽在线请求链路”的设计真实生效。

## 4. ⚠️遗留问题与注意事项

- 当前维护读取地板取 `30s`，维护长事务地板取 `10min`，后续若真实生产数据规模继续扩大，可以再结合运维观测把两者参数外置化。
- `ReplaceMemoryVectors` 等常规写路径当前仍使用 `queryContext`；这是为了避免把普通业务写入一并放宽，本次未改变其语义。
- 仓库中存在其他未提交改动，本次只围绕 PostgreSQL 维护超时链路进行了局部调整，未触碰无关文件。
