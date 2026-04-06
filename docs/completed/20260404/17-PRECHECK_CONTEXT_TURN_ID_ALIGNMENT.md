# 任务目标

修复 precheck 最终反馈中 gRPC 返回载荷的字段冗余问题，确保最终返回给上游 AI 的 `context_items[]` 只保留最小可用字段，并以 `turn_id` 为主提供可追查引用，而不是依赖固定标签或已废弃的 `context_text`。

# 详细执行步骤

1. 梳理 precheck 最终反馈链路，确认 `context_items`、`context_text`、gRPC `PreCheckResponse` 的组装与映射位置。
2. 明确当前返回中 `memory_id` 的暴露来源，以及哪些地方已经持有 `source_turn_id / turn_id` 但尚未透传给上游。
3. 调整统一上下文项结构：
   - 为 `context_items` 增加 `turn_id` 字段；
   - 记忆类上下文优先透传 `turn_id`；
   - 保持中间层原始字段不变，避免影响内部组装和测试语义。
4. 调整 gRPC 最终返回契约：
   - `context_text` 继续保留兼容字段，但最终返回固定置空；
   - `context_items[]` 只暴露正文、分数、是否存在关联对话以及 `turn_id`；
   - 固定的 `kind / title / source` 不再向最终调用方透传。
5. 更新 gRPC proto 映射、日志载荷与相关测试，确保调用方在结构化 `context_items` 中可以直接读取 `turn_id`。
6. 如有必要，补充文档说明 precheck 返回项中的最小字段规则，并在文末写入执行变更总结后归档计划。

# 技术选型

- 以现有 `logicdomain.ContextItem` 作为统一上下文项入口，但只在最终 gRPC 适配层做字段瘦身，避免影响中间层装配。
- 优先通过 `source_turn_id / turn_id` 透传可追溯引用，避免把长期 `memory_id` 暴露给上游提示词。
- 继续保持兼容：对没有来源 turn 的记忆返回 `turn_id = 0`，并通过布尔字段明确是否存在可追查对话。

# 验收标准

- gRPC 最终返回的 `context_items` 中，记忆类条目包含对应 `turn_id` 字段与是否存在关联对话标识。
- gRPC 最终返回的 `context_text` 固定为空，不再作为调用方主消费字段。
- 相关测试覆盖结构化返回、gRPC 映射与日志载荷，确保中间层与最终返回层边界清晰。
- 计划文件补齐执行变更总结并完成归档。

## 执行变更总结

### 1. 核心修复与调整概述

- 已将 precheck 最终 gRPC 返回调整为“最小可用载荷”模式：`context_text` 固定置空，不再作为客户端主消费字段。
- 已为 `context_items[]` 增加 `turn_id` 与 `has_dialogue`，让上游可以直接判断是否存在可追查对话，并在有来源 turn 时继续调用 `GetTurnDetails`。
- 已明确边界：中间层仍保留 `Kind / Title / Source` 等装配信息，只有最终 gRPC 返回与对应日志载荷会裁掉这些固定字段。

### 2. 📂 文件变更清单

- 新增：`internal/logic/domain/context_item.go`
- 修改：`internal/logic/domain/common.go`
- 修改：`internal/logic/processor/context_assembler.go`
- 修改：`internal/logic/processor/context_assembler_test.go`
- 修改：`internal/app/usecase/precheck.go`
- 修改：`internal/app/usecase/precheck_test.go`
- 修改：`internal/adapters/inbound/grpcapi/server.go`
- 修改：`internal/adapters/inbound/grpcapi/server_test.go`
- 修改：`internal/adapters/inbound/grpcapi/proto/v1/vmm.proto`
- 修改：`internal/adapters/inbound/grpcapi/proto/v1/vmm.pb.go`
- 修改：`README.md`
- 修改：`docs/hierarchy-grpc-design_CN.md`
- 修改：`docs/grpc-integration-guide_CN.md`

### 3. 💻 关键代码调整详情

- `logicdomain.ContextItem` 新增 `TurnID`，并通过 `MemoryHitTurnID` 统一从 `MemoryHit.metadata.turn_id` 解析来源 turn。
- precheck 在把采纳候选转换回 `MemoryHit` 时，会把 `source_turn_id` 写入 `metadata.turn_id`，确保共享 assembler 与 fallback 路径都能拿到来源 turn。
- gRPC `ContextItem` proto 新增 `turn_id` 与 `has_dialogue`；原有 `kind / title / source / context_text` 标记为 deprecated，并在最终返回中不再填充。
- gRPC 适配层返回与日志载荷现在都只暴露：正文、分数、`has_dialogue`、`turn_id`。
- 中间层 `PreCheckResult.ContextItems` 与共享 assembler 保持原始字段，避免内部组装能力被不必要削弱。

### 4. ⚠️ 遗留问题与注意事项

- 当前仅缩减 precheck 最终 gRPC 返回；内部 `PreCheckResult` 与共享 assembler 仍保留 richer context item 结构，这属于有意保留的中间层能力。
- `context_text` 兼容字段仍在 proto 中保留，但最终返回固定为空；后续若需要彻底删除，可在下一次明确版本升级时处理。
- 已执行 `go test ./internal/app/usecase ./internal/adapters/inbound/grpcapi` 与 `go test ./...`，结果全部通过。
