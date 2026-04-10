# 第二阶段执行计划：Memory UseCase 解耦拆分

## 1. 任务目标

本阶段聚焦 `internal/app/usecase/memory_query.go` 的职责解耦，在不改变现有对外行为、端口契约和测试预期的前提下，将统一记忆用例按搜索、详情、主动写入、排序辅助等职责拆分为同包内多个实现文件，降低单文件复杂度与后续演进耦合度。

## 2. 执行步骤

1. 盘点 `memory_query.go` 中的类型定义、构造函数、配置入口、公共方法与内部辅助函数边界。
2. 以“行为不变、接口不变、包路径不变”为约束，设计同包拆分方案，明确各文件职责。
3. 拆分搜索主流程及其检索、融合、重排、衰减、证据打分相关实现。
4. 拆分详情查询与 turn 详情装配相关实现。
5. 拆分主动写入、软幂等、语义替代、向量持久化相关实现。
6. 补齐新增源码文件的文件头双语注释，确保符合仓库注释规范。
7. 运行目标测试与全量测试，确认拆分后行为一致。
8. 在计划末尾追加执行变更总结，并将计划归档到 `docs/completed/20260410/`。

## 3. 技术方案

- 采用“同包拆文件”的方式重构，不引入新的包层级，避免额外依赖方向变化。
- 保留 `MemoryUseCase`、`MemoryExecutor`、命令/结果结构体和现有公共方法签名，确保 gRPC 入站层与测试代码无需适配。
- 以职责域划分文件：
  - 搜索入口与检索编排
  - 详情查询与 turn 明细装配
  - 主动写入与 direct-write 语义去重
  - 排序、融合、衰减与通用辅助逻辑
- 所有拆分以最小行为扰动为原则，优先迁移代码，不同时修改业务语义。

## 4. 验收标准

- `internal/app/usecase/memory_query.go` 不再承担搜索、详情、写入和大部分辅助逻辑的全部实现职责。
- 新增文件具备清晰职责分工，并保持同包内调用关系稳定。
- 对外暴露的接口、结构体、方法签名保持兼容。
- 以下测试至少通过：
  - `go test ./internal/app/usecase`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `go test ./...`

## 5. 风险与关注点

- 需要避免在拆分过程中误改私有辅助函数的可见性和调用顺序。
- 需要确认现有单元测试是否依赖同包私有函数的文件内顺序或初始化行为。
- 需要保证 direct-write 相关去重和回滚逻辑在拆分后仍保持完整闭环。

## 6. 当前状态

- 状态：已完成
- 当前阶段：代码拆分、验证与总结已完成，待归档

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已完成 `internal/app/usecase/memory_query.go` 的职责拆分，将统一记忆用例按“核心契约与装配入口 / 搜索编排 / 详情查询 / 主动写入”拆分为多个同包文件。
- 本次重构保持 `MemoryUseCase`、`MemoryExecutor`、命令与结果结构体、公共方法签名不变，确保入站 gRPC 适配层与现有测试无需适配。
- 已为新增源码文件补齐双语文件头注释，并修正拆分边界上的函数注释归属，保持仓库注释规范一致。

### 2. 📂 文件变更清单

#### 新增

- `internal/app/usecase/memory_query_search.go`
- `internal/app/usecase/memory_query_detail.go`
- `internal/app/usecase/memory_query_write.go`

#### 修改

- `internal/app/usecase/memory_query.go`

#### 删除

- 无

### 3. 💻 关键代码调整详情

- 将 `Search` 入口、一阶段召回、混合检索、RRF 融合、MMR、多维打分、Weibull 衰减、日志与命中映射辅助函数迁移至 `memory_query_search.go`。
- 将 `GetTurns`、`GetDetails`、turn 详情装配、mixed ref 归一化、dehydrated 内容解析等逻辑迁移至 `memory_query_detail.go`。
- 将 `Write`、direct-write 软幂等、语义替代决策、向量写入、回滚补偿、direct-memory TTL 与 hash 辅助逻辑迁移至 `memory_query_write.go`。
- 将 `memory_query.go` 收敛为统一记忆用例的核心契约文件，仅保留常量、结构体、接口、`MemoryUseCase` 定义、构造函数与运行时配置方法，降低主文件耦合度。
- 完成代码格式化，并通过以下验证：
  - `go test ./internal/app/usecase`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `go test ./...`

### 4. ⚠️ 遗留问题与注意事项

- 当前仍保持“同包拆分”而非进一步拆成独立包，这是为了避免在第二阶段引入依赖方向变化；后续若进入第三阶段或更深层边界治理，可再考虑按子域继续抽象。
- `memory_query.go` 虽已明显收敛，但统一记忆用例的类型定义仍集中在该文件；如果后续需要继续降低入口文件体积，可以再按命令/结果模型拆出只读契约文件。
