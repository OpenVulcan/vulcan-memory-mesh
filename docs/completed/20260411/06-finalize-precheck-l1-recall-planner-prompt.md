# 任务计划：定稿 precheck L1 长期记忆召回规划提示词

## 任务目标

将 `precheck_l1_main` 的提示词定稿为“长期记忆召回规划器”语义：准确判断当前问题是否需要从长期记忆中召回信息，并在需要时生成高质量检索语句；同时保留后端真实依赖的 `reason` 字段，但压缩成简短前置判断语句，以兼顾判断精度与速度。

## 详细执行步骤

1. 修改中英文 `precheck_l1_main` prompt：
   - 移除 `pre-check` 这类内部实现术语，改为纯职责描述。
   - 将 `current_context_hints / recent_context_hints` 改为用途型说明，不堆叠实现细节。
   - 将输出语言规则收敛为“跟随当前用户输入主导语言，而不是跟随提示词语言”。
   - 保留 `reason`，但明确为一句极短判断语句，并要求放在 JSON 第一位。
   - 保留“话题出现不等于答案存在”“即时追问 vs 追溯性表述”“个人历史事实 / 长期规则更倾向召回”等关键判断锚点。
   - 将 5 个长示例压缩为“判定矩阵 + 2 个完整示例”。
2. 不修改解析器字段契约，继续兼容 `reason / need_memory / queries`。
3. 运行最小相关测试，确认没有引入回归。
4. 执行标准构建，确保 `output/configs` 中的 prompt 同步更新。

## 技术选型与处理原则

- `precheck L1` 只负责“是否需要召回 + 如何构造检索语句”，不混入 post-action 的写入语义。
- `reason` 继续保留，因为它会作为 `intent_reason` 进入 L2；但要压缩长度，减少生成成本。
- 优先缩短无效描述，而不是削弱实际判定精度。

## 验收标准

- `precheck_l1_main` 中英文 prompt 与“长期记忆召回规划器”职责完全对齐。
- `reason` 放在输出 JSON 第一位，且被要求为一句极短判断。
- prompt 仍保留关键判定锚点，但整体更简洁。
- 相关测试通过，标准构建完成。

## 执行变更总结

### 1. 核心修复与调整概述

- 已将 `precheck_l1_main` 从“内部链路术语驱动”收敛为“长期记忆召回规划器”语义，让提示词只描述模型真正需要完成的职责。
- 已保留后端真实依赖的 `reason` 字段，但把它收缩为一句极短判断语句，并强制放在 JSON 第一位，以强化生成顺序并尽量减少输出成本。
- 已把原先 5 个长示例压缩为“判定矩阵 + 2 个完整示例”，在保留关键场景覆盖的同时缩短 prompt 体积。

### 2. 📂文件变更清单

- 新增：`docs/plan/20260411-06-finalize-precheck-l1-recall-planner-prompt.md`
- 修改：`configs/prompts/default_cn/precheck_l1_main.md`
- 修改：`configs/prompts/default_en/precheck_l1_main.md`

### 3. 💻关键代码调整详情

- 中文 `precheck_l1_main`：
  - 将角色改为“判断是否需要长期记忆召回，并生成检索语句”。
  - 将 `current_context_hints / recent_context_hints` 改成用途说明，不再混入实现细节。
  - 将输出语言规则压缩为“跟随当前用户输入主导语言，而不是跟随提示词文件语言”。
  - 强化 `reason` 为前置短判断，并要求 `reason -> need_memory -> queries` 的输出顺序。
  - 保留“话题出现不等于答案存在”“即时追问 vs 追溯性表述”“个人历史事实 / 长期规则更倾向召回”等关键判定锚点。
- 英文 `precheck_l1_main`：
  - 与中文版本保持一致的职责、规则与输出顺序约束。
- 已执行验证命令并通过：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `.\make.ps1 build`

### 4. ⚠️遗留问题与注意事项

- 当前后端仍会继续解析 `reason`，并把它作为 `intent_reason` 传入 L2；因此本次保留 `reason` 是刻意设计，不建议像 L2 一样直接删除。
- 标准构建已经同步了 `output/configs` 中的 prompt；若实际运行仍命中旧行为，需要确认使用的是否就是标准产物 `output/bin/vmm-local.exe` 及其配套配置目录。
