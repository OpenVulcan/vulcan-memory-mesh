# 任务计划：清理旧分词兼容字段与遗留逻辑

## 任务目标

本次任务目标是彻底清理 VulcanMemoryMesh 中因旧 Go 侧分词逻辑保留的兼容字段、兼容分支与兼容文档说明，确保当前项目仅使用 `vldb-sqlite` 内置 tokenizer / FTS 能力，不再保留无必要的历史开关与回退路径。

## 执行步骤

1. 定位当前项目中与旧分词逻辑相关的所有兼容点。
2. 区分“仍然被主链路使用”与“仅为历史兼容保留”的代码与配置。
3. 删除无必要的兼容字段、旧 fallback 分支、旧 tokenizer 相关辅助逻辑。
4. 同步调整配置默认值、示例配置与 README / 指南文档中的描述。
5. 运行规定测试与必要构建命令，确认清理后主链路仍可正常工作。

## 技术选型与处理原则

- 仅保留当前本地 FFI + SQLite 内置 tokenizer 方案。
- 不再保留 `memory_pipeline.lexical_pre_tokenize` 之类仅用于历史兼容的字段。
- 不再保留仅服务于旧 SQL FTS 方案的 Go 侧 tokenizer 构造逻辑。
- 若某段代码仅用于历史 gRPC / mock 兼容，且当前正式架构已明确废弃，则一并清理。
- 修改过程中保持依赖方向不变，不重新引入旧 gRPC 存储路径。

## 验收标准

1. 配置结构中不再存在无必要的旧分词兼容字段。
2. SQLite 适配层不再保留旧 Go tokenizer fallback 主逻辑。
3. 文档中不再将旧分词开关作为兼容说明保留。
4. 规定测试通过，且标准构建可完成。

## 执行变更总结

### 1. 核心修复与调整概述

- 删除 `memory_pipeline.lexical_pre_tokenize` 配置字段及其环境变量覆盖入口，避免继续暴露已失效的旧分词开关。
- 清理 SQLite 适配层中仅服务于旧 Go 预分词链路的遗留代码，包括旧 lexical fallback 查询路径、旧 FTS 镜像 SQL 构造器，以及依赖旧镜像表的脚本片段。
- 删除 `internal/platform/textutil` 下已不再被主链路使用的旧 `LexicalTokenizer` 实现与对应测试。
- 同步更新 README 与基础配置说明，使当前仓库对外只保留 `sqlite.tokenizer_mode` 这一条正式分词控制入口。

### 2. 📂文件变更清单

新增：

- 无

修改：

- `README.md`
- `configs/base.yaml`
- `go.mod`
- `go.sum`
- `internal/adapters/outbound/vldb_sqlite/store.go`
- `internal/adapters/outbound/vldb_sqlite/retention_store.go`
- `internal/adapters/outbound/vldb_sqlite/store_test.go`
- `internal/adapters/outbound/vldb_sqlite/retention_store_test.go`
- `internal/config/config.go`
- `internal/config/config_validate.go`

删除：

- `internal/platform/textutil/lexical_tokenizer.go`
- `internal/platform/textutil/lexical_tokenizer_test.go`

### 3. 💻关键代码调整详情

- `MemoryPipelineConfig` 中移除了 `LexicalPreTokenize` 字段，同时删除默认值填充与 `VMM_MEMORY_LEXICAL_PRETOKENIZE` 环境变量映射。
- `SearchLexicalMemory` 现在只接受本地 SQLite FFI 数据库句柄，不再保留旧 SQL FTS 镜像回退逻辑。
- retention 回收脚本不再拼接 `vmm_memory_nodes_fts` 旧镜像表删除语句，转而完全依赖当前内建 FTS 索引同步机制。
- 删除旧 `LexicalTokenizer` 后，`go.mod` / `go.sum` 同步移除了 `github.com/go-ego/gse` 及其残留间接依赖。
- SQLite 相关测试改为验证“不会再引用旧 FTS 镜像表”，并删除了针对旧 Go 预分词行为的测试断言。

### 4. ⚠️遗留问题与注意事项

- `go test ./internal/adapters/outbound/vldb_sqlite ./internal/config ./internal/platform/textutil ./internal/platform/pii ./internal/logic/processor ./internal/app/usecase ./internal/adapters/inbound/grpcapi -count=1` 已通过。
- `go test ./internal/app -count=1` 在当前环境下仍出现运行期失败与超时，表现为本地存储维护链路测试中的上下文取消；本次改动已额外通过 `go test ./internal/app -run TestDoesNotExist -count=1` 确认包级编译无误。
- `go test ./... -count=1` 在当前环境下超时；本次改动已额外通过 `go test ./... -run TestDoesNotExist -count=1` 确认全仓编译无误。
