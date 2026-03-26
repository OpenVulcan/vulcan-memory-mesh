# VMM OSS Local (Go)

VulcanMemoryMesh 当前聚焦本地开源版本，并且已经把入站服务从 HTTP 切换为 gRPC。当前主线能力仍然保持保守节奏：

- `PreCheck` 会固定返回“不注入”
- `PostAction` 负责接收、清洗并持久化会话文本
- `SeedMemory` 用于本地向量库预热
- TLS 已从应用内移除；如需 TLS，请在服务前使用 [Caddy](https://caddyserver.com/)

## 文档导航

- [gRPC 对接说明（中文）](./docs/grpc-integration-guide_CN.md)
- [gRPC 接口测试说明（中文）](./docs/api-test-guide_CN.md)
- [DockDB Schema 版本管理说明（中文）](./docs/dockdb-schema-versioning_CN.md)
- [post-action 接口说明（中文）](./docs/post-action-guide_CN.md)
- [记忆准入噪声门说明（中文）](./docs/noise-gate-guide_CN.md)
- [后续记忆提炼与画像合并分析（非决案，中文）](./docs/memory-extraction-analysis_CN.md)

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
    inbound/grpcapi/
    outbound/
      memory_mock/
      openai_native/
      vldg_dockdb/
      vldg_lancedb/
scripts/
```

## 已实现内容

- gRPC 服务 `vmm.v1.VMMService`
- `Healthz`
- `Chat`
- `PreCheck`
- `PostAction`
- `PostActionOld`
- `SeedMemory`
- `cmd/vmm-local` 本地启动入口
- LanceDB 本地向量库适配器
- DockDB 本地长期库适配器
- 内存向量库 / 内存关系库存根回退实现
- OpenAI 兼容原生 LLM / Embedding 适配器（运行时不再提供 mock 模型）
- TraceID / Recovery / 请求日志拦截器
- 路由级超时控制
- 优雅停机顺序
- 单元测试与 gRPC 回归测试样例

## 核心规则

### PreCheck

- 当前版本在用例入口固定短路，不会触发画像加载、意图提炼、embedding 或向量召回
- 对任意合法请求都返回：
  - `should_inject = false`
  - `context_text = ""`
  - `context_items = []`
- 当前请求字段只保留：
  - `session_id`
  - `user_id`
  - `team_id`
  - `space_id`
  - `project_id`
  - `user_content`

### PostAction

- `PostAction` 是新的字符串契约入口
- 顶层只接收：
  - `user_content`
  - `assistant_content`
  - `timeline`
- `timeline` 非空时会跳过 `NoiseGate`
- `timeline` 为空时会继续执行标准噪声门判定
- 方法会先返回 `accepted=true`，然后在后台继续复用旧版持久化逻辑
- `PostActionOld` 仍保留旧版原始快照契约
- 入站清洗会处理：
  - `<think>`
  - base64 媒体数据
  - Markdown/HTML 图片与附件链接
- 缺失的 `user/team/space/project` 会自动补成 `default`

### SeedMemory

- 将 `memory_text` 向量化
- 组装 `MemoryRecord`
- 写入 LanceDB

## 启动示例

### 标准构建与运行

```powershell
.\make.ps1 build
.\output\bin\vmm-local.exe
```

### 用户覆盖目录

```powershell
.\output\bin\vmm-local.exe -config ~/.vmm
```

`-config` 现在表示“覆盖根目录”，不是单个配置文件路径。标准运行时会先读取 `output/configs/local.json`，再叠加 `~/.vmm/local.json` 或 `-config` 指向目录中的 `local.json`。

### gRPC 监听与 TLS

- 当前服务监听地址来自 `grpc.listen_addr`
- 当前服务只暴露纯 gRPC，不再内建 TLS
- 如果你需要 TLS、域名或公网入口，请在前面使用 Caddy 反代

### gRPC 调试

- 当前运行时已开启 gRPC reflection
- 可以直接用 `grpcurl` 枚举服务和方法，而不需要额外传 proto 文件

## 配置说明

当前关键配置项包括：

- `grpc.listen_addr`
- `grpc.max_receive_message_bytes`
- `grpc.request_timeout.*`
- `dockdb.address`
- `lancedb.address`
- `lancedb.table_name`
- `lancedb.vector_column`
- `llm.*`
- `embedding.*`
- `post_action.input_mode`
- `noise.*`

如果某个兼容模型需要额外参数，例如关闭 thinking、调整 `reasoning_effort` 或透传 provider 专属字段，可以在配置里使用：

- `llm.params`
- `llm.model_params`
- `embedding.params`
- `embedding.model_params`

优先级是：

1. 基础 `params`
2. 命中模型名的 `model_params`
3. 运行时请求级 `ProviderHints`

## 测试

```powershell
go test ./...
```

如果你要验证真实模型链路，请先确保：

- `output/configs/local.json` 已配置为 OpenAI-compatible provider
- `output/configs/.env` 已存在并包含可用的 API Key / Base URL / Model

## 说明

- `MockPersonaProvider` 对 `usr_8899` 返回固定画像
- `post-action` 默认写入 DockDB 长期库；如需最小化本地调试，可显式改成 `relational.provider=memory`
