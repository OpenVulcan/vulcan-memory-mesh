# SELF REVIEW AND FIX PHASE38 PLAN

## 任务目标

- 在当前干净工作区基础上继续执行一轮真实自检。
- 优先审阅最近修复相邻路径，重点关注 gRPC 入站适配层、请求验证与异常输入下的健壮性。
- 仅修复能够通过代码路径、测试或运行语义确认的真实问题，并完成验证与提交。

## 执行步骤

1. 核对当前工作区状态，确认从干净基线开始。
2. 审阅 gRPC 入站适配层、请求验证与错误映射相邻逻辑，寻找可被真实触发的不完善点或风险问题。
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

- 本轮自检确认一个真实问题：导出的 `Server` RPC 方法默认假定调用方一定通过 `NewServer(...)` 完整装配并传入非 nil `context.Context`。在直接测试或手工集成里，如果使用部分装配的 `Server` 调用导出方法，会在 `s.validate.Validate...` 或 `withTimeout(nil, ...)` 路径上直接 panic。
- 已完成修复：
  - 新增 `Server.validator()` 回退逻辑，在 `validate == nil` 时自动使用默认 `RequestValidator`。
  - 将 `withTimeout(...)` 调整为 nil-safe，在 `ctx == nil` 时回退到 `context.Background()`。
  - 补充定向回归测试，覆盖 `ListProjects(nil, ...)` 与 `ResolveProject(...)` 在 nil-context / nil-validator 路径下的行为。
- 已完成验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
