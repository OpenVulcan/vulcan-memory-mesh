# 任务目标

修复向量重建链路在代码审查中发现的两个 P1 问题，确保一次性维护工具 `vmm-migrate -vector-rebuild` 在异常场景下具备可恢复、可预期且不破坏现网可见召回面的行为。

# 执行步骤

1. 梳理 `internal/app/vector_rebuild.go`、`internal/adapters/outbound/ai_key_failover` 与相关存储端口的当前实现，确认两个问题的真实触发链路与受影响边界。
2. 修复 embedding 重建批次中的错误判定逻辑，区分“预算暂时耗尽”和“永久性无可用 Key / 节点”两类场景，避免维护命令进入无限等待。
3. 修复 split 模式向量重建的批次写入顺序或回滚策略，避免 LanceDB 回填失败时把 SQLite durable 与 LanceDB sidecar 留在半重建状态。
4. 补充或更新对应单元测试，覆盖永久性 Key 失效、预算型重试、split 模式批次失败等关键边界。
5. 运行定向测试验证修复闭环，并对照审查意见逐项复核是否完全消除风险。

# 技术选型

- 优先复用现有 `ai_key_failover` 的错误分类与类型机制，避免在维护链路里重新发明一套不一致的判断规则。
- split 模式修复优先选择“先完成 sidecar 可见写入，再提交 durable 回写”或“失败可补偿回滚”的方式，以保证实际召回面与 durable 数据在失败时保持一致。
- 测试层继续沿用当前仓库的 stub/fake 模式，不引入额外外部依赖。

# 验收标准

1. 当 embedding Key 全部永久失效或节点不可用时，`vector rebuild` 会及时返回错误，不会进入无限 30 秒轮询等待。
2. 当 split 模式下 LanceDB 批次写入中途失败时，不会出现 SQLite 已写入新向量而 LanceDB 仅部分成功的半重建状态。
3. 新增或更新的测试能够稳定覆盖上述两个问题的触发路径，并在本地通过。
4. 修复内容与代码审查提出的两个 P1 问题逐项对齐，无新增行为回归。

# 执行变更总结

## 1. 核心修复与调整概述

- 在 `ai_key_failover` 里为 `exhaustedCandidatesError` 增加“预算耗尽 / 候选不可用”原因区分，并把 `vector rebuild` 的等待逻辑收紧为只对预算型耗尽执行重试，避免永久性无可用 Key 时无限等待。
- 在 split 模式向量重建链路中引入“reset 后失败自动回滚”机制：一旦重建表、embedding、维度校验、durable 替换或 LanceDB upsert 任一步失败，就恢复原始 durable 向量与 sidecar 表数据，避免留下半重建状态。
- 补充 selector 与 vector rebuild 的回归测试，覆盖预算型耗尽、永久不可用、embedding 失败、维度异常以及 sidecar 中途 upsert 失败后的恢复路径。

## 2. 📂文件变更清单

- 修改：`internal/adapters/outbound/ai_key_failover/errors.go`
- 修改：`internal/adapters/outbound/ai_key_failover/state.go`
- 修改：`internal/adapters/outbound/ai_key_failover/selector.go`
- 修改：`internal/adapters/outbound/ai_key_failover/key_failover_test.go`
- 修改：`internal/app/vector_rebuild.go`
- 修改：`internal/app/vector_rebuild_test.go`

## 3. 💻关键代码调整详情

- `errors.go`：为耗尽错误增加原因枚举，新增 `IsBudgetExhaustedCandidatesError`，供维护链路只在“预算暂时耗尽”时等待预算窗口。
- `state.go` / `selector.go`：选择器在节点预检查、节点内 Key 选择以及执行循环兜底阶段，都会保留预算型耗尽与永久不可用的区分，不再把所有 exhaustion 统一抹平成同一种错误。
- `vector_rebuild.go`：split 模式在 destructive reset 后会保留原始快照；若后续任一步失败，则通过 durable 恢复、sidecar 重建与旧向量重新 upsert 执行回滚，同时将错误明确标记为“已回滚”或“回滚失败”。
- `vector_rebuild_test.go`：stub 存储增加当前快照跟踪与失败注入能力，断言失败场景下 durable / sidecar 最终都恢复为旧向量，而不是停留在 reset 后的空状态或半成品状态。

## 4. ⚠️遗留问题与注意事项

- 本次仅运行了与修复点直接相关的定向测试：`go test ./internal/adapters/outbound/ai_key_failover ./internal/app ./cmd/vmm-migrate`，尚未执行 `go test ./...`。
- `internal/adapters/outbound/ai_key_failover/key_failover_test.go` 当前工作区本身还包含用户已有的多路由测试改动，本次修复是在这些既有改动基础上继续补充，不包含对其余未提交变更的回退处理。
