# 任务计划：修复 LanceDB FFI runtime 的 EnterGuard panic

## 任务目标

本次任务目标是修复 `D:\projects\VulcanLocalDataGateway\vldb-lancedb` 在 FFI 模式下触发的 Tokio `EnterGuard values dropped out of order` panic，消除当前 Windows 环境中 `go test ./internal/app -count=1` 因真实 LanceDB 动态库初始化/关闭而出现的超时与崩溃；同时把修复后的动态库同步到 VMM 的本地运行目录，并完成回归验证。

## 执行步骤

1. 梳理 `vldb-lancedb` 当前 FFI 层的 Tokio runtime 使用方式，定位 `ffi_runtime`、`ffi_block_on` 与 runtime 生命周期管理的根因。
2. 将 FFI 执行模型调整为更稳健的运行方式，避免外部线程直接对全局 Tokio runtime 反复 `block_on/enter`。
3. 补充或更新上游 Rust 测试，验证运行时创建、引擎打开、表操作与销毁在重复调用下不会再触发 panic。
4. 重新构建 `vldb-lancedb` 动态库，并把产物同步到 `D:\projects\VulcanMemoryMesh\output\libs`。
5. 回到 VMM 仓库执行相关测试与构建，确认 `internal/app` 这类真实运行时装配测试不再因 LanceDB FFI runtime 崩溃。
6. 补充执行变更总结并归档计划文件。

## 技术选型与处理原则

- 优先从 `vldb-lancedb` 上游 FFI 层根治问题，而不是仅在 VMM 侧绕开真实 LanceDB。
- 保持现有导出符号与 FFI ABI 尽量兼容，不做不必要的接口破坏。
- 新增或修改的 Rust / Go 代码注释遵守当前仓库双语规范，重点说明为什么这样调整 runtime 模型。
- 回归验证同时覆盖上游库自身测试与 VMM 侧实际集成场景。

## 验收标准

1. `vldb-lancedb` FFI 不再因重复初始化/关闭或测试场景触发 `EnterGuard values dropped out of order` panic。
2. 新动态库已同步到 VMM 的 `output/libs` 目录。
3. VMM 至少完成 `go test ./internal/app -count=1` 与 `.\make.ps1 build` 的回归验证。
4. 计划文档完成执行总结并归档。

## 执行变更总结

### 1. 核心修复与调整概述

本次修复从 `vldb-lancedb` 上游 FFI 层根治了 Tokio `EnterGuard values dropped out of order` 问题。原先 FFI 直接在全局 Tokio runtime 上由外部线程反复 `block_on`，在 Go + purego + 多次初始化/关闭的真实集成场景下容易触发 runtime 上下文错位。现在改为由固定后台 worker 线程独占一个 `current_thread` Tokio runtime，所有 FFI async 调用通过 channel 串行投递到该线程执行，从而彻底避免外部线程直接进入 Tokio runtime。

### 2. 📂文件变更清单

新增：
- 无

修改：
- `D:\projects\VulcanLocalDataGateway\vldb-lancedb\src\ffi.rs`
- `docs/plan/20260418-01-fix-lancedb-ffi-runtime-enterguard.md`

删除：
- 无

### 3. 💻关键代码调整详情

- 在 `vldb-lancedb/src/ffi.rs` 中新增 `FfiRuntimeWorker` 与 `FfiRuntimeJob`，把 FFI 异步任务统一调度到固定后台线程。
- 将原先的 `ffi_runtime() + runtime.block_on(future)` 模式重构为 `ffi_runtime_worker() + ffi_block_on(move || async move { ... })` 模式，所有调用点均改为拥有值的 `async move`，避免跨线程借用问题。
- FFI worker 内部使用 `tokio::runtime::Builder::new_current_thread()` 构建单线程 runtime，进一步消除 multi-thread runtime worker 线程上的 EnterGuard 上下文错位风险。
- 增加跨线程压力测试 `ffi_block_on_dispatches_cross_thread_jobs_without_enter_guard_panics`，验证多线程并发发起 FFI 调用时不会再触发 EnterGuard panic。
- 重新构建 `vldb_lancedb.dll`，并同步到 `D:\projects\VulcanMemoryMesh\output\libs\vldb_lancedb.dll` 与 `D:\projects\VulcanMemoryMesh\third_party\deps\vldb_lancedb.dll`。

### 4. ⚠️遗留问题与注意事项

- `cargo build --release` 仍会提示同名 `pdb` 输出文件冲突 warning，这来自 crate 同时生成 bin 与 cdylib 的 Cargo 产物命名碰撞，不影响当前 DLL 功能，但后续若要清理发布日志，建议单独调整 bin/lib 产物命名或发布流程。
- 这次修复解决的是 LanceDB FFI runtime 的 EnterGuard panic；VMM 运行期里看到的 `noise gate embedding failed`、`pii engine common.json not found`、`sqlite query: context canceled` 属于现有测试夹具或启动/关闭过程中的预期噪音，不是本次 runtime 修复引入的新问题。
- 回归验证已通过：
  - `cargo test`（`D:\projects\VulcanLocalDataGateway\vldb-lancedb`）
  - `go test ./internal/app -count=1 -timeout 10m`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `.\make.ps1 build`
