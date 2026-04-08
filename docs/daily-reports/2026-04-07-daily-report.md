# 日报 - 2026-04-07

## 总结

2026 年 4 月 7 日，仓库共完成并归档了 16 份计划，整体主题比前两天更聚焦，主要围绕三条主线展开：向量重建与维护命令的可控性修复、LLM 路由权重模型的分级重构，以及 embedding 调用链在批次、非法输入和严格/降级语义上的系统性收束。

从结果上看，这一天更像是一次“维护链与向量链的工程收口日”。早期任务先围绕 `vmm-migrate`、split/combined 迁移与 maintenance 超时做 review 修复，随后把 LLM 选模权重从单一 `priority` 改造成分层 `weights.*`，最后又把 embedding 控制器、本地预算、外层重建批次以及单条非法输入的处理策略全部重新梳理了一遍。

## 主要产出

### 1. 向量重建与维护命令可靠性持续加固

- 修复了 `split` 模式向量重建在 embedding 维度变化时的回滚缺陷，避免 durable 与 sidecar 留下不一致状态。
- 拆分在线查询超时与维护超时，让 PostgreSQL 记忆读取在在线路径和维护路径下使用不同预算。
- 为 `vmm-migrate` 增加停服探测约束，明确 `-vector-rebuild` 只能在服务停止时执行，避免运行时与维护动作互相踩踏。
- 补齐 maintenance CLI 的信号取消、PostgreSQL 维护超时、Windows 构建脚本兼容与 review findings 修复，使维护工具更接近可长期使用的工程状态。

### 2. LLM 路由权重从单权重升级为分级权重体系

- 将单一 `priority` 拆分为 `precheck_l1 / precheck_l2 / postaction_l1 / postaction_l2 / reserve` 五槽位 `weights.*`。
- 让 `IntentExtractor`、`PreCheckMemoryReviewer`、`TurnAnalyzer`、`PostActionCandidateReviewer` 等调用点显式传入路由选择层级，运行时据此动态排序 route。
- 随后按最新决策彻底移除 `llm.routes[].priority` 兼容入口，只保留 `weights.*` 作为 LLM 选路的唯一来源。
- 同步更新配置样例、README 与设计文档，避免旧字段残留继续误导配置使用者。

### 3. Embedding 调用链职责边界被重新收口

- 先明确 `embedding.max_batch_size` 只表达 provider 单次请求批宽，不再承载业务层自己的批次语义。
- 把 embedding 拆批、长度错误识别和单条超长文本截断重试统一收到 embedding 控制器中，删除业务层和适配器侧的重复拆批逻辑。
- 随后继续推进到下一阶段：移除本地 `max_input_tokens_per_text` 预算与截断重试，改为完全依赖 provider 对非法输入的权威判断。
- 在 `post-action` 路径中开放“单条确定性非法输入可丢弃”的 best-effort 语义，而 `memory_query`、`noise_gate`、`vector rebuild` 等路径仍保持 strict 或整体降级，不做 silent skip。
- 恢复 `vector rebuild` 自己的外层 materialize 批次，使维护规模控制与 provider 批宽控制彻底解耦。

### 4. Embedding 失败边界与覆盖测试进一步补齐

- 继续修复部分无效输入、全部输入被丢弃、向量重建批次解耦等后续问题，确保控制器不会误报成功或把缺失向量当成正常结果。
- 让真实运行时测试基座复用正式 embedding 装配链，减少测试行为与运行时行为分叉。
- 对 invalid input、partial drop、all dropped、strict validation 和向量重建批次切分等场景补齐回归测试。

## 验证情况

- 当天多个任务执行了定向测试，覆盖 `cmd/vmm-migrate`、`internal/app`、`internal/adapters/outbound/vldb_postgres`、`internal/adapters/outbound/vldb_sqlite`、`internal/adapters/outbound/ai_key_failover` 等关键模块。
- 在当天后半程的多项任务中，已多次执行并通过 `go test ./...` 全量回归。
- 至少完成了一次标准构建验证：
  - `pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\\make.ps1 build`
- 少量早期 review 修复任务只运行了定向测试，但后续围绕权重重构、离线重建约束和 embedding 链路重构的任务已补齐全量回归。

## 后续关注点

- `weights.reserve` 当前更多承担兼容与预留作用，还没有进入 precheck/postaction 主链路的核心决策。
- embedding 非法输入的处理语义已经更清晰，但 best-effort 目前仅在 `post-action` 放开，若未来要扩展到其他链路，需要单独设计审计与一致性策略。
- 向量重建已被明确收紧为停服维护动作，后续若再扩展恢复或跳过策略，需要继续保持“显式约束优先于文档约定”的原则。

## 覆盖的归档计划

1. `20260407-01-VECTOR_REBUILD_REVIEW_FIXES`
2. `20260407-02-MODEL_WEIGHT_LEVEL_SPLIT`
3. `20260407-03-VECTOR_REBUILD_REVIEW_P1_FIXES`
4. `20260407-04-MODEL_WEIGHT_AND_VECTOR_REBUILD_FIXES`
5. `20260407-05-VECTOR_REBUILD_REVIEW_FOLLOWUP_FIXES`
6. `20260407-06-REMOVE_PRIORITY_AND_REQUIRE_OFFLINE_VECTOR_REBUILD`
7. `20260407-07-MAINTENANCE_CLI_SIGNAL_CANCELLATION`
8. `20260407-08-POSTGRES_MAINTENANCE_TIMEOUTS`
9. `20260407-09-MAINTENANCE_TOOL_TIMEOUT_CONFIG`
10. `20260407-10-MAINTENANCE_REVIEW_FIXES`
11. `20260407-11-VECTOR_REBUILD_FINAL_FIXES`
12. `20260407-12-EMBEDDING_BATCH_LIMIT_PARAMS`
13. `20260407-13-EMBEDDING_CONTROLLER_BATCHING_AND_TRUNCATION`
14. `20260407-14-EMBEDDING_DROP_INVALID_INPUT_AND_REBUILD_BATCH_DECOUPLING`
15. `20260407-15-EMBEDDING_PARTIAL_DROP_FIXES`
16. `20260407-16-EMBEDDING_ALL_DROPPED_COVERAGE_FIX`
