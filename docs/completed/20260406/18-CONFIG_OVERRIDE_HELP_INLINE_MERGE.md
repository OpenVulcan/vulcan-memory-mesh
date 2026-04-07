# 任务目标

将 `configs/base.yaml` 中已经建立的帮助说明思路同步覆盖到 `configs/config.yaml` 与 `configs/openai.config.example.yaml`，让覆盖配置文件和示例配置文件本身也具备可读、自解释的使用说明，减少使用者来回对照多个文件理解配置意图的成本。

# 详细执行步骤

1. 盘点 `configs/config.yaml` 与 `configs/openai.config.example.yaml` 当前内容，确认它们分别承担的覆盖职责与示例职责。
2. 设计注释组织方式，确保帮助信息既能说明“这个字段覆盖什么”，也能说明“为什么推荐这样写”，同时不破坏 YAML 的日常编辑体验。
3. 修改 `configs/config.yaml`，补充文件级说明、覆盖层使用约束、关键字段用途以及环境变量占位说明。
4. 修改 `configs/openai.config.example.yaml`，补充示例用途说明、适用场景说明以及关键路由字段的注释。
5. 运行必要的配置解析相关验证，确认新增注释不会影响现有 YAML 加载链路。
6. 在本文末尾追加执行变更总结，并在任务完成后迁移到 `docs/completed/20260406/`。

# 技术选型

- 保持 `base.yaml` 作为最完整的主配置说明入口，但让 `config.yaml` 与 `openai.config.example.yaml` 各自拥有与自身职责匹配的内联帮助信息。
- `config.yaml` 重点说明“覆盖层”的使用边界与推荐实践，避免误把它当成全量默认配置文件。
- `openai.config.example.yaml` 重点说明“示例模板”的适用部署方式、环境变量占位意义以及常见调整点。
- 注释以简洁、实用、面向修改者为原则，不把 `base.yaml` 的完整字段手册逐字复制过去，避免三份文件再次形成高耦合文档漂移。

# 验收标准

- `configs/config.yaml` 已具备清晰的文件级与字段级帮助说明。
- `configs/openai.config.example.yaml` 已具备清晰的示例用途与关键字段帮助说明。
- 新增注释不会影响 YAML 解析与现有加载链路。
- 三份主配置文件的说明层次清晰：`base.yaml` 负责完整底座说明，`config.yaml` 与 `openai.config.example.yaml` 负责各自职责范围内的落地说明。

# 执行变更总结

## 1. 核心修复与调整概述

- 已将 `config.yaml` 与 `openai.config.example.yaml` 的帮助信息补齐为可直接阅读的内联注释，不再只有 `base.yaml` 一处带说明。
- 已按最新要求把三份 YAML 配置文件中的注释统一为“英文在上、中文在下”的双语格式。
- 已同步修复 `base.yaml` 中先前存在的单语注释与中英文风格不一致问题，使三份 YAML 文件保持同一套说明规范。

## 2. 📂文件变更清单

### 新增

- 无

### 修改

- `configs/base.yaml`
- `configs/config.yaml`
- `configs/openai.config.example.yaml`

### 删除

- 无

## 3. 💻关键代码调整详情

- 本次变更不涉及 Go 运行时代码，重点是配置文件内联文档层的统一整理。
- `configs/base.yaml` 已重写为完整双语注释版本，覆盖文件定位、加载顺序、主配置分组用途与关键字段说明。
- `configs/config.yaml` 已补充“覆盖层”定位说明、环境变量占位说明，以及项目当前启用路由和存储模式的双语注释。
- `configs/openai.config.example.yaml` 已补充“示例模板”定位说明、OpenAI-compatible 场景用途说明，以及关键字段的双语注释。

## 4. ⚠️遗留问题与注意事项

- `base.yaml` 仍是最完整的底座说明入口；`config.yaml` 与 `openai.config.example.yaml` 的注释更偏向覆盖场景与示例场景说明，而不是复制整份字段手册。
- `noise_rules` 与 `pii_rules` 仍保持原有独立结构，本次没有改动其格式与加载方式。
- 已执行 `go test ./internal/config ./internal/testutil`，确认双语注释不会影响 YAML 解析与测试夹具加载。
