# 任务目标

全面取消当前 AI 配置中的多模式兼容语义，统一收敛为新的明确配置契约，确保：

1. `llm` 与 `rerank` 仅支持显式 `routes` 配置，不再保留旧版单路由顶层运行时语义。
2. `embedding` 仅支持单协议、单模型配置，但继续支持多 `key` 与 `nodes` 吞吐预算控制，不支持多 provider / 多模型 route。
3. 删除或停用所有围绕旧模式兼容而引入的覆盖、归一化与装配逻辑，避免继续产生语义混杂。
4. 一并修复当前已知 review 问题，确保新的单一契约下不再出现“顶层覆盖破坏 route 拓扑”与“局部覆盖抹掉整套 failover 配置”的问题。

# 详细执行步骤

1. 梳理现状与目标边界
   - 检查 `internal/config/config.go`、`internal/app/app.go`、`internal/adapters/outbound/ai_key_failover/*` 的当前职责边界。
   - 明确哪些结构、校验、环境变量、文档和测试仍在服务旧版单路由兼容模式。
2. 收敛配置契约
   - 将 `llm` 与 `rerank` 收敛为仅显式 `routes` 可运行。
   - 明确 `embedding` 仍保留单 provider / endpoint / model / dimension，但仅支持多 key 与节点预算。
   - 删除或停用 route 模式与 legacy 顶层字段互相覆盖的兼容逻辑。
3. 调整运行时装配
   - 让 `buildLLM` 与 `buildReranker` 直接面向显式 routes 构建客户端。
   - 让 `buildEmbedding` 继续走单 provider / 单模型装配，并保留 key failover 与节点预算。
4. 清理旧兼容实现
   - 删除不再需要的 legacy override presence、route 反向覆盖、旧单路由 fallback 视图等逻辑。
   - 同步清理相关测试、文档说明与配置示例中的旧模式内容。
5. 修复已知问题并补测试
   - 在新契约下修正当前 review 指出的 route 节点拓扑与 key failover 覆盖问题。
   - 增加或改写配置校验、运行时装配、failover 行为测试。
6. 执行验证
   - 运行与本次改动直接相关的 Go 测试集合。
   - 对照计划逐项核验，确认没有残留旧模式运行时入口。

# 技术选型

1. 以“单一配置语义”优先，避免继续维护 legacy 与 route 并存的归一化和覆盖规则。
2. 对 `llm` / `rerank` 采用显式 route 自包含设计：每条 route 自己声明 provider、endpoint、model、nodes、key_failover。
3. 对 `embedding` 保持固定单路由设计，仅保留多 key 与节点预算，不扩展为多 route。
4. 优先通过删除兼容分支而非继续追加条件判断来降低复杂度。

# 验收标准

1. `llm` 与 `rerank` 不再依赖旧版单路由顶层语义完成运行时装配。
2. `embedding` 仍然支持多 key 与 `nodes` 吞吐预算，但不能配置多 provider / 多模型 route。
3. 当前 review 已知问题在新配置契约下被一并解决。
4. 配置校验、运行时装配、关键 failover 测试通过。
5. `README.md`、`configs/vmm_config_readme.md` 与相关中文设计文档已同步到新的统一配置语义。

# 执行变更总结

## 1. 核心修复与调整概述

1. 已将 `llm` 与 `rerank` 的运行时配置彻底收敛为 `routes[]` 模式，删除顶层单路由字段参与装配、归一化与环境变量覆盖的兼容语义。
2. 已保留 `embedding` 的单 provider / 单模型设计，同时明确只支持多 `api_keys` 与 `nodes` 吞吐预算，不支持多 provider / 多模型 route。
3. 已通过移除 legacy 顶层覆盖逻辑，从根源上消除 review 中指出的“顶层 key 覆盖破坏 route 节点拓扑”和“局部 failover 覆盖抹掉整套 route 配置”的问题。
4. 已同步更新示例配置、中文设计文档、配置说明文档与打包目录配置副本，确保仓库内外部入口都只呈现新契约。

## 2. 📂文件变更清单

### 修改

- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/app/app.go`
- `internal/app/app_test.go`
- `internal/testutil/realruntime.go`
- `internal/adapters/outbound/dashscope_rerank/client_live_test.go`
- `README.md`
- `configs/local.json`
- `configs/openai.local.example.json`
- `configs/.env.example`
- `configs/vmm_config_readme.md`
- `docs/ai-model-failover-design_CN.md`

### 同步打包配置副本

- `output/configs/local.json`
- `output/configs/openai.local.example.json`
- `output/configs/.env.example`
- `output/configs/vmm_config_readme.md`

## 3. 💻关键代码调整详情

1. `internal/config/config.go`
   - `LLMConfig` 与 `RerankConfig` 仅保留显式 `Routes` 视图。
   - `EmbeddingConfig` 仅保留单模型配置，并删除单值 `api_key`。
   - 归一化逻辑只支持 `api_keys -> nodes` 的单向折叠，不再支持 legacy 单路由顶层字段回流到 routes。
   - 新增已移除字段与已移除环境变量的快速拒绝逻辑，避免旧配置静默生效。
2. `internal/app/app.go`
   - `buildLLM` 与 `buildReranker` 改为直接基于显式 routes 装配。
   - `buildEmbedding` 继续走单 provider / 单模型，但保留多 key 与节点预算。
   - `PrimaryModel()` 由最高优先级 LLM route 导出，供提示词与运行时诊断使用。
3. 测试与运行时夹具
   - `internal/config/config_test.go` 与 `internal/app/app_test.go` 已改写为新配置契约。
   - `internal/testutil/realruntime.go` 已改为从主 LLM route 与 embedding 多 key 配置读取真实测试客户端。
   - `internal/adapters/outbound/dashscope_rerank/client_live_test.go` 已移除旧 `VMM_RERANK_*` 测试环境变量依赖，改为使用共享 `OPENAI_*` 风格变量。
4. 文档与示例配置
   - 两份示例 JSON 已改为 `llm.routes[]` / `rerank.routes[]`。
   - `.env.example` 已移除旧 `VMM_LLM_*` 与 route 顶层 `VMM_RERANK_*` 覆盖示例。
   - 设计文档与配置文档已明确声明不再保留旧模式兼容。

## 4. ⚠️遗留问题与注意事项

1. `output/configs/*` 当前通过与 `configs/*` 同步保持一致；后续如继续修改源配置，应保持打包目录副本同步，或通过标准构建流程重新生成。
2. 仓库中仍存在本次任务之外的未提交改动与新增文件，本次未做回退或整理，应由后续提交按实际工作范围统一处理。
3. 当前已验证相关核心测试与 `go test ./...` 全量测试通过，但尚未执行 `make.ps1 build`，因为本次未修改运行时打包路径或配置目录规则。
