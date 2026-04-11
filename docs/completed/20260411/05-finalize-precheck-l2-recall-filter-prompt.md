# 任务计划：定稿 precheck L2 长期记忆召回过滤提示词

## 任务目标

将 `precheck_l2_main` 的提示词定稿为“长期记忆召回过滤器”语义：严格围绕当前问题筛选真正有直接帮助的候选记忆编号，不混入“是否写入记忆”的语义，不输出额外解释文本，同时保留对当前问题判定精度有帮助的关键辅助信号约束。

## 详细执行步骤

1. 基于当前仓库中已修改但未提交的 `precheck_l2_main` 版本，进一步收敛成最终提示词文案。
2. 中英文 prompt 同步做以下优化：
   - 将 `intent_reason` 的语义明确为“为什么需要回忆/召回长期记忆”。
   - 明确 `user_content` 是最高判据，`search_queries / intent_reason` 只做消歧辅助。
   - 保留 `matched_context_*`、`score*`、`origin*`、`support/rebuttal*` 的辅助约束，但禁止其替代直接相关性判断。
   - 明确“最小必要集合”原则，避免过量选择。
   - 保持只输出 `selected_candidate_numbers`。
3. 保持后端解析兼容逻辑不变，仅让 prompt 更贴近实际职责与最小输出。
4. 运行最小测试，并执行标准构建，确保实际运行产物中的 prompt 已同步更新。

## 技术选型与处理原则

- 提示词必须严格贴合当前后端真实职责：L2 只做召回候选过滤，不做记忆写入判断。
- 优先保留能提升筛选准确度的关键约束，避免为了极简而丢失上下文判定锚点。
- 继续保持最小输出格式，减少 LLM 生成 token 与延迟。

## 验收标准

- `precheck_l2_main` 中英文 prompt 与“长期记忆召回过滤器”职责完全对齐。
- prompt 不再偏离为“是否需要记忆写入”的语义。
- prompt 只要求输出 `selected_candidate_numbers`。
- 相关测试通过，标准构建完成。

## 执行变更总结

### 1. 核心修复与调整概述

- 已将 `precheck_l2_main` 定稿为“长期记忆召回过滤器”语义，明确它只负责从候选中筛选对回答当前问题有直接帮助的记忆编号。
- 已把 `intent_reason` 的描述收敛为“为什么需要回忆/召回长期记忆”，避免与 `postaction` 的“写入长期记忆”语义混淆。
- 已在保持最小输出的同时，保留 `matched_context_*` 与检索意图消歧等关键判断锚点，避免因提示词过度极简导致 LLM 丢失判定精度。

### 2. 📂文件变更清单

- 新增：`docs/plan/20260411-05-finalize-precheck-l2-recall-filter-prompt.md`
- 修改：`configs/prompts/default_cn/precheck_l2_main.md`
- 修改：`configs/prompts/default_en/precheck_l2_main.md`
- 修改：`internal/logic/domain/precheck.go`

### 3. 💻关键代码调整详情

- 中文 `precheck_l2_main`：
  - 将角色改为“上下文相关性过滤器”。
  - 强调 `user_content` 是最高判据。
  - 明确 `search_queries / intent_reason` 只做消歧辅助。
  - 保留 `matched_context_values / matched_context_support_count / matched_context_rebuttal_count / matched_context_score_delta` 的判定规则。
  - 加入“最小必要候选集合”原则，避免过量选择。
  - 维持只输出 `selected_candidate_numbers`。
- 英文 `precheck_l2_main`：
  - 与中文版本保持完全对齐的职责和约束。
- 领域注释：
  - 将 `PreCheckMemoryReviewInput` 的注释从“需要记忆”改为“需要召回长期记忆”，让代码层语义与提示词保持一致。
- 已执行验证命令并通过：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `.\make.ps1 build`

### 4. ⚠️遗留问题与注意事项

- 当前后端仍然兼容旧格式的 `reason` 字段解析，但 prompt 已不再要求模型输出它；这属于刻意保留的兼容层，不影响当前最小输出提速目标。
- 标准构建已完成并同步了 `output/configs`，但若线上或本地仍使用其他配置根目录或非标准二进制路径，仍需确保实际运行实例读取的是最新 prompt 文件。
