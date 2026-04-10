# Bug 修复计划 - 第四轮审核修复

## 任务目标
修复第四轮深度审核中确认的 6 个已确认 bug。

## 修复清单

### 高优先级: 密集文本跳过 token 预算截断 (token_budget.go:223)
- **文件**: `internal/platform/textutil/token_budget.go`
- **问题**: 当 tokens 超标但 runes <= HeadRunes+TailRunes 时，直接返回原文本不做任何裁剪
- **修复方案**: 当文本极短但 tokens 超标时，按比例缩减 head/tail 直到满足预算

### 高优先级: 噪声门语义预加载单次失败导致全局降级 (noise_gate.go:130)
- **文件**: `internal/logic/processor/noise_gate.go`
- **问题**: preloadSemanticPrototypes 任何一个 embedding 调用失败，semanticEnabled 被置为 false，所有类别的语义匹配全部失效
- **修复方案**: 改为 per-category 降级而非全局降级；失败类别回退到正则模式，其他类别保持语义匹配

### 中优先级: 噪声门缺少 common.json 时静默丢失公共类别 (noise_gate.go:447)
- **文件**: `internal/logic/processor/noise_gate.go`
- **问题**: 只检查 common 和语言文件都为空才报错，导致 common 缺失时静默丢失公共类别
- **修复方案**: 增加 warn 日志；或者要求在 semantic 模式下 common.json 必须存在

### 中优先级: PII 引擎缺少 common.json 时静默丢失通用规则 (pii/engine.go:428)
- **文件**: `internal/platform/pii/engine.go`
- **问题**: common.json 缺失时 applyLanguageOverride 跳过，通用 PII（邮箱、信用卡、API Key 等）全部失效
- **修复方案**: 增加警告日志或启动校验要求 common.json 存在

### 中优先级: config_validate 缺少 post_action 超时校验 + env 整数错误静默忽略
- **文件**: `internal/config/config_validate.go`
- **问题**: 1) 没有校验 post_action timeout 是否大于其内部 intent timeout；2) env 整数解析错误静默忽略
- **修复方案**: 增加超时关系校验 + env 解析失败时 warn 日志

### 中优先级: PostAction 队列损坏 turn 跳过但不清理 (postaction_queue.go)
- **文件**: `internal/app/usecase/postaction_queue.go`
- **问题**: continue 跳过后 turn 仍留在 pending 状态，下次扫描重复跳过
- **修复方案**: 对损坏 turn 做标记（跳过计数/死信），避免无限重试

## 执行步骤
1. 逐个修复 bug
2. 编译验证
3. 测试验证
4. 提交代码

## 验收标准
1. 所有 6 个 bug 已修复 ✅
2. 编译通过 ✅
3. 测试通过 ✅

## 执行变更总结

### 核心修复概述
1. **HIGH: token_budget 密集文本绕过** — 移除 `len(runes) <= HeadRunes+TailRunes` 时的直接返回，改为对高密度文本直接进入渐进收缩逻辑
2. **HIGH: noise_gate 语义预加载全局降级** — 将 `preloadSemanticPrototypes` 从返回 error 改为返回失败类别计数；单个类别 embedding 失败不再禁用全局语义匹配，失败类别自动回退到正则模式
3. **MEDIUM: noise_gate 缺失 common.json 静默丢失** — 在 `NewNoiseGate` 中增加 `selectNoiseRuleFile` 检查，当 common.json 不存在时输出 Warn 日志
4. **MEDIUM: PII engine 缺失 common.json 静默丢失** — 在 `NewEngineWithLogger` 中检查 `commonLanguage` 是否存在于已加载语言中，缺失时输出 Warn 日志
5. **MEDIUM: config_validate 校验增强** — 新增 `grpc.request_timeout.post_action > pre_check.intent_timeout` 校验；`applyEnvOverrides` 现在返回解析失败的 env key 列表，并在 `config_load.go` 中输出到 stderr
6. **MEDIUM: postaction 损坏 turn 无限跳过** — 将 `turnRecordFromStoredTurn` 失败时的 `continue` 改为 `return`，避免损坏 turn 永远停留在 pending 状态导致无限循环跳过

### 📂 文件变更清单
- **修改**: `internal/platform/textutil/token_budget.go` — 移除短文本绕过逻辑
- **修改**: `internal/logic/processor/noise_gate.go` — per-category 降级 + common.json 缺失警告
- **修改**: `internal/platform/pii/engine.go` — common.json 缺失警告
- **修改**: `internal/config/config_validate.go` — post_action 超时校验 + env 解析失败收集
- **修改**: `internal/config/config_load.go` — 输出 env 解析失败警告
- **修改**: `internal/config/config_test.go` — 6 处 `applyEnvOverrides` 返回值处理 + 2 处 test config 增加 post_action timeout
- **修改**: `internal/app/usecase/postaction_queue.go` — 损坏 turn 从 continue 改为 return
