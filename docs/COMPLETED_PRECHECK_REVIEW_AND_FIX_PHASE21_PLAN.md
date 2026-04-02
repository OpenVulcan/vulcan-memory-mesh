# PRECHECK REVIEW AND FIX PHASE 21 PLAN

## 任务目标

- 继续审阅 `PreCheck` 候选构造、第二层评审恢复和最终注入链。
- 找出一个真实且可复现的问题，并完成修复、测试补强与闭环验证。
- 保持现有对外 gRPC 契约不变，只修正内部实现的一致性与稳定性问题。

## 执行步骤

1. 审阅 `searchMemoryCandidates(...)`、`reviewMemoryCandidates(...)`、`finalizePreCheck(...)` 以及相关处理器测试。
2. 检查是否存在排序、截断、恢复、去重或降级路径上的真实实现缺口。
3. 对发现的问题做最小化修复，并补充定向测试。
4. 运行仓库要求的测试和构建命令。
5. 对照计划逐项复核，并在完成后将文件重命名为 `COMPLETED_` 前缀。

## 技术约束

- 不修改对外 proto 和 RPC 字段。
- 保持当前仓库分层依赖方向不变。
- 新增代码和测试继续遵守双语注释规范。

## 验收标准

- 至少修复一个真实问题，而不是样式整理。
- 新增测试能够覆盖该问题。
- 通过以下验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
