# VMM v1 Gateway (Go)

这是一个按 `vmm-v1-gateway-architecture-plan.md` 与架构图要求从零搭建的 Go 版本 VMM v1 网关工程。

## 项目目标

VMM v1 是一个旁路式记忆服务，插件在主模型调用前调用 `pre-check` 获取可注入上下文，在会话结束后调用 `post-action` 做极速落盘；本地或内网环境可通过 `seed-memory` 预热向量库。

## 目录

```text
cmd/
  vmm-local/
  vmm-saas/
configs/
deploy/sql/
internal/
  app/
  config/
  platform/
  core/
    domain/
    ports/
    services/
  adapters/
    inbound/http/
    outbound/
      memory_mock/
      aliyun_dashscope/
      openai_native/
      aliyun_dashvector/
      postgres_store/
third_party/gin/
```

## 已实现内容

- 严格六边形分层
- `POST /v1/chat/pre-check`
- `POST /v1/chat/post-action`
- `POST /v1/admin/seed-memory`（仅 local 暴露）
- `cmd/vmm-local` 与 `cmd/vmm-saas` 双启动入口
- `MockPersonaProvider`
- 内存向量库 / 内存关系库存根实现
- DashScope / DashVector / Postgres 适配器骨架
- OpenAI `openai-go/v3` 原生 LLM / Embedding 适配器
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
go run ./cmd/vmm-local -config configs/local.json
```

### saas

```bash
go run ./cmd/vmm-saas -config configs/saas.json
```

### OpenAI

```bash
go run ./cmd/vmm-local -config configs/openai.local.example.json
```

`llm.provider` 与 `embedding.provider` 可配置为 `openai`、`openai_go` 或 `openai_native`。
如果你需要显式透传组织或项目头，可以使用 `organization` 和 `project` 字段。

> 题目明确说“无需考虑实际运行”，所以这里更重视逻辑完整性和工程边界完整性。`third_party/gin` 是为了让工程保持自包含；`postgres` / `aliyun` 适配器给出了完整逻辑骨架与请求/SQL 流程。

## 测试

```bash
go test ./...
```

## 说明

- `MockPersonaProvider` 对 `usr_8899` 返回固定画像
- 内存向量库支持 `user/project/space` 范围过滤
- Postgres 适配器将日志 upsert 与 session 更新时间刷新合并进同一事务，随后 `RefreshSession` 会识别同请求的重复刷新并跳过
