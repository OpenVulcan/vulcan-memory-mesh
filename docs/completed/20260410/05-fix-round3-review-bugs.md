# Bug 修复计划 - 第三轮审核修复

## 任务目标
修复第三轮代码审核中发现的 5 个已确认 bug。

## 修复清单

### 高优先级: LuhnSum/WeightSum TrimSpace bug (pii/validators.go)
- **文件**: `internal/platform/pii/validators.go`
- **问题**: LuhnSum 和 WeightSum 检查 `TrimSpace(s)` 是否为空，但迭代循环使用原始 `s`，导致包含空格的输入被错误拒绝
- **修复方案**: 在迭代前先 `s = strings.TrimSpace(s)`

### 中优先级: ensureSession ON CONFLICT no-op (vldb_postgres/workspace.go:435)
- **文件**: `internal/adapters/outbound/vldb_postgres/workspace.go`
- **问题**: `DO UPDATE SET updated_at = 表名.updated_at` 设置为自身值，应为 `EXCLUDED.updated_at`
- **修复方案**: 改为 `EXCLUDED.updated_at`

### 中优先级: reconcileKeyBudget 遗漏 DayRequests (ai_key_failover/state.go)
- **文件**: `internal/adapters/outbound/ai_key_failover/state.go`
- **问题**: reserveKeyBudgetLocked 预留 MinuteRequests/MinuteTokens/DayRequests，但 reconcileKeyBudget 只调整 MinuteTokens
- **修复方案**: reconcileKeyBudget 同时调整 DayRequests 和 MinuteRequests

### 中优先级: Google 错误分类器忽略 Retry-After (ai_key_failover/classifier.go)
- **文件**: `internal/adapters/outbound/ai_key_failover/classifier.go`
- **问题**: classifyGoogleAIStudioError 429 处理不使用 chooseCooldown，忽略 Retry-After header
- **修复方案**: Google 429 处理也使用 chooseCooldown 解析 Retry-After

## 执行步骤
1. 逐个修复 bug
2. 编译验证
3. 测试验证
4. 提交代码

## 验收标准
1. 所有 5 个 bug 已修复
2. 编译通过
3. 测试通过

---

## 执行变更总结

### 核心修复概述

修复了第三轮深度代码审核中发现的 5 个已确认 bug，涵盖 PII 验证器字符串处理、PostgreSQL 会话更新 no-op、配额回补遗漏、Google 错误分类器忽略 Retry-After。

### 文件变更清单（修改 4 个文件）

| 文件 | 变更类型 | 修复内容 |
|------|---------|---------|
| `internal/platform/pii/validators.go` | 修改 | LuhnSum/WeightSum TrimSpace bug |
| `internal/adapters/outbound/vldb_postgres/workspace.go` | 修改 | ensureSession ON CONFLICT no-op |
| `internal/adapters/outbound/ai_key_failover/state.go` | 修改 | reconcileKeyBudget 遗漏 DayRequests/MinuteRequests |
| `internal/adapters/outbound/ai_key_failover/classifier.go` | 修改 | Google 分类器忽略 Retry-After |

### 关键代码调整详情

1. **LuhnSum** (validators.go): 将 `strings.TrimSpace(s)` 的结果赋值回 `s`，使后续迭代循环使用去空格后的字符串。WeightSum 同理修复。

2. **ensureSession** (workspace.go): `DO UPDATE SET updated_at = %s.updated_at` 改为 `DO UPDATE SET updated_at = EXCLUDED.updated_at`，确保冲突时更新时间戳。同时去掉多余的 `r.sessionsTable()` 参数。

3. **reconcileKeyBudget** (state.go): 新增 `requestDelta` 计算并同步调整 `MinuteRequests` 和 `DayRequests`。去掉了 `tokenDelta == 0` 的早期返回，因为现在还需要检查 request 是否为零差值。

4. **classifyGoogleAIStudioError** (classifier.go): 429 处理改为调用 `chooseCooldownFromGoogleError`，该函数尝试从 `genai.APIError.Details` 中提取 `retry_after_ms` / `retry_after` 字段，并从错误消息字符串中解析 `Retry-After` header 模式。新增 `parseRetryAfterFromText` 辅助函数。

### 遗留问题与注意事项

- Google AI SDK 的 `genai.APIError.Details` 字段类型为 `[]map[string]any`，Retry-After 信息通常出现在 `retry_after_ms` 键中。如果 Google 改变 SDK 结构，`chooseCooldownFromGoogleError` 会安全地回退到配置的默认 cooldown。
- `reconcileKeyBudget` 去掉了 `tokenDelta == 0` 的早期返回，现在即使 token 为零差值也会执行 request 差值的检查。这是正确的，因为 token 预估可能准确而 request 预估不准确。
- 所有修复已通过 `go build ./...` 和 `go test ./...` 验证。
