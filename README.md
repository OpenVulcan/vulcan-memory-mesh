# VMM OSS Local (Go)

当前已发布版本：**v0.1.0**，其发行包仅包含 native 存储依赖。后续全模式发行流程支持手动选择 Git 标签构建 Windows x64、Linux x64/ARM64、macOS Intel/ARM64 完整包；每个平台新包都包含 native、split、controller 依赖及 combined PostgreSQL 模式所需的 VMM 代码。新包必须重新构建、验签并发布后才能由独立安装器提供全部存储选项，详见 [GitHub 发行说明](docs/github-release-guide_CN.md)。

VulcanMemoryMesh 当前主线只保留本地版、gRPC 版和三条核心业务链：

- `PreCheck`
- `ChatCompact`
- `PostAction`

当前运行时的定位是：

- 用 `project_id + user_id + session_id` 做确定性层级寻址
- 默认 `split` 模式使用 SQLite 保存关系数据、用 LanceDB 保存向量数据
- `storage.mode=controller` 保持 SQLite/LanceDB 接口与数据布局不变，但由单个 `vldb-controller` 进程统一持有两套数据库句柄
- `storage.mode=native` 使用进程内的 modernc SQLite 与原生 LanceDB 薄 ABI，数据与旧 split 根目录隔离
- 显式切换 `storage.mode=combined` 后，会改为由 PostgreSQL 统一承载关系与向量能力
- 由 Caddy 等外部反向代理负责 TLS

## 标准构建与运行

标准构建入口请使用：

```powershell
.\make.bat build
.\make.bat build release
```

如果您使用 PowerShell 脚本入口，也请保持同样的标准打包方式：

```powershell
.\make.ps1 build
.\make.ps1 build release
```

说明：

- 不要直接使用 `go build` 绕过打包脚本。
- 正式运行产物位于 `output/bin/`。
- 如需手工启动，请先进入 `output/bin/`，再运行其中的 `vmm-local(.exe)`。
- 不要从仓库根目录直接运行临时可执行文件。

## 独立安装器所用运行时命令

标准包的 `bin/vmm-local(.exe)` 提供以下只读命令，供 VMMM 在配置提交前后调用：

```text
vmm-local config schema --json
vmm-local config validate --config <绝对配置目录或 YAML 文件> --json
vmm-local config show-effective --config <绝对配置目录或 YAML 文件> --json
vmm-local health --config <绝对配置目录或 YAML 文件> --json
```

`config validate` 复用运行时配置加载和规则资源编译，并以脱敏诊断返回结果；它不启动数据库或供应商网络请求。`health` 只探测本机回环地址上的 VMM gRPC `Healthz`，用于启动后的状态判断。

`config show-effective` 使用同一配置加载器，展示系统底座、包内覆盖、用户覆盖及当前进程环境合并并归一化后的值。输出为版本化 JSON，省略 schema 标注的 API 密钥、管理令牌、载荷加密密钥和数据库 DSN（包含路由及节点内的密钥池）；不会写回配置或启动数据库。它反映执行命令时的环境，服务账户环境不同则应在相同环境中检查；其他普通字段仍显示原值。

供应商在线测试需显式允许网络，可能产生费用：`vmm-local config test-provider --config <配置根> --purpose llm --route 0 --allow-network --json`。用途可选 `llm`、`embedding`、`rerank`；路由下标从零开始，embedding 只接受零。命令使用当前配置的真实客户端和密钥故障转移策略，总时限二十秒，只发送固定测试内容，不创建数据库，不输出供应商回复或凭据。网络、认证或供应商请求失败与静态配置校验分开；成功只说明所选路由此次请求成功，不证明所有路由或密钥可用。

服务安装时应明确传入安装器保存的配置根。Linux/macOS 还必须用 `-user` 指定已确认的本机账户；配置、数据及程序路径须可由该账户访问。Windows 不传 `-user`：

```text
# Linux/macOS
vmm-local service install VulcanMemoryMesh -config <绝对配置目录> -user <本机账户> -auto-start=true
# Windows
vmm-local service install VulcanMemoryMesh -config <绝对配置目录> -auto-start=true
vmm-local service status VulcanMemoryMesh
vmm-local service restart VulcanMemoryMesh
vmm-local service disable VulcanMemoryMesh
vmm-local service enable VulcanMemoryMesh
```

`storage.mode=split` 或 `controller` 可设置 `storage.local_data_root` 指定 SQLite 与 LanceDB 的本地数据根。字段留空时继续使用旧版程序根下的 `database/`。`native` 仍分别使用 `sqlite.native.path` 与 `lancedb.native.path`；`combined` 使用 `postgres.dsn`。新根可在首次安装或旧根无数据时直接配置；旧根已有数据时，配置检查和启动都会拒绝直接切换，程序不会因修改 YAML 自动搬迁数据。迁移须先停止所有 VMM 进程并保留完整备份，将旧根的 `sqlite.db`、SQLite 日志文件、`lancedb/` 及迁移标记作为同一组数据迁到新根，确认旧根不再包含活动数据，再设置新路径并运行 `vmm-local config validate --config <配置根> --json`。迁移期间不可同时启动旧库和新库；若检查失败，恢复备份与原配置后再启动。

## 文档导航

- [当前架构与存储模型（中文）](./docs/hierarchy-grpc-design_CN.md)
- [独立原生存储说明（中文）](./docs/native-storage-guide_CN.md)
- [Vulcan Code 托管运行退役与迁移说明](./docs/VULCAN_CODE_MANAGED_RUNTIME.md)
- [gRPC 对接说明（中文）](./docs/grpc-integration-guide_CN.md)
- [gRPC 接口测试说明（中文）](./docs/api-test-guide_CN.md)
- [post-action 接口说明（中文）](./docs/post-action-guide_CN.md)
- [记忆准入噪声门说明（中文）](./docs/noise-gate-guide_CN.md)
- [PII 规则配置说明（中文）](./docs/pii-validator-config_CN.md)
- [PII Rule Configuration Guide (English)](./docs/pii-validator-config_EN.md)
- [PII 引擎开发说明（中文）](./docs/pii-validator-developer_CN.md)
- [PII Validator Developer Guide (English)](./docs/pii-validator-developer_EN.md)
- [PII 国家与地区规则矩阵（中文）](./docs/pii-country-matrix_CN.md)
- [冷数据回收治理说明（中文）](./docs/retention-governance-guide_CN.md)
- [当前未接入主运行时的配置参数清单（中文）](./docs/unused-config-parameters_CN.md)
- [画像节点生命周期与渲染方案（中文）](./docs/profile-node-lifecycle_CN.md)
- [画像 gRPC 查询与手工指令接口（中文）](./docs/profile-grpc-interfaces_CN.md)

## PII 规则开发与验证

当前仓库保留一套独立的 PII 规则引擎和测试器，主要用于规则研发、误杀排查和离线验证；同一套规则也会在主运行时的 `precheck` / `postaction` / `WriteMemories` 首环节执行请求级脱敏：

- 规则实现位于 `internal/platform/pii`
- 独立测试入口位于 `cmd/vmm-pii-tester`
- 系统规则目录固定为 `configs/pii_rules`
- 用户覆盖目录固定为 `~/.vmm/pii_rules` 或 `-config` 指向根目录下的 `pii_rules`

可以直接使用以下命令编译测试器：

```powershell
.\make.ps1 tester
```

也可以按标准构建一起产出：

```powershell
.\make.ps1 build
```

说明：

- 当前主线 gRPC 运行时已在 `PreCheck`、`PostAction` 和 `WriteMemories` 的首环节自动挂接 PII 脱敏器
- 脱敏规则的实现位于 `internal/platform/pii`
- 独立测试入口位于 `cmd/vmm-pii-tester`
- 系统规则目录固定为 `configs/pii_rules`
- 用户覆盖目录固定为 `~/.vmm/pii_rules` 或 `-config` 指向根目录下的 `pii_rules`

## 当前运行模型

### 入站协议

当前只暴露 gRPC：

- 服务：`vmm.v1.VMMService`

当前主线方法：

- `Healthz`
- `ListProjects`
- `ResolveProject`
- `EnsureProject`
- `DeleteProject`
- `MigrateProject`
- `ResolveUser`
- `ListUsers`
- `DeleteUser`
- `GetProfileNodes`
- `GetProfileBundle`
- `ApplyProfileInstruction`
- `SearchMemoryEvents`
- `GetTurnDetails`
- `WriteMemories`
- `DeleteMemories`
- `ChatCompact`
- `PreCheck`
- `PostAction`

### 数据后端

当前主线保留三种本地 SQLite/LanceDB 模式；`combined` 是单独的 PostgreSQL 组合模式：

  - `split`（默认）
  - SQLite：关系库存储，负责层级、用户、session、turn、长期记忆、画像与回收治理元数据
    - 运行时通过本地 `vldb-sqlite` 动态库接入，不再依赖外部 gRPC 网关
    - 留空 `storage.local_data_root` 时沿用 `output/database/sqlite.db`；设置后位于 `<local_data_root>/sqlite.db`
    - FTS 与中文分词由库内能力负责，分词模式通过 `sqlite.tokenizer_mode` 控制
    - 当前 schema 基线固定为 `20`，空库会直接 bootstrap 到该版本
    - 当前版本信息只写入 `vmm_schema_versions`
    - 低于 `19` 的旧本地库不再自动 reset 或自动迁移，需要手工重建后再运行
    - `19 -> 20` 迁移只负责删除已移除的工作记忆旧表
    - 未来 schema 升级仍沿用组件版本框架，后续可直接追加 `20 -> 21` 这类增量迁移
  - LanceDB：向量写入、检索和删除
    - 运行时通过本地 `vldb-lancedb` 动态库接入，不再依赖外部 gRPC 网关
    - 留空 `storage.local_data_root` 时沿用 `output/database/lancedb/`；设置后位于 `<local_data_root>/lancedb/`
    - 向量 schema 版本与 SQLite 独立跟踪
    - 只有 LanceDB 列结构变化时，启动期才会触发表重建与 SQLite 回灌
- `controller`（显式启用）
  - 业务层仍使用与 `split` 完全相同的 SQLite 关系接口和 LanceDB 向量接口
  - VMM 进程不再加载或打开数据库动态库；SQLite 与 LanceDB 调用统一透传给 `vldb-controller`
  - 多个 VMM 客户端会话可以复用 controller 内按规范化物理路径注册的数据库资源，从而避免多个宿主进程分别持有同一个数据库文件
  - SQLite 与 LanceDB 与 split 共用同一数据根；未配置 `storage.local_data_root` 时沿用 `output/database/`，切换模式不迁移业务 schema
  - 启动时必须同时启用 SQLite 与 LanceDB binding；任一失败都会令 VMM 启动失败，绝不会静默回退到直接 FFI
  - SQLite 强制启用数据库文件锁校验，底层固定对齐 `vldb-sqlite v0.1.6`
  - controller 固定为 `v0.2.3`，LanceDB 固定为 `v0.1.5`
  - 开发环境可使用 `controller.auto_spawn=true` 与 `managed` 模式；正式系统服务优先独立运行 controller，并把 VMM 设置为 `auto_spawn=false`
- `native`（显式启用）
  - 关系库由进程内 `modernc.org/sqlite` 适配器持有，向量库由 `output/libs/` 中的平台原生 LanceDB 薄 ABI 持有，不启动 `vldb-controller`
  - 默认 SQLite 文件为 `output/database/native/sqlite.db`，默认 LanceDB 目录为 `output/database/native/lancedb/`
  - `sqlite.native.path` 与 `lancedb.native.path` 的相对路径都相对于标准 `output/` 根目录解析；`lancedb.native.library_path` 留空时由应用选择 `output/libs/` 下当前平台的唯一库名
  - Windows、Linux、macOS 的原生库名分别为 `vmm_lancedb_native.dll`、`libvmm_lancedb_native.so`、`libvmm_lancedb_native.dylib`
  - 原生 SQLite 分词器单独使用 `sqlite.native.tokenizer`，只接受 `gse`（默认）或 `unicode61`；旧 split/controller 的 `sqlite.tokenizer_mode` 只接受 `jieba` 或 `none`
  - `sqlite.timeout`、`lancedb.timeout`、`lancedb.table_name`、`lancedb.vector_column` 与 `embedding.dimension` 在各本地存储模式间共用
  - 原生模式不会因为库缺失、路径非法、模式或分词器不支持而静默切回 split/controller；启动会直接报告配置或 ABI 校验错误
  - 原生库随包契约为 manifest schema `1`、ABI `1`、LanceDB 引擎 `0.39.0`；`VMM_BUILD_STORAGE_PROFILE` 接受 `legacy`、`native`、`all`，未设置时仍为 `legacy`，因此默认 split 行为不变
  - `make.ps1 deps native`（或 Unix 的 native 依赖脚本）负责显式制备并校验 Cargo 缓存；日常 `make.ps1 build` / `build release` 只编译 Go、同步配置、复制已校验库和 manifest，不会自动运行 Cargo
  - SQLite 文件旁的 `<sqlite>.pair.json` 与 LanceDB 目录中的 `.vmm-pair.json` 必须同时存在且拥有相同 `format_version` 与 `pair_id`；缺失、孤立或不一致都会拒绝启动。`<sqlite>.embedding.json` 记录 provider、model、dimension 以及 params、model params、endpoint 和有序 routing 节点摘要，不保存凭据或 endpoint 明文；identity 缺失、孤立或与合并配置不一致也会拒绝启动
  - `<sqlite>.migration-incomplete` 或 `<sqlite>.vector-rebuild-incomplete` 存在时，普通服务和普通维护入口都会拒绝打开该 native 存储；迁移失败会保留目标目录供诊断，修复后必须重新使用新的空目标目录，不能自动切换
  - 迁移命令会生成 `migration-report.json` 与 `native-storage-override.fragment.yaml`。迁移会复制活动事实及可恢复 trash 行的关系数据和向量，并在报告中记录数量；fragment 只能合并回原配置覆盖根，不能直接作为 `-config`，且必须保留原 embedding provider/model/endpoint/routing/params 以及 prompts、pii_rules、noise_rules 覆盖
  - `vmm-migrate -fts-rebuild` 仅适用于 native，必须停写后从持久化原文重建 SQLite FTS。`-clean sqlite,lancedb` 等破坏性清理通过 native 单一 owner 执行；删除向量表后必须先运行 `-vector-rebuild -confirm-vector-rebuild`，再启动服务，普通启动不会自动接管缺失的已登记向量表
- `combined`（显式启用）
  - PostgreSQL：统一承载关系数据、检索索引与向量能力
  - 该模式只在 `storage.mode=combined` 且 `storage.combined_provider=postgres` 时启用
  - PostgreSQL 组合库现在支持受控的 tracked schema 自动升级；当前共享 schema 基线为 `3`
  - 共享 schema `1` 或 `2` 会升级到 `3`，升级过程只负责删除已移除的工作记忆旧表

运行时已经移除：

- HTTP 服务
- 应用内 TLS
- 旧历史兼容 provider 回退路径
- 内存关系库存根 / 内存向量库存根回退

原生动态库验收边界：当前只有 Windows amd64 的实际打包动态库加载、中文写入/检索和向量链路得到真实运行验证。Linux amd64/arm64 与 macOS amd64/arm64 的证据仅限 `CGO_ENABLED=0` 的 Go 交叉构建；交叉构建不等于这些平台的动态库运行通过。

## 核心约束

### 时间展示契约

当前主线关于时间的统一约定如下：

- 数据库中的 Unix 时间戳是唯一基准时间。
- 给分析型 LLM 的可读时间统一使用 `datetime`，并按“当前运行环境的系统本地时间”展开。
- 对外返回与最终画像组合会按场景提供可读时间：需要精确时间的场景返回 `datetime`，最终画像组合只显示日期。
- 当前不会额外保存 `user timezone`、`session timezone` 或“历史写入时原始时区”这类独立时区元数据。
- 因此，如果宿主机系统时区后来发生变化，历史记录重新展开出来的可读时间可能出现几小时偏移，甚至跨自然日变化；这属于当前产品契约下的预期行为。
- 对调用方与提示词来说，可读时间字段只是展示与推理辅助字段，不是底层真值；真正稳定的基准仍然是时间戳。

### 业务入参

`PreCheck`、`ChatCompact` 和 `PostAction` 现在都接受：

- `session_id`
- `user_id`
- `project_id`

说明：

- `user_id`、`project_id` 必须是数字 ID
- `team_id`、`space_id` 不再由客户端传入
- 服务端会先解析 `project_id -> space_id -> team_id`
- 如果 `session_id` 不存在，会自动在 `vmm_sessions` 中创建一条 session 记录
- 如果 `user_id / project_id` 非法或不存在
  - 会在前置范围解析阶段同步直接报错
  - 不会进入 `PreCheck` / `PostAction` 业务逻辑

### PreCheck

当前 `PreCheck` 已恢复为实时两层流程：

- 完成请求校验与范围解析
- 读取最近 turn 窗口，并在 token 预算内混合：
  - 已提炼 turn 的 `details`
  - 未提炼 turn 的脱水原文
- 第一层 `precheck_l1_main` 会结合最近 turn、当前输入和服务端提取的 context hints，先判断是否真的需要长期记忆，再生成带情境锚点的检索语句
- 通过统一记忆检索接口批量向量化这些检索语句并召回长期候选
  - 当前服务端检索链是：`vector + lexical + RRF + rerank(optional) + Weibull + context-aware scoring + MMR`
  - 当前检索过滤范围是已解析出来的 `team_id + space_id + project_id`
  - 同时带 `user_id = 0 OR current_user_id` 过滤，允许共享记忆和当前用户私有记忆一起参与召回
  - 省略 `recall_mode` 或传 `0/UNSPECIFIED` 时，当前运行时默认使用 compact-aware 基线
  - `recall_mode=1/SESSION_COMPACT` 表示显式启用同一 compact-aware 基线：
     - 若当前 session 尚未 compact，会排除当前 session 的 turn-extract 记忆
     - 若当前 session 已 compact，只允许召回 `source_turn_id <= last_compacted_turn_id` 的同 session 历史记忆
  - `recall_mode=2/FULL` 表示显式关闭当前 session compact 边界，在其他作用域与生命周期过滤条件内执行全量召回
  - 当 `recall_mode` 为未来新增值时，当前版本会回退到 compact-aware 基线，避免整段当前 session 被重新开放召回
- 第二层 `precheck_l2_main` 会结合按当前运行系统本地时间展开、且不显示时区后缀的当前 `datetime`、候选创建 `datetime`、候选摘要、最终分数解释、统一来源解释、累计 support/rebuttal 和当前 query 命中的 context evidence，只采纳对当前请求真正有帮助的候选编号
- 仅对被采纳的记忆写回生命周期计数与有效期
- 只把被采纳的记忆通过 gRPC `context_items[]` 返回给调用方
  - `PreCheckResponse` 已移除旧 `context_text` 字段
  - 调用方应直接消费 `context_items[]`
  - 每条 `context_items[]` 仅保留记忆正文、分数、`memory_id`、`has_dialogue`、`turn_id` 与 `created_datetime`
  - `memory_id` 可继续传给 `DeleteMemories`，用于移除误召回或无用的具体记忆条目
  - 这里的 `created_datetime` 是按当前运行环境系统本地时间展开的可读显示值；底层基准仍然是时间戳
  - 当 `turn_id > 0` 时，可继续调用 `GetTurnDetails`
- 画像读取仍走独立接口：`GetProfileNodes / GetProfileBundle`
- 当某一步降级且没有任何记忆最终被采纳时，会返回空上下文，并把 `degraded=true`

### ChatCompact

当前 `ChatCompact` 用于显式告诉服务端“这个 session 已完成一次上下文压缩”：

- 请求字段仍然只有：
  - `session_id`
  - `user_id`
  - `project_id`
- 服务端会把该 session 当前最新已持久化的 `turn_id` 写入 `vmm_sessions.last_compacted_turn_id`
- 同时写入 `vmm_sessions.last_compacted_timestamp`
- 如果当前 session 还没有任何 turn：
  - 会返回成功
  - 但不会更新 compact 边界
- 如果重复 compact 到同一最新 turn：
  - 会保持幂等
  - 不会重复改写边界

### PostAction

当前 `PostAction` 是新的文本契约入口，只接受：

- `user_content`
- `assistant_content`
- `timeline[]`

服务端流程是：

1. 先解析 `session_id / user_id / project_id`
2. 记录原始日志
3. 清洗 `user_content`、`timeline[].content`、`assistant_content`
4. 记录清洗后日志
5. 先按 `user / timeline / assistant` 组装一条脱水 turn 记录
6. 当 `timeline` 为空时，先过 `NoiseGate`
7. 如果不过滤，则写入 `vmm_turn_records`
8. 同步更新 `vmm_sessions.turn_count / summarize_budget / updated_timestamp`
9. turn 写入成功后，立即返回 `accepted=true`
10. 后台工作器异步读取 pending turn，并发起单轮 `postaction_l1_main`
11. 单轮分析输入会包含：
    - 服务端当前时间锚点（按当前运行系统本地时间展开、且不显示时区后缀的 `datetime`，不再传原始毫秒时间戳）
    - 最近若干条已提炼完成的历史 `details`
    - 当前 turn 的原始脱水 JSON 与其创建时间（`created_datetime`）
    - 当前 session 在上次提炼观察之后新增的 `recent_grpc_memory_writes`（只提供可读的 `created_datetime`）
    - 上述 `datetime` 全都属于“当前运行系统时区下的展开结果”；底层排序、锚点与持久化仍以时间戳为准
12. `postaction_l1_main` 会返回：
    - 当前 turn 的 `user_input_kind`
    - 当前 turn 的 `turn_id`
    - 当前 turn 的 `details`
    - 当前 turn 的 `memory_nodes[]`
      - 每条 `memory_nodes[]` 可选携带 `context_edges[]`
      - 每条 edge 只允许包含 `context_key / context_value / relation(support|rebuttal)`
      - 每条 `memory_nodes[]` 还会携带：
        - `evidence_source`
        - `admission`
        - `admission_reason`
    - 当前 turn 的 `profile_nodes[]`
      - 每条 `profile_nodes[]` 也会携带：
        - `evidence_source`
        - `admission`
        - `admission_reason`
13. `postaction_l1_main` 的第一层准入会先压缩明显噪音：
    - 用户提问后，助手只是回显既有记忆、既有画像或通识答案时，会优先标记为 `drop`
    - 通过外部检索、访问网站、资料归纳、工具调用发现的新长期事实，仍然允许标记为 `keep`
    - 但实时天气、当前 CPU 温度、当前系统负载等临时态结果，仍会按 `non_durable` 拒绝
    - 当当前轮文本里出现“昨天 / 上周 / 最近 / 当时”等相对时间表达，且能稳定解析时，`postaction_l1_main` 会优先以目标 turn 的 `created_datetime` 作为主锚点、以服务端当前 `datetime` 作为兜底或校验锚点，把它们转换成明确的绝对日期或日期区间
    - 如果用户明确补充、确认或纠正自己的稳定画像，`postaction_l1_main` 仍应产出画像候选；助手口头更正不等于系统画像已经完成替换，最终仍交由后续画像评审链路决定
14. 如果当前 turn 有新的 `memory_nodes[]` 或 `profile_nodes[]`：
    - 服务端会按与 `PreCheck` 对等的检索作用域召回高相似旧记忆
      - 默认 `space`
      - 支持显式 `team / project`
    - 同时加载当前 user/project 下仍然 `active` 且未过期的画像节点
    - 然后把当前 `datetime`、记忆候选、相似旧记忆、画像活跃节点和新画像候选，一起送入一次 `postaction_l2_main`
    - 其中候选日期与相似旧记忆创建日期只表示条目写入系统的创建时间锚点，不能直接等同于事实真实发生时间
    - 自动提炼与统一评审都要求按领域拆分节点，不能把饮食偏好、生活习惯、编程语言偏好、项目技术栈等无关主题揉成一条综合画像
15. 对统一评审后保留下来的新 `memory_nodes[].abstract` 生成 embedding，并先写入 LanceDB
16. 只有向量侧写入成功后，才会回写当前启用的关系库存储：
    - `vmm_turn_records.details / details_budget / extracted_status`
    - 统一后的 `vmm_memory_nodes`
      - 包含聚合后的 `support_count / rebuttal_count`
    - `vmm_memory_context_edges`
    - `vmm_profile_nodes`
    - 当前轮筛选后的记忆与画像变更会在同一个 `ApplyTurnAnalysis` 写回事务里一起提交，避免只写入一侧造成长期状态脑裂
17. 分析结果日志会输出：
    - `raw_candidates`
    - `final_stored_nodes`
    - `compaction_rate`
    - `admission_drop_count`
    - `review_drop_count`
    - `external_research_kept_count`
18. 后台定时维护仍会做一次过期画像收敛：把到期的 `active` 画像节点标成 `expired`，并重建受影响的 user/project 画像文本
  - `vmm_memory_nodes.vector_id` 与 LanceDB 行 `id` 一一对应
  - LanceDB 行里的 `session_id` 会保存真实来源 session
  - LanceDB 顶层列现在额外包含 `source_turn_id`，用于 compact 边界过滤直接下推到向量检索层
  - `metadata_json` 只保留 `category / details / source_kind / scope_level / priority / memory_level` 这类补充信息
  - 如果 SQLite 在最后回写阶段失败，会反向删除刚写入的 LanceDB 向量行
  - `vmm_teams.profile / vmm_spaces.profile` 不参与 post-action 自动合并，但现在支持通过显式手工画像指令重建

#### PostAction 职责切割说明

- `L1` 现在只负责“当前轮候选提炼 + 首轮粗过滤信号”：
  - 只看历史 `details`、当前原始 turn 和 `recent_grpc_memory_writes`
  - 不再看 whole-session 的 `active_memory_nodes`
  - 调整原因是：长 session 下活跃旧记忆会无限增长，容易把 prompt 膨胀成高噪声上下文；而 `L1` 的真实职责本来就应该是判断“当前轮有没有值得形成候选的长期信息”
- `recent_grpc_memory_writes` 仍然保留给 `L1`：
  - 这是 `memory_nodes` 的确定性排斥区，不代表画像已经完成处理
  - 目的是避免工具链已经主动写入的记忆事实又被 `L1` 再提炼成重复记忆
  - 如果同一事实来自当前 turn 中用户主动陈述、确认或纠正，并且适合作为稳定画像，`L1` 仍应产出 `profile_nodes` 候选
- 记忆去重与 supersede 主责任现在下沉到“检索层 + hard dedupe + `L2`”：
  - 先由检索层召回本轮候选的高相似旧记忆
  - 再由保守的 hard dedupe 快速拦截最明显的近重复
  - 最后由 `L2` 基于当前轮候选、实际召回证据和轻量日期上下文做 keep / drop / supersede 判断
  - 调整原因是：去重和替代本来就依赖候选生成后的语义检索证据，但当候选与旧记忆属于同一事实域时，轻量日期上下文还能辅助区分“语义重复”和“阶段更新”
- 最终 supersede 集合只从 surviving `memory_nodes[].supersede_memory_ids[]` 推导：
  - 不再保留整轮级顶层 `superseded_memory_ids`
  - 调整原因是：如果某条候选后续被 admission、hard dedupe、review 或 embedding 阶段丢弃，它就不应继续贡献旧记忆退役目标
- 当前策略追求的是“安全、渐进式收敛”，而不是“一次性清光所有重复”：
  - 只有本轮真实召回到、并且被 `L2` 明确认可的旧记忆才会被 supersede
  - 没有命中的重复项允许继续保留，留给后续轮次再次命中和清理
  - 这样做是为了避免 LLM 或检索误判时误删无关长期记忆

### 画像接口

当前画像相关 gRPC 能力拆成三条独立方法：

- `GetProfileNodes`
- `GetProfileBundle`
- `ApplyProfileInstruction`

`GetProfileNodes` 的特点：

- 可返回单个目标，或显式 `PROFILE_TARGET_ALL` 下当前 `active` 的原子化画像节点
- `PROFILE_TARGET_UNSPECIFIED = 0` 继续表示未指定目标，仍会被拒绝，不会被兼容性复用为全量查询
- `PROFILE_TARGET_ALL` 需要同时传 `user_id + project_id`，并按 `TEAM -> SPACE -> PROJECT -> USER` 展开
- 不提供 `all` 状态过滤
- 不返回渲染后的 profile Blob
- 返回内容以节点编号、内容、`P/L/W` 相关元数据和来源信息为主

`GetProfileBundle` 的特点：

- 输入固定是：
  - `project_id`
  - `user_id`
- 输出模式支持：
  - `full`
    - 服务端直接返回可注入的组合提示词
    - 这是权威输出，调用方应直接消费 `combined_text`
    - 为避免重复拼接，辅助说明字段和拆分字段会留空
    - 只会保留实际存在正文的 scope；不存在的 `TEAM / SPACE / PROJECT / USER` 不会额外生成对应说明或标签
    - 如果四个 scope 都没有画像正文，则 `combined_text` 直接为空字符串
    - `include_explanation`
      - 省略时默认开启
      - 打开时，会把 `P/L/W` 说明与“当前实际存在的 scope”含义直接内嵌进 `combined_text`
      - 关闭时，只返回正文结构
  - `split`
    - 服务端只分别返回 `[TEAM] / [SPACE] / [PROJECT] / [USER]` 四段正文
    - 不返回完整组合文本
    - `include_explanation` 在该模式下不会额外返回说明字段
- 组合文本固定强调环境约束优先级：
  - `Project > Space > Team`
- 组合结果只为实际存在正文的 scope 显式保留结构标签：
  - `[TEAM]`
  - `[SPACE]`
  - `[PROJECT]`
  - `[USER]`
- 这条接口不触发 LLM，只做确定性拼接

`ApplyProfileInstruction` 的特点：

- 接收单个目标上的显式自然语言画像指令
- 指令不会绑定 `turn_id`
- 关系库存储中这类节点的 `vmm_profile_nodes.turn_id` 会保持 `NULL`
- 服务端会先写入 `vmm_profile_instructions`
- 再把当前 active 节点与这条显式指令交给 `profile_instruction_main`
- 同目标同指令的并发调用会复用第一次进行中的结果，不会重复触发第二次 LLM 评审
- 同一目标上的不同手工指令会按目标串行执行，避免基于同一批旧节点并发写回
- 最后持久化新节点、退役旧节点，并重建对应 scope 的 profile 文本
- 如果一条手工指令同时涉及多个领域，也必须拆成多条画像节点，不能生成跨领域综合节点
- 但“按领域拆分”不等于“一个名词一条节点”：同领域、同语义方向、同生命周期层级的并列事实可以合并进一条节点
- 当默认 SQLite provider 返回 `SQLITE_BUSY / SQLITE_LOCKED / SQLITE_SCHEMA` 且网关通过 trailer 标记为可重试时：
  - 适配层会先做有界指数退避重试
- 当关系库存储 provider 返回类似 `resource deadlock would occur` / `Failed to commit` 的“提交结果不确定”错误时：
  - 服务端会先回查 `vmm_profile_instructions`、`vmm_profile_nodes`、退役状态和最终 profile Blob
  - 如果副作用其实已经落库，则会把这次请求收敛成成功
  - 只有回查也无法确认最终状态时，才会向客户端返回 `STORAGE_OUTCOME_UNCERTAIN`
- 当出现 `STORAGE_OUTCOME_UNCERTAIN` 时，服务端不会立刻把该 instruction 再补写成 `failed`
  - 目的是避免在坏连接或污染连接上继续追加状态写入，把一次不确定提交放大成重复节点或脏状态

目标范围支持：

- `USER`
- `PROJECT`
- `TEAM`
- `SPACE`

权限规则：

- `USER / PROJECT`
  - 手工指令属于高权威输入，但后端仍会按规则施加最低 `P / L` 地板
- `TEAM / SPACE`
  - 手工指令直接视为最高权限规则
  - 后端会强制钳制到最高权威语义，不允许降级成普通偏好或短期上下文

### AI 工具记忆接口

当前 AI 工具记忆相关 gRPC 能力拆成四条独立方法：

- `SearchMemoryEvents`
- `GetTurnDetails`
- `WriteMemories`
- `DeleteMemories`

`SearchMemoryEvents` 的特点：

- 输入固定是：
  - `project_id`
  - `user_id`
  - `queries[]`
  - `top_k`
- `top_k` 省略或传 `0` 时默认返回 `8` 条，最大值为 `32`；超过上限时按 `32` 执行
- `queries[]` 是简单字符串数组：
  - 每项代表一条独立检索语句
  - 不再使用 `query_json`
  - 不再使用 `background`
- 服务端会：
  - 先解析 `project_id + user_id`
  - 对每条 query 做裁剪、规范化和 embedding
  - 在当前 `team / space / project / user` 范围内执行统一检索链路
- 返回结果按 query 分组：
  - `results[].query_index`
  - `results[].query`
  - `results[].hits[]`
- 每条命中当前只返回 AI 真正需要的最小字段：
  - `memory_id`
  - `source_turn_id`
  - `abstract`
  - `details_preview`
  - `category`
  - `created_datetime`
  - 这里的 `created_datetime` 是按当前运行环境系统本地时间展开的可读值；如果宿主机系统时区变化，历史命中的 `created_datetime` 重新展示时也可能发生对应偏移，这属于预期行为
- 如果 `source_turn_id > 0`：
  - 可以继续调用 `GetTurnDetails`
  - 读取对应 turn 的结构化对话详情

`GetTurnDetails` 的特点：

- 输入固定是：
  - `turn_ids[]`
- 支持单条或多条查询
- 返回的是面向 AI 的结构化 turn 视图，而不是存储导向的脱水 JSON：
  - `user_question`
  - `timeline`
  - `assistant_answer`
  - `detail`
- 同时还会补充当前 turn 在同一 session 中的上下文编号：
  - `previous_turn_ids`
  - `next_turn_ids`
- 默认返回当前 turn 前后各 `3` 轮的编号，方便上层 AI 继续按 id 发起更细的详情查询
- 适合和 `SearchMemoryEvents` 联动：
  - 先通过记忆查询拿到 `source_turn_id`
  - 再按 `source_turn_id` 回查结构化对话和相邻编号

`WriteMemories` 的特点：

- 输入固定是：
  - `session_id`
  - `user_id`
  - `project_id`
  - `items[]`
- 每条 `items[]` 当前只保留最小主动写记忆载荷：
  - `scope_level`
  - `abstract`
  - `details`
  - `category`
  - `priority`
  - `memory_level`
- 服务端会在已解析范围内补齐生命周期、执行短窗口软幂等，并复用统一 reviewer 做语义去重或替代
- 返回只保留最小结果字段：
  - `items[].memory_id`
  - `items[].deduped`

`DeleteMemories` 的特点：

- 输入固定是：
  - `user_id`
  - `project_id`
  - `memory_ids[]`
  - `reason`
- 只删除具体 memory node，不会删除来源 `turn` / detail
- 删除语义是把关系表中的 `memory_status` 标记为 `deleted`，后续召回不会再返回这些记忆
- SQLite split 模式会同步移除内建 FTS 索引项，并尽力删除旁路向量；旁路向量删除失败时会进入 Vector GC 重试队列
- PostgreSQL combined 模式同样更新关系行状态；向量列由同一 memory row 持有，不需要额外物理删除
- 返回结果区分：
  - `deleted_memory_ids[]`
  - `not_found_memory_ids[]`
  - `deleted_vector_rows`
- `deleted_memory_ids[]` 和 `not_found_memory_ids[]` 会按请求中 `memory_ids[]` 的首次出现顺序返回，重复 id 只按第一次出现参与结果排序

## 构建与运行

标准构建入口只有：

```powershell
.\make.ps1 build
```

或：

```powershell
.\make.bat build
```

标准运行产物：

- `output/bin/vmm-local.exe`
- `output/bin/vldb-controller.exe`
- `output/libs/`
- `output/database/sqlite.db`
- `output/database/lancedb/`
- native 模式默认使用 `output/database/native/sqlite.db` 与 `output/database/native/lancedb/`

标准配置目录：

- `output/configs/`
- `output/logs/<YYYYMMDD>/<YYYYMMDDHH>.log`

说明：

- 运行时日志会同时输出到 stdout 和文件。
- `split` 模式依赖的 SQLite / LanceDB 动态库会从 `output/libs/` 加载。
- `native` 模式的 LanceDB 薄 ABI 也从 `output/libs/` 加载；打包校验缓存 manifest 和文件哈希，启动校验 ABI、引擎版本及必要操作语义。
- `controller` 模式使用 `output/bin/vldb-controller(.exe)`，不会由 VMM 进程直接加载数据库动态库。
- `split` 与 `controller` 模式使用相同的 `output/database/` 数据布局。
- 标准打包产物默认写入 `output/logs/`。
- 显式设置 `logging.directory` 后，文件日志改写到该绝对目录；VMMM 管理的前台和服务安装都会使用所选数据根下的 `logs/`。
- 如果是 `go run` 或直接在仓库内调试，日志会写入仓库根目录下的 `logs/`。

首次构建本地 split 依赖前，请先执行：

```powershell
.\make.ps1 deps host
```

原生 LanceDB 依赖属于低频、显式的制备步骤。需要 native 依赖时先执行：

```powershell
.\make.ps1 deps native
```

该步骤在 `third_party/deps/native_lancedb/<platform>/` 下生成带源码摘要、ABI、LanceDB 引擎版本和文件哈希的缓存 manifest，并发布精确的 `current.json` 选择。日常 `build` / `build release` 只执行 Go 编译、配置同步和已有缓存校验，不会自动执行 Cargo 或重新编译 LanceDB。发行构建使用 `VMM_BUILD_STORAGE_PROFILE=all`，把 legacy SQLite/LanceDB、`vldb-controller` 与 native LanceDB 全部放入同一平台包；未设置时保持 legacy 默认：

```powershell
$env:VMM_BUILD_STORAGE_PROFILE = "native"
.\make.ps1 build
```

### 启动示例

```powershell
.\make.bat build
.\output\bin\vmm-local.exe
```

标准构建会产出以下可执行文件：

- `output/bin/vmm-local.exe`：正常 gRPC 运行时
- `output/bin/vldb-controller.exe`：统一持有 SQLite/LanceDB 资源的本地控制器
- `output/bin/vmm-migrate.exe`：一次性维护工具
- `output/bin/vmm-pii-tester.exe`：PII 规则测试器

### 注册为系统服务

`vmm-local` 支持自注册为 Windows / Linux / macOS 系统服务。服务名可省略，默认使用 `VulcanMemoryMesh`。安装时建议显式指定绝对配置根；该路径会保存在原生服务定义中，并在服务运行时作为 `-config` 参数传入。以下 Windows 示例假设已经准备好 `C:\VMM\config` 配置目录。

```powershell
.\make.ps1 build
.\output\bin\vmm-local.exe service install -config "C:\VMM\config" -auto-start=false
.\output\bin\vmm-local.exe service start
```

如果需要自定义服务名，将名称作为安装及后续操作的第一个位置参数传入：

```powershell
.\output\bin\vmm-local.exe service install VMMLocal -config "C:\VMM\config" -auto-start=false
.\output\bin\vmm-local.exe service start VMMLocal
```

服务生命周期命令：

```text
vmm-local service install [service-name] -config <绝对配置根> [-user <本机账户>] [-auto-start=true|false]
vmm-local service uninstall [service-name]
vmm-local service start [service-name]
vmm-local service stop [service-name]
vmm-local service restart [service-name]
vmm-local service enable|disable [service-name]
vmm-local service status [service-name]
```

平台行为：

`service status` 在系统确认服务未注册时输出 `state=not-installed`、`auto_start=false` 并成功退出；此结果不同于已注册但停止的 `state=stopped`。对已确认不存在的服务重复执行 `service uninstall` 也成功退出，便于安装中断后重新安装。权限错误、无法查询或同名外部服务仍返回错误，不能当成未注册。

- Windows：通过 Windows Service Control Manager 注册，服务启动时会进入原生 SCM 托管模式。
- Linux：写入 `/etc/systemd/system/<service-name>.service`，执行 `systemctl daemon-reload`；仅当 `-auto-start=true` 时启用开机自启。
- macOS：写入 `/Library/LaunchDaemons/<service-name>.plist`，通过 plist 的 `RunAtLoad` 控制自动启动；关闭自动启动后仍可手动加载服务。指定 `-user` 时，stdout/stderr 写入 `/private/var/log/vmmm/<service-name>/` 下由该账户持有的日志文件。

注意：

- 注册、卸载、启动和停止系统服务通常需要管理员 / root 权限。
- 服务运行时会自动把工作目录切到 `output/bin/`，保持与正式手工启动规则一致。
- 安装命令传入 `-config` 后，原生服务定义会持久化该绝对配置根；运行时仍先加载包内 `configs/base.yaml`，再加载该用户覆盖层。省略 `-config` 仅用于兼容已有手工安装。
- Linux/macOS 指定 `-user` 后，会在注册前检查程序、配置、`.env`、用户规则覆盖与数据库路径对该账户的访问权限。配置和数据根应由该账户持有，程序包则由管理员持有。
- Linux/macOS 系统服务还须在用户覆盖配置中设置 `logging.directory` 为所选账户可写的绝对路径，例如私有数据根下的 `logs`；留空会继续使用程序包同级的旧日志位置，而管理员持有的正式程序包不可由服务账户写入。服务注册前会检查该日志路径的所有权与创建权限。VMMM 安装器会自动配置此字段。
- 如果配置文件中引用了 `OPENROUTER_KEY`、`BAILIAN_API_KEY` 等环境变量，建议放在所选配置根的私有 `.env` 中，并确保服务账户可读取；不要依赖交互式 shell 环境。
- `storage.mode=controller` 用于正式服务时，优先把 controller 注册为独立系统服务，并设置 `controller.auto_spawn=false`；当前 VMM 安装命令不会代替运维自动注册 controller 服务。

### 用户覆盖目录

```powershell
.\output\bin\vmm-local.exe -config ~/.vmm
```

`-config` 默认表示“覆盖根目录”；如果显式传入配置文件，该文件必须是 `.yaml` / `.yml`，其所在目录仍视为覆盖根目录。

当前主配置加载顺序是：

- `output/configs/base.yaml`（项目基础配置，总是加载）
- `output/configs/config.yaml`（项目覆盖层，仅当文件存在时加载；正式发行包刻意不携带源码中的开发覆盖层）
- 以下三项互斥，根据 `-config` 参数的取值选择其一：
  - 未传 `-config`：尝试加载 `~/.vmm/config.yaml`（仅当文件存在时）
  - `-config` 指向目录：尝试加载该目录下的 `config.yaml`（仅当文件存在时）
  - `-config` 指向 `.yaml` / `.yml` 文件：直接使用该文件（总是加载）
- 环境变量覆盖：仅对配置文件中对应字段显式写成 `${VMM_...}` 的字段生效（按需 opt-in 模型），不是全局覆盖；在不相关字段中引用同名变量不会解锁其他字段的覆盖
- `.env` 文件：如果配置文件中存在 `${...}` 占位符，也会在同级目录下搜索 `.env` 文件作为补充来源

说明：

- `base.yaml` 只随项目或打包产物分发，不放到用户目录。
- 用户目录只负责提供覆盖层 `config.yaml`。
- 当前随包 `base.yaml` 保留完整字段说明、节点样例和通用默认参数，但不携带真实或占位 API Key。
- 当前项目级 `configs/config.yaml` 仅用于开发与测试覆盖，可能通过环境变量读取测试凭据；正式发行脚本明确排除该文件，避免把开发路由或凭据占位符带入发行包。
- 当前测试覆盖中的 embedding 与 rerank 仍使用百炼变量 `BAILIAN_API_KEY`、`BAILIAN_BASE_URL`、`BAILIAN_RERANK_URL`。
- `configs/bailian.config.example.yaml` 保留了此前的百炼 LLM、embedding、rerank 覆盖示例。
- 必填运行字段中的 `${ENV_NAME}` 占位符如果缺失或为空，会在展开前直接报出变量名与配置路径，避免被归一化为空节点后产生误导性的路由错误。
- 类型化的 `${VMM_...}` 覆盖值如果无法解析为目标字段所需的整数、浮点数、布尔值或时长，会在配置加载阶段直接失败，不会静默回退到文件值或默认值。
- 未知配置字段会在配置加载阶段直接失败；顶层 `x-*` 仅作为 YAML 锚点辅助块被剥离，不进入运行时配置，也不会参与环境变量覆盖白名单。
- 旧的 `-vulcan-managed-config` 托管参数已退役；`vmm-local` 与 `vmm-migrate` 都会明确拒绝它，请使用独立分层配置和 `-config` 覆盖根目录。

### 维护工具

一次性维护动作已经从 `vmm-local` 剥离到独立工具 `vmm-migrate`。维护命令执行完成后会立即退出，不会启动 VMM gRPC 服务。

#### 清库

```powershell
.\output\bin\vmm-migrate.exe -clean sqlite
.\output\bin\vmm-migrate.exe -clean lancedb
.\output\bin\vmm-migrate.exe -clean postgres
.\output\bin\vmm-migrate.exe -clean all
```

说明：

- `-clean` 会只连接目标后端并执行受管数据清理
- `split` 模式下可以分别清理 SQLite 和 LanceDB
- `controller` 模式使用独立维护 client/session/binding，经 controller 执行 SQLite、LanceDB 清理与迁移导出，不会绕过控制器直接开库
- `controller` 模式的维护会话要求目标 space 独占；如果仍有其他活跃 VMM client 附着同一数据根，维护命令会直接拒绝执行
- `native` 模式的 SQLite/LanceDB 清理通过单一 native owner 执行，不能让服务进程同时写入；SQLite schema 与 FTS 在下次打开时重建，删除 LanceDB 表后则必须先执行 `-vector-rebuild -confirm-vector-rebuild` 再启动服务
- native 清理不会自动恢复旧事实或旧向量；pair marker 与 embedding identity 仍需通过启动校验，配置或身份不一致时继续拒绝启动
- `combined` 模式下可以单独清理 PostgreSQL 受管 schema

#### 存储迁移

native 迁移必须离线执行，并写入新的空目标目录。先停止 `vmm-local` 与相关 controller 写入，再根据当前源模式选择一种迁移：

```powershell
.\output\bin\vmm-migrate.exe -migrate split-to-native -native-output <new-empty-dir> -confirm-migrate
.\output\bin\vmm-migrate.exe -migrate controller-to-native -native-output <new-empty-dir> -confirm-migrate
```

迁移目标必须是新建且为空的目录，不能原地覆盖旧的 split/controller 数据根。迁移过程应保留原始 SQLite/LanceDB 快照，关系事实、来源标识和向量标识按原值写入 native 目标；FTS 属于派生索引，应从持久化原文重新构建。LanceDB 中已有向量只有在模型与 `embedding.dimension` 都匹配时才可复用，迁移本身不自动调用 embedding 服务。可恢复 trash 行的关系数据和旁路向量也会保留，`migration-report.json` 会记录恢复 trash、总向量、重复向量等计数。迁移会生成 `migration-report.json` 与 `native-storage-override.fragment.yaml`；后者是配置片段，不能直接作为 `-config` 参数，必须合并到原配置覆盖根，并保留原有 embedding provider、model、endpoint、routing nodes、params、model params 以及 prompts、pii_rules、noise_rules 覆盖。当前 CLI 参数和迁移代码入口已接入，跨平台历史数据验收需单独完成。

迁移期间的 `sqlite.db.migration-incomplete` 标记会把目标库隔离；失败时目标目录会保留供诊断，修复后必须重新迁移到新的空目录，不会自动切换或自动接管未完成目标。native 启动还会校验 SQLite/LanceDB 成对 pair marker，以及 embedding provider、model、dimension、params、model params、endpoint 和 routing topology 摘要；身份不匹配会拒绝启动。

向量重建会在 native 破坏性重置前捕获一份固定 UTC 观察时刻，并以这份快照同时筛选 active 事实和 eligible restorable trash；随后写入 `sqlite.db.vector-rebuild-incomplete`，普通启动和普通维护入口在该 marker 存在时拒绝服务。重建会对唯一向量事实只生成一次 embedding，将 active 行写回关系库，并按精确的 `(batch_id,memory_id)` 更新 trash 向量，再写入 LanceDB；冲突 payload、身份、关系事实、trash 行、物理向量行数和 schema 版本任一不一致都会失败。全部核验及 embedding identity 更新成功后才清除 marker；reset 后的中途失败会尝试在有界恢复窗口内修复，无法修复则保留 marker 供显式维护处理。

`-fts-rebuild` 的参数和调度入口已接入，只适用于 `storage.mode=native`，运行前必须停写；它从持久化原文重建 SQLite FTS，不改变 embedding identity。跨平台正式打包库实测仍需单独完成，本 README 不把 Go 入口或静态校验记为动态库运行验收：

```powershell
.\output\bin\vmm-migrate.exe -fts-rebuild
```

当需要把历史 `split` 模式的 SQLite 事实库迁移到 PostgreSQL 组合库时，可以执行：

```powershell
.\output\bin\vmm-migrate.exe -migrate split-to-combined
```

说明：

- 迁移源固定为 SQLite，不依赖 LanceDB；controller 模式下 SQLite 快照经 controller 读取
- 迁移过程中会在 Go 内存中解析 `vector_json`，然后直接写入 PostgreSQL 原生 `embedding` 向量列
- 迁移目标使用 `postgres.*` 配置，不要求当前 `storage.mode` 已经切到 `combined`

#### 向量重建

当切换新的 embedding 模型，尤其是向量维度发生变化时，可以执行：

```powershell
.\output\bin\vmm-migrate.exe -vector-rebuild -confirm-vector-rebuild
```

说明：

- 执行前必须先停止 `vmm-local` / gRPC 运行时服务；如果 `grpc.listen_addr` 仍被占用，命令会直接拒绝执行，并在整个重建期间继续占住该监听地址
- 执行前必须手工输入 `Y` 明确确认；未确认会立即退出
- `split` 与 `controller` 模式只处理 `active` 且未过期的长期记忆，不重建失活数据或垃圾箱向量；两种模式都会先清空 SQLite 中的 durable 向量并重建 LanceDB 表，再按当前模型重新生成 active 向量并同步回填两边，controller 模式全程经独立维护会话执行
- `native` 模式会在 reset 前固定一次 UTC 观察时刻，同时纳入 active 事实和 eligible restorable trash；相同事实只生成一次 embedding，active 行与精确 `(batch_id,memory_id)` 的 trash 行分别回写，LanceDB 只写入唯一向量，并在关系 payload、trash 身份、物理行数和 schema 校验通过后发布结果
- `combined` 模式会先重建 PostgreSQL `embedding` 列的向量维度，再为当前有效记忆重新生成向量
- 重建时直接复用当前配置里的 embedding 路由与预算；如果所有 Key 都因为预算耗尽暂时不可用，会自动等待 30 秒后继续
- 该命令的目标是“维度迁移型重建”，默认围绕模型切换后的向量维度变化执行

## 配置说明

当前关键配置项：

- `grpc.listen_addr`
- `grpc.max_receive_message_bytes`
- `grpc.request_timeout.workspace`
- `grpc.request_timeout.pre_check`
- `grpc.request_timeout.post_action`
- `logging.level`
- `logging.debug_rpc_payloads`
- `logging.protect_payloads`
- `logging.payload_encryption_key`
- `prompts.prompt_language`
- `storage.mode`
- `storage.combined_provider`
- `controller.*`
- `relational.provider`
- `sqlite.timeout`
- `sqlite.tokenizer_mode`
- `sqlite.native.path`
- `sqlite.native.tokenizer`
- `postgres.*`
- `lancedb.timeout`
- `lancedb.table_name`
- `lancedb.vector_column`
- `lancedb.native.path`
- `lancedb.native.library_path`
- native 环境变量：`VMM_SQLITE_NATIVE_PATH`、`VMM_SQLITE_NATIVE_TOKENIZER`、`VMM_LANCEDB_NATIVE_PATH`、`VMM_LANCEDB_NATIVE_LIBRARY_PATH`
- `llm.*`
- `embedding.*`
- `memory_pipeline.*`
- `rerank.*`
- `noise.*`
- `post_action.input_mode`
- `post_action.session_analysis_turn_threshold`
- `post_action.session_analysis_token_threshold`
- `post_action.session_analysis_idle_timeout`
- `post_action.session_analysis_history_turns`
- `post_action.session_analysis_max_input_tokens`
- `post_action.max_queue_workers`

`prompts` 下当前提示词目录选择规则已经收敛为显式配置：

- `prompt_language`
  - 默认值：`default_en`
  - 内建英文提示词目录：`configs/prompts/default_en`
  - 内建中文提示词目录：`configs/prompts/default_cn`
  - 支持别名：
    - 英文：`default_en` / `default` / `en` / `english`
    - 中文：`default_cn` / `zh` / `zh-cn` / `cn` / `chinese` / `zh_hans`
  - 也可以直接填写 `configs/prompts/` 下的其他目录名，例如自定义的 `custom-bundle`
- 服务端启动时会严格校验当前选中的提示词目录：
  - 如果目录不存在，启动会直接失败
  - 如果目录下缺少任一必需提示词文件，启动也会直接失败
- 当前必需主提示词文件固定为：
  - `precheck_l1_main.md`
  - `precheck_l2_main.md`
  - `postaction_l1_main.md`
  - `postaction_l2_main.md`
  - `profile_instruction_main.md`
- 这 5 个主提示词入口都会在运行时自动追加统一语言规则：
  - 优先跟随用户最新自然语言输入
  - 混合语言时优先选择主导语言
  - 所有自由文本字段必须一致使用该目标语言
- 当前已经彻底取消“根据模型名称自动匹配提示词目录”的逻辑：
  - 不再支持 `prompts.routes`
  - 如果旧配置里仍保留 `prompts.routes`，加载阶段会直接报错

`logging` 下当前与业务链 RPC 载荷调试相关的新增项：

- `level`
  - 支持 `debug` / `info` / `warn` / `error`
  - 默认 `info`
  - release 环境如需尽量只保留错误日志，可直接设为 `error`
  - 如需走环境变量注入，请在配置中显式写成 `${VMM_LOG_LEVEL}`
- `debug_rpc_payloads`
  - 默认 `false`
  - 关闭时：`PreCheck`、`PostAction`、`memory query` 等 payload 相关日志只输出存在性、计数、长度、摘要和执行状态等安全字段，不写正文
  - 开启时：`pre-check received` / `pre-check returned` 会输出请求正文和组装上下文；`pre-check recent turns prepared` / `pre-check intent analyzed` / `pre-check memory query prepared` / `pre-check memory candidates recalled` / `pre-check memory candidates reviewed` / `pre-check lifecycle write-back completed` / `pre-check finalized` 会输出完整阶段诊断；`post-action received raw` / `post-action received cleaned` 会输出正文和 timeline JSON；`post-action turn analysis result`、`memory ... degraded`、`pre-check ... degraded` 等日志会输出完整 payload 诊断内容，便于本地排障
  - 如需走环境变量注入，请在配置中显式写成 `${VMM_LOG_DEBUG_RPC_PAYLOADS}`
  - 建议仅在本地调试或受控环境下临时开启
- `protect_payloads`
  - 默认 `false`
  - 关闭时：当 `debug_rpc_payloads=false`，payload 类日志只保留安全摘要
  - 开启时：当 `debug_rpc_payloads=false`，`PreCheck` 请求/返回与完整阶段诊断、`PostAction`/`memory query` 等 payload 日志会额外写入加密后的 `..._protected` JSON 信封，便于事后审计
  - 如需走环境变量注入，请在配置中显式写成 `${VMM_LOG_PROTECT_PAYLOADS}`
  - 建议与专用密钥一起使用，而不是在没有密钥治理的场景下长期开启
- `payload_encryption_key`
  - 仅在 `logging.protect_payloads=true` 时生效
  - 支持 32 字节原始字符串、64 位 hex，或 base64 编码后的 32 字节密钥
  - 如需通过环境变量注入，请在配置中显式写成 `${VMM_LOG_PAYLOAD_ENCRYPTION_KEY}`
  - 推荐通过环境变量注入，不建议把真实密钥直接写进仓库配置文件

日志文件落盘规则：

- 每天一个目录：`logs/<YYYYMMDD>/`
- 每小时一个文件：`<YYYYMMDDHH>.log`
- 例如：`output/logs/20260403/2026040316.log`

`memory_pipeline` 下当前新增的是“向量 + SQLite FTS5 混合召回”参数：

- `max_search_keywords`
  - 仍用于 `PreCheck` 的关键词扇出上限
- `min_similarity_score`
  - 仍用于 `PreCheck` 过滤低质量召回候选
- `replace_min_similarity_score`
  - 仍用于 `PostAction` / `WriteMemories` 把高相似旧记忆送给统一 reviewer 前的最低展示阈值
- `hard_dedupe_cosine_threshold`
  - 仍用于 `PostAction` / `WriteMemories` 在统一 reviewer 前，基于真实向量 cosine 直接短路明显重复项；设置为 `0` 时可关闭该捷径
  - 当前默认值为 `0.99`，只会短路最明显的近重复命中
- `hard_dedupe_pool_top_k`
  - 控制 `PostAction` / `WriteMemories` 在 reviewer 前，最多扫描多少条 MMR 之前的旧记忆候选来执行硬排重
  - 这个窗口独立于 reviewer 最终看到的 `top_k`，默认值为 `16`
- `hybrid_enabled`
  - 是否启用“向量召回 + SQLite FTS5 lexical 召回 + RRF 融合”链路；关闭时保持纯向量检索
- `sqlite.tokenizer_mode`
  - 控制本地 `vldb-sqlite` FTS 使用的内建分词模式
  - `jieba` 表示启用库内中文分词器，适合中文场景
  - `none` 表示关闭分词扩展，适合纯英文或需要最朴素匹配的场景
- `lexical_top_k`
  - 每个 query group 最多取多少条 lexical 候选参与 RRF 融合
- `rrf_k`
  - Reciprocal Rank Fusion 的平滑常数；值越大，不同通道的名次差异被压得越平缓
- `mmr_enabled`
  - 是否在融合或 rerank 之后启用 MMR 多样性控制；开启后会优先保留更分散的候选，减少近重复记忆挤占名额
- `mmr_lambda`
  - MMR 的相关性与多样性权重，越接近 `1` 越偏向原始相关性排序，越接近 `0` 越偏向去重分散
- `weibull_enabled`
  - 是否启用读时 Weibull 衰减；开启后会在融合或 rerank 之后，对陈旧且强化较弱的记忆做自然降权
- `weibull_shape`
  - Weibull 形状参数；值越大，衰减在后段越陡
- `weibull_scale_hours`
  - Weibull 基础时间尺度（小时）；值越大，整体衰减越慢
- `weibull_min_multiplier`
  - 读时衰减的最低保底乘子，避免未过期记忆在排序上被直接打到接近零
- `weibull_reinforce_weight`
  - 强化次数对衰减尺度的放大权重；越大表示“访问强化”效果越明显
- `weibull_cross_session_boost`
  - 跨 session 采纳次数对衰减尺度的额外放大权重，用于保护真正跨任务复用的记忆

`rerank` 下当前新增的是“向量召回后的第二阶段重排序”参数：

- `enabled`
  - 是否启用 rerank 第二阶段重排序；关闭时检索链保持当前首轮召回 / 融合排序结果
- `top_n`
  - 每个 query group 最多送多少条首轮向量命中进入 rerank
- `routes`
  - 当前只认 `rerank.routes[]`
  - 每条 route 必须自包含 `provider + endpoint（或 provider 默认值） + model + api_keys/nodes + timeout`
  - 如果不拆 `nodes`，可以直接在 route 上配置 `rpm / tpm / rpd`
  - route 间按 `priority` 做有序容灾，route 内部继续执行 `nodes + key_failover`
  - 当前内置 provider 包含 `dashscope / siliconflow / openrouter`
  - `params.provider` 目前仅由 `openrouter` rerank 消费，用于 OpenRouter 上游供应商偏好；其他 provider 会自动忽略
  - route 全部失败时，检索链会按 `rerank=false` 语义降级回首轮排序

AI 容灾边界当前统一为：

- `llm`
  - 只支持 `llm.routes[]`
  - 支持多 provider / 多 model / 多 route 的有序容灾
  - 当前内置 provider 包含 `openai / openai_native / openai_go / google_ai_studio / openrouter`
  - 不再支持 `llm.routes[].priority`
  - `llm.routes[]` 示例现已包含 `rpm / tpm / rpd / weights`
  - `llm.routes[].weights` 目前包含 `precheck_l1 / precheck_l2 / postaction_l1 / postaction_l2 / profile_instruction / reserve`
  - 未声明的 `weights.*` 默认都是 `100`
  - LLM route 会按“当前调用层级对应的 weight”排序；route 内部再做 `nodes + key_failover`
- `rerank`
  - 只支持 `rerank.routes[]`
  - 顶层只保留 `enabled / top_n / routes`
  - `rerank.routes[]` 示例现已包含 `rpm / tpm / rpd`
  - `rerank.routes[].params.provider` 只对 `openrouter` 生效，不支持该参数的 provider 会忽略
  - 所有 route 都失败时检索链退回首轮排序
- `embedding`
  - 不支持 `routes`
  - 只支持固定 `provider + endpoint（或 SDK 默认值） + model + dimension` 下的多 key 与 `nodes + key_failover`
  - 当前内置 provider 包含 `openai / openai_native / openai_go / google_ai_studio / openrouter`
  - `embedding` 示例现已包含顶层 `rpm / tpm / rpd`
  - `nodes` 只负责吞吐分档，不允许跨模型或跨 provider 混用向量空间

当前已彻底移除：

- `llm` 顶层单路由字段
- `rerank` 顶层单路由字段
- 所有位置的单值 `api_key`
- 对应的旧版 `VMM_LLM_*` 与 route 顶层 `VMM_RERANK_*` 运行时覆盖

`post_action` 下当前保留 6 个与异步单轮提炼窗口和恢复扫描相关的参数：

- `session_analysis_turn_threshold`
  - 兼容保留参数，当前主线不会再按”累计待处理 turn 数”触发批量提炼
- `session_analysis_token_threshold`
  - 兼容保留参数，当前主线不会再按”累计待处理 token”触发批量提炼
- `session_analysis_idle_timeout`
  - 当前仍用于后台恢复扫描：如果某个 session 的 pending turn 长时间未被消费，会在超过该阈值后被重新入队
- `session_analysis_history_turns`
  - 每次单轮 `postaction_l1_main` 最多回带多少条历史 `details` 精要作为参考
- `session_analysis_max_input_tokens`
  - 单次 `postaction_l1_main` 允许发送给 LLM 的总输入预算上限，统计口径是”当前 turn 原始脱水预算 + 历史精要预算”
- `max_queue_workers`
  - 后台并行队列工作器数量，控制最多同时有多少个 worker 处理不同 session 的 turn 分析
  - 同一 session 的多个 turn 仍按顺序串行处理，不会并发
  - 默认值：`4`
  - 环境变量覆盖：`VMM_POST_ACTION_MAX_QUEUE_WORKERS`

当前主线的行为是：

- `PostAction` 成功写入 turn 并完成入队后，后台会尽快发起一次 `postaction_l1_main`
- `postaction_l1_main` 会基于“当前 `datetime` 时间锚点 + 历史精要 + 当前原始 turn + recent_grpc_memory_writes”返回当前这一轮的结构化结果
- 如果本轮有新的 `memory_nodes` 或 `profile_nodes`，会统一走一次 `postaction_l2_main`
- 这次统一评审会同时处理：
  - 记忆候选的高重复去重
  - user/project 两侧画像候选的接纳、无效、替代与 retire-only 决策
  - 当前时间与候选/历史条目时间的时序判断
- 如果有新的 `memory_nodes`，会先写入 LanceDB
- SQLite 成功回写后，会更新：
  - `vmm_turn_records.details / details_budget / extracted_status`
  - `vmm_memory_nodes`
  - `vmm_profile_nodes`
- `vmm_profile_nodes` 现在保存原子化画像事实节点，包含：
  - `priority`
  - `profile_level`
  - `level_reason`
  - `refresh_weight`
  - `expires_timestamp`
  - `superseded_by_id`
  - `profile_date`
- `vmm_users.profile / vmm_projects.profile` 不再作为画像合并输入，而是由当前有效画像节点自动重建的渲染结果
- 自动重建出来的 scope `profile` 文本现在只保存正文时间轴，不再带 `[Profile Legend]` 说明头
- 正文仍然按日期输出，并在每条记录上显示 `[P?][L?][W?]`
- 如果调用方需要 `P/L/W` 说明，应通过 `GetProfileBundle` 的 `full` 模式在输出层按需附加；省略 `include_explanation` 时默认开启
- 如果同 session 分析器或统一 reviewer 判定旧记忆已被新事实覆盖：
  - 会把对应 `vmm_memory_nodes.memory_status` 标成 `superseded`
  - 并在关系库提交后删除 LanceDB 旧向量
  - 记忆检索路径只读取 `active` 且未过期的记忆，所以被替代的旧事实不会再次进入热召回
- LanceDB 行里的 `session_id` 会和来源 turn 的 session 保持一致
- 如果 SQLite 回写失败，会尝试回滚这次新增的 LanceDB 向量
- 后台 worker 还会按 `session_analysis_idle_timeout` 周期性补扫陈旧 pending session，帮助崩溃或临时失败后的恢复

另外，当前还有一批“已经声明但尚未接入主运行时”的配置参数，见：

- [当前未接入主运行时的配置参数清单（中文）](./docs/unused-config-parameters_CN.md)

### 超时模型

- `grpc.request_timeout.pre_check`
  - 表示整次 `PreCheck` RPC 的外层预算
- `pre_check.intent_timeout`
  - 表示未来恢复意图提取后，内部子步骤的预算

当前要求：

- `grpc.request_timeout.pre_check > pre_check.intent_timeout`

### PreCheck 检索范围

- `pre_check.search_scope`
  - 控制 `PreCheck` 长期记忆检索的层级范围
  - 可选值：`team`、`space`、`project`
  - 默认值：`space`

当前语义：

- `team`
  - 在当前 team 范围内召回长期记忆
- `space`
  - 在当前 space 范围内召回长期记忆，不再限制到单个 project
- `project`
  - 仅在当前 project 范围内召回长期记忆

这个开关只影响 `PreCheck` 这条链路，不改变通用 `SearchMemoryEvents` 的默认项目级检索语义。

### 记忆更替范围

- `memory_replace_scope`
  - 控制 `PostAction` 统一 reviewer 在“新记忆接管旧记忆”场景下的相似旧记忆召回范围
  - 可选值：`session`、`team`、`space`、`project`
  - 默认值：`project`

当前语义：

- `session`
  - 只允许当前 session 内的新事实替代旧事实
- `project`
  - 允许当前 project 下跨 session 的旧事实被更新事实接管
- `space`
  - 允许当前 space 下相关 project 的共享事实被更新
- `team`
  - 允许当前 team 范围内的稳定规则替代较旧副本

说明：

- 这个开关独立于 `pre_check.search_scope`
- `PreCheck` 的召回范围不会再隐式决定 `PostAction` 的记忆更替范围
- `WriteMemories` 在 24 小时软幂等之后，也会复用同一个 `memory_replace_scope` 做语义去重与替代判断
- 当前统一 reviewer 的记忆结果块已经支持：
  - `accepted_candidates[]`
  - `accepted_candidates[].supersede_memory_ids[]`
  - `dropped_candidates[]`
  - `dropped_candidates[].dedupe_memory_id`
  - 只有当 reviewer 在 `dropped_candidates[].dedupe_memory_id` 中显式指向某条 `similar_memories.memory_id` 时，`WriteMemories` 才会复用已有记忆引用
  - 如果 reviewer 只是丢弃候选，但没有显式给出 dedupe 目标，则 direct-write 会安全降级为新建，而不是误复用第一条旧记忆

### Retention 回收参数

- `retention.enabled`
  - 是否启用冷数据回收治理入口
  - 默认值：`true`
- `retention.recycle_scan_interval`
  - 定时扫描周期
  - 默认值：`30m`
- `retention.turn_keep_extra_turns`
  - 在热窗口基础上额外保留的 turn 数量
  - 默认值：`5`
- `retention.session_idle_recycle_after`
  - session 长期无新增有效信息后允许进入回收判定的阈值
  - 默认值：`360h`
- `retention.trash_retention`
  - 回收站保留时长
  - 默认值：`720h`
- `retention.protect_priority_floor`
  - 受保护共享记忆的优先级下限
  - 默认值：`P1`
- `retention.protect_memory_level_floor`
  - 受保护共享记忆的层级下限
  - 默认值：`stable`
- `retention.skip_protected_shared_memories`
  - 是否跳过受保护的共享记忆
  - 默认值：`true`

当前阶段说明：

- 这组参数已经接入独立 retention 维护器
- 当前维护器每轮会按固定顺序执行：
  - 终态记忆回收
  - 独立冷 `turn` 扫描入队
  - 已领取冷 `turn` 回收任务执行
  - idle-session recycle
  - vector GC retry
  - trash purge
- 当前维护器会周期性回收 `superseded / expired / deleted` 的终态记忆，并把对应 `memory_context_edges` 一并迁入回收站
- 独立冷 `turn` 回收现在使用持久化 `recycle_jobs` 队列做 scan / claim / execute 分离：
  - 扫描阶段只为确实存在可回收旧 `turn` 的 session 入队
  - 执行阶段只消费已 claim 的任务
  - 多 worker 依赖队列租约语义避免重复消费同一条任务
- 当前维护器也会按 `session_idle_recycle_after` 扫描长期空闲 session，并回收：
  - 已过期且长期未被强化的 `session` 级记忆
  - 超出热窗口 `session_analysis_history_turns + turn_keep_extra_turns` 且不再被主表记忆/画像引用的旧 `turn`
- 冷 `turn` 回收与 idle-session 回收都只会迁移“没有主表记忆/画像引用”的旧 `turn`，以保持 `source_turn_id -> GetTurnDetails` 契约不被破坏
- 已执行的回收结果会记录到 `recycle_batches`，只承担当前回收站批次锚点职责，不再作为长期恢复台账
- SQLite 会同步清理 `vmm_memory_nodes_fts` 镜像；PostgreSQL 组合存储模式下不额外走向量 GC
- 超过 `trash_retention` 的回收站批次会被后台 purge 永久清理，覆盖：
  - `memory_nodes_trash`
  - `memory_context_edges_trash`
  - `turn_records_trash`
  - 对应的 `recycle_batches` 批次元数据
- 当前仍不提供产品级恢复接口；回收站仅作为数据库层防灾缓冲

### LanceDB 表名规则

`lancedb.table_name` 现在是基础表名。

运行时会自动追加 embedding 维度：

- 基础表名：`vmm_memory_vectors`
- 维度：`1024`
- 实际表名：`vmm_memory_vectors_1024`

这样可以避免不同向量维度共用同一张物理表。

## 开发验证

提交较大改动前，至少执行：

```powershell
go test ./... -count=1
.\make.bat build
```

## TLS 说明

当前应用内不再支持 TLS。

如果需要 TLS、域名或公网入口，请在前面使用 [Caddy](https://caddyserver.com/) 做反向代理。
