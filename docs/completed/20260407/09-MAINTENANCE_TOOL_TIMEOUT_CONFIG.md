# 任务目标

将 PostgreSQL 维护链路中新增的“维护读取超时”和“维护长事务超时”从代码常量提取为配置项，放入 `base.yaml` 内的独立维护工具配置节点中，不与常规 `postgres` 运行时配置节点混合，同时保持默认值、校验、加载与调用链路完整闭环。

# 执行步骤

1. 复核当前配置模型、默认值填充、环境变量覆盖和 PostgreSQL 适配器装配路径，确定新增维护工具配置节点的最优挂载位置。
2. 在配置层新增维护工具专用节点及其超时字段，并同步更新默认值、归一化与校验逻辑。
3. 将 PostgreSQL 组合库存储的维护读取/维护长事务上下文改为读取新配置，而非使用代码内硬编码常量。
4. 更新 `configs/base.yaml` 等示例配置，使默认配置文件直接体现新节点结构。
5. 增加或更新测试，验证默认值、配置注入与运行时超时派生逻辑均符合预期。

# 技术选型

- 采用独立顶层配置节点承载维护工具超时，避免把仅对维护链路生效的参数继续塞入 `postgres` 常规连接配置。
- 保持维护读取超时与维护长事务超时分离，分别服务于项目级 durable memory 导出与破坏性重建事务。
- 默认值仍沿用当前已验证的预算：维护读取 `30s`、维护长事务 `10m`，并通过配置允许运维按实例规模调整。
- 优先复用现有 `Duration`、配置归一化、环境变量映射与适配器装配路径，避免引入新一套配置解析机制。

# 验收标准

1. `base.yaml` 中出现独立的维护工具配置节点，且不与常规 `postgres` 节点混合。
2. PostgreSQL 维护读取与维护长事务上下文不再依赖代码常量，而是读取配置值。
3. 默认配置下行为与当前预期一致：维护读取默认 `30s`，维护长事务默认 `10m`。
4. 配置默认值、运行时装配和相关测试全部通过。

# 执行变更总结

## 1. 核心修复与调整概述

- 将 PostgreSQL 维护读取超时与维护长事务超时从代码常量提取为独立配置，新增 `maintenance_tool.postgres` 节点承载维护工具专属预算。
- 保持常规在线请求仍使用 `postgres.query_timeout`，避免把维护工具预算回灌到普通请求链路。
- 将 PostgreSQL 组合库存储和应用装配入口改为读取新配置节点，让向量重建、托管快照导入和项目记忆导出真正受独立维护配置控制。
- 补充配置默认值、环境变量覆盖和 PostgreSQL 维护超时派生测试，并完成全仓回归验证。

## 2. 📂文件变更清单

### 修改

- `configs/base.yaml`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/app/app.go`
- `internal/adapters/outbound/vldb_postgres/store.go`
- `internal/adapters/outbound/vldb_postgres/helpers.go`
- `internal/adapters/outbound/vldb_postgres/context_timeout_test.go`

### 新增

- 无

## 3. 💻关键代码调整详情

- 在根配置中新增 `MaintenanceToolConfig` 与 `MaintenanceToolPostgresConfig`，定义 `read_timeout` / `write_timeout` 两个维护工具专用字段。
- 在 `DefaultBase`、`Normalize`、`Validate` 和 `applyEnvOverrides` 中补齐默认值、校验与环境变量映射，支持 `VMM_MAINTENANCE_TOOL_POSTGRES_READ_TIMEOUT` 与 `VMM_MAINTENANCE_TOOL_POSTGRES_WRITE_TIMEOUT`。
- 在 `buildCombinedStore` 中把维护工具超时显式注入 PostgreSQL 组合库存储配置，确保运行时装配不会丢失该节点。
- 在 PostgreSQL 组合库存储中新增对应配置字段，并让 `maintenanceReadContext` / `maintenanceWriteContext` 改为直接读取维护工具超时；当调用方绕过配置层直接构造 store 时，仍回退到仓库默认值。
- 在测试层验证默认值、环境变量覆盖与维护上下文预算，确保新节点既可配置，也不会污染常规 `postgres.query_timeout`。

## 4. ⚠️遗留问题与注意事项

- 当前维护工具节点仅覆盖 PostgreSQL 维护读取与维护写事务超时，后续如果还要提取批量大小、并发度等维护参数，可以继续挂到该节点下。
- `configs/config.yaml` 目前未额外声明该节点，默认依赖 `base.yaml` 提供初值；如需项目级覆盖，可在本地配置层按相同结构覆写。
- 仓库中仍存在其他未提交改动；本次只围绕维护工具超时配置抽取做了局部调整，未处理无关差异。
