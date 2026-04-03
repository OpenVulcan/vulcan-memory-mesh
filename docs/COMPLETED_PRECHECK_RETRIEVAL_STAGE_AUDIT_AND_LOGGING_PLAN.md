# PreCheck Retrieval Stage Audit And Logging Plan

## 任务目标 / Goal

- 核实当前 `PreCheck` 是否真实进入向量检索、混合检索，以及各阶段返回记录数是否已被日志覆盖。
- 明确“`candidate_count=0` 是否可能发生在向量命中存在但被后续阈值过滤”的真实链路。
- 若日志确有缺口，补齐 `PreCheck` 检索阶段日志，至少覆盖：
  - 是否进入向量检索
  - 向量检索返回条数
  - lexical / RRF / rerank / MMR 前后条数
  - 即便最终候选为 0，也要能看到最相近记录及其分数

## 执行步骤 / Steps

1. 审计 `PreCheck -> MemoryUseCase.Search(...)` 的真实调用路径，确认向量检索与混合检索的触发条件。
   - 结论：`PreCheck` 在第一层 LLM 产出 `need_memory=true` 后，会真实进入 `MemoryUseCase.Search(...)`。
   - 结论：`MemoryUseCase.Search(...)` 会先做 embedding，再无条件执行 `u.vector.Search(...)`；若启用 hybrid，则再执行 lexical + RRF；若启用 rerank/MMR，再继续后续阶段。
2. 核对当前日志点，判断是否已有“向量返回条数 / 最相似命中 / 阈值过滤前后条数”等关键信息。
   - 结论：修改前仅有 `pre-check memory candidates recalled`，它记录的是 **阈值过滤后的候选数**，无法判断底层是否已有命中。
   - 结论：修改前没有稳定日志能回答：
     - 向量检索返回了多少条
     - 混合检索返回了多少条
     - `candidate_count=0` 是“真没召回”还是“召回后被阈值裁掉”
     - 被阈值裁掉的最相近候选是谁
3. 如果日志缺失，按最小真实改动补齐检索阶段日志，不改变现有检索排序与召回逻辑。
   - 已修改 [internal/app/usecase/memory_query.go](D:/projects/VulcanMemoryMesh/internal/app/usecase/memory_query.go)
     - 新增 `memory search vector stage completed`
     - 新增 `memory search hybrid stage completed`
     - 新增 `memory search rerank stage completed`
     - 新增 `memory search final stage completed`
     - 每阶段都会输出条数以及当前 top hit 摘要
   - 已修改 [internal/app/usecase/precheck.go](D:/projects/VulcanMemoryMesh/internal/app/usecase/precheck.go)
     - 新增 `pre-check raw memory hits returned`
     - 新增 `pre-check memory candidate filtering applied`
     - 即便最终 `candidate_count=0`，也会记录：
       - `raw_hit_count`
       - `below_threshold_count`
       - `best_raw_hit`
       - `best_below_threshold_hit`
4. 补充回归测试，覆盖“最终候选为 0 但底层确实有召回”的可观测性。
   - 已修改 [internal/app/usecase/memory_query_test.go](D:/projects/VulcanMemoryMesh/internal/app/usecase/memory_query_test.go)
     - 新增 `TestMemoryUseCaseSearchLogsRetrievalStageCounts`
   - 已修改 [internal/app/usecase/precheck_test.go](D:/projects/VulcanMemoryMesh/internal/app/usecase/precheck_test.go)
     - 新增 `TestPreCheckExecuteLogsRawRecallAndBestRejectedHit`
5. 更新计划结论并在完成后改名为 `COMPLETED_` 前缀。
   - 已完成。

## 验收标准 / Acceptance Criteria

- 能明确回答：
  - 当前是否一定进入向量检索
  - 当前是否已经有向量/混合检索返回条数日志
  - 当前是否能看到被阈值过滤掉的最相似候选
- 若存在日志缺口，修复后能在日志中看到检索阶段关键数量与 top hit。
- 已通过：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config ./internal/platform/logx -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`

## 实际结论 / Actual Findings

- 真实存在的问题：
  - 是。`PreCheck` 的现有日志无法证明是否进入向量检索，也无法解释 `candidate_count=0` 的真实原因。
- 不存在的问题：
  - 不是“`PreCheck` 根本没走向量检索”。代码路径确认只要 `need_memory=true` 就会进入向量检索。
- 额外判断：
  - 当前代码检索面是 `LanceDB + SQLite(vmm_memory_nodes / vmm_memory_nodes_fts)`。
  - 仓库内不存在名为 `fsb` 的检索读取路径，因此“`fsb` 表里有内容”本身不能证明这些内容已经进入 `PreCheck` 当前检索面。
