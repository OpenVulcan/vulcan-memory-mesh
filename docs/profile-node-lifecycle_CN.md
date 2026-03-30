# 画像节点生命周期与渲染方案（中文）

## 文档目标

这份文档用于固化 VMM 当前讨论中的画像系统改造方向，避免后续开发时再次退回到“直接维护一段大画像 Blob”的旧思路。

这里的目标不是让 LLM 直接写最终画像文本，而是把画像系统拆成：

- 画像节点事实层
- LLM 节点评审层
- 后端渲染层

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

## 画像节点字段设计

`vmm_profile_nodes` 继续维持一张表，通过 `profile_type + bind_id` 区分 user/project，不拆成两张表。

建议字段：

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
- `expires_timestamp`
  - 后端根据 `profile_level + refresh_weight` 计算得到
- `superseded_by_id`
  - 记录一条旧画像被哪条新画像替代
- `profile_date`
  - 渲染时间轴时使用的展示日期，格式固定 `YYYY-MM-DD`

## 画像状态机

建议状态机如下：

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

这样：

- “当前买车偏油车”会自然衰减
- “偏好 Rust 开发”会更稳定
- “必须中文回复”可以长期存在

## 渲染原则

最终 `vmm_users.profile` 与 `vmm_projects.profile` 由后端从当前有效节点自动渲染，不再由 LLM 直接生成。

渲染规则：

1. 只取 `active` 且未过期节点
2. 顶部固定输出 legend
3. 正文按日期分组
4. 每条显示：
   - `[P?][L?][W?]`

渲染示例：

```text
[Profile Legend]
- P = Priority
  - P0: Hard requirement / non-negotiable rule
  - P1: Important preference / important working rule
  - P2: General reference / lower-priority preference
- L = Lifetime Level
  - L0: Transient, short-lived context
  - L1: Situational, phase-specific preference or context
  - L2: Stable, long-lived preference or habit
  - L3: Persistent, durable rule / identity / hard constraint
- W = Refresh Weight
  - Higher W means this memory has been reaffirmed or refreshed more times.

[Profile Timeline]
2026-03-29:
[P1][L2][W0] 偏好使用 Rust 作为主要开发语言

2026-03-30:
[P0][L3][W1] 必须使用中文回复
[P1][L1][W0] 当前阶段优先考虑技术成熟方案
```

## LLM 视角下的通用说明

今后所有自动生成的 profile 文本，固定带上 `P / L / W` 的说明头。

原因：

- 后续这些 profile 文本还会作为上下文再喂给 LLM
- 必须让 LLM 清楚：
  - `P` 表示优先级
  - `L` 表示生命周期等级
  - `W` 表示被重复确认的次数与当前新鲜度信号

## 执行顺序建议

1. 先调整 `vmm_profile_nodes` schema 与状态机
2. 新增独立的画像节点评审 prompt
3. 把 `PostAction` 里的画像处理改为：
   - 提炼候选
   - 拉取 active 节点
   - LLM 评审
   - 持久化新节点 / 旧节点 supersede
4. 最后由后端重建 `vmm_users.profile / vmm_projects.profile`

## 最终结论

以后：

- `vmm_profile_nodes` 是事实层
- `profile` 是渲染层
- LLM 给出节点判断
- 后端负责状态流转、生命周期、权重和最终画像编译

这条路线比“直接维护一段大画像 Blob”更稳定，也更适合后续扩展为真正的长期记忆系统。
