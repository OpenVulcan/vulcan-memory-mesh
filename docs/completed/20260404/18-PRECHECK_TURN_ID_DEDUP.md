# 任务目标

修复 precheck / 最终记忆返回中同一 `turn_id` 被重复返回的问题，确保当多条候选来自同一来源对话时，最终返回不会把同一个 `turn_id` 的内容重复交给上游 AI。

# 详细执行步骤

1. 梳理当前记忆候选从统一检索、precheck 候选筛选、评审采纳到最终返回的完整链路。
2. 定位重复项产生的位置，确认是：
   - 同一 `turn_id` 下存在多条不同 `memory_id`；
   - 同一记忆通过不同检索方式重复命中；
   - 最终返回阶段未做基于 `turn_id` 的聚合去重。
3. 设计最终去重策略，优先保证：
   - 同一 `turn_id` 最终只返回一条；
   - 保留分数更高、信息更完整的一条；
   - 没有 `turn_id` 的项不被误删。
4. 在最合适的边界实现去重逻辑，并补充双语注释说明为什么要在该层去重。
5. 为同一 `turn_id` 多候选场景补充回归测试，验证最终返回与 gRPC 层输出都不再重复。
6. 执行相关测试并在文末写入执行变更总结，然后归档计划文件。

# 技术选型

- 优先在最终返回前的聚合边界处理，避免过早去重影响 review 阶段可见证据。
- 以 `turn_id` 为主键去重；若 `turn_id = 0`，继续按既有规则保留。
- 当同一 `turn_id` 存在多条候选时，优先保留分数更高且文本更完整的项。

# 验收标准

- 同一 `turn_id` 的多条候选不会同时出现在最终返回中。
- `turn_id = 0` 的无来源项不会被错误合并。
- 新增测试覆盖同一 `turn_id` 的重复命中场景。
- 计划文件补齐执行变更总结并完成归档。

## 执行变更总结

### 1. 核心修复与调整概述

- 已确认重复问题不是检索层把同一 `memory_id` 重复返回，而是第二层 reviewer 可能同时选中了多个来自同一 `source_turn_id` 的不同 memory 候选。
- 已在“review 选中之后、生命周期写回之前”新增最终去重逻辑：同一 `turn_id` 只保留一个代表候选，再进入写回与最终注入。
- 该边界可以保证 reviewer 仍然能看到全部候选证据，但最终返回给上游 AI 的结果不会再为同一段对话返回多条记忆摘要。

### 2. 📂 文件变更清单

- 新增：`docs/plan/20260404-18-PRECHECK_TURN_ID_DEDUP.md`
- 修改：`internal/app/usecase/precheck.go`
- 修改：`internal/app/usecase/precheck_test.go`
- 删除：无

### 3. 💻 关键代码调整详情

- 在 `internal/app/usecase/precheck.go` 中，`reviewMemoryCandidates` 返回后新增 `deduplicateSelectedCandidatesByTurnID`。
- 去重规则：
  - `SourceTurnID > 0` 时按 `turn_id` 去重；
  - `SourceTurnID = 0` 的项保持原样，不做误合并；
  - 当同一 `turn_id` 出现多条候选时，沿用既有 `shouldPreferIncomingPreCheckCandidate` 规则选择信息更强的代表项。
- 由于去重发生在 lifecycle write-back 之前，所以 `ApplyMemoryAdoption` 也不会再对同一 `turn_id` 的多条候选重复写回。
- 在 `internal/app/usecase/precheck_test.go` 中补充回归测试，覆盖 reviewer 同时选中两个相同 `turn_id` 候选的场景。

### 4. ⚠️ 遗留问题与注意事项

- 当前去重主键是 `turn_id`，这符合“上游用 `turn_id` 继续追详情”的使用模式；如果未来需要保留同一 turn 内多个不同主题摘要，需要重新定义更细粒度的返回契约。
- 本次未改动检索阶段的候选数量与排序，只约束最终选中结果。
- 已执行 `go test ./internal/app/usecase -run "TestPreCheckExecuteDeduplicatesSelectedCandidatesBySourceTurnID|TestPreCheckExecuteUsesMixedRecentTurnsAndAdoptsSelectedCandidates"` 与 `go test ./...`，结果全部通过。
