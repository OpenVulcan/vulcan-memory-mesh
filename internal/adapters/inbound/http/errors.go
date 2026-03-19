// errors.go implements the inbound HTTP adapter layer.
// errors.go 用于实现入站 HTTP 适配层。
package httpapi

import (
	"context"
	"errors"
	"net/http"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// mapStatus maps values into the target shape.
// mapStatus 用于将值映射到目标结构。
func mapStatus(err error) (int, string) {
	switch {
	case err == nil:
		return http.StatusOK, "ok"
	case logicdomain.IsValidationError(err):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, logicdomain.ErrTimeout):
		return http.StatusGatewayTimeout, "request timeout"
	default:
		return http.StatusInternalServerError, err.Error()
	}
}
