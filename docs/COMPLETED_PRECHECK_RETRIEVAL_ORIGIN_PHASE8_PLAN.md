# PRECHECK Retrieval Origin Phase 8 Plan

## 目标

- 统一 `PreCheck` 候选里的检索来源语义，让 reviewer 和调试侧都能更直观看懂候选为什么以当前顺序出现。
- 在不修改外部 gRPC `proto` 的前提下，把当前 `origin` 代码值整理成稳定、可解释的来源标签与解释文本。
- 保持现有检索链和排序行为不变，只增强候选解释层。

## 执行步骤

1. 审核当前来源链路：
   - `internal/app/usecase/memory_query.go`
   - `internal/app/usecase/precheck.go`
   - `internal/logic/domain/precheck.go`
   - `internal/logic/processor/precheck_memory_reviewer.go`
   - `configs/prompts/*/review_precheck_memory.md`
2. 设计统一来源语义：
   - 梳理 `vector_search / lexical_search / hybrid_rrf / *_rerank / *_mmr` 的稳定解释
   - 给 `PreCheckMemoryCandidate` 补充 reviewer 可读字段
3. 落地来源解释：
   - 在 `PreCheck` 候选构建处填充统一来源标签和解释
   - 在 reviewer 输入归一化时保留这些字段
4. 补文档与测试：
   - 覆盖来源解释映射与 reviewer 请求体渲染
   - 同步 README 中对第二层候选来源说明的必要文字
5. 验证与收口：
   - 运行仓库要求的最小测试
   - 运行 `go test ./... -count=1`
   - 运行 `.\make.ps1 build`

## 技术取舍

- 这一阶段不扩 `SearchMemoryEvents` 的对外返回字段，避免直接引入协议变更。
- `origin` 继续保留现有稳定代码值，新增字段只承担解释层职责。
- 优先让 reviewer 可读，而不是暴露更多底层排序实现细节。

## 验收标准

- `PreCheckMemoryCandidate` 能同时保留原始 `origin` 与统一的来源解释字段。
- `review_precheck_memory` 能看到更可读的来源标签/说明。
- 外部 gRPC 契约保持不变。
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `.\make.ps1 build`
