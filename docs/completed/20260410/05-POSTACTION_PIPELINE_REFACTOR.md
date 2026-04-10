# 第三阶段执行计划：PostAction 流水线解耦拆分

## 1. 任务目标

本阶段聚焦 `internal/app/usecase` 中的 PostAction 流水线，将当前分散在 `postaction.go`、`postaction_queue.go`、`postaction_profiles.go` 等文件中的 intake、队列调度、分析落地、画像维护职责进一步拆分为更清晰的同包实现文件，在不改变外部契约、行为语义和测试结果的前提下，降低后续演进时的耦合风险。

## 2. 执行步骤

1. 盘点 PostAction 入口、后台队列、画像维护、分析落地相关的结构体、接口、公共方法和私有辅助函数。
2. 确认现有测试覆盖面与同包私有函数依赖，避免拆分后破坏测试编译边界。
3. 设计同包拆分方案，明确哪些逻辑保留在核心入口文件，哪些逻辑迁移到独立职责文件。
4. 拆分 intake 与同步入口逻辑。
5. 拆分后台队列与 worker 调度逻辑。
6. 拆分分析结果应用、记忆落地和画像维护逻辑。
7. 补齐新增源码文件的双语文件头注释，并修正函数注释归属。
8. 运行关键测试与全量测试，确认行为一致。
9. 在计划末尾追加执行变更总结，并将计划归档到 `docs/completed/20260410/`。

## 3. 技术方案

- 采用“同包拆文件”的方式重构，不新增包层级，避免打乱 `adapters -> app -> logic/domain` 的依赖方向。
- 保持 `PostActionUseCase` 及其现有公共方法签名不变，确保 gRPC 入站层、应用装配层和现有测试无需适配。
- 以职责域划分文件：
  - PostAction 入口与 intake 编排
  - 队列、worker、后台调度
  - 分析结果应用与记忆持久化
  - 画像维护与辅助收敛逻辑
- 以“先迁移、后验证”的方式控制风险，不在本阶段引入新的业务语义调整。

## 4. 验收标准

- PostAction 相关实现不再由少数大文件集中承载主要职责。
- 新增文件职责单一，且同包内调用关系清晰。
- 对外接口、结构体、公共方法签名保持兼容。
- 以下测试至少通过：
  - `go test ./internal/app/usecase`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `go test ./...`

## 5. 风险与关注点

- 需要避免拆分过程中破坏后台队列初始化、worker 生命周期和运行时关闭顺序。
- 需要确认画像维护逻辑与分析落地逻辑之间的隐式共享辅助函数不会被错误切断。
- 需要保证 post-action 与 noise / pii / 文本清洗相关测试在拆分后仍保持原有行为。

## 6. 当前状态

- 状态：已完成
- 当前阶段：代码拆分、验证与总结已完成，待归档

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已完成 PostAction 流水线的同包职责拆分，把原先仍集中在 `postaction.go` 内的 intake 与 analysis apply 逻辑拆到独立实现文件。
- 本次重构保持 `PostActionUseCase`、`PostActionExecutor` 以及现有公共方法签名不变，确保运行时装配、gRPC 入站层与测试代码无需适配。
- 已把画像专用的 `postActionProfileNodeRef` 从队列文件归位到画像维护文件，使 queue / profiles 的边界更清晰。

### 2. 📂 文件变更清单

#### 新增

- `internal/app/usecase/postaction_intake.go`
- `internal/app/usecase/postaction_analysis.go`

#### 修改

- `internal/app/usecase/postaction.go`
- `internal/app/usecase/postaction_queue.go`
- `internal/app/usecase/postaction_profiles.go`

#### 删除

- 无

### 3. 💻 关键代码调整详情

- 将 `Execute`、`validatePostAction`、turn 重建、payload 组装与预算裁剪逻辑迁移到 `postaction_intake.go`，使入站接收与分析落地解耦。
- 将 `applyImmediateTurnAnalysis`、`buildTurnAnalysisInput`、`persistMemoryNodeVectors`、分析结果日志、UUID 与 transcript 辅助逻辑迁移到 `postaction_analysis.go`，集中承载分析应用与向量持久化职责。
- 将 `postaction.go` 收敛为 PostAction 的核心契约、`PostActionUseCase` 结构、构造函数与基础配置入口。
- 将 `postActionProfileNodeRef` 迁移到 `postaction_profiles.go`，避免队列文件继续承载画像域专用类型。
- 已完成代码格式化，并通过以下验证：
  - `go test ./internal/app/usecase`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `go test ./...`

### 4. ⚠️ 遗留问题与注意事项

- 当前阶段仍采用“同包拆文件”策略，尚未把 PostAction 进一步拆成独立子包；这是为了避免在这一轮引入更大范围的依赖重组。
- `postaction_candidate_review.go` 仍然承载统一 reviewer 相关逻辑，后续若进入更深一层治理，可考虑继续把 memory review 构建与 reviewer 结果应用拆开。
