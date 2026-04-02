# PRECHECK Review And Fix Phase 15 Plan

## 目标

- 继续对 `PreCheck` 候选合并后的派生说明字段做代码审阅，修复“统计已更新但 score/origin 说明文本未重算”这类一致性问题。
- 确保 reviewer 看到的 `score_label / score_explanation / origin_label / origin_explanation` 与最终合并后的候选值一致。
- 保持当前外部 gRPC 契约不变。

## 执行步骤

1. 审阅当前派生说明字段链路：
   - `internal/app/usecase/precheck.go`
   - `internal/app/usecase/precheck_candidate_merge.go`
   - `internal/app/usecase/precheck_score.go`
   - `internal/app/usecase/precheck_origin.go`
   - 相关测试
2. 识别真实问题：
   - 合并后 `score/origin` 说明是否仍引用旧命中信息
3. 修复确认存在的问题：
   - 调整派生字段的生成时机或重算逻辑
   - 补充测试
   - 必要时同步文档
4. 验证与收口：
   - 运行仓库要求的最小测试
   - 运行 `go test ./... -count=1`
   - 运行 `.\make.ps1 build`

## 技术取舍

- 本阶段继续只修真实问题，不新增功能面。
- 优先保证 reviewer 输入中的派生说明与最终候选值严格一致。
- 若未发现问题，则给出明确结论和剩余风险。

## 验收标准

- 合并后的候选其 `score/origin` 说明字段与最终值保持一致。
- 问题有测试覆盖。
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `.\make.ps1 build`
