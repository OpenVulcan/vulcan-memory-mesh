# PRECHECK Review And Fix Phase 14 Plan

## 目标

- 继续对 `PreCheck` 候选合并逻辑做代码审阅，重点检查最近的代表性命中选择是否引入了新的计数/增减分丢失问题。
- 确保合并后的 `matched_context_*` 统计不会因为主次候选切换而错误忽略另一条命中的更强 evidence。
- 保持当前外部 gRPC 契约不变。

## 执行步骤

1. 审阅当前候选合并逻辑：
   - `internal/app/usecase/precheck_candidate_merge.go`
   - `internal/app/usecase/precheck.go`
   - 相关测试
2. 识别真实问题：
   - 主次候选切换后 `matched_context_support_count / rebuttal_count / score_delta` 是否仍正确合并
3. 修复确认存在的问题：
   - 调整合并实现
   - 补充测试
   - 必要时同步文档
4. 验证与收口：
   - 运行仓库要求的最小测试
   - 运行 `go test ./... -count=1`
   - 运行 `.\make.ps1 build`

## 技术取舍

- 本阶段继续只修真实问题，不新增功能面。
- 优先保证 merged candidate 的 evidence 统计正确，再谈说明层表现。
- 若未发现问题，则给出明确结论和剩余风险。

## 验收标准

- 相同 memory 被重复命中时，合并后的 `matched_context_*` 统计不会因主次切换丢失更强 evidence。
- 问题有测试覆盖。
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `.\make.ps1 build`
