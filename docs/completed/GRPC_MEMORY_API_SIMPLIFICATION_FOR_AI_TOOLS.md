# gRPC Memory API Simplification For AI Tools

## 任务目标

把当前面向 AI 工具调用的记忆类 gRPC 接口收缩为“最小必要信息”形态，降低模型调用复杂度、减少无意义字段、降低错误率，并使调用流程回归到以下简化模式：

1. 搜索接口只负责返回“有哪些候选记忆”。
2. 结果里只暴露 AI 真正需要的最小字段：`memory_id`、`source_turn_id`、`abstract`、`details_preview`、`category`。
3. 详情接口只保留 turn 详情查询；不再保留 `GetMemoryDetails`。
4. 主动写入接口仅在 AI 明确要写记忆时使用，并继续支持必要的写入控制字段。
5. 搜索请求去掉 `background` 设计，不再要求分组 JSON 结构；改成简单字符串查询列表。

## 背景与问题判断

当前接口设计更偏向“内部系统完整传输”，而不是“AI 工具最小调用契约”，存在以下问题：

1. `SearchMemoryEvents` 目前要求 `query_json`，其中每项包含 `background + query`，对 AI 工具来说结构复杂且容易出错。
2. 搜索结果当前携带 `MemoryRef`、`SourceRef`、`scope_level`、`source_kind`、`session_id`、数值型 `category` 等大量内部字段，AI 实际只关心：
   - 这条记忆的 `memory_id`
   - 是否有来源 `source_turn_id`
   - 摘要和预览
   - 类别语义
3. `GetMemoryDetails` 与搜索结果存在职责重叠。既然搜索返回的就是 memory 维度结果，而 AI 后续真正需要深挖的是 turn 对话详情，那么 `GetMemoryDetails` 的价值很低。
4. `GetTurnDetails` 当前返回原始脱水 JSON 和较多内部预算字段，不适合 AI 直接消费。

## 目标接口形态

### 1. SearchMemoryEvents

建议将请求从：

- `user_id`
- `project_id`
- `query_json`
- `top_k`

调整为：

- `user_id`
- `project_id`
- `repeated string queries`
- `top_k`

建议将返回从“完整内部命中结构”调整为“AI 最小命中结构”：

- `query_index`
- `query`
- `hits`

每条 hit 仅返回：

- `memory_id`
- `source_turn_id`
- `abstract`
- `details_preview`
- `category`

其中：

- `source_turn_id = 0` 代表该记忆没有来源 turn，属于独立记忆，例如 AI 主动写入。
- `category` 不再返回数字，而是直接返回英文标签，例如：
  - `general`
  - `architecture_decision`
  - `tech_spec_api`
  - `business_logic`
  - `requirement_todo`
  - `project_context`
  - `logical_bug_debt`
  - `security_policy`

### 2. GetTurnDetails

保留该接口，但改成直接面向 AI 使用的结构化返回，不再返回脱水 JSON 原文。

建议请求保持：

- `repeated uint64 turn_ids`

建议返回每条 turn 仅包含：

- `turn_id`
- `user_question`
- `timeline`
- `assistant_answer`
- `detail`
- `previous_turn_ids`
- `next_turn_ids`

其中：

- `timeline` 必须是结构化数组，而不是 JSON 字符串。
- `previous_turn_ids` 只返回前 3 条。
- `next_turn_ids` 只返回后 3 条。
- 若没有相邻 turn，返回 `[]`。

### 3. GetMemoryDetails

建议移除该接口，不再作为对外 AI 工具契约的一部分。

理由：

1. 搜索本身返回的就是 memory 维度结果。
2. AI 后续最有价值的追溯动作是“查看来源 turn 对话”，而不是再次读取同一条 memory 的内部落库字段。
3. 继续保留会让工具选择路径变复杂，增加模型误调用率。

### 4. WriteMemories

保留该接口，但对外文档与 proto 语义要转向“仅保留写入所需最小控制项”。

写入项保留：

- `scope_level`
- `abstract`
- `details`
- `category`
- `priority`
- `memory_level`

其中：

- `scope_level` 直接传数字概念值，不传完整字符串名称。
- `priority` 直接传数字概念值，不传完整字符串名称。
- `memory_level` 直接传数字概念值，不传完整字符串名称。
- `expires_timestamp` 对外移除，不再由 AI 工具指定有效期。
- 有效期由系统按标准算法自动计算。

返回仅保留：

- `memory_id`
- `deduped`

不再返回 `turn_id`，因为 AI 主动写入记忆默认就是独立记忆，没有来源 turn。

## 详细执行步骤

### 步骤 A：proto 契约重构

1. 修改 `vmm.proto` 中以下消息定义：
   - `SearchMemoryEventsRequest`
   - `MemorySearchHit`
   - `MemorySearchGroupResult`
   - `GetTurnDetailsResponse` 相关结构
   - `WriteMemoryResultItem`
   - `WriteMemoryItem`
2. 删除或废弃 `GetMemoryDetails` 的 rpc 与相关 message。
3. 重新生成 pb.go / grpc.pb.go。

### 步骤 B：gRPC 服务映射层重构

1. 修改 `server.go` 中以下 RPC 的输入输出映射：
   - `SearchMemoryEvents`
   - `GetTurnDetails`
   - `WriteMemories`
2. 删除 `GetMemoryDetails` 的 transport 层映射。
3. 把 category 从内部数字映射为英文标签字符串。

### 步骤 C：use case 层收口

1. 把 `MemoryQueryCommand` 的输入从 `QueryJSON` 重构为简单 query 列表。
2. 删除 `background` 相关解析、归一化、回显和上下文评分依赖。
3. 保留向量检索与混合检索主链路，但将查询输入简化为单纯 query 文本。
4. 评估并处理以下连带影响：
   - 去重 cache key
   - context evidence scoring
   - precheck 内部若仍调用同一 use case 的兼容策略

### 步骤 D：turn 详情返回收缩

1. 让 `GetTurnDetails` 直接返回：
   - `user_question`
   - `timeline`
   - `assistant_answer`
   - `detail`
   - `previous_turn_ids`
   - `next_turn_ids`
2. 删除脱水 JSON 原文、budget、extracted_status 等 AI 无意义字段的对外暴露。
3. 明确相邻 turn 仅返回前后各 3 条。

### 步骤 E：写入结果收缩

1. `WriteMemories` 请求字段收口为 AI 真正需要的字段。
2. `scope_level / priority / memory_level` 对外统一使用数字概念值，而不是字符串型名称。
3. 去掉 `expires_timestamp` 输入，保留系统内部 TTL / 生命周期自动计算逻辑。
4. 返回值收口为最小必要字段，不继续暴露固定为 0 的 `turn_id`。

### 步骤 F：验证与文档同步

1. 更新 gRPC proto 注释与对接文档。
2. 更新单元测试与集成测试。
3. 核验插件侧 / TUI 调用方是否需要同步改动。
4. 同步更新 `docs/grpc-integration-guide_CN.md`，明确 AI 工具接口已经改成最小必要字段。

## 技术选型与实现原则

### 1. 查询输入结构

选型建议：使用 `repeated string queries`，不再使用 JSON 字符串。

原因：

1. protobuf 原生 repeated string 对 AI 工具最友好。
2. 避免 JSON 转义和嵌套结构错误。
3. 消除 `background` 的概念负担。

### 2. 写入枚举表达方式

选型建议：对外把 `scope_level / priority / memory_level` 统一压缩为数字概念值。

原因：

1. AI 工具更容易稳定地产生数字，而不是枚举全名。
2. 降低模型在大小写、前缀、完整枚举名上的出错概率。
3. 内部仍可继续保留枚举映射，不影响服务端存储与校验。

### 3. category 表达方式

选型建议：对外返回英文标签字符串，而不是数字枚举。

原因：

1. AI 更容易直接理解和复述标签语义。
2. 降低插件侧再做数字映射的复杂度。
3. 保持内部仍可继续使用整数分类，不影响存储层。

### 4. 写入有效期处理方式

选型建议：对外移除 `expires_timestamp`。

原因：

1. 有效期属于系统生命周期策略，不应让 AI 工具承担。
2. 去掉后可显著降低工具调用复杂度。
3. 系统已经具备按 scope / level 计算默认 TTL 的能力。

### 5. Memory 详情接口处理方式

推荐方案：直接移除 `GetMemoryDetails` 对外接口。

备选方案：先标记 deprecated，一期保留、二期删除。

推荐理由：

1. 这次目标是降低 AI 工具误调用率。
2. 若继续保留，会让工具继续误判“是否还要查一遍 memory 详情”。
3. 现有搜索结果加 turn 详情接口已经覆盖主要工具链路。

### 6. compatibility 策略

这是一次破坏性 gRPC 契约调整，建议二选一：

1. 推荐：直接修改 proto，并同步更新当前仓库内全部调用方。
2. 保守：保留旧接口一段时间，同时新增精简版接口，再统一切换。

## 验收标准

1. `SearchMemoryEvents` 不再接收 `background/query_json`，而是直接接收字符串查询列表。
2. 搜索结果不再返回内部 ref 组合与内部状态字段，只返回：
   - `memory_id`
   - `source_turn_id`
   - `abstract`
   - `details_preview`
   - `category`
3. `category` 对外为英文标签，而非数字。
4. `WriteMemories` 的请求不再要求 AI 提供有效期。
5. `WriteMemories` 的 `scope_level / priority / memory_level` 改为数字概念值。
6. `GetTurnDetails` 返回结构化字段，不再要求调用方解析脱水 JSON。
7. `GetMemoryDetails` 不再作为 AI 工具接口使用。
8. `WriteMemories` 返回结构收缩为最小必要字段，不再暴露 `turn_id`。
9. `docs/grpc-integration-guide_CN.md` 与新的 proto 契约保持一致。

## 需要您确认的重大决策

### 决策 1：`GetMemoryDetails` 的处理方式

推荐：直接删除该 RPC 及其 proto message。  
原因：这是最符合“AI 工具最小接口面”的方案。

### 决策 2：搜索请求形态

推荐：`repeated string queries`。  
原因：比“单个字符串”更能保留一次请求多组查询的能力，同时仍远比 JSON 简单。

### 决策 3：兼容策略

推荐：直接修改现有接口并同步仓库内调用方，不做双轨兼容。  
原因：当前目标不是兼容历史 SDK，而是尽快把 AI 工具调用面收缩到正确形态。

## 变更影响范围预估

预计会影响：

1. `internal/adapters/inbound/grpcapi/proto/v1/vmm.proto`
2. 自动生成的 pb/grpc 文件
3. `internal/adapters/inbound/grpcapi/server.go`
4. `internal/adapters/inbound/grpcapi/validation.go`
5. `internal/app/usecase/memory_query.go`
6. 对应测试文件
7. `docs/grpc-integration-guide_CN.md`
8. 当前仓库内所有直接调用上述 RPC 的调试/桥接逻辑

## 补充确认后的冻结项

以下内容已根据最新要求冻结为实现基线：

1. `WriteMemories` 的 `scope_level / priority / memory_level` 对外按数字概念值传输。
2. `WriteMemories` 移除 `expires_timestamp` 输入。
3. `WriteMemories` 返回不再包含 `turn_id`。
4. gRPC 对接文档必须同步修改，不能只改 proto 和代码。
5. `SearchMemoryEvents` 保留 `source_turn_id`，当记忆没有来源 turn 时统一返回 `0`。

## 当前结论

这次改动方向明确，且与你的目标一致：让 AI 工具只面对“搜记忆、看 turn、必要时写记忆”这 3 条简单链路，不再暴露内部持久化细节。

在你确认前，本计划只做设计冻结，不进入实现阶段。

---

## 执行变更总结

### 1. 核心修复与调整概述

1. 已把 AI 工具记忆接口收缩为 `SearchMemoryEvents / GetTurnDetails / WriteMemories` 三条主链路。
2. `SearchMemoryEvents` 已移除 `query_json + background` 设计，改为 `repeated string queries`，并把返回字段收缩为 `memory_id / source_turn_id / abstract / details_preview / category`。
3. `GetTurnDetails` 已改为返回结构化对话字段，不再暴露脱水 JSON、预算字段和内部持久化细节。
4. `WriteMemories` 已改为接受紧凑数字概念值 `scope_level / priority / memory_level`，并移除 `expires_timestamp` 输入；有效期继续由系统默认算法计算。
5. `GetMemoryDetails` 已从对外 gRPC proto 与 transport 测试契约中移除，不再作为 AI 工具接口使用。
6. `docs/grpc-integration-guide_CN.md` 已同步更新为新的 AI 工具对接模型，并补充 `x-trace-id` 必须 ASCII-safe 的说明。

### 2. 📂文件变更清单

新增：

1. `docs/plan/GRPC_MEMORY_API_SIMPLIFICATION_FOR_AI_TOOLS.md`

修改：

1. `internal/adapters/inbound/grpcapi/proto/v1/vmm.proto`
2. `internal/adapters/inbound/grpcapi/proto/v1/vmm.pb.go`
3. `internal/adapters/inbound/grpcapi/proto/v1/vmm_grpc.pb.go`
4. `internal/adapters/inbound/grpcapi/server.go`
5. `internal/adapters/inbound/grpcapi/server_test.go`
6. `internal/adapters/inbound/grpcapi/validation.go`
7. `internal/app/usecase/memory_query.go`
8. `internal/app/usecase/memory_query_test.go`
9. `internal/app/usecase/precheck.go`
10. `internal/app/usecase/precheck_test.go`
11. `docs/grpc-integration-guide_CN.md`

删除：

1. 无额外物理源码删除；对外删除体现在 proto/gRPC 契约中不再暴露 `GetMemoryDetails`。

### 3. 💻关键代码调整详情

1. gRPC proto：
   - 删除 `GetMemoryDetails` 对外 RPC。
   - `SearchMemoryEventsRequest` 改为 `queries[]`。
   - `MemorySearchHit` 改为最小 AI 字段集合。
   - `TurnDetailEntry` 改为结构化对话详情。
   - `WriteMemoryItem` 改为紧凑数字概念值输入。
   - `WriteMemoryResultItem` 改为仅返回 `memory_id + deduped`。
2. transport 映射：
   - `server.go` 增加分类英文标签映射。
   - `server.go` 增加紧凑数字概念值到内部枚举的映射。
   - `server.go` 只向外返回 AI 需要的最小字段。
3. usecase：
   - `MemoryQueryCommand` 改为直接接收 `Queries []string`。
   - 检索规范化、缓存键、上下文信号构建已全部去掉 `background` 依赖。
   - `PreCheck` 侧改为直接生成 query 列表并复用新的统一检索契约。
4. 测试与验证：
   - 已同步修正 gRPC transport 测试与 usecase 测试的旧断言。
   - 已运行 `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - 已运行 `go test ./...`

### 4. ⚠️遗留问题与注意事项

1. 当前内部 domain/usecase 仍保留 `MemoryRef` 与 `GetDetails` 等内部模型/方法，主要用于现有内部实现复用；本次只收缩对外 AI 工具接口面，没有强制清理所有内部抽象。
2. 本仓库当前工作区存在大量与本任务无关的既有未提交变更/删除项，本次未对其做回滚或整理。
3. `vmm.proto` 最后仅补了一次注释准确性修正，没有改变 wire contract，因此未单独重新生成 pb 文件。
