# PRECHECK Review And Fix Phase 11 Plan

## 目标

- 继续对 `PreCheck` 与统一检索增强链路做代码审阅，寻找第二批真实行为问题。
- 优先关注多 query group 去重、候选信息保真、以及 reviewer 输入是否因合并逻辑而丢失关键证据。
- 在不修改外部 gRPC 契约的前提下完成修复、测试与验证。

## 执行步骤

1. 继续审阅关键热路径：
   - `internal/app/usecase/precheck.go`
   - `internal/app/usecase/memory_query.go`
   - `internal/logic/processor/precheck_memory_reviewer.go`
   - 相关测试
2. 识别真实问题：
   - 多 query group 合并与去重
   - 证据字段保留与合并
   - reviewer 看到的信息是否与检索结果一致
3. 修复确认存在的问题：
   - 修改代码
   - 补充测试
   - 必要时同步文档
4. 验证与收口：
   - 运行仓库要求的最小测试
   - 运行 `go test ./... -count=1`
   - 运行 `.\make.ps1 build`

## 技术取舍

- 本阶段继续只修真实问题，不引入新功能面。
- 优先修复会影响候选正确性、证据完整性、reviewer 判断一致性的问题。
- 若未发现新问题，则明确说明审阅结论和剩余风险。

## 验收标准

- 至少完成一次有依据的代码审阅。
- 若发现问题，则问题已修复并有测试覆盖。
- 若未发现问题，则给出明确结论且验证通过。
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `.\make.ps1 build`
