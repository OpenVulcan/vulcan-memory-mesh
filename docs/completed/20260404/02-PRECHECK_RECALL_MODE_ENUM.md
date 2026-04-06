# 任务计划：PreCheck recall_mode 改为可扩展 mode 枚举

## 1. 任务目标

将当前 `PreCheckRequest` 的 compact 控制位调整为可扩展的 `recall_mode` 枚举形式，避免未来新增更多召回策略时必须强制升级所有插件。

本次先落两个模式：

- `0`：原始方式，保持旧版 pre-check 召回逻辑，不读取 compact 边界
- `1`：session compact 模式，按当前 session 的 compact 边界过滤 turn-extract 记忆

并保证：

- 字段省略时默认等同于 `0`
- gRPC、服务端 usecase、测试、文档同步更新
- 当前已完成的 SQLite / LanceDB compact 边界能力不回退

## 2. 执行步骤

1. 调整 gRPC 契约
   - 在 `vmm.proto` 中引入 `recall_mode` 枚举字段
   - 为枚举预留未来扩展空间，只在当前版本开放 `0/1`
   - 重新生成 protobuf 代码

2. 调整服务端与 usecase 透传
   - 将 `grpc server -> usecase.PreCheckCommand` 的召回控制字段改为 mode
   - 把原先基于布尔值的判断逻辑重构为基于 mode 的分支
   - 未识别 mode 的处理策略保持安全回退，避免误放开召回边界

3. 调整测试
   - 更新原有 compact 边界相关单测
   - 覆盖默认模式、legacy 模式、compact 模式三类行为
   - 确保旧逻辑与新逻辑在 mode 切换下语义清晰

4. 调整文档
   - 更新 `README.md`
   - 更新 gRPC 集成文档与接口设计文档
   - 明确 `0` 为默认值、`1` 为 compact 模式，以及后续可扩展更多值

5. 验证
   - 运行 gRPC / usecase / 文本与配置链路的必要测试
   - 视改动范围执行 `go test ./... -count=1`

## 3. 技术选型

- gRPC 字段采用 `enum`，字段名使用 `recall_mode`
  - 原因：枚举在协议层天然更适合承载“当前已知模式 + 未来预留模式”的语义
  - 即便后续新增 `2/3/4`，旧客户端也不必为了字段类型变化而重写接口
- 未识别的非零 mode 回退到当前支持的最严格 compact-aware 基线
  - 原因：这样老服务在遇到未来新 mode 时，仍能避免把整个当前 session 的 turn-extract 记忆重新开放出来
- 服务端内部也使用显式 mode 分支，而不是把枚举再次压扁成布尔值
  - 原因：这样后续扩展时不会在 usecase 内部形成隐式兼容债务

## 4. 验收标准

- `PreCheckRequest` 使用 `recall_mode` 而不是布尔位
- 未传该字段时，服务端默认走 `0` 原始模式
- 传 `1` 时，服务端按 compact 边界执行向量检索与 BM25 检索过滤
- 相关测试全部通过
- 文档说明与当前真实接口一致

## 执行变更总结

### 1. 核心修复与调整概述

- 将 `PreCheckRequest` 的控制字段从布尔型 `ignore_compact_boundary` 正式升级为枚举型 `recall_mode`，并把 protobuf wire 层继续保持为同一字段号 `5` 的 varint 语义。
- 新增 `PreCheckRecallMode` / `PreCheckRecallModeLegacy` / `PreCheckRecallModeSessionCompact`，服务端默认模式为 `0`，显式 `1` 时启用 compact-aware 召回。
- `PreCheck` 用例内部同步改为基于 `RecallMode` 做分支，未知的未来非零 mode 会回退到当前支持的 compact-aware 基线，避免老服务端意外重新开放整个当前 session 的 turn-extract 记忆。
- gRPC 文档、接口设计文档和测试示例已全部同步改为 `recall_mode`，并补充默认值与兼容回退语义说明。

### 2. 📂 文件变更清单

- 修改：
  - `internal/adapters/inbound/grpcapi/proto/v1/vmm.proto`
  - `internal/adapters/inbound/grpcapi/proto/v1/vmm.pb.go`
  - `internal/adapters/inbound/grpcapi/proto/v1/vmm_grpc.pb.go`
  - `internal/adapters/inbound/grpcapi/server.go`
  - `internal/adapters/inbound/grpcapi/server_test.go`
  - `internal/app/usecase/precheck.go`
  - `internal/app/usecase/precheck_scope.go`
  - `internal/app/usecase/precheck_test.go`
  - `README.md`
  - `docs/grpc-integration-guide_CN.md`
  - `docs/hierarchy-grpc-design_CN.md`
  - `docs/api-test-guide_CN.md`
  - `docs/plan/20260404-02-PRECHECK_RECALL_MODE_ENUM.md`
- 新增：
  - 无
- 删除：
  - 无

### 3. 💻 关键代码调整详情

- 在 proto 层引入 `enum PreCheckRecallMode`，保留 `0=LEGACY`、`1=SESSION_COMPACT` 两个已开放模式，并将 `PreCheckRequest` 字段改为 `recall_mode = 5`。
- 在 `server.go` 中把 gRPC 请求的 `recall_mode` 透传为 `usecase.PreCheckCommand.RecallMode`，同时将请求日志字段改为 `recall_mode / recall_label`。
- 在 `precheck_scope.go` 中新增 `normalizePreCheckRecallMode`，把未识别的未来非零值折叠到 `PreCheckRecallModeSessionCompact`，实现“默认兼容旧调用、未来模式安全回退”的运行时策略。
- 在测试层补齐三类行为：
  - 省略 `recall_mode` 时默认 legacy
  - 显式 `SESSION_COMPACT` 时开启 compact-aware 召回
  - 未来未知 mode 时回退到 compact-aware 基线

### 4. ⚠️ 遗留问题与注意事项

- 当前只正式开放 `0` 和 `1` 两种 `recall_mode`；未来若新增 `2/3/4`，仍需补齐明确语义、文档与测试，但旧服务端至少会按 compact-aware 基线安全回退。
- 本次只调整了 `PreCheck` 对外契约与服务端语义，不影响 `ChatCompact`、SQLite migration runner 和 LanceDB schema 版本流程本身。
- 已完成验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./internal/app ./internal/app/usecase ./internal/adapters/inbound/grpcapi ./internal/adapters/outbound/vldb_sqlite ./internal/adapters/outbound/vldb_lancedb -count=1`
  - `go test ./... -count=1`
