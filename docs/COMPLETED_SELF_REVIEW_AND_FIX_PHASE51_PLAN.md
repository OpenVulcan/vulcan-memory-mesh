# SELF REVIEW AND FIX PHASE51 PLAN

## 任务目标

- 在当前干净基线下继续执行一轮真实自检。
- 优先审阅导出 usecase 入口与直接调用路径，确认是否仍存在可被直接触发的不完善点或风险问题。
- 仅修复可以通过代码路径、测试或运行行为确认的真实问题，并完成验证与提交。

## 执行步骤

1. 核对当前工作区状态，确认从干净基线开始。
2. 审阅导出 usecase 入口、直接调用路径和相邻依赖保护逻辑，寻找可被真实触发的问题。
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

- 本轮确认了一个真实问题：`WorkspaceUseCase` 的全部导出管理入口之前都直接访问 `u.store`，没有 `nil receiver` 或 `nil store` 防护。
- 该问题会导致直接测试或手工集成在使用 `nil *WorkspaceUseCase` 或部分装配实例时，`ListProjects / ResolveProject / EnsureProject / DeleteProject / MigrateProject / ResolveUser / ListUsers / DeleteUser` 都可能因为访问 `u.store` 触发真实 panic。
- 已完成修复：
  - `internal/app/usecase/workspace.go`
  - 新增统一 `workspaceStore()` helper，让全部导出管理入口在 `nil receiver / nil store` 下快速返回稳定错误 `workspace store is nil`。
- 已补回归测试：
  - `TestWorkspaceUseCaseRejectsNilReceiver`
  - `TestWorkspaceUseCaseRejectsNilStore`
  - `TestWorkspaceUseCaseListProjectsUsesStore`
- 已完成验证：
  - `go test ./internal/app/usecase -count=1`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
