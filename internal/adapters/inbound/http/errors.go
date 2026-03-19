package httpapi

import (
	"context"
	"errors"
	"net/http"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

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
