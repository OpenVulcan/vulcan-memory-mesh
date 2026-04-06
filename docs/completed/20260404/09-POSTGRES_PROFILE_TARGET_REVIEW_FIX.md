# 任务计划：修复 PostgreSQL 画像目标解析的审查回归问题

## 1. 任务目标

修复 PostgreSQL 组合存储中 `ResolveProfileTarget` 对画像目标范围解析过严的问题，确保：

- `ProfileTypeUser` 仅依赖 `user_id` 即可完成目标解析
- `ProfileTypeProject`、`ProfileTypeTeam`、`ProfileTypeSpace` 仅依赖 `project_id` 即可完成目标解析
- PostgreSQL 组合存储与现有 SQLite 实现、用例层参数校验契约保持一致
- 增加回归测试，避免后续再次引入“无关 ID 被强制要求”的问题

## 2. 执行步骤

1. 对齐现有契约
   - 复查 `internal/app/usecase/profile.go` 中画像查询与手工画像指令的参数校验
   - 对照 `internal/adapters/outbound/vldb_sqlite/store.go` 中 `ResolveProfileTarget` 的既有行为
   - 确认 PostgreSQL 版本的最小修复边界

2. 修复 PostgreSQL 画像目标解析逻辑
   - 调整 `internal/adapters/outbound/vldb_postgres/workspace.go`
   - 按 `profileType` 分支加载必要的 user 或 project 记录
   - 保持返回的 `ProfileTargetRef` 字段含义与现有调用链兼容

3. 补充回归测试
   - 在 `internal/adapters/outbound/vldb_postgres/dialect_test.go` 或同包测试文件中增加无需真实数据库的回归测试
   - 覆盖 user 画像与 project/team/space 画像各自只依赖必要 ID 的行为

4. 运行验证
   - 运行 `go test ./internal/adapters/outbound/vldb_postgres`
   - 若测试组织或依赖变化影响相关调用链，再补充运行必要的关联测试

## 3. 技术选型

- 修复方式优先采用“按目标类型最小化依赖加载”
  - 原因：这是与现有 SQLite 契约、用例层校验和外部调用方式最一致的方案，且不会扩大 PostgreSQL 组合存储的职责边界
- 测试优先采用“抽取可纯函数化的目标组装逻辑”或“最小无数据库辅助函数测试”
  - 原因：当前仓库未引入 PostgreSQL 查询 mock 框架，优先复用现有轻量测试方式，避免为了单一回归引入额外测试依赖

## 4. 验收标准

- `ResolveProfileTarget` 不再无条件读取 user 与 project 两类记录
- `ProfileTypeUser` 在缺少 `project_id` 时仍能成功解析目标
- `ProfileTypeProject`、`ProfileTypeTeam`、`ProfileTypeSpace` 在缺少 `user_id` 时仍能成功解析目标
- PostgreSQL 相关回归测试通过
- 计划文件补充执行变更总结后迁移到 `docs/completed/`

## 执行变更总结

### 1. 核心修复与调整概述

- 已修复 PostgreSQL 组合存储中 `ResolveProfileTarget` 无条件加载 user 与 project 的回归问题。
- 当前实现已按 `profileType` 只解析必要层级：`ProfileTypeUser` 仅查用户，`ProfileTypeProject/Team/Space` 仅查项目层级。
- 已补充无需真实 PostgreSQL 连接的回归测试，明确约束“只查一次、只查正确表”的行为，防止后续再次引入无关 ID 强依赖。

### 2. 📂文件变更清单

- 新增：`docs/completed/20260404-09-POSTGRES_PROFILE_TARGET_REVIEW_FIX.md`
- 修改：`internal/adapters/outbound/vldb_postgres/workspace.go`
- 修改：`internal/adapters/outbound/vldb_postgres/dialect_test.go`

### 3. 💻关键代码调整详情

- 在 `internal/adapters/outbound/vldb_postgres/workspace.go` 中：
  - 新增 `resolveProfileTargetWithQueryer`，把画像目标解析逻辑下沉到可注入 `queryer` 的内部辅助函数。
  - 将 `ResolveProfileTarget` 改为仅做初始化校验并委托给共享辅助函数，避免逻辑重复。
  - 新增 `loadProjectByIDWithQueryer`，使项目层级查询与现有 `loadUserByIDWithQueryer` 保持同一测试与复用模型。
  - 按目标类型分支解析范围，恢复与 SQLite 及用例层参数校验一致的契约。
- 在 `internal/adapters/outbound/vldb_postgres/dialect_test.go` 中：
  - 新增 user 画像范围回归测试，验证缺少 `project_id` 时仍只访问用户表并成功解析目标。
  - 新增 project/team/space 画像范围回归测试，验证缺少 `user_id` 时仍只访问项目表并成功解析目标。
  - 扩展 `captureProfileQueryer`，记录 `QueryRow` 调用次数，便于断言不会发生多余查询。
- 已执行验证：
  - `go test ./internal/adapters/outbound/vldb_postgres ./internal/app/usecase`
  - `go test ./...`

### 4. ⚠️遗留问题与注意事项

- 当前工作区仍包含本次修复之外的未提交改动与缓存目录（如 `go.mod`、`go.sum`、`internal/app/app.go`、`internal/config/*`、`.gocache/`、`.gomodcache/` 等），本次处理未对其做清理或回退。
- 本次修复严格聚焦审查意见对应的画像目标解析回归，不额外扩展 PostgreSQL 组合存储的其他行为边界。
