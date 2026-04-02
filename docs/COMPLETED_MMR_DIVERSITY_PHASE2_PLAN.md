# MMR Diversity Phase 2 Plan

## 任务目标

- 在当前已经完成的 `vector + lexical + RRF + rerank(optional)` 检索链上补齐 `MMR` 多样性控制。
- 让 `PreCheck` 和 `SearchMemoryEvents` 都能从更分散的候选结果中受益，减少高分近重复记忆挤占名额的问题。
- 保持当前 gRPC 契约兼容，不修改 proto，不引入新的外部 provider。

## 执行步骤

1. 审视现有 `MemoryUseCase.Search(...)` 的候选池截断位置，改造成“先保留候选池，再做 rerank/MMR，最后截断最终 topK”。
2. 给 `MemoryQueryHit` 补充仅服务端内部使用的向量字段，以便在小候选池里计算候选之间的相似度。
3. 在 `memory_query.go` 中新增 MMR 配置、余弦相似度辅助函数和 `applyMMR(...)` 阶段。
4. 在 `config.go`、`app.go`、示例配置和 README 中新增 MMR 开关与参数说明。
5. 补充单元测试：
   - 检索链开启 MMR 后会优先保留更分散的候选
   - 关闭 MMR 时保持原始排序
   - 配置默认值、环境变量覆盖和值校验正确
6. 运行 `gofmt`、定向测试、全量测试和标准构建，确认没有回归。

## 技术选择

- 算法：Maximal Marginal Relevance (MMR)
- 落点：`MemoryUseCase.Search(...)` 内部，在 `hybrid/rerank` 之后、最终 `topK` 截断之前
- 相似度：候选记忆向量之间的余弦相似度
- 配置：
  - `memory_pipeline.mmr_enabled`
  - `memory_pipeline.mmr_lambda`

## 验收标准

- 开启 MMR 时，给定高分近重复候选池，最终结果会保留更高多样性。
- 关闭 MMR 时，检索顺序与当前 `hybrid/rerank` 输出保持一致。
- 配置默认值、环境变量覆盖、README 与示例配置保持同步。
- `go test ./... -count=1` 通过。
- `.\make.ps1 build` 通过。
