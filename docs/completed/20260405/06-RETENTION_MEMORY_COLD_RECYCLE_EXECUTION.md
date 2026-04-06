# 冷状态记忆回收与独立 Retention 维护器执行计划

## 1. 任务目标

本阶段目标是把“记忆替代只退出热召回、不退出热主表”的半闭环补成真正可控的冷数据治理主链，具体包括：

1. 为 `memory_nodes` 的冷状态数据提供持久化回收站表与批次记录。
2. 实现独立于 `post-action` 队列的 retention 维护器，按配置周期扫描并回收冷状态记忆。
3. 第一阶段仅处理 `superseded / expired / deleted` 的记忆及其上下文边，不启动 `turn` 归档、不提供产品级恢复能力。
4. 保持 PostgreSQL 与 SQLite 语义一致，不把本阶段做成 PostgreSQL-only。

## 2. 范围边界

### 2.1 本阶段必须完成

1. 新增记忆回收批次与回收站结构。
2. 新增冷状态记忆扫描与批次回收存储方法。
3. 新增独立 retention 维护器，并接入应用启动与优雅停机。
4. 新增最小可用测试，覆盖：
   - 冷状态记忆迁入回收站；
   - 主表记忆与上下文边删除；
   - SQLite 词法索引清理；
   - 维护器按配置启停。

### 2.2 本阶段明确不做

1. 不做 `turn_records` 冷归档。
2. 不做 session 全量回收。
3. 不做任何产品级恢复接口、命令或手动恢复工作流。
4. 不把 `vmm_vector_gc_jobs` 作为 PostgreSQL 主链阻塞项；PostgreSQL 组合存储模式下，主表删除即可覆盖向量清理语义。

## 3. 技术方案

### 3.1 存储层设计

1. 保留现有 `vmm_recycle_batches` 作为回收批次主表。
2. 新增：
   - `vmm_memory_nodes_trash`
   - `vmm_memory_context_edges_trash`
3. 回收站表需镜像主表关键字段，并额外记录：
   - `batch_id`
   - `recycled_at`
   - `recycle_reason`
4. PostgreSQL 直接补管理 schema。
5. SQLite 通过 schema 版本升级和增量迁移补齐表结构。

### 3.2 回收判定规则

1. 第一阶段仅扫描：
   - `memory_status IN ('superseded', 'expired', 'deleted')`
2. 终态记忆一经扫描命中即可迁入回收站，不再把 `trash_retention` 复用为“进入回收站前观察期”。
3. `trash_retention` 只用于控制回收站批次的延迟硬删除窗口。
4. 不动仍处于 `active + unexpired` 的记忆。
5. 默认尊重保护配置：
   - `protect_priority_floor`
   - `protect_memory_level_floor`
   - `skip_protected_shared_memories`

### 3.3 事务执行语义

对一批待回收记忆，在同一关系事务内执行：

1. 插入 `vmm_recycle_batches`。
2. 复制 `memory_nodes` 到 `vmm_memory_nodes_trash`。
3. 复制对应 `memory_context_edges` 到 `vmm_memory_context_edges_trash`。
4. 删除主表 `memory_context_edges`。
5. 删除主表 `memory_nodes`。
6. SQLite 同事务删除 `vmm_memory_nodes_fts` 对应行。

事务提交后：

1. SQLite / LanceDB 组合模式下，按已删除记忆的 `vector_id` 做最佳努力向量删除。
2. PostgreSQL 组合存储模式下不额外做向量 GC。
3. 后台维护器会继续对 `recycled_at <= now - trash_retention` 的批次做永久 purge。

### 3.4 运行时接线

1. 新增独立 retention 维护器，不复用 `post-action` 的 30 秒扫描循环。
2. 维护器受以下配置控制：
   - `retention.enabled`
   - `retention.recycle_scan_interval`
3. 维护器首版只执行冷状态记忆回收。
4. 应用关闭时要参与统一 `Shutdown`，避免后台循环泄露。

## 4. 执行步骤

1. 梳理当前 `retention` 配置、生命周期规则与存储实现，确认新接口落点。
2. 扩展领域模型与应用端口，增加回收批次与冷状态记忆回收结果结构。
3. 落 PostgreSQL 与 SQLite 的回收站表结构。
4. 实现存储层冷状态记忆扫描、批次回收与删除逻辑。
5. 实现独立 retention 维护器并接入 `app` 组合根。
6. 补齐测试与必要文档同步。
7. 逐项自检，通过后补写执行变更总结并归档到 `docs/completed/`。

## 5. 验收标准

1. 启用 retention 后，后台会按 `recycle_scan_interval` 周期扫描冷状态记忆。
2. 满足条件的 `superseded / expired / deleted` 记忆会迁入回收站，并从热主表删除。
3. 对应 `memory_context_edges` 会同步迁入回收站并从热主表删除。
4. SQLite 的 `vmm_memory_nodes_fts` 不会残留孤儿行。
5. PostgreSQL 与 SQLite 都能通过测试验证核心语义一致。
6. `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
7. `go test ./internal/adapters/outbound/vldb_sqlite ./internal/adapters/outbound/vldb_postgres`
8. `go test ./...`

## 6. 风险与注意事项

1. `turn_records` 归档继续后置，避免破坏现有 `source_turn_id -> GetTurnDetails` 契约。
2. 本阶段回收站仅作为数据库层软备份与人工防灾缓冲，不承诺产品级恢复能力。
3. `trash_retention` 在本阶段已收敛为“回收站保留时长”，不再复用为进入回收站前的冷却门槛。

## 执行变更总结

### 1. 核心修复与调整概述

1. 新增独立 `RetentionUseCase`，将冷状态记忆回收与回收站 purge 从 `post-action` 维护循环中拆出，形成独立后台治理链路。
2. 补齐 PostgreSQL 与 SQLite 的回收批次表、记忆回收站表、情境边回收站表，并实现终态记忆的事务性迁移与热表删除。
3. SQLite 回收流程已同步清理 `vmm_memory_nodes_fts`，PostgreSQL 组合存储模式下继续保持向量删除 no-op，由主表删除覆盖热路径语义。
4. `trash_retention` 已明确仅用于控制回收站延迟硬删除窗口，不再承担进入回收站前观察期的双重语义。

### 2. 📂 文件变更清单

新增：

1. `internal/logic/domain/retention.go`
2. `internal/app/usecase/retention.go`
3. `internal/app/usecase/retention_test.go`
4. `internal/adapters/outbound/vldb_postgres/retention_store.go`
5. `internal/adapters/outbound/vldb_postgres/retention_store_test.go`
6. `internal/adapters/outbound/vldb_sqlite/retention_store.go`
7. `internal/adapters/outbound/vldb_sqlite/retention_store_test.go`

修改：

1. `internal/app/ports/interfaces.go`
2. `internal/app/app.go`
3. `internal/adapters/outbound/vldb_postgres/helpers.go`
4. `internal/adapters/outbound/vldb_postgres/schema.go`
5. `internal/adapters/outbound/vldb_sqlite/store.go`
6. `internal/adapters/outbound/vldb_sqlite/schema_migrations.go`
7. `README.md`
8. `docs/plan/20260405-06-RETENTION_MEMORY_COLD_RECYCLE_EXECUTION.md`

删除：

1. 无

### 3. 💻 关键代码调整详情

1. 在应用装配阶段新增 `RetentionStore` 端口断言与 `RetentionUseCase` 接线，运行时启动独立 ticker，并参与统一优雅停机。
2. PostgreSQL 新增：
   - `vmm_memory_nodes_trash`
   - `vmm_memory_context_edges_trash`
   - 冷状态记忆 `SELECT ... FOR UPDATE SKIP LOCKED` 批次回收
   - 超期回收站批次 purge
3. SQLite 新增：
   - schema `15 -> 16` 迁移
   - 回收批次与回收站表 bootstrap
   - 单脚本事务式 `memory -> trash -> FTS 删除 -> hot table 删除`
   - 超期回收站批次 purge
4. 测试补齐：
   - retention 用例配置透传与向量清理桥接
   - SQLite recycle / purge SQL 构造
   - PostgreSQL 保护谓词与批次 project 归并辅助逻辑

### 4. ⚠️ 遗留问题与注意事项

1. `turn` 冷归档、session 全量回收、长期空闲 session 级记忆回收仍未实现，继续后置到下一阶段。
2. PostgreSQL 仍未引入 recycle batch claim / vector GC job claim 机制；本阶段通过 `FOR UPDATE SKIP LOCKED` 只覆盖了冷记忆与 trash purge 的行级并发保护。
3. 回收站仍只作为数据库层软备份缓冲，不提供产品级恢复入口。
4. `retention.turn_keep_extra_turns` 与 `retention.session_idle_recycle_after` 目前仍处于配置已落地、行为待实现状态。
5. 验证已完成：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - `go test ./internal/adapters/outbound/vldb_sqlite ./internal/adapters/outbound/vldb_postgres ./internal/config`
   - `go test ./...`
