# 任务目标

执行组件解耦计划的第一阶段，重构 `internal/app/app.go` 的运行时装配结构，将 AI 构建、存储构建、pipeline 组装、gRPC transport 构建与 shutdown 辅助逻辑从单一大型组合根中拆分出来，收敛 `app.go` 为薄编排入口，同时保持既有运行时行为不变。

# 执行步骤

1. 分析 `internal/app/app.go` 当前承担的职责块与测试依赖。
2. 设计并新增同包拆分文件，承载以下能力：
   - AI 适配器构建
   - 存储构建与 capability 解析
   - pipeline/usecase 装配
   - transport/gRPC server 构建
   - shutdown 去重辅助
3. 调整 `newApplication`，让其主要负责高层初始化与组装编排。
4. 保持对外接口、现有测试入口和运行时行为不变。
5. 运行最小回归测试，验证本轮拆分未引入行为回归。

# 技术选型与实现原则

- 采用“同包多文件拆分”，暂不引入新的跨包依赖，优先降低风险。
- 不修改 gRPC 协议、不修改对外导出 API、不改运行时配置契约。
- 新增源码文件必须遵守双语注释和文件头注释规范。
- 优先抽离纯 builder / assembler / helper，避免在本轮顺手改动业务逻辑。

# 验收标准

1. `internal/app/app.go` 职责明显收敛，主要负责高层编排。
2. AI、存储、pipeline、transport、shutdown 至少完成文件级职责拆分。
3. 不破坏现有 `NewLocal`、维护工具依赖、vector schema sync 与 shutdown 行为。
4. 通过最小回归测试：
   - `go test ./internal/app ./internal/config`

# 当前状态

- 已完成第一阶段运行时装配重构，并通过回归测试验证。

# 执行变更总结

## 1. 核心修复与调整概述

- 本次完成了组件解耦计划的第一阶段落地，把 `internal/app/app.go` 从“大型运行时组合根”收敛为薄型编排入口。
- 运行时装配逻辑已按职责拆分为 5 个同包文件：
  - `runtime_ai.go`
  - `runtime_storage.go`
  - `runtime_pipeline.go`
  - `runtime_transport.go`
  - `runtime_shutdown.go`
- 保持了既有 `NewLocal`、维护入口、gRPC 注册、shutdown 流程和 vector schema 同步行为不变。

## 2. 📂文件变更清单

### 新增

- `internal/app/runtime_ai.go`
- `internal/app/runtime_storage.go`
- `internal/app/runtime_pipeline.go`
- `internal/app/runtime_transport.go`
- `internal/app/runtime_shutdown.go`
- `docs/plan/20260410-03-RUNTIME_COMPOSITION_REFACTOR.md`

### 修改

- `internal/app/app.go`
- 当前计划文件本身，补充了执行总结。

### 删除

- 无。

## 3. 💻关键代码调整详情

### 运行时装配层拆分

- `app.go` 现在主要保留：
  - `Application` 生命周期定义
  - `NewLocal`
  - `newApplication`
  - `Run`
  - `Shutdown`
- AI 相关 builder 与路由适配提取到了 `runtime_ai.go`。
- 存储构建、capability 解析和 schema 同步前置收敛到了 `runtime_storage.go`。
- usecase/pipeline 装配与 PII/Noise 构建提取到了 `runtime_pipeline.go`。
- gRPC keepalive、server option 和 server 注册提取到了 `runtime_transport.go`。
- shutdown 去重与 identity 计算提取到了 `runtime_shutdown.go`。

### 结构收益

- `internal/app/app.go` 的职责从“细节汇聚”转为“高层编排”。
- 存储能力断言不再直接堆叠在 `newApplication` 中。
- usecase 装配与 transport 装配形成了清晰边界，为下一阶段继续拆 `memory_query` 和 `postaction` 提供了更稳定的入口。

### 验证结果

- 已执行：
  - `go test ./internal/app`
  - `go test ./internal/config`
  - `go test ./...`

## 4. ⚠️遗留问题与注意事项

- 本轮拆分仍然是“同包多文件拆分”，尚未进一步细化为子包级组件；这是有意控制风险的第一步。
- `runtime_pipeline.go` 目前仍持有较多 usecase 装配细节，后续若进入下一阶段，可继续拆成 memory/postaction/workspace 等更细装配单元。
- `runtime_storage.go` 已完成 capability 收口，但 `vldb_postgres` 仍是单一大 Store；这属于后续阶段 4 的工作范围。
