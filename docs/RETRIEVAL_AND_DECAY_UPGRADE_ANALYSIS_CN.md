# 混合检索、RRF 与 Weibull 衰减升级分析

更新时间：2026-04-02  
分析范围：基于当前仓库真实实现，评估以下能力是否适合引入，并给出细化到表结构、接口、配置与代码位置的改造建议：

- 混合检索：向量 + BM25
- RRF 融合与重排序
- Weibull 记忆衰减模型

## 1. 结论摘要

### 1.1 是否建议引入

| 能力 | 是否可引入 | 是否建议引入 | 建议优先级 | 结论 |
| --- | --- | --- | --- | --- |
| 向量 + BM25 混合检索 | 可以 | 建议 | 高 | 当前收益最大，和现有架构契合度最高 |
| RRF 融合 | 可以 | 强烈建议 | 高 | 低风险、高收益，适合做混合检索的默认融合算法 |
| 二阶段重排序 | 可以 | 建议，但分阶段 | 中 | 先做“规则/特征重排”，后做“模型重排” |
| Weibull 记忆衰减 | 可以 | 建议，但不要一步替换现有 TTL | 中高 | 先作为读时打分层，再逐步演进到生命周期主模型 |

### 1.2 推荐实施顺序

推荐按 4 个阶段推进：

1. 先补当前检索链的“活跃/未过期”过滤缺口
2. 再做 `SQLite FTS5 + BM25 + RRF`
3. 再做“特征型重排序”
4. 最后再引入 `Weibull` 衰减评分与 memory 过期收敛

### 1.3 不建议的做法

- 不建议第一步就上“模型级 reranker”
  - 当前项目只有 `OpenAI-compatible LLM` 与 `Embedding` 端口，没有专门的 reranker 端口
  - 直接用聊天模型做 rerank，成本高、延迟高、稳定性一般
- 不建议第一步就让 `Weibull` 完全替换 `expires_timestamp`
  - 当前 memory 侧没有完整的“过期收敛”维护流程
  - 应先把 `Weibull` 作为检索打分层，而不是立刻替换硬过期边界
- 不建议在历史兼容 provider 上继续追求双 provider 完整对齐
  - 旧兼容 provider 的基线 schema 曾明显落后于 `SQLite`
  - 当前主线已经只保留 `SQLite` 主路径

## 2. 当前实现现状

## 2.1 当前检索链

当前 `MemoryUseCase.Search(...)` 的链路是：

1. 解析 `query_json`
2. 对每个 query 做 embedding
3. 调 `VectorStore.Search(...)`
4. 用 `LoadMemoryNodesByVectorIDs(...)` 从 SQL 回表补全
5. 按向量分数排序返回

也就是说，当前是“纯向量单通道”，没有：

- BM25 / 关键词检索
- 多通道融合
- rerank
- decay score

## 2.2 当前排序与分数语义

当前返回给上层的 `score` 本质是向量距离转出来的相似度分数：

- LanceDB 返回距离
- `distanceToScore(distance) = 1 / (1 + distance)`
- 分数越高越靠前

这意味着当前 `score` 是“单一向量相似度分”，不是融合分，也不是最终业务分。

## 2.3 当前记忆生命周期

当前 memory 侧主要依赖：

- `scope_level`
- `memory_level`
- `priority`
- `refresh_weight`
- `expires_timestamp`
- `last_adopted_timestamp`
- `adopted_count`
- `cross_session_adopted_count`

当前行为：

- 新 memory 默认按 scope 给固定 TTL
  - `SESSION = 15d`
  - `PROJECT = 180d`
  - `USER = 365d`
- `PreCheck` 采纳时：
  - `recalled_count++`
  - `adopted_count++`
  - `refresh_weight++`
  - 若 session 级记忆跨 session 采纳达到 2 次，则提升到 project 级
  - 过期时间只做“延长”，不会缩短

## 2.4 当前的关键缺口

### 检索缺口

- 纯向量对“精确术语 / API 名 / 配置键 / 短 token / 英文标识符”不稳定
- 没有 lexical 通道，对“用户明明记得关键词但 semantic 没召回来”的场景不友好

### 排序缺口

- 没有融合机制
- 没有区分：
  - semantic 强相关
  - lexical 强匹配
  - 新鲜度高
  - 已多次采纳
  - 高优先级规则

### 生命周期缺口

- memory 侧没有与 profile 对等的“过期收敛 worker”
- `expires_timestamp` 目前更像“静态 TTL 字段”，还不是一套完整的主动衰减系统

### 数据过滤缺口

当前这些查询没有显式排除“已过期” memory：

- `LoadMemoryNodesByVectorIDs(...)`
- `LoadActiveSessionMemoryNodes(...)`
- `LoadRecentDirectMemoryWrites(...)`

这会成为后续混合检索和 Weibull 的前置风险。

## 3. 能力逐项评估

## 3.1 混合检索：向量 + BM25

### 可行性结论

可以，而且很适合当前项目。

原因：

- 当前长期事实主表已经统一为 `vmm_memory_nodes`
- 向量检索已经独立在 `LanceDB`
- 关系存储已经承担 metadata 回表
- lexical 检索最自然的落点就是当前 `SQLite` 关系层

### 最适合的实现方式

推荐方案：

- 保持 `LanceDB` 负责向量召回
- 在 `SQLite` 增加 `FTS5` 索引表，承接 `BM25`
- 在 `MemoryUseCase.Search(...)` 中做双通道召回与融合

不推荐方案：

- 把 BM25 强塞进 `VectorStore`
- 用 `LIKE '%xxx%'` 假装 lexical search
- 首先在 `SQLite` 实现 lexical 检索

### 推荐为什么是 `SQLite FTS5`

因为当前项目：

- 本地默认 `relational.provider = sqlite`
- 历史兼容 provider 的 schema 曾明显落后
- `SQLite` 适合本地轻量全文检索
- 代码里关系读写已经高度集中在 `vldb_sqlite/store.go`

## 3.2 RRF 融合

### 可行性结论

可以，而且应作为混合检索的默认融合算法。

推荐理由：

- 向量分与 BM25 分不可直接线性比较
- RRF 只依赖“名次”，不依赖不同检索器的分值尺度兼容
- 对当前项目非常适合做增量接入

推荐公式：

```text
RRF(doc) = Σ 1 / (k + rank_i)
```

推荐默认值：

- `k = 60`
- `vector_top_k = 24`
- `lexical_top_k = 24`
- `candidate_pool_k = 48`

## 3.3 重排序

### 可行性结论

可以，但应拆成两层：

1. 第一阶段：规则/特征型重排序
2. 第二阶段：模型型 rerank

### 当前阶段建议

当前仓库更适合先做“特征型重排序”：

- 输入：RRF 后的候选集合
- 输出：最终排序分数
- 依赖：现有 memory metadata，不新增模型依赖

推荐初版特征：

- `rrf_score`
- `priority_bonus`
- `scope_bonus`
- `decay_score`
- `cross_session_bonus`
- `adoption_bonus`

建议初版公式：

```text
final_score =
  0.50 * normalized_rrf +
  0.15 * priority_score +
  0.10 * scope_score +
  0.15 * decay_score +
  0.10 * adoption_score
```

### 为什么不建议第一步上模型 rerank

因为当前没有：

- 专门的 `RerankerClient` 端口
- 专门的 rerank provider
- 专门的低延迟 cross-encoder 通道

如果强行用聊天模型 rerank，会带来：

- `PreCheck` 延迟明显变长
- token 成本上升
- 输出稳定性下降

## 3.4 Weibull 记忆衰减

### 可行性结论

可以，但推荐“先作为读时打分层”，不要第一步就替换掉现有 `expires_timestamp`。

### 为什么适合当前项目

当前 memory 已经有足够多的生命周期信号：

- `created_timestamp`
- `last_adopted_timestamp`
- `refresh_weight`
- `adopted_count`
- `cross_session_adopted_count`
- `memory_level`
- `priority`

这些足够支撑一版基于现有字段的 Weibull 衰减评分。

### 推荐落地姿势

第一阶段：

- 保留现有 `expires_timestamp` 作为硬边界
- 新增 `decay_score` 只参与检索排序
- 不改写当前写入逻辑

第二阶段：

- 补齐 memory 过期收敛
- 再考虑让 `Weibull` 成为主生命周期模型

### 推荐初版思路

对每条 memory 计算：

```text
age = now - reinforced_at
decay_score = exp(- (age / lambda_eff) ^ k )
```

其中：

- `reinforced_at = max(created_at, last_adopted_at)`
- `k` 由 `memory_level` 决定
- `lambda_eff` 由 `memory_level + priority + refresh_weight + adopted_count + cross_session_adopted_count` 决定

推荐初版参数方向：

- `L0`: 高遗忘速度
- `L1`: 中高遗忘速度
- `L2`: 中低遗忘速度
- `L3`: 不参与 Weibull，直接视为 decay pinned

## 4. 必须先做的前置修正

在引入三项能力前，建议先补这 3 个点：

### 4.1 把 vector 回表补全改成“只返回 active 且未过期”

当前方法：

- `LoadMemoryNodesByVectorIDs(...)`

当前查询：

```sql
FROM vmm_memory_nodes
WHERE vector_id IN (...)
```

建议改成新增方法，而不是直接复用旧 detail 语义：

- 新增：`LoadActiveMemoryNodesByVectorIDs(ctx, vectorIDs, now)`

建议查询条件：

```sql
FROM vmm_memory_nodes
WHERE vector_id IN (...)
  AND memory_status = ACTIVE
  AND (expires_timestamp <= 0 OR expires_timestamp > now)
```

### 4.2 `LoadActiveSessionMemoryNodes(...)` 也要过滤过期

建议从：

```sql
WHERE origin_session_id = ?
  AND memory_status = ACTIVE
```

改成：

```sql
WHERE origin_session_id = ?
  AND memory_status = ACTIVE
  AND (expires_timestamp <= 0 OR expires_timestamp > now)
```

### 4.3 `LoadRecentDirectMemoryWrites(...)` 也要过滤过期

否则 turn analyzer 会把已经过期的 direct write 误当成“排斥区”。

## 5. 数据表修改建议

## 5.1 推荐主方案

### 修改表：`vmm_memory_nodes`

推荐新增字段：

```sql
ALTER TABLE vmm_memory_nodes ADD COLUMN last_reinforced_timestamp BIGINT NOT NULL DEFAULT 0;
ALTER TABLE vmm_memory_nodes ADD COLUMN decay_disabled TINYINT NOT NULL DEFAULT 0;
```

字段用途：

- `last_reinforced_timestamp`
  - 明确记录“最近一次强化/续命”的时间
  - 不再混用 `updated_timestamp`
  - 便于 Weibull 基于统一锚点计算年龄
- `decay_disabled`
  - 允许 `P0/L3` 或未来系统种子记忆跳过衰减
  - 避免把“强规则”混进通用衰减曲线

### 新增表：`vmm_memory_nodes_fts`

推荐新增一个独立 lexical index 表，而不是直接依赖 `vmm_memory_nodes.id` 的 rowid 语义。

推荐 DDL：

```sql
CREATE VIRTUAL TABLE IF NOT EXISTS vmm_memory_nodes_fts USING fts5(
  memory_id UNINDEXED,
  abstract,
  details,
  tokenize = 'unicode61 remove_diacritics 2'
);
```

推荐这么做的原因：

- 当前 `vmm_memory_nodes.id` 是 `BIGINT PRIMARY KEY`，不建议把它强绑定到 SQLite rowid 语义
- 独立 `memory_id UNINDEXED` 更稳
- lexical 表只承载文本，不承载 scope metadata
- project/user/scope/status 过滤继续从 `vmm_memory_nodes` 主表做 join 过滤

### 修改索引：替换旧 retrieval 主索引

当前索引：

```sql
CREATE INDEX IF NOT EXISTS idx_vmm_memory_nodes_project_status
ON vmm_memory_nodes(project_id, memory_status, id);
```

建议改成：

```sql
CREATE INDEX IF NOT EXISTS idx_vmm_memory_nodes_project_status_window
ON vmm_memory_nodes(project_id, memory_status, expires_timestamp, id);
```

原因：

- 后续 retrieval 查询一定会带 `memory_status + expires_timestamp`
- 旧索引无法覆盖“活跃窗口”过滤

### 新增索引：按 user/project 窗口过滤

建议新增：

```sql
CREATE INDEX IF NOT EXISTS idx_vmm_memory_nodes_user_project_window
ON vmm_memory_nodes(user_id, project_id, memory_status, expires_timestamp, id);
```

用途：

- 支撑当前检索过滤中的 user + project 组合
- 方便 lexical join 后快速过滤候选

### 新增索引：衰减扫描

建议新增：

```sql
CREATE INDEX IF NOT EXISTS idx_vmm_memory_nodes_decay_scan
ON vmm_memory_nodes(memory_status, decay_disabled, expires_timestamp, last_reinforced_timestamp, id);
```

用途：

- 为未来 memory 过期收敛与 decay 评估做扫描优化

## 5.2 最小改动方案

如果只想先做一版可跑的混合检索：

- `vmm_memory_nodes` 不加新字段
- 只新增：
  - `vmm_memory_nodes_fts`
  - `idx_vmm_memory_nodes_project_status_window`
  - `idx_vmm_memory_nodes_user_project_window`

而 `Weibull` 第一版直接用现有字段推导：

- `reinforced_at = max(created_at, last_adopted_at)`
- 不强依赖 `last_reinforced_timestamp`

## 5.3 当前 schema 演进机制的风险

当前 `SQLite` 适配器的 schema 机制是：

- `currentSchemaVersion` 变更
- 触发 `resetCurrentSchema(...)`
- 直接 drop 受管表并重建

也就是说，如果你按当前风格把 schema 从 `10 -> 11`：

- 会触发本地数据重置

所以要分两种情况：

### 如果接受“本地数据清空”

可以直接：

- `currentSchemaVersion: 10 -> 11`
- 修改 `currentSchemaSQL`
- 修改 `resetManagedSchemaSQL`

### 如果不能接受“数据清空”

要先补真正的 migration 机制，再上表结构升级。

## 6. 代码改造建议

## 6.1 `internal/config/config.go`

建议新增：

```go
type RetrievalConfig struct {
    Mode            string `json:"mode"`              // vector | hybrid
    VectorTopK      int    `json:"vector_top_k"`
    LexicalTopK     int    `json:"lexical_top_k"`
    CandidatePoolK  int    `json:"candidate_pool_k"`
    FusionMethod    string `json:"fusion_method"`     // rrf
    RRFK            int    `json:"rrf_k"`
    RerankEnabled   bool   `json:"rerank_enabled"`
    RerankMethod    string `json:"rerank_method"`     // feature | model
    RerankTopN      int    `json:"rerank_top_n"`
}

type MemoryDecayConfig struct {
    Enabled              bool    `json:"enabled"`
    Model                string  `json:"model"` // ttl | weibull
    SessionShape         float64 `json:"session_shape"`
    SessionScaleHours    float64 `json:"session_scale_hours"`
    PhaseShape           float64 `json:"phase_shape"`
    PhaseScaleHours      float64 `json:"phase_scale_hours"`
    StableShape          float64 `json:"stable_shape"`
    StableScaleHours     float64 `json:"stable_scale_hours"`
    PriorityP0Boost      float64 `json:"priority_p0_boost"`
    PriorityP1Boost      float64 `json:"priority_p1_boost"`
    RefreshWeightBoost   float64 `json:"refresh_weight_boost"`
    AdoptionCountBoost   float64 `json:"adoption_count_boost"`
    CrossSessionBoost    float64 `json:"cross_session_boost"`
}
```

建议新增到根配置：

- `Config.Retrieval`
- `Config.MemoryDecay`

建议默认值：

```json
"retrieval": {
  "mode": "vector",
  "vector_top_k": 24,
  "lexical_top_k": 24,
  "candidate_pool_k": 48,
  "fusion_method": "rrf",
  "rrf_k": 60,
  "rerank_enabled": false,
  "rerank_method": "feature",
  "rerank_top_n": 12
},
"memory_decay": {
  "enabled": false,
  "model": "ttl",
  "session_shape": 1.8,
  "session_scale_hours": 48,
  "phase_shape": 1.5,
  "phase_scale_hours": 720,
  "stable_shape": 1.2,
  "stable_scale_hours": 4320,
  "priority_p0_boost": 2.0,
  "priority_p1_boost": 1.3,
  "refresh_weight_boost": 0.15,
  "adoption_count_boost": 0.20,
  "cross_session_boost": 0.35
}
```

### 配置校验建议

新增校验：

- `retrieval.mode in [vector, hybrid]`
- `fusion_method in [rrf]`
- `rerank_method in [feature, model]`
- `memory_decay.model in [ttl, weibull]`
- 若 `retrieval.mode = hybrid` 且 `relational.provider != sqlite`
  - 建议直接报错，或者明确 fallback 为 vector-only

## 6.2 `internal/logic/domain`

### 建议新增检索打分结构

在 `internal/logic/domain/memory.go` 中新增：

```go
type LexicalMemoryHit struct {
    MemoryID uint64
    Rank     int
    Score    float64
}

type MemoryScoreBreakdown struct {
    VectorScore float64
    VectorRank  int
    LexicalScore float64
    LexicalRank int
    FusionScore float64
    RerankScore float64
    DecayScore  float64
}
```

并把 `MemorySearchRecord` 扩展为带 breakdown。

### 建议扩展 `PreCheckMemoryCandidate`

当前：

- `Score`
- `Origin`

建议改成：

- 保留 `Score` 作为最终分
- `Origin` 从单字符串升级为“融合来源说明”
- 新增：
  - `VectorScore`
  - `LexicalScore`
  - `FusionScore`
  - `DecayScore`
  - `MatchedChannels []string`

## 6.3 `internal/app/ports/interfaces.go`

### `MemoryStore`

建议新增：

```go
SearchLexicalMemory(ctx context.Context, query string, topK int, filter logicdomain.SearchFilter, now time.Time) ([]logicdomain.LexicalMemoryHit, error)
LoadActiveMemoryNodesByVectorIDs(ctx context.Context, vectorIDs []string, now time.Time) ([]logicdomain.MemoryNodeRecord, error)
ConvergeExpiredMemoryNodes(ctx context.Context, now time.Time, limit int) ([]logicdomain.MemoryNodeRecord, error)
```

说明：

- `SearchLexicalMemory(...)`：供 `MemoryUseCase.Search(...)` 调 lexical 通道
- `LoadActiveMemoryNodesByVectorIDs(...)`：替代现在不带 active/expiry 过滤的版本
- `ConvergeExpiredMemoryNodes(...)`：供未来 worker 做 memory 过期收敛

### `RelationalStore`

如果你希望 memory 过期收敛走和 profile 一样的维护 worker，建议也把：

```go
ConvergeExpiredMemoryNodes(ctx context.Context, limit int) ([]logicdomain.MemoryNodeRecord, error)
```

加入 `RelationalStore`

## 6.4 `internal/adapters/outbound/vldb_sqlite/store.go`

这是这次改造的主战场。

### 建议新增方法

- `SearchLexicalMemory(...)`
- `LoadActiveMemoryNodesByVectorIDs(...)`
- `ConvergeExpiredMemoryNodes(...)`
- `insertMemoryFTSRows(...)`
- `deleteMemoryFTSRows(...)`

### 建议修改的现有方法

#### `ApplyTurnAnalysis(...)`

当前功能：

- 插 memory rows
- 插 profile rows
- supersede memory

建议新增：

- 对每个新 memory row 同步插入 `vmm_memory_nodes_fts`
- 对 superseded memory 同步删除 `vmm_memory_nodes_fts`

#### `CreateDirectMemoryNode(...)`

当前功能：

- 插 unified direct-write memory row

建议新增：

- 插入成功后同步维护 `vmm_memory_nodes_fts`

#### `buildMemoryNodesSupersedeSQL(...)`

当前只做：

```sql
UPDATE vmm_memory_nodes
SET memory_status = SUPERSEDED
WHERE ...
```

建议额外补：

```sql
DELETE FROM vmm_memory_nodes_fts
WHERE memory_id IN (...);
```

#### `LoadMemoryNodesByVectorIDs(...)`

不建议继续复用作“检索时回表补全”。

建议保留它给详情用途，并新增：

- `LoadActiveMemoryNodesByVectorIDs(...)`

专供检索链使用。

### 建议新增 lexical 查询 SQL

推荐逻辑：

```sql
SELECT
  CAST(f.memory_id AS BIGINT) AS memory_id,
  bm25(vmm_memory_nodes_fts, 5.0, 1.0) AS lexical_score
FROM vmm_memory_nodes_fts f
JOIN vmm_memory_nodes m ON m.id = CAST(f.memory_id AS BIGINT)
WHERE vmm_memory_nodes_fts MATCH ?
  AND m.memory_status = ACTIVE
  AND (m.expires_timestamp <= 0 OR m.expires_timestamp > ?)
  AND m.project_id = ?
  AND (m.user_id = 0 OR m.user_id = ?)
ORDER BY lexical_score
LIMIT ?;
```

说明：

- `abstract` 权重建议高于 `details`
- 不直接把 BM25 原始分数和 vector 分数线性相加
- lexical 只返回“名次”和候选集合，真正融合用 RRF

## 6.5 `internal/app/usecase/memory_query.go`

这是第二主战场。

### 当前 `Search(...)` 建议拆成 5 段

1. `searchVectorCandidates(...)`
2. `searchLexicalCandidates(...)`
3. `fuseCandidatesByRRF(...)`
4. `rerankHybridCandidates(...)`
5. `materializeSearchResponse(...)`

### 推荐新流程

#### 第一步：vector candidate pool

- 仍按当前逻辑 embedding
- 但不直接作为最终结果
- 每个 query item 取 `vector_top_k`

#### 第二步：lexical candidate pool

- 对同一个 query item 做 SQLite FTS/BM25
- 每个 query item 取 `lexical_top_k`

#### 第三步：RRF 融合

按 `memory_id` 合并两个候选池：

```text
rrf_score = 1/(k + vector_rank) + 1/(k + lexical_rank)
```

#### 第四步：特征重排序

在融合结果上叠加：

- `priority_score`
- `scope_score`
- `decay_score`
- `adoption_score`

#### 第五步：最终 response

- 只返回 final top_k
- `score` 表示最终 fused/reranked 分

### 建议改动点

当前函数：

- `mapSearchHits(...)`

建议改成：

- `mapHybridCandidates(...)`

由它来统一补全：

- memory row
- turn source
- vector/lexical/fusion/decay breakdown

## 6.6 `internal/app/usecase/precheck.go`

建议保留外部行为不变，但内部更新：

- `searchMemoryCandidates(...)` 不再只吃 vector 结果
- 让 `PreCheck` 直接复用新的 hybrid search 结果
- 让第二层 reviewer 拿到更干净、更稳定的候选集合

### 推荐扩展 reviewer 输入

如果你要让二层 reviewer 更聪明，建议把 candidate 中增加：

- `matched_channels`
- `fusion_score`
- `decay_score`

这样 prompt 可以显式指导：

- 在相关性接近时，优先采纳“同时被 lexical 与 vector 命中”的事实

## 6.7 `internal/adapters/inbound/grpcapi/proto/v1/vmm.proto`

### `SearchMemoryEventsRequest`

最小改动方案：

- 不改 request
- 全部由服务端配置驱动

推荐增强方案：

```proto
enum RetrievalMode {
  RETRIEVAL_MODE_UNSPECIFIED = 0;
  RETRIEVAL_MODE_AUTO = 1;
  RETRIEVAL_MODE_VECTOR_ONLY = 2;
  RETRIEVAL_MODE_HYBRID = 3;
}
```

并在 `SearchMemoryEventsRequest` 新增：

- `RetrievalMode retrieval_mode = 5;`
- `uint32 candidate_pool_k = 6;`
- `bool include_score_breakdown = 7;`

### `MemorySearchHit`

建议在保留现有 `score` 的前提下新增：

- `double vector_score = 10;`
- `double lexical_score = 11;`
- `double fusion_score = 12;`
- `double rerank_score = 13;`
- `double decay_score = 14;`
- `repeated string matched_channels = 15;`

建议语义：

- `score`：最终分
- 其他字段：诊断分

### `PreCheck` / `PostAction` / `WriteMemories`

不建议改请求结构。

这些接口对混合检索和 Weibull 来说都可以保持外部契约稳定。

## 6.8 Prompt 调整建议

### `configs/prompts/default/review_precheck_memory.md`

建议扩展 reviewer 输入说明：

- 候选可能来自多个检索通道
- 若两个候选都能回答问题，优先选择：
  - 同时被 lexical + vector 支持
  - decay 更健康
  - priority 更高

### 不建议修改的 prompt

- `extract_intent.md`
  - 它负责生成 query，不负责决定融合策略
- `analyze_turn.md`
  - 它属于写入链，不属于 retrieval 主逻辑

## 7. RRF 与重排序的推荐实现细节

## 7.1 RRF

建议直接把 RRF 放在 `MemoryUseCase.Search(...)`，不要下沉到 adapter。

原因：

- adapter 只负责单通道能力
- RRF 属于业务层融合决策
- 更符合当前依赖方向

## 7.2 特征重排序

推荐在用例层实现一个小型 deterministic ranker：

```go
type MemoryRanker interface {
    Rank(now time.Time, query MemoryQueryItem, candidates []HybridMemoryCandidate) []HybridMemoryCandidate
}
```

推荐第一版直接放在 `internal/app/usecase/memory_query.go` 附近，稳定后再抽出来。

## 7.3 模型 rerank 的后续方向

如果以后要上模型 rerank，建议新增独立端口：

```go
type RerankerClient interface {
    Rerank(ctx context.Context, query string, docs []RerankDocument) ([]RerankScore, error)
}
```

而不是复用聊天模型接口。

## 8. Weibull 的推荐实现细节

## 8.1 第一版：只做读时打分

建议新增一个纯函数：

```go
func ComputeMemoryDecayScore(now time.Time, row logicdomain.MemoryNodeRecord, cfg config.MemoryDecayConfig) float64
```

放置位置建议：

- `internal/logic/processor` 不合适
- 更适合：
  - `internal/app/usecase/memory_rank.go`
  - 或 `internal/logic/domain/memory_decay.go`

### 推荐输入

- `created_at`
- `last_adopted_at`
- `refresh_weight`
- `adopted_count`
- `cross_session_adopted_count`
- `priority`
- `memory_level`
- `decay_disabled`

### 推荐输出

- `0 ~ 1` 之间的 decay score

## 8.2 第二版：加入 memory 过期收敛

建议新增维护流程，和 profile 的 `convergeExpiredProfiles()` 对齐：

- `convergeExpiredMemories()`

职责：

1. 找出已过期 active memory
2. 标记 `memory_status = EXPIRED`
3. 删除其 LanceDB vector rows
4. 删除其 FTS rows

建议挂载位置：

- `internal/app/usecase/postaction_queue.go`
- 与 profile convergence 同一个 maintenance ticker 中

## 8.3 `expires_timestamp` 怎么处理

推荐不要废弃。

建议把它保留为：

- 硬删除/硬淘汰边界

而把 `Weibull` 作为：

- 软排序衰减层

这样两层语义分离：

- `decay_score` 决定“现在该不该优先召回”
- `expires_timestamp` 决定“是否已经彻底退出活跃池”

## 9. 推荐实施路径

## 9.1 路线 A：推荐路径

### 阶段 1：检索链前置修正

- 新增 `LoadActiveMemoryNodesByVectorIDs(...)`
- 所有 active memory 查询统一过滤 `expires_timestamp`

### 阶段 2：SQLite 混合检索

- schema `10 -> 11`
- 新增 `vmm_memory_nodes_fts`
- 新增 lexical search
- `MemoryUseCase.Search(...)` 增加双通道候选池

### 阶段 3：RRF + 特征重排序

- 引入 `RRF`
- 引入 deterministic rerank
- proto 响应增加 score breakdown

### 阶段 4：Weibull

- 引入 decay config
- 增加 `last_reinforced_timestamp` / `decay_disabled`
- `Search(...)` 叠加 `decay_score`
- 后续补 `convergeExpiredMemories()`

## 9.2 路线 B：最小改动路径

如果你想最小化工程量：

1. 不改 gRPC 请求
2. 不上模型 rerank
3. 不先动历史兼容 provider
4. 只做：
   - SQLite FTS
   - RRF
   - 基于现有字段的 Weibull read-time score

这是当前最现实、性价比最高的组合。

## 10. 最终建议

### 推荐结论

- 混合检索：建议做
- RRF：建议做，而且应作为默认融合算法
- 重排序：建议先做 deterministic feature rerank，暂不建议第一步做模型 rerank
- Weibull：建议做，但先做“排序层”，不要第一步替换现有 TTL

### 最关键的落地原则

1. 不要把 BM25 塞进 `VectorStore`
2. 不要在旧兼容 provider 上先追求 provider parity
3. 不要直接用聊天模型做第一版 rerank
4. 不要让 `Weibull` 一步替换 `expires_timestamp`
5. 先把 active/unexpired 过滤修正好，再上高级检索与衰减

### 我认为最优的当前方案

基于当前仓库现状，最优方案是：

- `SQLite FTS5` 承接 lexical
- `LanceDB` 保持 semantic
- `MemoryUseCase.Search(...)` 做 `RRF`
- 在用例层做 deterministic rerank
- `Weibull` 先只做读时衰减分
- `expires_timestamp` 继续保留为硬边界

这条路线最符合当前项目的稳定性、可维护性和可回滚性要求。
