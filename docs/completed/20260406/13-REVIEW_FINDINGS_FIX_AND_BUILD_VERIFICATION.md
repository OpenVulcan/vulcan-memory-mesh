# 任务计划：评审问题修复与构建复核

## 1. 任务目标

本任务针对未提交改动中的三条评审意见执行复核与修复，确保：

1. 准确复核 `make.ps1 tester` / `scripts/vmm.ps1` 的构建链路，判断独立测试器产物在标准使用路径下是否仍存在配置缺失或规则陈旧风险。
2. 修复 `configs/pii_rules/common.json` 中裸值敏感键规则遗漏 `password` / `passwd` / `pwd` 的问题。
3. 修复北美电话号码规则对 `+1 ...` 与 `(xxx) ...` 格式的漏匹配问题，并同步覆盖 `en-US` / `en-CA`。
4. 为上述修复补充或更新测试，保证规则行为可以被自动化验证。

## 2. 执行步骤

1. 检查当前 `make.ps1`、`scripts/vmm.ps1`、`cmd/vmm-pii-tester/main.go`、`internal/config/loader.go` 的联动关系。
2. 明确 `tester` 目标在“干净输出目录”和“已有 output/configs”两种场景下的实际行为，判断第 1 条评审意见是否成立。
3. 如第 1 条评审意见不成立，则记录复核结论并保持代码不做多余改动；如成立，则以最稳妥方式修复。
4. 调整 `common.json` 的裸值 secret 规则，使其与单双引号规则在敏感键覆盖面上保持一致。
5. 调整 `en-US.json` 与 `en-CA.json` 的电话号码正则，消除前导 `\b` 对 `+1` 和 `(` 起始格式的拦截。
6. 在 `internal/platform/pii` 相关测试中补充针对上述规则的自动化用例。
7. 执行最小必要测试与验证命令，确认行为正确且未引入回归。

## 3. 技术选型与处理原则

- 构建链路复核优先基于现有打包约定判断，不为了迎合评审意见而引入额外重复复制逻辑。
- 规则修复优先保持现有 DSL / JSON 结构不变，仅做最小必要差异。
- 测试优先落在 `internal/platform/pii`，直接验证脱敏结果，避免只验证正则字符串本身。
- 若发现文档与实现存在偏差，但本次改动未触及文档承诺边界，则优先修复实现问题并在总结中记录。

## 4. 验收标准

满足以下条件视为本任务完成：

1. 已明确给出第 1 条评审意见的复核结论，并能用实际代码链路说明原因。
2. `password` / `passwd` / `pwd` 裸值赋值场景可被正确脱敏。
3. `+1 212-555-1234` 与 `(212) 555-1234` 这类格式可被 `en-US` / `en-CA` 规则正确脱敏。
4. 相关测试通过，且未破坏当前 PII 引擎已有能力。
5. 任务完成后补齐执行变更总结，并将计划文件迁移到 `docs/completed/20260406/`。

## 5. 当前状态

- 状态：已完成
- 结论：第 1 条评审意见复核后确认成立，且已与第 2、3 条问题一并修复并完成验证。

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已复核 `make.ps1 tester` 对应的实际执行链路，确认此前评审意见成立：`build` 目标会同步 `configs/`，但 `tester` 目标原本不会同步，因此在干净输出目录下确实存在 `output/bin/vmm-pii-tester.exe` 缺少 `../configs` 的问题。
- 已在 `scripts/vmm.ps1` 中抽取并复用 `Sync-Configs`，让标准 `build` 与独立 `tester` 目标都同步当前 `configs/`，消除“干净环境缺配置”和“残留旧配置导致规则陈旧”两类风险。
- 已修复 `configs/pii_rules/common.json` 中裸值 secret 规则遗漏 `password` / `passwd` / `pwd` 的问题。
- 已修复 `configs/pii_rules/en-US.json` 与 `configs/pii_rules/en-CA.json` 中北美电话号码规则前导 `\b` 导致 `+1 ...` 与 `(xxx) ...` 格式漏匹配的问题。
- 已在 `internal/platform/pii/engine_test.go` 补充回归测试，覆盖上述两类规则行为。

### 2. 📂文件变更清单

新增：

- `docs/completed/20260406/13-评审问题修复与构建复核.md`

修改：

- `scripts/vmm.ps1`
- `configs/pii_rules/common.json`
- `configs/pii_rules/en-US.json`
- `configs/pii_rules/en-CA.json`
- `internal/platform/pii/engine_test.go`

删除：

- 无

### 3. 💻关键代码调整详情

- `scripts/vmm.ps1`
  - 新增 `Sync-Configs` 复用函数，统一负责复制 `configs/` 到 `output/configs/`。
  - `Do-Build` 改为调用 `Sync-Configs`，避免复制逻辑重复。
  - `Do-BuildTester` 在仅构建 `vmm-pii-tester.exe` 后也会调用 `Sync-Configs`，使独立测试器产物具备完整规则目录。
- `configs/pii_rules/common.json`
  - 在 `CONTEXT_SECRET_BARE` 的敏感键集合中补入 `password|passwd|pwd`，使裸值赋值与单双引号规则保持一致覆盖面。
- `configs/pii_rules/en-US.json` / `configs/pii_rules/en-CA.json`
  - 去除电话号码规则最前面的 `\b`，恢复对 `+1 212-555-1234` 与 `(212) 555-1234` 的匹配能力。
- `internal/platform/pii/engine_test.go`
  - 新增裸值 password 类字段脱敏测试。
  - 新增北美电话号码国际区号和括号区号格式脱敏测试。

### 4. ⚠️遗留问题与注意事项

- 当前 `make.ps1 tester` 的正确执行仍依赖以 `pwsh -ExecutionPolicy Bypass -File .\\make.ps1 tester`、`make.bat tester` 或允许脚本执行的终端环境启动；直接在受限 PowerShell 策略下运行 `.\make.ps1 tester` 可能被系统策略拦截，这属于本机 PowerShell 执行策略问题，不是本次代码逻辑问题。
- 本次未继续扩展更多国家/地区电话号码格式，只修复了本轮评审明确指出的 `en-US` / `en-CA` 漏匹配问题。
- 验证已完成，当前状态应视为已完成并可归档。
