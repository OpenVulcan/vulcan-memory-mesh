# DuckDB Removal And Weibull Phase 3 Plan

## 任务目标

- 彻底移除当前仓库中的 DuckDB 运行时支持、兼容 provider、相关配置入口和调试入口。
- 保持本地版关系库存储只剩 `SQLite` 主路径，避免后续功能继续被双 provider 兼容逻辑拖累。
- 在只保留 SQLite 的前提下，继续落地 v2 文档中的下一阶段：`Weibull` 读时衰减分与强化字段骨架。

## 执行步骤

1. 审核 DuckDB 残留面：
   - `internal/app/app.go` 中的 provider 装配
   - `internal/config/config.go` 中的 DuckDB 配置、默认值、环境变量和校验
   - `cmd/vmm-local/debug_clean.go` / `main.go` 中的 DuckDB 清理入口
   - `internal/adapters/outbound/vldb_duckdb/` 目录及其测试
   - README 与相关中文文档中的 DuckDB 说明
2. 移除 DuckDB 支持：
   - 删除 DuckDB adapter、proto 和相关测试文件
   - 删除运行时装配分支、配置结构、环境变量和 provider 校验
   - 调整 debug-clean，只保留 `sqlite` / `lancedb` / `all`
   - 同步 README 和必要文档
3. 落地 Weibull phase 1：
   - 为 `vmm_memory_nodes` 增加 `last_reinforced_timestamp`、`reinforcement_count`、`decay_disabled`
   - 让 `ApplyMemoryAdoption(...)` 写回强化字段，并按当前 `memory_level` / `cross_session_adopted_count` 做轻量晋升
   - 在 `memory_query.go` 的最终排序中增加 `decay_score`
   - 保留 `expires_timestamp` 作为硬边界
   - 为配置增加基础 Weibull 参数
4. 补充测试：
   - 配置与运行时只接受 SQLite
   - debug-clean 新契约
   - adoption 写回强化字段与轻量晋升
   - Weibull 读时衰减分能影响排序
5. 运行 `gofmt`、定向测试、全量测试和标准构建。

## 技术选择

- 关系库存储：仅保留 SQLite gateway
- 衰减模型：`decay_score = exp(-((effective_age / eta) ^ beta))`
- 强化时间：优先使用 `max(last_reinforced_timestamp, last_adopted_timestamp, created_timestamp)`
- 晋升规则：
  - `L0/L1 -> L2`：`reinforcement_count >= 2` 或 `cross_session_adopted_count >= 2`
  - `L2 -> L3`：`reinforcement_count >= 5` 且高优先级，或未来显式人工锁定

## 验收标准

- 运行时、配置和调试入口不再接受 DuckDB。
- `internal/adapters/outbound/vldb_duckdb/` 不再参与主仓库构建链。
- 检索最终分已叠加 Weibull 风格的 `decay_score`，且只作用于 active + unexpired 集合。
- adoption 写回会更新强化字段，并能触发现有 `memory_level` 的轻量晋升。
- `go test ./... -count=1` 通过。
- `.\make.ps1 build` 通过。
