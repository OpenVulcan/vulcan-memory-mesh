# 任务计划：分析 SQLite FTS BM25 变化对当前评分策略的影响

## 任务目标

本次任务目标是系统性分析 VulcanMemoryMesh 在从旧 Go 侧 SQL FTS 路径切换到 `vldb-sqlite` 库内 FTS 之后，BM25 算法或其实现细节变化是否会影响当前检索、融合、重排与最终候选评分策略，并明确是否需要补充保护措施或调参策略。

## 执行步骤

1. 梳理当前 VMM 中 lexical 检索、RRF 融合、Weibull 衰减、MMR、多阶段筛选与最终结果形成链路。
2. 回溯旧 SQL FTS 路径中的 BM25 使用方式、排序方向、字段权重、结果消费方式。
3. 分析新 `vldb-sqlite` 库内 FTS / BM25 的对外返回语义，以及 VMM 当前如何消费这些分数。
4. 评估 BM25 算法变化对：
   - lexical 通道内部排序
   - vector + lexical 融合
   - rerank 前候选池
   - reviewer 侧观测候选
   - 采纳与写回策略
   的实际影响。
5. 输出结论：判断当前是否会引发评分策略失真、边界风险或需要额外保护，并给出建议。

## 技术选型与处理原则

- 优先基于当前仓库源码和本地依赖库源码做静态分析，不做拍脑袋推断。
- 区分“绝对分数变化”和“相对排序变化”两类风险，避免把所有 BM25 差异都误判成主链路问题。
- 区分“影响召回结果”与“仅影响解释性日志/候选展示”的不同级别影响。
- 若涉及不确定语义，必须明确指出推断依据和不确定点，不能把推断包装成事实。

## 验收标准

1. 明确描述旧 lexical 评分路径与新 lexical 评分路径的差异。
2. 明确说明当前 RRF / rerank / MMR / reviewer 是否依赖 BM25 的绝对值还是仅依赖排序。
3. 明确指出当前是否存在评分策略风险，以及风险出现的具体条件。
4. 输出可执行的建议：保持现状、补监控、加归一化、加回归测试，或需要进一步改造。

## 执行变更总结

### 1. 核心修复与调整概述

本次未修改业务代码，完成了对当前 SQLite FTS 路径、BM25 结果消费方式、混合检索融合链路以及 reviewer 阈值链路的系统性分析。最终结论是：BM25 或 tokenizer 变化不会直接破坏当前对外评分阈值语义，但会对词法召回通道内部排序和候选池构成带来中等风险，需要把关注点放在“召回结构漂移”而不是“绝对分数漂移”。

### 2. 📂文件变更清单

新增：
- `docs/plan/20260417-07-analyze-bm25-scoring-impact.md`

修改：
- 无

删除：
- 无

分析涉及的关键文件：
- `internal/adapters/outbound/vldb_sqlite/store.go`
- `internal/app/usecase/memory_query_search.go`
- `internal/app/usecase/precheck.go`
- `internal/app/usecase/precheck_score.go`
- `internal/app/usecase/postaction_candidate_review.go`
- `D:/projects/VulcanLocalDataGateway/vldb-sqlite/src/fts.rs`

### 3. 💻关键代码调整详情

本次为分析任务，无源码调整。核心结论如下：

- 当前 VMM 的 lexical 通道虽然接收了 SQLite FTS 的 `hit.Score`，但在统一搜索结果里并不直接使用 BM25 原始值做最终评分，而是按 lexical 排名重建 0 到 1 的归一化 rank score。
- 当前 hybrid 融合使用 RRF，核心依赖的是 rank 而不是 BM25 绝对分数。
- pre-check、post-action reviewer 等阈值逻辑消费的是融合或重排后的统一分数，而不是 SQLite FTS 的原始 BM25 值。
- 因此 BM25 变化的主要风险并非“阈值直接失真”，而是 lexical 通道内部顺序变化后，影响 topK 边界、候选池构成以及后续融合排序。

### 4. ⚠️遗留问题与注意事项

- 当前最值得关注的是 tokenizer 更换后，中文 query 的 term 切分与旧 Go 预分词路径不再一致，这比 BM25 数学公式本身更可能影响候选排序。
- `vldb-sqlite` 当前仍使用 `bm25(index, 2.0, 1.0)` 并按 `raw_score ASC` 排序，公式层面未发现明显突变，但词项生成变化仍可能改变结果。
- 如果后续需要更高确定性，建议增加离线回归集，对比旧路径与新路径的 topK overlap、RRF 后 topN 变化、reviewer 命中变化，而不要只比较 BM25 数值。
