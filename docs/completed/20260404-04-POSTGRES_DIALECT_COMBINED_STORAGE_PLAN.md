# PostgreSQL 组合库模式设计与迁移计划（支持 Dialect 方言）

## 1. 任务目标

本次任务的最终目标，不再是“单独做一个 ParadeDB 组合库适配器”，而是引入一套可扩展的 PostgreSQL 组合库架构：

- `split` 模式下继续保持当前 `SQLite + LanceDB` 架构零回归。
- `combined` 模式下统一使用 `PostgreSQL` 作为组合库底座。
- 在 PostgreSQL 组合库内部，通过 `flavor`（方言）参数路由两种不同的词法检索实现：
  - `paradedb`
  - `standard`
- 连接池、事务管理、关系数据落盘、向量存储、向量检索与管理删除逻辑全部共享。
- 差异只隔离在“全文索引建模”和“词法候选查询”两处，不为 10% 的方言差异重复造轮子。
- 迁移时继续以 `SQLite` 为事实主源，且强制废弃 `vector_json` 冗余列。

这份计划的核心思想是：

- 统一底座是 `postgres`
- 差异能力通过 `dialect/flavor` 内部路由
- 共享 90% 基础设施代码
- 对外保留一个稳定的组合库能力抽象

## 2. 当前现状与架构判断

结合当前仓库实现和新架构意图，先明确几个关键事实：

1. 当前主线的长期事实源是 `SQLite`，不是 `LanceDB`。
2. 当前 `vmm_memory_nodes` 已在 `SQLite` 中保存 `vector_json`，因此迁移到 PostgreSQL 组合库时，可直接从 `SQLite` 回放出 `[]float32`。
3. 当前 `LanceDB` 更像向量索引面，而不是唯一事实源。
4. 当前 `MemoryUseCase` 的召回链路由多个阶段拼接组成：
   - 向量召回
   - 关系回表
   - lexical 召回
   - RRF
   - rerank
   - Weibull 衰减
   - MMR
5. 无论是否启用 ParadeDB，以下部分都天然共享：
   - `pgxpool` 连接池
   - 事务边界
   - 关系表结构
   - `embedding vector(n)` 原生向量列
   - 向量距离 `<=>` 查询
   - 绝大多数 CRUD
   - 迁移和管理删除逻辑
6. 真正发生变化的只有两部分：
   - 如何建立词法索引
   - 如何发起词法候选召回并生成可融合分数

结论：

- `combined_provider` 不应再是 `paradedb`，而应统一抽象为 `postgres`。
- `paradedb` 与 `standard` 应作为 PostgreSQL 内部 flavor，而不是两个完全独立的外部 provider。
- 组合模式应设计成一个共享的 PostgreSQL 适配器，加两个 flavor 路由分支。

## 3. 核心架构决策

### 3.1 统一 provider 命名

组合模式统一使用：

- `storage.combined_provider = postgres`

不再使用：

- `storage.combined_provider = paradedb`

原因：

- ParadeDB 和标准 PostgreSQL 本质上共享同一底座能力。
- 将 provider 抽象成 `postgres`，更符合长期可扩展性，也更方便未来继续接入新 flavor。

### 3.2 引入 flavor（方言）参数

在组合模式下新增：

- `postgres.flavor = paradedb | standard`

语义如下：

- `paradedb`
  - 私有化、自托管、追求极致全文性能的部署模式
  - 使用 ParadeDB 的 BM25 能力
- `standard`
  - 面向公有云 RDS / TDSQL / 标准 PostgreSQL 受限环境的兜底模式
  - 使用 `pg_trgm` 三步符索引做泛中文/模糊匹配兜底

### 3.3 方言差异严格限制在两处

在适配器内部，只有以下两个环节允许使用 flavor 路由：

1. 搜索扩展与词法索引初始化
2. 词法候选 SQL 的生成与分数归一方式

其余部分必须共享：

- 连接池
- 事务管理
- 表结构迁移
- 基础 CRUD
- 向量检索
- 关系过滤
- 管理删除
- 数据迁移

## 4. 商业场景与架构价值

这套架构的商业价值非常明确：

### 4.1 `flavor = standard`

适用场景：

- 阿里云 RDS
- 腾讯云 TDSQL
- 其它只允许标准 PostgreSQL 扩展、不能安装 ParadeDB 的托管环境

价值：

- 不依赖自定义数据库发行版
- 可以在受限云环境中落地
- 通过 `pg_trgm` 获得可接受的泛中文模糊召回能力

### 4.2 `flavor = paradedb`

适用场景：

- 私有化 Docker 部署
- 自建数据库节点
- 对全文检索质量与性能要求更高的客户环境

价值：

- 使用 ParadeDB 的 BM25 与 Tantivy 引擎
- 中文通过 `jieba` 分词器获得更高质量的召回
- 查询层更接近现代搜索引擎的打分模型

### 4.3 对上层业务的统一收益

无论采用哪种 flavor，对上层业务接口都保持统一：

- gRPC 契约不需要因 flavor 改动而分叉
- `MemoryUseCase` 仍看到统一的候选集接口
- 迁移、删除、写回、回滚策略可以复用同一套组合库实现

## 5. 配置设计计划

### 5.1 顶层配置结构

建议新增或调整为：

```json
{
  "storage": {
    "mode": "split",
    "combined_provider": "postgres"
  },
  "postgres": {
    "dsn": "${VMM_POSTGRES_DSN}",
    "schema": "public",
    "flavor": "paradedb",
    "query_timeout": "5s",
    "connect_timeout": "5s",
    "max_open_conns": 10,
    "min_idle_conns": 1,
    "auto_create_extensions": false,
    "bm25_index_concurrently": true,
    "bm25_index_name": "vmm_memory_nodes_bm25_idx",
    "trgm_similarity_threshold": 0.2,
    "vector_lists": 100,
    "vector_probes": 10,
    "migration_batch_size": 500
  }
}
```

### 5.2 关键配置项说明

- `storage.mode`
  - `split | combined`
- `storage.combined_provider`
  - 固定为 `postgres`
- `postgres.flavor`
  - `paradedb | standard`
- `postgres.auto_create_extensions`
  - 启动时是否自动尝试执行 `CREATE EXTENSION`
- `postgres.trgm_similarity_threshold`
  - 标准方言下 trigram 相似度阈值
- `postgres.vector_lists / vector_probes`
  - PostgreSQL 向量索引调优参数

### 5.3 环境变量计划

建议新增：

- `VMM_STORAGE_MODE`
- `VMM_STORAGE_COMBINED_PROVIDER`
- `VMM_POSTGRES_DSN`
- `VMM_POSTGRES_SCHEMA`
- `VMM_POSTGRES_FLAVOR`
- `VMM_POSTGRES_QUERY_TIMEOUT`
- `VMM_POSTGRES_CONNECT_TIMEOUT`
- `VMM_POSTGRES_MAX_OPEN_CONNS`
- `VMM_POSTGRES_MIN_IDLE_CONNS`
- `VMM_POSTGRES_AUTO_CREATE_EXTENSIONS`
- `VMM_POSTGRES_BM25_INDEX_CONCURRENTLY`
- `VMM_POSTGRES_BM25_INDEX_NAME`
- `VMM_POSTGRES_TRGM_SIMILARITY_THRESHOLD`
- `VMM_POSTGRES_VECTOR_LISTS`
- `VMM_POSTGRES_VECTOR_PROBES`
- `VMM_POSTGRES_MIGRATION_BATCH_SIZE`

### 5.4 配置兼容策略

- `split` 模式：
  - 继续要求 `sqlite` 与 `lancedb` 配置完整
- `combined` 模式：
  - 以 `storage.mode` 与 `postgres` 节点为准
  - 原有 `vector.provider` / `relational.provider` 不再作为主控制项
  - 可保留兼容字段，但必须在 `Normalize / Validate` 中明确忽略或映射，并记录日志

## 6. 代码架构设计：Dialect Pattern

### 6.1 新适配器目录

建议新增：

- `internal/adapters/outbound/vldb_postgres`

### 6.2 共享代码与方言代码的边界

推荐目录职责拆分如下：

- `store.go`
  - PostgreSQL 统一入口适配器
- `pool.go`
  - `pgxpool` 初始化与连接参数
- `schema_shared.go`
  - 共享表结构迁移
- `vector.go`
  - 共享向量写入与 `<=>` 检索
- `memory_shared.go`
  - 共享关系读写逻辑
- `migration.go`
  - split -> combined 数据迁移
- `dialect.go`
  - flavor 接口定义
- `dialect_paradedb.go`
  - ParadeDB 方言实现
- `dialect_standard.go`
  - 标准 PostgreSQL 方言实现

### 6.3 推荐的方言接口

建议内部抽象出类似接口：

```go
type searchDialect interface {
    Name() string
    EnsureSearchExtensions(ctx context.Context, tx pgx.Tx) error
    EnsureSearchIndexes(ctx context.Context, tx pgx.Tx) error
    BuildLexicalCandidateQuery(input LexicalQueryInput) (sql string, args []any)
    NormalizeLexicalScore(row LexicalCandidateRow) float64
}
```

目的：

- 把 flavor 差异压缩到“初始化”和“查询 SQL”两个点
- 让 `Store` 主体逻辑继续共享

### 6.4 共享能力清单

以下能力必须 100% 共享，不允许 flavor 各自重复实现：

- `pgxpool` 连接池创建
- 事务 begin/commit/rollback
- 基础 schema migration
- 会话、turn、profile、memory 的关系写入
- `embedding` 原生向量列写入
- 向量距离 `<=>` 检索
- 项目/用户删除
- 迁移统计输出

## 7. PostgreSQL 组合库表结构设计

### 7.1 总体原则

- 表名与字段语义尽量沿用当前 SQLite 设计
- 时间字段第一阶段继续保留毫秒时间戳风格，避免一次性引发全局时间类型迁移
- 词法能力的差异只通过 flavor 下的物理索引和查询 SQL 体现

### 7.2 重点表：`vmm_memory_nodes`

继续将 `vmm_memory_nodes` 作为统一长期记忆主表。

保留现有主要字段：

- `id`
- `team_id`
- `space_id`
- `project_id`
- `user_id`
- `origin_session_id`
- `source_turn_id`
- `vector_id`
- `source_kind`
- `scope_level`
- `category`
- `abstract`
- `details`
- `memory_status`
- `priority`
- `memory_level`
- `refresh_weight`
- `support_count`
- `rebuttal_count`
- `status_reason`
- `expires_timestamp`
- `last_recalled_timestamp`
- `last_adopted_timestamp`
- `last_reinforced_timestamp`
- `recalled_count`
- `adopted_count`
- `reinforcement_count`
- `cross_session_adopted_count`
- `decay_disabled`
- `dedupe_hash`
- `created_timestamp`
- `updated_timestamp`

组合模式新增字段：

- `embedding vector(<dimension>) NOT NULL`

强制约束：

- 目标 PostgreSQL 组合库中绝不保留 `vector_json` 冗余列。
- 迁移时只允许从 SQLite 读取 `vector_json`，在 Go 内存中解析为 `[]float32` 后直接写入 `embedding`。
- JSON 串解析完成后必须立即丢弃，绝不在目标库持久化。

原因：

- JSON 向量会造成明显的存储膨胀与 I/O 浪费。
- 在企业级组合库中不可接受。

### 7.3 共享索引设计

不受 flavor 影响的共享索引包括：

- 常规过滤索引
  - `(project_id, memory_status, expires_timestamp, id)`
  - `(origin_session_id, memory_status, expires_timestamp, id)`
  - `(source_turn_id, id)`
  - `(origin_session_id, project_id, user_id, source_kind, scope_level, dedupe_hash, memory_status, expires_timestamp, created_timestamp)`
- 向量索引
  - 针对 `embedding` 建立向量索引

说明：

- 向量检索无论 `flavor = paradedb` 还是 `standard` 都统一使用 `embedding <=> $1`。

## 8. 两种 Flavor 下的分词、索引与检索物理实现

### 8.1 `flavor = paradedb`：纯血 BM25 高级模式

#### 建索引阶段

使用 ParadeDB 的 BM25 索引。

必须显式指定 `jieba` 中文分词器。

建议口径：

```sql
CREATE INDEX vmm_memory_nodes_bm25_idx
ON vmm_memory_nodes
USING bm25 (
    id,
    (abstract::pdb.jieba),
    (details::pdb.jieba),
    team_id,
    space_id,
    project_id,
    origin_session_id,
    source_turn_id,
    user_id,
    memory_status,
    priority,
    memory_level,
    expires_timestamp,
    created_timestamp,
    updated_timestamp
)
WITH (key_field = 'id');
```

要求：

- `jieba` 必须显式写出，不能依赖默认 tokenizer。
- `abstract` 与 `details` 必须直接作为 BM25 文本字段进入索引。
- 常用过滤与排序字段要尽量纳入覆盖索引，利用 ParadeDB 自定义扫描下推能力。

#### 检索阶段

统一使用 ParadeDB 的 `@@@` Query Builder 路由词法检索。

同时使用 `pdb.score(id)` 取回 BM25 分数。

设计目标：

- 让 SQL 层直接得到高质量 BM25 lexical candidates
- 候选结果天然带可排序的 BM25 score

### 8.2 `flavor = standard`：公有云 / 原生 PG 兜底模式

#### 建索引阶段

明确放弃：

- `tsvector`
- PostgreSQL 默认 FTS 分词
- 任何依赖编译额外中文词典插件的方案

必须自动尝试执行：

```sql
CREATE EXTENSION IF NOT EXISTS pg_trgm;
```

然后建立基于三步符的 GIN 倒排索引，例如：

```sql
CREATE INDEX vmm_memory_nodes_abstract_trgm_idx
ON vmm_memory_nodes
USING GIN (abstract gin_trgm_ops);

CREATE INDEX vmm_memory_nodes_details_trgm_idx
ON vmm_memory_nodes
USING GIN (details gin_trgm_ops);
```

说明：

- `pg_trgm` 对中文并不是分词器，但通过 n-gram/三步符匹配可以提供泛中文模糊匹配兜底能力。
- 对于大多数公有云托管 PostgreSQL，这比要求安装 ParadeDB 或中文 FTS 词典更现实。
- 如果运行时没有创建扩展权限，启动应快速失败，并给出明确的运维提示。

#### 检索阶段

标准方言下放弃 BM25 算分，统一采用 trigram 模糊匹配召回。

推荐手段：

- `ILIKE '%关键词%'`
- `similarity(column, $1)`
- 必要时结合 `%` 相似操作符

设计目标：

- 触发 `gin_trgm_ops` 索引
- 以模糊匹配为兜底词法候选来源
- 在 SQL 层生成一个统一的 lexical score 近似值，供后续与向量候选融合

### 8.3 Flavor 差异总表

| 项目 | `paradedb` | `standard` |
| --- | --- | --- |
| 底座 | PostgreSQL + ParadeDB 扩展能力 | 标准 PostgreSQL |
| 中文文本处理 | `jieba` 分词器 | trigram / n-gram 模糊匹配 |
| 索引方式 | `USING bm25` | `pg_trgm` + `GIN` |
| 查询语法 | `@@@` + `pdb.score(id)` | `ILIKE` / `similarity()` |
| 词法得分 | 原生 BM25 score | 近似 trigram similarity score |
| 目标场景 | 私有化高性能全文检索 | 公有云标准环境兜底 |

## 9. 查询优化与混合检索设计

### 9.1 共享目标

无论 flavor 如何变化，都不能回退成旧的“双后端兼容拼接”路径。

目标应是：

- PostgreSQL 内部完成第一层候选召回
- SQL 层先完成向量候选与 lexical 候选的合并
- 应用层只保留真正与模型策略相关的后处理

### 9.2 共享向量路径

两种 flavor 完全共享：

- `embedding <=> $1` 向量距离查询
- 层级过滤
- `BoundarySessionID / BoundaryMaxTurnID / ExcludeBoundaryTurn` 过滤
- `memory_status / expires_timestamp` 过滤

### 9.3 `paradedb` 方言下的混合召回

SQL 层完成：

- BM25 lexical 候选召回
- 取回 `pdb.score(id)` 作为 lexical score
- 向量候选召回
- 通过 CTE 或窗口函数做候选合并
- 在 SQL 层先做第一轮 RRF 或等价融合

应用层继续保留：

- rerank
- Weibull 衰减
- MMR
- 最终 top-k 裁剪

### 9.4 `standard` 方言下的混合召回

SQL 层完成：

- trigram lexical 候选召回
- 从 `similarity()` 或匹配命中中构造一个 0..1 区间的近似 lexical score
- 向量候选召回
- 候选合并
- SQL 层第一轮 RRF 或等价融合

说明：

- `standard` flavor 不追求 BM25 级精确词法打分。
- 它的目标是：
  - 在受限环境下提供可靠可用的词法兜底
  - 维持统一的组合库架构

### 9.5 统一候选输出模型

建议无论 flavor 如何，方言层都产出同一类 lexical candidate：

- `memory_id`
- `lexical_score`
- `lexical_origin`

这样共享的 PostgreSQL 适配器主流程就可以：

- 不关心具体 lexical score 来自 BM25 还是 trigram
- 只消费统一字段做后续融合

## 10. 写入与事务一致性计划

### 10.1 PostAction 写回

组合模式下改为单事务完成：

- turn analysis 写回
- `vmm_memory_nodes` 插入或更新
- `embedding` 向量列写入
- `vmm_memory_context_edges` upsert
- 被覆盖记忆状态更新

### 10.2 主动写记忆

组合模式下改单事务完成：

- 幂等校验
- 记忆主表写入
- 向量列写入

### 10.3 管理删除

组合模式下：

- `DeleteProject`
- `DeleteUser`
- `MigrateProject`

都必须尽量在同一库、同一事务语义下完成。

## 11. Schema 版本与启动流程计划

### 11.1 组件版本命名

建议新增 PostgreSQL 组合库组件版本：

- `postgres_combined`

同时在内部记录 flavor 相关索引版本信息，例如：

- `postgres_combined_search_paradedb`
- `postgres_combined_search_standard`

目的：

- 避免 split 模式版本与 combined 模式版本互相污染
- 避免 `paradedb` 和 `standard` 的词法索引版本判断互相覆盖

### 11.2 启动流程

`split` 模式：

- 继续执行 SQLite migration
- 继续执行 LanceDB schema sync

`combined` 模式：

- 执行共享 PostgreSQL schema migration
- 按 flavor 执行搜索扩展检查与索引初始化
- 不再执行 LanceDB rebuild

## 12. 数据迁移计划

### 12.1 迁移入口

建议新增：

- `--debug-migrate split-to-combined`

并补充：

- `--debug-clean postgres`

### 12.2 迁移原则

- 默认不删除源数据
- 默认要求停写或停机后执行
- 迁移必须幂等
- 统计结果必须结构化输出

### 12.3 迁移顺序

建议顺序：

1. 连接目标 PostgreSQL
2. 初始化共享表结构
3. 按 flavor 初始化搜索扩展与索引能力
4. 从 `SQLite` 迁移层级、会话、turn、memory、profile、noise 数据
5. 在 Go 内存中把 `vector_json` 解析成 `[]float32`
6. 直接写入目标表的 `embedding` 原生列
7. 完成统计与抽样校验

### 12.4 强制废弃 `vector_json`

迁移时明确取消：

- 在目标库保留 `vector_json`
- 把 JSON 向量再次写回 PostgreSQL
- 任何“为了调试方便保留一份 JSON”的妥协实现

迁移时只允许：

- 从 SQLite 读 `vector_json`
- 在 Go 内存中解析成 `[]float32`
- 直写 `embedding`
- 解析后立即丢弃 JSON

### 12.5 flavor 对迁移后的索引影响

迁移完成后：

- `flavor = paradedb`
  - 原文 `abstract / details` 建 BM25 + `jieba` 索引
- `flavor = standard`
  - 原文 `abstract / details` 建 trigram GIN 索引

两者都不需要：

- 预分词中间列
- `tsvector`
- FTS 物化列

## 13. 详细执行步骤

### 第 1 阶段：配置与命名体系重构

1. 将组合库 provider 统一改为 `postgres`
2. 新增 `postgres.flavor`
3. 更新配置加载、环境变量、校验逻辑
4. 更新示例配置与文档

### 第 2 阶段：共享 PostgreSQL 适配器落地

1. 新建 `internal/adapters/outbound/vldb_postgres`
2. 实现共享连接池与事务层
3. 实现共享表结构与基础 CRUD
4. 实现共享向量写入与 `<=>` 检索

### 第 3 阶段：方言层落地

1. 实现 `dialect_paradedb.go`
2. 实现 `dialect_standard.go`
3. 将差异严格限制在：
   - 搜索扩展/索引初始化
   - lexical query builder

### 第 4 阶段：组合模式混合检索打通

1. `MemoryUseCase` 增加统一优化搜索入口
2. 在 PostgreSQL 适配器中完成：
   - 向量候选
   - lexical 候选
   - 第一轮 SQL 融合
3. 应用层继续保留 rerank / decay / MMR

### 第 5 阶段：迁移与运维命令

1. 新增 `debug-migrate`
2. 新增 PostgreSQL 清理命令
3. 实现 split -> combined 迁移报告

### 第 6 阶段：文档同步

至少同步更新：

- `README.md`
- `configs/vmm_config_readme.md`
- `docs/post-action-guide_CN.md`
- `docs/noise-gate-guide_CN.md`
- 必要时新增 `docs/postgres-combined-guide_CN.md`

### 第 7 阶段：测试与验收

1. 配置测试
2. app 组合根装配测试
3. 共享 PostgreSQL 适配器单测
4. `paradedb` / `standard` 两套 flavor 测试
5. 迁移命令测试
6. 检索与边界过滤测试
7. 最少测试集
8. 大改后执行 `go test ./...`

## 14. 验收标准

### 14.1 功能验收

- `split` 模式零回归
- `combined + postgres + paradedb` 正常可用
- `combined + postgres + standard` 正常可用
- 同一套上层用例无需区分 flavor

### 14.2 检索验收

- `paradedb` flavor 能使用 BM25 + `jieba` 获得高质量中文词法召回
- `standard` flavor 能通过 trigram 获得可接受的泛中文模糊召回
- 两种 flavor 都能与共享向量召回链路稳定融合

### 14.3 一致性验收

- `vector_json` 在目标库中彻底消失
- `memory row + embedding` 单事务写入
- 删除与迁移不再依赖双库补偿

### 14.4 运维验收

- CLI 命名统一
- 扩展缺失时错误信息清晰
- 文档与配置示例完整

## 15. 还需要额外关注的风险点

### 15.1 扩展权限风险

并不是所有公有云 PostgreSQL 都允许运行时执行 `CREATE EXTENSION`。  
因此计划中虽然要求自动尝试安装扩展，但必须兼容：

- 权限不足时快速失败
- 输出明确的运维指引

### 15.2 `standard` flavor 的分数语义弱于 BM25

`standard` flavor 的 trigram similarity 不是 BM25。  
这意味着：

- 它是词法兜底，不是全文打分替代品
- SQL 层融合时必须做显式归一化，避免与向量分数直接裸比

### 15.3 中文质量验证不可跳过

虽然 trigram 对中文天然兼容性较好，但仍需专门验证：

- 中文自然语言
- 中英混合
- 路径、包名、类名
- 代码片段关键词

### 15.4 未来 flavor 扩展边界

当前只规划：

- `paradedb`
- `standard`

但接口设计要为未来预留可能性，例如：

- 进一步 specialized 的 cloud flavor
- 其它扩展组合模式

## 16. 当前状态

- 本文件对应的 PostgreSQL Dialect Pattern 组合库主计划已完成实施。
- 当前代码已完成以下主链路落地：
  - `config` 已支持 `storage.mode=combined`、`storage.combined_provider=postgres`、`postgres.flavor=paradedb|standard`
  - `app` 组合根已支持在 combined 模式下装配统一的 `vldb_postgres` 适配器
  - `internal/adapters/outbound/vldb_postgres` 已完成共享连接池、共享 schema、版本初始化与版本校验
  - `paradedb` 方言已支持 BM25 + `jieba` 建索引与 `@@@` 词法召回
  - `standard` 方言已支持 `pg_trgm` + `GIN` 建索引与 `ILIKE/similarity` 词法召回
  - 统一向量检索、统一记忆读写、turn/post-action 写回、profile 工作流、workspace 管理、noise cache、调试迁移与调试清理均已落地
  - `SQLite -> PostgreSQL` 的 `split-to-combined` 迁移命令已可用，并继续坚持 `SQLite` 为事实主源、目标库彻底废弃 `vector_json`
  - PostgreSQL 组合库现已补齐 SQL 层首轮混合召回：优先在数据库内融合向量候选与 lexical 候选，再进入应用层 rerank / Weibull / MMR
- 这意味着本计划中的“配置接入、统一适配器、方言路由、迁移运维、组合检索优化”五大主目标均已完成。
- 下一阶段如继续演进，应另起新计划承载：
  - PostgreSQL migration runner
  - 旧 flavor 索引收敛与清理策略
  - 调试迁移/清理链路的运维健壮性增强

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已按 Dialect Pattern 完成 PostgreSQL 组合库主方案落地，不再暴露独立 `paradedb provider`，统一通过 `postgres + flavor` 管理。
- 已完成 `paradedb` 与 `standard` 两套词法实现，并把共享能力稳定收敛到一个统一的 `vldb_postgres` 适配器中。
- 已完成此前阶段遗留的 turn/post-action、profile、workspace、迁移、清理与 schema 版本初始化修复。
- 本轮补齐了最后一个真正阻碍计划闭环的核心缺口：PostgreSQL 组合库现在可以在 SQL 层完成首轮混合召回融合，不再只是在应用层把向量检索和 lexical 检索重新拼接。

### 2. 📂文件变更清单

- 新增：`internal/adapters/outbound/vldb_postgres/hybrid_search.go`
- 修改：`internal/adapters/outbound/vldb_postgres/dialect.go`
- 修改：`internal/adapters/outbound/vldb_postgres/dialect_paradedb.go`
- 修改：`internal/adapters/outbound/vldb_postgres/dialect_standard.go`
- 修改：`internal/adapters/outbound/vldb_postgres/dialect_test.go`
- 修改：`internal/app/usecase/memory_query.go`
- 修改：`internal/app/usecase/memory_query_test.go`
- 修改：`configs/local.json`
- 修改：`configs/openai.local.example.json`
- 修改：`configs/vmm_config_readme.md`
- 修改：`configs/.env.example`
- 修改：`README.md`
- 修改：`internal/adapters/outbound/vldb_postgres/schema.go`
- 修改：`internal/adapters/outbound/vldb_postgres/debug_migrate.go`
- 新增：`internal/adapters/outbound/vldb_postgres/schema_version.go`
- 新增：`internal/adapters/outbound/vldb_postgres/schema_version_test.go`
- 新增：`cmd/vmm-local/debug_migrate.go`
- 新增：`internal/adapters/outbound/vldb_sqlite/debug_export.go`
- 新增：`internal/adapters/outbound/vldb_postgres/debug_clean.go`
- 新增：`internal/platform/storagemigrate/snapshot.go`

### 3. 💻关键代码调整详情

- 在 PostgreSQL 方言接口中新增 SQL 级混合召回构建能力，使 `paradedb` 与 `standard` 都能在数据库内直接输出一阶段融合候选。
- `paradedb` 方言新增基于 `@@@ + pdb.score + FULL OUTER JOIN + RRF` 的融合 SQL；`standard` 方言新增基于 `pg_trgm/similarity + pgvector + RRF` 的融合 SQL。
- `MemoryUseCase` 新增组合库快速路径识别：当底层向量存储显式支持 `SearchHybridMemory` 时，优先走单条 SQL 融合查询；失败时自动降级回历史的“向量 + lexical + 应用层 RRF”链路。
- 命中映射阶段现在会保留底层返回的 `origin` 标签，使 SQL 层混合召回与旧链路在上层排序、日志与解释文本中保持统一语义。
- 同期已完成并并入本计划闭环的实现包括：PostgreSQL schema 版本初始化与校验、`split-to-combined` 迁移、`debug-clean postgres`、配置示例与环境变量文档同步。

### 4. ⚠️遗留问题与注意事项

- 当前已实现版本初始化与版本漂移 fail-fast，但尚未实现真正的 PostgreSQL migration runner。
- flavor 切换后旧索引的主动收敛/清理策略尚未实现，当前更偏向“新 flavor 补建所需索引并继续工作”。
- 调试迁移和调试清理链路仍存在可继续加强的运维边界，例如 DSN 输出脱敏、导出只读化和 post-commit 后处理上下文隔离；这些属于后续专项优化，不再阻塞本计划闭环。
