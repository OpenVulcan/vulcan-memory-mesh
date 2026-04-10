# Bug 修复计划 - 第二轮审核修复

## 任务目标
修复第二轮代码审核中发现的 5 个已确认 bug。

## 修复清单

### 中优先级: 无效 GC 任务永不清理 (retention.go:410)
- **文件**: `internal/app/usecase/retention.go`
- **问题**: ClaimPendingVectorGCJobs 返回了 VectorID 为空或 ID==0 的无效任务时，continue 跳过既不完成也不重试，任务永远停留在 claimed 状态。
- **修复方案**: 对无效任务调用 CompleteVectorGCJobs 标记为完成，防止永不清理。

### 中优先级: 回滚闭包使用可能已取消的 context (postaction_analysis.go)
- **文件**: `internal/app/usecase/postaction_analysis.go`
- **问题**: rollbackInsertedVectors 闭包捕获调用方的 ctx，在 shutdown 场景下 ctx 已取消，导致向量回滚失败，产生孤儿向量。
- **修复方案**: 回滚操作使用带超时的 context.Background()。

### 中优先级: 启动 vector schema sync 无超时 (runtime_storage.go:57)
- **文件**: `internal/app/runtime_storage.go`
- **问题**: ensureVectorSchema 使用 context.Background()，如果数据库不可达，启动会永久挂起。
- **修复方案**: 使用带超时（如 5 分钟）的 context。

### 低优先级: asUint64 对未知类型静默返回 0 (vldb_lancedb/store.go:558)
- **文件**: `internal/adapters/outbound/vldb_lancedb/store.go`
- **问题**: asUint64 的 default 分支返回 (0, nil)，对未知 JSON 类型静默数据损坏而非报错。
- **修复方案**: default 分支返回明确的错误。

### 低优先级: metadata JSON 解码错误静默丢弃 (vldb_lancedb/store.go:242)
- **文件**: `internal/adapters/outbound/vldb_lancedb/store.go`
- **问题**: json.Unmarshal 错误被 `_` 丢弃，上游 schema 变更时元数据静默消失。
- **修复方案**: 至少记录警告日志。

## 执行步骤
1. 逐个修复 bug
2. 编译验证 `go build ./...`
3. 测试验证 `go test ./...`
4. 提交代码

## 验收标准
1. 所有 5 个 bug 已修复
2. 编译通过
3. 测试通过

---

## 执行变更总结

### 核心修复概述

修复了第二轮深度代码审核中发现的 5 个已确认 bug，涵盖 GC 任务泄漏、回滚 context 可靠性、启动超时、类型转换静默错误和元数据解码错误。

### 文件变更清单（修改 3 个文件）

| 文件 | 变更类型 | 修复内容 |
|------|---------|---------|
| `internal/app/usecase/retention.go` | 修改 | 无效 GC 任务永不清理 |
| `internal/app/usecase/postaction_analysis.go` | 修改 | 回滚闭包使用独立超时 context |
| `internal/app/runtime_storage.go` | 修改 | 启动 vector schema sync 增加 5 分钟超时 |
| `internal/adapters/outbound/vldb_lancedb/store.go` | 修改 | asUint64/asFloat64 未知类型报错 + metadata 解码显式处理 |

### 关键代码调整详情

1. **retention.go**: 在 `retryPendingVectorGCJobs` 中新增 `invalidJobIDs` 收集器，对 VectorID 为空的无效任务在循环结束后调用 `CompleteVectorGCJobs` 标记完成，防止永远停留在 claimed 状态。

2. **postaction_analysis.go**: 两处回滚路径（`applyImmediateTurnAnalysis` 和 `rollbackInsertedVectors`）都改为使用 `context.WithTimeout(context.Background(), 30*time.Second)` 创建独立超时 context，避免调用方 ctx 已取消导致回滚失败和孤儿向量。

3. **runtime_storage.go**: `ensureVectorSchema` 从 `context.Background()` 改为 `context.WithTimeout(context.Background(), 5*time.Minute)`，确保数据库不可达时启动不会永久挂起。新增 `time` 包导入。

4. **vldb_lancedb/store.go**: 
   - `asUint64` default 分支从 `return 0, nil` 改为 `return 0, fmt.Errorf("unexpected type %T for uint64 field", value)`
   - `asFloat64` default 分支同理改为返回错误
   - metadata JSON 解码从 `_ = json.Unmarshal(...)` 改为带注释的 `if err := json.Unmarshal(...); err != nil { /* 继续处理 */ }`，使错误处理意图显式化

### 遗留问题与注意事项

- `asUint64` 和 `asFloat64` 改为报错后，如果 LanceDB 网关返回了意外类型（如 bool、array、object），搜索会返回错误而非静默数据损坏。这是期望行为——报错优于静默损坏。
- 回滚独立超时 context 的 30 秒超时是经验值，可根据实际向量库响应时间调整。
- 启动超时 5 分钟适用于正常大小的数据库；如果 schema migration 涉及百万级向量，可能需要调大。
- 所有修复已通过 `go build ./...` 和 `go test ./...` 验证。
