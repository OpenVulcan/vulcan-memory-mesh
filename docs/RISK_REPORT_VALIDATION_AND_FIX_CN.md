# VulcanMemoryMesh 风险报告核查与修复记录

## 使用说明

- 本文档先完整保留用户提供的 8 个候选问题原文要点，避免后续上下文压缩导致原始 claim 丢失。
- 每个问题后续都会补充：
  - `核查结论`：存在 / 不存在 / 部分成立
  - `是否需要修复`：需要 / 不需要
  - `验证依据`：实际代码路径、运行链路、测试或复现结果
  - `修复结果`：若已修复，记录具体改动与验证

---

## 1. 风险代码片段 1：硬编码默认值

### 原始 claim

- 文件：`internal/config/config.go`
- 片段：

```go
func DefaultLocal() Config {
    return Config{
        // ...
        LLM:        LLMConfig{Provider: "openai", Model: "gpt-4.1-mini"},
        Embedding:  EmbeddingConfig{Provider: "openai", Model: "text-embedding-3-large", Dimension: 1024},
        // ...
    }
}
```

### 原始风险描述

- 硬编码的模型名称和提供商可能导致安全配置问题。

### 原始修复建议

```go
func DefaultLocal() Config {
    defaultProvider := getEnvOrDefault("VMM_DEFAULT_PROVIDER", "openai")
    defaultModel := getEnvOrDefault("VMM_DEFAULT_LLM_MODEL", "")
    defaultEmbeddingModel := getEnvOrDefault("VMM_DEFAULT_EMBEDDING_MODEL", "")

    return Config{
        // ...
        LLM:        LLMConfig{Provider: defaultProvider, Model: defaultModel},
        Embedding:  EmbeddingConfig{Provider: defaultProvider, Model: defaultEmbeddingModel, Dimension: 1024},
        // ...
    }
}
```

### 核查结论

- 不存在报告所描述的安全缺陷

### 是否需要修复

- 不需要

### 验证依据

- `DefaultLocal()` 里的 provider/model 是运行时默认配置，不是敏感信息，也不是越权路径。
- 当前配置加载链已经支持：
  - 配置文件覆盖
  - `.env` 覆盖
  - 环境变量覆盖
- 若把默认模型直接改成空字符串，反而会让默认本地运行开箱即坏，不属于安全增强。
- 因此该问题更接近“默认值产品策略”，不是“安全漏洞”。

### 修复结果

- 未修改代码。
- 报告结论：原始 claim 误把“硬编码默认配置”判成“安全风险”。

---

## 2. 风险代码片段 2：敏感信息处理不当

### 原始 claim

- 文件：`internal/adapters/inbound/grpcapi/server.go`
- 片段：

```go
func (s *Server) logPostActionReceipt(traceID, message string, req *vmmv1.PostActionRequest) {
    if s == nil || s.logger == nil || req == nil {
        return
    }
    s.logger.Info(message, "trace_id", traceID, "session_id", req.GetSessionId(),
        "user_content", req.GetUserContent(), "assistant_content", req.GetAssistantContent(),
        "timeline", string(timelineJSON))
}
```

### 原始风险描述

- 日志中直接记录用户输入内容，可能包含敏感信息。

### 原始修复建议

```go
func (s *Server) logPostActionReceipt(traceID, message string, req *vmmv1.PostActionRequest) {
    if s == nil || s.logger == nil || req == nil {
        return
    }

    sanitizedUserContent := s.sanitizer.SanitizeForLog(req.GetUserContent())
    sanitizedAssistantContent := s.sanitizer.SanitizeForLog(req.GetAssistantContent())

    s.logger.Info(message, "trace_id", traceID, "session_id", req.GetSessionId(),
        "user_content_len", len(sanitizedUserContent),
        "assistant_content_len", len(sanitizedAssistantContent))
}
```

### 核查结论

- 真实存在

### 是否需要修复

- 需要

### 验证依据

- `internal/adapters/inbound/grpcapi/server.go` 的 `logPostActionReceipt(...)` 在真实热路径 `PostAction(...)` 中被调用两次：
  - `post-action received raw`
  - `post-action received cleaned`
- 修改前日志会直接写入：
  - `user_content`
  - `assistant_content`
  - `timeline`
- `server_test.go` 也明确断言日志中出现原始文本，说明这不是死代码，而是当前运行时行为。

### 修复结果

- 已修复。
- 修改内容：
  - 不再把用户/助手/timeline 原文写入日志。
  - 改为记录 `*_len` 和短 `sha256` 摘要，保留排障关联能力但不暴露正文。
- 受影响文件：
  - `internal/adapters/inbound/grpcapi/server.go`
  - `internal/adapters/inbound/grpcapi/server_test.go`
- 验证方式：
  - 更新测试，确认日志仍保留 raw/cleaned 两次 receipt 事件，但不再出现正文。

---

## 3. 风险代码片段 3：错误处理不一致

### 原始 claim

- 文件：`internal/app/app.go`
- 片段：

```go
func (a *Application) Shutdown(ctx context.Context) error {
    for i := len(a.Shutdowns) - 1; i >= 0; i-- {
        if err := a.Shutdowns[i].Shutdown(ctx); err != nil {
            return fmt.Errorf("shutdown dependency[%d]: %w", i, err)
        }
    }
    return nil
}
```

### 原始风险描述

- 一旦某个依赖关闭失败，其他依赖就不会被关闭，可能导致资源泄漏。

### 原始修复建议

```go
func (a *Application) Shutdown(ctx context.Context) error {
    var errs []error

    for i := len(a.Shutdowns) - 1; i >= 0; i-- {
        if err := a.Shutdowns[i].Shutdown(ctx); err != nil {
            errs = append(errs, fmt.Errorf("shutdown dependency[%d]: %w", i, err))
        }
    }

    if len(errs) > 0 {
        return fmt.Errorf("shutdown errors occurred: %v", errs)
    }
    return nil
}
```

### 核查结论

- 真实存在

### 是否需要修复

- 需要

### 验证依据

- `internal/app/app.go` 的 `Shutdown(...)` 在倒序关闭依赖时，遇到第一个错误就直接 `return`。
- 这会导致后续依赖不再执行关闭流程。
- 在当前代码里 `Shutdowns` 包含关系存储、向量存储、post-action 队列等资源，提前返回会留下真实的资源释放缺口。

### 修复结果

- 已修复。
- 修改内容：
  - 关闭流程改为继续尝试释放所有依赖。
  - 使用聚合错误返回最终失败信息。
- 受影响文件：
  - `internal/app/app.go`
  - `internal/app/app_test.go`
- 验证方式：
  - 新增测试，确认一个 shutdowner 失败时，后续 shutdowner 仍会被调用。

---

## 4. 风险代码片段 4：资源泄漏风险

### 原始 claim

- 文件：`internal/app/app.go`
- 片段：

```go
func (a *Application) Run(ctx context.Context) error {
    errCh := make(chan error, 1)
    go func() {
        if err := a.Server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
            errCh <- err
            return
        }
        errCh <- nil
    }()
    // 如果发生错误，listener 可能没有被关闭
}
```

### 原始风险描述

- 如果发生错误，listener 可能没有被关闭。

### 原始修复建议

```go
func (a *Application) Run(ctx context.Context) error {
    listener, err := net.Listen("tcp", a.Config.GRPC.ListenAddr)
    if err != nil {
        return err
    }
    defer func() {
        if listener != nil {
            listener.Close()
        }
    }()
    // ...
}
```

### 核查结论

- 不存在报告所描述的问题

### 是否需要修复

- 不需要

### 验证依据

- 使用本地文档核查：`go doc google.golang.org/grpc.Server.Serve`
- gRPC 官方实现说明明确写到：`lis will be closed when this method returns`
- 因此报告中“Serve 返回时 listener 可能泄漏”的判断不成立。
- 该点如果强行再加 `defer listener.Close()`，只能算额外保险，不是对当前缺陷的必要修复。

### 修复结果

- 未修改代码。
- 报告结论：该问题为误报。

---

## 5. 风险代码片段 5：SQL 注入风险

### 原始 claim

- 文件：`internal/adapters/outbound/vldb_sqlite/store.go`
- 片段：

```go
func sqlStringLiteral(s string) string {
    return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
```

### 原始风险描述

- 手动转义可能不够安全，应该使用参数化查询。

### 原始修复建议

- 为执行路径统一改成参数化查询，不再依赖手写字符串字面量拼接。

### 核查结论

- 未确认存在 SQL 注入漏洞

### 是否需要修复

- 当前不按该建议修复

### 验证依据

- 当前仓库并非完全依赖字符串拼接执行 SQL：
  - `exec(...)`
  - `execBatch(...)`
  - `queryRows(...)`
  已支持原生强类型参数。
- 报告引用的 `sqlStringLiteral(...)` 确实存在，也被部分脚本构造器使用，但它当前采用的是 SQLite 字符串字面量安全转义：把单引号转为 `''`。
- 结合当前 SQLite 方言，这不足以直接证明存在可利用的注入漏洞。
- 因此“已存在 SQL 注入风险”这个结论证据不足；更准确的说法应是：部分写路径仍采用原始脚本构造，长期来看可考虑继续向参数化迁移。

### 修复结果

- 未按报告建议做大规模参数化改造。
- 原因：这会是较大的存储层重构，不应在“漏洞修复”名义下对一个未证实漏洞直接动刀。
- 报告结论：原始 claim 夸大，当前记录为“可选工程优化”，不是已证实安全缺陷。

---

## 6. 风险代码片段 6：配置验证不足

### 原始 claim

- 文件：`internal/config/config.go`
- 片段：

```go
func (c Config) Validate() error {
    if strings.TrimSpace(c.LLM.Provider) == "" || strings.TrimSpace(c.Embedding.Provider) == "" || strings.TrimSpace(c.Vector.Provider) == "" || strings.TrimSpace(c.Relational.Provider) == "" {
        return errors.New("provider fields are required")
    }
    if !isOpenAIProvider(c.LLM.Provider) {
        return errors.New("llm.provider must use one openai-compatible provider")
    }
    if !isOpenAIProvider(c.Embedding.Provider) {
        return errors.New("embedding.provider must use one openai-compatible provider")
    }
    // 缺少对 API 密钥等敏感信息的验证
}
```

### 原始风险描述

- 缺少对敏感配置项的验证。

### 原始修复建议

```go
if strings.TrimSpace(c.LLM.APIKey) == "" {
    return errors.New("llm.api_key is required and cannot be empty")
}
if strings.TrimSpace(c.Embedding.APIKey) == "" {
    return errors.New("embedding.api_key is required and cannot be empty")
}
if !isValidAPIKeyFormat(c.LLM.APIKey) {
    return errors.New("llm.api_key has invalid format")
}
if !isValidAPIKeyFormat(c.Embedding.APIKey) {
    return errors.New("embedding.api_key has invalid format")
}
```

### 核查结论

- 不存在报告所描述的问题

### 是否需要修复

- 不需要

### 验证依据

- `Config.Validate()` 当前已经校验：
  - `llm.endpoint`
  - `llm.api_key`
  - `llm.model`
  - `embedding.endpoint`
  - `embedding.api_key`
  - `embedding.model`
  - `embedding.dimension`
  - `rerank` 启用时的 endpoint/model/api_key/top_n/timeout
- 因此“缺少对 API 密钥等敏感项验证”与当前代码不符。
- 报告建议中的 `sk-` 格式校验也不适合当前仓库，因为本项目显式支持 OpenAI-compatible provider，DashScope/Qwen 等密钥并不遵循 `sk-` 规则。

### 修复结果

- 未修改代码。
- 报告结论：该问题为误报，且建议修法会破坏现有多 provider 兼容性。

---

## 7. 风险代码片段 7：并发访问未加锁

### 原始 claim

- 文件：`internal/app/usecase/postaction.go`
- 片段：

```go
type PostActionUseCase struct {
    queueCh    chan uint64
    queueState map[uint64]*postActionQueueState
}
```

### 原始风险描述

- 多个 goroutine 可能同时访问 `queueState` map。

### 原始修复建议

```go
type PostActionUseCase struct {
    queueCh    chan uint64
    queueState map[uint64]*postActionQueueState
    queueMu    sync.RWMutex
}
```

### 核查结论

- 不存在报告所描述的问题

### 是否需要修复

- 不需要

### 验证依据

- `queueState` 的访问实际集中在 `internal/app/usecase/postaction_queue.go`。
- 核查结果显示：
  - 入队更新使用 `u.queueMu.Lock()`
  - worker 取状态使用 `u.queueMu.Lock()`
  - 回写/删除状态使用 `u.queueMu.Lock()`
- `queueState` 并不是“裸 map 并发访问”，报告只看到了结构体字段，没有继续看到真实访问点。

### 修复结果

- 未修改代码。
- 报告结论：该问题为误报。

---

## 8. 风险代码片段 8：内存泄漏风险

### 原始 claim

- 文件：`internal/logic/processor/noise_gate.go`
- 片段：

```go
func (g *NoiseGate) preloadSemanticPrototypes(ctx context.Context) error {
    for _, category := range g.categories {
        if len(category.Phrases) == 0 {
            continue
        }
        resp, err := g.embedding.Embed(ctx, appports.EmbeddingRequest{
            Model:     g.model,
            Texts:     category.Phrases,
            Dimension: g.dimension,
        })
        // ...
    }
}
```

### 原始风险描述

- 大量文本同时处理可能导致内存使用过高。

### 原始修复建议

- 按固定批次对 `category.Phrases` 做 embedding，避免一次请求携带过多文本。

### 核查结论

- “内存泄漏”结论不成立，当前更像潜在优化点

### 是否需要修复

- 当前不需要按漏洞修复处理

### 验证依据

- `preloadSemanticPrototypes(...)` 的确按 category 一次性发送 `category.Phrases` 做 embedding。
- 但这不构成“内存泄漏”：
  - 该流程是启动期预热，不是无限增长缓存。
  - 预热后结果会进入 cache，后续可复用。
  - 当前随仓库发布的 `noise_rules` phrase 数量很小，没有证据表明现有规则会造成异常内存峰值。
- 若未来允许非常大的自定义规则集，分批 embedding 可以作为鲁棒性优化，但不是当前已证实缺陷。

### 修复结果

- 未修改代码。
- 报告结论：当前记为“可选性能优化”，不是已确认的内存泄漏问题。
