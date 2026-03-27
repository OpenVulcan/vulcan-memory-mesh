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

- 显式确认后删除某个项目及其 DockDB/LanceDB 数据

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

### PreCheck

当前状态：

- 保守禁用
- 固定返回不注入

### PostAction

当前状态：

- 主业务入口
- 记录原始日志和清洗后日志
- 清洗 `user_content` / `timeline[].content` / `assistant_content`
- 立即返回 `accepted=true`
- 后台继续写入 DockDB

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
