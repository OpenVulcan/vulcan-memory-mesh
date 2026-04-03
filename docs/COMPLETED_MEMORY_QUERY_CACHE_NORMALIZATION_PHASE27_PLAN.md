# MEMORY QUERY CACHE NORMALIZATION PHASE27 PLAN

## 任务目标

- 继续审阅 `MemoryQuery` 单请求内 group 结果复用逻辑，修复仍会导致重复检索执行的等价输入场景。
- 保持 phase 26 的“逐组回显、顺序不变”对外行为，同时进一步收紧内部复用键的一致性。
- 为定位到的真实问题补充定向测试，并完成仓库要求的验证闭环。

## 执行步骤

1. 审阅 `buildMemoryQueryCacheKey(...)`、`parseMemoryQueryJSON(...)` 与 `PreCheck` 侧已有 query 归一策略。
2. 找出仍会让等价 query group 重复触发 embedding / vector / hybrid / rerank 的输入差异。
3. 做最小化修复，并补充针对性测试。
4. 运行仓库要求的测试与构建命令。
5. 对照计划逐项复核，并在完成后将文件重命名为 `COMPLETED_` 前缀。

## 技术约束

- 不修改对外 proto、RPC 返回结构和字段语义。
- 保持现有分层依赖方向不变。
- 新增实现与测试继续遵守仓库双语注释规范。

## 验收标准

- 至少修复一个真实的重复执行场景，而不是纯样式整理。
- 新增测试能够证明等价 group 不再重复触发检索链。
- 通过以下验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
