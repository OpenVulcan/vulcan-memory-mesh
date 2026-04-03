# SELF REVIEW AND FIX PHASE40 PLAN

## 任务目标

- 在当前干净工作区基础上继续执行一轮真实自检。
- 优先审阅 gRPC 入站适配层的请求规范化与校验链路，确认异常输入是否会触发 panic。
- 仅修复能够通过代码路径、测试或运行语义确认的真实问题，并完成验证与提交。

## 执行步骤

1. 核对当前工作区状态，确认从干净基线开始。
2. 审阅请求规范化、请求校验与 RPC 入口的相邻逻辑，寻找可被真实触发的不完善点或风险问题。
3. 对候选问题先做代码级验证；若问题成立，再做最小化修复并补测试。
4. 运行仓库要求的测试与构建命令。
5. 对照计划复核完成情况，将计划文件改名为 `COMPLETED_` 前缀并提交。

## 技术约束

- 不修改外部 gRPC 契约。
- 不引入未经证实的大范围重构。
- 新增代码与测试继续遵守仓库双语注释规范。

## 验收标准

- 至少完成一次真实自检，并对发现的问题给出代码级修复或明确说明未发现新问题。
- 若有代码改动，必须附带相应验证。
- 通过以下验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`

## 实际结果

- 本轮自检确认一个真实问题：`NormalizePostActionRequest(...)` 和 `NormalizeWriteMemoriesRequest(...)` 会直接写入 repeated message 元素字段。如果直接测试或手工集成传入的 `timeline` / `items` 切片里混入 `nil` 元素，会在规范化阶段先于校验直接 panic。
- 已完成修复：
  - 让 `NormalizePostActionRequest(...)` 与 `NormalizeWriteMemoriesRequest(...)` 在遍历 repeated message 时跳过 nil 元素，避免规范化阶段崩溃。
  - 让 `ValidatePostAction(...)` 与 `ValidateWriteMemories(...)` 对 nil 元素返回稳定的 `InvalidArgument` 校验错误，而不是让这类脏请求悄悄穿过或直接 panic。
  - 补充定向回归测试，覆盖 `PostAction(...)` 和 `WriteMemories(...)` 收到 nil repeated-item 时的传输层行为。
- 已完成验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
