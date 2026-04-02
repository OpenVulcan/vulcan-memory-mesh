# VMM OSS Local (Go)

VulcanMemoryMesh 当前主线只保留本地版、gRPC 版和两条核心业务链：

- `PreCheck`
- `PostAction`

当前运行时的定位是：

- 用 `project_id + user_id + session_id` 做确定性层级寻址
- 默认用 SQLite 保存层级、session、turn 记录、turn 提炼结果与长期 SQL 数据
- 用 LanceDB 保存向量数据
- 由 Caddy 等外部反向代理负责 TLS

## 文档导航

- [层级数据模型与 gRPC 设计（中文）](./docs/hierarchy-grpc-design_CN.md)
- [gRPC 对接说明（中文）](./docs/grpc-integration-guide_CN.md)
- [gRPC 接口测试说明（中文）](./docs/api-test-guide_CN.md)
- [post-action 接口说明（中文）](./docs/post-action-guide_CN.md)
- [记忆准入噪声门说明（中文）](./docs/noise-gate-guide_CN.md)
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

运行时已经移除：

- HTTP 服务
- 应用内 TLS
- 旧兼容关系库存 provider
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

当前 `PreCheck` 已恢复为实时两层流程：

- 完成请求校验与范围解析
- 读取最近 turn 窗口，并在 token 预算内混合：
  - 已提炼 turn 的 `details`
  - 未提炼 turn 的脱水原文
- 第一层 `extract_intent` 判断是否需要记忆，并生成多条向量检索语句
- 通过统一记忆检索接口批量向量化这些检索语句并召回长期候选
  - 当前服务端检索链是：`vector + lexical + RRF + rerank(optional) + Weibull + context-aware scoring + MMR`
- 第二层 `review_precheck_memory` 只采纳对当前请求真正有帮助的候选编号
- 仅对被采纳的记忆写回生命周期计数与有效期
- 将稳定画像和被采纳记忆一起组装为 `context_text / context_items`
- 当某一步降级时，仍会尽量返回可用的画像上下文，并把 `degraded=true`

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
5. 先按 `user / timeline / assistant` 组装一条脱水 turn 记录
6. 当 `timeline` 为空时，先过 `NoiseGate`
7. 如果不过滤，则写入 `vmm_turn_records`
8. 同步更新 `vmm_sessions.turn_count / summarize_budget / updated_timestamp`
9. turn 写入成功后，立即返回 `accepted=true`
10. 后台工作器异步读取 pending turn，并发起单轮 `analyze_turn`
11. 单轮分析输入会包含：
    - 最近若干条已提炼完成的历史 `details`
    - 当前 turn 的原始脱水 JSON
    - 当前 session 下仍活跃的旧记忆节点锚点
      - 会携带 `support_count / rebuttal_count` 作为既有证据强度提示
    - 当前 session 在上次提炼观察之后新增的 `recent_grpc_memory_writes`
12. `analyze_turn` 会返回：
    - 当前 turn 的 `turn_id`
    - 当前 turn 的 `details`
    - 当前 turn 的 `memory_nodes[]`
      - 每条 `memory_nodes[]` 可选携带 `context_edges[]`
      - 每条 edge 只允许包含 `context_key / context_value / relation(support|rebuttal)`
    - 当前 turn 的 `profile_nodes[]`
    - `superseded_memory_ids`
13. 如果当前 turn 有 `profile_nodes[]`：
    - 会把当前 user/project 下仍然 `active` 且未过期的画像节点，与本轮新画像候选一起送入 `review_profile_nodes`
    - 如果本轮只有 user 或只有 project 画像候选，则只把对应一侧送进 LLM，不会把缺失侧作为空块一起传入
    - 自动提炼与画像评审都要求按领域拆分节点，不能把饮食偏好、生活习惯、编程语言偏好、项目技术栈等无关主题揉成一条综合画像
14. 对新 `memory_nodes[].abstract` 生成 embedding，并先写入 LanceDB
15. 只有 LanceDB 成功后，才会回写关系库存储（默认 SQLite）：
    - `vmm_turn_records.details / details_budget / extracted_status`
    - 统一后的 `vmm_memory_nodes`
      - 包含聚合后的 `support_count / rebuttal_count`
    - `vmm_memory_context_edges`
    - `vmm_profile_nodes`
16. 画像评审完成后，会在关系库存储中写入新的画像节点、标记被替代旧节点，并由后端基于有效节点重新渲染 `vmm_users.profile / vmm_projects.profile`
17. 后台定时维护仍会做一次过期画像收敛：把到期的 `active` 画像节点标成 `expired`，并重建受影响的 user/project 画像文本
  - `vmm_memory_nodes.vector_id` 与 LanceDB 行 `id` 一一对应
  - LanceDB 行里的 `session_id` 会保存真实来源 session
  - `metadata_json` 只保留 `category / details / source_kind / scope_level / priority / memory_level` 这类补充信息
  - 如果 SQLite 在最后回写阶段失败，会反向删除刚写入的 LanceDB 向量行
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
.\make.bat run --debug-clean sqlite
.\make.bat run --debug-clean lancedb
.\make.bat run --debug-clean all
```

说明：

- `make` 只负责透传参数，不在脚本里直接做数据库清理
- `vmm-local --debug-clean ...` 会只连接对应的 SQLite / LanceDB gRPC 网关
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
- `lancedb.address`
- `lancedb.table_name`
- `lancedb.vector_column`
- `llm.*`
- `embedding.*`
- `memory_pipeline.*`
- `rerank.*`
- `noise.*`
- `post_action.input_mode`
- `post_action.session_analysis_turn_threshold`
- `post_action.session_analysis_token_threshold`
- `post_action.session_analysis_idle_timeout`
- `post_action.session_analysis_history_turns`
- `post_action.session_analysis_max_input_tokens`

`memory_pipeline` 下当前新增的是“向量 + SQLite FTS5 混合召回”参数：

- `max_search_keywords`
  - 仍用于 `PreCheck` 的关键词扇出上限
- `min_similarity_score`
  - 仍用于 `PreCheck` 过滤低质量召回候选
- `hybrid_enabled`
  - 是否启用“向量召回 + SQLite FTS5 lexical 召回 + RRF 融合”链路；关闭时保持纯向量检索
- `lexical_top_k`
  - 每个 query group 最多取多少条 lexical 候选参与 RRF 融合
- `rrf_k`
  - Reciprocal Rank Fusion 的平滑常数；值越大，不同通道的名次差异被压得越平缓
- `mmr_enabled`
  - 是否在融合或 rerank 之后启用 MMR 多样性控制；开启后会优先保留更分散的候选，减少近重复记忆挤占名额
- `mmr_lambda`
  - MMR 的相关性与多样性权重，越接近 `1` 越偏向原始相关性排序，越接近 `0` 越偏向去重分散
- `weibull_enabled`
  - 是否启用读时 Weibull 衰减；开启后会在融合或 rerank 之后，对陈旧且强化较弱的记忆做自然降权
- `weibull_shape`
  - Weibull 形状参数；值越大，衰减在后段越陡
- `weibull_scale_hours`
  - Weibull 基础时间尺度（小时）；值越大，整体衰减越慢
- `weibull_min_multiplier`
  - 读时衰减的最低保底乘子，避免未过期记忆在排序上被直接打到接近零
- `weibull_reinforce_weight`
  - 强化次数对衰减尺度的放大权重；越大表示“访问强化”效果越明显
- `weibull_cross_session_boost`
  - 跨 session 采纳次数对衰减尺度的额外放大权重，用于保护真正跨任务复用的记忆

`rerank` 下当前新增的是“向量召回后的第二阶段重排序”参数：

- `enabled`
  - 是否启用 DashScope rerank；关闭时检索链保持当前的纯向量顺序
- `provider`
  - 当前仅支持 `dashscope`
- `endpoint`
  - 默认使用阿里云 DashScope `text-rerank` 地址
- `api_key`
  - 可单独配置；若为空，运行时会回退复用 `llm.api_key`
- `model`
  - 当前默认 `qwen3-vl-rerank`
- `top_n`
  - 每个 query group 最多送多少条首轮向量命中进入 rerank
- `timeout`
  - 单次 rerank HTTP 调用预算；超时或失败时检索链会降级回原始向量排序

`post_action` 下当前保留 5 个与异步单轮提炼窗口和恢复扫描相关的参数：

- `session_analysis_turn_threshold`
  - 兼容保留参数，当前主线不会再按“累计待处理 turn 数”触发批量提炼
- `session_analysis_token_threshold`
  - 兼容保留参数，当前主线不会再按“累计待处理 token”触发批量提炼
- `session_analysis_idle_timeout`
  - 当前仍用于后台恢复扫描：如果某个 session 的 pending turn 长时间未被消费，会在超过该阈值后被重新入队
- `session_analysis_history_turns`
  - 每次单轮 `analyze_turn` 最多回带多少条历史 `details` 精要作为参考
- `session_analysis_max_input_tokens`
  - 单次 `analyze_turn` 允许发送给 LLM 的总输入预算上限，统计口径是“当前 turn 原始脱水预算 + 历史精要预算”

当前主线的行为是：

- `PostAction` 成功写入 turn 并完成入队后，后台会尽快发起一次 `analyze_turn`
- `analyze_turn` 会基于“历史精要 + 当前原始 turn + 活跃记忆节点”返回当前这一轮的结构化结果
- 如果本轮有 `profile_nodes`，会统一走一次 `review_profile_nodes`，按 user/project 两侧分别评审新旧画像节点
- 如果有新的 `memory_nodes`，会先写入 LanceDB
- SQLite 成功回写后，会更新：
  - `vmm_turn_records.details / details_budget / extracted_status`
  - `vmm_memory_nodes`
  - `vmm_profile_nodes`
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
- 如果 SQLite 回写失败，会尝试回滚这次新增的 LanceDB 向量
- 后台 worker 还会按 `session_analysis_idle_timeout` 周期性补扫陈旧 pending session，帮助崩溃或临时失败后的恢复

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
