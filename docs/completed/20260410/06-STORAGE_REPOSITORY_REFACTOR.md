# 第四阶段执行计划：存储仓储边界收口

## 1. 任务目标

本阶段聚焦 `internal/adapters/outbound/vldb_postgres` 当前“大 Store 多职责承载”的实现形态，在不改变上层 `app ports` 契约、运行时装配方式与现有测试结果的前提下，进一步按职责收口仓储边界，降低单一 `Store` 对 workspace、analysis、memory、retention、scratchpad 等能力的耦合度。

## 2. 执行步骤

1. 盘点 `vldb_postgres` 当前 `Store` 结构、字段组成、构造函数与各职责文件之间的调用关系。
2. 梳理其实现的 `app ports` 接口覆盖面，以及测试或运行时是否依赖某些隐式能力断言。
3. 设计“保持对外兼容、内部按职责拆装”的收口方案，明确核心入口与子仓储边界。
4. 将 `Store` 的内部依赖按职责域收束为更清晰的 repository bundle / capability 结构。
5. 修正相关构造、字段访问与注释，确保新结构与当前文件分工一致。
6. 运行关键测试与全量测试，确认行为不变。
7. 在计划末尾追加执行变更总结，并将计划归档到 `docs/completed/20260410/`。

## 3. 技术方案

- 保持 `vldb_postgres.Store` 作为对外兼容入口，避免影响运行时装配与上层 usecase 依赖。
- 在包内部引入更明确的职责承载结构，让 `Store` 从“直接堆叠全部实现”收口为“组合多个内聚子仓储能力”的薄入口。
- 优先采用同包内结构重组，不新增跨包依赖，不改变 `adapters -> app -> logic/domain` 的依赖方向。
- 对已经按文件分散但仍通过共享大结构耦合的逻辑，优先做构造与字段访问层面的解耦。

## 4. 验收标准

- `vldb_postgres.Store` 不再直接暴露为“单一大而全状态载体”，内部职责边界更清晰。
- 现有 public constructor、对外方法签名与接口满足关系保持兼容。
- 关键存储职责之间的字段访问与初始化路径更加明确，不再继续扩大“共享大状态”模式。
- 以下测试至少通过：
  - `go test ./internal/adapters/outbound/vldb_postgres`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `go test ./...`

## 5. 风险与关注点

- 需要避免影响运行时的 capability 断言与现有 `app` 层装配逻辑。
- 需要确认 SQLite / PostgreSQL 共享抽象是否在字段层面存在隐式耦合，避免误收口后破坏事务或后台补偿逻辑。
- 需要保证构造期默认值、logger、schema 管理与 GC 相关能力在拆分后仍完整可用。

## 6. 当前状态

- 状态：已完成
- 当前阶段：结构收口、验证与总结已完成，待归档

---

## 执行变更总结

### 1. 核心修复与调整概述

- 已将 `vldb_postgres.Store` 从“直接只靠共享字段表达全部职责”的形态，收口为“public facade + shared core + repository bundle”的内部结构。
- 本次保留了 `Store` 的对外构造方式与现有方法集合，不影响运行时装配、上层 `app ports` 契约和既有测试。
- 同时兼顾了现有大量轻量测试直接使用 `Store{cfg: ...}` 的构造方式，避免为了内部收口破坏仓库已有测试风格。

### 2. 📂 文件变更清单

#### 新增

- `internal/adapters/outbound/vldb_postgres/repositories.go`

#### 修改

- `internal/adapters/outbound/vldb_postgres/store.go`

#### 删除

- 无

### 3. 💻 关键代码调整详情

- 新增 `storeShared`，统一承载 PostgreSQL 适配器共享的 `pool`、`cfg`、`dialect` 三类运行时核心状态。
- 新增 `storeRepositories` 以及 workspace / turn / analysis / memory / profile / retention / scratchpad / vector / maintenance 等内部 repository marker 结构，明确当前组合库内部的职责分区。
- 将 `Store` 重构为显式持有 `shared` 与 `repos` 的薄门面，同时保留 `pool`、`cfg`、`dialect` 兼容镜像字段，确保旧测试与现有轻量构造方式不受影响。
- 在 `NewStore` 中新增 shared core 与 repository bundle 的装配逻辑，使运行时结构与后续继续拆分的目标边界保持一致。
- 已完成代码格式化，并通过以下验证：
  - `go test ./internal/adapters/outbound/vldb_postgres`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
  - `go test ./...`

### 4. ⚠️ 遗留问题与注意事项

- 当前阶段主要完成的是“结构表达层”的收口，尚未把各职责文件的方法 receiver 全量迁移到各自 repository 类型上；这是为了避免一次性打断现有大量跨文件内部辅助调用。
- `pool` / `cfg` / `dialect` 兼容镜像字段仍暂时保留在 `Store` 上，后续如果继续推进更深层的 receiver 迁移，可以再逐步减少这些兼容字段的直接使用面。
