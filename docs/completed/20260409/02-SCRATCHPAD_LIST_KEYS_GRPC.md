# 任务计划：新增 ScratchpadListKeys gRPC 接口

## 任务目标

为当前 DWM scratchpad 链路新增一个只读 gRPC 接口 `ScratchpadListKeys`，在传入当前既有的必备 scope 参数后，直接返回：

- 当前 scope 对应的 `plan_name`
- 当前 scope 下全部 scratchpad key 列表

该接口需要保持与现有 scratchpad 契约一致的无副作用语义，不得创建新计划、不得修改主 session 主链，也不能破坏现有 `ScratchpadGet / Upsert / Delete / Clean` 行为。

## 执行步骤

1. 梳理当前 scratchpad 的 proto、gRPC server、validation、usecase、store 与测试覆盖结构。
2. 设计 `ScratchpadListKeys` 的 proto 请求/响应契约，保持与现有 scratchpad scope 参数和 no-data 语义一致。
3. 在用例层补充只读查询结构与执行逻辑，确保：
   - 当前 scope 不存在 plan 时返回成功且 keys 为空
   - 当前 scope 存在 plan 时返回 plan_name 与全部 key 列表
   - 返回结果保持稳定排序，便于 AI 与调用方消费
4. 在存储端口与具体存储实现中补充列 key 的只读能力。
5. 更新 gRPC 传输层规范化、校验和 server 映射。
6. 补充 RPC 层与用例层测试，覆盖：
   - 有计划且存在多条 key
   - 当前 scope 无记录
   - 非法 scope 请求被拦截
7. 同步更新 README 与相关 DWM / gRPC 文档。
8. 运行要求的回归测试，完成自检后追加执行变更总结并归档计划文件。

## 技术选型与实现约束

- `ScratchpadListKeys` 只返回 key 列表与 plan metadata，不返回 value，避免与 `ScratchpadGet` 语义重叠。
- 接口应复用现有 scratchpad 的 scope 约束：`session_id / user_id / project_id` 必填。
- 结果排序应沿用现有 scratchpad 读取的稳定排序策略，避免调用方收到非确定性顺序。
- 空结果语义应与现有 `ScratchpadGet` 保持一致：属于成功无数据，而不是错误。
- 新增代码必须补齐仓库要求的中英文双语注释，并尽量沿用现有 scratchpad 的命名风格与分层边界。

## 验收标准

1. 新增 `ScratchpadListKeys` proto 契约与 gRPC server 实现。
2. 调用方只传 scope 参数即可获取 `plan_name` 与全部 key 列表。
3. 无 plan 或无 key 时返回成功响应与空数组，而不是错误。
4. 用例层、存储层、RPC 层测试已覆盖关键场景并通过。
5. README 与相关中文/英文文档已同步更新。
6. 至少完成：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`

## 执行变更总结

### 1. 核心修复与调整概述

- 已新增只读 gRPC 接口 `ScratchpadListKeys`，支持调用方仅传 `session_id / user_id / project_id` 即可直接获取当前 canonical `plan_name` 与完整有序 `keys[]`。
- 已在领域层、用例层、传输层和 SQLite / Postgres 存储实现中补齐对应查询能力，并保证该接口不会拉取 value、不会创建 plan、不会触碰主 session 链路。
- 已同步更新 protobuf 生成代码、RPC 测试、用例测试以及 README / DWM / gRPC 接口文档，保持外部契约一致。

### 2. 文件变更清单

#### 修改

- `README.md`
- `docs/api-test-guide_CN.md`
- `docs/dwm-working-memory-guide_CN.md`
- `docs/dwm-working-memory-guide_EN.md`
- `docs/grpc-integration-guide_CN.md`
- `docs/hierarchy-grpc-design_CN.md`
- `internal/adapters/inbound/grpcapi/proto/v1/vmm.proto`
- `internal/adapters/inbound/grpcapi/proto/v1/vmm.pb.go`
- `internal/adapters/inbound/grpcapi/proto/v1/vmm_grpc.pb.go`
- `internal/adapters/inbound/grpcapi/scratchpad_rpc_test.go`
- `internal/adapters/inbound/grpcapi/server.go`
- `internal/adapters/inbound/grpcapi/validation.go`
- `internal/adapters/outbound/vldb_postgres/scratchpad.go`
- `internal/adapters/outbound/vldb_sqlite/scratchpad.go`
- `internal/app/ports/interfaces.go`
- `internal/app/usecase/scratchpad.go`
- `internal/app/usecase/scratchpad_test.go`
- `internal/logic/domain/scratchpad.go`

#### 新增

- `docs/plan/20260409-02-SCRATCHPAD_LIST_KEYS_GRPC.md`

### 3. 关键代码调整详情

- 在 `vmm.proto` 中新增 `ScratchpadListKeys` RPC，以及 `ScratchpadListKeysRequest / ScratchpadListKeysResponse` 消息，响应中返回 `plan_name / keys / key_count / updated_timestamp`。
- 在 `internal/logic/domain` 中新增 `ScratchpadKeyListResult`，在 `internal/app/usecase` 中新增 `ScratchpadListKeysQuery` 与 `ListKeys` 方法，复用现有 scope/no-data 语义并保持稳定排序。
- 在 `internal/app/ports` 与两个持久化适配器中新增 `ListScratchpadKeys`，数据库层只查询 `item_key`，避免把 value 一并加载出来。
- 在 `server.go` 与 `validation.go` 中补齐 list-keys 的规范化、校验和 RPC 映射。
- 在 `scratchpad_rpc_test.go` 与 `scratchpad_test.go` 中新增 plan name / keys 返回、空范围成功、非法 scope 拦截等覆盖。

### 4. 遗留问题与注意事项

- 本次改动属于新增 protobuf 契约，外部 gRPC 调用方需要在升级后重新生成或更新客户端桩代码。
- 当前仓库里已有一批与 `ScratchpadGet` 多 key 相关的已暂存改动，本次 `ScratchpadListKeys` 改动是叠加在这些未提交变更之上的。
- 由于当前工作树同时存在已暂存和未暂存修改，如果后续需要拆分提交，建议按“`ScratchpadGet` 多 key”和“`ScratchpadListKeys` 新接口”两个主题重新核对一次提交边界。
