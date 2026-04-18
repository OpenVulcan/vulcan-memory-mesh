# 任务计划：补充 LanceDB FFI runtime 修复文档并提交推送

## 任务目标

本次任务目标是在 `D:\projects\VulcanLocalDataGateway\vldb-lancedb` 仓库中补充本轮 FFI runtime `EnterGuard` 修复的说明文档，明确新的执行模型、兼容性边界以及对其他集成端的影响；随后完成提交与推送，并在 VMM 侧记录归档。

## 执行步骤

1. 梳理 `vldb-lancedb` 当前 README、库使用说明或示例文档，确定最适合补充 FFI runtime 修复说明的位置。
2. 增补文档，说明：
   - 问题现象与修复背景
   - 当前 FFI runtime 的 worker-thread + current-thread Tokio 模型
   - 对 ABI、现有接入代码和并发特性的兼容性影响
3. 检查文档改动后仓库状态，使用中文提交信息完成 commit。
4. 推送到远端分支。
5. 在当前仓库补执行总结并归档计划文件，同时向用户说明是否会影响其他已集成端。

## 技术选型与处理原则

- 不夸大修复范围，只说明真实影响。
- 文档优先落在最容易被集成方看到的位置。
- 保持导出符号、头文件和 FFI 调用方式不变的兼容性结论清晰可见。
- 如存在潜在吞吐差异，也必须如实写明。

## 验收标准

1. `vldb-lancedb` 仓库文档已补充本次 FFI runtime 修复说明。
2. 修改已使用中文提交信息完成 commit 并 push。
3. 已明确说明对其他集成端的实际影响与边界。
4. 当前计划文件已完成执行总结并归档。

## 执行变更总结

### 1. 核心修复与调整概述

本次在 `vldb-lancedb` 仓库中补充了 FFI runtime 修复说明文档，并将前一轮 runtime 修复代码与文档一并提交推送到远端 `main`。文档重点说明了固定 worker 线程 + `current_thread` Tokio runtime 的执行模型、为何修复 `EnterGuard` 崩溃，以及对 ABI、gRPC 模式和其他集成端的兼容性影响。

### 2. 📂文件变更清单

新增：
- 无

修改：
- `D:\projects\VulcanLocalDataGateway\vldb-lancedb\src\ffi.rs`
- `D:\projects\VulcanLocalDataGateway\vldb-lancedb\README.md`
- `D:\projects\VulcanLocalDataGateway\vldb-lancedb\docs\LIBRARY_USAGE.zh-CN.md`
- `D:\projects\VulcanLocalDataGateway\vldb-lancedb\docs\README.en.md`

删除：
- 无

### 3. 💻关键代码调整详情

- 在 `README.md` 中补充 FFI runtime 修复说明，给接入方一个最短路径结论：ABI 不变、gRPC 不受影响、FFI 极端高并发入口层会串行调度。
- 在 `docs/LIBRARY_USAGE.zh-CN.md` 中增加完整的 FFI runtime 执行模型与兼容性章节，明确：
  - 当前 FFI 不再由宿主线程直接 `block_on` Tokio runtime
  - Go / purego / 多线程宿主下的 `EnterGuard` 问题已被规避
  - Rust 直接嵌入和 gRPC 服务模式不受影响
- 在 `docs/README.en.md` 中同步增加英文兼容性说明，避免英文读者只看到旧行为描述。
- 使用中文结构化提交信息完成 commit，并已推送到 `origin/main`：
  - 提交号：`21b62b7`
  - 标题：`修复 FFI runtime 的 EnterGuard panic 并补充兼容性文档`

### 4. ⚠️遗留问题与注意事项

- 对其他已集成端的影响结论如下：
  - **不会影响 ABI 层接入**
    - 导出符号没变
    - 头文件没变
    - 现有 Go / C / 其他原生绑定不需要改函数签名
  - **不会影响 gRPC 集成端**
    - 服务模式走 `main.rs` 自己的 Tokio runtime，不经过 FFI worker
  - **不会影响 Rust 直接嵌入端**
    - 直接用 `lib` typed API 不经过这层 FFI 调度器
  - **可能影响极端高并发 FFI 宿主的吞吐上限**
    - 入口层改为 worker 串行调度后，多宿主线程同时密集发起 FFI 调用时，跨线程并行度会低于旧实现
    - 这是为了换取更稳定的 runtime 生命周期与跨语言可预测性
- 远端推送已完成，无需额外人工操作。
