## 任务目标

根据本轮代码审查结论，修复向量重建链路中的两个确定性问题：

1. 修复 split 模式向量重建在 embedding 返回错误维度时可能先写入 SQLite、再在 LanceDB 阶段失败，导致 durable 与向量库状态分裂的问题。
2. 修复 combined PostgreSQL 模式向量维度迁移时，先提交列重建事务、再单独重建 ANN 索引，导致索引缺失可能被部分提交的问题。
3. 重新核对“维护工具是否会在空库上隐式补种默认工作区”这一行为是否符合当前仓库预期，并明确本次是否需要改动。

## 详细执行步骤

1. 梳理当前向量重建链路
   - 核对 `cmd/vmm-migrate`、`internal/app/vector_rebuild.go`、SQLite/PostgreSQL 维护端口实现。
   - 明确错误维度当前是在什么阶段暴露、是否已经发生 durable 写入。
   - 核对 PostgreSQL 维度迁移与 ANN 索引重建的事务边界。

2. 修复 split 模式错误维度保护
   - 在向量重建编排路径中增加“embedding 结果维度必须等于当前配置维度”的严格校验。
   - 确保校验发生在任何 durable 写回之前，避免 SQLite 先写入错误向量。
   - 补充对应单元测试，覆盖错误维度直接失败且不会触发持久化的场景。

3. 修复 combined 模式索引原子性问题
   - 调整 PostgreSQL 向量维度迁移实现，使 ANN 索引重建不再落在事务提交之后的独立失败窗口内。
   - 复用或抽取统一的向量索引 SQL 构造逻辑，避免后续索引定义漂移。
   - 补充测试，覆盖迁移 SQL 与索引 SQL 的一致性约束。

4. 复核“默认工作区补种”行为
   - 核对 `BuildMaintenanceDependencies` 复用的存储构造路径与 SQLite/PostgreSQL 启动补种逻辑。
   - 结合仓库既有启动约束和本次用户说明，确认该行为是否为预期能力而非缺陷。
   - 若确认是预期，则仅在总结中说明，不改动代码。

5. 验证与收尾
   - 运行与本次改动直接相关的 Go 测试。
   - 对照计划逐项自检。
   - 在计划文件末尾补充执行变更总结后，迁移到 `docs/completed/20260407/`。

## 技术选型及实现原则

1. 第 1 项优先在应用编排层前置校验，而不是依赖下游存储失败后补救，确保错误在第一次 durable 写入之前暴露。
2. 第 3 项优先保证迁移步骤的原子性，避免 schema 已切换但 ANN 索引缺失的部分完成状态。
3. 不对第 2 项做主观推翻，必须先依据实际代码链路与仓库约定确认其是否为既定行为。
4. 所有新增或调整代码继续遵守仓库现有的中英文双语注释要求。

## 验收标准

1. split 模式下，如果 embedding 返回的向量维度与 `cfg.Embedding.Dimension` 不一致，命令会在 durable 写回前直接失败。
2. combined PostgreSQL 模式下，向量列维度迁移与 ANN 索引重建之间不再存在“事务已提交但索引未恢复”的部分失败窗口。
3. 相关单元测试已补充并通过。
4. 已明确说明第 2 项在当前仓库中是否属于预期行为，并据此决定是否改动。

## 执行变更总结

### 1. 核心修复与调整概述

- 在向量重建编排层增加了 embedding 结果维度前置校验，确保 split/combined 两条维护路径都会在任何 durable 写回前拦截错误维度，避免出现“SQLite 已写、LanceDB 未完成”的半迁移状态。
- 调整 PostgreSQL 向量维度迁移流程，把 ANN 索引重建纳入事务提交前的同一原子阶段，消除了 schema 已提交但索引缺失的失败窗口。
- 复核后确认：第 2 项所提到的“空库补种默认工作区”确实是当前仓库共享启动链路的一部分，维护工具复用该链路会继承这一行为；结合本次说明，这里暂按预期能力处理，不做改动。

### 2. 📂文件变更清单

- 新增
  - `docs/plan/20260407-01-VECTOR_REBUILD_REVIEW_FIXES.md`
- 修改
  - `internal/app/vector_rebuild.go`
  - `internal/app/vector_rebuild_test.go`
  - `internal/adapters/outbound/vldb_postgres/schema.go`
  - `internal/adapters/outbound/vldb_postgres/vector_dimension_migration.go`
  - `internal/adapters/outbound/vldb_postgres/vector_dimension_migration_test.go`

### 3. 💻关键代码调整详情

- `internal/app/vector_rebuild.go` 新增 `validateVectorRebuildDimensions`，并在 `embedVectorRebuildBatch` 返回后、`ReplaceMemoryVectors` 调用前执行严格维度校验。
- `internal/app/vector_rebuild_test.go` 补充错误维度回归测试，同时把既有 split/combined 成功路径测试的目标维度调整为与测试向量长度一致。
- `internal/adapters/outbound/vldb_postgres/schema.go` 抽取共享的 ANN 索引 SQL 构造函数，避免启动初始化与维护重建使用不同索引定义。
- `internal/adapters/outbound/vldb_postgres/vector_dimension_migration.go` 改为在事务内完成索引重建，再统一提交迁移事务。
- `internal/adapters/outbound/vldb_postgres/vector_dimension_migration_test.go` 补充索引 SQL 构造测试，确保索引名称、目标表和 `lists` 参数保持稳定。

### 4. ⚠️遗留问题与注意事项

- 第 2 项我已按代码链路确认：`BuildMaintenanceDependencies` 通过 `buildStorageDependencies` 复用正式存储构造，SQLite 会落到 `bootstrapCurrentSQLiteSchema -> seedDebugWorkspaceIfEmpty`，PostgreSQL 会落到 `ensureDebugSeedWorkspace`；因此维护工具在空库上补种默认层级是当前共享 bootstrap 行为，而不是这次修复引入的新偏差。
- 本次只运行了与改动直接相关的定向测试：`go test ./cmd/vmm-migrate ./internal/app ./internal/adapters/outbound/vldb_postgres`。未额外执行 `go test ./...`。
