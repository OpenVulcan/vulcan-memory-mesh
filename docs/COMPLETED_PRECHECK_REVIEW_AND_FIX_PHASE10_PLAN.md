# PRECHECK Review And Fix Phase 10 Plan

## 目标

- 对当前 `PreCheck` 与统一检索增强链路做一次面向真实缺陷的代码审阅。
- 找出高价值问题并直接修复，避免 reviewer 解释增强之后留下行为不一致或边界缺陷。
- 保持当前外部 gRPC 契约不变。

## 执行步骤

1. 审阅近期改动与关键热路径：
   - `internal/app/usecase/precheck*.go`
   - `internal/app/usecase/memory_query.go`
   - `internal/logic/processor/precheck_memory_reviewer.go`
   - 相关测试与 README / prompt
2. 识别真实问题：
   - 优先看排序/候选保留逻辑
   - 再看 reviewer 载荷一致性
   - 再看降级和边界条件
3. 直接修复发现的问题：
   - 代码修改
   - 必要测试补充
   - 文档同步
4. 验证与收口：
   - 运行仓库要求的最小测试
   - 运行 `go test ./... -count=1`
   - 运行 `.\make.ps1 build`

## 技术取舍

- 本阶段不新增功能面，只修复通过审阅确认存在的问题。
- 优先修复会影响候选正确性、排序稳定性、reviewer 判断质量的问题。
- 若未发现需要动手的真实问题，也要明确审阅结论和剩余风险。

## 验收标准

- 至少完成一次有依据的代码审阅。
- 若发现问题，则问题已修复并有测试覆盖。
- 若未发现问题，则给出明确结论且验证通过。
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `.\make.ps1 build`
