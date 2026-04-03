# LanceDB SQLite Risk Validation And Fix Plan

## 任务目标 / Goal

- 核实用户提供的两类风险判断是否准确：
  - `internal/adapters/outbound/vldb_lancedb/store.go` 中忽略 `strconv` 错误是否会导致真实风险。
  - `internal/adapters/outbound/vldb_sqlite/store.go` 中 `fmt.Sprintf` 构造 SQL 是否属于需要修复的真实风险。
- Validate whether the two reported risks are accurate, and only fix the findings that are confirmed by code-level verification.

## 执行步骤 / Steps

1. 读取相关代码与测试，定位用户给出的具体位置和当前实现上下文。
2. 结合 Go 后端安全最佳实践，判断风险描述是否成立，以及影响面是否真实。
3. 对确认存在的问题实施最小、可靠且不改变既有语义的修复；对不成立的问题保留结论并说明原因。
4. 补充回归测试和分析报告，记录“是否存在、是否需要修复、如何修复”。
5. 运行规定测试与构建，确认无回归后提交。

## 技术原则 / Technical Principles

- 只修复经验证的真实问题，不为“看起来像风险”但实际不可触发的情况做装饰性改动。
- 优先采用参数化查询与显式错误处理等 Go 后端安全基线方案。
- 保持现有 OSS 运行时架构、依赖方向和存储语义不变。

## 验收标准 / Acceptance Criteria

- 为两类风险都形成明确结论：`存在/不存在`、`是否需要修复`、`理由`。
- 若确认存在问题，完成代码修复、测试补齐和结果文档化。
- 完成后将计划文件重命名为 `COMPLETED_` 前缀。

## 实际结果 / Actual Result

- 已完成两类风险的代码级核实，并将详细结论写入 `docs/LANCEDB_SQLITE_RISK_VALIDATION_AND_FIX_CN.md`。
- 结论 1：
  - `vldb_lancedb/store.go` 中忽略 `strconv` / `json.Number` 解析错误的问题真实存在。
  - 更准确地说，它属于错误处理与数据完整性风险，而不是高危可利用漏洞。
  - 已修复为显式报错，并补充 `TestSearchRejectsMalformedNumericFields`。
- 结论 2：
  - `vldb_sqlite/store.go` 中被报告为“SQL 注入风险”的位置，按当前代码形态不构成真实可利用注入漏洞，因为参与拼接的都是 `uint64` 与内部常量。
  - 但参数化仍然是更好的安全编码基线，因此本次对低成本且收益明确的点完成了硬化：
    - `loadRenderedProfileByTarget(...)` 改为 `?` 占位符和 typed params。
    - 项目画像节点删除/统计 SQL 改为返回 `sql + params`，调用方通过 `exec(..., params...)` / `countRows(..., params...)` 绑定参数。
  - 已补充 `TestStoreLoadRenderedProfileUsesTypedSQLiteParams` 与 `TestBuildProjectProfileNodesDeleteSQLUsesTypedParams`。
- 已完成验证：
  - `go test ./internal/adapters/outbound/vldb_lancedb ./internal/adapters/outbound/vldb_sqlite -count=1`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
