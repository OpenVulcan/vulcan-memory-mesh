# VulcanMemoryMesh 全仓库审计（2026-09-26）

> 本文保留修复前的审计基线、行号及验证结果。A01–A13 的后续修复和最新验收见 [修复记录](REPOSITORY_AUDIT_REPAIRS_20260926_CN.md)。

## 审计结论

**存在用户提出的全部五类现象，但程度不均：最需要修正的是边界上的静默纠错和缺失字段验证，其次是生产路径已不需要、却由测试持续维持的兼容分支，以及 SQLite 存储职责过度集中。不能据此认定全部测试或全部防护都应删除。**

本报告列出 13 项发现。其中 5 项属于已复现的验证/错误处理缺口，另有接口与测试冗余、大文件、手写基础逻辑、死代码及仓库规范冲突。P2 表示影响正确性或持续维护、建议近期处理；P3 表示低风险清理和测试质量改进。不将风格偏好或文件长度直接等同于运行故障。

- 基线：`main`，提交 `3a6fe87`；开始审计及移除临时探针后，工作区均干净。
- 第一方 Go：270 个生产文件、79,661 行；138 个测试文件、54,740 行。按 Git 跟踪文件统计，含注释和空行，排除 protobuf 生成文件和第三方模块。
- 范围：全仓库结构扫描；深入追踪配置、gRPC、管理 HTTP、PreCheck/PostAction、记忆查询与写入、存储、PII/Noise、FFI、维护、服务安装和发行链路中的高风险节点。
- 方法：AST 结构、符号引用、类型/协议、实际装配路径、既有测试以及临时离线探针交叉验证。不是对全部 13 万余行逐行人工审读，也不是各平台正式运行验收。
- 本次仅交付报告；业务源码、既有测试、配置、数据库和 Git 历史均未修改。

## 一、项目理解

### 1. 实际业务链路

`cmd/vmm-local` 加载分层配置与规则，由 `internal/app` 作为组合根装配日志、AI 客户端、存储、处理器、业务用例和服务入口。

- **PreCheck**：请求范围解析及脱敏 → 意图判断 → 向量/词法检索 → 关系事实实体化 → 融合、衰减、重排、多样性选择 → 候选评审与上下文组装。
- **PostAction**：请求校验、脱敏、清洗 → 标准轮次 → NoiseGate → 关系记录 → 后台队列分析与评审 → 记忆/画像更新、向量补偿。
- **ChatCompact**：维护当前会话压缩边界，供后续召回限制使用。
- **主动记忆写入**：校验与脱敏 → embedding → 去重/替代评审 → 向量写入 → 关系持久化 → 旧向量清理与补偿。
- **维护与管理**：回收、恢复、清除、向量重建、旧存储迁移；可选的管理 HTTP 接口提供查询和带预览令牌的变更操作。

`logic`、`app/usecase` 的本次依赖搜索没有发现对 adapters 的反向引用。`internal/app` 的组合根负责创建具体 adapter，不能把这部分装配依赖直接判为业务层越层。

### 2. 实际存储模式

| 模式 | 关系存储 | 向量存储 | 所有权 |
| --- | --- | --- | --- |
| split | legacy SQLite FFI | legacy LanceDB FFI | VMM 进程 |
| controller | SQLite | LanceDB | controller 进程 |
| native | modernc SQLite | 仓库内 Rust LanceDB ABI | VMM 进程 |
| combined | PostgreSQL | PostgreSQL 向量能力 | PostgreSQL |

依据：[运行时存储选择](../internal/app/runtime_storage.go#L146)、[运行时组合](../internal/app/app.go#L49)、[业务装配](../internal/app/runtime_pipeline.go#L47)。

这意味着数据库对账、跨存储补偿、FFI 生命周期、离线迁移隔离确实有真实需求；不能仅因代码复杂就删除。

## 二、需要优先修正的验证缺口

### A01 · P2 · LLM 必填归属字段缺失，被零值解释为用户画像

**位置**：[响应类型](../internal/logic/processor/structured_output_schema.go#L50)、[解析器](../internal/logic/processor/turn_analyzer.go#L167)。

`turnAnalysisProfileNodePayload.ProfileType` 是 `int`。JSON 缺失或显式传入 null 后得到 0；`ValidProfileType(0)` 又合法，因为 0 表示用户画像。生成的结构化输出 schema 将该字段列为必填，但本地解析没有验证“是否存在”。

实际离线探针：其余字段合法，省略 `profile_type`，解析成功并输出 `ProfileType=0`。

下游 [画像分组](../internal/app/usecase/postaction_profiles.go#L113) 将其放入用户候选；若后续评审接纳，[持久化](../internal/adapters/outbound/vldb_sqlite/store.go#L1777) 使用 `session.UserID` 绑定。这是归属信息丢失后被猜成用户画像，不能由枚举范围检查补救。

**建议**：传输解码类型用指针或明确的 presence 状态区分“缺失”与合法 0；在解析边界拒绝缺失/null，领域模型仍用确定的整数。无需引入通用 schema 校验库。

**边界**：已复现解析器行为，未声称真实供应商已返回该异常响应或生产数据已被污染。

### A02 · P2 · 向量距离缺失，被解释成完全匹配

**位置**：[行解码](../internal/adapters/outbound/vldb_lancedb/store.go#L760)、[分数映射](../internal/adapters/outbound/vldb_lancedb/store.go#L379)、[距离换算](../internal/adapters/outbound/vldb_lancedb/store.go#L714)。

`asFloat64(nil)` 返回 `0, nil`；`Search` 在 `Distance == 0` 时改取 `Score`；两字段都缺失则继续保留 0，最终转换为相似度 1。

实际探针提供合法标识和内容、同时省略 `_distance` 与 `score`，`Search` 无错误并返回满分 `1`。此外，合法零距离也无法与字段缺失区分；字符串 NaN/Inf 与负距离的验证也不完整。

[用例实体化](../internal/app/usecase/memory_query_search.go#L1397) 继续使用该分数。关系记录存在时，实体化不会纠正这个排名依据。

**建议**：根据各后端已确认的响应契约读取明确的距离字段，验证存在性、有限值及非负性。若确有版本兼容，也应依据字段存在性选择，不能依据数值是否为零选择。

### A03 · P2 · 配置先被“修好”，后面的精确校验失效

**位置**：[加载顺序](../internal/config/config_load.go#L95)、[Normalize](../internal/config/config_runtime.go#L191)、[Validate](../internal/config/config_validate.go#L168)。

真实加载顺序为 `Normalize → Validate`。大量显式非法值先变成默认值或被钳制；对应 Validate 分支因此在正常配置加载中不可触发。

已通过真实 YAML 文件及 `Load` 复现：

| 用户显式输入 | 加载结果 | 错误 |
| --- | --- | --- |
| grpc.max_receive_message_bytes = -1 | 1048576 | 无 |
| pre_check.top_k = -2 | 5 | 无 |
| memory_pipeline.max_search_keywords = 999 | 10 | 无 |
| memory_pipeline.mmr_lambda = 2 | 0.75 | 无 |

配置检查命令因此可能给出成功结果，而实际运行参数已经改变。单独调用 `Validate` 的测试无法覆盖该问题。仓库对 `hard_dedupe_pool_top_k` 等字段已使用 presence 标志，说明“缺省”和“显式非法”在现有模型内可以区分。

**建议**：统一采用“缺省填值、显式非法拒绝”；默认配置已有默认值的字段，不再把任意负数当缺省。补测真实加载入口，保留字段定位。

### A04 · P2 · 持久化向量 JSON 损坏，被吞成空数组

**位置**：[decodeFloat32Slice](../internal/adapters/outbound/vldb_sqlite/store.go#L5694)、[行转换](../internal/adapters/outbound/vldb_sqlite/store.go#L6883)。

非法 JSON 会返回空切片，函数没有错误通道。探针输入 `[broken]` 得到 `[]`。

这不只用于可重建的噪声缓存：`memoryNodeRow.toMemoryNodeRecord` 也调用它，[按向量 ID 读取关系事实](../internal/adapters/outbound/vldb_sqlite/store.go#L3546) 会把损坏记录作为成功结果返回；下游 [余弦计算](../internal/app/usecase/memory_query_search.go#L1110) 对空向量再返回 0。错误可能最终表现为去重/多样性计算失真，而非可定位的数据损坏。

**建议**：解码函数返回 error，携带记录 ID 与字段名逐层上报。合法的空向量状态与格式损坏分别处理。噪声缓存若允许重建，应由缓存层记录原因并重建，不应在共用解码器中抹掉异常。

### A05 · P2 · 用户提示词读取失败，静默改用系统内容

**位置**：[GetPrompt](../internal/config/manager.go#L53)、[启动期包选择](../internal/config/manager.go#L78)。

`GetPrompt` 仅在用户文件读取成功时返回，否则所有错误都改读系统文件；没有区分“不存在”与权限、I/O、文件变目录等错误。启动期要求用户提示词包完整，但运行时又逐文件尝试两层，选中来源没有固定下来。

探针用目录替代用户文件制造确定性读取错误，结果为 `prompt="system prompt", error=nil`。

**建议**：初始化时确认并保存实际生效的包目录。已经选中的用户包读取失败就明确报错；只有契约允许的“没有用户覆盖包”才选择系统包。PII/Noise 已约定的用户优先规则本身不需要取消。

## 三、冗余接口、防护与测试

### A06 · P2 · 所有生产后端都有原子写入，仍维持无原子能力的备用路径

**位置**：[persistDirectWriteMemory](../internal/app/usecase/memory_query_write.go#L475)、[可选接口](../internal/app/usecase/memory_query.go#L280)、[MemoryStore](../internal/app/ports/ports_memory.go#L54)。

每次主动写入都断言 `directMemoryWriteApplier`，失败后走 `CreateDirectMemoryNode`，并重复处理失败、回滚、结果不明和日志。

实际装配已确认：

- split/controller/native 的关系层都使用 `vldb_sqlite.Store`，实现 [ApplyDirectMemoryWrite](../internal/adapters/outbound/vldb_sqlite/store.go#L3721)。
- combined 使用 `vldb_postgres.Store`，实现 [同一方法](../internal/adapters/outbound/vldb_postgres/memory_store.go#L777)。
- 缺少此能力的 `fallbackOnlyMemoryStore` 只存在于 [测试文件](../internal/app/usecase/memory_query_test.go#L3959)。

**建议**：将主动写入用例必需的原子操作表达为明确端口，去掉每次请求的能力探测及旧备用分支；迁移旧测试到正式路径。对“提交结果不明时保留可能已引用的向量”的测试必须保留，这一业务保证仍然必要。

[resolveRuntimeStorageCapabilities](../internal/app/runtime_storage.go#L75) 中多项启动期接口断言也可在同一轮整理中改为组合端口的编译期约束；不建议简单合并成所有用例都依赖的巨大接口。

### A07 · P3 · 为不符合真实 gRPC 调用契约的测试输入扩张生产防护

**位置**：[拦截器辅助函数](../internal/adapters/inbound/grpcapi/interceptors.go#L151)、[请求类型推断](../internal/adapters/inbound/grpcapi/interceptors.go#L217)、[相关测试](../internal/adapters/inbound/grpcapi/server_test.go#L1498)。

约 200 行、10 个测试专门维护 nil context、nil info、nil handler 或缺少方法信息时按请求类型推断 RPC 的行为；后面还存在服务方法的 nil context 测试。

本仓库 [生成的 gRPC handler](../internal/adapters/inbound/grpcapi/proto/v1/vmm_grpc.pb.go#L766) 明确创建 info 和 handler。生产装配经过统一 interceptor 链；没有发现需要手工传 nil 来使用该内部组件的生产入口。相关注释也明确将“直接测试忘记参数”作为防护理由。

**建议**：测试按真实 gRPC 契约构造上下文、方法信息和 handler；移除仅为非法测试调用维护的兼容分支。保留真实请求校验、panic recovery、错误映射、日志脱敏和范围解析验证。

这组测试是本次最明确的“可以减少的测试”之一，而不是据文件行数猜测冗余。

### A08 · P3 · 自有 rerank 客户端已有类型化错误，测试仍保活字符串状态码解析

**位置**：[分类器](../internal/adapters/outbound/ai_key_failover/classifier.go#L368)、[字符串解析](../internal/adapters/outbound/ai_key_failover/classifier.go#L531)、[测试](../internal/adapters/outbound/ai_key_failover/key_failover_test.go#L489)。

DashScope 和 SiliconFlow 客户端对非 2xx 响应均返回自身的 `APIError{StatusCode, Headers, Body}`。分类器已经支持 `errors.As`，但又保留解析 `"status 429"` 文本的路径。

已找到维持这一旧契约的测试输入：`errors.New("dashscope rerank status 429: insufficient_quota")`。实际客户端现在产出的是类型化错误；剩余普通错误来自请求构造、读取、解析等阶段，不应靠内容中的数字推断认证或配额失败。

**建议**：测试使用生产 APIError；对非类型化且非网络错误返回明确 unknown 分类。删除这一局部文本状态码兜底，不要连带删除 SDK 确实需要的供应商特定错误分类。

### A09 · P3 · 部分测试锁定措辞或宽松替身，价值低于真实边界验证

**位置**：[提示词测试](../internal/logic/processor/turn_analyzer_test.go#L19)、[SQLite 替身](../internal/adapters/outbound/vldb_sqlite/store_fake_test.go#L153)、[LanceDB 替身](../internal/adapters/outbound/vldb_lancedb/engine_fake_test.go#L19)。

- 两个提示词测试复制中英文片段并检查 `strings.Contains`。等价措辞调整也会失败，但包含正确片段并不能证明模型遵循政策。
- SQLite 替身在未配置执行函数时默认 Success，未配置查询时默认空结果；LanceDB 的建表/删除替身也默认成功。测试遗漏一个预期调用时，额外行为可能被吸收。
- 大量 SQL 参数绑定测试具有真实价值，但对相同绑定机制逐个重复模板、同时过度固定 SQL 排版，会抬高维护成本。

**建议**：

1. 把必须保留的提示词政策检查集中成一组明确契约；不要让任意润色都成为业务回归。模型语义效果需另行评估。
2. 变更/删除相关替身对未声明的调用默认失败；查询/关闭等可忽略操作明确选择宽松模式。
3. 合并同机制的参数化案例，保留不同作用域、事务边界、部分提交、结果不明和补偿分支。不能整批删除 SQL 测试。

## 四、文件体积、实现复杂度及遗留代码

### A10 · P2 · SQLite 单文件聚集多个业务域，最值得优先拆分

| 文件 | 物理行数 | 已确认职责问题 |
| --- | ---: | --- |
| internal/adapters/outbound/vldb_sqlite/store.go | 7,094 | schema、DB 执行、turn、画像、记忆、用户/项目管理、FTS、序列化混在同一文件 |
| internal/adapters/outbound/vldb_sqlite/store_test.go | 8,514 | 上述各域的正常行为与故障注入测试混排 |
| internal/app/usecase/memory_query_test.go | 4,278 | 查询、融合、生命周期、主动写入、删除及替身集中 |
| internal/app/usecase/precheck_test.go | 2,599 | 多阶段业务及日志/降级断言混排 |
| internal/adapters/outbound/vldb_sqlite/retention_store_test.go | 2,564 | 回收业务和大量持久化故障场景集中 |
| internal/config/config_test.go | 2,396 | 加载、环境变量、路由、规范化和校验混排 |
| internal/app/usecase/memory_query_search.go | 1,578 | 查询编排、RRF、衰减、情境证据、MMR、rerank、实体化及日志集中 |
| cmd/vmm-local/service_darwin.go | 1,227 | launchd 配置、命令执行、状态解释和生命周期操作集中 |
| internal/app/usecase/precheck.go | 1,157 | 编排、候选处理、上下文渲染与日志集中 |
| internal/app/vector_rebuild.go | 1,131 | 重建编排、迁移接口、数据枚举及持久化步骤集中 |

SQLite `store.go` 的 AST 包含 210 个函数/方法；其中 [ApplyTurnAnalysis](../internal/adapters/outbound/vldb_sqlite/store.go#L1650) 单函数跨度 382 行。这里的主要问题是修改边界不清，而不是中英文注释本身。

仓库内 Rust `native/lancedb/src/lib.rs` 也约 2,061 行，混合 ABI、错误转换、marker、Arrow/JSON 转换、数据库操作及少量测试；可以按现有职责拆模块，但不应为了减少行数弱化 ABI 验证。

**建议**：先在原包内按 schema、execution、turn、memory、profile、workspace、rows 拆文件，并同域拆测试，保持公开方法与行为不变。检索代码按编排/排序/实体化/日志拆分。不要第一步就引入 repository 框架、插件注册器或泛型执行引擎。

### A11 · P3 · 手写路径父目录算法比标准库更长，且语义不正确

**位置**：[filepathDir](../internal/adapters/outbound/vldb_sqlite/store.go#L6470)、[调用点](../internal/adapters/outbound/vldb_sqlite/store.go#L602)。

函数手工寻找最后一个斜杠来返回父目录。Windows 实测：

- 输入 `C:\audit.db`
- 当前实现：`C:`
- 标准库 `filepath.Dir`：`C:\`

`C:` 是驱动器相对路径，和根目录 `C:\` 不等价。这里只证明父目录计算偏离平台语义，不扩大成已证实数据库写错位置。

**建议**：使用标准库 `filepath.Dir`，路径合法性由已确认的输入边界负责。这是“可以安全简单实现，却额外手写逻辑”的直接例子。

### A12 · P3 · 存在可以确定归属的遗留死代码

引用搜索确认以下符号在当前仓库没有有效生产调用：

| 符号 | 位置 | 依据 |
| --- | --- | --- |
| memoryEntryRow 及其 toDomain | [SQLite store.go:7061](../internal/adapters/outbound/vldb_sqlite/store.go#L7061) | 仅声明及接收器自身引用，未被实例化或用作查询目标 |
| SQLite decodeStringMap | [store.go:5707](../internal/adapters/outbound/vldb_sqlite/store.go#L5707) | 唯一调用来自上述未使用转换 |
| PostgreSQL decodeStringMap | [helpers.go:241](../internal/adapters/outbound/vldb_postgres/helpers.go#L241) | 仅定义，无调用 |
| ProfileStatusMerged | [turn_analysis.go:236](../internal/logic/domain/turn_analysis.go#L236) | 仅声明和解释旧名兼容的注释，无引用 |

**建议**：先清理这些经过引用链确认的小范围遗留，再考虑更大重构。不要把所有带 legacy 名称的代码都当死代码；split/controller 仍是当前支持的生产模式。

## 五、仓库规则与实际产品不一致

### A13 · P2 · 协作规范仍描述已过期的存储与协议范围

**位置**：[AGENTS.md:8](../AGENTS.md#L8)、[AGENTS.md:76](../AGENTS.md#L76)、[README.md:3](../README.md#L3)、[README.md:129](../README.md#L129)。

- AGENTS 要求不要重新引入 PostgreSQL 专用存储，但当前实际支持 combined，且 PostgreSQL adapter 有 48 个 Go 文件。
- AGENTS 和 README 部分段落称只装配 gRPC，但 [newApplication](../internal/app/app.go#L125) 会在 management.enabled 时装配管理 HTTP 服务。
- 新发行说明明确支持全部存储模式，旧协作范围却未同步。

**影响**：后续开发可能把有效产品能力误判成违规遗留，也可能依据过期文档删掉入口、存储和测试。

**建议**：统一当前支持能力与开发约束。此项是范围治理冲突，不能据旧 AGENTS 直接删除 PostgreSQL 或管理 HTTP。若产品目标是缩减模式，需要先形成明确的能力取舍，再评估代码与测试删除范围。

## 六、不应当作为“冗余”删除的内容

| 防护/测试 | 应保留的原因 |
| --- | --- |
| 关系写入结果不明、向量回滚及 GC 补偿 | 关系库与向量库不是同一事务；错误处理错误会产生断引用或误删 |
| 参数绑定、作用域限制、行数校验 | 防止查询越界及部分写入被误报成功；应合并重复测试，不应取消语义 |
| native pair marker、embedding identity、未完成迁移/重建 marker | 防止配错两套存储或在半成品数据上启动 |
| FFI 句柄并发访问、关闭互斥和缓冲区释放 | 对象跨 Go/Rust/动态库边界，生命周期检查有实际作用 |
| 标准打包目录、签名、manifest 与资产哈希验证 | 与发行包可运行性和来源验证直接相关 |
| PII 执行异常时优先脱敏，并提供原因码 | 有明确隐私语义，不是把坏数据解释成正常数据 |
| NoiseGate embedding 失败降为 regex-only | 代码有明确降级日志与策略；与持久化事实静默损坏不同 |
| gRPC panic recovery、日志脱敏、管理接口认证/请求体限制 | 保护外部输入和服务边界 |
| 用户覆盖优先、系统默认规则 | 是现有配置契约；需要改的是错误分类，不是正常默认选择 |

例如 [PII evaluator](../internal/platform/pii/evaluator.go#L179) 的异常处理返回脱敏决策和原因码；[FFI lockForCall](../internal/platform/ffi/nativelance/nativelance.go#L610) 用锁保证关闭与调用互斥。这些不能与 A07 的非法内部测试输入兼容等量齐观。

## 七、验证结果及边界

| 验证 | 结果 |
| --- | --- |
| 主模块 `rtk go test ./...` | 通过；JSON 结果中 1,303 个测试/子测试 pass 事件、19 个测试 skip，无 fail |
| `rtk go vet ./...` | 通过 |
| 第三方嵌套 Go 模块单独 `rtk go test ./...` | 通过；5 个测试，2 个包 |
| `rtk proxy python -m unittest discover -s scripts -p "test_*.py"` | 23 个测试：20 通过、3 跳过 |
| 5 个临时离线审计探针 | 全部完成；复现 A01–A05，以及 A11 的路径结果 |
| PowerShell 构建/依赖脚本语法检查 | 已检查脚本无语法错误；签名脚本用 PowerShell 7.6.5 验证通过 |

Go 的跳过项包含：未设置/未找到原生 LanceDB、未提供标准 native 打包目录、Windows 上的 Unix 权限/符号链接条件，以及真实供应商测试。**全量测试命令通过，不代表这些真实后端和打包链路已验收。**

CI 已在 PR 路径中安排真实依赖安装、标准构建、重新执行测试和强制 native 验收：[ci.yml:137](../.github/workflows/ci.yml#L137)。不能把本地 skip 直接说成 CI 完全无覆盖。

本次没有执行正式构建、Rust 全量编译测试、Linux/macOS 服务安装、真实 PostgreSQL 端到端或收费供应商请求；没有启动正式服务或改动现存数据。Rust、平台服务和发行代码的结论限于静态审计与已执行的脚本测试。

临时探针使用既有测试夹具与临时目录，完成后已删除。探针 PASS 表示准确观察到报告中的问题行为，不表示该行为符合应有契约。

## 八、建议的整改次序

1. **先恢复精确验证**：A01–A05。验收应从真实入口覆盖缺失/null/非法值，并确认错误字段和来源；不是再加一层静默兜底。
2. **减少生产分支及其衍生测试**：A06–A08；同时清理 A11–A12。先让端口与真实能力一致，再删不再代表产品契约的测试。
3. **拆分职责但保持行为**：A10。优先 SQLite 和 memory query；同一包内移动优先，避免新抽象扩大改动范围。
4. **调整测试结构与仓库规范**：A09、A13。保留故障和数据完整性测试，减少措辞锁定、重复模板以及宽松替身造成的假确定性。

整改后应继续运行主模块、嵌套模块及脚本测试；涉及配置、清洗、规则或打包行为时遵守仓库对应验收命令。任何存储模式删减都不应混入上述局部清理。
