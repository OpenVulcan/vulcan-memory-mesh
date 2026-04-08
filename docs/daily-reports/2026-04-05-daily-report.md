# 日报 - 2026-04-05

## 总结

2026 年 4 月 5 日，仓库共完成并归档了 38 份计划，工作密度明显高于前一日，主要集中在四个方向：长期记忆替代与 retention 治理闭环、全局代码审查后的稳定性加固、session scratchpad 独立链路落地，以及 AI Key 级容灾体系从设计到实现的完整推进。

这一天的工作从“记忆何时替代旧数据、何时退出热主表、何时进入回收站、何时清理向量与垃圾元数据”一路推进到“PreCheck 证据规范化、召回去重、查询与生命周期一致性”层面，同时补齐了 scratchpad 的独立 gRPC/存储支线，并正式让 `llm / embedding / rerank` 具备 key 级 failover 能力。整体成果更偏向底层能力收束与系统稳定性加固。

## 主要产出

### 1. 记忆替代与 retention 治理链路显著前进

- 启动并落地了跨 scope 记忆替代闭环，让新记忆不再只是“被接受”，而是可以在 `project / space / team / session` 范围内真正接管旧记忆。
- 让 `WriteMemories` 与 `post-action` 的 replace 语义对齐，修复候选级 supersede、裁剪后残留 supersede、`similar_memories` 顺序错配等风险点。
- 新增独立 `RetentionUseCase`，把冷状态记忆回收、session 空闲回收、回收站 purge、向量 GC 元数据治理从零散逻辑收敛为后台维护链。
- 补齐 PostgreSQL 与 SQLite 的 recycle batch、trash 表、冷状态扫描、批次 purge 和向量 GC 重试/元数据压缩等能力，使 retention 不再停留在配置占位阶段。

### 2. 全局代码审查推动多条稳定性修复持续落地

- 连续完成多轮全局 review，围绕运行时稳定性、查询正确性、召回一致性、生命周期语义、维护行为和向量 GC 元数据做了高频修补。
- 收敛了 PreCheck 证据规范化逻辑，让 `matched_context_values` 在 query、候选合并和 reviewer 请求三条链路上共享同一套 canonical 规则。
- 修复了最终注入文本去重、采纳候选与最终注入一致性、检索 rerank 分数钳制、非有限值处理、召回阶段过期记忆保护等细节问题。
- 对 retention 剩余问题做了集中收尾，使冷回收、共享记忆保护、空闲 session 判定、向量 GC 重试与批次元数据清理更加可靠。

### 3. Session Scratchpad 独立能力链正式建立

- 设计并落地了独立于长期记忆体系的 session scratchpad 能力，新增 `ScratchpadUpsert`、`ScratchpadDelete`、`ScratchpadGet`、`ScratchpadClean` 四个 gRPC 接口。
- 新增独立存储结构，保持 scratchpad 与 `memory_nodes / turn_records / retention / vector` 主链解耦，不混入长期记忆语义。
- 对 DWM scratchpad 契约、gRPC 校验、存储结构、自动迁移和 review findings 做了成套修正，使其可以作为 AI 临时执行上下文的稳定侧链存在。

### 4. AI Key 容灾体系从方案走向实现

- 完成 AI key-only failover 设计，并正式实现 `llm / embedding / rerank` 的 API Key 级容灾包装层。
- 在配置层新增 `api_keys` 与 `key_failover`，保留旧 `api_key` 的兼容写法，并接入环境变量覆盖与运行时装配。
- 明确将限流、配额、鉴权类错误纳入 key 切换范围，同时对 rerank 降级和配置分层回归做了修复。
- 补齐相关文档、示例配置与自审结论，为后续多 route 与多 provider 演进打下基础。

### 5. 文档整理与运行时约束同步推进

- 基于前一天 completed 记录补齐了 `2026-04-04` 的日报。
- 对 README、post-action/retention/scratchpad/AI failover 等文档进行了同步整理，减少文档与运行时行为漂移。
- 额外完成一次 retention 向量 GC 测试口径核对，明确以当前实际 `go test` 结果为准，不再依赖过期描述做判断。

## 验证情况

- 当天大量任务按仓库规则执行了最小测试集：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
- retention、SQLite/PostgreSQL 适配层、scratchpad、AI failover 等主题任务补充执行了大量定向测试。
- 多个任务明确完成了 `go test ./...` 全量回归，部分 PreCheck 修复还额外执行了 `go vet ./...`。
- 文档型任务未统一执行构建命令，但当天核心代码任务均伴随测试闭环。

## 后续关注点

- retention 已具备冷状态回收、回收站与向量 GC 元数据治理，但 `turn` 冷归档与完整产品级恢复能力仍未落地。
- scratchpad 已有独立 gRPC 和存储支线，后续还需要继续观察与现有 session 生命周期、清理节奏之间的长期边界。
- AI key failover 已经成型，但当日实现仍聚焦“固定 provider + endpoint + model”的 key 级容灾，尚未扩展到更复杂的多 provider 选路语义。

## 覆盖的归档计划

1. `20260405-01-POSTGRES_RETENTION_MEMORY_REPLACE_EXECUTION`
2. `20260405-02-WRITEMEMORIES_MEMORY_REPLACE_PARITY`
3. `20260405-03-UNCOMMITTED_CODE_REVIEW`
4. `20260405-04-YESTERDAY_COMPLETED_PLANS_DAILY_REPORT`
5. `20260405-05-REVIEW_FINDINGS_FIX`
6. `20260405-06-RETENTION_MEMORY_COLD_RECYCLE_EXECUTION`
7. `20260405-07-SESSION_IDLE_RECYCLE_AND_GLOBAL_REVIEW`
8. `20260405-08-GLOBAL_CODE_REVIEW_AND_OPTIMIZATION`
9. `20260405-09-GLOBAL_CODE_REVIEW_AND_STABILITY_HARDENING`
10. `20260405-10-GLOBAL_CODE_REVIEW_AND_RUNTIME_HARDENING`
11. `20260405-11-GLOBAL_CODE_REVIEW_AND_CORRECTNESS_HARDENING`
12. `20260405-12-GLOBAL_CODE_REVIEW_AND_RETENTION_STABILITY_HARDENING`
13. `20260405-13-GLOBAL_CODE_REVIEW_AND_POSTGRES_RETENTION_HARDENING`
14. `20260405-14-GLOBAL_CODE_REVIEW_AND_MEMORY_DETAIL_HARDENING`
15. `20260405-15-GLOBAL_CODE_REVIEW_AND_QUERY_STABILITY_HARDENING`
16. `20260405-16-GLOBAL_CODE_REVIEW_AND_RETRIEVAL_CORRECTNESS_HARDENING`
17. `20260405-17-GLOBAL_CODE_REVIEW_AND_LIFECYCLE_CORRECTNESS_HARDENING`
18. `20260405-18-GLOBAL_CODE_REVIEW_AND_RECALL_STABILITY_HARDENING`
19. `20260405-19-GLOBAL_CODE_REVIEW_AND_MAINTENANCE_CORRECTNESS_HARDENING`
20. `20260405-20-GLOBAL_CODE_REVIEW_AND_PRECHECK_EVIDENCE_NORMALIZATION_HARDENING`
21. `20260405-21-GLOBAL_CODE_REVIEW_AND_RETENTION_VECTOR_GC_RETRY_HARDENING`
22. `20260405-22-GLOBAL_CODE_REVIEW_AND_VECTOR_GC_METADATA_COMPACTION_HARDENING`
23. `20260405-23-GLOBAL_CODE_REVIEW_AND_PRECHECK_REVIEWER_EVIDENCE_CANONICALIZATION_HARDENING`
24. `20260405-24-GLOBAL_CODE_REVIEW_AND_PRECHECK_FINAL_INJECTION_DEDUP_HARDENING`
25. `20260405-25-GLOBAL_CODE_REVIEW_AND_PRECHECK_ADOPTION_INJECTION_CONSISTENCY_HARDENING`
26. `20260405-26-RETENTION_REMAINING_ISSUES_REMEDIATION`
27. `20260405-27-PLAN26_COMPLETION_VERIFICATION_AND_REMEDIATION`
28. `20260405-28-DOCUMENTATION_SYNC_AND_RUNTIME_ALIGNMENT`
29. `20260405-29-SESSION_SCRATCHPAD_GRPC_AND_STORAGE_PLAN`
30. `20260405-30-DWM_SCRATCHPAD_CONTRACT_ALIGNMENT`
31. `20260405-31-POSTGRES_SCRATCHPAD_AUTO_MIGRATION`
32. `20260405-32-SCRATCHPAD_REVIEW_FINDINGS_FIX`
33. `20260405-33-AI_KEY_ONLY_FAILOVER_DESIGN`
34. `20260405-34-AI_KEY_FAILOVER_IMPLEMENTATION`
35. `20260405-35-LLM_KEY_FAILOVER_SELF_REVIEW`
36. `20260405-36-RERANK_KEY_DEGRADE_AND_FAILOVER_FIX`
37. `20260405-37-REVIEW_FINDINGS_FIX`
38. `20260405-38-RETENTION_VECTOR_GC_TEST_ALIGNMENT`
