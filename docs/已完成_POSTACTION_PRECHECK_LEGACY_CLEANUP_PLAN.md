# PostAction / PreCheck Legacy Cleanup Plan

## 任务目标 / Goal

- 提交并推送当前已经完成的异步 `PostAction` 与 turn-centric `PreCheck` 调整。
- 清理仓库中已经不再作为主链路使用的旧 batch 处理逻辑、残余提示词文件和过期文档描述。
- 保证清理后构建、测试、文档与运行时行为保持一致。

## 技术策略 / Technical Approach

- 保留当前异步 `PostAction` 主链路与 turn-centric `PreCheck` 主链路。
- 移除仅服务旧 `analyze_session_batch` 批处理模式的处理器、prompt、测试和无效引用。
- 如果底层 schema 或兼容字段暂时仍被运行时依赖，则只清理“未再被主链调用的逻辑代码与提示词文件”，避免误删存量数据结构。
- 文档同步以“当前运行时真实行为”为准，移除对旧批处理主链的描述。

## 执行步骤 / Execution Steps

1. 检查当前 git 状态，确认本轮需要提交的文件与远端分支信息。
2. 提交并推送当前已经完成的异步 `PostAction` 与 `PreCheck` 调整。
3. 识别旧 batch 处理链路的残余入口，包括：
   - `SessionBatchAnalyzer`
   - `analyze_session_batch` prompt
   - `LoadPendingSessionTurns` / `ApplySessionBatchAnalysis` 等仅用于旧链路的接口与实现
   - 相关遗留测试与文档描述
4. 在不破坏当前主链的前提下移除上述旧逻辑与残余文件。
5. 同步更新文档、注释与配置说明，确保描述和代码一致。
6. 运行最小必测集与全量测试，修复清理带来的问题。
7. 对照本计划逐项复核；全部完成后，将本计划文件重命名为 `已完成_...`。

## 验收标准 / Acceptance Criteria

- 当前工作区中的已实现功能已成功提交并推送到远端。
- 仓库中不再保留 `analyze_session_batch` 主链处理器、相关 prompt 文件与无效代码入口。
- `PostAction` 仍然保持“先返回成功、后台异步提炼”的行为。
- `PreCheck` 仍然保持“最近 N 轮 turn 混合上下文 + 多检索语句 + 二轮候选选择”的行为。
- 关键文档已同步更新，不再误导为旧 batch 主链。
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  通过。
- `go test ./...` 通过。

## 风险与边界 / Risks And Boundaries

- `vmm_sessions` 中与旧总结进度相关的兼容字段如果仍被 schema 或已有数据使用，本轮默认不做破坏性 schema 删除。
- 若发现某段旧逻辑仍被当前主链或兼容链路隐式依赖，则先修正依赖再删除，不做盲删。
