# 任务目标

对 VulcanMemoryMesh 仓库进行全量结构分析，识别当前实现中是否存在适合独立拆分的组件或模块边界，并从降低耦合、明确依赖方向、提升可维护性与可测试性的角度给出结构化建议。

# 执行步骤

1. 盘点仓库目录结构、主要分层与核心运行链路，明确当前模块职责分布。
2. 分析 Go 包之间的依赖关系、跨层调用情况与潜在的反向依赖风险。
3. 重点审查配置加载、文本清洗、PII 处理、Noise Gate、Post Action、持久化适配层、gRPC 入站等关键链路，定位职责混杂区域。
4. 汇总高耦合点，判断哪些能力适合独立为组件、子模块或更清晰的边界接口。
5. 输出最终结论，区分：
   - 建议立即拆分的组件
   - 建议保持现状但补边界的区域
   - 暂不建议拆分的区域及原因

# 技术选型与分析方法

- 以仓库现有分层约束为基线：`adapters -> app -> logic/domain`。
- 优先通过目录结构、包依赖、接口定义、配置入口和核心用例编排关系进行静态分析。
- 结合测试分布判断模块是否具备独立演进能力。
- 输出建议时以稳定性、可维护性、扩展性和测试隔离性为优先，不以机械拆分为目标。

# 验收标准

1. 明确说明当前系统的主要组件边界与职责归属。
2. 明确列出存在耦合风险的代码区域，并给出判断依据。
3. 对每个候选拆分项说明：
   - 为什么适合独立
   - 推荐拆分边界是什么
   - 拆分后对现有依赖方向的影响
   - 不拆分的短期风险与拆分的实施成本
4. 给出最终结论：哪些应拆、哪些不应拆、哪些只需轻量重构。

# 当前状态

- 已完成全量代码结构分析，并形成组件拆分建议。

# 执行变更总结

## 1. 核心修复与调整概述

- 本次任务未修改业务源码，重点完成了仓库全量结构审查、内部依赖梳理和高耦合区域识别。
- 结论上，仓库**宏观分层方向基本健康**，未发现明显跨层反向依赖；但存在多个**组件边界过宽**的问题，主要集中在运行时装配、统一记忆用例、PostgreSQL 组合库、gRPC 入站适配器和配置中心。
- 建议优先进行**仓库内子组件/子包拆分**，而**不建议拆成独立进程或微服务**，以免破坏当前本地版运行时与标准打包路径。

## 2. 📂 文件变更清单

### 新增

- `docs/plan/20260410-01-COUPLING_COMPONENT_ANALYSIS.md`

### 修改

- 当前计划文件本身，补充了本次分析结论与执行总结。

### 删除

- 无。

## 3. 💻关键代码调整详情

### 本次未改动业务代码

- 本任务属于结构分析与拆分建议输出，没有对 `internal/`、`cmd/`、`configs/` 下业务代码实施变更。

### 识别出的主要耦合热点

- `internal/app/app.go`
  - 运行时组合根承担了依赖装配、适配器选择、模型路由、PII/Noise 初始化、Schema 同步、gRPC Server 装配与 shutdown 编排等多种职责。
  - 还存在对单一 `relational` 实例的大量能力断言，说明存储边界对上层暴露过宽。
- `internal/app/usecase/memory_query.go`
  - 同时承担记忆检索、详情查询、主动写入、去重评审、RRF/MMR、重排序、衰减和上下文证据打分。
  - 这是当前最明显的“单文件多子域混合”区域，建议优先拆出独立记忆搜索/记忆写入组件。
- `internal/app/usecase/postaction.go` + `postaction_queue.go` + `postaction_profiles.go`
  - 一条 PostAction 链路同时承担 turn 落库、异步队列、单轮分析、向量写入、画像收敛与维护扫描。
  - 适合拆成 intake、queue worker、analysis apply、profile maintenance 几个内部组件。
- `internal/adapters/outbound/vldb_postgres/*`
  - 当前 `Store` 同时承载 workspace、memory、profile、retention、scratchpad、schema、maintenance 等多种仓储职责。
  - 文件虽已按主题拆开，但仍由同一个总 store 对上提供大量接口能力，建议后续演进为 repository bundle，而不是继续扩张单一 `Store`。
- `internal/adapters/inbound/grpcapi/server.go` + `validation.go`
  - 入站适配器聚合了过多 RPC surface，transport 层已经成为职责汇聚点。
  - 适合拆成 workspace/profile/memory/scratchpad/session-flow 等 handler 组件，共享拦截器和错误转换。
- `internal/config/config.go`
  - 一个文件同时承载配置模型、层叠加载、YAML/JSON 解码、环境变量展开、归一化和校验。
  - 不一定需要独立成业务组件，但应至少拆成多个内部子模块，降低配置中心对全局演进的阻力。

### 识别出的相对健康区域

- `internal/app/usecase/scratchpad.go`
  - 领域边界清晰，端口最小化明确，已经具备独立组件特征。
- `internal/app/usecase/workspace.go`
  - 体量适中，职责单一，主要是管理编排，不建议优先拆分。
- `internal/logic/processor/noise_gate.go`
  - 作为独立规则门控组件的边界较清晰；若后续继续演进，建议只拆“规则加载/编译”和“运行时判定”两个内部层次，而不是拆成单独服务。
- `internal/adapters/outbound/ai_key_failover/*`
  - 已具备较好的独立能力，建议保持现状。

## 4. ⚠️遗留问题与注意事项

- 本次仅完成静态结构分析，**尚未进入实际重构阶段**，因此当前结论是“拆分建议”而非“重构落地结果”。
- 后续若开始实施拆分，建议优先顺序为：
  1. `internal/app` 运行时装配拆分
  2. `memory_query` 拆分为搜索/写入/详情与排序策略组件
  3. 存储层从“单一大 Store”演进为按边界分仓储组件
  4. gRPC handler 拆分
  5. config 与 ports 的内部子模块拆分
- 拆分过程中必须继续保持仓库既有依赖方向：`adapters -> app -> logic/domain`，不要把 adapter 细节重新渗回 `usecase` 或 `logic`。
- 不建议把上述能力直接拆成独立运行时进程；当前更合适的是**仓库内包级/组件级解耦**，否则会与本地版打包、配置解析和维护链路产生新的系统复杂度。
