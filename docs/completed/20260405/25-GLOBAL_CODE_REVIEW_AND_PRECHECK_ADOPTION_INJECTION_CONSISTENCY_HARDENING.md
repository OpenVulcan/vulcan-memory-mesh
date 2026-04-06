# 任务目标

本轮任务聚焦 pre-check 在 reviewer 采纳后的一致性缺口。当前流程会先按 `source_turn_id` 去重，再执行 `ApplyMemoryAdoption`。这会导致“同一 turn 内被 reviewer 选中的多条不同 memory”只有一条获得生命周期写回，而另一条虽然被 reviewer 明确采纳，却被静默丢弃。需要把“最终注入去重”与“生命周期写回”拆开：注入层继续压缩同 turn 重复摘要，但 adoption 必须忠实覆盖 reviewer 最终选中的全部 memory id。

# 详细执行步骤

1. 梳理 pre-check 从 reviewer 选择、同 turn 去重、adoption 写回到最终注入的顺序，确认当前同 turn 去重提前发生导致生命周期写回失真的位置。
2. 调整 `internal/app/usecase/precheck.go`，把按 `source_turn_id` 的去重从 reviewer 后移到最终注入阶段，只影响 assembler 输入，不再影响 adoption 写回。
3. 保留现有最终注入的“同 turn 去重 + 文本等价去重”收敛策略，确保上游上下文依然低噪声。
4. 更新并补充 pre-check 回归测试，验证：
   - 最终注入仍只保留同一来源 turn 的一个代表项；
   - 生命周期写回会覆盖 reviewer 选中的全部 memory id，而不再被同 turn 去重提前截断。
5. 运行定向测试、仓库规定的最小测试集、`go test ./...` 与 `go vet ./...`，确保修复没有副作用。

# 技术选型与实现约束

- 去重职责下沉到 `finalizePreCheck`，避免 reviewer 选择结果在写回前被展示层逻辑改写。
- adoption 语义以 reviewer 最终选择为准，不附带任何 turn 级展示压缩逻辑。
- 保持同 turn 注入仍优先保留更优代表候选，继续复用现有 `deduplicateSelectedCandidatesByTurnID` 选择策略，避免输出行为回退。
- 新增与修改代码必须继续满足仓库双语注释规范。

# 验收标准

- reviewer 选中同一来源 turn 的多条 memory 时，`ApplyMemoryAdoption` 会收到全部选中的 memory id。
- 最终 `ContextItems` 与 `ContextText` 仍只保留同一来源 turn 的一个代表项，不放大重复摘要。
- 既有最终注入文本级去重与同 turn 去重行为保持稳定，只修正 lifecycle write-back 的覆盖范围。
- 定向测试、最小测试集、`go test ./...` 与 `go vet ./...` 全部通过。

---

# 执行变更总结

## 1. 核心修复与调整概述

- 修正 pre-check 在 reviewer 采纳后先做同 turn 去重、再执行 adoption 写回的一致性问题。
- 把同 turn 去重下沉到最终注入阶段，仅影响 assembler 输入，不再截断 reviewer 选中的 lifecycle write-back 范围。
- 保持最终注入继续低噪声：同 turn 代表项压缩与文本等价去重仍然生效。

## 2. 📂文件变更清单

### 新增文件

- `docs/completed/20260405-25-GLOBAL_CODE_REVIEW_AND_PRECHECK_ADOPTION_INJECTION_CONSISTENCY_HARDENING.md`

### 修改文件

- `internal/app/usecase/precheck.go`
- `internal/app/usecase/precheck_test.go`

### 删除文件

- 无

## 3. 💻关键代码调整详情

- 从 `Execute` 的 reviewer 成功分支中移除 `deduplicateSelectedCandidatesByTurnID`，让 `selectedCandidates` 保持 reviewer 原始选中集合，用于后续 adoption 写回。
- 在 `finalizePreCheck` 开头重新应用 `deduplicateSelectedCandidatesByTurnID`，把同 turn 展示压缩限定在最终注入构建阶段。
- 更新 `TestPreCheckExecuteDeduplicatesSelectedCandidatesBySourceTurnID`：
  - 保持最终 `ContextItems` 仍只保留一个同 turn 代表项；
  - 改为断言 `store.adoptedIDs` 覆盖 reviewer 选中的全部 memory id。

## 4. ⚠️遗留问题与注意事项

- 本轮没有改变 reviewer 选择顺序，也没有扩大最终注入的去重规则，只是把展示去重与 lifecycle 写回职责彻底拆开。
- 当前同 turn 压缩仍优先保留 `deduplicateSelectedCandidatesByTurnID` 选择出的代表候选；若未来要改为“同 turn 但不同摘要允许并存”，需重新评估 prompt 噪声与展示稳定性。
- 已完成验证：
  - `go test ./internal/app/usecase -run "Test(PreCheckExecuteDeduplicatesSelectedCandidatesBySourceTurnID|PreCheckExecuteDeduplicatesEquivalentFinalInjectionText|PreCheckExecute|PreCheckSearchCandidates)"`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `go test ./...`
  - `go vet ./...`
