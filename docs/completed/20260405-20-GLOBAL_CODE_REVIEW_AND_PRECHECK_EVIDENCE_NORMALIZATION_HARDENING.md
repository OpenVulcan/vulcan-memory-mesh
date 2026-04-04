# 全局代码审核与 PreCheck 证据归一一致性加固执行计划

## 1. 任务目标

本阶段在前序多轮稳定性修复基础上，继续执行新一轮全局代码审核，重点检查 pre-check 候选合并链路与统一记忆查询链路之间是否仍存在 reviewer 证据语义不一致、重复解释、排序漂移或低噪声但会持续污染输出质量的边界问题。

具体目标如下：

1. 复核 `pre-check` 候选合并与统一记忆查询阶段的 `matched context evidence` 表面规范是否保持一致。
2. 如果确认存在真实风险，则以最小改动面、最高稳定性的方式完成修复，避免引入额外运行时开销或行为干扰。
3. 补齐对应测试，确保后续重构不会再次把 context evidence 退回到字面值级去重。
4. 完成回归验证、自检、执行变更总结、计划归档、提交与推送，保持工程闭环。

## 2. 审核范围

### 2.1 本轮重点覆盖

1. `internal/app/usecase/precheck_candidate_merge.go`
2. `internal/app/usecase/precheck.go`
3. `internal/app/usecase/memory_query.go`
4. `internal/app/usecase/precheck_test.go`
5. 与 `matched context evidence` 归一和 reviewer 说明稳定性相关的测试

### 2.2 本轮不主动扩展

1. 不引入计划外的大型架构重构。
2. 不修改对外接口定义，除非确认存在阻断级兼容性错误且存在低风险最优解。
3. 不做与本轮真实风险无关的风格性清理。

## 3. 执行策略

1. 先确认 pre-check 候选合并阶段是否仍使用字面值去重，而统一检索链路是否已经拥有更严格的 context evidence 规范化逻辑。
2. 若确认存在语义等价标签重复展示的风险，则优先复用现有共享规范化 helper，避免复制第二套规则。
3. 修复必须同时满足：
   - 不改变已有合法证据的保留能力；
   - 不增加额外 provider 往返、数据库访问或后台噪声；
   - 输出仍保持稳定、可排序、可测试；
   - 后续维护者可以从共享 helper 明确理解规范来源。

## 4. 详细执行步骤

1. 确认仓库基线并建立本阶段计划文件。
2. 深审 pre-check 候选合并与统一记忆查询的 context evidence 去重实现，定位真实风险。
3. 采用最优方案修复实现，并补齐必要测试。
4. 运行定向测试、最少必测集、全量测试与静态检查。
5. 对照计划逐项自检，补写执行变更总结。
6. 将计划迁移至 `docs/completed/`，完成 `git commit` 与 `git push`。

## 5. 验收标准

1. 必须明确说明本轮定位到的真实问题与修复原因。
2. 修复后，pre-check 合并出的 `MatchedContextValues` 不得因历史格式漂移而重复展示同一语义标签。
3. 修复不得引入新的排序漂移、说明缺失或明显额外开销。
4. 至少完成以下验证：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`

## 6. 风险与注意事项

1. 本轮仍以功能稳定、效率高、性能强、运行时无干扰为最高优先级。
2. 共享 helper 的复用必须确保输出契约不发生非必要变化，避免影响已有 reviewer 文案与断言。
3. 所有新增或修改代码必须继续遵守仓库双语注释、测试约束、计划归档和中文提交规范。

## 执行变更总结

### 1. 核心修复与调整概述

1. 本轮全局审核确认了一个真实的 reviewer 证据一致性漏洞：`pre-check` 重复候选合并阶段对 `MatchedContextValues` 仍使用字面值去重，而统一记忆查询链路已经在查询期把等价的 context evidence 标签归一到共享规范表面。
2. 这会导致同一条 memory 被多个 query group 命中时，只要历史标签存在大小写、下划线、连字符或空白差异，reviewer 仍可能看到两条语义完全相同的 matched context 证据，污染候选说明并放大“好像证据更多”的错觉。
3. 本轮修复直接复用现有共享 helper `normalizeMemoryContextEvidenceLabel`，把 pre-check 合并阶段收口到与查询期一致的 canonical `key=value` 表面；同时新增回归测试验证格式变体会被折叠成一条规范证据，并继续保留更强的 matched support 与 delta。

### 2. 📂文件变更清单

1. 新增：`docs/plan/20260405-20-GLOBAL_CODE_REVIEW_AND_PRECHECK_EVIDENCE_NORMALIZATION_HARDENING.md`
2. 修改：`internal/app/usecase/precheck_candidate_merge.go`
3. 修改：`internal/app/usecase/precheck_test.go`

### 3. 💻关键代码调整详情

1. 调整 `appendSortedUniquePreCheckValues`，不再按 `strings.TrimSpace` 后的原始字面值去重，而是先通过 `normalizeMemoryContextEvidenceLabel` 归一出共享规范标签，再以规范标签去重和排序。
2. 这样既能折叠 `deployment_mode=LOCAL_OSS`、`deployment mode= local-oss ` 这类历史格式漂移标签，也能保证最终 reviewer 看到的是稳定的 canonical `deployment_mode=local oss`。
3. 新增 `TestPreCheckExecuteNormalizesEquivalentMatchedEvidenceLabels`，覆盖“同一 memory 在重复 query group 命中时，等价 context evidence 标签只保留一条规范值”的回归场景，并断言更强的 matched 支持计数和 score delta 仍会保留下来。

### 4. ⚠️遗留问题与注意事项

1. 本轮没有额外引入新的 helper 或第二套规范逻辑，而是刻意复用统一记忆查询链路已经在用的共享 canonical 规则，减少未来再次漂移的维护风险。
2. 当前修复重点解决的是 reviewer 证据去重一致性；它不会改变合法非等价 context evidence 的保留数量，也不会改变 pre-check 候选排序策略。
3. 已完成验证：
   - `go test ./internal/app/usecase -run "TestPreCheckExecute(MergesEvidenceAcrossRepeatedMemoryHits|NormalizesEquivalentMatchedEvidenceLabels|KeepsStrongerMatchedEvidenceFromSecondary)"`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`
