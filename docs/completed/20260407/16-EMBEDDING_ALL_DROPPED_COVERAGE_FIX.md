# 任务计划：embedding 全量 dropped 覆盖校验修复

## 一、任务目标

修复 `EmbeddingResponse.normalizedResultIndices` 在“`ResultIndices` 为空、`Dropped` 非空、`Vectors` 为空”分支下的校验缺口，确保只有当 `Dropped` 完整覆盖全部输入索引时，才把该响应判定为合法的“全量 dropped”结果。

## 二、技术方案

### 2.1 收紧全量 dropped 的合法条件

在 `internal/logic/ports/embedding.go` 中保留现有的越界与重复检查，并额外补充：

1. 统计 `Dropped` 中已声明的唯一输入索引数量。
2. 若该数量不等于 `inputCount`，则返回明确错误，而不是继续当作合法成功响应。

这样可以防止适配器返回“零向量 + 部分 dropped + 其余输入未声明”的不完整响应时，被上层静默吞掉未覆盖的输入。

### 2.2 回归测试补齐

在 `internal/logic/ports/embedding_test.go` 中新增一条回归测试，验证：

1. 对于 3 条输入，只声明 1 条 dropped 且无向量时，`IndexedVectors` 会返回错误。
2. 现有“全部输入都 dropped”测试继续保持通过。

## 三、执行步骤

1. 检查 `normalizedResultIndices` 的全量 dropped 分支。
2. 增加“dropped 必须覆盖全部输入”的校验。
3. 补充对应测试用例。
4. 运行与 embedding 契约和主要调用链相关的测试，确认 strict 与 best-effort 路径都未回归。

## 四、验收标准

1. “零向量 + 部分 dropped + 缺失其余输入声明”会被拒绝。
2. “零向量 + 全量 dropped”仍被接受。
3. 相关测试全部通过。

## 执行变更总结

### 1. 核心修复与调整概述

本次修复补上了 `EmbeddingResponse` 在“零向量 + dropped”快捷路径中的覆盖校验缺口，避免适配器只声明部分 dropped 条目时被误判为合法的“全量 dropped”成功响应。

### 2. 📂文件变更清单

新增：

1. 无

修改：

1. `internal/logic/ports/embedding.go`
2. `internal/logic/ports/embedding_test.go`

删除：

1. 无

### 3. 💻关键代码调整详情

1. 在 `internal/logic/ports/embedding.go` 中保留 dropped 索引越界与重复校验的基础上，新增“已声明 dropped 的唯一索引数量必须等于输入总数”的约束。
2. 在 `internal/logic/ports/embedding_test.go` 中新增“不完整 dropped 覆盖必须报错”的回归测试，同时保留并验证“完整全量 dropped”仍合法。

### 4. ⚠️遗留问题与注意事项

1. 仓库中仍有本次任务之外的未提交修改，已保持原样未回退。
2. 本次已执行 `go test ./...`，未发现新增回归。
