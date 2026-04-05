# 任务计划：Retention Vector GC 测试对齐与审阅修复闭环

## 1. 任务目标

针对 `docs/VMM_Audit_Report.md` 中 VMM 第 4 条“Vector GC 重试队列被错误绑定在 retention 开关上”的修复状态，完成当前仓库的最终闭环校验与必要修正：

- 核对当前运行时代码是否已经按新职责边界拆分 `retention recycle` 与 `vector GC retry`
- 修正仍然沿用旧职责假设的 `retention` 测试
- 重新执行相关测试，确认“运行时代码 + 测试语义 + 审阅结论”三者一致

## 2. 详细执行步骤

1. 阅读 `docs/VMM_Audit_Report.md` 中 VMM 第 4 条，明确报告要求的目标行为。
2. 对照当前 `internal/app/usecase/retention.go`，确认以下边界是否成立：
   - `runMaintenance()` 只负责经典 retention 主流程
   - `runVectorGCMaintenance()` 独立负责 Vector GC retry
   - `runScheduledMaintenance()` 统一调度 retention / vector GC / scratchpad 三类维护任务
3. 检查 `internal/app/usecase/retention_test.go` 中现存失败测试，识别哪些断言仍绑定旧语义。
4. 仅修改测试与必要的验证逻辑，使测试契约与当前运行时代码职责边界保持一致。
5. 执行最小必要测试：
   - `go test ./internal/app/usecase`
   - 如有必要，补充执行 `go test ./internal/app`
6. 若验证通过，在计划末尾补充执行变更总结，并将计划迁移到 `docs/completed/`。

## 3. 技术选型与处理原则

- 以“当前运行时代码职责拆分已经正确”为前提，优先修正测试，不随意回退运行时结构。
- 保持修改范围最小，只处理与第 4 条审阅问题直接相关的内容。
- 所有判断以当前代码、现有回归测试、实际 `go test` 结果为准，不依赖报告中的口头描述单独下结论。
- 如发现运行时代码与报告目标仍存在真实偏差，再补做最小必要代码修正；若仅为测试语义滞后，则只修测试。

## 4. 验收标准

- `internal/app/usecase/retention.go` 的职责拆分与报告第 4 条描述一致。
- `internal/app/usecase/retention_test.go` 不再要求 `runMaintenance()` 处理 Vector GC retry。
- `go test ./internal/app/usecase` 全量通过。
- 最终可以明确说明：VMM 第 4 条问题在当前仓库中已被正确修复并通过测试闭环验证。

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已确认当前运行时代码中的职责拆分是正确的：`runMaintenance()` 负责经典 retention 主流程，`runVectorGCMaintenance()` 独立负责 Vector GC retry，`runScheduledMaintenance()` 负责统一调度。
- 已修正两条仍沿用旧职责假设的测试，使其不再错误要求 `runMaintenance()` 处理 Vector GC retry。
- 已通过实际测试验证本次闭环修复结果，当前 `retention` 相关维护语义与审阅报告第 4 条保持一致。

### 2. 📂文件变更清单

- 新增：`docs/plan/20260405-38-RETENTION_VECTOR_GC_TEST_ALIGNMENT.md`
- 修改：`internal/app/usecase/retention_test.go`
- 删除：无

### 3. 💻关键代码调整详情

- 将原 `TestRetentionUseCaseRunMaintenanceCompletesClaimedVectorGCJobs` 调整为直接验证 `runVectorGCMaintenance()`。
- 将原 `TestRetentionUseCaseRunMaintenanceReschedulesClaimedVectorGCJobsOnRetryFailure` 调整为直接验证 `runVectorGCMaintenance()`。
- 在上述两条测试中显式设置 `cfg.Enabled = false`，用于证明 Vector GC retry 已独立于经典 retention 开关存在。
- 执行验证：
  - `go test ./internal/app/usecase`
  - `go test ./internal/app`

### 4. ⚠️遗留问题与注意事项

- 本次未改动 `internal/app/usecase/retention.go` 运行时代码，因为现有职责拆分实现本身已与报告目标一致。
- 本次闭环重点是“修复测试语义滞后”，避免后续再次误判为运行时代码仍存在绑定关系。
- 当前与第 4 条审阅问题直接相关的代码与测试已完成闭环验证。
