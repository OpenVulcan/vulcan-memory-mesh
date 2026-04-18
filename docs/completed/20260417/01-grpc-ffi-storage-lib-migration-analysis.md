# gRPC 对接 SQLite / LanceDB 本地 lib 化改造分析与方案设计

## 任务目标

围绕当前 VulcanMemoryMesh 项目的 gRPC 存储接入链路，完成以下分析与方案设计：

1. 梳理当前项目内 SQLite、LanceDB、分词器、gRPC 适配层、构建与运行目录的现状。
2. 对照 `D:\projects\vulcan-mcp-client\make.ps1` 所采用的 `make deps host` 在线拉取宿主依赖模式，评估如何将 `D:\projects\VulcanLocalDataGateway\vldb-sqlite` 与 `D:\projects\VulcanLocalDataGateway\vldb-lancedb` 两个已实现 lib 形式的库引入当前服务。
3. 形成统一的 FFI 扩展装配方案，包括第三方目录落盘、`.gitignore` 管理、构建拷贝、运行时 `libs` 目录布局、数据库目录迁移、中文分词配置切换与 Go 侧分词器退役策略。

## 执行步骤

1. 检查仓库计划目录与已完成目录，确定当天计划编号与命名。
2. 梳理当前仓库中的以下内容：
   - gRPC 入口与运行时存储装配路径
   - SQLite / LanceDB 当前接入方式
   - 分词器相关实现与配置读取位置
   - 构建脚本、输出目录与运行目录规则
3. 分析外部两个 lib 仓库：
   - 导出物类型（dll / so / dylib / 伴随头文件）
   - API 形态、初始化参数、路径约束、分词器开关能力
   - 构建产物与宿主依赖下载方式
4. 分析 `D:\projects\vulcan-mcp-client\make.ps1` 及其 deps host 依赖安装链路，提炼可迁移的机制。
5. 基于以上信息，输出改造方案，至少覆盖：
   - 第三方依赖下载目录设计
   - 跨平台产物命名与复制规则
   - 运行时 `output/bin` 同级 `output/libs` 与 `output/database` 的目录约定
   - Go 侧接口抽象与 FFI 适配替换思路
   - SQLite 中文分词配置策略与 Go 额外分词器下线策略
   - 风险点、验证项、实施顺序建议

## 技术选型与约束

- 优先沿用现有 `make.ps1` / `make.bat` 作为标准构建入口，不绕过项目既有打包规范。
- 宿主依赖获取方式优先参考 `make deps host` 的在线下载与按平台分发模式，不直接把大体积二进制提交到主仓库。
- 运行时动态库采用“可执行文件上层目录集中放置”的统一布局，避免散落在 `bin` 根目录。
- 数据库存放位置统一迁移到 `bin` 同级的 `database/sqlite.db` 与 `database/lancedb/`。
- 分词配置必须由配置文件驱动，不允许硬编码仅中文启用。
- 若分析中发现涉及重大架构调整、核心依赖策略变化或 FFI 边界不清的地方，需要在方案中显式标注需二次确认的决策点。

## 验收标准

1. 明确当前项目 SQLite / LanceDB / 分词器 / gRPC / 构建链路的现状与问题边界。
2. 明确外部两个 lib 仓库的接入方式、能力差异与对当前项目的映射关系。
3. 给出可执行的改造方案，覆盖目录、脚本、配置、运行时装配、测试验证与风险控制。
4. 方案能回答以下关键问题：
   - 在线依赖如何按宿主系统下载并缓存到第三方目录？
   - 编译后如何把动态库复制到运行目录上层的 `libs` 目录？
   - gRPC 存储链路如何从当前直连/内嵌实现切换到 lib / FFI 对接？
   - SQLite 内建分词器如何替换当前 Go 侧额外分词方案？
   - 如何通过配置文件控制是否启用中文分词？

## 执行变更总结

### 1. 核心修复与调整概述

本轮未直接修改业务代码，而是完成了面向后续实施的全链路分析与改造方案设计，核心结论如下：

1. 当前 VulcanMemoryMesh 并不是“本地直接嵌入 SQLite / LanceDB”，而是通过 `internal/adapters/outbound/vldb_sqlite` 与 `internal/adapters/outbound/vldb_lancedb` 两个 gRPC 客户端适配层访问外部网关服务。
2. 当前构建链路仅负责产出 `output/bin` 与 `output/configs`，尚未建立 `output/libs` 与 `output/database` 的运行时装配规范，也没有对应的宿主依赖拉取脚本。
3. `vldb-sqlite` 与 `vldb-lancedb` 两个外部库已经具备稳定的 lib / FFI 能力，并且都提供了 Go `purego` 形式的调用示例，适合作为当前服务内嵌式对接的基础。
4. `vldb-sqlite` 已经把中文分词、FTS 索引、词典热更新等能力封装在库内部，当前项目 Go 侧额外的 `LexicalTokenizer` 预分词链路可以下线或仅保留兼容层，不应继续作为主路径。
5. 后续改造的最佳方向是：保留现有 app / logic 层接口边界，在 outbound 适配层内部把 gRPC 客户端替换为本地动态库加载与 FFI 调用，同时把数据库路径切换到 `output/bin` 同级的 `output/database/sqlite.db` 与 `output/database/lancedb/`。

### 2. 📂 文件变更清单

新增：

1. `docs/plan/20260417-01-grpc-ffi-storage-lib-migration-analysis.md`

修改：

1. 当前阶段仅对上述计划文件追加了分析记录与执行总结。

删除：

1. 无。

### 3. 💻 关键代码调整详情

本轮为分析设计阶段，未直接提交代码实现，但已明确后续代码改造应分为以下几个工作包：

1. 构建与依赖装配层：
   - 在 `make.ps1` / `make.bat` 中新增 `deps` 或 `deps host` 入口。
   - 参考 `D:\projects\vulcan-mcp-client\scripts\install_host_deps.ps1`，按宿主系统下载 `vldb_sqlite` 与 `vldb_lancedb` 的动态库归档，解压到 `third_party/`，并在构建后复制到 `output/libs/`。
2. 运行时路径解析层：
   - 在当前基于可执行文件定位 `output/configs` 的基础上，新增对 `output/libs` 与 `output/database` 的统一解析逻辑。
   - 使 SQLite 数据库固定落到 `output/database/sqlite.db`，LanceDB 数据目录固定落到 `output/database/lancedb/`。
3. SQLite 适配层：
   - 以外部库提供的 `purego` 示例为蓝本，将现有 gRPC 调用替换为 lib / FFI 调用。
   - 把 FTS 预分词与查询表达式构造主逻辑迁移到 SQLite lib 内部的 tokenizer / FTS API。
   - 增加配置项，用于在 `none` 与 `jieba` 两种分词模式之间切换，而不是继续沿用当前语义偏旧的 `lexical_pre_tokenize` 作为唯一控制开关。
4. LanceDB 适配层：
   - 使用 `vldb-lancedb` 提供的原生向量 upsert / search FFI 接口替换现有 gRPC 向量服务调用。
   - 保持上层向量检索端口不变，尽量把影响面控制在 outbound adapter 内。
5. 配置与兼容层：
   - 当前 `sqlite.address` 与 `lancedb.address` 明显偏向远程 gRPC 模式，后续需要新增或重定义本地 lib 模式配置项。
   - 旧字段可在过渡期保留兼容，但默认值应逐步转向本地路径与本地库装配。

### 4. ⚠️ 遗留问题与注意事项

1. 这是一次明确的存储接入架构调整：从“进程外 gRPC 网关”切换到“进程内 FFI 动态库”，属于重大决策，正式实施前应先由您确认是否完全放弃现有 gRPC 运行方式，还是保留一段时间的双模式兼容。
2. `vldb-sqlite` 的 FTS 能力虽然足够强，但当前项目已有自己的 `vmm_memory_nodes_fts` 辅助表、BM25 权重与写入策略。正式迁移时需要决定是完全切到库提供的 FTS 文档接口，还是保留现有表结构、仅复用其分词器能力。
3. 当前 `memory_pipeline.lexical_pre_tokenize` 的语义是“Go 侧是否预分词”，而目标语义应变为“SQLite FTS 是否启用中文分词器 / 选择哪种 tokenizer”。建议新增显式配置项，例如 `sqlite.fts_tokenizer_mode`，避免新旧语义混淆。
4. 运行时动态库的搜索路径不能依赖系统 PATH，建议统一由程序按可执行文件位置解析 `../libs` 后显式加载，确保 Windows / Linux / macOS 行为一致。
5. 后续如果修改构建目录、配置规则、存储接入与分词链路，需要同步补齐测试与文档，至少覆盖仓库协作说明中要求的 `grpcapi`、`usecase`、`processor`、`textutil`、`pii`、`config` 相关测试，以及最终的 `.\make.ps1 build` 验证。
