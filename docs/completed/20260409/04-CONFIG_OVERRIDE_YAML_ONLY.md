# 任务目标

收紧主运行时 `-config` 参数的显式文件加载规则，确保主配置链路只接受 YAML 文件，不再兼容显式传入的 JSON/TOML 等主配置文件；同时保持目录型 `-config` 覆盖根目录语义不变，并同步更新测试、CLI 帮助文案与相关文档说明。

# 详细执行步骤

1. 复核 `vmm-local`、`vmm-migrate`、`vmm-pii-tester` 与 `internal/config/loader.go` 的 `-config` 解析链路，确认当前“目录参数”和“显式文件参数”的分流逻辑。
2. 在配置布局解析层新增显式配置文件格式校验，仅允许 `.yaml` / `.yml` 作为主配置文件输入；对 `.json`、`.toml` 及其他非 YAML 文件给出明确错误。
3. 保持目录型 `-config` 行为不变，继续把目录视为覆盖根目录，并从该目录下寻找 `config.yaml`。
4. 更新 `internal/config/loader_test.go`，补充“显式 YAML 文件仍可用”和“显式 JSON 文件被拒绝”的回归测试，覆盖已存在文件与文件式参数场景。
5. 更新 CLI 帮助文案与仓库文档，明确主配置默认链路与显式 `-config` 文件路径都只接受 YAML；同时保留 `pii_rules` / `noise_rules` 的独立 JSON 规则说明，不混淆主配置与规则文件格式。
6. 运行受影响测试并逐项对照计划自检；确认无遗漏后在文末追加执行变更总结，并将计划文件迁移到 `docs/completed/20260409/`。

# 技术选型

- 延续当前 `ResolvePromptLayout -> ConfigPaths -> LoadPaths` 的主配置装配链路，不引入新的配置解析分支。
- 在 `internal/config/loader.go` 中集中处理显式文件参数的格式校验，避免把“主配置文件格式限制”散落到各个 CLI 入口。
- 继续使用 YAML 作为主配置唯一正式格式，维持 `base.yaml -> config.yaml -> 用户覆盖 config.yaml` 的分层模型。
- 通过单元测试锁定约束，避免后续再次把 JSON/TOML 误放回显式主配置文件路径。

# 验收标准

- `vmm-local`、`vmm-migrate` 与 `vmm-pii-tester` 的 `-config` 参数在传入显式文件时，仅接受 `.yaml` / `.yml`。
- 显式传入 `.json`、`.toml` 等主配置文件路径时，布局解析阶段直接返回清晰错误。
- 目录型 `-config` 覆盖根目录行为不变，仍按目录下的 `config.yaml` 参与主配置覆盖。
- 相关 loader 测试通过，并补齐拒绝非 YAML 显式主配置文件的回归覆盖。
- CLI 帮助文案与仓库文档已同步说明“主配置必须是 YAML”，不再保留误导性的 JSON 兼容表述。

# 执行变更总结

## 1. 核心修复与调整概述

- 已把显式 `-config` 文件参数收紧为只接受 `.yaml` / `.yml`，避免主运行时通过 JSON/TOML 等旧格式重新进入配置链。
- 保留了目录型 `-config` 语义，继续把目录视为覆盖根目录，并从该目录下寻找 `config.yaml`。
- 已同步更新 CLI 帮助文案、README 与 PII 配置说明，明确主配置默认链路和显式文件链路都以 YAML 为唯一主配置格式。

## 2. 文件变更清单

- 修改：`internal/config/loader.go`
- 修改：`internal/config/loader_test.go`
- 修改：`cmd/vmm-local/main.go`
- 修改：`cmd/vmm-migrate/main.go`
- 修改：`cmd/vmm-pii-tester/main.go`
- 修改：`README.md`
- 修改：`docs/pii-validator-config_CN.md`
- 修改：`docs/pii-validator-config_EN.md`
- 新增：`docs/plan/20260409-04-CONFIG_OVERRIDE_YAML_ONLY.md`
- 删除：无

## 3. 关键代码调整详情

- 在 `resolveUserDir(...)` 中新增显式主配置文件后缀校验：当 `-config` 传入文件路径时，现仅允许 `.yaml` / `.yml`。
- 新增 `validateExplicitMainConfigFilePath(...)`，集中拒绝 `.json`、`.toml` 及无效显式文件后缀，避免把格式约束分散到多个 CLI 入口。
- 保留 `pathLooksLikeFile(...)` 的“文件式参数识别”职责，让尚未创建的 YAML 文件路径也能继续走显式文件分支。
- 在 `loader_test.go` 中补充“显式 YAML 文件仍可用”和“显式非 YAML 文件被拒绝”的回归用例，覆盖已存在文件与未创建文件两条分支。
- 更新三个可执行入口的 `-config` 帮助文案，以及 README / PII 配置文档中的覆盖链说明，避免代码与文档继续不一致。

## 4. 遗留问题与注意事项

- 本次只收紧了主配置文件格式；`pii_rules` 与 `noise_rules` 的独立 JSON 规则文件体系没有调整。
- README 里此前“`-config` 不是单个配置文件路径”的表述已改为更精确的兼容说明；后续如果决定彻底移除显式文件参数支持，还需要再做一轮行为和文档收敛。
- 已完成验证：
  - `go test ./internal/config`
  - `go test ./cmd/vmm-local ./cmd/vmm-migrate ./cmd/vmm-pii-tester`
  - `go test ./...`
