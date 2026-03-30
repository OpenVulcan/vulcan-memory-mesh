# VMM `PostAction` 接口说明（中文）

## 文档目标

这份文档说明当前主线版本唯一有效的 `PostAction` gRPC 契约、清洗流程、噪声门位置，以及当前如何通过后台队列把结果写入 DuckDB 与 LanceDB。

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
3. 清洗待存储文本
4. 记录清洗后日志
5. 立即返回 `accepted=true`
6. 后台继续把脱水后的 turn 记录写入 DuckDB，并把后续 LLM 分析转交给 session 队列

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
14. 追加到 DuckDB：
    - `vmm_turn_records`
    - 同步更新 `vmm_sessions.turn_count / summarize_budget / updated_timestamp`
15. turn 写入成功后：
    - 不在主写链路里直接调 LLM
    - 而是把当前 `session` 投递到后台队列
16. 后台队列有两条工作线：
    - 优先消费显式入队的 `session`
    - 每 30 秒扫描一次“超过空闲阈值且仍有待处理 turn”的 `session`
17. 当满足任一条件时，会触发一次批量分析：
    - 待处理 turn 数达到 `session_analysis_turn_threshold`
    - 待处理 turn 的累计 token 预算达到 `session_analysis_token_threshold`
    - 距离最后一次会话更新时间超过 `session_analysis_idle_timeout`
18. 触发后会组装一份 `analyze_session_batch` 请求：
    - 最近若干条已提炼历史 `details`
    - 当前仍未提炼的原始 turn
    - 当前 session 下仍然活跃的旧记忆节点
    - 其中：
      - 历史部分只用于参考
      - 待处理部分必须逐条输出对应结果
      - 活跃记忆节点会带：
        - `turn_id`
        - `memory_node_id`
        - `vector_id`
19. `analyze_session_batch` 会返回：
    - 每条待处理 turn 的：
      - `details`
      - `memory_nodes[]`
      - `profile_nodes[]`
    - 以及：
      - `obsolete_memory_turn_ids`
20. 如果批次里有 `profile_nodes[]`：
    - 会先加载当前仍然 `active` 且未过期的 user/project 画像节点
    - 把这些活跃节点与本批次新画像候选一起送入一次 `review_profile_nodes`
    - 如果本批次只有 user 或只有 project 候选，则只发送存在的一侧
    - `review_profile_nodes` 会分别返回：
      - `user`
      - `project`
    - 每个结果块都会给出：
      - 哪些候选应接纳为新画像节点
      - 哪些候选应判定为 `invalid`
      - 哪些旧画像节点需要 `superseded`
      - 每条新画像节点的：
        - `priority`
        - `profile_level`
        - `level_reason`
21. 如果批次里有新的 `memory_nodes[]`：
    - 会先对每条 `memory_nodes[].abstract` 做 embedding
    - 先把新向量写入 LanceDB
    - LanceDB 行 `id` 会回填成对应 `memory_nodes[].vector_id`
22. 只有新向量写入成功后，才会批量回写 DuckDB：
    - 更新每条待处理 turn：
      - `details`
      - `details_budget`
      - `extracted_status = 1`
    - 同步插入：
      - `vmm_memory_nodes`
      - `vmm_profile_nodes`
    - 同步更新：
      - `vmm_users.profile`
      - `vmm_projects.profile`
      - `vmm_sessions.last_summarized_id`
      - `vmm_sessions.summarize_budget`
    - 这里的 `vmm_users.profile / vmm_projects.profile` 不再是 LLM 直接输出的大 Blob
      - 而是后端根据当前有效画像节点自动重建的时间轴文本
      - 每条记录会带：
        - `P`
        - `L`
        - `W`
23. 如果 `obsolete_memory_turn_ids` 不为空：
    - 会把这些 turn 对应的 `vmm_memory_nodes.node_status` 标成 `superseded`
    - DuckDB 提交成功后，再删除 LanceDB 对应的旧向量
24. 如果画像评审中有旧画像节点需要被替代：
    - 会把旧画像节点标成 `superseded`
    - 新画像节点会写入：
      - `priority`
      - `profile_level`
      - `level_reason`
      - `refresh_weight`
      - `expires_timestamp`
      - `superseded_by_id`
      - `profile_date`
25. 每次队列扫描还会额外做一次过期画像收敛：
    - 会查找已经超过 `expires_timestamp` 的 `active` 画像节点
    - 把这些节点批量标记成 `expired`
    - 读取受影响 user/project 当前剩余的 `active` 节点
    - 由后端重新渲染 `vmm_users.profile / vmm_projects.profile`
26. 如果 LanceDB 已写入新向量，但 DuckDB 最终回写失败：
    - 会尝试按这次新生成的 `vector_id` 反向删除 LanceDB 行
    - 避免 `extracted_status=0` 却残留孤立新向量
27. 当前限制：
    - 仍不自动更新 `vmm_teams.profile / vmm_spaces.profile`

## 队列处理细节

当前后台队列是单 worker goroutine，不是多 worker 并行。

它的职责分两类：

1. 优先消费显式入队的 `session`
2. 每 30 秒执行一次周期性维护

队列的显式入队来自：

- `PostAction` 成功写入 `vmm_turn_records` 后
- 空闲超时扫描把待处理 session 强制提升为 `force=true`

同一个 `session` 在队列里会做去重和状态合并：

- 已经在排队，则只刷新 session 快照
- 已经在处理中，则标记为 `dirty`
- 强制任务会把 `force` 置位，保证下次处理时跳过阈值拦截

### 显式队列消费顺序

当队列开始处理一个 session 时，会按以下顺序执行：

1. 读取该 session 下所有 `extracted_status = pending` 的 turn
2. 计算 pending turn 的累计 `dehydrated_budget`
3. 计算当前 session 的空闲时长 `idle_gap`
4. 阈值判断：
   - 如果不是 `force=true`
   - 且 pending turn 条数未达到阈值
   - 且 pending token 预算未达到阈值
   - 则这次先跳过，不发起 LLM 分析
5. 如果达到条件，则在 `session_analysis_max_input_tokens` 预算内，优先选择最早的 pending turn 进入本批次
6. 剩余预算再用于回带历史 `details`
7. 再加载当前 session 下仍然活跃的旧记忆节点
8. 组装 `analyze_session_batch` 请求
9. 调 LLM 批量返回：
   - 每条待处理 turn 的 `details`
   - `memory_nodes[]`
   - `profile_nodes[]`
   - `obsolete_memory_turn_ids`
10. 如果有 `profile_nodes[]`，统一走一次 `review_profile_nodes`
11. 如果有 `memory_nodes[]`，先写 LanceDB
12. 然后批量回写 DuckDB
13. 如果有旧记忆被淘汰，再删除 LanceDB 旧向量

### 历史窗口与预算规则

当前批处理组 prompt 的规则是：

- 待处理 turn 使用原始脱水 JSON
- 历史只使用已经提炼完成的 `details`
- 历史默认最多回带 `session_analysis_history_turns`
- 单次总输入预算上限由 `session_analysis_max_input_tokens` 控制
- 历史预算的统计口径是历史 `details_budget`
- 当前预算的统计口径是 pending turn 的 `dehydrated_budget`

换句话说，当前 LLM 批处理不是“历史原文 + 当前原文”，而是：

- 历史精要
- 当前原始 turn
- 旧记忆锚点

## 定时监测流程

当前队列 worker 每 30 秒会做两件事。

### 1. 空闲超时扫描

它会查找：

- `vmm_sessions.updated_timestamp` 已经超过 `session_analysis_idle_timeout`
- 且该 session 仍存在 `extracted_status = pending` 的 turn

符合条件的 session 会被重新入队，并带 `force=true`。

这意味着：

- 即使 pending turn 条数还没到阈值
- 或 pending token 预算还没到阈值
- 只要空闲超时，也会被强制触发一次批处理

### 2. 过期画像收敛

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
  - turn 已写入 DuckDB，但还没完成 LLM 提炼
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

当前 `PostAction` 在入库前会执行面向长期存储的文本清洗。

主要目标是：

- 去掉对长期记忆没有价值、但会污染 token 预算或语义的噪声
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
2. 文本先完成清洗
3. 后台进入 `PostActionUseCase`
4. 当 `timeline == 0` 时：
   - 才执行一次简单单轮噪声过滤
5. 当 `timeline > 0` 时：
   - 直接跳过噪声门

原因：

- 带 `timeline` 的流程通常表示多步补充、打断或回合穿插
- 这类流程不适合用单轮噪声门做简单拦截

## 最终写入的表

当前主线会写入 DuckDB 的以下表：

- `vmm_sessions`
- `vmm_turn_records`
- `vmm_memory_nodes`
- `vmm_profile_nodes`

其中，当前自动重建会额外更新：

- `vmm_users.profile`
- `vmm_projects.profile`

同时会在 LanceDB 中写入当前 turn 提炼出的记忆向量：

- 行主键：`id`
- 关联键：与 `vmm_memory_nodes.vector_id` 一一对应
- 向量来源：`memory_nodes[].abstract`
- `session_id`：保存真实来源 session，而不是占位值
- 元数据中会附带：
  - `turn_id`
  - `session_id`
  - `user_id`
  - `project_id`
  - `category`

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
  - 表示同一个 session 在后台累计达到多少条 `turn` 后，满足一次后续 LLM 分析条件
- `post_action.session_analysis_token_threshold`
  - 表示同一个 session 在后台累计达到多少 token 预算后，满足一次后续 LLM 分析条件
- `post_action.session_analysis_idle_timeout`
  - 表示距离同一个 session 最后一次会话更新时间超过多久后，强制满足一次后续 LLM 分析条件
- `post_action.session_analysis_history_turns`
  - 表示每次批处理最多回带多少条历史 `details` 精要
- `post_action.session_analysis_max_input_tokens`
  - 表示单次批处理允许送给 LLM 的总输入预算上限

当前已经接入的行为是：

- `PostAction` 成功写入 turn 后，只负责入库并投递 `session` 队列任务
- 队列优先消费显式入队内容
- 同时每 30 秒扫描一次空闲超时且仍有待处理 turn 的 `session`
- 同时每 30 秒扫描一次到期画像节点，并把它们收敛成 `expired`
- 达到条数、token、空闲任一阈值，就会触发一次 `analyze_session_batch`
- `analyze_session_batch` 会基于“历史精要 + 待处理原始 turn + 活跃记忆节点”返回整批结果
- 如果批次里有 `profile_nodes`，会统一走一次 `review_profile_nodes`
- `review_profile_nodes` 会分开返回 user/project 两块 JSON 结果
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
- DuckDB 成功回写后会更新：
  - `vmm_turn_records.details / details_budget / extracted_status`
  - `vmm_memory_nodes`
  - `vmm_profile_nodes`
  - `vmm_sessions.last_summarized_id / summarize_budget`
- `vmm_memory_nodes.vector_id` 会关联 LanceDB 行 `id`
- LanceDB 行里的 `session_id` 会和来源 turn 的 session 保持一致
- 如果旧记忆 turn 被判定淘汰，会把对应 `vmm_memory_nodes.node_status` 标成 `superseded`
- DuckDB 成功提交后，会删除 LanceDB 中对应的旧向量
- 如果 DuckDB 最后回写失败，会尝试回滚这次新增的 LanceDB 向量
- `profile` 渲染文本顶部会固定带有 `P / L / W` 说明头，便于后续再次喂给 LLM
- 仍然不自动更新 `vmm_teams.profile / vmm_spaces.profile`

## 当前限制

- 当前主线不再支持旧的 `raw_messages_snapshot` 契约
- 当前主线不再支持 `team_id / space_id` 由客户端直接传入
- 当前 `PostAction` 只接受纯文本字段，不接受原始消息节点对象
- 当前 `PostAction` 返回的是“已接收”，不是“已写库完成”
- 当前 session 批处理已经接入，但 `vmm_sessions.summarize_content` 仍未开始维护宏观会话总结正文
- 当前只会自动合并 user/project 画像，team/space 画像仍需后续显式配置
