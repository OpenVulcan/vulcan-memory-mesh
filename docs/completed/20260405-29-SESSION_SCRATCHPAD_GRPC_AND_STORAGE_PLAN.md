# 任务目标

本计划用于为当前仓库新增一套独立于长期记忆体系的 session scratchpad 能力，提供面向 AI Agent 的临时计划/执行摘要存取接口，解决多工具调用、上下文压缩和阶段切换后，AI 无法稳定保留“当前任务计划”和“关键执行信息”的问题。

本轮目标包括：

1. 新增 4 个 gRPC 接口：
   - `ScratchpadUpsert`
   - `ScratchpadDelete`
   - `ScratchpadGet`
   - `ScratchpadClean`
2. 新增独立关系表：
   - `vmm_scratchpad_plans`
   - `vmm_scratchpad_nodes`
3. 建立 plan-name 一致性守卫：
   - 大小写忽略比对
   - 完全不一致时拦截
   - 仅大小写漂移时放行但强提示
4. 保持该能力与传统 `vmm_sessions / memory_nodes / retention / vector` 体系解耦，并在代码文件链上独立成支线实现。

# 需求理解与设计原则

## 一、能力定位

这套 scratchpad 不是长期记忆，也不是 turn 提炼结果。

它应满足：

1. 只服务于当前 `project_id + user_id + session_id(string)` 下的临时执行上下文。
2. 允许 AI 在 3-5 次工具调用后，把阶段计划、关键文件摘要、关键代码结论、执行约束等内容按 `key/value` 写入。
3. 允许在上下文压缩后，通过一次 `Get` 直接恢复完整 scratchpad 状态。
4. 在任务结束后，通过 `Clean` 明确清空。

## 二、架构约束

本能力必须遵守当前仓库分层：

- `grpc transport`
- `validator`
- `usecase`
- `app ports`
- `sqlite / postgres adapters`

并且必须避免以下错误：

1. 不能复用 `RequestScopeResolver` 的 `session` 自动创建行为。
   - 原因：现有 resolver 会操作 `vmm_sessions`，而本能力要求 `session_id` 只作为 scratchpad 定位键，独立于传统记忆体系。
2. 不能写入向量库。
3. 不能接入 retention 自动回收链。
4. 不能混入 `memory_nodes` 或 `turn_records`。
5. 代码实现链要独立：
   - 独立 proto message / RPC
   - 独立 validator
   - 独立 usecase
   - 独立 store port
   - 独立 sqlite / postgres adapter 方法
   - 不复用 `MemoryUseCase`、`ProfileUseCase`、`RetentionUseCase` 的内部逻辑

补充约束：

- 可以复用系统现有“每 30 分钟一次”的后台维护时钟；
- 但 scratchpad 的清理语义必须独立，不进入回收站、不走软备份、不复用 `RetentionStore` 的 memory/turn 回收规则。

## 三、推荐总体方案

推荐新增一条独立能力链：

1. proto 新增 4 个 RPC
2. validator 新增 4 组请求校验
3. usecase 新增 `ScratchpadUseCase`
4. ports 新增 `ScratchpadStore`
5. SQLite / PostgreSQL 都补齐实现
6. README 与 gRPC 文档同步补齐

同时采用“两表模型”替代此前的单表 anchor 方案：

1. `vmm_scratchpad_plans`
   - 保存当前 `project_id + user_id + session_key` 下唯一生效的 plan
2. `vmm_scratchpad_nodes`
   - 保存该 plan 下的具体 `key/value` 节点

# 推荐接口契约

## 一、RPC 命名

推荐使用当前仓库一致的 UpperCamelCase 命名：

1. `ScratchpadUpsert`
2. `ScratchpadDelete`
3. `ScratchpadGet`
4. `ScratchpadClean`

说明：

- 这已经满足“scratchpad_开头”的语义要求；
- 不建议在 proto RPC 名中继续使用下划线风格。

## 二、公共载荷

四个接口都固定包含：

- `project_id`
- `user_id`
- `session_id`

其中：

- `session_id`
  - 类型：`string`
  - 长度建议：`<= 128`
  - 只作为 scratchpad 定位键
  - 不与 `vmm_sessions.id` 或 `vmm_sessions.session_key` 发生强绑定

## 三、Upsert

### 推荐请求结构

```proto
message ScratchpadItem {
  string key = 1;
  string value = 2;
}

message ScratchpadUpsertRequest {
  string session_id = 1;
  uint64 user_id = 2;
  uint64 project_id = 3;
  string plan_name = 4;
  optional string key = 5;
  optional string value = 6;
  repeated ScratchpadItem items = 7;
}
```

### 语义

1. 支持单项：
   - `key + value`
2. 也支持批量：
   - `items[]`
3. 传输层先归一化为统一 `items[]`
4. `key` 唯一，后写覆盖前写

### 约束建议

- `plan_name <= 128`
- `key <= 128`
- `value <= 16000`
- `items <= 32`

## 四、Delete

### 推荐请求结构

```proto
message ScratchpadDeleteRequest {
  string session_id = 1;
  uint64 user_id = 2;
  uint64 project_id = 3;
  string plan_name = 4;
  optional string key = 5;
  repeated string keys = 6;
}
```

### 语义

1. 支持单键删除：
   - `key`
2. 支持批量删除：
   - `keys[]`
3. 传输层先统一归一化为 `keys[]`

说明：

- 我建议用 `keys[]`，而不是 `items[]=key`。
- 原因：delete 没有 `value`，直接用 `keys[]` 更清晰，也更符合当前 proto 风格。

## 五、Get

### 推荐请求结构

```proto
message ScratchpadGetRequest {
  string session_id = 1;
  uint64 user_id = 2;
  uint64 project_id = 3;
  optional string key = 4;
}
```

### 语义

1. `key` 为空：
   - 返回当前 session 下全部 scratchpad item
2. `key` 非空：
   - 只返回该 key 对应项
3. 无论单项还是多项，响应都统一返回 `items[]`

## 六、Clean

### 推荐请求结构

```proto
message ScratchpadCleanRequest {
  string session_id = 1;
  uint64 user_id = 2;
  uint64 project_id = 3;
}
```

### 语义

- 删除当前 `project_id + user_id + session_id` 下全部 scratchpad 数据

## 七、响应结构

### 推荐公共状态

推荐新增统一枚举：

```proto
enum ScratchpadStatus {
  SCRATCHPAD_STATUS_UNSPECIFIED = 0;
  SCRATCHPAD_STATUS_SUCCESS = 1;
  SCRATCHPAD_STATUS_FAILED = 2;
}
```

原因：

1. 比直接返回中文字符串更稳定；
2. 仍然可以在文档中明确映射为“成功/失败”；
3. 符合当前仓库 proto 风格。

### 推荐响应

```proto
message ScratchpadUpsertResponse {
  ScratchpadStatus status = 1;
  string msg = 2;
}

message ScratchpadDeleteResponse {
  ScratchpadStatus status = 1;
  string msg = 2;
}

message ScratchpadGetResponse {
  ScratchpadStatus status = 1;
  string msg = 2;
  repeated ScratchpadItem items = 3;
}

message ScratchpadCleanResponse {
  ScratchpadStatus status = 1;
  string msg = 2;
}
```

说明：

- `status` 正式采用 proto enum。
- `msg` 固定使用英文自然语言提示。
- `ScratchpadGetResponse` 也需要保留 `msg`，用于“当前没有记录”但不视为错误的场景提示。
- gRPC 文档必须明确说明：
  - 当接口开放给 AI Agent 使用时，调用方需要主动把 enum 转译成自身可理解的语义标签，而不是把 proto 枚举名原样暴露给最终大模型。

# 推荐表结构

## 一、核心目标

将“当前 session 对应的唯一计划”和“计划下的 scratchpad 节点”拆开保存。

这样比单表 anchor 行更合适，原因是：

1. plan 锁定语义更直接：
   - 一个 `session_key` 只有一条 plan 行
2. 节点 CRUD 更简单：
   - `nodes` 只关心 `plan_id + key`
3. 不需要引入伪造的 anchor item 行
4. 更容易做 project/user 删除与 project 迁移
5. 更符合“独立临时工具数据”而不是“长期记忆行变体”的定位

## 二、推荐字段

### `vmm_scratchpad_plans`

推荐字段：

- `id`
  - 内部数值主键
- `project_id`
- `user_id`
- `session_key`
  - `TEXT`
  - 直接保存外部字符串 session_id
  - 数据库侧建议命名为 `session_key`，避免与传统 `vmm_sessions.id` 数值主键混淆
- `plan_name`
  - 当前 canonical 计划名
- `plan_name_norm`
  - `strings.ToLower(plan_name)` 结果
- `created_timestamp`
- `updated_timestamp`

说明：

- `created_timestamp` 是硬性字段，不只是审计字段。
- `updated_timestamp` 才是“当前 session 最后内容更新时间”的主锚点。
- 过期强删必须以 `updated_timestamp` 为准。

### `vmm_scratchpad_nodes`

推荐字段：

- `id`
  - 内部数值主键
- `plan_id`
  - 外键，指向 `vmm_scratchpad_plans.id`
- `item_key`
- `item_value`
- `created_timestamp`
- `updated_timestamp`

说明：

- `created_timestamp` 同样必须保留。
- 这样后续即使要做节点级清理、诊断或审计，也不需要回推创建时间。
- `updated_timestamp` 用于保留节点最后变更时间，但 session 级过期判定仍以 `plans.updated_timestamp` 为准。

## 三、推荐索引与约束

### `vmm_scratchpad_plans`

1. 唯一约束：
   - `(project_id, user_id, session_key)`
2. 查询索引：
   - `(project_id, user_id, session_key, updated_timestamp)`
3. 过期清理索引：
   - `(updated_timestamp)`

### `vmm_scratchpad_nodes`

1. 唯一约束：
   - `(plan_id, item_key)`
2. 查询索引：
   - `(plan_id, updated_timestamp)`
3. 垃圾回收索引：
   - `(created_timestamp)`

## 四、垃圾回收准备

虽然 scratchpad 不接入主线 retention 的回收站语义，但为了后续独立垃圾回收，当前设计必须预埋时间字段和清理锚点。

推荐规则：

1. `plans.created_timestamp`
   - 作为创建时间与审计锚点
2. `plans.updated_timestamp`
   - 作为 session scratchpad 生命周期的主锚点
2. `nodes.created_timestamp`
   - 作为节点级诊断与细粒度清理的辅助锚点
3. `nodes.updated_timestamp`
   - 作为节点级最后变更时间
4. 后续 GC 应优先以 `plans.updated_timestamp` 为准：
   - 先删除过期 `plan`
   - 再级联删除其下全部 `nodes`

当前计划先把“GC 所需字段和索引”一次设计进去。

本轮还需要同时落地“固定 15 天强制硬删”的实际清理机制，而不是只预留字段。

## 五、scratchpad 过期清理机制

新增规则：

1. 以 `vmm_scratchpad_plans.updated_timestamp` 作为“当前 session 最后内容更新时间”。
2. 当：
   - `plans.updated_timestamp <= now - 15d`
   时，视为该 session scratchpad 已过期。
3. 过期后执行：
   - 直接硬删除 `vmm_scratchpad_nodes`
   - 再删除对应 `vmm_scratchpad_plans`
4. 不进入回收站。
5. 不保留任何软备份。
6. 不产生 vector GC 或 recycle batch。

执行方式：

1. 不单独新建 scratchpad 自己的 ticker。
2. 复用系统现有半小时维护周期。
3. 在现有后台维护时钟里增加一个 scratchpad 专属清理 pass。

推荐接线方式：

1. scratchpad 仍保持独立的 CRUD 用例与 store port。
2. 仅额外新增一个窄口接口，例如：
   - `ScratchpadGCStore`
3. 由当前后台维护器在每轮 tick 中调用：
   - `DeleteExpiredScratchpadSessions(before, limit)`

这样可以同时满足：

1. 代码链独立；
2. 不污染主记忆主线；
3. 复用现有半小时维护节奏。

# Plan 一致性守卫设计

## 一、守卫规则

在执行 `Upsert` 或 `Delete` 前：

1. 先读取当前 scope 下的 `vmm_scratchpad_plans` 行
2. 若不存在，则视为当前 session 尚未锁定计划
3. 若存在，则取出当前 canonical `plan_name`

## 二、判定逻辑

### 场景 1：当前 scope 完全为空

当前确认行为分流如下：

- `Upsert`
  - 直接锁定为当前传入的 `plan_name`
  - 通过写入一条 `vmm_scratchpad_plans` 行落库
- `Delete`
  - 不执行锁定
  - 直接返回空结果提示
  - 不创建 `plans` 行

### 场景 2：`ToLower(stored_plan_name) != ToLower(input_plan_name)`

行为：

- 拦截 `Upsert / Delete`
- 返回失败
- `msg` 使用英文自然语言模板，例如：
  - `The input plan name does not match the current scratchpad plan. Check whether the plan name is misspelled or call Clean before switching to a new plan. Current plan: xxx. Input plan: xxx.`

### 场景 3：忽略大小写后一致，但原始字符不一致

行为：

- 放行操作
- 不更新 canonical `plan_name`
- 最终 `msg` 前强插：
  - `[FORMAT DRIFT WARNING] The canonical plan name is xxx. Align future calls to this exact spelling.`

### 场景 4：完全一致

行为：

- 正常放行

# 四个接口的推荐业务语义

## 一、Upsert

1. 先做 plan 守卫
2. 单项或批量归一化为 `items[]`
3. 若 plan 行不存在：
   - 先创建 `vmm_scratchpad_plans`
   - 再取得 `plan_id`
3. 对每个 key 执行：
   - 存在则 update
   - 不存在则 insert
4. 只要实际发生内容写入或覆盖：
   - 同步刷新 `plans.updated_timestamp`
4. 返回：
   - 成功/失败
   - 新增/更新条数
   - 若有格式漂移，加警告前缀

## 二、Delete

1. 先做 plan 守卫
2. 单项或批量归一化为 `keys[]`
3. 若当前 scope 下没有 `plans` 行：
   - 不锁定 plan
   - 直接返回成功状态 + 英文提示：
     - `No scratchpad plan exists for the current session. Create records first.`
4. 若 plan 行存在，先取到 `plan_id`
5. 删除匹配 item
6. 如果删完后没有任何 item：
   - 默认保留 `plans` 行
   - 保持当前 session 的 plan 锁定语义，直到显式 `Clean`
7. 只有当确实删除了至少一条 node 时：
   - 才刷新 `plans.updated_timestamp`
8. 返回：
   - 删除成功 / 找不到对应记录
   - 若有格式漂移，加警告前缀

## 三、Get

1. 不要求传 `plan_name`
2. 先读取当前 scope 下的 `plans` 行
3. `key` 为空时返回该 `plan_id` 下全部 item
4. `key` 非空时返回单项
5. 返回结构统一为 `items[]`
6. `plan_name` 不混入 `items[]`
7. 若当前 session 下没有任何 scratchpad 数据：
   - 不报错
   - 返回成功状态
   - `items = []`
   - `msg` 使用英文提示，例如：
     - `No scratchpad records found for the current session.`

## 四、Clean

1. 删除当前 scope 下：
   - `vmm_scratchpad_nodes`
   - `vmm_scratchpad_plans`
2. 返回：
   - `Scratchpad history has been cleared.`
   - 或 `The current scratchpad is already empty.`

# 与当前仓库其他能力的关系

## 一、必须接入

1. gRPC proto
2. transport normalize
3. request validator
4. `Server` 路由装配
5. app ports
6. usecase
7. SQLite 实现
8. PostgreSQL 实现
9. 半小时后台维护接线
10. README 与 gRPC 文档

## 二、必须同步考虑

1. `DeleteProject`
   - 需要删除对应 scratchpad 数据
2. `DeleteUser`
   - 需要删除对应 scratchpad 数据
3. `MigrateProject`
   - 需要迁移对应 scratchpad 数据到新 `project_id`
4. `debug-clean`
   - 需要覆盖 scratchpad 表
5. 半小时后台维护器
   - 需要增加 scratchpad 过期强删 pass

## 三、明确不接入

1. vector store
2. retention 的回收站与软备份语义
3. PreCheck 自动注入
4. PostAction 自动提炼
5. memory_nodes / profile_nodes 生命周期

# 详细执行步骤

## 阶段一：契约与数据结构落定

1. 更新 proto，新增 4 个 RPC 和对应 message / enum。
2. 新增 scratchpad 领域模型与结果结构。
3. 确认两表方案与 `updated_timestamp` 作为 15 天过期锚点。
4. 固化英文 `msg` 模板：
   - mismatch 拦截
   - 大小写漂移警告
   - delete 空 session 提示
   - get 空结果提示
   - clean 成功/空结果提示

## 阶段二：传输层与用例层接线

1. `validation.go` 新增 4 组校验。
2. `server.go` 新增 4 个 RPC 实现。
3. app 层新增 `ScratchpadUseCase`。
4. 新增独立范围校验能力，确保不触碰 `vmm_sessions` 自动创建链。
5. 新增 scratchpad 专属 GC 窄口，不把 scratchpad CRUD 混入 retention store 主接口。

## 阶段三：存储层实现

1. SQLite：
   - 建表
   - 索引
   - `plans + nodes` 的 CRUD + plan guard 查询
2. PostgreSQL：
   - schema
   - 索引
   - `plans + nodes` 的 CRUD + plan guard 查询
3. 管理链路同步：
   - delete project
   - delete user
   - migrate project
4. 半小时维护接线：
   - 每轮硬删 `updated_timestamp` 超过 15 天的 scratchpad session

## 阶段四：文档与测试

1. README 补充 scratchpad 接口说明。
2. gRPC 对接文档和测试文档补示例。
3. 新增一份 `docs/` 下的中英文体系说明文档，作为 DWM 对外介绍与内部设计入口。
   - 核心命名：
     - `DWM - 确定性工作记忆中枢`
     - `Deterministic Working Memory`
     - 核心代号：`Cognitive Anchor / 认知锚点`
   - Slogan：
     - `在概率的混沌中，锚定确定性的认知边界。`
     - `Anchoring deterministic logic in the chaos of probability.`
   - 文档内容必须覆盖：
     - 行业痛点
     - DWM 的四大核心机制
     - 与 VMM 主记忆体系的边界
     - 典型使用方式
4. 在实现完成后，继续回填该文档的技术细节章节：
   - gRPC 契约
   - 两表结构
   - plan guard 机制
   - 15 天硬删策略
   - 与宿主框架做上下文压缩恢复时的推荐集成方式
5. 测试覆盖：
   - 空库首次锁定
   - plan 不一致拦截
   - 大小写漂移警告
   - upsert 覆盖
   - delete 单项/批量
   - get 单项/全量
   - get 空结果但不报错
   - clean 空/非空
   - project/user 删除级联
   - project 迁移同步
   - 半小时维护下的 15 天过期强删
   - 强删不进入任何回收站

# 验收标准

1. 4 个 scratchpad gRPC 接口完成，并能通过 `grpcurl` 调用。
2. `vmm_scratchpad_plans / vmm_scratchpad_nodes` 在 SQLite 和 PostgreSQL 都可用。
3. `session_id` 在 scratchpad 表中以字符串形式直接存储，不依赖 `vmm_sessions`。
4. `Upsert / Delete` 在 plan mismatch 时能正确拦截。
5. 大小写漂移时可放行，但返回结果带强警告。
6. `Get` 始终以 `items[]` 返回。
7. `Get` 在无数据时不报错，并返回英文提示消息。
8. `Clean` 能清空当前 session scratchpad 全部数据，并返回英文消息。
9. `DeleteProject / DeleteUser / MigrateProject` 不会遗留或污染 scratchpad 数据。
10. 超过 15 天未更新的 scratchpad session 会在半小时维护周期内被强制硬删。
11. `Delete` 在无 plan 时不会锁定任何计划，并返回英文指导消息。
12. 中英文 DWM 体系文档完成，并在落地后补齐技术细节。
13. 文档同步完成。

# 当前已确认的关键决策与默认假设

## 1. Delete 首次调用行为

当前已确认：

- `Delete` 在数据库为空时不锁定 plan
- 不创建 `vmm_scratchpad_plans`
- 直接返回空结果提示

## 2. `status` 传输格式

当前已确认：

- proto 层使用稳定 enum：
  - `SUCCESS`
  - `FAILED`
- 文档中明确映射为“成功/失败”
- 对接文档额外说明：
  - 当开放给 AI Agent 使用时，调用方应主动把 enum 转译成自身语义标签
  - 不建议把 proto 枚举名直接原样喂给大模型

## 3. Delete 批量字段命名

我当前推荐：

- 单项：`key`
- 批量：`keys[]`

而不是：

- `items[]=key`

原因是 transport 更清晰，也更符合当前 proto 风格。

如果您更希望严格按 `items[]=key` 来设计，我也可以调整。

当前已确认保留：

- 单项：`key`
- 批量：`keys[]`

## 4. `msg` 语言

当前已确认：

- `msg` 固定使用英文自然语言提示
- 所有 mismatch、warning、empty、success 文本都按英文模板统一输出

## 5. 空结果行为

当前已确认：

- `Delete`
  - 不做锁定
  - 如果当前 session 尚未创建任何 scratchpad plan，返回英文提示：
    - `No scratchpad plan exists for the current session. Create records first.`
- `Get`
  - 没有数据时不报错
  - 返回成功状态
  - `items = []`
  - 返回英文提示：
    - `No scratchpad records found for the current session.`

## 6. DWM 体系说明文档

当前已确认：

- 需要在 `docs/` 下新增一份中英文说明文档。
- 该文档先承担“体系介绍”职责，说明：
  - DWM 的命名、定位、Slogan
  - 上下文压缩、任务污染、格式漂移、工具 I/O 拥堵四大痛点
  - `Task Lock Guardrails / Forgive-but-Warn Engine / Zero-shot Auto-Injection / Batch Upsert Pipeline`
- 在功能完工后，还要继续补写“详细技术说明”章节。

## 7. 数据库字段命名默认假设

当前默认采用：

- 外部 gRPC 字段继续使用 `session_id`
- 数据库列使用 `session_key`

原因：

- 这样可以和传统 `vmm_sessions.id` 的数值主键明确区分
- 更符合“scratchpad 独立 session 定位键”的语义

---

## 执行变更总结

### 1. 核心修复与调整概述

本次已经完成 DWM / scratchpad 独立支线的完整落地，核心结果如下：

1. 新增 4 个 gRPC 接口：
   - `ScratchpadUpsert`
   - `ScratchpadDelete`
   - `ScratchpadGet`
   - `ScratchpadClean`
2. 新增独立数据表：
   - `vmm_scratchpad_plans`
   - `vmm_scratchpad_nodes`
3. 完成独立 usecase、独立存储端口、SQLite/PostgreSQL 双适配器实现。
4. 完成 plan guard：
   - 大小写不敏感比对
   - mismatch 拦截
   - 格式漂移 warning 放行
   - `Delete` 空范围不锁 plan
5. 完成 15 天未更新 scratchpad 的硬删除治理，并接入现有半小时维护时钟。
6. 完成 README、gRPC 文档、接口测试文档、架构总览文档和 DWM 中英文专题文档同步。

### 2. 📂文件变更清单

新增文件：

- `docs/dwm-working-memory-guide_CN_EN.md`
- `internal/logic/domain/scratchpad.go`
- `internal/app/usecase/scratchpad.go`
- `internal/app/usecase/scratchpad_test.go`
- `internal/adapters/outbound/vldb_sqlite/scratchpad.go`
- `internal/adapters/outbound/vldb_sqlite/scratchpad_test.go`
- `internal/adapters/outbound/vldb_postgres/scratchpad.go`

修改文件：

- `README.md`
- `docs/api-test-guide_CN.md`
- `docs/grpc-integration-guide_CN.md`
- `docs/hierarchy-grpc-design_CN.md`
- `docs/retention-governance-guide_CN.md`
- `internal/adapters/inbound/grpcapi/proto/v1/vmm.proto`
- `internal/adapters/inbound/grpcapi/proto/v1/vmm.pb.go`
- `internal/adapters/inbound/grpcapi/proto/v1/vmm_grpc.pb.go`
- `internal/adapters/inbound/grpcapi/server.go`
- `internal/adapters/inbound/grpcapi/validation.go`
- `internal/adapters/outbound/vldb_sqlite/store.go`
- `internal/adapters/outbound/vldb_sqlite/schema_migrations.go`
- `internal/adapters/outbound/vldb_postgres/schema.go`
- `internal/adapters/outbound/vldb_postgres/helpers.go`
- `internal/adapters/outbound/vldb_postgres/contracts.go`
- `internal/adapters/outbound/vldb_postgres/debug_clean.go`
- `internal/adapters/outbound/vldb_postgres/workspace_admin.go`
- `internal/app/app.go`
- `internal/app/ports/interfaces.go`
- `internal/app/usecase/retention.go`
- `internal/app/usecase/retention_test.go`

### 3. 💻关键代码调整详情

1. gRPC 层
   - 在 `vmm.proto` 中新增 scratchpad 请求/响应 message 与 `ScratchpadStatus` 枚举。
   - 在 `server.go` 中新增 4 个 RPC handler，并完成 usecase 映射。
   - 在 `validation.go` 中新增 scratchpad 请求规范化与校验逻辑，支持单项简写合并。

2. 领域与用例层
   - 新增 `ScratchpadScope / ScratchpadPlanRecord / ScratchpadItem / ScratchpadMutationResult` 等模型。
   - 新增 `ScratchpadUseCase`，实现：
     - 首写建 plan
     - `Delete` 空范围不锁定
     - `Get` 空数据成功返回
     - `plan_name` mismatch 拦截
     - case drift warning 放行

3. 存储层
   - SQLite 与 PostgreSQL 都新增 `plans + nodes` 双表模型。
   - SQLite 增加 schema `18 -> 19` 迁移。
   - 两种存储都支持：
     - `Load/CreatePlan`
     - `Upsert/Delete/List/Clean`
     - `DeleteExpiredScratchpadSessions`
   - 补齐 `DeleteProject / DeleteUser / MigrateProject` 对 scratchpad 的级联清理与迁移冲突保护。

4. 维护链路
   - 在 `RetentionUseCase` 中增加独立 scratchpad maintenance port。
   - 复用现有半小时维护时钟执行 15 天过期硬删除。
   - scratchpad 清理不进入 recycle trash，不提供恢复路径。

5. 文档
   - 新增 DWM 中英文体系文档，并补齐当前实现细节。
   - README、gRPC 对接说明、接口测试说明、架构总览、retention 文档都已同步到当前真实行为。

6. 测试与验证
   - 新增 usecase 测试，覆盖：
     - `Delete` 空范围不锁定
     - `Get` 无数据成功返回
     - mismatch 拦截
     - format drift warning 放行
   - 新增 SQLite scratchpad GC 测试，覆盖计划表按 `id` 删除。
   - 新增 retention scratchpad 调度测试，覆盖 retention 关闭时 scratchpad GC 仍可运行。
   - 已通过：
     - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
     - `go test ./...`
     - `go vet ./...`

### 4. ⚠️遗留问题与注意事项

1. 当前 DWM 的 15 天过期窗口是固定实现值，不是独立配置项。
2. `ScratchpadStatus` 采用 proto enum；宿主框架若直接对接 AI Agent，仍应先做语义转译。
3. scratchpad 目前定位为确定性工作记忆缓存，不参与长期记忆检索、画像系统或 recycle trash。

## 后续修订说明（20260405-30）

本计划落地后，后续在 `20260405-30-DWM_SCRATCHPAD_CONTRACT_ALIGNMENT.md` 中继续收敛了以下内容：

1. `ScratchpadUpsert`
   - 从“单项与批量可自动合并”收敛为“禁止混传，混传直接报错”。
2. `ScratchpadDelete`
   - 从“单键与批量可自动合并”收敛为“禁止混传，混传直接报错”。
3. `ScratchpadGet`
   - 补充返回 `plan_name / item_count / updated_timestamp` metadata。
4. `ScratchpadUpsert / ScratchpadDelete`
   - 补充稳定计数字段，便于宿主框架调试与集成。
5. DWM 文档
   - 从单文件中英混写，拆分为独立中文与英文两份文档。
6. scratchpad 与项目迁移链路
   - 移除对 `MigrateProject` 的参与，只保留 `DeleteProject / DeleteUser` 的级联清理。
