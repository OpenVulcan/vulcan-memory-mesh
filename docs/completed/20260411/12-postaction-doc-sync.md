# 任务目标

针对本次 `postaction` 职责切割重构，补齐并同步仓库文档，确保文档不仅描述“现在怎么运行”，还清楚说明“为什么这样调整”。

本次文档同步需要覆盖以下目标：

1. 明确 `postaction L1 / 检索层 / hard dedupe / L2 / 持久化` 的新职责边界。
2. 说明为什么删除 `active_memory_nodes` 与顶层 `superseded_memory_ids`。
3. 说明为什么最终 supersede 集合改为只从 surviving `memory_nodes[].supersede_memory_ids` 推导。
4. 保证 `README.md` 与 `docs/post-action-guide_CN.md` 的叙述一致，不再保留旧链路表述。

# 实施步骤

## 1. 梳理需要同步的关键调整点

1. 梳理本次 `postaction` 实际代码行为
2. 提炼每个环节的调整原因
3. 找出当前文档中仍然缺少“为什么”说明的位置

## 2. 更新 README

1. 更新 `PostAction` 主流程描述
2. 补充职责切割后的简明说明
3. 保证高层说明不再混入旧 L1 去重语义

## 3. 更新 `docs/post-action-guide_CN.md`

1. 更新详细执行顺序
2. 为 L1、检索层、L2、持久化分别补充“调整原因”
3. 明确 supersede 的新来源与安全边界

## 4. 文档校对与归档

1. 检查 README 与详细文档是否一致
2. 在计划文件末尾补充执行变更总结
3. 完成后迁移到 `docs/completed/20260411/`

# 验收标准

1. 文档中不再出现与当前代码不一致的旧 `postaction` 职责描述
2. 文档明确解释：
   - 为什么 L1 不再看 `active_memory_nodes`
   - 为什么 dedupe / supersede 主责任下沉到检索层与 L2
   - 为什么最终 supersede 只从 surviving memory nodes 推导
3. `README.md` 与 `docs/post-action-guide_CN.md` 对同一链路的描述保持一致

# 执行变更总结

## 1. 核心修复与调整概述

1. 已在 `README.md` 中补充 `PostAction` 职责切割说明，明确 `L1`、检索层、hard dedupe、`L2` 与持久化各自负责什么。
2. 已在 `docs/post-action-guide_CN.md` 中新增专门的“本次职责切割调整说明”章节，解释每个环节为什么这样调整，而不是只罗列现在的行为。
3. 已同步更新两份文档中的链路描述，去掉旧的 `active_memory_nodes` / 顶层 `superseded_memory_ids` 表述，并说明当前采用的是“安全、渐进式收敛”策略。

## 2. 📂文件变更清单

### 修改

1. `README.md`
2. `docs/post-action-guide_CN.md`
3. `docs/completed/20260411/12-postaction-doc-sync.md`

### 新增

1. 无额外业务文档新增；本次主要是补充既有文档中的职责说明与调整原因。

### 删除

1. 无

## 3. 💻关键调整详情

1. `README.md` 现在不仅描述 `PostAction` 主流程，还补充了为什么：
   - `L1` 不再看 whole-session 的活跃旧记忆
   - `recent_grpc_memory_writes` 仍要保留
   - dedupe / supersede 主责任下沉到检索层与 `L2`
   - 最终 supersede 只从 surviving memory nodes 推导
2. `docs/post-action-guide_CN.md` 现在把这次调整拆成五个明确问题分别说明：
   - 为什么 `L1` 不再看 `active_memory_nodes`
   - 为什么 `recent_grpc_memory_writes` 仍然保留
   - 为什么记忆去重与 supersede 主责任下沉到检索层与 `L2`
   - 为什么最终 supersede 只从 surviving `memory_nodes[].supersede_memory_ids[]` 推导
   - 为什么当前策略不追求“一次性清光所有重复”
3. 文档中已经明确写出当前设计的取舍：系统追求的是“保守、安全、渐进式收敛”，而不是依赖一次 LLM 判定清理所有语义近重复。

## 4. ⚠️遗留问题与注意事项

1. 本次主要是文档同步，没有新增业务代码变更；代码侧仍以前一轮已通过验证的实现为准。
2. 当前工作区里仍保留未跟踪的过程文档 `08~11` 以及未跟踪产物 `vmm-local.exe`，本次没有处理。
