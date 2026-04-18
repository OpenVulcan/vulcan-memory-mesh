# 任务计划：基于真实 SQLite FFI 数据库执行 BM25 与评分链路测试

## 任务目标

本次任务目标是在当前 VulcanMemoryMesh 仓库内，基于真实 `vldb-sqlite` 动态库、本地临时数据库和模拟业务数据，构建一组可直接执行的集成测试，验证 BM25 / tokenizer 变化在真实 FFI 数据库路径下是否会影响当前业务评分语义与候选顺序。

## 执行步骤

1. 梳理适合承载真实 SQLite FFI 集成测试的测试包与辅助桩，确定最小接入路径。
2. 新增基于临时数据库、真实 `vldb_sqlite.dll` 和模拟 memory 节点的测试夹具。
3. 构造中文查询样本，验证：
   - 真实 SQLite FTS 排序能驱动 lexical 候选顺序
   - 业务对外分数仍保持 rank-normalized 语义
4. 运行集成测试并记录结果，判断是否需要进一步补日志或调参。
5. 补充执行变更总结并归档计划文件。

## 技术选型与处理原则

- 必须走真实本地 FFI 动态库和临时 SQLite 数据库，不允许只停留在桩对象层。
- 测试数据必须在测试时目录中创建并在测试结束后自动回收，避免污染正式 `output/database`。
- 优先复用现有 `MemoryUseCase` 主链路与 `vldb_sqlite.Store`，确保结论贴近真实业务运行路径。
- 新增测试代码必须遵守仓库双语注释规范。

## 验收标准

1. 至少新增一条真实 SQLite FFI 集成测试，并成功执行通过。
2. 明确验证真实数据库路径下对外评分是否仍是 rank-normalized。
3. 明确验证真实数据库路径下 lexical 排序是否会影响最终候选顺序。
4. 输出基于真实数据库测试的结论与后续建议。

## 执行变更总结

### 1. 核心修复与调整概述

本次新增了基于真实 `vldb_sqlite` 动态库和临时 SQLite 数据库的集成测试，不再停留在 mocked lexical hits。测试过程中直接发现当前 FTS 索引名接入存在运行时缺陷：代码把 FTS 索引名错误绑定为关系表 `vmm_memory_nodes`，而上游库要求这是独立的 FTS 文档表。修正为 `vmm_memory_nodes_fts` 后，真实数据库测试和相关包测试全部通过。

同时，这次真实测试进一步验证了两个关键结论：

- 真实 SQLite BM25 路径下，对外业务分数仍然保持 rank-normalized，不直接暴露 lexical 原始分。
- 真实 SQLite lexical 排序变化会传导到最终 top candidate，这说明我们真正要关注的是候选漂移，而不是阈值标尺失真。

### 2. 📂文件变更清单

新增：
- `internal/app/usecase/memory_query_sqlite_integration_test.go`
- `docs/plan/20260417-09-real-sqlite-bm25-test.md`

修改：
- `internal/adapters/outbound/vldb_sqlite/store.go`

删除：
- 无

### 3. 💻关键代码调整详情

本次新增了三条真实 SQLite FFI 集成测试：

- `TestRealSQLiteLexicalRecallReturnsRankedHits`
  - 真实创建临时数据库，插入模拟 memory 节点，并验证 SQLite FTS 能返回确定性的 lexical 排序。

- `TestMemoryUseCaseSearchWithRealSQLiteFTSKeepsRankNormalizedScores`
  - 在真实 SQLite lexical 路径下验证 `MemoryUseCase.Search` 的对外分数仍是 `1.0 / 0.75` 这类 rank-normalized 语义。

- `TestMemoryUseCaseSearchWithRealSQLiteFTSChangesTopCandidateAcrossQueries`
  - 在真实数据库路径下验证查询词变化会导致首条候选切换，证明 lexical 排序漂移会真实传导到业务结果。

本次还修正了一处真实运行时缺陷：

- 将 SQLite 适配器中的 `memoryFTSIndexName` 从错误的关系表名 `vmm_memory_nodes` 调整为独立 FTS 文档表名 `vmm_memory_nodes_fts`。
- 这样库侧 `EnsureFtsIndex / RebuildFtsIndex / UpsertFtsDocument / SearchFts` 会正确操作独立 fts5 表，而不是误把关系表当作 FTS 表。

本次执行的关键测试命令：

- `go test ./internal/app/usecase -run "Test(RealSQLiteLexicalRecallReturnsRankedHits|MemoryUseCaseSearchWithRealSQLiteFTSKeepsRankNormalizedScores|MemoryUseCaseSearchWithRealSQLiteFTSChangesTopCandidateAcrossQueries)$" -count=1`
- `go test ./internal/adapters/outbound/vldb_sqlite ./internal/app/usecase -count=1`

结果：

- 全部通过。

### 4. ⚠️遗留问题与注意事项

- 这次已经通过真实数据库路径证明 BM25 不会直接破坏当前对外评分契约，但仍未覆盖更大规模中文语料回归；如果要评估 tokenizer 切换带来的真实召回漂移，还应补充离线 query 集回归。
- 当前新增测试依赖 `output/libs` 下已有宿主机对应的 `vldb_sqlite` 动态库；如果测试环境缺少该产物，测试会自动跳过。
- 本次修复说明此前“静态分析看起来正确”的 FTS 接入，在真实运行链路里仍可能因库约定细节而失效，后续涉及 FFI 能力变更时应优先补真实集成测试。
