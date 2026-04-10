# 任务目标

在不改变当前对外 `VMMService` 一元 RPC 协议的前提下，为项目补充更稳健的 gRPC 连接治理能力，降低长时间连接复用场景下因空闲连接回收、代理层超时或连接老化带来的不稳定性。同时补齐配置、测试与文档说明，确保现有客户端无需修改即可继续使用，新增能力以可选配置的方式增强稳定性。

# 详细执行步骤

1. 梳理当前 gRPC 服务装配路径、配置模型与文档现状，确认缺失的连接治理能力和可插入点。
2. 为 gRPC 配置新增连接治理相关参数，优先覆盖服务端 keepalive 参数与连接生命周期控制，保持默认值安全且向后兼容。
3. 将新增配置接入 gRPC Server 构造流程，确保仅增强连接稳定性，不改变现有业务 RPC 语义与错误模型。
4. 增加或更新测试，验证默认值、配置注入与服务装配行为，避免回归当前请求超时和消息大小限制逻辑。
5. 同步更新 gRPC 对接文档，明确说明：
   - 当前仍然是普通一元 RPC；
   - 现有客户端不修改也可继续使用；
   - 若客户端复用连接并按需配置 keepalive，可获得更高稳定性；
   - 服务端新增的连接治理配置如何生效。
6. 完成代码与文档后逐项对照计划自检，并在文件末尾追加执行变更总结。

# 技术选型

- 延续当前 `google.golang.org/grpc` 服务端实现，不引入新的传输框架或额外代理依赖。
- 使用 gRPC 官方 `keepalive.ServerParameters` 与 `keepalive.EnforcementPolicy` 作为服务端连接治理手段。
- 采用“默认兼容、显式增强”的配置策略：
  - 默认值保持宽松，避免影响现有客户端；
  - 通过配置文件或环境变量可按需调优；
  - 不新增任何流式 RPC，不改动现有 proto。
- 文档继续以中文指南为主，并与现有 `configs/` 示例配置保持一致。

# 验收标准

- `VMMService` 对外 proto 无流式变更，现有 unary RPC 契约保持不变。
- gRPC 服务端支持通过配置启用连接治理参数，并成功接入 `grpc.NewServer(...)`。
- 默认配置下现有客户端行为保持兼容，不要求必须修改才能访问服务。
- 至少补齐配置与服务装配层测试；若修改范围触及公共配置和 gRPC 装配，还需运行相关 Go 测试。
- `docs/grpc-integration-guide_CN.md` 与相关配置示例完成同步，能够明确指导调用方理解“无需改协议、可选增强稳定性”的使用方式。

# 执行变更总结

## 1. 核心修复与调整概述

- 为运行时 `grpc` 配置新增 `keepalive` 配置块，补充服务端 keepalive ping、连接寿命治理和客户端 ping 策略控制能力。
- 在 `internal/app` 的 gRPC Server 装配路径中接入 keepalive 参数与 enforcement policy，保持 `VMMService` 继续使用原有一元 RPC 契约。
- 同步补充配置层、装配层测试，并更新中文对接文档和默认配置示例，明确“现有客户端无需修改也可继续使用，复用连接可进一步提升稳定性”。

## 2. 📂文件变更清单

- 修改：`internal/config/config.go`
- 修改：`internal/config/config_test.go`
- 修改：`internal/app/app.go`
- 修改：`internal/app/app_test.go`
- 修改：`configs/base.yaml`
- 修改：`docs/grpc-integration-guide_CN.md`
- 新增：无
- 删除：无

## 3. 💻关键代码调整详情

- 在 `GRPCConfig` 下新增 `GRPCKeepaliveConfig`，字段包括 `enabled`、`time`、`timeout`、`max_connection_idle`、`max_connection_age`、`max_connection_age_grace`、`min_ping_interval`、`permit_without_stream`。
- 在 `DefaultBase()` 与 `Normalize()` 中补齐 keepalive 默认值与负数钳制逻辑，默认开启服务端 keepalive ping，但默认不主动限制连接空闲寿命与总寿命。
- 在 `Validate()` 中增加 keepalive 启用场景下的必要时序校验，并为新字段增加环境变量覆盖入口。
- 在 `internal/app/app.go` 中新增 keepalive 配置转换与 gRPC ServerOption 组装逻辑，把 `grpc.KeepaliveParams(...)` 与 `grpc.KeepaliveEnforcementPolicy(...)` 接入运行时。
- 新增测试覆盖 keepalive 默认值恢复、环境变量注入，以及装配层 keepalive 参数映射行为。
- 更新 `configs/base.yaml` 和 `docs/grpc-integration-guide_CN.md`，补充 keepalive 配置说明、客户端连接复用建议及可用环境变量列表。

## 4. ⚠️遗留问题与注意事项

- 本次仅增强服务端连接治理能力，没有在仓库内新增独立的 `VMMService` 客户端 SDK；外部调用方若要进一步提升稳定性，仍建议复用单个 `grpc.ClientConn`。
- 默认 keepalive 策略偏兼容与保守：会定期发 ping，但不会默认强制连接轮换；若部署在有特殊代理或负载均衡器的环境中，可按实际空闲超时继续调优 `grpc.keepalive.*`。
- 已完成验证：
  - `go test ./internal/app ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `go test ./...`
