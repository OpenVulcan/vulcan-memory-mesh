# 全局代码审核与检索正确性加固执行计划

## 1. 任务目标

本阶段基于第 15 阶段已经完成的 retention purge 批次元数据清理收口，继续执行新一轮全局代码审核，重点检查统一检索链路、主动写入链路、回收维护链路以及近期多轮修复叠加后的边界一致性，确认是否仍存在会影响功能稳定性、检索正确性、性能或运行时无干扰目标的真实风险，并以最优方案完成修复。

具体目标如下：

1. 复核统一检索、主动写入、回收维护之间的热路径与冷路径边界，确认是否仍存在冷热状态不一致、重复候选、错误降级或后台维护副作用等问题。
2. 仅修复能够通过代码逻辑、测试或运行时语义证明的真实问题，不做猜测式重构。
3. 在不牺牲稳定性、效率和可维护性的前提下，完成必要代码修复与测试补强。
4. 完成回归验证、自检、计划归档、提交与推送，保持工程闭环。

## 2. 审核范围

### 2.1 本轮重点覆盖

1. `internal/app/usecase/memory_query.go`
2. `internal/app/usecase/precheck.go`
3. `internal/app/usecase/retention.go`
4. `internal/adapters/outbound/vldb_sqlite/retention_store.go`
5. `internal/adapters/outbound/vldb_postgres/retention_store.go`

### 2.2 本轮不主动扩展

1. 不引入计划外的大型架构重构。
2. 不修改对外接口定义，除非确认存在阻断级错误且存在低风险最优解。
3. 不做与当前风险无关的风格性清理。

## 3. 执行策略

1. 基于当前主分支最新状态执行定向深审，优先覆盖最近几轮修复叠加后最容易发生语义漂移的检索热路径与维护边界。
2. 若发现真实问题，优先在 usecase 或单一适配器边界内收口，避免扩大改动面。
3. 修复方案必须同时满足：
   - 不破坏既有成功路径契约；
   - 不增加明显额外 provider 往返、锁竞争或后台噪声；
   - 能通过测试长期约束；
   - 便于后续维护者继续理解和演进。

## 4. 详细执行步骤

1. 确认仓库基线并建立本阶段计划文件。
2. 深审统一检索、主动写入与回收维护关键链路，定位真实风险问题。
3. 采用最优方案实施修复，并补齐必要测试。
4. 运行最少必测集、全量测试与必要静态检查。
5. 对照计划逐项自检，补写执行变更总结。
6. 将计划迁移至 `docs/completed/`，完成 `git commit` 与 `git push`。

## 5. 验收标准

1. 必须明确说明本轮定位到的真实问题与修复原因。
2. 修复后不得引入新的检索错误、重复结果、冷热状态不一致或后台维护副作用。
3. 修复不得引入明显额外开销或运行时干扰。
4. 至少完成以下验证：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`

## 6. 风险与注意事项

1. 本轮仍以功能稳定、效率高、性能强、运行时无干扰为最高优先级。
2. 若发现某项原设计应后置或不应继续推进，必须在总结中明确说明原因。
3. 修复必须继续遵守仓库双语注释、测试约束、计划归档和中文提交规范。

## 执行变更总结

### 1. 核心修复与调整概述

1. 本轮全局审核确认了统一检索链路里的一个真实分数语义漏洞：`fuseSearchHitsByRRF`、SQL 混合检索归一和注释都把对外命中分数定义为稳定的 `0..1` 区间，但后续 `rerank`、`context evidence` 加减分以及未来新增后处理阶段并没有统一收口。
2. 这意味着只要 rerank provider 返回越界分数，或者 context 反驳/支持证据把分数继续推高或压低，`SearchMemoryEvents` 和内部 `MemoryQueryResult` 就可能直接暴露 `>1` 或 `<0` 的分数，破坏检索语义稳定性，也会让下游排序解释和阈值心智模型产生漂移。
3. 本轮新增统一的检索后处理分数钳制辅助逻辑，在映射、融合、rerank、Weibull、context scoring、MMR 之后都执行同一条 `0..1` 收口；同时补充 rerank 越界分数与 context 证据溢出两类回归测试，确保这条契约长期稳定。

### 2. 📂文件变更清单

1. 修改：`internal/app/usecase/memory_query.go`
2. 修改：`internal/app/usecase/memory_query_test.go`

### 3. 💻关键代码调整详情

1. 新增 `clampMemoryQueryHitScores`，作为统一检索后处理阶段的分数最终防线。
2. 在 `Search` 主链中，把 `mapSearchHits`、混合召回、rerank、Weibull、context scoring、MMR 之后的命中结果都统一过一遍 `0..1` 钳制，避免任意上游阶段把越界分数泄露到调用方响应。
3. 新增 `TestMemoryUseCaseSearchClampsRerankScores`，验证 rerank provider 返回 `1.70 / -0.20` 这类自定义分数时，最终响应仍会收敛到 `1 / 0`。
4. 新增 `TestMemoryUseCaseSearchClampsContextAdjustedScores`，验证支持或反驳型 context evidence 在高强度场景下也不会把最终命中分数推到统一分数契约之外。

### 4. ⚠️遗留问题与注意事项

1. 本轮没有改变 pre-check 阈值语义本身；它此前已经通过 `normalizePreCheckReviewScore` 做了 reviewer 侧收口，本次修复补的是统一检索主链对外响应的稳定分数契约。
2. 统一钳制现在落在 usecase 层，因此不依赖底层向量检索、SQL 混合检索或 rerank provider “恰好”输出仓库约定分数范围，后续新增排序阶段也更容易被同一条契约兜住。
3. 已完成验证：
   - `go test ./internal/app/usecase -run "TestMemoryUseCaseSearch(AppliesRerank|ClampsRerankScores|AppliesContextAwareScoring|ClampsContextAdjustedScores)"`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`
