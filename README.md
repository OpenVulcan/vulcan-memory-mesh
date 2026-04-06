# VMM OSS Local (Go)

VulcanMemoryMesh 当前主线只保留本地版、gRPC 版和三条核心业务链：

- `PreCheck`
- `ChatCompact`
- `PostAction`

另外当前还提供一条与主长期记忆体系隔离的 DWM 支线：

- `ScratchpadUpsert / ScratchpadDelete / ScratchpadGet / ScratchpadClean`

当前运行时的定位是：

- 用 `project_id + user_id + session_id` 做确定性层级寻址
- 默认 `split` 模式使用 SQLite 保存关系数据、用 LanceDB 保存向量数据
- 显式切换 `storage.mode=combined` 后，会改为由 PostgreSQL 统一承载关系与向量能力
- 由 Caddy 等外部反向代理负责 TLS

## 文档导航

- [当前架构与存储模型（中文）](./docs/hierarchy-grpc-design_CN.md)
- [gRPC 对接说明（中文）](./docs/grpc-integration-guide_CN.md)
- [gRPC 接口测试说明（中文）](./docs/api-test-guide_CN.md)
- [post-action 接口说明（中文）](./docs/post-action-guide_CN.md)
- [记忆准入噪声门说明（中文）](./docs/noise-gate-guide_CN.md)
- [PII 规则配置说明（中文）](./docs/pii-validator-config_CN.md)
- [PII Rule Configuration Guide (English)](./docs/pii-validator-config_EN.md)
- [PII 引擎开发说明（中文）](./docs/pii-validator-developer_CN.md)
- [PII Validator Developer Guide (English)](./docs/pii-validator-developer_EN.md)
- [PII 国家与地区规则矩阵（中文）](./docs/pii-country-matrix_CN.md)
- [冷数据回收治理说明（中文）](./docs/retention-governance-guide_CN.md)
- [DWM 确定性工作记忆说明（中文）](./docs/dwm-working-memory-guide_CN.md)
- [DWM Deterministic Working Memory Guide (English)](./docs/dwm-working-memory-guide_EN.md)
- [当前未接入主运行时的配置参数清单（中文）](./docs/unused-config-parameters_CN.md)
- [画像节点生命周期与渲染方案（中文）](./docs/profile-node-lifecycle_CN.md)
- [画像 gRPC 查询与手工指令接口（中文）](./docs/profile-grpc-interfaces_CN.md)

## PII 规则开发与验证

当前仓库保留一套独立的 PII 规则引擎和测试器，主要用于规则研发、误杀排查和离线验证；同一套规则也会在主运行时的 `precheck` / `postaction` / `WriteMemories` 首环节执行请求级脱敏：

- 规则实现位于 `internal/platform/pii`
- 独立测试入口位于 `cmd/vmm-pii-tester`
- 系统规则目录固定为 `configs/pii_rules`
- 用户覆盖目录固定为 `~/.vmm/pii_rules` 或 `-config` 指向根目录下的 `pii_rules`

可以直接使用以下命令编译测试器：

```powershell
.\make.ps1 tester
```

也可以按标准构建一起产出：

```powershell
.\make.ps1 build
```

说明：

- 当前主线 gRPC 运行时并不会自动把这套脱敏器挂接到现有接口链路
- 如需验证或演进规则，请优先使用 `vmm-pii-tester` 或直接调用 `internal/platform/pii`

## 当前运行模型

### 入站协议

当前只暴露 gRPC：

- 服务：`vmm.v1.VMMService`

当前主线方法：

- `Healthz`
- `ListProjects`
- `ResolveProject`
- `EnsureProject`
- `DeleteProject`
- `MigrateProject`
- `ResolveUser`
- `ListUsers`
- `DeleteUser`
- `GetProfileNodes`
- `GetProfileBundle`
- `ApplyProfileInstruction`
- `SearchMemoryEvents`
- `GetTurnDetails`
- `WriteMemories`
- `ScratchpadUpsert`
- `ScratchpadDelete`
- `ScratchpadGet`
- `ScratchpadClean`
- `ChatCompact`
- `PreCheck`
- `PostAction`

### 数据后端

当前主线保留两种正式运行模式：

- `split`（默认）
  - SQLite：关系库存储，负责层级、用户、session、turn、长期记忆、画像与回收治理元数据
    - 适配层优先使用 typed params、`ExecuteBatch` 和 sqlite 网关声明的可重试 trailer 语义
    - schema 升级改为非破坏性 migration，不再因版本变化直接清空历史调试数据
    - 当前版本信息会写入 `vmm_schema_versions`，并保留对旧 `vmm_version` 的兼容同步
  - LanceDB：向量写入、检索和删除
    - 向量 schema 版本与 SQLite 独立跟踪
    - 只有 LanceDB 列结构变化时，启动期才会触发表重建与 SQLite 回灌
- `combined`（显式启用）
  - PostgreSQL：统一承载关系数据、检索索引与向量能力
  - 该模式只在 `storage.mode=combined` 且 `storage.combined_provider=postgres` 时启用
  - PostgreSQL 组合库现在支持受控的 tracked schema 自动升级；当前已覆盖共享 schema `1 -> 2` 的 scratchpad 升级

运行时已经移除：

- HTTP 服务
- 应用内 TLS
- 旧历史兼容 provider 回退路径
- 内存关系库存根 / 内存向量库存根回退

## 核心约束

### 业务入参

`PreCheck`、`ChatCompact` 和 `PostAction` 现在都接受：

- `session_id`
- `user_id`
- `project_id`

说明：

- `user_id`、`project_id` 必须是数字 ID
- `team_id`、`space_id` 不再由客户端传入
- 服务端会先解析 `project_id -> space_id -> team_id`
- 如果 `session_id` 不存在，会自动在 `vmm_sessions` 中创建一条 session 记录
- 如果 `user_id / project_id` 非法或不存在
  - 会在前置范围解析阶段同步直接报错
  - 不会进入 `PreCheck` / `PostAction` 业务逻辑

### PreCheck

当前 `PreCheck` 已恢复为实时两层流程：

- 完成请求校验与范围解析
- 读取最近 turn 窗口，并在 token 预算内混合：
  - 已提炼 turn 的 `details`
  - 未提炼 turn 的脱水原文
- 第一层 `extract_intent` 会结合最近 turn、当前输入和服务端提取的 context hints，先判断是否真的需要长期记忆，再生成带情境锚点的检索语句
- 通过统一记忆检索接口批量向量化这些检索语句并召回长期候选
  - 当前服务端检索链是：`vector + lexical + RRF + rerank(optional) + Weibull + context-aware scoring + MMR`
  - 当前检索过滤范围是已解析出来的 `team_id + space_id + project_id`
  - 同时带 `user_id = 0 OR current_user_id` 过滤，允许共享记忆和当前用户私有记忆一起参与召回
  - 当 `recall_mode=0` 或省略时，会回退到旧版行为，不读取 compact 边界
  - 当 `recall_mode=1` 时：
    - 若当前 session 尚未 compact，会排除当前 session 的 turn-extract 记忆
    - 若当前 session 已 compact，只允许召回 `source_turn_id <= last_compacted_turn_id` 的同 session 历史记忆
  - 当 `recall_mode` 为未来新增的非零值时，当前版本会回退到 compact-aware 基线，避免整段当前 session 被重新开放召回
- 第二层 `review_precheck_memory` 会结合候选摘要、最终分数解释、统一来源解释、累计 support/rebuttal 和当前 query 命中的 context evidence，只采纳对当前请求真正有帮助的候选编号
- 仅对被采纳的记忆写回生命周期计数与有效期
- 只把被采纳的记忆组装为 `context_text / context_items`
  - `PreCheckResponse.context_text` 已废弃，gRPC 返回中固定留空
  - 调用方应直接消费 `context_items[]`
  - 每条 `context_items[]` 仅保留记忆正文、分数、`has_dialogue` 与 `turn_id`
  - 当 `turn_id > 0` 时，可继续调用 `GetTurnDetails`
- 画像读取仍走独立接口：`GetProfileNodes / GetProfileBundle`
- 当某一步降级且没有任何记忆最终被采纳时，会返回空上下文，并把 `degraded=true`

### ChatCompact

当前 `ChatCompact` 用于显式告诉服务端“这个 session 已完成一次上下文压缩”：

- 请求字段仍然只有：
  - `session_id`
  - `user_id`
  - `project_id`
- 服务端会把该 session 当前最新已持久化的 `turn_id` 写入 `vmm_sessions.last_compacted_turn_id`
- 同时写入 `vmm_sessions.last_compacted_timestamp`
- 如果当前 session 还没有任何 turn：
  - 会返回成功
  - 但不会更新 compact 边界
- 如果重复 compact 到同一最新 turn：
  - 会保持幂等
  - 不会重复改写边界

### PostAction

当前 `PostAction` 是新的文本契约入口，只接受：

- `user_content`
- `assistant_content`
- `timeline[]`

服务端流程是：

1. 先解析 `session_id / user_id / project_id`
2. 记录原始日志
3. 清洗 `user_content`、`timeline[].content`、`assistant_content`
4. 记录清洗后日志
5. 先按 `user / timeline / assistant` 组装一条脱水 turn 记录
6. 当 `timeline` 为空时，先过 `NoiseGate`
7. 如果不过滤，则写入 `vmm_turn_records`
8. 同步更新 `vmm_sessions.turn_count / summarize_budget / updated_timestamp`
9. turn 写入成功后，立即返回 `accepted=true`
10. 后台工作器异步读取 pending turn，并发起单轮 `analyze_turn`
11. 单轮分析输入会包含：
    - 最近若干条已提炼完成的历史 `details`
    - 当前 turn 的原始脱水 JSON
    - 当前 session 下仍活跃的旧记忆节点锚点
      - 会携带 `support_count / rebuttal_count` 作为既有证据强度提示
    - 当前 session 在上次提炼观察之后新增的 `recent_grpc_memory_writes`
12. `analyze_turn` 会返回：
    - 当前 turn 的 `user_input_kind`
    - 当前 turn 的 `turn_id`
    - 当前 turn 的 `details`
    - 当前 turn 的 `memory_nodes[]`
      - 每条 `memory_nodes[]` 可选携带 `context_edges[]`
      - 每条 `memory_nodes[]` 现在还允许携带候选级 `supersede_memory_ids[]`
      - 每条 edge 只允许包含 `context_key / context_value / relation(support|rebuttal)`
      - 每条 `memory_nodes[]` 还会携带：
        - `evidence_source`
        - `admission`
        - `admission_reason`
    - 当前 turn 的 `profile_nodes[]`
      - 每条 `profile_nodes[]` 也会携带：
        - `evidence_source`
        - `admission`
        - `admission_reason`
    - 顶层兼容字段 `superseded_memory_ids`
13. `analyze_turn` 的第一层准入会先压缩明显噪音：
    - 用户提问后，助手只是回显既有记忆、既有画像或通识答案时，会优先标记为 `drop`
    - 通过外部检索、访问网站、资料归纳、工具调用发现的新长期事实，仍然允许标记为 `keep`
    - 但实时天气、当前 CPU 温度、当前系统负载等临时态结果，仍会按 `non_durable` 拒绝
14. 如果当前 turn 有新的 `memory_nodes[]` 或 `profile_nodes[]`：
    - 服务端会按与 `PreCheck` 对等的检索作用域召回高相似旧记忆
      - 默认 `space`
      - 支持显式 `team / project`
    - 同时加载当前 user/project 下仍然 `active` 且未过期的画像节点
    - 然后把记忆候选、相似旧记忆、画像活跃节点和新画像候选，一起送入一次 `review_postaction_candidates`
    - 自动提炼与统一评审都要求按领域拆分节点，不能把饮食偏好、生活习惯、编程语言偏好、项目技术栈等无关主题揉成一条综合画像
15. 对统一评审后保留下来的新 `memory_nodes[].abstract` 生成 embedding，并先写入 LanceDB
16. 只有向量侧写入成功后，才会回写当前启用的关系库存储：
    - `vmm_turn_records.details / details_budget / extracted_status`
    - 统一后的 `vmm_memory_nodes`
      - 包含聚合后的 `support_count / rebuttal_count`
    - `vmm_memory_context_edges`
    - `vmm_profile_nodes`
    - 当前轮筛选后的记忆与画像变更会在同一个 `ApplyTurnAnalysis` 写回事务里一起提交，避免只写入一侧造成长期状态脑裂
17. 分析结果日志会输出：
    - `raw_candidates`
    - `final_stored_nodes`
    - `compaction_rate`
    - `admission_drop_count`
    - `review_drop_count`
    - `external_research_kept_count`
18. 后台定时维护仍会做一次过期画像收敛：把到期的 `active` 画像节点标成 `expired`，并重建受影响的 user/project 画像文本
  - `vmm_memory_nodes.vector_id` 与 LanceDB 行 `id` 一一对应
  - LanceDB 行里的 `session_id` 会保存真实来源 session
  - LanceDB 顶层列现在额外包含 `source_turn_id`，用于 compact 边界过滤直接下推到向量检索层
  - `metadata_json` 只保留 `category / details / source_kind / scope_level / priority / memory_level` 这类补充信息
  - 如果 SQLite 在最后回写阶段失败，会反向删除刚写入的 LanceDB 向量行
  - `vmm_teams.profile / vmm_spaces.profile` 不参与 post-action 自动合并，但现在支持通过显式手工画像指令重建

### 画像接口

当前画像相关 gRPC 能力拆成三条独立方法：

- `GetProfileNodes`
- `GetProfileBundle`
- `ApplyProfileInstruction`

`GetProfileNodes` 的特点：

- 只返回单个目标下当前 `active` 的原子化画像节点
- 不提供 `all` 过滤
- 不返回渲染后的 profile Blob
- 返回内容以节点编号、内容、`P/L/W` 相关元数据和来源信息为主

`GetProfileBundle` 的特点：

- 输入固定是：
  - `project_id`
  - `user_id`
- 输出模式支持：
  - `full`
    - 服务端直接返回可注入的组合提示词
    - 这是权威输出，调用方应直接消费 `combined_text`
    - 为避免重复拼接，辅助说明字段和拆分字段会留空
    - 只会保留实际存在正文的 scope；不存在的 `TEAM / SPACE / PROJECT / USER` 不会额外生成对应说明或标签
    - 如果四个 scope 都没有画像正文，则 `combined_text` 直接为空字符串
    - `include_explanation`
      - 省略时默认开启
      - 打开时，会把 `P/L/W` 说明与“当前实际存在的 scope”含义直接内嵌进 `combined_text`
      - 关闭时，只返回正文结构
  - `split`
    - 服务端只分别返回 `[TEAM] / [SPACE] / [PROJECT] / [USER]` 四段正文
    - 不返回完整组合文本
    - `include_explanation` 在该模式下不会额外返回说明字段
- 组合文本固定强调环境约束优先级：
  - `Project > Space > Team`
- 组合结果只为实际存在正文的 scope 显式保留结构标签：
  - `[TEAM]`
  - `[SPACE]`
  - `[PROJECT]`
  - `[USER]`
- 这条接口不触发 LLM，只做确定性拼接

`ApplyProfileInstruction` 的特点：

- 接收单个目标上的显式自然语言画像指令
- 指令不会绑定 `turn_id`
- 关系库存储中这类节点的 `vmm_profile_nodes.turn_id` 会保持 `NULL`
- 服务端会先写入 `vmm_profile_instructions`
- 再把当前 active 节点与这条显式指令交给 `review_profile_instruction`
- 同目标同指令的并发调用会复用第一次进行中的结果，不会重复触发第二次 LLM 评审
- 同一目标上的不同手工指令会按目标串行执行，避免基于同一批旧节点并发写回
- 最后持久化新节点、退役旧节点，并重建对应 scope 的 profile 文本
- 如果一条手工指令同时涉及多个领域，也必须拆成多条画像节点，不能生成跨领域综合节点
- 但“按领域拆分”不等于“一个名词一条节点”：同领域、同语义方向、同生命周期层级的并列事实可以合并进一条节点
- 当默认 SQLite provider 返回 `SQLITE_BUSY / SQLITE_LOCKED / SQLITE_SCHEMA` 且网关通过 trailer 标记为可重试时：
  - 适配层会先做有界指数退避重试
- 当关系库存储 provider 返回类似 `resource deadlock would occur` / `Failed to commit` 的“提交结果不确定”错误时：
  - 服务端会先回查 `vmm_profile_instructions`、`vmm_profile_nodes`、退役状态和最终 profile Blob
  - 如果副作用其实已经落库，则会把这次请求收敛成成功
  - 只有回查也无法确认最终状态时，才会向客户端返回 `STORAGE_OUTCOME_UNCERTAIN`
- 当出现 `STORAGE_OUTCOME_UNCERTAIN` 时，服务端不会立刻把该 instruction 再补写成 `failed`
  - 目的是避免在坏连接或污染连接上继续追加状态写入，把一次不确定提交放大成重复节点或脏状态

目标范围支持：

- `USER`
- `PROJECT`
- `TEAM`
- `SPACE`

权限规则：

- `USER / PROJECT`
  - 手工指令属于高权威输入，但后端仍会按规则施加最低 `P / L` 地板
- `TEAM / SPACE`
  - 手工指令直接视为最高权限规则
  - 后端会强制钳制到最高权威语义，不允许降级成普通偏好或短期上下文

### 记忆查询接口

当前主动记忆查询相关 gRPC 能力拆成两条独立方法：

- `SearchMemoryEvents`
- `GetTurnDetails`

`SearchMemoryEvents` 的特点：

- 输入固定是：
  - `project_id`
  - `user_id`
  - `queries[]`
  - `top_k`
- `queries[]` 是简单字符串数组：
  - 每项代表一条独立检索语句
  - 不再使用 `query_json`
  - 不再使用 `background`
- 服务端会：
  - 先解析 `project_id + user_id`
  - 对每条 query 做裁剪、规范化和 embedding
  - 在当前 `team / space / project / user` 范围内执行统一检索链路
- 返回结果按 query 分组：
  - `results[].query_index`
  - `results[].query`
  - `results[].hits[]`
- 每条命中当前只返回 AI 真正需要的最小字段：
  - `memory_id`
  - `source_turn_id`
  - `abstract`
  - `details_preview`
  - `category`
- 如果 `source_turn_id > 0`：
  - 可以继续调用 `GetTurnDetails`
  - 读取对应 turn 的结构化对话详情

`GetTurnDetails` 的特点：

- 输入固定是：
  - `turn_ids[]`
- 支持单条或多条查询
- 返回的是面向 AI 的结构化 turn 视图，而不是存储导向的脱水 JSON：
  - `user_question`
  - `timeline`
  - `assistant_answer`
  - `detail`
- 同时还会补充当前 turn 在同一 session 中的上下文编号：
  - `previous_turn_ids`
  - `next_turn_ids`
- 默认返回当前 turn 前后各 `3` 轮的编号，方便上层 AI 继续按 id 发起更细的详情查询
- 适合和 `SearchMemoryEvents` 联动：
  - 先通过记忆查询拿到 `source_turn_id`
  - 再按 `source_turn_id` 回查结构化对话和相邻编号

`WriteMemories` 的特点：

- 输入固定是：
  - `session_id`
  - `user_id`
  - `project_id`
  - `items[]`
- 每条 `items[]` 当前只保留最小主动写记忆载荷：
  - `scope_level`
  - `abstract`
  - `details`
  - `category`
  - `priority`
  - `memory_level`
- 服务端会在已解析范围内补齐生命周期、执行短窗口软幂等，并复用统一 reviewer 做语义去重或替代
- 返回只保留最小结果字段：
  - `items[].memory_id`
  - `items[].deduped`

### DWM / Scratchpad 接口

当前还提供一组独立于长期记忆体系的 DWM 接口：

- `ScratchpadUpsert`
- `ScratchpadDelete`
- `ScratchpadGet`
- `ScratchpadClean`

这组接口的定位不是长期记忆，也不是 turn 提炼结果，而是给 AI Agent 保存“当前任务计划、关键步骤、关键文件摘要、关键约束”的确定性工作态。

核心约束：

- 固定定位字段：
  - `project_id`
  - `user_id`
  - `session_id`
- `session_id` 在这条链路里只是字符串定位键：
  - 不会自动创建 `vmm_sessions`
  - 不依赖主 session 生命周期
- 每个 `project_id + user_id + session_id` 只允许存在一个 canonical `plan_name`
- `ScratchpadUpsert`：
  - 允许 `key + value` 单项写入
  - 也允许 `items[]` 批量写入
  - 但两种形式不能混传；一旦混传直接返回校验错误
- `ScratchpadDelete`：
  - 允许 `key` 单键删除
  - 也允许 `keys[]` 批量删除
  - 但两种形式不能混传；一旦混传直接返回校验错误
- `Delete` 在空范围下不会锁定新计划：
  - 直接返回成功
  - `msg = "No scratchpad plan exists for the current session. Create records first."`
- `Get` 在无数据时不会报错：
  - 返回成功
  - `items = []`
  - `msg = "No scratchpad records found for the current session."`
- `Get` 返回 metadata：
  - `plan_name`
  - `item_count`
  - `updated_timestamp`
- `Upsert / Delete` 返回稳定计数字段：
  - `affected_count`
  - `inserted_count`
  - `updated_count`（仅 `Upsert`）
- `Get` 返回的 `plan_name` 永远是当前 canonical 值
- scratchpad 的批量写入/删除采用整批原子语义：
  - 任一 item 非法则整批失败
- scratchpad 不参与 `MigrateProject`
  - 仅参与 `DeleteProject / DeleteUser` 的级联清理

计划守卫规则：

- 完全不一致：
  - 拦截写入
  - 返回英文自然语言提示，要求检查拼写或先执行 `Clean`
- 忽略大小写后一致，但原始拼写不一致：
  - 允许写入
  - 返回消息前缀强插：
    - `[FORMAT DRIFT WARNING] ...`

接口返回约束：

- `status` 使用 proto enum
- `msg` 固定英文
- 如果上层直接把结果暴露给 AI Agent：
  - 调用方应先把 `status enum` 转译成模型更容易理解的文本语义

当前 DWM 持久化模型：

- `vmm_scratchpad_plans`
  - 锁定唯一 `plan_name`
  - `updated_timestamp` 作为整个 scratchpad session 的最后活动时间
- `vmm_scratchpad_nodes`
  - 保存具体 `key/value` 锚点

当前过期治理：

- 超过 `15` 天未更新的 scratchpad session 会被硬删除
- 直接删 `nodes`，再删 `plans`
- 不进入 recycle trash
- 这条清理 pass 复用系统现有的半小时维护时钟

## 构建与运行

标准构建入口只有：

```powershell
.\make.ps1 build
```

或：

```powershell
.\make.bat build
```

标准运行产物：

- `output/bin/vmm-local.exe`

标准配置目录：

- `output/configs/`
- `output/logs/<YYYYMMDD>/<YYYYMMDDHH>.log`

说明：

- 运行时日志会同时输出到 stdout 和文件。
- 标准打包产物默认写入 `output/logs/`。
- 如果是 `go run` 或直接在仓库内调试，日志会写入仓库根目录下的 `logs/`。

### 启动示例

```powershell
.\make.bat build
.\output\bin\vmm-local.exe
```

### 用户覆盖目录

```powershell
.\output\bin\vmm-local.exe -config ~/.vmm
```

`-config` 表示“覆盖根目录”，不是单个配置文件路径。

当前主配置加载顺序是：

- `output/configs/base.yaml`
- `output/configs/config.yaml`
- `~/.vmm/config.yaml` 或 `-config` 指向目录下的 `config.yaml`

说明：

- `base.yaml` 只随项目或打包产物分发，不放到用户目录。
- 用户目录只负责提供覆盖层 `config.yaml`。

### 调试清库

需要调试时，可以继续通过 `make` 调主程序，但把清理目标通过 `--debug-clean` 传给二进制：

```powershell
.\make.bat run --debug-clean sqlite
.\make.bat run --debug-clean lancedb
.\make.bat run --debug-clean postgres
.\make.bat run --debug-clean all
```

说明：

- `make` 只负责透传参数，不在脚本里直接做数据库清理
- `vmm-local --debug-clean ...` 会只连接对应的 SQLite / LanceDB / PostgreSQL 存储后端
- 清理完成后立即退出，不会启动 VMM gRPC 服务

### 调试迁移

当需要把历史 split 模式的 SQLite 事实库迁移到 PostgreSQL 组合库时，可以通过调试迁移命令执行一次性回放：

```powershell
.\make.bat run --debug-migrate split-to-combined
```

说明：

- 迁移源固定为 SQLite，不依赖 LanceDB
- 迁移过程中会在 Go 内存中解析 `vector_json`，然后直接写入 PostgreSQL 的原生 `embedding` 向量列
- 迁移目标使用 `postgres.*` 配置，不要求当前 `storage.mode` 已经切到 `combined`
- 迁移完成后立即退出，不会启动 VMM gRPC 服务

## 配置说明

当前关键配置项：

- `grpc.listen_addr`
- `grpc.max_receive_message_bytes`
- `grpc.request_timeout.workspace`
- `grpc.request_timeout.pre_check`
- `grpc.request_timeout.post_action`
- `logging.level`
- `logging.debug_rpc_payloads`
- `logging.protect_payloads`
- `logging.payload_encryption_key`
- `storage.mode`
- `storage.combined_provider`
- `relational.provider`
- `sqlite.address`
- `postgres.*`
- `lancedb.address`
- `lancedb.table_name`
- `lancedb.vector_column`
- `llm.*`
- `embedding.*`
- `memory_pipeline.*`
- `rerank.*`
- `noise.*`
- `post_action.input_mode`
- `post_action.session_analysis_turn_threshold`
- `post_action.session_analysis_token_threshold`
- `post_action.session_analysis_idle_timeout`
- `post_action.session_analysis_history_turns`
- `post_action.session_analysis_max_input_tokens`

`logging` 下当前与业务链 RPC 载荷调试相关的新增项：

- `level`
  - 支持 `debug` / `info` / `warn` / `error`
  - 默认 `info`
  - release 环境如需尽量只保留错误日志，可直接设为 `error`
  - 也可通过环境变量 `VMM_LOG_LEVEL=error` 覆盖
- `debug_rpc_payloads`
  - 默认 `false`
  - 关闭时：`PreCheck`、`PostAction`、`memory query` 等 payload 相关日志只输出存在性、计数、长度、摘要和执行状态等安全字段，不写正文
  - 开启时：`pre-check received` / `pre-check returned` 会输出请求正文和组装上下文；`pre-check recent turns prepared` / `pre-check intent analyzed` / `pre-check memory query prepared` / `pre-check memory candidates recalled` / `pre-check memory candidates reviewed` / `pre-check lifecycle write-back completed` / `pre-check finalized` 会输出完整阶段诊断；`post-action received raw` / `post-action received cleaned` 会输出正文和 timeline JSON；`post-action turn analysis result`、`memory ... degraded`、`pre-check ... degraded` 等日志会输出完整 payload 诊断内容，便于本地排障
  - 也可通过环境变量 `VMM_LOG_DEBUG_RPC_PAYLOADS=true` 显式开启
  - 建议仅在本地调试或受控环境下临时开启
- `protect_payloads`
  - 默认 `false`
  - 关闭时：当 `debug_rpc_payloads=false`，payload 类日志只保留安全摘要
  - 开启时：当 `debug_rpc_payloads=false`，`PreCheck` 请求/返回与完整阶段诊断、`PostAction`/`memory query` 等 payload 日志会额外写入加密后的 `..._protected` JSON 信封，便于事后审计
  - 也可通过环境变量 `VMM_LOG_PROTECT_PAYLOADS=true` 临时开启
  - 建议与专用密钥一起使用，而不是在没有密钥治理的场景下长期开启
- `payload_encryption_key`
  - 仅在 `logging.protect_payloads=true` 时生效
  - 支持 32 字节原始字符串、64 位 hex，或 base64 编码后的 32 字节密钥
  - 对应环境变量：`VMM_LOG_PAYLOAD_ENCRYPTION_KEY`
  - 推荐通过环境变量注入，不建议把真实密钥直接写进仓库配置文件

日志文件落盘规则：

- 每天一个目录：`logs/<YYYYMMDD>/`
- 每小时一个文件：`<YYYYMMDDHH>.log`
- 例如：`output/logs/20260403/2026040316.log`

`memory_pipeline` 下当前新增的是“向量 + SQLite FTS5 混合召回”参数：

- `max_search_keywords`
  - 仍用于 `PreCheck` 的关键词扇出上限
- `min_similarity_score`
  - 仍用于 `PreCheck` 过滤低质量召回候选
- `hybrid_enabled`
  - 是否启用“向量召回 + SQLite FTS5 lexical 召回 + RRF 融合”链路；关闭时保持纯向量检索
- `lexical_pre_tokenize`
  - 是否启用“应用层 GSE 预分词 + SQLite FTS5 unicode61”模式；默认开启，用来修复中文 BM25 只能把整句汉字当成单个 token 的问题
  - 开启后，记忆写入会先把 `abstract/details` 切成空格分隔 token，再写入独立 FTS5 表；查询时会把原始 query 组装成 tokenized phrase + OR tokens 的 `MATCH` 表达式
  - 关闭时，会回退到仓库原有的正则分词与原始文本索引方式，适合纯英文或需要完全保留旧行为的环境
- `lexical_top_k`
  - 每个 query group 最多取多少条 lexical 候选参与 RRF 融合
- `rrf_k`
  - Reciprocal Rank Fusion 的平滑常数；值越大，不同通道的名次差异被压得越平缓
- `mmr_enabled`
  - 是否在融合或 rerank 之后启用 MMR 多样性控制；开启后会优先保留更分散的候选，减少近重复记忆挤占名额
- `mmr_lambda`
  - MMR 的相关性与多样性权重，越接近 `1` 越偏向原始相关性排序，越接近 `0` 越偏向去重分散
- `weibull_enabled`
  - 是否启用读时 Weibull 衰减；开启后会在融合或 rerank 之后，对陈旧且强化较弱的记忆做自然降权
- `weibull_shape`
  - Weibull 形状参数；值越大，衰减在后段越陡
- `weibull_scale_hours`
  - Weibull 基础时间尺度（小时）；值越大，整体衰减越慢
- `weibull_min_multiplier`
  - 读时衰减的最低保底乘子，避免未过期记忆在排序上被直接打到接近零
- `weibull_reinforce_weight`
  - 强化次数对衰减尺度的放大权重；越大表示“访问强化”效果越明显
- `weibull_cross_session_boost`
  - 跨 session 采纳次数对衰减尺度的额外放大权重，用于保护真正跨任务复用的记忆

`rerank` 下当前新增的是“向量召回后的第二阶段重排序”参数：

- `enabled`
  - 是否启用 rerank 第二阶段重排序；关闭时检索链保持当前首轮召回 / 融合排序结果
- `top_n`
  - 每个 query group 最多送多少条首轮向量命中进入 rerank
- `routes`
  - 当前只认 `rerank.routes[]`
  - 每条 route 必须自包含 `provider + endpoint + model + api_keys/nodes + timeout`
  - 如果不拆 `nodes`，可以直接在 route 上配置 `rpm / tpm / rpd`
  - route 间按 `priority` 做有序容灾，route 内部继续执行 `nodes + key_failover`
  - 当前内置 provider 包含 `dashscope / siliconflow`
  - route 全部失败时，检索链会按 `rerank=false` 语义降级回首轮排序

AI 容灾边界当前统一为：

- `llm`
  - 只支持 `llm.routes[]`
  - 支持多 provider / 多 model / 多 route 的有序容灾
  - 当前内置 provider 包含 `openai / openai_native / openai_go / google_ai_studio`
  - `llm.routes[]` 示例现已包含 `rpm / tpm / rpd`
  - route 之间按 `priority` 切换，route 内部再做 `nodes + key_failover`
- `rerank`
  - 只支持 `rerank.routes[]`
  - 顶层只保留 `enabled / top_n / routes`
  - `rerank.routes[]` 示例现已包含 `rpm / tpm / rpd`
  - 所有 route 都失败时检索链退回首轮排序
- `embedding`
  - 不支持 `routes`
  - 只支持固定 `provider + endpoint + model + dimension` 下的多 key 与 `nodes + key_failover`
  - 当前内置 provider 包含 `openai / openai_native / openai_go / google_ai_studio`
  - `embedding` 示例现已包含顶层 `rpm / tpm / rpd`
  - `nodes` 只负责吞吐分档，不允许跨模型或跨 provider 混用向量空间

当前已彻底移除：

- `llm` 顶层单路由字段
- `rerank` 顶层单路由字段
- 所有位置的单值 `api_key`
- 对应的旧版 `VMM_LLM_*` 与 route 顶层 `VMM_RERANK_*` 运行时覆盖

`post_action` 下当前保留 5 个与异步单轮提炼窗口和恢复扫描相关的参数：

- `session_analysis_turn_threshold`
  - 兼容保留参数，当前主线不会再按“累计待处理 turn 数”触发批量提炼
- `session_analysis_token_threshold`
  - 兼容保留参数，当前主线不会再按“累计待处理 token”触发批量提炼
- `session_analysis_idle_timeout`
  - 当前仍用于后台恢复扫描：如果某个 session 的 pending turn 长时间未被消费，会在超过该阈值后被重新入队
- `session_analysis_history_turns`
  - 每次单轮 `analyze_turn` 最多回带多少条历史 `details` 精要作为参考
- `session_analysis_max_input_tokens`
  - 单次 `analyze_turn` 允许发送给 LLM 的总输入预算上限，统计口径是“当前 turn 原始脱水预算 + 历史精要预算”

当前主线的行为是：

- `PostAction` 成功写入 turn 并完成入队后，后台会尽快发起一次 `analyze_turn`
- `analyze_turn` 会基于“历史精要 + 当前原始 turn + 活跃记忆节点”返回当前这一轮的结构化结果
- 如果本轮有新的 `memory_nodes` 或 `profile_nodes`，会统一走一次 `review_postaction_candidates`
- 这次统一评审会同时处理：
  - 记忆候选的高重复去重
  - user/project 两侧画像候选的接纳、无效、替代与 retire-only 决策
- 如果有新的 `memory_nodes`，会先写入 LanceDB
- SQLite 成功回写后，会更新：
  - `vmm_turn_records.details / details_budget / extracted_status`
  - `vmm_memory_nodes`
  - `vmm_profile_nodes`
- `vmm_profile_nodes` 现在保存原子化画像事实节点，包含：
  - `priority`
  - `profile_level`
  - `level_reason`
  - `refresh_weight`
  - `expires_timestamp`
  - `superseded_by_id`
  - `profile_date`
- `vmm_users.profile / vmm_projects.profile` 不再作为画像合并输入，而是由当前有效画像节点自动重建的渲染结果
- 自动重建出来的 scope `profile` 文本现在只保存正文时间轴，不再带 `[Profile Legend]` 说明头
- 正文仍然按日期输出，并在每条记录上显示 `[P?][L?][W?]`
- 如果调用方需要 `P/L/W` 说明，应通过 `GetProfileBundle` 的 `full` 模式在输出层按需附加；省略 `include_explanation` 时默认开启
- 如果同 session 分析器或统一 reviewer 判定旧记忆已被新事实覆盖：
  - 会把对应 `vmm_memory_nodes.memory_status` 标成 `superseded`
  - 并在关系库提交后删除 LanceDB 旧向量
  - 记忆检索路径只读取 `active` 且未过期的记忆，所以被替代的旧事实不会再次进入热召回
- LanceDB 行里的 `session_id` 会和来源 turn 的 session 保持一致
- 如果 SQLite 回写失败，会尝试回滚这次新增的 LanceDB 向量
- 后台 worker 还会按 `session_analysis_idle_timeout` 周期性补扫陈旧 pending session，帮助崩溃或临时失败后的恢复

另外，当前还有一批“已经声明但尚未接入主运行时”的配置参数，见：

- [当前未接入主运行时的配置参数清单（中文）](./docs/unused-config-parameters_CN.md)

### 超时模型

- `grpc.request_timeout.pre_check`
  - 表示整次 `PreCheck` RPC 的外层预算
- `pre_check.intent_timeout`
  - 表示未来恢复意图提取后，内部子步骤的预算

当前要求：

- `grpc.request_timeout.pre_check > pre_check.intent_timeout`

### PreCheck 检索范围

- `pre_check.search_scope`
  - 控制 `PreCheck` 长期记忆检索的层级范围
  - 可选值：`team`、`space`、`project`
  - 默认值：`space`

当前语义：

- `team`
  - 在当前 team 范围内召回长期记忆
- `space`
  - 在当前 space 范围内召回长期记忆，不再限制到单个 project
- `project`
  - 仅在当前 project 范围内召回长期记忆

这个开关只影响 `PreCheck` 这条链路，不改变通用 `SearchMemoryEvents` 的默认项目级检索语义。

### 记忆更替范围

- `memory_replace_scope`
  - 控制 `PostAction` 统一 reviewer 在“新记忆接管旧记忆”场景下的相似旧记忆召回范围
  - 可选值：`session`、`team`、`space`、`project`
  - 默认值：`project`

当前语义：

- `session`
  - 只允许当前 session 内的新事实替代旧事实
- `project`
  - 允许当前 project 下跨 session 的旧事实被更新事实接管
- `space`
  - 允许当前 space 下相关 project 的共享事实被更新
- `team`
  - 允许当前 team 范围内的稳定规则替代较旧副本

说明：

- 这个开关独立于 `pre_check.search_scope`
- `PreCheck` 的召回范围不会再隐式决定 `PostAction` 的记忆更替范围
- `WriteMemories` 在 24 小时软幂等之后，也会复用同一个 `memory_replace_scope` 做语义去重与替代判断
- 当前统一 reviewer 的记忆结果块已经支持：
  - `accepted_candidates[]`
  - `accepted_candidates[].supersede_memory_ids[]`
  - `dropped_candidates[]`
  - `dropped_candidates[].dedupe_memory_id`
  - 只有当 reviewer 在 `dropped_candidates[].dedupe_memory_id` 中显式指向某条 `similar_memories.memory_id` 时，`WriteMemories` 才会复用已有记忆引用
  - 如果 reviewer 只是丢弃候选，但没有显式给出 dedupe 目标，则 direct-write 会安全降级为新建，而不是误复用第一条旧记忆

### Retention 回收参数

- `retention.enabled`
  - 是否启用冷数据回收治理入口
  - 默认值：`true`
- `retention.recycle_scan_interval`
  - 定时扫描周期
  - 默认值：`30m`
- `retention.turn_keep_extra_turns`
  - 在热窗口基础上额外保留的 turn 数量
  - 默认值：`5`
- `retention.session_idle_recycle_after`
  - session 长期无新增有效信息后允许进入回收判定的阈值
  - 默认值：`360h`
- `retention.trash_retention`
  - 回收站保留时长
  - 默认值：`720h`
- `retention.protect_priority_floor`
  - 受保护共享记忆的优先级下限
  - 默认值：`P1`
- `retention.protect_memory_level_floor`
  - 受保护共享记忆的层级下限
  - 默认值：`stable`
- `retention.skip_protected_shared_memories`
  - 是否跳过受保护的共享记忆
  - 默认值：`true`

当前阶段说明：

- 这组参数已经接入独立 retention 维护器
- 当前维护器每轮会按固定顺序执行：
  - 终态记忆回收
  - 独立冷 `turn` 扫描入队
  - 已领取冷 `turn` 回收任务执行
  - idle-session recycle
  - vector GC retry
  - trash purge
- 当前维护器会周期性回收 `superseded / expired / deleted` 的终态记忆，并把对应 `memory_context_edges` 一并迁入回收站
- 独立冷 `turn` 回收现在使用持久化 `recycle_jobs` 队列做 scan / claim / execute 分离：
  - 扫描阶段只为确实存在可回收旧 `turn` 的 session 入队
  - 执行阶段只消费已 claim 的任务
  - 多 worker 依赖队列租约语义避免重复消费同一条任务
- 当前维护器也会按 `session_idle_recycle_after` 扫描长期空闲 session，并回收：
  - 已过期且长期未被强化的 `session` 级记忆
  - 超出热窗口 `session_analysis_history_turns + turn_keep_extra_turns` 且不再被主表记忆/画像引用的旧 `turn`
- 冷 `turn` 回收与 idle-session 回收都只会迁移“没有主表记忆/画像引用”的旧 `turn`，以保持 `source_turn_id -> GetTurnDetails` 契约不被破坏
- 已执行的回收结果会记录到 `recycle_batches`，只承担当前回收站批次锚点职责，不再作为长期恢复台账
- SQLite 会同步清理 `vmm_memory_nodes_fts` 镜像；PostgreSQL 组合存储模式下不额外走向量 GC
- 超过 `trash_retention` 的回收站批次会被后台 purge 永久清理，覆盖：
  - `memory_nodes_trash`
  - `memory_context_edges_trash`
  - `turn_records_trash`
  - 对应的 `recycle_batches` 批次元数据
- 当前仍不提供产品级恢复接口；回收站仅作为数据库层防灾缓冲

### LanceDB 表名规则

`lancedb.table_name` 现在是基础表名。

运行时会自动追加 embedding 维度：

- 基础表名：`vmm_memory_vectors`
- 维度：`1024`
- 实际表名：`vmm_memory_vectors_1024`

这样可以避免不同向量维度共用同一张物理表。

## 开发验证

提交较大改动前，至少执行：

```powershell
go test ./... -count=1
.\make.bat build
```

## TLS 说明

当前应用内不再支持 TLS。

如果需要 TLS、域名或公网入口，请在前面使用 [Caddy](https://caddyserver.com/) 做反向代理。
