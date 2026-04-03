# SELF REVIEW AND FIX PHASE47 PLAN

## 任务目标

- 在当前干净基线下继续执行一轮真实自检。
- 优先审阅 gRPC 入站装配、请求处理和相邻运行时保护逻辑，确认是否仍存在可被直接触发的不完善点或风险问题。
- 仅修复可以通过代码路径、测试或运行行为确认的真实问题，并完成验证与提交。

## 执行步骤

1. 核对当前工作区状态，确认从干净基线开始。
2. 审阅 gRPC 入站链路、运行时保护和相关 helper，寻找可被真实触发的问题。
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

- 本轮确认了一个真实问题：`Server.validator()` 已经为绕过 `NewServer(...)` 的部分装配实例提供默认兜底，但 `PostAction` 链路使用的 `sanitizer` 没有同等保护。
- 该问题会导致直接测试或手工集成如果构造 `&Server{postAction: ...}` 并直接调用 `PostAction(...)`，请求虽然不会崩溃，但会把未经存储型清洗的原始文本直接传给用例层，和主运行时契约不一致。
- 已完成修复：
  - `internal/adapters/inbound/grpcapi/server.go`
  - 新增 `postActionSanitizer()` 默认兜底 helper。
  - `sanitizePostActionRequest(...)` 现在会在部分装配实例上自动回退到默认 `PostActionTextSanitizer`。
- 已补回归测试：
  - `TestPostActionDirectCallUsesDefaultSanitizer`
- 已完成验证：
  - `go test ./internal/adapters/inbound/grpcapi -count=1`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
