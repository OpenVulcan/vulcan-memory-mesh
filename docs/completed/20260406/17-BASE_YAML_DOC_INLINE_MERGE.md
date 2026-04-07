# 任务目标

将 `configs/vmm_config_readme.md` 中承担的主配置说明信息收敛并融入 `configs/base.yaml`，让基础配置文件本身具备可读、自解释的文档能力，减少“配置内容”和“独立说明文档”之间的长期漂移风险。

# 详细执行步骤

1. 盘点 `configs/base.yaml` 与 `configs/vmm_config_readme.md` 的内容分工，确认哪些说明应内嵌为 YAML 注释，哪些说明只保留为简短入口提示。
2. 设计 `base.yaml` 的注释组织方式，优先按配置分组、字段用途、默认语义和关键注意事项内联说明，保证阅读时不会破坏 YAML 可维护性。
3. 修改 `configs/base.yaml`，将主配置节点说明、关键字段说明和覆盖优先级等核心文档信息融入文件注释。
4. 收敛 `configs/vmm_config_readme.md`，避免保留第二份完整配置手册；如有必要，仅保留迁移说明或入口提示，指向 `base.yaml` 作为主文档。
5. 自检配置文件可读性与格式，必要时执行相关格式或加载验证，确保新增注释不会影响 YAML 解析与仓库内使用方式。
6. 在本文末尾追加执行变更总结，并在任务完成后迁移到 `docs/completed/20260406/`。

# 技术选型

- 以 `configs/base.yaml` 作为主配置文档的唯一权威入口，优先将解释性信息转为 YAML 注释，而不是继续维护并行 Markdown 全量手册。
- 注释内容以“字段作用、关键约束、常用注意事项”为主，避免把过长的运维教程逐字搬运进配置文件，防止影响日常编辑体验。
- 对仍需保留的 Markdown 文件采用轻量跳转或说明形式，降低历史链接失效风险，同时明确当前主阅读入口已经迁移到 `base.yaml`。

# 验收标准

- `configs/base.yaml` 已包含足够完整的主配置说明，阅读该文件即可理解主要配置节点和关键字段含义。
- `configs/vmm_config_readme.md` 不再保留一份与 `base.yaml` 平行的完整配置字段手册。
- 变更后的 YAML 文件格式正确，不影响现有加载链路。
- 相关说明与本次实际交付结果一致，没有再制造新的文档漂移点。

# 执行变更总结

## 1. 核心修复与调整概述

- 已将主配置说明从独立 Markdown 手册收敛到 `configs/base.yaml` 的内联注释中，使基础配置文件本身成为主阅读入口。
- 已为 `base.yaml` 补充加载顺序、覆盖方式、字段用途和关键约束说明，覆盖主配置各个核心分组。
- 已将 `configs/vmm_config_readme.md` 收敛为轻量入口说明，避免继续维护第二份完整配置字段手册。

## 2. 📂文件变更清单

### 新增

- 无

### 修改

- `configs/base.yaml`
- `configs/vmm_config_readme.md`

### 删除

- 无

## 3. 💻关键代码调整详情

- 本次未改动 Go 运行时代码，核心变更集中在配置文档组织方式。
- `configs/base.yaml` 增加了面向运维与开发者的内联注释，包括：
  - 顶部的加载顺序与使用建议；
  - 各主配置分组的用途说明；
  - 关键字段的默认语义、作用范围和重要约束；
  - `memory_replace_scope`、`prompts.routes`、多路由与 failover 等容易误解的配置点说明。
- `configs/vmm_config_readme.md` 调整为迁移说明页，只保留阅读顺序与边界提示，明确 `base.yaml` 已成为主文档入口。

## 4. ⚠️遗留问题与注意事项

- `noise_rules` 与 `pii_rules` 仍保持原有独立目录结构和原有协议，本次没有把它们并入 `base.yaml`。
- `base.yaml` 已具备主说明能力，但环境差异化配置仍建议写入 `config.yaml`，避免把底座配置持续改脏。
- 已执行 `go test ./internal/config ./internal/testutil`，确认带注释的 `base.yaml` 不影响主配置解析与测试夹具加载。
