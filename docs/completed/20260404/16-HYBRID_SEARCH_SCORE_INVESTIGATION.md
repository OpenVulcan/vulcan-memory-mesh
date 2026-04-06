# 任务目标

排查并修复 `memory search first-stage combined sql completed` 日志中混合检索候选分数异常偏低的问题，确认是否存在计分逻辑错误、日志字段语义错误或分数归一化链路不一致，并补齐验证用例，确保第一阶段检索日志能够准确反映候选质量。

# 详细执行步骤

1. 梳理第一阶段混合检索链路，定位 `combined sql` 结果从数据库读取、映射到命中候选、再输出日志的完整路径。
2. 对比向量检索分、词法检索分、融合分、排序补偿分、review 阶段分数的语义与取值范围，确认当前日志中的 `score` 字段实际代表什么。
3. 结合当前日志样例与代码实现，判断是否存在以下问题：
   - 数据库返回的是距离值却被当作相似度直接透出；
   - SQL 融合分未做归一化却直接写入统一命中结构；
   - 第一阶段日志错误复用了仅供内部排序的临时值；
   - 混合检索命中在映射或去重阶段被低分覆盖。
4. 若确认是实现问题，则按最优方案修复代码，并补充针对异常分值场景的单元测试或集成测试。
5. 执行与本次改动相关的 Go 测试，验证检索分数、排序稳定性与日志语义均符合预期。
6. 对照本计划逐项复核完成情况，在文末补充「执行变更总结」，然后将计划文件迁移到 `docs/completed/`。

# 技术选型

- 以现有 Go 单元测试为主，优先在 `internal/app/usecase` 与相关适配器层补充回归测试。
- 尽量保持既有对外接口与阈值契约不变，优先修复内部 score 语义错位或日志误导问题。
- 若涉及多层 score 语义冲突，优先保证：
  - 检索排序使用的内部分数自洽；
  - pre-check 阈值使用的 reviewer 分数稳定；
  - 日志输出能明确表达当前字段属于哪一类分数。

# 验收标准

- 能明确说明日志样例中低分的根因，不保留“疑似”状态。
- 修复后，第一阶段日志中的顶部命中分数与该阶段实际排序语义一致，不再出现明显违背直觉的低分展示。
- 相关测试通过，至少覆盖混合检索首条命中分数展示与排序稳定性。
- 计划文件补齐执行变更总结并完成归档。

## 执行变更总结

### 1. 核心修复与调整概述

- 已确认根因：PostgreSQL `combined sql` 一阶段检索直接把 SQL 侧的原始 RRF `fused_score` 透传为统一检索分数；在 `rrfK=60` 时，单通道首名天然会落到 `1/(60+1)=0.016393...`，这不是“命中质量很差”，而是“内部排序值被误当成对外分数”。
- 已在统一检索用例中为 `combined sql` 路径补充分数归一化，把原始 RRF 小分数转换为稳定的 `0..1` 排名分语义，确保日志展示、后续阈值判断与最终响应保持一致。
- 已额外保留 `raw_score` 诊断字段，让日志既能展示可解释的对外分数，也能在需要时回看 SQL 侧原始融合分。

### 2. 📂 文件变更清单

- 新增：`docs/plan/20260404-16-HYBRID_SEARCH_SCORE_INVESTIGATION.md`
- 修改：`internal/app/usecase/memory_query.go`
- 修改：`internal/app/usecase/memory_query_test.go`
- 删除：无

### 3. 💻 关键代码调整详情

- 在 `internal/app/usecase/memory_query.go` 的 `Search` 流程中，针对 `usedCombinedHybridSQL=true` 的首阶段命中增加 `normalizeCombinedHybridSQLHits` 处理。
- 新增 `normalizeCombinedHybridSQLHits`：按当前已排好序的 SQL 融合结果，把原始 RRF 分数改写为稳定的 rank-normalized 分数，并把原始值写入 `metadata.raw_score`。
- 扩展 `memoryQueryHitLogPayload` 与 `summarizeRawMemoryHitForLog`，在日志中增加 `raw_score` 输出，避免后续再次把“内部排序值”误读为“最终相似度分”。
- 在 `internal/app/usecase/memory_query_test.go` 中补充回归测试：
  - 验证原始 `0.032786/0.016393` 场景会被归一化成稳定的 caller-facing 分数；
  - 验证 `memory search first-stage combined sql completed` 日志会同时输出 `score` 与 `raw_score`。

### 4. ⚠️ 遗留问题与注意事项

- 当前修复聚焦于 PostgreSQL `combined sql` 快速路径；普通向量检索、应用层 hybrid fan-out、rerank/MMR 链路未改变既有语义。
- 现有日志字段名仍沿用 `top_vector_hit` / `vector_hit_count`，但在 `combined sql` 场景下它们表示“首阶段返回命中”，不是“纯向量召回命中”；这次未改字段名，以避免额外的日志契约波动。
- 已执行 `go test ./internal/app/usecase` 与 `go test ./...`，结果全部通过。
