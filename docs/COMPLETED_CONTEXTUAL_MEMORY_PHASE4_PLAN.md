# Contextual Memory Phase 4 Plan

## 目标

- 在当前 `SQLite + LanceDB` 主线下，为长期记忆补齐“情境化记忆”第一阶段结构骨架。
- 先落地关系结构和内部数据模型：
  - `vmm_memory_nodes` 上的 `support_count / rebuttal_count`
  - 新表 `vmm_memory_context_edges`
- 保持当前外部 gRPC 契约稳定，不在这一阶段扩展 proto。

## 执行步骤

1. 审核当前记忆结构与落点：
   - `internal/logic/domain/memory.go`
   - `internal/adapters/outbound/vldb_sqlite/store.go`
   - `internal/app/ports/interfaces.go`
   - `internal/app/usecase/memory_query.go`
2. 扩展领域模型：
   - 为长期记忆记录补 `support_count / rebuttal_count`
   - 新增 `MemoryContextEdge` 领域结构
3. 扩展 SQLite schema：
   - `vmm_memory_nodes` 增加支持/反驳累计字段
   - 新增 `vmm_memory_context_edges`
   - 增加必要索引
4. 扩展 store 能力：
   - 新增 `ReplaceMemoryContextEdges(...)`
   - 在 turn 提炼写回时同步持久化上下文边
   - direct memory write 默认不写 context edge
5. 扩展用例侧骨架：
   - 让检索命中带出 `support/rebuttal` 字段
   - 为后续 context-aware scoring 预留字段与排序入口
   - 本阶段暂不改变外部 RPC
6. 补测试、文档并验证：
   - 覆盖 schema / store / usecase / config 相关测试
   - 运行仓库要求的最小测试、`go test ./...`、`.\make.ps1 build`

## 技术取舍

- 本阶段不引入 `context_json` 聚合列，避免后续支持/反驳聚合和精确过滤退化成整列重写。
- 本阶段不扩 gRPC proto，先把内部持久化骨架和排序预留位铺好。
- 上下文边写入只接 turn-analysis 路径；主动写记忆先保持空边集合，避免工具态接口被一并扩大。

## 验收标准

- `SQLite` schema 能创建并读写 `vmm_memory_context_edges`。
- `MemoryNodeRecord` 能读取并返回 `support_count / rebuttal_count`。
- `TurnAnalysis` 写回路径支持保存上下文边。
- 当前外部 gRPC 契约保持兼容。
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `.\make.ps1 build`
