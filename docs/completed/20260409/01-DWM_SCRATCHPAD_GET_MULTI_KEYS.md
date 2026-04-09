# 任务计划：扩展 DWM ScratchpadGet 支持多 key 批量读取

## 任务目标

将当前 DWM 的 `ScratchpadGet` 接口从“可选单个 `key`”扩展为“可选批量 `keys[]`”，并统一语义为：

- `keys` 为空数组或未传时：返回当前 scope 下全部 scratchpad item
- `keys` 非空时：按给定 key 集合返回多条匹配 item

同时保持现有 DWM 传输校验、服务端实现、测试和对外文档一致更新，避免继续保留单键读取的旧契约。

## 执行步骤

1. 梳理当前 `ScratchpadGet` 的 proto 契约、gRPC 校验、服务端实现、用例入参以及测试覆盖点。
2. 将 proto 契约中的 `optional key` 改为 `repeated keys`，明确“空数组表示读取全部”。
3. 更新 gRPC 传输层规范化与校验逻辑，确保：
   - `keys[]` 中每个 key 都会被裁剪
   - 批量 key 数量与长度受控
   - 空数组合法且表示全量读取
4. 更新服务端 `ScratchpadGet` 实现与相关用例调用适配，使其把多 key 请求正确下传。
5. 更新相关测试，覆盖：
   - 空 `keys[]` → 全量读取
   - 指定多个 key → 多条返回
   - 非法 key → 传输层拒绝
6. 同步更新 README / gRPC 文档 / DWM 文档中的接口描述。
7. 执行最小回归、全量测试与标准构建。
8. 在计划文件末尾追加执行变更总结并归档。

## 技术选型与实现约束

- 本轮扩展的是 DWM scratchpad 支线，不得把长期记忆主链的概念混入 DWM 契约。
- `ScratchpadGet` 需要与 `ScratchpadDelete` 的调用形态对齐，但语义仍应保留“空数组即全量读取”的读取特性。
- 传输层应尽量保持 deterministic 行为，避免多 key 输入在后续链路里被重新排序或静默丢弃。
- 若内部用例层当前只支持单 key，需要同步扩展，但不得破坏“空 key = 全量读取”的现有业务语义。

## 验收标准

1. `ScratchpadGetRequest` 已改为使用 `keys[]`，不再暴露旧的单 `key` 契约。
2. 空 `keys[]` 或未传时，仍返回当前 scope 下全部 scratchpad item。
3. 传入多个 key 时，可返回匹配的多条 item。
4. gRPC 校验、服务端实现、测试与文档全部同步通过。
5. 已完成：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `.\make.bat build`

## 执行变更总结

### 1. 核心修复与调整概述

- 已将 DWM 的 `ScratchpadGet` gRPC 契约从单 `key` 扩展为 `keys[]` 批量读取模式，并明确“空数组或未传等价于全量读取”。
- 已同步更新传输层规范化、请求校验、服务端转发和用例层查询结构，使多 key 读取行为与 `ScratchpadDelete` 的调用形态保持一致。
- 已重新生成 protobuf 代码，并修正生成命令导致的错误输出目录问题，确保生成文件落回仓库标准路径。
- 已补充批量读取、空 key 拒绝和全量读取相关测试，并完成最小回归、全量测试与标准构建验证。

### 2. 📂文件变更清单

#### 修改

- `README.md`
- `docs/api-test-guide_CN.md`
- `docs/dwm-working-memory-guide_CN.md`
- `docs/dwm-working-memory-guide_EN.md`
- `docs/grpc-integration-guide_CN.md`
- `internal/adapters/inbound/grpcapi/proto/v1/vmm.proto`
- `internal/adapters/inbound/grpcapi/proto/v1/vmm.pb.go`
- `internal/adapters/inbound/grpcapi/proto/v1/vmm_grpc.pb.go`
- `internal/adapters/inbound/grpcapi/server.go`
- `internal/adapters/inbound/grpcapi/validation.go`
- `internal/adapters/inbound/grpcapi/scratchpad_rpc_test.go`
- `internal/app/usecase/scratchpad.go`
- `internal/app/usecase/scratchpad_test.go`

#### 删除

- 无业务文件删除；已清理误生成的 `github.com/...` protobuf 输出目录。

### 3. 💻关键代码调整详情

- 在 `vmm.proto` 中把 `ScratchpadGetRequest.key` 替换为 `repeated string keys`，并更新接口注释，使协议层直接表达批量读取意图。
- 在 `validation.go` 中将 `ScratchpadGet` 的规范化与校验逻辑改为处理 `keys[]`：逐项裁剪、限制最多 64 个 key、逐项校验长度与空白值。
- 在 `server.go` 中调整 `ScratchpadGet` 入参映射，把 `req.GetKeys()` 直接下传到用例层，保留空数组代表全量读取的语义。
- 在 `scratchpad.go` 中把 `ScratchpadGetQuery` 改为 `Keys []string`，并复用既有 `normalizeScratchpadKeys` 逻辑，下传给存储层的 `ListScratchpadItems`。
- 在测试层新增批量 key 读取和非法 key 拒绝用例，并修正 fake store 的返回顺序，使其与真实存储“按 key 稳定排序”的契约一致。

### 4. ⚠️遗留问题与注意事项

- 该改动修改了 protobuf 契约，所有外部 gRPC 调用方后续都需要从单 `key` 切换为 `keys[]`。
- protobuf 重新生成时必须使用模块参数（`--go_opt=module=github.com/openvulcan/vmm` 与 `--go-grpc_opt=module=github.com/openvulcan/vmm`），否则会误生成到仓库根目录下的 `github.com/...` 路径。
- `ScratchpadGet` 当前返回顺序遵循存储层的“按 key 稳定排序”，不是按请求 `keys[]` 的传入顺序返回，调用方如需自定义展示顺序应自行重排。
