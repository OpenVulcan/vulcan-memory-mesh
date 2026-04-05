# VMM gRPC 对接说明（中文）

## 文档目标

这份文档面向插件、客户端和网关开发者，说明当前主线版本应该如何接入 VMM 的 gRPC 服务。

重点包括：

- 当前开放的方法
- 业务接口应该传什么
- 管理接口应该传什么
- trace、大小限制、超时和 TLS 应该怎么处理
- 默认运行模式是 `split(SQLite + LanceDB)`，显式切换 `storage.mode=combined` 时会改为 PostgreSQL 组合库

## 一、当前服务模型

当前服务定义在：

- [internal/adapters/inbound/grpcapi/proto/v1/vmm.proto](../internal/adapters/inbound/grpcapi/proto/v1/vmm.proto)

服务名：

- `vmm.v1.VMMService`

当前只暴露 gRPC，不再暴露 HTTP。

当前应用内也不再支持 TLS。

如果需要 TLS，请在前面使用：

- [Caddy](https://caddyserver.com/)

## 二、当前开放的方法

### 管理面

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

### AI 工具记忆接口

- `SearchMemoryEvents`
- `GetTurnDetails`
- `WriteMemories`
- `ScratchpadUpsert`
- `ScratchpadDelete`
- `ScratchpadGet`
- `ScratchpadClean`

### 业务面

- `ChatCompact`
- `PreCheck`
- `PostAction`

## 三、连接方式

监听地址来自：

- `grpc.listen_addr`

典型配置：

```json
{
  "grpc": {
    "listen_addr": "127.0.0.1:17625",
    "max_receive_message_bytes": 1048576
  }
}
```

说明：

- 当前默认只监听本机
- 需要跨主机访问时，请自行调整监听地址并加反向代理

## 四、metadata 与 trace

当前支持透传：

- `x-trace-id`

如果你传入这个 metadata，服务端会继续沿用它。

如果不传，服务端会自动生成新的 trace id。

另外要注意：

- `x-trace-id` 必须保持 ASCII-safe
- 不要把原始中文 query、原始 JSON、或其他非 ASCII 内容直接拼进 metadata
- 中文正文应该放在 protobuf 请求体里，而不是 metadata/header 里

## 五、大小限制

当前请求大小限制使用：

- `grpc.max_receive_message_bytes`

默认：

- `1048576`，也就是 1MB

超限时通常会直接返回：

- `ResourceExhausted`

## 六、超时

当前服务端按方法配置超时：

- `grpc.request_timeout.workspace`
- `grpc.request_timeout.pre_check`
- `grpc.request_timeout.post_action`

环境变量：

- `VMM_GRPC_WORKSPACE_TIMEOUT`
- `VMM_GRPC_PRE_CHECK_TIMEOUT`
- `VMM_GRPC_POST_ACTION_TIMEOUT`

## 七、业务接口约束

### 0. Profile 接口

当前画像相关能力拆成三条独立 RPC：

- `GetProfileNodes`
- `GetProfileBundle`
- `ApplyProfileInstruction`

约束如下：

- 只支持单目标请求
- 不提供 `all` 过滤
- `GetProfileNodes` 只返回当前 `active` 的原子化画像节点
- `GetProfileNodes` 不返回渲染后的 profile Blob
- `GetProfileBundle` 用于按 `project_id + user_id` 读取并组装最终组合画像结果
- `ApplyProfileInstruction` 会同步触发一次 LLM 评审并落库
- `ApplyProfileInstruction` 对同目标同指令的并发调用会复用第一次进行中的结果
- `ApplyProfileInstruction` 对同一目标上的不同指令会串行执行，避免同一批旧节点并发写回
- `SearchMemoryEvents` 用于按 `project_id + user_id + queries[]` 主动搜索长期记忆
- `GetTurnDetails` 用于按 `turn_ids[]` 回查结构化 turn 详情
- `WriteMemories` 用于让 AI 工具主动写入长期记忆，不生成 turn
- `Scratchpad*` 用于让 AI Agent 把当前任务的确定性工作态写入独立 DWM scratchpad
- 如果默认 SQLite provider 返回 `SQLITE_BUSY / SQLITE_LOCKED / SQLITE_SCHEMA`，并通过 trailer 标记为可重试：
  - 服务端适配层会先做有界指数退避重试
- 如果手工画像持久化阶段遇到关系库存储 provider 返回的“提交结果不确定”错误：
  - 服务端会先回查 instruction 行、profile node 行、退役状态和最终 profile Blob
  - 如果副作用其实已经存在，则会把这次请求收敛成成功
  - 只有回查也无法确认最终状态时，才返回 `Aborted / STORAGE_OUTCOME_UNCERTAIN`

目标范围支持：

- `USER`
- `PROJECT`
- `TEAM`
- `SPACE`

目标字段要求：

- `USER`
  - 必须传 `user_id`
- `PROJECT`
  - 必须传 `project_id`
- `TEAM`
  - 必须传 `project_id`
  - 服务端通过 `project_id -> team_id`
- `SPACE`
  - 必须传 `project_id`
  - 服务端通过 `project_id -> space_id`

另外需要注意：

- `TEAM / SPACE` 的手工画像指令会被视为最高权限规则
- 后端会强制把它们钳制到最高权威语义
- 它们不走 post-action 的自动画像提炼路径

### 1. PreCheck

当前业务请求只接受：

- `session_id`
- `user_id`
- `project_id`
- `user_content`
- `recall_mode`

不再接受：

- `team_id`
- `space_id`

原因：

- `project_id` 在数据库层级上已经唯一绑定 `space_id` 和 `team_id`
- 这两个字段由服务端统一反查，不再由客户端传入
- `recall_mode=0` 或省略时，服务端会回退到旧版 pre-check 检索行为
- `recall_mode=1` 时，服务端会应用当前 session 的 compact 边界：
  - 未 compact：排除当前 session 的 turn-extract 记忆
  - 已 compact：只允许召回 `source_turn_id <= last_compacted_turn_id` 的同 session 历史记忆
- `recall_mode` 为未来新增的非零值时，当前版本会回退到 compact-aware 基线，避免整段当前 session 被重新开放召回

### 2. ChatCompact

当前 `ChatCompact` 只接受：

- `session_id`
- `user_id`
- `project_id`

语义如下：

- 用于显式告诉服务端“当前 session 已执行一次上下文压缩”
- 服务端会把该 session 当前最新已持久化的 turn 记录为 `last_compacted_turn_id`
- `PreCheckResponse.context_text` 已废弃，当前 gRPC 返回固定为空字符串
- `PreCheckResponse.context_items[]` 仅保留：
  - 记忆正文
  - `score`
  - `has_dialogue`
  - `turn_id`
- 当 `context_items[].turn_id > 0` 时，客户端可继续调用 `GetTurnDetails`
- 同时更新 `last_compacted_timestamp`
- 如果当前 session 没有 turn，允许返回成功但不更新 compact 边界
- 如果重复 compact 到同一最新 turn，会保持幂等

### 2.5 DWM / Scratchpad

当前还提供一条与长期记忆主链隔离的 DWM scratchpad 支线：

- `ScratchpadUpsert`
- `ScratchpadDelete`
- `ScratchpadGet`
- `ScratchpadClean`

这条支线的边界非常明确：

- 不进入 `memory_nodes`
- 不进入 `SearchMemoryEvents`
- 不创建 `vmm_sessions`
- 不复用 retention trash
- 只保存当前任务的确定性工作态

固定定位字段：

- `project_id`
- `user_id`
- `session_id`

其中：

- `session_id` 在这条链路里只是字符串 `session_key`
- 服务端不会像 `PreCheck / ChatCompact / PostAction` 那样通过统一范围解析拦截器自动创建主 session

计划守卫规则：

- `ScratchpadUpsert / ScratchpadDelete` 都要求传 `plan_name`
- 同一 `project_id + user_id + session_id` 只允许存在一个 canonical `plan_name`
- 若当前范围还没有任何 scratchpad 数据：
  - `Upsert` 首次写入会创建计划锁
  - `Delete` 不会创建计划锁，只返回引导消息
- 若输入计划名与当前计划名忽略大小写后不一致：
  - 拦截写入或删除
  - 返回英文自然语言提示，要求检查拼写或先调用 `Clean`
- 若忽略大小写后一致，但原始拼写不同：
  - 允许放行
  - `msg` 最前面强插 `[FORMAT DRIFT WARNING]`

请求形态规则：

- `ScratchpadUpsert`
  - 允许 `key + value`
  - 也允许 `items[]`
  - 但两种形式不能混传
  - 若混传，直接返回 `InvalidArgument`
- `ScratchpadDelete`
  - 允许 `key`
  - 也允许 `keys[]`
  - 但两种形式不能混传
  - 若混传，直接返回 `InvalidArgument`

返回约束：

- `status` 使用 `ScratchpadStatus` 枚举
- `msg` 固定英文
- `ScratchpadGet` 额外返回：
  - `plan_name`
  - `item_count`
  - `updated_timestamp`
- `ScratchpadUpsert` 额外返回：
  - `affected_count`
  - `inserted_count`
  - `updated_count`
- `ScratchpadDelete` 额外返回：
  - `affected_count`
- 调用方如果要把结果直接交给 AI Agent：
  - 应先把 `ScratchpadStatus` 转译成模型更容易理解的文本状态

空数据语义：

- `Get` 无数据时：
  - 返回成功
  - `items = []`
  - `msg = "No scratchpad records found for the current session."`
- `Delete` 在当前无计划时：
  - 返回成功
  - `msg = "No scratchpad plan exists for the current session. Create records first."`
- `Get` 返回的 `plan_name` 永远是当前 canonical 值
- scratchpad 批量写入和批量删除都采用整批原子事务语义
- scratchpad 不参与 `MigrateProject`
  - 仅参与 `DeleteProject / DeleteUser` 的级联清理

### 3. PostAction

当前业务请求只接受：

- `session_id`
- `user_id`
- `project_id`
- `user_content`
- `assistant_content`
- `timeline[]`

`timeline[]` 的元素格式：

- `type`
- `content`

其中：

- `type` 只能是 `user` 或 `assistant`
- `content` 必须是字符串

## 八、统一前置拦截

`PreCheck`、`ChatCompact` 和 `PostAction` 在真正进入业务逻辑之前，都会经过统一的范围解析拦截器。

拦截器会做这些事：

1. 检查 `session_id`
2. 检查 `user_id`
3. 检查 `project_id`
4. 根据 `project_id` 反查：
   - `team_id`
   - `space_id`
   - 层级名称
5. 根据 `session_id`：
   - 已存在则读取
   - 不存在则创建 `vmm_sessions` 行
6. 把解析结果注入上下文

这意味着：

- `PreCheck` / `ChatCompact` / `PostAction` 用例层看到的已经是完整 `SessionRef`
- 业务层无需再处理 `team_id` / `space_id` 解析

另外需要特别注意：

- 如果 `user_id = 0` 或 `project_id = 0`
  - 会直接返回 `InvalidArgument`
- 如果 `user_id` 或 `project_id` 不是 0，但数据库里不存在对应记录
  - 会直接返回 `NotFound`
- 这类失败发生在拦截器阶段
  - 不会进入 `PreCheck` / `ChatCompact` / `PostAction` 用例层
  - 不会自动创建 `session`
- `Scratchpad*` 不走这条拦截器
  - 它只校验 `project_id / user_id / session_id`
  - 并确认 `project_id / user_id` 对应记录真实存在
  - `session_id` 仅作为隔离 DWM 的字符串定位键

## 九、当前方法语义

### Healthz

用途：

- 健康检查

### ListProjects

用途：

- 列出全部项目空间

返回展示格式：

- `[PROJECT_ID]TeamName/SpaceName/ProjectName`

### ResolveProject

用途：

- 通过数字 `project_id` 或 `Team/Space/Project` 路径解析项目

### EnsureProject

用途：

- 根据 `confirm_create` 规则解析或创建 `Team/Space/Project`

### DeleteProject

用途：

- 显式确认后删除某个项目及其关系库存储/LanceDB 数据
- 返回真正删除的项目、画像、session、turn、memory 计数
- 如果该项目删除后其 `space` 变空，会级联删除该 `space`
- 如果级联删除 `space` 后其 `team` 也变空，会继续级联删除该 `team`

### MigrateProject

用途：

- 显式确认后，把源项目的数据迁移到目标项目

### ResolveUser

用途：

- 通过数字 ID 或用户名解析用户
- 在 `confirm_create=true` 时创建用户

### ListUsers

用途：

- 返回用户列表

### DeleteUser

用途：

- 通过确认码保护删除用户及其 SQL/向量数据
- 返回真正删除的用户、画像、session、turn、memory 计数
- 删除用户时只删除该用户自身画像节点
- `project/team/space` 的共享画像节点不会被删；如果它们原本来自该用户的 turn，会先脱离旧 turn 来源再保留

### GetProfileNodes

用途：

- 读取单个目标下当前 `active` 的原子化画像节点

返回内容特点：

- 每条节点都带稳定 `id`
- 返回 `content / priority / level / refresh_weight / profile_date`
- 同时返回 `source_kind / source_id`
- 方便插件按需挑选节点，再自行组织成大模型上下文

### GetProfileBundle

用途：

- 按 `project_id + user_id` 读取当前四层 scope 的已渲染画像正文，并由服务端做一次确定性组合

请求字段：

- `project_id`
- `user_id`
- `mode`
  - `PROFILE_BUNDLE_MODE_FULL`
  - `PROFILE_BUNDLE_MODE_SPLIT`
- `include_explanation`

返回特点：

- `FULL`
  - 返回一段可直接注入的大模型提示词
  - 这是权威输出，调用方应直接消费 `combined_text`
  - 为避免重复拼接，辅助说明字段和拆分字段会保持为空
  - 只会输出存在真实正文的 scope；不存在的 `TEAM / SPACE / PROJECT / USER` 不会补空标签或额外说明
  - 如果四个 scope 都没有画像正文，则 `combined_text` 直接为空字符串
  - 结果中显式保留：
    - `[TEAM]`
    - `[SPACE]`
    - `[PROJECT]`
    - `[USER]`
  - 环境约束头固定写明：
    - `Project > Space > Team`
  - `include_explanation`
    - 省略时默认开启
    - 打开时，会把 `P/L/W` 与“当前实际存在的 scope”含义直接内嵌到 `combined_text`
    - 关闭时，只返回正文结构
- `SPLIT`
  - 不返回完整合并文本
  - 只分别返回：
    - `team_profile`
    - `space_profile`
    - `project_profile`
    - `user_profile`
  - `include_explanation` 在该模式下不会额外返回说明字段

额外说明：

- 这条接口不会触发 LLM
- 它依赖当前数据库里已经自动重建好的 scope `profile` 正文
- scope `profile` 本身不再保存说明头，说明头只在 bundle 输出里按需附加

### ApplyProfileInstruction

用途：

- 对单个目标提交一条显式自然语言画像修改指令

执行方式：

1. 服务端解析目标 scope
2. 读取当前 active 节点
3. 写入 `vmm_profile_instructions`
4. 调用 `review_profile_instruction`
5. 持久化新节点与退役节点
6. 重建对应 scope 的 `profile`

需要额外注意：

- 这条链路不绑定 `turn_id`
- 关系库存储中这类画像节点的 `vmm_profile_nodes.turn_id` 会保持 `NULL`
- 新节点会记录 `source_kind = manual_instruction`
- `source_id` 会指向对应的 `instruction_id`
- `TEAM / SPACE` 的手工指令会被视为最高权限规则
- 如果关系库存储 provider 返回“提交结果不确定”：
  - 服务端会先做状态回查，再决定是否把本次请求视为成功
  - 只有回查也无法确认最终状态时，才会返回 `Aborted / STORAGE_OUTCOME_UNCERTAIN`

### SearchMemoryEvents

用途：

- 主动发起一次面向长期记忆的检索
- 搜索目标是 memory 记录，不是 turn 记录
- 返回结果只保留 AI 后续决策需要的最小字段

请求字段：

- `project_id`
- `user_id`
- `queries[]`
- `top_k`

其中：

- `queries[]`
  - 是字符串数组
  - 每项都是一条完整查询语句
  - 不再使用 `query_json`
  - 不再使用 `background`
  - 当前最大 `16` 条
- `top_k`
  - 单条 query 期望返回的命中上限
  - transport 上限为 `64`
  - 实现层会进一步做保护性截断

请求示例：

```json
{
  "project_id": 9,
  "user_id": 7,
  "queries": [
    "喜欢的水果",
    "最近确认过的并发方案"
  ],
  "top_k": 5
}
```

### PreCheck 检索范围配置

- `pre_check.search_scope`
  - 控制 `PreCheck` 长期记忆召回的层级范围
  - 可选值：`team`、`space`、`project`
  - 默认值：`space`

语义说明：

- `team`
  - 允许当前 team 下的共享长期记忆参与 `PreCheck`
- `space`
  - 允许当前 space 下的共享长期记忆参与 `PreCheck`
- `project`
  - 仅允许当前 project 下的长期记忆参与 `PreCheck`

注意：

- 这个配置只影响 `PreCheck`
- 通用 `SearchMemoryEvents` 仍保持项目级过滤默认语义

服务端行为：

- 先通过 `project_id + user_id` 解析当前检索 scope
- 对每条 query 做规范化、embedding 和统一召回
- 在当前 `team / space / project / user` 范围内检索 LanceDB
- 原样回显每条 query 的：
  - `query_index`
  - `query`

返回命中字段：

- `memory_id`
- `source_turn_id`
- `abstract`
- `details_preview`
- `category`

字段说明：

- `memory_id`
  - 这条长期记忆自身的稳定 ID
- `source_turn_id`
  - 这条记忆若来自某条 turn 提炼，则返回该 turn id
  - 若这条记忆是工具直接写入、没有来源 turn，则返回 `0`
- `abstract`
  - 这条记忆的摘要
- `details_preview`
  - 详情预览，不是完整 details
- `category`
  - 直接返回稳定英文标签，而不是内部数字

`category` 当前标签：

- `general`
- `architecture_decision`
- `tech_spec_api`
- `business_logic`
- `requirement_todo`
- `project_context`
- `logical_bug_debt`
- `security_policy`

### GetTurnDetails

用途：

- 按 `turn_ids[]` 读取一条或多条 turn 的结构化详情
- 适合在 `SearchMemoryEvents` 命中后，按 `source_turn_id` 再追查原始对话
- 不再直接返回脱水 JSON 原文和内部预算字段

请求字段：

- `turn_ids[]`

返回字段：

- `turn_id`
- `user_question`
- `timeline`
- `assistant_answer`
- `detail`
- `previous_turn_ids`
- `next_turn_ids`

返回说明：

- `timeline`
  - 直接返回 JSON 数组结构，不需要再解析字符串
- `previous_turn_ids`
  - 最多返回当前 turn 之前 `3` 条相邻 turn id
- `next_turn_ids`
  - 最多返回当前 turn 之后 `3` 条相邻 turn id

### WriteMemories

用途：

- 让 AI 工具主动写入长期记忆
- 这条接口只写入 memory，不会生成 turn
- 适合把已经明确、值得长期保留的事实或约束直接落库

请求字段：

- `session_id`
- `user_id`
- `project_id`
- `items[]`

`items[]` 每项字段：

- `scope_level`
  - 使用紧凑数字概念值
  - `1 = session`
  - `2 = project`
  - `3 = user`
  - `0` 或省略时，由服务端按默认策略处理，默认落到 `project`
- `abstract`
  - 必填
  - 用于摘要和 embedding
- `details`
  - 必填
  - 用于保存完整记忆正文
- `category`
  - 必填
  - 使用内部分类编号
- `priority`
  - 可选
  - `1 = P0`
  - `2 = P1`
  - `3 = P2`
  - `0` 或省略时，由服务端默认成 `P2`
- `memory_level`
  - 可选
  - `1 = L0`
  - `2 = L1`
  - `3 = L2`
  - `4 = L3`
  - `0` 或省略时，由服务端按 scope 自动补默认值

`category` 当前编号：

- `0 = general`
- `1 = architecture_decision`
- `2 = tech_spec_api`
- `3 = business_logic`
- `4 = requirement_todo`
- `5 = project_context`
- `6 = logical_bug_debt`
- `7 = security_policy`

额外说明：

- 不再接收 `expires_timestamp`
- 有效期由服务端按标准算法自动计算
- 写入链路会做软幂等，避免短时间重复写入同一条记忆

返回字段：

- `memory_id`
- `deduped`

返回说明：

- `memory_id`
  - 新建或复用的长期记忆 ID
- `deduped`
  - `true` 表示命中软幂等，复用了既有 memory
  - `false` 表示这次实际创建了新 memory

### ScratchpadUpsert

用途：

- 把当前任务的确定性工作记忆写入隔离 DWM scratchpad
- 适合写入：
  - 当前计划
  - 关键发现
  - 文件级摘要
  - 后续执行步骤

请求字段：

- `session_id`
- `user_id`
- `project_id`
- `plan_name`
- `key + value`
  或
- `items[] = { key, value }`

约束：

- `plan_name` 长度必须 `<= 128`
- 单次批量最多 `32` 条 item
- `key` 长度必须 `<= 128`
- `value` 长度必须 `<= 16000`
- `key + value` 与 `items[]` 不能混传
- 批量写入采用整批原子事务

### ScratchpadDelete

用途：

- 删除当前 DWM scratchpad 中的一个或多个 key

请求字段：

- `session_id`
- `user_id`
- `project_id`
- `plan_name`
- `key`
  或
- `keys[]`

约束：

- `Delete` 在空范围时不会锁定 plan
- `key` 与 `keys[]` 不能混传
- 批量删除采用整批原子事务

### ScratchpadGet

用途：

- 在上下文压缩后重新拉取完整 scratchpad 或某个单键锚点

请求字段：

- `session_id`
- `user_id`
- `project_id`
- 可选 `key`

语义：

- 不传 `key`
  - 返回当前范围下全部 scratchpad item
- 传 `key`
  - 返回命中的单项或空数组
- 同时返回 metadata：
  - `plan_name`
  - `item_count`
  - `updated_timestamp`

### ScratchpadClean

用途：

- 显式结束当前任务并清空整份 DWM scratchpad

请求字段：

- `session_id`
- `user_id`
- `project_id`

语义：

- 删除当前范围下全部 scratchpad nodes 与 plan 锁
- 空范围返回成功，不报错
- 不参与项目迁移

### PreCheck

当前状态：

- 已接回实时两层召回
- 第一层 `extract_intent` 会读取最近 turn 窗口，并混合：
  - 已提炼 turn 的 `details`
  - 未提炼 turn 的脱水原文
- 第一层输出多条向量检索语句，而不是只给关键词
- 检索范围由服务端解析出的 `team / space / project` 决定，并附带 `user_id = 0 OR current_user_id` 过滤
- 默认不会再额外按 `session_id` 收窄长期记忆检索
- 第二层 `review_precheck_memory` 负责在统一记忆召回结果里按候选编号选择真正要注入的条目
- 只有被第二层采纳的 memory id 才会刷新生命周期
- `PreCheck` 不再混入画像 bundle；画像读取继续通过 `GetProfileNodes / GetProfileBundle`

但有一个前提：

- 只有 `session_id / user_id / project_id` 都通过前置范围解析时，才会进入 `PreCheck`
- 如果 `user_id` 或 `project_id` 非法或不存在
  - 会同步直接返回错误
  - 不会返回降级版 `should_inject=false`

### PostAction

当前状态：

- 主业务入口
- 记录原始日志和清洗后日志
- 清洗 `user_content` / `timeline[].content` / `assistant_content`
- 稳定写入一条 turn 后立即返回 `accepted=true`
- 后台异步工作器再继续执行单轮提炼
- 异步提炼在写回当前启用的存储后端前，会先完成统一记忆写入和必要的回滚保护

同样也有一个前提：

- 只有 `session_id / user_id / project_id` 都通过前置范围解析时，才会进入 `PostAction`
- 如果 `user_id` 或 `project_id` 非法或不存在
  - 会同步直接返回错误
  - 不会返回 `accepted=true`
  - 也不会进入异步后台写入阶段

## 十、推荐对接顺序

建议你按下面顺序联调：

1. `Healthz`
2. `ListProjects`
3. `ResolveProject`
4. `ResolveUser`
5. `PostAction`
6. `PreCheck`

原因：

- 先打通层级和用户解析
- 再打通主写入链
- 最后再验证实时 `PreCheck`
