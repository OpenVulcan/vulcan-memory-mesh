// errors.go implements the inbound gRPC adapter error mapping.
// errors.go 用于实现入站 gRPC 适配层的错误映射。
package grpcapi

import (
	"context"
	"errors"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrorDescriptor is the normalized gRPC error catalog entry returned to clients as status details.
// ErrorDescriptor 用于表示通过 gRPC 状态详情返回给客户端的标准化错误目录项。
type ErrorDescriptor struct {
	Code     codes.Code
	ID       string
	Category string
	Message  string
}

// Variables expose reusable gRPC error descriptors so every RPC returns stable IDs.
// Variables 用于暴露可复用的 gRPC 错误描述，保证所有 RPC 返回稳定的错误 ID。
var (
	errInvalidArgument = ErrorDescriptor{Code: codes.InvalidArgument, ID: "GRPC_VALIDATION_FAILED", Category: "validation", Message: "request validation failed"}
	errRouteDisabled   = ErrorDescriptor{Code: codes.Unimplemented, ID: "GRPC_ROUTE_DISABLED", Category: "routing", Message: "rpc is disabled"}
	errNotFound        = ErrorDescriptor{Code: codes.NotFound, ID: "RESOURCE_NOT_FOUND", Category: "lookup", Message: "requested resource was not found"}
	errConflict        = ErrorDescriptor{Code: codes.AlreadyExists, ID: "RESOURCE_CONFLICT", Category: "conflict", Message: "requested resource conflicts with existing data"}
	errConfirmation    = ErrorDescriptor{Code: codes.FailedPrecondition, ID: "CONFIRMATION_REQUIRED", Category: "confirmation", Message: "explicit confirmation is required"}
	errOutcomeUnknown  = ErrorDescriptor{Code: codes.Aborted, ID: "STORAGE_OUTCOME_UNCERTAIN", Category: "storage", Message: "storage outcome is uncertain"}
	errTimeout         = ErrorDescriptor{Code: codes.DeadlineExceeded, ID: "UPSTREAM_TIMEOUT", Category: "timeout", Message: "request timeout"}
	errInternal        = ErrorDescriptor{Code: codes.Internal, ID: "INTERNAL_ERROR", Category: "internal", Message: "internal server error"}
	errTooLarge        = ErrorDescriptor{Code: codes.ResourceExhausted, ID: "GRPC_REQUEST_TOO_LARGE", Category: "transport", Message: "request payload exceeds size limit"}
)

// describeError converts domain and runtime failures into a stable gRPC status descriptor.
// describeError 用于把领域层和运行时失败转换成稳定的 gRPC 状态描述。
func describeError(err error) ErrorDescriptor {
	switch {
	case err == nil:
		return ErrorDescriptor{}
	case logicdomain.IsValidationError(err):
		return withMessage(errInvalidArgument, err.Error())
	case logicdomain.IsNotFoundError(err):
		return withMessage(errNotFound, err.Error())
	case logicdomain.IsConflictError(err):
		return withMessage(errConflict, err.Error())
	case logicdomain.IsConfirmationRequired(err):
		return withMessage(errConfirmation, err.Error())
	case logicdomain.IsOutcomeUncertain(err):
		return withMessage(errOutcomeUnknown, err.Error())
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, logicdomain.ErrTimeout):
		return errTimeout
	default:
		if strings.Contains(strings.ToLower(err.Error()), "received message larger than max") {
			return errTooLarge
		}
		return withMessage(errInternal, sanitizeErrorMessage(err.Error()))
	}
}

// toStatus converts one error descriptor into a gRPC status with ErrorInfo metadata.
// toStatus 用于把单个错误描述转换成携带 ErrorInfo 元数据的 gRPC 状态。
func toStatus(desc ErrorDescriptor) error {
	if desc.Code == codes.OK {
		return nil
	}
	st := status.New(desc.Code, sanitizeErrorMessage(desc.Message))
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: desc.ID,
		Domain: "vmm.grpc",
		Metadata: map[string]string{
			"category": desc.Category,
		},
	})
	if err == nil {
		return withDetails.Err()
	}
	return st.Err()
}

// withMessage clones one descriptor with a caller-provided safe message.
// withMessage 用于复制错误描述并带上调用方提供的安全消息。
func withMessage(base ErrorDescriptor, msg string) ErrorDescriptor {
	msg = sanitizeErrorMessage(msg)
	if msg == "" {
		return base
	}
	base.Message = msg
	return base
}

// sanitizeErrorMessage removes empty or whitespace-only error text before it reaches clients.
// sanitizeErrorMessage 用于在错误文本返回给客户端前去掉空值或纯空白内容。
func sanitizeErrorMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "internal server error"
	}
	return msg
}
