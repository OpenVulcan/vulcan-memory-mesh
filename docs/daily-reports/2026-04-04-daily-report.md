# 日报 - 2026-04-04

## 总结

2026 年 4 月 4 日，仓库共完成并归档了 18 份计划，工作重点集中在三个方向：`PreCheck` 与 gRPC 契约收敛、PostgreSQL 组合存储能力落地与稳定性修复，以及 `PostAction` 噪音压缩与统一评审可靠性增强。此外，当天还完成了 PostgreSQL `combined sql` 混合检索分数语义排查，以及一次真实 PostgreSQL combined 模式的连接与集成验证。

整体来看，这批工作一方面持续推进了 OSS 本地版从 `split` 模式向可用的 PostgreSQL 组合存储路径演进，另一方面也进一步收紧了记忆召回边界、减少了重复与噪音入库，并简化了最终返回给上游 AI 的上下文载荷。

## 主要产出

### 1. PreCheck 与 gRPC 召回链路进一步收敛

- 新增 `ChatCompact` 能力，并将 session compact 边界持久化，使 `PreCheck` 能基于最近 compact 的 turn 控制召回范围，而不是默认重新召回当前 session 全量内容。
- 将原先布尔语义的 compact 边界控制升级为枚举型 `recall_mode`，在保留兼容性的同时，为后续召回模式扩展留出更安全的协议空间。
- 修复了 SQLite 增量迁移、session compact 加锁顺序、向量表重建回灌过滤等一致性问题。
- 精简了 `PreCheck` 的最终 gRPC 返回载荷，使上游优先依赖 `turn_id` 与 `has_dialogue`，不再依赖已弱化的固定文本字段。
- 在 reviewer 选中之后新增基于 `turn_id` 的最终去重，避免同一来源对话被重复返回给上游 AI。

### 2. PostgreSQL 组合存储方案完成落地并持续加固

- 明确了组合模式统一走 `postgres` provider，并通过 `flavor` 路由 `paradedb` 与 `standard` 两套词法检索实现，不再对外暴露割裂的 provider 设计。
- 补齐了 PostgreSQL combined 模式下的主运行时能力，包括 turn 持久化、近期历史读取、记忆采纳、画像链路、工作区管理和向量检索等关键链路。
- 连续修复了多轮 review 暴露出的事务一致性、并发安全、画像 supersede、冲突翻译、配置归一化、启动补种、画像目标解析等问题。
- 新增 `debug-clean postgres` 与 `debug-migrate split-to-combined` 调试维护能力，迁移严格以 SQLite 为事实主源，不重新引入 PostgreSQL 侧的 `vector_json` 冗余存储。
- 补齐了 PostgreSQL 配置示例、schema version 启动写回与校验逻辑，并通过自动化测试和真实连接冒烟验证确认 combined 路径可用。

### 3. PostAction 噪音压缩与持久化质量明显提升

- 为 `analyze_turn` 输出新增 `user_input_kind`、`evidence_source`、`admission`、`admission_reason` 等结构化字段，用于区分真正可沉淀的用户事实与问答型回显内容。
- 在 `PostAction` 中增加首轮准入过滤，优先拦截记忆回显、画像回显、通识回答以及 `non_durable` 的临时外部状态，降低噪音入库概率。
- 引入统一 `review_postaction_candidates` reviewer，在一次模型调用中同时处理 memory 判重与 profile 准入，减少分裂判断。
- 复用与 `PreCheck` 对等的共享作用域完成 post-action 记忆判重，并新增 `raw_candidates`、`final_stored_nodes`、`compaction_rate` 等可观测字段。
- 修复统一 reviewer 重构带来的回归问题，使评审失败时仍可优雅降级，同时保证 `invalid` 画像节点仍能进入关系库存档以保留审计链。

### 4. 混合检索分数语义变得更一致

- 针对 PostgreSQL `combined sql` 一阶段检索日志中“分数看起来异常偏低”的现象完成排查，确认根因是原始 RRF 排序值被直接当成了对外分数，而不是真正的命中质量异常。
- 在统一检索用例中补充了该路径的分数归一化逻辑，同时保留 `raw_score` 作为诊断字段，使日志展示和后续阈值判断更加可解释。

## 验证情况

- 多个任务分别执行了针对 `grpcapi`、`internal/app/usecase`、`internal/logic/processor`、`internal/platform/textutil`、`internal/platform/pii`、`internal/config` 以及 PostgreSQL/SQLite 适配层的定向测试。
- 当天多项任务明确执行并通过了 `go test ./...` 全量回归。
- 在 PostAction 统一 reviewer 与 prompt 变更任务中，额外执行了 `.\make.ps1 build`。
- 对 PostgreSQL combined 模式完成了真实启动、端口探测、数据库读取与 schema/version 行为验证，同时确认当前默认运行模式仍为 `split`，只有显式切换后才会进入 `combined` 路径。

## 文档与使用体验更新

- 更新了与 gRPC 集成、层级行为、接口测试、post-action 行为以及 README 使用说明相关的文档。
- 为本地配置模板补充了 PostgreSQL DSN 示例。
- 同步更新了新 unified post-action reviewer 依赖的 prompt 与 prompt 要求。

## 后续关注点

- PostgreSQL schema version 当前已经具备启动期校验与 fail-fast 行为，但完整的 PostgreSQL migration runner 仍未实现。
- `ChatCompact` 边界过滤目前主要作用于 `PreCheck` 链路，尚未推广到所有通用搜索接口。
- `review_postaction_candidates` 已成为 post-action 主路径，若 prompt bundle 缺失，当前设计会直接启动失败，而不是静默降级。

## 覆盖的归档计划

1. `20260404-01-GRPC_CHATCOMPACT_PRECHECK_FILTER`
2. `20260404-02-PRECHECK_RECALL_MODE_ENUM`
3. `20260404-03-REVIEW_FINDINGS_FIXES`
4. `20260404-04-POSTGRES_DIALECT_COMBINED_STORAGE_PLAN`
5. `20260404-05-POSTGRES_COMBINED_RUNTIME_FIXES`
6. `20260404-06-POSTGRES_REVIEW_FINDINGS_FIX`
7. `20260404-07-POSTGRES_DEBUG_SEED_BOOTSTRAP_FIX`
8. `20260404-08-POSTGRES_REVIEW_FINDINGS_ALL_FIX`
9. `20260404-09-POSTGRES_PROFILE_TARGET_REVIEW_FIX`
10. `20260404-10-POSTGRES_UNFINISHED_WORK_CONTINUATION`
11. `20260404-11-CONFIG_EXAMPLE_POSTGRES_DSN`
12. `20260404-12-POSTGRES_CONNECTION_AND_INTEGRATION_TEST`
13. `20260404-13-POSTGRES_SCHEMA_VERSION_BOOTSTRAP_FIX`
14. `20260404-14-POSTACTION_QA_GUARD_AND_BATCH_MEMORY_DEDUP`
15. `20260404-15-POSTACTION_UNIFIED_REVIEWER_REGRESSION_FIX`
16. `20260404-16-HYBRID_SEARCH_SCORE_INVESTIGATION`
17. `20260404-17-PRECHECK_CONTEXT_TURN_ID_ALIGNMENT`
18. `20260404-18-PRECHECK_TURN_ID_DEDUP`
