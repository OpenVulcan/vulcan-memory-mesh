## 任务目标

为当前数据库增加一个“向量重建”独立迁移工具，用于在切换新的 embedding 模型后，按当前数据库中的有效记忆重新生成向量并写回向量存储。该工具必须脱离主服务启动流程，作为一次性维护命令执行；执行前要求操作者进行二次确认；执行时不再额外输入 RPM，而是直接遵循当前配置里的 embedding 路由/Key 预算；当所有 Key 都因预算耗尽而暂时不可用时，工具需自动等待 30 秒后继续重试；当存储为分离模式时，需要把 SQLite 与 LanceDB 一并完整重建同步；当存储为合并模式时，需要直接重建 PostgreSQL 的 embedding 列。同时，把当前 `vmm-local` 中的一次性数据库清理入口迁移到该独立工具内，统一维护动作入口。

## 详细执行步骤

1. 盘点现有能力与边界  
   - 梳理现有 `debug-clean`、`debug-migrate` 和启动期 `vector schema rebuild` 的实现位置。  
   - 确认可复用的依赖装配方式，包括配置加载、embedding 客户端构建、向量存储构建、工作区记忆遍历能力。  
   - 明确本次不改动正常 gRPC 启动链路，只新增独立维护入口。

2. 设计独立迁移工具入口  
   - 新增独立 `cmd` 工具入口，使其与主程序分离。  
   - 设计启动参数，统一承接数据库清理、已有迁移动作与新的向量重建动作。  
   - 参数至少包含配置路径、动作选择与确认流程；向量重建不额外暴露 RPM 参数。  
   - 保持与现有仓库分层一致，避免把一次性维护逻辑耦合进业务用例主链路。

3. 实现向量重建流程  
   - 从关系存储枚举当前数据库中的项目与有效记忆。  
   - 使用当前配置中的 embedding 模型重新生成向量。  
   - 在合并模式下，直接按 `vector_id` 重写 PostgreSQL `embedding` 列。  
   - 在分离模式下，先按 `vector_id` 重写 SQLite `vector_json`，再重建 LanceDB 表并全量回灌，确保两边完全同步。  

4. 实现配置驱动的预算等待  
   - 直接复用当前配置里的 embedding 路由与 Key 预算能力，不新增独立 RPM 参数。  
   - 当 embedding 调用因为“所有候选 Key 都超出预算”而无法继续时，自动等待 30 秒后重试。  
   - 保证预算耗尽属于可恢复等待，而不是直接中断整次重建。

5. 迁移现有清理入口并统一安全保护  
   - 把 `vmm-local` 里的数据库清理入口迁移到独立工具。  
   - 评估并按最小破坏原则处理现有调试迁移入口的复用或归并。  
   - 执行破坏性重建前输出摘要并要求操作者输入 `Y` 确认。  
   - 若未确认，则立即安全退出，不进行实际删除或写入。  
   - 在执行过程中输出必要的进度与结果统计，便于运维确认执行状态。

6. 补齐测试与文档  
   - 为参数解析、确认流程、RPM 校验、LanceDB 特殊处理以及核心重建逻辑补充单元测试。  
   - 更新 README 等相关文档，说明独立工具的用途、参数和注意事项。  
   - 按仓库规则执行规定范围的 Go 测试。

## 技术选型及实现原则

1. 独立工具优先采用新增 `cmd` 可执行入口，并逐步接管 `vmm-local` 中的一次性维护参数，以降低运维误触发和运行时耦合风险。  
2. 重建过程中以关系库存储的数据为事实源，重新调用 embedding，而不是复用旧向量。  
3. RPM 限速采用进程内节流器实现，保证行为稳定、可测试且不依赖外部服务。  
4. 对 LanceDB 采用“先清理/重建表，再重新写入”的策略，确保换模型时维度和表状态一致。  
5. 所有新增源码与关键逻辑遵守仓库要求的中英文双语注释规范。

## 验收标准

1. 仓库中存在一个与主程序分离的独立迁移工具，可单独执行向量重建，并承接数据库清理等一次性维护动作。  
2. 工具执行前必须要求输入 `Y` 二次确认，未确认不得执行实际重建。  
3. 工具运行时不会启动 gRPC 主服务。  
4. 分离模式下会先重建 SQLite durable 向量，再重建 LanceDB 并完成全量同步回灌。  
5. 合并模式下会直接重建 PostgreSQL 的 `embedding` 列，而不是误走 LanceDB 路径。  
6. 当所有候选 Key 都因预算耗尽不可用时，工具会自动等待 30 秒后继续，而不是立即失败退出。  
7. 相关测试与文档已同步更新，并完成仓库要求的最少测试验证。

## 执行变更总结

### 1. 核心修复与调整概述

- 新增独立维护工具 `vmm-migrate`，统一承接清库、`split-to-combined` 迁移和新的向量重建动作，不再通过 `vmm-local` 承载一次性破坏性维护入口。  
- 新增“维度迁移型向量重建”主流程：按当前配置重新生成有效长期记忆向量；`split` 模式下同步重写 SQLite durable 向量并重建 LanceDB；`combined` 模式下先重建 PostgreSQL `embedding` 列维度，再回填有效向量。  
- 向量重建不再要求独立 RPM 参数，而是直接复用当前 embedding 配置；当所有候选 Key 都因预算耗尽不可用时，自动等待 30 秒后继续。  
- 标准构建链路已补齐 `vmm-migrate.exe` 产物，并修正 `make.ps1` 在 Windows PowerShell 下自动重进 `pwsh` 的兼容行为，保证 `.\make.ps1 build` 与 `.\make.bat build` 都能生成维护工具。  

### 2. 📂文件变更清单

- 新增  
  - `cmd/vmm-migrate/main.go`  
  - `cmd/vmm-migrate/clean.go`  
  - `cmd/vmm-migrate/migrate.go`  
  - `cmd/vmm-migrate/vector_rebuild.go`  
  - `cmd/vmm-migrate/clean_test.go`  
  - `cmd/vmm-migrate/migrate_test.go`  
  - `cmd/vmm-migrate/vector_rebuild_test.go`  
  - `internal/app/maintenance.go`  
  - `internal/app/vector_rebuild.go`  
  - `internal/app/vector_rebuild_test.go`  
  - `internal/adapters/outbound/vldb_postgres/vector_dimension_migration.go`  
  - `internal/adapters/outbound/vldb_postgres/vector_dimension_migration_test.go`  
- 修改  
  - `README.md`  
  - `cmd/vmm-local/main.go`  
  - `internal/app/ports/interfaces.go`  
  - `internal/adapters/outbound/ai_key_failover/errors.go`  
  - `internal/adapters/outbound/vldb_sqlite/store.go`  
  - `internal/adapters/outbound/vldb_sqlite/store_test.go`  
  - `internal/adapters/outbound/vldb_postgres/memory_store.go`  
  - `scripts/vmm.ps1`  
  - `make.ps1`  
- 删除  
  - `cmd/vmm-local/debug_clean.go`  
  - `cmd/vmm-local/debug_clean_test.go`  
  - `cmd/vmm-local/debug_migrate.go`  
  - `cmd/vmm-local/debug_migrate_test.go`  

### 3. 💻关键代码调整详情

- 应用层新增维护依赖装配与 `RunVectorRebuild` 编排逻辑，统一处理配置加载、工作区遍历、批量 embedding、预算耗尽重试、向量持久化与向量库回灌统计。  
- SQLite 新增 `ReplaceMemoryVectors`，按 `vector_id` 原地重写 durable `vector_json`；PostgreSQL 新增按 `vector_id` 重写 `embedding` 的能力，并补充向量维度重建 SQL。  
- PostgreSQL 维度迁移会重建 `noise_embeddings`、`memory_nodes`、`memory_nodes_trash` 的向量列结构，其中只有 active 且未过期的长期记忆会在重建流程里重新生成语义向量，垃圾箱仅做结构维持，不做语义重建。  
- `vmm-local` 移除了旧 `debug-clean` / `debug-migrate` 参数入口；README 改为说明 `vmm-migrate` 的独立使用方式、二次确认要求、`split`/`combined` 差异及预算耗尽等待行为。  
- 构建脚本新增 `vmm-migrate.exe` 产物，并保证在当前仓库默认 Windows 环境下直接执行 `.\make.ps1 build` 也会自动切换到 `pwsh` 完成构建。  

### 4. ⚠️遗留问题与注意事项

- 本次向量重建目标明确围绕“embedding 模型切换后的维度迁移”；当前实现会在 `combined` 模式下执行列维度重建后再回填有效向量，适用于需要统一迁移维度的场景。  
- 垃圾箱数据不会参与语义重建；在 PostgreSQL 组合模式下仅同步其列结构，避免后续运行时因维度不一致失败。  
- 预算耗尽等待采用固定 30 秒重试窗口；若后续需要更细粒度的退避或可观测性，可在维护工具内继续扩展。  
- 已完成并验证：定向测试、仓库要求的最小测试集、`go test ./...`、`.\make.ps1 build`。  
