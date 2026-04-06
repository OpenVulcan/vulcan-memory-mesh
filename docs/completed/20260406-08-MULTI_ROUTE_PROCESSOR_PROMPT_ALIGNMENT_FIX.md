## 任务目标

修复多路由 LLM 处理器在异构模型容灾场景下的提示词错配问题，确保处理器不会把主模型专属 prompt 错误地发送给实际执行请求的备用模型。

## 详细执行步骤

1. 审查当前 `internal/app/app.go` 中处理器的 prompt 模型选择逻辑，确认 `PrimaryModel`、`routeFailoverAwareLLMClient` 与 `PromptManager` 的协作边界。
2. 设计兼容修复方案，满足以下约束：
   - 单路由场景保持现有 prompt 路由行为不变；
   - 多路由同提示词族场景继续使用对应 prompt；
   - 多路由异构提示词族场景避免继续把主模型 prompt 误送给备用模型。
3. 在运行时装配层实现修复，并补充必要的双语注释，说明为什么异构多路由需要退回到默认 prompt。
4. 新增或调整测试，覆盖：
   - 单路由仍使用主模型 prompt；
   - 多路由同提示词目录时仍保留主模型 prompt；
   - 多路由跨提示词目录时退回默认 prompt，避免错配。
5. 运行相关 Go 测试并做闭环验证，确认修复不会破坏现有多路由与 prompt 路由逻辑。

## 技术选型及验收策略

- 优先采用最小侵入修复，不重构处理器与 prompt 接口契约。
- 运行时通过“提示词目录是否一致”来判断是否可以继续复用主模型 prompt；若多路由会跨 prompt 目录，则统一退回 `default` prompt，避免模型专属 prompt 与实际执行模型错配。
- 修复必须以测试覆盖为准，不能仅依赖人工推断。

## 验收标准

1. `internal/app/app.go` 不再在异构多路由场景下固定使用主模型 prompt。
2. 单路由与同 prompt 目录的多路由场景保持兼容，不引入回归。
3. 至少相关单元测试通过，并补充针对本问题的回归测试。
4. 任务完成后，在本文末尾追加「执行变更总结」，再将文件迁移到 `docs/completed/`。

## 执行变更总结

### 1. 核心修复与调整概述

- 已修复多路由处理器统一固定主模型 prompt 的问题。
- 现在运行时会先判断全部 `llm.routes` 是否仍映射到同一提示词目录：
  - 如果仍属于同一 prompt 目录，则继续复用主模型 prompt；
  - 如果跨越不同 prompt 目录，则退回 `default` prompt，避免把主模型专属 prompt 错发给备用模型。
- 这样保留了多路由请求级容灾能力，同时消除了异构 prompt 目录下的提示词错配风险。

### 2. 📂文件变更清单

- 修改：`internal/app/app.go`
- 修改：`internal/app/app_test.go`
- 修改：`docs/plan/20260406-08-MULTI_ROUTE_PROCESSOR_PROMPT_ALIGNMENT_FIX.md`

### 3. 💻关键代码调整详情

- 在 `internal/app/app.go` 中新增 `promptFolderMatcher`、`selectProcessorPromptModel` 与 `allLLMRoutesSharePromptFolder`：
  - 通过 `PromptManager.MatchFolder` 判断多条路由是否共享同一 prompt 目录；
  - 多路由跨 prompt 目录时，返回空模型名，让处理器回退到 `default` prompt。
- `newApplication` 不再无条件使用 `cfg.LLM.PrimaryModel()` 作为处理器 prompt 模型，而是改为调用 `selectProcessorPromptModel`。
- 在 `internal/app/app_test.go` 中新增回归测试：
  - 验证同 prompt 目录的多路由仍保留主模型 prompt；
  - 验证跨 prompt 目录的多路由会退回默认 prompt。

### 4. ⚠️遗留问题与注意事项

- 本次修复没有改动多路由请求执行链路本身，只修正处理器 prompt 选择策略，因此单路由与同 prompt 目录多路由的现有行为保持不变。
- 当多路由跨越不同 prompt 目录时，当前策略是优先保证安全与一致性，统一退回 `default` prompt，而不是在一次请求生命周期内动态追踪“最终实际命中的 route”再重取 prompt。
- 已执行：
  - `go test ./internal/app ./internal/config ./internal/logic/processor ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/platform/textutil ./internal/platform/pii`
  - `go test ./...`
