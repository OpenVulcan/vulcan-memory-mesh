# 任务计划：修复 PostgreSQL 组合库启动时的默认调试种子误写入问题

## 1. 任务目标

修复 PostgreSQL 组合库在启动阶段无条件补种 `default/default/default` 调试层级的问题，确保：

- 仅在真正的空工作区场景下才写入默认调试层级
- 已有工作区在启动时不会被额外写入默认数据
- 当现有数据中存在名称为 `default` 的真实业务层级时，不会因调试种子逻辑触发唯一约束冲突导致启动失败
- PostgreSQL 组合库的启动语义与当前 SQLite 路径保持一致

## 2. 执行步骤

1. 复核当前 PostgreSQL 与 SQLite 的调试种子行为差异
   - 对照 `internal/adapters/outbound/vldb_postgres/schema.go`
   - 对照 `internal/adapters/outbound/vldb_sqlite/schema_migrations.go`
   - 明确“空工作区才允许补种”的一致性约束

2. 修复 PostgreSQL 启动逻辑
   - 在调试种子写入前增加空工作区判断
   - 将该判断收敛为可复用的小型辅助逻辑，避免后续再次偏离 SQLite 语义

3. 补充回归验证
   - 增加无需真实数据库依赖的单元测试
   - 验证“空工作区时允许补种、非空工作区时禁止补种”的判断逻辑

4. 执行测试并确认结果
   - 运行 PostgreSQL 适配器与应用层相关测试
   - 必要时执行全量 `go test ./...` 回归

## 3. 技术选型

- 修复策略采用“启动前先统计项目数，再决定是否补种默认层级”
  - 原因：这与 SQLite 现有实现保持一致，改动最小，行为最明确
- 测试策略优先采用“提炼纯判断函数 + 单元测试”
  - 原因：本次问题核心在启动条件判断，不需要引入真实 PostgreSQL 依赖即可稳定覆盖

## 4. 验收标准

- PostgreSQL 组合库在已有项目数据时启动，不会再写入默认调试层级
- PostgreSQL 组合库在空工作区初始化时，仍能保留原有默认调试层级能力
- 与本次修复相关的测试全部通过
- 计划文件补充完整的执行变更总结后迁移到 `docs/completed/`

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已为 PostgreSQL 组合库的 `ensureDebugSeedWorkspace` 增加“仅空工作区补种”的前置判断，避免已有工作区在启动时被额外写入 `default/default/default` 调试层级。
- 已将该启动条件提炼为独立的小型判断函数，使 PostgreSQL 路径与 SQLite 现有语义保持清晰对齐，也便于后续继续回归验证。
- 已补充针对该判断逻辑的单元测试，并完成全量 `go test ./...` 验证。

### 2. 📂文件变更清单

- 新增：
  - `docs/plan/20260404-07-POSTGRES_DEBUG_SEED_BOOTSTRAP_FIX.md`
- 修改：
  - `internal/adapters/outbound/vldb_postgres/schema.go`
  - `internal/adapters/outbound/vldb_postgres/dialect_test.go`
- 删除：
  - 无

### 3. 💻关键代码调整详情

- `internal/adapters/outbound/vldb_postgres/schema.go`
  - 在 `ensureDebugSeedWorkspace` 中新增项目数量统计。
  - 当项目数大于 0 时直接跳过默认调试层级补种，避免对已有工作区产生副作用。
  - 新增 `shouldSeedDebugWorkspace`，将“是否允许补种”的启动条件固定为可复用逻辑。
- `internal/adapters/outbound/vldb_postgres/dialect_test.go`
  - 新增 `TestShouldSeedDebugWorkspace`，覆盖空工作区与非空工作区两类行为，确保后续修改不会再次放宽启动条件。

### 4. ⚠️遗留问题与注意事项

- 本次修复仅处理 PostgreSQL 组合库默认调试种子的启动条件问题，没有扩展或改写其他 schema/bootstrap 逻辑。
- 当前 `syncDebugSeedSequences` 仍会在启动阶段执行；本次未调整其行为，因为该逻辑本身不属于本次审查意见指出的问题范围。
- 已执行并通过：
  - `go test ./...`
