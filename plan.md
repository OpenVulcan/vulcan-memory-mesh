# VMM Async PostAction + Turn-Centric PreCheck Plan

## Goal

This document fixes the full target workflow in-repo so implementation progress does not depend on transient chat context.

本文用于把当前目标工作流固化到仓库内，避免后续实现再次依赖临时对话上下文。

The target state is:

- `PostAction` returns success after durable turn persistence and queueing, without waiting for LLM extraction
- background workers still execute per-turn extraction and unified-memory write-back asynchronously
- `PreCheck` no longer feeds session memory rows into the first-stage LLM
- `PreCheck` first reads the most recent session turns under a turn-count limit and token budget
- extracted turns contribute `details`, while pending turns contribute dehydrated raw-turn content
- the first-stage LLM outputs multiple vector-search query sentences instead of only loose keywords
- those query sentences are batch-embedded and used for unified memory retrieval
- the second-stage LLM receives numbered retrieved candidates and returns the chosen numbers in priority order
- only the finally chosen memory rows get lifecycle hit/adoption write-back

目标状态是：

- `PostAction` 在 turn 稳定落库并成功入队后立即返回，不等待 LLM 提炼完成
- 后台工作器继续异步执行逐轮提炼和统一记忆回写
- `PreCheck` 第一层不再直接把 session 记忆行喂给 LLM
- `PreCheck` 先在“最近 N 轮 + token 预算”窗口内读取 session 历史 turn
- 已提炼 turn 传 `details`，未提炼 turn 传脱水后的原始 turn 内容
- 第一层 LLM 输出多条“向量检索语句”，而不是只有宽泛关键词
- 这些检索语句进行批量向量化，并据此查询统一记忆库
- 第二层 LLM 接收带编号的候选记忆，并按优先顺序返回选中的编号
- 只有最终被选中的记忆才会写回命中/采纳生命周期

## Constraints

- Keep the OSS-local architecture clean: `adapters -> app -> logic/domain`
- Do not reintroduce SaaS-only runtime paths
- Do not reintroduce Postgres-specific storage
- Keep new code under the repo bilingual-comment convention
- Keep the SQLite mainline as the production-complete path
- Preserve current gRPC contracts unless a concrete external adjustment is necessary

约束：

- 保持 OSS 本地版依赖方向清晰：`adapters -> app -> logic/domain`
- 不重新引入 SaaS 专用运行时路径
- 不重新引入 Postgres 存储
- 新增代码必须遵守仓库双语注释规范
- 继续以 SQLite 主线作为生产完整实现
- 除非确有必要，不随意破坏当前 gRPC 外部契约

## Required Workflow

### 1. PostAction

1. validate request
2. run noise gate when applicable
3. append one canonical turn row
4. enqueue the session for background extraction
5. return success immediately

### 2. Background Turn Extraction

1. load pending turns for the queued session
2. process pending turns in order
3. for each pending turn:
   - load reference turns and active memory anchors
   - load recent direct-write exclusion rows
   - run `analyze_turn`
   - review profile nodes
   - write vectors
   - apply turn analysis
   - advance exclusion window

### 3. PreCheck Stage 1

1. load recent session turns under:
   - recent-turn count limit
   - total token budget
2. build one mixed turn window:
   - extracted turn -> `details`
   - pending turn -> dehydrated raw turn JSON
3. send only:
   - mixed turn window
   - current user question
4. stage-1 LLM returns:
   - `need_memory`
   - `queries[]`
   - `reason`

### 4. Unified Recall

1. convert `queries[]` into grouped search items
2. batch-embed all queries
3. search unified memory vectors
4. merge and de-duplicate memory hits
5. assign stable candidate numbers in final review order

### 5. PreCheck Stage 2

1. send current user question
2. send stage-1 `reason` and `queries[]`
3. send numbered candidate list
4. stage-2 LLM returns:
   - `selected_candidate_numbers[]`
   - `reason`
5. map chosen numbers back to memory ids
6. write lifecycle adoption only for mapped ids

### 6. Final Injection

1. keep persona/project context assembly
2. inject only the adopted memory texts
3. preserve chosen priority order

## Current Gaps

Before this round of implementation, the codebase still had these mismatches:

1. `PostAction` was synchronous and blocked on turn analysis.
2. `PreCheck` stage 1 consumed extracted history snippets only, not a mixed refined/raw recent-turn window.
3. `PreCheck` stage 1 produced keyword-style search terms rather than richer vector query sentences.
4. `PreCheck` stage 2 selected memory ids directly instead of reviewing numbered candidates.
5. The plan document still described the previously completed synchronous path and had become stale.

在本轮实现开始前，代码里仍有这些偏差：

1. `PostAction` 仍然是同步提炼，会阻塞前端。
2. `PreCheck` 第一层只吃“已提炼历史摘要”，没有实现 refined/raw 混合 turn 窗口。
3. `PreCheck` 第一层输出仍偏向关键词，而不是更完整的向量检索语句。
4. `PreCheck` 第二层仍直接选择 memory id，而不是按编号候选做采纳。
5. `plan.md` 仍描述旧的同步主链，已经不再准确。

## Implementation Plan

### Phase 1. Re-baseline the plan document

1. Rewrite `plan.md` to match the async post-action plus turn-centric pre-check workflow.
2. Keep a working log here as each phase completes.

### Phase 2. Restore async PostAction mainline

1. Change `PostAction.Execute` to:
   - persist the turn
   - enqueue the session
   - return success immediately
2. Keep background worker startup/shutdown in the runtime path.
3. Reuse queued processing for runtime, but make it process pending turns asynchronously with the single-turn analyzer.
4. Preserve the legacy batch helper path only as compatibility/test support where still needed.

### Phase 3. Add recent mixed-turn read path for PreCheck

1. Add one relational-store method that loads the latest session turns regardless of extracted status.
2. Keep returned order stable from oldest to newest after the final window is chosen.
3. Reuse stored budgets:
   - extracted turn -> `details_budget`
   - pending turn -> `dehydrated_budget`
4. Build helpers that trim the recent-turn window by:
   - max turn count
   - max total input tokens

### Phase 4. Redesign stage-1 pre-check intent extraction

1. Replace the old history-snippet input with a structured recent-turn window.
2. Update the stage-1 domain model and prompt renderer.
3. Update the prompt so the model returns:
   - `need_memory`
   - `queries[]`
   - `reason`
4. Keep backward-tolerant parsing where inexpensive, but make the new contract primary.

### Phase 5. Redesign stage-2 numbered candidate review

1. Replace the old direct-memory-id selection contract.
2. Build one numbered candidate list after vector recall and dedupe.
3. Update the stage-2 prompt so the model returns:
   - `selected_candidate_numbers[]`
   - `reason`
4. Map candidate numbers back to memory ids inside the use case.
5. Keep final adopted order consistent with the returned number order.

### Phase 6. Rewire PreCheck use case

1. Load persona as before.
2. Load recent mixed turn window from storage.
3. Run stage 1 on recent turns + current user question.
4. Stop early when `need_memory=false`.
5. Build grouped memory query JSON from `queries[]`.
6. Reuse unified memory search for batch embedding and recall.
7. Number final candidates and run stage 2.
8. Apply lifecycle adoption only for selected memory ids.
9. Assemble final injection text and items.

### Phase 7. Synchronize runtime wiring and docs

1. Update application composition if config or processor wiring changes.
2. Update prompt files for all shipped model directories.
3. Sync:
   - `README.md`
   - `docs/grpc-integration-guide_CN.md`
   - `docs/hierarchy-grpc-design_CN.md`
   - `docs/post-action-guide_CN.md`
   - any other touched contract docs
4. Fix stale comments that still describe synchronous post-action or old pre-check semantics.

### Phase 8. Verification

At minimum run:

- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`

Before closing the task also run:

- `go test ./...`

If any runtime/build layout changes are introduced, also run:

- `.\make.ps1 build`
  or
- `.\make.bat build`

## Working Log

- 2026-04-02: rewrote the plan document to match the async post-action plus turn-centric pre-check workflow
- 2026-04-02: restored async `PostAction` mainline so accepted requests return after durable turn persistence and queueing
- 2026-04-02: rewired `PreCheck` stage 1 to read recent mixed turn windows and emit multiple search-query sentences
- 2026-04-02: rewired `PreCheck` stage 2 to review numbered memory candidates and map chosen numbers back to memory ids
- 2026-04-02: synced prompt contracts, gRPC comments, README, and Chinese integration/design guides with the new async + turn-centric flow
- 2026-04-02: focused tests and full `go test ./...` passed after the workflow change
