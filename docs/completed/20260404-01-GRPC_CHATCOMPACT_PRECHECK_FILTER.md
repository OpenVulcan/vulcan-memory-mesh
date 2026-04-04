# grpc chatcompact 与 PreCheck 压缩边界检索控制实施计划

## 1. 任务目标

为当前 gRPC 体系新增一个显式 `ChatCompact` 能力，并改造 `PreCheck` 的记忆召回边界控制逻辑，使系统能够识别“当前 session 是否已经发生上下文压缩”，并据此决定是否允许检索同 session 的历史记忆。

目标行为如下：

- 哪些 turn 发生在最近一次压缩之前；
- 哪些 turn 发生在最近一次压缩之后；
- 在未压缩的正常对话阶段，不额外召回“当前 session 压缩前的旧内容”；
- 只有在发生压缩后，才允许对压缩点之前、可能已经从模型上下文中丢失的历史内容进行检索补偿。

同时，本次改造还必须满足：

- `PreCheck` 需要新增一个“忽略压缩判定”的参数，供不支持 compact 判定的插件回退到现有行为。
- 向量检索与 BM25/FTS 检索都必须在第一层就支持压缩边界过滤，不能依赖全量召回后再统一丢弃。
- SQLite 与 LanceDB 的 schema 升级必须是非破坏性的，不能再因为版本变化而直接清空历史数据。

## 2. 已确认的设计约束

### 2.1 `PreCheck` 需要新增兼容参数

- 在 `PreCheckRequest` 中新增布尔参数，暂定命名为 `ignore_compact_boundary`。
- 当该参数为 `true` 时：
  - 完全沿用当前 `PreCheck` 行为；
  - 不读取也不应用 compact 边界过滤；
  - 适用于尚未接入 compact 判定能力的旧插件。
- 当该参数为 `false` 或省略时：
  - 启用新的 compact 边界规则；
  - 根据当前 session 的 compact 锚点限制同 session 记忆召回。

### 2.2 compact 权威状态落在 `session` 维度

- 以 `vmm_sessions.last_compacted_turn_id` 作为当前 session 的权威 compact 边界。
- 同时增加 `vmm_sessions.last_compacted_timestamp`，用于排障和审计。
- turn 本身不强制增加“压缩前 / 压缩后”持久化标记列，首版通过：
  - `turn.id <= last_compacted_turn_id` 视为压缩前；
  - `turn.id > last_compacted_turn_id` 视为压缩后。
- 这样可以避免每次 compact 时批量回写整段 turn 历史。

### 2.3 `ChatCompact` RPC 的职责

- 新增 `ChatCompact` gRPC 接口。
- 接口仍复用现有 `session_id + user_id + project_id` 的统一范围解析链路。
- 首版默认语义为：
  - 服务端自动把“当前 session 已持久化的最新 turn_id”记录为本次 compact 边界；
  - 不要求上游额外传入 turn id。
- 接口需具备幂等性：
  - 如果当前 session 尚无 turn，则允许返回成功但不更新 compact 边界；
  - 如果最新 turn 未变化，重复调用不会产生副作用。

### 2.4 `PreCheck` 的 compact 过滤规则

- 当 `ignore_compact_boundary = true` 时：
  - 保持当前行为，不做 compact 相关过滤。
- 当 `ignore_compact_boundary = false` 且 `last_compacted_turn_id = 0` 时：
  - 排除当前 session 的全部 turn-extract 记忆；
  - 因为这些内容仍在模型连续上下文中，没必要通过 `PreCheck` 再次注入。
- 当 `ignore_compact_boundary = false` 且 `last_compacted_turn_id > 0` 时：
  - 仅允许当前 session 中 `source_turn_id <= last_compacted_turn_id` 的记忆参与召回；
  - 排除当前 session 中 `source_turn_id > last_compacted_turn_id` 的记忆；
  - 其他非当前 session 的项目级 / 用户级共享长期记忆维持原有作用域规则。

### 2.5 LanceDB 必须具备一等过滤字段

- 当前代码中，post-action 写入向量时只把 `turn_id` 放进了 `metadata_json`，没有顶层 `source_turn_id` 字段。
- 由于 compact 过滤必须在向量召回第一层执行，不能依赖全量召回后回表排除，所以本次必须为 LanceDB 增加顶层 `source_turn_id` 字段。
- 如果检查现网 / 本地已有 LanceDB 表时发现不存在该字段，则需要：
  - 变更 LanceDB 表结构；
  - 回填历史行；
  - 后续所有向量写入都同步写顶层 `source_turn_id`。
- 如果后续发现某些环境已有等价字段但仍在 `metadata` 中，则需要把其提升为可过滤的顶层列，而不是继续停留在 `metadata_json` 中。

### 2.6 数据库升级必须改为非破坏性迁移

- 当前 SQLite 适配器的版本升级会触发 reset managed schema，这一行为本次必须改造。
- 本次 schema 升级要求：
  - SQLite：使用显式 migration 方案执行 `ALTER TABLE / backfill / create index`；
  - LanceDB：执行增列与历史数据回填；
  - 禁止因版本变化直接删表重建；
  - 禁止清理历史 session / turn / memory / profile 数据。
- 由于当前阶段仍属本体调试模式，允许做一次性结构迁移与数据回填，不需要为了兼容旧发布包而牺牲正确性。

### 2.7 迁移框架必须可递进复用

- 本次不能只为当前版本手写一次性迁移脚本。
- 必须同时落地一个可复用的数据库迁移流程骨架，至少包含：
  - 迁移版本定义结构体；
  - 当前版本号与目标版本号管理；
  - 按版本逐步递进执行的 migration runner；
  - 每一步 migration 的独立 `up` 实现；
  - 执行日志与错误定位信息。
- 未来新增 schema 版本时，应只需要继续追加一个版本 migration 定义，而不需要重写整套升级框架。
- 该框架首版至少服务于 SQLite；LanceDB 的字段升级与回填流程也要尽量按同类步骤组织，避免后续再次大改。
- SQLite schema 版本与 LanceDB schema 版本必须分开管理：
  - 仅当 SQL schema 变化时，执行 SQL migration；
  - 不得因为 SQL 版本变化而顺带重建向量表；
  - 仅当 LanceDB 列结构发生变化时，才允许触发向量表重建与回填。

## 3. 详细执行步骤

1. 调整 proto 与传输层契约：
   - 新增 `ChatCompactRequest / ChatCompactResponse`；
   - 在 `VMMService` 中注册 `ChatCompact`；
   - 为 `PreCheckRequest` 增加 `ignore_compact_boundary` 字段；
   - 更新 gRPC server、validation、测试桩与文档。
2. 扩展领域模型与 scope 数据：
   - 为 `SessionRecord / SessionRef` 增加 `LastCompactedTurnID` 与 `LastCompactedAt`；
   - 评估是否需要补充 compact 相关结果结构体或命令对象。
3. 改造 SQLite schema 迁移机制：
   - 停止当前“版本不一致即 reset”逻辑；
   - 引入可配置、可递进的 migration 结构体与执行器；
   - 引入按版本递进的 schema migration；
   - 为 `vmm_sessions` 增加 compact 边界字段；
   - 补充必要索引与回填逻辑。
4. 实现 `ChatCompact` 用例与持久化：
   - 解析已持久化 session；
   - 读取当前 session 最新 turn id；
   - 更新 `vmm_sessions.last_compacted_turn_id / last_compacted_timestamp`；
   - 保证幂等返回。
5. 扩展 LanceDB 表结构与写入路径：
  - 为向量表新增顶层 `source_turn_id` 列；
  - 更新 post-action 与 direct memory write 的向量写入逻辑；
  - 为历史向量行做字段回填；
  - 更新向量检索过滤表达式生成逻辑。
   - 设计独立的 LanceDB schema 版本检查机制，避免与 SQL migration 绑定导致不必要的重建。
6. 改造 SQLite lexical/BM25 检索：
   - 为当前 session 增加 compact 边界过滤条件；
   - 保证与向量检索使用一致的 compact 语义。
7. 改造 `PreCheck` 召回行为：
   - 新增 `ignore_compact_boundary` 控制；
   - 在统一记忆检索命令中透传 compact 过滤参数；
   - 保证 vector 与 lexical 两路召回一致遵守边界。
8. 补充测试与文档：
   - `grpcapi`：新 RPC、请求校验、兼容参数默认行为；
   - `usecase`：未 compact / 已 compact / 忽略 compact 三类场景；
   - `sqlite` / `lancedb`：schema migration、过滤条件、历史数据回填；
   - 更新 README 与 gRPC 对接文档。

## 4. 技术选型原则

- 尽量复用现有 `session/user/project` 统一范围解析链路，不引入额外寻址模式。
- 优先选择“一个权威压缩锚点 + 一致性过滤”的方案，避免多处重复状态导致语义漂移。
- 检索过滤必须同时覆盖向量检索与 BM25 检索，避免两路候选语义不一致。
- 压缩边界过滤必须尽量在检索第一层落地，避免先全量召回再排除带来的性能浪费与重复噪声。
- schema 升级必须优先保证历史数据不丢失，即使需要增加迁移与回填复杂度也不能偷懒重置。
- 首版优先保证行为可解释、幂等、可回溯，再考虑更复杂的多锚点压缩历史分析。

## 5. 预期验收标准

- 可以通过一个显式 RPC 把某个 session 标记为“已在某个 turn 处完成压缩”。
- `PreCheck` 提供兼容参数，旧插件可以显式绕过 compact 判定并保持当前行为。
- `PreCheck` 在未压缩场景下不会因为同 session 的旧对话而做多余召回。
- `PreCheck` 在已压缩场景下，只会对压缩点之前的同 session 内容开放检索补偿。
- vector 与 BM25 检索遵守相同的压缩边界规则。
- LanceDB 具备可直接过滤的顶层 `source_turn_id` 字段，不依赖 metadata 文本字段做运行时解析。
- SQLite 与 LanceDB 升级后历史数据仍然存在，schema 变更通过 migration 与回填完成。
- 文档、测试与 schema 变更说明同步齐全。

## 6. 当前实现假设

- 首版 `ChatCompact` 默认以“当前 session 最新已持久化 turn”为 compact 锚点，不额外开放自定义 anchor turn 参数。
- 首版 compact 过滤主要针对来源于 turn 提炼的统一记忆；如果某条记忆没有 `source_turn_id`，则不应被误判为当前 session 可恢复的 compact 前内容。
- `SearchMemoryEvents` 等通用记忆接口暂不强制套用 compact 规则，规则先限定在 `PreCheck` 链路内。

## 执行变更总结

### 1. 核心修复与调整概述

- 新增 `ChatCompact` gRPC 接口、用例与 SQLite 持久化能力，服务端会把当前 session 最新已持久化 `turn_id` 写入 `vmm_sessions.last_compacted_turn_id / last_compacted_timestamp`，并保持空 session 与重复 compact 场景幂等。
- `PreCheckRequest` 新增 `ignore_compact_boundary` 兼容参数；当其关闭时，`PreCheck -> MemoryQuery -> LanceDB / SQLite FTS` 整条链路都会统一应用当前 session compact 边界过滤。
- SQLite 初始化从“版本不一致即 reset”切换为可递进的非破坏性 migration runner，引入组件级版本表 `vmm_schema_versions`，并保留对旧 `vmm_version` 的兼容同步。
- LanceDB 增加顶层 `source_turn_id` 列，并引入独立的向量 schema 版本对齐流程；只有 LanceDB schema 版本变化时才允许重建向量表并从 SQLite 回灌，SQL-only 升级不会触发向量库重建。

### 2. 📂 文件变更清单

- 新增：
  - `internal/adapters/outbound/vldb_sqlite/schema_migrations.go`
  - `internal/adapters/outbound/vldb_sqlite/session_compact.go`
  - `internal/app/usecase/chatcompact.go`
  - `internal/app/usecase/chatcompact_test.go`
  - `internal/app/vector_schema_sync.go`
  - `internal/app/vector_schema_sync_test.go`
- 修改：
  - `internal/logic/domain/common.go`
  - `internal/app/ports/interfaces.go`
  - `internal/app/app.go`
  - `internal/app/app_test.go`
  - `internal/app/usecase/precheck.go`
  - `internal/app/usecase/precheck_scope.go`
  - `internal/app/usecase/precheck_test.go`
  - `internal/app/usecase/memory_query.go`
  - `internal/app/usecase/postaction.go`
  - `internal/adapters/outbound/vldb_sqlite/store.go`
  - `internal/adapters/outbound/vldb_sqlite/store_test.go`
  - `internal/adapters/outbound/vldb_sqlite/debug_clean.go`
  - `internal/adapters/outbound/vldb_lancedb/store.go`
  - `internal/adapters/outbound/vldb_lancedb/store_test.go`
  - `internal/adapters/inbound/grpcapi/proto/v1/vmm.proto`
  - `internal/adapters/inbound/grpcapi/proto/v1/vmm.pb.go`
  - `internal/adapters/inbound/grpcapi/proto/v1/vmm_grpc.pb.go`
  - `internal/adapters/inbound/grpcapi/server.go`
  - `internal/adapters/inbound/grpcapi/server_test.go`
  - `internal/adapters/inbound/grpcapi/validation.go`
  - `internal/adapters/inbound/grpcapi/interceptors.go`
  - `README.md`
  - `docs/grpc-integration-guide_CN.md`
  - `docs/hierarchy-grpc-design_CN.md`
  - `docs/api-test-guide_CN.md`
  - `docs/post-action-guide_CN.md`
- 删除：
  - 无

### 3. 💻 关键代码调整详情

- 在 SQLite 适配层新增 `schemaMigrationPlan / schemaMigrationStep` 迁移骨架，通过 `ensureSQLiteSchema` 统一负责空库 bootstrap、旧版版本表回退读取、版本步进执行与版本持久化。
- `currentSchemaVersion` 升级到 `15`，并通过 `14 -> 15` 迁移显式新增 `vmm_sessions.last_compacted_turn_id / last_compacted_timestamp` 与索引，不再破坏历史数据。
- 应用组合根新增 `ensureVectorSchema` 启动期流程：从 SQLite 读取 LanceDB schema 版本，只有版本不匹配时才调用 `RecreateTable`，随后按项目从 SQLite `ListProjectMemories` 回灌向量行并写回向量版本号。
- LanceDB 向量行新增 `source_turn_id` 顶层列；post-action 写入、direct memory write、SQLite 回灌、向量搜索过滤表达式和测试都已同步对齐。
- `PreCheck` 用例新增 compact 边界透传逻辑：未 compact 时排除当前 session turn-extract 记忆；已 compact 时只允许 `source_turn_id <= last_compacted_turn_id`；显式忽略时回退旧行为。

### 4. ⚠️ 遗留问题与注意事项

- 当前 compact 边界规则只在 `PreCheck` 链路内生效，`SearchMemoryEvents` 等通用查询接口仍保持原有项目级默认语义。
- 首版 `ChatCompact` 不支持上游自定义 anchor turn，仍以“当前 session 最新已持久化 turn”为唯一 compact 锚点。
- 启动期如果检测到 LanceDB schema 版本缺失或落后，会按新规则重建一次向量表；这是受控迁移行为，但版本写回成功前若中途失败，下一次启动会再次尝试回灌。
- 已完成验证：
  - `go test ./internal/app ./internal/app/usecase ./internal/adapters/inbound/grpcapi ./internal/adapters/outbound/vldb_sqlite ./internal/adapters/outbound/vldb_lancedb -count=1`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
