# Vector Log Redaction Plan

## 任务目标 / Goal

- 审查当前项目中所有与向量写入、向量检索、向量相关 payload 诊断有关的日志输出。
- 确认是否真实存在“1024 维 embedding 向量被直接写入日志”的问题，而不是误把普通调试正文当成向量日志。
- 对真实存在的问题进行修复，使日志不再输出高维向量内容，并在需要时给出固定英文提示说明已过滤。

## 执行步骤 / Steps

1. 扫描当前代码库中与向量、embedding、memory vector、vector upsert/search/delete 相关的日志点。
2. 逐条判断：
   - 是否真实写入了完整文本内容
   - 是否只是计数、长度、摘要或 id 级别的安全诊断
3. 只对真实存在风险的日志点做定点修复：
   - 不误伤正常调试正文
   - 仅移除高维向量数组
   - 必要时追加固定英文提示，说明向量已过滤
4. 补充或更新测试，锁定日志输出行为。
5. 运行规定测试与构建。
6. 完成后将计划文件改名为 `COMPLETED_` 前缀。

## 技术原则 / Technical Principles

- 优先做“真实问题修复”，不因为外观相似就扩大改动范围。
- 调试日志仍应保留对排障真正有价值的文本与结构信息。
- 高维向量数组本身没有人工排障价值，应从日志载荷中剥离。
- 尽量复用现有日志策略，避免引入分散的特判逻辑。

## 实际判断 / Actual Findings

1. 初步扫描后，`PreCheck`、`PostAction` 收据、`memory query` 降级日志虽然会在调试开关下输出正文，但它们输出的是请求文本或上下文，不是 1024 维向量数组。
2. 真实存在的问题位于 `internal/app/usecase/postaction.go` 的 `logPostActionAnalysisResult(...)`：
   - 该日志在 `PayloadDebugEnabled()` 为 `true` 时会直接输出 `analysis_json`
   - `analysis_json` 在 `persistMemoryNodeVectors(...)` 执行后包含 `analysis.MemoryNodes[].Vector`
   - 因此会把完整 embedding 维度写入日志，形成“1024 行向量输出”的实际问题
3. 结论：
   - “所有调试正文都该裁短”的判断不准确
   - “post-action analysis debug JSON 会带出高维向量数组”的判断真实存在，且需要修复

## 实际修改 / Actual Changes

1. 在 `internal/app/usecase/postaction.go` 新增 `redactTurnAnalysisVectorsForLog(...)`
   - 复制 `logicdomain.TurnAnalysis`
   - 仅清空 `MemoryNodes[].Vector`
   - 保留 `Details`、`Abstract`、`Details`、画像内容等原有调试正文
2. `logPostActionAnalysisResult(...)` 现在会：
   - 在 debug 模式下继续输出 `analysis_json`
   - 但其中已不再包含高维向量数组
   - 额外输出固定英文提示：`embedding vectors omitted from analysis_json`
   - 额外输出被过滤的节点数量：`vector_payload_redacted_nodes`
3. `internal/app/usecase/postaction_test.go` 已同步更新：
   - 继续断言 debug 模式下存在完整 `analysis_json`
   - 继续断言派生文本仍可用于排障
   - 新增断言：日志中不再出现 `Vector` 数组与具体维度值
   - 新增断言：固定英文提示和过滤计数存在

## 验证结果 / Verification

- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `.\make.ps1 build`

以上验证均已通过。

## 验收标准 / Acceptance Criteria

- 已审查当前与向量相关的日志点，并记录真实判断。
- 真实存在的高维向量日志已修复。
- 修复后的日志不再输出完整 embedding 维度内容，并会附带固定英文过滤提示。
- 相关测试通过。
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `.\make.ps1 build`
