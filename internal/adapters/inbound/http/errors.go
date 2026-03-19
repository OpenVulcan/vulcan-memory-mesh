// errors.go implements the inbound HTTP adapter layer.
// errors.go 用于实现入站 HTTP 适配层。
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// ErrorDescriptor is the normalized HTTP error catalog entry returned to clients.
// ErrorDescriptor 用于表示返回给客户端的标准化 HTTP 错误目录项。
type ErrorDescriptor struct {
	Status   int
	ID       string
	Category string
	Message  string
}

// Variables expose reusable HTTP error descriptors so every handler returns stable IDs.
// Variables 用于暴露可复用的 HTTP 错误描述，保证所有处理器返回稳定的错误 ID。
var (
	errNotFound         = ErrorDescriptor{Status: http.StatusNotFound, ID: "HTTP_NOT_FOUND", Category: "routing", Message: "not found"}
	errMethodNotAllowed = ErrorDescriptor{Status: http.StatusMethodNotAllowed, ID: "HTTP_METHOD_NOT_ALLOWED", Category: "routing", Message: "method not allowed"}
	errInvalidJSON      = ErrorDescriptor{Status: http.StatusBadRequest, ID: "HTTP_INVALID_JSON", Category: "transport", Message: "invalid json request body"}
	errValidation       = ErrorDescriptor{Status: http.StatusBadRequest, ID: "HTTP_VALIDATION_FAILED", Category: "validation", Message: "request validation failed"}
	errRouteDisabled    = ErrorDescriptor{Status: http.StatusForbidden, ID: "HTTP_ROUTE_DISABLED", Category: "routing", Message: "route is disabled"}
	errRequestTooLarge  = ErrorDescriptor{Status: http.StatusRequestEntityTooLarge, ID: "HTTP_REQUEST_TOO_LARGE", Category: "transport", Message: "request body exceeds size limit"}
	errTimeout          = ErrorDescriptor{Status: http.StatusGatewayTimeout, ID: "UPSTREAM_TIMEOUT", Category: "timeout", Message: "request timeout"}
	errInternal         = ErrorDescriptor{Status: http.StatusInternalServerError, ID: "INTERNAL_ERROR", Category: "internal", Message: "internal server error"}
)

// describeError maps transport, domain, and runtime failures into one catalog entry.
// describeError 用于把传输层、领域层和运行时失败映射到统一错误目录项。
func describeError(err error) ErrorDescriptor {
	var maxErr *http.MaxBytesError
	switch {
	case err == nil:
		return ErrorDescriptor{Status: http.StatusOK}
	case errors.As(err, &maxErr):
		return errRequestTooLarge
	case logicdomain.IsValidationError(err):
		return withMessage(errValidation, err.Error())
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, logicdomain.ErrTimeout):
		return errTimeout
	default:
		return withMessage(errInternal, sanitizeErrorMessage(err.Error()))
	}
}

// describeDecodeError specializes JSON transport failures before they reach use-case validation.
// describeDecodeError 用于在进入用例层校验前对 JSON 传输错误做专门映射。
func describeDecodeError(err error) ErrorDescriptor {
	var maxErr *http.MaxBytesError
	switch {
	case err == nil:
		return ErrorDescriptor{Status: http.StatusOK}
	case errors.As(err, &maxErr):
		return errRequestTooLarge
	default:
		return withMessage(errInvalidJSON, sanitizeErrorMessage(err.Error()))
	}
}

// writeErrorDescriptor serializes one normalized error envelope back to the HTTP client.
// writeErrorDescriptor 用于把标准化错误响应包写回 HTTP 客户端。
func writeErrorDescriptor(w http.ResponseWriter, traceID string, desc ErrorDescriptor) {
	writeJSON(w, desc.Status, Envelope{
		Code:          desc.Status,
		Msg:           desc.Message,
		ErrorID:       desc.ID,
		ErrorCategory: desc.Category,
		TraceID:       traceID,
	})
}

// withMessage clones one descriptor with a caller-specified message when extra detail is safe to expose.
// withMessage 用于在可安全暴露更多细节时复制错误描述并替换消息文本。
func withMessage(base ErrorDescriptor, msg string) ErrorDescriptor {
	msg = sanitizeErrorMessage(msg)
	if msg == "" {
		return base
	}
	base.Message = msg
	return base
}

// formatValidationError converts validator and domain validation failures into one concise field message.
// formatValidationError 用于把校验库和领域层校验失败转换成简洁的字段消息。
func formatValidationError(field, message string) string {
	field = sanitizeErrorMessage(field)
	message = sanitizeErrorMessage(message)
	if field == "" {
		return message
	}
	if message == "" {
		return field
	}
	return fmt.Sprintf("%s: %s", field, message)
}
