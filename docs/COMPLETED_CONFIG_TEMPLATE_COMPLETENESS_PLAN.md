# Config Template Completeness Plan

## 任务目标 / Goal

- 审计当前仓库中已经存在的配置项，找出哪些配置项尚未写入实际配置文件或示例配置文件。
- 将缺失的配置项补入对应配置文件，并使用运行时默认值作为默认示例值。
- 在不破坏用户现有本地配置意图的前提下，尽量只补缺失项，不回退已有改动。
- 记录本次审计结果、实际新增项与未改动原因。

## 执行步骤 / Steps

1. 审计 `internal/config/config.go` 中的配置结构、默认值和环境变量覆盖入口。
   - 结果：确认当前代码已支持但示例未完整展示的项主要集中在：
     - `logging.protect_payloads`
     - `logging.payload_encryption_key`
     - `llm.params`
     - `embedding.params`
     - `embedding.model_params`
     - `pre_check.search_scope`
     - `pre_check.similarity_threshold`
     - 多个 `VMM_*` 环境变量覆盖项
2. 审计当前配置文件与示例配置文件，确认缺失项。
   - 结果：`configs/local.json` 与 `configs/openai.local.example.json` 都缺少部分已支持字段；`configs/.env.example` 未展示完整的可选覆盖项集合。
3. 以最小改动补齐缺失配置项，优先更新：
   - `configs/local.json`
   - `configs/openai.local.example.json`
   - `configs/.env.example`
   - 实际修改：
     - [local.json](D:/projects/VulcanMemoryMesh/configs/local.json)
     - [openai.local.example.json](D:/projects/VulcanMemoryMesh/configs/openai.local.example.json)
     - [configs/.env.example](D:/projects/VulcanMemoryMesh/configs/.env.example)
4. 若相关文档中的配置示例已与实际不一致，同步修正。
   - 结果：本次主要修正配置文件本身，现有文档无需额外改写即可对齐。
5. 补充或更新配置层测试，验证默认值与示例配置覆盖关系。
   - 结果：本次没有新增代码路径，只需执行现有测试与构建验证。
6. 完成验证后将计划文件改名为 `COMPLETED_` 前缀。
   - 已完成。

## 验收标准 / Acceptance Criteria

- 已明确列出“代码已支持但配置文件未展示”的配置项。
- 配置文件 / 示例配置文件已补齐缺失项，并使用默认值。
- 未误覆盖用户已有的非默认本地配置语义。
- 已通过：
  - `go test ./internal/config -count=1`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config ./internal/platform/logx -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`

## 实际结论 / Actual Findings

- 真实存在的问题：
  - 是。代码里的部分配置项已经可用，但 `configs/local.json`、`configs/openai.local.example.json` 与 `.env.example` 没有完整展示。
- 实际新增到 JSON 配置文件的缺失项：
  - `logging.protect_payloads`
  - `logging.payload_encryption_key`
  - `llm.params`
  - `embedding.params`
  - `embedding.model_params`
  - `pre_check.search_scope`
  - `pre_check.similarity_threshold`
- 实际新增到 `.env.example` 的缺失项：
  - 采用“注释掉的完整覆盖项”方式补齐当前代码支持的 `VMM_*` 环境变量覆盖入口。
  - 这样既能展示完整配置面，又不会因为 env 覆盖优先级高于 JSON，而意外改变现有示例配置的有效行为。
- 特别说明：
  - 本次对已经存在于文件中的取值不做回退，只补缺失键。
