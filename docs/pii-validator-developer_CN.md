# VMM PII 脱敏验证器开发者文档

## 文档目标

这份文档面向开发者、贡献者和 AI 助手，用来解释 VMM PII 验证器当前的内部工作方式。

它重点覆盖：

- 运行时架构
- 规则编译机制
- 条件表达式 DSL
- 规则覆盖模型
- 调试工具
- 实现层面的常见坑

如果你只想了解文件放哪里、JSON 怎么写、如何配置，请阅读：

- [pii-validator-config_CN.md](./pii-validator-config_CN.md)

## 核心架构

验证器在运行时由三部分组成：

1. 规则加载与编译层

- [engine.go](../internal/platform/pii/engine.go)
- [evaluator.go](../internal/platform/pii/evaluator.go)
- 职责：
  - 加载 JSON 规则包
  - 编译正则
  - 将 `condition` 编译成 opcode
  - 在启动期拒绝非法规则

2. 原子校验函数层

- [validators.go](../internal/platform/pii/validators.go)
- 职责：
  - 校验和验证
  - 加权数字验证
  - 严格格式验证

3. 运行时脱敏执行层

- [engine.go](../internal/platform/pii/engine.go)
- 职责：
  - 使用 `FindAllStringSubmatchIndex` 扫描
  - 执行预编译条件
  - 仅替换通过复核的命中项

## 运行时入口

应用层实际使用的入口是：

```go
Scrub(text string, lang string) string
```

当前主线仓库里，这套能力主要以“可复用平台组件 + 独立测试器”的方式保留：

- 平台实现：
  - [engine.go](../internal/platform/pii/engine.go)
- 独立调试入口：
  - [main.go](../cmd/vmm-pii-tester/main.go)

本地独立调试工具入口：

- [main.go](../cmd/vmm-pii-tester/main.go)

需要特别注意：

- 当前主线 gRPC 运行时并不会自动把这套脱敏器挂接到现有接口链路
- 如果未来要重新接线，必须同步更新 README、相关链路文档和测试说明，避免把“离线验证能力”误写成“已上线主流程能力”

## 规则选择模型

当前的规则选择模型刻意保持简单明确。

规则包分为两类：

- `common.json`
- 语言文件，例如 `zh-CN.json`

系统规则和用户规则之间不做合并。

选择顺序如下：

1. 先选择一个 common 规则包
   - 如果存在用户 `common.json`，就用用户的
   - 否则用系统 `common.json`
2. 再选择一个语言规则包
   - 如果存在用户 `<lang>.json`，就用用户的
   - 否则用系统 `<lang>.json`
3. 构建最终运行时规则
   - 先应用选中的 common
   - 再应用选中的语言规则

冲突规则：

- 只有同名 `name` 才会覆盖
- 语言规则会覆盖 common 中的同名规则
- 不冲突的规则会继续保留

这意味着：

- 用户可以完整替换系统 common 规则包
- 用户可以完整替换系统语言规则包
- 只有“最终选中的 common”和“最终选中的语言规则”会在运行时组合

## 条件表达式 DSL

`condition` 会在应用启动时预编译成紧凑的 opcode 程序。

这里没有使用任何外部重型表达式引擎。

### 支持的变量

- `$0`
  - 正则完整命中
- `$1`, `$2`, ...
  - 正则捕获组

### 支持的操作符

- `%`
- `==`
- `!=`
- `>`
- `>=`
- `<`
- `<=`
- `&&`
- `||`

### 支持的字面量

- 整数，例如 `9`、`11`、`18`
- 整数数组，例如 `[7,9,10,5]`

### 支持的内建函数

- `weight_sum(s, weights[])`
- `cn_check(char)`
- `len(s)`
- `is_luhn(s)`
- `is_base64(s)`

### 示例

中国大陆身份证复核：

```text
(weight_sum($1, [7,9,10,5,8,4,2,1,6,3,7,9,10,5,8,4,2]) % 11) == cn_check($2)
```

Ad-hoc 测试示例：

```text
len($0) == 9
```

## 启动期安全校验

每条规则都会在应用启动时完成编译和校验。

如果规则非法，应用会直接拒绝启动。

会被拒绝的情况包括：

- 未知函数
- `$n` 捕获组越界
- 非法数组字面量
- 括号不匹配
- 非法操作符
- 函数参数个数错误
- 同一 JSON 文件内重复规则名
- opcode 数量超过 64

这套设计就是为了把复杂性压在启动阶段，保证运行时足够快、足够稳。

## 运行时安全模型

求值器采用 fail-safe 策略。

如果求值阶段发生异常，系统会强制脱敏，而不是放过敏感内容。

原因码包括：

- `EVAL_OK`
- `COND_FALSE`
- `PANIC_MASKED`
- `STEP_LIMIT_MASKED`
- `BAD_GROUP_REF_MASKED`

## 日志模型

生产环境下的 PII 求值日志绝不能包含明文敏感内容。

日志字段：

- `rule`
- `result`
- `match_len`
- `match_hash`
- `reason`

其中 `match_hash` 是对命中文本做 SHA256 后截断前 12 位十六进制字符。

这样既能做日志关联，又不会泄露原文。

## Debug 模式

`Engine.DebugMode` 只用于本地调试和规则开发。

默认值：

- `DebugMode == false`

这意味着生产链路默认不会输出原始匹配内容。

当 `DebugMode == true` 时，引擎会为每条命中输出完整轨迹：

- 规则名
- 完整命中
- `$0`
- `$1/$2/...`
- VM 结果
- 最终动作

这个能力被下面的独立测试器使用：

- [vmm-pii-tester](../cmd/vmm-pii-tester/main.go)

### 文件模式

加载固定系统/用户规则目录，然后按真实规则包执行。

示例：

```powershell
go run ./cmd/vmm-pii-tester -lang zh-CN -text '我的身份证是 11010519491231002X，手机号是 13812345678'
```

### Ad-hoc 模式

直接在内存中编译一条规则，不修改任何 JSON 文件。

示例：

```powershell
go run ./cmd/vmm-pii-tester -text '我的代号是 8888-9999' -pattern '\b\d{4}-\d{4}\b' -condition 'len($0) == 9' -replace '[SECRET]'
```

这个模式非常适合：

- 快速测试新正则
- 验证一条 DSL 条件
- 查看捕获组
- 重现误杀而不改规则文件

### PowerShell 注意事项

只要表达式中包含 `$0`、`$1` 这类变量，就要优先使用单引号。

正确：

```powershell
-condition 'len($0) == 9'
```

错误：

```powershell
-condition "len($0) == 9"
```

因为 PowerShell 会在双引号中先展开 `$0`，导致测试器拿到的表达式已经被改写。

## 性能模型

引擎没有使用 `ReplaceAllStringFunc`。

而是使用：

1. `FindAllStringSubmatchIndex`
2. 一个 `strings.Builder`
3. 手动重建输出字符串

这样有几个好处：

- 不重复执行正则
- 可以直接拿到捕获组
- 运行时行为更可预测

求值器使用跳转指令来实现 `&&` 和 `||` 的真短路。

当前实现刻意不池化：

- 正则结果切片
- `strings.Builder`

## 常见陷阱

### 1. 对只接受纯数字的函数误用 `$0`

错误示例：

```text
weight_sum($0, [...])
```

如果 `$0` 里包含 `X` 这样的校验字符，就一定会出错。

应改成：

```text
weight_sum($1, [...]) == cn_check($2)
```

### 2. 手机号规则误命中更长数字串

VMM 实际踩到过一个问题：

- 没有边界的手机号正则，会误命中 18 位身份证中间的 11 位数字

错误示例：

```json
{ "pattern": "1[3-9]\\d{9}" }
```

更安全的写法：

```json
{ "pattern": "\\b1[3-9]\\d{9}\\b" }
```

一定要验证：

- 正样本
- 接近但不应命中的样本
- 更长字符串中的包裹场景
- 混合 Unicode 与标点的文本

### 3. 误以为 `excludes` 已经具备字段级过滤能力

当前并没有。

`excludes` 现在只是元数据。

### 4. 误以为用户规则会和系统规则合并

不会。

如果用户存在 `common.json`，系统 `common.json` 就不会参与。

如果用户存在 `zh-CN.json`，系统 `zh-CN.json` 就不会参与。

只有“最终选中的 common”和“最终选中的语言规则”会一起运行。

## 测试覆盖

当前测试已经覆盖：

- 原子校验函数
- 非法 DSL 的启动期拒绝
- 真短路 VM 行为
- 合法与非法身份证案例
- 安全日志
- 并发 scrub 与 reload 的死锁安全
- common 规则加载
- 语言规则覆盖 common 同名规则
- 用户 common 替换系统 common
- 用户语言包替换系统语言包
- 重复规则名拒绝
- Debug Trace 输出
- Ad-hoc 内存规则注入
- Ad-hoc 编译失败返回

相关测试文件：

- [validators_test.go](../internal/platform/pii/validators_test.go)
- [evaluator_test.go](../internal/platform/pii/evaluator_test.go)
- [engine_test.go](../internal/platform/pii/engine_test.go)

## 贡献者检查清单

提交变更前请确认：

- 正则只命中目标数据形态
- `condition` 使用了合法捕获组
- 替换文本明确且安全
- 没有把明文敏感信息加进生产日志
- 已覆盖反例样本
- 规则名稳定且唯一
- 公共规则放在 `common.json`
- 地区相关规则放到正确语言文件中
- 已理解覆盖语义

如果规则由 AI 生成，请务必人工检查：

- 边界使用
- 捕获组编号
- 纯数字假设
- 替换标记命名
- 误杀样本
- 它到底应该放在 `common.json` 还是语言文件

## 当前限制

- 不支持用户自定义函数
- DSL 不支持字符串拼接
- `excludes` 还没有真正生效
- 节点级覆盖仍依赖稳定规则名

这些限制是刻意保留的，用来保证运行时足够小、可预测且安全。
