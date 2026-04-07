# 任务计划：向量重建末轮问题修复

## 任务目标

本次任务针对最新代码审查中发现的 3 个遗留问题做一次性收口修复，目标是让 `vmm-migrate -vector-rebuild` 在 `split` 与 `combined` 两种存储模式下都具备可预期、可回滚、可恢复的维护行为，避免再次出现“修复后仍有未覆盖问题”的往返。

本次需要解决的问题包括：

1. `split` 模式在真正进入向量重建流程前，就可能因为维护依赖装配而提前创建新维度的 LanceDB 表。
2. `split` 模式在维度切换场景下，如果 destructive reset 之后失败，当前实现会把 SQLite 恢复回来，但仍可能把运行时 sidecar 表留成空表。
3. 向量重建的维护读超时目前只覆盖项目内记忆枚举，没有覆盖项目列表枚举，导致大型项目集场景下仍可能先被普通在线超时截断。

## 详细执行步骤

1. 复核 `cmd/vmm-migrate`、`internal/app/maintenance.go`、`internal/app/vector_rebuild.go` 以及 PostgreSQL/SQLite 相关适配器，确认当前维护依赖装配与重建编排的真实执行顺序。
2. 调整维护依赖装配方式：
   - 避免 `split` 模式在仅为向量重建准备依赖时就提前创建当前维度的 LanceDB 表。
   - 保证向量重建命令只在真正进入受控重建阶段后才触碰 sidecar 表。
3. 重构 `split` 模式失败回滚策略：
   - 在维度切换失败场景下，不再把运行时 current-dimension sidecar 留在空表状态。
   - 让失败后的可见状态要么恢复原召回面，要么明确保留一个可继续恢复的中间态，且不能直接破坏运行时可用性。
4. 为项目列表枚举补充 maintenance-only 读取路径或等价维护超时策略，使向量重建的整个“枚举 active 数据”过程都受维护预算保护。
5. 补充/更新测试：
   - 覆盖 split 维度迁移场景下的依赖装配、reset、失败回滚与 sidecar 最终状态。
   - 覆盖维护项目枚举路径的超时选择逻辑。
6. 运行定向测试；如改动影响面较大，再补充全量测试，确认当前仓库未提交改动下的整体行为没有被本轮修复打破。

## 技术选型与处理原则

- 复用现有狭窄端口思想，不把维护命令重新耦合进在线运行时。
- 对 `split` 模式优先保证“失败后运行时仍可恢复旧召回面”，不接受“SQLite 已恢复但 sidecar 为空”的半恢复状态。
- 对维护超时问题优先采用显式 maintenance-only 端口，而不是篡改公共在线路径的默认预算。
- 测试补齐要聚焦真实失败链路，而不是只验证 happy path。

## 验收标准

1. `split` 模式执行 `-vector-rebuild` 时，不会在真正进入重建前提前留下一个新的空 LanceDB 运行时表。
2. `split` 模式在维度切换后的失败路径上，不会把当前运行时 sidecar 表留成空表并破坏 active 向量召回。
3. 向量重建的项目列表与项目内记忆枚举都能走维护专用读取预算。
4. 相关新增/修改测试通过，且至少覆盖 `cmd/vmm-migrate`、`internal/app`、`internal/adapters/outbound/vldb_postgres`。

## 执行变更总结

### 1. 核心修复与调整概述

- 调整维护依赖装配，在 `split` 模式下为 LanceDB 引入“只建连接、不预建表”的维护构造路径，避免 `-vector-rebuild` 在真正进入受控重建前就提前创建当前维度 sidecar 表。
- 重构 `split` 模式向量重建编排，改为“先完整物化目标向量，再执行破坏性 reset 与回填”，并在 reset 后中途失败时自动重放目标快照，使最终状态收敛到当前配置维度的可用目标面，而不是把运行时 sidecar 留成空表。
- 为项目列表枚举补齐 maintenance-only 读取路径，让向量重建的“项目列表 + 项目内记忆”两段枚举都走维护超时预算，不再被普通在线查询超时提前截断。
- 补齐并更新相关测试，覆盖 split 模式准备阶段失败、维度切换中途失败自动修复、维护项目枚举超时选择等关键链路，并完成全量回归验证。

### 2. 📂 文件变更清单

- 新增：
  - `internal/app/maintenance.go`
  - `internal/app/vector_rebuild.go`
  - `internal/app/vector_rebuild_test.go`
  - `internal/adapters/outbound/vldb_postgres/workspace_timeout_test.go`
- 修改：
  - `internal/adapters/outbound/vldb_lancedb/store.go`
  - `internal/adapters/outbound/vldb_postgres/workspace.go`
  - `internal/adapters/outbound/vldb_sqlite/store.go`
  - `internal/app/ports/interfaces.go`
- 删除：
  - 无

### 3. 💻 关键代码调整详情

- 在 `internal/adapters/outbound/vldb_lancedb/store.go` 中抽出 `newStore(...)`，新增 `NewStoreWithoutInit(...)`，让维护命令可显式控制 LanceDB 当前维度表的首次创建时机。
- 在 `internal/app/maintenance.go` 中新增维护专用的存储装配逻辑：`combined` 模式继续沿用原路径，`split` 模式则改为惰性 sidecar 初始化，避免预建空表。
- 在 `internal/app/vector_rebuild.go` 中将 split 重建改为“两阶段”：
  - 阶段一：通过 `materializeVectorRebuildRecords(...)` 预先生成并校验所有目标向量。
  - 阶段二：通过 `applySplitVectorRebuildRecords(...)` 执行 reset 与回填；若中途失败，则由 `recoverSplitVectorRebuildAfterReset(...)` 自动重放目标快照完成修复。
- 在 `internal/app/vector_rebuild.go` 与存储端口中补齐维护项目枚举能力，使 `loadVectorRebuildRecords(...)` 可以优先调用 `ListProjectsForMaintenance(...)` 与 `ListProjectMemoriesForMaintenance(...)`。
- 在 `internal/adapters/outbound/vldb_postgres/workspace.go` 中抽出共享的项目枚举辅助逻辑，并分别接入在线 `queryContext` 与维护 `maintenanceReadContext`。
- 在测试侧新增/更新 split 失败保护、自动修复与维护超时相关用例，确保本轮修复点都有对应回归覆盖。

### 4. ⚠️ 遗留问题与注意事项

- 当前工作区仍存在大量与本任务无关的未提交改动；本次修复只围绕向量重建与维护超时路径进行了局部调整，没有触碰其他用户改动。
- 本轮已执行 `go test ./...` 并通过；若后续继续改动维护命令入口或存储装配路径，建议保持同等级别的全量回归，避免再次出现“修一轮漏一轮”的连锁问题。
