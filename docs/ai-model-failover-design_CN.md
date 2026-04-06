# AI Key 级容灾设计（固定模型 / 固定 Provider / 固定 Endpoint）

## 1. 设计结论

基于当前仓库的检索链路特性，本项目的 AI 容灾方案应当**收敛为单服务配置下的固定模型节点轮询 + 节点内多 API Key 容灾**，而不是多 provider / 多 model 容灾。

明确结论如下：

1. `llm`、`embedding`、`rerank` 三类能力都保持**单一 provider、单一 endpoint、单一 model**。
2. 容灾只发生在 **固定模型节点** 和 **节点内 API Key** 层面。
3. `embedding` 链路严禁为容灾目的切换到另一套 embedding 模型。
4. `向量维度相同` 只是必要条件，不是充分条件；即使维度一致，不同 embedding 模型仍可能处于完全不同的语义空间。
5. 混用不同 embedding 模型生成的向量，会直接破坏：
   - 记忆检索相似度
   - 向量召回稳定性
   - noise gate 语义判断
   - post-action 写入与后续查询的一致性

因此，本设计不再推荐：

- 多 provider failover
- 多 model failover
- 多 route 轮询
- 混合 embedding 模型池

本设计只保留：

- 同一模型配置下的节点轮询
- 同一节点内的多 key 轮换
- 同一节点内的 key 暂停 / 恢复
- 节点下每个 key 自己的 RPM / TPM / RPD 预判断
- 重启后清空运行态

## 2. 为什么必须收窄到 Key 级容灾

## 2.1 当前系统不是纯文本生成系统

本仓库不只是调用 LLM 生成文本，还把外部 AI 能力深度嵌入以下链路：

- `embedding`
  - 记忆查询向量化
  - post-action 记忆写入向量化
  - noise gate 语义匹配
- `rerank`
  - 检索候选二阶段排序
- `llm`
  - 结构化 JSON 输出
  - pre-check / post-action / profile review 等流程

其中最敏感的是 `embedding`。

## 2.2 向量维度一致，不代表向量语义一致

例如两个 embedding 模型即使都输出 `1024` 维向量，也不代表：

- 同一条文本会落在相同语义区域
- 余弦距离具有可比性
- 历史写入向量与新查询向量仍然可匹配

也就是说：

- `dimension 相同` 只能说明“数据形状没炸”
- 不能说明“语义检索仍然正确”

如果运行时在容灾时把查询向量从模型 A 切到模型 B：

1. 历史 memory row 的向量是模型 A 写入的
2. 当前 query 的向量却由模型 B 生成
3. 相似度比较会失去语义可解释性
4. 检索结果可能出现完全错误但表面“格式合法”的命中

这种错误最危险，因为：

- 不一定报错
- 不一定超时
- 但结果不可信

## 2.3 混合向量模型会直接污染检索链

当前仓库里，向量不仅用于 ANN 检索，还参与：

- 召回
- 融合
- rerank 前候选生成
- noise gate 语义判断
- 去重与替换相关逻辑

因此 embedding 模型一旦混用，风险不是“某一次召回略微变差”，而是：

- 检索命中面系统性偏移
- 旧数据与新数据不可比
- 同一批请求不同时间表现失真
- 调试时很难从日志直接看出根因

所以本设计必须明确：

> Embedding 模型固定，key 可切；模型不可切。

## 2.4 LLM 虽然不是向量，但也不建议切模型

从纯文本生成角度看，LLM 切模型的破坏性小于 embedding。
但对本仓库而言，LLM 仍承担：

- 结构化 JSON 输出
- 严格字段约束
- 固定 prompt 契约
- 审核 / 评审 / 提炼结果稳定性

因此如果 LLM 也做多模型容灾，会引入：

- JSON 输出风格漂移
- 枚举遵循度漂移
- 审核尺度漂移
- 同一场景不同行为基线

为了让整个系统保持一致，本设计统一采用：

> `llm / embedding / rerank` 都固定 provider + endpoint + model，只切 key。

## 3. 当前代码现状

当前仓库的外部 AI 能力接入点如下：

- 配置定义：`internal/config/config.go`
- 运行时装配：`internal/app/app.go`
- LLM 适配器：`internal/adapters/outbound/openai_native/llm.go`
- Embedding 适配器：`internal/adapters/outbound/openai_native/embedding.go`
- Rerank 适配器：`internal/adapters/outbound/dashscope_rerank/client.go`

当前每类能力都是：

- 单 `provider`
- 单 `endpoint`
- 单 `model`
- 单 `api_key`

当前失败语义：

- `rerank`
  - 失败后在 `internal/app/usecase/memory_query.go` 中降级回首轮排序
- `noise gate`
  - embedding 失败时在 `internal/logic/processor/noise_gate.go` 中退化为 regex-only
- `llm`
  - 失败直接返回业务错误
- `embedding`
  - 失败通常直接返回错误

这说明现在最适合补的不是“多 route 调度器”，而是：

> 在现有单模型适配器外面包一层 key 级 failover wrapper。

## 4. 设计目标

## 4.1 目标

1. 支持 `llm / embedding / rerank` 各自配置多个 API Key。
2. 保持每类服务的 `provider / endpoint / model` 固定不变。
3. 对 key 限流、配额、鉴权失败做自动切 key。
4. 对被判定为暂时不可用的 key 做内存态暂停。
5. 支持至少两种 key 选择模式：
   - `ordered_failover`
   - `round_robin`
6. 运行态状态不持久化，重启后全部清空。
7. 不改变上层业务接口：
   - `LLMClient`
   - `EmbeddingClient`
   - `RerankerClient`

## 4.2 非目标

1. 不做多 provider 容灾。
2. 不做多 model 容灾。
3. 不做 embedding 模型池。
4. 不做跨重启状态保留。
5. 不做数据库化 key 配额状态。

## 5. 核心边界约束

## 5.1 强约束

对于任意一种服务配置，以下字段必须在 key 池内保持完全一致：

- `provider`
- `endpoint`
- `model`

对于 `embedding`，还必须保持：

- `dimension`

也就是说，一个 key 池的本质是：

> 同一服务配置 + 多个凭据

而不是：

> 多个服务配置 + 多个凭据

## 5.2 Embedding 的安全规则

对于 `embedding`，建议把以下规则写入配置校验和设计文档：

1. 不允许声明多个 embedding model。
2. 不允许声明多个 embedding endpoint。
3. 不允许声明多个 embedding provider。
4. 不允许因 failover 切换 embedding model。
5. 不允许因 failover 切换到另一套“维度相同”的 embedding 服务。

因为：

- 维度一致不等于语义空间一致
- 语义空间不一致时，检索结果将不可解释

## 5.3 Rerank 的安全规则

`rerank` 的风险小于 `embedding`，但为了系统一致性，仍建议固定：

- `provider`
- `endpoint`
- `model`

原因：

- 不同 rerank 模型的打分分布会显著不同
- 对 recall 结果排序的偏好会不同
- 同一 query 在不同 key 下应尽量保持一致，不应再叠加模型漂移

## 5.4 LLM 的安全规则

`llm` 同样固定：

- `provider`
- `endpoint`
- `model`

原因：

- 当前系统依赖结构化 JSON 输出
- 多模型切换会造成行为基线漂移
- 调试和验收会变得不稳定

## 6. 配置设计

## 6.1 设计原则

保留现有单服务配置结构，但引入**固定模型节点**概念；节点不是跨 provider / 跨 model route，而是同一模型下的配额组。

推荐思路：

- 原有 `api_key` 保留兼容
- 原有 `api_keys` 保留兼容
- 新增 `rpm / tpm / rpd`
- 新增 `nodes`
- 保留 `key_failover`

## 6.2 推荐结构

以 `llm` 为例：

```json
{
  "llm": {
    "provider": "openai",
    "endpoint": "${OPENAI_BASE_URL}",
    "model": "${OPENAI_MODEL}",
    "nodes": [
      {
        "name": "free-tier-a",
        "api_keys": [
          "${OPENAI_KEY_1}",
          "${OPENAI_KEY_2}"
        ],
        "rpm": 3,
        "tpm": 40000,
        "rpd": 200
      },
      {
        "name": "free-tier-b",
        "api_key": "${OPENAI_KEY_3}",
        "rpm": 2,
        "tpm": 20000,
        "rpd": 100
      }
    ],
    "organization": "${OPENAI_ORGANIZATION}",
    "project": "${OPENAI_PROJECT}",
    "params": {},
    "model_params": {},
    "key_failover": {
      "enabled": true,
      "policy": "ordered_failover",
      "respect_retry_after": true,
      "rate_limit_cooldown": "5m",
      "quota_cooldown": "10m",
      "auth_cooldown": "12h",
      "probe_after_cooldown": true
    }
  }
}
```

`embedding` 与 `rerank` 结构同理；节点都必须保持同一组固定的 `provider + endpoint + model`，其中 `embedding` 还必须保持同一 `dimension`。节点上声明的 `rpm / tpm / rpd` 表示该节点内每个 key 各自独享的额度；如果不同 key 档位不同，应拆成多个节点。

## 6.3 `api_key` / `api_keys` / `nodes` 兼容规则

建议归一化规则如下：

1. 如果显式声明 `nodes`，优先使用 `nodes`
2. 否则读取顶层 `api_keys`
3. 再否则读取顶层 `api_key`
4. `api_key` 允许是：
   - 单 key
   - 逗号分隔
   - 分号分隔
   - 换行分隔
5. 归一化后：
   - 去空白
   - 去空串
   - 去重
6. 当未声明 `nodes` 时，顶层 `api_key / api_keys + rpm / tpm / rpd` 会被折叠为一个默认节点

推荐约定：

- 文件配置优先写 `nodes`
- 环境变量可以写成分隔字符串
- 同平台但额度不同的免费 key，建议拆成多个节点，而不是塞进同一个节点

## 6.4 `key_failover` 建议字段

建议统一定义：

- `enabled`
  - 是否启用 key 级容灾
- `policy`
  - `ordered_failover`
  - `round_robin`
- `respect_retry_after`
  - 是否尊重 provider 返回的 `Retry-After`
- `rate_limit_cooldown`
  - RPM / TPM / rate limit 冷却时长
- `quota_cooldown`
  - quota / insufficient balance 冷却时长
- `auth_cooldown`
  - 401 / 403 / invalid key 冷却时长
- `probe_after_cooldown`
  - 冷却到期后是否允许重新尝试

## 6.5 配置校验建议

### `llm`

- `provider` 必填
- `endpoint` 必填
- `model` 必填
- `nodes` 归一化后至少 1 个
- 每个节点都必须至少包含 1 个 key
- 每个节点的 `rpm / tpm / rpd` 必须 `>= 0`

### `embedding`

- `provider` 必填
- `endpoint` 必填
- `model` 必填
- `dimension` 必填
- `nodes` 归一化后至少 1 个
- 每个节点都必须至少包含 1 个 key
- 每个节点的 `rpm / tpm / rpd` 必须 `>= 0`
- 不允许出现任何多模型切换配置

### `rerank`

- `enabled=true` 时：
  - `provider`
  - `endpoint`
  - `model`
  - `nodes`
  均必须有效
- 每个节点都必须至少包含 1 个 key
- 每个节点的 `rpm / tpm / rpd` 必须 `>= 0`

## 7. 运行时结构设计

## 7.1 整体思路

运行时仍然只装配一套服务配置，但该配置下面挂多个固定模型节点；每个节点内部再挂多个 key。

建议新增 key 级包装器：

- `KeyFailoverLLMClient`
- `KeyFailoverEmbeddingClient`
- `KeyFailoverRerankerClient`

它们对上继续实现原接口：

- `LLMClient`
- `EmbeddingClient`
- `RerankerClient`

对下则维护：

- 当前可用节点列表
- 每个节点内各 key 自己的 RPM / TPM / RPD 运行态计数
- 每个节点内部各 key 的运行时状态
- key 对应的底层客户端缓存

## 7.2 运行时状态

建议维护两层状态：

- `keyBudgetState`
  - `minuteBucket`
  - `minuteRequests`
  - `minuteTokens`
  - `dayBucket`
  - `dayRequests`
- `keyState`
  - `disabledUntil`
  - `lastErrorClass`
  - `consecutiveFailures`
  - `lastUsedAt`

不再维护：

- `providerState`
- `modelState`

因为本设计不允许 provider / model 切换。

## 7.3 状态生命周期

### `active`

- 可参与选择

### `cooling_down`

- 因限流、配额、鉴权等被暂时停用
- 到 `disabledUntil` 前不再参与选择

### `eligible_again`

- 冷却结束
- 可重新参与选择

### 进程重启

- 全部状态清空
- 不做持久化恢复

## 7.4 客户端缓存

建议按 `key` 维度缓存底层客户端实例：

- LLM：`openai_native.NewLLMClient(...)`
- Embedding：`openai_native.NewEmbeddingClient(...)`
- Rerank：`dashscope_rerank.NewClient(...)`

但要注意：

- 除 `api_key` 外，其余参数必须完全相同

## 8. Key 选择策略

## 8.1 `ordered_failover`

适合“主 key 优先，失败再切下一个”。

规则：

1. 总是优先选第一个健康 key
2. 当前 key 被暂停后，选下一个健康 key
3. 前面的 key 冷却结束后，可重新回到优先位

优点：

- 行为稳定
- 排查简单

## 8.2 `round_robin`

适合“多个 key 均衡消耗额度”。

规则：

1. 维护 key 游标
2. 每次请求从下一个健康 key 开始
3. 被暂停的 key 在冷却结束前跳过
4. 冷却结束后重新回到轮询集合

优点：

- 更均衡地消耗免费额度

## 8.3 推荐默认值

建议默认：

- `policy = ordered_failover`

因为它更符合：

- 可观测性
- 预期稳定性
- 风险最小化

如果用户明确追求均摊免费额度，再切到：

- `policy = round_robin`

## 9. 错误分类与动作设计

本设计的核心不是“任何错误都换 key”，而是：

> 只有当错误大概率是 key 级问题时，才切换 key。

## 9.1 适合切 key 的错误

### A. 限流类

典型表现：

- 429
- RPM limit
- TPM limit
- rate limit exceeded

动作：

- 暂停当前 key
- 冷却一段时间
- 切到下一个 key

### B. 配额类

典型表现：

- quota exceeded
- insufficient quota
- insufficient balance

动作：

- 暂停当前 key
- 冷却时间通常长于限流
- 切到下一个 key

### C. 鉴权类

典型表现：

- 401
- 403
- invalid api key
- key disabled

动作：

- 长时间暂停当前 key
- 切到下一个 key

## 9.2 不适合切 key 的错误

### A. 请求无效

典型表现：

- 400 bad request
- prompt 太长
- 参数结构不合法

动作：

- 不切 key
- 直接返回错误

原因：

- 这类错误换 key 没意义
- 继续轮询只会把坏请求广播到全部 key

### B. 服务端公共故障

典型表现：

- 5xx
- 网络超时
- dial error
- TLS 握手失败

默认动作建议：

- 不暂停当前 key
- 不轮询全部 key
- 直接返回错误

原因：

- 当前设计下所有 key 指向的是同一个 provider / endpoint / model
- 这类错误大概率不是 key 自身问题
- 盲目切 key 只会放大无效请求量

如果后续要做增强，也建议仅作为可选开关，而不是默认行为。

## 9.3 建议错误动作矩阵

| 错误类别 | 示例 | 默认动作 |
| --- | --- | --- |
| 限流 | 429 / RPM / TPM | 暂停当前 key，切下一个 key |
| 配额不足 | quota / balance / insufficient_quota | 暂停当前 key，切下一个 key |
| 鉴权失败 | 401 / 403 / invalid key | 长暂停当前 key，切下一个 key |
| 请求无效 | 400 / 参数错误 | 不切 key，直接返回 |
| 公共故障 | 5xx / timeout / network | 不切 key，直接返回 |

## 9.4 冷却时长建议

建议默认值：

- `rate_limit_cooldown = 5m`
- `quota_cooldown = 10m`
- `auth_cooldown = 12h`

冷却时长优先级：

1. provider 返回的 `Retry-After`
2. 本地配置默认值

## 10. 对三条业务链路的影响

## 10.1 LLM

涉及位置：

- `internal/logic/processor/turn_analyzer.go`
- `internal/logic/processor/intent_extractor.go`
- `internal/logic/processor/precheck_memory_reviewer.go`
- `internal/logic/processor/postaction_candidate_reviewer.go`
- `internal/logic/processor/profile_merger.go`
- `internal/logic/processor/manual_profile_reviewer.go`
- `internal/logic/processor/entry_summarizer.go`

预期行为：

- 同一 `provider + endpoint + model` 下多个 key 轮换
- 不切模型
- 全 key 失败后，沿用当前业务错误路径

## 10.2 Embedding

涉及位置：

- `internal/app/usecase/memory_query.go`
- `internal/app/usecase/postaction.go`
- `internal/logic/processor/noise_gate.go`

预期行为：

- 同一 embedding 模型下多个 key 轮换
- 不切 embedding 模型
- 不切 endpoint
- 不切 provider
- 所有 key 都失败后：
  - 记忆检索 / post-action 仍按当前逻辑报错
  - noise gate 仍按当前逻辑退化为 regex-only

## 10.3 Rerank

涉及位置：

- `internal/app/usecase/memory_query.go`

预期行为：

- 同一 rerank 模型下多个 key 轮换
- 不切 rerank 模型
- 不复用 `llm` key 池
- 所有 key 都失败后，继续降级回首轮排序
- 降级时记录 warning，并按 `rerank=false` 语义继续主检索链路

## 11. 推荐代码落点

## 11.1 配置层

修改：

- `internal/config/config.go`

建议新增：

- `APIKeys []string`
- `KeyFailoverConfig`
- `api_key -> api_keys` 归一化逻辑

## 11.2 组合根

修改：

- `internal/app/app.go`

建议改法：

- `buildLLM`
  - 返回 key 级 failover wrapper
- `buildEmbedding`
  - 返回 key 级 failover wrapper
- `buildReranker`
  - 返回 key 级 failover wrapper

## 11.3 新增包装模块

建议新增目录：

- `internal/adapters/outbound/ai_key_failover/`

建议文件：

- `state.go`
  - key 状态
- `selector.go`
  - key 选择逻辑
- `classifier.go`
  - 错误分类
- `llm.go`
  - `LLMClient` 包装器
- `embedding.go`
  - `EmbeddingClient` 包装器
- `rerank.go`
  - `RerankerClient` 包装器

## 11.4 适配器层增强

建议适度增强：

- `internal/adapters/outbound/openai_native/*`
- `internal/adapters/outbound/dashscope_rerank/client.go`

目标：

- 暴露更明确的状态码与错误类型
- 便于上层做 key 级分类

## 12. 推荐执行流程

统一流程建议如下：

```text
收到请求
-> 选择当前健康 key
-> 使用该 key 对应客户端发起请求
-> 成功：清空该 key 的失败态并返回
-> 失败：分类错误
   -> 限流 / 配额 / 鉴权：暂停该 key，选择下一个 key 再试
   -> 请求无效：立即返回
   -> 公共故障：立即返回
-> 若全部 key 都失败
-> 回到当前业务语义
```

## 13. 日志建议

建议统一记录：

- `service = llm|embedding|rerank`
- `model`
- `provider`
- `endpoint_digest`
- `key_index`
- `key_fingerprint`
- `attempt`
- `error_class`
- `cooldown_until`
- `degraded`

注意：

- 严禁打印完整 key

## 14. 测试建议

## 14.1 配置测试

至少覆盖：

- `api_key` 单值归一化
- `api_key` 分隔字符串拆分
- `api_keys` 去重
- `api_keys` 为空时报错
- 不允许出现多 model / 多 provider 容灾配置

## 14.2 选择器测试

至少覆盖：

- `ordered_failover`
- `round_robin`
- key 暂停后跳过
- 冷却结束后重新加入

## 14.3 错误分类测试

至少覆盖：

- 429 -> 切 key
- quota -> 切 key
- 401/403 -> 切 key
- 400 -> 不切 key
- 5xx / timeout -> 默认不切 key

## 14.4 业务测试

至少覆盖：

- `LLM`：第一个 key 限流，第二个 key 成功
- `Embedding`：第一个 key 配额耗尽，第二个 key 成功
- `Rerank`：全部 key 失败后仍退回首轮排序
- `NoiseGate`：全部 embedding key 失败后仍降级为 regex-only

## 15. 推荐实施顺序

### 第一阶段

- 配置归一化
- key 选择器
- rerank key failover

理由：

- rerank 已有安全降级
- 风险最小

### 第二阶段

- embedding key failover
- 接入 memory search / post-action / noise gate

### 第三阶段

- llm key failover
- 补齐结构化链路测试

## 16. 最终建议

对于当前仓库，最稳妥、最符合检索正确性要求的方案不是“多模型容灾”，而是：

1. 固定 `provider`
2. 固定 `endpoint`
3. 固定 `model`
4. 固定 `embedding.dimension`
5. 只扩展 `api_keys`
6. 只做 key 级轮换与暂停

尤其对 `embedding` 而言，必须把这条原则写死：

> 向量维度相同，不代表向量语义相同；检索系统禁止以容灾名义混用不同 embedding 模型。

后续如果进入实现阶段，建议从 `rerank` 开始验证 key-only failover 包装层，再推广到 `embedding` 与 `llm`。
