# SELF REVIEW AND FIX PHASE41 PLAN

## 任务目标

- 在当前干净工作区基础上继续执行一轮真实自检。
- 优先审阅 gRPC 入站适配层与拦截器装配入口，确认异常装配输入是否会触发 panic 或错误链路。
- 仅修复能够通过代码路径、测试或运行语义确认的真实问题，并完成验证与提交。

## 执行步骤

1. 核对当前工作区状态，确认从干净基线开始。
2. 审阅拦截器链构建、请求入口和相邻 helper，寻找可被真实触发的不完善点或风险问题。
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

- 本轮自检确认一个真实问题：`BuildUnaryInterceptors(...)` 会把 `Dependencies.ExtraInterceptors` 原样追加到传输链。如果调用方误传了 `nil` 拦截器，按 gRPC 本地源码的链式执行方式，这个 nil 槽位会在请求过程中被当作可执行拦截器使用，从而把请求异常降级成 `Internal`。
- 已完成修复：
  - 新增 `appendNonNilUnaryInterceptors(...)`，在装配期过滤掉 nil 额外拦截器。
  - 让 `BuildUnaryInterceptors(...)` 统一通过该 helper 追加额外拦截器，避免异常装配输入污染整条请求链。
  - 补充 bufconn 回归测试，覆盖带 `nil` 额外拦截器时 `Healthz(...)` 仍可正常返回的真实请求路径。
- 已完成验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
