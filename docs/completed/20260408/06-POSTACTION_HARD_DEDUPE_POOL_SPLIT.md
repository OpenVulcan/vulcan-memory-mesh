# 任务计划：PostAction 硬去重候选池拆分与阈值收敛

## 任务目标

在本轮实现中，完成 PostAction 记忆候选评审链路的硬去重收敛改造，确保：

1. `postaction` 不引入任何基于 `hash` 的重复判断。
2. `hard dedupe` 使用独立于 Reviewer 展示集的更大候选池。
3. `hard dedupe` 默认阈值调整为 `0.99`。
4. `hard dedupe` 仅在候选与旧记忆 `Category` 相同的前提下生效。
5. 新增可调配置项 `hard_dedupe_pool_top_k`，默认值为 `16`。

## 执行步骤

1. 梳理当前 `memory_query` 与 `postaction_candidate_review` 的数据流，确认检索结果在何处被 `topK` 裁剪，以及当前硬去重复用 Reviewer 结果集的具体位置。
2. 调整 `memory_query` 的结果结构，拆分 Reviewer 使用的 `Hits` 与硬去重专用的 `HardDedupeHits`，并确保硬去重候选池在 `MMR` 与最终 `topK` 裁剪之前截取。
3. 扩展配置结构与默认配置加载逻辑，新增 `memory_pipeline.hard_dedupe_pool_top_k`，默认值设为 `16`，同时将 `hard_dedupe_cosine_threshold` 默认值收敛为 `0.99`。
4. 调整 `postaction_candidate_review`，让 Reviewer 继续只读取小集合 `Hits`，而 `detectHardDedupeMemoryCandidate` 改为读取 `HardDedupeHits`，并增加 `Category` 相同的限制条件。
5. 补充或修正单元测试，覆盖以下场景：
   - `hard dedupe` 能看到最终 `topK` 之外但仍在候选池中的高相似旧记忆；
   - `Category` 不同即使向量分足够高也不能自动硬去重；
   - 默认配置值与配置覆盖行为正确。
6. 按仓库要求执行测试与自检，并视需要同步更新相关文档说明。

## 技术选型与实现约束

- 保持当前依赖方向不变，仅在 `app/usecase` 与 `config` 范围内完成本轮主改动。
- 不引入新的 hash 去重逻辑，不扩大 direct-write 现有 hash 的职责边界。
- 保持 Reviewer Prompt 上下文稳定，避免通过简单放大 Reviewer `topK` 来替代候选池拆分。
- 新增和重写的关键结构、函数及复杂逻辑块必须遵守仓库现有中英文双语注释规范。

## 验收标准

1. `memory_query.Search` 返回的 Reviewer 命中集与硬去重命中集职责分离，且硬去重命中集不受最终 `topK` 裁剪影响。
2. 当高相似旧记忆因 `MMR` 或最终裁剪未进入 Reviewer 结果集时，`hard dedupe` 仍能基于扩大的候选池识别重复。
3. `hard dedupe` 只在 `Category` 相同且相似度达到 `0.99` 以上时自动命中。
4. 配置文件、配置解析与默认值测试通过，`hard_dedupe_pool_top_k` 默认值为 `16`。
5. 相关测试通过，且无引入已知构建或契约回归。

## 执行变更总结

### 1. 核心修复与调整概述

- 已将 `memory_query.Search` 的结果拆分为 Reviewer 可见的 `Hits` 与硬排重专用的 `HardDedupeHits`，其中后者会在 `MMR` 与最终 `topK` 截断之前截取，避免高相似旧记忆因多样性重排而从硬排重视野中消失。
- 已新增 `memory_pipeline.hard_dedupe_pool_top_k` 配置项，默认值设为 `16`；同时将 `memory_pipeline.hard_dedupe_cosine_threshold` 的默认值调整为 `0.99`。
- 已将 `postaction` / direct-write 的硬排重限制为“同 `Category` 且 cosine 达到阈值”才允许自动短路，其余情况继续交给 Reviewer 处理。
- 已同步补充回归测试与文档说明，覆盖候选池拆分、类目限制和默认配置行为。

### 2. 📂 文件变更清单

- 修改：
  - `configs/base.yaml`
  - `README.md`
  - `docs/post-action-guide_CN.md`
  - `internal/app/app.go`
  - `internal/app/usecase/memory_query.go`
  - `internal/app/usecase/memory_query_test.go`
  - `internal/app/usecase/postaction.go`
  - `internal/app/usecase/postaction_candidate_review.go`
  - `internal/app/usecase/postaction_candidate_review_test.go`
  - `internal/config/config.go`
  - `internal/config/config_test.go`
- 归档：
  - `docs/completed/20260408/06-POSTACTION_HARD_DEDUPE_POOL_SPLIT.md`
- 删除：
  - 无

### 3. 💻 关键代码调整详情

- `internal/app/usecase/memory_query.go`
  - 为 `MemoryQueryGroupResult` 增加 `HardDedupeHits` 字段。
  - 为 `MemoryUseCase` 增加 `ConfigureHardDedupePoolTopK` 与专用候选池计算逻辑。
  - 在检索流水线中于 `applyContextEvidenceScoring` 之后、`applyMMRSearchHits` 之前截取硬排重候选池。
- `internal/app/usecase/postaction_candidate_review.go`
  - `buildScopedMemoryReviewCandidates` 继续用 `Hits` 构建 `SimilarMemories`，改为用 `HardDedupeHits` 执行 `detectHardDedupeMemoryCandidate`。
  - `detectHardDedupeMemoryCandidate` 新增同 `Category` 限制，避免跨类目自动误杀。
- `internal/config/config.go` 与 `internal/app/app.go`
  - 增加 `hard_dedupe_pool_top_k` 的默认值、Normalize / Validate / 环境变量接入与运行时装配。
  - 将硬排重阈值默认值收敛为 `0.99`。
- 测试层
  - 增加了“reviewer 看不到但硬排重能看到”的回归样例。
  - 增加了“不同 `Category` 不允许自动硬排重”的回归样例。
  - 补充了配置默认值与校验测试。

### 4. ⚠️ 遗留问题与注意事项

- 本轮没有引入你上一轮提到的 `🧠 [基于 VMM 历史记忆]` / `[^🧠...]` 识别逻辑；这一部分仍建议放在下一轮，从 Analyzer 输入与提示词两侧同时落地。
- `postaction` 依然不会通过事件级幂等或内容 hash 来避免重复分析；当前收敛的是“结果去重”而不是“前置分析成本”。
- 历史完成记录 `docs/completed/20260408/04-POSTACTION_VECTOR_HARD_DEDUPE_CONFIG.md` 仍保留当时的 `0.985` 说明，属于历史沉淀，不作为当前运行时默认值依据。
