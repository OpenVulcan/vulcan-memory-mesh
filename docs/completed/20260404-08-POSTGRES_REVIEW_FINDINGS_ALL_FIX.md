# 任务计划：修复 PostgreSQL 审查意见中的配置与启动一致性问题

## 1. 任务目标

针对本次审查确认的 4 个有效问题完成修复，确保：

- `EnsureProjectPath` 在并发创建相同 Team/Space/Project 路径时保持幂等，不会因唯一约束竞争直接失败
- `storage.mode=split` 时不会再因为未启用的 `storage.combined_provider` 配置而启动失败
- PostgreSQL 组合库在空项目库但存在同名 `default` 层级数据时，启动补种逻辑不会因名称唯一约束冲突而失败
- 显式配置 `postgres.min_idle_conns=0` 时，归一化逻辑能够保留该值，不再被静默改写为 `1`

本次任务仅修复审查意见对应的问题，不额外扩展 PostgreSQL 组合库的功能边界。

## 2. 执行步骤

1. 梳理问题落点与现有契约
   - 复查 `internal/config/config.go`
   - 复查 `internal/adapters/outbound/vldb_postgres/workspace_admin.go`
   - 复查 `internal/adapters/outbound/vldb_postgres/schema.go`
   - 对照现有 SQLite 路径与配置装配语义，确认修复后的预期行为

2. 修复配置层问题
   - 调整 `storage.combined_provider` 的校验时机，仅在 `storage.mode=combined` 时强校验
   - 调整 `postgres.min_idle_conns` 的 Normalize 逻辑，保留显式的 `0`
   - 补充配置层回归测试

3. 修复 PostgreSQL 组合库的并发与补种问题
   - 让 Team/Space/Project 创建 SQL 具备并发幂等能力
   - 重构默认调试种子补种逻辑，按业务唯一键解析或创建默认层级，而不是只依赖固定 id
   - 保证全新空库仍保留原有可预测的默认层级启动语义

4. 执行回归验证
   - 运行 `go test ./internal/config ./internal/adapters/outbound/vldb_postgres ./internal/app/usecase`
   - 运行 `go test ./...`
   - 若发现与本次修复直接相关的问题，继续修正直到通过

## 3. 技术选型

- 并发创建修复优先采用 PostgreSQL 原生 `INSERT ... ON CONFLICT ... RETURNING`
  - 原因：这样可以在单条语句内同时处理“首次创建”和“并发已存在”两种路径，避免事务因唯一键异常进入 aborted 状态
- 默认调试种子改为“按业务唯一键解析或创建”
  - 原因：调试层级是否可用的关键在于 `default` 名称层级存在，而不是行 id 必须由显式插入固定值产生
- 配置修复继续遵循“split 模式不约束 combined 配置、combined 模式严格校验”
  - 原因：这样最符合配置字段的实际生效范围，也能兼容已有 split 部署

## 4. 验收标准

- 相同项目路径被并发创建时，不再因唯一约束冲突直接报错
- `storage.mode=split` 且 `storage.combined_provider` 为非 `postgres` 时，配置校验不会失败
- `postgres.min_idle_conns=0` 经过 Normalize 后仍为 `0`
- PostgreSQL 组合库在“无项目但已有同名 default 层级”的场景下启动，不会因默认补种失败
- 定向测试与 `go test ./...` 全部通过
- 计划文件补充完整的执行变更总结后迁移到 `docs/completed/`

## 执行变更总结

### 1. 核心修复与调整概述

- 已完成 4 条审查意见的全部修复，覆盖配置校验时机、连接池最小空闲连接归一化、项目路径并发创建幂等性，以及调试默认层级补种的名称冲突处理。
- 配置侧已保证 `split` 模式不再受未启用的 `combined_provider` 残留配置影响，同时保留显式配置的 `postgres.min_idle_conns=0`。
- PostgreSQL 组合库存储侧已将 Team、Space、Project 以及 debug seed 默认层级的创建逻辑统一改为基于业务唯一键的 upsert/返回路径，避免并发或历史同名数据导致启动和创建流程失败。
- 已完成定向测试与全量回归测试，当前修复范围内未发现新的阻断性问题。

### 2. 📂文件变更清单

- 修改：`internal/config/config.go`
- 修改：`internal/config/config_test.go`
- 修改：`internal/adapters/outbound/vldb_postgres/helpers.go`
- 修改：`internal/adapters/outbound/vldb_postgres/schema.go`
- 修改：`internal/adapters/outbound/vldb_postgres/workspace_admin.go`
- 修改：`internal/adapters/outbound/vldb_postgres/dialect_test.go`
- 修改：`docs/plan/20260404-08-POSTGRES_REVIEW_FINDINGS_ALL_FIX.md`

### 3. 💻关键代码调整详情

- 在 `internal/config/config.go` 中调整 `Normalize` 与 `Validate`：
  - 将 `postgres.min_idle_conns` 的归一化条件从“小于等于 0”收敛为“仅小于 0 时回退默认值”，保留显式 `0`。
  - 将 `storage.combined_provider` 的合法性校验下沉到 `storage.mode=combined` 分支，仅在实际启用组合库时强制要求 `postgres`。
- 在 `internal/config/config_test.go` 中补充配置回归测试：
  - 覆盖 `split` 模式下忽略未使用 `combined_provider` 的行为。
  - 覆盖 `Normalize` 对显式 `postgres.min_idle_conns=0` 的保留行为。
- 在 `internal/adapters/outbound/vldb_postgres/workspace_admin.go` 中，将 Team/Space/Project 的插入 SQL 改为 `INSERT ... ON CONFLICT ... RETURNING`：
  - Team 按 `name` 处理冲突。
  - Space 按 `(team_id, name)` 处理冲突。
  - Project 按 `(space_id, name)` 处理冲突并返回现存记录，确保并发创建路径保持幂等。
- 在 `internal/adapters/outbound/vldb_postgres/schema.go` 中重构 debug seed 默认层级补种逻辑：
  - 移除仅依赖固定 id 的插入策略。
  - 新增按业务唯一键执行 upsert 并返回 id 的辅助方法，兼容“已有同名 default 层级但项目表为空”的启动场景。
- 在 `internal/adapters/outbound/vldb_postgres/dialect_test.go` 中补充 SQL 级断言测试，验证 upsert 语句确实使用了预期的冲突键。
- 已执行验证：
  - `go test ./internal/config`
  - `go test ./internal/adapters/outbound/vldb_postgres`
  - `go test ./internal/app/usecase`
  - `go test ./...`

### 4. ⚠️遗留问题与注意事项

- 当前工作区存在本次任务之外的未提交改动与缓存目录（如 `go.mod`、`go.sum`、`internal/app/app.go`、`.gocache/`、`.gomodcache/` 等），本次修复未对这些内容做回退或清理。
- 本次修复严格聚焦审查意见本身，未扩展 PostgreSQL 组合库的功能边界，也未重新引入仓库说明中禁止恢复的 SaaS/旧存储路径。
