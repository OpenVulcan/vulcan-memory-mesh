# 全仓库审计修复记录（2026-09-26）

## 结果与范围

对应 [审计报告](REPOSITORY_AUDIT_20260926_CN.md) 的 A01–A13，已完成代码修复、测试清理、按职责拆分和文档同步。源码位于原工作区，分支为 `codex/repository-audit-repair`，尚未提交。

保留当前四种存储模式及管理 HTTP 能力。没有更换核心依赖或数据库 schema；跨库存储结果不明、向量补偿、FFI 生命周期、隐私清洗和外部请求边界继续按原有业务契约处理。

## 逐项修复

| 编号 | 实际调整 | 主要入口及验证 |
| --- | --- | --- |
| A01 | LLM 输出的 `profile_type` 使用指针区分缺失与合法零值；缺失、null、越界明确报错 | [结构定义](../internal/logic/processor/structured_output_schema.go)、[解析回归](../internal/logic/processor/profile_type_test.go) |
| A02 | LanceDB 检索仅从 `_distance` 计算分数；拒绝缺失、空白、非有限和负距离；合法零距离不再被 `score` 覆盖；损坏元数据返回记录与字段错误 | [适配器](../internal/adapters/outbound/vldb_lancedb/store.go)、[检索行回归](../internal/adapters/outbound/vldb_lancedb/search_row_test.go) |
| A03 | 所有实际配置加载入口从 `DefaultBase` 初始化；移除 68 处数值回填/钳位块和为补救归一化而引入的显式值标记、自定义反序列化；数值范围、key policy 和负 cooldown 由校验拒绝 | [归一化](../internal/config/config_runtime.go)、[校验](../internal/config/config_validate.go)、[真实加载回归](../internal/config/config_numeric_test.go) |
| A04 | 统一 SQLite 在线读取、迁移、导出和缓存的严格向量解码；损坏值不再转为空数组。写入序列化拒绝 NaN/Inf；管理恢复在首次恢复写入前验证完整向量快照 | [行编码解码](../internal/adapters/outbound/vldb_sqlite/rows.go)、[向量完整性回归](../internal/adapters/outbound/vldb_sqlite/vector_validation_test.go)、[迁移及在线读取回归](../internal/adapters/outbound/vldb_sqlite/native_migration_test.go) |
| A05 | 启动时选择并验证完整提示词包，运行时固定其目录；用户包读取错误不会切换来源；错误携带场景与路径 | [提示词管理](../internal/config/manager.go)、[包选择和文件变更回归](../internal/config/manager_test.go) |
| A06 | `MemoryStore` 明确要求 `ApplyDirectMemoryWrite`；删除可选能力探测、用例层自行拼装写入的备用分支和仅为它存在的替身测试。SQLite 仍按语句提交，由存储显式报告部分写入与新向量引用状态 | [端口](../internal/app/ports/ports_memory.go)、[写入用例](../internal/app/usecase/memory_query_write.go)、[提交及补偿测试](../internal/app/usecase/memory_write_commit_test.go) |
| A07 | gRPC interceptor 按框架保证的 context/info/handler 使用；删除 10 个不符合实际调用契约的测试和请求类型猜测辅助逻辑 | [拦截器](../internal/adapters/inbound/grpcapi/interceptors.go)、[真实边界测试](../internal/adapters/inbound/grpcapi/server_test.go) |
| A08 | DashScope/SiliconFlow rerank 分类依赖已有类型化 APIError；删除从普通错误文本猜测 HTTP 状态码的逻辑，未知错误归为 unknown | [错误分类](../internal/adapters/outbound/ai_key_failover/classifier.go)、[类型化错误测试](../internal/adapters/outbound/ai_key_failover/key_failover_test.go) |
| A09 | 删除两项提示词固定措辞测试；SQLite/LanceDB 测试替身对未声明数据库操作默认失败。修正由此暴露的 12 个夹具，FTS 共享辅助函数精确断言操作 ID 和顺序 | [SQLite 替身](../internal/adapters/outbound/vldb_sqlite/store_fake_test.go)、[LanceDB 替身](../internal/adapters/outbound/vldb_lancedb/engine_fake_test.go) |
| A10 | 10 个 Go 大文件按实际职责在原包内拆分；Rust 按 ABI、错误、FFI 辅助、marker、Arrow 转换和表操作拆成模块；保留函数体和现有故障场景 | 见下表 |
| A11 | SQLite 父目录计算使用 `filepath.Dir`，删除手工斜杠扫描 | [存储构造](../internal/adapters/outbound/vldb_sqlite/store.go) |
| A12 | 删除无引用的 `memoryEntryRow/toDomain`、两个 `decodeStringMap` 和 `ProfileStatusMerged` | 已检索确认符号及其调用均清除 |
| A13 | 更新 AGENTS 与 README 的四模式范围和管理 HTTP 描述，同步配置、提示词包和持久化验证边界；检查并更新 post-action 与 NoiseGate 文档 | [协作规范](../AGENTS.md)、[README](../README.md)、[post-action](post-action-guide_CN.md)、[NoiseGate](noise-gate-guide_CN.md) |

配置中已明确规定的零值语义继续有效：例如可选 rerank 路由超时与 key cooldown 的零值采用默认值，负值拒绝；允许零的衰减参数保持零。空/缺省持久化向量仍按既有契约表示没有向量，非空损坏内容则报错。

NoiseGate 缓存损坏现在提供明确诊断，再由已有业务策略重新计算 embedding；语义服务失败时的 regex-only 策略仍保留。

## 拆分结果

行数含注释与空行。“入口行数”仅表示原文件拆分后保留的职责，其他代码迁入同包文件，不表示将对应行为删除。

| 原文件 | 审计时行数 | 当前入口行数 | 拆分后的职责 |
| --- | ---: | ---: | --- |
| SQLite `store.go` | 7,094 | 199 | 生命周期、schema、执行、写入对账、turn、memory、profile、workspace、FTS、rows，共 18 个文件 |
| SQLite `store_test.go` | 8,514 | 132 | 按关系域、写入阶段、FTS、生命周期、上下文边和故障场景拆为 23 个文件 |
| `memory_query_test.go` | 4,278 | 443 | 检索、融合、排序、去重、提交、删除和夹具，共 12 个文件 |
| `precheck_test.go` | 2,599 | 188 | 入口、召回、上下文、PII、评审、证据、日志和夹具，共 8 个文件 |
| SQLite `retention_store_test.go` | 2,564 | 122 | 记忆、session、turn、purge、回收任务、向量 GC 和夹具，共 7 个文件 |
| `config_test.go` | 2,396 | 200 | 默认值、存储、路由、分层、环境变量和加载，共 6 个文件 |
| `memory_query_search.go` | 1,578 | 360 | 编排、融合、衰减、上下文、排序、日志、实体化，共 7 个文件 |
| `service_darwin.go` | 1,227 | 259 | 生命周期、plist、服务规范、状态解释，共 4 个文件 |
| `precheck.go` | 1,157 | 364 | 编排、历史、召回、上下文、日志，共 5 个文件 |
| `vector_rebuild.go` | 1,131 | 267 | 编排、快照、校验、native、split、embedding，共 6 个文件 |
| Rust `lib.rs` | 2,061 | 639 | ABI 入口、error、ffi、marker、arrow_codec、table，共 6 个模块 |

拆分后第一方生产 Go 文件最大为 `config_validate.go`（1,023 行）。仍有超过 2,000 行的集成/业务测试文件；本次按已确认的职责混杂拆分，不以统一行数阈值判断测试是否冗余。

## 验证证据

| 验证 | 结果 |
| --- | --- |
| 启用真实打包依赖后的 `go test ./... -count=1 -json -timeout=20m` | 1,342 个测试/子测试 pass 事件，5 个测试 skip，0 fail；38 个有测试包通过，另 7 个包无测试 |
| `go vet ./...` | 通过 |
| 第三方嵌套 Go 模块 `third_party/vldb-controller-client-go` 的 `go test ./...` | 5 个测试通过，2 个包 |
| `python -m unittest discover -s scripts -p "test_*.py"` | 23 项：20 通过，3 跳过 |
| Rust `cargo test --locked` | 2 个 marker 恢复/原子写入测试通过 |
| Rust `cargo fmt -- --check` | 通过 |
| `make.ps1 deps native` | 从当前源码成功构建 Windows 原生动态库，锁文件与源码摘要校验通过 |
| `VMM_BUILD_STORAGE_PROFILE=all` 下 `make.ps1 build` | 在隔离工作区按标准布局成功构建，并校验/同步全部依赖和配置 |
| Darwin ARM64 的 `cmd/vmm-local` 测试交叉编译 | 通过；仅编译证据 |
| `git diff --check` 与修改文件 `gofmt -l` | 通过，无格式残留 |

真实依赖验收已通过以下测试：

- `TestNativeLibraryABIAndErrorBoundary`
- `TestNativeLibraryRoundTrip`
- `TestNativeLibraryPendingSchemaRecovery`
- `TestPackagedNativeRuntimeUsesIsolatedNativeArtifacts`
- 同一打包程序的 `legacy-split` 与 `legacy-controller` 子测试

这些测试使用临时数据与本地供应商替身，实际启动标准包并走存储/向量链路；没有消费收费模型服务或改动现存业务数据库。

### 标准构建目录

原工作区第一次标准构建在同步 `output/configs/base.yaml` 时拒绝覆盖未归属且内容不同的文件。保留该保护及文件原内容；该次执行已经编译二进制，但配置同步未完成，因此原目录不能被当作本次完整验收包。

完整验收包位于：

`C:\Users\lingeasy\.codex\worktrees\repository-audit-validation\VulcanMemoryMesh\output`

其中运行入口为 `output/bin/vmm-local.exe`，配置为 `output/configs`，原生及旧模式依赖为 `output/libs`。已对原工作区与隔离构建工作区的 140 个变更 Go/Rust 文件逐字节比较，全部一致。

最终全量测试从原工作区运行，设置：

```text
VMM_NATIVE_PACKAGED_ROOT=<上述隔离工作区>/output
VMM_NATIVE_LANCEDB_LIBRARY=<上述隔离工作区>/output/libs/vmm_lancedb_native.dll
VMM_PACKAGED_STORAGE_PROFILE=all
```

### 尚未进行的外部验收

五个跳过测试分别是：一个真实 DashScope 供应商测试、两个需要 Windows 符号链接权限的测试、两个依赖 Unix 权限位的测试。Python 跳过项受 Unix Bash/GnuPG 条件限制。

本次没有执行真实 PostgreSQL 服务端验收、Linux/macOS 动态库运行或系统服务安装；这些不由本机单元测试与交叉编译结果替代。修改源码和完整验收包均保留供复核，尚未提交或推送。
