# Profile Review Duplicate Recovery Plan

## 任务目标 / Goal

- 验证 `post-action turn profile review failed` 中的 `duplicate candidate_index` 是否为真实缺陷。
- 修复 `review_profile_nodes` 在 LLM 重复输出同一 `candidate_index` 时整批失败的问题。
- 保障重复候选不会再导致整轮 `profile_nodes` 被清空，从而修复“明明提到了稳定习惯，却没有落画像”的现象。

## 问题拆解 / Problem Breakdown

1. `review_profile_nodes` 返回 `accepted_candidates` 时，如果同一 `candidate_index` 重复出现，当前解析器会直接返回 `InvalidLLMOutputError`。
2. `post-action` 在画像评审失败后会执行降级清空：
   - `analysis.ProfileNodes = nil`
   - `analysis.UserProfileMerged = false`
   - `analysis.ProjectProfileMerged = false`
3. 因此“日志里出现 profile review error”和“最终没有画像节点”是两段因果链，而不是两个互不相关的问题。

## 执行步骤 / Steps

1. 核实 `profile reviewer` 解析器当前对重复 `candidate_index` 的处理是否确实会整批失败。
2. 设计稳定的重复恢复策略：
   - 对同一 `candidate_index` 的重复 `accepted_candidates` 做可恢复去重
   - 在不破坏覆盖校验的前提下保留更强或更完整的一条
3. 补充处理器级测试，覆盖重复候选的恢复场景。
4. 视需要补充 `post-action` 用例级测试，验证重复候选不再导致画像整批丢失。
5. 运行规定测试与构建。
6. 完成后把计划文件改名为 `COMPLETED_` 前缀。

## 技术原则 / Technical Principles

- 保持当前 reviewer 的严格结构校验，不把明显畸形 JSON 一概放过。
- 只对“同一候选被重复输出”这种可恢复、常见的 LLM 抖动做容错。
- 恢复策略必须保持确定性，不能让同一输入在不同运行中得到不同结果。

## 验收标准 / Acceptance Criteria

- 真实确认该问题存在，并在计划中记录判断结果。
- 当 `accepted_candidates` 中重复出现同一 `candidate_index` 时，不再直接整批失败。
- 重复恢复后，覆盖校验仍然有效。
- 相关测试通过。
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `.\make.ps1 build`

## 实际判断 / Actual Findings

- 问题 1：`review_profile_nodes` 的 `duplicate candidate_index` 是真实存在的问题。
  - 当前 `internal/logic/processor/profile_reviewer.go` 会在发现重复 `accepted_candidates[].candidate_index` 时直接返回 `InvalidLLMOutputError`。
- 问题 2：日志里“没有提取画像”不是独立根因，而是问题 1 的直接后果。
  - 当前 `internal/app/usecase/postaction.go` 在画像评审失败后，会执行：
    - `analysis.ProfileNodes = nil`
    - `analysis.UserProfileMerged = false`
    - `analysis.ProjectProfileMerged = false`
  - 因此用户明明表达了稳定习惯，但最终日志仍显示 `profile_node_count=0`。

## 实际修改 / Actual Changes

- 已在 `internal/logic/processor/profile_reviewer.go` 增加重复候选恢复逻辑：
  - 对同一 `candidate_index` 的重复 `accepted_candidates` 先做稳定去重
  - 优先保留内容更丰富、元数据更完整的一条
  - 对 `supersede_node_ids` 做并集归一
- 覆盖校验仍保留：
  - 恢复后仍然要求所有候选恰好分类一次
  - accepted / invalid 仍不允许交叉重叠
- 已在 `internal/logic/processor/profile_reviewer_test.go` 增加回归测试：
  - 验证重复 `accepted_candidates` 不再整批失败
  - 验证更强内容与合并后的 `supersede_node_ids` 会被保留

## 验证结果 / Verification

- 已通过：
  - `go test ./internal/logic/processor ./internal/app/usecase -count=1`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`

## 验收对照 / Acceptance Check

- 真实确认问题存在：已完成
- 重复 `candidate_index` 不再直接整批失败：已完成
- 覆盖校验保持有效：已完成
- 测试与构建：已完成
- 计划文件改名为 `COMPLETED_`：执行中
