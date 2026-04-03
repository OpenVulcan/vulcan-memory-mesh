# VMM gRPC 对接说明（中文）

## 文档目标

这份文档面向插件、客户端和网关开发者，说明当前主线版本应该如何接入 VMM 的 gRPC 服务。

重点包括：

- 当前开放的方法
- 业务接口应该传什么
- 管理接口应该传什么
- trace、大小限制、超时和 TLS 应该怎么处理
- 默认关系库存储已经切到 SQLite，旧兼容 provider 已移除

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
- `SearchMemoryEvents`
- `GetTurnDetails`

### 业务面

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
- `SearchMemoryEvents` 用于按 `project_id + user_id + query_json` 主动搜索向量记忆
- `GetTurnDetails` 用于按 `turn_ids[]` 回查脱水 turn 原文
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

不再接受：

- `team_id`
- `space_id`

原因：

- `project_id` 在数据库层级上已经唯一绑定 `space_id` 和 `team_id`
- 这两个字段由服务端统一反查，不再由客户端传入

### 2. PostAction

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

`PreCheck` 和 `PostAction` 在真正进入业务逻辑之前，都会经过统一的范围解析拦截器。

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

- `PreCheck` / `PostAction` 用例层看到的已经是完整 `SessionRef`
- 业务层无需再处理 `team_id` / `space_id` 解析

另外需要特别注意：

- 如果 `user_id = 0` 或 `project_id = 0`
  - 会直接返回 `InvalidArgument`
- 如果 `user_id` 或 `project_id` 不是 0，但数据库里不存在对应记录
  - 会直接返回 `NotFound`
- 这类失败发生在拦截器阶段
  - 不会进入 `PreCheck` / `PostAction` 用例层
  - 不会自动创建 `session`

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
  - 结果中显式保留：
    - `[TEAM]`
    - `[SPACE]`
    - `[PROJECT]`
    - `[USER]`
  - 环境约束头固定写明：
    - `Project > Space > Team`
  - `include_explanation`
    - 省略时默认开启
    - 打开时，会把 `P/L/W` 与 `[TEAM]/[SPACE]/[PROJECT]/[USER]` 的含义直接内嵌到 `combined_text`
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

- 主动发起一次面向向量记忆的检索

请求字段：

- `project_id`
- `user_id`
- `query_json`
- `top_k`

其中 `query_json` 必须是 JSON 数组，每项结构为：

```json
[
  {
    "background": "用户最近一直在讨论水果和饮品。",
    "query": "喜欢的水果"
  }
]
```

服务端行为：

- 先通过 `project_id + user_id` 解析当前检索 scope
- 对每条查询项做 embedding
- 在当前 `team / space / project / user` 范围内检索 LanceDB
- 原样回显每条查询项的：
  - `background`
  - `query`

返回命中字段：

- `memory_id`
- `turn_id`
- `session_id`
- `content`
- `details`
- `category`
- `score`

### GetTurnDetails

用途：

- 按 `turn_ids[]` 读取一条或多条脱水 turn 原文
- 同时返回服务端已经拆好的具体对话字段，以及当前 turn 前后各 `3` 轮的编号

请求字段：

- `turn_ids[]`

返回字段：

- `turn_id`
- `session_id`
- `project_id`
- `dehydrated_content`
- `user_content`
- `timeline`
- `assistant_content`
- `dehydrated_budget`
- `extracted_status`
- `details`
- `details_budget`
- `created_timestamp`
- `updated_timestamp`
- `previous_turn_ids`
- `next_turn_ids`

典型联动方式：

1. 先调用 `SearchMemoryEvents`
2. 从命中结果中拿到 `memory_ref / source_ref`
3. 如果需要混合详情，调用 `GetMemoryDetails`
4. 如果只想按旧接口回看 turn 原文，仍可对 `source_ref.type=TURN` 的条目调用 `GetTurnDetails`

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
- 异步提炼写入关系库存储（默认 SQLite）前，会先完成统一记忆向量写入和必要的回滚保护

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
