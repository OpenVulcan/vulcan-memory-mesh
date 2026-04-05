# 冷数据回收治理说明

本文用于集中说明当前主线的冷数据回收治理链路，避免再从 README、历史计划和测试代码里拼接理解运行时行为。

## 1. 设计目标

当前 retention 维护器的主目标只有三个：

1. 让冷状态记忆尽快退出热主表，避免 `memory_nodes` 和上下文边长期膨胀。
2. 让长期空闲 session 中已经脱离热窗口、且不再被主表引用的旧 `turn` 退出热主表。
3. 在不提供产品级恢复接口的前提下，为数据库层保留一个有限期的防灾缓冲窗口。

另外要额外说明：

- 当前共享半小时维护时钟还会顺带执行一次独立的 scratchpad 过期硬删除。
- 但 scratchpad 不进入 recycle trash，因此它不属于本文档的主治理语义。

当前不做的事情也需要明确：

- 不提供面向产品用户的“恢复回收内容”接口。
- 不为了提升归档率而破坏 `source_turn_id -> GetTurnDetails` 契约。
- 不把回收批次、向量 GC 队列或冷 turn 队列演化成永久台账。

## 2. 后台维护顺序

当前共享维护器每轮按以下顺序执行：

1. 终态记忆回收
2. 独立冷 `turn` 扫描入队
3. 已领取冷 `turn` 回收任务执行
4. idle-session recycle
5. vector GC retry
6. trash purge
7. scratchpad 过期硬删除

这样安排的原因是：

- 先把明显的终态记忆搬离热表，后续 session 压缩才能聚焦真正剩余的活跃冷数据。
- 冷 `turn` 先扫描再执行，避免扫描和归档共用一条耦合事务路径。
- vector GC retry 必须晚于关系回收成功之后，防止冷热状态不一致。
- purge 最后执行，确保 `trash_retention` 代表的是“进入回收站后的保留窗口”。
- scratchpad 过期硬删除最后执行，因为它不依赖 recycle trash，也不应干扰主 retention 统计语义。

## 3. 热窗口规则

当前运行时的 turn 热窗口由组合根直接计算：

```text
effective_keep_turns = post_action.session_analysis_history_turns + retention.turn_keep_extra_turns
```

含义如下：

- `post_action.session_analysis_history_turns`
  - 决定单轮 `analyze_turn` 回带多少条历史 `details`
- `retention.turn_keep_extra_turns`
  - 在上述分析窗口之外额外保留的热 `turn` 数量

只要某条 turn 仍处在这个热窗口内，就不会被冷 turn 回收链路迁出主表。

## 4. 终态记忆回收

当前维护器会周期性扫描以下冷状态记忆：

- `superseded`
- `expired`
- `deleted`

满足条件后会执行以下动作：

1. 把 `vmm_memory_nodes` 行迁入 `memory_nodes_trash`
2. 把对应 `vmm_memory_context_edges` 迁入 `memory_context_edges_trash`
3. 从热主表删除原始记忆与上下文边
4. SQLite 同步删除 `vmm_memory_nodes_fts`
5. 如旁路向量删除失败，则把任务持久化到 `vector_gc_jobs`

保护边界：

- 受保护的共享记忆会根据 `protect_priority_floor`、`protect_memory_level_floor`、`skip_protected_shared_memories` 决定是否跳过。
- 读时 Weibull 衰减不会直接触发删除；真正删除仍以生命周期与治理规则为准。

## 5. 独立冷 turn 回收

### 5.1 为什么需要独立 pass

此前旧 `turn` 回收只存在于 idle-session recycle 内部，意味着：

- 是否能处理旧 `turn` 取决于 session 是否整体进入 idle 压缩条件
- 扫描和执行共用同一条事务路径，难以清晰表达多 worker 不重复消费

现在已经拆成独立链路：

1. 扫描阶段发现“存在可回收旧 `turn`”的 session
2. 为 session 持久化一条 `recycle_job`
3. 由执行阶段领取 job，再独立归档该 session 的旧 `turn`

### 5.2 可回收条件

冷 `turn` 只有在同时满足以下条件时才允许迁出主表：

1. 不在当前热窗口内
2. `extracted_status` 不是 `pending`
3. 没有任何主表记忆引用该 `turn`
4. 没有任何主表画像引用该 `turn`

这条约束是为了保持：

```text
source_turn_id -> GetTurnDetails
```

只要某条记忆仍能被搜索命中，它的 `source_turn_id` 就不会指向一个已经被回收掉的 turn。

## 6. recycle_jobs 与 recycle_batches 的职责分工

当前有两类和回收相关的表，它们职责不同：

### 6.1 `recycle_jobs`

这是独立冷 `turn` 回收使用的持久化任务队列。

职责：

- 只表示“还有待执行的回收任务”
- 支持 scan / claim / execute 分离
- 支持失败后延迟重试
- 成功后立即删行，不保留完成台账

它解决的是“多 worker / 多轮维护下不要重复执行同一条冷 turn 任务”。

### 6.2 `recycle_batches`

这是实际已经执行成功的回收批次锚点。

职责：

- 为 trash 表里的行提供 `batch_id`
- 作为 purge 的批次边界
- 在 `trash_retention` 到期后连同批次元数据一起删除

它解决的是“trash 行属于哪一批实际回收结果”。

补充说明：

- 历史计划里曾使用过“recycle batch claim”这类表述。
- 当前正式实现并不会对 `recycle_batches` 本身做 claim。
- 多 worker / 多轮维护下的扫描、领取、执行去重语义，当前全部由 `recycle_jobs` 队列承担。
- `recycle_batches` 只保留“实际已经完成的回收批次锚点”职责，不再承担任务队列职责。

## 7. idle-session recycle

idle-session recycle 负责处理“整个 session 已长期空闲”的压缩场景。

主要处理对象：

- 已过期且长期未被强化的 `session` 级记忆
- 超出热窗口且不再被主表记忆/画像引用的旧 `turn`

进入 idle 回收判定前，session 至少需要满足：

- 超过 `retention.session_idle_recycle_after`
- 不存在 pending turn

这样做的目的，是避免把仍在活跃分析或刚被刷新过的 session 过早压缩掉。

## 8. 向量 GC 重试

当前旁路向量删除不会只靠日志兜底。

如果发生以下情况：

- 关系回收已成功
- `vector.DeleteByIDs` 失败

系统会把失败任务写入 `vector_gc_jobs`，后续维护轮次再领取执行。

关键规则：

- 成功后立即删行
- 失败时递增 `attempt_count` 并重新设置 `next_run_at`
- PostgreSQL 组合库存储下通常不会产生额外的向量侧回收负担，但接口语义保持一致

## 9. 回收站与 purge

当前 trash 相关主表包括：

- `memory_nodes_trash`
- `memory_context_edges_trash`
- `turn_records_trash`

超过 `retention.trash_retention` 后，后台会做最终 purge，覆盖：

1. 对应 trash 行
2. `recycle_batches` 元数据

回收站的定位是：

- 数据库层有限期防灾缓冲
- 不是产品级恢复入口

因此当前不会提供：

- gRPC 恢复接口
- 手工恢复命令
- “回收站浏览器”之类的运行时产品能力

## 10. 配置项说明

当前与 retention 直接相关的配置项如下：

- `retention.enabled`
  - 是否启用后台回收治理
- `retention.recycle_scan_interval`
  - 维护轮次周期
- `retention.turn_keep_extra_turns`
  - 热窗口额外保留 turn 数
- `retention.session_idle_recycle_after`
  - session 进入空闲回收判定的阈值
- `retention.trash_retention`
  - 回收站保留时长
- `retention.protect_priority_floor`
  - 共享记忆保护优先级地板
- `retention.protect_memory_level_floor`
  - 共享记忆保护层级地板
- `retention.skip_protected_shared_memories`
  - 是否跳过受保护共享记忆

## 11. 当前边界与已知取舍

以下属于当前明确保留的取舍，而不是缺陷：

1. 不提供产品级恢复接口。
2. 冷 turn 回收优先保证 `GetTurnDetails` 契约稳定，而不是最大化归档率。
3. `recycle_jobs` 与 `vector_gc_jobs` 都是短期队列，不保留完成台账。
4. `recycle_batches` 只承担 trash 批次锚点职责，不承担长期审计职责。

如果未来要继续扩展治理能力，应优先遵守这四条边界，避免再次把“短期治理队列”做成长久台账。
