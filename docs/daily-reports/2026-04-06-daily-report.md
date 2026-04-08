# 日报 - 2026-04-06

## 总结

2026 年 4 月 6 日，仓库共完成并归档了 22 份计划，工作重心从前一天的 key failover 基础能力继续向前推进，逐步演化为“节点级预算预判、多 route 配置收束、主配置 YAML 化、PII 能力恢复与前置脱敏接入，以及新增模型供应商支持”的组合推进日。

当天最明显的变化有两条：一条是 AI 路由与配置体系从“单 key 容灾”继续升级到“节点级额度治理 + 多 route provider 感知 + YAML 分层配置”；另一条是 PII 能力从规则、求值器、测试器到前置脱敏接线被系统性补回，并进一步影响了 PreCheck、PostAction 与 WriteMemories 的输入处理边界。

## 主要产出

### 1. AI 路由、节点预算与多 route 容灾进一步成型

- 为 `llm / embedding / rerank` 新增节点级 `RPM / TPM / RPD` 配额，运行时在真正发请求前就能做额度预判与提前切换。
- 明确节点语义是“同一固定模型下的额度组”，不是跨 provider 或跨 model 的另一套 route 体系，避免配置概念继续混淆。
- 推进了 LLM、Rerank 的多 provider / 多 route failover 语义，完善模型 failover、route priority、400 错误分类与 prompt 对齐等细节。
- 将原先分散在 legacy 字段和 route 字段中的 AI 配置进一步收敛为 route-only 结构，减少同义配置并存带来的歧义。

### 2. 主配置体系完成 YAML 分层迁移

- 把正式运行时配置从 JSON 统一切换到 YAML，建立 `base.yaml -> config.yaml -> 用户侧 config.yaml` 的分层加载链路。
- 将独立的 `prompts-routes.json` 合并进主配置树中的 `prompts.routes`，使提示词路由与主配置不再分离漂移。
- 保持 `noise_rules` 与 `pii_rules` 的独立目录结构与优先级不变，同时补齐默认配置、示例文件、测试夹具与 README 说明。
- 标准构建验证同步通过，确认新的打包产物目录结构仍与仓库约束一致。

### 3. PII 核心能力恢复并前移到主链路

- 从历史实现中系统性补回 PII 规则加载、条件求值器、原子校验函数、共享规则包、多地区规则、独立测试器和配套文档。
- 将 PII 前置脱敏能力真正接入 PreCheck 与 PostAction 主链，使进入标准化和持久化流程前的文本能够更早完成红线处理。
- 修复 PostAction 重复脱敏、WriteMemories 脱敏与日志收紧等问题，减少重复处理和敏感内容泄露风险。
- 同步更新 `base.yaml` 与配置帮助，使 PII 行为和配置说明与当前运行时保持一致。

### 4. 新 provider 能力继续扩展

- 新增 Google AI Studio 原生 `LLM + Embedding` 适配器，使用官方 `google.golang.org/genai` SDK 接入。
- 对 Google provider 的 key failover 与配置校验做了 review 修复，使其能安全融入现有 failover 体系。
- 新增 SiliconFlow rerank provider，并将 rerank failover 从 DashScope 固定实现抽象为 provider-aware 实现。
- 持续完善示例配置、设计文档和 README，消除“只支持单一 provider”的旧描述。

### 5. 向量重建维护工具能力补齐

- 新增 `vmm-migrate` 维护工具产物，补齐 vector rebuild 迁移入口。
- 修复 `make.ps1` 在 Windows PowerShell 下自动切换到 `pwsh` 的兼容问题，保证 `.\make.ps1 build` 与 `.\make.bat build` 都能构建维护工具。
- 为后续向量重建和离线维护链路奠定了正式交付入口。

## 验证情况

- 多个任务执行了仓库要求的最小测试集以及 `go test ./...` 全量回归。
- 配置、AI 路由、多 provider failover、PII 与脱敏链路均补充了定向测试。
- 当天至少完成了以下构建验证：
  - `.\make.bat build`
  - `powershell -ExecutionPolicy Bypass -File .\make.ps1 build`
  - `.\make.ps1 build`
- YAML 配置迁移、Google AI Studio 接入、vector rebuild 维护工具等关键任务均明确完成构建或全量回归验证。

## 后续关注点

- 节点级预算预判和 route-only 配置已经建立，但后续若继续扩展更多 provider，仍需要持续关注配置可读性和错误分类边界。
- PII 规则与测试器能力已经恢复，但当前主线重点仍是规则引擎和前置红线处理，未重新引入历史 HTTP/TLS 等旧运行时路径。
- Google AI Studio 当前覆盖 `LLM + Embedding`，rerank 仍未纳入 Google provider 范围。

## 覆盖的归档计划

1. `20260406-01-AI_NODE_RATE_LIMIT_PRECHECK_AND_FAILOVER`
2. `20260406-02-AI_NODE_KEY_LEVEL_BUDGET_ALIGNMENT`
3. `20260406-03-LLM_RERANK_MULTI_PROVIDER_MODEL_FAILOVER`
4. `20260406-04-LLM_RERANK_ROUTE_PRIORITY`
5. `20260406-05-REVIEW_FINDINGS_FIX`
6. `20260406-06-AI_ROUTE_ONLY_CONFIGURATION_REFACTOR`
7. `20260406-07-MULTI_ROUTE_FAILOVER_REVIEW_FIX`
8. `20260406-08-MULTI_ROUTE_PROCESSOR_PROMPT_ALIGNMENT_FIX`
9. `20260406-09-MULTI_ROUTE_400_FAILOVER_FIX`
10. `20260406-10-CONFIG_YAML_LAYERED_MIGRATION`
11. `20260406-11-REVIEW_FINDINGS_FIX`
12. `20260406-12-PII_CORE_CAPABILITIES_RESTORATION`
13. `20260406-13-REVIEW_FINDINGS_FIX_AND_BUILD_VERIFICATION`
14. `20260406-14-PRECHECK_POSTACTION_PII_EARLY_REDACTION_INTEGRATION`
15. `20260406-15-POSTACTION_DUPLICATE_REDACTION_FIX`
16. `20260406-16-WRITE_MEMORIES_REDACTION_AND_PII_LOGGING_TIGHTENING`
17. `20260406-17-BASE_YAML_DOC_INLINE_MERGE`
18. `20260406-18-CONFIG_OVERRIDE_HELP_INLINE_MERGE`
19. `20260406-19-GOOGLE_AI_STUDIO_NATIVE_ADAPTER`
20. `20260406-20-GOOGLE_AI_STUDIO_FAILOVER_REVIEW_FIX`
21. `20260406-21-SILICONFLOW_RERANK_PROVIDER_INTEGRATION`
22. `20260406-22-VECTOR_REBUILD_MIGRATION_TOOL`
