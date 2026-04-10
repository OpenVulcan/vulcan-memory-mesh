# 第五阶段执行计划：gRPC 入站处理层拆分

## 1. 任务目标

本阶段聚焦 `internal/adapters/inbound/grpcapi/server.go` 当前单文件集中承载全部 RPC surface 的问题，在不改变 gRPC 服务注册方式、proto 契约、对外方法签名和现有测试结果的前提下，按业务面拆分 handler 实现，降低入站层的文件级耦合与后续维护成本。

## 2. 执行步骤

1. 盘点 `server.go` 当前包含的 Server 结构、构造入口、依赖注入和 RPC 方法分布。
2. 梳理验证逻辑与各 RPC 业务面的耦合关系，确认测试是否依赖同包私有辅助函数。
3. 设计同包拆分方案，明确哪些内容保留在 `server.go`，哪些迁移到独立 handler 文件。
4. 按业务面拆分 memory / profile / workspace / scratchpad / post-action 等 RPC 方法。
5. 保持公共 Server 结构和 gRPC 注册入口不变，修正必要的同包辅助函数归属。
6. 补齐新增源码文件的双语文件头注释，并修正函数注释归属。
7. 运行关键测试与全量测试，确认行为一致。
8. 在计划末尾追加执行变更总结，并将计划归档到 `docs/completed/20260410/`。

## 3. 技术方案

- 采用“同包拆文件”的方式重构，不新增新的 Go package，避免影响 gRPC 注册和已有引用。
- 保留 `Server` 结构、`NewServer` 构造函数以及 gRPC 注册方式，确保运行时装配无需改动。
- 以业务面划分 handler 文件：
  - 基础 server 结构与构造
  - workspace / profile 相关 RPC
  - memory 相关 RPC
  - scratchpad 相关 RPC
  - post-action / profile instruction 等流程型 RPC
- 以“先搬迁实现，后统一格式化和验证”的方式控制风险，不在本阶段混入额外业务语义调整。

## 4. 验收标准

- `internal/adapters/inbound/grpcapi/server.go` 不再集中承载全部 RPC 处理实现。
- 新增 handler 文件职责清晰，且同包内调用关系稳定。
- `Server` 对外方法签名、proto 契约、运行时装配方式保持兼容。
- 以下测试至少通过：
  - `go test ./internal/adapters/inbound/grpcapi`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `go test ./...`

## 5. 风险与关注点

- 需要避免在拆分过程中破坏 gRPC error mapping、请求校验和 trace / logger 相关共用逻辑。
- 需要确认一些跨业务面的辅助函数是否被多个 RPC 共用，避免机械搬迁后形成新的隐式耦合。
- 需要保持 `validation.go` 与各 handler 文件的分工清晰，避免重复校验或漏校验。

## 6. 当前状态

- 状态：已完成
- 当前阶段：handler 拆分、测试验证与归档总结已完成

## 7. 执行变更总结

### 1. 核心修复与调整概述

- 将 `internal/adapters/inbound/grpcapi/server.go` 收敛为“Server 结构、构造入口、拦截器装配与共享转换辅助函数”的薄入口文件。
- 按业务面拆分 gRPC RPC 实现，分别新增 admin、memory、scratchpad、business 四个 handler 文件，降低单文件职责密度。
- 保持 `Server` 对外方法签名、proto 契约、gRPC 注册方式、trace 传递、请求校验顺序和错误映射行为不变。

### 2. 📂文件变更清单

- 新增：`internal/adapters/inbound/grpcapi/server_admin.go`
- 新增：`internal/adapters/inbound/grpcapi/server_memory.go`
- 新增：`internal/adapters/inbound/grpcapi/server_scratchpad.go`
- 新增：`internal/adapters/inbound/grpcapi/server_business.go`
- 修改：`internal/adapters/inbound/grpcapi/server.go`

### 3. 💻关键代码调整详情

- 将 workspace / user / profile 相关 RPC 从 `server.go` 迁移到 `server_admin.go`，并保留共用的 `toProjectEntry`、`toUserEntry`、画像枚举转换等公共辅助函数在主入口文件中。
- 将 memory 检索、turn 详情查询与主动写入 RPC 迁移到 `server_memory.go`，保持对 `MemoryExecutor` 的调用路径和 AI 写入参数映射逻辑不变。
- 将 scratchpad 的 upsert / delete / get / list / clean RPC 迁移到 `server_scratchpad.go`，继续复用同包 scratchpad 传输转换函数。
- 将 `ChatCompact`、`PreCheck`、`PostAction` 及其日志与清洗辅助函数迁移到 `server_business.go`，使流程型 RPC 与 transport 侧 payload 日志逻辑归于同一职责面。
- 对拆分后的 grpcapi 文件执行了 `gofmt`，并验证新文件内的双语文件头注释、函数注释与关键逻辑注释完整保留。

### 4. ⚠️遗留问题与注意事项

- 当前仍保留同包级共享辅助函数模式，后续如果继续进入第六阶段，可再评估 `validation.go`、枚举转换与 transport helper 是否需要进一步分文件瘦身。
- 本阶段只做 handler 职责拆分，不调整 proto 契约、不改动 gRPC 注册入口，也不引入新的 package 边界。
- 本阶段已完成以下验证：
  - `go test ./internal/adapters/inbound/grpcapi`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `go test ./...`
