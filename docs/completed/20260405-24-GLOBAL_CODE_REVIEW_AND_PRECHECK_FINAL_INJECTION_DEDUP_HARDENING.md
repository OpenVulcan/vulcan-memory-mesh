# 任务目标

本轮任务聚焦全局代码审核中发现的 pre-check 最终注入重复问题。当前流程只会按 `source_turn_id` 去重第二层 reviewer 采纳的候选，但不同 memory 行若最终注入文本等价，仍会一起进入最终上下文。这会增加 token 消耗、放大重复暗示，并降低注入内容的信噪比。需要在不改变 reviewer 选择与生命周期写回语义的前提下，仅对最终注入阶段增加等价文本去重。

# 详细执行步骤

1. 梳理 pre-check 从 reviewer 采纳、turn 去重、生命周期写回到最终上下文组装的执行顺序，确认文本去重应该落在最终注入阶段而不是 reviewer 或 adoption 阶段。
2. 在 `internal/app/usecase/precheck.go` 中引入最终注入文本去重逻辑，复用现有的 `equivalentPreCheckMemoryText` 语义，保证只消除等价内容，不改写选中顺序。
3. 保持 `ApplyMemoryAdoption` 仍对 reviewer 最终选中的全部 memory id 生效，避免因注入去重改变现有生命周期统计与提级语义。
4. 补充 pre-check 回归测试，覆盖“不同 memory 行、不同 turn、但最终注入文本等价”时只保留一条注入内容的场景。
5. 运行定向测试、仓库规定的最小测试集、`go test ./...` 与 `go vet ./...`，确认修复不影响现有行为边界。

# 技术选型与实现约束

- 文本去重只放在 `finalizePreCheck` 阶段，避免 reviewer 选择排序、候选解释、生命周期 adoption 被意外改写。
- 去重规则复用现有 `equivalentPreCheckMemoryText`，只把大小写、空白等格式差异视为等价，不扩大到模糊语义匹配。
- 保留 reviewer 最终选中顺序中的第一条等价文本作为注入来源，后续重复项直接跳过，确保行为稳定且最小干扰。
- 新增与修改代码必须继续满足仓库的中英文双语注释规范。

# 验收标准

- 当两条或多条已采纳候选最终渲染出的注入文本等价时，最终 `ContextText` 与 `ContextItems` 中只出现一次。
- `ApplyMemoryAdoption` 仍对 reviewer 最终选中的全部 memory id 生效，不因最终注入去重而丢失生命周期更新。
- pre-check 现有的按 `source_turn_id` 去重行为保持不变，仅新增最终注入文本级别的补充去重。
- 定向测试、最小测试集、`go test ./...` 与 `go vet ./...` 全部通过。

---

# 执行变更总结

## 1. 核心修复与调整概述

- 在 pre-check 最终注入阶段新增等价文本去重，避免不同 memory 行在 reviewer 已经采纳后，仍把同一条语义等价文本重复注入上游上下文。
- 保持 reviewer 选择顺序与 adoption 写回不变，只过滤最终注入给 assembler 的重复 `MemoryHit.Text`，做到低干扰收敛。
- 新增穿透 `Execute` 的回归测试，验证“最终注入去重”与“全部 memory id 仍参与 adoption”可以同时成立。

## 2. 📂文件变更清单

### 新增文件

- `docs/completed/20260405-24-GLOBAL_CODE_REVIEW_AND_PRECHECK_FINAL_INJECTION_DEDUP_HARDENING.md`

### 修改文件

- `internal/app/usecase/precheck.go`
- `internal/app/usecase/precheck_test.go`

### 删除文件

- 无

## 3. 💻关键代码调整详情

- 在 `finalizePreCheck` 中引入 `appendUniquePreCheckMemoryHit`，基于现有 `equivalentPreCheckMemoryText` 规则对最终注入的 `MemoryHit` 做等价文本去重。
- 去重只发生在 assembler 前的最终组装口，不改变第二层 reviewer 的候选选择，也不改变 `ApplyMemoryAdoption` 对全部选中 memory id 的生命周期写回。
- 新增 `TestPreCheckExecuteDeduplicatesEquivalentFinalInjectionText`，覆盖两条不同 memory 行、不同 source turn、但最终注入文本等价时：
  - assembler 只接收到一条 hit
  - adoption 仍保留全部 reviewer 选中的 memory id

## 4. ⚠️遗留问题与注意事项

- 本轮去重仅覆盖“文本等价”的重复注入，不做模糊语义聚类，避免把本应并存的近义信息误合并。
- 当前保留的是 reviewer 最终顺序中的第一条等价文本；如果未来要改为“同文本保留更高分或更丰富 metadata”，应单独评估是否会改变解释稳定性。
- 已完成验证：
  - `go test ./internal/app/usecase -run "Test(PreCheckExecuteDeduplicatesEquivalentFinalInjectionText|BuildPreCheckMemoryTextDeduplicatesFormattingVariants|PreCheckExecute|PreCheckSearchCandidates)"`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `go test ./...`
  - `go vet ./...`
