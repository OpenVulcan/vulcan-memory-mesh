# VMM HTTP/TLS/Validation Test Report (2026-03-20)
# VMM HTTP/TLS/校验测试报告（2026-03-20）

## Summary
## 摘要

- Added first-class TLS support to the local runtime so the server can run with direct HTTPS when deployed on a host.
- 为本地运行时增加了原生 TLS 支持，使服务在部署到服务器时可以直接启用 HTTPS。
- Added a hard request-body size limit with a default of 1 MB and early rejection before JSON decoding.
- 增加了默认 1MB 的请求包大小硬限制，并在 JSON 解析前提前拒绝超限请求。
- Standardized HTTP error responses with stable `error_id` and `error_category` fields.
- 将 HTTP 错误返回标准化，增加稳定的 `error_id` 和 `error_category` 字段。
- Introduced a lightweight structured logger and a lightweight validation library for request cleaning and input validation.
- 引入了轻量结构化日志组件和轻量校验库，用于请求清洗和输入校验。

## Code Changes
## 代码改动

- TLS, body-limit, and logging configuration were added in [config.go](/D:/projects/VulcanMemoryMesh/internal/config/config.go).
- TLS、包体限制和日志配置已加入 [config.go](/D:/projects/VulcanMemoryMesh/internal/config/config.go)。
- Structured logger implementation was added in [logger.go](/D:/projects/VulcanMemoryMesh/internal/platform/logx/logger.go).
- 结构化日志实现新增于 [logger.go](/D:/projects/VulcanMemoryMesh/internal/platform/logx/logger.go)。
- HTTP request limiting and request logging were updated in [middleware.go](/D:/projects/VulcanMemoryMesh/internal/adapters/inbound/http/middleware.go).
- HTTP 请求限制与请求日志更新在 [middleware.go](/D:/projects/VulcanMemoryMesh/internal/adapters/inbound/http/middleware.go)。
- Unified HTTP error catalog was added in [errors.go](/D:/projects/VulcanMemoryMesh/internal/adapters/inbound/http/errors.go).
- 统一的 HTTP 错误目录新增于 [errors.go](/D:/projects/VulcanMemoryMesh/internal/adapters/inbound/http/errors.go)。
- DTO-level validation and normalization were added in [validation.go](/D:/projects/VulcanMemoryMesh/internal/adapters/inbound/http/validation.go) and [dto.go](/D:/projects/VulcanMemoryMesh/internal/adapters/inbound/http/dto.go).
- DTO 级校验和归一化新增于 [validation.go](/D:/projects/VulcanMemoryMesh/internal/adapters/inbound/http/validation.go) 和 [dto.go](/D:/projects/VulcanMemoryMesh/internal/adapters/inbound/http/dto.go)。
- Handlers and router were updated to use the new logger, validator, error catalog, and body-limit middleware in [handlers.go](/D:/projects/VulcanMemoryMesh/internal/adapters/inbound/http/handlers.go) and [router.go](/D:/projects/VulcanMemoryMesh/internal/adapters/inbound/http/router.go).
- Handler 和路由已在 [handlers.go](/D:/projects/VulcanMemoryMesh/internal/adapters/inbound/http/handlers.go) 与 [router.go](/D:/projects/VulcanMemoryMesh/internal/adapters/inbound/http/router.go) 中切换到新的日志、校验、错误目录和包体限制中间件。
- Application startup was updated to support HTTPS startup and structured logging in [app.go](/D:/projects/VulcanMemoryMesh/internal/app/app.go).
- 应用启动流程已在 [app.go](/D:/projects/VulcanMemoryMesh/internal/app/app.go) 中支持 HTTPS 启动和结构化日志。
- Example configuration files were updated in [local.json](/D:/projects/VulcanMemoryMesh/configs/local.json), [openai.local.example.json](/D:/projects/VulcanMemoryMesh/configs/openai.local.example.json), and [.env.example](/D:/projects/VulcanMemoryMesh/configs/.env.example).
- 示例配置文件已更新于 [local.json](/D:/projects/VulcanMemoryMesh/configs/local.json)、[openai.local.example.json](/D:/projects/VulcanMemoryMesh/configs/openai.local.example.json) 和 [.env.example](/D:/projects/VulcanMemoryMesh/configs/.env.example)。

## Verification
## 验证结果

- `go test ./...` passed.
- `go test ./...` 已通过。
- `.\make.ps1 build` passed and produced the runtime layout under `output/bin` and `output/configs`.
- `.\make.ps1 build` 已通过，并生成了 `output/bin` 与 `output/configs` 运行目录结构。
- HTTP runtime checks passed:
- HTTP 运行时检查已通过：
  - `GET /healthz` returned `200`.
  - `GET /healthz` 返回 `200`。
  - `POST /v1/admin/seed-memory` returned `200`.
  - `POST /v1/admin/seed-memory` 返回 `200`。
- `POST /vmm/post-action` returned `200`.
- `POST /vmm/post-action` 返回 `200`。
- Oversized `POST /vmm/pre-check` returned `413` with `error_id=HTTP_REQUEST_TOO_LARGE`.
- 超大 `POST /vmm/pre-check` 返回 `413`，并带有 `error_id=HTTP_REQUEST_TOO_LARGE`。
- HTTPS runtime checks passed with a self-signed certificate:
- 使用自签证书的 HTTPS 运行时检查已通过：
  - `GET https://127.0.0.1:18443/healthz` returned `200`.
  - `GET https://127.0.0.1:18443/healthz` 返回 `200`。
- `POST https://127.0.0.1:18443/vmm/pre-check` returned `200`.
- `POST https://127.0.0.1:18443/vmm/pre-check` 返回 `200`。

## Observations
## 观察结论

- The TLS startup path is working correctly when `http.tls.enabled=true` and valid `cert_file` / `key_file` are provided.
- 当 `http.tls.enabled=true` 且提供有效 `cert_file` / `key_file` 时，TLS 启动链路工作正常。
- Request-size rejection happens before handler-level JSON parsing, which protects the runtime from oversized payload decoding.
- 请求大小限制发生在 handler 级 JSON 解析之前，可以避免超大载荷进入解码流程。
- The new structured logger makes filtering by `trace_id`, `path`, `status`, and `request_body` much easier than the previous plain-text logger.
- 新的结构化日志相比之前的纯文本日志，更方便按 `trace_id`、`path`、`status` 和 `request_body` 做过滤。
- Mock embedding is still too weak for realistic multilingual semantic recall, so successful HTTP/HTTPS transport does not guarantee realistic memory recall quality.
- mock embedding 对真实多语言语义召回仍然偏弱，因此 HTTP/HTTPS 传输成功不等于记忆召回质量已经足够真实。

## Remaining Notes
## 余留说明

- The current runtime supports direct HTTPS, but certificate issuance and rotation are still external responsibilities.
- 当前运行时已支持直接 HTTPS，但证书签发和轮换仍需由外部负责。
- For public deployment, a reverse proxy can still be preferable even though direct TLS now works.
- 对外网部署来说，虽然程序内 TLS 已可用，反向代理依然可能是更稳妥的选择。
