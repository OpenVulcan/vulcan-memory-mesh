# 任务计划：embedding 部分丢弃回归修复

## 一、任务目标

修复当前未提交改动中由 embedding “部分成功 / 部分丢弃”新契约引入的两个回归问题，确保：

1. 当 `AllowPartialInvalidTexts=true` 且所有输入都被判定为无效时，调用链能够返回“全量 dropped”这一合法结果，而不是被误判为响应格式错误。
2. post-action 在丢弃部分 memory node 后，会同步收敛 `SupersededMemoryIDs`，避免错误退役未被新节点替代的旧记忆。
3. 新契约继续保持 strict 调用方与 best-effort 调用方的边界清晰，不破坏 `memory_query`、`noise_gate`、`vector_rebuild` 等严格路径。

## 二、技术方案

### 2.1 放宽全量 dropped 的索引契约

在 `EmbeddingResponse.normalizedResultIndices` 中区分两种情况：

1. 没有 `ResultIndices` 且也没有 `Dropped`：保持旧语义，要求 `Vectors` 数量与输入数量一致。
2. 没有 `ResultIndices` 但存在 `Dropped`：
   - 若 `Vectors` 为空，则允许这是“全量 dropped”的合法部分成功响应；
   - 若 `Vectors` 非空，则仍要求调用方显式提供 `ResultIndices`，避免成功向量无法回绑原始索引。

这样可以保证：

- best-effort 路径支持“全部条目都被跳过”；
- 只要返回里混有成功向量，就仍然必须带索引映射，避免顺序歧义。

### 2.2 post-action 在丢弃后重新对齐 supersede 集合

`persistMemoryNodeVectors` 在根据 embedding 结果收缩 `analysis.MemoryNodes` 后，需要立即基于保留下来的节点重新计算顶层 `analysis.SupersededMemoryIDs`。

处理原则：

1. 仅保留仍然存在于 `keptNodes` 中的 candidate-local `SupersedeMemoryIDs`。
2. 若全部 memory node 都被丢弃，则将顶层 `SupersededMemoryIDs` 清空。
3. 使用现有统一 reviewer 已经采用的候选级 supersede 汇总逻辑，保持 post-action 各阶段语义一致。

### 2.3 测试补齐

需要新增或扩展以下测试：

1. `EmbeddingResponse` 支持“全量 dropped、零向量”的合法结果读取。
2. post-action 在部分 memory node 被 dropped 时，不仅保留成功节点，还会同步清理顶层 `SupersededMemoryIDs`。
3. post-action 在全量 memory node 被 dropped 时，不报错，并且不会产生错误 supersede。

## 三、执行步骤

1. 检查 `internal/logic/ports/embedding.go` 的索引归一化逻辑，修复“全量 dropped”误判。
2. 检查 `internal/app/usecase/postaction.go` 的 memory node 收缩逻辑，补充 supersede 重算。
3. 在对应测试文件中补充最小闭环测试，覆盖 review 提到的两个回归点。
4. 运行与 post-action / 配置 / 处理器相关的必需测试命令，确认没有引入新的 strict/best-effort 语义回归。

## 四、验收标准

1. `AllowPartialInvalidTexts=true` 时，`IndexedVectors` 能接受“零向量 + 全量 dropped”的响应。
2. post-action 在 memory node 被 dropped 后，不会错误保留已经失效的 `SupersededMemoryIDs`。
3. 相关新增测试全部通过，且仓库要求的最小回归测试通过。

## 执行变更总结

### 1. 核心修复与调整概述

本次修复围绕 review 指出的两个回归点完成闭环：

1. 调整 `EmbeddingResponse` 的索引归一化逻辑，允许 best-effort embedding 在“全部输入都被判定为无效”时返回“零向量 + 全量 dropped”的合法结果，不再误报格式错误。
2. 调整 post-action 在 embedding 过滤 memory node 后的收敛流程，确保仅保留仍然存活节点对应的 supersede 目标，避免 dropped 节点继续错误退役旧记忆。
3. 补充覆盖 embedding、candidate review 与 post-action 的回归测试，并修正测试夹具中的 active memory anchor 前置条件，保证用例验证真实生产路径。

### 2. 📂文件变更清单

新增：

1. `internal/logic/ports/embedding_test.go`

修改：

1. `internal/logic/ports/embedding.go`
2. `internal/app/usecase/postaction_candidate_review.go`
3. `internal/app/usecase/postaction.go`
4. `internal/app/usecase/postaction_candidate_review_test.go`
5. `internal/app/usecase/postaction_test.go`

删除：

1. 无

### 3. 💻关键代码调整详情

1. 在 `internal/logic/ports/embedding.go` 中放宽 `normalizedResultIndices` 对 best-effort 响应的判定：当 `Dropped` 覆盖全部输入且 `Vectors` 为空时，允许无 `ResultIndices` 的合法返回；但若仍存在成功向量，则继续要求显式索引映射。
2. 在 `internal/app/usecase/postaction_candidate_review.go` 中统一 reviewer 后的 supersede 合并与过滤逻辑，使被接纳候选的 candidate-local supersede id 可以稳定保留，并与 reviewer 批准的 similar-memory 替代目标合并去重。
3. 在 `internal/app/usecase/postaction.go` 中，在 embedding 丢弃 memory node 后立即重新收敛 `analysis.SupersededMemoryIDs`，确保 dropped 节点不再对最终持久化 supersede 集合产生影响。
4. 在测试中新增“全量 dropped 合法返回”“部分 dropped 后 supersede 收敛”“全量 dropped 后不再错误 supersede”三类断言，并为依赖 `SupersedeMemoryIDs` 的场景补齐 active memory anchor，避免测试被前置校验误伤。

### 4. ⚠️遗留问题与注意事项

1. 当前仓库仍存在本次任务之外的未提交修改，已按协作规范保持原样，未对其做回退或整理。
2. `go test ./...` 已完成通过；本次未涉及构建/打包目录规则，因此未执行 `make.ps1 build`。
