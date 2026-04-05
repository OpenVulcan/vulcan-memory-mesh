# 任务目标

本计划不再用于“继续核验问题是否存在”，而是作为当前唯一有效的遗留问题整改计划，直接面向以下已确认问题的解决：

1. 收口旧总控计划，避免 `docs/plan/20260404-19-SESSION_RECYCLE_BIN_AND_RETENTION_PLAN.md` 继续混合“已完成事实”和“未完成事项”，制造 backlog 误判。
2. 修正文档漂移，重点解决 `README.md` 中记忆查询接口仍保留旧 `query_json / background` 表述的问题，并统一当前 gRPC 契约描述。
3. 补齐独立的 retention / 回收治理专题文档，解决当前规则散落在 `README` 与多份 completed plan 中的问题。
4. 继续完成 retention 主链中仍真实未落地的工程项：
   - 独立冷 `turn` 回收扫描链路；
   - recycle batch 的显式批次 claim 与 scan / execute 分离；
   - 与上述能力配套的测试、文档与运维语义收口。
5. 对低优先级但真实存在的维护债务做收尾评估，例如 `vector_gc_jobs` 兼容字段残留是否适合在不破坏兼容性的前提下清理。

本计划的定位是“解决问题”，而不是再次查找问题。已被证伪或已确认属于设计取舍的事项，不再纳入本计划执行范围。

# 已确认问题清单

## 一、必须解决

1. 旧总控计划仍停留在 `docs/plan/`，且与当前实际实现进度不一致。
2. `README.md` 的记忆查询接口章节存在真实文档漂移：
   - 当前 proto 已使用 `repeated string queries`；
   - 但 README 仍写着 `query_json / background`。
3. 缺少独立 retention 专题文档，导致回收策略、回收站、向量 GC、session idle recycle 等规则只能在 README 和已完成计划中拼接理解。
4. retention 运行时当前没有独立的“冷 turn 回收扫描” pass；`turn` 回收仅作为 idle-session recycle 的一部分存在。
5. recycle / retention 当前已有行级并发保护与 vector GC claim，但尚未完整实现旧总控计划要求的“recycle batch 显式批次 claim + scan / execute 分离”。

## 二、需要评估后处理

1. `vector_gc_jobs` 的兼容字段残留属于真实 schema 债务，但当前不构成立即的运行时阻断。
2. 历史 completed plan 中部分“后置 / 未完成”措辞已被后续实现覆盖，后续若要继续做治理文档，应决定是否增加一份统一的历史映射说明，避免再次误读。

## 三、明确不纳入本计划

1. `GRPC_MEMORY_API_SIMPLIFICATION_FOR_AI_TOOLS.md` 不再视为当前未实现问题来源。
2. pre-check “同 turn 单代表项注入”与“等价文本保留第一条”属于当前确认过的稳定性取舍，不作为本计划缺陷处理目标。

# 详细执行步骤

## 阶段一：计划与文档真源收口

1. 修改 `docs/plan/20260404-19-SESSION_RECYCLE_BIN_AND_RETENTION_PLAN.md`：
   - 在文末补齐执行变更总结；
   - 明确标注该计划的大部分内容已由 `20260405-01` 至 `20260405-25` 分阶段实现；
   - 把尚未完成的真实事项显式转移到本计划；
   - 将其迁移到 `docs/completed/`，不再保留在 `docs/plan/`。
2. 让 `docs/plan/` 目录只保留本计划，作为唯一在编的 retention 遗留项整改入口。
3. 在本计划中固定“问题分流规则”：
   - 已完成事项不再重复列入 backlog；
   - 已证伪事项不再继续追踪；
   - 设计取舍事项只保留边界说明，不进入修复列表。

## 阶段二：修正文档漂移

1. 更新 `README.md` 中记忆查询接口章节：
   - 改正 `SearchMemoryEvents` 的请求说明，去掉 `query_json / background`；
   - 对齐当前 proto 的 `repeated string queries`；
   - 对齐当前最小化 `MemorySearchHit` 字段；
   - 对齐当前结构化 `GetTurnDetails` 与精简 `WriteMemories` 描述。
2. 检查并同步下列文档的相关口径：
   - `docs/grpc-integration-guide_CN.md`
   - `docs/post-action-guide_CN.md`
   - 其他直接描述 memory query / retention 的中文文档
3. 新增 retention 专题说明文档，至少覆盖：
   - 冷状态记忆回收；
   - session idle recycle；
   - turn 热窗口与 `turn_keep_extra_turns`；
   - recycle batch；
   - trash retention；
   - vector GC retry；
   - 当前“不提供产品级恢复”的边界。

## 阶段三：补齐真实未完工程项

1. 设计并实现独立的冷 `turn` 回收扫描 pass：
   - 不再只依赖 idle-session recycle 顺带处理；
   - 仍必须保持 `source_turn_id -> GetTurnDetails` 契约不被破坏；
   - 保持仅回收“超出热窗口且无主表引用”的旧 `turn`。
2. 设计并实现 recycle batch 的显式批次 claim 与 scan / execute 分离：
   - 扫描阶段只发现候选；
   - 执行阶段只消费已 claim 的 batch；
   - 多 worker / 多实例下不得重复消费同一批次。
3. 如果实现 scan / execute 分离时发现 `vector_gc_jobs` 兼容字段清理可以无扰完成，则一并处理；
   - 否则显式记为继续保留的 schema 债务，不做隐性遗留。

## 阶段四：验证与收口

1. 文档验证：
   - README 与 proto 不再漂移；
   - retention 专题文档可独立解释当前治理链路。
2. 行为验证：
   - 独立冷 `turn` pass 能工作，且不会误归档仍可被详情查询依赖的 `turn`；
   - recycle batch claim 在多 worker 下不重复消费。
3. 计划验证：
   - 完成后，本计划末尾补齐执行变更总结；
   - 再迁移到 `docs/completed/`。

# 技术选型与原则

1. 文档修正必须以当前主分支代码与 proto 为准，不以历史计划为准。
2. 冷 `turn` 回收仍以“保守不破坏对外契约”为最高优先级，不能为了提高归档率牺牲 `GetTurnDetails` 一致性。
3. recycle batch 的并发治理要延续当前已有的 claim / `SKIP LOCKED` 思路，而不是另起一套与现有 vector GC 脱节的机制。
4. schema 债务只在“收益明确且不会引入兼容性风险”时处理；否则明确延期，不做模糊承诺。

# 验收标准

1. `docs/plan/` 中只剩本计划一份在编计划。
2. `20260404-19-SESSION_RECYCLE_BIN_AND_RETENTION_PLAN.md` 已补齐执行变更总结并归档到 `docs/completed/`。
3. `README.md` 中不再出现与当前 proto 冲突的 `query_json / background` 旧表述。
4. 仓库中新增一份 retention / 回收治理专题文档，能够独立说明当前生命周期维护规则。
5. 独立冷 `turn` 回收扫描 pass 已落地，且不破坏 `source_turn_id -> GetTurnDetails` 契约。
6. recycle batch 的显式批次 claim 与 scan / execute 分离已落地，且具备多 worker 不重复消费保障。
7. 所有真实遗留问题都有结果：
   - 已修复；
   - 明确延期并说明原因；
   - 或明确降级为非阻断债务。

---

# 执行变更总结

## 1. 核心修复与调整概述

本次整改已经把 plan26 覆盖的真实遗留问题全部收口：

1. 旧总控计划 `20260404-19-SESSION_RECYCLE_BIN_AND_RETENTION_PLAN.md` 已补齐执行变更总结并迁入 `docs/completed/`，不再继续停留在 `docs/plan/` 混合已完成事实与未完成事项。
2. `README.md` 中记忆查询接口的旧 `query_json / background` 表述已全部替换为当前 proto 的 `queries[]` 契约，同时补齐了当前 `GetTurnDetails` 与 `WriteMemories` 的最小化接口说明。
3. 新增独立 retention 专题文档 `docs/retention-governance-guide_CN.md`，集中说明冷状态记忆回收、独立冷 `turn` 扫描、idle-session recycle、trash retention、vector GC retry 以及“不提供产品级恢复”的边界。
4. retention 主链已补齐独立冷 `turn` 回收 pass，并通过持久化 `recycle_jobs` 队列实现 scan / claim / execute 分离；多 worker 不重复消费依赖 PostgreSQL 的 `SKIP LOCKED` 与 SQLite 的租约时间戳语义。
5. 为新回收任务队列补齐了 usecase 层和适配器层测试，验证冷 `turn` 扫描入队、领取、执行、重试与完成关闭的完整链路。
6. `vector_gc_jobs` 兼容字段残留已完成评估：当前仍保留，原因是这属于跨 SQLite / PostgreSQL 的 schema 清理债务，不影响运行时正确性，且立刻清理会引入额外 migration 风险；因此本轮明确降级为非阻断维护债务，不再阻塞 plan26 关闭。

## 2. 📂文件变更清单

### 新增

1. `docs/retention-governance-guide_CN.md`
2. `internal/adapters/outbound/vldb_postgres/recycle_jobs.go`
3. `internal/adapters/outbound/vldb_sqlite/recycle_jobs.go`

### 修改

1. `README.md`
2. `internal/logic/domain/retention.go`
3. `internal/app/ports/interfaces.go`
4. `internal/app/usecase/retention.go`
5. `internal/app/usecase/retention_test.go`
6. `internal/adapters/outbound/vldb_postgres/helpers.go`
7. `internal/adapters/outbound/vldb_postgres/schema.go`
8. `internal/adapters/outbound/vldb_postgres/retention_store_test.go`
9. `internal/adapters/outbound/vldb_sqlite/store.go`
10. `internal/adapters/outbound/vldb_sqlite/schema_migrations.go`
11. `internal/adapters/outbound/vldb_sqlite/retention_store_test.go`

### 迁移 / 归档

1. `docs/plan/20260404-19-SESSION_RECYCLE_BIN_AND_RETENTION_PLAN.md` -> `docs/completed/20260404-19-SESSION_RECYCLE_BIN_AND_RETENTION_PLAN.md`
2. `docs/plan/20260405-26-RETENTION_REMAINING_ISSUES_REMEDIATION.md` -> `docs/completed/20260405-26-RETENTION_REMAINING_ISSUES_REMEDIATION.md`

## 3. 💻关键代码调整详情

### 3.1 独立冷 `turn` 回收链路

1. 在 `internal/logic/domain/retention.go` 中新增冷 `turn` 回收相关领域模型：
   - `ColdTurnRecycleJobEnqueueQuery`
   - `ColdTurnRecycleQuery`
   - `ColdTurnRecycleResult`
   - `RecycleJobRecord`
2. 在 `RetentionStore` 端口中新增：
   - `EnqueueColdTurnRecycleJobs`
   - `ClaimPendingRecycleJobs`
   - `RecycleColdTurns`
   - `CompleteRecycleJobs`
   - `RetryRecycleJobs`
3. 在 `RetentionUseCase.runMaintenance` 中新增独立冷 `turn` pass：
   - 先扫描并入队；
   - 再领取持久化任务；
   - 按 session 独立执行回收；
   - 失败时重试，成功后立即删队列行。

### 3.2 PostgreSQL / SQLite 队列与 schema

1. PostgreSQL 新增 `vmm_recycle_jobs` 表与索引，并在 `recycle_jobs.go` 中实现：
   - 扫描入队；
   - `SKIP LOCKED` 领取；
   - 冷 `turn` 归档；
   - 成功删除任务；
   - 失败延期重试。
2. SQLite 将 schema 版本提升到 `18`，新增 `vmm_recycle_jobs` 表及 migration，并在 `recycle_jobs.go` 中实现：
   - 基于 `writeMu` 的顺序化入队；
   - 通过 `claimed_timestamp + next_run_timestamp` 表达租约；
   - 独立执行冷 `turn` 归档；
   - 失败后重排队；
   - 成功后直接删行。

### 3.3 文档与测试

1. README 已与当前 gRPC proto 和 retention 真实行为对齐。
2. 新增 retention 专题文档，集中解释当前治理链路与边界。
3. 新增与补强测试覆盖：
   - `RetentionUseCase` 的冷 `turn` 扫描、领取、执行、重试；
   - PostgreSQL 的冷 `turn` 候选谓词与 `SKIP LOCKED` 领取 SQL；
   - SQLite 的冷 `turn` 候选谓词、入队、领取、重试、完成。
4. 已完成验证：
   - `go test ./internal/app/usecase ./internal/adapters/outbound/vldb_sqlite ./internal/adapters/outbound/vldb_postgres`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`

## 4. ⚠️遗留问题与注意事项

1. `vector_gc_jobs` 中的 `completed_timestamp / completed_at` 兼容字段仍保留；这是显式确认后的非阻断 schema 债务，不影响当前运行时正确性。
2. 当前冷 `turn` 回收的多 worker 去重语义依赖 `recycle_jobs` 队列，而不是把 `recycle_batches` 本身做成第二套 claim 状态机；这是本轮选择的正式实现，不再把“必须对 batch 自身 claim”视为未完成项。
3. 当前仍不提供产品级恢复接口；回收站仍只承担数据库层有限期防灾缓冲，不应被误读为面向调用方可恢复的产品能力。

## 5. 后验复核补记

本计划在归档后又做了一轮后验复核，结论如下：

1. 当前没有再发现新的运行时代码缺口；plan26 的主要工程目标已经真实落地。
2. 本计划正文中“recycle batch 的显式批次 claim 与 scan / execute 分离”这句，如果按字面理解，容易让人误以为必须对 `recycle_batches` 自身建立第二套 claim 状态机。当前正式实现不是这样：
   - scan / claim / execute 分离已经通过 `recycle_jobs` 队列落地；
   - `recycle_batches` 只承担“已完成回收批次锚点”职责；
   - 因此这属于“实现方式演进后的文档措辞滞后”，不是未完成项。
3. 本计划执行总结里已经明确这一点，所以当前无需为了迁就旧措辞而反向改动代码。
