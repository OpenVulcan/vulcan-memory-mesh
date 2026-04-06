# 任务目标

将当前项目中的主运行时配置体系从 JSON 统一改造为 YAML 模式，建立“项目内基础配置 + 项目/用户覆盖配置”的分层加载机制。基础配置由项目目录中的 `base.yaml` 提供，作为系统默认值来源；项目内和用户侧覆盖配置统一改为 `config.yaml`，沿用当前 `local.json` 的作用和查找方式，但不再要求或支持在用户目录中放置基础配置副本。同时将原先独立的 `prompts-routes.json` 合并进主配置结构，不再单独维护独立路由文件。`noise_rules` 与 `pii_rules` 保持当前独立结构，不纳入本次 YAML 化范围。

# 详细执行步骤

1. 盘点当前主配置入口、配置结构体、默认配置文件、`prompts-routes.json` 与 `local.json` 的加载链路，明确现有 JSON 依赖点与覆盖优先级。
2. 设计 YAML 配置文件布局，确认 `base.yaml`、`config.yaml` 及相关目录的命名、查找顺序和合并策略。
3. 修改主配置加载实现，将默认配置与用户覆盖配置统一切换为 YAML 解析，并确保支持当前项目已经暴露的全部配置项，而不只是部分列表配置。
4. 调整提示词路由装配逻辑，把 `prompts-routes.json` 融合进主配置结构，并保持原有的路由优先级与提示词目录校验能力。
5. 补充或更新对应的默认配置文件、示例配置文件和必要注释，保证配置内容与代码结构一致。
6. 按仓库约束执行相关测试，校验配置加载、规则目录优先级及关键链路行为没有回归。
7. 完成自检后，在本文末尾追加执行变更总结，并将计划文件迁移到 `docs/completed/20260406/` 目录。

# 技术选型

- 优先复用当前配置结构体与装配链路，只调整配置文件格式、文件名约定和解析实现，避免引入额外的运行时分支。
- 默认配置采用项目内 `base.yaml` 作为唯一基础来源，保持随构建产物分发；用户目录仅保留覆盖配置职责，不复制基础配置。
- 覆盖配置采用与当前 `local.json` 等价的查找逻辑，改名为 `config.yaml`，并在合并时保持“用户层优先、系统层兜底”的设计原则。
- 提示词路由改为配置树中的一部分，避免再出现主配置与单独路由文件分离漂移的问题。
- 若现有实现对部分主配置采用硬编码默认值或非文件注入方式，需要统一评估并纳入 `base.yaml`，确保“支持的一切配置”都可显式配置。
- `noise_rules` 与 `pii_rules` 继续保持当前独立目录和原有文件结构，本次不调整其加载协议。

# 验收标准

- 项目运行时的主配置不再依赖 JSON 文件作为正式配置输入，核心配置读取与覆盖逻辑全部切换到 YAML。
- 项目目录中的 `base.yaml` 可以独立提供完整默认配置，且不会被要求复制到用户目录加载。
- 用户侧 `config.yaml` 能按当前 `local.json` 的路径规则参与覆盖，并正确覆盖 `base.yaml` 中的对应项。
- 提示词路由不再依赖独立的 `prompts-routes.json` 文件，而是随主配置一起加载并生效。
- `noise_rules` 与 `pii_rules` 的现有目录结构和文件格式保持不变。
- 当前仓库要求的配置相关测试通过，必要文档或样例文件已同步更新。

# 执行变更总结

## 1. 核心修复与调整概述

- 已将主运行时配置的正式文件模式从 JSON 调整为 YAML，建立 `base.yaml -> config.yaml -> 用户侧 config.yaml` 的分层加载链路。
- 已把原先独立的 `prompts-routes.json` 合并进主配置树中的 `prompts.routes`，由统一配置加载流程完成解析、归一化与校验。
- 已保留 `noise_rules` 与 `pii_rules` 的原有独立目录和文件格式，不将其纳入本次 YAML 化改造范围。
- 已同步更新默认配置文件、示例配置文件、测试夹具与相关文档，确保运行时、测试环境和标准打包产物保持一致。

## 2. 📂文件变更清单

### 新增

- `configs/base.yaml`
- `configs/config.yaml`
- `configs/openai.config.example.yaml`

### 修改

- `cmd/vmm-local/main.go`
- `internal/config/config.go`
- `internal/config/loader.go`
- `internal/config/manager.go`
- `internal/testutil/realruntime.go`
- `internal/config/config_test.go`
- `internal/config/loader_test.go`
- `internal/config/manager_test.go`
- `internal/app/app_test.go`
- `README.md`
- `configs/vmm_config_readme.md`
- `docs/noise-gate-guide_CN.md`
- `go.mod`

### 删除

- `configs/local.json`
- `configs/openai.local.example.json`
- `configs/prompts-routes.json`

## 3. 💻关键代码调整详情

- 配置装载层新增项目基础配置路径概念，主配置查找顺序改为优先读取项目内 `base.yaml`，再读取项目内可选的 `config.yaml`，最后读取用户目录或 `-config` 指向位置下的 `config.yaml` 覆盖。
- 主配置结构新增 `prompts.routes`，并将原提示词路由文件的加载职责移除，改为直接消费已合并完成的配置路由映射，再执行默认路由与目录存在性校验。
- 配置解析逻辑补充 YAML 解码与向 JSON 兼容结构的转换流程，复用现有主配置归一化、环境变量展开和默认值补足能力，避免为 YAML 引入一套平行装配分支。
- 运行时入口、真实运行时测试夹具与应用层测试均改为基于 `base.yaml/config.yaml` 布局进行加载，保证本地开发、测试和打包后的 `output/configs` 目录规则一致。
- 补充针对 YAML 分层合并与提示词路由合并的测试，验证 `prompts.routes` 能跟随主配置分层正确生效。

## 4. ⚠️遗留问题与注意事项

- `base.yaml` 仅作为项目内随包分发的基础配置，不应复制到 `~/.vmm` 或其他用户覆盖目录；用户侧只需要维护 `config.yaml`。
- `noise_rules` 与 `pii_rules` 继续沿用原有独立结构和原有优先级逻辑，本次没有改变它们的文件格式或装载协议。
- PowerShell 直接执行 `make.ps1` 会受到当前机器执行策略限制，已通过仓库推荐入口 `make.bat build` 完成标准构建验证。
- 已完成 `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`、`go test ./...` 与 `.\make.bat build` 验证。
