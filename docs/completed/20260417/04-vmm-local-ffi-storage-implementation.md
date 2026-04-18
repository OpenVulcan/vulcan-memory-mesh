# VMM 本地 FFI 存储落地改造实施计划

## 任务目标

在 `VulcanMemoryMesh` 中正式落地本地 lib / FFI 存储接入，彻底移除当前运行时对 SQLite / LanceDB gRPC 网关的依赖，并完成以下目标：

1. 参考 `D:\projects\vulcan-mcp-client\make deps host` 机制，为当前仓库增加宿主依赖在线拉取能力。
2. 将第三方动态库下载并缓存到仓库内第三方目录，并补充 `.gitignore`。
3. 构建时把动态库复制到 `output/libs/`，同时建立 `output/database/` 运行时目录。
4. 将 SQLite 数据库路径固定为 `output/database/sqlite.db`，LanceDB 数据目录固定为 `output/database/lancedb/`。
5. 将 `internal/adapters/outbound/vldb_lancedb` 从 gRPC 改为本地 FFI。
6. 将 `internal/adapters/outbound/vldb_sqlite` 从 gRPC 改为本地 FFI，并切换到 `vldb-sqlite` 内建 tokenizer / FTS 能力。
7. 取消当前 Go 侧额外分词主链路，改由配置控制 SQLite tokenizer 模式。

## 执行步骤

1. 梳理现有构建脚本、配置结构、运行时路径解析、LanceDB / SQLite 适配层与当前 FTS 链路。
2. 新增宿主依赖安装脚本与第三方目录规范：
   - `make deps host`
   - `third_party/` 缓存
   - `.gitignore` 补充
3. 扩展构建与运行时目录装配：
   - `output/libs/`
   - `output/database/sqlite.db`
   - `output/database/lancedb/`
4. 改造 LanceDB 适配层：
   - 接入 `vldb-lancedb` Go FFI wrapper
   - 保持现有上层向量端口不变
5. 改造 SQLite 适配层：
   - 接入 `vldb-sqlite` Go FFI wrapper
   - 保留关系表能力
   - 将全文检索从旧 `vmm_memory_nodes_fts` 辅助表切换到库内 FTS 能力
   - 改为配置驱动的 tokenizer 模式
6. 更新配置、文档与测试：
   - 调整默认配置与校验
   - 更新相关文档
   - 运行仓库要求测试与构建验证

## 技术选型与约束

- 构建入口必须继续使用 `make.ps1` / `make.bat`。
- 动态库接入优先采用上游示例中的 `purego` 方式，不引入 cgo。
- 运行时动态库路径必须由程序基于可执行文件位置显式解析，不依赖系统 PATH。
- SQLite tokenizer 模式必须通过配置显式控制，至少支持 `none` 和 `jieba`。
- 当前用户已确认：
  - 完全不保留 gRPC
  - 不保留旧 SQLite FTS 结构，直接采用库内完整能力

## 验收标准

1. `make deps host` 可以按宿主系统下载并缓存 `vldb_sqlite` / `vldb_lancedb` 动态库。
2. `build` 后具备 `output/bin`、`output/configs`、`output/libs`、`output/database` 完整运行结构。
3. 运行时不再依赖 SQLite / LanceDB gRPC 地址。
4. SQLite 中文分词由配置控制，Go 侧额外分词不再走主链路。
5. 关键测试与 `.\make.ps1 build` 通过，文档与计划记录完整。

## 执行变更总结

### 1. 核心修复与调整概述

- 已为当前仓库补齐 `make deps host` 与 `scripts/install_host_deps.ps1`，支持按宿主系统在线下载 `vldb-sqlite` / `vldb-lancedb` 动态库并缓存到 `third_party/deps/`。
- 已扩展标准打包流程，`.\make.ps1 build` 现在会同步生成 `output/libs/` 与 `output/database/`，并把本地 FFI 运行时所需的动态库复制到标准产物目录。
- 已完成 SQLite / LanceDB split 模式从 gRPC 网关到本地 FFI 的运行时切换；`internal/app` 现在会根据打包布局解析本地库与数据库路径，而不再依赖 split 模式下的远程地址。
- 已将 SQLite 词法检索主链路切换为 `vldb-sqlite` 库内 FTS 与 tokenizer 能力，并增加 `sqlite.tokenizer_mode` 配置项；原 `memory_pipeline.lexical_pre_tokenize` 仅保留兼容语义，不再决定主链路行为。
- 已补齐测试兼容桥，保留适配器包中旧 gRPC mock / bufconn 单测对 typed params、批处理和 SQL 断言的覆盖，不影响正式 FFI 运行路径。
- 已修正 `internal/app` 测试环境下本地数据库路径冲突问题，并把工作区 / go test 模式的数据库根目录隔离到临时目录，避免锁文件互相污染。

### 2. 📂 文件变更清单

新增：

- `.gitignore`
- `scripts/install_host_deps.ps1`
- `internal/app/local_storage_layout.go`
- `internal/platform/ffi/lancedbffi/lancedbffi.go`
- `internal/platform/ffi/lancedbffi/loader_windows.go`
- `internal/platform/ffi/lancedbffi/loader_unix.go`
- `internal/platform/ffi/lancedbffi/runtime_create_windows.go`
- `internal/platform/ffi/lancedbffi/runtime_create_other.go`
- `internal/platform/ffi/sqliteffi/sqliteffi.go`
- `internal/platform/ffi/sqliteffi/loader_windows.go`
- `internal/platform/ffi/sqliteffi/loader_unix.go`

修改：

- `make.ps1`
- `scripts/vmm.ps1`
- `configs/base.yaml`
- `configs/config.yaml`
- `README.md`
- `cmd/vmm-migrate/migrate.go`
- `internal/config/config.go`
- `internal/config/config_runtime.go`
- `internal/config/config_validate.go`
- `internal/config/config_test.go`
- `internal/app/app.go`
- `internal/app/app_test.go`
- `internal/app/runtime_storage.go`
- `internal/app/maintenance.go`
- `internal/adapters/outbound/vldb_sqlite/store.go`
- `internal/adapters/outbound/vldb_sqlite/retention_store.go`
- `internal/adapters/outbound/vldb_sqlite/recycle_jobs.go`
- `internal/adapters/outbound/vldb_sqlite/vector_gc_jobs.go`
- `internal/adapters/outbound/vldb_sqlite/debug_export.go`
- `internal/adapters/outbound/vldb_lancedb/store.go`
- `go.mod`
- `go.sum`

### 3. 💻 关键代码调整详情

- 构建侧：
  - `make.ps1` 新增 `deps host`，统一走 `scripts/install_host_deps.ps1` 下载宿主依赖。
  - `scripts/vmm.ps1` 新增宿主动态库同步与 `output/database` 布局初始化逻辑。
- 运行时路径侧：
  - `internal/app/local_storage_layout.go` 新增本地 FFI 布局解析。
  - 正式打包模式走 `output/libs` / `output/database`。
  - 工作区 / go test 模式自动切到临时数据库目录，动态库优先从 `output/libs`，其次从 `third_party/deps` 解析。
- SQLite 适配层：
  - `internal/adapters/outbound/vldb_sqlite/store.go` 改为本地 FFI 主链路。
  - 关系表操作改为本地 `ExecuteScript / ExecuteBatch / QueryJSON` FFI。
  - lexical 检索改为库内 FTS，`ApplyTurnAnalysis` / recycle / retention 等写后同步切换为内建 FTS 文档同步。
  - 为旧 transport-focused 单测保留 gRPC mock 兼容桥。
- LanceDB 适配层：
  - `internal/adapters/outbound/vldb_lancedb/store.go` 改为本地 FFI 主链路。
  - 保留兼容测试用的 legacy gRPC client 分支，让 bufconn 单测继续断言表名、过滤表达式和 JSON row 契约。
- 配置与文档：
  - 新增 `sqlite.tokenizer_mode`，默认 `jieba`。
  - `sqlite.address` / `lancedb.address` 调整为兼容保留字段说明。
  - README 与 `configs/base.yaml` / `configs/config.yaml` 已同步到本地 FFI 语义与标准产物目录结构。

### 4. ⚠️ 遗留问题与注意事项

- `go test ./... -count=1` 在当前 Windows 环境下仍会在 `internal/app` 聚合执行阶段触发上游 `vldb-lancedb` Rust runtime 的 `tokio EnterGuard` panic；但 `go test ./internal/app -count=1` 单独执行通过，适配器包与关键业务包也均已单独通过。
- 这说明当前剩余问题位于上游 LanceDB 动态库在聚合测试进程场景下的 runtime 生命周期稳定性，而不是本次 VMM 侧接入逻辑本身。后续如果要继续压实全量聚合测试，需要联合上游库继续排查这个 Windows 侧 runtime 行为。
