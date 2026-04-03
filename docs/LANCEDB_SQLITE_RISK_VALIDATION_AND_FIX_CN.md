# LanceDB / SQLite 风险判断核实与修复报告

## 摘要

本次对 2 类风险做了代码级核实，结论如下：

1. `vldb_lancedb/store.go` 中忽略 `strconv` / `json.Number` 解析错误的问题，结论是“风险判断部分准确”。
   它确实会把畸形数值静默吞成零值，属于真实的错误处理与数据完整性风险；但更准确地说，它不是高危远程安全漏洞，而是网关返回异常数据时会导致结果被错误接受的问题。该问题已修复。
2. `vldb_sqlite/store.go` 中 `fmt.Sprintf` 构造 SQL 被直接定性为“SQL 注入风险”，结论是“作为可利用漏洞的判断不准确”。
   当前涉及值都是 `uint64` 和内部常量，不存在把攻击者字符串拼进 SQL 的直接注入面；但从安全编码最佳实践看，参数化查询依然更合适。因此本次对低成本且收益明确的点完成了参数化硬化。

## 1. LanceDB 数字解析错误被静默吞掉

### 原始判断是否准确

部分准确。

### 真实情况

问题位置在：

- `internal/adapters/outbound/vldb_lancedb/store.go`

原实现里：

- `team_id / space_id / project_id / session_id / user_id` 的字符串数值解析使用 `strconv.ParseUint(... )`，但忽略错误。
- `_distance / distance` 的字符串或 `json.Number` 解析也忽略错误。

这意味着当 LanceDB 网关返回畸形数字字符串时：

- 解析失败会被静默吞掉；
- `searchRow` 会继续以零值 `0` 完成反序列化；
- 上层检索流程会把坏数据当作合法结果继续处理。

这不是典型的“攻击者可直接注入执行”的高危安全洞，但确实是一个真实的稳健性与数据完整性问题。

### 是否需要修复

需要修复。

### 修复方式

已改为：

- `asUint64(...)` / `asFloat64(...)` 返回显式错误；
- `searchRow.UnmarshalJSON(...)` 按字段包装并上抛解析错误；
- 坏数据现在会让 `Search(...)` 失败，而不是被默默归零。

### 修复结果

已修复，并补充回归测试：

- `TestSearchRejectsMalformedNumericFields`

该测试验证：当 LanceDB 返回畸形数值字符串时，搜索会明确报错，而不是吞掉错误继续返回结果。

## 2. SQLite `fmt.Sprintf` 构造 SQL

### 原始判断是否准确

如果把它定性为“当前存在可利用 SQL 注入漏洞”，这个判断不准确。

### 真实情况

问题位置主要包括：

- `internal/adapters/outbound/vldb_sqlite/store.go` 中 `loadRenderedProfileByTarget(...)`
- `internal/adapters/outbound/vldb_sqlite/store.go` 中项目画像节点删除/统计 SQL 构造辅助逻辑

这些位置插入到 SQL 中的值当前都是：

- `uint64` 类型 ID；
- 内部枚举常量；
- 由服务端内部逻辑控制的布尔分支。

因此从当前代码形态看：

- 不存在把攻击者提供的原始字符串直接拼接进 SQL 的路径；
- 这不构成一个现实可利用的 SQL 注入漏洞。

不过，从安全编码与可维护性角度看，参数化查询仍然是更好的默认方案，因为它能：

- 避免后续改动时不小心把非数值输入扩展进原始 SQL；
- 让查询构造逻辑和参数绑定语义更清晰；
- 保持与仓库其它 SQLite typed params 路径一致。

### 是否需要修复

作为“当前 SQL 注入漏洞”，不需要按漏洞处置。

作为“安全硬化与最佳实践统一”，值得修复，并且本次已对低成本且收益明确的点完成修复。

### 修复方式

本次已完成两处硬化：

1. `loadRenderedProfileByTarget(...)`
   - 从 `fmt.Sprintf("... id = %d", bindID)` 改为 `WHERE id = ?`
   - 通过 `queryRows(..., bindID)` 传入 typed params
2. 项目画像节点删除/统计辅助逻辑
   - 把 `buildProjectProfileNodesDeleteWhere(...)` 改为返回 `where SQL + params`
   - `buildProjectProfileNodesDeleteSQL(...)` / `buildProjectProfileNodesCountSQL(...)` 改为返回 `sql + params`
   - 调用方通过 `exec(..., params...)` / `countRows(..., params...)` 绑定参数

### 修复结果

已完成硬化，并补充回归测试：

- `TestStoreLoadRenderedProfileUsesTypedSQLiteParams`
- `TestBuildProjectProfileNodesDeleteSQLUsesTypedParams`

这两条测试分别验证：

- scope 画像读取确实改成了 `?` 占位符和 typed params；
- 项目画像节点删除/统计 SQL 不再把数值直接嵌入语句文本，而是返回占位符加稳定参数顺序。

## 本次变更文件

- `internal/adapters/outbound/vldb_lancedb/store.go`
- `internal/adapters/outbound/vldb_lancedb/store_test.go`
- `internal/adapters/outbound/vldb_sqlite/store.go`
- `internal/adapters/outbound/vldb_sqlite/store_test.go`

## 验证结果

已通过：

- `go test ./internal/adapters/outbound/vldb_lancedb ./internal/adapters/outbound/vldb_sqlite -count=1`

并将在本次任务结束前继续通过仓库要求的完整验证：

- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `.\make.ps1 build`
