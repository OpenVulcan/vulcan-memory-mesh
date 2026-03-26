# VMM gRPC 对接说明（中文）

## 文档目标

这份文档面向需要接入 VulcanMemoryMesh 的客户端、插件和网关开发者。

它重点说明：

- 当前 gRPC 服务如何连接
- 当前 proto 服务和方法有哪些
- metadata、错误、消息大小限制如何处理
- 各方法适合什么场景
- 什么时候应该使用 Caddy 处理 TLS

这份文档是“对接说明”。

如果你只是想快速做联调测试，请优先看：

- [gRPC 接口测试说明（中文）](./api-test-guide_CN.md)

## 当前运行模型

当前 VMM 本地版只暴露 gRPC 服务，不再内建 HTTP 或 TLS。

运行模型如下：

1. VMM 自身监听本地 gRPC 地址
2. 客户端直接通过 gRPC 连接
3. 如果需要域名、TLS 或公网接入，请在前面放 Caddy

推荐理解为：

- VMM 负责业务协议
- Caddy 负责 TLS 和反向代理

## 连接方式

监听地址来自：

- `configs/local.json`
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
- 如果要跨主机访问，请自行调整监听地址并加反向代理

## TLS 说明

当前应用内已经移除 TLS。

也就是说：

- VMM 不提供证书加载
- VMM 不提供 `ListenAndServeTLS`
- VMM 不负责域名证书续期

如果你需要 TLS，请在前面使用：

- [Caddy](https://caddyserver.com/)

推荐原因：

- 配置更简单
- 证书管理更稳定
- 后续切换域名和反代更方便

## Proto 服务

当前服务定义在：

- [internal/adapters/inbound/grpcapi/proto/v1/vmm.proto](../internal/adapters/inbound/grpcapi/proto/v1/vmm.proto)

当前 package：

- `vmm.v1`

当前 service：

- `vmm.v1.VMMService`

当前方法：

- `Healthz`
- `Chat`
- `PreCheck`
- `PostAction`
- `PostActionOld`
- `SeedMemory`

## 推荐客户端接入方式

推荐优先使用 protobuf 生成客户端代码，不建议长期依赖手写 JSON over grpcurl。

建议做法：

1. 拉取或复制 `vmm.proto`
2. 用你自己的语言生成 gRPC client
3. 在业务代码里按 proto 消息结构发起调用

`grpcurl` 更适合：

- 本地联调
- 快速验证
- 排查参数错误

## Reflection

当前运行时已启用 gRPC reflection。

这意味着：

- 可以直接用 `grpcurl list`
- 可以直接用 `grpcurl describe`
- 联调时不一定需要手动传入 proto 文件

## Metadata

当前服务支持通过 metadata 透传：

- `x-trace-id`

用途：

- 把上游 trace id 继续传入 VMM
- 方便串联插件层、网关层、VMM 日志

如果你不传：

- 服务端会自动生成新的 `trace_id`

建议：

- 网关或插件层如果本身已有 trace id，请透传给 VMM

## 大小限制

当前服务使用：

- `grpc.max_receive_message_bytes`

默认值：

- `1048576`，也就是 1MB

超过限制时：

- 请求通常会在 gRPC 传输层直接被拒绝
- 常见表现是 `ResourceExhausted`

这类错误不一定会进入业务层，因此也不一定会带完整业务错误结构。

## 超时模型

当前服务按方法配置超时：

- `grpc.request_timeout.chat`
- `grpc.request_timeout.pre_check`
- `grpc.request_timeout.post_action`
- `grpc.request_timeout.seed_memory`

说明：

- 这些是服务端方法级超时
- 超时后通常返回 gRPC 错误

## 错误模型

当前服务主要通过：

- gRPC status code
- `google.rpc.ErrorInfo`（尽量附带）

来表达错误。

常见 reason 包括：

- `GRPC_VALIDATION_FAILED`
- `GRPC_ROUTE_DISABLED`
- `UPSTREAM_TIMEOUT`
- `INTERNAL_ERROR`

建议调用方同时处理两层：

1. gRPC code
2. `ErrorInfo.reason`

## 方法说明

### 1. Healthz

用途：

- 判断进程是否存活

适合：

- 启动探针
- 存活检查
- 反代健康检查

输入：

- 空请求

输出：

- `status`
- `trace_id`

### 2. Chat

用途：

- 接收一条消息
- 先做 PII 脱敏
- 再写入长期库
- 返回脱敏后的文本

适合：

- 纯文本单条归档
- 需要先走脱敏再存储的轻量场景

输入字段：

- `session_id`
- `message`
- `accept_language`

输出字段：

- `session_id`
- `message`
- `language`
- `trace_id`

说明：

- 返回的 `message` 是脱敏后的文本，不是原文

### 3. PreCheck

用途：

- 当前版本仅保留接口，不做真正注入计算

当前行为：

- 固定返回“不注入”

输入字段：

- `session_id`
- `user_id`
- `team_id`
- `space_id`
- `project_id`
- `user_content`

输出字段：

- `should_inject`
- `context_text`
- `context_items`
- `degraded`
- `trace_id`

当前返回特征：

- `should_inject = false`
- `context_text = ""`
- `context_items = []`

所以当前它更像：

- 占位接口
- 契约对齐接口

而不是完整记忆注入入口。

### 4. PostAction

用途：

- 新版字符串契约入口
- 接收一段整理后的对话结果
- 立即确认已接收
- 然后后台继续处理

输入字段：

- `session_id`
- `user_id`
- `team_id`
- `space_id`
- `project_id`
- `user_content`
- `assistant_content`
- `timeline`

其中：

- `user_content`：首轮用户问题
- `assistant_content`：最后一条助手回答
- `timeline`：中间流程数组

`timeline` 元素结构：

- `type`
- `content`

约束：

- `type` 只能是 `user` 或 `assistant`
- `content` 必须是字符串

服务端行为：

1. 校验参数
2. 把内容打到控制台日志
3. 立即返回 `accepted=true`
4. 后台继续复用旧版持久化主线

额外规则：

- 当 `timeline` 非空时，会跳过 `NoiseGate`
- 当 `timeline` 为空时，才会执行标准噪声门

适合：

- 插件层已经整理出首问、尾答和中间过程

### 5. PostActionOld

用途：

- 保留旧版原始快照契约
- 兼容仍然上传原始节点快照的旧调用方

输入字段：

- `session_id`
- `user_id`
- `team_id`
- `space_id`
- `project_id`
- `raw_messages_snapshot`

每个 `raw_messages_snapshot` 节点包括：

- `role`
- `content_json`
- `tool_calls`
- `meta_json`

适合：

- 还没迁移到新版 `PostAction` 契约的客户端

### 6. SeedMemory

用途：

- 主动灌入一条向量记忆

输入字段：

- `user_id`
- `project_id`
- `memory_text`
- `space_id`

输出字段：

- `accepted`
- `memory_id`
- `trace_id`

适合：

- 本地调试
- 管理员预热测试数据

## 当前对接建议

如果你是新的调用方，建议优先使用：

1. `Healthz`
2. `Chat`
3. `PostAction`
4. `PreCheck`

其中：

- `PostActionOld` 只用于兼容旧调用方
- `SeedMemory` 更偏管理员或测试用途

## 当前行为上的保守点

当前主线仍然是保守版本，对接方需要知道这几个事实：

1. `PreCheck` 现在不会真正返回记忆注入
2. `PostAction` 的后台处理仍以本地调试和逐步放通为主
3. 新旧 `PostAction` 同时存在，是为了迁移期兼容

所以如果你在联调时发现：

- `PreCheck` 一直不注入

这在当前版本是正常行为，不是对接失败。

## 推荐 Caddy 方式

如果你准备把 VMM 放到局域网或公网环境中，推荐：

1. VMM 本地监听纯 gRPC
2. Caddy 负责：
   - TLS
   - 域名
   - 反向代理
   - 暴露公网入口

这样可以避免：

- 在应用里自行管理证书
- 在业务代码里维护 TLS 细节

## 与测试文档的区别

这份文档关注：

- 如何接入
- 方法有什么语义
- 应该如何设计客户端

而测试文档关注：

- 如何用 `grpcurl` 快速验证
- 请求/响应示例
- 常见测试顺序

如果你只是想先打通联调，请继续看：

- [gRPC 接口测试说明（中文）](./api-test-guide_CN.md)
