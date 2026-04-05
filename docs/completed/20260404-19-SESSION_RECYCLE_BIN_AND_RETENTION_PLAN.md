# 任务目标

为当前会话与长期记忆体系设计一套可落地的“回收站 + 定时回收 + 最终清理”方案，重点解决以下问题：

1. 将超出热路径窗口、且不会再参与 `precheck` / 上下文召回的旧 `turn` 从主表移出，降低主链路读取体积。
2. 对长期无新增信息的 `session` 执行回收，但不删除 `session` 本身，只回收该 `session` 内未提级的 session 级记忆与可下沉的 `turn`。
3. 为回收内容提供持久化回收站，作为数据库层软备份与人工防灾缓冲区；不在 VMM 代码中提供任何恢复能力。
4. 为回收、保留、彻底清理设置明确的配置项与默认值，并保证与现有提级、退役、衰减、compact、pending 恢复链路不冲突。
5. 明确保护规则：对仍在有效期内、且具备高优先级 / 高等级 / 非 session 共享价值的数据，不进入自动清理。
6. 补齐“新记忆替代旧记忆”的闭环，尤其是跨 `session` 但仍在共享 scope 内的记忆更新场景，避免“旧事实长期 active、新事实又无法真正接管”的脏状态进入后续回收链路。

# 详细执行步骤

1. 先梳理当前热路径与生命周期边界，明确新增回收链路只能作为“冷数据维护层”，不能反向污染在线召回主链。
   - `PreCheck` 当前读取的是最近混合 turn 窗口：已提炼 turn 用 `details`，未提炼 turn 用 `dehydrated_content`。
   - `PostAction` 的单轮分析历史只读取“已提炼完成且有 `details`”的最近历史窗口。
   - 当前后台维护 worker 仅做两件事：过期画像收敛、空闲 pending session 补扫。
   - 当前记忆衰减是读时 Weibull 乘子，不会主动删除主表数据。
   - 当前 session 级记忆会在 `ApplyMemoryAdoption` 中因跨 session 多次被采纳而提级到 `project`，因此回收逻辑不能抢在提级规则之前错误清理。
   - 在进入回收设计前，必须先补齐记忆替代闭环；否则共享 scope 内旧记忆依然会长期停留在 `active`，回收策略就只能在脏前提上做被动清理。
   - 当前已存在的替代能力只有半套：
     - `analyze_turn -> superseded_memory_ids` 只能处理“同 `origin_session_id` 内已加载进输入的 active 旧记忆”；
     - `review_postaction_candidates` 虽然能看到跨 scope 的 `similar_memories`，但还不能输出“保留新候选并替代哪些旧记忆”。
   - 因此计划里需要把“记忆替代闭环”作为回收方案的前置子任务，一并落地。

2. 先补齐记忆更新 / 替代闭环，避免共享范围内的旧记忆长期悬挂在 `active`。
   - 保留现有 `analyze_turn -> superseded_memory_ids` 机制，继续处理“同 session 内明确覆盖”的低成本场景。
   - 扩展 `review_postaction_candidates` 的 memory 输出契约：
     - 从当前的 `accepted_candidate_indexes[]` 升级为 `accepted_candidates[]`；
     - 每条包含 `candidate_index`；
     - 并新增 `supersede_memory_ids[]`，只允许引用该候选挂载的 `similar_memories.memory_id`。
   - 这样 reviewer 就能表达三类决策：
     - 语义相同且信息量相当：丢弃新候选；
     - 新候选更完整、更明确、更新，且语义覆盖旧结论：保留新候选，并替代旧记忆；
     - 新候选只是并行补充、不构成覆盖：保留新候选，但不替代旧记忆。
   - 不在热主表里新增长期 `superseded_by_memory_id` 链路字段，避免为不会暴露给产品的历史关系长期增加写放大与引用管理成本。
   - 替代后的旧记忆不应长期留在热主表中：
     - 对在线正确性，必须立即退出 `active` 召回面；
     - 对数据治理，必须进入回收 / 延迟清理链路，而不是永久挂一个 `superseded` 状态不动。
   - 因此第一阶段建议采用统一处理方式：
     - 新记忆写入成功后；
     - 在同一 SQL 事务内把被替代旧记忆与其 `memory_context_edges` 直接迁入回收站表；
     - 同时删除热主表、FTS 与向量引用；
     - 事务提交后异步删除旧向量。
   - 这样“替代”与“回收”会汇合到同一套数据退出机制里，不会出现一套逻辑只做 supersede、另一套逻辑再单独清理的双轨状态机。
   - 新增独立配置项控制“记忆更替作用域”，不复用 `pre_check.search_scope`：
     - 建议命名：`memory_replace_scope`
     - 默认值：`project`
     - 支持值：`session / project / space / team`
   - 作用是：限制“相似旧记忆召回 + reviewer 替代决策”在多大共享范围内生效，避免 session 内更新误伤更大范围的共享事实。
   - `WriteMemories` 主动写记忆路径当前只有同 session、24 小时窗口内的 `dedupe_hash` 软幂等，没有语义级替代闭环。
   - 第一阶段先补齐 `post-action` 自动提炼主链；第二阶段再决定是否让 `WriteMemories` 复用同一套 replace scope 与替代逻辑，避免两条写入链路长期语义不一致。

3. 设计冷热分层规则，先定义哪些 `turn` 可以从主表移入回收站。
   - 不单独配置一个绝对的“保留 10 轮”，而是配置“检索 / 分析窗口之外额外保留多少轮”。
   - 新增“热 turn 额外边际缓存”配置，默认值为 `5` 轮。
   - 真实生效窗口定义为：
     - `effective_keep_turns = max(pre_check.history_turns, post_action.session_analysis_history_turns) + retention.turn_keep_extra_turns`。
   - 按当前仓库默认值：
     - `pre_check.history_turns = 3`
     - `post_action.session_analysis_history_turns = 3`
     - 因此默认 `effective_keep_turns = 8`
   - 这样做的原因是：超过当前在线检索 / 分析窗口再额外多留 `5` 条，已经足够覆盖抖动与边界场景，继续保留更多 `turn` 只会增加主表体积，而不会提升实际命中价值。
   - 只有满足以下条件的 `turn` 才允许进入回收站：
     - 不在最近 `effective_keep_turns` 热窗口内；
     - 不存在任何仍留在主表中的 `memory_nodes.source_turn_id = turn_id`；
     - 不存在任何仍留在主表中的 `profile_nodes.turn_id = turn_id`；
     - 不存在 pending 提炼状态，即 `extracted_status != pending`；
     - 不属于仍被当前 compact 之后热上下文依赖的 turn。
   - 这里必须使用“任意状态都不再引用”而不是只看 `active / unexpired`，因为当前仓库仍允许按 id 读取 `expired / superseded / deleted` 记忆行；如果 turn 先被移走，而这些生命周期历史行还留在主表，就会留下悬空 `source_turn_id`。
   - 这样可以保证被移走的 `turn` 不再被 `precheck`、`post-action` 历史组装、记忆详情追溯或任何仍保留在主表中的生命周期历史行直接依赖。
   - 这里还要补一条对外契约保护：当前 `SearchMemoryEvents`、`context_items[].turn_id` 与 `GetTurnDetails` 已经把 `source_turn_id` 当成可继续追查原始对话的句柄。
   - 因此第一阶段不能把“只要离开热窗口就归档”当成硬目标，而必须坚持“只归档无任何主表引用的 turn”这一保守前提；否则会出现“热路径还能返回 turn_id，但详情接口已经查不到”的契约回归。
   - 这也意味着 turn 冷归档在第一阶段本质上是“机会式减负”而不是“保证把热窗口之外所有 turn 都压走”：
     - 若某些旧 turn 仍被 `project / user / team / space` 级活跃记忆或画像长期引用，它们会继续留在主表；
     - 如果未来业务必须进一步压缩这类 turn，必须另开重大设计决策，先明确是引入“无原始 turn 绑定的 retained source kind”，还是允许只读详情查询透过回收站取数，不能在本方案中悄悄改变现有契约。
   - 这里还要额外处理一个容易被忽略的风险：`turn` 迁出后，`vmm_sessions.turn_count / summarize_budget / last_summarized_id / last_compacted_turn_id` 的语义会发生漂移。
   - 第一阶段必须先固定一套一致语义，避免实现时出现“有的字段按主表剩余行数理解、有的字段按历史累计值理解”的混乱：
     - 建议 `turn_count` 保持“历史累计已持久化 turn 数”的单调语义，不因归档而回退；
     - `last_compacted_turn_id` 保持“历史 compact 锚点”语义，不随归档回写；
     - `last_summarized_id / summarize_budget` 当前仍属预留/弱使用字段，若不新增替代聚合字段，就必须在文档中明确它们不能再被解释为“主表剩余 turn 的即时统计”。
   - 也就是说，回收设计不能只盯着 `vmm_turn_records` 本身，还必须同步约束 session 聚合字段的解释口径与文档说明。

4. 设计长期空闲 `session` 的回收判定规则，避免把仍可能继续成长的 session 过早回收。
   - 新增“session 空闲回收阈值”配置，默认值为 `15d`。
   - 只有满足以下条件的 `session` 才允许进入回收候选集：
     - `session.updated_at` 早于 `now - session_idle_recycle_after`；
     - 不存在 pending `turn`；
     - `last_extract_completed_at` 不晚于空闲阈值；
     - 当前没有正在进行中的队列处理；
     - 当前 session 下的热 turn 之外的历史 turn 满足回收条件。
   - session 回收不删除 `vmm_sessions` 主记录，也不修改 `session_key / project_id / user_id / compact 边界` 等身份与结构信息。
   - `session` 级回收的“入口条件”与“具体 memory 行是否可删”要拆开处理：
     - 入口条件看 `session.updated_at / pending turn / 队列状态`；
     - 具体 memory 行是否可删，仍要再过一遍各自行的生命周期条件；
     - 不能因为 session 进入候选集，就默认该 session 下所有 session 级记忆都一起删除。

5. 设计 session 回收时对记忆的处理规则，严格区分“未提级 session 级记忆”与“仍有效的高价值长期记忆”。
   - 自动回收仅作用于：
     - `scope_level = session`；
     - 且仍未提级到 `project / user`；
     - 且已经退出当前在线 `active + unexpired` 生效窗口；
     - 且未被保护规则豁免的记忆。
   - 这里不能只看“origin session 很旧”，还必须尊重当前记忆生命周期算法已经写回的时间语义：
     - `defaultUnifiedMemoryExpiry(scope=session)` 当前默认是 `15d`；
     - `evolveAdoptedMemoryRecord` 会在记忆被采纳后刷新 `last_recalled_at / last_adopted_at / last_reinforced_at`，并通过 `chooseLongerMemoryExpiry` 延长 `expires_at`；
     - 因此，只要某条 session 级记忆仍未过期，就不能因为 origin session 很久没写新 turn 而被提前回收。
   - 自动保护规则默认包含：
     - 仍在有效期内的 `priority <= P1` 记忆；
     - 或 `memory_level >= stable` 的记忆；
     - 或 `decay_disabled = true` 的记忆；
     - 或已提级为 `project / user` 的记忆。
   - 这条规则用来承接您提出的“PL 级别在有效期内的数据不清理”。在当前仓库语义里，应映射为：
     - `P` 对应 `priority`；
     - `L` 对应 `memory_level`。
   - 对未被保护的 session 级记忆，不只删除向量，还必须从热主表中移出，否则 lexical / FTS 仍会把它们召回。
   - 为了与现有生命周期算法完全对齐，第一阶段建议把“session 级记忆是否可回收”收敛成保守条件：
     - `expires_at <= now`
     - 且 `max(last_recalled_at, last_adopted_at, last_reinforced_at, created_at) <= now - session_idle_recycle_after`
   - 这样可以避免把“最近仍被强化、只是还没达到 project 提级阈值”的 session 级记忆过早清掉。

6. 设计回收站表，而不是 SQL `TEMP TABLE`。
   - 为了满足“软备份、重启后仍可用于人工防灾排查”，这里不使用进程级临时表，而是新增持久化回收站表。
   - 建议新增以下表：
     - `vmm_recycle_batches`
       - 记录 `batch_id / recycle_type / session_id / project_id / reason / recycled_at / operator / purged_at`。
       - `recycle_type` 需要至少区分：
         - `turn_cold_archive`
         - `session_idle_recycle`
         - `memory_supersede_recycle`
     - `vmm_turn_records_trash`
       - 结构镜像 `vmm_turn_records`，额外记录 `batch_id / recycled_at / recycle_reason`。
     - `vmm_memory_nodes_trash`
       - 结构镜像 `vmm_memory_nodes`，额外记录 `batch_id / recycled_at / recycle_reason`。
     - `vmm_memory_context_edges_trash`
       - 回收被移走记忆对应的 context edge，保证离线审计时上下文证据仍完整。
     - `vmm_vector_gc_jobs`
       - 记录待删除向量任务，承接关系库事务与向量库删除之间的最终一致性。
       - 任务字段至少要覆盖：`job_id / batch_id / vector_id / job_type / attempt_count / next_run_at / claimed_at / completed_at / last_error`，否则后续无法做幂等重试与多 worker 抢占控制。
   - 如需保留画像来源 turn 的审计追踪，可在第一阶段先不做 `profile_nodes_trash`，但前提必须与前文保持一致：
     - turn 回收前必须保证不存在任何仍留在主表中的 `profile_nodes.turn_id = turn_id`；
     - 不能退化成“只检查 active / pending profile”，否则会与前面的 turn 归档约束互相矛盾，并在 `expired / superseded` 画像仍留主表时留下悬空引用。
   - 回收站的职责只到“保留一段时间的软备份与审计线索”为止，不承担产品级恢复能力。
   - 还需要补一个存量治理风险：当前主表里已经可能存在历史 `superseded / expired / deleted` 记忆行。
   - 如果新方案只覆盖“以后产生的替代 / 回收动作”，这些历史存量仍会继续留在热主表，导致表体积和语义长期双轨。
   - 因此计划必须包含一次性存量收敛策略：
     - 至少提供离线迁移或后台补扫，把历史冷状态行逐步迁入回收站或做最终清理；
     - 否则这套方案只能优化增量，不能真正解决现有热表膨胀问题。

7. 设计“移动到回收站”的事务模型，保证主表、回收站、向量清理之间不出现脏链路。
   - 对 `turn` 回收：
     - 在同一 SQL 事务内把主表行插入 `vmm_turn_records_trash`；
     - 成功后删除主表行；
     - 写入 `vmm_recycle_batches` 记录。
   - 对 session 级记忆回收：
     - 在同一 SQL 事务内把 `memory_nodes` 与 `memory_context_edges` 复制到 trash 表；
     - 删除主表 `memory_nodes`、`memory_context_edges`，并按方言做词法索引清理：
       - PostgreSQL 侧 lexical 索引直接依附主表行，不单独维护 FTS 镜像；
       - SQLite 侧仍需同步删除 `vmm_memory_nodes_fts` 镜像行；
     - 把对应 `vector_id` 写入 `vmm_vector_gc_jobs`；
     - 事务提交后再异步删除向量库数据。
   - 对“新记忆替代旧记忆”场景：
     - 与 session idle recycle 共用同一套 `memory -> trash -> vector GC` 出口；
     - 区别只在 `recycle_type = memory_supersede_recycle`；
     - 这样可以保证共享 scope 的旧记忆在被新事实接管后，立即离开热主表，而不是长期停留在 `superseded` 行里。
   - 这样可以保证：
     - 热路径查询永远只看主表；
     - 回收站保留完整离线审计 / 人工防灾素材；
     - 向量删除失败时可继续重试，不影响主事务提交。
   - 但这里还有一个 PG 侧并发风险必须前置处理：
     - 回收扫描、向量 GC、现有 post-action 队列维护都可能同时运行；
     - 如果没有“批次 claim / 行级抢占”机制，多实例或多 goroutine 会重复处理同一批次，甚至互相放大锁冲突。
   - 因此 PostgreSQL 第一阶段必须采用显式 claim 语义：
     - 对 `vmm_vector_gc_jobs` 使用 `FOR UPDATE SKIP LOCKED` 或等价批次抢占；
     - 对 recycle 批次/候选集也要用同类 claim 机制，避免同一批次被重复归档或重复清理；
     - 所有 claim 都要有超时回收与失败重试字段，避免僵尸任务长期占坑。

8. 明确“无恢复能力”的产品边界，避免回收站概念被误解成正式备份系统。
   - VMM 不新增任何恢复相关的 gRPC 接口、usecase、后台 worker 或管理命令。
   - 回收站只提供：
     - 一段有限时间内的数据库层软备份；
     - 批次化审计信息；
     - 供维护人员在极端情况下离线检查的原始数据。
   - 这意味着：
     - 应用层不承诺“点按钮恢复”；
     - 向量库也不设计反向恢复任务；
     - 若未来极端场景需要回灌，只能由维护人员直接做数据库层离线处理，不纳入当前方案实施范围。

9. 设计彻底清理策略，避免回收站本身长期膨胀。
   - 新增“回收站保留时长”配置，默认值为 `30d`。
   - 当 `trash.recycled_at <= now - trash_retention` 时，允许对对应 trash 行做永久删除。
   - 永久删除应满足：
     - 该批次没有未完成的向量删除任务；
     - 若存在历史审计要求，则仅保留最小批次元数据，不再保留原始正文与向量载荷。

10. 设计后台调度模型，避免把重维护逻辑硬塞进当前每 30 秒的轻量维护周期。
   - 现有 worker 的 30 秒 ticker 继续保留给：
     - 过期画像收敛；
     - idle pending session 补扫。
   - 新增独立的“回收维护周期”配置，建议默认 `30m`。
   - 回收维护任务按顺序执行：
     - 冷 turn 回收扫描；
     - 长期空闲 session 回收扫描；
     - 向量 GC 任务执行；
     - 回收站过期批次清理。
   - 这样可以避免重查询、大批量复制删除、向量批量清理频繁打到当前在线 worker 上。
   - 对 PostgreSQL 主实现，还必须明确“扫描”和“执行”分离：
     - 扫描阶段只负责把候选收敛成批次/job；
     - 执行阶段只消费已 claim 的 batch/job；
     - 这样才能把候选判定、事务迁移、向量删除重试拆开，降低长事务和锁竞争。

11. 设计配置结构与默认值，避免把“冷数据治理配置”混进 `pre_check` 或 `memory_pipeline`。
   - 建议新增顶层配置块：`retention`。
   - 第一阶段建议字段如下：
     - `retention.enabled = true`
     - `retention.recycle_scan_interval = "30m"`
     - `retention.turn_keep_extra_turns = 5`
     - `retention.session_idle_recycle_after = "360h"`（15 天）
     - `retention.trash_retention = "720h"`（30 天）
     - `retention.protect_priority_floor = "P1"`
     - `retention.protect_memory_level_floor = "stable"`
     - `retention.skip_protected_shared_memories = true`
     - `memory_replace_scope = "project"`
   - 其中 `turn` 热窗口不是直接配置绝对值，而是运行期动态计算：
     - `max(pre_check.history_turns, post_action.session_analysis_history_turns) + retention.turn_keep_extra_turns`
   - 这样可以保证：
     - 当前默认配置下自动得到 `8`；
     - 如果未来在线检索窗口从 `3` 改到 `5`，热窗口会自动变成 `10`，不用再手工维护第二份绝对值配置。
   - 同时增加校验规则：
     - `turn_keep_extra_turns >= 0`
     - 两个 duration 必须 `> 0`
     - `session_idle_recycle_after` 不能小于当前 session 级默认生命周期窗口；在现有实现里，这个下限应为 `360h`（15 天）
     - `trash_retention > session_idle_recycle_after` 不是强制条件，但应在文档中说明其差异含义。
     - `memory_replace_scope` 只能取 `session / project / space / team`

12. 明确与现有退役算法、衰减算法、提级算法的冲突处理策略。
   - 与记忆退役 / supersede 的关系：
     - `superseded / deleted / expired` 属于记忆事实层状态；
     - recycle 属于冷数据治理层动作；
     - 但新增“共享 scope 替代闭环”后，旧记忆不再只停留在 `superseded`，而是直接并入回收 / 清理出口；
     - 因此需要把“替代退出热主表”和“自然老化回收”统一到同一条退出链上。
   - 与 Weibull 读时衰减的关系：
     - Weibull 继续只影响在线排序分数，不决定是否进入回收站；
     - 回收规则不直接参考 Weibull 分值，避免“低分但仍有效的高优先级记忆”被误删；
     - 但必须尊重 Weibull 依赖的底层强化时间戳，不能在 `last_reinforced_at` 仍然很新的情况下仅因 origin session 老旧就提前删行。
   - 与 session -> project 提级的关系：
     - 只回收仍停留在 `scope_level=session` 的记忆；
     - 已提级到 `project / user` 的记忆不参与 session 回收。
   - 与跨 scope 替代的关系：
     - 当前半套闭环只覆盖同 session 旧记忆；
     - 计划新增的 `memory_replace_scope` 用于把替代边界扩展到 `project / space / team`；
     - 但 scope 越大，误替代风险越高，所以 reviewer 只能替代其 `similar_memories` 输入里明确给出的旧记忆 id，不能自由指向任意历史行。
   - 与默认有效期链路的关系：
     - 当前热路径通过 `activeUnexpiredMemoryCondition` 统一忽略已过期行；
     - 回收只能发生在这条条件已经不再把该记忆视为“在线有效”之后；
     - 否则回收逻辑就会先于现有生命周期算法生效，形成语义冲突。
   - 与 pending 恢复链路的关系：
     - 任何仍有 pending turn 的 session 一律不进入回收候选。

13. 设计实现分期，控制改造风险。
   - 第一阶段：
     - 先补齐 PostgreSQL 主链上的记忆替代闭环，包括 reviewer 输出契约、replace scope 配置与“替代后旧记忆迁入回收站”事务路径；
     - 以 PostgreSQL 适配器为主实现对象，先补齐回收站表、回收扫描能力与清理任务表；
     - 实现 `turn` 回收与 session 级记忆回收；
     - 实现向量 GC 任务表；
     - 同时补一条历史冷状态行的收敛路径，至少覆盖已存在的 `superseded / expired / deleted` 记忆存量；
     - 完成文档与测试闭环。
   - 第二阶段：
     - 将同样的回收语义补齐到 SQLite 适配器，保持接口契约、状态语义与清理顺序一致；
     - 让 `WriteMemories` 复用与 `post-action` 一致的 replace scope 与语义级替代闭环，避免它长期停留在“只有 dedupe_hash 幂等、没有 replace”状态；
     - 同时补齐第一阶段遗留的三项替代稳健性问题，保证统一 reviewer 的替代语义真正达到“候选级闭环”：
       - 替代决策必须从“整轮级 `SupersededMemoryIDs`”收敛到“候选级映射”，避免 reviewer 丢弃某条候选后，该候选对应的旧记忆仍被整轮级 supersede 误退役；
       - admission filter 或其他 reviewer 前置裁剪一旦移除了全部相关记忆候选，必须同步清空对应 supersede 结果，避免“没有任何新记忆入库，但旧记忆仍被退役”；
       - `similar_memories` 的回贴必须改用 `MemoryQueryGroupResult.QueryIndex` 做显式映射，不能继续假设 search 结果切片顺序永远与请求顺序一致，否则后续检索层乱序/跳组后会把旧记忆挂错候选；
     - 上述三项都要在第二阶段与 `WriteMemories` 统一语义一起落地，避免届时一边补工具写链路、一边继续携带第一阶段的候选级错配风险；
     - 校验本地默认运行路径不会因“先做 PostgreSQL”而退化成只支持 PostgreSQL 的实现。
   - 这样既满足“PostgreSQL 作为主要支持对象”，也遵守“不能做 Postgres-only 方案”的仓库约束。

14. 补齐测试与文档计划，确保这不是仅靠口头约束的维护策略。
   - 配置测试：
     - 默认值测试；
     - 校验错误测试；
     - 环境变量 / 配置覆盖测试。
   - 用例测试：
     - 同 session `superseded_memory_ids` 旧能力不回归；
     - 跨 session 共享 scope 下的 `similar_memories -> supersede_memory_ids[]` 替代闭环；
     - `memory_replace_scope = project / space / team / session` 的边界测试；
     - reviewer 丢弃单个记忆候选时，不会继续错误退役该候选对应的旧记忆；
     - admission filter 或 reviewer 前置裁剪导致相关候选全部消失时，不会留下悬空 supersede；
     - search 结果顺序被打乱或跳过空组时，`similar_memories` 仍能通过 `QueryIndex` 正确回贴到原候选；
     - `WriteMemories` 与 `post-action` 在同一 replace scope 下对“保留新记忆 / 替代旧记忆 / 并行补充”三类决策保持一致；
     - `turn` 归档后 `session.turn_count / last_compacted_turn_id / summarize_budget` 的语义不发生未定义漂移；
     - 仍被活跃记忆或画像引用的旧 `turn` 不会被错误归档；
     - 当热路径返回 `source_turn_id > 0` 时，`GetTurnDetails` 仍能读取对应 turn，不出现“返回了 turn_id 但详情为空”的契约回归；
     - 冷 turn 回收候选筛选；
     - 长期空闲 session 回收；
     - 保护高优先级 / 高等级记忆不被误回收；
     - pending session 不被回收；
     - 不新增任何恢复接口，同时回收站批次与主链删除动作保持一致。
   - 存储测试：
     - PostgreSQL“新记忆插入 + 旧记忆迁入 trash + 向量 GC 入队”原子性；
     - PostgreSQL 回收事务完整性；
     - PostgreSQL 回收站过期批次清理；
     - PostgreSQL 向量 GC 失败重试；
     - PostgreSQL 多 worker / 多实例下 `SKIP LOCKED` claim 不重复消费同一 batch/job；
     - 历史 `superseded / expired / deleted` 存量补扫不会污染 active 热表；
     - SQLite 语义对齐回归测试。
   - 文档同步：
     - `README.md`
     - `docs/post-action-guide_CN.md`
     - 新增一份专门的回收与清理说明文档，避免把生命周期维护规则散落在多个文件中。

# 技术选型

1. 使用“持久化回收站表 + 延迟清理”而不是 SQL 临时表。
   - 原因：临时表无法跨进程保留，不满足“软备份与人工防灾缓冲”的要求。

2. 回收站与热主表物理隔离，不在热查询上做 `UNION ALL`。
   - 原因：在线链路只应读取主表，避免每次查询都为冷数据治理付出性能成本。

3. 向量删除采用“SQL 事务写任务表 + 事务后异步删除”的最终一致性模型。
   - 原因：关系库与向量库不是同一事务域，必须通过任务表兜住一致性。

4. 回收判定优先依赖结构化字段，不直接用 LLM 或模糊分值。
   - 原因：这类维护任务必须稳定、可测试、可回放。

5. 高优先级 / 高等级 / 提级后共享记忆默认受保护。
   - 原因：这类数据虽然可能旧，但业务价值更高，不应因 session 冷却而被顺手清掉。

6. 热窗口采用“当前在线检索 / 分析窗口最大值 + 5 条边际缓存”。
   - 原因：这比固定写死一个偏大的保留轮数更严谨，也更符合当前 `precheck` 与 `post-action` 的真实使用边界。

7. 记忆替代与数据回收共用一条数据退出链，而不是分别维护“superseded 软状态”和“后续清理”两套独立流程。
   - 原因：只做 supersede 不做退出，会让旧记忆长期堆积；只做清理不补替代，又会让共享 scope 的旧事实长期停留在 active。

8. 以 PostgreSQL 作为主要支持对象，SQLite 作为必须保持语义一致的兼容实现。
   - 原因：回收站、批次清理与延迟删除这类维护能力更适合先在 PostgreSQL 上做完整事务语义与批量处理设计；但仓库约束决定它最终不能停留在 Postgres-only。

9. session 聚合字段必须先明确定义“历史累计值”还是“热主表即时值”，再允许 turn 归档落地。
   - 原因：如果 `turn_count / summarize_budget / compact 边界` 的解释口径前后不一致，回收方案会把存储层优化变成语义层回归。

10. PostgreSQL 主实现必须使用显式 claim 机制处理 recycle batch 和 vector GC job。
   - 原因：没有 `FOR UPDATE SKIP LOCKED` 一类抢占语义，回收/清理任务在多实例下必然重复执行，甚至与现有 post-action 维护器形成锁竞争。

11. 新方案必须包含历史冷状态行的存量收敛，而不是只覆盖未来新增数据。
   - 原因：当前已经存在的 `superseded / expired / deleted` 旧行如果继续滞留在主表，热表膨胀问题不会真正消失。

12. 第一阶段 turn 冷归档以不破坏 `source_turn_id -> GetTurnDetails` 契约为优先，只承诺机会式减负，不承诺压走全部超过窗口的 turn。
   - 原因：当前多个对外链路都把 `source_turn_id` 当成可追查原始对话的稳定句柄；在没有新增 retained source kind 或 trash 只读详情能力前，不能为了提升归档率而破坏既有接口语义。

# 验收标准

1. 现有同 session `analyze_turn -> superseded_memory_ids` 能力保持不回归。
2. 新增跨 session 共享 scope 的记忆替代闭环后，当项目阶段从 A 更新到 B 时：
   - B 可以在 `memory_replace_scope` 允许的范围内替代 A；
   - A 会立即离开热主表与热召回面；
   - A 的旧向量进入延迟删除任务；
   - 不会再出现“A 和 B 都长期 active”的状态。
3. 默认配置下，`memory_replace_scope = project`，并支持 `session / project / space / team` 合法枚举。
4. 默认配置下，热窗口按 `max(pre_check.history_turns, post_action.session_analysis_history_turns) + 5` 计算；在当前默认配置中，该值应为 `8`。
5. 超过该热窗口、且没有主表依赖的旧 `turn`，会被批次化移入回收站，并记录 `recycled_at`。
6. 若某条热路径结果仍返回 `source_turn_id > 0`，则 `GetTurnDetails` 仍能在主表读取到该 turn；不会出现 dangling `turn_id`。
7. 默认配置下，超过 `15` 天未产生新增活动、且没有 pending turn 的 `session`，会执行回收：
   - `session` 主记录保留；
   - 未提级且未受保护的 session 级记忆从热主表移出；
   - 可下沉的历史 `turn` 进入回收站。
8. 仍在有效期内、且满足保护条件的高优先级 / 高等级 / 共享级记忆不会被自动回收。
9. 回收站中超过 `30` 天的内容会被最终清理，且不会误删仍处于未完成删除任务的批次。
10. 方案不新增任何恢复接口、恢复 worker 或管理命令；回收站仅作为数据库层软备份与人工防灾缓冲。
11. 新增方案不影响：
   - `precheck` 最近 turn 窗口；
   - `post-action` 历史 `details` 组装；
   - pending session 恢复；
   - 现有 Weibull 读时衰减；
   - session -> project 记忆提级。
12. `turn` 归档后，`session` 聚合字段的语义仍然保持一致且文档有明确说明，不出现“字段值存在但含义失真”的灰区。
13. PostgreSQL 主实现下，多 worker / 多实例不会重复消费同一 recycle batch 或同一 vector GC job。
14. 历史 `superseded / expired / deleted` 存量能够被补扫收敛，不再无限滞留在热主表。
15. 配置、用例、存储与文档更新全部补齐，并通过仓库要求的测试集合。

---

## 执行变更总结

### 1. 核心修复与调整概述

1. 本计划所定义的大部分主线能力已经在后续分阶段任务中落地，尤其包括：
   - 跨 scope 记忆替代闭环；
   - `memory_replace_scope` 配置接入；
   - 冷状态记忆回收与 idle-session recycle；
   - recycle trash / vector GC retry / metadata compaction；
   - 多轮全局代码审核后的 correctness 与 stability hardening。
2. 经后续核验，原计划中部分早期判断已被现实实现覆盖，不能再继续作为当前 backlog 直接使用。
3. 当前仍未完全完成的真实遗留项已经被收敛并转移到新的现行计划：
   - `docs/plan/20260405-26-RETENTION_REMAINING_ISSUES_REMEDIATION.md`
4. 因此本计划从“唯一总控计划”转为“历史总控设计记录”，不再继续保留在 `docs/plan/` 目录中作为当前执行入口。

### 2. 📂文件变更清单

新增：

1. 无

修改：

1. `docs/plan/20260404-19-SESSION_RECYCLE_BIN_AND_RETENTION_PLAN.md`

删除：

1. 无

### 3. 💻关键落地结果归纳

1. 已落地的核心能力：
   - `post-action` 与 `WriteMemories` 的记忆替代闭环；
   - retention 独立维护器；
   - 终态记忆回收、session idle recycle、trash purge；
   - SQLite / PostgreSQL 两侧的回收站与向量 GC 重试链路；
   - 针对 pre-check、retrieval、lifecycle、retention 的多轮稳定性修复。
2. 经复核后确认仍未彻底闭环的事项：
   - 独立冷 `turn` 回收扫描 pass；
   - recycle batch 显式批次 claim 与 scan / execute 分离；
   - retention 专题文档；
   - `README.md` 中记忆查询章节与当前 proto 的文档漂移。
3. 已证伪或不再继续追踪的事项：
   - `GRPC_MEMORY_API_SIMPLIFICATION_FOR_AI_TOOLS.md` 不再视为当前未实现主问题来源；
   - pre-check 的同 turn 单代表项与等价文本保留第一条，视为当前确认过的设计取舍。

### 4. ⚠️遗留问题与注意事项

1. 本计划的原始内容已经不再适合作为当前唯一 backlog 真源，后续如继续引用，必须以后续完成计划与现行计划为准。
2. 本计划归档后，真实未完成项统一由 `20260405-26-RETENTION_REMAINING_ISSUES_REMEDIATION.md` 承接。
3. 若未来继续补 retention 工程尾项，应避免重新开启第二份总控计划，防止再次出现“多个计划同时描述同一 backlog”的问题。

## 5. 后验复核补记

本总控计划归档后，结合 `20260405-26` 与后续复核结果，补记两点容易误读的历史差异：

1. 文中早期使用过“`max(pre_check.history_turns, post_action.session_analysis_history_turns) + turn_keep_extra_turns`”描述热窗口。
   - 当前正式实现已经收敛为共享 `post_action.session_analysis_history_turns + retention.turn_keep_extra_turns`。
   - 原因是当前运行时里 `PreCheck` 与 retention 已共用这组历史轮数基线，不再存在独立 `pre_check.history_turns` 参与热窗口计算。
   - 因此这里属于历史设计口径，不再视为当前未完成项。
2. 文中早期还要求 PostgreSQL 主实现“对 recycle batch 自身做显式 claim”。
   - 当前正式实现已经把 scan / claim / execute 分离收敛到 `recycle_jobs` 队列；
   - `recycle_batches` 保留为已完成 trash 批次锚点，不再兼做任务队列。
   - 因此这同样属于实现方案演进后的历史口径，不再视为当前缺陷。
