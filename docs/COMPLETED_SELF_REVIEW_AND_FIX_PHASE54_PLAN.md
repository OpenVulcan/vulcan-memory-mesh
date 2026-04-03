# Self Review And Fix Phase 54 Plan

## 任务目标 / Goal

- 继续执行仓库自检，定位仍可能存在的真实代码不完善点或风险问题。
- Continue the repository self-review and identify any remaining real robustness or risk issues.

## 执行步骤 / Steps

1. 审查尚未重点覆盖的导出入口，尤其是 memory/profile/workspace/gRPC 辅助路径中的直接调用与部分装配场景。
2. 验证候选问题是否能够通过真实代码路径成立，避免修复建立在误读或猜测之上。
3. 对确认存在的问题实施最小且可靠的修复，并补齐回归测试。
4. 运行仓库要求的测试与构建，确认行为稳定后提交结果。

## 技术原则 / Technical Principles

- 只修复真实存在的问题，不做装饰性改动。
- Keep dependency direction and current OSS runtime architecture unchanged.
- 所有新增测试都要能解释“此前为什么会出错、现在为什么不会”。

## 验收标准 / Acceptance Criteria

- 找到并验证至少 1 个真实问题，或明确记录本轮未发现新问题。
- 若有修复，补齐回归测试并完成规定验证。
- 计划文件完成后改名为 `COMPLETED_` 前缀。

## 实际结果 / Actual Result

- 本轮确认了 1 个真实问题，位置在 `internal/app/usecase/profile.go`：
  - `ProfileUseCase.ApplyInstruction(...)` 在命中共享 in-flight 去重路径时，会进入 `waitProfileInstructionFlight(...)`。
  - 该函数此前直接对 `ctx.Done()` 做 `select`，所以直接测试或手工集成若误传 `nil context`，会在我们自己的代码里真实 panic。
- 已完成修复：
  - 新增 `normalizeProfileUseCaseContext(...)`，统一把 `nil context` 归一到 `context.Background()`。
  - 已把该归一化接入 `GetNodes(...)`、`GetBundle(...)`、`ApplyInstruction(...)` 和 `waitProfileInstructionFlight(...)`，保证导出画像入口与共享 flight 等待路径都保持 nil-safe。
- 已补充回归测试：
  - `TestProfileUseCaseApplyInstructionReusesSharedFlightWithNilContext`
- 已完成验证：
  - `go test ./internal/app/usecase -count=1`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
