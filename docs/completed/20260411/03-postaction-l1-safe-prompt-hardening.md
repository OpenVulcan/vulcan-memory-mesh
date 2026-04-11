# 任务计划：按安全契约优化 postaction L1 提示词

## 任务目标

在不破坏当前运行时代码校验契约的前提下，对 `postaction_l1_main` 提示词做一次安全增强，保留“用户显式纠正画像必须产出画像候选”的核心规则，同时补强纯 JSON 输出约束，避免引入当前代码未支持的新枚举或过强的互斥策略。

## 详细执行步骤

1. 对照当前仓库已提交版本与现有工作区版本，确认哪些增强项属于安全可落地的 prompt 优化。
2. 仅保留以下安全项：
   - 强化 `user_confirmed` 对画像纠偏场景的覆盖。
   - 强化“用户显式纠正画像时必须产出 `profile_nodes` 候选”的规则。
   - 增加“输出必须是绝对纯净 JSON object”的硬约束。
3. 显式排除以下高风险项：
   - 不新增当前代码未支持的 `admission_reason` 枚举。
   - 不引入 `memory_nodes` / `profile_nodes` 的绝对互斥硬规则。
   - 不把“静默丢弃”扩展成会改变当前审计可观测性的激进策略。
4. 如有必要，同步微调中英文 prompt bundle，保持双语 bundle 一致。
5. 运行 `post-action` 相关最小测试，确认 prompt 调整未破坏现有契约。

## 技术选型与处理原则

- 优先遵守当前代码中的解析常量、枚举校验与现有示例契约，不让 prompt 超前于代码能力。
- 优先做“约束更清晰”的增强，而不是“语义边界重构”。
- 保持现有 L1/L2 职责边界：
  - L1 负责提炼候选与首轮准入提示；
  - L2 与后续写回流程负责最终采纳、替换、退役。

## 验收标准

- `postaction_l1_main` 中英文 prompt 明确要求输出绝对纯净 JSON object。
- 画像纠偏规则继续保留，且不引入新的非法 `admission_reason`。
- 不新增 `memory/profile` 绝对互斥约束，不破坏当前示例与现有系统设计。
- 最小测试命令通过。

## 执行变更总结

### 1. 核心修复与调整概述

- 已按安全契约完成 `postaction_l1_main` 的二次优化，保留了此前关于“用户显式纠正画像必须产出画像候选”的修复。
- 已新增更严格的纯 JSON 输出红线，降低 LLM 用代码块、解释性前后缀或思维链污染解析结果的风险。
- 已明确不引入当前代码未支持的新 `admission_reason`，也未引入会改变系统现有语义边界的强互斥规则。

### 2. 📂文件变更清单

- 新增：`docs/plan/20260411-03-postaction-l1-safe-prompt-hardening.md`
- 修改：`configs/prompts/default_cn/postaction_l1_main.md`
- 修改：`configs/prompts/default_en/postaction_l1_main.md`

### 3. 💻关键代码调整详情

- 在中英文 `postaction_l1_main` 中将约束 1 从“返回合法 JSON”加强为：
  - 首字符必须是 `{`
  - 末字符必须是 `}`
  - 禁止 Markdown 代码块
  - 禁止前缀、后缀、解释性说明、思考过程
- 本次没有引入 `not_valuable_for_long_term` 等新枚举，避免触发当前运行时对 `admission_reason` 的非法值校验。
- 本次没有引入 `memory_nodes` 与 `profile_nodes` 的绝对互斥规则，避免与现有示例、现有沉淀设计和后续链路预期冲突。
- 已执行最小验证命令并通过：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`

### 4. ⚠️遗留问题与注意事项

- 当前仓库中仍有上一轮未提交的文档与 prompt 改动，本次是在其基础上继续收敛，没有回退已有修复。
- 如果后续仍希望引入更细粒度的拒绝原因，例如“长期无价值”，需要同步修改：
  - `internal/logic/domain/turn_analysis.go`
  - `internal/logic/processor/turn_analyzer.go`
  - `internal/app/usecase/postaction_analysis.go`
  - 以及相关测试与文档
- 工作区里存在未跟踪文件 `vmm-local.exe`，本次未处理。
