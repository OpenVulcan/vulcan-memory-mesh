# PRECHECK Review And Fix Phase 12 Plan

## 目标

- 继续对 `PreCheck` 候选合并逻辑做代码审阅，修复“证据已合并但说明字段仍停留在较弱命中”这类一致性问题。
- 确保 reviewer 看到的 `origin / score explanation / matched context` 是相互一致的，不会出现合并后说明滞后的情况。
- 保持当前外部 gRPC 契约不变。

## 执行步骤

1. 审阅当前候选合并逻辑：
   - `internal/app/usecase/precheck_candidate_merge.go`
   - `internal/app/usecase/precheck.go`
   - 相关测试
2. 识别真实问题：
   - 相同分数下的代表性候选选择
   - `origin`、`score explanation` 与合并后证据是否一致
3. 修复确认存在的问题：
   - 修改合并启发式
   - 补充测试
   - 必要时同步文档
4. 验证与收口：
   - 运行仓库要求的最小测试
   - 运行 `go test ./... -count=1`
   - 运行 `.\make.ps1 build`

## 技术取舍

- 本阶段继续只修真实问题，不新增功能面。
- 优先保证 reviewer 输入内部一致性，而不是引入更多底层调试字段。
- 若未发现问题，则给出明确结论和剩余风险。

## 验收标准

- 相同 memory 被重复命中时，合并后的说明字段与证据字段保持一致。
- 问题有测试覆盖。
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `.\make.ps1 build`
