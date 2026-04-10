# 任务目标

基于上一轮全量耦合分析结果，输出一份面向 VulcanMemoryMesh 仓库的具体组件解耦实施计划，明确拆分优先级、阶段目标、执行步骤、风险控制与验收标准，为后续实际重构提供可直接落地的路线图。

# 背景与问题定义

当前仓库整体分层方向基本健康，但存在若干职责边界过宽的汇聚点：

1. `internal/app/app.go` 组合根承担过多运行时装配职责。
2. `internal/app/usecase/memory_query.go` 将记忆搜索、详情、主动写入、排序策略和去重评审揉在同一用例中。
3. `internal/app/usecase/postaction*.go` 同时承担 intake、异步队列、分析、向量落库与画像维护。
4. `internal/adapters/outbound/vldb_postgres/*` 虽按文件拆分，但仍以单一 `Store` 对外承载过多仓储能力。
5. `internal/adapters/inbound/grpcapi/server.go` 汇聚全部 RPC surface，传输层边界过宽。
6. `internal/config/config.go` 集中了模型定义、加载、归一化、校验和环境变量展开。

本次计划不建议拆成独立服务或独立进程，重点是进行**仓库内组件级/包级解耦**，保持本地版运行方式、打包规范和依赖方向不变。

# 重构总原则

1. 严格保持依赖方向：`adapters -> app -> logic/domain`。
2. 先拆“编排边界”，再拆“数据边界”，最后拆“传输与配置边界”。
3. 每次重构必须优先保证行为等价，避免在同一阶段同时做大规模逻辑改写。
4. 先抽取内部组件/子包，再决定是否需要新增更细的 port；不要一开始就引入过量接口。
5. 每个阶段都必须带测试基线与回归验证，防止结构优化引入行为漂移。

# 技术选型与执行策略

- 采用“阶段式渐进重构”，不做一次性大拆。
- 采用“先抽内部组件，再收口入口文件”的方式，优先减少单文件职责密度。
- 优先通过新增包内协作者、子目录、builder/repository/handler 拆分降低耦合，不引入新运行时进程。
- 对存储层优先做“按边界拆仓储实现”，而不是立即拆分数据库或引入新的持久化技术。
- 对传输层优先做“同协议下 handler 分治”，而不是改 gRPC 契约。

# 总体实施阶段

## 阶段 0：重构基线建立

### 目标

为后续所有结构重构建立稳定基线，确保每一阶段都可验证行为未回归。

### 执行步骤

1. 记录当前关键链路及主文件职责映射。
2. 补齐重构范围内关键回归测试清单。
3. 固化以下最小验证命令：
   - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
4. 若进入较大阶段收尾，补跑：
   - `go test ./...`

### 交付物

- 一份阶段级测试基线清单。
- 一份重构目标文件/包与对应责任映射。

### 验收标准

- 能明确说清每个高耦合点当前负责什么。
- 有固定的最小回归测试入口。

## 阶段 1：运行时装配层拆分

### 目标

将 `internal/app/app.go` 从“大型组合根”重构为清晰的运行时装配协调器，拆出独立装配单元。

### 推荐拆分边界

1. `runtime/bootstrap`
   - 管理应用初始化、shutdown 顺序、gRPC server 创建。
2. `runtime/storage`
   - 管理 relational/vector/combined store 的构建与 capability 解析。
3. `runtime/ai`
   - 管理 LLM、embedding、rerank 的构建与路由适配。
4. `runtime/pipeline`
   - 管理 NoiseGate、PII、usecase 组装。
5. `runtime/schema`
   - 管理 vector schema sync 与维护工具相关 schema 逻辑。

### 执行步骤

1. 先抽取 `buildLLM`、`buildEmbedding`、`buildReranker` 为独立 builder。
2. 抽取 `buildStorageDependencies` 与 capability 断言逻辑。
3. 抽取 `buildNoiseGate`、`buildPIIScrubber`、usecase 装配逻辑。
4. 抽取 gRPC server 构建与 shutdown sequence 管理。
5. 将 `app.go` 收敛成只负责总装配编排的薄入口。

### 风险点

- capability 断言迁移后可能出现装配顺序变化。
- shutdown 顺序若变化，可能引发资源释放时序问题。

### 验收标准

- `app.go` 明显降薄，只保留高层编排。
- AI、存储、pipeline、transport 的构建逻辑均有明确归属。
- 应用启动与关闭行为不变。

## 阶段 2：统一记忆用例拆分

### 目标

把 `memory_query.go` 从“多子域大用例”拆为可维护的记忆组件簇。

### 推荐拆分边界

1. `memory/search`
   - query normalize、embedding、vector/lexical recall、hit materialize。
2. `memory/ranking`
   - RRF、MMR、rerank、Weibull decay、上下文证据打分。
3. `memory/detail`
   - turn detail、memory detail 的加载与格式整理。
4. `memory/write`
   - direct write、soft dedupe、candidate review、persist。
5. `memory/shared`
   - 通用常量、命令模型、校验与工具函数。

### 执行步骤

1. 先把 `Search` 相关流程从 `Write` 相关流程中切开。
2. 再把 ranking/policy 相关纯函数迁移到独立策略层。
3. 将 detail 查询独立成读取组件。
4. 最后把 `MemoryUseCase` 收敛为 façade，只负责路由到 search/detail/write 子组件。

### 风险点

- 排序策略函数分离后，原有共享状态引用可能丢失。
- direct write 与 candidate review 仍依赖 post-action reviewer 端口，边界需谨慎处理。

### 验收标准

- 搜索、详情、写入三类职责不再混在同一文件。
- 排序策略成为可单测的独立模块。
- `MemoryUseCase` 成为薄编排层。

## 阶段 3：PostAction 流水线拆分

### 目标

把 PostAction 从“入口 + 队列 + 分析 + 画像维护”的混合实现，拆成明确的流水线组件。

### 推荐拆分边界

1. `postaction/intake`
   - 校验、PII 清洗、noise gate、turn append。
2. `postaction/queue`
   - 队列状态、worker 生命周期、idle 扫描、maintenance backoff。
3. `postaction/analysis`
   - turn analysis input build、analysis validate、analysis apply。
4. `postaction/vector`
   - memory node vector persist、向量补写与失败回滚。
5. `postaction/profile`
   - expired profile converge、render/replace 维护逻辑。

### 执行步骤

1. 把 `Execute` 与 queue worker 生命周期分离。
2. 把 `buildTurnAnalysisInput` 和 `applyImmediateTurnAnalysis` 提取为 analysis service。
3. 把 `persistMemoryNodeVectors` 提取为 vector apply service。
4. 把 profile convergence 独立为 maintenance service。
5. 保留 `PostActionUseCase` 作为 façade 与 worker owner。

### 风险点

- 队列状态与 store 生命周期绑定紧密，拆分时最容易引入竞态问题。
- 一旦分析与持久化边界断错，可能导致“已落 turn 未落 memory/profile”的状态难以恢复。

### 验收标准

- intake、queue、analysis、profile maintenance 有独立协作者。
- 队列生命周期与业务写入流程分离清晰。
- 异步分析与失败日志行为保持一致。

## 阶段 4：PostgreSQL 仓储边界收口

### 目标

将 `vldb_postgres` 从“单一大 Store”演进为按职责聚合的 repository bundle。

### 推荐拆分边界

1. `workspace repository`
2. `memory repository`
3. `turn/analysis repository`
4. `profile repository`
5. `retention repository`
6. `scratchpad repository`
7. `schema/maintenance repository`

### 执行步骤

1. 保留共享连接池与公共 SQL helper。
2. 为每类仓储引入内部实现结构体，统一挂接同一 pool。
3. 让 `Store` 从“业务全集实现体”收敛成“聚合出口”或“构造入口”。
4. 按 app port 逐步切换到更细的 repository 提供能力。
5. 最终视需要再决定是否保留兼容型大 `Store` 外观。

### 风险点

- 一次性改 port 容易波及大范围编译错误。
- 若同时改 SQL 与结构，回归定位成本会大幅上升。

### 验收标准

- `Store` 不再直接承载全部高层业务语义实现。
- 各仓储按边界独立演进，但仍共享统一 pool 与 helper。
- app 层 capability 断言数量下降。

## 阶段 5：gRPC 入站适配层拆分

### 目标

把单一 `server.go` 拆为多 handler 组件，降低 transport 汇聚耦合。

### 推荐拆分边界

1. `workspace_handler`
2. `profile_handler`
3. `memory_handler`
4. `scratchpad_handler`
5. `session_flow_handler`
   - PreCheck / PostAction / ChatCompact
6. `transport_shared`
   - request normalize、error map、response map、logging helper

### 执行步骤

1. 保留统一 `Server` 作为 gRPC 注册入口。
2. 将 RPC 方法按业务面拆到子 handler。
3. 将 normalize/validate/mapper/log helper 提取到 shared。
4. 保持 interceptor、proto、对外方法签名不变。

### 风险点

- 如果 shared helper 设计不稳，可能导致方法拆开后反而形成新的工具函数泥团。

### 验收标准

- `server.go` 只负责聚合依赖与路由委派。
- 各 RPC surface 对应清晰 handler。
- 拦截器与错误转换逻辑不发生契约变化。

## 阶段 6：配置与端口定义瘦身

### 目标

降低 `internal/config/config.go` 和 `internal/app/ports/interfaces.go` 的职责密度。

### 推荐拆分边界

1. `config/model`
2. `config/load`
3. `config/env`
4. `config/normalize`
5. `config/validate`
6. `app/ports/memory`
7. `app/ports/profile`
8. `app/ports/workspace`
9. `app/ports/retention`
10. `app/ports/scratchpad`

### 执行步骤

1. 先按文件拆分，不先改数据结构。
2. 维持原 public API 名称与导出行为。
3. 最后再清理重复 helper 与跨文件耦合。

### 风险点

- config 相关测试覆盖虽多，但拆分时 import 循环风险较高。
- ports 拆分过细会导致查找成本上升，需要平衡粒度。

### 验收标准

- `config.go` 不再承载全部核心配置流程。
- `interfaces.go` 不再成为所有 app port 的单点汇总文件。

# 实施顺序建议

## 第一优先级

1. 阶段 1：运行时装配层拆分
2. 阶段 2：统一记忆用例拆分
3. 阶段 3：PostAction 流水线拆分

## 第二优先级

4. 阶段 4：PostgreSQL 仓储边界收口
5. 阶段 5：gRPC 入站适配层拆分

## 第三优先级

6. 阶段 6：配置与端口定义瘦身

# 分阶段验证要求

## 每阶段结束至少执行

- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`

## 关键阶段结束额外执行

- 阶段 1、2、3、4 收尾时执行：
  - `go test ./...`

## 若影响运行时打包/配置目录规则

- 额外执行：
  - `.\make.ps1 build`
  - 或 `.\make.bat build`

# 最终验收标准

1. 高耦合文件的职责边界明显收敛，主要入口文件显著变薄。
2. `app` 层从“细节汇聚”转向“编排协调”。
3. `vldb_postgres` 从单一大实现向分边界仓储演进。
4. gRPC 入站适配层从单文件全集转向按业务面分治。
5. 配置与端口定义不再形成新的结构性瓶颈。
6. 所有阶段均保持既有运行时契约、测试结果与打包规范不被破坏。

# 当前状态

- 已生成具体实施计划，待用户确认具体从哪个阶段开始落地。

# 执行变更总结

## 1. 核心修复与调整概述

- 本次任务未修改业务代码，重点是把上一轮耦合分析结论整理为可执行的分阶段解耦路线图。
- 计划已明确给出六个阶段的拆分顺序、每阶段目标、推荐边界、执行步骤、风险点和验收标准。
- 计划整体采用“先装配、再用例、再仓储、再传输、最后配置与端口瘦身”的渐进式重构策略。

## 2. 📂文件变更清单

### 新增

- `docs/plan/20260410-02-COMPONENT_DECOUPLING_EXECUTION_PLAN.md`

### 修改

- 当前计划文件本身，补充了完整实施路线与执行总结。

### 删除

- 无。

## 3. 💻关键代码调整详情

### 本次未改动业务源码

- 没有修改 `internal/`、`cmd/`、`configs/` 下的任何业务实现。

### 本次沉淀的关键计划内容

- 明确了 6 个实施阶段：
  1. 运行时装配层拆分
  2. 统一记忆用例拆分
  3. PostAction 流水线拆分
  4. PostgreSQL 仓储边界收口
  5. gRPC 入站适配层拆分
  6. 配置与端口定义瘦身
- 明确了推荐优先级：
  - 第一优先级：阶段 1、2、3
  - 第二优先级：阶段 4、5
  - 第三优先级：阶段 6
- 明确了每阶段的最小测试要求与关键阶段全量测试要求。

## 4. ⚠️遗留问题与注意事项

- 当前仅完成计划制定，尚未进入任何阶段的实际重构。
- 后续正式开始落地前，建议优先从“阶段 1：运行时装配层拆分”启动，因为它对后续 memory/postaction/store 的边界整理具有先导作用。
- 若后续执行过程中涉及更深层的 port 重新定义或 PostgreSQL 仓储总入口重构，应把变更控制在单阶段内，避免横跨多个阶段同时改动导致回归面失控。
