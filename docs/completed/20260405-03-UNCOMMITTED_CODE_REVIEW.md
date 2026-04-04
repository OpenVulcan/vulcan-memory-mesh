# 任务目标

对当前工作区全部未提交代码执行一次系统化自审，覆盖：

1. 已修改但未提交的源码、配置、文档与测试文件；
2. 未跟踪但已加入本轮工作流的计划/完成文件；
3. 跨文件联动的行为回归、生命周期冲突、存储一致性问题与测试覆盖缺口。

本轮输出目标不是重复变更总结，而是识别真实缺陷、风险冲突和仍未验证的边界。

# 详细执行步骤

1. 收集当前 `git status` 与 `git diff`，明确未提交文件集合。
2. 以业务链路为主线分组审查：
   - `post-action / analyze_turn / unified reviewer`
   - `WriteMemories / 语义替代 / 原子持久化`
   - `SQLite / PostgreSQL` 存储实现一致性
   - 配置与组合根接线
   - 文档与测试是否与实现同步
3. 对每组变更重点检查：
   - 是否存在逻辑错误、遗漏路径、回归风险
   - 是否违反现有生命周期算法或存储契约
   - 是否存在测试未覆盖的高风险分支
4. 输出按严重度排序的 findings，附带文件与行号。
5. 在计划末尾追加执行变更总结，并迁移到 `docs/completed/`。

# 技术选型

1. 评审基线以当前工作区相对 `HEAD` 的未提交状态为准，不只看本轮最后修改的文件。
2. 结论优先基于源码和测试实际行为，不以计划或口头约定替代代码事实。
3. Findings 以“真实 bug / 风险 / 缺口”为主，不把纯风格建议混入主结论。

# 验收标准

1. 完整覆盖当前未提交文件集合，不遗漏未跟踪但已纳入工作流的关键文件。
2. 输出至少包含：
   - 明确的发现项
   - 对应文件与行号
   - 风险解释
3. 若未发现阻断级问题，也要明确说明剩余风险与测试盲区。

# 执行变更总结

## 1. 核心修复与调整概述

1. 本次未直接修改业务实现代码，工作内容为对当前全部未提交变更执行系统化自审并收敛真实 findings。
2. 已完成对 `post-action` 记忆替代链路、`WriteMemories` 语义替代链路、SQLite/PostgreSQL 持久化实现、配置接线以及文档说明的一轮交叉审查。
3. 已额外执行 `go test ./...`，确认当前未提交代码在现有测试集下全部通过。
4. 自审最终收敛出 3 个需要优先处理的风险点：`WriteMemories` 的 dropped-candidate 误判去重、语义去重目标的并发失稳、direct-write 路径对 legacy reviewer 结果的兼容回归。

## 2. 📂文件变更清单

### 修改

1. `docs/plan/20260405-03-UNCOMMITTED_CODE_REVIEW.md`

### 新增

1. 无

### 删除

1. 无

## 3. 💻关键代码调整详情

1. 无业务代码改动；本轮仅完成代码审查、测试复核与风险归纳。
2. 自审重点覆盖了以下关键方法与链路：
   - `internal/app/usecase/memory_query.go` 中 `Write`、`buildDirectWriteMemoryDecisions`、`loadDirectWriteDedupedExistingRows`
   - `internal/app/usecase/postaction_candidate_review.go` 中 reviewer 结果合并与 `similar_memories` 回贴逻辑
   - `internal/logic/processor/postaction_candidate_reviewer.go` 中 memory reviewer 结果兼容解析逻辑
   - `internal/adapters/outbound/vldb_sqlite/store.go` 与 `internal/adapters/outbound/vldb_postgres/memory_store.go` 中 direct-write 持久化相关实现
   - `internal/config/config.go` 中新增 retention 与 memory replace 配置的归一化/校验逻辑

## 4. ⚠️遗留问题与注意事项

1. 现有测试全部通过，但测试通过不代表上述 3 个逻辑风险已被覆盖；尤其并发时序与 reviewer 语义落差问题仍需实现侧修正。
2. 当前 `retention` 仍主要处于配置与 schema 入口阶段，未形成完整回收 worker；阅读文档时需注意其“已接入配置”不等于“已完整落地”。
3. 本计划文件在输出评审结果后应迁移到 `docs/completed/`，保持日期序号前缀不变。
