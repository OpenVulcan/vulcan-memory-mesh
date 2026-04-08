## 任务目标

为 `PostAction` / `WriteMemories` 的重复记忆判定补充一条“真实向量硬排重”能力，并移除当前用例层对 `0.90` reviewer 入口阈值的硬编码钳制。所有相关阈值必须改为可通过配置文件设置，同时保留明确默认值，便于后续实验和调优。

## 执行步骤

1. 梳理当前相似度阈值的来源与落点，确认：
   - `memory_pipeline.min_similarity_score` 如何传入 `PreCheck`、`PostAction` 和 `WriteMemories`
   - `PostAction` / `WriteMemories` 当前对 `<0.90` 的硬钳制位置
   - 查询向量与召回命中向量在现有链路中的可复用位置
2. 设计并实现新的配置项，要求：
   - `PostAction` / `WriteMemories` 的 reviewer 入口阈值不再写死为 `0.90`
   - 新增“真实向量硬排重阈值”配置项，支持显式关闭或调整
   - 默认值清晰、校验规则明确，并接入配置加载与环境变量映射
3. 调整记忆召回与候选评审链路：
   - 让 `buildScopedMemoryReviewCandidates` 能拿到查询向量与召回命中向量
   - 在送入 LLM reviewer 前，先对每条候选与其召回集合做一次真实 cosine 扫描
   - 当任一命中满足硬排重阈值时，直接将该候选视为重复并跳过后续 reviewer
4. 为硬排重结果补充必要日志与测试，覆盖：
   - 阈值可配置且默认值生效
   - reviewer 入口阈值不再被 `0.90` 强行抬高
   - 高 cosine 命中时可直接复用旧记忆而不进入 reviewer
   - 阈值关闭或未命中时仍保持现有 reviewer 行为
5. 对照计划自检，补充执行变更总结并归档到 `docs/completed/20260408/`。

## 技术选型与策略

- 优先复用现有 `MemoryUseCase.Search` 中已生成的查询 embedding，避免为硬排重再额外发一次 embedding 请求。
- “硬排重”使用真实余弦相似度，不复用当前对外 `Score`，避免被 RRF、排名归一、MMR 等排序因素污染。
- 入口阈值与硬排重阈值都统一纳入 `memory_pipeline` 配置，保持检索相关参数集中管理。
- 默认值采取“安全优先”策略：入口阈值保持较稳的默认值；硬排重阈值采用高置信默认值，先保守拦截最明显的重复。

## 验收标准

1. `PostAction` / `WriteMemories` 不再在用例层把 `<0.90` 的配置强制抬高。
2. 新增的真实 cosine 硬排重阈值可通过配置文件设置，并有明确默认值与合法性校验。
3. 当召回集合中存在真实 cosine 达到硬排重阈值的旧记忆时，新候选可直接视为重复，跳过 LLM reviewer。
4. 当未命中硬排重条件时，系统仍按现有 reviewer 流程工作，不破坏原有输入输出契约。
5. 至少完成配置与 `post-action` / `memory_query` 相关测试；若改动较大，再跑仓库全量测试。

## 执行变更总结

### 1. 核心修复与调整概述

- 将 `PostAction` / `WriteMemories` 的 reviewer 入口阈值从原先的用例层 `0.90` 硬钳制改为配置驱动，统一接入 `memory_pipeline.replace_min_similarity_score`，默认值为 `0.80`。
- 新增 `memory_pipeline.hard_dedupe_cosine_threshold` 配置项，默认值为 `0.985`，用于在 reviewer 前基于“查询向量 vs 全召回命中向量”的真实 cosine 扫描执行硬排重；设为 `0` 时可关闭。
- `buildScopedMemoryReviewCandidates` 现在会保留查询向量并扫描每条召回命中的真实向量，相似度达到硬阈值时直接把对应候选标记为重复，跳过后续 LLM reviewer。
- 为避免新增能力污染未显式配置的轻量场景，`WriteMemories` 仅在应用装配层显式调用 `ConfigureMemoryReplace(...)` 后才启用共享 reviewer / 硬排重链路；裸构造 `NewMemoryUseCase(...)` 继续保持历史默认行为。
- 为调试 rollout 效果，`post-action turn analysis result` 新增了 `hard_dedupe_drop_count` 日志字段，用于区分 reviewer 丢弃与 reviewer 前硬排重丢弃。

### 2. 📂文件变更清单

- 修改：[D:\projects\VulcanMemoryMesh\configs\base.yaml](D:\projects\VulcanMemoryMesh\configs\base.yaml)
- 修改：[D:\projects\VulcanMemoryMesh\internal\config\config.go](D:\projects\VulcanMemoryMesh\internal\config\config.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\config\config_test.go](D:\projects\VulcanMemoryMesh\internal\config\config_test.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\app\app.go](D:\projects\VulcanMemoryMesh\internal\app\app.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\app\usecase\postaction.go](D:\projects\VulcanMemoryMesh\internal\app\usecase\postaction.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\app\usecase\postaction_candidate_review.go](D:\projects\VulcanMemoryMesh\internal\app\usecase\postaction_candidate_review.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\app\usecase\postaction_candidate_review_test.go](D:\projects\VulcanMemoryMesh\internal\app\usecase\postaction_candidate_review_test.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\app\usecase\memory_query.go](D:\projects\VulcanMemoryMesh\internal\app\usecase\memory_query.go)
- 修改：[D:\projects\VulcanMemoryMesh\internal\app\usecase\memory_query_test.go](D:\projects\VulcanMemoryMesh\internal\app\usecase\memory_query_test.go)

### 3. 💻关键代码调整详情

- 配置层：
  - 在 `memory_pipeline` 中新增 `replace_min_similarity_score` 与 `hard_dedupe_cosine_threshold` 两个参数。
  - 完成默认值注入、合法性校验以及环境变量映射，支持后续通过配置文件或环境变量调试阈值。
- 装配层：
  - `app.go` 将新的两个阈值同时传给 `PostActionUseCase` 与 `MemoryUseCase.ConfigureMemoryReplace(...)`，保证 `PreCheck` 仍使用原有通道，而 `PostAction` / `WriteMemories` 共享新的重复记忆策略。
- 检索与评审层：
  - `MemoryUseCase.Search` 现在会在结果组中回传 `QueryVector`，供后续硬排重直接复用，避免重复 embedding。
  - `buildScopedMemoryReviewCandidates` 会扫描全部召回命中，而不是只盯着排序第一条；即使高 cosine 命中不是 top1，也能被识别并直接 dedupe。
  - `reviewTurnCandidates` 与 direct-write reviewer 路径都会先裁掉已硬排重的候选，只把剩余候选送入统一 reviewer。
- 兼容性收口：
  - direct-write 路径新增“显式配置才启用”的保护，修复了默认构造场景额外多跑一次搜索与 embedding 的回归。
  - `post-action` 分析日志新增 `hard_dedupe_drop_count`，便于观察硬排重命中量。

### 4. ⚠️遗留问题与注意事项

- 当前硬排重只在 `PostAction` / `WriteMemories` 的 reviewer 前链路生效，`PreCheck` 仍保持现有“低于阈值不进 reviewer”的过滤模型，没有引入“高相似直接短路”。
- 默认 `hard_dedupe_cosine_threshold=0.985` 采取的是保守策略，适合先拦截最明显的近重复；如果后续观测到仍有大量重复写入，可继续结合日志里的 `hard_dedupe_drop_count` 与实际样本分布做调参。
- 日志里的原有 `score` 依旧是融合排序分，不是原始 cosine；后续调试“直接短路阈值”时应以新增硬排重行为为准，而不要把排序分误当成真实向量相似度。

### 验证结果

- 已通过：`go test ./internal/app/usecase ./internal/config`
- 已通过：`go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
- 已通过：`go test ./...`
