# SELF REVIEW AND FIX PHASE42 PLAN

## 任务目标

- 在当前干净工作区基础上继续执行一轮真实自检。
- 优先审阅 gRPC 入站适配层与运行时装配入口，确认异常停机路径是否会遗漏依赖释放。
- 仅修复能够通过代码路径、测试或运行语义确认的真实问题，并完成验证与提交。

## 执行步骤

1. 核对当前工作区状态，确认从干净基线开始。
2. 审阅运行时启动、停机与相邻 helper，寻找可被真实触发的不完善点或风险问题。
3. 对候选问题先做代码级验证；若问题成立，再做最小化修复并补测试。
4. 运行仓库要求的测试与构建命令。
5. 对照计划复核完成情况，并提交结果。

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

- 本轮自检确认一个真实问题：`Application.Run(...)` 在 gRPC 服务器被外部直接 `Stop()` 时，会沿 `errCh <- nil` 分支直接返回，而不会继续执行 `Shutdown()`。这会导致 `Shutdowns` 里的依赖关闭链被跳过，形成真实的资源释放缺口。
- 已完成修复：
  - 调整 `Run(...)` 的 `errCh` 分支，在 `Serve(...)` 返回后统一进入 `Shutdown()`。
  - 当 `Serve(...)` 自身有错误且 `Shutdown()` 也失败时，使用聚合错误一起返回，避免丢失任一侧失败信息。
  - 补充定向回归测试，覆盖“外部直接停止 gRPC 服务器后，`Run(...)` 仍会继续执行 shutdown hooks”这条真实路径。
- 已完成验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
