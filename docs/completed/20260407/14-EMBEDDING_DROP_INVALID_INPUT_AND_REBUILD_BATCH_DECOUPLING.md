# 任务计划：embedding 单条失败丢弃与重建批次解耦

## 一、任务目标

本次任务的目标是调整当前 embedding 调用链与向量重建流程，解决两类工程问题：

1. 向量重建流程不应一次性持有全量 embedding 中间结果，避免在大规模重建时抬高峰值内存。
2. embedding 控制器不再维护本地 `max_input_tokens_per_text` 预算、截断与重试逻辑，而是改为依赖上游向量模型 API 的权威判断；当递归拆分后定位到单条输入仍然触发确定性非法输入错误时，直接将该条标记为失败并交由上层决定是否丢弃。

本次改造的最终方向为：

- `vector rebuild` 保持自己的外层处理批次，用于控制内存与维护任务规模。
- embedding 控制器保持自己的 provider 批次控制，用于适配上游 API 的单次安全请求上限。
- 两层批次职责解耦，不再共用同一个“批大小”含义。
- post-action / 新增记忆入库链路支持“单条确定性非法输入失败后丢弃该条并跳过入库”。
- `vector rebuild` 先保持严格模式，不对失败条目做静默跳过。
- 所有实际调用 embedding 的链路都要重新核对调用语义，不能只修改单一路径。

## 二、技术决策

### 2.1 重建批次与 embedding 批次解耦

采用“双层批次”设计：

- 外层批次：由 `vector rebuild` 自己控制，仅用于限制单次 materialize 的记录数与内存占用。
- 内层批次：由 embedding 控制器根据 `embedding.max_batch_size` 控制，仅用于 provider 兼容性与上游安全请求大小。

预期行为：

- 若重建批次 `20`，embedding 批次 `32`，则单个重建批次直接一次完成。
- 若重建批次 `40`，embedding 批次 `32`，则 embedding 控制器内部拆为 `32 + 8` 处理，并按原顺序拼回。

### 2.2 删除本地单条 token 预算控制

不再保留 `embedding.max_input_tokens_per_text` 配置、环境变量、校验、文档与本地 token 截断逻辑。

原因：

- 上游向量模型 API 已经是单条输入是否合法、是否超长的权威判断来源。
- 本地估算器与 provider tokenizer 可能不一致，会增加排查复杂度。
- 当前业务前提下，精炼后的摘要理论上不应超过上游支持上限；若仍触发确定性非法输入，应视为异常条目，而不是继续本地截断抢救。

### 2.3 单条失败处理策略

embedding 控制器保留“递归拆分定位坏条”的能力，但在定位到单条后：

- 若错误属于“单条确定性非法输入/超长输入”，则将该条标记为失败结果。
- 若错误属于网络错误、超时、429、5xx、鉴权、配额等暂时性或环境性错误，则继续按失败处理，不允许静默丢弃。

### 2.4 上层消费策略

- post-action / 新增记忆入库路径：
  - 对单条确定性失败结果执行丢弃。
  - 不为该条写入关系库，不为该条写入向量库。
  - 需要保留日志与索引映射，确保成功条目与原始条目一一对应。

- vector rebuild 路径：
  - 保持严格失败。
  - 一旦某条在递归拆分后仍属于确定性失败，当前重建任务直接报错退出，不做 silent skip。
  - 后续若要支持 rebuild skip，必须单独设计 skipped 报告、旧向量处理与一致性策略，本次不纳入。

### 2.5 全部 embedding 调用点的处理矩阵

本次实现必须覆盖仓库内所有真实 embedding 调用点，并为不同调用场景明确失败语义：

1. `internal/app/usecase/postaction.go`
   - 场景：新提炼出的 memory node 写入前生成向量。
   - 语义：允许 best-effort。
   - 处理：单条确定性非法输入可丢弃；其余成功条目继续入库。

2. `internal/app/vector_rebuild.go`
   - 场景：维护命令对已有 durable memory 重新生成向量。
   - 语义：严格失败。
   - 处理：任何无法恢复的单条失败都会使当前重建任务失败，不允许 silent skip。

3. `internal/app/usecase/memory_query.go`
   - 场景：查询语句生成检索向量。
   - 语义：严格失败。
   - 原因：查询向量缺失会直接破坏检索结果，不能返回部分 query embedding 并继续执行。

4. `internal/logic/processor/noise_gate.go` 启动预热阶段
   - 场景：为 noise category phrases 生成语义原型向量。
   - 语义：保持现有“语义能力降级为 regex-only”的大方向。
   - 处理：若 embedding 调用无法完整获得当前类别需要的向量，则视为该次 semantic preload 失败，继续沿用现有降级机制，而不是构造半套 prototype。

5. `internal/logic/processor/noise_gate.go` 运行时阶段
   - 场景：对 user / assistant 文本实时生成向量，判断是否命中语义噪声。
   - 语义：保持保守降级。
   - 处理：若运行时 embedding 无法完整获得当前轮次所需向量，则按现有 `SEMANTIC_UNAVAILABLE` 语义降级，不能静默用不完整结果继续判定。

6. `internal/testutil/realruntime.go`
   - 场景：集成测试前对 live embedding endpoint 做可用性探针。
   - 语义：严格失败 / skip。
   - 处理：探针是单条请求，不涉及部分成功；只需跟随新接口与调用方式同步调整。

7. 适配器层与 failover 包装层测试
   - 场景：`ai_key_failover`、`openai_native`、`google_ai_studio` 相关单元测试。
   - 语义：必须同步到新的调用契约，覆盖“部分成功 / 严格失败 / provider 单条非法输入隔离”三类路径。

## 三、详细执行步骤

### 步骤 0：梳理全部 embedding 实际调用点

在开始改接口前，先逐一核对所有真实调用位置，并为每个位置确定它属于以下哪一类：

- 允许部分成功并丢弃失败条目；
- 必须完整成功，否则整次调用失败；
- 允许整体降级，但不能在部分成功结果上继续半执行。

产出要求：

- 明确每个调用点采用的失败语义。
- 避免接口改动后只修复 `postaction` 与 `vector_rebuild`，却遗漏 `memory_query`、`noise_gate` 等路径。

### 步骤 1：梳理并裁剪 embedding 控制器职责

检查并改造以下内容：

- `internal/adapters/outbound/ai_key_failover/embedding.go`
- `internal/adapters/outbound/ai_key_failover/classifier.go`
- 相关测试文件

目标：

- 保留 provider 批次拆分能力。
- 删除单条 token 截断重试相关常量、估算器依赖、marker 与配置接入。
- 将“单条确定性非法输入”从“重试截断”改为“失败结果返回”。

### 步骤 2：设计带索引的 embedding 结果表达

为避免“丢弃单条”后上层顺序错位，需要新增或调整内部结果表达方式，使上层能够知道：

- 原始第几条文本成功生成了向量；
- 原始第几条文本被判定为确定性失败；
- 失败原因是什么。

注意：

- 不能继续仅依赖裸 `[][]float32` 作为唯一结果载体。
- 需要确保上层在过滤失败项后，剩余条目与原始 memory node / 文本条目索引仍然可准确回填。
- 需要兼容“严格失败路径”与“允许部分成功路径”两类调用方，不得把所有调用都强制改成 best-effort。

### 步骤 3：改造 post-action 入库链路

检查并改造以下内容：

- `internal/app/usecase/postaction.go`
- `internal/app/usecase/postaction_test.go`

目标：

- 让 post-action 在 embedding 返回“部分成功、部分失败”时，只为成功条目继续写向量与入库。
- 对确定性失败条目直接取消入库。
- 保持其余成功条目的顺序稳定与回填正确。
- 补充日志，使后续审计可知道被丢弃条目的原因与数量。

### 步骤 4：同步调整其余 embedding 调用链的消费语义

检查并改造以下内容：

- `internal/app/usecase/memory_query.go`
- `internal/logic/processor/noise_gate.go`
- `internal/testutil/realruntime.go`
- 相关测试文件

目标：

- `memory_query` 保持 strict 语义，确保 query embedding 缺失时直接失败。
- `noise_gate` 的 preload 与 runtime 路径保持现有降级语义，不使用部分成功结果继续半执行。
- `realruntime` 探针与测试桩适配新的 embedding 调用约定。

### 步骤 5：恢复向量重建的外层 materialize 批次

检查并改造以下内容：

- `internal/app/vector_rebuild.go`
- `internal/app/vector_rebuild_test.go`

目标：

- 恢复按“重建流程自己的批次”做 materialize。
- embedding 控制器内部仍可继续按 provider 批次二次拆分。
- 避免一次性保留全量 vectors 中间结果。
- `vector rebuild` 保持严格失败，不静默跳过失败条目。

### 步骤 6：删除冗余配置项与文档

检查并改造以下内容：

- `internal/config/config.go`
- `internal/config/config_test.go`
- `configs/base.yaml`
- `configs/config.yaml`
- `configs/openai.config.example.yaml`
- `configs/google_ai_studio.config.example.yaml`
- `docs/ai-model-failover-design_CN.md`

目标：

- 删除 `max_input_tokens_per_text` 配置项及其环境变量覆盖。
- 删除相关校验、默认值、示例配置与文档说明。
- 保留 `max_batch_size`，并明确其仅表示 embedding provider 批次上限。

### 步骤 7：补齐测试并完成回归验证

至少补充或更新以下测试场景：

1. embedding 控制器对大批次执行内部拆分并保持顺序返回。
2. embedding 控制器递归定位到单条确定性非法输入后，能够返回失败索引而不是截断重试。
3. post-action 在部分条目生成向量失败时，只保留成功条目入库。
4. memory_query 在 embedding 无法完整生成查询向量时继续保持 strict 失败。
5. noise_gate preload / runtime 在 embedding 不完整时维持既有降级语义，不误用部分结果。
6. vector rebuild 使用独立外层批次，不再一次性 materialize 全量向量。
7. 配置层移除 `max_input_tokens_per_text` 后，相关默认值、环境变量与校验测试同步更新。

回归命令至少包括：

- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
- `go test ./...`

## 四、验收标准

满足以下条件方可视为完成：

1. `vector rebuild` 不再一次性保留全量 embedding 中间向量结果，外层 materialize 恢复为有界批次。
2. embedding 控制器不再依赖本地 `max_input_tokens_per_text` 做截断与重试。
3. 单条确定性非法输入可被递归定位并以失败项形式返回。
4. post-action 链路能够正确丢弃失败条目，且成功条目入库、向量写入与索引映射保持正确。
5. `memory_query`、`noise_gate`、`realruntime probe` 等其余 embedding 实际调用点均已按各自语义完成调整，没有遗漏。
6. `vector rebuild` 仍保持严格失败语义，不因单条异常而 silent skip。
7. 配置、示例文件、环境变量与设计文档已同步移除无效 token 限制项。
8. 相关测试补齐并通过，`go test ./...` 全绿。

## 五、风险与注意事项

1. “允许丢弃单条”只适用于确定性输入错误，不得误覆盖网络、限流、5xx、配额、鉴权等暂时性故障。
2. 若 embedding 返回部分成功结果，必须显式保留原始索引映射，避免上层错位写库。
3. `memory_query`、`noise_gate`、`vector rebuild` 与 `post-action` 的失败语义不同，不能混用。
4. `noise_gate` 属于“可整体降级但不可半执行”的链路，不能只拿到部分 prototype / 部分查询向量就继续语义判定。
5. 若在实现过程中发现现有 `EmbeddingClient` 接口不足以表达“部分成功 + 部分失败”，需要先做内部契约调整，再推进上层接入。

## 六、执行变更总结

### 1. 核心修复与调整概述

本次任务已经完成以下核心调整：

- embedding 控制器移除了本地 `max_input_tokens_per_text` 截断预算与单条截断重试逻辑，改为完全依赖 provider 对“输入过长”的权威判断。
- embedding 响应契约新增“成功向量索引映射 + 丢弃条目”能力，支持上层按调用语义区分 strict 失败与 best-effort 丢弃。
- `post-action` 改为允许丢弃 provider 明确拒绝的单条记忆节点输入，只保留健康条目继续写入向量库与关系库。
- `memory_query`、`direct write`、`noise_gate`、`vector rebuild`、`realruntime probe` 等其余调用点已分别收口到各自应有的 strict / 降级语义。
- `vector rebuild` 恢复独立的外层 materialize 批次，通过 `maintenance_tool.vector_rebuild_batch_size` 控制内存峰值，不再一次性保留全量中间向量结果。
- 配置、示例文件、环境变量与设计文档已经同步清理无效的 token 限制项，并补充新的维护批次配置说明。
- 已完成定向测试与 `go test ./...` 全量回归验证，结果全部通过。

### 2. 📂文件变更清单

新增：

- 无

修改：

- `configs/base.yaml`
- `configs/config.yaml`
- `configs/google_ai_studio.config.example.yaml`
- `configs/openai.config.example.yaml`
- `docs/ai-model-failover-design_CN.md`
- `internal/adapters/outbound/ai_key_failover/classifier.go`
- `internal/adapters/outbound/ai_key_failover/embedding.go`
- `internal/adapters/outbound/ai_key_failover/key_failover_test.go`
- `internal/app/app.go`
- `internal/app/ports/interfaces.go`
- `internal/app/usecase/memory_query.go`
- `internal/app/usecase/postaction.go`
- `internal/app/usecase/postaction_test.go`
- `internal/app/vector_rebuild.go`
- `internal/app/vector_rebuild_test.go`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/logic/ports/embedding.go`
- `internal/logic/processor/noise_gate.go`
- `internal/testutil/realruntime.go`

删除：

- 无

### 3. 💻关键代码调整详情

- `internal/logic/ports/embedding.go`
  - 扩展 `EmbeddingRequest`，支持 `AllowPartialInvalidTexts`。
  - 扩展 `EmbeddingResponse`，增加 `ResultIndices`、`Dropped`、`IndexedVectors`、`ValidateStrict`。
  - 收紧历史兼容契约：当响应未显式提供索引映射时，必须满足完整一一对应关系，避免 best-effort 调用点误把缺失向量当成合法部分结果。
- `internal/adapters/outbound/ai_key_failover/embedding.go`
  - 移除本地 token 截断预算、缩放与单条截断重试逻辑。
  - 保留 provider 批次拆分，并在识别到“当前批次过大”时继续递归二分。
  - 当递归定位到单条输入且 provider 仍明确判定其过大时：
    - strict 调用方直接失败；
    - best-effort 调用方仅丢弃该条并返回索引映射。
- `internal/app/usecase/postaction.go`
  - `post-action` 通过 `AllowPartialInvalidTexts=true` 获取部分成功结果。
  - 仅把成功向量对应的记忆节点写入向量库与关系库，并把丢弃日志记录到运行时日志中。
- `internal/app/usecase/memory_query.go`
  - 查询 embedding 与 direct write 仍保持 strict 语义。
  - `direct write` 不再复用 post-action 的 best-effort helper，避免显式写入请求被静默丢弃。
- `internal/logic/processor/noise_gate.go`
  - preload 与 runtime 路径统一使用 `ValidateStrict`。
  - preload 保持“失败则整体降级为 live/regex-only”的原语义；runtime 保持 `SEMANTIC_UNAVAILABLE` 降级，不误用部分结果。
- `internal/app/vector_rebuild.go`
  - 使用 `maintenance_tool.vector_rebuild_batch_size` 控制外层 materialize 批次。
  - embedding 控制器内部仍保留 provider 批次二次拆分能力，两个层次的批次语义已经解耦。
  - `vector rebuild` 继续保持 strict 失败，不对单条异常做 silent skip。
- 测试补充
  - 新增 post-action 单条丢弃回归测试，验证只保留健康记忆节点。
  - 新增 vector rebuild 外层批次测试，验证确实按维护批次切分。
  - 更新 key-failover 测试，覆盖 strict 拒绝与 best-effort 丢弃两种单条超长输入路径。
- 验证命令
  - `go test ./internal/app/usecase ./internal/app ./internal/adapters/outbound/ai_key_failover ./internal/logic/processor ./internal/config`
  - `go test ./...`

### 4. ⚠️遗留问题与注意事项

- 本次仅在 `post-action` 路径开放了“单条确定性非法输入可丢弃”的 best-effort 语义；`memory_query`、`direct write`、`noise_gate`、`vector rebuild` 继续保持 strict 或整体降级，不会静默跳过。
- “可丢弃”仅适用于 provider 已确认的单条输入确定性错误；网络故障、429、5xx、鉴权失败、额度耗尽等暂时性问题仍然按整体失败处理。
- 历史 `docs/completed/20260407/12-*` 与 `13-*` 文档保留了前一轮方案记录，其中仍会出现 `max_input_tokens_per_text` 描述；这些是历史归档，不代表当前实现。
