# Context-Aware Retrieval Phase 5 Plan

## 目标

- 在当前 `SQLite + LanceDB + Hybrid/RRF + Rerank + Weibull + MMR` 主链上，补齐第一版情境化检索打分。
- 复用已经落地的 `vmm_memory_context_edges` 与 `support_count / rebuttal_count`，让检索链可以根据 query/background 中的情境线索提升匹配记忆、压低相反证据。
- 保持当前外部 gRPC proto 不变，不新增 RPC 字段。

## 执行步骤

1. 审核当前检索链与可插入点：
   - `internal/app/usecase/memory_query.go`
   - `internal/adapters/outbound/vldb_sqlite/store.go`
   - `internal/app/ports/interfaces.go`
2. 扩展关系读取能力：
   - 为 memory store 增加按 memory ids 批量加载 context edges 的能力
   - 保持接口局部扩展，不影响 gRPC 契约
3. 实现情境打分：
   - 从 `background + query` 提取轻量上下文 token/phrase
   - 命中 edge 时按 `support_count / rebuttal_count` 生成 context evidence boost
   - 作为 RRF/rerank/Weibull 之后、MMR 之前的读时排序层
4. 保持兼容性：
   - 无匹配 edge 时完全退化为当前排序
   - 不引入强过滤默认值，先做 soft boost / soft demote
5. 补测试、文档并验证：
   - 覆盖 store / usecase 相关测试
   - 运行仓库要求的最小测试、`go test ./...`、`./make.ps1 build`

## 技术取舍

- 第一版只做读时 soft scoring，不新增外部过滤参数，避免 proto 扩张过快。
- 情境提取先采用 deterministic lexical normalization，不引入额外模型调用。
- 匹配策略优先走 `context_value` 与 query/background 的规范化 phrase/token 命中；后续再考虑 key-aware query builder。

## 验收标准

- `MemoryStore` 能按 memory ids 批量返回 `vmm_memory_context_edges`。
- 检索链能在命中情境 edge 时稳定提升支持证据、压低反驳证据。
- 没有情境 edge 的记忆排序行为与当前版本兼容。
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `./make.ps1 build`
