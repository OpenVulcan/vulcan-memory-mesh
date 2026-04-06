# VMM Config README

这份文档用于帮助运维、开发和调试人员理解当前 VulcanMemoryMesh 本地版配置文件里每个节点的作用。

说明原则：

- 以当前代码中的真实配置结构为准，来源是 [config.go](D:/projects/VulcanMemoryMesh/internal/config/config.go)
- 默认值以 `DefaultLocal()` 为基准
- 示例配置以 [local.json](D:/projects/VulcanMemoryMesh/configs/local.json) 和 [openai.local.example.json](D:/projects/VulcanMemoryMesh/configs/openai.local.example.json) 为参考
- 环境变量覆盖以 [configs/.env.example](D:/projects/VulcanMemoryMesh/configs/.env.example) 为参考

## 1. 根节点总览

当前根配置节点如下：

- `grpc`
- `logging`
- `pii`
- `noise`
- `storage`
- `sqlite`
- `lancedb`
- `postgres`
- `llm`
- `embedding`
- `rerank`
- `vector`
- `relational`
- `post_action`
- `pre_check`
- `memory_pipeline`

## 2. grpc

### `grpc.listen_addr`

- 作用：gRPC 服务监听地址
- 默认值：`:8080`
- 常见示例：`127.0.0.1:17625`
- 建议：
  - 本地调试可绑定 `127.0.0.1`
  - 如果前面接 Caddy/Nginx，一般仍建议只监听内网或本机地址

### `grpc.max_receive_message_bytes`

- 作用：gRPC 单次请求允许接收的最大消息大小
- 默认值：`1048576`
- 说明：
  - 单位是字节
  - 过大可能放大异常请求的资源占用

### `grpc.request_timeout.workspace`

- 作用：管理类接口的超时预算
- 默认值：`15s`

### `grpc.request_timeout.pre_check`

- 作用：`PreCheck` RPC 的总超时预算
- 默认值：`8s`
- 注意：
  - 必须大于 `pre_check.intent_timeout`

### `grpc.request_timeout.post_action`

- 作用：`PostAction` RPC 的总超时预算
- 默认值：`8s`

### `grpc.shutdown_timeout`

- 作用：应用关闭时等待 gRPC 服务与依赖收尾的预算
- 默认值：`10s`

## 3. logging

### `logging.level`

- 作用：运行时日志级别
- 默认值：`info`
- 常见值：
  - `debug`
  - `info`
  - `warn`
  - `error`
- 建议：
  - `release` 环境可设为 `error`
  - 排障时再临时切到 `info` 或 `debug`

### `logging.format`

- 作用：日志输出格式
- 默认值：`text`
- 当前建议值：
  - `text`

### `logging.debug_rpc_payloads`

- 作用：是否输出明文调试载荷
- 默认值：`false`
- 影响：
  - 开启后，`PreCheck`、`PostAction`、检索阶段等日志会输出更多完整调试内容
- 建议：
  - 仅在本地排障时开启

### `logging.protect_payloads`

- 作用：当不输出明文时，是否把阶段载荷加密记录到日志
- 默认值：`false`
- 说明：
  - 用于保留可审计能力，但避免直接明文落日志

### `logging.payload_encryption_key`

- 作用：受保护载荷日志的加密密钥
- 默认值：空
- 注意：
  - 只有 `logging.protect_payloads=true` 时才需要
  - 当前要求是可解析成 32 字节密钥

## 4. pii

### `pii.default_language`

- 作用：PII 规则加载时使用的默认语言目录
- 默认值：`zh-CN`
- 建议：
  - 当前仓库主要按中文规则组织，通常保持默认即可

## 5. noise

### `noise.enabled`

- 作用：是否启用写库前的噪声过滤
- 默认值：`true`

### `noise.default_language`

- 作用：噪声规则默认语言
- 默认值：`zh-CN`

### `noise.semantic_enabled`

- 作用：是否启用语义噪声门
- 默认值：`true`
- 说明：
  - 如果 embedding 接口不可用，系统会降级为 regex-only

### `noise.semantic_threshold`

- 作用：语义噪声判定阈值
- 默认值：`0.88`
- 建议：
  - 阈值越高越保守
  - 调低会增加“拦截为噪声”的概率

## 6. storage

### `storage.mode`

- 作用：选择当前运行时使用分离存储还是组合存储
- 默认值：`split`
- 当前有效值：
  - `split`
  - `combined`
- 语义：
  - `split`：沿用历史分离架构，关系存储走 `sqlite`，向量存储走 `lancedb`
  - `combined`：切换到统一 PostgreSQL 组合库，关系与向量检索共享一套底座

### `storage.combined_provider`

- 作用：声明组合存储模式下启用的底座提供方
- 默认值：`postgres`
- 当前有效值：
  - `postgres`
- 注意：
  - 只有 `storage.mode=combined` 时该字段才生效

## 7. sqlite

### `sqlite.address`

- 作用：SQLite gRPC 网关地址
- 默认值：`127.0.0.1:19501`
- 说明：
  - 这是关系存储层，不是 SQLite 文件路径

### `sqlite.timeout`

- 作用：SQLite 网关调用超时
- 默认值：`5s`

## 8. lancedb

### `lancedb.address`

- 作用：LanceDB gRPC 网关地址
- 默认值：`127.0.0.1:19301`

### `lancedb.timeout`

- 作用：LanceDB 网关调用超时
- 默认值：`5s`

### `lancedb.table_name`

- 作用：向量表基础名称
- 默认值：`vmm_memory_vectors`
- 说明：
  - 运行时会自动拼接 embedding 维度，例如 `vmm_memory_vectors_1024`

### `lancedb.vector_column`

- 作用：向量列名
- 默认值：`vector`

## 9. postgres

### `postgres.dsn`

- 作用：PostgreSQL 组合库存储的连接串
- 默认值：空
- 常见示例：
  - `${VMM_POSTGRES_DSN}`
- 注意：
  - 当 `storage.mode=combined` 时启动必填
  - 建议通过环境变量注入，避免把凭据明文写入仓库内配置

### `postgres.schema`

- 作用：VMM 在 PostgreSQL 中使用的业务 schema
- 默认值：`public`
- 建议：
  - 本地调试可先使用 `public`
  - 多套环境共享同一实例时，建议为 VMM 单独划分 schema

### `postgres.flavor`

- 作用：选择 PostgreSQL 组合库内部使用的搜索方言
- 默认值：`paradedb`
- 当前有效值：
  - `paradedb`
  - `standard`
- 语义：
  - `paradedb`：使用 ParadeDB 的 BM25 与 `@@@` 检索能力，适合私有化高性能部署
  - `standard`：使用标准 PostgreSQL + `pg_trgm` 兜底，适合大多数公有云受限环境

### `postgres.query_timeout`

- 作用：PostgreSQL 组合库查询与写入操作的超时预算
- 默认值：`5s`

### `postgres.connect_timeout`

- 作用：建立 PostgreSQL 连接时的超时预算
- 默认值：`5s`

### `postgres.max_open_conns`

- 作用：连接池允许打开的最大连接数
- 默认值：`10`

### `postgres.min_idle_conns`

- 作用：连接池期望保留的最小空闲连接数
- 默认值：`1`

### `postgres.auto_create_extensions`

- 作用：是否允许运行时自动创建组合库所需扩展
- 默认值：`false`
- 说明：
  - `paradedb` flavor 可能依赖 ParadeDB/pgvector 相关扩展
  - `standard` flavor 需要 `pg_trgm`
- 建议：
  - 生产环境通常由 DBA 预先创建扩展，再保持该值为 `false`

### `postgres.bm25_index_concurrently`

- 作用：在 `paradedb` flavor 下是否以并发方式创建 BM25 索引
- 默认值：`true`
- 注意：
  - 仅在 `postgres.flavor=paradedb` 时生效

### `postgres.bm25_index_name`

- 作用：`paradedb` flavor 下 memory node BM25 索引名称
- 默认值：`vmm_memory_nodes_bm25_idx`

### `postgres.trgm_similarity_threshold`

- 作用：`standard` flavor 下 trigram 相似度检索阈值
- 默认值：`0.2`
- 注意：
  - 仅在 `postgres.flavor=standard` 时生效

### `postgres.vector_lists`

- 作用：向量索引构建时使用的 list 数
- 默认值：`100`
- 说明：
  - 该参数影响 PostgreSQL 组合库的向量检索索引规模与召回/性能平衡

### `postgres.vector_probes`

- 作用：查询阶段向量索引的 probe 数
- 默认值：`10`
- 说明：
  - probe 越大，通常召回更充分，但查询延迟也会更高

### `postgres.migration_batch_size`

- 作用：调试迁移链路把分离库存量数据导入组合库时的批次大小
- 默认值：`500`
- 适用场景：
  - `debug-migrate split-to-combined`

## 10. llm

### `llm.routes`

- 作用：声明 LLM 的显式多路由表
- 默认值：
  - `DefaultLocal()` 会提供一条默认 route，provider 为 `openai`，model 为 `gpt-4.1-mini`
- 当前约束：
  - `llm` 只允许使用 `routes`
  - 不再支持顶层 `llm.provider / llm.endpoint / llm.model / llm.api_keys / llm.nodes / llm.key_failover`
  - 不再支持任何位置的单值 `api_key`
- 运行时行为：
  - 先按 `priority` 从高到低选择 route
  - 同优先级保持配置声明顺序
  - 选中 route 后，再在 route 内部执行 `nodes + key_failover`

### `llm.routes[].provider`

- 作用：声明该 LLM route 使用的 provider
- 当前要求：
  - 必须是 OpenAI-compatible provider

### `llm.routes[].endpoint`

- 作用：该 LLM route 的接口根地址
- 启动要求：
  - 必填

### `llm.routes[].api_keys`

- 作用：该 LLM route 的 API Key 池
- 默认值：空数组
- 说明：
  - 当未显式声明 `nodes` 时，运行时会把 `api_keys + rpm/tpm/rpd` 折叠成一个默认节点
  - 这是 route 内部的 key 池，不是跨 route 的容灾能力

### `llm.routes[].rpm / llm.routes[].tpm / llm.routes[].rpd`

- 作用：route 级默认节点的吞吐额度
- 默认值：`0`
- 说明：
  - 仅在该 route 未显式声明 `nodes` 时生效
  - 表示默认节点下每个 key 各自独享的额度

### `llm.routes[].nodes`

- 作用：显式声明该 route 内部的吞吐节点
- 默认值：空数组
- 说明：
  - `nodes` 只在一条固定 `provider + endpoint + model` 路由内部工作
  - 每个节点可配置：
    - `name`
    - `api_keys`
    - `rpm`
    - `tpm`
    - `rpd`
  - 同一节点下多个 key 会各自独享该节点声明的额度
  - 如果额度档位不同，应拆成不同节点

### `llm.routes[].model`

- 作用：该 route 使用的 LLM 模型名
- 默认值：
  - `DefaultLocal()` 为 `gpt-4.1-mini`

### `llm.routes[].priority`

- 作用：控制 route 间的优先级
- 默认值：`0`
- 说明：
  - 数值越大越优先

### `llm.routes[].organization / llm.routes[].project`

- 作用：OpenAI-compatible 路由的可选组织与 project 标识
- 默认值：空

### `llm.routes[].params / llm.routes[].model_params`

- 作用：该 route 的额外请求参数
- 默认值：空对象
- 常见用途：
  - provider 特定开关
  - 模型级参数差异

### `llm.routes[].key_failover`

- 作用：该 route 内部 API Key 池的容灾策略
- 默认值：
  - `enabled=true`
  - `policy=ordered_failover`
  - `respect_retry_after=true`
  - `rate_limit_cooldown=5m`
  - `quota_cooldown=10m`
  - `auth_cooldown=12h`
  - `probe_after_cooldown=true`
- 说明：
  - 只对当前 route 内部的 key 轮换生效
  - route 之间的切换由 `priority + 声明顺序` 负责

## 11. embedding

### `embedding.provider`

- 作用：Embedding 提供方别名
- 默认值：`openai`
- 当前约束：
  - embedding 只允许固定 provider
  - 不支持 `routes`

### `embedding.endpoint`

- 作用：Embedding 接口根地址
- 启动要求：
  - 必填

### `embedding.api_keys`

- 作用：Embedding 的多 key 池
- 默认值：空数组
- 说明：
  - 如果未显式声明 `nodes`，运行时会把 `api_keys + rpm/tpm/rpd` 折叠成一个默认节点
  - 不再支持 `embedding.api_key`

### `embedding.rpm / embedding.tpm / embedding.rpd`

- 作用：默认 embedding 节点的吞吐额度
- 默认值：`0`
- 说明：
  - 仅在未显式声明 `embedding.nodes` 时生效
  - 表示默认节点下每个 key 各自独享的额度

### `embedding.nodes`

- 作用：显式声明 embedding 的吞吐节点
- 默认值：空数组
- 说明：
  - `nodes` 只用于吞吐分档和多 key 轮换
  - 不允许借由节点混用不同 provider / model / dimension
  - 每个节点可配置：
    - `name`
    - `api_keys`
    - `rpm`
    - `tpm`
    - `rpd`

### `embedding.model`

- 作用：Embedding 模型名
- 默认值：`text-embedding-3-large`

### `embedding.dimension`

- 作用：Embedding 维度
- 默认值：`1024`
- 注意：
  - `split` 模式下必须与 LanceDB 实际表维度一致
  - `combined` 模式下必须与 PostgreSQL 组合库向量列维度一致

### `embedding.organization / embedding.project`

- 作用：Embedding 请求的可选组织与 project 标识
- 默认值：空

### `embedding.params / embedding.model_params`

- 作用：Embedding 的附加参数
- 默认值：空对象

### `embedding.key_failover`

- 作用：固定模型 embedding 的 API Key 容灾策略
- 默认值：
  - `enabled=true`
  - `policy=ordered_failover`
  - `respect_retry_after=true`
  - `rate_limit_cooldown=5m`
  - `quota_cooldown=10m`
  - `auth_cooldown=12h`
  - `probe_after_cooldown=true`
- 重要说明：
  - embedding 只允许切 key
  - 不允许切 provider
  - 不允许切 model
  - 不允许切 dimension

## 12. rerank

### `rerank.enabled`

- 作用：是否启用重排序
- 默认值：`false`

### `rerank.top_n`

- 作用：进入 rerank 的候选数量
- 默认值：`8`

### `rerank.routes`

- 作用：声明 rerank 的显式多路由表
- 默认值：
  - `DefaultLocal()` 会提供一条默认 route，provider 为 `dashscope`
- 当前约束：
  - `rerank` 只允许顶层保留 `enabled / top_n / routes`
  - 不再支持顶层 `rerank.provider / rerank.endpoint / rerank.model / rerank.api_keys / rerank.timeout / rerank.key_failover`
  - 不再支持任何位置的单值 `api_key`
- 运行时行为：
  - 先按 `priority` 从高到低选择 route
  - 当前 route 失败后再切下一条 route
  - 所有 route 都失败时，检索链按 `rerank=false` 语义降级

### `rerank.routes[].provider`

- 作用：声明该 rerank route 使用的 provider
- 当前要求：
  - 当前内置实现只支持 `dashscope`

### `rerank.routes[].endpoint`

- 作用：该 rerank route 的接口地址
- 默认值：
  - `DefaultLocal()` 为 DashScope `text-rerank` 地址

### `rerank.routes[].api_keys`

- 作用：该 rerank route 的 key 池
- 默认值：空数组
- 说明：
  - 当未显式声明 `nodes` 时，运行时会把 `api_keys + rpm/tpm/rpd` 折叠成一个默认节点

### `rerank.routes[].rpm / rerank.routes[].tpm / rerank.routes[].rpd`

- 作用：route 级默认节点的吞吐额度
- 默认值：`0`

### `rerank.routes[].nodes`

- 作用：显式声明该 rerank route 内部的吞吐节点
- 默认值：空数组
- 说明：
  - 每个节点只在当前固定模型 route 内部工作
  - 每个节点可配置：
    - `name`
    - `api_keys`
    - `rpm`
    - `tpm`
    - `rpd`

### `rerank.routes[].model`

- 作用：该 rerank route 的模型名
- 默认值：
  - `DefaultLocal()` 为 `qwen3-vl-rerank`

### `rerank.routes[].timeout`

- 作用：该 route 的 HTTP 调用预算
- 默认值：
  - `DefaultLocal()` 为 `8s`

### `rerank.routes[].priority`

- 作用：控制 route 间的优先级
- 默认值：`0`

### `rerank.routes[].key_failover`

- 作用：该 route 内部 API Key 池的容灾策略
- 默认值：
  - `enabled=true`
  - `policy=ordered_failover`
  - `respect_retry_after=true`
  - `rate_limit_cooldown=5m`
  - `quota_cooldown=10m`
  - `auth_cooldown=12h`
  - `probe_after_cooldown=true`
- 说明：
  - 只对当前 route 内部的 key 轮换生效

## 13. vector

### `vector.provider`

- 作用：向量后端选择
- 默认值：`lancedb`
- 当前有效值：
  - `lancedb`
- 注意：
  - 该字段只在 `storage.mode=split` 时生效
  - `storage.mode=combined` 时，向量能力由 `postgres` 组合库统一承载

## 14. relational

### `relational.provider`

- 作用：关系存储后端选择
- 默认值：`sqlite`
- 当前有效值：
  - `sqlite`
- 注意：
  - 该字段只在 `storage.mode=split` 时生效
  - `storage.mode=combined` 时，关系存储由 `postgres` 组合库统一承载

## 15. post_action

### `post_action.input_mode`

- 作用：`PostAction` 输入模式
- 默认值：`compat`
- 当前常见值：
  - `compat`
  - `strict`

### `post_action.session_analysis_turn_threshold`

- 作用：触发 session 级分析的轮次阈值
- 默认值：`2`

### `post_action.session_analysis_token_threshold`

- 作用：触发 session 级分析的 token 阈值
- 默认值：`12000`

### `post_action.session_analysis_idle_timeout`

- 作用：空闲多长时间后触发 session 分析
- 默认值：`15m`

### `post_action.session_analysis_history_turns`

- 作用：分析时纳入多少条最近历史 turn
- 默认值：`3`

### `post_action.session_analysis_max_input_tokens`

- 作用：session 分析的最大输入 token 预算
- 默认值：`6000`

## 16. pre_check

### `pre_check.intent_timeout`

- 作用：`PreCheck` 第一层 LLM 意图提取的内部超时
- 默认值：`5s`
- 注意：
  - 必须小于 `grpc.request_timeout.pre_check`

### `pre_check.top_k`

- 作用：每个 pre-check 检索语句的候选上限
- 默认值：`5`

### `pre_check.search_scope`

- 作用：`PreCheck` 长期记忆检索范围
- 默认值：`space`
- 可选值：
  - `team`
  - `space`
  - `project`
- 语义：
  - `team`：检索当前 team 范围共享记忆
  - `space`：检索当前 space 范围共享记忆
  - `project`：只检索当前 project

### `pre_check.similarity_threshold`

- 作用：旧桥接字段，仅在 `memory_pipeline.min_similarity_score` 为空时用于回填
- 默认值：`0`
- 建议：
  - 新配置优先直接使用 `memory_pipeline.min_similarity_score`

## 17. memory_pipeline

### `memory_pipeline.max_search_keywords`

- 作用：第一层意图提取最多生成多少个搜索关键词/检索句
- 默认值：`5`

### `memory_pipeline.min_similarity_score`

- 作用：`PreCheck` 候选进入第二层评审前的最小相似度阈值
- 默认值：`0.75`

### `memory_pipeline.hybrid_enabled`

- 作用：是否启用混合检索
- 默认值：`true`
- 说明：
  - 只允许在 rerank 自己的 key 池内轮换
  - 不会复用 `llm` key
  - 如果 provider 返回 `Retry-After` / `Retry-After-Ms`，会优先采用其冷却建议
  - 所有 key 不可用时，运行时会记录 warning 并按禁用 rerank 继续执行
  - 开启后会尝试 `vector + lexical + RRF`

### `memory_pipeline.lexical_pre_tokenize`

- 作用：是否启用“应用层预分词后再写入 SQLite FTS5”的 lexical 检索模式
- 默认值：`true`
- 说明：
  - 开启时，运行时会在应用层使用纯 Go 的 `github.com/go-ego/gse` 预分词，并把结果以空格形式写入 `unicode61` FTS5 表
  - 查询阶段会对原始 query 做同样的预分词，再组装成 `MATCH` 表达式，以修复中文 BM25 对整句汉字难以切词的问题
  - 关闭时，会回退到仓库原有的正则分词与原始文本写入方式，适合纯英文或希望完全保持旧行为的部署
  - 该字段主要影响 `split` 模式下的 SQLite lexical 检索链路；`combined` 模式会改走 PostgreSQL 方言对应的词法检索实现

### `memory_pipeline.lexical_top_k`

- 作用：lexical 召回阶段的候选数
- 默认值：`8`

### `memory_pipeline.rrf_k`

- 作用：RRF 融合参数
- 默认值：`60`

### `memory_pipeline.mmr_enabled`

- 作用：是否启用 MMR 多样性控制
- 默认值：`true`

### `memory_pipeline.mmr_lambda`

- 作用：MMR 权重
- 默认值：`0.75`

### `memory_pipeline.weibull_enabled`

- 作用：是否启用读时 Weibull 衰减
- 默认值：`true`

### `memory_pipeline.weibull_shape`

- 作用：Weibull 形状参数
- 默认值：`1.35`

### `memory_pipeline.weibull_scale_hours`

- 作用：Weibull 时间尺度，单位小时
- 默认值：`2160`

### `memory_pipeline.weibull_min_multiplier`

- 作用：衰减乘子的最小值
- 默认值：`0.4`

### `memory_pipeline.weibull_reinforce_weight`

- 作用：强化次数对衰减速度的影响权重
- 默认值：`0.18`

### `memory_pipeline.weibull_cross_session_boost`

- 作用：跨 session 采纳对衰减速度的提升权重
- 默认值：`0.12`

## 18. 配置优先级

当前配置来源优先级可以简单理解为：

1. 代码默认值
2. `configs/*.json`
3. 环境变量覆盖

如果同一个字段同时出现在 JSON 和环境变量里，环境变量优先。

## 19. 推荐做法

### 本地开发

- 用 `configs/local.json` 作为主配置
- 敏感信息通过 `.env` 或本地环境变量注入
- 调试时再临时开启：
  - `logging.debug_rpc_payloads=true`

### 组合库存储调试

- 如果要测试统一 PostgreSQL 组合库：
  - `storage.mode=combined`
  - `storage.combined_provider=postgres`
  - 配置 `postgres.dsn`
- 私有化高性能全文检索场景：
  - `postgres.flavor=paradedb`
- 公有云受限标准 PostgreSQL 场景：
  - `postgres.flavor=standard`

### 线上 / 半生产环境

- `logging.level=error`
- `logging.debug_rpc_payloads=false`
- 如需保留审计载荷：
  - `logging.protect_payloads=true`
  - 配置合法的 `logging.payload_encryption_key`

### 检索范围

- 想让同空间项目共享记忆：
  - `pre_check.search_scope=space`
- 想严格项目隔离：
  - `pre_check.search_scope=project`
- 想扩大到 team 共享：
  - `pre_check.search_scope=team`

## 20. 参考文件

- [local.json](D:/projects/VulcanMemoryMesh/configs/local.json)
- [openai.local.example.json](D:/projects/VulcanMemoryMesh/configs/openai.local.example.json)
- [.env.example](D:/projects/VulcanMemoryMesh/configs/.env.example)
- [config.go](D:/projects/VulcanMemoryMesh/internal/config/config.go)
