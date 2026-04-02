# PRECHECK REVIEW AND FIX PHASE 16 PLAN

## 任务目标

- 对当前 `PreCheck` 检索、候选合并、reviewer 输入构造链路继续做代码审阅。
- 找出一个真实且可复现的问题，并完成代码修复与测试补强。
- 保持现有外部 gRPC 契约不变，只修正内部实现一致性与稳定性问题。

## 执行步骤

1. 审阅 `PreCheck` 主链路相关实现，重点检查：
   - 多 query group 候选合并后的字段一致性
   - reviewer 侧候选序号、候选内容与最终排序之间的一致性
   - 候选截断、排序与展示字段是否存在失配
2. 对发现的问题补充或调整实现，确保修复最小化且逻辑闭环。
3. 增加针对性测试，保证问题可复现、修复可验证。
4. 运行仓库要求的测试与构建命令。
5. 完成后对照本计划逐项复核，并将文件重命名为 `COMPLETED_` 前缀。

## 技术约束

- 不修改对外 gRPC proto。
- 继续遵守当前 `adapters -> app -> logic/domain` 依赖方向。
- 新增或改动代码必须保持仓库现有双语注释规范，不做无意义注释膨胀。

## 验收标准

- 至少定位并修复一个真实问题，而不是仅做样式整理。
- 补充相应测试，并能稳定通过。
- 通过以下验证：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`
