# gRPC Review Validation And Fix Plan

## 任务目标 / Goal

- 逐条核实用户提供的审阅结论与当前代码实际是否匹配。
- Validate each reported finding against the actual code and only fix issues that are confirmed to be real.

## 执行步骤 / Steps

1. 审查报告中 11 个问题对应的真实代码位置，确认是否属于：
   - 真实缺陷 / 风险
   - 合理的当前设计，不应视为问题
   - 更像风格建议或架构建议，而不是缺陷
2. 对真实问题给出最小、可靠、不会破坏现有 OSS 运行时契约的修复。
3. 把每一条问题的“是否存在 / 是否需要修复 / 原因 / 实际修改”写入中文报告。
4. 运行仓库要求的测试与构建，确认无回归后提交。

## 技术原则 / Technical Principles

- 只修复经代码验证能真实成立的问题，不做基于误读的“装饰性改动”。
- Do not force major architectural changes such as global rate limiting or secret-management rearchitecture without explicit confirmation.
- 保持当前 gRPC-only、SQLite + LanceDB 的 OSS 运行时架构不变。

## 验收标准 / Acceptance Criteria

- 对报告中的每一项形成明确结论：存在 / 不存在 / 不建议按原建议修改。
- 若发现真实问题，完成代码修复、回归测试、文档记录和提交。
- 完成后将计划文件重命名为 `COMPLETED_` 前缀。

## 实际结果 / Actual Result

- 已对审阅内容中的 11 条问题逐条做代码级核实，并把结论写入 `docs/GRPC_REVIEW_VALIDATION_AND_FIX_CN.md`。
- 本轮判断结果：
  - 真实存在并已修复：1 条
  - 属于架构建议 / 风格建议，不是当前缺陷：7 条
  - 属于更大范围的安全能力或部署策略议题，本轮不应自动修改：3 条
- 本轮真实修复的问题：
  - `PostAction` 收据日志仍写入了基于正文的稳定摘要字段，能泄露跨日志关联性与可猜测性。
  - 已改为只记录存在性与条目计数，不再记录 `user_content_sha256 / assistant_content_sha256 / timeline_sha256`。
- 实际修改文件：
  - `internal/adapters/inbound/grpcapi/server.go`
  - `internal/adapters/inbound/grpcapi/server_test.go`
  - `docs/GRPC_REVIEW_VALIDATION_AND_FIX_CN.md`
- 已完成验证：
  - `go test ./internal/adapters/inbound/grpcapi -count=1`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
