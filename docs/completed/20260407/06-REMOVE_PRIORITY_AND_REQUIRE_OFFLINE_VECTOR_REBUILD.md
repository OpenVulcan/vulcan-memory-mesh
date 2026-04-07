# 任务目标

按最新决策修正未提交改动中的两项设计约束：一是彻底移除 `llm.routes[].priority` 及其兼容逻辑，不再接受该字段参与 LLM 路由排序；二是把 `vmm-migrate -vector-rebuild` 收紧为“仅允许停服执行”的维护动作，禁止在运行时服务仍在监听时继续重建。

# 执行步骤

1. 盘点 `internal/config`、`internal/app`、`internal/adapters/outbound/ai_key_failover` 与相关配置样例、文档、测试中所有 `llm.routes[].priority` 的使用点，明确需要删除或替换的范围。
2. 修改 LLM 配置模型、运行时选路逻辑与测试，只保留按 `weights.*` 选路的实现，并移除对 `priority` 的兼容字段、校验、文档说明和样例配置。
3. 在 `vmm-migrate -vector-rebuild` 入口增加“停服探测”保护，基于当前 `grpc.listen_addr` 在本机执行监听探测；若发现运行时服务仍在监听，则直接拒绝执行重建。
4. 同步更新 README、设计文档和命令提示文案，明确说明向量重建必须在停服状态下执行。
5. 运行定向测试与全量测试，对照用户的两项指令逐项复核，并在完成后补充执行变更总结。

# 技术选型

- LLM 路由排序继续以 `weights.precheck_l1 / precheck_l2 / postaction_l1 / postaction_l2 / reserve` 作为唯一来源，不再保留 `priority` 的兼容回退，避免“未上线项目仍背负兼容逻辑”的复杂度。
- 停服约束优先采用维护工具内的本机 gRPC 监听探测，而不是仅靠文档约定，确保误操作时命令会在真正开始 destructive rebuild 前直接失败。
- 测试继续沿用现有 Go 单元测试和 stub/fake 风格，避免引入额外依赖或复杂集成环境。

# 验收标准

1. `llm.routes[].priority` 不再出现在 LLM 配置结构、运行时逻辑、配置样例和说明文档中。
2. LLM 主路由选择与多路由排序仅由 `weights.*` 控制，且相关测试通过。
3. 当本机运行时 gRPC 服务仍在监听 `grpc.listen_addr` 时，`vmm-migrate -vector-rebuild` 会直接报错退出，不会进入确认和重建流程。
4. README 与命令输出明确声明“向量重建必须停服执行”，相关测试通过。

# 执行变更总结

## 1. 核心修复与调整概述

- 已按最新决策彻底移除 `llm.routes[].priority` 的 LLM 兼容入口：配置结构不再声明该字段，加载阶段若仍出现该字段会直接报错，运行时主路由选择也只按 `weights.*` 执行。
- 已把 `vmm-migrate -vector-rebuild` 收紧为“必须停服”的维护动作：命令会先尝试占住 `grpc.listen_addr`，只有确认运行时服务已停止且命令自身持有该监听地址后，才允许进入后续确认与重建流程。
- 已同步补齐测试与文档，确保行为约束不是“仅靠约定”，而是由配置校验、命令保护和使用说明共同落地。

## 2. 📂文件变更清单

- 修改：`internal/config/config.go`
- 修改：`internal/config/config_test.go`
- 修改：`cmd/vmm-migrate/vector_rebuild.go`
- 修改：`cmd/vmm-migrate/vector_rebuild_test.go`
- 修改：`README.md`
- 修改：`docs/ai-model-failover-design_CN.md`
- 修改：`configs/base.yaml`

## 3. 💻关键代码调整详情

- 在 `internal/config/config.go` 中删除 `LLMRouteConfig.Priority`，新增针对 `llm.routes[].priority` 的显式拒绝逻辑，并保留 `weights.*` 作为 LLM 路由排序的唯一权重来源。
- 在 `internal/config/config_test.go` 中移除 legacy priority 相关断言，改为验证分场景 `weights.*` 的主路由选择、默认权重回退和负值校验，同时新增“出现 `llm.routes[].priority` 时加载失败”的覆盖。
- 在 `cmd/vmm-migrate/vector_rebuild.go` 中新增运行时监听占位保护：若 `grpc.listen_addr` 仍被占用则直接失败；若可占用，则在整个重建期间持续持有监听，阻止重建中途重新拉起 gRPC 服务。
- 在 `cmd/vmm-migrate/vector_rebuild_test.go` 中补充空闲端口可执行、被占用端口拒绝执行、以及拒绝阶段不会继续进入人工确认提示的行为测试。
- 在 `README.md`、`docs/ai-model-failover-design_CN.md` 与 `configs/base.yaml` 中同步移除 LLM `priority` 的说明，并明确向量重建必须停服执行。

## 4. ⚠️遗留问题与注意事项

- 本次“彻底移除 priority”仅针对 `llm.routes[].priority`；`rerank.routes[].priority` 仍保持现有设计，未在本任务范围内调整。
- 当前工作区存在其他未提交改动与新增文件，本次只围绕用户指定的两项修正做了最小必要调整，未回退或整理无关变更。
- 已执行 `go test ./internal/config ./internal/app ./cmd/vmm-migrate`、`go test ./internal/adapters/outbound/ai_key_failover` 与 `go test ./...`，结果均通过。
