# PRECHECK Review Evidence Loop Phase 7 Plan

## 目标

- 增强 `PreCheck` 第二层 `review_precheck_memory` 的输入质量，让 reviewer 能看到检索侧命中的情境证据，而不是只看到摘要文本和基础分数。
- 把查询期 `context-aware scoring` 的关键信号向上透传到 `PreCheck` 候选层，形成更闭环的“为什么这条记忆适合当前请求”链路。
- 保持现有外部 gRPC 契约不变，只增强服务端内部模型输入与本地排序依据。

## 执行步骤

1. 审核当前链路：
   - `internal/app/usecase/memory_query.go`
   - `internal/app/usecase/precheck.go`
   - `internal/logic/domain/precheck.go`
   - `internal/logic/processor/precheck_memory_reviewer.go`
   - `configs/prompts/default/review_precheck_memory.md`
2. 扩展检索命中的证据摘要：
   - 为 query-time 命中的 context evidence 补充可透传字段
   - 保持现有排序与分数语义兼容
3. 扩展 `PreCheck` 候选与 reviewer 输入：
   - 让第二层看到支持/反驳和命中 context 的摘要
   - 保持编号选择协议不变
4. 补文档与测试：
   - 覆盖 evidence 透传和 reviewer 请求体渲染
   - 同步 README 中对 `PreCheck` 第二层的必要说明
5. 验证与收口：
   - 运行仓库要求的最小测试
   - 运行 `go test ./... -count=1`
   - 运行 `.\make.ps1 build`

## 技术取舍

- 这一阶段不扩 proto，不修改对外 `PreCheck` 返回结构。
- 优先透传“已命中的情境证据”，而不是把所有底层 edge 原样暴露给 reviewer。
- 继续使用 deterministic 的本地匹配结果，不增加新的模型调用。

## 验收标准

- query-time context evidence 能透传到 `PreCheckMemoryCandidate`。
- `review_precheck_memory` 能看到与当前 query 直接相关的 support / rebuttal / matched context 摘要。
- 外部 gRPC 契约保持不变。
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `.\make.ps1 build`
