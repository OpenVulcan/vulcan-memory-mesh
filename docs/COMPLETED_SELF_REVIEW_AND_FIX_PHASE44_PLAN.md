# SELF REVIEW AND FIX PHASE44 PLAN

## 任务目标

- 在当前干净工作区基础上继续执行一轮真实自检。
- 优先审阅运行时装配入口与配置归一化链路，确认“配置可通过校验但装配阶段仍失败”的真实缺口。
- 仅修复能够通过代码路径、测试或运行语义确认的真实问题，并完成验证与提交。

## 执行步骤

1. 核对当前工作区状态，确认从干净基线开始。
2. 审阅配置校验、运行时装配与相邻 helper，寻找可被真实触发的不完善点或风险问题。
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

- 本轮自检确认一个真实问题：`config.Validate()` 会按 `TrimSpace` 接受带首尾空白的 provider，例如 `" openai "`、`" sqlite "`；但运行时的 `buildLLM(...)`、`buildEmbedding(...)`、`buildReranker(...)`、`buildVector(...)`、`buildRelational(...)` 只做了 `ToLower`，没有同样的裁剪。结果就是同一份配置可以通过校验，却在装配阶段报出 `unsupported ... provider`。
- 已完成修复：
  - 新增统一的 `normalizeProviderAlias(...)` helper，把运行时 provider 归一化口径对齐到配置校验。
  - 让 `buildLLM(...)`、`buildEmbedding(...)`、`buildReranker(...)`、`buildVector(...)`、`buildRelational(...)` 全部复用该 helper。
  - 补充定向回归测试，覆盖“带首尾空白的 provider 配置能通过校验，也能成功完成运行时适配器构建”这条真实路径。
- 已完成验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
