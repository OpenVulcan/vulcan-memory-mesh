## 任务目标

根据本轮代码审查结论，修复向量重建链路里的两个 `P1` 问题：

1. 修复 combined PostgreSQL 模式在向量重建过程中先清空/重建 embedding 列、后续步骤失败时可能留下错误零向量的问题。
2. 修复 combined PostgreSQL 模式在没有 active 长期记忆时直接返回，导致向量列维度未迁移的问题。

## 详细执行步骤

1. 梳理当前 combined 向量重建链路
   - 核对 `internal/app/vector_rebuild.go` 当前的执行顺序。
   - 核对 PostgreSQL 维护端口的事务边界与列重建行为。

2. 调整应用层编排顺序
   - 让 combined 模式先完成 embedding 生成与维度校验，再进入数据库列迁移。
   - 确保 embedding 失败时不会提前触发 PostgreSQL 向量列改动。
   - 确保无 active 记忆时仍然执行维度迁移。

3. 调整 PostgreSQL 维护实现
   - 让 combined 模式的维度迁移与 active 行向量回填处于同一事务内完成。
   - 避免外部观察到“列已重建但 active 行仍是零向量”的中间态。

4. 补充与更新测试
   - 补充 combined 模式下“空数据也迁移维度”的测试。
   - 补充 combined 模式下“embedding 失败不会先做维度迁移”的测试。
   - 更新受影响的维护端口替身与断言。

5. 验证与收尾
   - 运行本次改动直接相关的 Go 测试。
   - 对照计划逐项自检。
   - 在计划文件末尾补充执行变更总结后，迁移到 `docs/completed/20260407/`。

## 技术选型及实现原则

1. 优先保证 combined 模式的正确性与原子性，不接受“部分成功、需要人工再跑一次才能恢复”的中间态。
2. 优先在维护端口内部完成数据库事务收口，而不是把原子性依赖分散到应用层多次调用。
3. 尽量把改动限定在向量重建与 PostgreSQL 维护路径，避免影响你这批未提交的其他功能改动。

## 验收标准

1. combined 模式下，embedding 生成失败时不会提前触发 PostgreSQL 向量列迁移。
2. combined 模式下，即使没有 active 长期记忆，也会完成向量列维度迁移。
3. combined 模式下，维度迁移与 active 行向量回填在同一事务内完成。
4. 定向测试通过。

## 执行变更总结

### 1. 核心修复与调整概述

- 调整了 `combined-postgres` 模式的向量重建编排顺序，改为先完成全部新向量的生成与维度校验，再进入 PostgreSQL 维度迁移与回填阶段，避免 embedding 失败时提前破坏线上向量列。
- 调整了 PostgreSQL 维护端口签名与实现，让向量列维度迁移和 active 行 embedding 回填在同一事务内完成，消除了外部读到零向量中间态的窗口。
- 修复了空 active 数据集时直接提前返回的问题；现在 `combined-postgres` 模式下即使没有 active 长期记忆，也会完成向量列维度迁移。

### 2. 📂文件变更清单

- 新增
  - `docs/plan/20260407-03-VECTOR_REBUILD_REVIEW_P1_FIXES.md`
- 修改
  - `internal/app/ports/interfaces.go`
  - `internal/app/vector_rebuild.go`
  - `internal/app/vector_rebuild_test.go`
  - `internal/adapters/outbound/vldb_postgres/vector_dimension_migration.go`
- 删除
  - 无

### 3. 💻关键代码调整详情

- `internal/app/ports/interfaces.go`
  - 把 `MemoryVectorDimensionMigrationStore` 改为接收待回填的 `records`，明确该维护端口负责“维度迁移 + active 向量原子回填”。
- `internal/app/vector_rebuild.go`
  - 新增 `materializeVectorRebuildRecords`，用于在 combined 模式下先批量完成 embedding 与维度校验。
  - 调整 `runVectorRebuildWithPorts` 流程：combined 模式下先准备重建载荷，再调用原子迁移；空数据集时也会执行迁移。
- `internal/adapters/outbound/vldb_postgres/vector_dimension_migration.go`
  - 让 `RebuildMemoryVectorDimensions` 在同一事务里完成列重建、active 行 embedding 回填和索引恢复。
- `internal/app/vector_rebuild_test.go`
  - 补充“空数据也迁移维度”与“embedding 失败前不触发迁移”的回归测试。
  - 同步更新 combined 模式替身断言，改为校验原子迁移接口收到的 active 记录。

### 4. ⚠️遗留问题与注意事项

- 本次 combined 模式为了保证原子切换，会在进入 PostgreSQL 事务前先把全部 active 记忆的新向量载荷准备到内存里；这是有意用内存换正确性的维护路径设计。
- `ReplaceMemoryVectors` 的 PostgreSQL 实现仍然保留，但当前 combined 向量重建主路径已经不再依赖它逐批回填。
- 本次已执行：
  - `go test ./cmd/vmm-migrate ./internal/app ./internal/adapters/outbound/vldb_postgres`
  - `go test ./...`
