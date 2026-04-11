# 任务计划：移除 precheck L2 提示词中的 reason 输出要求

## 任务目标

在保持当前后端解析兼容的前提下，移除 `precheck_l2_main` 提示词对 `reason` 输出字段的要求，减少 LLM 第二层 reviewer 的输出 token 和生成负担，以优化实际等待时间。

## 详细执行步骤

1. 确认 `precheck_l2_main` 的 `reason` 在当前后端中的真实用途，确保它不参与采纳、写回和最终业务结果决策。
2. 修改中英文 `precheck_l2_main` prompt：
   - 删除对 `reason` 的输出语言要求。
   - 删除输出格式中的 `reason` 字段。
   - 保留只输出 `selected_candidate_numbers` 的结构化契约。
3. 不主动删除后端解析兼容逻辑，保持历史响应和偶发额外字段仍可被安全解析。
4. 运行与 `precheck` 相关的最小测试，确认 prompt 调整未破坏现有链路。

## 技术选型与处理原则

- 采用“prompt 先瘦身、代码后兼容”的最小变更策略。
- 当前 `reason` 只用于日志观察，因此本次不扩大到业务层重构，优先获取实际速度收益。
- 保持解析器兼容额外字段，避免因模型偶发返回旧格式而造成回归。

## 验收标准

- `configs/prompts/default_cn/precheck_l2_main.md` 不再要求输出 `reason`。
- `configs/prompts/default_en/precheck_l2_main.md` 不再要求输出 `reason`。
- 后端仍可兼容旧格式返回，不因缺少 `reason` 报错。
- 最小相关测试通过。

## 执行变更总结

### 1. 核心修复与调整概述

- 已移除 `precheck_l2_main` 中英文 prompt 对 `reason` 输出字段的要求，让 L2 reviewer 只返回 `selected_candidate_numbers`。
- 已保留后端对旧格式 `reason` 字段的兼容解析，避免历史响应或偶发多字段输出导致回归。
- 已补充解析器测试，明确覆盖“无 `reason` 的紧凑输出仍可被安全解析”的场景。
- 已执行标准构建，确保 `output/configs` 中的 prompt 同步到最新版本，便于实际运行时直接生效。

### 2. 📂文件变更清单

- 新增：`docs/plan/20260411-04-remove-precheck-l2-reason-output.md`
- 修改：`configs/prompts/default_cn/precheck_l2_main.md`
- 修改：`configs/prompts/default_en/precheck_l2_main.md`
- 修改：`internal/logic/processor/precheck_memory_reviewer_test.go`

### 3. 💻关键代码调整详情

- 在 `precheck_l2_main` 中删除了：
  - 对 `reason` 的输出语言要求
  - 输出示例中的 `reason` 字段
- 保留 `selected_candidate_numbers` 作为唯一必需输出字段，直接降低 L2 输出 token。
- 新增测试 `TestParsePreCheckMemoryReviewResponseAllowsMissingReason`，验证如下契约：
  - 只返回 `selected_candidate_numbers` 时解析成功
  - `Reason` 为空字符串不会报错
- 已执行验证命令并通过：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `.\make.ps1 build`

### 4. ⚠️遗留问题与注意事项

- 当前后端仍会继续解析并记录 `reason`，只是 prompt 不再要求模型输出它；这意味着兼容性保留，但日志里大概率会看到空的 `review_reason`。
- 如果后续希望进一步压缩日志开销，可以再单独评估是否删除 `precheck` 阶段日志中的 `review_reason_present / review_reason` 字段，但这已经不影响 LLM 生成速度。
