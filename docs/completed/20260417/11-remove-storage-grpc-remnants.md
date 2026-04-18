# 任务计划：移除 SQLite 与 LanceDB 的 gRPC 残留并继续收敛本地 FFI 存储链路

## 任务目标

本次任务目标是彻底移除当前项目中 SQLite 与 LanceDB 外部 gRPC 对接的残留物，包括 proto 生成代码、legacy fallback、旧测试桩与调试清理工具中的网关依赖；同时继续完成上一轮 SQLite schema 基线冻结任务的剩余验证与归档，使当前存储链路完全收敛到本地 FFI 形态。

## 执行步骤

1. 梳理当前仓库中 `vldb_sqlite/proto/v1`、`vldb_lancedb/proto/v1`、legacy gRPC fallback 与相关测试/调试代码的真实引用。
2. 移除 SQLite 与 LanceDB 适配器中不再需要的 gRPC proto 依赖和 fallback 逻辑，改为纯本地 FFI 实现。
3. 重写受影响测试，去掉对旧 gRPC fake gateway / proto request 的依赖，改用更贴近当前实现的本地 stub 或真实集成测试。
4. 清理不再需要的 outbound proto 生成文件与旧调试工具残留。
5. 继续完成并验证上一轮“SQLite schema 基线固定为 19”的相关工作，运行测试并归档计划文件。

## 技术选型与处理原则

- 不保留任何对外部 SQLite / LanceDB gRPC 网关的正式运行时依赖。
- 优先保留当前已经稳定的本地 FFI 主链路，不为迁移成本继续维持双轨实现。
- 测试应尽量转向本地 stub 或真实 FFI 集成测试，减少对旧 proto 结构的耦合。
- 新增或重写代码、注释、测试必须遵守仓库双语规范。

## 验收标准

1. 当前仓库不再保留 `internal/adapters/outbound/vldb_sqlite/proto/v1` 与 `internal/adapters/outbound/vldb_lancedb/proto/v1` 的运行时依赖。
2. SQLite / LanceDB 适配器中不再存在面向旧 gRPC client 的 fallback 逻辑。
3. 相关测试可通过且不再依赖旧 proto request/response 结构。
4. `go test` 相关包通过，且上一轮 schema 基线任务的行为保持正确。

## 执行变更总结

### 1. 核心修复与调整概述

本次调整彻底移除了 SQLite 与 LanceDB 存储侧外部 gRPC 网关残留，统一收敛为本地 FFI 形态。适配器、调试清理工具与迁移命令均改为直接打开本地动态库与数据库目录；对应的旧 proto 生成代码与基于 gRPC request/response 的测试桩已全部清理，避免仓库继续误导为“双轨存储接入”。

### 2. 📂文件变更清单

新增：
- `internal/adapters/outbound/vldb_sqlite/store_fake_test.go`
- `internal/adapters/outbound/vldb_lancedb/engine_fake_test.go`

修改：
- `internal/adapters/outbound/vldb_sqlite/store.go`
- `internal/adapters/outbound/vldb_sqlite/debug_clean.go`
- `internal/adapters/outbound/vldb_sqlite/store_test.go`
- `internal/adapters/outbound/vldb_sqlite/scratchpad_test.go`
- `internal/adapters/outbound/vldb_sqlite/retention_store_test.go`
- `internal/adapters/outbound/vldb_lancedb/store.go`
- `internal/adapters/outbound/vldb_lancedb/debug_clean.go`
- `internal/adapters/outbound/vldb_lancedb/store_test.go`
- `internal/app/local_storage_layout.go`
- `internal/app/app_test.go`
- `cmd/vmm-migrate/clean.go`
- `cmd/vmm-migrate/main.go`

删除：
- `internal/adapters/outbound/vldb_sqlite/proto/v1/sqlite.proto`
- `internal/adapters/outbound/vldb_sqlite/proto/v1/sqlite.pb.go`
- `internal/adapters/outbound/vldb_sqlite/proto/v1/sqlite_grpc.pb.go`
- `internal/adapters/outbound/vldb_lancedb/proto/v1/lancedb.pb.go`
- `internal/adapters/outbound/vldb_lancedb/proto/v1/lancedb_grpc.pb.go`

### 3. 💻关键代码调整详情

- 在 SQLite 与 LanceDB store 中删除旧的 gRPC client / fallback 分支，改为通过收窄接口直接面向本地 FFI database / engine 句柄。
- 在 `debug_clean.go` 与 `cmd/vmm-migrate/clean.go` 中改用本地库路径、数据库路径与向量目录执行清理，不再依赖远程 address。
- 重写相关测试，新增本地 fake database / engine，直接校验 typed params、批量写入、过滤条件和 schema 版本行为，避免继续耦合旧 proto 结构。
- 删除 outbound proto 生成物与 proto 定义文件，清空仓库对存储侧 `sqlitev1` / `lancedbv1` 的编译依赖。

### 4. ⚠️遗留问题与注意事项

- `go test ./internal/adapters/outbound/vldb_sqlite -count=1`、`go test ./internal/adapters/outbound/vldb_lancedb -count=1`、`go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`、`go test ./... -run TestDoesNotExist -count=1` 与 `.\make.ps1 build` 已通过。
- `go test ./internal/app -count=1` 在当前 Windows 环境下仍会命中上游 `vldb-lancedb` 动态库的 `tokio EnterGuard` panic / timeout；这反映的是上游 runtime 生命周期问题，不是本次移除 proto 与 fallback 后的新编译缺口。
