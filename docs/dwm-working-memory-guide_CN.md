# DWM - 确定性工作记忆中枢

> 核心代号：`Cognitive Anchor / 认知锚点`
>
> Slogan：`在概率的混沌中，锚定确定性的认知边界。`

## 一、DWM 是什么

DWM（Deterministic Working Memory，确定性工作记忆中枢）是面向 AI Agent 的隔离工作记忆层。

它不是长期记忆，不参与 `SearchMemoryEvents`，也不是画像系统的变体。  
它的职责只有一个：在长周期、多工具、多文件的工程任务里，为当前任务保留一份确定性、可校验、可覆盖的工作态锚点。

当前仓库里的 DWM 实现形态就是独立的 scratchpad 支线：

- `ScratchpadUpsert`
- `ScratchpadDelete`
- `ScratchpadGet`
- `ScratchpadClean`

## 二、DWM 解决什么问题

### 1. 上下文压缩后的毁灭性遗忘

当 Agent 读取大量文件、日志和工具结果后，上下文会被压缩、截断或粗摘要。  
这会让模型丢失刚刚建立起来的任务主线、关键文件和执行顺序。

DWM 的作用就是在压缩发生前，把这些关键锚点以结构化 key/value 方式保存下来；压缩后只需一次 `ScratchpadGet` 就能恢复。

### 2. 多任务串行执行时的上下文污染

Agent 刚做完任务 A，立刻切到任务 B 时，模型非常容易把旧任务推断带入新任务。  
DWM 通过 `plan_name` 锁定当前任务，只允许同一个 session 工作区里存在一个 canonical plan。

### 3. 长链路任务中的格式漂移

模型很难在长链路里始终保持专有名词完全一致。  
DWM 采用“忽略大小写校验 + 格式漂移警告”的折中策略：

- 忽略大小写后不一致：直接拦截
- 忽略大小写后一致但原始拼写不同：放行，但强制返回 `[FORMAT DRIFT WARNING]`

### 4. 串行工具调用的 I/O 拥堵

若发现了多条关键信息，Agent 不应被迫逐条写入。  
DWM 支持批量 `items[]` 写入，一次工具调用即可原子写入一批确定性工作记忆。

## 三、当前实现边界

当前 scratchpad 链路与主长期记忆系统严格隔离：

- 不进入 `memory_nodes`
- 不进入 `SearchMemoryEvents`
- 不创建 `vmm_sessions`
- 不依赖主 session 生命周期
- 不进入 retention recycle trash
- 不提供恢复接口

它只依赖固定范围坐标：

- `project_id`
- `user_id`
- `session_id`

其中：

- 外部接口字段叫 `session_id`
- 数据库存储列叫 `session_key`
- 这个字符串键只用于 scratchpad 隔离，不绑定主 `vmm_sessions.id`

## 四、数据模型

当前使用两张表：

### 1. `vmm_scratchpad_plans`

职责：

- 锁定某个 `project_id + user_id + session_key` 下唯一 canonical `plan_name`
- 保存整份 scratchpad 的最后活动时间

关键字段：

- `id`
- `project_id`
- `user_id`
- `session_key`
- `plan_name`
- `plan_name_norm`
- `created_timestamp`
- `updated_timestamp`

唯一约束：

- `(project_id, user_id, session_key)`

### 2. `vmm_scratchpad_nodes`

职责：

- 保存某个 `plan_id` 下的确定性 `key/value` 锚点

关键字段：

- `id`
- `plan_id`
- `item_key`
- `item_value`
- `created_timestamp`
- `updated_timestamp`

唯一约束：

- `(plan_id, item_key)`

## 五、四个接口的最终语义

### 1. `ScratchpadUpsert`

用途：

- 写入或覆盖当前任务的 key/value 锚点

支持两种输入形式：

- 单项：`key + value`
- 批量：`items[]`

固定规则：

- `items[]` 非空时，不允许再传 `key/value`
- 混传直接返回校验错误
- 批量写入采用整批原子事务语义
- 任一 item 非法，则整批失败

响应字段：

- `status`
- `msg`
- `affected_count`
- `inserted_count`
- `updated_count`

### 2. `ScratchpadDelete`

用途：

- 删除一个或多个 key

支持两种输入形式：

- 单项：`key`
- 批量：`keys[]`

固定规则：

- `keys[]` 非空时，不允许再传 `key`
- 混传直接返回校验错误
- 批量删除采用整批原子事务语义
- `Delete` 在空范围时不会创建 plan 锁

空范围返回：

- `status = SUCCESS`
- `msg = "No scratchpad plan exists for the current session. Create records first."`

响应字段：

- `status`
- `msg`
- `affected_count`

### 3. `ScratchpadGet`

用途：

- 读取完整 scratchpad
- 或按单个 `key` 读取局部锚点

请求规则：

- `key` 为空：返回全部
- `key` 非空：返回单项或空数组

无数据时：

- 不报错
- `status = SUCCESS`
- `items = []`
- `msg = "No scratchpad records found for the current session."`

响应 metadata：

- `plan_name`
- `item_count`
- `updated_timestamp`

重要约束：

- `Get` 返回的 `plan_name` 永远是 canonical 值
- 若当前没有 plan，则 `plan_name` 为空

### 4. `ScratchpadClean`

用途：

- 显式结束当前任务并清空整份 scratchpad

语义：

- 删除当前范围下全部 `nodes`
- 再删除对应 `plan`
- 空范围仍返回成功

成功消息：

- `Scratchpad history has been cleared.`
- 或 `The current scratchpad is already empty.`

## 六、Plan Guard 与格式漂移处理

当前 plan guard 规则如下：

1. 当前 scope 没有 plan：
   - `Upsert` 首次写入会创建 plan 锁
   - `Delete` 不会创建 plan 锁
2. 若 `ToLower(stored_plan_name) != ToLower(input_plan_name)`：
   - 拦截
   - 返回英文自然语言指导，要求检查拼写或先调用 `Clean`
3. 若忽略大小写后一致，但原始字符不一致：
   - 放行
   - 在 `msg` 最前方注入 `[FORMAT DRIFT WARNING]`
   - warning 中始终以 canonical plan 为准

## 七、后台清理

scratchpad 不进入 recycle trash，也不保留恢复入口。  
当前采用直接硬删除。

规则如下：

- 当 `vmm_scratchpad_plans.updated_timestamp` 早于“当前时间减 15 天”
- 后台维护器会先删 `vmm_scratchpad_nodes`
- 再删 `vmm_scratchpad_plans`

这条清理逻辑复用系统现有的半小时维护时钟，不额外新建 scratchpad 专属 ticker。

## 八、与主系统的关系

scratchpad 当前参与的系统级联动作只有：

- `DeleteProject`
- `DeleteUser`
- 15 天过期硬删除

明确不参与：

- `MigrateProject`
- 长期记忆召回
- 画像节点生成
- recycle trash / soft backup
- 向量索引

## 九、建议宿主框架如何接入

推荐工作流：

1. 长任务开始时，确定当前 `plan_name`
2. 每 3-5 次工具调用，把最新关键事实批量写入 `ScratchpadUpsert`
3. 一旦方案变化，继续用同一 `plan_name` 覆盖更新
4. 如果上下文压缩发生，立刻调用 `ScratchpadGet`
5. 将返回的 `items[]` 重新注入宿主系统提示
6. 任务结束后调用 `ScratchpadClean`

同时建议宿主层：

- 先把 `ScratchpadStatus` 枚举转译成模型友好的文本
- 不要把整段源码塞进 `value`
- `value` 应只保存对执行真正有意义的摘要、约束、关键文件定位或步骤描述

## 十、非目标

DWM 当前不是：

- 长期记忆系统
- 自动任务规划器
- 回收站恢复系统
- 项目迁移的一部分
- 任意文本大对象存储
