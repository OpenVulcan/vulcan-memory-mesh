# VMM gRPC 接口测试说明（中文）

## 文档目标

这份文档面向联调和测试人员，说明当前 VMM 本地版的 gRPC 服务如何调用、每个方法的输入输出，以及推荐的测试方式。

当前服务名：

- `vmm.v1.VMMService`

## 连接信息

默认监听地址来自：

- `configs/local.json`
- `grpc.listen_addr`

默认值通常是：

```json
{
  "grpc": {
    "listen_addr": "127.0.0.1:17625"
  }
}
```

如果需要 TLS，请不要在应用内开启。当前推荐做法是：

- VMM 只监听本地纯 gRPC
- 需要 TLS 时，由 Caddy 反向代理处理

当前本地数据面只保留两条主线：

- DockDB：会话文本与脱敏文本存储
- LanceDB：向量存储

## 推荐测试工具

推荐使用：

- `grpcurl`

如果本地尚未安装，可参考：

- [grpcurl](https://github.com/fullstorydev/grpcurl)

当前运行时已开启 gRPC reflection，因此可以直接使用 `grpcurl list` / `grpcurl describe`，不需要额外传 proto 文件。

## 当前开放的方法

- `vmm.v1.VMMService/Healthz`
- `vmm.v1.VMMService/Chat`
- `vmm.v1.VMMService/PreCheck`
- `vmm.v1.VMMService/PostAction`
- `vmm.v1.VMMService/PostActionOld`
- `vmm.v1.VMMService/SeedMemory`

## Trace ID

当前服务支持通过 gRPC metadata 透传：

- `x-trace-id`

例如：

```powershell
grpcurl -plaintext -H "x-trace-id: trace-fixed" 127.0.0.1:17625 list
```

如果不传，服务端会自动生成。

## 1. Healthz

方法：

- `vmm.v1.VMMService/Healthz`

示例：

```powershell
grpcurl -plaintext `
  -d '{}' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/Healthz
```

预期响应：

```json
{
  "status": "ok",
  "traceId": "trc_xxx"
}
```

## 2. Chat

用途：

- 接收一条聊天消息
- 先做 PII 脱敏
- 再写入长期库
- 返回脱敏后的文本

方法：

- `vmm.v1.VMMService/Chat`

示例：

```powershell
grpcurl -plaintext `
  -d '{
    "sessionId": "sess_001",
    "message": "你好，我的电话是 13800138000",
    "acceptLanguage": "zh-CN"
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/Chat
```

预期响应示例：

```json
{
  "sessionId": "sess_001",
  "message": "你好，我的电话是 [MOBILE_MASKED]",
  "language": "zh-CN",
  "traceId": "trc_xxx"
}
```

说明：

- `Chat` 只是临时测试入口
- 它会复用与 `PostAction` 相同的 DockDB 存储后端

## 3. PreCheck

用途：

- 当前版本保持保守模式
- 无论传什么，都会返回“不注入”

方法：

- `vmm.v1.VMMService/PreCheck`

请求字段：

- `sessionId`
- `userId`
- `teamId`
- `spaceId`
- `projectId`
- `userContent`

示例：

```powershell
grpcurl -plaintext `
  -d '{
    "sessionId": "sess_001",
    "userId": "usr_001",
    "teamId": "team_001",
    "spaceId": "space_001",
    "projectId": "proj_001",
    "userContent": "请回忆一下我之前说过的内容"
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/PreCheck
```

预期响应：

```json
{
  "shouldInject": false,
  "contextText": "",
  "contextItems": [],
  "degraded": false,
  "traceId": "trc_xxx"
}
```

## 4. PostAction

用途：

- 新的字符串契约入口
- 收到后立即返回 `accepted=true`
- 后台继续复用旧版持久化主线

方法：

- `vmm.v1.VMMService/PostAction`

请求字段：

- `sessionId`
- `userId`
- `teamId`
- `spaceId`
- `projectId`
- `userContent`
- `assistantContent`
- `timeline`

其中：

- `userContent` 表示首轮用户提问
- `assistantContent` 表示最后一条助手回答
- `timeline` 表示两者之间的中间流程

示例：

```powershell
grpcurl -plaintext `
  -d '{
    "sessionId": "sess_001",
    "userId": "usr_001",
    "teamId": "team_001",
    "spaceId": "space_001",
    "projectId": "proj_001",
    "userContent": "最开始的问题",
    "assistantContent": "最后的回答",
    "timeline": [
      { "type": "assistant", "content": "中间回答" },
      { "type": "user", "content": "补充提问" }
    ]
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/PostAction
```

预期响应：

```json
{
  "accepted": true,
  "traceId": "trc_xxx"
}
```

说明：

- `timeline` 非空时会跳过 `NoiseGate`
- `timeline` 为空时，才会继续执行标准噪声门判定
- 服务端会输出两份控制台日志，分别是：
  - 原始请求
  - 清洗后请求
- `userContent`、`timeline[].content`、`assistantContent` 在入库前会额外执行：
  - 媒体与 base64 清理
  - 机器文本压缩
  - token 预算裁剪

## 5. PostActionOld

用途：

- 保留旧版原始快照契约
- 适合仍然上传 `raw_messages_snapshot` 的调用方

方法：

- `vmm.v1.VMMService/PostActionOld`

请求字段：

- `sessionId`
- `userId`
- `teamId`
- `spaceId`
- `projectId`
- `rawMessagesSnapshot`

其中每个 `rawMessagesSnapshot` 节点包括：

- `role`
- `contentJson`
- `toolCalls`
- `metaJson`

说明：

- `contentJson` 需要传 JSON 字符串
- 例如纯文本 `"你好"` 应传成 `"\"你好\""`

示例：

```powershell
grpcurl -plaintext `
  -d '{
    "sessionId": "sess_001",
    "userId": "usr_001",
    "teamId": "team_001",
    "projectId": "proj_001",
    "rawMessagesSnapshot": [
      {
        "role": "user",
        "contentJson": "\"你好\""
      },
      {
        "role": "assistant",
        "contentJson": "\"已收到\""
      }
    ]
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/PostActionOld
```

预期响应：

```json
{
  "accepted": true,
  "traceId": "trc_xxx"
}
```

## 6. SeedMemory

用途：

- 管理员主动灌入一条向量记忆

方法：

- `vmm.v1.VMMService/SeedMemory`

请求字段：

- `userId`
- `projectId`
- `memoryText`
- `spaceId`

示例：

```powershell
grpcurl -plaintext `
  -d '{
    "userId": "usr_001",
    "projectId": "proj_001",
    "memoryText": "fastapi backend framework decision",
    "spaceId": "space_001"
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/SeedMemory
```

预期响应：

```json
{
  "accepted": true,
  "memoryId": "mem_xxx",
  "traceId": "trc_xxx"
}
```

## 常见错误

当前业务层错误会使用标准 gRPC status code，并尽量附带 `ErrorInfo`：

- `GRPC_VALIDATION_FAILED`
- `GRPC_ROUTE_DISABLED`
- `UPSTREAM_TIMEOUT`
- `INTERNAL_ERROR`

例如参数缺失时，通常会得到：

- gRPC code: `InvalidArgument`
- ErrorInfo.reason: `GRPC_VALIDATION_FAILED`

需要注意：

- `grpc.max_receive_message_bytes` 超限通常是在 gRPC 传输层直接被拒绝
- 这类错误一般会返回 `ResourceExhausted`
- 不保证一定附带业务层组装的 `ErrorInfo`

## 大小限制

当前服务使用：

- `grpc.max_receive_message_bytes`

默认值：

- `1048576`，也就是 1MB

超过后会直接被 gRPC 服务端拒绝，通常表现为 `ResourceExhausted`。

## 当前联调建议

推荐按这个顺序测试：

1. `Healthz`
2. `Chat`
3. `PostAction`
4. `PostActionOld`
5. `SeedMemory`
6. `PreCheck`

这样可以先确认：

- 服务启动正常
- 脱敏正常
- 持久化路径正常
- 新旧 post-action 契约都可用
- 向量灌库正常
- pre-check 当前仍然处于“固定不注入”状态
