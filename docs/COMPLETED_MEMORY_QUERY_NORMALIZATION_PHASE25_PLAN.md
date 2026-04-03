# MEMORY QUERY NORMALIZATION PHASE25 PLAN

## 任务目标

- 审阅并修复通用 `MemoryQuery` 输入归一逻辑与 `PreCheck` 已有归一策略之间的偏差。
- 找出一个真实且可复现的问题，并完成修复、测试补强与闭环验证。
- 保持现有对外 gRPC 契约不变，只修正内部实现的一致性和检索稳定性问题。

## 执行步骤

1. 审阅 `parseMemoryQueryJSON(...)`、`buildMemorySearchText(...)`、`buildMemoryLexicalQuery(...)` 以及相关测试。
2. 确认通用 MemoryQuery 是否仍把内部空白差异直接透传到 embedding 与 lexical 检索，导致格式差异被误当成不同 query。
3. 对问题做最小化修复，并补充定向测试。
4. 运行仓库要求的测试与构建命令。
5. 对照计划逐项复核，并在完成后将文件重命名为 `COMPLETED_` 前缀。

## 技术约束

- 不修改对外 proto 和 RPC 字段。
- 保持当前仓库分层依赖方向不变。
- 新增代码与测试继续遵守双语注释规范。

## 验收标准

- 至少修复一个真实问题，而不是样式整理。
- 新增测试能够覆盖该问题。
- 通过以下验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
