package httpapi

import (
	"context"
	"errors"
	nethttp "net/http"

	"github.com/openvulcan/vmm/internal/core/domain"
)

func mapStatus(err error) (int, string) {
	switch {
	case err == nil:
		return nethttp.StatusOK, "ok"
	case domain.IsValidationError(err):
		return nethttp.StatusBadRequest, err.Error()
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, domain.ErrTimeout):
		return nethttp.StatusGatewayTimeout, "request timeout"
	default:
		return nethttp.StatusInternalServerError, err.Error()
	}
}
