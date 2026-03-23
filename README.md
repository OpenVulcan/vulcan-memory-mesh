# VMM OSS Local (Go)

VulcanMemoryMesh 当前聚焦本地开源版本：插件在主模型调用前通过 `pre-check` 获取可注入上下文，在对话结束后通过 `post-action` 写入本地关系存储；联调环境可使用 `seed-memory` 预热向量库。

## 文档导航

- [post-action 接口说明（中文）](./docs/post-action-guide_CN.md)
- [记忆准入噪声门说明（中文）](./docs/noise-gate-guide_CN.md)

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
- OpenAI 兼容原生 LLM / Embedding 适配器（运行时不再提供 mock 模型）
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
- 严格模式下只接受标准 user/assistant 文本快照，额外内容直接报错
- 兼容模式下会自动修剪 `system/tool`、`tool_calls` 和非文本内容块
- 会在入口侧清理 `<think>` 标签、base64 媒体数据、Markdown/HTML 图片与附件链接
- 缺失的 `user/team/space/project` 会自动补成 `default`
- 标准化后还会进入“噪声准入门”，过滤拒答、元问题、会话样板和诊断残留
- 丢弃 `tool/system`
- 丢弃带 `tool_calls` 的节点
- 仅保留用户问题与 AI 最终回复，并压缩成 `[]NormalizedTurn`

### seed-memory

- 将 `memory_text` 向量化
- 组装 `MemoryRecord`
- 写入向量库

## 启动示例

### 标准构建与运行

```bash
./make.ps1 build
./output/bin/vmm-local.exe
```

### 用户覆盖目录

```bash
./output/bin/vmm-local.exe -config ~/.vmm
```

`-config` 现在表示“覆盖根目录”，不是单个配置文件路径。标准运行时会先读取 `output/configs/local.json`，再叠加 `~/.vmm/local.json` 或 `-config` 指向目录中的 `local.json`。

`llm.provider` 与 `embedding.provider` 当前都必须使用 `openai`、`openai_go` 或 `openai_native`。如果你需要显式透传组织或项目头，可以使用 `organization` 和 `project` 字段；任何 OpenAI-compatible endpoint 都可以直接通过 `endpoint` 接入。

如果某个兼容模型需要额外参数，例如关闭 thinking、调整 `reasoning_effort` 或透传 provider 专属字段，可以在配置里使用：

- `llm.params`
- `llm.model_params`
- `embedding.params`
- `embedding.model_params`

优先级是：

1. 基础 `params`
2. 命中模型名的 `model_params`
3. 运行时请求级 `ProviderHints`

这意味着不同模型可以通过配置自由指定不同的关闭方式或兼容参数，而不需要修改代码。

### post-action 入口模式

`post_action.input_mode` 当前支持：

- `compat`
  - 默认模式
  - 自动修剪 `system/tool`
  - 自动丢弃 `tool_calls`
  - 对内容数组只保留 `type=text` 的文本块
- `strict`
  - 遇到非标准 user/assistant 快照直接返回校验错误
  - 适合插件已经保证只上传标准问答文本的环境

### 噪声准入门

`noise` 当前支持：

- `enabled`
  - 是否启用写库前的噪声拦截
- `default_language`
  - 规则语言包，当前默认 `zh-CN`
- `semantic_enabled`
  - 是否启用语义相似度拦截
- `semantic_threshold`
  - 默认语义阈值，类别可在规则文件里覆盖

## 测试

```bash
go test ./...
```

如果你要验证真实模型链路，请先确保：

- `output/configs/local.json` 已配置为 OpenAI-compatible provider
- `output/configs/.env` 已存在并包含可用的 API Key / Base URL / Model

## 说明

- `MockPersonaProvider` 对 `usr_8899` 返回固定画像
- 内存向量库支持 `user/project/space` 范围过滤
- `post-action` 默认写入内存关系库，适合本地联调和开源版最小运行集
