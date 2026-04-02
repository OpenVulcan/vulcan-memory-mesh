# 混合检索阶段一实施计划

更新时间：2026-04-02

## 1. 任务目标

- 按 `RETRIEVAL_AND_DECAY_UPGRADE_ANALYSIS_V2_CN.md` 的后续路线，优先完成阶段 0 和阶段 1 的核心落地。
- 修复当前 memory 检索链里 active / unexpired 过滤缺口。
- 在 SQLite 路径上接入 `FTS5 + BM25` lexical recall。
- 在 `MemoryUseCase.Search(...)` 中接入 `Hybrid + RRF`，并保持现有 gRPC 契约可用。
- 让 `PreCheck` 自动复用新的混合检索能力，而不先扩大协议变更面。

## 2. 执行步骤

1. 检查当前 `MemoryStore` / `vldb_sqlite` / `memory_query.go` 的真实检索链与过滤缺口。
2. 扩展应用层端口与领域模型，为 lexical hit、RRF 融合和来源标识提供最小必要结构。
3. 修改 SQLite schema，新增 `vmm_memory_nodes_fts` 并补齐写入/状态变更时的 FTS 同步。
4. 在 SQLite 适配器中实现 lexical search，并修复 `LoadMemoryNodesByVectorIDs` / `LoadActiveSessionMemoryNodes` / `LoadRecentDirectMemoryWrites` 的过期过滤。
5. 在旧兼容 provider 已移除前，曾短暂补最小兼容桩以保证运行时组合和测试稳定；当前主线已不再保留该路径。
6. 改造 `MemoryUseCase.Search(...)`，实现：
   - vector recall
   - lexical recall
   - RRF 融合
   - 可选 rerank 继续生效
7. 更新配置、文档和测试。
8. 跑相关测试与标准构建，完成后把计划文件改名为 `COMPLETED_...`。

## 3. 技术选型

- 关系检索：`SQLite FTS5`
- 融合算法：`RRF`
- 当前阶段不扩大 proto 字段，仅保持返回 `score` 与 `origin` 语义升级
- 当前阶段仍保留 DashScope rerank 为混合检索后的可选重排层

## 4. 风险与约束

- 当前 SQLite schema 仍使用 `currentSchemaVersion` + `resetCurrentSchema()` 机制。
- 本阶段涉及 SQLite schema 变更，开发期旧数据存在被受管表重建清理的风险。
- 必须保持依赖方向：`adapters -> app -> logic/domain`。

## 5. 验收标准

- `LoadMemoryNodesByVectorIDs(...)` 相关读路径不再把已过期 memory 带入检索热链。
- SQLite 可执行 lexical recall，并能和向量结果做 RRF 融合。
- `PreCheck` 不改 RPC 契约即可自动使用混合检索结果。
- 相关测试通过，且至少执行：
  - `go test ./internal/adapters/outbound/vldb_sqlite ./internal/app/usecase ./internal/config`
  - `go test ./...`
  - `.\make.ps1 build`
