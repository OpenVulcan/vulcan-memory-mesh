# PRECHECK REVIEW AND FIX PHASE 18 PLAN

## 任务目标

- 继续审阅 `PreCheck` 第二层 reviewer 载荷、采纳结果映射和最终注入链。
- 找出一个真实且可复现的一致性或稳定性问题，并完成修复与测试补强。
- 保持现有对外 gRPC 契约不变，只修正内部实现语义。

## 执行步骤

1. 审阅 `reviewMemoryCandidates(...)`、reviewer 输入输出解析以及 `finalizePreCheck(...)` 的衔接逻辑。
2. 检查是否存在编号映射、重复采纳、顺序恢复或最终注入内容不一致的问题。
3. 对发现的问题做最小化修复，并补充定向测试。
4. 运行仓库要求的测试和构建命令。
5. 对照计划逐项复核，并在完成后将文件重命名为 `COMPLETED_` 前缀。

## 技术约束

- 不修改 proto 和外部 RPC 字段。
- 保持现有分层依赖方向不变。
- 新增代码和测试继续遵守仓库双语注释规范。

## 验收标准

- 至少修复一个真实问题，而不是样式性调整。
- 新增测试能够覆盖该问题。
- 通过以下验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
