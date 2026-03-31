# VMM gRPC 对接说明（中文）

## 文档目标

这份文档面向插件、客户端和网关开发者，说明当前主线版本应该如何接入 VMM 的 gRPC 服务。

重点包括：

- 当前开放的方法
- 业务接口应该传什么
- 管理接口应该传什么
- trace、大小限制、超时和 TLS 应该怎么处理

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
- `ApplyProfileInstruction`

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

当前画像相关能力拆成两条独立 RPC：

- `GetProfileNodes`
- `ApplyProfileInstruction`

约束如下：

- 只支持单目标请求
- 不提供 `all` 过滤
- `GetProfileNodes` 只返回当前 `active` 的原子化画像节点
- `GetProfileNodes` 不返回渲染后的 profile Blob
- `ApplyProfileInstruction` 会同步触发一次 LLM 评审并落库

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

- 显式确认后删除某个项目及其 DuckDB/LanceDB 数据
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
- DuckDB 中这类画像节点的 `vmm_profile_nodes.turn_id` 会保持 `NULL`
- 新节点会记录 `source_kind = manual_instruction`
- `source_id` 会指向对应的 `instruction_id`
- `TEAM / SPACE` 的手工指令会被视为最高权限规则

### PreCheck

当前状态：

- 保守禁用
- 固定返回不注入

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
- 立即返回 `accepted=true`
- 后台继续写入 DuckDB

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
- 最后再验证当前保守版 `PreCheck`
