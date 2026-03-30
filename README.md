# VMM OSS Local (Go)

VulcanMemoryMesh 当前主线只保留本地版、gRPC 版和两条核心业务链：

- `PreCheck`
- `PostAction`

当前运行时的定位是：

- 用 `project_id + user_id + session_id` 做确定性层级寻址
- 用 DuckDB 保存层级、session、turn 记录、turn 提炼结果与长期 SQL 数据
- 用 LanceDB 保存向量数据
- 由 Caddy 等外部反向代理负责 TLS

## 文档导航

- [层级数据模型与 gRPC 设计（中文）](./docs/hierarchy-grpc-design_CN.md)
- [gRPC 对接说明（中文）](./docs/grpc-integration-guide_CN.md)
- [gRPC 接口测试说明（中文）](./docs/api-test-guide_CN.md)
- [post-action 接口说明（中文）](./docs/post-action-guide_CN.md)
- [记忆准入噪声门说明（中文）](./docs/noise-gate-guide_CN.md)
- [DuckDB Schema 版本管理说明（中文）](./docs/duckdb-schema-versioning_CN.md)
- [当前未接入主运行时的配置参数清单（中文）](./docs/unused-config-parameters_CN.md)
- [后续记忆提炼与画像合并分析（非决案，中文）](./docs/memory-extraction-analysis_CN.md)

## 当前运行模型

### 入站协议

当前只暴露 gRPC：

- 服务：`vmm.v1.VMMService`

当前主线方法：

- `Healthz`
- `ListProjects`
- `ResolveProject`
- `EnsureProject`
- `DeleteProject`
- `MigrateProject`
- `ResolveUser`
- `ListUsers`
- `DeleteUser`
- `PreCheck`
- `PostAction`

### 数据后端

当前只保留两条本地数据线：

- DuckDB：层级、用户、session、turn 记录、长期 SQL 记录
- LanceDB：向量写入、检索和删除

运行时已经移除：

- HTTP 服务
- 应用内 TLS
- SQLite 运行时支持
- 内存关系库存根 / 内存向量库存根回退

## 核心约束

### 业务入参

`PreCheck` 和 `PostAction` 现在只接受：

- `session_id`
- `user_id`
- `project_id`

说明：

- `user_id`、`project_id` 必须是数字 ID
- `team_id`、`space_id` 不再由客户端传入
- 服务端会先解析 `project_id -> space_id -> team_id`
- 如果 `session_id` 不存在，会自动在 `vmm_sessions` 中创建一条 session 记录
- 如果 `user_id / project_id` 非法或不存在
  - 会在前置范围解析阶段同步直接报错
  - 不会进入 `PreCheck` / `PostAction` 业务逻辑

### PreCheck

当前 `PreCheck` 仍然是保守模式：

- 完成请求校验与范围解析
- 固定返回 `should_inject = false`
- 不做真正的记忆注入

### PostAction

当前 `PostAction` 是新的文本契约入口，只接受：

- `user_content`
- `assistant_content`
- `timeline[]`

服务端流程是：

1. 先解析 `session_id / user_id / project_id`
2. 记录原始日志
3. 清洗 `user_content`、`timeline[].content`、`assistant_content`
4. 记录清洗后日志
5. 立即返回 `accepted=true`
6. 后台继续：
   - 按 `user / timeline / assistant` 组装一条脱水 turn 记录
   - 写入 `vmm_turn_records`
   - 当 `timeline` 为空时，先过 `NoiseGate`
   - 只做 turn 持久化，不在主写链路里直接调 LLM
   - turn 写入成功后，把对应 `session` 投递到后台队列
   - 队列优先处理显式入队 session
   - 同时每 30 秒扫描一次“超过空闲阈值且仍有待处理 turn”的 session
   - 当待处理 turn 数或待处理 token 预算达到阈值时，或空闲时间达到阈值时，触发一次批量 LLM 分析
   - LLM 输入会包含：
     - 最近若干条历史 `details` 精要
     - 当前待处理原始 turn
     - 当前 session 下仍活跃的旧记忆节点编号
   - LLM 会返回：
     - 每条待处理 turn 的 `details`
     - 每条待处理 turn 的 `memory_nodes[]`
     - 每条待处理 turn 的 `profile_nodes[]`
     - 需要淘汰的旧记忆 `turn_id` 列表
   - 如果有 `profile_nodes[]`，会把整批 user/project 画像证据一起送入一次 `merge_profile` prompt
   - 如果本批次只有 user 或只有 project 画像候选，则只把对应那一侧送进 LLM，不会把缺失侧作为空块一起传入
   - 对新 `memory_nodes[].abstract` 生成 embedding，并先写入 LanceDB
   - 只有 LanceDB 成功后，才会批量回写 DuckDB：
     - `vmm_turn_records.details / details_budget / extracted_status`
     - `vmm_memory_nodes`
     - `vmm_profile_nodes`
     - `vmm_sessions.last_summarized_id / summarize_budget`
   - 如果画像合并成功，会同步更新 `vmm_users.profile / vmm_projects.profile`
   - 如果 LLM 判定旧记忆 turn 需要淘汰，会把对应 `vmm_memory_nodes.node_status` 标成 `superseded`
   - 然后删除 LanceDB 中对应的旧向量行
   - `vmm_memory_nodes.vector_id` 与 LanceDB 行 `id` 一一对应
   - LanceDB 行里的 `session_id` 会保存真实来源 session
   - 如果 DuckDB 在最后回写阶段失败，会反向删除刚写入的 LanceDB 向量行
   - `vmm_teams.profile / vmm_spaces.profile` 仍保留给后续显式配置，不做自动合并

## 构建与运行

标准构建入口只有：

```powershell
.\make.ps1 build
```

或：

```powershell
.\make.bat build
```

标准运行产物：

- `output/bin/vmm-local.exe`

标准配置目录：

- `output/configs/`

### 启动示例

```powershell
.\make.bat build
.\output\bin\vmm-local.exe
```

### 用户覆盖目录

```powershell
.\output\bin\vmm-local.exe -config ~/.vmm
```

`-config` 表示“覆盖根目录”，不是单个配置文件路径。

### 调试清库

需要调试时，可以继续通过 `make` 调主程序，但把清理目标通过 `--debug-clean` 传给二进制：

```powershell
.\make.bat run --debug-clean duckdb
.\make.bat run --debug-clean lancedb
.\make.bat run --debug-clean all
```

说明：

- `make` 只负责透传参数，不在脚本里直接做数据库清理
- `vmm-local --debug-clean ...` 会只连接对应的 DuckDB / LanceDB gRPC 网关
- 清理完成后立即退出，不会启动 VMM gRPC 服务

## 配置说明

当前关键配置项：

- `grpc.listen_addr`
- `grpc.max_receive_message_bytes`
- `grpc.request_timeout.workspace`
- `grpc.request_timeout.pre_check`
- `grpc.request_timeout.post_action`
- `duckdb.address`
- `lancedb.address`
- `lancedb.table_name`
- `lancedb.vector_column`
- `llm.*`
- `embedding.*`
- `noise.*`
- `post_action.input_mode`
- `post_action.session_analysis_turn_threshold`
- `post_action.session_analysis_token_threshold`
- `post_action.session_analysis_idle_timeout`
- `post_action.session_analysis_history_turns`
- `post_action.session_analysis_max_input_tokens`

`post_action` 下当前已经接入 5 个批处理参数：

- `session_analysis_turn_threshold`
  - 同一个 session 待处理 turn 达到多少条后，触发一次批量分析条件
- `session_analysis_token_threshold`
  - 同一个 session 待处理原始 turn 的累计 token 预算达到多少后，触发一次批量分析条件
- `session_analysis_idle_timeout`
  - 距离同一个 session 最后一次会话更新时间超过多久后，强制触发一次批量分析
- `session_analysis_history_turns`
  - 每次批处理最多回带多少条历史 `details` 精要作为参考
- `session_analysis_max_input_tokens`
  - 单次批处理允许发送给 LLM 的总输入预算上限，统计口径是“待处理原始 turn 脱水预算 + 历史精要预算”

当前主线的行为是：

- `PostAction` 成功写入 turn 后，只负责入库并投递 session 队列任务
- 队列会优先消费显式入队内容
- 同时每 30 秒扫描一次空闲超时且仍有待处理 turn 的 session
- 达到 turn / token / idle 任一阈值，就会触发一次 `analyze_session_batch` prompt
- `analyze_session_batch` 会基于“历史精要 + 待处理原始 turn + 活跃记忆节点”返回整批结构化结果
- 如果本批次有 `profile_nodes`，会统一走一次 `merge_profile`，并按 user/project 两侧整体合并
- 如果有新的 `memory_nodes`，会先写入 LanceDB
- DuckDB 成功回写后，会更新：
  - `vmm_turn_records.details / details_budget / extracted_status`
  - `vmm_memory_nodes`
  - `vmm_profile_nodes`
  - `vmm_sessions.last_summarized_id / summarize_budget`
- 如果 LLM 判定旧记忆 turn 已被覆盖，会把对应 `vmm_memory_nodes.node_status` 标成 `superseded`，并删除 LanceDB 旧向量
- LanceDB 行里的 `session_id` 会和来源 turn 的 session 保持一致
- 如果 DuckDB 回写失败，会尝试回滚这次新增的 LanceDB 向量

另外，当前还有一批“已经声明但尚未接入主运行时”的配置参数，见：

- [当前未接入主运行时的配置参数清单（中文）](./docs/unused-config-parameters_CN.md)

### 超时模型

- `grpc.request_timeout.pre_check`
  - 表示整次 `PreCheck` RPC 的外层预算
- `pre_check.intent_timeout`
  - 表示未来恢复意图提取后，内部子步骤的预算

当前要求：

- `grpc.request_timeout.pre_check > pre_check.intent_timeout`

### LanceDB 表名规则

`lancedb.table_name` 现在是基础表名。

运行时会自动追加 embedding 维度：

- 基础表名：`vmm_memory_vectors`
- 维度：`1024`
- 实际表名：`vmm_memory_vectors_1024`

这样可以避免不同向量维度共用同一张物理表。

## 开发验证

提交较大改动前，至少执行：

```powershell
go test ./... -count=1
.\make.bat build
```

## TLS 说明

当前应用内不再支持 TLS。

如果需要 TLS、域名或公网入口，请在前面使用 [Caddy](https://caddyserver.com/) 做反向代理。
