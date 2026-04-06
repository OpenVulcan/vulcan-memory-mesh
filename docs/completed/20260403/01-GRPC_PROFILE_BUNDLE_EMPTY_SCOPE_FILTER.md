# grpc 画像 bundle 空分段过滤修复计划

## 1. 任务目标

修复当前 `grpc` 画像获取接口在 `GetProfileBundle` 场景下的输出问题：

- 当 `TEAM / SPACE / PROJECT / USER` 某个 scope 没有实际画像正文时，不应继续输出该 scope 的介绍或标签段落。
- 当四个 scope 都不存在画像正文时，应直接返回空结果，而不是返回只包含说明头和介绍文案的伪完整 `FULL` 文本。
- 保持现有接口模式语义稳定，不引入额外协议破坏。

## 2. 详细执行步骤

1. 定位 `GetProfileBundle` 的实际组装链路，确认空内容仍被拼接的根因是在传输层还是用例层。
2. 调整 bundle 组装逻辑：
   - 仅在对应 scope 存在非空正文时输出该 scope 标签与对应说明块。
   - 仅在至少存在一个环境画像时输出环境说明头。
   - 仅在存在用户画像时输出用户说明头。
   - 当所有 scope 全部为空时，直接返回空字符串，不输出总介绍、P/L/W 说明或其他引导文案。
3. 补充或调整测试用例，覆盖以下场景：
   - 部分 scope 为空时，只保留非空 scope。
   - 全部 scope 为空时，`FULL` 模式返回空字符串。
   - 现有 split/full 基本行为不发生非预期回归。
4. 按仓库约定运行最少测试集，确认画像 bundle 修复未破坏 `grpc` 与相关 usecase 行为。
5. 完成后逐项对照本计划复核，并在文末追加执行变更总结，再将计划迁移到 `docs/completed/`。

## 3. 技术选型与实现原则

- 优先在 `internal/app/usecase` 的 bundle 组装逻辑内完成修复，避免把展示过滤散落到 `grpc` 传输层。
- 尽量保持改动面最小，只修正空内容拼接策略与对应测试。
- 新增或修改代码继续遵守仓库现有的中英文双语注释规范。

## 4. 验收标准

- `FULL` 模式下不会再输出空的 `[TEAM] / [SPACE] / [PROJECT] / [USER]` 分段。
- 四个 scope 全为空时，`combined_text` 为空字符串。
- 相关测试通过，且不存在因这次修复导致的现有 bundle 行为回归。

## 5. 风险与关注点

- 需要确保“全空返回空”只影响 bundle 输出层，不误伤底层画像存储或 scope 解析逻辑。
- 需要兼顾 `include_explanation` 的行为：当 bundle 全空时，即便调用方要求说明，也不应单独返回说明文案误导 AI。

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已在 `internal/app/usecase/profile_bundle.go` 修复 `GetProfileBundle` 的 full 模式拼装逻辑。
- 现在仅会为“实际存在正文”的 scope 输出标签与结构说明，不再为缺失的 `TEAM / SPACE / PROJECT / USER` 补空介绍。
- 当四个 scope 都没有画像正文时，`combined_text` 直接返回空字符串，不再返回只包含总说明和 P/L/W 介绍的伪完整文本。
- 已同步更新测试与接口文档，确保实现、验证和对外说明保持一致。

### 2. 📂文件变更清单

新增：

- `docs/plan/20260403-01-GRPC_PROFILE_BUNDLE_EMPTY_SCOPE_FILTER.md`

修改：

- `internal/app/usecase/profile_bundle.go`
- `internal/app/usecase/profile_test.go`
- `README.md`
- `docs/grpc-integration-guide_CN.md`
- `docs/profile-grpc-interfaces_CN.md`

删除：

- 无

### 3. 💻关键代码调整详情

- 将原先固定写死四个 scope 说明的 `profileBundleExplanationText` 拆成基础 `P/L/W` 说明与按实际非空 scope 动态生成的结构说明。
- 在 `GetBundle` 中改为调用新的动态说明构造逻辑，避免 explanation 文本继续暗示不存在的画像层级。
- 在 `buildProfileBundleText` 中增加“全 scope 为空则直接返回空字符串”的短路逻辑。
- 保留原有环境/用户分段顺序，但只有对应正文存在时才输出环境头、用户头以及对应标签。
- 新增全空场景测试，并补强“缺失 scope 不应出现在说明中”的断言。

### 4. ⚠️遗留问题与注意事项

- 当前 `split` 模式本身依旧通过空字符串表达“该 scope 无画像正文”，这次未修改协议字段结构。
- 这次修复没有变更 proto 契约，因此无需重新生成 pb 文件。
- 已完成最少测试集验证；本次改动规模较小，未额外执行 `go test ./...`。
