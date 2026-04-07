## 任务目标

围绕当前未提交的 embedding 批处理参数改动，进一步把拆批职责统一收敛到向量模型控制器（`ai_key_failover` embedding 包装器），并补上“上游返回长度超限时按单条截断重试”的兜底能力，避免业务层继续重复处理批次，也避免单条异常长文本拖垮整批写入链路。

## 详细执行步骤

1. 复核当前 embedding 控制器、provider 适配器、业务层调用点和真实运行时测试基座，确认哪些位置仍在重复拆批或本地硬拒绝超长文本。
2. 调整 `ai_key_failover` embedding 控制器：
   - 保留统一输入标准化与拆批能力。
   - 去掉发送前基于本地估算器的单条硬拒绝。
   - 增加对 provider 长度超限错误的识别、批次递归缩小和单条文本截断重试。
3. 调整 provider 直连适配器：
   - 让其只承担单次请求映射与基础输入清洗，不再在适配器层重复做批宽/单条 token 上限策略。
4. 调整业务层调用点：
   - 删除 `postaction`、主动写记忆、`vector_rebuild` 中重复的 embedding 拆批逻辑。
   - 调用方直接把完整文本集合交给 embedding 控制器处理。
5. 调整真实模型测试基座与相关测试：
   - 让 `realruntime` 构造链与正式运行时保持一致。
   - 更新/补充控制器、适配器和业务层测试，覆盖统一拆批与超长文本截断重试场景。
6. 运行受影响模块测试并做必要的全量验证，确认行为闭环。

## 技术选型与处理原则

- `embedding.max_batch_size` 只表达“单次 embedding API 请求的最大文本数”，由 embedding 控制器统一执行。
- `embedding.max_input_tokens_per_text` 不再作为发送前的本地精确裁判，而是作为超长单条文本回退截断时的目标预算。
- token 真值以 provider 实际报错为准；本地估算器只用于保守截断，不作为最终拒绝依据。
- provider 适配器保持轻量，避免运行时策略散落到多个实现中。
- 真实运行时测试基座必须复用正式运行时同一条 embedding 客户端装配链，避免测试语义与线上分叉。

## 验收标准

1. 业务层不再维护重复的 embedding 拆批循环，完整文本集合可直接交由 embedding 控制器处理。
2. 当 provider 因单条文本过长返回长度超限错误时，系统会自动缩小定位并对该条执行截断重试，而不是整批失败。
3. provider 直连适配器不再重复实现与控制器冲突的批宽或单条 token 策略。
4. `realruntime` 测试基座与正式运行时使用同一条 embedding 控制器装配链。
5. 相关测试通过，且不会破坏现有 embedding / failover / 向量重建链路。

## 执行变更总结

### 1. 核心修复与调整概述

- 把 embedding 拆批、长度错误识别、超长单条文本截断重试统一收敛到 `ai_key_failover` embedding 控制器中处理。
- 去掉 OpenAI / Google provider 直连适配器里的本地批宽与单条 token 硬拒绝逻辑，让 provider 适配器只负责单次请求映射与基础输入清洗。
- 删除 `postaction`、主动写记忆和向量重建中重复的 embedding 拆批逻辑，改为一次性把完整文本集合提交给 embedding 控制器。
- 调整 `realruntime` 测试基座，让真实模型测试也复用运行时同一条 embedding 控制器装配链，避免测试语义与正式运行时分叉。
- 同步更新文档说明，把 `max_input_tokens_per_text` 的语义改为“provider 长度错误后的截断重试目标预算”，而不是“发送前本地硬拒绝阈值”。

### 2. 📂 文件变更清单

- 修改：
  - `internal/adapters/outbound/ai_key_failover/embedding.go`
  - `internal/adapters/outbound/ai_key_failover/classifier.go`
  - `internal/adapters/outbound/ai_key_failover/provider_factory.go`
  - `internal/adapters/outbound/ai_key_failover/key_failover_test.go`
  - `internal/adapters/outbound/openai_native/embedding.go`
  - `internal/adapters/outbound/openai_native/embedding_test.go`
  - `internal/adapters/outbound/google_ai_studio/embedding.go`
  - `internal/app/app.go`
  - `internal/app/usecase/postaction.go`
  - `internal/app/vector_rebuild.go`
  - `internal/testutil/realruntime.go`
  - `configs/base.yaml`
  - `docs/ai-model-failover-design_CN.md`
- 新增：
  - `docs/plan/20260407-13-EMBEDDING_CONTROLLER_BATCHING_AND_TRUNCATION.md`（完成后归档）
- 删除：
  - 无

### 3. 💻 关键代码调整详情

- 在 `ai_key_failover.EmbeddingClient` 中新增：
  - 上游长度错误识别
  - 逻辑批次递归缩小
  - 单条文本渐进截断重试
  - 统一结果计数校验
- `max_batch_size` 继续仅用于 embedding API 请求批宽控制，由控制器统一执行；业务层不再重复拆批。
- `max_input_tokens_per_text` 改为截断回退时的目标估算预算；当配置为 `0` 时，控制器会在 provider 已确认“文本过长”之后基于原文估算推导回退预算。
- `vector_rebuild` 的 embedding 生成阶段改为整批提交给控制器，而 split 模式 durable/vector 回填写批次改回独立常量，不再绑定 embedding API 批宽配置。
- `realruntime` 现在通过 `ai_key_failover.NewProviderEmbeddingClient` 构造 embedding 客户端，从而与正式运行时共享统一拆批与回退逻辑。
- 补充了控制器测试，覆盖“整批先失败 -> 拆小定位 -> 单条截断重试成功”的关键场景。

### 4. ⚠️ 遗留问题与注意事项

- 目前“输入过长”识别仍基于 OpenAI / Gemini 常见错误文案与状态码模式匹配；如果后续接入新 provider 或上游调整报错文案，需要同步扩展识别标记。
- `max_input_tokens_per_text` 仍然依赖本地保守估算器参与截断预算推导，它用于回退收缩而不是精确 tokenizer 判定，因此预算效果是“保守可用”而非 provider 官方逐 token 精算。
- 本轮已执行 `go test ./...`，当前全量通过。
