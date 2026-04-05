# Rerank Key 降级与容灾修复计划

## 任务目标

修复上一轮自检中发现的 Key 容灾实现问题，重点包括：

1. 修复单值环境变量无法真正覆盖 `api_keys` 池的问题。
2. 移除 `rerank` 对 `llm` key 池的隐式复用，避免跨 provider / 跨凭据误用。
3. 保证 `rerank` 在 key 失效、限流、配额耗尽或全部 key 暂停等场景下，统一按 `rerank=false` 的效果降级执行，并记录明确警告日志。
4. 修复 DashScope rerank 链路中 `respect_retry_after` 配置无法生效的问题。

## 执行步骤

1. 审查并调整配置层覆盖逻辑，确保单值环境变量写入时会清空历史 `api_keys` 池。
2. 调整 `rerank` 配置校验与运行时装配，禁止在 `rerank` 未配置自身 key 时复用 `llm` key。
3. 调整 rerank 失败路径的日志语义，确保运行时出现 key 相关故障时按“禁用 rerank”方式继续首轮排序，同时输出明确 warning。
4. 为 DashScope 适配器补充可分类的错误结构，把状态码与响应头传递到 failover 分类层，使 `Retry-After` 能参与冷却时间计算。
5. 补充和更新测试，覆盖环境变量覆盖优先级、rerank key 配置要求、rerank 失败降级与 DashScope `Retry-After` 行为。
6. 运行仓库规定测试，确认修改不会破坏现有链路。

## 技术选型与实现原则

- 保持容灾边界收敛到“同 provider、同 endpoint、同 model 的 key 轮换”，不重新引入跨 provider 或跨模型容灾。
- `rerank` 继续保持“可选增强能力”定位；一旦其 key 失效或服务失败，不应阻断主检索链路。
- 失败分类仍坚持“只对 key 级故障切 key”，公共故障默认不广播到全部 key。
- 优先通过小范围结构化修改完成修复，不引入额外全局状态持久化。

## 验收标准

- `VMM_*_API_KEY` 能正确覆盖并替换文件中的 `api_keys` 池。
- `rerank.enabled=true` 时必须显式配置 `rerank.api_key` 或 `rerank.api_keys`，不再接受 `llm` key 池兜底。
- `rerank` 调用失败时，查询结果退化为首轮排序，且日志明确体现“按禁用 rerank 继续执行”。
- DashScope 链路在返回 `Retry-After` 时能够影响 key 冷却时长。
- 至少通过仓库要求的相关测试，并完成 `go test ./...`。

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已修复单值环境变量与 `api_keys` 池的覆盖冲突，确保 `VMM_*_API_KEY` 会替换旧 key 池而不是与其合并。
- 已移除 `rerank` 对 `llm` key 池的隐式复用，`rerank.enabled=true` 现在必须显式配置自己的 key。
- 已明确 `rerank` 故障降级语义：当 rerank key 失效或调用失败时，运行时按 `rerank=false` 的效果退回首轮排序，并记录 warning。
- 已为 DashScope rerank 增加结构化 API 错误透传，支持把响应头中的 `Retry-After` / `Retry-After-Ms` 纳入冷却计算。

### 2. 📂文件变更清单

- 修改：
  - `internal/config/config.go`
  - `internal/app/app.go`
  - `internal/app/usecase/memory_query.go`
  - `internal/adapters/outbound/dashscope_rerank/client.go`
  - `internal/adapters/outbound/ai_key_failover/classifier.go`
  - `internal/config/config_test.go`
  - `internal/app/app_test.go`
  - `internal/app/usecase/memory_query_test.go`
  - `internal/adapters/outbound/ai_key_failover/key_failover_test.go`
  - `internal/adapters/outbound/dashscope_rerank/client_test.go`
  - `configs/vmm_config_readme.md`
  - `docs/ai-model-failover-design_CN.md`
  - `docs/plan/20260405-36-RERANK_KEY_DEGRADE_AND_FAILOVER_FIX.md`
- 新增：
  - 无
- 删除：
  - 无

### 3. 💻关键代码调整详情

- 在配置层新增 `setSingleKey` 覆盖逻辑，使单值环境变量写入时会清空原有 key 池，并同步收紧 `rerank` 的配置校验。
- 在组合根中移除 `buildReranker` 对 `llm` key 池的兜底拼接，只允许使用 `rerank` 自己的 `api_keys`。
- 在检索用例中把 rerank 失败警告升级为“`rerank-disabled fallback`”语义，并显式写入 `fallback_mode=rerank_disabled` 字段。
- 在 DashScope 适配器中引入结构化 `APIError`，并在 failover 分类器中基于响应头选择冷却时长，补足 `respect_retry_after` 的真实生效路径。
- 补充测试覆盖：
  - 单值环境变量替换 key 池
  - `rerank` 必须显式配置专属 key
  - DashScope 结构化错误与 `Retry-After` 冷却
  - rerank 降级日志包含 `rerank_disabled`

### 4. ⚠️遗留问题与注意事项

- 当前 `rerank` 仍保持“固定 provider / endpoint / model 下的 key 轮换”设计，没有引入跨 provider、跨模型容灾。
- `rerank` 的降级策略是“首轮排序继续返回结果”，因此 warning 会频繁出现在 key 额度耗尽或上游抖动期间，属于预期行为。
- 本轮已完成规定测试与 `go test ./...`；若后续再扩展其他 provider 的 rerank 适配器，需要同步提供结构化错误透传，否则 `respect_retry_after` 仍可能退化为静态冷却。
