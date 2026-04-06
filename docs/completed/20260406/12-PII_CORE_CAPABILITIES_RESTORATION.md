# 任务目标

结合指定对比分支范围内的历史提交，梳理并补回当前仓库中已经丢失的 PII 核心能力，确保规则加载、规则校验、替换逻辑、区域化规则、测试工具与相关文档恢复到可维护、可验证状态，并与当前 OSS 本地版架构保持一致。

# 执行步骤

1. 盘点当前仓库状态，确认工作区是否存在未提交改动，并定位当前 PII 相关实现、测试与文档分布。
2. 基于指定提交范围逐个审阅 `d068607475`、`71f0334ec4`、`23e4eea88a`、`8e3ededf57`、`07ec7aa080` 涉及的文件与逻辑，整理“应恢复内容”与“当前仍保留内容”的差异清单。
3. 按最小必要且工程化完整的原则，将缺失的 PII 能力补回到当前代码基线，避免重新引入不符合本仓库当前架构约束的旧路径。
4. 为新增或重写的源码补齐双语注释，确保复杂规则处理、替换执行、校验流程和测试入口具备清晰的中英文说明。
5. 按仓库规则运行至少以下测试：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
   - 若改动范围较大或影响面超出 PII 子模块，再运行 `go test ./...`
6. 对照本计划逐项自检，确认实现、测试与文档同步无遗漏后，在文末追加“执行变更总结”，最后将计划文件迁移到 `docs/completed/20260406/` 目录。

# 技术选型

- 以当前仓库本地代码和 Git 历史提交为主进行差异恢复，优先复用既有实现而不是重新发明规则引擎。
- 恢复过程中遵守当前依赖方向与本地版运行时边界，不重新引入 SaaS 专属路径、Postgres 专用存储或不再支持的服务装配方式。
- 对 PII 规则能力采取“能力补回 + 接口兼容 + 文档同步”的方式推进，优先保证规则文件、加载流程、替换结果和测试覆盖闭环。

# 验收标准

1. 指定提交范围中的核心 PII 能力已在当前分支恢复，且不存在明显缺漏。
2. PII 相关源码新增或重写部分满足仓库要求的中英文双语注释规范。
3. PII 相关测试能够通过，且未引入与当前配置加载、文本净化、请求处理链路相冲突的回归问题。
4. 若提交范围涉及文档或测试工具，相关文档与测试入口已经同步恢复或更新。
5. 本计划末尾已补充结构化“执行变更总结”，并在任务完成后按规范迁移到完成目录。

## 执行变更总结

### 1. 核心修复与调整概述

- 已从历史提交链中补回 PII 条件求值器、原子校验函数、共享规则包、多地区语言规则、独立测试器及配套文档。
- 已在当前主线基线之上完成适配，保留当前仓库的 gRPC-only 架构，不重新引入旧 HTTP `/chat`、应用内 TLS 或历史运行时接线。
- 已补充 `make.ps1` / `scripts/vmm.ps1` 的测试器构建能力，并修复脚本里原有的 `Join-Path` 参数错误与 `$LASTEXITCODE` 判定隐患，确保标准构建可真正同步新的 `configs/pii_rules`。

### 2. 📂文件变更清单

- 新增：
  - `cmd/vmm-pii-tester/main.go`
  - `internal/platform/pii/evaluator.go`
  - `internal/platform/pii/evaluator_test.go`
  - `internal/platform/pii/validators.go`
  - `internal/platform/pii/validators_test.go`
  - `configs/pii_rules/common.json`
  - `configs/pii_rules/ar-AE.json`、`da-DK.json`、`de-DE.json`、`en-AU.json`、`en-CA.json`、`en-GB.json`、`en-IE.json`、`en-IN.json`、`en-MY.json`、`en-NZ.json`、`en-PH.json`、`en-SG.json`、`en-US.json`
  - `configs/pii_rules/es-AR.json`、`es-CL.json`、`es-ES.json`、`es-MX.json`、`es-PE.json`
  - `configs/pii_rules/fi-FI.json`、`fr-FR.json`、`id-ID.json`、`it-IT.json`、`ja-JP.json`、`ko-KR.json`、`nb-NO.json`、`nl-NL.json`、`pl-PL.json`、`pt-BR.json`、`pt-PT.json`、`sv-SE.json`、`th-TH.json`、`tr-TR.json`、`vi-VN.json`、`zh-HK.json`、`zh-TW.json`
  - `docs/pii-country-matrix_CN.md`
  - `docs/pii-validator-config_CN.md`
  - `docs/pii-validator-config_EN.md`
  - `docs/pii-validator-developer_CN.md`
  - `docs/pii-validator-developer_EN.md`
- 修改：
  - `internal/platform/pii/engine.go`
  - `internal/platform/pii/engine_test.go`
  - `configs/pii_rules/zh-CN.json`
  - `make.ps1`
  - `scripts/vmm.ps1`
  - `README.md`
- 删除：
  - 无

### 3. 💻关键代码调整详情

- `internal/platform/pii/engine.go`
  - 恢复 `common.json + <lang>.json` 的双层规则装配模型。
  - 恢复 `condition` 条件表达式编译、模板替换、调试 Trace、安全摘要日志输出。
  - 增加兼容入口 `NewEngine(systemDir, userDir, defaultLang)`，并保留 `NewEngineWithLogger(...)` 供测试器和定制调用使用。
- `internal/platform/pii/evaluator.go` / `validators.go`
  - 恢复 opcode 编译器、短路求值虚拟机，以及 `weight_sum`、`cn_check`、`len`、`is_luhn`、`is_base64` 等原子能力。
- `configs/pii_rules/*.json`
  - 恢复共享密钥规则、上下文密钥模板保留替换、多地区号码/证件/地址规则，以及“环境变量占位符不误脱敏”的修复。
- `cmd/vmm-pii-tester/main.go`
  - 恢复基于文件规则和 Ad-hoc 规则的双模式调试入口。
- `make.ps1` / `scripts/vmm.ps1`
  - 新增 `tester` 构建目标。
  - 标准 `build` 现在会同时产出 `vmm-local.exe` 与 `vmm-pii-tester.exe`，并正确同步完整 `configs/pii_rules`。

### 4. ⚠️遗留问题与注意事项

- 当前主线 gRPC 运行时并未自动接入这套 PII 引擎；本次恢复的是规则引擎、测试器和文档资产，不包含重新引入旧 HTTP 脱敏链路。
- 在当前机器上直接执行 `.\make.ps1 build` 会受到本地 PowerShell 执行策略影响；已使用 `powershell -ExecutionPolicy Bypass -File .\make.ps1 build` 完成构建验证。
- `make.ps1` 与 `scripts/vmm.ps1` 目前在 Git 中提示 CRLF/LF 规范告警，但不影响本次功能与构建结果。
