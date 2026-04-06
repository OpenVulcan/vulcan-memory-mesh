# 任务目标

本计划用于对已归档的 `20260405-26-RETENTION_REMAINING_ISSUES_REMEDIATION.md` 做一次后验复核，确认其中是否还存在“计划文字已宣告完成，但实际实现或最终文档口径并未真正对齐”的残留项，并将确认存在的问题继续整改收口。

本轮重点不再扩展新功能，而是做“已完成计划的真实性校验与收尾修正”。

# 复核范围

1. `docs/completed/20260405-26-RETENTION_REMAINING_ISSUES_REMEDIATION.md`
2. `docs/completed/20260404-19-SESSION_RECYCLE_BIN_AND_RETENTION_PLAN.md`
3. 当前 retention 真实实现：
   - `internal/app/usecase/retention.go`
   - `internal/adapters/outbound/vldb_postgres/recycle_jobs.go`
   - `internal/adapters/outbound/vldb_sqlite/recycle_jobs.go`
   - `internal/app/app.go`
4. 当前对外文档真源：
   - `README.md`
   - `docs/retention-governance-guide_CN.md`

# 已知需要重点确认的疑点

1. 历史计划与已归档 plan26 中，仍保留“recycle batch 显式批次 claim”的表述；但当前正式实现实际采用的是 `recycle_jobs` 队列完成冷 `turn` 的 scan / claim / execute 分离。
2. 历史总控计划中，热窗口曾写为 `max(pre_check.history_turns, post_action.session_analysis_history_turns) + turn_keep_extra_turns`；而当前真实实现与正式文档口径已经收敛为共享 `post_action.session_analysis_history_turns + turn_keep_extra_turns`。
3. 需要确认上述差异到底是“真实未完成”，还是“实现方案已变化但归档文档尚未补充后验说明”。

# 详细执行步骤

## 阶段一：后验复核

1. 逐项核对 plan26 的目标、验收标准、执行总结与当前代码实现。
2. 对照旧总控计划，区分：
   - 真实未完成；
   - 已通过不同实现方式完成；
   - 仅剩历史文档未补记说明。
3. 对确认存在的残留问题做结论化判断，避免再次把旧设计草案误读成当前缺陷。

## 阶段二：整改收口

1. 若确认只是“实现方案已变化但归档文档没有补记”，则直接修正归档文档：
   - 给 plan26 增加后验复核补记；
   - 给旧总控计划增加后验复核补记；
   - 让历史文档明确指出当前正式实现与历史设计草案的差异。
2. 若确认存在真实实现缺口，则继续补代码、补测试、补文档，直到与计划目标闭环。
3. 若确认没有真实实现缺口，则明确写出“无需继续改代码”的理由，避免后续重复追踪。

## 阶段三：验证与归档

1. 再次检查：
   - 已归档计划不再保留会误导后续判断的未校正表述；
   - 当前真源文档与当前代码实现保持一致；
   - 本轮结论能清晰回答“plan26 是否真的完成”。
2. 在本计划末尾补齐执行变更总结。
3. 将本计划迁移到 `docs/completed/`。

# 技术原则

1. 后验复核以当前主分支代码和当前真源文档为准，不以历史计划草案为准。
2. 如果差异已经通过更优实现方式解决，应修正文档认知，而不是为了“对齐旧计划措辞”反向改坏代码。
3. 只有确认存在真实运行时缺口时，才继续动代码。

# 验收标准

1. 能明确回答 plan26 是否还存在真实未完成项。
2. 若无真实缺口，则所有残留矛盾都已通过归档文档补记收口。
3. 若有真实缺口，则该缺口已被补齐并验证。
4. 本计划完成后迁移到 `docs/completed/`。

---

# 执行变更总结

## 1. 核心修复与调整概述

本轮对已归档的 plan26 做了后验复核，结论是：

1. 当前没有再发现新的运行时代码缺口，plan26 的主要工程目标已经真实落地。
2. 本轮确认剩余问题并不是“代码没做完”，而是“历史计划文案仍保留旧设计口径，容易让后续审查把旧草案误读成当前未完成项”。
3. 因此本轮整改没有继续改业务代码，而是把后验结论补写回历史归档文档和 retention 真源说明中，避免再次把：
   - `recycle batch claim`
   - `max(pre_check.history_turns, post_action.session_analysis_history_turns)`
   这两类旧表述当成当前真实缺口。

## 2. 📂文件变更清单

### 修改

1. `docs/retention-governance-guide_CN.md`
2. `docs/completed/20260405-26-RETENTION_REMAINING_ISSUES_REMEDIATION.md`
3. `docs/completed/20260404-19-SESSION_RECYCLE_BIN_AND_RETENTION_PLAN.md`

### 迁移 / 归档

1. `docs/plan/20260405-27-PLAN26_COMPLETION_VERIFICATION_AND_REMEDIATION.md` -> `docs/completed/20260405-27-PLAN26_COMPLETION_VERIFICATION_AND_REMEDIATION.md`

## 3. 💻关键调整详情

1. 在 retention 专题文档中补充说明：
   - 当前 scan / claim / execute 分离由 `recycle_jobs` 队列承担；
   - `recycle_batches` 只承担已完成回收批次锚点职责，不再兼做任务队列。
2. 在已归档的 plan26 中新增“后验复核补记”：
   - 明确 plan26 已无真实运行时未完成项；
   - 明确“recycle batch 显式 claim”属于旧措辞，不再按字面视为当前未完成项。
3. 在旧总控计划中新增“后验复核补记”：
   - 明确当前热窗口口径已经收敛为共享 `post_action.session_analysis_history_turns + retention.turn_keep_extra_turns`；
   - 明确当前多 worker 去重语义已经收敛到 `recycle_jobs`，而不是 `recycle_batches` 状态机。

## 4. ⚠️遗留问题与注意事项

1. 当前没有新增需要继续追踪的 retention 运行时缺口。
2. `vector_gc_jobs` 兼容字段残留仍然只是已知 schema 债务，不因本轮复核升级为阻断问题。
3. 后续若再做 retention 审查，应优先以：
   - `README.md`
   - `docs/retention-governance-guide_CN.md`
   - 当前主分支代码
   为真源，而不是直接把历史 completed plan 的早期设计稿段落视为当前事实。
