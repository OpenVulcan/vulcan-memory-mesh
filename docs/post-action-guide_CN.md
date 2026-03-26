# VMM `PostAction` 接口说明（中文）

## 文档目标

这份文档说明当前 gRPC 版 `PostAction` / `PostActionOld` 的输入契约、后台处理方式、噪声门规则和调试行为。

当前相关方法：

- `vmm.v1.VMMService/PostAction`
- `vmm.v1.VMMService/PostActionOld`

## 接口定位

### 新版 `PostAction`

新版 `PostAction` 是字符串契约入口，用于接收：

- `user_content`
- `assistant_content`
- `timeline`

特点：

- 同步校验参数是否合法
- 立即返回 `accepted=true`
- 然后在后台继续复用旧版持久化主线

### 旧版 `PostActionOld`

旧版 `PostActionOld` 用于接收完整原始快照：

- `raw_messages_snapshot`

特点：

- 入口侧完成二次安全过滤与格式规范化
- 再把可保留的 `user -> assistant` 问答轮次写入长期存储

## 新版方法：`vmm.v1.VMMService/PostAction`

### 请求结构

```json
{
  "sessionId": "sess_123",
  "userId": "usr_8899",
  "teamId": "team_001",
  "spaceId": "space_001",
  "projectId": "proj_abc",
  "userContent": "首轮问题",
  "assistantContent": "最后回答",
  "timeline": [
    {
      "type": "assistant",
      "content": "中间回答"
    },
    {
      "type": "user",
      "content": "补充问题"
    }
  ]
}
```

### 字段规则

- `sessionId`
  - 必填
- `userId` / `teamId` / `spaceId` / `projectId`
  - 可选
  - 后台处理时为空会补成 `default`
- `userContent`
  - 必填
  - 必须是字符串
  - 表示当前用户首轮提问
- `assistantContent`
  - 必填
  - 必须是字符串
  - 表示当前助手最后回答
- `timeline`
  - 必须是数组
  - 每项都必须是：
    - `type=user|assistant`
    - `content` 为字符串

### 时间线语义

`timeline` 表示位于顶层首轮 `userContent` 和最后一条 `assistantContent` 之间的中间流程。

这意味着：

- `userContent` 是首轮用户提问
- `assistantContent` 是最后一条助手回答
- `timeline` 不包含这两条边界文本
- `timeline` 可以为空数组
- `timeline` 可以有 1 条或多条中间消息

### 时间线与噪声门关系

当前规则是：

- 当 `timeline` 长度大于 `0` 时：
  - 视为复杂中间流程
  - 后台跳过 `NoiseGate`
  - 直接继续主线持久化
- 当 `timeline` 长度等于 `0` 时：
  - 视为标准单轮 `user -> assistant`
  - 后台继续执行默认噪声门流程

### 同步返回

```json
{
  "accepted": true,
  "traceId": "trc_xxx"
}
```

说明：

- 这里的 `accepted=true` 表示“请求已接收”
- 不表示后台一定已经写库完成
- 真正的持久化和噪声门判断仍在后台继续执行

### 后台处理顺序

后台会按固定顺序把新版请求转换成旧版内部快照：

1. 顶层 `userContent`
2. `timeline` 中间流程
3. 顶层 `assistantContent`

然后继续执行旧版主线：

1. 文本净化
2. `MessageNormalizer`
3. `NoiseGate`（仅 `timeline=[]` 时启用）
4. 长期存储写入

### 调试日志

新版 `PostAction` 在通过校验后，会把收到的内容输出到运行时日志，方便本地联调直接确认入参。

日志里会包含：

- `trace_id`
- `session_id`
- `user_content`
- `assistant_content`
- `timeline`

## 旧版方法：`vmm.v1.VMMService/PostActionOld`

### 请求结构

```json
{
  "sessionId": "sess_123",
  "userId": "usr_8899",
  "teamId": "team_001",
  "spaceId": "space_001",
  "projectId": "proj_abc",
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
}
```

### 字段说明

- `sessionId`
  - 必填
- `userId` / `teamId` / `spaceId` / `projectId`
  - 可选
  - 为空时自动补成 `default`
- `rawMessagesSnapshot`
  - 可为空
  - 每项包括：
    - `role`
    - `contentJson`
    - `toolCalls`
    - `metaJson`

注意：

- `contentJson` 必须是 JSON 字符串
- 如果要表达纯文本 `"你好"`，应传成 `"\"你好\""`

## 输入模式

配置项：

- `post_action.input_mode`

可选值：

- `compat`
- `strict`

### `compat`

兼容模式会自动修剪：

- `system`
- `tool`
- `tool_calls`
- 非文本内容块

适合上游插件暂时还不能保证只上传标准问答文本的情况。

### `strict`

严格模式会直接拒绝不合规的 `user/assistant` 文本节点，例如：

- `content` 不是字符串或文本块数组
- 数组里混入 `type != text`
- `user/assistant` 节点携带 `tool_calls`

## 入口清洗规则

当前文本净化集中在入站层完成一次。

主要会处理：

- `<think>...</think>`
- base64 图片/视频/音频数据
- Markdown 图片与文件链接
- HTML 媒体标签
- 裸露的媒体/附件 URL

处理目标是：

- 避免把无意义媒体垃圾、超长 base64 文本或附件地址送进长期存储

## 最终持久化内容

当前不会直接保存原始节点，而是保存标准化后的问答轮次，并刷新会话元数据。

如果修剪后没有形成完整 `user -> assistant` 配对：

- 仍可能返回成功
- 但不会产生有效持久化内容

## 相关配置

示例：

```json
{
  "grpc": {
    "max_receive_message_bytes": 1048576,
    "request_timeout": {
      "post_action": "3s"
    }
  },
  "post_action": {
    "input_mode": "compat"
  }
}
```

### 配置项说明

- `grpc.max_receive_message_bytes`
  - 单次 gRPC 请求大小上限
  - 默认 1MB
- `grpc.request_timeout.post_action`
  - `PostAction` / `PostActionOld` 后台处理的超时
- `post_action.input_mode`
  - 入口处理模式
- `noise.enabled`
  - 是否启用写库前噪声准入门
- `noise.default_language`
  - 当前噪声判定默认语言
- `noise.semantic_enabled`
  - 是否启用语义相似度判定
- `noise.semantic_threshold`
  - 默认语义阈值

## 当前限制

- 当前不支持多模态内容入库
- 图片、音频、文件类块不会保留
- `metaJson` 当前不会进入长期记忆正文
- `PostAction` 只是立即确认接收，后台是否真正产生有效轮次要看清洗和标准化结果
