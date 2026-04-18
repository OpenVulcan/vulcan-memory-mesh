# 任务计划：执行 BM25 变化对检索与评分影响的直接测试

## 任务目标

本次任务目标是基于当前 VulcanMemoryMesh 本地 FFI 存储链路，直接构造可复现的测试数据和查询样本，验证 SQLite FTS / BM25 变化是否会对当前业务评分策略、候选池构成以及融合排序产生可观测影响，并输出可操作的测试结论。

## 执行步骤

1. 梳理当前仓库中适合承载回归测试的检索入口与测试夹具，确定最小可执行测试路径。
2. 构造覆盖中文短词、复合词、实体词、近义表达的测试数据，覆盖 lexical-only 与 hybrid 检索场景。
3. 运行直接测试，记录 lexical 排序、统一搜索评分、候选池与阈值行为。
4. 对测试结果进行分析，判断 BM25 / tokenizer 变化是否造成评分策略失真或仅造成候选排序漂移。
5. 输出结论，并在计划文件末尾补充执行变更总结后归档。

## 技术选型与处理原则

- 优先使用仓库现有 Go 测试框架与本地 FFI 存储链路，不做脱离主链路的纸面推演。
- 测试重点放在“排序是否漂移、候选是否换人、阈值是否误伤”，而不是只比较 BM25 原始数值。
- 若测试需要临时数据文件，必须使用测试时目录并在测试结束后自动清理，不污染正式输出目录。
- 若发现需要补测试代码，新增代码必须遵守仓库双语注释规范。

## 验收标准

1. 至少完成一组能够直接运行的 BM25 / lexical 排序影响测试。
2. 明确说明当前评分阈值是否受 BM25 绝对值变化直接影响。
3. 明确指出 lexical 排序和 hybrid 候选池是否出现可观测漂移。
4. 输出可执行建议：保持现状、补日志、补回归集或进一步调整策略。

## 执行变更总结

### 1. 核心修复与调整概述

本次围绕 BM25 / tokenizer 变更是否会影响当前业务评分策略，补充了两条直接可运行的回归测试，并执行了相关 hybrid 检索测试。测试结果确认：当前系统对 lexical 原始分值大小变化基本免疫，但对 lexical 排名顺序变化敏感，真正的风险点在候选顺序和候选池组成漂移，而不在业务阈值本身。

### 2. 📂文件变更清单

新增：
- `docs/plan/20260417-08-test-bm25-regression-impact.md`

修改：
- `internal/app/usecase/memory_query_test.go`

删除：
- 无

### 3. 💻关键代码调整详情

本次新增了两条搜索回归测试：

- `TestMemoryUseCaseSearchKeepsCallerFacingScoresStableWhenLexicalBM25MagnitudeChanges`
  - 用于验证在 lexical 排序不变的前提下，仅改变 BM25 风格原始分数大小，不会改变最终对外的 hit score。
  - 测试结果表明最终对外分数仍是按 rank 归一化后的 `1.0 / 0.75`，与原始 lexical 分值无关。

- `TestMemoryUseCaseSearchChangesCandidateOrderWhenLexicalRankChanges`
  - 用于验证只要 lexical 排名顺序发生变化，最终候选顺序就会跟着变化。
  - 测试结果表明候选顺序确实随 lexical rank 漂移而变化，但分数仍保持 rank-normalized 语义。

本次执行的直接测试命令：

- `go test ./internal/app/usecase -run "TestMemoryUseCaseSearch(FusesHybridRecall|KeepsCallerFacingScoresStableWhenLexicalBM25MagnitudeChanges|ChangesCandidateOrderWhenLexicalRankChanges)$" -count=1`

测试结果：

- 通过，耗时约 1.4 秒。

### 4. ⚠️遗留问题与注意事项

- 这次测试证明了“评分阈值语义”目前稳定，但没有证明“候选集合完全不漂移”；后者仍需要更贴近真实语料的中文 query 回归集来覆盖。
- 若后续需要进一步观察 tokenizer 从旧 Go 预分词切换到 `vldb-sqlite` 库内 tokenizer 的真实影响，建议补充真实 FFI 数据集测试，并记录 lexical rank / raw_score 诊断日志。
- 当前结论可以支持一个明确判断：不必因为 BM25 切换就立即重调 `min_similarity_score` 一类阈值，但应持续关注 lexical topK overlap 与 hybrid topN overlap。
