# 独立原生存储说明

本文说明 `storage.mode=native` 的配置、随包布局、依赖制备和离线迁移边界。native 是本地存储模式，运行时不启动 `vldb-controller`，也不读取旧的 Vulcan Code 托管清单。

## 存储模式

本地 SQLite/LanceDB 后端有三种模式：

- `split`：默认模式。VMM 进程通过 legacy 动态库使用 `output/database/sqlite.db` 和 `output/database/lancedb/`。
- `controller`：SQLite 与 LanceDB 仍使用 legacy 数据布局，但由独立 `vldb-controller` 进程持有句柄。
- `native`：VMM 进程内使用 native SQLite 与原生 LanceDB 薄 ABI，数据根与旧 split 数据隔离。

`combined` 是独立的 PostgreSQL 组合模式，不属于 native SQLite/LanceDB 三种模式。切换模式不会自动把旧数据原地转换；需要使用离线迁移流程。

## 配置

最小 native 覆盖可以从 [`configs/native.config.example.yaml`](../configs/native.config.example.yaml) 开始：

```yaml
storage:
  mode: "native"

relational:
  provider: "sqlite"

vector:
  provider: "lancedb"

sqlite:
  timeout: "5s"
  native:
    path: "database/native/sqlite.db"
    tokenizer: "gse"

lancedb:
  timeout: "5s"
  table_name: "vmm_memory_vectors"
  vector_column: "vector"
  native:
    path: "database/native/lancedb"
    library_path: ""
```

在正式打包布局中，相对的 `sqlite.native.path` 和 `lancedb.native.path` 都相对于标准 `output/` 根目录解析：

- SQLite 默认文件：`output/database/native/sqlite.db`
- LanceDB 默认目录：`output/database/native/lancedb/`
- `lancedb.native.library_path` 留空时由应用选择 `output/libs/` 下当前平台的库

平台库名由 ABI 契约固定为 `vmm_lancedb_native.dll`、`libvmm_lancedb_native.so` 或 `libvmm_lancedb_native.dylib`。显式填写 `library_path` 时，打包布局下的相对路径也相对于 artifact/output 根解析；路径必须指向明确的 native 库。SQLite 文件与 LanceDB 目录不能互相包含。

原生库的随包契约固定为 manifest schema `1`、C ABI `1` 和 LanceDB 引擎 `0.39.0`。打包阶段校验 manifest、库文件及支持文件的源码摘要、target 和哈希；运行时加载阶段校验导出符号、ABI、引擎版本、原子更新、L2 预过滤和超时能力，不读取打包 manifest 重新计算文件哈希。

`sqlite.timeout`、`lancedb.timeout`、`lancedb.table_name`、`lancedb.vector_column` 和 `embedding.dimension` 是各本地存储模式共用的字段。native SQLite 分词器与旧模式分开校验：

- native：`sqlite.native.tokenizer` 只接受 `gse` 或 `unicode61`，默认 `gse`
- split/controller：`sqlite.tokenizer_mode` 只接受 `jieba` 或 `none`

native SQLite 使用 modernc SQLite 适配器。`gse` 通过 GSE 词典生成 FTS5 的词法文本，`unicode61` 使用 SQLite FTS5 的 Unicode 分词器。原始关系字段仍单独保存，分词文本属于可重建的索引投影。

配置错误会在启动或配置加载阶段直接失败：未知的 `storage.mode`、不支持的 tokenizer、非法路径、库文件缺失、ABI 或引擎能力不匹配都不会静默回退到 split 或 controller。manifest 不匹配由打包阶段拒绝。

### 环境变量

native 字段对应的环境变量为：

- `VMM_SQLITE_NATIVE_PATH`
- `VMM_SQLITE_NATIVE_TOKENIZER`
- `VMM_LANCEDB_NATIVE_PATH`
- `VMM_LANCEDB_NATIVE_LIBRARY_PATH`

它们遵循当前分层配置的 opt-in 规则：只有已加载配置层显式引用 `${VMM_...}` 时才会覆盖对应字段。旧的 `VMM_SQLITE_TOKENIZER_MODE` 仍只服务于 split/controller 的旧字段。

## 原生依赖与日常构建

原生 LanceDB 依赖使用仓库内 `native/lancedb` 的 C ABI 薄库，锁定官方 LanceDB 引擎 `0.39.0`。Cargo 只在显式制备依赖时执行：

Windows：

```powershell
.\make.ps1 deps native
```

Unix：

```bash
./scripts/build_native_deps.sh
```

制备结果位于 `third_party/deps/native_lancedb/<platform>/`。每个源码摘要目录包含库文件和 `manifest.json`，同级 `current.json` 只选择一个已校验的源码摘要；manifest 记录 ABI、引擎版本、Cargo lock 摘要、target 和库文件哈希。也可以只校验已有选择而不运行 Cargo：

```powershell
.\scripts\build_native_deps.ps1 -VerifyOnly
```

```bash
./scripts/build_native_deps.sh --verify-only
```

日常构建仍使用标准入口：

```powershell
$env:VMM_BUILD_STORAGE_PROFILE = "native"
.\make.ps1 build
```

```bash
VMM_BUILD_STORAGE_PROFILE=native ./scripts/vmm.sh build
```

`VMM_BUILD_STORAGE_PROFILE` 接受 `legacy`、`native`、`all`，未设置时保持 `legacy`。`build` 和 `build release` 只进行 Go 编译、配置同步、库复制和 manifest/ABI 校验，不会自动 Cargo 编译；缓存缺失或校验失败时直接报错。

这条 profile 规则不会改变默认 split：未设置 profile 时仍只准备 legacy 动态库和原有 `output/database/sqlite.db`、`output/database/lancedb/`。只有显式选择 `native` 或 `all`，并且先用 `make.ps1 deps native` / `build_native_deps.sh` 制备出对应缓存，标准构建才会把 native 库和 manifest 放入 `output/libs/`。

### 成对 marker 与 embedding identity

native 存储在打开后端前先校验以下磁盘身份：

- SQLite 旁边的 `<sqlite>.pair.json` 与 LanceDB 目录中的 `.vmm-pair.json` 是同一对 marker，必须同时存在，并且 `format_version` 与 `pair_id` 完全一致。新建空组合会一次生成同一 `pair_id`；只出现一侧、两侧身份不一致或路径不是新组合时直接失败。
- SQLite 旁边的 `<sqlite>.embedding.json` 是无凭据 identity sidecar，包含 provider、model、dimension、embedding params 摘要、当前 model params 摘要、规范化 endpoint 摘要和有序 routing 节点拓扑摘要。endpoint、API key、限额等敏感或易变内容不会以明文写入。
- 现有 SQLite 缺少 embedding sidecar、sidecar 没有对应数据库、pair marker 不完整，或合并后的配置与已有 identity 不一致时，普通启动拒绝打开 native 存储，不会静默重建或切换 provider。

`<sqlite>.migration-incomplete` 表示迁移尚未通过复制、FTS、向量和健康检查；`<sqlite>.vector-rebuild-incomplete` 表示向量重建尚未完成。任一 marker 存在时，普通服务和普通维护入口都会拒绝启动。只有显式维护流程可以读取重建中的目标，成功完成所有核验、schema 登记和 identity 更新后才会清除 marker。

## 离线迁移

迁移前必须停止 `vmm-local` 和会写入源数据的 controller，并保留 SQLite/LanceDB 快照。目标必须是新建且为空的 native 输出目录，迁移工具不会原地覆盖旧数据：

```powershell
.\output\bin\vmm-migrate.exe -migrate split-to-native -native-output <new-empty-dir> -confirm-migrate
.\output\bin\vmm-migrate.exe -migrate controller-to-native -native-output <new-empty-dir> -confirm-migrate
```

`split-to-native` 只适用于当前源模式为 `split`，`controller-to-native` 只适用于当前源模式为 `controller`。迁移完成后应先检查 `migration-report.json` 和 `native-storage-override.fragment.yaml`，再合并配置片段。fragment 文件头已经明确写出它不能直接作为 `-config` 参数；必须把它合并回原配置覆盖根中的原文件，并保留原 embedding provider、model、endpoint、routing nodes、params、model params 以及 prompts、pii_rules、noise_rules 覆盖。

迁移的事实边界如下：

- 关系事实、来源标识、memory ID、turn ID 和已有向量 ID 按原值迁移
- FTS 表和 token 文本是派生数据，不直接复制；native SQLite 应从持久化原文重新生成 FTS
- LanceDB 向量只有在模型和 `embedding.dimension` 都匹配时才可复用
- 迁移本身不自动调用 embedding 服务；需要换模型或维度时，另行执行受控的向量重建
- 迁移会同时保存 SQLite/LanceDB 成对 pair marker，以及 embedding provider、model、dimension、params、model params、endpoint 和 routing topology 摘要；任一 pair marker 不成对或 embedding identity 错配都必须拒绝启动
- 可恢复 trash 行的关系数据和旁路向量会一并保留；迁移报告记录 `restorable_trash_rows`、向量总数、重复向量和缺失向量的计数，恢复动作可以重新使用这些 trash 事实及其向量
- 目标目录、库路径和 tokenizer 有任何不合法或不匹配，迁移必须失败并保留源数据

迁移中的 `sqlite.db.migration-incomplete` 标记会隔离未完成目标；普通服务启动和普通维护入口都会拒绝该目标。失败后目标目录保留供诊断，修复后必须重新使用新的空目标目录执行迁移，不会自动切换到该目标。

`-fts-rebuild` 是 native SQLite FTS 重建维护命令，当前参数和调度入口已接入：

```powershell
.\output\bin\vmm-migrate.exe -fts-rebuild
```

该命令只适用于 `storage.mode=native`，运行前必须停写；它从持久化原文重建 SQLite FTS，不改变 embedding identity。当前仓库已有参数互斥和模式校验，但仍需在目标平台的正式打包库上执行运行验收，本文不把静态入口或 Go 测试写成跨平台动态库已通过。

### 向量重建与未完成隔离

显式 `-vector-rebuild -confirm-vector-rebuild` 会在 native 向量表重建前捕获一份固定 UTC 观察时刻，并用同一时刻筛选 active 事实与 eligible restorable trash，随后写入 `<sqlite>.vector-rebuild-incomplete`。重建会对 live 与 trash 合并后的唯一向量事实只生成一次 embedding；active 行写回关系库，trash 行按精确 `(batch_id,memory_id)` 更新，唯一向量写入 LanceDB。流程会核验关系 payload、trash 身份与 payload、向量维度、物理向量行数和 schema 版本，全部成功并更新 embedding identity 后才删除 marker。

如果 reset 后的中途步骤失败，native 流程会在有界恢复窗口内使用同一份快照自动修复；修复或后续发布失败时保留 marker，普通启动拒绝服务，运维必须先完成显式恢复流程。当前集成测试已覆盖 live 与 restorable trash 的重建、重启后的向量检索以及恢复 trash 批次。

### clean 后的操作顺序

`vmm-migrate -clean sqlite`、`-clean lancedb` 或 `-clean sqlite,lancedb` 是破坏性操作，native 模式通过同一个 owner 执行，不能让服务进程同时写入。SQLite 清理删除受管 schema、FTS 派生对象和版本元数据；LanceDB 清理删除配置的向量表。清理完成后命令立即退出，pair marker 和 embedding identity 保留。

SQLite schema 和 FTS 会在后续打开关系库时重新建立。已登记的 LanceDB 表被删除后，普通启动会拒绝缺失表，必须先执行 `vmm-migrate -vector-rebuild -confirm-vector-rebuild`：只清理 LanceDB 时从保留的关系事实重建；两端都清理时显式建立空向量表。不要把单独清理 SQLite 当作完整存储清空，因为 LanceDB 的物理旧向量仍然存在；需要全清时选择两端并完成上述维护重建。

## 托管配置退役

`-vulcan-managed-config` 已从 standalone 运行时和维护工具移除；传入该参数会直接报错。请使用随包 `output/configs/base.yaml`、项目覆盖层、`~/.vmm` 或 `-config` 指定的独立覆盖根。旧托管清单字段不能混入普通配置，未知字段会在加载阶段拒绝。

详见 [Vulcan Code 托管运行退役与迁移说明](./VULCAN_CODE_MANAGED_RUNTIME.md)。

## 验证边界

当前只有 Windows amd64 的正式打包动态库完成真实加载、中文写入/检索和向量写入/查询验证，使用的库名是 `vmm_lancedb_native.dll`。Linux amd64/arm64 与 macOS amd64/arm64 目前只有 `CGO_ENABLED=0` 的 Go 交叉构建证据，不能表述为这些平台的动态库运行通过。

配置单元测试、native adapter 测试和构建脚本的静态校验只能证明各自的代码契约。它们不等于每个平台的真实动态库加载、LanceDB ABI 兼容性或带历史数据的迁移验收；每个目标平台仍需在准备对应缓存和库文件后单独执行正式打包验收。
