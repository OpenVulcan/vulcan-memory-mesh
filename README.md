# VMM OSS Local (Go)

VulcanMemoryMesh 当前聚焦本地开源版本：插件在主模型调用前通过 `pre-check` 获取可注入上下文，在对话结束后通过 `post-action` 写入本地关系存储；联调环境可使用 `seed-memory` 预热向量库。

## 目录

```text
cmd/
  vmm-local/
configs/
deploy/sql/
internal/
  app/
  config/
  logic/
  platform/
  adapters/
    inbound/http/
    outbound/
      memory_mock/
      openai_native/
scripts/
```

## 已实现内容

- `POST /v1/chat/pre-check`
- `POST /v1/chat/post-action`
- `POST /v1/admin/seed-memory`
- `cmd/vmm-local` 本地启动入口
- `MockPersonaProvider`
- 内存向量库 / 内存关系库存根实现
- OpenAI 兼容原生 LLM / Embedding 适配器
- TraceID / Recovery / 请求日志中间件
- 路由级超时控制
- 优雅停机顺序
- 单元测试与 HTTP 回归测试样例

## 核心规则

### pre-check

- 只取最近 2 轮 `history_content`
- `is_first_turn=true` 时并发执行「画像读取 + 语义检索」
- LLM 意图提炼固定 2 秒超时，失败自动降级为原问题
- 使用 `SearchFilter{UserID, ProjectID, SpaceID}` 检索
- 固定顺序拼装：
  1. 项目约束
  2. 个人画像
  3. 偏好习惯
  4. 向量召回记忆

### post-action

- 单次线性清洗
- 丢弃 `tool/system`
- 丢弃带 `tool_calls` 的节点
- 清除 `<think>...</think>` 等思维链标签
- 仅保留用户问题与 AI 最终回复，并压缩成 `[]NormalizedTurn`

### seed-memory

- 将 `memory_text` 向量化
- 组装 `MemoryRecord`
- 写入向量库

## 启动示例

### local

```bash
go run ./cmd/vmm-local/main.go -config configs/local.json
```

### OpenAI

```bash
go run ./cmd/vmm-local/main.go -config configs/openai.local.example.json
```

`llm.provider` 与 `embedding.provider` 可配置为 `openai`、`openai_go` 或 `openai_native`。如果你需要显式透传组织或项目头，可以使用 `organization` 和 `project` 字段；任何 OpenAI-compatible endpoint 都可以直接通过 `endpoint` 接入。

## 测试

```bash
go test ./...
```

## 说明

- `MockPersonaProvider` 对 `usr_8899` 返回固定画像
- 内存向量库支持 `user/project/space` 范围过滤
- `post-action` 默认写入内存关系库，适合本地联调和开源版最小运行集
