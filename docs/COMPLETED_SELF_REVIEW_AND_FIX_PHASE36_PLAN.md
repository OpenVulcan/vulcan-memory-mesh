# SELF REVIEW AND FIX PHASE36 PLAN

## 任务目标

- 在当前干净工作区基础上继续执行一轮真实自检。
- 只修复能够通过代码路径、测试或运行语义确认的真实问题，不做无依据的大范围整理。
- 完成修复、验证、计划闭环与提交。

## 执行步骤

1. 核对当前工作区状态，确认从干净基线开始。
2. 审阅近期修复相邻路径，优先检查 gRPC 拦截器、trace/context 辅助逻辑与导出方法在异常输入下的健壮性。
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

- 本轮自检确认一个真实问题：导出的 gRPC 拦截器与 trace/session context helper 在直接测试或手工集成传入 `nil context` 时，会沿 `metadata.FromIncomingContext(nil)`、`peer.FromContext(nil)` 或 `context.WithValue(nil, ...)` 路径触发 panic。
- 已完成修复：
  - 为一元拦截器入口统一增加 `nil context -> context.Background()` 归一化。
  - 将 `trace.WithTraceID(...)` 调整为 nil-safe。
  - 将 `withResolvedSessionRef(...)` 与 `resolvedSessionRefFromContext(...)` 调整为 nil-safe。
  - 补充 trace 与 gRPC 拦截器的定向回归测试，覆盖 `TraceIDInterceptor`、`ScopeResolutionInterceptor`、`RequestLoggerInterceptor` 以及 `WithTraceID(...)` 的 nil-context 路径。
- 已完成验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
