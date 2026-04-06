# 任务目标

修复 YAML 配置迁移后在代码评审中发现的两个回归问题：一是 `prompts.routes` 中非法键值会被静默忽略，导致提示词路由悄然回退；二是真实模型测试夹具的配置查找顺序未与运行时保持一致，导致仅有 `base.yaml` 加用户覆盖配置的场景下测试初始化失败。

# 详细执行步骤

1. 复核 `internal/config/loader.go` 中 `prompts.routes` 的归一化与校验链路，明确当前为何会吞掉非法项而非快速失败。
2. 设计并实现新的提示词路由校验方案，保持 YAML 分层合并能力，同时恢复对空键、空目录等非法配置的显式报错行为。
3. 复核 `internal/testutil/realruntime.go` 与运行时 `ResolvePromptLayout` / `LoadPaths` 的配置查找顺序，找出测试夹具与正式运行时不一致的点。
4. 调整真实模型测试夹具的配置根目录解析与加载链路，使其支持“仅打包 `base.yaml` + 用户覆盖 `config.yaml`”的合法运行时布局。
5. 补充或更新相关测试，覆盖上述两个回归场景，避免后续再次引入同类问题。
6. 执行受影响模块测试与必要的全量测试，确认修复没有破坏现有配置迁移结果。
7. 完成自检后，在本文末尾追加执行变更总结，并将计划文件迁移到 `docs/completed/20260406/`。

# 技术选型

- 优先在现有配置装载链路上做局部修复，不重新拆分 YAML 解析和提示词管理职责。
- 对 `prompts.routes` 采用“归一化与校验分离”的方式：继续保留基础裁剪能力，但对会改变运行时语义的非法项恢复显式错误，避免静默降级。
- 真实模型测试夹具复用与运行时一致的配置链概念，允许项目内基础配置和用户侧覆盖配置分处不同目录，只在真正缺少必要配置时失败。
- 测试优先覆盖评审指出的具体回归场景，确保修复具有可证明性。

# 验收标准

- 当 `prompts.routes` 中出现空键、空目录或经环境变量展开后变为空的非法项时，配置加载会明确失败，而不是静默忽略。
- 真实模型测试夹具能够在仅存在 `base.yaml` 且用户覆盖位于 `~/.vmm/config.yaml` 的场景下正确解析配置链。
- 相关单元测试补齐并通过，受影响模块测试通过。
- 全量 `go test ./...` 通过。

# 执行变更总结

## 1. 核心修复与调整概述

- 已修复 `prompts.routes` 在归一化阶段静默丢弃空键与空目录的问题，改为保留归一化结果并在校验阶段显式报错，避免提示词路由悄然回退到默认目录。
- 已修复真实模型测试夹具的配置解析路径，使其直接复用运行时布局解析逻辑，支持“打包 `base.yaml` + 用户目录 `config.yaml` 覆盖”的合法场景。
- 已补充针对上述两类回归的自动化测试，并完成定向测试与全量测试验证。

## 2. 📂文件变更清单

### 修改

- `internal/config/loader.go`
- `internal/config/manager.go`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/config/manager_test.go`
- `internal/testutil/realruntime.go`
- `internal/testutil/realruntime_test.go`

### 新增

- 无

### 删除

- 无

## 3. 💻关键代码调整详情

- 在 `internal/config/loader.go` 中调整 `normalizeRouteMap`，不再把空键或空目录直接吞掉；同时新增路由项校验逻辑，统一为配置加载和提示词管理器构造提供显式错误。
- 在 `internal/config/config.go` 中把 `prompts.routes` 非法项校验接入 `Config.Validate`，确保 YAML 加载链在正式启动前就能阻断错误配置。
- 在 `internal/config/manager.go` 中增加对空路由键和空目录的校验，保证即使直接构造 `PromptManager` 也不会静默接受非法路由。
- 在 `internal/testutil/realruntime.go` 中移除“同目录必须同时存在 `base.yaml` 与 `config.yaml`”的假设，改为通过 `ResolvePromptLayout` 复用正式运行时的布局解析与配置链生成逻辑。
- 在测试侧新增 `prompts.routes` 非法项回归用例、`PromptManager` 直构校验用例，以及真实运行时夹具对“打包 base + 用户 override”场景的布局解析用例。

## 4. ⚠️遗留问题与注意事项

- 当前修复针对的是“空键/空目录被静默忽略”和“测试夹具查找顺序错误”两处明确回归，没有扩展修改 YAML 配置迁移的其他行为。
- 真实模型测试夹具现在会更严格地贴近正式运行时布局；如果打包态 `output/configs` 目录存在但内容残缺，测试会像正式运行时一样直接失败，而不是回退到工作区配置。
- 已完成 `go test ./internal/config ./internal/testutil` 与 `go test ./...` 验证。
