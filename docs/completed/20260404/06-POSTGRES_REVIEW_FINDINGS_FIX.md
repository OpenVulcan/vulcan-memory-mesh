# 任务计划：修复 PostgreSQL 审查意见中的并发与一致性缺陷

## 1. 任务目标

针对本次代码审查确认的 4 个有效问题完成修复，确保：

- `ApplyMemoryAdoption` 在并发采纳同一批记忆时不会发生计数器丢更新或作用域升级延迟
- `ApplyTurnAnalysis` 在插入新的画像节点后，能够正确 supersede 被评审器指定替代的旧画像节点
- 用户删除确认码在并发请求下始终返回数据库中真实持久化的确认码，不返回无效令牌
- 项目迁移在修改 `vmm_sessions(project_id, session_key)` 前能够预检并阻止冲突，避免用户确认后才在事务中途失败

本次任务仅修复审查意见对应的问题，不额外扩展 PostgreSQL 组合库的其他能力边界。

## 2. 执行步骤

1. 梳理问题落点与当前语义
   - 复查 `analysis_store.go`、`workspace_admin.go`、相关 usecase 调用链
   - 对照 SQLite 现有行为，确认 PostgreSQL 修复后的期望语义

2. 修复记忆采纳并发一致性
   - 将 `ApplyMemoryAdoption` 的目标行读取移动到事务内部
   - 结合行级锁或等价事务内读取方式，确保并发采纳不会基于旧快照覆盖计数器

3. 修复画像 supersede 缺失
   - 在 `ApplyTurnAnalysis` 插入新画像节点后，补上对 `SupersedeNodeIDs` 的状态更新
   - 确保仅允许 supersede 当前活跃节点，并保持返回结果与后续画像渲染语义一致

4. 修复管理流中的竞态与迁移冲突
   - 在 `ensureUserDeleteConfirmation` 中处理零行更新场景，重新读取真实确认码
   - 在 `MigrateProjectPath` 执行 session 迁移前预检 `session_key` 冲突，并返回明确错误

5. 补充验证
   - 为新增修复补单元测试或轻量适配器测试
   - 至少运行 PostgreSQL 适配器、应用层和配置相关测试
   - 最后运行 `go test ./...` 做全量回归验证

## 3. 技术选型

- 并发采纳修复优先采用“事务内读取 + 行级锁 + 同事务回写”
  - 原因：这与 PostgreSQL 的并发控制模型最贴近，也最能直接修复“读旧值后回写绝对值”造成的丢更新
- 画像 supersede 继续沿用现有 reviewer 产出的 `SupersedeNodeIDs`
  - 原因：上游评审契约已稳定，修复重点应放在持久化落地缺失，而不是重新设计评审协议
- session 迁移冲突采用“预检后失败快返”
  - 原因：当前迁移语义没有定义自动合并 session，显式拒绝比隐式改写或吞并更安全
- 确认码竞态采用“条件更新后检查结果，必要时回读最终值”
  - 原因：该方式可以保持删除第一阶段幂等，同时确保返回给调用方的令牌一定真实存在

## 4. 验收标准

- 并发执行同一批记忆采纳时，不再出现采纳计数、强化计数、跨会话采纳计数被覆盖的问题
- 新画像节点如果携带 `SupersedeNodeIDs`，对应旧节点会被标记为 superseded，不再继续作为 active 节点参与后续评审与渲染
- 并发触发用户删除确认时，任一调用方拿到的确认码都可以实际用于后续删除
- 若目标项目中已存在相同 `session_key`，项目迁移会在修改前返回明确冲突错误，而不是事务中途触发唯一约束失败
- 相关测试与 `go test ./...` 全部通过

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已将 `ApplyMemoryAdoption` 的记忆目标读取移动到事务内部，并使用 `FOR UPDATE` 锁定目标行，修复并发采纳同一批记忆时基于旧快照回写导致的计数丢失问题。
- 已在 `ApplyTurnAnalysis` 插入新的画像节点后，补齐对 `SupersedeNodeIDs` 指向旧节点的 supersede 写回，保证画像替换在同一事务内完成。
- 已将删除确认第一阶段改为“事务内锁行读取 + 条件更新 + 0 行回退读取真实值”，确保并发请求下始终返回数据库里真实存在的确认码。
- 已在项目迁移前新增 `session_key` 冲突预检，并对极端并发下可能出现的唯一键异常补充 conflict 翻译，避免用户只看到底层 SQL 报错。
- 已补充无需真实数据库的回归测试，并完成定向测试与全量 `go test ./...` 验证。

### 2. 📂文件变更清单

- 新增：无
- 修改：
  - `internal/adapters/outbound/vldb_postgres/analysis_store.go`
  - `internal/adapters/outbound/vldb_postgres/memory_store.go`
  - `internal/adapters/outbound/vldb_postgres/workspace.go`
  - `internal/adapters/outbound/vldb_postgres/workspace_admin.go`
  - `internal/adapters/outbound/vldb_postgres/dialect_test.go`
  - `docs/plan/20260404-06-POSTGRES_REVIEW_FINDINGS_FIX.md`
- 删除：无

### 3. 💻关键代码调整详情

- `analysis_store.go`
  - 在记忆采纳路径中新增事务内锁定查询，读取最新且被锁定的记忆行后再调用 `evolveAdoptedMemoryRecord`。
  - 在画像落库路径中将插入语句改为 `RETURNING id`，并基于新画像节点 id 回写被替换旧节点的 `profile_status`、`superseded_by_id` 与 `status_reason`。
- `memory_store.go`
  - 提炼 `queryMemoryNodesWithQueryer`，让连接池与事务都能复用同一套记忆扫描逻辑，供事务内锁定读取使用。
- `workspace.go`
  - 提炼 `loadUserByIDWithQueryer`，支持在事务中按需 `FOR UPDATE` 读取用户行，供删除确认幂等流程复用。
- `workspace_admin.go`
  - 新增项目迁移 session 冲突查询与 conflict 构造逻辑，在真正改写 `project_id` 前先拒绝已知冲突。
  - 在删除确认流程中检查条件更新影响行数；如果未成功写入，则回读数据库中的真实确认码并统一通过辅助函数选择最终返回值。
  - 对 `vmm_sessions(project_id, session_key)` 的唯一键异常补充显式冲突翻译，避免直接泄露底层数据库错误。
- `dialect_test.go`
  - 新增项目迁移冲突消息与删除确认码回退逻辑的单元测试。

### 4. ⚠️遗留问题与注意事项

- 本次修复仅覆盖审查意见对应的 4 个缺陷，没有扩展 PostgreSQL 组合库之外的运行时路径。
- 已执行：
  - `go test ./internal/adapters/outbound/vldb_postgres ./internal/app/usecase ./internal/config`
  - `go test ./...`
- 当前工作区仍存在本任务之外的既有变更（如 `go.mod`、`go.sum`、`internal/app/app.go`、`internal/config/config.go` 等），本次未对其进行回退或改写。
