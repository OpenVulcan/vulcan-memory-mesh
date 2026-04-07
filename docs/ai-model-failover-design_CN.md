# AI 路由与 Key 容灾设计

## 1. 当前结论

当前仓库已经全面收敛到一套 AI 配置契约，不再保留旧兼容模式：

- `llm`
  - 只允许 `llm.routes[]`
  - 支持多 provider / 多 endpoint / 多 model / 多 route
  - 当前内置 provider 包含 `openai / openai_native / openai_go / google_ai_studio`
  - route 之间按“当前调用层级对应的 weight”做有序容灾
  - `weights.*` 未声明时默认回退到 `100`
- `rerank`
  - 只允许 `rerank.routes[]`
  - 当前内置 provider 包含 `dashscope / siliconflow`
  - route 之间按 `priority` 做有序容灾
- `embedding`
  - 不允许 `routes`
  - 只允许固定 `provider + endpoint + model + dimension`
  - 只支持多 key 与 `nodes` 吞吐配置
  - 当前内置 provider 包含 `openai / openai_native / openai_go / google_ai_studio`
  - 不支持跨 provider / 跨 model / 跨维度容灾

这意味着：

- 不再支持 `llm.provider / llm.endpoint / llm.model / llm.api_keys` 这类顶层单路由写法
- 不再支持 `llm.routes[].priority`
- 不再支持 `rerank.provider / rerank.endpoint / rerank.model / rerank.api_keys` 这类顶层单路由写法
- 不再支持任何位置的单值 `api_key`
- 不再支持用环境变量去覆盖顶层 LLM / rerank 单路由字段

## 2. 设计边界

当前 AI 容灾分成两层：

1. `route` 层
   - 只对 `llm` 与 `rerank` 生效
   - 用来切 provider / endpoint / model
   - 失败后切到下一条 route
2. `node + key` 层
   - 对 `llm`、`rerank`、`embedding` 都生效
   - 只在一条固定模型配置内部工作
   - 用来做吞吐分档、节点轮询和多 key 轮换

职责分工固定如下：

- `routes`
  - 负责“不同模型配置之间”的切换
- `nodes`
  - 负责“同一模型配置内部”的吞吐与 key 池管理

## 3. 为什么 embedding 必须固定模型

`embedding` 直接影响以下链路的一致性：

- 记忆写入向量化
- 查询向量化
- noise gate 语义判断
- 首轮向量召回

即使两个 embedding 模型维度相同，也不代表它们处在同一语义空间。  
如果把查询向量从模型 A 切到模型 B，而历史向量仍由模型 A 写入，系统虽然还能计算相似度，但语义已不再可靠。

因此 `embedding` 的约束必须保持严格：

- 可以多 key
- 可以多 node
- 不可以多 provider
- 不可以多 model
- 不可以多 dimension

`embedding.nodes[]` 只是吞吐与额度分档，不是路由能力。

## 4. `llm` 配置契约

`llm` 现在只认 `routes[]`。

每条 route 必须自包含：

- `provider`
- `endpoint`
- `model`
- `api_keys` 或 `nodes`

可选字段：

- `name`
- `weights`
- `organization`
- `project`
- `params`
- `model_params`
- `key_failover`

示例：

```json
{
  "llm": {
    "routes": [
      {
        "name": "qwen-primary",
        "provider": "openai",
        "endpoint": "https://dashscope.aliyuncs.com/compatible-mode/v1",
        "model": "qwen3-32b",
        "weights": {
          "precheck_l1": 120,
          "precheck_l2": 100,
          "postaction_l1": 80,
          "postaction_l2": 160,
          "reserve": 100
        },
        "nodes": [
          {
            "name": "free-tier",
            "api_keys": ["key-a", "key-b"],
            "rpm": 3,
            "tpm": 40000,
            "rpd": 200
          }
        ],
        "key_failover": {
          "enabled": true,
          "policy": "ordered_failover"
        }
      },
      {
        "name": "openai-backup",
        "provider": "openai",
        "endpoint": "https://api.openai.com/v1",
        "api_keys": ["key-c"],
        "model": "gpt-4.1-mini",
        "weights": {
          "precheck_l1": 80,
          "precheck_l2": 100,
          "postaction_l1": 140,
          "postaction_l2": 90,
          "reserve": 100
        }
      }
    ]
  }
}
```

运行时顺序：

1. 先读取当前调用层级，例如 `precheck_l1 / precheck_l2 / postaction_l1 / postaction_l2`
2. 按该层级对应的 `weights.*` 从高到低排序
3. 同权重保持声明顺序
4. 先尝试第一条 route
5. 当前 route 失败后，再切下一条 route
6. route 内部再按 `nodes + key_failover` 处理节点与 key

## 5. `rerank` 配置契约

`rerank` 当前保留三个顶层字段：

- `enabled`
- `top_n`
- `routes`

其中 provider / endpoint / model / timeout / key 池都只能写在 `routes[]` 里。

示例：

```json
{
  "rerank": {
    "enabled": true,
    "top_n": 8,
    "routes": [
      {
        "name": "dashscope-primary",
        "priority": 100,
        "provider": "dashscope",
        "endpoint": "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank",
        "model": "qwen3-vl-rerank",
        "timeout": "8s",
        "api_keys": ["key-a", "key-b"]
      },
      {
        "name": "dashscope-backup",
        "priority": 50,
        "provider": "dashscope",
        "endpoint": "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank",
        "model": "gte-rerank",
        "timeout": "8s",
        "nodes": [
          {
            "name": "paid-tier",
            "api_keys": ["key-c"],
            "rpm": 60
          }
        ]
      },
      {
        "name": "siliconflow-fallback",
        "priority": 10,
        "provider": "siliconflow",
        "endpoint": "https://api.siliconflow.cn/v1/rerank",
        "model": "BAAI/bge-reranker-v2-m3",
        "timeout": "8s",
        "api_keys": ["key-d"]
      }
    ]
  }
}
```

说明：

- `top_n` 始终留在顶层
- route 级 provider 当前支持 `dashscope / siliconflow`
- 所有 route 都失败时，检索链会按 `rerank=false` 语义降级

## 6. `embedding` 配置契约

`embedding` 不支持 `routes`，只保留固定模型配置。

顶层字段包括：

- `provider`
- `endpoint`
- `api_keys`
- `rpm / tpm / rpd`
- `nodes`
- `model`
- `dimension`
- `max_batch_size`
- `organization`
- `project`
- `params`
- `model_params`
- `key_failover`

示例：

```json
{
  "embedding": {
    "provider": "openai",
    "endpoint": "https://api.openai.com/v1",
    "model": "text-embedding-3-large",
    "dimension": 1024,
    "max_batch_size": 10,
    "nodes": [
      {
        "name": "primary",
        "api_keys": ["key-a", "key-b"],
        "tpm": 1000000
      },
      {
        "name": "backup",
        "api_keys": ["key-c"],
        "tpm": 2000000
      }
    ],
    "key_failover": {
      "enabled": true,
      "policy": "ordered_failover"
    }
  }
}
```

说明：

- 如果未声明 `nodes`，则顶层 `api_keys + rpm/tpm/rpd` 会折叠成一个默认节点
- `nodes` 只表示吞吐分档
- `max_batch_size` 用于描述单次 embedding 请求允许携带的最大文本数
- 当前不再维护本地 `max_input_tokens_per_text` 截断预算；单条输入是否过长以 provider 返回为准，控制器只负责拆批、隔离超长条目，并由上层调用点决定是严格失败还是丢弃该条
- 不允许借由 `nodes` 或多个 key 混入不同 embedding 模型

## 7. `nodes` 的语义

`nodes` 的职责始终是：

> 在同一条固定模型配置内部，对不同额度档位的 key 池做分组

语义固定如下：

- 一个节点可以包含多个 key
- 节点上的 `rpm / tpm / rpd` 表示该节点下每个 key 各自独享的额度
- 如果两组 key 的额度档位不同，应拆成两个节点
- `nodes` 不是 provider/model 级路由能力

## 8. 已移除的旧模式

以下能力已被彻底移除：

- `llm` 顶层单路由字段：
  - `provider`
  - `endpoint`
  - `api_keys`
  - `rpm / tpm / rpd`
  - `nodes`
  - `model`
  - `organization`
  - `project`
  - `params`
  - `model_params`
  - `key_failover`
- `rerank` 顶层单路由字段：
  - `provider`
  - `endpoint`
  - `api_keys`
  - `rpm / tpm / rpd`
  - `nodes`
  - `model`
  - `timeout`
  - `key_failover`
- 所有位置的单值 `api_key`

以下环境变量也已移除：

- 所有 `VMM_LLM_*` 顶层运行时字段覆盖
- `VMM_RERANK_PROVIDER`
- `VMM_RERANK_ENDPOINT`
- `VMM_RERANK_API_KEY`
- `VMM_RERANK_API_KEYS`
- `VMM_RERANK_RPM`
- `VMM_RERANK_TPM`
- `VMM_RERANK_RPD`
- `VMM_RERANK_MODEL`
- `VMM_RERANK_TIMEOUT`
- 所有 `VMM_RERANK_KEY_FAILOVER_*` route 顶层覆盖
- `VMM_EMBED_API_KEY`

当前仍保留的 AI 相关环境变量只有：

- `embedding` 的固定模型字段覆盖
- `embedding` 的 key failover 覆盖
- `rerank.enabled`
- `rerank.top_n`

## 9. 校验规则

当前运行时校验规则如下：

- `llm.routes` 必须至少有一条 route
- `llm.routes[*]` 必须声明：
  - `provider`
  - `endpoint`
  - `model`
  - `api_keys` 或 `nodes`
- `rerank.enabled=true` 时，`rerank.routes` 必须至少有一条 route
- `rerank.routes[*]` 必须声明：
  - `provider`
  - `endpoint`
  - `model`
  - `timeout`
  - `api_keys` 或 `nodes`
- `embedding` 必须声明：
  - `provider`
  - `endpoint`
  - `model`
  - `dimension`
  - `api_keys` 或 `nodes`
- 任意 `nodes[*]` 都必须至少携带一组 `api_keys`

## 10. 推荐实践

建议按以下方式维护配置：

1. 所有 LLM / rerank 实际路由都显式写在 JSON 配置文件里
2. 密钥只通过 `${ENV_VAR}` 在 JSON 中展开，不再依赖旧的顶层运行时覆盖
3. embedding 只保留单模型，扩吞吐时使用多 key 或多 node
4. 不要在 route 之间共享“隐式继承”的 provider/model 语义；每条 route 都写成自包含配置
