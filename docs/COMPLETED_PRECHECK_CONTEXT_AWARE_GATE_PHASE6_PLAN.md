# PRECHECK Context-Aware Gate Phase 6 Plan

## 目标

- 增强 `PreCheck` 第一层意图提取与检索规划，让是否需要记忆、检索语句的组织方式更好地感知当前情境。
- 在不改变外部 gRPC 契约的前提下，让 `PreCheck` 在以下场景更稳：
  - 当前问题明显只依赖即时上下文时，减少不必要的 memory 检索
  - 当前问题带有明确情境锚点时，提升检索 query 对情境词的保留能力
- 保持现有两层 `extract_intent -> memory search -> review_precheck_memory` 主链不变。

## 执行步骤

1. 审核当前 `PreCheck` 第一层：
   - `internal/app/usecase/precheck.go`
   - `internal/logic/processor/render.go`
   - `internal/logic/processor/intent_extractor.go`
   - `configs/prompts/*/extract_intent.md`
2. 调整第一层输入与内部规则：
   - 补充情境线索输入组织方式
   - 优化 query 生成的情境保留策略
3. 增强服务端 gate：
   - 对明显无需记忆的 query 继续快速跳过
   - 对带强情境锚点的 query，保留背景 + query 的更稳定组合
   - 避免过度扩张 query 数量
4. 补测试和文档：
   - 覆盖 `PreCheck` 的 need-memory 判断与 query 生成行为
   - 同步 README / `post-action` 或相关检索文档中的必要说明
5. 验证并收口：
   - 运行仓库要求的最小测试、`go test ./...`、`./make.ps1 build`

## 技术取舍

- 这一阶段不扩 proto，不把复杂 gate 参数暴露给客户端。
- 先增强 server-side 规划与 prompt 契约，不引入额外模型调用。
- 情境感知仍以 deterministic 文本组织 + LLM 第一层判断为主，不做硬规则覆盖 LLM 主决策。

## 验收标准

- `PreCheck` 在无需长期记忆的请求上能继续稳定跳过检索。
- `PreCheck` 在带明确情境锚点的请求上能生成更贴近情境的 search queries。
- 不改变外部 gRPC 契约。
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `./make.ps1 build`
