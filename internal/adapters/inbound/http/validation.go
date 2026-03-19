// validation.go implements the inbound HTTP adapter layer.
// validation.go 用于实现入站 HTTP 适配层。
package httpapi

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// newValidator builds the shared DTO validator used by HTTP handlers.
// newValidator 用于构建 HTTP 处理器共享的 DTO 校验器。
func newValidator() *validator.Validate {
	validate := validator.New(validator.WithRequiredStructEnabled())
	validate.RegisterTagNameFunc(func(field reflect.StructField) string {
		name := strings.SplitN(field.Tag.Get("json"), ",", 2)[0]
		if name == "-" {
			return ""
		}
		return name
	})
	return validate
}

// normalizePreCheckRequest trims transport strings before validation and use-case mapping.
// normalizePreCheckRequest 用于在校验和映射到用例前清理 pre-check 传输层字符串。
func normalizePreCheckRequest(req *PreCheckRequestDTO) {
	if req == nil {
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.UserID = strings.TrimSpace(req.UserID)
	req.TeamID = strings.TrimSpace(req.TeamID)
	req.SpaceID = strings.TrimSpace(req.SpaceID)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.CurrentContent = strings.TrimSpace(req.CurrentContent)
	for i := range req.HistoryContent {
		req.HistoryContent[i].Role = strings.ToLower(strings.TrimSpace(req.HistoryContent[i].Role))
		req.HistoryContent[i].Content = strings.TrimSpace(req.HistoryContent[i].Content)
	}
}

// normalizePostActionRequest trims transport strings before normalization and persistence.
// normalizePostActionRequest 用于在清洗和持久化之前裁剪 post-action 传输层字符串。
func normalizePostActionRequest(req *PostActionRequestDTO) {
	if req == nil {
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.UserID = strings.TrimSpace(req.UserID)
	req.TeamID = strings.TrimSpace(req.TeamID)
	req.SpaceID = strings.TrimSpace(req.SpaceID)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	for i := range req.RawMessagesSnapshot {
		req.RawMessagesSnapshot[i].Role = strings.ToLower(strings.TrimSpace(req.RawMessagesSnapshot[i].Role))
		for j := range req.RawMessagesSnapshot[i].ToolCalls {
			req.RawMessagesSnapshot[i].ToolCalls[j].ID = strings.TrimSpace(req.RawMessagesSnapshot[i].ToolCalls[j].ID)
			req.RawMessagesSnapshot[i].ToolCalls[j].Type = strings.TrimSpace(req.RawMessagesSnapshot[i].ToolCalls[j].Type)
		}
	}
}

// normalizeSeedMemoryRequest trims transport strings before the seed-memory use case runs.
// normalizeSeedMemoryRequest 用于在执行 seed-memory 用例之前裁剪传输层字符串。
func normalizeSeedMemoryRequest(req *SeedMemoryRequestDTO) {
	if req == nil {
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.MemoryText = strings.TrimSpace(req.MemoryText)
	req.SpaceID = strings.TrimSpace(req.SpaceID)
}

// validateStruct converts validator errors into domain validation errors with JSON field names.
// validateStruct 用于把校验库错误转换成带 JSON 字段名的领域校验错误。
func validateStruct(validate *validator.Validate, req any) error {
	if validate == nil {
		return nil
	}
	if err := validate.Struct(req); err != nil {
		var validationErrors validator.ValidationErrors
		if errors.As(err, &validationErrors) && len(validationErrors) > 0 {
			first := validationErrors[0]
			return logicdomain.ValidationError{
				Field:   first.Field(),
				Message: validatorMessage(first),
			}
		}
		return logicdomain.ValidationError{Field: "request", Message: sanitizeErrorMessage(err.Error())}
	}
	return nil
}

// validatorMessage translates one validator field error into a client-facing message.
// validatorMessage 用于把单个 validator 字段错误翻译成面向客户端的消息。
func validatorMessage(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "is required"
	case "oneof":
		return fmt.Sprintf("must be one of [%s]", fe.Param())
	case "max":
		return fmt.Sprintf("must be at most %s characters", fe.Param())
	default:
		return "is invalid"
	}
}
