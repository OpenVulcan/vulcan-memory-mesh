# PRECHECK REVIEW AND FIX PHASE 19 PLAN

## 任务目标

- 继续审阅 `PreCheck` 第二层评审、最终注入内容和生命周期写回链路。
- 找出一个真实且可复现的问题，并完成修复、测试补强和闭环验证。
- 保持现有对外 gRPC 契约不变，仅修正内部实现一致性与副作用问题。

## 执行步骤

1. 审阅 `reviewMemoryCandidates(...)`、`finalizePreCheck(...)`、`writeMemoryAdoption(...)` 以及相关测试。
2. 检查是否存在请求失败后仍产生副作用、重复采纳导致异常写回、或最终注入内容与采纳结果不一致的问题。
3. 对发现的问题做最小化修复，并增加可复现测试。
4. 运行仓库要求的测试和构建命令。
5. 对照计划逐项复核，并在完成后将文件重命名为 `COMPLETED_` 前缀。

## 技术约束

- 不修改对外 gRPC proto 和请求响应字段。
- 保持当前仓库分层依赖方向不变。
- 新增代码与测试继续遵守双语注释规范。

## 验收标准

- 至少修复一个真实问题，而不是样式性整理。
- 新增测试能够覆盖该问题。
- 通过以下验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
