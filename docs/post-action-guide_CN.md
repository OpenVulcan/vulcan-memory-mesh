# VMM `PostAction` 接口说明（中文）

## 文档目标

这份文档说明当前主线版本唯一有效的 `PostAction` gRPC 契约、清洗流程、噪声门位置，以及最终如何写入 DuckDB。

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
6. 后台继续把脱水后的 turn 记录写入 DuckDB

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
    - 原始 `timeline[*]`
    - 顶层 `assistant_content`
12. 对 turn 做脱水：
    - 顶层 `user_content` 保留
    - `timeline[*].type=user` 的内容保留
    - `timeline[*].type=assistant` 的内容替换为固定占位文本
    - 顶层 `assistant_content` 保留
13. 计算脱水 JSON 的 token 预算
14. 追加到 DuckDB：
    - `vmm_turn_records`
    - 同步更新 `vmm_sessions.turn_count / updated_timestamp`

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

不会直接把原始请求 JSON 原样写入数据库。

真正持久化的是清洗后的 turn 脱水 JSON。

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
    "input_mode": "compat"
  }
}
```

当前真正相关的配置项：

- `grpc.max_receive_message_bytes`
- `grpc.request_timeout.post_action`
- `post_action.input_mode`
- `noise.enabled`
- `noise.default_language`
- `noise.semantic_enabled`
- `noise.semantic_threshold`

## 当前限制

- 当前主线不再支持旧的 `raw_messages_snapshot` 契约
- 当前主线不再支持 `team_id / space_id` 由客户端直接传入
- 当前 `PostAction` 只接受纯文本字段，不接受原始消息节点对象
- 当前 `PostAction` 返回的是“已接收”，不是“已写库完成”
