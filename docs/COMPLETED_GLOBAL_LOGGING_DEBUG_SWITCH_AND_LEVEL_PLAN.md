# Global Logging Debug Switch And Level Plan

## 任务目标 / Goal

- 评估并扩展当前统一 RPC 调试开关，使其覆盖项目中适合在调试模式下输出更完整信息的日志点。
- Verify whether runtime log-level control already exists, and implement any missing pieces needed so release environments can run with error-only logging.
- 保持默认安全输出，不因调试能力扩大默认泄露面。

## 执行步骤 / Steps

1. 审查当前日志配置、`logx` 能力和主要业务链日志点。
2. 判断“日志级别设定”是否已真实存在；若不足，则补齐并验证。
3. 识别适合接入统一调试载荷开关的日志位置，并避免把不必要的普通错误日志一起放大。
4. 实现配置接线与日志分流。
5. 补充回归测试和必要文档更新。
6. 运行规定测试与构建。
7. 完成后重命名计划文件并提交。

## 技术原则 / Technical Principles

- 默认行为必须保持安全、克制、可用于生产。
- Debug payload logging must stay explicit and opt-in.
- 日志级别控制必须真实生效，而不是只存在配置字段。
- 只有在调试模式下确有价值的日志点才接入详细输出，避免制造噪声。

## 验收标准 / Acceptance Criteria

- 明确记录“现有日志级别能力是否已存在、是否需要补改”。
- 统一调试开关扩展到合适的日志输出点。
- release 环境可通过配置做到只显示 `error` 级日志。
- 测试、文档、配置说明同步完成。
- 计划文件完成后改名为 `COMPLETED_` 前缀。

## 实际判断 / Actual Findings

- `logging.level` 的底层运行时能力原本就真实存在：
  - `internal/platform/logx/logger.go` 已支持 `debug/info/warn/error`
  - `internal/app/app.go` 也已把 `cfg.Logging.Level` 接到运行时 logger
- 但这项能力此前并不完整：
  - `Config.Validate()` 没有显式校验 `logging.level`
  - 文档没有明确说明 release 可以把日志级别设成 `error`
- 因此本轮对“日志级别设定”的结论是：
  - 不是从零新增能力
  - 但确实需要补齐“配置校验 + 测试 + 文档”，让它成为可正式依赖的能力

## 实际修改 / Actual Changes

- 把共享 payload 调试能力下沉到 logger：
  - `internal/platform/logx/logger.go`
  - 新增 `Config.DebugPayloads`
  - 新增 `Logger.PayloadDebugEnabled()`
- 扩展调试开关覆盖范围：
  - `internal/app/usecase/memory_query.go`
    - 默认记录 `query_len/query_sha256`
    - 调试模式下改为输出完整 `query`
  - `internal/app/usecase/postaction.go`
    - 默认记录 `analysis_len/analysis_sha256`
    - 调试模式下改为输出完整 `analysis_json`
  - `internal/app/usecase/precheck.go`
    - 默认降级日志保持安全字段
    - 调试模式下补充输出完整 `user_content`
- 补齐日志级别的正式契约：
  - `internal/config/config.go`
  - `logging.level` 现在显式只接受 `debug/info/warn/error`

## 实际修改文件 / Files Changed

- `internal/platform/logx/logger.go`
- `internal/platform/logx/logger_test.go`
- `internal/app/app.go`
- `internal/app/usecase/memory_query.go`
- `internal/app/usecase/memory_query_test.go`
- `internal/app/usecase/postaction.go`
- `internal/app/usecase/postaction_test.go`
- `internal/app/usecase/precheck.go`
- `internal/app/usecase/precheck_test.go`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `README.md`
- `docs/post-action-guide_CN.md`

## 验证结果 / Verification

- `go test ./internal/platform/logx ./internal/app/usecase ./internal/config -count=1`
- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config ./internal/platform/logx -count=1`
- `go test ./... -count=1`
- `.\make.ps1 build`

以上验证均已通过。
