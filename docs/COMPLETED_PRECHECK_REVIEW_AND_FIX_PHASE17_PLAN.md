# PRECHECK REVIEW AND FIX PHASE 17 PLAN

## 任务目标

- 继续审阅 `PreCheck` 与 unified memory search 的衔接链路。
- 重点检查候选筛选阈值是否仍与当前 `vector + hybrid + rerank + Weibull + context-aware + MMR` 记分语义一致。
- 找出一个真实问题并完成修复、测试补强与闭环验证。

## 执行步骤

1. 审阅 `PreCheck` 候选筛选逻辑与 unified memory search 各阶段的分数生成方式。
2. 确认是否存在“上游已排序但下游错误丢弃”或“不同记分量纲被错误共用”的真实问题。
3. 对问题做最小化修复，并补充可复现测试。
4. 运行仓库要求的测试和构建命令。
5. 对照计划逐项复核，并在完成后将文件重命名为 `COMPLETED_` 前缀。

## 技术约束

- 不修改对外 gRPC proto。
- 继续遵守当前仓库的分层依赖方向。
- 代码改动保持双语注释规范，优先修复实现语义问题，不做无关重构。

## 验收标准

- 至少修复一个真实且可复现的问题。
- 新增测试能够在修复前失败、修复后通过。
- 通过以下验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
