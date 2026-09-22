# VulcanMemoryMesh 全代码仓库性能与冗余审核记录

## 1. 审核说明

- 审核日期：2026-08-23
- 审核状态：已完成
- 审核性质：只审查并记录，不修改生产代码
- 审核重点：性能与效率、重复转换、可从分支/逐请求提升为全局处理的工作、明显冗余、无必要验证
- 事实要求：每项疑问先记录，后续必须通过源码、协议、调用链、测试或命令验证后再定性

## 2. 项目理解

### 2.1 当前已确认事实

1. 仓库是 VulcanMemoryMesh 的 OSS 本地版，依赖方向应保持 `adapters -> app -> logic/domain`。
2. 正式业务入口是 gRPC；管理 HTTP 能力由 `management.enabled` 条件装配，不是不可达的历史模块。
3. PostAction 入站先做媒体、base64、`<think>` 等文本清洗，`MessageNormalizer` 只负责标准轮次归一化，`NoiseGate` 在关系存储写入前执行。
4. 存储存在三条可达生产分支：`split(SQLite + LanceDB)`、`controller(vldb-controller 托管同一数据布局)`、`combined(PostgreSQL)`；三者的事务、结果不确定和补偿边界不能机械合并。
5. AI 能力包含 LLM、Embedding、Rerank、多路由与 Key 容灾；Embedding 模型具有固定维度契约，不能仅为了减少分支而任意全局合并。
6. 标准构建产物位于 `output/bin/`，配置位于 `output/configs/`，必须通过 `make.ps1` 或 `make.bat` 构建。
7. `newApplicationWithOptions` 是当前组合根：先创建日志、AI、存储、NoiseGate 与 PII，再装配 Workspace/Profile/Memory/ChatCompact/PreCheck/PostAction/Retention，最后装配 gRPC；当 `management.enabled=true` 时还会额外装配管理 HTTP 服务。
8. `split`、`controller`、`combined(PostgreSQL)` 都存在当前生产装配分支，`vldb_postgres` 不是仅凭目录即可判定的历史死代码。
9. PostAction 的后台分析从已持久化 `dehydrated_content` 重建 turn；`PostActionCommand.RawUserContent`、`RawAssistantContent`、`RawTimeline` 当前没有生产消费者。

### 2.2 审核后确认的核心链路

1. PreCheck 先解析完整 `SessionRef`，再生成去重 query embedding，逐 query 执行向量/FTS 混合召回、关系行物化、rerank、Weibull 衰减、context edge 扩展和 MMR；同一请求已有 context-edge cache，但没有跨 query 的关系行 cache。
2. PostAction 在 gRPC 入站完成克隆、校验与清洗，usecase 再执行标准化、PII、NoiseGate、关系库存储和异步入队；worker 从持久化脱水 JSON 重开分析，候选经过统一 reviewer 后写入关系库与向量库。
3. SQLite 通过 JSON FFI 自动提交并依赖 rows-changed/回查来处理提交结果不确定；PostgreSQL 使用事务与 `SET LOCAL`；controller 是对两套本地数据库能力的 gRPC 托管，不允许把 mutation 传输失败当作可盲目重放。
4. AI 客户端按 key 缓存；route/node/key 的预算与 failover 是必要分层。可优化点集中在 token 重估、hint 重合并、embedding 向量深拷贝、Prompt/Schema 每请求重建，而不是取消多 provider 分支。
5. Retention、Vector GC、迁移和向量重建均是当前可达维护链路；管理 HTTP 在配置开启时可达，PostgreSQL combined 分支也有正式装配与测试，均未按目录名称误判为死代码。
6. 配置按 `base.yaml -> 随包 config.yaml -> 用户覆盖 -> 显式占位符环境变量` 合并；当前随仓 `config.yaml` 会覆盖基础日志开关并开启 payload debug。Prompt bundle 在启动时做完整性校验，但正文仍在每次 LLM 调用时读盘。

## 3. 覆盖记录

### 3.1 已审核

| 范围 | 文件 | 审核状态 | 说明 |
|---|---|---|---|
| 仓库约束 | `AGENTS.md` | 已审核 | 已确认分层、构建、运行、配置、测试与文档同步规则 |
| 依赖清单 | `go.mod` | 已审核 | 已确认 Go 版本、主要依赖和本地 controller 替换边界 |
| 全仓源码 | 全部 329 个受版本控制 Go 文件 | 已审核 | 225 个手写生产 Go 文件完成 AST 结构审查；热路径、存储、FFI、controller、入口与关键算法完成函数体和调用链复核；100 个测试文件、4 个生成文件分别按下述边界处理 |
| 应用装配 | `internal/app/app.go`、`runtime_pipeline.go`、`runtime_storage.go`、`runtime_transport.go`、`runtime_shutdown.go`、`llm_output_logging.go` | 已完成结构审查与关键函数体审核 | 已确认运行时组件、三种存储分支、管理 HTTP 条件装配和关闭序列 |
| PostAction 入站 | `grpcapi/server_business.go`、`grpcapi/validation.go`、`usecase/postaction.go`、`postaction_intake.go`、`pii_redaction.go`、`textutil/postaction_sanitizer.go` | 已完成第一轮函数体审核 | 已确认 normalize、克隆、清洗、命令投影、PII、持久化与入队顺序 |
| 记忆检索 | `memory_query*.go`、`precheck*.go`、`postaction_candidate_review.go` 及对应 ports/domain | 已完成关键链路函数体审核 | 已确认 scope 解析、批量 embedding、vector/lexical 融合、关系行物化、rerank、衰减、context edge、MMR 与 hard-dedupe 顺序 |
| AI 适配 | `ai_key_failover/`、`providerinput/`、`providerhint/`、OpenAI/OpenRouter/Google/DashScope/SiliconFlow/Vulcan inference 适配器 | 已完成结构审核与请求/响应转换热点复核 | 已确认客户端按 key 缓存、路由预算、provider hint、embedding 拆批与响应重绑定 |
| 持久化 | `vldb_sqlite/`、`vldb_postgres/`、`vldb_lancedb/`、`vldb_controller/`、`storageutil/` | 已完成结构审核，关键写入/检索/回收函数已复核 | 已区分 SQLite FFI 的自动提交与提交未知对账、PostgreSQL 事务、LanceDB JSON/FFI 边界和 controller IPC |
| 平台层 | `internal/platform/{ffi,logx,pii,textutil,storagemigrate,timeutil,trace,xid}`、`internal/testutil`、`internal/buildinfo` | 已完成结构审核，日志/PII/文本/FFI 热点已复核 | PII/Noise 规则均在加载期编译；运行期重复工作另列问题 |
| 配置 | `internal/config/` 与 `configs/*.yaml` | 已完成结构与加载链审核 | 已确认 `base.yaml -> system config.yaml -> user config` 的分层顺序；当前随仓 `config.yaml` 会把 payload debug 覆盖为开启 |
| 构建与入口 | `cmd/`、`make.ps1`、`make.bat`、`scripts/*.ps1`、`scripts/*.sh` | 已审核 | 20 个 `cmd` 文件与 6 个脚本均已审查；已确认标准打包、运行入口、依赖安装、维护工具、服务管理与跨平台差异 |
| controller 客户端 | `third_party/vldb-controller-client-go/controller/` 与生成的 `v1/*.pb.go` | 已完成结构与转换边界审核 | 手写 client/config/convert 已复核；pb 文件按生成代码边界审核 |
| 测试 | 全部 100 个 `*_test.go` | 已审核并执行 | 通过测试名称、被测符号和失败边界审查；根模块 1,120 项普通测试通过，嵌套模块 5 项通过；竞态检查暴露 A-031 |
| 配置与规则 | `configs/` 下 8 个 YAML、39 个规则 JSON、10 个 Prompt Markdown | 已审核 | 配置引用、加载优先级、规则语法、Prompt 入口与打包边界已核对；全部 JSON 可解析 |
| 协议与接口 | `proto/v1/vmm.proto`、`docs/openapi/vmm-management-v1.yaml` | 已审核 | gRPC 定义完整阅读；OpenAPI 16 条管理路由与服务端注册表核对 |
| 生成代码 | 4 个 `*.pb.go` | 已审核生成边界 | 核对生成器版本、包路径、服务/消息与源协议一致性，不把机械生成的 getter/descriptor 逐行当作手写性能问题 |
| 文档 | 根目录、`docs/`、第三方 README 共 26 个非 Prompt Markdown | 已审核 | README 与现行接口/运行/治理文档做契约复核；历史日报和旧审核文档按标题、时间与当前实现差异审查，不作为当前实现事实来源 |
| 资产与仓库元数据 | 3 个二进制资产、模块清单、许可证、Git 属性/忽略规则、环境示例 | 已审核 | 二进制仅做大小、路径、引用与打包影响审查；模块/仓库元数据用于确认依赖和审计边界 |

### 3.2 完整性口径与不逐行边界

- 审核基线是 `git -c core.quotepath=false ls-files` 返回的 432 个受版本控制文件；本报告及忽略目录中的计划文件不计入基线。
- 其中包括 329 个 Go 文件（100 个测试、4 个生成 protobuf 文件）、9 个 YAML（8 个运行配置、1 个 OpenAPI）、40 个 JSON（39 个规则、1 个 managed contract fixture）、36 个 Markdown（10 个 Prompt、26 个文档）、6 个脚本、1 个 proto、2 组 `go.mod/go.sum` 与 3 个二进制资产。
- 所有手写 Go 文件均完成结构级审核；与请求热路径、批量处理、存储往返、锁、FFI、序列化和配置装配相关的函数完成深读。测试文件用于证明契约和剔除误报，不把重复 fixture 构造逐行列为生产性能问题。
- 4 个 `*.pb.go` 只审查生成版本、协议对应关系和被手写代码消费的边界；机械 descriptor/getter 不作为人工优化对象。
- RAR/JPG 无法按源码逐行审核，只审查元数据、引用和打包影响。忽略的 `output/`、本地 `.gomodcache/`、历史 `TASK_LOG.md` 及其他未受版本控制的运行产物不属于本次全代码基线。
- 未连接真实 OpenAI/Google/OpenRouter/百炼服务、真实 PostgreSQL 或独立 controller 服务做凭据化验收；本次结论以源码、fake/单元/集成测试、真实本地 DLL 构建链和静态工具为依据。

## 4. 问题清单

### A-001：PostAction 命令保留并重复处理无生产消费者的原始影子载荷

- 状态：已证实
- 严重度：中
- 类别：重复复制、重复全文处理、死字段/死函数
- 位置：
  - `internal/adapters/inbound/grpcapi/server_business.go:194-196,254-278`
  - `internal/app/usecase/pii_redaction.go:158-175`
  - `internal/app/usecase/postaction.go:24-32`
  - `internal/app/usecase/postaction_intake.go:143-170`
- 事实链：
  1. gRPC 入口为清洗前日志克隆一份 `rawReq`，这份请求本身确有日志用途；
  2. `toPostActionCommand` 随后又把 raw timeline 全量投影为第二个 `[]PostActionTimelineItem`，并把 raw user/assistant 文本写入命令；
  3. `scrubPostActionCommandPII` 对标准载荷和 raw 影子载荷分别复制/扫描；
  4. 全仓结构化引用搜索显示，三个 `Raw*` 字段除构造、PII 扫描和测试断言外没有生产读取点；
  5. 唯一面向分析的 `rawTurnFromCommand` 没有调用点，后台 worker 实际通过 `turnRecordFromStoredTurn` 从已持久化 `dehydrated_content` 重建输入。
- 影响：每次 PostAction 都会产生一套无效 timeline 分配与字段复制，并对无消费者文本执行额外 PII 全文扫描；长 timeline 和复杂 PII 规则下成本随载荷线性放大，同时保留无调用函数和测试耦合。
- 建议：保留 `rawReq` 仅供入口日志使用，但从 `PostActionCommand` 删除三个 `Raw*` 字段，删除 raw timeline 投影、对应 PII 分支、`rawTurnFromCommand` 及只验证该死契约的测试；以持久化脱水 turn 作为后台分析唯一来源。
- 验证建议：增加基准覆盖 0/10/100 条 timeline 与含大量 PII 命中的载荷，对比分配次数、分配字节和单次耗时；保留日志契约测试与异步重放一致性测试。

### A-002：默认关闭 payload 记录时仍先序列化完整 payload，并执行日志专用 PII/投影工作

- 状态：已证实
- 严重度：高
- 类别：热路径无效序列化、无效全文扫描、分支位置错误
- 位置：
  - `internal/platform/logx/logger.go:142-164`
  - `internal/app/usecase/precheck.go:468-488,538-557,594-608,1106-1122`
  - `internal/app/usecase/memory_query_search.go:1310-1321` 及各阶段调用点
  - `internal/adapters/inbound/grpcapi/server_business.go:98-145`
- 事实链：
  1. 基础配置 `base.yaml` 为 `debug_rpc_payloads=false`、`protect_payloads=false`，但当前随仓 `config.yaml` 会把 debug 覆盖为 true；因此本问题发生于用户关闭 debug 且未开启保护记录的部署，而不是当前仓库开发覆盖层；
  2. `Logger.AppendPayloadFields` 在判断两个开关前先调用 `jsonMarshalPayload(payload)`，随后在无保护器时直接返回原字段，序列化结果被丢弃；
  3. 调用方在进入该函数前已经构造 map、切片和摘要对象；PreCheck 还会构造 `rawGroups`、遍历每个 hit 选取最佳日志样本，并对日志预览执行 PII 扫描；
  4. 这些工作覆盖 PreCheck、记忆检索与 gRPC 收发等高频阶段，payload 中可包含 recent turns、queries、候选数组与上下文条目。
- 影响：即使用户没有开启明文或加密 payload 审计，仍为每个阶段支付 JSON 编码、分配、日志专用投影和部分 PII 规则扫描成本；候选数和文本长度越大，浪费越明显。
- 建议：
  1. 在 `AppendPayloadFields` 中先判断 `!debugPayloads && payloadProtector == nil` 并立即返回；
  2. 提供统一的 `PayloadCaptureEnabled()` 或惰性 `AppendPayloadFieldsLazy(..., func() any)`，让调用方只有在确实需要 payload 时才构建摘要、克隆切片和执行日志专用脱敏；
  3. 保留标量计数、trace、阶段名等普通日志字段，不把它们与 payload 开关绑定。
- 验证建议：为关闭/明文/保护三种模式增加带计数 marshaler 与 PII scrubber 的测试，并用多 query、多候选基准比较分配和耗时。

### A-003：PostAction worker 将已持久化的标准 turn JSON 解码后又重建为同形 JSON，并重复估算 token

- 状态：已证实
- 严重度：中
- 类别：重复反序列化/序列化、重复复制、重复全文估算
- 位置：
  - `internal/adapters/outbound/storageutil/normalize.go:17-53`
  - `internal/app/usecase/postaction_queue.go:356-385`
  - `internal/app/usecase/postaction_intake.go:172-225`
  - `internal/app/usecase/postaction_analysis.go:388-412`
- 事实链：
  1. `AppendTurnRecord` 已通过 `storageutil.DehydrateTurn` 生成字段顺序和标签固定的 `user/timeline/assistant` JSON，并持久化 `DehydratedBudget`；
  2. worker 读取 pending row 后调用 `turnRecordFromStoredTurn`，把同一 JSON 完整反序列化并复制 timeline；
  3. `buildTurnAnalysisInputWithResolvedTime` 又调用 `buildPostActionTurnPayload`，按完全相同的字段、裁剪规则与标签重新构造 timeline、重新 JSON 编码，并重新跑 token estimator；
  4. reviewer 实际只额外需要 user/assistant 文本，analyzer 的 `RawTurn` 可以直接消费持久化的标准 JSON。
- 影响：每个待分析 turn 至少多一次完整 JSON 编码、一次 timeline 重建和一次 token 全文扫描；长 timeline 会增加分配和 GC 压力。
- 建议：让 queued analysis 直接携带/使用 `SessionTurnRecord.DehydratedContent` 与 `DehydratedBudget`；仅为 reviewer 解码所需的 user/assistant 字段，或由共享 turn codec 一次解码后同时返回原始标准载荷与窄字段，避免重新编码。
- 验证建议：用包含长 timeline 的 pending turn 基准比较 worker 输入准备的 `ns/op`、`B/op` 与 `allocs/op`，并做 analyzer prompt 快照等价测试。

### A-004：内部已解析 session 的记忆检索仍重复查询 user/project 范围

- 状态：已证实
- 严重度：中
- 类别：重复数据库往返、无必要重复范围验证
- 位置：
  - `internal/adapters/inbound/grpcapi/interceptors.go:88-115`
  - `internal/adapters/outbound/vldb_sqlite/store.go:1472-1524,2059-2105`
  - `internal/app/usecase/memory_query_search.go:42-51`
  - `internal/app/usecase/precheck.go:435-464`
  - `internal/app/usecase/postaction_candidate_review.go:472-540`
- 事实链：
  1. PreCheck/PostAction 在进入 handler 前已经由范围拦截器调用 `ResolveRequestScope`，SQLite 实现会查询 user、project、session 与状态，并产出带完整层级 ID/名称的 `SessionRef`；
  2. PreCheck 与 PostAction 的内部检索把该 session 降格为 `UserID/ProjectID` 后调用 `MemoryUseCase.Search`；
  3. `MemoryUseCase.Search` 再分别调用两次 `ResolveProfileTarget`，SQLite 下对应再次 `loadUserByID` 与 `loadProjectByID`；
  4. 构建搜索过滤器需要的 user/project/team/space 信息已经全部存在于 `SessionRef`。
- 影响：每次内部记忆检索固定增加两次关系库/控制器往返；PreCheck 是实时请求路径，PostAction reviewer 也会重复承担该成本。controller 模式下额外往返比进程内 SQLite 更明显。
- 建议：为内部调用提供“已解析目标”入口，直接从 `SessionRef` 构造 `ProfileTargetRef/SearchFilter`；公开 `SearchMemoryEvents` 等只有裸 ID 的入口继续保留数据库解析，不降低外部边界验证强度。
- 验证建议：使用 counting store 断言内部 PreCheck/PostAction 检索不再调用 `ResolveProfileTarget`，同时保留公开搜索的解析与越权测试。

### A-005：多 query 检索只缓存 context edges，没有缓存重叠 vector hit 的关系行

- 状态：已证实
- 严重度：中
- 类别：重复数据库往返、请求级缓存缺失
- 位置：`internal/app/usecase/memory_query_search.go:113-188,1397-1501`
- 事实链：
  1. Embedding 已按唯一 query 批量执行，完全相同 query 也已折叠；
  2. 每个不同 query 仍独立调用 `mapSearchHits -> materializeVectorHitRows`；
  3. split/controller 模式的 vector hit 不携带完整关系行，因此每组都会调用 `LoadMemoryNodesByVectorIDs`；
  4. 不同但相近的 query 很容易召回重叠 memory ID，当前只有 `memoryContextEdgeCache` 会跨组复用，关系行没有同类缓存。
- 影响：一个 PreCheck 请求中的多条语义相近 query 会重复读取和映射相同记忆行；controller 模式会额外放大 IPC 成本。
- 建议：增加请求级 `memoryNodeRecordCache`，每组只批量补查尚未命中的 vector ID；或先收集全部首阶段命中，再全局一次物化，之后按组继续融合与排序。
- 验证建议：新增两个 query 返回部分重叠 vector IDs 的 counting-store 测试，断言每个 vector ID 最多回表一次，并用 split/controller 集成基准测量。

### A-006：Embedding 响应校验与 failover 重绑定会多次深拷贝高维向量

- 状态：已证实
- 严重度：高
- 类别：重复高维数组复制、验证实现额外分配
- 位置：
  - `internal/logic/ports/embedding.go:49-83`
  - `internal/adapters/outbound/ai_key_failover/embedding.go:190-227`
  - `internal/app/usecase/memory_query_search.go:81`
  - `internal/app/usecase/memory_query_write.go:464-468`
  - `internal/app/vector_rebuild.go:447-454`
  - `internal/logic/processor/noise_gate.go:243,384`
- 事实链：
  1. `EmbeddingResponse.IndexedVectors` 为每条 vector 执行一次 `append([]float32(nil), vector...)`；
  2. `ValidateStrict` 只需要核对数量、索引与 dropped 状态，却通过 `IndexedVectors` 间接复制全部向量，然后丢弃复制结果；
  3. failover 的 `offsetEmbeddingResponse` 先调用 `IndexedVectors` 复制一次，又把 `item.Vector` 复制进内部结果；
  4. `buildEmbeddingResponse` 再把内部结果复制回公开响应；上层严格调用方随后还会再次进入 `ValidateStrict`；
  5. 当前默认 embedding 维度为 1024，成本随批量数和维度线性增长。
- 影响：搜索、直写、PostAction、NoiseGate 预热/判定和向量重建均承担额外内存带宽、分配与 GC；批量 embedding 时放大明显。
- 建议：拆出不复制 vector 的索引校验函数；`ValidateStrict` 直接验证元数据；failover 重绑定只转移 slice 所有权一次，最终响应不要再次深拷贝。若确需隔离可变所有权，应只在公开边界做一次并在契约中明确。
- 验证建议：新增 1/16/64 条、1024 维响应的基准，记录 `B/op` 与 `allocs/op`；覆盖 partial dropped、乱序 result indices 和严格响应。

### A-007：普通记忆搜索也构造并多次深拷贝仅 PostAction hard-dedupe 需要的向量结果

- 状态：已证实
- 严重度：高
- 类别：分支专用数据被全局构造、重复高维数组复制、无效返回字段
- 位置：`internal/app/usecase/memory_query_search.go:113-196,314-339,1397-1468`
- 事实链：
  1. `EnableHardDedupePool` 只有 PostAction 候选复核显式设为 true；普通 gRPC 搜索和 PreCheck 不需要 `QueryVector`、`HardDedupeHits`；
  2. 即使该开关为 false，`Search` 仍创建 `cachedHardDedupeHits`、`cachedQueryVectors`，并把 top-k 命中作为 hard-dedupe 窗口；
  3. `mapSearchHits` 已复制一次每条关系行 vector，写入缓存时 `Hits` 与 `HardDedupeHits` 分别深拷贝，组装最终结果时又各深拷贝一次；query vector 也在缓存和结果阶段各复制一次；
  4. gRPC `SearchMemoryEvents` 只投影 `group.Hits` 的业务字段，不返回 query vector 或 hit vector；PreCheck 同样不消费 hard-dedupe 专用字段；
  5. MMR 可能在排序阶段需要候选 vector，但排序完成后普通结果无需继续携带这些高维数组。
- 影响：常规搜索也按 `query 数 × candidate 数 × embedding 维度` 重复占用内存和复制；多 query、top-k 较大时可能成为请求级主要分配来源。
- 建议：把搜索输出分为普通结果与 `IncludeHardDedupeArtifacts` 内部模式；只有 hard-dedupe 请求保留 query vector 与 pre-MMR vector hits。MMR 完成后对普通结果清除/不投影 vector，并避免缓存与最终结果的双重深拷贝。
- 验证建议：分别基准普通 gRPC 搜索、PreCheck、PostAction hard-dedupe；普通路径断言 `QueryVector/HardDedupeHits` 不构造，PostAction 仍保持余弦判定等价。

### A-008：LLM failover 对同一 prompt 重复估算 token，并在 wrapper/provider 两层重复合并 hints

- 状态：已证实
- 严重度：中
- 类别：重复全文扫描、重复 map 合并
- 位置：
  - `internal/adapters/outbound/ai_key_failover/llm.go:70-91`
  - `internal/adapters/outbound/ai_key_failover/request_cost.go:39-51,103-112`
  - 各 provider 的 `LLMClient.Generate` hint 合并入口
- 事实链：`Generate` 先以 `estimateTextTokens(system,user)` 得到 usage fallback，随后 `estimateLLMRequestCost` 又对同一两段 prompt 调用一次 estimator；wrapper 为预算先合并 provider hints，具体 provider 为请求体再次合并同组 base/model/request hints。
- 影响：每个 LLM 请求固定多一次 prompt 全文扫描和一次多层 map 合并；长 reference/history prompt 下更明显。
- 建议：让 cost 计算返回已经得到的 prompt token 数并复用于 usage fallback；将解析后的 effective hints 随内部请求传递，或让 cost 估算只读取具体字段，避免完整二次 merge，同时保留 provider 直连边界的独立规范化能力。
- 验证建议：长 prompt 基准；验证预算预留、fallback usage 与 provider 请求体在重构前后一致。

### A-009：PostAction 成功日志在非明文模式仍重建完整分析 JSON 并计算摘要

- 状态：已证实；是否保留摘要属于日志审计策略决策
- 严重度：中
- 类别：日志热路径对象重建、JSON 编码、全文哈希
- 位置：`internal/app/usecase/postaction_analysis.go:185-375`
- 事实链：
  1. 每个成功分析都先克隆 `MemoryNodes` 以清空 vector；
  2. `marshalTurnAnalysisForLog` 本身已使用不含 `Vector` 字段的专用 DTO，因此前置 vector 清空克隆不影响最终 JSON，只用于统计含向量节点数；
  3. 函数又重建 memory/context/profile DTO 切片并 JSON 编码；当 payload debug 关闭时不输出 JSON，而是继续对完整 JSON 计算 SHA-256；
  4. 日志已经同时记录 details 长度、节点数、vector 数、压缩率和各类 drop 计数；测试固化了 digest，但文档只承诺“安全摘要”，没有规定必须为完整派生载荷摘要。
- 影响：PostAction 成功主链对所有派生文本再次做一轮分配、编码和哈希；候选/情境边较多时成本线性上升。
- 建议：先移除无效 vector-redaction 克隆，直接扫描计数；仅在 debug/protected payload 模式构建完整日志 DTO。若确需非明文关联摘要，应确认审计需求后，改为对持久化结果 ID/稳定去敏字段生成窄摘要，或提供独立开关。
- 验证建议：保留明文模式不含 Vector 的测试；为关闭、保护、明文三种模式增加计数编码器/基准，并由产品确认 digest 的审计价值。

### A-010：PostAction 文本清洗在常见短文本/未截断路径重复执行全量 whitespace 规范化

- 状态：已证实
- 严重度：中
- 类别：重复全文扫描、分支后统一处理
- 位置：
  - `internal/platform/textutil/postaction_sanitizer.go:27-45,49-169`
  - `internal/platform/textutil/memory_cleaner.go:69-98`
  - `internal/platform/textutil/token_budget.go:212-257`
- 事实链：`CleanConversationTextForMemory` 末尾已经规范化 whitespace；短于 cleaner 免疫阈值的文本从 `CleanMemoryTextWithConfig` 原样返回，随后仍再次规范化；`EnforceTokenBudget` 未超预算时返回同一字符串，但调用方最后再规范化一次。短文本且未超预算是至少三次同类全量行扫描。
- 影响：PostAction 的 user、assistant 及每条 timeline item 都分别支付重复扫描和 builder 分配；timeline 越长，固定浪费越多。
- 建议：为清洗阶段建立一次性“已规范化/是否截断”结果契约；短文本直接跳过第二次 normalize，预算函数返回 `(text, truncated)`，只在确实发生头尾拼接时做最终规范化。不要删除资源占位符内部的局部 normalize。
- 验证建议：用短文本、长自然语言、代码块、媒体、超预算密集文本做快照等价测试和基准。

### A-011：多个热路径辅助函数反复创建相同的不可变 token estimator

- 状态：已证实
- 严重度：低
- 类别：可提升为包级共享实例的重复初始化
- 位置：
  - `internal/adapters/outbound/storageutil/normalize.go:57-64`
  - `internal/app/usecase/postaction_intake.go:196-236`
  - `internal/app/usecase/precheck.go:424-431`
  - `internal/platform/textutil/token_budget.go:212-257`
- 事实链：这些入口每次都用相同的 `DomesticTokenEstimatorConfig()` 调用 `NewTokenEstimator`；estimator 只保存规范化后的不可变配置。`ai_key_failover/request_cost.go` 已采用包级不可变 estimator，证明该模式在仓内可用。
- 影响：单次成本不大，但在 PreCheck/PostAction/存储预算估算中高频重复；同时散落的 helper 容易造成估算配置漂移。
- 建议：在 `textutil` 提供并复用统一的默认估算入口/只读实例；自定义预算配置仍保留独立 estimator。
- 验证建议：现有 token 测试加并发调用；基准确认无共享可变状态与行为变化。

### A-012：配置分层加载对每个配置文件执行两次磁盘读取和重复 YAML/JSON 解码

- 状态：已证实
- 严重度：低
- 类别：启动期重复 I/O、重复转换
- 位置：`internal/config/config_load.go:28-83,142-160,500-555`
- 事实链：`collectReferencedEnvKeysFromConfigPaths` 先逐文件 `ReadFile` 并解析原始层以收集环境变量引用；返回后 `LoadPaths` 紧接着再次逐文件 `ReadFile` 存入 `layerBodies`，随后又执行 YAML→JSON 与严格 JSON 解码。环境引用预扫描本身必要，但文件读取与中间解析结果没有复用。
- 影响：只发生在启动/维护工具加载配置时，严重度低；配置层或 YAML 体积增大时会增加冷启动工作。
- 建议：首轮一次读取并缓存 raw bytes，同时收集 env 引用；环境加载后基于缓存 body 做 ExpandEnv 与最终 decode。保持“先发现 .env 引用、再展开、最后严格校验”的语义顺序。
- 验证建议：现有分层配置、YAML anchor、env 引用测试全部保留，并用计数文件系统或小型基准断言每层只读取一次。

### A-013：PostgreSQL 生产批量写入仍逐条往返，未复用仓内已有 `pgx.Batch` 模式

- 状态：已证实
- 严重度：中
- 类别：逐条数据库往返、批处理能力未下沉到生产路径
- 位置：
  - `internal/adapters/outbound/vldb_postgres/analysis_store.go:271-500`
  - `internal/adapters/outbound/vldb_postgres/memory_store.go:616-646`
  - `internal/adapters/outbound/vldb_postgres/noise_cache.go:69-122`
  - `internal/adapters/outbound/vldb_postgres/debug_migrate.go:455-477`
- 事实链：PostAction 分析逐节点 `QueryRow RETURNING`，再逐 context edge `Exec`，画像节点也逐条插入；向量重建逐记录 UPDATE；Noise embedding cache 逐 entry INSERT。相同仓库的迁移实现已经使用 `pgx.Batch/SendBatch`，说明驱动与项目模式均支持批量发送。
- 影响：combined PostgreSQL 模式下，节点/情境边/向量重建批次会产生大量客户端—数据库往返；远程 PostgreSQL 的延迟放大尤为明显。
- 建议：在事务内按阶段使用 `pgx.Batch` 或集合型 SQL：先批量插入需返回 ID 的节点并逐结果读取，再按得到的 ID 批量写 edges/profile；向量重建与 noise cache 直接批量发送。每个结果仍必须校验，不降低事务与行数验证强度。
- 验证建议：增加 PostgreSQL 集成基准与 batch 结果漂移测试；比较 1/16/64 个节点或向量记录的 round trips 与耗时。

### A-014：SQLite FTS 同步对每条文档执行一次 FFI/IPC 调用，接口层没有批量能力

- 状态：已证实
- 严重度：中
- 类别：逐条 FFI/IPC 往返、批处理接口缺失
- 位置：
  - `internal/adapters/outbound/vldb_sqlite/store.go:5889-5925`
  - `internal/platform/ffi/sqliteffi/sqliteffi.go:140-164,499-539`
  - `internal/adapters/outbound/vldb_sqlite/controller_database.go:187-233`
- 事实链：关系写入完成后，`syncMemoryFTSAfterWrite` 对每个 inserted/deleted memory 分别调用 `UpsertFtsDocument/DeleteFtsDocument`；直接 FFI 与 controller RPC 契约都只提供单文档操作，没有 batch 入口。关系层本身已有 `ExecuteBatch`，但无法覆盖独立 FTS API。
- 影响：一次分析、恢复或回收包含多条 memory 时，FTS 同步按文档数线性增加跨语言/跨进程调用；controller 模式下更明显。
- 建议：在 vldb-sqlite/controller 协议层新增批量 FTS mutation，返回逐项状态与整体成功信息；VMM 聚合一次调用并只在批量失败时执行一次 rebuild fallback。该项涉及跨仓协议，需要单独确认与版本协同。
- 验证建议：直接 FFI 与 controller 两种模式分别基准 1/16/64 文档，并覆盖部分失败、全量重建回退和关系/FTS 一致性。

### A-015：Vulcan inference 的每个请求都在全局互斥锁内执行 discovery 文件 `Stat`

- 状态：已证实
- 严重度：中
- 类别：请求热路径文件系统 I/O、并发串行化
- 位置：`internal/adapters/outbound/vulcan_inference/client.go:728-755`
- 事实链：LLM/embedding/rerank 每次调用都会进入 `discovery(false)`；函数先获取同一个 mutex，再执行 `os.Stat`，只有比较完 modtime 才返回缓存。并发请求因此在文件系统元数据查询上串行，缓存命中仍不能避免 I/O。
- 影响：使用 Vulcan inference provider 时，所有 AI 请求多一次文件系统访问并争用同一锁；高并发和慢/受安全软件拦截的 Windows 文件系统上尾延迟更明显。
- 建议：缓存 grant 至“短轮询间隔或过期安全窗”内无条件复用；轮询时用单飞刷新或后台 watcher，401 仍强制刷新。校验 process/caller/profile/loopback 的安全逻辑必须保留，只降低检查频率。
- 验证建议：并发 benchmark 使用可计数 stat/read 抽象，验证缓存窗内只检查一次，401/过期/modtime 改变仍能立即刷新。

### A-016：五类静态 Prompt 在每次 LLM 调用前重新从磁盘读取

- 状态：已证实；缓存失效策略需确认是否支持未文档化的运行期热改
- 严重度：中
- 类别：请求热路径文件 I/O、可提升为启动期全局处理
- 位置：
  - `internal/config/manager.go:25-62`
  - `internal/logic/processor/intent_extractor.go:47-88`
  - `internal/logic/processor/precheck_memory_reviewer.go:66-105`
  - `internal/logic/processor/turn_analyzer.go:40-88`
  - `internal/logic/processor/postaction_candidate_reviewer.go:42-82`
  - `internal/logic/processor/manual_profile_reviewer.go:40-80`
- 事实链：`NewPromptManager` 启动时已经验证选中 bundle 完整，但没有缓存正文；每次 `GetPrompt` 都先尝试读取 user path，失败后再读取 system path。五个 processor 均在各自每次请求中调用该方法。文档没有声明无需重启的 Prompt 热加载契约。
- 影响：每次 L1/L2 分析固定增加一次或两次文件打开/读取；用户覆盖目录不存在时还固定制造一次失败 I/O。Windows 杀毒/索引器、机械盘或高并发下会增加尾延迟和文件系统争用。
- 建议：在 `NewPromptManager` 中按“用户 bundle 整体优先、系统 bundle 兜底”的现有规则一次性加载五个 scene 并保存只读 map；若确实需要热改，使用显式 reload/watch + 原子快照，不要在每个请求上探测文件。
- 验证建议：计数文件系统测试断言运行期请求不再读盘；覆盖用户 bundle 整体优先、bundle 不完整启动失败、显式 reload（若保留）与并发读取。

### A-017：静态 Structured Output Schema 在每次 LLM 调用中重复反射、建树和 JSON 编码

- 状态：已证实
- 严重度：中
- 类别：重复反射、重复对象图构建、重复 JSON 编码、分支无效工作
- 位置：
  - `internal/logic/processor/structured_output_schema.go:125-213`
  - 五个 processor 调用 `structuredOutputFor[...]` 的请求方法
  - `internal/adapters/outbound/vulcan_inference/client.go:359-439`
  - OpenAI/OpenRouter/Google 的 `Generate` 实现
- 事实链：五种 response payload 类型、schema 名称和说明都是静态的，但每个请求都通过 reflect 递归建立 map/slice schema 树并 `json.Marshal`；只有 Vulcan inference 适配器读取 `request.StructuredOutput`，当前 OpenAI/OpenRouter/Google 适配器未消费该字段，因此随仓默认 OpenAI 路由支付了生成成本却不把 schema 发给 provider。
- 影响：每个 LLM 阶段在网络调用前产生固定反射与分配成本；默认 provider 下这部分当前完全无下游消费者。同时“端口声明必须强制 schema”与部分适配器实现不一致，虽非本次性能主轴，但会影响优化边界。
- 建议：每种 schema 用包级 `sync.Once` 或启动期构造一次只读值；provider 若支持原生 structured output，应明确映射；不支持时应在装配/配置校验阶段显式拒绝或声明 prompt+本地校验模式，避免每请求生成死数据。该契约选择可能涉及对外行为，应先确认。
- 验证建议：schema 快照与并发复用测试；按 provider 验证请求中是否实际携带 schema，并为不支持分支增加装配期测试。

### A-018：MessageNormalizer 先构造完整中间切片再二次遍历，并重复规范化文本

- 状态：已证实
- 严重度：低
- 类别：冗余中间分配、重复全文规范化、批次级时间重复获取
- 位置：
  - `internal/logic/processor/normalizer.go:23-68`
  - `internal/platform/textutil/text.go:160-210`
- 事实链：
  1. `Normalize` 首轮把所有消息转换为私有 `cleaned` 切片，第二轮才做 user/assistant 配对；配对状态完全可以在首轮清洗后立即推进；
  2. `ExtractTextFromAny` 的所有可用返回分支已经执行 `NormalizeWhitespace`，调用方又对结果执行一次同函数；
  3. 每次 flush 都单独调用 `time.Now().UTC()`，同一批标准化 turn 没有复用一次解析时间；
  4. `ExtractTextFromRawJSON` 对对象输入依次尝试 string、list、object 三次 `json.Unmarshal`，可根据首个非空白字节直接分派。
- 影响：每次 PostAction 标准化增加与消息数成正比的中间对象和重复文本扫描；单次影响较小，但属于明确可删除的冗余。
- 建议：改为单遍状态机；统一约定 `ExtractTextFromAny` 返回已规范化文本并删除外层 normalize；批次开始解析一次 `now`；Raw JSON 按首字符选择目标类型。
- 验证建议：保留多 user、孤立 assistant、tool call、RawMessage object/list/string 和 turn 顺序测试，并增加 1/10/100 条消息基准。

### A-019：MMR 每轮重新计算候选与全部已选项的相似度，复杂度高于必要水平

- 状态：已证实
- 严重度：高
- 类别：重复高维计算、可增量复用的分支内全量重算
- 位置：`internal/app/usecase/memory_query_search.go:1038-1126`
- 事实链：每选中一条 hit 后，外层下一轮会遍历所有未选候选；`maxMMRSimilarity` 又从头遍历全部 `selected` 并重新计算 cosine。此前轮次已计算过的“候选—已选项”相似度没有保存；`cosineSimilarityFloat32` 还在每一对比较中重复计算两个 vector norm。
- 影响：候选数为 n、最终选择 k 时约执行 `O(n*k²)` 次高维扫描，k 接近 n 时趋近立方级 pair 工作；top-k 上限为 100、默认 1024 维时，极端请求会产生大量浮点计算。当前基础配置默认开启 MMR，因此不是冷分支。
- 建议：为每个候选维护当前 `maxSimilarity`，每新增一个 chosen 只计算候选与该新向量的一次 cosine 并更新最大值；同时预计算每个 vector norm。可降为约 `O(n*k*d)`，且无需 n² 矩阵。
- 验证建议：保留稳定排序与 tie-break 快照，增加 n=10/50/100、d=1024 的 benchmark，并用计数 similarity 函数断言 pair 计算次数从重复重算降为每对最多一次。

### A-020：`post_action.input_mode` 是无运行时消费者的死配置，却保留多层归一化与验证

- 状态：已证实
- 严重度：中
- 类别：死配置、无效验证、文档/实现语义漂移
- 位置：
  - `internal/config/config.go:457-520,615-766`
  - `internal/config/config_runtime.go:458-460,512`
  - `internal/config/config_validate.go:457-461,757`
  - `internal/config/managed.go:483-484`
  - `docs/unused-config-parameters_CN.md`
- 事实链：全仓结构化引用仅存在字段声明、YAML/JSON 解码、默认值、normalize、普通配置验证、managed 配置验证、环境覆盖和测试，没有进入 gRPC validator、PostAction usecase 或运行时装配。仓内文档也明确记录 `compat/strict` 当前没有运行时差异。
- 影响：用户会误以为切换模式能够改变入站契约；代码为一个无业务效果的字段维护两套验证和大量配置测试，增加认知与维护成本。性能成本只在启动期，主要风险是冗余与错误预期。
- 建议：二选一并先确认产品契约：若无需两种模式，完整移除字段、env override、验证、示例与测试；若确有需求，先定义 strict/compat 的准确差异并接入唯一运行时分支，避免继续保留“可配置但无效果”的状态。
- 验证建议：移除方案需测试旧配置给出明确迁移错误；接线方案需增加同一输入在两模式下的契约差异测试，并同步 README/PostAction 指南。

### A-021：管理面批量 session 操作存在 N+1 状态查询，SQLite 影响预览还拆成多次计数往返

- 状态：已证实
- 严重度：中
- 类别：N+1 查询、可由分支逐项提升为集合处理
- 位置：
  - `internal/app/usecase/management.go:333-374`
  - `internal/adapters/outbound/vldb_sqlite/management_mutations.go:85-183,243-300`
  - `internal/adapters/outbound/vldb_postgres/management_mutations.go:46-155,213-270`
- 事实链：管理选择允许最多 100 个唯一 target ID。session 预览在 SQLite/PostgreSQL 都逐 ID 调用状态查询；执行前为防漂移再次逐 ID查询（再验证本身必要，但实现是 N+1）。SQLite 预览的 session/turn/pending/memory/context/profile/hot/protected/maxUpdated 还分别通过多次 `countRows/int64` FFI 查询，而 PostgreSQL 已证明这些计数可用一条含标量子查询的 SQL 聚合。
- 影响：100 个 session 的预览与执行会产生上百次关系库/FFI/IPC 往返；controller 模式和远程 PostgreSQL 下延迟放大。管理面不是每请求热链，但批量操作正是该接口的目标场景。
- 建议：新增一次性加载 `session_id -> status` 的集合查询并在内存验证全部 transition；执行事务内用 `WHERE session_id IN/ANY` 加锁后集合复核。SQLite 影响计数合成一次 QueryJSON；archive/restart 写入可使用同构 batch/集合 SQL，同时保留精确 rows-changed 和提交未知边界。
- 验证建议：1/10/100 target 的 counting-store/集成测试，断言状态查询固定一次；覆盖某一 target 状态非法、并发漂移、部分写入和 controller 模式。

### A-022：画像过期收敛按目标逐次加载 active 节点，PostgreSQL 渲染画像回写还逐条更新

- 状态：已证实
- 严重度：中
- 类别：N+1 查询、批量写入缺失
- 位置：
  - `internal/adapters/outbound/vldb_sqlite/store.go:2898-3050`
  - `internal/adapters/outbound/vldb_postgres/profile_store.go:439-527,538-568,681-687`
- 事实链：两种关系库都会先批量选出最多 256 个过期画像节点并按 `(profile_type, bind_id)` 去重，但随后为每个目标单独调用 `loadActiveProfileNodes`。SQLite 的渲染画像回写已经按 scope 使用 `ExecuteBatch`；PostgreSQL 的 `replaceRenderedProfileBatch` 名称虽为 batch，实际仍遍历 binding ID 并逐次 `UPDATE`。
- 影响：一次过期收敛涉及的目标越分散，读查询次数越接近目标数；渲染完成后 PostgreSQL 再产生同数量级的写往返。远程 PostgreSQL 与 controller/FFI 边界都会放大此成本，并延长事务持有时间。
- 建议：按 profile type 分组，使用 `(profile_type, bind_id)` 集合条件一次加载全部 active 节点并在内存归组；PostgreSQL 回写使用 `pgx.Batch`，或用 `VALUES`/`UNNEST` 驱动单条集合 `UPDATE`。必须保留过期状态切换与快照读取处于同一事务/SQLite 写锁窗口的语义。
- 验证建议：构造 1、64、256 个分散目标，断言查询次数由 O(targets) 降为每 scope 常数；覆盖同一目标多个过期节点、空 active 集、事务回滚、提交结果未知和精确影响行数。

### A-023：启动阶段第二个日志 writer 创建失败时，第一个 writer 尚未纳入回收链

- 状态：已证实
- 严重度：低
- 类别：异常路径资源泄漏、初始化顺序冗余
- 位置：
  - `internal/app/app.go:81-110`
  - `internal/app/llm_output_logging.go:31-45`
- 事实链：`newApplicationWithOptions` 先成功创建主 `HourlyFileWriter`，随后创建可选 LLM output writer；只有第二步成功后才建立 `startupShutdowns`、登记主 writer 并安装失败清理 `defer`。若第二个 writer 创建失败，函数直接返回，主 writer 的当前文件句柄未被关闭。
- 影响：仅发生在开启 LLM output 日志且其专用文件无法创建的启动失败路径；反复拉起会暂时累积文件句柄，并使 Windows 上日志目录/文件清理更困难。
- 建议：主 writer 创建成功后立即安装最小清理 `defer`，再继续创建其余资源；或先初始化回收栈并在每个资源成功创建后立即登记。
- 验证建议：为第二个 writer 注入可重复失败点，断言主 writer 的 `Shutdown` 被调用；Windows 下补充失败后日志目录可重命名/删除测试。

### A-024：文本日志 handler 对同一 JSON 字符串连续执行两次解码与格式化

- 状态：已证实
- 严重度：中
- 类别：重复转换、日志热路径
- 位置：`internal/platform/logx/text_handler.go:165-170,192-195,212-233,273-288,314-335`
- 事实链：每个字符串属性先经 `detectRenderedFieldMode -> detectRenderedTextMode -> tryPrettyJSON` 完整 `Unmarshal + MarshalIndent`，返回的格式化正文被丢弃；紧接着 `renderAttrValue` 再对同一个字符串调用 `tryPrettyJSON`，重复相同转换。分组属性走同样路径。
- 影响：当 payload、模型输出、分析摘要等 JSON 字符串进入文本日志时，CPU、分配和临时对象数量近似翻倍；当前随仓配置启用了 payload debug，因此该路径具有现实可达性。
- 建议：用单个 `renderAttr` 步骤同时产出 `Mode` 与 `Text`，只解析/格式化一次；非 JSON 快速路径仍保留首字节检查。对 `slog.KindAny` 也应让模式与实际渲染文本共同决定，避免检测与渲染分离后再次漂移。
- 验证建议：新增 JSON 字符串、非法 JSON 前缀、普通文本、group/Any 的基准与分配断言；对比优化前后的 `ns/op`、`B/op`、`allocs/op`，并保持日志快照不变。

### A-025：仓库与正式配置树保留无引用备份/示例二进制资产

- 状态：已证实无代码、脚本或文档引用；资产保留目的待确认
- 严重度：低
- 类别：明显冗余、打包污染
- 位置：
  - `configs/prompts/提示词备份.rar`（32,377 字节）
  - `temp/images/cyberpunk_city_night.jpg`（508,829 字节）
  - `temp/images/enchanted_fantasy_forest.jpg`（501,987 字节）
  - `scripts/vmm.ps1:73-80,184-205`
- 事实链：全仓引用搜索对三个文件名及 `temp/images` 均为零命中。两个图片不参与构建；RAR 位于 `configs/prompts`，而标准 Windows 构建会无筛选复制整个 `configs` 树到 `output/configs`，所以它会进入正式运行包。Windows `Resolve-SourceIdentity` 还会读取并哈希所有 Git 已跟踪/未忽略文件。
- 影响：约 1 MiB 仓库噪音；RAR 额外污染正式配置交付物并参与每次源状态摘要。体积本身不大，但它模糊了“运行时配置树只含可消费资源”的边界。
- 建议：确认资产归属后移出仓库或迁入明确的 `docs/assets`/外部归档；至少从 `configs` 正式复制白名单中排除备份压缩包。不要在未确认历史用途前直接删除。
- 验证建议：构建后检查 `output/configs/prompts` 仅含两个合法 bundle 的五个 Prompt；运行 PromptManager 启动校验与全量测试。

### A-026：controller 客户端每次数据库操作前都新建临时 gRPC 连接执行健康探测

- 状态：已证实
- 严重度：高
- 类别：请求级工作可提升为连接级状态、额外网络往返、并发初始化竞态
- 位置：
  - `third_party/vldb-controller-client-go/controller/client.go:71-82,237-539,543-572,610-629,634-703`
  - `internal/adapters/outbound/vldb_sqlite/controller_database.go`
  - `internal/adapters/outbound/vldb_lancedb/controller_engine.go`
- 事实链：所有 SQLite/LanceDB 读写方法都进入 `withSession` 或 `withMutationSession`；两者每次先调用 `Connect`。`Connect` 无已连接快速路径，固定先执行 `ensureReady -> probeStatus`，而 `probeStatus` 每次创建一条新的阻塞 gRPC 连接、调用 `GetStatus`、再关闭，之后才复用正式业务连接。`ensureConnected`/`ensureRegistered` 又在慢操作前释放互斥锁且没有 singleflight/二次检查，并发首次连接或恢复时可能重复 dial/register，覆盖旧连接引用。
- 影响：controller 模式下每个 SQLite JSON 查询、batch、FTS 操作以及每个 LanceDB 操作都额外支付一次连接握手、状态 RPC 和关闭成本；本报告已发现的 N+1 链路会进一步乘上该开销。并发初始化/恢复还可能造成连接与租约风暴，并留下被覆盖连接无法正常关闭的风险。
- 建议：把 readiness 探测限制在首次连接/显式恢复：`Connect` 先在锁内检查完整健康状态（正式连接、RPC、session ID），命中即返回；丢失由实际 RPC 的 recoverable error 驱动 `resetConnection + Connect`。使用单次初始化门闩/singleflight 或锁内状态机串行化 dial/register/replay；不要对结果不确定的 mutation 增加重放。
- 验证建议：fake controller 统计 100 次顺序/并发 Query、Execute、VectorSearch 的 `GetStatus`、Dial、Register 次数，健康态应为常数；覆盖首次启动、controller 进程重启、租约丢失、并发恢复和 mutation outcome uncertain。

### A-027：已被本地 FFI 取代的 SQLite/LanceDB `address` 仍贯穿配置、环境变量、示例与测试

- 状态：已证实
- 严重度：低
- 类别：死配置、无必要桥接与验证面
- 位置：
  - `internal/config/config.go` 的 `SQLiteConfig.Address`、`LanceDBConfig.Address`
  - `internal/config/config_runtime.go:484-486`
  - `internal/config/config_validate.go:59-63,697-700`
  - `configs/base.yaml:183-188` 及六份 provider 示例/模板
- 事实链：基础配置注释明确 split 模式改为随包本地 FFI 并忽略 address；全仓生产引用仅剩字段定义、TrimSpace、环境覆盖，运行时 storage 装配不读取两字段。多份示例仍声明 `${VMM_SQLITE_ADDRESS}` 与 `${VMM_LANCEDB_ADDRESS}`，测试也继续为它们构造值。
- 影响：不会造成请求期性能损失，但扩大配置解析、环境变量白名单、示例与测试维护面，并继续向使用者暗示存在远程 address 能力。它与 A-020 同属“可配置但不产生对应运行时效果”的契约债务。
- 建议：若无旧配置兼容窗口，完整移除字段和两项 env override；若必须兼容，在 loader 中仅识别并发出明确弃用告警，示例配置不要再主动展示，设定删除版本。
- 验证建议：使用包含/不包含旧字段的迁移测试，明确旧字段是拒绝、告警忽略还是限期兼容；同步 README、配置示例和未使用参数清单。

### A-028：SQLite 空闲会话回收在整个多会话批次期间持有全局写互斥锁

- 状态：已证实
- 严重度：中
- 类别：锁粒度过大、维护任务阻塞前台写入
- 位置：`internal/adapters/outbound/vldb_sqlite/retention_store.go:220-292,413-680`
- 事实链：`RecycleIdleSessions` 在候选查询前取得 `s.writeMu`，直到最多 32 个 session 全部处理完才释放。每个 session 至少包含 memory 查询、可选 context 计数、turn 查询、ID 分配，以及多次 trash copy/delete FFI 写调用；controller 模式下每个调用还会叠加 A-026 的额外探测。行数核对与“不与前台写入交错”的边界本身必要，但无需把不同 session 的整批处理绑定为一个不可中断锁窗口。
- 影响：一次后台 retention pass 可长时间阻塞 PostAction 落盘、主动记忆写入、画像更新等所有共享 SQLite 写路径，尾延迟随批次 session 数与每个 session 的数据量增长。
- 建议：把锁粒度缩小到单 session：候选扫描与每个 session 处理之间允许释放锁；重新获取锁后先集合复核该 session 仍 idle、无 pending turn 且仍有可回收内容，再执行其不可交错写序列。若底层补齐真正事务，可进一步用单 session 事务替代进程级长锁。
- 验证建议：用阻塞 fake FFI 构造 32 个 session，证明前台 AppendTurn 可在 session 边界获得锁；覆盖候选选出后新增 turn/变 active 的漂移，确保复核后跳过而非误回收。

### A-029：向量端口只有单条 Upsert，批量 PostAction、主动写入和全量重建逐行跨 FFI/controller

- 状态：已证实
- 严重度：高
- 类别：批量能力缺失、逐条序列化与 IPC
- 位置：
  - `internal/app/ports/ports_memory.go:12-21`
  - `internal/adapters/outbound/vldb_lancedb/store.go:119-155`
  - `internal/app/usecase/postaction_analysis.go:502-590`
  - `internal/app/usecase/memory_query_write.go:475-518`
  - `internal/app/vector_rebuild.go:231-275`
  - `internal/app/usecase/workspace.go:158-195`
- 事实链：`VectorStore` 仅暴露单记录 `Upsert`；LanceDB 实现每次为一条记录单独编码 metadata JSON，再构造“只有一行”的 JSON rows 数组并调用一次 `VectorUpsertRaw`。PostAction 已批量生成 embedding，却逐 node Upsert；主动写入与项目迁移同样逐条；split 向量重建虽然按 100 条更新 durable store，sidecar 仍在内层逐条 Upsert。底层 LanceDB API本身接受多行 JSON，能力在端口层被压窄。
- 影响：split/controller 模式的大批量写入和重建产生 O(records) 次 JSON 编码、FFI/IPC 与错误边界；配合 A-026，controller 模式每条还多一次 readiness 连接探测。向量重建是最极端场景，批次概念名存实亡。
- 建议：新增显式 `UpsertBatch(ctx, []MemoryRecord)` 端口并让 LanceDB 一次编码多行；PostgreSQL combined 实现仍可做批量维度验证而不重复持久化。PostAction/直接写入按一个业务批次提交；重建复用现有 write batch size。必须定义批量调用的原子性与结果不确定语义，不能用盲目逐条重放掩盖未知结果。
- 验证建议：1/10/100/10,000 行基准，断言底层 `VectorUpsertRaw` 调用数按批次数而非行数增长；覆盖中间失败、commit/transport unknown、重复 ID upsert、rollback/GC 补偿和 controller 恢复。

### A-030：`make.ps1` 在 Windows PowerShell 5.1 下解析失败，自切换 PowerShell 7 的逻辑无法执行

- 状态：已复现
- 严重度：中
- 类别：构建入口失效、无效兼容分支
- 位置：`make.ps1:19-37,39-50`
- 事实链：文件是无 BOM UTF-8 且包含中文/emoji。Windows PowerShell 5.1 按本地代码页解码时，部分多字节字符吞并换行；实测第 44 行注释与下一行 `if` 被读成同一行，最终在第 47 行报 `Unexpected token '}'`。脚本原本在 19-37 行尝试检测 Desktop Edition 并重入 `pwsh`，但解析错误发生在执行之前，因此该兼容分支不可达。使用 PowerShell 7 直接执行同一脚本则标准构建成功。
- 影响：从 Windows PowerShell 5.1 直接运行文档规定的 `./make.ps1 build` 会立即失败；`make.bat` 因直接调用 `pwsh` 不受影响。用户会看到与真实语法无关的误导性解析错误。
- 建议：把 `make.ps1` 保存为 UTF-8 BOM，或保证重入前整个文件只含 ASCII；同时在 CI 中分别用 `powershell.exe -File` 与 `pwsh -File` 验证入口。若项目明确只支持 PowerShell 7，则删除无效的 5.1 自举逻辑并在入口处/文档中明确版本要求。
- 验证建议：`powershell -NoProfile -File ./make.ps1 build` 与 `pwsh -NoProfile -File ./make.ps1 build` 均应成功或前者给出明确版本错误；继续验证 `make.bat build`。

### A-031：两套 FFI `readCString` 把未知长度指针直接扩成固定超大切片，`-race/checkptr` 可稳定触发致命错误

- 状态：已复现
- 严重度：高
- 类别：不安全边界、伪有界验证
- 位置：
  - `internal/platform/ffi/sqliteffi/sqliteffi.go:756-768`
  - `internal/platform/ffi/lancedbffi/lancedbffi.go:586-598`
  - 两个包的 `cstring_test.go:11-40`
- 事实链：实现收到仅含 `*byte` 的未知长度指针后，直接执行 `unsafe.Slice(ptr, maxCStringReadBytes)`，再寻找 NUL。这个“固定上限”没有证明底层分配真的达到该长度；`go test -race ./...` 在两个 `TestReadCStringReturnsTerminatedPrefix` 中均稳定以 `fatal error: checkptr: unsafe.Slice result straddles multiple allocations` 终止。普通测试通过只说明默认运行时未开启该检查，不证明指针算术安全。
- 影响：这不是普通测试噪音：FFI 返回畸形、提前释放或 ABI 漂移时，Go 侧可能读取分配边界之外的内存；当前防畸形校验反而先构造了越界视图。两套实现重复了同一风险。
- 建议：首选升级 FFI ABI 为“指针 + 明确字节长度”，Go 侧只按返回长度复制并另行校验 NUL/最大值；将共享解码逻辑收敛到一处。若短期只能接收 C 字符串，应使用经过审计的 C/Rust 侧长度函数并明确所有权，但不能把任意固定上限当作已分配长度。
- 验证建议：修复后运行两包普通测试、`-race`、畸形无 NUL/超长/空指针/释放顺序测试，并用真实 DLL 完成 SQLite/LanceDB smoke test；不得仅通过在测试里分配 `maxCStringReadBytes` 来掩盖实现问题。

### A-032：仓库没有任何性能基准，最关键的 PostgreSQL、FFI 与 controller SDK 覆盖率偏低

- 状态：已证实
- 严重度：中
- 类别：性能回归保护缺失、验证盲区
- 位置：全仓测试体系
- 事实链：全仓搜索没有任何 `Benchmark*`。强制执行覆盖率测试后，`vldb_postgres` 为 13.5%，`lancedbffi` 为 2.9%，`sqliteffi` 为 2.0%，`vldb_lancedb` 为 33.9%，嵌套 controller SDK 手写包为 24.5%；这些恰好覆盖本次高风险的批量、IPC、连接与 unsafe 边界。普通测试 1,120 项全部通过，但 `-race` 仍在两个 FFI 包发现 A-031，说明仅看普通通过数会漏掉关键风险。
- 影响：A-006、A-019、A-024、A-026、A-029 等优化没有可量化基线，未来很难证明收益或防止回退；低覆盖的后端/FFI 也让模式差异更容易只在真实部署中暴露。
- 建议：建立最小性能基准矩阵：embedding 校验/重绑定、10/64 候选完整搜索与 MMR、JSON 文本日志、controller 100 次健康态调用、LanceDB 1/100/10,000 行 Upsert、PostgreSQL batch 写。CI 至少保存 `benchstat` 可比输出，并为 controller/FFI 增加调用计数与真实 DLL smoke 层。
- 验证建议：基准要报告 `ns/op`、`B/op`、`allocs/op` 和后端调用次数；性能阈值与功能测试分层，避免用不稳定墙钟断言替代结构化计数。

### A-033：文档规定的 `build release` 参数被静默接收但从未影响构建

- 状态：已证实
- 严重度：中
- 类别：死参数、无效果分支、构建契约漂移
- 位置：
  - `README.md:20-31`
  - `make.ps1:9-15,31-33,56-69`
  - `scripts/vmm.ps1:2-8,184-207,254-259`
- 事实链：README 与仓库协作规范把 `make.ps1 build release`、`make.bat build release` 列为标准 release 入口。顶层 `make.ps1` 确实把 `release` 收进 `ForwardArgs`，但 `build` 分支固定执行 `vmm.ps1 build`，没有转发参数；内层 `ProgramArgs` 也只在 `run` 分支使用。`Do-Build` 没有 release 开关或对应编译 flags，所以当前两个命令与普通 `build` 完全同路。
- 影响：调用者得到成功退出却没有得到任何可区分的 release 产物；多余参数和文档造成错误安全感，后续若围绕体积、符号或调试行为做发布判断会失真。
- 建议：先明确是否真的需要双构建型态。需要时将 build profile 变成显式、受 `ValidateSet` 约束的参数，并在 `go build` flags、产物身份和测试中形成可观察差异；不需要时从 README、协作规范和示例完整删除 `release`，同时拒绝未知 build 参数，避免继续静默忽略。
- 验证建议：测试普通/release 的参数解析和产物 `--version-json`；若保留双模式，至少断言 flags 或二进制身份不同；若移除，`build release` 应给出明确迁移错误。

### A-034：Unix 打包脚本遗漏 Windows 正式构建注入的源码身份

- 状态：已证实
- 严重度：中
- 类别：跨平台构建重复实现、构建身份不一致
- 位置：
  - `scripts/vmm.ps1:21-52,184-200`
  - `scripts/vmm.sh:63-83`
  - `internal/buildinfo/buildinfo.go:25-31,59-73`
- 事实链：Windows 脚本先计算 Git revision 与源状态摘要，并通过 `-ldflags -X` 注入 `vmm-local`/`vmm-migrate`；Unix 脚本完成同类二进制、配置、宿主依赖和数据库布局打包，却直接执行三次无 ldflags 的 `go build`。`buildinfo` 明确把两字段定义为正式构建注入值，缺失时固定为 `unknown`。
- 影响：Linux/macOS helper 产出的正式布局二进制在 `--version-json` 中无法提供源码 revision/freshness digest；托管运行时诊断和跨平台产物追踪不一致。两个平台分别维护打包逻辑也使后续 flags、产物列表与身份规则更容易继续漂移。
- 建议：把源码身份计算与 Go build 参数收敛为跨平台等价契约，至少让 shell 版本生成相同的 revision/digest 并注入两个正式程序；更长期可由一个小型跨平台构建工具产出 manifest，PowerShell/Shell 只负责宿主文件操作。
- 验证建议：Windows、Linux、macOS 构建后分别执行 `vmm-local --version-json` 与 `vmm-migrate --version-json`，断言干净/脏工作区身份语义一致；在 CI 对比 manifest 字段而不是比较二进制字节。

## 5. 疑问与验证台账

| 编号 | 疑问 | 当前证据 | 后续验证 | 状态 |
|---|---|---|---|---|
| Q-001 | `managementapi` 是否装配进 OSS 默认运行时 | `newApplicationWithOptions` 在 `management.enabled=true` 时创建并运行管理 HTTP 服务 | 已确认条件装配；后续核对默认配置与文档表述 | 已验证：条件可达 |
| Q-002 | `vldb_postgres` 是否仍存在当前 OSS 可达路径 | `buildStorageDependenciesWithDataRoot` 在 `UsesCombinedPostgres()` 为真时装配同一个 PostgreSQL Store 作为关系库与向量库 | 后续核对配置校验和测试 | 已验证：生产可达 |
| Q-003 | 大型 SQLite 单文件是否存在同类行转换、JSON 解码与校验重复 | 已按 workspace、turn、analysis、memory、profile、management、retention、queue、FTS、schema 区域复核 | JSON query 是当前 FFI 契约；提交未知对账是必要可靠性边界；FTS 逐项调用另列 A-014 | 已验证：不整体误判，发现局部问题 |
| Q-004 | PostAction 清洗器是否对同一字符串执行可合并的多轮 whitespace 规范化与 token 扫描 | 已对照 sanitizer、cleaner、budget 具体返回路径 | 短文本/未截断路径确认重复，列为 A-010；发生折叠/截断时部分规范化仍必要 | 已验证 |
| Q-005 | PostAction 分析结果日志的完整 JSON 重建与 SHA-256 是否属于必要审计契约 | 测试要求 digest；文档只承诺安全摘要，未要求完整载荷哈希 | 性能事实列 A-009；是否改窄摘要需产品确认 | 已验证：实现契约存在，策略待确认 |
| Q-006 | 当前随仓运行配置是否默认关闭 payload debug | `ConfigPaths` 顺序为 base、system config、user；`configs/config.yaml` 显式开启 debug | 已修正 A-002 表述，区分基础默认与随仓开发覆盖层 | 已验证：当前随仓覆盖为开启 |
| Q-007 | Prompt 每次读盘是否为了支持运行期热修改 | `PromptManager` 启动时校验 bundle，但正文按请求读取；README 未声明热加载/热重载 | 性能问题列 A-016；若产品要求热改，应改为显式 watcher/原子 reload | 待产品确认 |
| Q-008 | StructuredOutput 是否在所有声明支持的 LLM provider 上真正下发 | 仅 Vulcan inference 引用该字段；OpenAI/OpenRouter/Google 当前实现未引用 | 性能问题列 A-017；provider 契约需独立确认，未在本次审核中擅自修复 | 已验证实现差异，产品契约待确认 |
| Q-009 | 三个无引用二进制资产是否承担仓外归档/人工演示职责 | 全仓引用为零；RAR 会进入正式 `output/configs`，两张 JPG 不参与构建 | 冗余列 A-025；删除前需要资产所有者确认 | 待产品确认 |

## 6. 已排除项

以下候选经过源码、协议或测试核对后，不作为“无必要验证/冗余”问题：

1. gRPC 请求校验与 usecase 不变量校验处于不同信任边界；维护工具、内部调用和测试可绕过 gRPC，因此不能只因字段被检查两次就整体删除 usecase 防御。
2. SQLite FFI 的精确 `rows changed` 校验、写失败后的状态回查和 `STORAGE_OUTCOME_UNCERTAIN` 收敛用于处理自动提交结果不确定，不属于可随意移除的重复验证。
3. controller 对查询/幂等控制面允许恢复，对 mutation 传输失败禁止盲目重放，是副作用安全边界；A-026 建议取消的是每次健康态操作前的额外探测连接，不是取消错误分类和恢复状态机。
4. Embedding 的输入数、索引唯一性、丢弃状态、维度和有限数值校验属于向量空间契约；A-006 建议移除的是“为了验证而深拷贝整批向量”，不是降低严格校验。
5. `base/system/user` 配置分层、未知字段拒绝、环境占位符检查，以及 Prompt/PII/Noise bundle 的启动期完整性验证用于尽早失败；A-012/A-016 只针对可复用解析结果和正文的重复读取。
6. PostgreSQL 写路径中的事务和 `SET LOCAL` 不应为减少往返而拆除；可优化的是事务内部逐条 `Exec`，应使用同一事务内的 batch/集合 SQL。
7. PII 与 Noise 规则已在加载期编译，不存在“每请求重新编译正则”的全局问题；只对 A-001/A-002 中日志或死载荷导致的重复扫描计入问题。
8. 多 provider、route、node、key 的 failover 分支有模型能力、预算和错误分类依据，不能合并成一个猜测式全局分支；A-008/A-017 针对的是分支前后可共享的纯计算产物。
9. 生成的 protobuf descriptor/getter、协议兼容字段和多后端 adapter 形态本身不计为手写冗余；优化应在端口或调用方建立明确批量能力，不能直接修改生成文件。
10. Retention 的候选选出后复核、写锁/事务隔离和保护性引用检查是防止误回收的必要步骤；A-028 只建议缩短跨多个 session 的锁持有窗口，并在单 session 边界重新复核。

## 7. 验证记录

- 初始 `git status --short` 无输出，工作区在创建本次文档前为干净状态。
- 已读取全仓 AST 结构清单并建立第一版模块地图。
- 已通过结构化引用搜索确认 PostAction 三个 `Raw*` 命令字段与 `rawTurnFromCommand` 没有生产消费点。
- 已核对默认日志配置与 `AppendPayloadFields` 的判断顺序，确认关闭 payload 捕获时仍发生 JSON 编码。
- 已逐字段对比持久化 turn JSON 与 analyzer target JSON，确认二者为同形载荷且持久化行已有 token budget。
- 已核对 SQLite 的 `ResolveRequestScope` 与 `ResolveProfileTarget`，确认内部检索存在两次额外关系查询。
- 已核对 embedding response/failover/caller 三层，确认高维 vector 在校验和重绑定中多次复制。
- 已核对普通搜索与 PostAction hard-dedupe 的唯一调用点，确认普通搜索仍构造并深拷贝 hard-dedupe 专用向量产物。
- 已核对配置加载顺序，确认 `base.yaml` 的关闭值会被当前随仓 `config.yaml` 开启值覆盖。
- 已区分 SQLite FFI 提交未知对账与普通冗余校验：前者属于必要可靠性设计，不建议删除。
- 以 `git -c core.quotepath=false ls-files` 建立 432 个受版本控制文件的最终审计基线；329 个 Go 文件均进入 AST/包级结构地图，热点函数按调用链深读。
- 强制包测试 `rtk go test -count=1 ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config` 通过：525 项测试、6 个包。
- 根模块 `rtk go test -count=1 ./...` 通过：1,120 项测试、39 个包；嵌套 controller SDK `rtk go test -count=1 ./...` 通过：5 项测试、2 个包。
- 根模块与嵌套模块的 `rtk go vet ./...` 均通过。
- 根模块 `rtk go test -race ./...` 未通过：1,116 项通过、2 项失败、2 项跳过；两个失败均为 A-031 的 FFI `readCString` 致命 `checkptr`。排除这两个 FFI 包后的其余 35 个包竞态测试通过；嵌套 controller SDK 的竞态测试也通过。
- 强制覆盖率测试通过；关键低覆盖包为 `vldb_postgres 13.5%`、`lancedbffi 2.9%`、`sqliteffi 2.0%`、`vldb_lancedb 33.9%`，嵌套 controller 手写包为 `24.5%`，已归入 A-032。
- PowerShell 7 对 3 个 `.ps1` 的语法解析通过；Windows PowerShell 5.1 对 `make.ps1` 在执行自举前解析失败，直接 `-File` 返回失败，已归入 A-030。
- `rtk proxy pwsh -NoLogo -NoProfile -File .\make.ps1 build` 标准构建成功，生成 `output/bin/vmm-local.exe`、`vldb-controller.exe`、`vmm-migrate.exe`、`vmm-pii-tester.exe` 与两套 DLL；同时验证 `提示词备份.rar` 被复制进正式配置包。
- 40 个 JSON 全部通过 UTF-8 JSON 解析；1 个 proto 与 1 个 OpenAPI 完成结构/路由核对；4 个 pb.go 生成边界已核对。
- `gofmt -l` 仅列出 3 个既有轻微格式差异文件：`retention_delegation.go`、`prompt_bundle.go`、`noise_cache.go`，差异为对齐、空行或换行格式，不计为本次性能/冗余问题，且未修改。
- 当前环境没有 `bash`，因此两个 shell 脚本未执行 `bash -n`；已完成静态正文审核。当前环境也没有 `staticcheck`，因此静态检查边界为 `go vet`，未声称完成 `staticcheck` 验收。
- 未使用真实外部 AI 凭据、真实 PostgreSQL 或独立 controller 服务做在线验收；涉及这些后端的结论来自可达装配、实现、fake/集成测试与调用次数分析。

## 8. 最终结论与处理建议

### 8.1 总体结论

本次共确认 **34 项问题：高严重度 7 项、中严重度 21 项、低严重度 6 项**。仓库的分层和可靠性思路总体清晰：入站清洗、标准轮次、NoiseGate、关系事实库、向量旁路/组合库、结果不确定对账、统一 reviewer 和 retention 的职责边界基本成立。主要性能债务不是“算法到处都错”，而是四类边界放置问题：

1. 功能开关判断太晚，关闭功能后仍完成日志 payload、PII 预览或专用向量投影；
2. 业务上已经形成批次，但端口退化成逐行调用，跨 PostgreSQL/FFI/controller 反复往返；
3. 本可属于请求级、连接级或进程级的纯计算/状态，被放在 query、记录或每次调用分支内重复执行；
4. 为校验而先复制/转换完整数据，或同一 JSON/Prompt/Schema 在相邻层重复解析和重建。

最严重的安全问题是 A-031；最显著的 controller 性能放大器是 A-026；批量写入的结构性瓶颈是 A-029；实时检索中的主要 CPU/内存热点是 A-006、A-007、A-019；日志关闭态的无效工作是 A-002。普通测试全绿不能覆盖这些风险，竞态检查已经实际证明其中一项会致命失败。

### 8.2 建议处理顺序

#### P0：安全与数量级瓶颈

1. **A-031**：先升级/明确 FFI 字符串 ABI，消除未知分配长度上的 `unsafe.Slice`；这是内存安全修复，不能以压制 `checkptr` 代替。
2. **A-026 + A-029**：把 controller readiness 提升到连接/恢复状态机，并为向量端口增加有明确原子性与结果不确定语义的 batch Upsert。两项联动后才能消除“每记录一次业务 IPC，再额外一次探测连接”的乘法成本。
3. **A-006 + A-007 + A-019**：建立不复制的 embedding 校验、按用途返回 search artifact，并缓存 MMR 向量范数/最大相似度；先补 benchmark，再改数据所有权，避免为了少复制引入可变别名。
4. **A-002**：把 payload 开关判断移到所有日志专用投影之前，提供惰性构造 API；这是低风险、高覆盖面的热路径收益。

其中 A-031 的 ABI 调整和 A-029 的端口扩展属于重要接口决策，实施前应单独确认兼容与失败语义。

#### P1：数据库往返、锁与重复转换

- 集合化 A-004、A-005、A-013、A-014、A-021、A-022：复用已解析 scope、建立请求级关系行 cache、在事务内使用 batch/集合 SQL、为 FTS 增加批量端口。
- 缩短 A-028 的全局写锁到单 session 原子窗口，同时保留重新复核，优先用并发测试证明前台写入尾延迟改善。
- 合并 A-003、A-008、A-010、A-015、A-016、A-017、A-024 的纯计算：复用持久化 turn、单次 token 估算、单次 hint 合并、单次 whitespace pass、discovery 快照、Prompt 缓存/显式 reload、Schema 缓存和单次 JSON 渲染。
- 对 A-009 先确认审计摘要策略；如果 digest 必须覆盖完整分析结果，应缓存第一次序列化结果，而不是在日志分支再次重建。

#### P2：契约瘦身与工具链一致性

- 清理 A-001、A-020、A-027 的死字段/死配置，但先确定旧配置迁移策略；对 A-025 的资产先由所有者确认再移除。
- 修复 A-030、A-033、A-034：明确 PowerShell 最低版本或提供可解析入口；让未知 build 参数失败；统一 Windows/Unix 的构建身份与发布 profile 契约。
- 处理 A-011、A-012、A-018、A-023 等低风险局部债务，可随相关模块改动合并完成，避免单独制造大范围变更。

### 8.3 额外工程建议

1. 建立“端口先批量、单条为便捷封装”的约定：`VectorStore`、FTS、管理状态、画像渲染等天然集合业务不再从单条端口起步。
2. 为一次记忆检索建立显式 request execution state，统一承载 resolved scope、relation row cache、context-edge cache、query embedding 和日志捕获开关，避免同一请求在多个 usecase helper 之间降格再解析。
3. 对静态运行资产采用 immutable snapshot：Prompt 与 Structured Schema 启动加载；若确需热修改，使用显式 watcher、版本号和原子替换，而不是每次请求 `ReadFile/Stat`。
4. 所有可选观测能力遵循“先判断是否启用，再投影/脱敏/序列化”；普通计数日志与 payload 正文日志分离。
5. 新增最小基准套件并记录后端调用次数。优化验收至少同时比较 `ns/op`、`B/op`、`allocs/op`、SQL/FFI/RPC 次数和竞态结果，不能只看功能测试通过。
6. 构建脚本输出统一 manifest，至少包含 profile、Go flags、source revision、source state digest、宿主依赖版本和配置树摘要，使跨平台产物可追踪。

### 8.4 仍需产品确认的事项

- Q-007：Prompt 是否要求运行期无重启热修改；这决定使用启动快照还是 watcher。
- Q-008：Structured Output 是否必须在 OpenAI/OpenRouter/Google 等 provider 上统一生效；当前只有 Vulcan inference 消费该字段。
- Q-009：RAR/JPG 是否有仓外归档或人工演示职责；未确认前不建议直接删除。
- A-020/A-027：旧 `input_mode` 与 `address` 配置的兼容窗口和迁移报错策略。

本次只新增审核与计划记录，**没有修改任何生产代码、配置、规则、协议或测试**。
