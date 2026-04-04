# PostgreSQL 组合库未完成项续作计划

## 1. 任务目标

在当前已完成 PostgreSQL 组合库基础骨架、配置接入、方言路由与最小统一记忆读写能力的基础上：

1. 先整理并提交当前所有应纳入版本控制的代码与文档变更
2. 分析后续多轮评审产生的计划与完成记录，识别仍未关闭的缺口
3. 继续实现当前任务中尚未完成、且已具备明确设计输入的关键链路
4. 保持 `split` 模式零回归，不引入破坏现有行为的临时兼容分支

## 2. 当前已知背景

- 已存在 PostgreSQL Dialect Pattern 总体方案计划
- 当前仓库已新增 `internal/adapters/outbound/vldb_postgres`
- 当前已支持：
  - `storage.mode=combined`
  - `storage.combined_provider=postgres`
  - `postgres.flavor=paradedb|standard`
  - 共享 schema 初始化
  - 基础向量检索
  - 基础 lexical 检索
  - 统一记忆最小读写
  - 基础 scope/user/project 解析

## 3. 本轮执行步骤

### 3.1 提交前整理

1. 检查工作区状态
2. 清理 `.gocache`、`.gomodcache` 等不应提交的临时产物
3. 确认需要提交的代码与文档范围

### 3.2 评审结果分析

1. 阅读本轮之后新增的 `docs/completed/20260404-05` 到 `20260404-09` 文档
2. 归纳这些评审和修复记录中已经关闭的问题
3. 识别仍然未关闭、且会阻塞组合库继续推进的缺口

### 3.3 当前代码提交

1. 将当前应纳入版本控制的代码与文档加入暂存区
2. 生成一次清晰的阶段性提交
3. 保留后续实现空间，不在本次提交中夹带缓存或无关文件

### 3.4 未完成项续作

基于评审分析结果，优先补齐以下尚未完成且影响主链路的部分：

1. PostgreSQL 组合库仍未实现的关键关系工作流
2. 直接阻塞运行时使用的核心端口实现
3. 与当前架构计划不一致的缺失或半实现行为

优先级原则：

- 先主链路
- 先运行时阻塞项
- 先已有清晰设计输入的部分

### 3.5 验证

至少执行：

1. `go test ./internal/adapters/outbound/vldb_postgres ./internal/config ./internal/app ./internal/app/usecase`
2. `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
3. `go test ./...`

## 4. 技术策略

### 4.1 提交策略

- 提交当前真实代码成果
- 不提交缓存目录
- 文档状态与代码状态保持一致

### 4.2 续作策略

- 严格沿用统一 `postgres` 适配器 + `flavor` 路由设计
- 不回退到双存储兼容式查询
- 不重新引入 `vector_json` 目标库存储
- 尽量优先实现共享层逻辑，再在必要点做 flavor 分叉

## 5. 验收标准

### 5.1 提交验收

- 当前代码已形成一次干净提交
- 无缓存目录进入版本控制

### 5.2 分析验收

- 能明确说明多轮评审后剩余未完成项
- 能区分“已完成修复”和“仍待实现能力”

### 5.3 实现验收

- 至少关闭一批当前仍未完成的核心 PostgreSQL 组合库阻塞项
- 所有新增改动通过相关测试
- 计划文档与实际状态保持同步

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已完成对后续多轮评审文档的复核，确认 `20260404-05` 到 `20260404-09` 中描述的运行时主链路、review 修复和画像目标回归问题已经进入当前代码，不再是本轮真正阻塞项。
- 已补齐当前最明显未闭环的运维能力：
  - 新增 `debug-migrate split-to-combined` 命令入口；
  - 新增 PostgreSQL 组合库的 `debug-clean postgres` 清理入口；
  - 新增 split 模式 SQLite 到 PostgreSQL 组合库的受管数据导出/导入链路。
- 迁移实现严格遵守既定红线：
  - 以 SQLite 作为唯一事实主源；
  - 不依赖 LanceDB 作为迁移源；
  - 在 Go 内存中解析 `vector_json` 为 `[]float32` 后，直接写入 PostgreSQL 原生 `embedding` 向量列；
  - 目标库中不重新引入 `vector_json` 冗余字段。
- 已补充 README 中的调试清理与调试迁移说明，并完成定向测试与全量回归测试。

### 2. 📂文件变更清单

- 修改：`README.md`
- 修改：`cmd/vmm-local/main.go`
- 修改：`cmd/vmm-local/debug_clean.go`
- 修改：`cmd/vmm-local/debug_clean_test.go`
- 新增：`cmd/vmm-local/debug_migrate.go`
- 新增：`cmd/vmm-local/debug_migrate_test.go`
- 新增：`internal/adapters/outbound/vldb_sqlite/debug_export.go`
- 新增：`internal/adapters/outbound/vldb_postgres/debug_clean.go`
- 新增：`internal/adapters/outbound/vldb_postgres/debug_migrate.go`
- 新增：`internal/platform/storagemigrate/snapshot.go`

### 3. 💻关键代码调整详情

- `cmd/vmm-local`
  - `main.go` 新增 `-debug-migrate` 参数，并显式禁止与 `-debug-clean` 同时执行。
  - 新增 `debug_migrate.go`，支持 `split-to-combined` 调试迁移动作，输出稳定的逐表迁移统计。
  - 扩展 `debug_clean.go`，支持 `postgres` 清理目标；`all` 现在覆盖 `sqlite + lancedb + postgres`。
- `internal/adapters/outbound/vldb_sqlite/debug_export.go`
  - 新增 SQLite 受管快照导出能力。
  - 对高体量表按 `id` 或 `rowid` 分批导出，避免单次巨大 JSON 查询。
  - 在导出阶段就把 `vector_json` 解析为 `[]float32`，形成标准化迁移快照。
- `internal/adapters/outbound/vldb_postgres/debug_migrate.go`
  - 新增 PostgreSQL 组合库快照导入能力。
  - 在单事务内先 `TRUNCATE ... RESTART IDENTITY CASCADE` 清空受管表，再按外键顺序批量导入。
  - 对向量维度做显式校验，防止损坏的 SQLite `vector_json` 被静默写入目标库。
  - 导入完成后同步 identity sequence，并恢复 `postgres_combined` 与 flavor 搜索版本记录。
- `internal/adapters/outbound/vldb_postgres/debug_clean.go`
  - 新增 PostgreSQL 受管 schema 清理辅助逻辑，按受管表名单执行 `DROP TABLE IF EXISTS ... CASCADE`。
- `internal/platform/storagemigrate/snapshot.go`
  - 引入迁移共享快照与统计结构，避免 CLI 层直接耦合各适配器内部实现细节。
- 文档与测试
  - `README.md` 新增 `debug-clean postgres` 和 `debug-migrate split-to-combined` 说明。
  - `cmd/vmm-local/debug_clean_test.go` 扩展 PostgreSQL 清理目标解析测试。
  - `cmd/vmm-local/debug_migrate_test.go` 新增迁移动作解析测试。

### 4. ⚠️遗留问题与注意事项

- 当前仅实现 `split-to-combined` 单向迁移，不包含反向迁移或增量双写切换能力。
- 迁移目标仍依赖 PostgreSQL 侧扩展满足当前 flavor 要求：
  - `paradedb` 需要 `pg_search + vector`
  - `standard` 需要 `pg_trgm + vector`
- 迁移命令不会读取 LanceDB；这是有意设计，因为 SQLite 才是当前 split 模式的事实主源。
- 本轮已完成代码与文档实现及测试验证，但尚未对本轮新增改动再次创建新的 git 提交；当前仓库仍处于有未提交变更状态。

### 5. ✅验证记录

- `go test ./cmd/vmm-local ./internal/adapters/outbound/vldb_postgres ./internal/adapters/outbound/vldb_sqlite`
- `go test ./internal/adapters/outbound/vldb_postgres ./internal/config ./internal/app ./internal/app/usecase`
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
- `go test ./...`
