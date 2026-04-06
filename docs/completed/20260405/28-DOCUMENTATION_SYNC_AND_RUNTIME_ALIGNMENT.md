# 任务目标

本计划用于系统性修复当前仓库中 `README.md` 以及 `docs/` 目录下所有非计划类文档与实时实现不一致的内容，并根据当前主分支代码补齐缺失说明，确保文档真源重新与项目现状对齐。

本轮范围仅限当前有效文档，不处理 `docs/completed/` 中已归档历史计划的旧表述。

# 范围边界

## 纳入本轮

1. `README.md`
2. `docs/` 根目录下所有非 `plan/`、非 `completed/`、非日报目录的当前有效说明文档
3. 与以下实时实现直接相关的文档内容：
   - gRPC 接口契约
   - PreCheck / PostAction / ChatCompact 行为
   - retention / recycle / trash / vector GC 规则
   - profile 相关接口与生命周期
   - noise gate / pii / 规则加载
   - 构建、运行、配置与调试行为

## 不纳入本轮

1. `docs/completed/` 中的历史归档计划
2. `docs/plan/` 中的计划文件
3. 非当前主线真源的历史提案，只在它们被 README 或现行说明直接引用时做必要说明

# 详细执行步骤

## 阶段一：盘点当前文档与代码真源

1. 列出当前 `docs/` 目录中所有有效文档。
2. 以当前主分支代码为准，抽取文档真源：
   - gRPC proto
   - 组合根与运行时配置
   - PreCheck / PostAction / ChatCompact / retention 用例
   - profile / workspace / memory query 相关接口
3. 逐份文档建立“文档内容 -> 代码真源”映射，确认哪些是：
   - 已漂移；
   - 缺失；
   - 可保留但需要补充边界。

## 阶段二：批量修正文档漂移

1. 修正 README 中所有与当前实现不一致的接口、配置、运行时行为描述。
2. 修正 `docs/` 下各专题文档的陈旧口径，确保：
   - 接口字段名、返回结构、约束、默认值与当前代码一致；
   - 不再保留已废弃但未注明的旧行为；
   - 当前已落地的新行为有明确说明。
3. 对缺少关键上下文的文档补足：
   - 适用范围
   - 与其他链路的关系
   - 当前已知边界与不支持项

## 阶段三：一致性验证

1. 再次扫描文档中的高风险旧词和旧契约：
   - `query_json`
   - `background`
   - 过时的返回字段
   - 过时的配置项解释
   - 与当前 retention、profile、compact 逻辑冲突的描述
2. 对照代码复核 README 与各专题文档是否彼此一致。
3. 若在复核中发现真实代码与文档双向不一致的问题：
   - 先以当前代码真实行为为准修正文档；
   - 若确认代码本身存在明显缺陷，再单独新开计划处理，不在本轮混入代码改动。

## 阶段四：收口与归档

1. 在本计划末尾追加执行变更总结。
2. 将本计划迁移到 `docs/completed/`。

# 技术原则

1. 文档更新必须以当前主分支代码、proto 和配置校验逻辑为准，不以旧计划或旧提案为准。
2. 文档要优先说明“当前真实行为”，不是复述历史演进。
3. 对于当前保留的设计取舍，要明确写成边界，而不是让读者误解为遗漏。
4. 若某项内容属于已知债务但暂不处理，要在文档中给出最小必要说明，避免再次误判。

# 验收标准

1. `README.md` 与当前主分支实现一致。
2. `docs/` 下所有当前有效文档都已完成实时对齐。
3. 文档中不再保留明显过时的高风险旧契约和旧字段说明。
4. 当前关键链路：
   - gRPC
   - PreCheck
   - PostAction
   - retention
   - profile
   - 构建运行
   在文档层面的口径保持一致。
5. 本计划完成后迁移到 `docs/completed/`。

---

# 执行变更总结

## 1. 核心修复与调整概述

1. 修正了 `README.md` 中关于运行模式、存储后端和文档导航的漂移，去掉了已经失效的文档链接，并明确了 `split(SQLite + LanceDB)` 与 `combined(PostgreSQL)` 的当前口径。
2. 重写了 `docs/api-test-guide_CN.md`，补齐当前所有关键 gRPC 接口的 `grpcurl` 示例，移除了旧的 `ignoreCompactBoundary` 说明，并将 `PostAction` 的同步落库 + 异步提炼语义改为与现实现一致。
3. 重写了 `docs/hierarchy-grpc-design_CN.md`，将其从过时的旧表结构快照改为当前架构与存储模型总览，避免继续误导读者使用已不存在的 `memory_entries` 等旧概念。
4. 更新了 `docs/grpc-integration-guide_CN.md`、`docs/post-action-guide_CN.md`、`docs/noise-gate-guide_CN.md`、画像相关文档和未接线配置文档，使其在存储模式、接口边界和术语表述上与当前代码真源保持一致。
5. 为 `docs/GRPC_REVIEW_VALIDATION_AND_FIX_CN.md` 增加了“历史审阅记录”提示，避免把历史报告误读为现行规范。

## 2. 📂文件变更清单

### 修改

1. `README.md`
2. `docs/api-test-guide_CN.md`
3. `docs/grpc-integration-guide_CN.md`
4. `docs/hierarchy-grpc-design_CN.md`
5. `docs/post-action-guide_CN.md`
6. `docs/noise-gate-guide_CN.md`
7. `docs/profile-grpc-interfaces_CN.md`
8. `docs/profile-node-lifecycle_CN.md`
9. `docs/unused-config-parameters_CN.md`
10. `docs/GRPC_REVIEW_VALIDATION_AND_FIX_CN.md`
11. `docs/plan/20260405-28-DOCUMENTATION_SYNC_AND_RUNTIME_ALIGNMENT.md`

### 新增

无。

### 删除

无独立删除项；原有文档均保留在当前位置，仅进行了内容重写或修订。

## 3. 💻关键代码调整详情

本轮未修改业务代码或配置代码，全部变更都集中在文档层。

关键文档调整点如下：

1. `README.md`
   - 修正了运行模式说明，明确 `split` 与 `combined` 的当前支持关系。
   - 移除了失效的 `memory-extraction-analysis_CN.md` 链接。
2. `docs/api-test-guide_CN.md`
   - 补齐 `GetProfileNodes`、`GetProfileBundle`、`ApplyProfileInstruction`、`SearchMemoryEvents`、`GetTurnDetails`、`WriteMemories` 的测试示例。
   - 删除旧的 `ignoreCompactBoundary` 说明，统一改成当前 `recallMode` 语义。
   - 更正 `PostAction` 为“同步持久化 turn 并异步提炼”。
3. `docs/hierarchy-grpc-design_CN.md`
   - 删除旧的 `vmm_memory_entries`、旧 SQLite 表结构快照和过时流程描述。
   - 改为当前层级模型、逻辑实体、gRPC 面和 retention 链路总览。
4. 多份专题文档
   - 把“默认写入 SQLite”这类容易误读为唯一运行模式的说法，收敛为“写入当前启用的关系库存储”，仅在确属 `split` 专属行为时保留 `SQLite` 条件限定。

## 4. ⚠️遗留问题与注意事项

1. `docs/daily-reports/` 和 `docs/completed/` 下的历史性文档没有纳入当前口径重写，它们继续作为历史记录保留。
2. 本轮没有新增或修改代码，因此未运行 `go test ./...`；改动前后已通过：
   - 断链扫描
   - 旧字段/旧术语全文检索
   - `git diff --check`
3. 后续如果新增接口或运行模式差异，应优先同步：
   - `README.md`
   - `docs/grpc-integration-guide_CN.md`
   - `docs/api-test-guide_CN.md`
   - `docs/post-action-guide_CN.md`
