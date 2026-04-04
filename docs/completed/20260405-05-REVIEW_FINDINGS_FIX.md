# 任务目标

修复上一轮未提交代码自审中确认的 3 个真实问题，确保：

1. `WriteMemories` 不再把所有 dropped memory 候选都误判成“复用第一条旧记忆”；
2. direct-write 语义去重目标在 reviewer 与持久化之间发生并发失效时，流程能够安全降级，而不是返回过期/退役记忆或直接报错；
3. direct-write 路径继续兼容 legacy reviewer 仅返回 `accepted_candidate_indexes` / `dropped_candidate_indexes` 的结果格式。

# 详细执行步骤

1. 梳理 `review_postaction_candidates` 当前 memory 输出契约、解析逻辑与 direct-write 决策逻辑，明确需要新增的结构化 dropped 结果格式。
2. 修改统一 reviewer prompt、domain 结构和解析器：
   - 为 dropped memory 候选补充结构化输出；
   - 允许重复候选明确声明“复用哪条旧记忆”；
   - 保持 legacy index-only 输出兼容。
3. 修改 `WriteMemories` 语义去重决策链路：
   - 仅在 reviewer 明确给出 dedupe 目标时复用旧记忆；
   - 其他 dropped 情况继续按“安全降级为新建”处理，避免显式工具写入被静默吞掉。
4. 修改 direct-write dedupe 结果回填逻辑：
   - 重新加载 dedupe 目标时只接受仍然 `active` 且未过期的记忆；
   - 若目标在并发窗口内失效，则退化为新建，不再硬失败。
5. 补充并更新测试：
   - dropped duplicate 明确复用旧记忆；
   - dropped 但未声明 dedupe 目标时不误复用；
   - legacy reviewer 结果仍能正确接纳 direct-write 候选；
   - 并发失效目标会安全降级。
6. 跑最少规定测试与 `go test ./...`，完成自检后写入执行变更总结并归档。

# 技术选型

1. 继续沿用统一 reviewer 模型，不为 `WriteMemories` 引入第二套独立 reviewer 协议。
2. 通过在 memory review 结果中增加结构化 dropped 候选信息，显式区分：
   - “语义重复，应复用旧记忆”
   - “不应保留，但也没有旧记忆可复用”
3. 对并发失效场景采用“安全降级为新建”而不是“报错中断”，优先保证显式工具写入不会因为短时竞争失败。
4. 保持 `post-action` 主链语义不变：
   - dropped memory 候选仍然不会入库；
   - 新增的 dropped 结构化信息主要服务 direct-write 复用判断。

# 验收标准

1. `WriteMemories` 只会在 reviewer 显式指明 dedupe 目标时返回已有记忆引用。
2. dropped 但未声明 dedupe 目标的 direct-write 候选，不会被错误标记为 `Deduped=true`。
3. legacy reviewer 只填旧索引字段时，direct-write 仍能正常保留 accepted 候选。
4. dedupe 目标在并发窗口内退役或过期时，流程会安全降级为新建，不会返回 stale 记忆，也不会直接报错。
5. 相关单测更新通过，且 `go test ./...` 全量通过。

# 执行变更总结

## 1. 核心修复与调整概述

1. 为统一 reviewer 的 memory 结果新增结构化 `dropped_candidates[]`，并支持 `dedupe_memory_id`，显式区分“语义重复应复用旧记忆”和“仅应丢弃但不能复用旧记忆”两种 dropped 语义。
2. 修复 `WriteMemories` 的 direct-write 决策逻辑：
   - 不再把所有 dropped 且存在 similar memory 的候选自动复用第一条旧记忆；
   - 只有 reviewer 明确给出 `dedupe_memory_id` 时才返回已有记忆引用；
   - 其余 dropped 情况统一安全降级为新建。
3. 修复 direct-write dedupe 目标并发失稳问题：
   - dedupe 目标重新加载时只接受仍然 `active` 且未过期的长期记忆；
   - 如果目标在 reviewer 之后已被 supersede / 过期 / 消失，则退化为新建，不再返回 stale 记忆，也不再硬失败。
4. 补齐 legacy reviewer 兼容逻辑：
   - direct-write 现在同时兼容结构化 `accepted_candidates[]` 和旧版 `accepted_candidate_indexes[]`；
   - 同时兼容结构化 `dropped_candidates[]` 和旧版 `dropped_candidate_indexes[]`。
5. 同步更新了三套 reviewer prompt、README、`post-action` 中文指南与相关单元测试，保持契约、实现和文档一致。

## 2. 📂文件变更清单

### 修改

1. `README.md`
2. `configs/prompts/default/review_postaction_candidates.md`
3. `configs/prompts/qwen3.5-base/review_postaction_candidates.md`
4. `configs/prompts/qwen3.5-flash/review_postaction_candidates.md`
5. `docs/post-action-guide_CN.md`
6. `internal/app/usecase/memory_query.go`
7. `internal/app/usecase/memory_query_test.go`
8. `internal/app/usecase/postaction_candidate_review.go`
9. `internal/logic/domain/postaction_review.go`
10. `internal/logic/processor/postaction_candidate_reviewer.go`
11. `internal/logic/processor/postaction_candidate_reviewer_test.go`
12. `docs/plan/20260405-05-REVIEW_FINDINGS_FIX.md`

### 新增

1. 无

### 删除

1. 无

## 3. 💻关键代码调整详情

1. 在 `internal/logic/domain/postaction_review.go` 中新增 `PostActionDroppedMemoryCandidate`，让统一 reviewer 结果可以显式携带 dropped 候选的 dedupe 目标。
2. 在 `internal/logic/processor/postaction_candidate_reviewer.go` 中补齐：
   - `dropped_candidates[]` JSON 解析；
   - 新旧 memory review 结果的双格式兼容归一；
   - dropped 结构化结果到索引集的派生，继续复用既有覆盖率校验。
3. 在 `internal/app/usecase/memory_query.go` 中重写 direct-write 决策合成逻辑：
   - accepted 候选继续走 `supersede_memory_ids`；
   - dropped 候选只有显式 `dedupe_memory_id` 才走复用；
   - dedupe 目标失效时记录告警并回退为新建；
   - 新增 `memoryNodeRecordIsActiveUnexpiredAt`，让 dedupe 结果与热路径召回契约对齐。
4. 在 `internal/app/usecase/postaction_candidate_review.go` 中新增 `validatePostActionDroppedDedupeMemoryID`，确保 dropped 复用目标只能引用该候选真实看到过的 `similar_memories.memory_id`。
5. 在测试侧新增/更新以下覆盖：
   - 结构化 dropped dedupe 能正确复用旧记忆；
   - dropped 但未显式指定 dedupe 目标时会新建；
   - legacy accepted 索引结果仍能正常保留 direct-write 候选；
   - stale dedupe 目标会安全降级为新建；
   - parser 能正确解析 `dropped_candidates[].dedupe_memory_id`。

## 4. ⚠️遗留问题与注意事项

1. 当前 direct-write 对并发失效 dedupe 目标采用“安全降级为新建”，优先保证显式工具写入成功；这会在极端竞争窗口下引入少量重复风险，但优于返回 stale 记忆或中断请求。
2. 统一 reviewer prompt 现已在 `default / qwen3.5-base / qwen3.5-flash` 三套模板中同步；后续若新增模型目录，需要同时复制这套 dropped 结构化契约。
3. 本轮只修复自审确认的 3 个问题，没有扩展 retention worker 或 recycle/trash 的后续实现边界。
4. 计划文件在写入本总结后需要迁移到 `docs/completed/`，保持日期序号前缀不变。
