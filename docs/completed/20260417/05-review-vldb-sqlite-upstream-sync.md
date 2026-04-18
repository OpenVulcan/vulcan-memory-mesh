# VMM 对齐 vldb-sqlite 最近提交的影响评估计划

## 任务目标

分析 `D:\projects\VulcanLocalDataGateway\vldb-sqlite` 最近一次及近几次提交带来的接口、行为或依赖变化，判断当前 `VulcanMemoryMesh` 中已经落地的 SQLite FFI 接入是否需要同步调整，并输出明确结论：

1. 哪些变化对当前 VMM 有直接影响。
2. 哪些变化仅影响上游示例、文档或暂未使用能力。
3. 如果需要修改，具体应该修改哪些文件、为什么修改。

## 执行步骤

1. 查看 `vldb-sqlite` 最近提交记录与改动文件范围。
2. 聚焦 FFI 导出、头文件、Go FFI 示例、版本号与 QueryStream / 资源释放相关实现。
3. 对照当前 VMM 中的：
   - `internal/platform/ffi/sqliteffi`
   - `internal/adapters/outbound/vldb_sqlite`
   - `go.mod`
   - 构建与依赖下载脚本
4. 判断是否存在：
   - ABI 不兼容
   - 符号缺失或新增未接入
   - 返回结构变化
   - 行为变化导致的适配层逻辑偏差
   - 版本依赖应升级但尚未升级
5. 输出结论；若需要改动，再给出精确修改建议或直接补丁方案。

## 技术约束

- 本次首先进行分析，不盲目改动当前已工作的 FFI 主链路。
- 若发现问题，必须区分“阻塞性问题”和“建议性跟进”。
- 结论必须基于上游源码与当前 VMM 实现逐项对照，不凭印象判断。

## 验收标准

1. 能明确说明上游最近提交是否要求 VMM 同步改动。
2. 如果需要改动，必须指出具体文件与原因。
3. 如果不需要改动，也必须说明为什么当前实现仍然兼容。

## 执行变更总结

### 1. 核心修复与调整概述

- 已分析 `vldb-sqlite` 最近两次提交（`412dd79`、`1d5b399`）的改动范围。
- 结论是：当前 `VulcanMemoryMesh` 不需要为了这次上游提交立即做对应代码修改。
- 原因是上游本次真正发生行为变化的核心点在 `QueryStream`，而当前 VMM 的 SQLite FFI 主链路只使用：
  - `ExecuteScript`
  - `ExecuteBatch`
  - `QueryJSON`
  - FTS / tokenizer 相关接口
- 这些当前 VMM 实际使用的接口，在上游本次提交中没有发生破坏性 ABI 或调用语义变化。

### 2. 📂 文件变更清单

新增：

- `docs/plan/20260417-05-review-vldb-sqlite-upstream-sync.md`

修改：

- 无需修改当前业务代码与构建脚本。

删除：

- 无

### 3. 💻 关键代码调整详情

- 上游 `vldb-sqlite` 本次改动集中在：
  - `src/ffi.rs`
  - `src/sql_exec.rs`
  - `examples/go-ffi/sqliteffi/sqliteffi.go`
- 主要变化是：
  - `QueryStream` 改为后台生成 chunk，并落到临时文件
  - `chunk_count / row_count / total_bytes` 不再是“创建句柄时立即稳定可读”的模型，而是需要等待后台流完成
  - Go 示例新增 `WaitMetrics()`，避免调用方过早读取 `ChunkCount/RowCount/TotalBytes`
  - JSON 兼容层增加了 QueryStream registry 清理与 TTL 逻辑
- 当前 VMM 对照结果：
  - [sqliteffi.go](D:/projects/VulcanMemoryMesh/internal/platform/ffi/sqliteffi/sqliteffi.go) 没有封装 `QueryStream`
  - [store.go](D:/projects/VulcanMemoryMesh/internal/adapters/outbound/vldb_sqlite/store.go) 也没有在主链路使用 `QueryStream`
  - 当前运行时只依赖 `ExecuteScript / ExecuteBatch / QueryJSON / FTS`
  - `scripts/install_host_deps.ps1` 会取最新 tag，因此构建侧不需要为了 `0.1.5` 额外改脚本

### 4. ⚠️ 遗留问题与注意事项

- 当前无需修改 VMM 主链路代码。
- 但如果后续要在 VMM 中接入 SQLite FFI 的 `QueryStream`，则必须同步补以下能力：
  - 在 [sqliteffi.go](D:/projects/VulcanMemoryMesh/internal/platform/ffi/sqliteffi/sqliteffi.go) 增加 `QueryStream` 相关符号绑定
  - 采用与上游示例一致的“先 `WaitMetrics()`，再 `CollectAll()` 或逐块读取”的调用模型
  - 不能沿用“创建句柄后立即读取 chunk 数量”的旧假设
- 当前这次上游提交不会影响现有 `split` 模式运行、构建、SQLite FTS、分词选择或迁移导出链路。
