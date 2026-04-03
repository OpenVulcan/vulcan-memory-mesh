# SELF REVIEW AND FIX PHASE39 PLAN

## 任务目标

- 在当前干净工作区基础上继续执行一轮真实自检。
- 优先审阅最近修复相邻路径，重点关注 gRPC 入站适配层导出方法在异常调用方式下的健壮性。
- 仅修复能够通过代码路径、测试或运行语义确认的真实问题，并完成验证与提交。

## 执行步骤

1. 核对当前工作区状态，确认从干净基线开始。
2. 审阅 gRPC 入站适配层导出方法与相邻 helper，寻找可被真实触发的不完善点或风险问题。
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

- 本轮自检确认一个真实问题：导出的 `Server` RPC 方法在 `nil receiver` 下会直接 panic。例如 `(*Server)(nil).ListProjects(...)` 会沿 `s.workspace` 路径崩溃，而 `Healthz(...)` 甚至会在 nil receiver 上错误返回成功，这会误导直接测试或手工集成。
- 已完成修复：
  - 新增统一的 `requireReceiver()` 防护层，在导出 RPC 方法收到 nil `Server` 接收者时返回稳定的 `Internal` gRPC 错误。
  - 将全部导出 RPC 方法切换到该统一 guard，避免 nil receiver 路径上的 panic 或错误成功响应。
  - 补充定向回归测试，覆盖 `Healthz(...)` 与 `ListProjects(...)` 的 nil-receiver 路径。
- 已完成验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
