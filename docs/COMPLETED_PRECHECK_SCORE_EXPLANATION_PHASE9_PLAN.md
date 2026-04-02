# PRECHECK Score Explanation Phase 9 Plan

## 目标

- 增强 `PreCheck` 第二层候选的最终分数解释，让 reviewer 更容易理解“为什么这条候选当前排得更靠前”。
- 在不修改外部 gRPC `proto` 的前提下，把内部已有的排序信号整理成 reviewer 可读的分数字段和解释文本。
- 保持现有检索链行为不变，只增强候选解释层与 prompt 契约。

## 执行步骤

1. 审核当前候选排序链路：
   - `internal/app/usecase/memory_query.go`
   - `internal/app/usecase/precheck.go`
   - `internal/logic/domain/precheck.go`
   - `internal/logic/processor/precheck_memory_reviewer.go`
   - `configs/prompts/*/review_precheck_memory.md`
2. 设计统一分数解释：
   - 梳理最终 `score` 与 `matched_context_score_delta` 的关系
   - 设计 reviewer 可读的 `score_label / score_explanation`
3. 落地候选解释：
   - 在 `PreCheck` 候选构建处填充分数标签和说明
   - 在 reviewer 输入归一化中保留这些字段
4. 补文档与测试：
   - 覆盖分数解释映射与 reviewer 请求体渲染
   - 同步 README 中对第二层候选分数理解的必要说明
5. 验证与收口：
   - 运行仓库要求的最小测试
   - 运行 `go test ./... -count=1`
   - 运行 `.\make.ps1 build`

## 技术取舍

- 这一阶段不扩 `SearchMemoryEvents` 对外返回字段，不做协议变更。
- 解释层只消费现有最终分数和已命中的 context evidence，不反推底层每一步排序细节。
- 优先让 reviewer 做更稳的选择，而不是暴露更多调试噪声。

## 验收标准

- `PreCheckMemoryCandidate` 能同时保留最终分数与 reviewer 可读的分数解释字段。
- `review_precheck_memory` 能看到更可读的分数说明。
- 外部 gRPC 契约保持不变。
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `.\make.ps1 build`
