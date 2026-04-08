# 任务计划：Hard Dedupe 候选池与配置校验修复

## 任务目标

修复上一轮 `hard dedupe pool split` 改动中遗留的两个实现偏差，确保：

1. `memory_pipeline.hard_dedupe_pool_top_k` 不只是结果层裁剪参数，而是真正参与首轮召回窗口扩张。
2. 非法的 `memory_pipeline.hard_dedupe_pool_top_k` 配置值不会在 `Normalize` 阶段被静默改写，而是能在启动校验阶段明确报错。
3. 回归测试能够覆盖上述两个风险点，避免后续再次出现“表面有配置、运行时未生效”的问题。

## 执行步骤

1. 梳理 `MemoryUseCase.Search` 中 `topK`、`candidatePoolK`、`hardDedupePoolTopK` 三者的关系，定位首轮召回窗口没有被 `hard_dedupe_pool_top_k` 拉大的具体原因。
2. 调整候选池计算逻辑，使 hard dedupe 专用窗口在 vector / hybrid / rerank / MMR 路径下都能真正驱动首轮检索扩大，但仍保持现有上限保护。
3. 梳理 `config.Load` -> `Normalize` -> `Validate` 链路，修正 `HardDedupePoolTopK` 的默认补齐与显式非法值校验边界，区分“缺省未配置”和“用户明确给了非法值”。
4. 补充或调整单元测试，至少覆盖：
   - hard dedupe 配置值会把首轮 `vector.Search` 的 `topK` 扩大到预期窗口；
   - 非法 `hard_dedupe_pool_top_k` 在标准加载链路中会触发明确校验错误；
   - 既有功能回归样例继续通过。
5. 执行相关测试并对照计划自检，确认实现、测试与文档契约一致。

## 技术选型与实现约束

- 保持现有 `adapters -> app -> logic/domain` 依赖方向不变。
- 继续复用现有记忆检索流水线，不引入新的 reviewer / dedupe 分支协议。
- 对复杂逻辑和新增测试保持仓库要求的中英文双语注释风格。
- 不通过放宽校验或删减测试来掩盖问题，优先修正真实行为与配置契约。

## 验收标准

1. 当 `hard_dedupe_pool_top_k > topK` 时，首轮检索窗口会至少扩大到该值或其他更大的既有候选池需求值。
2. `HardDedupeHits` 不再受“首轮候选池本身过小”影响而虚设。
3. 显式配置 `hard_dedupe_pool_top_k <= 0` 时，`Load` / `Validate` 会返回错误而不是悄悄回退默认值。
4. 相关单元测试通过，且未破坏当前 `usecase`、`config`、`app` 包测试。

## 执行变更总结

### 1. 核心修复与调整概述

- 已为 `MemoryQueryCommand` 增加仅内部 reviewer 链路使用的 `EnableHardDedupePool` 标记，使 post-action / direct-write 在需要硬排重时，能够真正放大首轮检索窗口，而普通搜索链路保持原有召回规模。
- 已调整 `MemoryUseCase.Search` 的候选池计算逻辑：当启用 hard dedupe 专用窗口时，首轮 `candidatePoolK` 会至少扩大到配置的 `hard_dedupe_pool_top_k`，从而保证 `HardDedupeHits` 不再只是对过小候选集的二次裁剪。
- 已为 `MemoryPipelineConfig` 增加“字段是否显式设置”的跟踪逻辑，并修正 `Normalize` / `Load` / 环境变量覆盖流程，让 `hard_dedupe_pool_top_k <= 0` 的显式配置在标准启动链路中稳定报错，而不是被静默恢复成默认值。

### 2. 文件变更清单

- 新增：
  - `docs/plan/20260408-08-HARD_DEDUPE_POOL_FIX.md`
- 修改：
  - `internal/app/usecase/memory_query.go`
  - `internal/app/usecase/postaction_candidate_review.go`
  - `internal/app/usecase/memory_query_test.go`
  - `internal/config/config.go`
  - `internal/config/config_test.go`
- 删除：
  - 无

### 3. 关键代码调整详情

- `internal/app/usecase/memory_query.go`
  - 新增 `EnableHardDedupePool` 请求标记，仅在内部 dedupe 评审查询时扩张首轮召回窗口。
  - 新增 `configuredHardDedupePoolTopK` 辅助逻辑，把 hard dedupe 配置值纳入首轮 `candidatePoolK` 计算。
  - 保持调用方可见的最终 `Hits` 仍受原始 `TopK` 控制，避免 reviewer 结果集被无意放大。
- `internal/app/usecase/postaction_candidate_review.go`
  - 在构造 post-action / direct-write 去重查询时显式开启 `EnableHardDedupePool`，让修复真正作用到目标链路。
- `internal/config/config.go`
  - 为 `MemoryPipelineConfig` 增加显式字段跟踪，并通过自定义 `UnmarshalJSON` 识别 YAML/JSON 层是否提供了 `hard_dedupe_pool_top_k`。
  - 调整 `Normalize` 默认补齐条件，仅在字段缺失时补默认值。
  - 调整环境变量覆盖逻辑，在 `VMM_MEMORY_HARD_DEDUPE_POOL_TOP_K` 生效时同步标记为显式配置。
- 测试
  - 新增首轮候选池会因 hard dedupe 请求被放大的用例。
  - 新增 YAML 显式非法值与环境变量覆盖非法值的加载失败用例。
  - 补充 direct-write 硬排重链路对扩大搜索窗口的断言。

### 4. 遗留问题与注意事项

- 本轮只修复了 reviewer 前 hard dedupe 候选池与配置校验问题，没有改动更广泛的搜索默认策略；普通 `SearchMemoryEvents` / pre-check 仍保持原有候选池行为。
- `hard_dedupe_pool_top_k` 的“显式配置跟踪”目前只为该字段引入，未推广到其他整数配置项；这是有意保持本轮修复范围收敛。
- 已执行测试：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config ./internal/app`
