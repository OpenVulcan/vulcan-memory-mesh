# VMM `PostAction` 接口说明（中文）

## 文档目标

这份文档说明当前主线版本唯一有效的 `PostAction` gRPC 契约、清洗流程、噪声门位置，以及当前如何在单轮写入后先稳定落库并立即返回，再由后台异步完成 LLM 提炼，并把结果写入当前启用的存储后端。

当前默认 `split` 模式下的 SQLite 关系库存储适配层会优先使用：

- typed params
- `ExecuteBatch`
- sqlite 网关标记为可重试错误时的有界退避

当前相关方法只有：

- `vmm.v1.VMMService/PostAction`

这不是历史兼容说明。旧 HTTP、旧 `PostActionOld`、旧 `raw_messages_snapshot` 契约都不再属于当前主线。

## 接口定位

`PostAction` 是当前主业务写入入口，用于接收一段已经整理过的纯文本对话片段：

- 顶层首轮用户提问：`user_content`
- 中间时间线：`timeline[]`
- 顶层最终助手回答：`assistant_content`

服务端会：

1. 先校验 `session_id / user_id / project_id`
2. 记录原始日志
3. 对待存储文本执行结构性清洗
4. 记录清洗后日志
5. 在 `PostActionUseCase` 入口执行一次 PII 脱敏
6. 先把脱敏后的 turn 脱水记录稳定写入当前启用的关系库存储
7. 把后续 LLM 分析转交给 session 队列，并复用已脱敏的持久化数据继续推进，最后立即返回 `accepted=true`

## 请求结构

当前 proto 定义位于：

- [internal/adapters/inbound/grpcapi/proto/v1/vmm.proto](../internal/adapters/inbound/grpcapi/proto/v1/vmm.proto)

核心请求结构如下：

```proto
message PostActionRequest {
  string session_id = 1;
  uint64 user_id = 2;
  uint64 project_id = 3;
  string user_content = 4;
  string assistant_content = 5;
  repeated PostActionTimelineItem timeline = 6;
}

message PostActionTimelineItem {
  string type = 1;
  string content = 2;
}
```

## 字段规则

### 顶层范围字段

- `session_id`
  - 必填
  - 业务会话键
- `user_id`
  - 必填
  - 必须是数字 ID
- `project_id`
  - 必填
  - 必须是数字 ID

说明：

- 客户端不再传 `team_id`
- 客户端不再传 `space_id`
- 服务端会通过统一前置拦截器，根据 `project_id` 反查 `team_id / space_id`
- 如果目标 `project_id` 下不存在该 `session_id`，服务端会自动创建一条 `vmm_sessions` 记录
- 如果目标 `project_id` 下已经存在该 `session_id`，服务端会直接复用这条 `session`

### 顶层文本字段

- `user_content`
  - 必填
  - 必须是字符串
  - 表示当前这一段对话的首轮用户提问
- `assistant_content`
  - 必填
  - 必须是字符串
  - 表示当前这一段对话的最终助手回答

### `timeline`

- `timeline` 可以为空数组
- `timeline` 每一项都必须包含：
  - `type`
  - `content`

其中：

- `type` 只能是：
  - `user`
  - `assistant`
- `content` 必须是字符串且非空

## 时间线语义

`timeline` 只表示中间流程，不包含顶层的边界文本。

也就是说，最终的业务顺序永远是：

1. `user_content`
2. `timeline[0..n]`
3. `assistant_content`

这意味着：

- `user_content` 一定是首轮问题
- `assistant_content` 一定是最后回答
- `timeline` 是中间被插入的补充提问、追问或中断回答

## 完整执行顺序

当前 `PostAction` 的完整链路如下：

1. gRPC 入口收到请求
2. 轻量规范化：
   - trim `session_id`
   - trim `user_content`
   - trim `assistant_content`
   - `timeline[i].type` 转小写
   - trim `timeline[i].content`
3. 传输层校验：
   - `session_id` 必填
   - `user_id` 必须为数字 ID
   - `project_id` 必须为数字 ID
   - `user_content` 必填
   - `assistant_content` 必填
   - `timeline[*].type/content` 必须合法
4. 统一范围拦截器解析：
   - 检查 `user_id`
   - 检查 `project_id`
   - 反查 `team_id / space_id`
   - 必要时创建 `session`
   - 同一 `project_id + session_id` 已存在时直接复用，即使调试阶段上游手动切换过 `user_id` 也不会拦截
   - 如果 `user_id = 0` 或 `project_id = 0`
     - 会同步返回 `InvalidArgument`
   - 如果 `user_id` 或 `project_id` 不存在
     - 会同步返回 `NotFound`
   - 这类错误发生在进入用例层之前
     - 不会返回 `accepted=true`
     - 不会进入后台 goroutine
5. 记录原始请求日志
6. 对待存储文本执行清洗：
   - `user_content`
   - `timeline[*].content`
   - `assistant_content`
7. 记录清洗后日志
8. 立即返回：
   - `accepted = true`
   - `trace_id`
9. 后台 goroutine 调用 `PostActionUseCase.Execute`
10. 如果 `timeline` 为空且启用了 `NoiseGate`：
    - 先把顶层 `user_content + assistant_content` 视作一个简单单轮
    - 经过噪声门判断是否值得入库
11. 组装一条标准 turn：
    - 顶层 `user_content`
    - 清洗后的 `timeline[*]`
    - 顶层 `assistant_content`
12. 对 turn 做脱水：
    - 顶层 `user_content` 保留
    - `timeline[*]` 按顺序整体保留
    - 顶层 `assistant_content` 保留
    - 这里的“脱水”指的是整理成稳定 JSON 分析单元，不再对 `timeline[*].type=assistant` 做二次占位替换
13. 计算脱水 JSON 的 token 预算
14. 追加到关系库存储（默认 `split` 模式为 SQLite；`combined` 模式为 PostgreSQL）：
    - `vmm_turn_records`
    - 同步更新 `vmm_sessions.turn_count / summarize_budget / updated_timestamp`
15. turn 写入成功后，会由后台队列异步发起一次单轮 `analyze_turn`
16. `analyze_turn` 请求会包含：
    - 最近若干条已提炼历史 `details`
    - 当前 turn 的原始脱水 JSON
    - 当前 session 下仍然活跃的旧记忆节点
      - 这些节点现在会额外携带 `support_count / rebuttal_count`
    - 其中：
      - 历史部分只用于参考
      - 当前 turn 是唯一允许输出新 `details / memory_nodes / profile_nodes` 的目标
      - 活跃记忆节点用于去重与覆盖判断
17. `analyze_turn` 会返回：
    - 当前 turn 的 `user_input_kind`
    - 当前 turn 的 `turn_id`
    - 当前 turn 的 `details`
    - 当前 turn 的 `memory_nodes[]`
      - 每条 `memory_nodes[]` 现在允许可选 `context_edges[]`
      - 每条 `memory_nodes[]` 现在还允许携带候选级 `supersede_memory_ids[]`
      - 每条 edge 只允许包含 `context_key / context_value / relation(support|rebuttal)`
      - 每条 `memory_nodes[]` 还会明确给出：
        - `evidence_source`
        - `admission`
        - `admission_reason`
    - 当前 turn 的 `profile_nodes[]`
      - 每条 `profile_nodes[]` 也会明确给出：
        - `evidence_source`
        - `admission`
        - `admission_reason`
    - 顶层兼容字段 `superseded_memory_ids`
18. `analyze_turn` 的第一层准入会先压缩明显噪音：
    - 如果当前轮主要是用户提问或下指令，而助手只是基于既有记忆、既有画像或通识能力完成回答：
      - 这类候选通常会被标记为 `admission="drop"`
      - 常见原因包括：
        - `qa_answer_only`
        - `derived_from_existing_memory`
        - `derived_from_profile_echo`
        - `general_knowledge_answer`
    - 如果当前轮虽然是用户提问或下指令，但助手确实通过高成本外部检索、访问网站、资料归纳、工具调用或系统查询得到新的长期业务价值信息：
      - 这类候选仍可标记为 `admission="keep"`
    - 如果外部结果只是临时态、瞬时运行态或短期观测值，例如天气、当前 CPU 温度、当前系统负载：
      - 仍应标记为 `admission="drop"`
      - `admission_reason` 应为 `non_durable`
    - 如果当前 turn 只是“用户询问 AI 自己的喜好/习惯/画像是什么”，而回答只是助手基于上下文做的复述、猜测或迎合性总结：
      - 不应提炼成长期记忆
      - 也不应提炼成画像节点
      - 只有当用户自己明确确认、补充、纠正或直接陈述这些偏好时，才允许进入长期画像系统
19. 如果当前 turn 有新的 `memory_nodes[]` 或 `profile_nodes[]`：
    - 会按专用的 `memory_replace_scope`，先做一次高相似旧记忆召回：
      - 默认是 `project`
      - 支持显式 `session / team / space / project`
      - 当作用域是 `session` 时，会额外带上 `session_id` 过滤，确保只替代当前 session 旧事实
      - 这个搜索空间独立于 `pre_check.search_scope`
    - 如果当前 turn 有画像候选：
      - 会先加载当前仍然 `active` 且未过期的 user/project 画像节点
    - 然后把：
      - 本轮新记忆候选
      - 每条记忆候选对应的高相似旧记忆
      - 当前 user/project 活跃画像节点
      - 本轮新画像候选
      一起送入一次统一的 `review_postaction_candidates`
    - 这个统一 reviewer 会同时输出：
      - 哪些记忆候选应保留
        - 通过 `memory.accepted_candidates[]`
      - 每条保留的记忆候选是否应替代哪些旧记忆
        - 通过 `memory.accepted_candidates[].supersede_memory_ids[]`
      - 哪些记忆候选应丢弃
        - 通过 `memory.dropped_candidates[]`
      - 某条被丢弃记忆候选是否应复用一条旧记忆
        - 通过 `memory.dropped_candidates[].dedupe_memory_id`
        - 这个 id 只能引用该候选自己看到过的 `similar_memories.memory_id`
      - 哪些画像候选应接纳为新画像节点
      - 哪些画像候选应判定为 `invalid`
      - 哪些旧画像节点需要 `superseded` 或 `retire_only`
    - 统一评审与自动提炼都要求按领域拆分画像节点，不能把饮食偏好、生活习惯、编程语言偏好、项目技术栈等无关主题揉成一条综合画像
20. 如果当前 turn 有统一评审后保留下来的 `memory_nodes[]`：
    - 会先对每条 `memory_nodes[].abstract` 做 embedding
    - 先把新向量写入 LanceDB
    - LanceDB 行 `id` 会回填成对应 `memory_nodes[].vector_id`
21. 只有新向量写入成功后，才会回写当前启用的关系库存储：
    - 更新当前 turn：
      - `details`
      - `details_budget`
      - `extracted_status = 1`
    - 同步插入：
      - `vmm_memory_nodes`
        - 包含聚合后的 `support_count / rebuttal_count`
      - `vmm_memory_context_edges`
      - `vmm_profile_nodes`
    - 同步更新：
      - `vmm_users.profile`
      - `vmm_projects.profile`
    - 这里的 `vmm_users.profile / vmm_projects.profile` 不再是 LLM 直接输出的大 Blob
      - 而是后端根据当前有效画像节点自动重建的正文时间轴文本
      - 每条记录会带：
        - `P`
        - `L`
        - `W`
      - 但不会把 `P / L / W` 说明头长期存入 scope 字段
    - 当前轮最终保留下来的记忆与画像变更会在同一个 `ApplyTurnAnalysis` 写回事务中一起提交，避免只写入一侧导致长期记忆状态脑裂
22. 如果画像评审中有旧画像节点需要被替代：
    - 会把旧画像节点标成 `superseded`
    - 新画像节点会写入：
      - `priority`
      - `profile_level`
      - `level_reason`
      - `refresh_weight`
      - `expires_timestamp`
      - `superseded_by_id`
      - `profile_date`
23. 如果记忆评审中确认旧记忆已被新事实覆盖：
    - 会优先沿用分析器候选级 `memory_nodes[].supersede_memory_ids[]` 处理同 session 替代
    - 顶层 `superseded_memory_ids` 只作为兼容回退，不会在候选被过滤或被 reviewer 丢弃后继续盲目沿用
    - 也会把统一 reviewer 返回的 `supersede_memory_ids[]` 合并进最终写回事务
    - 这些 `supersede_memory_ids[]` 只能引用该候选实际看到过的 `similar_memories.memory_id`
    - 事务提交后会删除对应 LanceDB 旧向量，避免旧事实继续占用热索引
24. 后台日志现在会额外记录压缩诊断字段：
    - `raw_candidates`
    - `final_stored_nodes`
    - `compaction_rate`
    - `admission_drop_count`
    - `review_drop_count`
    - `external_research_kept_count`
    - 其中 `compaction_rate = (Raw_Candidates - Final_Stored_Nodes) / Raw_Candidates`
25. 后台定时维护还会额外做一次过期画像收敛：
    - 会查找已经超过 `expires_timestamp` 的 `active` 画像节点
    - 把这些节点批量标记成 `expired`
    - 读取受影响 user/project 当前剩余的 `active` 节点
    - 由后端重新渲染 `vmm_users.profile / vmm_projects.profile`
26. 在 `split` 模式下，如果 LanceDB 已写入新向量，但关系库异步回写失败：
    - 会尝试按这次新生成的 `vector_id` 反向删除 LanceDB 行
    - 避免 `extracted_status=0` 却残留孤立新向量
27. 当前限制：
    - 仍不自动更新 `vmm_teams.profile / vmm_spaces.profile`

## 后台维护细节

当前后台维护仍然是单 worker goroutine，不是多 worker 并行。

它现在不再承担旧的 batch 聚合提炼，但仍负责异步队列恢复相关维护：

1. 每 30 秒执行一次过期画像收敛
2. 每 30 秒扫描一次超过 `session_analysis_idle_timeout` 的 pending session，并重新入队恢复单轮提炼
3. 在共享存储连接出现死锁/污染症状时进入短暂退避

### 历史窗口与预算规则

当前单轮 prompt 的规则是：

- 当前 turn 使用原始脱水 JSON
- 历史只使用已经提炼完成的 `details`
- 历史默认最多回带 `session_analysis_history_turns`
- 单次总输入预算上限由 `session_analysis_max_input_tokens` 控制
- 历史预算的统计口径是历史 `details_budget`
- 当前预算的统计口径是目标 turn 的 `dehydrated_budget`

换句话说，当前 LLM 单轮提炼不是“历史原文 + 当前原文”，而是：

- 历史精要
- 当前原始 turn
- 旧记忆锚点

## 定时监测流程

当前后台维护 worker 每 30 秒会做一件事。

### 1. 过期画像收敛

它会查找：

- `profile_status = active`
- 且 `expires_timestamp <= now`

的画像节点，并把它们真实落成 `expired`。

完成后会：

1. 读取受影响 user/project 当前剩余的 `active` 节点
2. 由后端重新渲染对应的：
   - `vmm_users.profile`
   - `vmm_projects.profile`

所以，画像过期不是只在读取时临时过滤，而是会被后台扫描真实收敛到数据库状态中。

## 状态机速览

### Turn 提取状态

- `0 = pending`
- `1 = done`

语义：

- `pending`
  - turn 已写入关系库存储，但还没完成 LLM 提炼
- `done`
  - `details / memory_nodes / profile_nodes` 已完成回写

### Memory 节点状态

- `0 = active`
- `1 = superseded`
- `2 = deleted`

语义：

- `active`
  - 仍参与后续记忆检索与淘汰判断
- `superseded`
  - 已被更新记忆覆盖
- `deleted`
  - 被判定无效，不再使用

### Profile 节点状态

- `0 = invalid`
- `1 = pending`
- `2 = active`
- `3 = superseded`
- `4 = expired`

语义：

- `invalid`
  - 新候选无长期价值，但保留备案
- `pending`
  - 等待评审或重试
- `active`
  - 当前有效，并参与画像渲染
- `superseded`
  - 被更近的新节点替代
- `expired`
  - 生命周期自然到期

## 清洗行为

当前 `PostAction` 在入库前会先执行面向长期存储的文本清洗，并在用例入口执行一次 PII 脱敏；后续队列、分析与评审阶段复用这份已经脱敏的持久化文本，不再重复执行同链路脱敏。

主要目标是：

- 去掉对长期记忆没有价值、但会污染 token 预算或语义的噪声
- 在继续进入噪声门、写库和后续 LLM 单轮分析前，先把请求中的敏感信息替换成脱敏占位符
- 保留尽量稳定、可追溯的纯文本内容

当前会处理的内容包括：

- `<think>...</think>`
- base64 图片/音频/视频片段
- Markdown 图片链接
- HTML 媒体标签
- 裸露媒体 URL
- 长代码块、长日志、长 JSON、长堆栈
- 超过 token 预算的超长文本

## 噪声门位置

`NoiseGate` 不在 gRPC 入口层执行，而是在后台用例层执行。

具体位置：

1. 请求先通过传输层校验
2. 文本先完成结构性清洗
3. 后台进入 `PostActionUseCase`
4. 在 `PostActionUseCase` 入口执行一次 PII 脱敏
5. 当 `timeline == 0` 时：
   - 才执行一次简单单轮噪声过滤
6. 当 `timeline > 0` 时：
   - 直接跳过噪声门

原因：

- 带 `timeline` 的流程通常表示多步补充、打断或回合穿插
- 这类流程不适合用单轮噪声门做简单拦截

## 最终写入的表

当前主线会把关系数据写入当前启用的关系库存储（默认 `split` 模式为 SQLite；`combined` 模式为 PostgreSQL），逻辑表包括：

- `vmm_sessions`
- `vmm_turn_records`
- `vmm_memory_nodes`
- `vmm_profile_nodes`

其中，当前自动重建会额外更新：

- `vmm_users.profile`
- `vmm_projects.profile`

在 `split` 模式下，还会在 LanceDB 中写入当前 turn 提炼出的记忆向量：

- 行主键：`id`
- 关联键：与 `vmm_memory_nodes.vector_id` 一一对应
- 向量来源：`memory_nodes[].abstract`
- `session_id`：保存真实来源 session，而不是占位值
- `source_turn_id`：保存真实来源 turn，供 compact 边界过滤直接下推到向量层
- 元数据中会附带：
  - `turn_id`
  - `category`
  - `details`

其中：

- `session_id / user_id / project_id / source_turn_id` 已经作为 LanceDB 顶层列存在
- `metadata_json` 只保留真正需要补充的富化字段，避免重复存储

不会直接把原始请求 JSON 原样写入数据库。

真正持久化的是清洗后的 turn 脱水 JSON。

另外，以下层级表已经增加 `profile` 字段，用于为后续画像 Blob 合并预留结构：

- `vmm_users`
- `vmm_teams`
- `vmm_spaces`
- `vmm_projects`

当前主线的行为是：

- 自动重建并更新：
  - `vmm_users.profile`
  - `vmm_projects.profile`
- 暂不自动合并：
  - `vmm_teams.profile`
  - `vmm_spaces.profile`

## 响应结构

当前响应定义：

```proto
message PostActionResponse {
  bool accepted = 1;
  string trace_id = 2;
}
```

典型返回：

```json
{
  "accepted": true,
  "traceId": "trc_xxx"
}
```

说明：

- `accepted=true` 只表示“请求已被接收”
- 不表示后台一定已经写库完成
- 如果后台写库失败，会体现在运行日志里，而不是同步响应里
- 如果 `user_id / project_id` 在前置范围解析阶段就失败
  - 则不会返回 `accepted=true`
  - 而是直接返回同步 gRPC 错误

## grpcurl 示例

```powershell
grpcurl -plaintext `
  -d '{
    "sessionId": "sess_001",
    "userId": 7,
    "projectId": 9,
    "userContent": "最开始的问题",
    "assistantContent": "最后的回答",
    "timeline": [
      { "type": "assistant", "content": "中间回答" },
      { "type": "user", "content": "补充问题" }
    ]
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/PostAction
```

## 相关配置

示例：

```json
{
  "grpc": {
    "max_receive_message_bytes": 1048576,
    "request_timeout": {
      "post_action": "8s"
    }
  },
  "logging": {
    "debug_rpc_payloads": false,
    "protect_payloads": false,
    "payload_encryption_key": "${VMM_LOG_PAYLOAD_ENCRYPTION_KEY}"
  },
  "post_action": {
    "input_mode": "compat",
    "session_analysis_turn_threshold": 2,
    "session_analysis_token_threshold": 12000,
    "session_analysis_idle_timeout": "15m",
    "session_analysis_history_turns": 3,
    "session_analysis_max_input_tokens": 6000
  }
}
```

当前真正相关的配置项：

- `grpc.max_receive_message_bytes`
- `grpc.request_timeout.post_action`
- `logging.level`
- `logging.debug_rpc_payloads`
- `logging.protect_payloads`
- `logging.payload_encryption_key`
- `post_action.input_mode`
- `post_action.session_analysis_turn_threshold`
- `post_action.session_analysis_token_threshold`
- `post_action.session_analysis_idle_timeout`
- `post_action.session_analysis_history_turns`
- `post_action.session_analysis_max_input_tokens`
- `noise.enabled`
- `noise.default_language`
- `noise.semantic_enabled`
- `noise.semantic_threshold`

其中：

- `post_action.session_analysis_turn_threshold`
  - 兼容保留参数，当前主线不再按累计待处理 turn 数触发延后提炼
- `post_action.session_analysis_token_threshold`
  - 兼容保留参数，当前主线不再按累计待处理 token 触发延后提炼
- `post_action.session_analysis_idle_timeout`
  - 当前仍用于后台恢复扫描：如果某个 session 的 pending turn 长时间未被消费，会在超过该阈值后被重新入队
- `post_action.session_analysis_history_turns`
  - 表示每次单轮 `analyze_turn` 最多回带多少条历史 `details` 精要
- `post_action.session_analysis_max_input_tokens`
  - 表示单次 `analyze_turn` 允许送给 LLM 的总输入预算上限
- `logging.level`
  - 支持 `debug` / `info` / `warn` / `error`
  - 默认 `info`
  - release 环境如果只想保留错误日志，可以直接设成 `error`
  - 也可以通过环境变量 `VMM_LOG_LEVEL=error` 临时覆盖
- `logging.debug_rpc_payloads`
  - 默认关闭
  - 关闭时：`PostAction`、`PreCheck`、`memory query` 等 payload 相关日志只输出安全元信息，不记录正文
  - 开启时：`post-action received raw`、`post-action received cleaned`、`pre-check received`、`pre-check returned` 以及 `pre-check recent turns prepared` / `pre-check intent analyzed` / `pre-check memory query prepared` / `pre-check memory candidates recalled` / `pre-check memory candidates reviewed` / `pre-check lifecycle write-back completed` / `pre-check finalized` 等完整阶段日志会输出正文、timeline JSON、分析 JSON 或 query 文本，便于本地排障
  - 也可以通过环境变量 `VMM_LOG_DEBUG_RPC_PAYLOADS=true` 临时开启
  - 建议只在临时调试时开启
- `logging.protect_payloads`
  - 默认关闭
  - 当 `logging.debug_rpc_payloads=false` 时，开启后会把 `PreCheck` 请求/返回、完整阶段日志，以及 `PostAction` / `memory query` 等 payload 类日志额外记录为加密的 `..._protected` JSON 信封，默认输出里仍不出现明文
  - 也可以通过环境变量 `VMM_LOG_PROTECT_PAYLOADS=true` 临时开启
- `logging.payload_encryption_key`
  - 仅在 `logging.protect_payloads=true` 时使用
  - 支持 32 字节原始字符串、64 位 hex 或 base64 编码后的 32 字节密钥
  - 也可以通过环境变量 `VMM_LOG_PAYLOAD_ENCRYPTION_KEY` 注入

运行时日志落盘规则：

- 日志会同时输出到 stdout 和文件
- 标准打包产物默认写入：`output/logs/<YYYYMMDD>/<YYYYMMDDHH>.log`
- 如果是在仓库里直接调试运行，则会写入仓库根下的：`logs/<YYYYMMDD>/<YYYYMMDDHH>.log`

当前已经接入的行为是：

- `PostAction` 成功写入 turn 并完成入队后，后台会尽快触发一次 `analyze_turn`
- `analyze_turn` 会基于“历史精要 + 当前原始 turn + 活跃记忆节点”返回当前这一轮的结果
- 如果本轮有新的 `memory_nodes` 或 `profile_nodes`，会统一走一次 `review_postaction_candidates`
- `review_postaction_candidates` 会同时返回：
  - 记忆候选的保留/丢弃结果
  - user/project 两侧画像候选的接纳、无效、替代与 retire-only 结果
- 后端会把新候选落成原子化画像节点，并自动重建 `vmm_users.profile / vmm_projects.profile`
- `vmm_profile_nodes.profile_status` 会记录当前节点是 `active / invalid / superseded / pending / expired`
- `vmm_profile_nodes` 会额外记录：
  - `priority`
  - `profile_level`
  - `level_reason`
  - `refresh_weight`
  - `expires_timestamp`
  - `superseded_by_id`
  - `profile_date`
- 如果有新的 `memory_nodes`，会先写入 LanceDB
- SQLite 成功回写后会更新：
  - `vmm_turn_records.details / details_budget / extracted_status`
  - `vmm_memory_nodes`
  - `vmm_profile_nodes`
- `vmm_memory_nodes.vector_id` 会关联 LanceDB 行 `id`
- LanceDB 行里的 `session_id` 会和来源 turn 的 session 保持一致
- 如果 SQLite 最后回写失败，会尝试回滚这次新增的 LanceDB 向量
- `profile` 渲染文本现在只保存正文时间轴，不再固定带 `P / L / W` 说明头
- 如果调用方需要组合后的帮助说明，应通过 `GetProfileBundle.include_explanation=true` 让服务端在输出层附加
- 仍然不自动更新 `vmm_teams.profile / vmm_spaces.profile`

## 当前限制

- 当前主线不再支持旧的 `raw_messages_snapshot` 契约
- 当前主线不再支持 `team_id / space_id` 由客户端直接传入
- 当前 `PostAction` 只接受纯文本字段，不接受原始消息节点对象
- 当前 `PostAction` 在返回前只保证 turn 落库成功并完成异步入队，不会等待单轮提炼和结果回写完成
- 当前 `vmm_sessions.summarize_content` 仍未开始维护宏观会话总结正文
- 当前只会自动合并 user/project 画像，team/space 画像仍需后续显式配置
