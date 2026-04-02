# VMM OSS Local (Go)

VulcanMemoryMesh 当前主线只保留本地版、gRPC 版和两条核心业务链：

- `PreCheck`
- `PostAction`

当前运行时的定位是：

- 用 `project_id + user_id + session_id` 做确定性层级寻址
- 默认用 SQLite 保存层级、session、turn 记录、turn 提炼结果与长期 SQL 数据
- 保留 DuckDB 兼容关系库存储接口，便于调试和迁移期对照
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
- [画像节点生命周期与渲染方案（中文）](./docs/profile-node-lifecycle_CN.md)
- [画像 gRPC 查询与手工指令接口（中文）](./docs/profile-grpc-interfaces_CN.md)

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
- `GetProfileNodes`
- `GetProfileBundle`
- `ApplyProfileInstruction`
- `SearchMemoryEvents`
- `GetTurnDetails`
- `PreCheck`
- `PostAction`

### 数据后端

当前主线默认保留两条本地数据线：

- SQLite：默认关系库存储，负责层级、用户、session、turn 记录和长期 SQL 记录
  - 适配层优先使用 typed params、`ExecuteBatch` 和 sqlite 网关声明的可重试 trailer 语义
- LanceDB：向量写入、检索和删除

当前也保留一条兼容关系库存储路径：

- DuckDB：兼容 provider，可在配置中显式切换，用于迁移期对照和兼容转换

运行时已经移除：

- HTTP 服务
- 应用内 TLS
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
   - 先按 `user / timeline / assistant` 组装一条脱水 turn 记录
   - 当 `timeline` 为空时，先过 `NoiseGate`
   - 如果不过滤，则写入 `vmm_turn_records`
   - 同步更新 `vmm_sessions.turn_count / summarize_budget / updated_timestamp`
   - turn 写入成功后，把对应 `session` 投递到后台队列
   - 队列优先处理显式入队 session，同时每 30 秒扫描一次：
     - 空闲超时但仍有待处理 turn 的 session
     - 已到期的 active 画像节点
   - 当满足任一条件时，触发一次批量 LLM 分析：
     - 待处理 turn 数达到阈值
     - 待处理 token 预算达到阈值
     - 超过空闲阈值被强制处理
   - 批量分析输入会包含：
     - 最近若干条已提炼的历史 `details`
     - 当前待处理原始 turn 脱水 JSON
     - 当前 session 下仍活跃的旧记忆节点锚点
   - `analyze_session_batch` 会返回：
     - 每条待处理 turn 的 `details`
     - 每条待处理 turn 的 `memory_nodes[]`
     - 每条待处理 turn 的 `profile_nodes[]`
     - 需要淘汰的旧记忆 `obsolete_memory_turn_ids`
   - 如果批次里有 `profile_nodes[]`：
     - 会把当前 user/project 下仍然 `active` 且未过期的画像节点，与本批次新画像候选一起送入 `review_profile_nodes`
     - 如果本批次只有 user 或只有 project 画像候选，则只把对应一侧送进 LLM，不会把缺失侧作为空块一起传入
     - 自动提炼与画像评审都要求按领域拆分节点，不能把饮食偏好、生活习惯、编程语言偏好、项目技术栈等无关主题揉成一条综合画像
   - 对新 `memory_nodes[].abstract` 生成 embedding，并先写入 LanceDB
  - 只有 LanceDB 成功后，才会批量回写关系库存储（默认 SQLite，兼容 DuckDB）：
     - `vmm_turn_records.details / details_budget / extracted_status`
     - `vmm_memory_nodes`
     - `vmm_profile_nodes`
     - `vmm_sessions.last_summarized_id / summarize_budget`
  - 画像评审完成后，会在关系库存储中写入新的画像节点、标记被替代旧节点，并由后端基于有效节点重新渲染 `vmm_users.profile / vmm_projects.profile`
   - 队列扫描时还会同步做一次过期画像收敛：把到期的 `active` 画像节点标成 `expired`，并重建受影响的 user/project 画像文本
   - 如果 LLM 判定旧记忆 turn 需要淘汰，会把对应 `vmm_memory_nodes.node_status` 标成 `superseded`
  - DuckDB 成功提交后，会删除 LanceDB 中对应的旧向量行
  - `vmm_memory_nodes.vector_id` 与 LanceDB 行 `id` 一一对应
  - LanceDB 行里的 `session_id` 会保存真实来源 session
  - `metadata_json` 只保留 `turn_id / category / details` 这类顶层列之外的补充信息
  - 如果 DuckDB 在最后回写阶段失败，会反向删除刚写入的 LanceDB 向量行
   - `vmm_teams.profile / vmm_spaces.profile` 不参与 post-action 自动合并，但现在支持通过显式手工画像指令重建

### 画像接口

当前画像相关 gRPC 能力拆成三条独立方法：

- `GetProfileNodes`
- `GetProfileBundle`
- `ApplyProfileInstruction`

`GetProfileNodes` 的特点：

- 只返回单个目标下当前 `active` 的原子化画像节点
- 不提供 `all` 过滤
- 不返回渲染后的 profile Blob
- 返回内容以节点编号、内容、`P/L/W` 相关元数据和来源信息为主

`GetProfileBundle` 的特点：

- 输入固定是：
  - `project_id`
  - `user_id`
- 输出模式支持：
  - `full`
    - 服务端直接返回可注入的组合提示词
    - 这是权威输出，调用方应直接消费 `combined_text`
    - 为避免重复拼接，辅助说明字段和拆分字段会留空
    - `include_explanation`
      - 省略时默认开启
      - 打开时，会把 `P/L/W` 说明与 `[TEAM] / [SPACE] / [PROJECT] / [USER]` 的含义直接内嵌进 `combined_text`
      - 关闭时，只返回正文结构
  - `split`
    - 服务端只分别返回 `[TEAM] / [SPACE] / [PROJECT] / [USER]` 四段正文
    - 不返回完整组合文本
    - `include_explanation` 在该模式下不会额外返回说明字段
- 组合文本固定强调环境约束优先级：
  - `Project > Space > Team`
- 组合结果始终显式保留结构标签：
  - `[TEAM]`
  - `[SPACE]`
  - `[PROJECT]`
  - `[USER]`
- 这条接口不触发 LLM，只做确定性拼接

`ApplyProfileInstruction` 的特点：

- 接收单个目标上的显式自然语言画像指令
- 指令不会绑定 `turn_id`
- 关系库存储中这类节点的 `vmm_profile_nodes.turn_id` 会保持 `NULL`
- 服务端会先写入 `vmm_profile_instructions`
- 再把当前 active 节点与这条显式指令交给 `review_profile_instruction`
- 同目标同指令的并发调用会复用第一次进行中的结果，不会重复触发第二次 LLM 评审
- 同一目标上的不同手工指令会按目标串行执行，避免基于同一批旧节点并发写回
- 最后持久化新节点、退役旧节点，并重建对应 scope 的 profile 文本
- 如果一条手工指令同时涉及多个领域，也必须拆成多条画像节点，不能生成跨领域综合节点
- 但“按领域拆分”不等于“一个名词一条节点”：同领域、同语义方向、同生命周期层级的并列事实可以合并进一条节点
- 当默认 SQLite provider 返回 `SQLITE_BUSY / SQLITE_LOCKED / SQLITE_SCHEMA` 且网关通过 trailer 标记为可重试时：
  - 适配层会先做有界指数退避重试
- 当关系库存储 provider 返回类似 `resource deadlock would occur` / `Failed to commit` 的“提交结果不确定”错误时：
  - 服务端会先回查 `vmm_profile_instructions`、`vmm_profile_nodes`、退役状态和最终 profile Blob
  - 如果副作用其实已经落库，则会把这次请求收敛成成功
  - 只有回查也无法确认最终状态时，才会向客户端返回 `STORAGE_OUTCOME_UNCERTAIN`
- 当出现 `STORAGE_OUTCOME_UNCERTAIN` 时，服务端不会立刻把该 instruction 再补写成 `failed`
  - 目的是避免在坏连接或污染连接上继续追加状态写入，把一次不确定提交放大成重复节点或脏状态

目标范围支持：

- `USER`
- `PROJECT`
- `TEAM`
- `SPACE`

权限规则：

- `USER / PROJECT`
  - 手工指令属于高权威输入，但后端仍会按规则施加最低 `P / L` 地板
- `TEAM / SPACE`
  - 手工指令直接视为最高权限规则
  - 后端会强制钳制到最高权威语义，不允许降级成普通偏好或短期上下文

### 记忆查询接口

当前主动记忆查询相关 gRPC 能力拆成两条独立方法：

- `SearchMemoryEvents`
- `GetTurnDetails`

`SearchMemoryEvents` 的特点：

- 输入固定是：
  - `project_id`
  - `user_id`
  - `query_json`
  - `top_k`
- `query_json` 必须是 JSON 数组，每项结构为：
  - `background`
  - `query`
- 服务端会：
  - 先解析 `project_id + user_id`
  - 对每条 JSON 查询做 embedding
  - 在当前 `team / space / project / user` 范围内搜索向量记忆
- 返回内容会原样回显：
  - `background`
  - `query`
- 每条命中都至少包含：
  - `memory_id`
  - `turn_id`
  - `session_id`
  - `content`
  - `details`
  - `category`
  - `score`

`GetTurnDetails` 的特点：

- 输入固定是：
  - `turn_ids[]`
- 支持单条或多条查询
- 返回的是数据库里保存的脱水 turn 原文，以及服务端已经拆好的具体对话字段：
  - `dehydrated_content`
  - `user_content`
  - `timeline`
  - `assistant_content`
  - `dehydrated_budget`
  - `extracted_status`
  - `details`
  - `details_budget`
  - `created_timestamp`
  - `updated_timestamp`
- 同时还会补充当前 turn 在同一 session 中的上下文编号：
  - `previous_turn_ids`
  - `next_turn_ids`
- 默认返回当前 turn 前后各 `3` 轮的编号，方便上层 AI 继续按 id 发起更细的详情查询
- 适合和 `SearchMemoryEvents` 联动：
  - 先通过向量记忆查询拿到 `turn_id`
  - 再按 `turn_id` 回查脱水原文和相邻编号

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
- `vmm-local --debug-clean ...` 会只连接对应的 SQLite / DuckDB / LanceDB gRPC 网关
- 清理完成后立即退出，不会启动 VMM gRPC 服务

## 配置说明

当前关键配置项：

- `grpc.listen_addr`
- `grpc.max_receive_message_bytes`
- `grpc.request_timeout.workspace`
- `grpc.request_timeout.pre_check`
- `grpc.request_timeout.post_action`
- `relational.provider`
- `sqlite.address`
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
- 同时每 30 秒扫描一次已到期的 active 画像节点，并把它们收敛成 `expired`
- 达到 turn / token / idle 任一阈值，就会触发一次 `analyze_session_batch` prompt
- `analyze_session_batch` 会基于“历史精要 + 待处理原始 turn + 活跃记忆节点”返回整批结构化结果
- 如果本批次有 `profile_nodes`，会统一走一次 `review_profile_nodes`，按 user/project 两侧分别评审新旧画像节点
- 如果有新的 `memory_nodes`，会先写入 LanceDB
- DuckDB 成功回写后，会更新：
  - `vmm_turn_records.details / details_budget / extracted_status`
  - `vmm_memory_nodes`
  - `vmm_profile_nodes`
  - `vmm_sessions.last_summarized_id / summarize_budget`
- `vmm_profile_nodes` 现在保存原子化画像事实节点，包含：
  - `priority`
  - `profile_level`
  - `level_reason`
  - `refresh_weight`
  - `expires_timestamp`
  - `superseded_by_id`
  - `profile_date`
- `vmm_users.profile / vmm_projects.profile` 不再作为画像合并输入，而是由当前有效画像节点自动重建的渲染结果
- 自动重建出来的 scope `profile` 文本现在只保存正文时间轴，不再带 `[Profile Legend]` 说明头
- 正文仍然按日期输出，并在每条记录上显示 `[P?][L?][W?]`
- 如果调用方需要 `P/L/W` 说明，应通过 `GetProfileBundle` 的 `full` 模式在输出层按需附加；省略 `include_explanation` 时默认开启
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
