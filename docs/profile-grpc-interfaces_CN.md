## 文档目标

这份文档用于固化画像查询与手工画像指令两条 gRPC 线路的实现方向，避免后续再次把“画像读取”和“画像修改”混成同一条重上下文接口。

当前目标是：

- 提供轻量的 active 画像节点查询接口
- 提供基于自然语言指令的手工画像修改接口
- 保持画像事实层仍然落在 `vmm_profile_nodes`
- 让手工画像修改不再依赖 `turn_id`

## 接口拆分原则

画像相关的 gRPC 能力拆成两条独立 RPC：

1. `GetProfileNodes`
2. `ApplyProfileInstruction`

拆分原因：

- 查询链路应尽量轻量，只返回当前有效节点
- 修改链路会触发 LLM 评审与 DuckDB 状态更新，不应和查询混在一起
- 插件并不总是需要一次性拿到全部画像内容

## GetProfileNodes 目标

`GetProfileNodes` 只返回当前 `active` 的原子化画像节点。

它不提供：

- `all` 状态过滤
- rendered profile 文本
- 面向 LLM 的 legend 说明头

这样可以避免：

- 长期运行后一次性拉取过多历史节点
- 上下文膨胀
- 把渲染文本与事实节点混在一起

## GetProfileNodes 请求约束

请求中必须指定一个目标类型：

- `USER`
- `PROJECT`
- `TEAM`
- `SPACE`

字段约束：

- `USER`
  - 必须传 `user_id`
- `PROJECT`
  - 必须传 `project_id`
- `TEAM`
  - 必须传 `project_id`
  - 服务端通过 `project_id -> team_id`
- `SPACE`
  - 必须传 `project_id`
  - 服务端通过 `project_id -> space_id`

## GetProfileNodes 返回内容

返回内容为节点列表，每条节点至少包含：

- `id`
- `content`
- `priority`
- `level`
- `refresh_weight`
- `profile_date`
- `source_kind`
- `source_id`

可选附带：

- `level_reason`
- `expires_timestamp`

接口本身只返回事实节点，不返回最终渲染好的 `profile` Blob。

## ApplyProfileInstruction 目标

`ApplyProfileInstruction` 用于让用户显式提交一段自然语言指令，对指定目标的画像进行增量调整。

典型输入示例：

- `现在不需要 xxx、xxx、xxx 内容，我现在这个项目使用 Go 语言编写。`

它的行为不是直接把这段文本写入 `profile`，而是：

1. 读取当前目标下所有 active 节点
2. 把 active 节点与用户指令一起交给 LLM
3. 让 LLM 决定：
   - 哪些新画像应新增
   - 哪些旧画像应被废弃
   - 新画像的 `priority`
   - 新画像的 `profile_level`
   - 新画像的 `level_reason`
   - 废弃旧画像的原因
4. 由后端把结果写入 DuckDB
5. 最后重建目标对象的 `profile` 文本

## 手工画像与 turn 的关系

手工画像修改不再绑定 `turn_id`。

原因：

- 它来自用户显式配置，不属于对话轮次提炼结果
- 用 `turn_id=0` 或伪造 turn 都会让来源语义变脏

在 DuckDB 中，这类节点会把：

- `vmm_profile_nodes.turn_id = NULL`

因此需要新增独立来源表：

- `vmm_profile_instructions`

同时扩展 `vmm_profile_nodes` 的来源字段：

- `source_kind`
- `source_id`

## vmm_profile_instructions 目标字段

建议字段：

- `id`
- `profile_type`
- `bind_id`
- `instruction`
- `instruction_status`
- `review_result_json`
- `failure_reason`
- `created_timestamp`
- `updated_timestamp`

说明：

- `instruction` 保存用户原始显式画像指令
- `review_result_json` 保存本次 LLM 结构化判定结果，便于调试和追溯

## vmm_profile_nodes 扩展字段

为支持手工画像来源与节点状态原因，增加：

- `source_kind`
- `source_id`
- `status_reason`

语义：

- `source_kind`
  - `turn_extract`
  - `manual_instruction`
  - `system_seed`
- `source_id`
  - 当 `turn_extract` 时指向 `turn_id`
  - 当 `manual_instruction` 时指向 `instruction_id`
- `turn_id`
  - 对 `turn_extract` 保留真实来源 turn
  - 对 `manual_instruction` 为空，不再伪造 `0`
- `status_reason`
  - 记录节点为什么被判定为 `invalid / superseded / expired`

## 手工画像默认级别与权限规则

手工输入往往是用户明确表达的长期要求，因此其默认信息级别必须高于普通 turn 自动提取。

规则建议：

- `manual_instruction` 的新节点默认不低于：
  - `priority = P1`
  - `profile_level = L2`

如果指令语气属于显式覆盖、禁止、必须、统一要求，则默认地板抬高到：

- `priority = P0`
- `profile_level = L2`

但这条规则只适用于：

- `USER`
- `PROJECT`

对于：

- `TEAM`
- `SPACE`

需要额外遵守更强规则：

- 手工画像指令直接视为最高权限规则
- 后端会强制把它们钳制到最高权威语义
- 不能被降级成普通偏好或短期上下文
- 因此它们的最低地板等价于最高权限级别

这里不直接抬高 `refresh_weight`。

原因：

- `refresh_weight` 表示“重复确认/续期次数”
- 手工输入表达的是“高权威来源”
- 两者语义不同，不应混用

因此：

- 手工输入通过 `priority/profile_level` 提高默认信息级别
- `refresh_weight` 仍然只在“新节点替代旧节点”时递增

## team/space 的处理原则

当前 post-action 自动画像只覆盖：

- `USER`
- `PROJECT`

但手工画像接口应支持：

- `TEAM`
- `SPACE`

因此需要把 `vmm_profile_nodes.profile_type` 从现有的 user/project 两类扩展到四类：

- `USER`
- `PROJECT`
- `TEAM`
- `SPACE`

自动提炼流程仍只会产生 user/project 候选；
team/space 只通过手工画像指令写入，并按最高权限规则处理。

## 当前实现顺序

按以下顺序落地：

1. 新增文档并固化方案
2. 扩展 proto：
   - `GetProfileNodes`
   - `ApplyProfileInstruction`
3. 扩展 domain / ports / DuckDB schema
4. 新增手工画像评审 prompt
5. 实现 gRPC server、usecase、DuckDB 写入与 profile 重建
6. 同步 README、gRPC 文档和 post-action 文档
