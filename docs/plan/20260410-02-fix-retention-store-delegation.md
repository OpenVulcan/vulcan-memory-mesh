# 排查同类问题：PostgreSQL 组合存储缺失接口委托方法

## 问题描述

运行时启动报错：`build app: relational store does not support retention maintenance`

根因：`buildCombinedStore()` 返回的 `*vldb_postgres.Store` 没有实现 `RetentionStore` 接口的 12 个方法。PostgreSQL 的 retention 相关方法定义在内部的 `*retentionRepository` 和 `*vectorRepository` 上，而不是主 `*Store` 类型上。

## 同类问题排查

系统性地排查了 `runtime_storage.go` 和 `vector_rebuild.go` 中所有对 relational store 做类型断言的接口，共 10 个接口：

| 接口 | 断言位置 | PostgreSQL `*Store` 是否实现 | 状态 |
|---|---|---|---|
| WorkspaceStore | runtime_storage.go:83 | 是 | OK |
| SchemaVersionStore | runtime_storage.go:87 | 是 | OK |
| ProfileStore | runtime_storage.go:91 | 是 | OK |
| MemoryStore | runtime_storage.go:95 | 是 | OK |
| ChatCompactStore | runtime_storage.go:99 | 是 | OK |
| ScratchpadStore | runtime_storage.go:103 | 是 | OK |
| ScratchpadMaintenanceStore | runtime_storage.go:107 | 是 | OK |
| RetentionStore | runtime_storage.go:111 | **缺失** | 已修复 |
| MemoryVectorRebuildStore | vector_rebuild.go:72 | 是（已有委托） | OK |
| MemoryVectorDimensionMigrationStore | vector_rebuild.go:98 | **缺失** | 已修复 |

发现 **2 个同类问题**：
1. `RetentionStore` — 12 个方法缺失（已在上一个修复中处理）
2. `MemoryVectorDimensionMigrationStore` — `RebuildMemoryVectorDimensions` 方法定义在 `*vectorRepository` 上，不在 `*Store` 上

## 修复方案

在 `retention_delegation.go` 中：
- 添加 12 个 RetentionStore 委托方法（8 个 → `repos.retention`，4 个 → `repos.vector`）
- 添加 1 个 `MemoryVectorDimensionMigrationStore` 委托方法（`RebuildMemoryVectorDimensions` → `repos.vector`）
- 添加编译期接口断言 `var _ appports.RetentionStore = (*Store)(nil)` 和 `var _ appports.MemoryVectorDimensionMigrationStore = (*Store)(nil)`，防止将来再次出现同类问题

## 验证

- `go build ./...` 编译通过（含编译期接口断言）
- `go test ./... -count=1` 全部 21 个有测试的包通过
- `go vet` 无告警
