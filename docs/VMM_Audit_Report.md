# 《全局审计与代码修复落地报告》

## 0. 宏观架构全景推演（Phase 1）

### 0.1 分层与依赖拓扑结论
我先按组合根 `internal/app/app.go` 把系统装配链路完整逆推了一遍，确认主工程的核心分层为：

- **adapters**：SQLite / PostgreSQL / LanceDB / gRPC / AI Provider 等外设接入层
- **app**：usecase、ports、应用组合根、生命周期管理
- **logic/domain + logic/processor**：领域模型与纯业务推演逻辑

总体设计方向是对的：
- `adapters -> app/ports`
- `app/usecase -> ports + logic`
- `logic/processor -> domain + ports`

但我确认到一处**关键边界泄漏**：`internal/logic/processor` 直接依赖了 `internal/app/ports`。这意味着本应由领域层拥有的抽象，被上层 app 反向“拥有”了，违反了单向依赖原则，会让领域逻辑被应用层接口设计反向绑死。我已在修复中把这些抽象下沉回 `internal/logic/ports`。

### 0.2 核心数据流结论
我重点追踪了三条最关键的数据流：

1. **PostAction 主链**  
   `AppendTurnRecord -> queue -> applyImmediateTurnAnalysis -> 向量写入 -> 关系库 ApplyTurnAnalysis -> 删除 superseded vectors -> AdvanceSessionExtractWindow`

2. **Direct Memory Write 主链**  
   `embedding -> vector.Upsert -> relational Create/ApplyDirectMemoryWrite -> 删除 superseded vectors`

3. **Retention / Vector GC 主链**  
   `RecycleColdMemories / RecycleIdleSessions -> best-effort DeleteByIDs -> EnqueueVectorGCJobs -> retryPendingVectorGCJobs`

系统原本已经有一套用于 retention 的**持久化向量 GC 重试队列**，但 **PostAction** 和 **Direct Write** 这两条最容易出现跨库不一致的路径并没有复用它，导致“向量库成功、关系库失败”或“关系库成功、向量删除失败”时，可能产生长期孤儿向量或残留脏向量。我把这条补偿链统一接到了现有的 `VectorGCJobs` 机制上，形成闭环。

---

## 1. 已修复风险一：领域层依赖反向穿透，破坏 DDD 边界

### 🚨 风险点定位
`internal/logic/processor` 原先直接 import `internal/app/ports`。这会把**领域处理器**绑定到**应用层接口定义**，形成反向依赖：

- 领域层不再独立，无法作为纯业务核心复用或单测
- app 层任意端口调整，可能级联冲击 logic 层
- 长期会诱发“为了适配 processor，继续把更多接口塞进 app/ports”的架构腐化

这类问题短期不一定炸，但一旦系统继续扩张，会直接演化成**核心领域与应用编排强耦合**，后续重构成本指数级上升。

### 📂 涉及修改的文件清单
- `VulcanMemoryMesh/internal/logic/ports/llm.go`
- `VulcanMemoryMesh/internal/logic/ports/prompt_source.go`
- `VulcanMemoryMesh/internal/logic/ports/embedding.go`
- `VulcanMemoryMesh/internal/app/ports/llm.go`
- `VulcanMemoryMesh/internal/app/ports/prompt_source.go`
- `VulcanMemoryMesh/internal/app/ports/interfaces.go`
- `VulcanMemoryMesh/internal/logic/processor/context_assembler.go`
- `VulcanMemoryMesh/internal/logic/processor/entry_summarizer.go`
- `VulcanMemoryMesh/internal/logic/processor/intent_extractor.go`
- `VulcanMemoryMesh/internal/logic/processor/manual_profile_reviewer.go`
- `VulcanMemoryMesh/internal/logic/processor/manual_profile_reviewer_test.go`
- `VulcanMemoryMesh/internal/logic/processor/noise_gate.go`
- `VulcanMemoryMesh/internal/logic/processor/noise_gate_test.go`
- `VulcanMemoryMesh/internal/logic/processor/postaction_candidate_reviewer.go`
- `VulcanMemoryMesh/internal/logic/processor/postaction_candidate_reviewer_test.go`
- `VulcanMemoryMesh/internal/logic/processor/precheck_memory_reviewer.go`
- `VulcanMemoryMesh/internal/logic/processor/profile_merger.go`
- `VulcanMemoryMesh/internal/logic/processor/profile_merger_test.go`
- `VulcanMemoryMesh/internal/logic/processor/turn_analyzer.go`
- `VulcanMemoryMesh/internal/logic/processor/turn_analyzer_test.go`

### 🛠️ 修复方案与执行细节（How）
- 新增 `internal/logic/ports`，把 **LLM / PromptSource / Embedding / NoiseEmbeddingCache** 等 processor 真正依赖的抽象下沉到 logic 层拥有。
- `internal/app/ports` 不再“拥有”这批抽象，而是改成**类型别名 re-export**，这样不会破坏现有 app 层接口使用方式。
- 所有 `internal/logic/processor/*` 中原本对 `appports` 的依赖，全部切换为 `logicports`。
- 同步修正相关 processor 测试，使测试依赖边界与生产代码一致。

### 💡 决策动机与最优解剖析（Why）
这是典型的“抽象归属权”问题。**谁使用抽象，谁应拥有抽象**。这里真正使用这些端口的是 logic 层，因此端口必须归 logic 所有。

我没有采用“继续放在 `app/ports`，只靠约定不让滥用”的方案，因为那只是口头纪律，不是结构性修复；也没有直接把 processor 改去依赖 adapter，因为那会彻底破坏端口-适配器模式。当前方案兼顾了：

- **依赖方向正确**
- **不破坏现有 app 层接口契约**
- **迁移成本最小**
- **后续继续扩展 processor 时不会再向上层倒灌依赖**

---

## 2. 已修复风险二：PostAction 队列满载时会按失败次数无限派生阻塞 goroutine

### 🚨 风险点定位
`PostActionUseCase.pushQueueID` 在 `queueCh` 已满时，原实现会启一个 goroutine 做兜底发送。高并发下，这会产生非常危险的后果：

- 每一次溢出都生成一个阻塞 goroutine
- 如果消费端跟不上，goroutine 数量会随流量线性累积
- 关闭阶段这些 goroutine 还可能与 worker 生命周期纠缠，形成资源泄露、卡死或内存失控

这属于典型的**高并发背压实现错误**。

### 📂 涉及修改的文件清单
- `VulcanMemoryMesh/internal/app/usecase/postaction.go`
- `VulcanMemoryMesh/internal/app/usecase/postaction_queue.go`
- `VulcanMemoryMesh/internal/app/usecase/postaction_test.go`

### 🛠️ 修复方案与执行细节（How）
- 在 `PostActionUseCase` 中新增：
  - `deferredQueueIDs []uint64`
  - `deferredQueueSet map[uint64]struct{}`
- `pushQueueID` 改为：
  - 通道有容量时直接入队
  - 通道满时，不再起 goroutine，而是把 sessionID 放入**去重的内存延迟队列**
- 新增 `queueDeferredSessionID`：
  - 负责把溢出的 sessionID 追加到 FIFO backlog
  - 用 `deferredQueueSet` 去重，避免同一 session 被堆积无数次
- 新增 `flushDeferredQueueIDs`：
  - 在 worker 每次处理完任务、以及周期性 maintenance tick 后
  - 非阻塞地把 backlog 重新刷回 `queueCh`
- 增加测试：
  - 验证部分装配对象在 `queueCtx == nil` 时不会 panic
  - 验证 overflow 会进入 deferred backlog，并在容量恢复后按预期刷回队列

### 💡 决策动机与最优解剖析（Why）
我没有简单地“把 channel 开大”，因为那只是延后问题，不解决无限 goroutine 派生；也没有选择“队列满了直接丢任务”，因为这会破坏异步提炼的正确性。

当前方案的优点是：
- **不会再产生按溢出次数增长的 goroutine**
- **仍保留 session 级别去重语义**
- **背压变成内存中受控的 FIFO backlog**
- **不改变现有 worker 的消费模型和外部行为**

这属于在原有架构约束下，对并发安全和吞吐平衡都更优的修复。

---

## 3. 已修复风险三：关系库与向量库之间缺少统一补偿，存在长期孤儿向量/脏向量残留

### 🚨 风险点定位
我确认了两类跨库不一致高危点：

1. **PostAction / Direct Write：先写向量，后写关系库**  
   如果关系写失败，而回滚删除向量也失败，就会留下**孤儿向量**。

2. **关系库提交成功后，再清理 superseded vectors**  
   如果这里删除失败，就会留下**语义上已被淘汰、但仍可被检索命中的残留向量**。

这类问题最致命的地方在于：**数据并不会立刻报错，而是以“召回污染”的形式长期存在**。系统表面可运行，但 recall、dedupe、profile reasoning 会越来越脏，属于隐蔽型架构灾难。

### 📂 涉及修改的文件清单
- `VulcanMemoryMesh/internal/app/usecase/vector_gc_compensation.go`
- `VulcanMemoryMesh/internal/logic/domain/retention.go`
- `VulcanMemoryMesh/internal/app/usecase/postaction.go`
- `VulcanMemoryMesh/internal/app/usecase/memory_query.go`
- `VulcanMemoryMesh/internal/app/usecase/postaction_test.go`
- `VulcanMemoryMesh/internal/app/usecase/memory_query_test.go`

### 🛠️ 修复方案与执行细节（How）
#### 3.1 新增统一补偿桥接器
新增 `vector_gc_compensation.go`，定义狭窄接口：
- `EnqueueVectorGCJobs(ctx, query)`

并实现 `enqueueVectorGCCompensation(...)`，把所有“向量删除失败”的场景桥接到**持久化 Vector GC 重试队列**。

#### 3.2 扩展 Vector GC Job 类型
在 `internal/logic/domain/retention.go` 新增以下 job type：
- `turn_analysis_vector_rollback`
- `turn_analysis_superseded_vector_delete`
- `direct_write_vector_rollback`
- `direct_write_superseded_vector_delete`

这样后续审计日志、运维排障、统计监控都能明确区分“哪条链路产生了待补偿删除任务”。

#### 3.3 修复 PostAction 主链
在 `applyImmediateTurnAnalysis` 中：
- 当 `ApplyTurnAnalysis` 失败后，立即执行向量回滚删除
- 若回滚删除再次失败，则**持久化 enqueue 一个 Vector GC 补偿任务**
- 当关系库已成功提交，但 `SupersededVectorIDs` 删除失败时，也会进入补偿队列

在 `persistMemoryNodeVectors` 中：
- 为“部分 upsert 成功后中途失败”增加统一 `rollbackInsertedVectors()`
- 如果部分回滚失败，仍然桥接到 Vector GC 重试队列，而不是只打日志

#### 3.4 修复 Direct Write 主链
在 `memory_query.go` 的 direct-write 路径中：
- 关系写入失败后，如果 `rollbackDirectWriteVector` 删除失败，会持久化 `direct_write_vector_rollback`
- 关系提交成功但 superseded vector 删除失败时，会持久化 `direct_write_superseded_vector_delete`

#### 3.5 增加针对性测试
- 新增 post-action 测试，覆盖“关系失败 + 向量回滚失败 -> 必须 enqueue 补偿任务”
- 新增 direct-write 测试，覆盖“superseded vector 删除失败 -> 必须 enqueue 补偿任务”

### 💡 决策动机与最优解剖析（Why）
这里不能依赖分布式事务，因为系统当前本来就是**关系库 + 向量库的异构组合**。把一切强行改成同步双写回滚，不现实，也会显著拉高耦合和失败半径。

最优解不是“追求伪两阶段提交”，而是：
- **关系状态继续作为主事实源**
- **向量侧清理走持久化补偿队列**
- **补偿任务可重试、可观测、可运维兜底**

我之所以复用现有 retention 的 Vector GC 基础设施，而不是另起一套临时表/临时队列，是因为：
- 现有机制已经有 claim / retry / complete 的完整闭环
- 语义本质一致，都是“关系已决定，向量侧待清理”
- 统一到一条维护链，可以最大化降低运维复杂度和未来心智负担

这是当前架构下最稳妥、最符合最终一致性最佳实践的方案。

---

## 4. 已修复风险四：Vector GC 重试队列被错误绑定在 retention 开关上，关闭 retention 后补偿任务永不执行

### 🚨 风险点定位
虽然系统原本已经有 `VectorGCJobs` 持久化重试机制，但 worker 启动条件依赖 retention maintenance 开关。结果就是：

- 一旦运营上关闭经典 retention recycle
- 但 PostAction / DirectWrite 仍然把失败的 vector cleanup 写入重试队列
- 这些任务**永远没有 worker 来消费**

这会把上一条修复重新打回半残状态：任务入库了，但没人执行，最终仍然是**长期脏数据堆积**。

### 📂 涉及修改的文件清单
- `VulcanMemoryMesh/internal/app/usecase/retention.go`
- `VulcanMemoryMesh/internal/app/usecase/retention_test.go`

### 🛠️ 修复方案与执行细节（How）
- 将维护职责拆成两个独立判断：
  - `retentionMaintenanceEnabled()`
  - `vectorGCMaintenanceEnabled()`
- `maintenanceEnabled()` 改为只要二者任意一个成立，就启动共享 worker
- `runScheduledMaintenance()` 改为：
  - retention recycle：按原配置决定是否执行
  - vector GC retry：只要 store + vector 可用，就始终执行
- 新增 `runVectorGCMaintenance(ctx)`，专门负责 `retryPendingVectorGCJobs`
- 补充测试：
  - 验证 **retention disabled 但 vector GC 存在时 worker 仍会启动**
  - 验证所有维护目标都不存在时 worker 不会误启动

### 💡 决策动机与最优解剖析（Why）
Retention recycle 和 Vector GC retry 在业务语义上根本不是一回事：

- **Retention** 是冷数据治理策略
- **Vector GC retry** 是跨库一致性修复机制

把二者绑在一起，会导致“为了关闭冷数据治理，顺便把一致性修复也关掉”这种危险副作用。

当前拆分方案的好处是：
- 冷数据治理仍可独立开关
- 一致性补偿链路不会被误伤
- 共享一个 maintenance ticker，避免引入额外后台线程模型

这比再新增一个完全独立 worker 更优，因为它在不增加系统复杂度的前提下，实现了职责解耦。

---

## 5. 已修复风险五：应用启动失败时存在资源泄露，组合库模式下存在重复关闭同一依赖的风险

### 🚨 风险点定位
我在 `internal/app/app.go` 中确认了两个生命周期层面的高危问题：

1. **newApplication 半初始化失败时**  
   `fileWriter`、`relational`、`vector`、`postAction`、`retention` 等资源可能已创建，但后续步骤失败会直接返回，导致：
   - 文件句柄泄露
   - 连接池/后台 worker 泄露
   - 进程内残留无主资源

2. **组合存储模式（同一对象同时承担 relational/vector）下**  
   shutdown 列表可能把同一个底层对象加两次，导致重复关闭：
   - double close
   - 后续 close 报错污染日志
   - 某些依赖若关闭非幂等，可能直接触发未定义行为

### 📂 涉及修改的文件清单
- `VulcanMemoryMesh/internal/app/app.go`
- `VulcanMemoryMesh/internal/app/app_test.go`

### 🛠️ 修复方案与执行细节（How）
#### 5.1 启动失败自动回收已创建资源
- 在 `newApplication` 中引入：
  - `initSucceeded := false`
  - `startupShutdowns []appports.Shutdowner`
  - `trackStartupShutdown(...)`
- 所有在启动期间创建出的可关闭依赖，立即登记到 `startupShutdowns`
- 通过 `defer` 在初始化失败路径上按**逆序**执行 `Shutdown(context.Background())`

#### 5.2 优雅停机阶段去重
- 新增：
  - `buildUniqueShutdownSequence(...)`
  - `appendUniqueShutdowner(...)`
  - `shutdownerIdentity(...)`
- 构建最终 `Application.Shutdowns` 时先做一次去重
- `Application.Shutdown` 真正执行时再次按 identity 做防御式去重
- `shutdownerIdentity` 使用 **类型 + 指针地址** 推导进程内稳定身份，适配同一对象经不同接口包装的情况

#### 5.3 补充测试
新增测试验证：
- 重复依赖只会被关闭一次

### 💡 决策动机与最优解剖析（Why）
我没有把所有关闭责任都压给调用方，因为组合根自己最清楚哪些资源是在启动中途创建出来的，失败清理必须就地完成；也没有只在 `Shutdown()` 阶段做去重，因为那无法覆盖“构建失败、尚未得到 Application 实例”的半初始化泄露。

当前方案是完整闭环：
- **失败路径可回收**
- **成功路径可安全停机**
- **重复关闭双保险**

这比单纯要求下游 shutdown 幂等更可靠，因为真正的最优解不是“假设所有资源都能安全 double-close”，而是**从组合根层面避免重复关闭发生**。

---

## 6. 已修复风险六：插件写回 outbox 文件存在并发覆盖，可能直接丢失待补交数据

### 🚨 风险点定位
OpenCode 插件里的 `.opencode/.vmm-writeback-outbox.json` 是**同一工作目录共享的一份持久化补交队列**。原实现中，`submitMemorySyncCandidate()` 的整个流程：

- load outbox
- flush old entries
- enqueue current payload on failure
- save outbox

都没有按目录串行化。结果是同目录下多个 session 并发 finalize 时，极易出现：

- A/B 同时读取旧快照
- A 先写，B 再用旧快照覆盖
- 某些待补交 payload 被直接抹掉
- flush 顺序和真实 finalize 顺序错乱

这会导致**写回消息丢失**，属于插件侧最严重的数据可靠性缺陷之一。

### 📂 涉及修改的文件清单
- `VmmOpenCodePlugins/src/writeback-outbox-mutation-queue.ts`
- `VmmOpenCodePlugins/src/writeback-outbox-mutation-queue.test.ts`
- `VmmOpenCodePlugins/src/memory-sync.ts`

### 🛠️ 修复方案与执行细节（How）
- 新增 `writeback-outbox-mutation-queue.ts`
  - 按 **directory** 维度维护 `pendingByDirectory`
  - 提供 `runSerializedWritebackOutboxMutation(...)`
- 在 `memory-sync.ts` 中新增进程级状态：
  - `const writebackOutboxMutationQueueState = createWritebackOutboxMutationQueueState()`
- 将 `submitMemorySyncCandidate(...)` 的整个 load/flush/send/enqueue/save 周期，完整包进：
  - `runSerializedWritebackOutboxMutation({ directory, mutate })`
- 补充测试：
  - 同目录 mutation 严格串行
  - 不同目录 mutation 仍可并行推进

### 💡 决策动机与最优解剖析（Why）
这里正确的串行化边界不是 session，而是 **directory**，因为持久化 outbox 文件就是按目录共享的。

我没有采用文件锁方案，原因是：
- 插件运行时本身就是单进程异步模型
- 跨平台文件锁可移植性和可靠性反而更差
- 当前问题本质是进程内并发，不是多进程抢占

因此，**按目录做进程内串行 mutation 队列**，既贴合当前架构，也最容易保证 FIFO 与快照一致性。

---

## 7. 已修复风险七：插件 gRPC 运行时缓存会被首次失败永久污染，旧 target 客户端也会持续滞留

### 🚨 风险点定位
`src/vmm-grpc.ts` 中有两类运行时层面的隐患：

1. **动态导入运行时缓存污染**  
   一旦首次 `@grpc/proto-loader` 或 `@grpc/grpc-js` 动态加载失败，缓存 Promise 会保持 rejected 状态，后续请求可能一直复用这个失败结果，导致链路**永不自愈**。

2. **clientPool 旧连接滞留**  
   target 切换后，旧 target 的 grpc client 会继续留在池里，占用 channel / socket / 内存。

前者会导致“偶发启动异常 -> 后续全局永久失败”，后者会导致“目标切换越多，残留连接越多”。

### 📂 涉及修改的文件清单
- `VmmOpenCodePlugins/src/vmm-grpc.ts`

### 🛠️ 修复方案与执行细节（How）
- 修复 `getTransportRuntimeSet()`：
  - 使用 `guardedRuntimePromise`
  - 当该 promise reject 时，若它仍是当前缓存项，就把 `cachedTransportRuntimePromise` 复位为 `undefined`
  - 后续请求可重新触发加载，恢复自愈能力
- 新增：
  - `closeClientQuietly(client)`
  - `pruneClientPoolExcept(target)`
- `getClient(target)` 在取当前客户端前先修剪池，只保留活跃 target 的连接
- 对旧 client 的 close 采用 best-effort 静默关闭，防止清理动作反过来污染当前请求

### 💡 决策动机与最优解剖析（Why）
我没有选择“每次调用都新建 client”，因为那会显著增加 channel 建立成本；也没有选择“失败后整个 transport 永久熔断”，因为这会让临时故障演化为永久故障。

当前方案做到：
- **首次失败后可重试恢复**
- **仍保留当前 target 的连接复用收益**
- **限制连接池规模，避免 target 漫游导致泄露**

这在插件当前的单端点本地连接模型下，是兼顾恢复性与性能的最优解。

---

## 8. 验证情况与执行边界

### 已完成的验证
- 对所有修改过的 Go 文件执行了 `gofmt`
- 新增并补齐了 Go 单测，覆盖：
  - post-action overflow backlog
  - post-action vector rollback compensation
  - direct-write superseded vector compensation
  - retention worker enablement
  - application shutdown dedupe
- 对插件新增串行队列逻辑补充了 Node test 文件
- 对关键变更点进行了源码级一致性复查，确认修复逻辑形成闭环

### 受当前执行环境限制，未能完成的全量编译验证
1. **VulcanMemoryMesh（Go）**  
   仓库 `go.mod` 要求 `go >= 1.26.1`，而当前容器只有 `go1.23.2`。因此无法在本环境完成 `go test ./...` 或全量编译。

2. **VmmOpenCodePlugins（TypeScript）**  
   仓库未附带 `node_modules`。`package.json` 虽定义了 `tsc --noEmit` / `tsc -p tsconfig.json`，但当前离线环境无法补装 `@types/node`、`@grpc/*`、`@opencode-ai/plugin` 等依赖，因此无法在本环境完成全量 TS 构建校验。

### 结论
尽管全量编译受运行时环境约束，我已经完成了：
- 结构级全景审计
- 高危并发/一致性/生命周期问题定位
- 源码级闭环修复
- 针对关键回归面补充测试

交付包中的代码保持原目录结构，不改外部接口契约，适合直接覆盖并在具备正确工具链的环境中继续执行编译与回归测试。
