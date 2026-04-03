# PreCheck Verbose Logging And Encrypted Audit Plan

## 任务目标 / Goal

- 为 `PreCheck` 增加完整流程日志，覆盖关键阶段输入、LLM 判断、检索构造、候选筛选、二层评审、最终注入与降级路径。
- 在不开启明文调试详情时，提供受保护的日志记录方式，避免长期只剩长度/摘要而失去排障依据。
- 明确哪些字段记录明文，哪些字段记录加密载荷，并保持默认安全行为不回退。

## 执行步骤 / Steps

1. 审计当前 `PreCheck` 日志点、共享日志开关和 `logx` 能力，确认现有明文/安全输出边界。
2. 设计并实现 `PreCheck` 关键阶段的结构化日志。
3. 设计并实现“详情关闭时的受保护载荷记录”机制，优先保证默认安全、可配置开启、不会破坏现有日志格式。
4. 为新日志行为补充测试，覆盖：
   - debug 开启时的完整阶段日志
   - debug 关闭且保护开关开启时的密文/受保护字段日志
   - debug 关闭且未配置保护时的安全摘要日志
5. 同步更新 README/相关文档与计划结论。
6. 运行规定测试与标准构建。
7. 完成后将计划文件改名为 `COMPLETED_` 前缀。

## 技术原则 / Technical Principles

- 默认行为必须继续以安全输出为准，不能因为增加排障信息而回退到明文泄露。
- 调试明文与受保护载荷必须共用统一开关体系，避免 `PreCheck` 单独走野路子。
- 仅记录对排障有帮助的阶段信息，不做无边界日志膨胀。

## 验收标准 / Acceptance Criteria

- `PreCheck` 关键阶段具备可读的流程日志。
- 明文调试关闭时，存在可配置的受保护载荷记录能力。
- 默认配置仍保持安全摘要输出。
- 已补充测试并通过：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config ./internal/platform/logx -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`

## 实际判断 / Actual Findings

1. `PreCheck` 之前只有零散的降级日志，缺少最近 turn 准备、LLM intent 结果、检索 query 构造、候选召回、二层 reviewer 选择、回写完成、最终返回等完整阶段日志。
2. 现有 `logging.debug_rpc_payloads` 只能解决“是否打印明文”的问题，默认安全模式下并没有可供事后排障的受保护载荷留痕能力，这个缺口真实存在。
3. `server.go` 里的 `PreCheck` 收据日志已经切到共享 logger 载荷能力后，若直接用 `Dependencies.DebugRPCPayloads=true` 但不显式注入 logger，默认 logger 不会继承该开关，直调场景会和标准运行时表现不一致；这个兼容性问题真实存在。
4. `PreCheck` 第二层 reviewer 的 `Reason` 虽然真实返回，但原始阶段日志没有带出“为什么选中这些候选”，会让“完整分析日志”缺一层关键解释；这个信息缺口真实存在。

## 实际修改 / Actual Changes

### 1. 共享日志保护能力

- 在 `internal/platform/logx/logger.go` 新增受保护载荷能力：
  - `ProtectPayloads`
  - `PayloadEncryptionKey`
  - `PayloadProtectionEnabled()`
  - `AppendPayloadFields(...)`
- 新增 `internal/platform/logx/payload_protection.go`：
  - 使用 `AES-256-GCM` 封装 payload
  - 统一输出 `version / algorithm / key_id / ciphertext` JSON 信封
  - 支持 32 字节原始字符串、64 位 hex 和 base64 三种密钥格式

### 2. 配置与运行时装配

- 在 `internal/config/config.go` 增加：
  - `logging.protect_payloads`
  - `logging.payload_encryption_key`
  - `VMM_LOG_PROTECT_PAYLOADS`
  - `VMM_LOG_PAYLOAD_ENCRYPTION_KEY`
- 启动校验会在 `protect_payloads=true` 时强制校验密钥格式，避免运行时静默失效。
- 在 `internal/app/app.go` 把新日志配置装配进全局 logger。

### 3. PreCheck 完整流程日志

- 在 `internal/app/usecase/precheck.go` 为以下阶段新增结构化日志：
  - `pre-check recent turns prepared`
  - `pre-check intent analyzed`
  - `pre-check memory query prepared`
  - `pre-check memory candidates recalled`
  - `pre-check memory candidates reviewed`
  - `pre-check lifecycle write-back completed`
  - `pre-check finalized`
- 每个阶段都带：
  - 安全摘要字段
  - debug 模式下的明文 payload
  - 保护模式下的 `stage_payload_protected`
- reviewer 阶段额外补入了 `review_reason`，让日志能解释“为何采纳这些记忆”。

### 4. gRPC 适配层 PreCheck 收据日志

- 在 `internal/adapters/inbound/grpcapi/server.go` 中：
  - `pre-check received`
  - `pre-check returned`
  改为统一使用共享 logger 的 payload 追加能力。
- 默认只保留安全摘要；debug 开启时输出 `request_payload` / `response_payload`；保护模式开启时输出 `request_payload_protected` / `response_payload_protected`。
- 修正了 `NewServer(...)` 在未显式注入 logger 时的默认 logger 创建逻辑，让 `Dependencies.DebugRPCPayloads` 仍能影响直调场景。

### 5. 测试与文档

- 新增/更新测试：
  - `internal/platform/logx/logger_test.go`
  - `internal/config/config_test.go`
  - `internal/app/usecase/precheck_test.go`
  - `internal/adapters/inbound/grpcapi/server_test.go`
- 文档与示例配置同步更新：
  - `README.md`
  - `docs/post-action-guide_CN.md`
  - `configs/openai.local.example.json`

## 验证结果 / Verification Results

- 已通过：
  - `go test ./internal/platform/logx ./internal/app/usecase ./internal/adapters/inbound/grpcapi ./internal/config -count=1`
  - `go test ./internal/app/usecase ./internal/adapters/inbound/grpcapi -count=1`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config ./internal/platform/logx -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`

## 完成状态 / Completion Status

- 计划内目标已全部完成。
- 默认行为仍保持安全摘要输出。
- 开启 `logging.debug_rpc_payloads=true` 后，`PreCheck` 会输出完整阶段诊断与请求/返回 payload。
- 在 `logging.debug_rpc_payloads=false` 且 `logging.protect_payloads=true` 时，相关 payload 会以加密 JSON 信封形式写入日志，满足“默认安全 + 可追溯”的双目标。
