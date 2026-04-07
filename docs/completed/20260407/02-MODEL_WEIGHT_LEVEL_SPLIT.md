## 任务目标

本次任务需要将当前 `models` 中“单一权重”的概念拆分为“按调用级别生效的多权重配置”，并保证现有模型配置在未显式声明分级权重时仍保持兼容。

具体目标如下：

1. 将模型权重拆分为 5 个独立槽位：
   - `precheck L1`
   - `precheck L2`
   - `postaction L1`
   - `postaction L2`
   - `reserve`（备用，当前不参与运行时实际决策）
2. 获取 LLM 时支持传入“当前调用级别/调用场景”，并基于该级别选择对应权重。
3. 所有权重槽位的默认值统一为 `100`，即未配置时等价于 `100`。
4. 运行时当前只让前 4 个权重实际参与生效，第 5 个备用权重仅完成配置承载与解析，不接入当前调度逻辑。
5. 保持现有配置尽可能平滑兼容，避免因为未配置新字段而导致模型不可用或调度行为异常。

## 详细执行步骤

1. 梳理当前模型权重配置与调用链
   - 定位 `models` 配置结构、权重字段定义及默认值来源。
   - 核对 precheck 与 postaction 在 L1/L2 两层调用 LLM 的入口。
   - 明确当前“获取 LLM”或“选取模型”时，权重是在何处被读取和生效。

2. 设计新的分级权重配置结构
   - 为单个模型增加 5 个独立权重字段或等价结构。
   - 明确配置解析时的默认值回填策略，确保所有槽位缺省均为 `100`。
   - 保证备用权重可被配置与序列化，但当前不接入运行时选择分支。

3. 改造调用侧按级别取权重
   - 在获取 LLM 或选取模型的入口补充“当前调用级别”参数。
   - 在 precheck L1/L2、postaction L1/L2 对应链路上传递明确的级别标识。
   - 让运行时按级别读取对应权重，而不是继续复用单一权重。

4. 处理兼容性与默认行为
   - 若旧配置仍只有原始单权重字段，需要核对是否保留兼容映射，或统一转为 5 个缺省值。
   - 确保未配置新权重时，实际效果与“所有级别均为 100”一致。
   - 确保备用权重不会误参与当前 precheck/postaction 的任何实际模型决策。

5. 补充测试与必要文档
   - 增加或调整配置解析测试，覆盖“全缺省=100”“部分配置覆盖”“备用槽位不生效”等场景。
   - 增加或调整调用链测试，覆盖 precheck/postaction 在 L1/L2 下会读取不同权重的行为。
   - 如配置契约或使用方式发生变化，同步补充相关中文文档或配置示例。

6. 验证与收尾
   - 运行本次改动要求覆盖的相关 Go 测试。
   - 对照计划逐项自检，确认 5 个槽位、4 个生效、1 个备用、默认 100 均已成立。
   - 在计划文件末尾追加执行变更总结后，将本文件迁移至 `docs/completed/20260407/`。

## 技术选型及实现原则

1. 优先在配置模型与运行时选择层完成分级权重拆分，不把“场景判断”散落到各业务分支中，避免后续继续扩散耦合。
2. 优先通过显式的调用级别枚举或常量表达 precheck/postaction 的 L1/L2 语义，避免继续依赖隐式字符串或上下文约定。
3. 默认值策略必须集中在配置归一化或权重读取入口统一处理，避免不同调用方各自补默认值而产生漂移。
4. 备用权重本次只做结构预留，不额外引入新的调度语义，避免超出当前需求边界。
5. 所有新增代码、函数说明与关键逻辑区域继续遵守仓库现有的中英文双语注释规范。

## 验收标准

1. `models` 配置已经可以表达 5 个独立权重槽位，其中前 4 个分别对应 `precheck L1/L2` 与 `postaction L1/L2`。
2. 获取 LLM 或执行模型选择时，调用方会显式传入当前调用级别，并按该级别读取对应权重。
3. 未配置任意分级权重时，系统默认按 `100` 处理，不会因为缺少新字段导致行为异常。
4. 当前运行时只有前 4 个权重参与实际生效，备用权重不会误影响 precheck/postaction 的选模行为。
5. 相关测试通过，能够覆盖默认值、级别映射与备用权重不生效等核心场景。

## 执行变更总结

### 1. 核心修复与调整概述

- 为 `llm.routes[*]` 增加了 `weights` 五槽位配置，分别对应 `precheck_l1 / precheck_l2 / postaction_l1 / postaction_l2 / reserve`，并把未配置时的默认值统一收敛为 `100`。
- 保留了旧版 `priority` 作为兼容过渡字段：当新 `weights.*` 缺失时，旧 `priority` 会自动铺开到五个槽位；若两者都未配置，则回退到全 `100`。
- 在 `LLMRequest` 中新增“调用层级”字段，并让 `IntentExtractor / PreCheckMemoryReviewer / TurnAnalyzer / PostActionCandidateReviewer` 分别打上对应的 `L1/L2` 标记，使多路由 LLM 客户端能够按当前业务层级动态排序 route。
- 调整了 `selectProcessorPromptModel` 与 `LLMMultiRouteClient`，让提示词锚定模型和运行时 route 选择都改为“按当前层级权重”工作，而不是继续依赖全局单一 `priority`。

### 2. 📂文件变更清单

- 新增
  - 无
- 修改
  - `docs/plan/20260407-02-MODEL_WEIGHT_LEVEL_SPLIT.md`
  - `README.md`
  - `configs/base.yaml`
  - `configs/config.yaml`
  - `configs/openai.config.example.yaml`
  - `configs/google_ai_studio.config.example.yaml`
  - `docs/ai-model-failover-design_CN.md`
  - `internal/logic/ports/llm.go`
  - `internal/app/ports/llm.go`
  - `internal/config/config.go`
  - `internal/app/app.go`
  - `internal/adapters/outbound/ai_key_failover/llm_multi_route.go`
  - `internal/logic/processor/intent_extractor.go`
  - `internal/logic/processor/precheck_memory_reviewer.go`
  - `internal/logic/processor/turn_analyzer.go`
  - `internal/logic/processor/postaction_candidate_reviewer.go`
  - `internal/logic/processor/manual_profile_reviewer.go`
  - `internal/logic/processor/profile_merger.go`
  - `internal/logic/processor/entry_summarizer.go`
  - `internal/config/config_test.go`
  - `internal/app/app_test.go`
  - `internal/adapters/outbound/ai_key_failover/key_failover_test.go`
- 删除
  - 无

### 3. 💻关键代码调整详情

- `internal/config/config.go`
  - 新增 `LLMRouteWeightConfig` 与 `LLMRouteResolvedWeights`，并为 `LLMRouteConfig` 增加 `weights` 字段。
  - 新增 `ResolvedWeights / SelectionWeight / PrimaryRouteForSelection / PrimaryModelForSelection` 等方法，集中处理默认值、旧 `priority` 兼容和按层级选主模型逻辑。
  - 新增分场景权重校验，拒绝负数权重。
- `internal/logic/ports/llm.go` 与 `internal/app/ports/llm.go`
  - 新增 `LLMRouteSelectionLevel` 枚举及 `LLMRequest.RouteSelectionLevel` 字段，让调用侧可以显式声明当前业务层级。
- `internal/adapters/outbound/ai_key_failover/llm_multi_route.go`
  - 用五槽位 `SelectionWeights` 替代全局单一 `Priority`。
  - 路由表构建阶段不再预排序，而是在每次请求进入时，根据 `RouteSelectionLevel` 动态稳定排序，从而保留共享 key failover 状态。
- `internal/app/app.go`
  - 为 precheck/postaction 四条主链分别计算 prompt 锚定模型。
  - 构建 `LLMRouteOptions` 时透传分场景权重。
- `internal/logic/processor/*.go`
  - `IntentExtractor` 打 `precheck_l1`
  - `PreCheckMemoryReviewer` 打 `precheck_l2`
  - `TurnAnalyzer` 打 `postaction_l1`
  - `PostActionCandidateReviewer` 打 `postaction_l2`
  - `ManualProfileReviewer / ProfileMerger / EntrySummarizer` 走 `reserve` 兼容槽位
- 测试与文档
  - 补充默认值、层级选主模型、按层级 route 排序、processor 调用层级透传等测试。
  - 同步更新 README、配置模板和设计文档中的 LLM 选路说明。

### 4. ⚠️遗留问题与注意事项

- 本次明确生效的主链路仍是 `precheck_l1 / precheck_l2 / postaction_l1 / postaction_l2` 四个槽位。
- `reserve` 没有接入 precheck/postaction 主链路，但为了兼容当前仓库里其他非主链 LLM 调用（如手工画像评审与预留处理器），这些调用会回落到 `reserve` 槽位，而不是继续使用旧的全局单权重语义。
- 旧版 `priority` 目前仍可继续使用，但它已经退化为兼容字段；后续若配置全面迁移到 `weights.*`，可以再评估是否彻底移除。
- 本次已执行：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config ./internal/app ./internal/adapters/outbound/ai_key_failover`
  - `go test ./...`
