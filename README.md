# VMM OSS Local (Go)

VulcanMemoryMesh 当前聚焦本地开源版本：插件在主模型调用前可以调用 `pre-check`，但当前版本会固定返回“不注入”；对话结束后通过 `post-action` 写入本地关系存储；联调环境可使用 `seed-memory` 预热向量库。

## 文档导航

- [post-action 接口说明（中文）](./docs/post-action-guide_CN.md)
- [记忆准入噪声门说明（中文）](./docs/noise-gate-guide_CN.md)
- [HTTP 接口测试说明（中文）](./docs/api-test-guide_CN.md)

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

- `POST /vmm/pre-check`
- `POST /vmm/post-action`
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

- 当前版本在用例入口固定短路，不会触发画像加载、意图提炼、embedding 或向量召回
- 对任意合法请求都返回：
  1. `should_inject = false`
  2. `context_text = ""`
  3. `context_items = []`
- 当前请求体只保留：
  - `session_id`
  - `user_id`
  - `team_id`
  - `space_id`
  - `project_id`
  - `user_content`
- `current_content`、`history_content` 和 `is_first_turn` 已从入口契约移除
- 输入仍会经过请求校验和文本净化，以保证接口契约稳定

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

噪声门的语义原型向量现在会缓存到 SQLite：

- 启动时优先从 `archive.path` 指向的 SQLite 数据库读取
- 只有在模型、维度或规则内容指纹变化时才会重新计算
- 重算后的结果会回写到 SQLite，避免每次启动重复消耗 embedding 调用

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
