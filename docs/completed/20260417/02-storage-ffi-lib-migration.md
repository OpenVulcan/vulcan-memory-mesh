# SQLite / LanceDB 本地 lib 直连改造实施计划

## 任务目标

在 VulcanMemoryMesh 中彻底移除当前基于 gRPC 的 SQLite / LanceDB 存储对接方式，改为直接加载本地动态库进行 FFI 调用，并完成以下目标：

1. 参考 `D:\projects\vulcan-mcp-client\make.ps1` 所采用的 `make deps host` 模式，为当前仓库增加宿主依赖在线拉取能力。
2. 将第三方动态库下载并缓存到仓库内统一的第三方目录，同时补充 `.gitignore` 管理规则。
3. 在标准构建流程中，把所需动态库复制到运行目录上层的 `output/libs/`。
4. 将 SQLite 与 LanceDB 运行数据目录固定为：
   - `output/database/sqlite.db`
   - `output/database/lancedb/`
5. 将现有 SQLite / LanceDB outbound adapter 从 gRPC client 改造为 lib / FFI 直连实现。
6. 取消当前 Go 侧额外分词器主链路，改为完全使用 `vldb-sqlite` 提供的内建分词与 FTS 能力，并通过配置文件决定是否启用中文分词器。

## 执行步骤

1. 梳理当前构建脚本、配置结构、运行时路径解析、SQLite / LanceDB 适配层与测试覆盖位置。
2. 引入宿主依赖安装脚本与第三方目录规则：
   - 新增 `make deps host` 能力
   - 新增 `third_party/` 目录规范与 `.gitignore`
   - 明确不同平台动态库产物的落盘与复制规则
3. 扩展构建与运行时布局：
   - 构建后复制动态库到 `output/libs/`
   - 运行时统一解析 `output/libs/` 与 `output/database/`
   - 创建或确保 `sqlite.db` 与 `lancedb` 数据目录可用
4. 改造 LanceDB 适配层：
   - 使用 `vldb-lancedb` 提供的 lib / FFI 能力替换 gRPC client
   - 保持上层向量存储接口不变
5. 改造 SQLite 适配层：
   - 使用 `vldb-sqlite` 提供的 lib / FFI 能力替换 gRPC client
   - 改为使用库内 FTS / tokenizer / 自定义词典能力
   - 清理或下线当前 Go 侧预分词主链路
6. 重构配置项：
   - 新增或调整本地 lib 路径、数据库路径、分词模式配置
   - 去除对 gRPC 地址配置的运行时依赖
7. 更新文档并执行验证：
   - 运行仓库要求的关键测试
   - 运行 `.\make.ps1 build`
   - 记录执行变更总结并归档计划文件

## 技术选型与约束

- 构建入口必须继续使用现有 `make.ps1` / `make.bat`，不能绕开标准打包流程。
- 动态库接入优先采用外部库示例中的 Go `purego` 路径，避免引入 cgo 编译链复杂度。
- 运行时动态库必须由程序基于可执行文件路径显式解析并加载，不能依赖系统级 PATH。
- SQLite 分词器模式必须显式由配置控制，至少支持 `none` 与 `jieba` 两种模式。
- 由于您已经确认：
  - 完全不保留 gRPC
  - 不保留当前 SQLite FTS 结构，直接采用完整库能力
  因此本次实施将按“彻底切换”执行，而不是双模兼容。

## 验收标准

1. `make` 流程可以按宿主系统下载并缓存所需动态库依赖。
2. `build` 产物除 `output/bin`、`output/configs` 外，还能正确生成或装配 `output/libs` 与 `output/database` 运行结构。
3. 运行时不再依赖 SQLite / LanceDB gRPC 地址，而是改用本地动态库与本地数据库目录。
4. SQLite 中文分词是否启用可通过配置文件决定，且 Go 侧额外分词器不再作为主链路参与写入和检索。
5. 关键测试与构建验证通过，文档与计划记录完整。

## 执行变更总结

### 1. 核心修复与调整概述

本次改造已经完成 SQLite / LanceDB 从外部 gRPC 网关到本地 FFI 动态库的整体切换，并同步收敛了构建、运行布局、分词链路、维护工具和测试体系。当前 `split` 模式正式运行时已改为通过 `output/libs/` 加载 `vldb-sqlite` / `vldb-lancedb`，数据库位置固定为 `output/database/sqlite.db` 与 `output/database/lancedb/`，Go 侧旧预分词主链路已移除，存储相关 gRPC 残留和 proto 生成物也已删除。

### 2. 📂文件变更清单

新增：
- `scripts/install_host_deps.ps1`
- `internal/platform/ffi/sqliteffi/`
- `internal/platform/ffi/lancedbffi/`
- `internal/app/local_storage_layout.go`
- `internal/adapters/outbound/vldb_sqlite/store_fake_test.go`
- `internal/adapters/outbound/vldb_lancedb/engine_fake_test.go`
- `internal/app/usecase/memory_query_sqlite_integration_test.go`

修改：
- `make.ps1`
- `scripts/vmm.ps1`
- `.gitignore`
- `README.md`
- `configs/base.yaml`
- `configs/config.yaml`
- `cmd/vmm-migrate/clean.go`
- `cmd/vmm-migrate/main.go`
- `internal/app/runtime_storage.go`
- `internal/app/maintenance.go`
- `internal/adapters/outbound/vldb_sqlite/*`
- `internal/adapters/outbound/vldb_lancedb/*`
- `internal/config/*`
- `internal/app/usecase/*`
- `internal/app/app_test.go`

删除：
- `internal/platform/textutil/lexical_tokenizer.go`
- `internal/platform/textutil/lexical_tokenizer_test.go`
- `internal/adapters/outbound/vldb_sqlite/proto/v1/*`
- `internal/adapters/outbound/vldb_lancedb/proto/v1/*`

### 3. 💻关键代码调整详情

- 构建侧增加 `make deps host` 与 `third_party/deps` 缓存逻辑，打包阶段会把动态库同步到 `output/libs/`。
- 运行时通过 `local_storage_layout` 统一解析 `output/libs/` 和 `output/database/`，不再依赖 SQLite / LanceDB 的远程 address。
- SQLite 适配层切换到本地 `vldb-sqlite` FFI，关系 SQL、FTS 与分词都走库内能力，配置开关统一为 `sqlite.tokenizer_mode`。
- LanceDB 适配层切换到本地 `vldb-lancedb` FFI，向量写入、检索、删除和维护工具全部改为本地动态库调用。
- 删除存储侧 gRPC fallback、proto 生成代码以及旧 Go 预分词主链路，相关测试改为本地 fake/FFI 形态，并补充真实 SQLite FTS 集成测试。
- SQLite schema 体系冻结为基线 `19`，保留未来 `19 -> 20` 这类增量升级框架，但移除历史版本兼容迁移链。

### 4. ⚠️遗留问题与注意事项

- `vldb-lancedb` 的 FFI runtime 生命周期问题已在上游修复并发布到 `v0.1.5`，当前 VMM 的 `go test ./... -count=1` 已恢复全绿。
- 当前仍有部分说明文档与仓库提交尚未统一收口，这不影响功能，但需要通过后续正式提交完成仓库状态清理。
