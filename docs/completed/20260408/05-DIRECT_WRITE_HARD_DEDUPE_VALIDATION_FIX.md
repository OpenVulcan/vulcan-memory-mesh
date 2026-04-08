## 任务目标

修复 direct-write 语义替代链路中「本地 hard dedupe 命中 + 其余候选仍需 reviewer」的混合批次场景，避免本地硬排重结果被误当成 reviewer 输出再走 reviewer 专属校验，从而导致整批写入错误失败。

## 详细执行步骤

1. 梳理 `reviewDirectWriteMemoryCandidates`、`mergePostActionMemoryReviewSectionWithHardDropped` 与 `buildDirectWriteMemoryDecisions` 的数据流，确认 hard dedupe 结果与 reviewer 结果的来源差异。
2. 在 direct-write 决策构建阶段区分「本地 hard dedupe 命中」与「reviewer 返回 dropped candidate」两类 dropped 决策：
   - 本地 hard dedupe 命中直接复用 dedupe 目标，不再套用 reviewer 专属的 `SimilarMemories` 可见性校验。
   - reviewer 返回的 dropped candidate 继续保留现有校验，确保 LLM 不能引用未展示过的旧记忆。
3. 补充混合批次回归测试，覆盖以下场景：
   - 一个候选被本地 hard dedupe 命中，但其 dedupe 目标未进入 `SimilarMemories`。
   - 同一批次另一个候选仍需进入 reviewer 并正常创建新记忆。
   - 修复后整批请求不报错，且结果同时包含“复用旧记忆”和“新建记忆”两种路径。
4. 运行与本次改动相关的最小测试集，确认 post-action、memory query、配置链路没有回归。

## 技术选型

- 维持现有 `mergePostActionMemoryReviewSectionWithHardDropped` 的覆盖性校验职责，不改动 reviewer 结果合并契约。
- 在 direct-write 决策构建阶段显式接收 hard dedupe 映射，按来源差异决定是否执行 reviewer 专属校验，减少对现有 post-action 主链路的影响。
- 使用现有测试桩补充回归用例，避免引入额外测试基础设施。

## 验收标准

1. 混合批次 direct-write 场景不再出现 `references unavailable dedupe_memory_id` 误报。
2. 本地 hard dedupe 命中的候选会正确复用旧记忆；同批次 reviewer 接受的候选仍能正常持久化。
3. reviewer dropped candidate 的 dedupe 目标校验继续保留，避免放宽 LLM 输出约束。
4. 相关最小测试集通过。

## 执行变更总结

### 1. 核心修复与调整概述

- 修复了 direct-write 决策构建阶段把本地 hard dedupe 结果误当成 reviewer 输出再做可见性校验的问题。
- 现在 direct-write 在合并 reviewer 结果与 hard dedupe 结果后，会按来源区分处理：
  - 本地 hard dedupe 命中的候选直接复用 dedupe 目标。
  - reviewer 返回的 dropped candidate 仍然继续执行原有 `SimilarMemories` 可见性校验。
- 补充了“同一批次内同时存在 hard dedupe 复用与 reviewer 新建”的回归测试，验证整批请求不会再因误校验而失败。

### 2. 📂文件变更清单

- 新增：
  - `docs/plan/20260408-05-DIRECT_WRITE_HARD_DEDUPE_VALIDATION_FIX.md`
- 修改：
  - `internal/app/usecase/memory_query.go`
  - `internal/app/usecase/memory_query_test.go`
  - `internal/app/usecase/postaction_test.go`
- 删除：
  - 无

### 3. 💻关键代码调整详情

- `buildDirectWriteMemoryDecisions`
  - 新增 `hardDropped` 入参。
  - 在逐候选生成 direct-write 决策时，优先识别本地 hard dedupe 命中并直接生成 `DedupedExistingMemoryID`，不再走 reviewer 专属 `dedupe_memory_id` 可见性校验。
- `reviewDirectWriteMemoryCandidates`
  - 调整 direct-write 决策构建调用，显式把 `reviewBuild.HardDropped` 传入最终决策构建函数。
- `stubEmbeddingClient`
  - 为测试桩补充响应队列能力，支持单个测试覆盖“先做两条查询 embedding，再做一条新建 embedding”的顺序场景。
- 回归测试
  - 新增混合批次测试，验证第一条候选走 hard dedupe 复用旧记忆、第二条候选进入 reviewer 并成功新建。

### 4. ⚠️遗留问题与注意事项

- 本次修复只收敛 direct-write 路径，不改动 post-action 主链路的 reviewer 合并与覆盖性校验契约。
- 当前工作区仍存在其他未提交改动，本次仅针对 direct-write hard dedupe 误校验问题做了最小修复。
- 已执行测试：
  - `go test ./internal/app/usecase -run "TestMemoryUseCaseWriteHardDedupeReturnsExistingMemory|TestMemoryUseCaseWriteMixedHardDedupeAndReviewerBatch"`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
