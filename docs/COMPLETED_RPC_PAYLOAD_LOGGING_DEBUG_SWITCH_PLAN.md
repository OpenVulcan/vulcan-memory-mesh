# RPC Payload Logging Debug Switch Plan

## 任务目标 / Goal

- 为 `PreCheck` 和 `PostAction` 载荷日志增加一个统一的显式配置开关。
- Add one explicit shared configuration switch for `PreCheck` and `PostAction` payload logging.
- 默认保持当前安全日志输出，不记录原始正文。
- Keep the current secure logging behavior as the default and avoid recording raw payload text.
- 当明确开启调试开关时，允许输出用于排障的更详细内容。
- Allow more detailed debug-oriented payload output only when the debug switch is explicitly enabled.

## 执行步骤 / Steps

1. 审查当前 `PreCheck` / `PostAction` 日志、配置结构、默认配置文件和文档。
2. 设计一个默认关闭的统一日志调试开关，并把它接入 gRPC `Server`。
3. 在日志函数中按开关分流：
   - 默认：安全输出
   - 开启：输出调试信息
4. 为 `PreCheck` 补齐请求与返回日志，并与 `PostAction` 共用同一个开关。
5. 补充回归测试，覆盖默认安全模式和显式调试模式。
6. 同步更新配置示例和说明文档。
7. 运行规定测试与构建，完成后提交。

## 技术原则 / Technical Principles

- 默认行为必须保持安全，不因新增调试能力而扩大默认泄露面。
- The debug path must be opt-in and explicit.
- 调试输出应服务于本地排障，不改变主业务链路和外部 gRPC 契约。
- 配置命名应能准确反映其同时控制 `PreCheck` 与 `PostAction`，避免留下只覆盖单条链路的误导性字段名。

## 验收标准 / Acceptance Criteria

- 新增一个默认关闭的配置项。
- 默认模式下 `PreCheck` / `PostAction` 日志继续保持安全输出。
- 开启模式下 `PreCheck` / `PostAction` 都能输出更详细的调试内容。
- 测试、文档和配置示例同步完成。
- 计划文件完成后改名为 `COMPLETED_` 前缀。

## 实际结果 / Actual Outcome

- 已把原先仅面向 `PostAction` 的临时字段名收口为统一配置：
  - `logging.debug_rpc_payloads`
  - 环境变量：`VMM_LOG_DEBUG_RPC_PAYLOADS`
- 默认值保持 `false`，因此当前运行时仍然默认走安全日志输出。
- `PostAction`：
  - 默认模式下继续只记录存在性、计数和结构化安全元信息。
  - 调试模式下会输出 `post-action received raw` / `post-action received cleaned` 的完整正文和 `timeline_json`。
- `PreCheck`：
  - 新增 `pre-check received` 和 `pre-check returned` 两类日志。
  - 默认模式下只记录存在性、计数、`should_inject`、`degraded` 等安全元信息。
  - 调试模式下会输出完整 `user_content`、`context_text` 和 `context_items_json`。

## 实际修改文件 / Files Changed

- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/app/app.go`
- `internal/adapters/inbound/grpcapi/server.go`
- `internal/adapters/inbound/grpcapi/server_test.go`
- `configs/local.json`
- `configs/openai.local.example.json`
- `README.md`
- `docs/post-action-guide_CN.md`

## 验证结果 / Verification

- `go test ./internal/adapters/inbound/grpcapi -count=1`
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
- `go test ./... -count=1`
- `.\make.ps1 build`

以上验证均已通过。
