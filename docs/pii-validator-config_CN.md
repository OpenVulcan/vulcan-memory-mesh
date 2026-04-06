# VMM PII 脱敏验证器配置文档

## 文档目标

这份文档面向只关心“如何配置规则、如何放文件、如何使用测试器”的使用者。

如果你想了解内部架构、VM、运行机制，请阅读：

- [pii-validator-developer_CN.md](./pii-validator-developer_CN.md)

## 固定规则目录

PII 规则目录是固定的，不支持路径配置。

### 系统规则位置

- `SystemDir/pii_rules`

打包产物通常对应：

- `output/configs/pii_rules`

`go run` 调试时通常对应：

- `configs/pii_rules`

### 用户规则位置

- `UserDir/pii_rules`

`UserDir` 的来源：

- 默认 `~/.vmm`
- `-config` 指向的目录
- `-config` 文件所在目录

## 当前支持的文件类型

PII 目前支持两类规则包：

- `common.json`
  - 用于与语言无关的规则
- `<lang>.json`
  - 用于语言或地区相关的规则，例如 `zh-CN.json`

示例：

```text
configs/
  pii_rules/
    common.json
    zh-CN.json
```

```text
~/.vmm/
  pii_rules/
    common.json
    zh-CN.json
```

## 哪些规则放在哪里

适合放在 `common.json` 的规则：

- 通用 API Key
- Bearer Token
- AWS Access Key ID
- 阿里云 Access Key ID
- 腾讯云 Secret ID
- BTC WIF 类私钥

适合放在语言文件的规则：

- 身份证
- 手机号
- 税号
- 地区化账号格式

## 规则选择与覆盖规则

系统规则和用户规则之间不做 merge。

运行时选择方式：

1. 先选一个 common 规则包
   - 如果用户 `common.json` 存在，就用用户的
   - 否则用系统的
2. 再选一个语言规则包
   - 如果用户 `<lang>.json` 存在，就用用户的
   - 否则用系统的
3. 构建最终运行规则
   - 先应用 common
   - 再应用语言规则

冲突处理：

- 只有同名 `name` 才会覆盖
- 语言规则会覆盖 common 中的同名规则
- 不同名规则会继续保留

需要特别注意：

- 如果用户存在 `~/.vmm/pii_rules/common.json`，系统 common 就不会再使用
- 如果用户存在 `~/.vmm/pii_rules/zh-CN.json`，系统 `zh-CN` 就不会再使用

这样设计的目的，是让用户能完全掌控自己的规则包，而不是被迫继承系统规则。

## 规则文件格式

每个规则文件都是 JSON。

### 公共规则示例

```json
{
  "language": "common",
  "version": "1.0.0",
  "rules": [
    {
      "name": "OpenAI_Key",
      "pattern": "sk-[A-Za-z0-9]{20,}",
      "replacement": "[SK_MASKED]"
    },
    {
      "name": "Bearer_Token",
      "pattern": "Bearer\\s+([A-Za-z0-9._+=-]{10,})",
      "replacement": "Bearer [TOKEN_MASKED]"
    }
  ]
}
```

### 语言规则示例

```json
{
  "language": "zh-CN",
  "version": "1.1.0",
  "rules": [
    {
      "name": "CN_ID_PRO",
      "pattern": "\\b(\\d{17})([0-9Xx])\\b",
      "replacement": "[ID_VERIFIED]",
      "condition": "(weight_sum($1, [7,9,10,5,8,4,2,1,6,3,7,9,10,5,8,4,2]) % 11) == cn_check($2)"
    },
    {
      "name": "Mobile",
      "pattern": "\\b1[3-9]\\d{9}\\b",
      "replacement": "[MOBILE_MASKED]"
    }
  ],
  "excludes": ["session_id", "request_id", "max_tokens"]
}
```

### 字段说明

- `language`
  - 规则包的运行时键
- `version`
  - 可选版本号
- `rules`
  - 有顺序的规则列表
- `name`
  - 稳定规则标识
- `pattern`
  - Go 正则表达式
- `replacement`
  - 脱敏替换文本
- `condition`
  - 可选二次复核表达式
- `excludes`
  - 预留元数据

## 配置字段

相关运行时配置位于：

- [base.yaml](../configs/base.yaml)
- [config.yaml](../configs/config.yaml)

最小相关配置：

```yaml
pii:
  default_language: zh-CN
```

- `pii.default_language`
  - 当调用方未显式指定语言，或目标语言规则不存在时的回退语言

配置覆盖顺序：

1. 系统层 `configs/base.yaml`
2. 系统层 `configs/config.yaml`
3. 用户层 `~/.vmm/config.yaml` 或 `-config` 指向的目录 / 文件
4. `.env` 与进程环境变量

## 规则系统当前能做什么

从配置角度看，当前规则系统已经支持以下能力：

- 加载一个共享的 `common.json` 规则包
- 加载一个语言专属规则包，例如 `zh-CN.json`
- 允许用户规则包完整替换系统规则包
- 把“选中的 common 包”和“选中的语言包”组合成最终运行规则
- 先用正则做第一轮匹配
- 再用 `condition` 做第二轮复核
- 通过规则名让语言规则覆盖 common 中的同名规则
- 对通过复核的命中进行替换，对误杀保持原文
- 在生产日志中只输出安全摘要，不泄露明文
- 提供独立测试器输出完整 Trace
- 支持 Ad-hoc 单条规则即时测试，不必修改 JSON

这意味着配置文件并不只是静态数据，它们本身就定义了运行时脱敏器的行为。

## 条件表达式能力

`condition` 目前支持这些函数：

- `weight_sum(s, weights[])`
- `cn_check(char)`
- `len(s)`
- `is_luhn(s)`
- `is_base64(s)`

常用变量：

- `$0` 完整命中
- `$1`, `$2`, ... 捕获组

支持的操作符：

- `%`
- `==`
- `!=`
- `>`
- `>=`
- `<`
- `<=`
- `&&`
- `||`

支持的字面量：

- 整数，例如 `9`、`11`、`18`
- 整数数组，例如 `[7,9,10,5]`

### 典型示例

中国大陆身份证复核：

```text
(weight_sum($1, [7,9,10,5,8,4,2,1,6,3,7,9,10,5,8,4,2]) % 11) == cn_check($2)
```

Ad-hoc 长度判断：

```text
len($0) == 9
```

Base64 或 Luhn 复核：

```text
is_base64($0) || is_luhn($0)
```

## 启动期校验规则

每条规则都会在应用启动前完成校验。

如果规则包里包含以下内容，应用会直接拒绝启动：

- 未知函数名
- `$9` 这类越界捕获组引用
- 非法数组
- 括号不匹配
- 非法操作符
- 函数参数个数错误
- 同一 JSON 文件内重复规则名
- 编译后 opcode 超过 64 条

这对规则编写者很重要，因为配置错误会立刻 fail fast，而不会变成运行时的隐蔽问题。

## 编写规则的建议

### 与语言无关的规则优先写到 common.json

典型例子：

- `sk-...`
- `Bearer ...`
- 云厂商访问凭证
- 钱包私钥格式

### 与地区和语言相关的规则写到语言文件

典型例子：

- 中国大陆身份证
- 手机号
- 地区税号

### 高风险数字规则建议加 condition

推荐对象：

- 身份证
- 银行卡
- 长结构化编号

### 使用稳定规则名

当前覆盖是按规则名做的。

如果你换了名字，系统会把它当成新增规则，而不是覆盖旧规则。

### 高风险数字规则建议加 condition

以下模式很容易仅凭正则误杀：

- 身份证
- 银行卡
- 长结构化编号

这类规则建议尽量补上二次复核 `condition`。

### 理解最终分层模型

这里其实有两个独立决策：

1. 选择哪个 common 规则包
2. 选择哪个语言规则包

之后，语言规则包再按同名规则覆盖 common 包中的规则。

这就是当前唯一存在的 merge 行为。

### 谨慎处理边界

VMM 实际踩过一个坑：

- 没有边界的手机号正则，误命中 18 位身份证中的 11 位数字

错误示例：

```json
{ "pattern": "1[3-9]\\d{9}" }
```

更安全的写法：

```json
{ "pattern": "\\b1[3-9]\\d{9}\\b" }
```

这个手机号误杀问题不是理论担忧，而是在 VMM 实际规则验证过程中真实出现过的坑。

## 使用 PII 测试器

独立测试器入口：

- [main.go](../cmd/vmm-pii-tester/main.go)

### 只编译测试器

```powershell
.\make.ps1 tester
```

### 构建全部二进制

```powershell
.\make.ps1 build
```

### 文件模式

```powershell
go run ./cmd/vmm-pii-tester -lang zh-CN -text '我的身份证是 11010519491231002X，手机号是 13812345678'
```

### Ad-hoc 模式

```powershell
go run ./cmd/vmm-pii-tester -text '我的代号是 8888-9999' -pattern '\b\d{4}-\d{4}\b' -condition 'len($0) == 9' -replace '[SECRET]'
```

Ad-hoc 模式适合这些场景：

- 快速测试一条新正则
- 直接查看 `$0/$1/$2` 捕获结果
- 不改 JSON 就验证一条 `condition`
- 单独重现一个误杀样本

### Ad-hoc 失败示例

非法正则：

```powershell
go run ./cmd/vmm-pii-tester -text 'test' -pattern '[a-' -condition 'len($0) == 4'
```

未知 DSL 函数：

```powershell
go run ./cmd/vmm-pii-tester -text 'test' -pattern '\d+' -condition 'unknown_func($0)'
```

### PowerShell 注意事项

只要表达式里有 `$0`、`$1` 之类的变量，就要优先使用单引号。

## 验证器如何使用

当前这套验证器有三种主要使用方式。

### 1. 基于文件规则包的测试模式

当你想验证当前已经安装好的 `common.json` 和语言规则包时，用这种模式。

示例：

```powershell
go run ./cmd/vmm-pii-tester -lang zh-CN -text '我的身份证是 11010519491231002X，手机号是 13812345678'
```

这种模式会输出：

- 原始文本
- 命中的规则名
- `$0/$1/$2/...`
- VM 结果
- 最终脱敏结果

### 2. Ad-hoc 即时单条规则测试

当你想立刻测试一条规则，而不想改 JSON 文件时，用这种模式。

示例：

```powershell
go run ./cmd/vmm-pii-tester -text '我的代号是 8888-9999' -pattern '\b\d{4}-\d{4}\b' -condition 'len($0) == 9' -replace '[SECRET]'
```

这种模式特别适合：

- 草拟新正则
- 调试捕获组
- 验证一条 `condition`
- 快速重现误杀

### 3. 在 Go 代码中直接调用

如果你要在 Go 代码里集成验证器，主要入口是：

```go
engine, err := pii.NewEngine(systemDir, userDir, "zh-CN")
if err != nil {
    // handle error
}
scrubbed := engine.Scrub(text, lang)
```

其中：

- `text` 是原始字符串
- `lang` 是语言标签，例如 `zh-CN`

如果你要做一次性的内存规则测试，可以使用：

```go
engine, err := pii.NewEngineWithRules("adhoc", logger, rules)
```

这适合做内部工具、离线校验，或者 AI 生成规则后的快速实验。

需要特别说明：

- 当前主线 gRPC 运行时并不会自动把这套脱敏器挂接到现有接口链路
- `vmm-pii-tester` 和 `internal/platform/pii` 是当前最稳定的验证入口

## 本地覆盖示例流程

### 本地替换系统 common 规则

1. 创建 `~/.vmm/pii_rules/common.json`
2. 放入你自己的 common 规则包
3. 重启应用或测试器

### 本地替换系统中文规则

1. 创建 `~/.vmm/pii_rules/zh-CN.json`
2. 放入你自己的中文规则包
3. 重启应用或测试器

## 配置检查清单

在交付新规则文件前请确认：

- 正则只命中目标样本
- 误杀样本已经验证
- 同一文件中规则名唯一
- 替换文本明确
- `condition` 使用合法捕获组
- 公共规则放在 `common.json`
- 语言规则放在正确语言文件中
- 已理解用户规则会替换系统规则

同时建议确认：

- 任何高风险数字规则都带了二次复核 `condition`
- 规则顺序是有意识安排的
- 没有误以为“用户规则存在后系统规则还会一起生效”
- 已用测试器同时跑过正样本和反样本

## 当前限制

- `excludes` 还没有真正参与字段过滤
- 不支持用户自定义函数
- DSL 不支持字符串拼接
- 覆盖依赖稳定规则名

## 安全日志与生产行为

生产环境日志不会输出明文敏感信息。

运行时只记录安全摘要字段，例如：

- `rule`
- `result`
- `match_len`
- `match_hash`
- `reason`

如果你需要看明文命中和捕获组，请使用 `vmm-pii-tester`，不要依赖生产日志。
