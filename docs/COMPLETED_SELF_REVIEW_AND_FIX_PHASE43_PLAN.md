# SELF REVIEW AND FIX PHASE43 PLAN

## 任务目标

- 在当前干净工作区基础上继续执行一轮真实自检。
- 优先审阅运行时装配入口与生命周期方法，确认部分装配对象在启动路径上是否仍会触发 panic。
- 仅修复能够通过代码路径、测试或运行语义确认的真实问题，并完成验证与提交。

## 执行步骤

1. 核对当前工作区状态，确认从干净基线开始。
2. 审阅应用启动、停机与相邻 helper，寻找可被真实触发的不完善点或风险问题。
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

- 本轮自检确认一个真实问题：`Application.Shutdown(...)` 在逆序遍历 `Shutdowns` 时会直接调用每个槽位的 `Shutdown(...)`。如果部分装配对象把 `nil` 槽位塞进了这个切片，清理链会在该位置直接 panic，后续有效依赖也无法释放。
- 已完成修复：
  - 让 `Shutdown(...)` 在遍历 `Shutdowns` 时显式跳过 `nil` 依赖。
  - 保持其余有效 shutdown hook 继续执行，并保留原有的错误聚合语义。
  - 补充定向回归测试，覆盖 `Shutdowns` 切片含 `nil` 槽位时仍能正常释放有效依赖这条真实路径。
- 已完成验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
