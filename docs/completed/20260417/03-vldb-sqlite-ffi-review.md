# vldb-sqlite 通用 SQL FFI 改动评审计划

## 任务目标

对 `D:\projects\VulcanLocalDataGateway\vldb-sqlite` 当前已经完成的修改进行评审，判断其是否满足此前明确的需求：

1. 补齐 `ExecuteScript`、`ExecuteBatch`、`QueryJson`、`QueryStream` 四类通用 SQL FFI 能力。
2. 保持 gRPC 模式与 lib/FFI 模式双态并存，且核心执行逻辑尽量统一。
3. 不破坏当前 tokenizer / custom words / FTS 等已存在 FFI 能力。
4. 为后续 VMM 彻底移除 SQLite gRPC 依赖提供足够能力。

## 执行步骤

1. 检查 `vldb-sqlite` 当前改动文件、差异范围和提交状态。
2. 对照 proto、FFI 导出、头文件、示例与测试，核查新增能力是否完整。
3. 对照 VMM 当前 SQLite 适配层实际调用面，判断是否已经足够支撑迁移。
4. 输出结构化评审结论，优先列出阻塞问题、风险点和未覆盖项。

## 技术选型与约束

- 本轮仅做分析评审，不在当前仓库内修改业务代码。
- 评审标准以“是否满足 VMM 迁移需要”为主，而不只看 `vldb-sqlite` 自身是否能编译通过。
- 如果发现能力不满足，将明确指出缺失点属于：
  - 功能未实现
  - 接口未导出
  - 双态不一致
  - 测试或文档未闭环

## 验收标准

1. 能明确给出“满足 / 部分满足 / 不满足”的结论。
2. 若不满足，能指出具体阻塞项及所在文件。
3. 若满足，能说明为什么已经足够支撑 VMM 后续接入改造。

## 执行变更总结

### 1. 核心修复与调整概述

本轮对 `D:\projects\VulcanLocalDataGateway\vldb-sqlite` 当前已完成的 SQL FFI 改动进行了评审，结论为：

1. 当前版本已经补齐 `ExecuteScript`、`ExecuteBatch`、`QueryJson`、`QueryStream` 四类通用 SQL FFI 能力。
2. gRPC 与 FFI 已共享 `src/sql_exec.rs` 这一套核心执行逻辑，而不是各自复制一份实现。
3. 头文件、能力声明、Go FFI 示例、中文/英文文档与测试均已同步。
4. 对于 VMM 当前真实依赖面而言，这版 `vldb-sqlite` 已经足够支撑后续把 SQLite 接入从 gRPC 改为本地 lib/FFI。
5. 仍存在少量“非阻塞注意事项”，主要集中在 QueryStream 的内存形态与 FFI 错误分类仍偏文本语义，但不构成当前迁移阻塞。

### 2. 📂 文件变更清单

新增：

1. `docs/plan/20260417-03-vldb-sqlite-ffi-review.md`

修改：

1. 当前阶段仅在本计划文件中补充了评审结论与执行总结。

删除：

1. 无。

### 3. 💻 关键代码调整详情

本轮未修改业务代码，主要完成以下评审核验：

1. FFI 导出面：
   - `src/ffi.rs` 已新增 `database_execute_script`、`database_execute_batch`、`database_query_json`、`database_query_stream`，并补齐结果句柄 getter。
   - JSON 兼容层已新增 `execute_script_json`、`execute_batch_json`、`query_json_json`、`query_stream_json`、`query_stream_chunk_json`、`query_stream_close_json`。
2. 双态共享核心：
   - `src/service.rs` 与 `src/ffi.rs` 都已复用 `src/sql_exec.rs`。
   - 说明 gRPC 与 FFI 在通用 SQL 主路径上已经实现了共享核心，而不是漂移的双实现。
3. ABI 与宿主接入：
   - `include/vldb_sqlite.h` 已补齐通用 SQL FFI 所需的值类型、批量参数切片、查询结果句柄与 QueryStream chunk 读取接口。
   - `examples/go-ffi/sqliteffi/sqliteffi.go` 已同步补齐 Go wrapper。
4. 文档与验证：
   - `docs/LIBRARY_USAGE.zh-CN.md`、README、gRPC 集成文档与 Go FFI 示例文档均已同步说明 SQL FFI 能力。
   - `cargo test` 已在评审过程中执行通过。

### 4. ⚠️ 遗留问题与注意事项

1. `QueryStream` 在 FFI 非 JSON 主接口中仍采用“先收集 chunk，再由句柄逐块读取”的形态，这对当前 VMM 不是阻塞，因为 VMM 当前并不使用 `QueryStream`。
2. FFI 错误返回目前仍以 `last_error_message` 文本为主，并没有像 gRPC trailer 那样显式暴露 `retryable` 标记；后续 VMM 接入时仍需自己实现本地错误分类与重试策略。
3. 这并不影响当前结论：从 VMM 实际依赖的 `ExecuteScript`、`ExecuteBatch`、`QueryJson` 三类能力，以及 tokenizer / FTS 能力来看，当前 `vldb-sqlite` 已满足迁移前提。
