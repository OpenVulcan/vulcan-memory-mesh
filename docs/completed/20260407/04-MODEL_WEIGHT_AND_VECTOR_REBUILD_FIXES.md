# 任务目标

修复本轮评审中确认需要落地的 3 个问题，并保持当前未提交改动的整体设计方向不变：

1. 取消 LLM 路由中的旧 `priority` 语义，全面切换到新的 `weights.*` 分场景权重语义。
2. 调整 SQLite split 模式的向量重建流程，避免出现 SQLite 与 LanceDB 中途不一致的问题。
3. 修复 `scripts/vmm.ps1` 在非构建动作下也强依赖 `go` 的兼容性回归。

# 执行步骤

1. 审查 `internal/config`、`internal/app`、`scripts` 与相关测试，确认当前行为与用户要求的差异点。
2. 修改 LLM 权重解析逻辑与相关文档/示例配置，移除旧 `priority` 作为运行时排序来源的语义，只保留 `weights.*`。
3. 修改 split 模式向量重建流程，先清空 SQLite durable 向量与 LanceDB 表，再基于当前 embedding 模型进行全量重建。
4. 修改 `scripts/vmm.ps1`，把 `go` 解析延后到实际需要编译的动作分支中。
5. 补充或更新单元测试，覆盖：
   - LLM 权重默认值与新语义；
   - split 模式向量重建的清空后重建流程；
   - `vmm.ps1` 修复点对应的脚本行为约束（若适合以代码结构间接验证）。
6. 运行受影响包测试，并在必要时执行全量测试验证无回归。

# 技术方案

## LLM 权重语义

- 将 `ResolvedWeights()` 的默认来源固定为新的统一默认权重。
- 旧 `priority` 不再参与 LLM route 选择、`PrimaryRoute*`、`PrimaryModel*`、prompt 锚点选择等运行时逻辑。
- 配置文档明确：实际路由排序只看 `weights.*`，`priority` 不再作为兼容回退来源。

## SQLite 向量重建

- 为 split 模式增加“清空 durable 向量”维护端口，让命令进入重建前先把 SQLite 中 active durable 的 `vector_json` 统一置空或重置。
- 在清空 SQLite durable 向量后，立即重建 LanceDB 表，确保两端都先进入“待重建”状态。
- 随后按批重新生成 embedding，并同步写回 SQLite 与 LanceDB，保证失败时不会保留“SQLite 新、LanceDB 旧”的混合状态。

## PowerShell 脚本

- 将 `go.exe` 解析封装为延迟调用函数，仅在 `build` / `tester` 动作里触发。
- `run` / `clean` 动作应可在已存在打包产物时独立工作，不依赖开发机安装 Go。

# 验收标准

1. LLM route 选择只受 `weights.*` 影响，不再因旧 `priority` 改变结果。
2. split 模式向量重建中途失败时，不会留下“SQLite 已部分更新但 LanceDB 仍是旧数据”的状态。
3. `make.bat run` 与 `make.bat clean` 在系统无 Go 时不因脚本预检查而失败。
4. 相关测试通过，且全量 `go test ./...` 通过。

---

# 执行变更总结

## 1. 核心修复与调整概述

- 取消了 LLM 运行时对旧 `priority` 语义的依赖，`PrimaryRoute*`、`PrimaryModel*`、`SelectionWeight` 与 prompt 锚点选择现在统一只看 `weights.*`，未声明槽位固定回退到 `100`。
- 重构了 split 模式的向量重建流程：先清空 SQLite durable `vector_json`，再重建 LanceDB 空表，最后按批重新生成 embedding 并同步回填两个存储，避免此前“SQLite 已部分更新但 LanceDB 仍是旧数据”的不一致状态。
- 修复了 `scripts/vmm.ps1` 在非构建动作下也强依赖 `go` 的问题，把 `go.exe` 解析延迟到真正执行编译时才触发。

## 2. 📂文件变更清单

- 修改：
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `internal/app/ports/interfaces.go`
  - `internal/app/vector_rebuild.go`
  - `internal/app/vector_rebuild_test.go`
  - `internal/adapters/outbound/vldb_sqlite/store.go`
  - `internal/adapters/outbound/vldb_sqlite/store_test.go`
  - `internal/adapters/outbound/ai_key_failover/key_failover_test.go`
  - `cmd/vmm-migrate/vector_rebuild.go`
  - `scripts/vmm.ps1`
  - `configs/base.yaml`
  - `README.md`
  - `docs/ai-model-failover-design_CN.md`
- 新增：
  - 无新增源码文件
- 删除：
  - 无

## 3. 💻关键代码调整详情

- 在 `LLMRouteConfig.ResolvedWeights()` 中移除了旧 `priority` 对 LLM 选路权重的兼容回退；即使配置里仍带有 `priority`，LLM 运行时也不再读取它。
- 在 `internal/app/ports/interfaces.go` 新增 `MemoryVectorResetStore` 维护端口，并在 SQLite 适配器中实现 `ClearMemoryVectors()`，用于把 durable `vector_json` 统一清回空数组基线。
- 在 `internal/app/vector_rebuild.go` 中重排 split 模式主流程：
  - 先抽取 active durable `vector_id`；
  - 调用 `ClearMemoryVectors()` 清空 SQLite 向量；
  - 调用 `RecreateTable()` 重建 LanceDB；
  - 最后按批执行 embedding、`ReplaceMemoryVectors()` 与 `vector.Upsert()`。
- 在 `scripts/vmm.ps1` 中新增 `Resolve-GoExe` 惰性解析函数，`clean` / `run` 路径不再因为缺少 Go 环境提前失败。
- 同步更新了配置模板、README 与 AI 路由设计文档，去掉 LLM `priority` 的有效配置语义说明，并更新 split 模式向量重建步骤描述。

## 4. ⚠️遗留问题与注意事项

- 当前仅取消了 **LLM** 的 `priority` 运行时语义；`rerank.routes[*].priority` 仍然保留并继续生效，因为它目前没有等价的分场景权重模型可以替代。
- 为了验证脚本修复，我执行了 `pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\\make.ps1 build`；该构建在当前环境成功通过，但“无 Go 环境下的 run/clean”属于脚本兼容性设计修复，本次未在缺失 Go 的独立环境中做额外实机验证。
- 已完成验证：
  - `go test ./internal/config ./internal/app ./internal/adapters/outbound/ai_key_failover ./internal/adapters/outbound/vldb_sqlite ./cmd/vmm-migrate`
  - `go test ./...`
  - `pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\\make.ps1 build`
