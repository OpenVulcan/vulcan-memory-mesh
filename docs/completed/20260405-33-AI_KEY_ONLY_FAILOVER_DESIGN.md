# AI Key 级容灾设计收敛计划

## 任务目标

基于当前仓库已有的 AI 容灾设计文档，收敛并修正方案边界，明确以下新原则：

1. 取消多供应商容灾与多模型容灾。
2. 仅保留同一服务配置下的多 API Key 轮换与暂时停用能力。
3. 强调 Embedding / 向量检索链路不得混用不同向量模型，即使维度一致也不能视为可安全混用。
4. 让文档最终可直接指导后续实现，不产生“可以切模型/切 provider”的误导。

## 详细执行步骤

1. 重新审阅已有设计文档中的范围定义、配置结构、错误分类、轮询策略与实施顺序。
2. 删除或改写所有“多 provider / 多 route / 多 model failover”相关内容，统一改为“单配置多 key”。
3. 在文档中补充向量语义安全约束，明确：
   - Embedding 模型必须固定；
   - 不允许为容灾目的切换到另一套 embedding 模型；
   - 向量维度相同不是充分条件，语义空间一致才是前提，而运行时无法证明跨模型语义空间等价，因此禁止混用。
4. 重新设计配置结构，保留单一 `provider + endpoint + model`，仅把 `api_key` 扩展为 `api_keys` 或支持分隔字符串。
5. 重新设计运行时状态机，改成：
   - 单服务配置；
   - 多 key 选择；
   - key 冷却；
   - 冷却后恢复重试；
   - 进程重启后状态清空。
6. 重新整理错误分类与动作矩阵，确保动作只落到 key 级别，而不是 route / model 级别。
7. 检查文档中的实施顺序、代码落点、测试建议是否仍与“key-only failover”一致。
8. 完成后逐项自检，并在文末追加执行变更总结。
9. 自检通过后，将本计划文件迁移到 `docs/completed/`。

## 技术选型

### 设计边界

- 采用“单模型单 provider 单 endpoint，多 key 容灾”的收敛方案。
- 不引入多 route 调度器，不引入多模型池，不引入多 provider 池。

### 配置策略

- 保留当前 `llm / embedding / rerank` 三段配置结构。
- 在每段配置中扩展 `api_keys`。
- 兼容现有 `api_key` 单值写法，并支持通过分隔符拆分为多个 key。

### 运行时策略

- 在端口实现层前新增 key 轮换包装器。
- 包装器仅做：
  - key 选择
  - key 暂停
  - key 恢复
  - 日志与错误分类
- 不改变上层 `LLMClient / EmbeddingClient / RerankerClient` 接口。

### 风险控制策略

- Embedding 与检索链路严禁跨模型混向量。
- 对 400 类请求错误不做 key 轮换，避免把坏请求广播到全部 key。
- 对限流、配额、鉴权等错误，允许切到同模型下一个 key。

## 验收标准

1. 已有设计文档被修订为“仅多 key 容灾”，不再出现推荐性的多 provider / 多 model failover 方案。
2. 文档中明确写出向量维度一致不代表向量语义一致，禁止混用 embedding 模型。
3. 文档中给出新的配置建议、运行时状态设计、错误分类和测试建议。
4. 文档内容能直接映射到当前代码结构：
   - `internal/config/config.go`
   - `internal/app/app.go`
   - `internal/adapters/outbound/openai_native/*`
   - `internal/adapters/outbound/dashscope_rerank/client.go`
5. 本计划文件末尾已追加执行变更总结，并在完成后归档到 `docs/completed/`。

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已将 AI 容灾设计从“多 provider / 多 model / 多 route”收敛为“固定服务配置下的多 API Key 容灾”。
- 已明确写出 embedding 检索链路禁止混用不同模型，即使向量维度一致也不能视为安全。
- 已把错误动作从 route 级切换调整为 key 级切换，并补充了“公共故障默认不切 key”的保守策略。
- 已同步重写实施顺序、配置结构、状态机和测试建议，使其与 key-only failover 一致。

### 2. 📂文件变更清单

- 新增并归档：`docs/completed/20260405-33-AI_KEY_ONLY_FAILOVER_DESIGN.md`
- 修改：`docs/ai-model-failover-design_CN.md`

### 3. 💻关键代码调整详情

- 本次未修改运行时代码，变更集中在设计文档与计划文档。
- 在设计文档中取消了 `routes / routeState / 多 provider failover / 多 model failover` 的推荐实现方向。
- 在设计文档中新增了 `api_keys + key_failover + keyState` 的收敛方案，并重新定义了错误分类矩阵与默认动作。

### 4. ⚠️遗留问题与注意事项

- 当前仓库尚未落地 key-only failover 实现，本次仅完成设计收敛与计划同步。
- 若后续进入实现阶段，建议优先从 `rerank` 开始，再扩展到 `embedding` 与 `llm`。
- 当前未运行测试，因为本轮没有修改代码实现，仅调整文档与计划。
