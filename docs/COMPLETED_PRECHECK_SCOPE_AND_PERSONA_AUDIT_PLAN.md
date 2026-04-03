# PreCheck Scope And Persona Audit Plan

## 任务目标 / Goal

- 审计当前 `PreCheck` 链路的真实行为，确认检索范围如何限定在 `team / space / project / session`。
- 审计当前 `PreCheck` 是否真实启用了 LLM 意图分析，以及分析结果如何参与检索。
- 验证用户反馈的“`PreCheck` 返回画像信息”是否真实存在，并判断这是设计如此还是实现偏差。
- 如果确认存在实现偏差，则完成修复，并同步更新计划中的判断、修改点与验证结果。

## 执行步骤 / Steps

1. 阅读 `grpc` 入站层、`PreCheck` 用例层、画像读取与组装逻辑，梳理真实调用链。
2. 核对 `session / project / team / space` 的作用域来源与检索过滤条件。
3. 核对 `PreCheck` 第一层 LLM 意图提取是否开启、提示词输入是什么、返回字段如何影响检索。
4. 核对 `PreCheck` 最终上下文是否会混入画像信息，以及混入位置和触发条件。
5. 如果问题真实存在且偏离目标行为，则做最小且正确的修复。
6. 补充或更新测试，运行规定测试与构建。
7. 完成后将计划文件改名为 `COMPLETED_` 前缀。

## 技术原则 / Technical Principles

- 先验证真实行为，再判断是否修复，避免把“看起来像问题”的代码误改掉。
- 如果涉及 `PreCheck` 契约或上下文拼装逻辑，必须优先保证语义边界清晰：
  - `PreCheck` 负责“是否检索、检索什么、返回什么检索上下文”
  - 画像接口负责独立的画像读取
- 修复时优先采用最小改动，不破坏现有作用域与检索主链。

## 验收标准 / Acceptance Criteria

- 已说明当前 `PreCheck` 的真实检索范围。
- 已说明当前 `PreCheck` 是否真实启用了 LLM 分析，以及分析字段如何参与检索。
- 已验证“`PreCheck` 返回画像信息”是否真实存在。
- 若问题真实存在，已完成修复并补充测试。
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `.\make.ps1 build`

## 核查结论 / Findings

### 1. `PreCheck` 的真实检索范围

- 问题真实结论：
  - `PreCheck` 不会直接按 `session_id` 检索长期记忆。
  - 当前长期记忆检索范围由服务端已解析出的 `team_id + space_id + project_id` 限定。
  - 同时带 `user_id = 0 OR current_user_id` 过滤，因此会同时召回共享记忆和当前用户私有记忆。
- 代码依据：
  - `internal/app/usecase/memory_query.go`
  - `internal/adapters/outbound/vldb_lancedb/store.go`
  - `internal/adapters/outbound/vldb_sqlite/store.go`

### 2. `PreCheck` 是否真实启用了 LLM 意图分析

- 问题真实结论：
  - 是，真实启用。
  - 第一层 `IntentExtractor` 会读取最近 turn 窗口、当前输入和 context hints，输出 `need_memory / queries / reason`。
  - `need_memory=false` 时不会进入长期记忆检索。
  - `queries` 会被组装成统一记忆查询 JSON，直接驱动后续检索。
- 输入形态真实结论：
  - 最近 3 轮左右窗口来自 `SessionAnalysisHistoryTurns`。
  - 已提炼 turn 优先使用 `details`。
  - 未提炼 turn 回退为脱水后的原始 turn 文本。

### 3. “`PreCheck` 返回画像信息”是否真实存在

- 问题真实结论：
  - 是，用户反馈属实。
  - 修复前 `PreCheck` 会调用 `ProfileUseCase.GetBundle(... SPLIT ...)`，把 TEAM / SPACE / PROJECT / USER 画像 bundle 混入最终 `context_text / context_items`。
  - 这与“画像由独立接口返回”的语义边界冲突，属于实现偏差，不应保留。

## 实际修改 / Actual Changes

### 1. 移除 `PreCheck` 中的画像混入链路

- 修改文件：
  - `internal/app/usecase/precheck.go`
  - `internal/app/app.go`
- 修改方式：
  - 删除 `PreCheck` 对 `ProfileBundle` 的依赖注入与加载流程。
  - 删除 `loadPersonaContext(...)`。
  - `finalizePreCheck(...)` 改为仅基于被采纳记忆生成结果。
  - 当没有任何记忆穿过整条链路时，直接返回空上下文，而不是 persona/profile fallback。
  - fallback summary 也改为只渲染 memory section。

### 2. 同步修正 `PreCheck` 测试语义

- 修改文件：
  - `internal/app/usecase/precheck_test.go`
- 修改方式：
  - 把原先“persona-only fallback”测试改成“empty-context fallback”测试。
  - 新断言：
    - `need_memory=false` 时 `ShouldInject=false`
    - 第二层 reviewer 失败时返回空上下文且 `degraded=true`
    - 短指代追问且未形成稳定 query 时，不触发记忆检索，也不会组装 persona context
  - 移除不再需要的 `stubPreCheckProfiles`
  - 为 assembler stub 增加 `called` 标记，验证无记忆路径不会错误进入组装

### 3. 同步更新文档

- 修改文件：
  - `README.md`
  - `docs/grpc-integration-guide_CN.md`
  - `docs/hierarchy-grpc-design_CN.md`
  - `docs/SERVER_FULL_FLOW_AUDIT_CN.md`
- 修改方式：
  - 明确 `PreCheck` 的真实检索范围
  - 明确第一层 LLM 的真实参与方式
  - 明确 `PreCheck` 不再返回画像 bundle，画像读取继续由独立接口提供

### 4. 验证过程中顺手修正的一处真实日志问题

- 修改文件：
  - `internal/app/usecase/postaction.go`
  - `internal/app/usecase/postaction_test.go`
- 修改方式：
  - 包级验证时发现 `post-action` 的调试 JSON 虽然清空了向量值，但仍保留 `"Vector": null` 字段，测试断言也误用了会命中时间戳的 `0.1` 字符串。
  - 现已改为日志 JSON 完全省略 `Vector` 字段，并保留英文提示 `embedding vectors omitted from analysis_json`。

## 验证结果 / Verification

- 已通过：
  - `go test ./internal/app/usecase -count=1`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
