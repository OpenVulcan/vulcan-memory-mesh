# SELF REVIEW AND FIX PHASE48 PLAN

## 任务目标

- 在当前干净基线下继续执行一轮真实自检。
- 优先审阅 gRPC 入站链路与相邻响应构造逻辑，确认是否仍存在可被直接触发的不完善点或风险问题。
- 仅修复可以通过代码路径、测试或运行行为确认的真实问题，并完成验证与提交。

## 执行步骤

1. 核对当前工作区状态，确认从干净基线开始。
2. 审阅 gRPC 入站处理与响应构造链路，寻找可被真实触发的问题。
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

- 本轮确认了一个真实问题：`ScopeResolutionInterceptor` 之前只根据 `UnaryServerInfo.FullMethod` 判定是否需要范围解析，并且在记录 `"grpc scope resolved"` 日志时直接读取 `info.FullMethod`。
- 该问题会导致导出的拦截器在直接测试或手工集成缺少 `UnaryServerInfo` 时出现两类缺口：
  - `PreCheck/PostAction/WriteMemories` 这类业务链请求会被错误地跳过范围解析。
  - 一旦补上按请求类型推断范围解析，日志路径又会因为直接访问 `info.FullMethod` 而 panic。
- 已完成修复：
  - `internal/adapters/inbound/grpcapi/interceptors.go`
  - 新增 `requiresResolvedScopeInvocation(...)`，在缺少方法元数据时回退到具体请求类型推断业务链请求。
  - `ScopeResolutionInterceptor` 的日志输出改为统一复用 `unaryMethodName(info)`，避免 `nil info` 下的 panic。
- 已补回归测试：
  - `TestScopeResolutionInterceptorInfersBusinessRPCWithoutInfo`
- 已完成验证：
  - `go test ./internal/adapters/inbound/grpcapi -count=1`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
