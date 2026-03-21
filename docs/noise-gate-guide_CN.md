# VMM 记忆准入噪声门说明（中文）

## 文档目标

这份文档说明 VMM 在 `post-action` 写入长期记忆前新增的“噪声准入门（Noise Gate）”模块。它专门用于拦截那些虽然结构上合法、但语义上不值得进入长期记忆库的问答内容。

它和入口清洗不是同一层能力：

- 入口清洗负责去掉 `<think>`、base64、图片/附件链接等“脏文本”
- 噪声准入门负责判断“这一轮问答值不值得记下来”

## 它解决什么问题

如果把下面这些内容写进长期记忆，会明显污染召回质量：

- 代理拒答
  - 例如“我不记得”“没有相关记忆”“I don't have any information”
- 关于记忆本身的元问题
  - 例如“你还记得吗”“我之前说过什么来着”“只回复 none”
- 新会话或心跳样板
  - 例如“fresh session”“new session”“HEARTBEAT”
- 诊断、评测或测试残留
  - 例如 `query -> none`、`no explicit solution`

这些内容有些可以靠正则直接拦截，有些问法变化很多，更适合用向量相似度做语义复核。

## 模块位置

- [internal/logic/processor/noise_gate.go](../internal/logic/processor/noise_gate.go)
- [internal/logic/processor/noise_rules.go](../internal/logic/processor/noise_rules.go)

规则文件目录：

- 系统目录：`configs/noise_rules/`
- 用户覆盖目录：`~/.vmm/noise_rules/` 或 `-config` 指向目录下的 `noise_rules/`

## 工作流程

`post-action` 当前的写库链路是：

1. HTTP 入口做结构校验与文本净化
2. `MessageNormalizer` 把原始快照压成 `user -> assistant` 轮次
3. `NoiseGate` 对每条轮次做“是否准入长期记忆”的判断
4. 只有通过判定的轮次才会写入关系存储

也就是说：

- 结构不合法：在入口被拒绝
- 结构合法但语义无价值：在 `NoiseGate` 被拦截

## 规则加载优先级

噪声规则采用两层结构：

1. `common.json`
2. `<language>.json`，例如 `zh-CN.json`

实际加载顺序是：

1. 先确定 `common.json` 来源
   - 用户目录存在 `common.json` 时，只用用户的
   - 否则只用系统的
2. 再确定语言文件来源
   - 用户目录存在 `zh-CN.json` 时，只用用户的
   - 否则只用系统的
3. 最终把“选中的 common”和“选中的 language”组合
4. 如果语言文件和 `common.json` 中有同名类别，语言文件覆盖 `common.json`

注意：

- 系统和用户的同层文件不会 merge
- 用户可以通过提供自己的 `common.json` 或 `zh-CN.json`，完整替换系统同层规则

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
        "记不记得",
        "我(?:之前|上次|以前)(?:说|提|讲).*(?:吗|呢|？|\\?)"
      ],
      "phrases": [
        "你还记得我之前说过的内容吗",
        "记不记得我上次提到的代号",
        "我之前说过什么来着"
      ]
    }
  ]
}
```

### 字段说明

- `language`
  - 规则包语言标识
  - `common.json` 固定写 `common`
  - 中文包通常写 `zh-CN`
- `version`
  - 规则版本号
- `categories`
  - 类别列表

每个类别包含：

- `name`
  - 类别唯一名称
  - 例如 `meta_question`、`denial_response`
- `targets`
  - 作用对象
  - 可选值：
    - `user`
    - `assistant`
- `threshold`
  - 语义相似度阈值
  - 当 `phrases` 存在时生效
- `patterns`
  - 正则规则列表
  - 适合高置信、低成本拦截
- `phrases`
  - 语义原型短语列表
  - 启动时会做 embedding，用于运行时相似度比对

## 当前内置类别

当前系统规则中主要内置了这些类别：

- `meta_question`
  - 主要针对 `user`
  - 拦截“你还记得吗”“我之前说过什么”这类 recall / 元问题
- `denial_response`
  - 主要针对 `assistant`
  - 拦截“我不记得”“没有相关记忆”“I don't have access to ...”这类拒答
- `boilerplate`
  - 可同时针对 `user` 和 `assistant`
  - 拦截 `fresh session`、`new session`、`HEARTBEAT`
- `diagnostic_artifact`
  - 可同时针对 `user` 和 `assistant`
  - 拦截 `query -> none`、`no explicit solution`

## 判定策略

每条 `NormalizedTurn` 会按下面的顺序判定：

1. 先对 `user` 文本做正则判定
2. 再对 `assistant` 文本做正则判定
3. 如果正则都没命中，且语义门已启用：
   - 对 `user` 文本做 embedding，相似度比对 `user` 目标类别
   - 对 `assistant` 文本做 embedding，相似度比对 `assistant` 目标类别
4. 任一侧命中，即整条轮次不写库

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

### 配置字段

- `noise.enabled`
  - 是否启用噪声准入门
- `noise.default_language`
  - 用于选择 `<language>.json`
  - 当前 `post-action` 没有单独传入语言，因此这里作为默认判定语言
- `noise.semantic_enabled`
  - 是否启用语义相似度判定
- `noise.semantic_threshold`
  - 默认语义阈值
  - 如果类别自身配置了 `threshold`，优先使用类别值

## 语义向量加载方式

当前策略是：

- 在应用启动时，读取噪声规则里的 `phrases`
- 通过当前 embedding provider 生成向量
- 运行时只对当前轮次文本做 embedding，再和类别原型向量比较

如果启动时 embedding 失败：

- 服务不会直接起不来
- 噪声门会降级为 regex-only
- 会记录一条降级日志

## 为什么要把它放在 `post-action` 里

因为这层判断的目标不是“请求是否合法”，而是“这条内容值不值得长期保存”。

这意味着它天然属于：

- 业务准入门

而不是：

- 传输层校验
- 文本清洗器

## 实际效果示例

### 示例 1：元问题被拦截

输入：

- user: `你还记得我上次说过的部署方案吗`
- assistant: `我不记得`

结果：

- 这一轮不会进入长期记忆

### 示例 2：正常业务对话保留

输入：

- user: `数据库连接池建议开多大`
- assistant: `建议根据实例规格与并发量做压测后设定`

结果：

- 这一轮允许写入

## 编写规则时的建议

1. 高置信规则优先用 `patterns`
   - 更快，也更可解释
2. 容易变形的问法再补 `phrases`
   - 比如 recall 元问题
3. 不要把阈值设太低
   - 否则容易误杀正常业务提问
4. `common.json` 适合放跨语言概念
   - 例如英文拒答模板、诊断残留
5. 语言文件适合放本地语言表达
   - 例如中文“你还记得吗”

## 当前限制

- 目前 `post-action` 没有单独传语言，所以默认按 `noise.default_language` 判定
- 语义门只在写库前使用，不影响实时回答
- 语义判定失败时会降级为 regex-only，而不是阻断整个请求
