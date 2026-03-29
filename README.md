# VMM OSS Local (Go)

VulcanMemoryMesh 当前主线只保留本地版、gRPC 版和两条核心业务链：

- `PreCheck`
- `PostAction`

当前运行时的定位是：

- 用 `project_id + user_id + session_id` 做确定性层级寻址
- 用 DuckDB 保存层级、session、turn 记录与长期 SQL 数据
- 用 LanceDB 保存向量数据
- 由 Caddy 等外部反向代理负责 TLS

## 文档导航

- [层级数据模型与 gRPC 设计（中文）](./docs/hierarchy-grpc-design_CN.md)
- [gRPC 对接说明（中文）](./docs/grpc-integration-guide_CN.md)
- [gRPC 接口测试说明（中文）](./docs/api-test-guide_CN.md)
- [post-action 接口说明（中文）](./docs/post-action-guide_CN.md)
- [记忆准入噪声门说明（中文）](./docs/noise-gate-guide_CN.md)
- [DuckDB Schema 版本管理说明（中文）](./docs/duckdb-schema-versioning_CN.md)
- [后续记忆提炼与画像合并分析（非决案，中文）](./docs/memory-extraction-analysis_CN.md)

## 当前运行模型

### 入站协议

当前只暴露 gRPC：

- 服务：`vmm.v1.VMMService`

当前主线方法：

- `Healthz`
- `ListProjects`
- `ResolveProject`
- `EnsureProject`
- `DeleteProject`
- `MigrateProject`
- `ResolveUser`
- `ListUsers`
- `DeleteUser`
- `PreCheck`
- `PostAction`

### 数据后端

当前只保留两条本地数据线：

- DuckDB：层级、用户、session、turn 记录、长期 SQL 记录
- LanceDB：向量写入、检索和删除

运行时已经移除：

- HTTP 服务
- 应用内 TLS
- SQLite 运行时支持
- 内存关系库存根 / 内存向量库存根回退

## 核心约束

### 业务入参

`PreCheck` 和 `PostAction` 现在只接受：

- `session_id`
- `user_id`
- `project_id`

说明：

- `user_id`、`project_id` 必须是数字 ID
- `team_id`、`space_id` 不再由客户端传入
- 服务端会先解析 `project_id -> space_id -> team_id`
- 如果 `session_id` 不存在，会自动在 `vmm_sessions` 中创建一条 session 记录

### PreCheck

当前 `PreCheck` 仍然是保守模式：

- 完成请求校验与范围解析
- 固定返回 `should_inject = false`
- 不做真正的记忆注入

### PostAction

当前 `PostAction` 是新的文本契约入口，只接受：

- `user_content`
- `assistant_content`
- `timeline[]`

服务端流程是：

1. 先解析 `session_id / user_id / project_id`
2. 记录原始日志
3. 清洗 `user_content`、`timeline[].content`、`assistant_content`
4. 记录清洗后日志
5. 立即返回 `accepted=true`
6. 后台继续：
   - 按 `user / timeline / assistant` 组装一条脱水 turn 记录
   - 写入 `vmm_turn_records`
   - 当 `timeline` 为空时，先过 `NoiseGate`

## 构建与运行

标准构建入口只有：

```powershell
.\make.ps1 build
```

或：

```powershell
.\make.bat build
```

标准运行产物：

- `output/bin/vmm-local.exe`

标准配置目录：

- `output/configs/`

### 启动示例

```powershell
.\make.bat build
.\output\bin\vmm-local.exe
```

### 用户覆盖目录

```powershell
.\output\bin\vmm-local.exe -config ~/.vmm
```

`-config` 表示“覆盖根目录”，不是单个配置文件路径。

### 调试清库

需要调试时，可以继续通过 `make` 调主程序，但把清理目标通过 `--debug-clean` 传给二进制：

```powershell
.\make.bat run --debug-clean duckdb
.\make.bat run --debug-clean lancedb
.\make.bat run --debug-clean all
```

说明：

- `make` 只负责透传参数，不在脚本里直接做数据库清理
- `vmm-local --debug-clean ...` 会只连接对应的 DuckDB / LanceDB gRPC 网关
- 清理完成后立即退出，不会启动 VMM gRPC 服务

## 配置说明

当前关键配置项：

- `grpc.listen_addr`
- `grpc.max_receive_message_bytes`
- `grpc.request_timeout.workspace`
- `grpc.request_timeout.pre_check`
- `grpc.request_timeout.post_action`
- `duckdb.address`
- `lancedb.address`
- `lancedb.table_name`
- `lancedb.vector_column`
- `llm.*`
- `embedding.*`
- `noise.*`
- `post_action.input_mode`

### 超时模型

- `grpc.request_timeout.pre_check`
  - 表示整次 `PreCheck` RPC 的外层预算
- `pre_check.intent_timeout`
  - 表示未来恢复意图提取后，内部子步骤的预算

当前要求：

- `grpc.request_timeout.pre_check > pre_check.intent_timeout`

### LanceDB 表名规则

`lancedb.table_name` 现在是基础表名。

运行时会自动追加 embedding 维度：

- 基础表名：`vmm_memory_vectors`
- 维度：`1024`
- 实际表名：`vmm_memory_vectors_1024`

这样可以避免不同向量维度共用同一张物理表。

## 开发验证

提交较大改动前，至少执行：

```powershell
go test ./... -count=1
.\make.bat build
```

## TLS 说明

当前应用内不再支持 TLS。

如果需要 TLS、域名或公网入口，请在前面使用 [Caddy](https://caddyserver.com/) 做反向代理。
