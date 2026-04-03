# SELF REVIEW AND FIX PHASE30 PLAN

## 任务目标

- 在当前已完成但尚未提交的风险核查改动基础上继续执行一轮真实自检。
- 只修复能够通过代码路径、测试或运行语义确认的真实问题，不做无依据的样式整理。
- 完成修复、验证和提交闭环，并保留本轮计划文件。

## 执行步骤

1. 核对当前工作区状态，确认待提交改动范围。
2. 审阅当前改动及其相邻实现路径，优先检查：
   - 新增日志脱敏逻辑
   - 应用关闭与资源释放路径
   - 风险核查报告中关联到的热路径实现
3. 找到真实问题后做最小化修复，并补充必要测试。
4. 运行仓库要求的测试与构建命令。
5. 对照计划复核完成情况，重命名计划文件为 `COMPLETED_` 前缀，并统一提交。

## 技术约束

- 不修改外部 gRPC 契约。
- 不为了“看起来更安全”而引入未经证实的大范围重构。
- 新增代码与测试继续遵守仓库双语注释规范。

## 验收标准

- 至少完成一次真实自检，并对发现的问题给出代码级修复或明确说明未发现新问题。
- 若有代码改动，必须附带相应验证。
- 通过以下验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
