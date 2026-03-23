# VMM HTTP 接口测试说明（中文）

本文档面向测试同学，描述当前 VulcanMemoryMesh 本地版对外 HTTP 接口的调用方式、请求参数、返回结构和常见错误，便于联调和回归测试。

## 1. 服务地址

默认监听地址来自 `configs/local.json`：

```json
{
  "http": {
    "listen_addr": "127.0.0.1:17625"
  }
}
```

因此，本地默认 Base URL 为：

```text
http://127.0.0.1:17625
```

## 2. 通用规则

### 2.1 请求头

- `Content-Type: application/json`
- `/chat` 建议带上 `Accept-Language`，例如 `zh-CN`

### 2.2 请求体大小限制

- 默认最大请求体为 `1MB`
- 超过后会直接返回 `413`

### 2.3 统一返回结构

所有公开接口统一返回如下结构：

```json
{
  "code": 200,
  "msg": "ok",
  "data": {},
  "trace_id": "trc_xxx"
}
```

失败时会额外包含：

```json
{
  "code": 400,
  "msg": "request validation failed",
  "error_id": "HTTP_VALIDATION_FAILED",
  "error_category": "validation",
  "trace_id": "trc_xxx"
}
```

## 3. 接口清单

当前开放接口如下：

- `GET /healthz`
- `POST /chat`
- `POST /vmm/pre-check`
- `POST /vmm/post-action`
- `POST /vmm/post-action-old`
- `POST /v1/admin/seed-memory`

说明：

- `/v1/admin/seed-memory` 只有在 `admin.seed_enabled=true` 时开放
- 当前默认配置下该接口是开启的

## 4. 健康检查

### 4.1 请求

```bash
curl http://127.0.0.1:17625/healthz
```

### 4.2 返回

```json
{
  "code": 200,
  "msg": "ok",
  "data": {
    "status": "ok"
  },
  "trace_id": "trc_xxx"
}
```

## 5. /chat

`/chat` 是轻量脱敏归档入口。它会对 `message` 做 PII 脱敏，然后把脱敏后的文本写入 SQLite，并把脱敏结果直接返回。

### 5.1 请求体

```json
{
  "session_id": "chat_001",
  "message": "你好，我的电话是 13800138000"
}
```

### 5.2 curl 示例

```bash
curl -X POST http://127.0.0.1:17625/chat \
  -H "Content-Type: application/json" \
  -H "Accept-Language: zh-CN" \
  -d "{\"session_id\":\"chat_001\",\"message\":\"你好，我的电话是 13800138000\"}"
```

### 5.3 PowerShell 示例

```powershell
Invoke-RestMethod -Uri "http://127.0.0.1:17625/chat" `
  -Method Post `
  -Headers @{
    "Content-Type" = "application/json"
    "Accept-Language" = "zh-CN"
  } `
  -Body '{
    "session_id": "chat_001",
    "message": "你好，我的电话是 13800138000"
  }'
```

### 5.4 返回示例

```json
{
  "code": 200,
  "msg": "ok",
  "data": {
    "session_id": "chat_001",
    "message": "你好，我的电话是 [MOBILE_MASKED]",
    "language": "zh-CN"
  },
  "trace_id": "trc_xxx"
}
```

## 6. /vmm/pre-check

`pre-check` 当前版本保留接口，但已经固定短路，不再执行画像加载、意图提炼、Embedding 或向量召回。  
也就是说，只要请求格式合法，它就会稳定返回“不注入”。

### 6.1 请求体

当前请求体只保留这些字段：

- `session_id` 必填
- `user_id` 必填
- `team_id` 必填
- `project_id` 必填
- `space_id` 可选
- `user_content` 必填

示例：

```json
{
  "session_id": "sess_001",
  "user_id": "usr_001",
  "team_id": "team_001",
  "space_id": "space_001",
  "project_id": "proj_001",
  "user_content": "请帮我看一下这个问题"
}
```

### 6.2 curl 示例

```bash
curl -X POST http://127.0.0.1:17625/vmm/pre-check \
  -H "Content-Type: application/json" \
  -d "{\"session_id\":\"sess_001\",\"user_id\":\"usr_001\",\"team_id\":\"team_001\",\"space_id\":\"space_001\",\"project_id\":\"proj_001\",\"user_content\":\"请帮我看一下这个问题\"}"
```

### 6.3 返回示例

```json
{
  "code": 200,
  "msg": "ok",
  "data": {
    "should_inject": false,
    "context_text": "",
    "context_items": [],
    "degraded": false
  },
  "trace_id": "trc_xxx"
}
```

### 6.4 当前行为说明

- 接口仍会做基础校验
- `user_content` 仍会做文本净化
- 但不会返回任何记忆注入内容

## 7. /vmm/post-action

`/vmm/post-action` 是新的字符串契约入口。它会先同步校验请求结构，校验通过后立即返回 `accepted=true`，然后在后台继续复用旧版写库逻辑。

### 7.1 请求体

```json
{
  "session_id": "sess_001",
  "user_id": "usr_001",
  "team_id": "team_001",
  "space_id": "space_001",
  "project_id": "proj_001",
  "user_content": "最开始的问题",
  "assistant_content": "最后的回答",
  "timeline": [
    {
      "type": "assistant",
      "content": "中间回答"
    },
    {
      "type": "user",
      "content": "用户补充提问"
    }
  ]
}
```

### 7.2 字段规则

- `session_id`
  - 必填
- `user_id` / `team_id` / `space_id` / `project_id`
  - 可不传
  - 为空时后台处理阶段会补成 `default`
- `user_content`
  - 必填
  - 必须是字符串
  - 表示当前用户首轮提问
- `assistant_content`
  - 必填
  - 必须是字符串
  - 表示当前助手最后回答
- `timeline`
  - 允许为空数组
  - 如果存在元素，则必须是数组对象
  - 每项必须是：
    - `type`: `user` 或 `assistant`
    - `content`: 字符串

### 7.3 时间线语义

新接口的含义是：

- `user_content`：首轮用户问题
- `assistant_content`：最后一条助手回答
- `timeline`：两者之间的中间流程

也就是说，真实处理顺序是：

1. `user_content`
2. `timeline`
3. `assistant_content`

因此：

- `timeline` 可以为空
- `timeline` 可以只包含 1 条或多条中间消息
- `timeline` 的第一项不要求必须是 `user`
- `timeline` 的最后一项也不要求必须是 `assistant`
- 但时间线里的每一项都必须是标准文本节点：
  - `type` 只能是 `user` 或 `assistant`
  - `content` 必须是字符串

### 7.4 curl 示例

```bash
curl -X POST http://127.0.0.1:17625/vmm/post-action \
  -H "Content-Type: application/json" \
  -d "{\"session_id\":\"sess_001\",\"user_id\":\"usr_001\",\"team_id\":\"team_001\",\"space_id\":\"space_001\",\"project_id\":\"proj_001\",\"user_content\":\"最开始的问题\",\"assistant_content\":\"最后的回答\",\"timeline\":[{\"type\":\"assistant\",\"content\":\"中间回答\"},{\"type\":\"user\",\"content\":\"用户补充提问\"}]}"
```

### 7.5 返回示例

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

### 7.6 说明

- 这个接口在返回 `200 accepted=true` 之后，才会在后台继续处理
- 后台仍然复用旧版：
  - 文本净化
  - `MessageNormalizer`
  - `NoiseGate`
  - 关系存储写入
- 如果 `timeline` 为空，后台会自动用顶层：
  - `user_content`
  - `assistant_content`
  合成一个最小单轮对话

## 8. /vmm/post-action-old

`post-action-old` 仍然保留当前旧版快照写库逻辑，适合尚未迁移到新字符串契约的测试方。

### 8.1 请求体结构

```json
{
  "session_id": "sess_001",
  "user_id": "usr_001",
  "team_id": "team_001",
  "space_id": "space_001",
  "project_id": "proj_001",
  "raw_messages_snapshot": [
    {
      "role": "user",
      "content": "请帮我检查数据库状态"
    },
    {
      "role": "assistant",
      "content": "目前数据库状态正常。"
    }
  ]
}
```

## 9. /v1/admin/seed-memory

该接口用于向向量库存入调试用记忆，方便验证召回链路。  
虽然当前 `pre-check` 已固定不注入，但这个接口仍可用于联调其它存储与向量逻辑。

### 9.1 请求体

```json
{
  "user_id": "usr_001",
  "project_id": "proj_001",
  "space_id": "space_001",
  "memory_text": "后端统一使用 FastAPI。"
}
```

### 9.2 curl 示例

```bash
curl -X POST http://127.0.0.1:17625/v1/admin/seed-memory \
  -H "Content-Type: application/json" \
  -d "{\"user_id\":\"usr_001\",\"project_id\":\"proj_001\",\"space_id\":\"space_001\",\"memory_text\":\"后端统一使用 FastAPI。\"}"
```

### 9.3 返回示例

```json
{
  "code": 200,
  "msg": "ok",
  "data": {
    "accepted": true,
    "memory_id": "mem_xxx"
  },
  "trace_id": "trc_xxx"
}
```

## 10. 常见错误码

| HTTP 状态 | error_id | 含义 |
| --- | --- | --- |
| 400 | `HTTP_INVALID_JSON` | 请求体不是合法 JSON，或存在未知字段 |
| 400 | `HTTP_VALIDATION_FAILED` | 字段缺失、长度超限或格式不符合要求 |
| 403 | `HTTP_ROUTE_DISABLED` | 路由被显式关闭 |
| 404 | `HTTP_NOT_FOUND` | 路径不存在 |
| 405 | `HTTP_METHOD_NOT_ALLOWED` | 请求方法不正确 |
| 413 | `HTTP_REQUEST_TOO_LARGE` | 请求体超过大小限制 |
| 504 | `UPSTREAM_TIMEOUT` | 上游处理超时 |
| 500 | `INTERNAL_ERROR` | 服务器内部错误 |

## 11. 测试建议

建议测试同学至少覆盖以下场景：

### 11.1 /chat

- 中文手机号脱敏
- Bearer Token 脱敏
- `Accept-Language: zh-CN`
- 超过 1MB 请求体

### 11.2 /vmm/pre-check

- 合法请求固定返回 `should_inject=false`
- `user_content` 缺失时报 `HTTP_VALIDATION_FAILED`
- 未知字段时报 `HTTP_INVALID_JSON`

### 11.3 /vmm/post-action

- 顶层 `user_content` / `assistant_content` 传字符串
- `timeline` 必须是数组
- `timeline` 里的每一项都必须是 `user/assistant` 文本节点
- 非字符串 `user_content` / `assistant_content` / `timeline[*].content` 应直接报错
- 同步响应应立即返回，后台继续处理
- 校验通过后，控制台会额外输出一条 `post-action received` 日志，便于确认实际收到的 `user_content / assistant_content / timeline`

### 11.4 /vmm/post-action-old

- 标准 `user -> assistant` 快照成功写入
- `compat` 模式自动清理媒体与非文本块
- `strict` 模式拒绝非标准内容
- 拒答、元问题、会话样板会被噪声门过滤

## 12. 说明补充

- 当前标准构建方式是：

```bash
./make.ps1 build
```

或：

```bash
./make.bat build
```

- 正式运行产物位于：

```text
output/bin/vmm-local.exe
```

- 正式配置目录位于：

```text
output/configs/
```
