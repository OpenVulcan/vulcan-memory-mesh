# AI 节点 Key 级预算语义对齐计划

## 任务目标

把当前 AI 容灾实现从“节点内多 Key 共享 `RPM / TPM / RPD` 预算”修正为“节点上的额度配置表示该节点下**每个 Key 各自独享**的额度”。同时补齐当上游未返回实际 `usage` 时的 TPM 回填策略，改为按输入 token 估算值乘以 `1.3` 进行保守记账，避免预算被错误归零。

## 执行步骤

1. 审查当前 `ai_key_failover` 的节点预算结构、请求预留与回填流程，确认哪些状态仍按“节点共享预算”建模。
2. 将运行时预算状态从节点级共享计数改为 Key 级独立计数，保持节点仍只作为“同档位 Key 分组”存在。
3. 调整请求执行流程，使预算预留、失败退款、成功回补都绑定到具体 Key，而不是节点总量。
4. 修改 LLM 的 usage 对齐逻辑；当 provider 未返回 `usage` 时，按输入 token 估算值的 `1.3` 倍作为实际成本写回预算。
5. 更新单测，移除“同节点多 Key 共享预算”的断言，改为覆盖“同节点多 Key 各自独立预算”的行为。
6. 同步修正文档口径，明确节点配置描述的是“每个 Key 的额度”，并执行规定测试与全量 `go test ./...` 验证。

## 技术选型与实现原则

- `nodes` 的职责是表达“同一固定模型下的一组同档位 Key”，不是表达共享总额度。
- `rpm / tpm / rpd` 必须绑定到具体 Key 的运行时状态，而不是节点聚合状态。
- 当不同 Key 档位不同，仍通过拆分多个节点表达，以保持配置可读性。
- usage 缺失时采用保守估算，不把未知成本当成零，避免预判逻辑失效。

## 验收标准

- 同一节点内多个 Key 的预算互不影响；一个 Key 用尽额度后，仍可切到同节点内其他 Key。
- `429 / quota` 仅影响当前 Key 的预算与冷却状态，不会错误拖累同节点其他 Key。
- 当上游未返回 `usage` 时，TPM 预算仍会按保守估算持续累计，不会被全部退回。
- 测试、实现、配置文档与设计文档对“节点=分组、额度=每 Key 独享”的语义保持一致。
- 通过仓库要求的相关测试与 `go test ./...`。

## 执行变更总结

### 1. 核心修复与调整概述

- 将 `ai_key_failover` 的预算状态从“节点共享”改为“节点配置作用于每个 Key，各 Key 独立记账”。
- 调整请求执行流程，使预算预留、失败退款与成功回补全部绑定到具体 `node + key`，不再让同节点 Key 相互挤占额度。
- 对 LLM 成功响应的 usage 缺失场景补充保守回填逻辑：按输入 token 估算值的 `1.3` 倍计入实际消耗。
- 同步修正测试与文档口径，明确 `nodes` 只是同档位 Key 的分组，`rpm / tpm / rpd` 表示节点内每个 Key 各自独享的额度。

### 2. 📂文件变更清单

- 新增：
  - `docs/plan/20260406-02-AI_NODE_KEY_LEVEL_BUDGET_ALIGNMENT.md`
- 修改：
  - `internal/adapters/outbound/ai_key_failover/state.go`
  - `internal/adapters/outbound/ai_key_failover/selector.go`
  - `internal/adapters/outbound/ai_key_failover/request_cost.go`
  - `internal/adapters/outbound/ai_key_failover/llm.go`
  - `internal/adapters/outbound/ai_key_failover/key_failover_test.go`
  - `internal/config/config.go`
  - `README.md`
  - `configs/vmm_config_readme.md`
  - `docs/ai-model-failover-design_CN.md`

### 3. 💻关键代码调整详情

- `internal/adapters/outbound/ai_key_failover/state.go`
  - 删除节点共享预算状态，改为在每个具体 Key 上维护独立的固定窗口预算计数。
  - 节点筛选逻辑改为“只要节点内存在至少一个健康且预算可用的 Key，就保留该节点候选资格”。
- `internal/adapters/outbound/ai_key_failover/selector.go`
  - 请求循环改为基于具体 Key 进行预算预留、退款与回补。
  - 当某个 Key 预算预留失败时，会继续尝试同节点其他 Key，而不是直接放弃整个节点。
- `internal/adapters/outbound/ai_key_failover/request_cost.go` 与 `llm.go`
  - 补充 usage 缺失时的 TPM 保守估算逻辑，按输入 token 的 `1.3` 倍计入实际使用量。
- `internal/adapters/outbound/ai_key_failover/key_failover_test.go`
  - 将“同节点多 Key 共享预算”的测试改为“同节点多 Key 各自独立预算”。
  - 新增 usage 缺失时回填 `1.3x` 输入 token 的回归测试。
- 文档同步：
  - `README.md`
  - `configs/vmm_config_readme.md`
  - `docs/ai-model-failover-design_CN.md`
  - 全部改为“节点配置表示节点内每个 Key 各自独享额度”的表述。

### 4. ⚠️遗留问题与注意事项

- 当前预算状态仍是纯内存态，进程重启后会重新计数；这是既有设计约束，未在本次任务中改变。
- `embedding` 与 `rerank` 仍使用请求前估算值做预算累计；只有 LLM 路径在成功后会额外尝试按 provider usage 做回补。
- 如果未来要支持“同节点下不同 Key 拥有不同额度”，建议直接拆节点，不要继续向单节点内部扩展异构配额语义。
- 本次已执行 `go test ./internal/adapters/outbound/ai_key_failover ./internal/config ./internal/app` 与 `go test ./...`，结果均通过。
