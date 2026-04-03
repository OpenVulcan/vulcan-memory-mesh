# MEMORY QUERY PRECHECK REVIEW PHASE28 PLAN

## 任务目标

- 继续审阅 `MemoryQuery` 与 `PreCheck` 交界处的输入归一、候选构造与结果解释链路。
- 找出一个真实且可复现的一致性或行为问题，并完成修复、测试补强与验证闭环。
- 保持现有对外 gRPC 契约不变，只修正内部实现与 reviewer 行为稳定性。

## 执行步骤

1. 审阅 `MemoryQuery` 结果回传到 `PreCheck` 候选构造与 reviewer 输入的关键路径。
2. 找出一个真实问题，优先关注“输入等价但行为不一致”“解释字段与真实候选不一致”“重复或无效信息放大”这类缺陷。
3. 做最小化修复，并补充定向测试。
4. 运行仓库要求的测试与构建命令。
5. 对照计划逐项复核，并在完成后将文件重命名为 `COMPLETED_` 前缀。

## 技术约束

- 不修改 proto、RPC 外部字段和语义。
- 保持 `adapters -> app -> logic/domain` 的依赖方向不变。
- 新增实现与测试继续遵守仓库双语注释规范。

## 验收标准

- 至少修复一个真实问题，而不是纯样式整理。
- 新增测试能够稳定复现并覆盖该问题。
- 通过以下验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
