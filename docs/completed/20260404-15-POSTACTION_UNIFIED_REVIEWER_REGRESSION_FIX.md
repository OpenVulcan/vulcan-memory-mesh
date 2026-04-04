# 任务目标

修复 `PostAction` 统一评审器重构引入的两个回归问题，恢复统一评审失败时的优雅降级能力，并确保画像评审中被标记为 `invalid` 的节点仍能进入关系库存档，保持审计链完整。

# 执行步骤

1. 梳理 `PostAction` 当前主链路，确认 `applyImmediateTurnAnalysis`、`reviewTurnCandidates`、`ApplyTurnAnalysis` 之间的错误传播与数据写回关系。
2. 修改 `applyImmediateTurnAnalysis` 的统一评审错误处理逻辑：
   - 捕获 `reviewTurnCandidates` 错误并记录 `Warn` 日志；
   - 不中断后续流程；
   - 保持首轮分析器产出的 `memory_nodes` / `profile_nodes` 可继续参与持久化。
3. 重构统一评审后的画像结果回填逻辑，明确区分：
   - 用于数据库落库的完整 `analysis.ProfileNodes`；
   - 仅用于当前轮合并画像文本与运行态判断的活跃画像视图。
4. 补充或调整单元测试，覆盖：
   - 统一评审失败时仍继续落库的降级路径；
   - `invalid` 画像节点在统一评审后仍保留在待持久化列表中的行为。
5. 运行 `go test ./internal/app/usecase/...` 验证修复结果，必要时修正测试夹具或断言。

# 技术选型

1. 保持现有 `adapters -> app -> logic/domain` 单向依赖，不新增跨层引用。
2. 不修改数据库 Schema，不变更 gRPC 协议，只在应用层编排与测试层修复行为。
3. 复用现有日志器与画像状态枚举，避免引入新的状态机分支。

# 验收标准

1. 当统一评审器或记忆检索发生瞬时错误时，`PostAction` 不再提前返回错误，当前轮的首轮分析结果仍会进入 `ApplyTurnAnalysis`。
2. 被统一评审标记为 `ProfileStatusInvalid` 的画像节点仍保留在 `analysis.ProfileNodes` 中，并能够被后续存储层写入 `vmm_profile_nodes`。
3. 活跃画像过滤逻辑只影响画像合并视图，不影响待持久化实体集合。
4. `go test ./internal/app/usecase/...` 通过。

# 执行变更总结

## 1. 核心修复与调整概述

1. 恢复了统一评审失败时的优雅降级：`applyImmediateTurnAnalysis` 现在会在统一 reviewer 失败时记录 `Warn` 日志，并回退到首轮分析器在评审前的候选结果继续执行后续落库。
2. 修复了画像 `invalid` 审计链断裂：统一评审回填后不再用活跃节点过滤器裁剪 `analysis.ProfileNodes`，从而保证 `invalid` 节点仍会进入存储层。
3. 补充了两条回归测试，分别锁定“统一 reviewer 失败仍继续持久化”和“`invalid` 画像仍保留在待持久化集合”。

## 2. 📂文件变更清单

### 修改

1. `internal/app/usecase/postaction.go`
2. `internal/app/usecase/postaction_candidate_review.go`
3. `internal/app/usecase/postaction_test.go`
4. `internal/app/usecase/postaction_candidate_review_test.go`

### 新增

1. `docs/completed/20260404-15-POSTACTION_UNIFIED_REVIEWER_REGRESSION_FIX.md`

### 删除

1. 无

## 3. 💻关键代码调整详情

1. 在 `postaction.go` 中新增评审前分析结果深拷贝逻辑，并在统一 reviewer 失败时恢复该快照，避免瞬时错误导致整轮 `ApplyTurnAnalysis` 被跳过。
2. 在降级路径中保留 `memory_nodes` / `profile_nodes` 的持久化机会，同时显式把画像节点状态归一为 `pending`，保证 reviewer 缺席时的生命周期字段仍然稳定。
3. 在 `postaction_candidate_review.go` 中新增画像节点深拷贝逻辑，并把 `analysis.ProfileNodes` 改为承载完整评审结果集合，而不是只保留 `active` 子集。
4. 调整 `filterPostActionActiveProfileNodes` 的职责说明，使其只代表运行态活跃视图过滤，不能再参与数据库待写实体裁剪。
5. 在测试中新增：
   - 统一 reviewer 超时后仍继续写回 analyzer 输出；
   - `invalid` 画像节点仍停留在待持久化列表，且不会被计入 merged active profile。

## 4. ⚠️遗留问题与注意事项

1. 当前仅验证了 `./internal/app/usecase/...`，未额外扩展到全仓 `go test ./...`。
2. 统一 reviewer 降级后，画像节点会以 `pending` 状态落库，后续仍需要依赖下一次正常评审或维护链路完成进一步状态收敛。
