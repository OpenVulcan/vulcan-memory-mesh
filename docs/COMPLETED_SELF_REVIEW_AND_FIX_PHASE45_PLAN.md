# SELF REVIEW AND FIX PHASE45 PLAN

## 任务目标

- 在当前干净工作区基础上继续执行一轮真实自检。
- 优先审阅配置归一化与运行时启动链路，确认“配置可通过校验但实际监听/拨号失败”的真实缺口。
- 仅修复能够通过代码路径、测试或运行语义确认的真实问题，并完成验证与提交。

## 执行步骤

1. 核对当前工作区状态，确认从干净基线开始。
2. 审阅配置校验、配置归一化、运行时启动与相邻 helper，寻找可被真实触发的不完善点或风险问题。
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

- 本轮自检确认一个真实问题：`Config.Validate()` 会按 `TrimSpace` 接受带首尾空白的运行时字符串，例如 `grpc.listen_addr`、SQLite/LanceDB 地址、provider、endpoint 等；但 `Config.Normalize()` 之前并没有把这些值真正裁剪掉。结果就是配置可以通过校验，但运行时像 `net.Listen(...)` 这样的真实调用仍会因为残留空白而失败。
- 已完成修复：
  - 新增 `normalizeRuntimeStrings()`，在 `Config.Normalize()` 入口统一裁剪参与运行时装配的关键字符串字段。
  - 覆盖了 `grpc.listen_addr`、SQLite/LanceDB 地址、table/vector column、LLM/Embedding/Rerank 的 provider/endpoint/api key/model 以及 vector/relational provider、`post_action.input_mode` 等关键字段。
  - 补充定向回归测试，覆盖“带首尾空白的运行时关键字符串在 Normalize 后会被裁干净”这条真实路径。
- 已完成验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
