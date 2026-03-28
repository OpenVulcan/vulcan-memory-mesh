# VMM 记忆准入噪声门说明（中文）

## 文档目标

这份文档说明当前主线版本中，`NoiseGate` 在 `PostAction` 链路里的职责、触发条件、规则来源和语义缓存方式。

当前它只服务于：

- `vmm.v1.VMMService/PostAction`

它不是传输层校验器，也不是文本清洗器。

## 它解决什么问题

即使请求结构合法，也不代表这条对话值得写进长期记忆。

下面这些内容如果直接持久化，会明显污染后续召回和提炼质量：

- 关于“记忆本身”的元问题
  - 例如“你还记得吗”
  - “我之前说过什么”
- 明显拒答
  - 例如“我不记得”
  - “没有相关信息”
- 心跳或会话样板
  - 例如 `fresh session`
  - `HEARTBEAT`
- 诊断残留
  - 例如 `query -> none`
  - `no explicit solution`

`NoiseGate` 的目标就是在真正写入长期存储前，把这类语义上低价值的单轮内容拦下来。

## 它不负责什么

`NoiseGate` 不负责：

- `session_id / user_id / project_id` 校验
- gRPC 请求大小限制
- base64 / 图片 / `<think>` 文本清洗
- timeline 结构合法性判断

这些能力分别由：

- gRPC 请求校验器
- gRPC 接收大小限制
- `PostAction` 存储清洗器
- 统一范围拦截器

负责。

## 当前在链路中的位置

当前 `PostAction` 的执行顺序里，`NoiseGate` 位于：

1. gRPC 请求校验
2. 作用域解析
3. 原始日志记录
4. 文本清洗
5. 清洗后日志记录
6. 后台 `PostActionUseCase`
7. **NoiseGate**
8. DuckDB 写入

也就是说：

- 它发生在文本已经清洗之后
- 发生在真正写入 `vmm_turn_records` 之前

## 当前触发规则

当前主线不是“所有 `PostAction` 都走噪声门”，而是：

- 当 `timeline == 0`：
  - 认为这是一个简单单轮 `user -> assistant`
  - 才执行 `NoiseGate`
- 当 `timeline > 0`：
  - 认为这是带中间流程的复杂会话片段
  - 直接跳过 `NoiseGate`

原因是：

- `NoiseGate` 当前只针对简单单轮问答做准入判断
- 中间包含补充提问、插话或回答中断的复杂片段，不适合直接套这层单轮过滤

## 当前判定对象

当前当 `timeline == 0` 时，`NoiseGate` 实际看到的是一个临时单轮：

- `user_message = user_content`
- `assistant_reply = assistant_content`

也就是说：

- 它不会看原始 `timeline`
- 它不会看原始 JSON
- 它只看一个标准化的简单问答对

如果这对问答被判定为噪声：

- 整次后台持久化直接结束
- 不写入 `vmm_turn_records`

## 规则目录

当前规则目录仍然是固定结构：

- 系统目录：`configs/noise_rules/`
- 用户覆盖目录：`~/.vmm/noise_rules/` 或 `-config` 指向目录下的 `noise_rules/`

规则分两类文件：

- `common.json`
- `<language>.json`，例如 `zh-CN.json`

## 规则选择优先级

当前仍然遵循“用户层优先、系统层兜底”的文件选择逻辑：

1. `common.json`
   - 用户目录存在时，只用用户的
   - 否则只用系统的
2. `<language>.json`
   - 用户目录存在时，只用用户的
   - 否则只用系统的
3. 最终把“选中的 common”和“选中的 language”组合
4. 如果语言文件里有和 common 同名的类别，语言文件覆盖 common

注意：

- 系统层和用户层不会做同层 merge
- 用户可以通过自己的 `common.json` 或 `<language>.json` 完整替换系统同层规则

## JSON 规则格式

示例：

```json
{
  "language": "zh-CN",
  "version": "1.0.0",
  "categories": [
    {
      "name": "meta_question",
      "targets": ["user"],
      "threshold": 0.88,
      "patterns": [
        "你还?记得",
        "记不记得"
      ],
      "phrases": [
        "你还记得我之前说过的内容吗",
        "我之前说过什么来着"
      ]
    }
  ]
}
```

### 类别字段说明

- `name`
  - 类别唯一名称
- `targets`
  - 生效对象：
    - `user`
    - `assistant`
- `patterns`
  - 高置信正则列表
- `phrases`
  - 语义原型短语
- `threshold`
  - 该类别自己的语义阈值

## 判定顺序

每条简单单轮问答会按这个顺序判定：

1. 对 `user_message` 做正则匹配
2. 对 `assistant_reply` 做正则匹配
3. 如果正则都没命中，并且启用了语义判定：
   - 对 `user_message` 做 embedding，相似度比对面向 `user` 的类别
   - 对 `assistant_reply` 做 embedding，相似度比对面向 `assistant` 的类别
4. 任一侧命中，整条轮次不写库

## 语义原型缓存

当前语义原型不会每次启动都重新计算。

当前策略是：

1. 应用启动时读取当前生效规则
2. 对规则内容计算 `rules_hash`
3. 优先从 DuckDB 的 `vmm_noise_embeddings` 读取缓存
4. 只有当以下条件全部一致时才复用：
   - `scope`
   - `language`
   - `model`
   - `dimension`
   - `rules_hash`
5. 如果不一致：
   - 重新做 embedding
   - 覆盖写回 DuckDB

这意味着以下变化会触发重算：

- embedding 模型变化
- embedding 维度变化
- 当前 `common.json + <language>.json` 内容变化

## 配置项

当前相关配置位于：

- [configs/local.json](../configs/local.json)

示例：

```json
{
  "noise": {
    "enabled": true,
    "default_language": "zh-CN",
    "semantic_enabled": true,
    "semantic_threshold": 0.88
  }
}
```

字段说明：

- `noise.enabled`
  - 是否启用噪声门
- `noise.default_language`
  - 当前默认规则语言
- `noise.semantic_enabled`
  - 是否启用语义相似度判断
- `noise.semantic_threshold`
  - 默认语义阈值

## 当前效果示例

### 示例 1：单轮元问题被拦截

输入：

- `user_content = "你还记得我上次说过什么吗"`
- `assistant_content = "我不记得"`
- `timeline = []`

结果：

- 这次后台持久化被 `NoiseGate` 拦下
- 不写入长期消息存储

### 示例 2：复杂 timeline 跳过噪声门

输入：

- `user_content = "最开始的问题"`
- `assistant_content = "最终回答"`
- `timeline` 包含多条中间问答

结果：

- 跳过 `NoiseGate`
- 直接把 `user / timeline / assistant` 组装成一条脱水 turn 写入 DuckDB

## 编写规则建议

1. 高置信规则优先放 `patterns`
2. 易变表达再补 `phrases`
3. 阈值不要设置过低
4. `common.json` 放跨语言概念
5. `<language>.json` 放本地语言表达

## 当前限制

- 当前 `NoiseGate` 只处理简单单轮问答
- 当前 `timeline > 0` 的复杂流程直接跳过噪声门
- 当前 `PostAction` 没有单独传语言，因此默认使用 `noise.default_language`
- 当前语义判定失败时会降级为 regex-only，而不是阻断整个请求
