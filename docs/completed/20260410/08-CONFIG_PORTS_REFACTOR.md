# 第六阶段执行计划：CONFIG_PORTS_REFACTOR

## 1. 任务目标

本阶段聚焦 `internal/config/config.go` 与 `internal/app/ports/interfaces.go` 的文件级职责过宽问题，在不改变现有配置加载行为、环境变量覆盖规则、端口接口签名和依赖方向的前提下，按职责拆分配置实现与端口定义文件，降低后续维护成本与耦合度。

## 2. 执行步骤

1. 盘点 `config.go` 当前承载的配置模型、默认值、加载逻辑、规则目录解析、环境变量覆盖、归一化与校验职责。
2. 梳理 `interfaces.go` 当前聚合的端口接口边界，识别 memory / profile / workspace / scratchpad / retention 等职责面。
3. 设计同包拆分方案，明确哪些内容继续保留在原文件，哪些迁移到新的同包文件中。
4. 按职责拆分 `internal/config` 下的配置实现文件，并保持现有对外函数与配置契约兼容。
5. 按领域边界拆分 `internal/app/ports` 下的接口定义文件，并避免引入反向依赖。
6. 为新增源码文件补齐双语文件头注释，并保持类型、函数、关键逻辑注释完整。
7. 运行最小回归测试与全量测试，确认行为一致。
8. 在计划末尾追加执行变更总结，并将计划归档到 `docs/completed/20260410/`。

## 3. 技术方案

- 采用“同包拆文件”方式重构，不新建额外 package，保证外部导入路径不变。
- `internal/config` 采用“配置模型 / 默认值与目录解析 / 加载与覆盖 / 归一化与校验”的拆分方向。
- `internal/app/ports` 采用“shared / workspace / memory / profile / scratchpad / retention”等按职责分文件的拆分方向。
- 优先搬迁实现而不改变函数签名和行为，避免把本阶段演变成配置语义调整阶段。

## 4. 验收标准

- `internal/config/config.go` 不再独自承载全部配置职责。
- `internal/app/ports/interfaces.go` 不再集中承载所有端口接口定义。
- 配置加载、规则目录解析、环境变量覆盖与现有测试行为保持兼容。
- 以下测试至少通过：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `go test ./...`

## 5. 风险与关注点

- 需要避免在拆分配置文件时破坏 `-config` 覆盖根目录语义，以及 prompt / pii_rules / noise_rules 的优先级规则。
- 需要避免在端口接口拆分时引入循环依赖或隐藏式重复定义。
- 需要确认现有测试或运行时代码是否依赖某些私有辅助函数仍位于原文件中。

## 6. 当前状态

- 状态：已完成
- 当前阶段：配置与端口拆分、测试验证与归档总结已完成

## 7. 执行变更总结

### 1. 核心修复与调整概述

- 将 `internal/config/config.go` 从单一超大文件拆分为“配置模型与默认值 / 分层加载 / AI 路由辅助 / 运行时归一化 / 校验与环境覆盖”五个同包文件，保留原有对外 API 和配置语义不变。
- 将 `internal/app/ports/interfaces.go` 收敛为共享横切端口入口，并按职责把 AI、memory、profile、scratchpad、retention、workspace 等接口定义迁移到独立文件。
- 保持配置加载顺序、`.env` 合并规则、`-config` 覆盖根目录语义、端口接口签名与依赖方向兼容，不引入新的 package 边界。

### 2. 📂文件变更清单

- 修改：`internal/config/config.go`
- 新增：`internal/config/config_load.go`
- 新增：`internal/config/config_ai_routing.go`
- 新增：`internal/config/config_runtime.go`
- 新增：`internal/config/config_validate.go`
- 修改：`internal/app/ports/interfaces.go`
- 新增：`internal/app/ports/ports_ai.go`
- 新增：`internal/app/ports/ports_memory.go`
- 新增：`internal/app/ports/ports_profile.go`
- 新增：`internal/app/ports/ports_retention.go`
- 新增：`internal/app/ports/ports_scratchpad.go`
- 新增：`internal/app/ports/ports_workspace.go`

### 3. 💻关键代码调整详情

- `internal/config` 中保留 `config.go` 承载配置结构体、默认值、类型定义和基础反序列化逻辑；将 `Load/LoadPaths`、YAML 转 JSON、占位符发现、`.env` 合并迁移到 `config_load.go`。
- 将 LLM / embedding / rerank 相关的路由归一化、路由权重解析、AI 节点校验与 provider 默认值迁移到 `config_ai_routing.go`，避免这些 AI 专属规则继续散落在配置总文件中。
- 将运行时字符串裁剪、默认值回填和枚举规范化迁移到 `config_runtime.go`；将 `Validate`、环境变量覆盖、provider 能力判定和受保护日志密钥校验迁移到 `config_validate.go`。
- `internal/app/ports` 中把记忆链路、画像链路、scratchpad、retention、workspace 等接口拆到按职责命名的独立文件，`interfaces.go` 只保留 `Shutdowner`、noise 相关别名、`RequestScopeResolver` 与 `IDGenerator` 等共享横切能力。
- 为所有新增源码文件补齐了双语文件头注释，并执行了 `gofmt`，保持现有中英文注释风格一致。

### 4. ⚠️遗留问题与注意事项

- 本阶段只做“同包拆文件”，没有改动任何配置键、默认值、环境变量名称和端口签名；后续若要继续瘦身，可再评估 `internal/config` 内部是否需要把 storage / retention / grpc 专属规则进一步局部聚合。
- `internal/app/ports` 目录里原有的 [llm.go](D:/projects/VulcanMemoryMesh/internal/app/ports/llm.go) 与 [prompt_source.go](D:/projects/VulcanMemoryMesh/internal/app/ports/prompt_source.go) 未改动，继续作为独立职责文件存在。
- 本阶段已完成以下验证：
  - `go test ./internal/config ./internal/app/ports`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `go test ./...`
