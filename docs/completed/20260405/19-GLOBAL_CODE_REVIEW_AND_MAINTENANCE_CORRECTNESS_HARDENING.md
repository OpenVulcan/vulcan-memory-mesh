# 全局代码审核与维护正确性加固执行计划

## 1. 任务目标

本阶段基于第 18 阶段已经完成的 SQLite 采纳回写并发收口，继续执行新一轮全局代码审核，重点检查统一检索、生命周期维护、回收维护、后台工作器以及近期多轮修复叠加后的边界一致性，确认是否仍存在会影响功能稳定性、性能、正确性或运行时无干扰目标的真实风险，并以最优方案完成修复。

具体目标如下：

1. 复核统一检索、生命周期更新、回收维护与后台工作器之间的交界面，确认是否仍存在冷热状态不一致、统计漂移、重复处理、错误排序或后台副作用等问题。
2. 仅修复能够通过代码逻辑、测试或运行时语义证明的真实问题，不做猜测式重构。
3. 在不牺牲稳定性、效率和可维护性的前提下，完成必要代码修复与测试补强。
4. 完成回归验证、自检、计划归档、提交与推送，保持工程闭环。

## 2. 审核范围

### 2.1 本轮重点覆盖

1. `internal/app/usecase/memory_query.go`
2. `internal/app/usecase/retention.go`
3. `internal/adapters/outbound/vldb_sqlite/store.go`
4. `internal/adapters/outbound/vldb_sqlite/retention_store.go`
5. `internal/adapters/outbound/vldb_postgres/analysis_store.go`
6. 与维护工作器、生命周期边界相关的测试与文档

### 2.2 本轮不主动扩展

1. 不引入计划外的大型架构重构。
2. 不修改对外接口定义，除非确认存在阻断级错误且存在低风险最优解。
3. 不做与当前风险无关的风格性清理。

## 3. 执行策略

1. 基于当前主分支最新状态执行定向深审，优先覆盖最近多轮修复叠加后最容易发生语义漂移的维护与生命周期边界。
2. 若发现真实问题，优先在 usecase 或单一适配器边界内收口，避免扩大改动面。
3. 修复方案必须同时满足：
   - 不破坏既有成功路径契约；
   - 不增加明显额外 provider 往返、锁竞争或后台噪声；
   - 能通过测试长期约束；
   - 便于后续维护者继续理解和演进。

## 4. 详细执行步骤

1. 确认仓库基线并建立本阶段计划文件。
2. 深审统一检索、生命周期更新、回收维护与后台工作器关键链路，定位真实风险问题。
3. 采用最优方案实施修复，并补齐必要测试。
4. 运行最少必测集、全量测试与必要静态检查。
5. 对照计划逐项自检，补写执行变更总结。
6. 将计划迁移至 `docs/completed/`，完成 `git commit` 与 `git push`。

## 5. 验收标准

1. 必须明确说明本轮定位到的真实问题与修复原因。
2. 修复后不得引入新的生命周期漂移、回收错误、后台重复处理或运行时副作用。
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

1. 本轮全局审核确认了一个分数稳定性漏洞：`clampUnitScore` 之前只处理普通数值的上下界，没有处理 `NaN` 与 `Inf`。一旦上游 rerank/provider 或后处理环节输出非有限值，这些值就会原样进入排序、阈值链路和最终 RPC 响应。
2. 这类非有限分数不仅会破坏统一的 `0..1` 分数契约，还可能让排序比较与阈值判断出现不可预测结果，属于会悄悄污染整个召回链路的基础型风险。
3. 本轮把 `clampUnitScore` 收紧为“负无穷和 NaN -> 0，正无穷 -> 1”，让所有已经复用该 helper 的检索后处理链路自动获得非有限值防线；同时补充 end-to-end 回归测试，验证最终搜索结果不会再携带 `NaN/Inf`。

### 2. 📂文件变更清单

1. 修改：`internal/app/usecase/memory_query.go`
2. 修改：`internal/app/usecase/memory_query_test.go`

### 3. 💻关键代码调整详情

1. 更新 `clampUnitScore`，新增 `NaN` 和正负无穷的显式处理逻辑，确保所有进入该 helper 的分数最终都是有限的 `0..1` 值。
2. 保持既有上下界钳制语义不变，使普通分数路径零成本兼容已有排序和阈值逻辑。
3. 新增 `TestMemoryUseCaseSearchClampsNonFiniteRerankScores`，通过让 rerank provider 返回 `+Inf` 和 `NaN`，验证最终检索结果会收敛成 `1` 和 `0`，并且不会把非有限值继续暴露给调用方。

### 4. ⚠️遗留问题与注意事项

1. 本轮没有单独为每个后处理阶段重复加一套非有限值判断，而是继续把防线收口在共享 helper 上，保持实现简单且覆盖面广。
2. 当前修复优先保证“非有限值不会穿透到排序和响应”；如果未来引入新的分数来源，只要仍经过 `clampUnitScore`，就能自动获得同样的安全边界。
3. 已完成验证：
   - `go test ./internal/app/usecase -run "TestMemoryUseCaseSearch(ClampsRerankScores|ClampsNonFiniteRerankScores|AppliesRerank)"`
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./...`
   - `go vet ./...`
