# 任务目标

针对当前 `google_ai_studio` 原生适配器的评审问题做定向修复，确保 Google AI Studio 在固定模型 `key_failover` 场景下：

- 能正确识别“坏 Key / 无效 Key”并切换到下一把 Key；
- 不会把共享权限类 `403` 误判成单 Key 失效并批量熔断整池 Key；
- 保持现有 OpenAI-compatible、DashScope 以及其余运行时路径不受影响。

# 详细执行步骤

1. 复查 `ai_key_failover/classifier.go` 中 Google AI Studio 错误分类逻辑，确认当前 `400` 与 `403` 的决策边界。
2. 设计 Google AI Studio 专属判定规则：
   - 哪些 `400` 需要视为单 Key 失效并允许切 Key；
   - 哪些 `403` 应视为共享权限或模型访问问题，禁止盲目切 Key；
   - 继续保持真正的限流、额度、鉴权错误与现有 failover 状态机兼容。
3. 在 `classifier.go` 中实现最小化修复，并补充必要的中英文双语注释，说明判定依据与设计意图。
4. 在 `key_failover_test.go` 中补充或更新针对 Google AI Studio 的回归测试，覆盖：
   - `400 invalid key` 场景会触发切 Key；
   - `403 permission denied` 共享权限场景不会切 Key。
5. 运行最少必要测试，确认修复没有破坏当前 Google AI Studio 路径与既有 failover 行为。
6. 自检通过后，在本文末尾补充执行变更总结，并迁移到 `docs/completed/20260406/`。

# 技术选型

- 不新增新的 provider 配置字段，继续沿用现有 `google_ai_studio` provider 形态，避免扩大配置面。
- 保持修复聚焦在错误分类层，不改动 `selector`、多路由主流程或配置结构，降低回归风险。
- 优先依赖 Google GenAI SDK 已暴露的 `Code / Status / Message / Details` 信息做错误归类，避免引入额外网络协议解析耦合。

# 验收标准

- Google AI Studio 的坏 Key 类 `400` 错误可以触发切 Key。
- Google AI Studio 的共享权限类 `403` 错误不会误触发整池 Key 冷却。
- 至少相关 failover 单测通过。
- 仓库规定的相关 Go 测试通过，且没有引入新的编译或测试回归。

# 执行变更总结

## 1. 核心修复与调整概述

- 修复 `google_ai_studio` 在 `400` 与 `403` 场景下的 Key 容灾判定边界。
- 将原先“所有 `400` 都视为 `invalid_request`、所有 `403` 都视为 `auth`”的粗粒度逻辑，收敛为“仅当错误文本明确指向单 Key 凭据失效时才切 Key”的细粒度逻辑。
- 保持 `429` 的额度 / 限流判定、OpenAI-compatible 路径以及既有多路由逻辑不变，避免扩大本次修复范围。

## 2. 📂文件变更清单

### 修改

- `internal/adapters/outbound/ai_key_failover/classifier.go`
- `internal/adapters/outbound/ai_key_failover/key_failover_test.go`

### 新增

- 无

### 删除

- 无

## 3. 💻关键代码调整详情

- 在 `classifier.go` 中新增 Google AI Studio 凭据级错误判定辅助函数：
  - 当 `400` 载荷包含 `API_KEY_INVALID`、`api key not valid`、`invalid authentication credentials` 等明确坏 Key 特征时，改判为 `auth + switch key`。
  - 当 `403` 仅表现为共享权限或模型访问问题时，改判为 `public_fault`，避免整池 Key 被错误打入 `auth_cooldown`。
  - 当 `403` 明确描述单 Key 凭据失效时，仍保留切 Key 行为。
- 在 `key_failover_test.go` 中新增三组 Google AI Studio 回归测试：
  - `400 invalid key` 会切到下一把 Key；
  - 共享权限类 `403` 不会继续尝试其他 Key；
  - 凭据级 `403` 仍会切到下一把 Key。

## 4. ⚠️遗留问题与注意事项

- 当前 Google AI Studio 的分类仍然主要依赖 `Code / Status / Message / Details` 中的文本与 reason 特征；如果 Google 后续调整错误载荷字段命名，需要同步更新判定关键字。
- 本次未扩展 Google AI Studio 的 `Retry-After` 冷却时长解析能力，因为 `genai.APIError` 当前未直接暴露响应头，修复范围保持在评审指出的 `400/403` 误判问题上。
- 已执行：
  - `go test ./internal/adapters/outbound/ai_key_failover`
  - `go test ./internal/app ./internal/config`
  - `go test ./...`
