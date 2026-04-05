# DWM Scratchpad 第二轮收敛与文档拆分计划

## 一、任务目标

本计划用于在已完成的 `20260405-29-SESSION_SCRATCHPAD_GRPC_AND_STORAGE_PLAN.md` 基础上，继续收敛 DWM / scratchpad 的接口契约、数据库职责边界与专题文档结构，确保当前实现与最新需求完全一致。

本轮重点不是新增另一套能力，而是对 plan29 已落地内容进行二次修正，消除以下偏差：

1. transport 层当前仍允许 `Upsert` 的 `key/value` 与 `items[]` 混用，需要改为显式报错。
2. transport 层当前仍允许 `Delete` 的 `key` 与 `keys[]` 混用，需要改为显式报错。
3. `Get / Upsert / Delete` 响应缺少足够工程化的 metadata / count 字段。
4. `DWM` 文档当前写在同一个中英混合文件里，需要拆分成独立中文版与英文版。
5. scratchpad 当前仍接入 `MigrateProject` 路径，而该能力并非真实需求，需要彻底移除。
6. 需要把本轮变化明确标注为对 plan29 的调整项，避免历史计划与现状再次漂移。
7. 结合本次表接入，要顺手完善独立表升级链路的文档与实现说明，保证 scratchpad 表升级语义清晰可追踪。

## 二、相对 Plan29 的变更点

相对于 `20260405-29-SESSION_SCRATCHPAD_GRPC_AND_STORAGE_PLAN.md`，本轮新增/修订以下约束：

1. `Upsert`：
   - 若 `items[]` 非空，则不允许同时再传 `key/value`。
   - 若发生混传，直接返回校验错误，不再做 transport 合并。
2. `Delete`：
   - 若 `keys[]` 非空，则不允许同时再传 `key`。
   - 若发生混传，直接返回校验错误，不再做 transport 合并。
3. `Get` 响应新增 metadata：
   - `plan_name`
   - `item_count`
   - `updated_timestamp`
4. `Upsert/Delete` 响应新增计数字段：
   - `affected_count`
   - `Upsert` 额外补 `inserted_count / updated_count`
5. 批量语义明确写死为：
   - `v1` 采用整批原子事务
   - 任一 item 非法则整批失败
6. canonical plan 规则明确写死：
   - `Get` 返回的 `plan_name` 永远是 canonical 值
   - 大小写漂移 warning 一律以 canonical 值为准
7. 移除 scratchpad 与项目迁移链路的耦合：
   - 不再参与 `MigrateProject`
   - 仅保留 `DeleteProject / DeleteUser` 的级联清理
8. DWM 文档拆分为：
   - 中文文档
   - 英文文档
   - 不再使用单文件双语混写

## 三、执行步骤

### 1. 调整计划与文档真源

1. 新建本计划文件，明确标注相对 plan29 的差异。
2. 保留 plan29 作为历史执行记录，不覆盖其完成事实。
3. 在新的 DWM 中文/英文文档中补充本轮修订后的接口语义与工程边界。

### 2. 调整 proto 契约

1. `ScratchpadUpsertRequest`：保留 `key/value` 与 `items[]` 两种入口，但在文档与校验层明确禁止混传。
2. `ScratchpadDeleteRequest`：保留 `key` 与 `keys[]` 两种入口，但在文档与校验层明确禁止混传。
3. `ScratchpadGetResponse` 新增：
   - `string plan_name`
   - `uint32 item_count`
   - `int64 updated_timestamp`
4. `ScratchpadUpsertResponse` 新增：
   - `uint32 affected_count`
   - `uint32 inserted_count`
   - `uint32 updated_count`
5. `ScratchpadDeleteResponse` 新增：
   - `uint32 affected_count`
6. 所有 scratchpad response 继续保留：
   - `status`
   - `msg`
7. gRPC 文档需明确说明：
   - `status` 为 enum
   - 对接 AI Agent 时调用方要自行转译 enum

### 3. 调整 validation 与 transport 归一化规则

1. `NormalizeScratchpadUpsertRequest`：只做 trim，不再把 `key/value` 自动并入 `items[]`。
2. `ValidateScratchpadUpsert`：
   - `items[]` 非空且 `key/value` 任一出现 => 直接报错
   - `items[]` 为空时，必须同时提供 `key` 与 `value`
3. `NormalizeScratchpadDeleteRequest`：只做 trim，不再把 `key` 自动并入 `keys[]`。
4. `ValidateScratchpadDelete`：
   - `keys[]` 非空且 `key` 出现 => 直接报错
   - `keys[]` 为空时，必须提供 `key`
5. gRPC handler 再显式把单项形式转为内部 command，保持 transport 语义清晰。

### 4. 调整领域模型与 usecase 结果结构

1. `ScratchpadMutationResult` 补充：
   - `PlanName`
   - `UpdatedAt`
   - `AffectedCount`
   - `InsertedCount`
   - `UpdatedCount`
2. `ScratchpadQueryResult` 补充：
   - `PlanName`
   - `UpdatedAt`
   - `ItemCount`
3. `Upsert` 成功时返回计数字段，并在内部继续携带 canonical plan metadata 供 handler 与后续扩展使用。
4. `Delete` 成功时返回 `affected_count`，并在内部继续携带 canonical plan metadata 供 handler 与后续扩展使用。
5. `Get` 无数据时：
   - `status = SUCCESS`
   - `msg = No scratchpad records found for the current session.`
   - `items = []`
   - `item_count = 0`
6. `Delete` 空范围时：
   - 不锁定 plan
   - 返回 `No scratchpad plan exists for the current session. Create records first.`
7. 明确在代码注释和文档中写死：
   - 批量写入与删除均为整批原子事务

### 5. 调整存储层与迁移边界

1. 保持 `vmm_scratchpad_plans / vmm_scratchpad_nodes` 双表结构。
2. 保持表独立 schema 升级能力，并补足本轮对应说明与测试。
3. 移除 scratchpad 参与 `MigrateProject` 的实现：
   - SQLite 路径删除 scratchpad migration 更新与冲突逻辑
   - PostgreSQL 路径删除 scratchpad migration 更新与冲突逻辑
4. 保留：
   - `DeleteProject` 删除 scratchpad 数据
   - `DeleteUser` 删除 scratchpad 数据
5. 保持 scratchpad 与主记忆、profile、vector、recycle trash 的隔离边界不变。

### 6. 拆分并更新 DWM 文档

1. 删除混合文档：
   - `docs/dwm-working-memory-guide_CN_EN.md`
2. 新增：
   - `docs/dwm-working-memory-guide_CN.md`
   - `docs/dwm-working-memory-guide_EN.md`
3. 中文版和英文版都必须包含：
   - DWM 命名与定位
   - 核心痛点与解决方案
   - scratchpad 四个接口
   - 计划锁 / 格式漂移 / clean 语义
   - compact 恢复建议
   - 原子事务语义
   - 15 天硬删除机制
4. README 与其他 docs 统一改链接与引用。

### 7. 测试与回归

至少新增/调整以下测试：

1. `Upsert`：混传 `key/value + items[]` 必须报错。
2. `Delete`：混传 `key + keys[]` 必须报错。
3. `Get`：返回 canonical `plan_name`、`item_count`、`updated_timestamp`。
4. `Upsert`：返回 `affected_count / inserted_count / updated_count`。
5. `Delete`：返回 `affected_count`。
6. `Delete` 空 plan：不建锁、返回指导消息。
7. 迁移链路：scratchpad 不再参与 `MigrateProject`。
8. 跑最少测试集：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
9. 跑全量：
   - `go test ./...`
10. 必要时补：
   - `go vet ./...`

## 四、验收标准

1. scratchpad 四个 gRPC 接口与最新约束一致。
2. `Upsert` 与 `Delete` 都禁止单项/批量混传。
3. `Get` 返回 canonical `plan_name`、`item_count`、`updated_timestamp`。
4. `Upsert/Delete` 返回明确计数字段。
5. scratchpad 批量写入语义在代码与文档中都明确为整批原子事务。
6. scratchpad 不再参与任何项目迁移逻辑。
7. DWM 文档已拆分成独立中文/英文两份。
8. README 与 docs 下相关文档已与当前实现同步。
9. 计划文件末尾补写“执行变更总结”后，再迁移到 `docs/completed/`。

## 执行变更总结

### 1. 核心修复与调整概述

本轮已完成对 plan29 的第二次收敛，重点解决了 scratchpad 契约与最新需求不一致的问题：

1. 收紧 `ScratchpadUpsert / ScratchpadDelete` 的 transport 规则，不再允许单项与批量混传。
2. 补齐 `ScratchpadGet` 的 metadata，以及 `ScratchpadUpsert / ScratchpadDelete` 的工程化计数字段。
3. 明确并实现 scratchpad 批量写入/删除的整批原子语义。
4. 将 scratchpad 从 `MigrateProject` 链路中移除，恢复其“独立工作记忆支线”的职责边界。
5. 将 DWM 文档从中英混排单文件拆分为独立中文与英文两份，并同步更新 README 与 gRPC/测试/架构文档。
6. 补齐 SQLite scratchpad 独立表升级回归测试，验证 `18 -> 19` 迁移确实通过增量升级完成，而不是依赖全量重建。

### 2. 📂文件变更清单

新增文件：

- `docs/dwm-working-memory-guide_CN.md`
- `docs/dwm-working-memory-guide_EN.md`
- `internal/adapters/inbound/grpcapi/scratchpad_rpc_test.go`

修改文件：

- `README.md`
- `docs/api-test-guide_CN.md`
- `docs/grpc-integration-guide_CN.md`
- `docs/hierarchy-grpc-design_CN.md`
- `docs/completed/20260405-29-SESSION_SCRATCHPAD_GRPC_AND_STORAGE_PLAN.md`
- `internal/adapters/inbound/grpcapi/proto/v1/vmm.proto`
- `internal/adapters/inbound/grpcapi/proto/v1/vmm.pb.go`
- `internal/adapters/inbound/grpcapi/proto/v1/vmm_grpc.pb.go`
- `internal/adapters/inbound/grpcapi/server.go`
- `internal/adapters/inbound/grpcapi/validation.go`
- `internal/app/usecase/scratchpad.go`
- `internal/app/usecase/scratchpad_test.go`
- `internal/logic/domain/scratchpad.go`
- `internal/adapters/outbound/vldb_sqlite/store.go`
- `internal/adapters/outbound/vldb_sqlite/scratchpad.go`
- `internal/adapters/outbound/vldb_sqlite/store_test.go`
- `internal/adapters/outbound/vldb_postgres/workspace_admin.go`

删除文件：

- `docs/dwm-working-memory-guide_CN_EN.md`

### 3. 💻关键代码调整详情

1. gRPC 契约层
   - 为 `ScratchpadUpsertResponse` 增加 `affected_count / inserted_count / updated_count`
   - 为 `ScratchpadDeleteResponse` 增加 `affected_count`
   - 为 `ScratchpadGetResponse` 增加 `plan_name / item_count / updated_timestamp`
2. transport 校验层
   - `NormalizeScratchpadUpsertRequest` 与 `NormalizeScratchpadDeleteRequest` 不再自动合并单项和批量字段
   - `ValidateScratchpadUpsert` 改为显式拒绝 `key/value + items[]` 混传
   - `ValidateScratchpadDelete` 改为显式拒绝 `key + keys[]` 混传
3. gRPC handler
   - handler 在校验通过后再显式把单项简写展开为内部 command
   - 响应层完成新增 metadata/count 字段映射
4. 领域与用例层
   - `ScratchpadMutationResult / ScratchpadQueryResult` 补齐 metadata 与计数字段
   - `Get` 在空结果场景下继续返回成功，同时在有计划存在时返回 canonical plan metadata
   - `Delete` 空范围继续保持“不锁定 plan”的幂等语义
5. 存储与管理边界
   - SQLite / PostgreSQL 项目迁移逻辑已移除 scratchpad 冲突检查和计划迁移更新
   - `DeleteProject / DeleteUser` 仍保留 scratchpad 级联清理
6. 文档与治理
   - DWM 文档拆分成中英文两份
   - README、gRPC 对接文档、接口测试文档、架构文档同步更新
   - plan29 追加“后续修订说明”，明确哪些行为已被 plan30 收敛
7. 测试与验证
   - 新增 gRPC transport 测试，覆盖：
     - 混传报错
     - 单项简写展开
     - metadata / count 回传
   - 新增 SQLite schema 升级测试，覆盖 scratchpad `18 -> 19` 增量迁移
   - 已通过：
     - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
     - `go test ./...`
     - `go vet ./...`

### 4. ⚠️遗留问题与注意事项

1. scratchpad 当前仍沿用固定 `15` 天过期窗口，未扩展为独立配置项。
2. `ScratchpadStatus` 仍为 proto enum；若宿主直接把结果暴露给 AI Agent，必须先做语义转译。
3. scratchpad 当前故意不参与 `MigrateProject`；这属于需求边界收紧，不是遗漏。
