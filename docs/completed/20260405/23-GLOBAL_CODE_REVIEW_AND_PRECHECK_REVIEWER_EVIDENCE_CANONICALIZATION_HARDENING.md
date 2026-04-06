# 任务目标

本轮任务聚焦一次全局代码审核后发现的 pre-check 第二层 reviewer 证据规范化缺口。当前 usecase 层已经会在候选合并时把 `matched_context_values` 折叠到统一的 `key=value` 规范表面，但 processor 层在渲染 reviewer 请求时仍只做字面裁剪与精确去重，导致大小写、连字符、下划线、空白等格式变体有机会重新进入模型输入。需要把这部分规范化能力下沉为共享 helper，并让 usecase 与 processor 共用同一套规则，保证检索解释、候选合并与 reviewer 请求的证据表面完全一致。

# 详细执行步骤

1. 梳理当前 `matched_context_values` 在 query 命中、pre-check 候选合并、reviewer 请求渲染三条链路中的规范化逻辑，确认现有局部 helper 的行为边界。
2. 在 `internal/logic/domain` 提供共享的 context evidence 标签规范化 helper，统一 `key=value` 表面的生成规则，并补齐中英文注释。
3. 调整 `internal/app/usecase/memory_query.go` 与 `internal/app/usecase/precheck_candidate_merge.go`，改为复用共享 helper，避免 usecase 层继续持有局部实现。
4. 调整 `internal/logic/processor/precheck_memory_reviewer.go`，让 `MatchedContextValues` 在 reviewer 请求渲染阶段也走同一套 canonical 去重逻辑，而普通字符串数组仍保留原有字面去重语义。
5. 补充 domain 与 processor 侧回归测试，覆盖格式变体折叠、canonical 输出稳定性，以及 reviewer 请求中重复语义证据不会重复出现的场景。
6. 运行定向测试、仓库规定的最小测试集、`go test ./...` 与 `go vet ./...`，确保没有回归。

# 技术选型与实现约束

- 共享 helper 放在 `internal/logic/domain`，因为它表达的是长期记忆 context evidence 的领域级规范，而不是某条具体 usecase 的局部显示逻辑。
- helper 只依赖标准库与现有 domain 归一化函数，不引入新的平台层依赖，避免打乱现有依赖方向。
- processor 层只对 `MatchedContextValues` 使用 canonical 规范化，`SearchQueries` 等普通字段仍维持原有顺序保留与字面去重，避免无关字段被过度处理。
- 若发现现有测试覆盖不足，以新增回归测试优先，而不是通过放宽断言掩盖行为变化。

# 验收标准

- 对同一语义的 `matched_context_values` 格式变体，例如 `deployment_mode=LOCAL_OSS`、`deployment mode = local-oss`、`deployment_mode= local oss`，最终只保留一条稳定的 canonical 标签。
- pre-check 候选合并、reviewer 请求渲染、query 命中解释三处都复用同一套共享规范化逻辑，不再存在局部实现漂移。
- 所有新增或修改的函数、关键逻辑块都符合仓库要求的中英文双语注释规范。
- 相关定向测试、最小测试集、`go test ./...` 与 `go vet ./...` 全部通过。

---

# 执行变更总结

## 1. 核心修复与调整概述

- 将 context evidence 标签的 canonical 归一化能力下沉到 `internal/logic/domain`，新增共享 helper `NormalizeMemoryContextEvidenceLabel`。
- 统一 `memory_query`、`precheck_candidate_merge` 与 `precheck_memory_reviewer` 三条链路的 `matched_context_values` 规范化入口，消除 processor 层仅做字面去重导致的格式漂移回流问题。
- 补充 domain 与 processor 侧回归测试，验证格式变体会在 reviewer 请求前折叠为稳定的 `key=value` canonical 表面。

## 2. 📂文件变更清单

### 新增文件

- `docs/completed/20260405-23-GLOBAL_CODE_REVIEW_AND_PRECHECK_REVIEWER_EVIDENCE_CANONICALIZATION_HARDENING.md`

### 修改文件

- `internal/logic/domain/memory.go`
- `internal/logic/domain/memory_test.go`
- `internal/app/usecase/memory_query.go`
- `internal/app/usecase/precheck_candidate_merge.go`
- `internal/logic/processor/precheck_memory_reviewer.go`
- `internal/logic/processor/precheck_memory_reviewer_test.go`

### 删除文件

- 无

## 3. 💻关键代码调整详情

- 在领域层新增 `NormalizeMemoryContextEvidenceLabel`，把已渲染 evidence 标签重新收敛到统一的 `key=value` 规范表面，并复用现有 `NormalizeMemoryContextKey/Value` 规则。
- 删除 `memory_query.go` 内部的局部 evidence 规范化实现，改为调用领域层共享 helper，避免 query 链路与其他调用方继续各自演化。
- 调整 `precheck_candidate_merge.go`，使重复候选合并阶段也复用共享 helper，保持 reviewer-facing `MatchedContextValues` 的排序与去重建立在 canonical 表面上。
- 在 `precheck_memory_reviewer.go` 中新增 `normalizePreCheckMatchedContextValues`，只对 reviewer 载荷里的 `MatchedContextValues` 做 canonical 去重，保持 `SearchQueries` 等普通字符串字段仍使用原有字面去重逻辑。
- 新增测试覆盖：
  - domain 层验证 context evidence 标签 canonical 化行为。
  - processor 层验证 reviewer 请求渲染会把格式变体折叠为一条稳定 canonical 证据。

## 4. ⚠️遗留问题与注意事项

- 本轮修复只针对 reviewer 请求渲染边界的证据规范化缺口，没有改变检索排序、候选阈值或 reviewer 选择策略。
- 当前 `matched_context_values` 的 canonical 表面仍是 `key=value` 文本标签；若未来需要结构化输出给模型，应基于该共享 helper 再演进，而不是重新散落局部实现。
- 已完成验证：
  - `go test ./internal/logic/domain ./internal/logic/processor ./internal/app/usecase -run "Test(NormalizeMemoryContextEvidenceLabel|RenderPreCheckMemoryReviewRequestPreservesMatchedContextEvidence|PreCheckExecuteMergesEvidenceAcrossRepeatedMemoryHits|PreCheckExecuteNormalizesEquivalentMatchedEvidenceLabels|MemoryUseCaseSearch)"`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `go test ./...`
  - `go vet ./...`
