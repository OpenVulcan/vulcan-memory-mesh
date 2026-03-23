# VMM `post-action` 接口说明（中文）

## 文档目标

这份文档同时说明：

1. 新版字符串契约入口 `POST /vmm/post-action`
2. 旧版快照入口 `POST /vmm/post-action-old`

以及它们与后台写库逻辑之间的关系、输入参数、配置项和清洗规则。

如果你要对接插件、Agent 网关或调试原始会话上报，这份文档应作为首选参考。

## 接口定位

新版 `post-action` 用于接收一组更稳定的文本参数：

- `user_content`
- `assistant_content`
- `timeline`

它会先同步校验参数是否合法，随后立即返回 `accepted=true`，并在后台继续复用旧版写库逻辑。

旧版 `post-action-old` 则用于接收完整原始快照，并在入口侧完成二次安全过滤与格式规范化，然后把可保留的 `user -> assistant` 问答轮次写入本地关系存储。

说明：

- `POST /vmm/post-action` 现在已经启用新版字符串契约
- `POST /vmm/post-action-old` 继续保留旧版快照契约，后续确认无价值后再清理

它的设计目标不是“完整归档插件传来的所有原始节点”，而是：

1. 允许插件上报完整快照
2. 在 VMM 内部再次过滤潜在脏数据
3. 最终只持久化可接受的、可解释的用户问答文本

另外，`post-action` 在标准化完成后还会进入一层“记忆准入噪声门（Noise Gate）”。

这层不会再修改文本内容，而是判断这一轮问答是否值得进入长期记忆。例如：

- “你还记得吗”“我之前说过什么来着”这类元问题
- “我不记得”“没有相关记忆”这类拒答
- `fresh session`、`query -> none` 这类样板或诊断残留

如果你要详细了解它的规则结构和配置方式，请参考：

- [docs/noise-gate-guide_CN.md](./noise-gate-guide_CN.md)

## 新版路由：`POST /vmm/post-action`

```http
POST /vmm/post-action
Content-Type: application/json
```

### 新版请求体结构

```json
{
  "session_id": "sess_123",
  "user_id": "usr_8899",
  "team_id": "team_001",
  "space_id": "space_001",
  "project_id": "proj_abc",
  "user_content": "首轮问题",
  "assistant_content": "最后回答",
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

### 新版字段规则

- `session_id`
  - 必填
- `user_id` / `team_id` / `space_id` / `project_id`
  - 可选
  - 为空时后台处理阶段补成 `default`
- `user_content`
  - 必填
  - 必须是字符串
  - 表示当前用户首轮提问
- `assistant_content`
  - 必填
  - 必须是字符串
  - 表示当前助手最后回答
- `timeline`
  - 必须是数组
  - 每项必须是：
    - `type`
    - `content`
  - `type` 只能是：
    - `user`
    - `assistant`
  - `content` 必须是字符串

### 新版时间线校验

`timeline` 表示位于顶层首轮 `user_content` 和最后一条 `assistant_content` 之间的中间流程。

这意味着：

- `user_content` 是首轮用户提问
- `assistant_content` 是最后一条助手回答
- `timeline` 不包含这两条边界文本
- `timeline` 可以为空数组
- `timeline` 里可以有 1 条、2 条或多条中间消息
- `timeline` 的第一项不强制必须是 `user`
- `timeline` 的最后一项也不强制必须是 `assistant`
- 但只要存在项，每项都必须满足：
  - `type` 只能是 `user` 或 `assistant`
  - `content` 必须是字符串

### 新版时间线与噪声门关系

新版 `post-action` 当前还有一条额外规则：

- 当 `timeline` 长度大于 `0` 时：
  - 视为复杂中间流程
  - 后台会跳过 `NoiseGate`
  - 直接继续主线持久化
- 当 `timeline` 长度等于 `0` 时：
  - 视为标准单轮 `user -> assistant`
  - 后台会继续执行默认噪声门流程
  - 包括正则规则和语义判定

这样做的原因是：

- 只要存在中间时间线，就说明这轮对话包含补充提问、打断、澄清或中间多轮交互
- 这类复杂流程不能再简单套用“首问 + 末答”的单轮噪声判断
- 因此当前只对 `timeline=[]` 的简单单轮请求执行标准噪声门

### 新版返回

同步返回仍然参考旧版：

```json
{
  "code": 200,
  "msg": "ok",
  "data": {
    "accepted": true
  },
  "trace_id": "trc_xxx"
}
```

但语义上有一个很重要的区别：

- 新版 `POST /vmm/post-action` 是“先确认接收，再后台处理”
- 也就是说，返回 `200` 后，旧版写库链路才在后台继续运行

### 新版后台处理方式

新版入口不会自己单独实现一套写库逻辑，而是把字符串契约转成内部标准快照后，继续复用旧版的：

- 文本净化
- `MessageNormalizer`
- `NoiseGate`（仅 `timeline=[]` 时启用）
- 关系存储写入

后台实际组装顺序固定是：

1. 顶层 `user_content`
2. `timeline` 中的中间消息
3. 顶层 `assistant_content`

如果 `timeline` 是空数组，则后台会只使用顶层两条文本，拼成一个最小单轮问答继续处理。
此时也会继续执行默认噪声门判定。

### 新版调试日志

新版 `POST /vmm/post-action` 在通过校验后，会把收到的内容输出到运行时控制台日志，便于本地联调时直接确认实际入参。

日志里当前会包含：

- `trace_id`
- `session_id`
- `user_content`
- `assistant_content`
- `timeline`

说明：

- 如果请求在校验阶段就返回 `400`，这条日志不会出现
- 是否同时打印原始 `request_body` 取决于：
  - `logging.log_request_bodies`
- 当前默认配置里该开关通常是关闭的，因此最稳定的调试日志是新版接口自己的 `post-action received` 记录

## 旧版路由：`POST /vmm/post-action-old`

```http
POST /vmm/post-action-old
Content-Type: application/json
```

## 请求体结构

当前请求体结构如下：

```json
{
  "session_id": "sess_123",
  "user_id": "usr_8899",
  "team_id": "team_001",
  "space_id": "space_001",
  "project_id": "proj_abc",
  "raw_messages_snapshot": [
    {
      "role": "user",
      "content": "你好"
    },
    {
      "role": "assistant",
      "content": "已收到"
    }
  ]
}
```

### 顶层字段

- `session_id`
  - 必填
  - 会话唯一标识
- `user_id`
  - 可选
  - 不传时自动补成 `default`
- `team_id`
  - 可选
  - 不传时自动补成 `default`
- `space_id`
  - 可选
  - 不传时自动补成 `default`
- `project_id`
  - 可选
  - 不传时自动补成 `default`
- `raw_messages_snapshot`
  - 可选
  - 缺失时按空数组处理
  - 最终是否有可持久化内容，要看清洗后的结果

## `raw_messages_snapshot` 节点结构

每个节点当前支持这些字段：

```json
{
  "role": "user | assistant | system | tool",
  "content": "... 任意合法 JSON ...",
  "tool_calls": [
    {
      "id": "call_1",
      "type": "function"
    }
  ],
  "meta": {}
}
```

### 字段说明

- `role`
  - 入口允许传 `user`、`assistant`、`system`、`tool`
  - 但最终只有 `user` / `assistant` 节点有机会进入持久化结果
- `content`
  - 允许是字符串
  - 允许是数组
  - 允许是对象
  - 是否接受，取决于 `post_action.input_mode`
- `tool_calls`
  - 在 `user` / `assistant` 节点上，最终不会被保留
- `meta`
  - 当前不会参与持久化逻辑

## 配置文件

相关配置位于：

- [configs/local.json](../configs/local.json)

当前与 `post-action` 直接相关的配置如下：

```json
{
  "http": {
    "max_request_body_bytes": 1048576,
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

- `http.max_request_body_bytes`
  - 整个请求体大小上限
  - 默认 `1048576`，也就是 1MB
  - 超限直接返回错误，不进入 JSON 解析
- `http.request_timeout.post_action`
  - `post-action` 路由级超时
- `post_action.input_mode`
  - 入口处理模式
  - 可选值：
    - `compat`
    - `strict`
- `noise.enabled`
  - 是否启用写库前噪声准入门
- `noise.default_language`
  - `post-action` 当前用于噪声判定的默认语言
- `noise.semantic_enabled`
  - 是否启用语义相似度判定
- `noise.semantic_threshold`
  - 默认语义阈值，类别可在规则文件中单独覆盖

## 两种输入模式

### 1. `compat` 兼容模式

这是当前默认模式。

目标是：

- 尽可能接受插件传来的复杂快照
- 自动修剪掉不符合要求的节点
- 最终保留能落库的有效文本

在兼容模式下：

- `system` 节点直接删除
- `tool` 节点直接删除
- 带 `tool_calls` 的 `user` / `assistant` 节点直接删除
- `content` 为字符串时：
  - 直接接受
  - 会过滤 `<think>...</think>` 等思维链标签
- `content` 为数组时：
  - 只保留 `type == "text"` 的块
  - 其他类型块直接丢弃
- `content` 为对象时：
  - 仅兼容 `{"type":"text","text":"..."}` 这种对象
  - 其他对象直接丢弃
- `user_id` / `team_id` / `space_id` / `project_id`
  - 缺失时自动补成 `default`

如果修剪后没有任何可落库内容：

- 接口仍然返回成功
- 但不会写出有效对话轮次

### 2. `strict` 严格模式

严格模式适合已经能保证输入质量的插件或网关。

目标是：

- 入口即拒绝非标准快照
- 让调用方尽早发现自己上报的数据结构不符合约定

在严格模式下：

- `system`、`tool` 节点仍会被直接删除
- 但以下情况会直接返回校验错误：
  - `role` 不是 `user / assistant / system / tool`
  - `user` / `assistant` 节点包含 `tool_calls`
  - `content` 不是字符串，也不是“纯文本数组块”
  - 数组中出现 `type != text` 的块
  - 文本块没有 `text` 字段
  - 文本块为空字符串

注意：

- `strict` 并不会要求“请求里完全不能出现 system/tool”
- 对于 `system` / `tool`，当前策略仍是删除，而不是报错
- 真正严格的是 `user` / `assistant` 节点必须是标准文本消息

## `content` 的接受规则

### 合法写法 1：字符串

```json
{
  "role": "assistant",
  "content": "已收到"
}
```

这是最推荐的写法。

### 合法写法 2：文本块数组

```json
{
  "role": "assistant",
  "content": [
    { "type": "text", "text": "第一段" },
    { "type": "text", "text": "第二段" }
  ]
}
```

处理后会拼成一个文本字符串。

### 兼容模式可接受的对象写法

```json
{
  "role": "assistant",
  "content": { "type": "text", "text": "已收到" }
}
```

这个写法仅在 `compat` 下会被兼容。

### 不合法或会被丢弃的示例

```json
{
  "role": "assistant",
  "content": [
    { "type": "image_url", "image_url": { "url": "https://example.com/a.png" } }
  ]
}
```

- `compat`：图片块会被直接丢弃
- `strict`：直接返回校验错误

## 入口清洗规则

`post-action` 当前把主要内容清洗集中在入口层完成一次，`normalizer` 不再重复做同样的剥离。

### 入口层清洗

文件位置：

- [validation.go](../internal/adapters/inbound/http/validation.go)

主要动作：

- 规范化 role
- 补齐默认 scope 值
- 删除 `system` / `tool`
- 删除带 `tool_calls` 的 `user` / `assistant`
- 只保留文本内容
- 过滤 `<think>` 标签
- 清理被压平到字符串里的富媒体垃圾内容，包括：
  - `data:image/...;base64,...`、`data:video/...;base64,...`、`data:audio/...;base64,...`
  - Markdown 图片，如 `![猫](https://cdn.example.com/cat.jpg)`
  - Markdown 文件或压缩包链接，如 `[logs](https://cdn.example.com/logs.zip)`
  - HTML 媒体标签，如 `<img ...>`、`<video ...>`、`<audio ...>`
  - 裸露的媒体/附件 URL，如 `https://.../a.png`、`https://.../logs.zip`、`blob:...`、`file://...`
- 将上述内容替换成短占位符，例如：
  - `[图片已过滤]`
  - `[图片: 架构图]`
  - `[压缩包: logs]`
  - `[视频: demo.mp4]`

这样做的目的，是避免 base64 图片、压缩包下载链接和附件 URL 混入历史记忆，导致后续 token 爆炸或把无意义噪音送进 LLM。

### normalizer 层清洗

文件位置：

- [normalizer.go](../internal/logic/processor/normalizer.go)

主要动作：

- 再次过滤非 `user` / `assistant`
- 提取入口已经净化过的纯文本内容
- 只有完整的 `user -> assistant` 配对才会形成 `NormalizedTurn`

这意味着：

- 单独的 `user`
- 单独的 `assistant`
- 或残缺对话

都不会形成最终落库的标准轮次。

## 富媒体垃圾清洗示例

### 1. Base64 图片数据

输入：

```text
请参考 ![](data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA)
```

输出：

```text
请参考 [图片已过滤]
```

### 2. Markdown 图片

输入：

```text
请看这个截图 ![接口响应截图](https://cdn.example.com/shot.png)
```

输出：

```text
请看这个截图 [图片: 接口响应截图]
```

### 3. Markdown 压缩包链接

输入：

```text
日志在这里 [logs](https://cdn.example.com/logs.zip)
```

输出：

```text
日志在这里 [压缩包: logs]
```

### 4. HTML 媒体标签

输入：

```text
<video src="https://cdn.example.com/demo.mp4"></video>
```

输出：

```text
[视频: demo.mp4]
```

## 最终持久化内容

`post-action` 最终不会保存原始快照，而是保存标准化后的轮次：

- `TurnIndex`
- `UserMessage`
- `AssistantReply`
- `CreatedAt`

并刷新会话元数据：

- `SessionID`
- `UserID`
- `TeamID`
- `SpaceID`
- `ProjectID`
- `UpdatedAt`

当前本地 OSS 版本默认写入的是内存关系存储，不是 SQLite。

## 返回格式

成功返回：

```json
{
  "code": 200,
  "msg": "ok",
  "data": {
    "accepted": true
  },
  "trace_id": "trc_xxx"
}
```

### 注意

这里的 `accepted: true` 并不等于“一定写出了有效轮次”。

例如：

- 快照全是 `system`
- 快照只有工具调用
- 修剪后没有完整 `user -> assistant` 配对

都可能返回成功，但不会产生有效落库轮次。

## 常见错误场景

### 1. 严格模式下数组里混入非文本块

请求：

```json
{
  "session_id": "s1",
  "raw_messages_snapshot": [
    {
      "role": "user",
      "content": [
        { "type": "text", "text": "你好" },
        { "type": "image_url", "image_url": { "url": "https://example.com" } }
      ]
    }
  ]
}
```

严格模式下会返回校验错误，类似：

```json
{
  "code": 400,
  "error_id": "HTTP_VALIDATION_FAILED",
  "msg": "raw_messages_snapshot[0].content[1].type: must be text"
}
```

### 2. 兼容模式下工具节点被自动修剪

请求里如果包含：

- `system`
- `tool`
- 带 `tool_calls` 的节点

兼容模式下不会报错，而是直接删除这些节点。

### 3. 兼容模式下 `content` 不是纯文本

如果 `content` 是复杂对象且无法提取文本：

- 兼容模式下直接丢弃该节点
- 严格模式下报错

## 推荐的插件上报方式

如果插件能力足够，建议直接上传已经扁平化的标准问答：

```json
{
  "session_id": "sess_123",
  "raw_messages_snapshot": [
    { "role": "user", "content": "帮我查一下数据库状态" },
    { "role": "assistant", "content": "目前数据库状态一切正常。" }
  ]
}
```

这是最稳定、最可预测的接入方式。

## 插件不能提前分解时怎么办

如果插件拿到的是更复杂的 Agent 快照，可以先使用 `compat` 模式接入。

这样 VMM 会：

- 自动丢弃无关节点
- 自动保留文本块
- 自动剥离 `<think>`
- 自动补 `default` 作用域

等插件侧逐步成熟后，再切换到 `strict`。

## 当前限制

- `post-action` 目前不支持多模态内容入库
- 图片、音频、文件类块不会被保留
- `meta` 目前不会参与持久化
- 返回结果不会告诉你“本次实际落了几条轮次”

如果后续需要进一步调试入口清洗行为，建议增加专门的 debug 响应或测试器，而不是放宽生产入口。
