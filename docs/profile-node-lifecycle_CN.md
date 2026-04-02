# 画像节点生命周期与渲染方案（中文）

## 文档目标

这份文档用于说明 VMM 当前主线已经落地的画像节点生命周期与渲染方案，避免后续开发时再次退回到“直接维护一段大画像 Blob”的旧思路。

当前实现里，画像系统已经拆成：

- 画像节点事实层
- LLM 节点评审层
- 后端渲染层
- 手工画像指令层

## 核心原则

1. `vmm_profile_nodes` 才是画像事实源。
2. `vmm_users.profile` 与 `vmm_projects.profile` 只是派生结果，不再作为合并输入。
3. LLM 不直接生成最终画像 Blob。
4. LLM 只负责判断：
   - 新候选是否有效
   - 新候选的优先级 `P`
   - 新候选的生命周期等级 `L`
   - 新候选为什么属于该等级
   - 哪些旧节点应被替代
5. 后端负责：
   - 持久化新节点
   - 标记旧节点 `superseded`
   - 计算 `refresh_weight`
   - 计算 `expires_timestamp`
   - 重新渲染 `profile`

## 领域分离原则

画像节点必须保持“一个节点只表达一个清晰领域”的原则。

允许作为独立领域存在的典型示例包括：

- 饮食偏好
- 抽烟/喝酒等生活习惯
- 沟通与回复偏好
- 编程语言或开发工具偏好
- 项目技术栈与工程约定

这意味着：

- 不能把饮食偏好、生活习惯和开发语言偏好揉成一条综合画像
- 如果一次提取或一次手工指令同时涉及多个领域，必须拆成多条画像节点
- 如果旧节点本身是跨领域“大泥球”，后续更新时应优先把它拆成多个更原子化的替代节点

## 画像节点字段设计

`vmm_profile_nodes` 继续维持一张表，通过 `profile_type + bind_id` 区分不同 scope，不拆成多张表。

当前主线字段：

- `id`
- `turn_id`
- `profile_type`
- `bind_id`
- `content`
- `profile_status`
- `priority`
- `profile_level`
- `level_reason`
- `refresh_weight`
- `source_kind`
- `source_id`
- `status_reason`
- `expires_timestamp`
- `superseded_by_id`
- `profile_date`
- `created_timestamp`
- `updated_timestamp`

说明：

- `priority`
  - `P0`：硬约束 / 底线
  - `P1`：重要偏好 / 重要规则
  - `P2`：普通参考
- `profile_level`
  - `L0 transient`：一次性上下文
  - `L1 situational`：阶段性偏好或上下文
  - `L2 stable`：稳定偏好 / 长期习惯
  - `L3 persistent`：长期原则 / 强约束 / 身份特征
- `level_reason`
  - 记录 LLM 为什么把节点判成这个等级
- `refresh_weight`
  - 表示同类记忆被刷新、续期、再次确认的次数
- `source_kind`
  - 表示节点来自 `turn_extract / manual_instruction / system_seed / retained_after_user_delete`
- `source_id`
  - 当来源是 `manual_instruction` 时，指向 `vmm_profile_instructions.id`
  - 当来源是 `retained_after_user_delete` 时，指向被删除的原用户 ID
- `turn_id`
  - 对话提炼节点保留真实 turn 绑定
  - 手工画像节点在 DuckDB 中保持 `NULL`，因为它们并不来自语义对话片段
  - 如果共享范围画像来自某个用户历史 turn，但该用户后来被删除，则节点会被保留并把 `turn_id` 置为 `NULL`
- `status_reason`
  - 记录节点为什么进入 `invalid / superseded / expired`
- `expires_timestamp`
  - 后端根据 `profile_level + refresh_weight` 计算得到
- `superseded_by_id`
  - 记录一条旧画像被哪条新画像替代
- `profile_date`
  - 渲染时间轴时使用的展示日期，格式固定 `YYYY-MM-DD`

## 画像状态机

当前主线状态机如下：

- `0 invalid`
- `1 pending`
- `2 active`
- `3 superseded`
- `4 expired`

说明：

- `invalid`
  - 新候选被判定为无长期价值，但仍保留备案
- `pending`
  - 仅用于评审中间态或失败重试
- `active`
  - 当前有效画像节点
- `superseded`
  - 被新的节点取代
- `expired`
  - 超过生命周期自然失效

## LLM 输入原则

LLM 不再接收：

- `current_profile`

LLM 改为接收：

- 当前 target 下所有 `active` 且未过期的画像节点
- 当前批次新提取出的画像候选

输入示例：

```json
{
  "user": {
    "active_nodes": [
      {
        "id": 101,
        "date": "2026-03-29",
        "priority": "P1",
        "level": "L1",
        "refresh_weight": 0,
        "content": "预算 200 万左右"
      },
      {
        "id": 105,
        "date": "2026-03-29",
        "priority": "P1",
        "level": "L0",
        "refresh_weight": 0,
        "content": "偏好油车"
      }
    ],
    "new_candidates": [
      {
        "candidate_index": 0,
        "turn_id": 88,
        "date": "2026-03-30",
        "content": "考虑技术成熟的电车"
      }
    ]
  }
}
```

规则：

- 如果这轮只有 `user` 画像候选，只传 `user`
- 如果这轮只有 `project` 画像候选，只传 `project`
- 缺失侧不以空块、空数组、空 profile 的形式进入 LLM

## LLM 输出原则

LLM 不再返回最终画像全文，而是返回“节点处理指令”。

输出示例：

```json
{
  "user": {
    "accepted_candidates": [
      {
        "candidate_index": 0,
        "normalized_content": "考虑技术成熟的电车",
        "priority": "P1",
        "level": "L1",
        "level_reason": "这是当前阶段的决策偏好，具备阶段性参考价值，但不属于长期人格特征。",
        "supersede_node_ids": [105]
      }
    ],
    "invalid_candidate_indexes": [],
    "retire_only_node_ids": [],
    "reason": "新偏好替代旧偏好。"
  }
}
```

字段含义：

- `accepted_candidates`
  - 需要进入数据库的新有效画像节点
- `normalized_content`
  - 标准化后的画像文本
- `priority`
  - 重要度
- `level`
  - 生命周期等级
- `level_reason`
  - 等级判定理由
- `supersede_node_ids`
  - 需要被该新节点替代的旧画像 ID
- `invalid_candidate_indexes`
  - 无长期价值的新候选
- `retire_only_node_ids`
  - 需要直接废弃、但不产生新替代节点的旧画像

## 刷新与替代规则

如果新候选与旧画像语义冲突：

- 新建一条新节点
- 旧节点标记为 `superseded`

如果旧画像只在局部上与新候选冲突，而其中仍有未冲突的有效事实：

- 不能把整条旧节点的剩余有效事实一起丢掉
- 应通过新的替代节点继续保留这些未冲突事实
- 必要时拆成多个更原子化的新节点，再把旧节点标记为 `superseded`

如果新候选与旧画像语义相同，属于“再次确认”：

- 也必须新建一条新节点
- 旧节点标记为 `superseded`
- 新节点 `refresh_weight = max(旧节点.refresh_weight) + 1`

这意味着：

- “重复提到”不是噪声
- 而是一种记忆续期

## 生命周期与衰减策略

不让 LLM 直接计算具体过期时间戳。

由后端统一根据 `profile_level + refresh_weight` 计算：

- `L0 transient`
  - 基础有效期 `3d`
  - 每次刷新额外 `+2d`
  - 最大 `14d`
- `L1 situational`
  - 基础有效期 `14d`
  - 每次刷新额外 `+7d`
  - 最大 `90d`
- `L2 stable`
  - 基础有效期 `90d`
  - 每次刷新额外 `+30d`
  - 最大 `365d`
- `L3 persistent`
  - 默认不过期或极长有效期

## 手工画像指令层

当前除了 post-action 自动提炼外，还新增了一条显式画像修改链路：

- `GetProfileNodes`
- `ApplyProfileInstruction`

其中：

- `GetProfileNodes`
  - 只返回单个目标下当前 `active` 的原子化画像节点
  - 不返回渲染后的 profile Blob
- `ApplyProfileInstruction`
  - 不绑定 `turn_id`
  - 先写入 `vmm_profile_instructions`
  - 再把 active 节点和显式指令交给 `review_profile_instruction`
  - 由后端持久化节点结果并重建 profile

这意味着当前画像系统除了 `vmm_profile_nodes`，还多了一张来源表：

- `vmm_profile_instructions`

它用于保存：

- 原始指令文本
- 指令状态
- LLM 评审结果 JSON
- 失败原因

## team/space 的最高权限规则

当前自动提炼仍只覆盖：

- `USER`
- `PROJECT`

但手工画像接口已经支持：

- `TEAM`
- `SPACE`

并且需要遵守额外规则：

- `TEAM / SPACE` 的手工画像指令直接视为最高权限规则
- 后端会强制把它们钳制到最高权威语义
- 不允许降级成普通偏好或短期上下文
- 它们不会由 post-action 自动画像流程生成

这样：

- “当前买车偏油车”会自然衰减
- “偏好 Rust 开发”会更稳定
- “必须中文回复”可以长期存在

## 渲染原则

最终 `vmm_users.profile` 与 `vmm_projects.profile` 由后端从当前有效节点自动渲染，不再由 LLM 直接生成。

渲染规则：

1. 只取 `active` 且未过期节点
2. 正文按日期分组
3. 每条显示：
   - `[P?][L?][W?]`

渲染示例：

```text
2026-03-29:
[P1][L2][W0] 偏好使用 Rust 作为主要开发语言

2026-03-30:
[P0][L3][W1] 必须使用中文回复
[P1][L1][W0] 当前阶段优先考虑技术成熟方案
```

## Bundle 输出层的通用说明

scope `profile` 正文不再长期保存 `P / L / W` 的说明头。

原因：

- 这些正文既可能被直接拼接注入，也可能被用于再组合
- 如果把说明头长期存入 scope 字段，会带来重复 token 成本
- 说明文本应由输出层按需附加，而不是写死在存储层

当前主线中：

- `vmm_users.profile / vmm_projects.profile / vmm_teams.profile / vmm_spaces.profile`
  - 保存正文时间轴
- `GetProfileBundle`
  - 负责在需要时为组合结果附加 `P / L / W` 说明
  - 并输出固定的 `[TEAM] / [SPACE] / [PROJECT] / [USER]` 结构

## 当前执行顺序

当前 `PostAction` 主线里的画像链路已经是：

1. `analyze_turn` 为当前 turn 产出 `profile_nodes[]`
2. 后端按 user/project 两侧收集本轮新画像候选
3. 读取当前目标下仍然 `active` 且未过期的画像节点
4. 把“活跃旧节点 + 新候选”送入 `review_profile_nodes`
5. LLM 返回：
   - 哪些候选应接纳
   - 哪些候选应无效
   - 哪些旧节点应被替代
   - 新节点的 `priority / level / level_reason`
   - 并保持每条新节点只表达一个清晰领域
6. 后端根据评审结果：
   - 插入新节点
   - 标记旧节点 `superseded`
   - 计算 `refresh_weight`
   - 计算 `expires_timestamp`
7. 最后由后端重建 `vmm_users.profile / vmm_projects.profile`

另外，后台维护 worker 每 30 秒还会额外执行一次过期画像收敛：

1. 把到期的 `active` 节点改成 `expired`
2. 重新渲染受影响 user/project 的 profile 文本

## 最终结论

当前主线已经是：

- `vmm_profile_nodes` 是事实层
- `profile` 是渲染层
- LLM 给出节点判断
- 后端负责状态流转、生命周期、权重和最终画像编译

这条路线比“直接维护一段大画像 Blob”更稳定，也更适合后续扩展为真正的长期记忆系统。
