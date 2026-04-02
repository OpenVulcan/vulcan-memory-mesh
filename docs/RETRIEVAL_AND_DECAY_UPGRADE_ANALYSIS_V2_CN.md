# 检索、重排序、衰减与情境化记忆升级分析（修正版 V2）

更新时间：2026-04-02  
适用范围：基于当前仓库真实实现，对上一版 `RETRIEVAL_AND_DECAY_UPGRADE_ANALYSIS_CN.md` 进行修正、补充与重判。  
本文定位：**替代旧版结论**，尤其修正 `交叉编码器重排序`、`MMR`、`情境化记忆`、`语义去重`、`自适应检索` 相关判断。

---

## 1. 修正版总判断

## 1.1 结论总表

| 能力 | 修正版结论 | 优先级 | 说明 |
| --- | --- | --- | --- |
| 向量 + BM25 混合检索 | 可以，而且强烈建议引入 | 高 | 当前收益最大，且与 `LanceDB + SQLite` 架构天然兼容 |
| RRF 融合 | 可以，建议作为默认融合算法 | 高 | 最适合当前“向量分数”和“BM25 分数”不可比的局面 |
| 交叉编码器重排序 | 可以，且应提升为二阶段正式能力 | 中高 | 旧版“先不建议”需要修正为“不是第一阶段，但应作为第二阶段推荐项” |
| MMR 多样性控制 | 可以，建议接在融合或 rerank 之后 | 中高 | 对 `PreCheck` 第二层候选尤其有价值，能减少高度相似候选挤占名额 |
| Weibull 衰减模型 | 可以，但不建议第一步直接替换硬 TTL | 中高 | 第一阶段仍应保留 `expires_timestamp` 作为硬边界 |
| 三层晋升机制 | 可以，但不建议新建第二套等级体系 | 中高 | 应优先复用现有 `memory_level(L0-L3)`，而不是再平行引入另一套 tier 字段 |
| 访问强化机制 | 可以，但应把“访问”定义成“有效使用/被采纳”而不是“被召回过一次” | 中 | 当前代码只有“被采纳”才写回生命周期，语义上更稳妥 |
| 情境化记忆 | 可以，但属于中期结构升级 | 中高 | 需要新增上下文关系表，不是只改排序参数即可完成 |
| 支持/反驳统计 | 可以，建议和情境化记忆一起设计 | 中高 | 需要新增统计字段或上下文证据表 |
| 多提供商重排序 | 可以，建议抽象独立端口 | 中 | 当前项目没有 `RerankerClient` 端口，需要补基础抽象 |
| 语义去重 | 可以，建议“精确去重 + 语义去重”双层并存 | 中高 | 不应废弃现有 `dedupe_hash`，而应在其上叠加语义层 |
| 自适应检索 | **当前已经部分存在** | 中 | 这不是“从零引入”，应改为“增强现有 `PreCheck` 第一层决策器” |

## 1.2 对旧报告的关键修正

上一版总体方向没有错，但有 5 个地方需要明确修正：

1. `自适应检索` 不是新能力，当前 `PreCheck` 第一层 `extract_intent.md` 已经在输出 `need_memory`，项目里已经存在“是否需要记忆召回”的动态判断。
2. `交叉编码器重排序` 不应该再简单归类为“后面再看”。如果接受外部 provider 依赖，它应当成为 **混合检索后的标准二阶段能力**。
3. `MMR` 在旧报告中缺失，但它对当前 `PreCheck` 第二层评审特别重要，因为当前候选容易被相似记忆挤满。
4. `三层晋升机制` 不应再新加一套 `Peripheral / Working / Core` 持久化枚举。当前已有 `memory_level = L0-L3`，应优先映射复用。
5. `情境化记忆 + 支持/反驳统计` 不能只靠 prompt 或配置实现，它需要 **新增关系结构和检索过滤链路**。

---

## 2. 当前代码现状与重新判断依据

## 2.1 当前检索链仍是“纯向量单通道”

当前 `internal/app/usecase/memory_query.go` 中 `MemoryUseCase.Search(...)` 的真实链路仍然是：

1. 解析 `query_json`
2. 对每个 query 做 embedding
3. 调 `VectorStore.Search(...)`
4. 调 `LoadMemoryNodesByVectorIDs(...)` 回表
5. 直接按向量分数返回

当前没有：

- `BM25 / FTS`
- `RRF`
- `rerank`
- `MMR`
- `context filter`
- `support / rebuttal weighting`

因此，你给出的新方案不会与现有复杂检索链冲突，反而是顺着现有单通道链路逐层扩展。

## 2.2 当前项目已经有“自适应检索”雏形

`PreCheck` 不是无脑检索：

- `configs/prompts/default/extract_intent.md`
- `internal/logic/processor/intent_extractor.go`
- `internal/app/usecase/precheck.go`

这条链已经会输出：

- `need_memory`
- `queries`
- `reason`

然后 `PreCheckUseCase.Execute(...)` 只有在 `intent.NeedMemory == true` 时才会进入记忆召回。

所以修正版结论必须是：

- 当前项目已经具备 **LLM 驱动的自适应检索 gate**
- 后续要做的不是“新增 adaptive retrieval”
- 而是把它升级为“**混合检索感知**、**代价感知**、**情境感知**”的自适应 gate

## 2.3 当前生命周期字段已经能承接 Weibull，但还不够完整

当前 `vmm_memory_nodes` 已经有这些衰减相关信号：

- `memory_level`
- `priority`
- `refresh_weight`
- `expires_timestamp`
- `last_recalled_timestamp`
- `last_adopted_timestamp`
- `recalled_count`
- `adopted_count`
- `cross_session_adopted_count`

因此：

- 做 Weibull 读时打分是可行的
- 做三层晋升也是可行的
- 但如果要把 Weibull 变成真正的生命周期主模型，还缺：
  - memory 侧过期收敛任务
  - 更明确的“强化”时间戳
  - 更清晰的访问/强化统计语义

## 2.4 当前有两个前置缺陷必须先修

这两个问题在修正版里优先级必须继续维持为最高：

1. `LoadMemoryNodesByVectorIDs(...)` 当前没有过滤 `memory_status = ACTIVE` 和 `expires_timestamp`
2. `LoadActiveSessionMemoryNodes(...)`、`LoadRecentDirectMemoryWrites(...)` 也没有过滤过期记忆

如果这两个缺口不补：

- 混合检索会把已过期数据和有效数据一起融合
- Weibull 读时打分会和“已过期仍可被召回”相互打架
- 交叉编码器和 MMR 会浪费在脏候选上

---

## 3. 能力逐项重判

## 3.1 向量 + BM25 混合检索：从“建议”提升为“应优先落地”

### 修正版判断

建议引入，而且应作为当前检索升级的第一主线。

### 原因

当前 VMM 的长期记忆里有大量非常适合 lexical recall 的内容：

- API 名
- 配置键
- 文件名
- 类名 / 函数名
- 英文短 token
- 版本号
- 特定错误文案

纯向量检索在这些场景下不稳定，而 `SQLite FTS5` 正好可以在当前默认持久化路径里无缝接入。

### 具体建议

新增 FTS 表，不把 BM25 强塞进 `VectorStore`：

```sql
CREATE VIRTUAL TABLE IF NOT EXISTS vmm_memory_nodes_fts USING fts5(
  memory_id UNINDEXED,
  abstract,
  details,
  tokenize = 'unicode61'
);
```

不建议第一版就做：

- `LIKE '%xxx%'`
- 词法检索放进 LanceDB
- 先做 DuckDB 版本对齐

## 3.2 RRF：维持“默认融合算法”结论，但需要落到接口和返回字段

### 修正版判断

维持旧结论，仍然推荐作为默认融合算法。

### 推荐原因

当前项目里：

- 向量检索返回的是 `distance -> score`
- BM25 返回的是 lexical relevance

这两个分数不能直接线性混加。  
RRF 只依赖名次，非常适合作为当前项目第一版融合策略。

### 推荐参数

- `vector_top_k = 24`
- `lexical_top_k = 24`
- `candidate_pool_k = 48`
- `rrf_k = 60`

## 3.3 交叉编码器重排序：从“先不建议”修正为“第二阶段推荐”

### 修正版判断

建议引入，但不是第一阶段；应放在 **Hybrid + RRF 稳定后** 的第二阶段。

### 为什么要改判

旧版把它放得太保守了。  
你补充的多 provider 方向是成立的，只要接受新增 provider 抽象：

- `Jina`
- `TEI`
- `SiliconFlow`

那么 cross-encoder rerank 就不再是“远期想法”，而是 **非常自然的 phase 2**。

### 但为什么仍不建议直接第一步上

因为当前仓库还没有：

- `RerankerClient` 端口
- provider 抽象
- 降级策略
- top-N rerank 裁剪链

所以正确判断应是：

- 不是“不建议做”
- 而是“**不建议跳过 Hybrid/RRF 直接先做**”

## 3.4 MMR：新增为正式推荐项

### 修正版判断

建议引入，位置放在：

1. `RRF` 之后
2. 如果启用 cross-encoder，则放在 `rerank` 之后
3. 放在给 `PreCheck` 第二层 reviewer 编号之前

### 为什么它对当前 VMM 很重要

当前 `PreCheckUseCase.searchMemoryCandidates(...)` 的候选合并方式是：

- 以 `memory_id` 去重
- 按 `score` 排序
- 截断成 `ReviewCandidateLimit`

这会导致一个现实问题：

- 如果 top results 全是同类近似记忆
- 第二层 reviewer 看到的候选其实信息冗余很高

`MMR` 非常适合解决这个问题。

### 第一版怎么落

第一版不需要改表，直接复用当前 `vmm_memory_nodes.vector_json`：

- `LoadMemoryNodesByVectorIDs(...)` 本来就能回出 `Vector`
- 在融合结果 materialize 后即可做候选之间的相似度计算
- 最终只对小候选池做 MMR，计算成本可控

### 推荐参数

- `mmr_lambda = 0.70 ~ 0.80`
- `mmr_pool_k = 20 ~ 40`
- `final_precheck_k = ReviewCandidateLimit`

## 3.5 Weibull：继续建议引入，但保留硬过期边界

### 修正版判断

维持旧结论：  
可以引入，但 **第一阶段不要直接废掉 `expires_timestamp`**。

### 修正版补充

Weibull 更符合实际记忆规律，这点判断成立。  
但 VMM 当前 memory 生命周期更新是“被采纳才写回”，而不是“只要被召回就刷新”，所以更稳妥的做法是：

- 先让 Weibull 参与排序分
- 再让它逐步接管生命周期决策
- 最后才考虑弱化固定 TTL

### 推荐公式

建议把读时衰减分设计成：

```text
decay_score = exp(-((effective_age / eta) ^ beta))
```

其中：

- `effective_age = now - max(created_timestamp, last_reinforced_timestamp, last_adopted_timestamp)`
- `eta` 由 `memory_level + priority + scope_level + reinforcement_count` 联合决定
- `beta` 随等级提高而变大

## 3.6 三层晋升机制：建议复用现有 `memory_level`

### 修正版判断

可以做，但不要新增第二套 `tier` 枚举与字段。

### 推荐映射

| 目标语义 | 当前建议映射 |
| --- | --- |
| Peripheral | `L0(Session)` + `L1(Phase)` |
| Working | `L2(Stable)` |
| Core | `L3(Persistent)` |

### 原因

当前仓库已经有：

- `MemoryLevelSession`
- `MemoryLevelPhase`
- `MemoryLevelStable`
- `MemoryLevelPersistent`

如果再增加一套：

- `peripheral / working / core`

会导致：

- 配置重复
- prompt 重复
- 排序逻辑重复
- 接口字段重复

因此正确做法是：

- 保留 `memory_level`
- 在文档和配置层把它解释成三层晋升模型

## 3.7 访问强化：建议定义成“强化”而不是“曝光”

### 修正版判断

可以引入，但不建议把每次召回命中都视作强化。

### 原因

当前 `PreCheck` 只有在第二层 reviewer 真的采纳某条记忆后，才会调用：

- `ApplyMemoryAdoption(...)`

这个语义比“看见一次候选就算访问”更可靠。

### 修正版建议

把访问强化定义成更稳的“强化事件”：

- 被第二层采纳
- 被 direct-write 明确刷新
- 被人工确认保留
- 后续如果有工具反馈，也可记为正强化

而不是：

- 每次进入 topK 就算一次强化

---

## 4. 情境化记忆：建议引入，但必须新增数据结构

## 4.1 修正版判断

情境化记忆是有价值的，但这不是“小改配置”能完成的事。  
它需要新增结构化上下文关联层。

## 4.2 推荐设计：新增上下文关系表

推荐新增表：

```sql
CREATE TABLE IF NOT EXISTS vmm_memory_context_edges (
  memory_id BIGINT NOT NULL,
  context_key TEXT NOT NULL,
  context_value TEXT NOT NULL,
  support_count INTEGER NOT NULL DEFAULT 0,
  rebuttal_count INTEGER NOT NULL DEFAULT 0,
  last_supported_timestamp BIGINT NOT NULL DEFAULT 0,
  last_rebutted_timestamp BIGINT NOT NULL DEFAULT 0,
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL,
  PRIMARY KEY (memory_id, context_key, context_value)
);

CREATE INDEX IF NOT EXISTS idx_vmm_memory_context_edges_lookup
ON vmm_memory_context_edges(context_key, context_value, memory_id);

CREATE INDEX IF NOT EXISTS idx_vmm_memory_context_edges_memory
ON vmm_memory_context_edges(memory_id);
```

### 为什么不建议第一版只加一个 `context_json`

因为后续需要做：

- 条件过滤
- 支持/反驳聚合
- 上下文命中排序

如果只塞 JSON：

- 过滤难做
- 索引难做
- 统计难做

关系表更适合当前项目。

## 4.3 建议给主表加聚合字段

在 `vmm_memory_nodes` 新增：

```sql
ALTER TABLE vmm_memory_nodes ADD COLUMN support_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE vmm_memory_nodes ADD COLUMN rebuttal_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE vmm_memory_nodes ADD COLUMN last_reinforced_timestamp BIGINT NOT NULL DEFAULT 0;
ALTER TABLE vmm_memory_nodes ADD COLUMN reinforcement_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE vmm_memory_nodes ADD COLUMN decay_disabled TINYINT NOT NULL DEFAULT 0;
```

这几个字段的职责分别是：

- `support_count`
  - 该记忆在所有上下文中累计获得支持的次数
- `rebuttal_count`
  - 该记忆在所有上下文中累计被反驳的次数
- `last_reinforced_timestamp`
  - 最后一次被“有效强化”的时间，不等同于普通召回
- `reinforcement_count`
  - 被有效强化的累计次数，用于 Weibull 的 `eta` 调整
- `decay_disabled`
  - 给核心记忆 / 人工锁定记忆留一个硬保护位

---

## 5. 数据表修改建议

## 5.1 `vmm_memory_nodes` 应该怎么改

### 现有表继续保留，不建议重建成新主表

当前主表 `vmm_memory_nodes` 设计方向是对的，不建议推翻。  
建议是在此基础上补字段、补索引、补辅助表。

### 推荐字段新增

```sql
ALTER TABLE vmm_memory_nodes ADD COLUMN support_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE vmm_memory_nodes ADD COLUMN rebuttal_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE vmm_memory_nodes ADD COLUMN last_reinforced_timestamp BIGINT NOT NULL DEFAULT 0;
ALTER TABLE vmm_memory_nodes ADD COLUMN reinforcement_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE vmm_memory_nodes ADD COLUMN decay_disabled TINYINT NOT NULL DEFAULT 0;
```

### 推荐索引调整

把当前：

```sql
CREATE INDEX IF NOT EXISTS idx_vmm_memory_nodes_project_status
ON vmm_memory_nodes(project_id, memory_status, id);
```

改成：

```sql
CREATE INDEX IF NOT EXISTS idx_vmm_memory_nodes_project_status_window
ON vmm_memory_nodes(project_id, memory_status, expires_timestamp, id);
```

新增：

```sql
CREATE INDEX IF NOT EXISTS idx_vmm_memory_nodes_user_project_window
ON vmm_memory_nodes(user_id, project_id, memory_status, expires_timestamp, id);

CREATE INDEX IF NOT EXISTS idx_vmm_memory_nodes_decay_scan
ON vmm_memory_nodes(memory_status, decay_disabled, expires_timestamp, last_reinforced_timestamp, id);
```

### 为什么这样改

- `project_status_window`
  - 给“活跃 + 未过期”记忆扫描提供更合适的组合索引
- `user_project_window`
  - 给 user 级与 project 级混合过滤预留更稳的读路径
- `decay_scan`
  - 给未来 memory 过期收敛任务和 Weibull scan 提供入口

## 5.2 新增 `vmm_memory_nodes_fts`

推荐新增：

```sql
CREATE VIRTUAL TABLE IF NOT EXISTS vmm_memory_nodes_fts USING fts5(
  memory_id UNINDEXED,
  abstract,
  details,
  tokenize = 'unicode61'
);
```

### 同步策略

第一版建议在应用代码里显式维护，不建议一开始就靠 SQLite trigger：

- `ApplyTurnAnalysis(...)` 插入记忆时 upsert FTS
- `CreateDirectMemoryNode(...)` 插入记忆时 upsert FTS
- `SUPERSEDED / DELETED / EXPIRED` 状态变化时删除或更新 FTS 行

## 5.3 新增 `vmm_memory_context_edges`

这个表负责：

- 记忆与情境标签的多对多关系
- 每个情境下的支持/反驳统计
- 情境化检索命中

建议不把这些统计全塞回主表。

## 5.4 语义去重第一版不强制改表

这里和旧报告相比，需要更细化：

- 第一版语义去重 **不强制新增表字段**
- 直接在写前执行“向量相似邻居查重”即可

推荐写前流程：

1. 先走现有 `dedupe_hash` 软幂等
2. 如果未命中，再做同 scope / 同 category 的向量近邻查重
3. 当相似度高于阈值时，不新建 memory，而是刷新已有 memory

如果未来需要“重复合并审计”，再考虑新增：

```sql
ALTER TABLE vmm_memory_nodes ADD COLUMN merged_into_memory_id BIGINT NOT NULL DEFAULT 0;
```

但这不是第一版必须项。

---

## 6. 接口与结构体修改建议

## 6.1 `internal/app/ports/interfaces.go`

### 建议新增 `RerankerClient`

```go
type RerankerDocument struct {
    ID      string
    Text    string
    Meta    map[string]string
}

type RerankerResult struct {
    ID    string
    Score float64
}

type RerankerClient interface {
    Rerank(ctx context.Context, query string, docs []RerankerDocument, topN int) ([]RerankerResult, error)
}
```

### 建议给 `MemoryStore` 增补 lexical / context / decay 能力

建议新增：

```go
SearchLexicalMemory(ctx context.Context, query string, topK int, filter logicdomain.SearchFilter, now time.Time) ([]logicdomain.MemoryLexicalHit, error)
LoadActiveMemoryNodesByVectorIDs(ctx context.Context, vectorIDs []string, now time.Time) ([]logicdomain.MemoryNodeRecord, error)
ReplaceMemoryContextEdges(ctx context.Context, memoryID uint64, edges []logicdomain.MemoryContextEdge) error
ConvergeExpiredMemoryNodes(ctx context.Context, limit int, now time.Time) ([]uint64, error)
```

### 为什么建议把 `LoadMemoryNodesByVectorIDs` 改名

因为修正版里它的职责已经不是“按 vector_id 裸加载”了，而是：

- 只加载 active
- 只加载未过期
- 供检索链使用

因此改成 `LoadActiveMemoryNodesByVectorIDs(...)` 更准确。

## 6.2 `internal/logic/domain/memory.go`

建议新增这些结构：

```go
type MemoryLexicalHit struct {
    MemoryID uint64
    Score    float64
}

type MemoryContextEdge struct {
    MemoryID                uint64
    ContextKey              string
    ContextValue            string
    SupportCount            int
    RebuttalCount           int
    LastSupportedAt         time.Time
    LastRebuttedAt          time.Time
}

type MemoryScoreBreakdown struct {
    VectorScore  float64
    LexicalScore float64
    RRFScore     float64
    RerankScore  float64
    DecayScore   float64
    FinalScore   float64
}
```

并给 `MemorySearchRecord` / `PreCheckMemoryCandidate` 扩展：

- `VectorScore`
- `LexicalScore`
- `RRFScore`
- `RerankScore`
- `DecayScore`
- `SupportCount`
- `RebuttalCount`
- `MatchedContexts`
- `Origin`

## 6.3 `internal/app/usecase/memory_query.go`

建议把当前单函数检索链拆成以下阶段：

1. `searchVectorCandidates(...)`
2. `searchLexicalCandidates(...)`
3. `fuseCandidatesByRRF(...)`
4. `rerankCandidates(...)`
5. `applyMMR(...)`
6. `materializeMemoryCandidates(...)`

这样做的原因：

- 混合检索、rerank、MMR、Weibull 各自职责更清晰
- 降级策略更容易控制
- 单元测试更容易补

## 6.4 `internal/app/usecase/precheck.go`

这里是修正版里最值得改的一处。

当前 `searchMemoryCandidates(...)` 只认：

- `vector_search`
- `hit.Score >= MinSimilarityScore`

修正版建议改为：

1. `Origin` 改成真正反映召回来源
   - `vector_search`
   - `lexical_search`
   - `hybrid_rrf`
   - `hybrid_rrf_rerank`
2. `MinSimilarityScore` 重命名为 `MinCandidateScore` 或 `MinFinalScore`
3. 在送入 reviewer 之前执行 `MMR`
4. 把支持/反驳和命中 context 一并送入 `review_precheck_memory.md`

## 6.5 gRPC `proto` 是否要调整

### 第一阶段

第一阶段可以不调整 `PreCheckRequest`，只调整 `SearchMemoryEvents` 系列即可。  
因为 `PreCheck` 当前已有服务端内置检索流程，不一定要把复杂检索参数暴露给调用方。

### 推荐追加字段

对 `SearchMemoryEventsRequest` 增加可选字段：

```proto
enum RetrievalMode {
  RETRIEVAL_MODE_UNSPECIFIED = 0;
  RETRIEVAL_MODE_VECTOR_ONLY = 1;
  RETRIEVAL_MODE_HYBRID_RRF = 2;
  RETRIEVAL_MODE_HYBRID_RRF_RERANK = 3;
}

message ContextFilter {
  string key = 1;
  string value = 2;
}
```

```proto
message SearchMemoryEventsRequest {
  uint64 user_id = 1;
  uint64 project_id = 2;
  string query_json = 3;
  uint32 top_k = 4;
  RetrievalMode retrieval_mode = 5;
  uint32 candidate_pool_k = 6;
  bool include_debug_scores = 7;
  repeated ContextFilter context_filters = 8;
}
```

对 `MemorySearchHit` 建议追加：

```proto
message MemorySearchHit {
  MemoryRef memory_ref = 1;
  MemoryRef source_ref = 2;
  MemorySourceKind source_kind = 3;
  MemoryScopeLevel scope_level = 4;
  uint64 session_id = 5;
  string abstract = 6;
  string details_preview = 7;
  int32 category = 8;
  double score = 9;

  double vector_score = 10;
  double lexical_score = 11;
  double rrf_score = 12;
  double rerank_score = 13;
  double decay_score = 14;
  double final_score = 15;
  repeated string matched_contexts = 16;
  uint32 support_count = 17;
  uint32 rebuttal_count = 18;
  string origin = 19;
}
```

### `MemoryDetailEntry` 建议补充

```proto
uint32 reinforcement_count = 26;
uint32 support_count = 27;
uint32 rebuttal_count = 28;
int64 last_reinforced_timestamp = 29;
bool decay_disabled = 30;
```

这样可以保持向后兼容，因为都是追加字段。

---

## 7. 配置修改建议

## 7.1 `internal/config/config.go` 应新增的配置组

建议新增：

```go
type RetrievalConfig struct {
    Mode                 string
    VectorTopK           int
    LexicalTopK          int
    CandidatePoolK       int
    RRFK                 int
    MinFinalScore        float64
    EnableMMR            bool
    MMRLambda            float64
    SemanticDedupeTopK   int
    SemanticDedupeScore  float64
}

type RerankConfig struct {
    Enabled    bool
    Provider   string
    Endpoint   string
    APIKey     string
    Model      string
    TopN       int
    Timeout    time.Duration
}

type MemoryDecayConfig struct {
    Enabled                 bool
    Model                   string
    PeripheralScaleHours    int
    WorkingScaleHours       int
    CoreScaleHours          int
    PromotionToWorkingCount int
    PromotionToCoreCount    int
}

type ContextualMemoryConfig struct {
    Enabled          bool
    MaxContextTags   int
    SupportWeight    float64
    RebuttalPenalty  float64
}
```

## 7.2 `PreCheckConfig` 建议改名一项

当前：

- `MinSimilarityScore`

建议改为：

- `MinCandidateScore`
  或
- `MinFinalScore`

因为引入 hybrid / rerank / decay 后，这个分数已经不再是“纯相似度分”。

---

## 8. 多提供商重排序支持建议

## 8.1 建议抽象成单独适配器族

建议新增目录：

- `internal/adapters/outbound/reranker_http/`

目录内可拆：

- `client.go`
- `provider_jina.go`
- `provider_tei.go`
- `provider_siliconflow.go`

### 这样做的好处

- 统一 `RerankerClient` 端口
- 不污染现有 LLM/Embedding 适配器
- 更容易做 provider 级降级与超时控制

## 8.2 推荐调用位置

只对小候选池做 rerank：

- 输入：RRF 后 top `N=20~40`
- 输出：rerank 后 top `N=10~20`
- 再进入 `MMR`

如果 rerank provider 超时或失败：

- 直接回退到 `RRF` 顺序
- 不应让 `PreCheck` 整体失败

---

## 9. Weibull 与三层晋升的具体落地建议

## 9.1 推荐先做“读时分数”，再做“生命周期主模型”

推荐顺序：

1. 保留 `expires_timestamp` 硬边界
2. 在 active + unexpired 集合内叠加 `decay_score`
3. 增加 memory 过期收敛任务
4. 再评估是否弱化固定 TTL

## 9.2 推荐的等级晋升规则

建议直接基于现有 `memory_level`：

- `L0/L1 -> L2`
  - `reinforcement_count >= 2`
  - 或 `cross_session_adopted_count >= 2`
- `L2 -> L3`
  - `reinforcement_count >= 5`
  - 且 `priority in (P0, P1)`
  - 或显式人工写入标记为长期核心

### 配套规则

- `L3` 可设置 `decay_disabled = 1`
- `L0/L1` 使用更短 `eta`
- `L2` 使用中等 `eta`
- `L3` 使用极长 `eta` 或禁衰减

## 9.3 读时最终分建议

建议初版最终分：

```text
final_score =
  0.45 * normalized_rrf +
  0.20 * rerank_score +
  0.15 * decay_score +
  0.10 * support_score +
  0.10 * priority_score
```

如果未启用 rerank：

```text
final_score =
  0.60 * normalized_rrf +
  0.20 * decay_score +
  0.10 * support_score +
  0.10 * priority_score
```

---

## 10. 语义去重与自适应检索的修正版建议

## 10.1 语义去重：保留 `dedupe_hash`，叠加向量近邻

### 修正版判断

这是值得做的，但不能替代当前 `dedupe_hash`。

### 推荐流程

在以下两条链路写前增加语义去重：

- `MemoryUseCase.Write(...)`
- `RelationalStore.ApplyTurnAnalysis(...)`

流程：

1. 先做现有 `dedupe_hash`
2. 未命中时，再做向量近邻搜索
3. 限制在同 `scope_level`、同 `category`、同 `project/user` 范围
4. 当相似度超过阈值时，刷新旧节点而不是创建新节点

### 为什么这样更合理

- 现有 `dedupe_hash` 负责硬幂等
- 语义去重负责“同义复写、近义重复”
- 两者职责不同，不能互相替代

## 10.2 自适应检索：建议增强，而不是重做

### 修正版判断

当前已经部分实现，所以建议做三项增强：

1. 第一层 intent extractor 输出 `retrieval_mode_hint`
   - `vector_only`
   - `hybrid`
   - `skip`
2. 当 query 含明显代码 token / 配置键 / 文件名时，优先启用 hybrid
3. 当最近 turn 已足够解释问题时，继续允许 `need_memory = false`

这意味着：

- 不是重建 PreCheck
- 而是增强 `extract_intent.md` 和 `IntentResult`

---

## 11. 推荐实施顺序（修正版）

## 11.1 阶段 0：先修现有缺陷

必须先做：

1. `LoadMemoryNodesByVectorIDs(...)` 增加 active + unexpired 过滤
2. `LoadActiveSessionMemoryNodes(...)` 增加过期过滤
3. `LoadRecentDirectMemoryWrites(...)` 增加过期过滤
4. `PreCheckConfig.MinSimilarityScore` 改名，避免和 hybrid 最终分混淆

## 11.2 阶段 1：混合检索最小闭环

建议做：

1. `SQLite FTS5`
2. `SearchLexicalMemory(...)`
3. `RRF`
4. `SearchMemoryEvents` 返回 score breakdown

## 11.3 阶段 2：高质量排序

建议做：

1. `RerankerClient`
2. `Jina/TEI/SiliconFlow` provider
3. `MMR`
4. `PreCheck reviewer` 输入增强

## 11.4 阶段 3：记忆生命周期升级

建议做：

1. `last_reinforced_timestamp / reinforcement_count`
2. `Weibull decay_score`
3. memory 过期收敛任务
4. 基于 `memory_level` 的三层晋升

## 11.5 阶段 4：情境化记忆

建议做：

1. `vmm_memory_context_edges`
2. context-aware filtering
3. support/rebuttal scoring
4. prompt 与详情接口补充

---

## 12. 最终修正版结论

结合你新增的方案，修正版最终结论如下：

1. 旧报告关于 `混合检索 + RRF` 的方向是对的，而且现在应进一步明确为 **优先级最高的落地方向**。
2. 旧报告对 `交叉编码器 rerank` 的判断过于保守，修正后应定义为 **phase 2 正式推荐项**，前提是补 `RerankerClient` 抽象。
3. `MMR` 是旧报告缺失但非常值得补上的能力，尤其适合当前 `PreCheck` 第二层候选筛选。
4. `Weibull` 仍然建议引入，但第一阶段继续保留 `expires_timestamp` 硬边界，这一点不改。
5. `三层晋升` 不建议另起炉灶，应直接复用现有 `memory_level(L0-L3)`。
6. `情境化记忆 + 支持/反驳统计` 是成立的，但这已经属于 **需要新增关系结构、接口字段和排序链的中期升级**。
7. `语义去重` 应作为 `dedupe_hash` 的增强层，不应替代现有硬幂等。
8. `自适应检索` 当前已经存在雏形，后续应增强，而不是从零设计。

## 13. 一个必须继续保留的现实提醒

当前 `SQLite` schema 版本机制仍然是：

- `currentSchemaVersion` 变更
- 触发 `resetCurrentSchema()`
- 默认重建受管表

这意味着：

- 只要你真的开始加上述字段和表
- 在当前实现下就存在清数据风险

所以真正落地这些改造前，建议先决定以下二选一：

1. 接受“开发期升级清库”的现状
2. 先补一个真正的 schema migration 机制，再做上述表结构升级

如果不先明确这一点，后面的表结构设计即便正确，实际落地也会有运行风险。
