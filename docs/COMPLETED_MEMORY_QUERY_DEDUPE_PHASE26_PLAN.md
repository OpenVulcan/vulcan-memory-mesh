# MEMORY QUERY DEDUPE PHASE26 PLAN

## 任务目标

- 审阅并修复同一次 `MemoryQuery` 请求中等价 query group 会重复执行完整检索链的问题。
- 在不改变对外 gRPC 契约的前提下，让等价 group 复用 embedding 与检索结果，同时保持调用方看到的回显顺序不变。
- 为该问题补充可复现测试，并完成仓库要求的验证闭环。

## 执行步骤

1. 审阅 `MemoryUseCase.Search(...)` 当前对 grouped query 的 embedding 与检索执行路径。
2. 设计“同请求内结果复用”方案，确保重复 group 不会重复触发 embedding、向量检索和后续混合检索链。
3. 实现修复，并补充定向测试验证重复 group 的成本不会被放大。
4. 运行仓库要求的测试与构建命令。
5. 对照计划逐项复核，并在完成后将文件重命名为 `COMPLETED_` 前缀。

## 技术约束

- 不修改 proto、RPC 字段和返回结构。
- 保持 `adapters -> app -> logic/domain` 的依赖方向不变。
- 新增实现与测试继续遵守仓库双语注释规范。

## 验收标准

- 同一次请求内，等价 query group 不再重复触发完整检索链。
- 返回结果仍保持逐 group 回显，且顺序与调用方输入一致。
- 新增测试能够验证 embedding / vector 调用不会因为重复 group 被放大。
- 通过以下验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
