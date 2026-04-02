# PRECHECK REVIEW AND FIX PHASE 20 PLAN

## 任务目标

- 继续审阅 `PreCheck` 检索链，重点检查 query 构造、分组召回和候选聚合之间的一致性与冗余问题。
- 找出一个真实且可复现的问题，并完成修复、测试补强与闭环验证。
- 保持现有对外 gRPC 契约不变，仅修正内部实现语义与无效开销。

## 执行步骤

1. 审阅 `buildPreCheckMemoryQueryJSON(...)`、`searchMemoryCandidates(...)` 以及相关测试。
2. 确认是否存在重复 query 导致的重复召回、重复证据或额外成本问题。
3. 对发现的问题做最小化修复，并补充定向测试。
4. 运行仓库要求的测试与构建命令。
5. 对照计划逐项复核，并在完成后将文件重命名为 `COMPLETED_` 前缀。

## 技术约束

- 不修改对外 proto 和 RPC 字段。
- 保持现有分层依赖方向不变。
- 新增代码与测试继续遵守双语注释规范。

## 验收标准

- 至少修复一个真实问题，而不是样式性整理。
- 新增测试能够覆盖该问题。
- 通过以下验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
