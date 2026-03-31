// errors.go declares reusable domain-level errors shared by validation and processor logic.
// errors.go 用于声明校验和处理器逻辑复用的领域级错误。
package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Variables expose reusable sentinel errors that higher layers can match without string parsing.
// Variables 用于暴露可复用的哨兵错误，方便上层在不解析字符串的情况下做匹配。
var (
	ErrValidation       = errors.New("validation failed")
	ErrTimeout          = errors.New("request timeout")
	ErrNotFound         = errors.New("resource not found")
	ErrConflict         = errors.New("resource conflict")
	ErrConfirm          = errors.New("confirmation required")
	ErrOutcomeUncertain = errors.New("storage outcome uncertain")
)

// ValidationError marks one concrete field-level validation failure coming from domain or use-case checks.
// ValidationError 用于标记来自领域层或用例层的单个字段级校验失败。
type ValidationError struct {
	Field   string
	Message string
}

// Error executes the Error logic.
// Error 用于执行 Error 逻辑。
func (e ValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// Unwrap executes the Unwrap logic.
// Unwrap 用于执行 Unwrap 逻辑。
func (e ValidationError) Unwrap() error { return ErrValidation }

// IsValidationError reports whether the condition is true.
// IsValidationError 用于返回条件是否成立。
func IsValidationError(err error) bool { return errors.Is(err, ErrValidation) }

// NotFoundError marks one hierarchy or user lookup failure that should be surfaced as a stable not-found response.
// NotFoundError 用于标记一次层级或用户查询失败，并将其稳定映射为 not-found 响应。
type NotFoundError struct {
	Resource string
	Message  string
}

// Error executes the Error logic.
// Error 用于执行 Error 逻辑。
func (e NotFoundError) Error() string {
	if e.Resource == "" {
		return e.Message
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("%s not found", e.Resource)
	}
	return fmt.Sprintf("%s: %s", e.Resource, e.Message)
}

// Unwrap executes the Unwrap logic.
// Unwrap 用于执行 Unwrap 逻辑。
func (e NotFoundError) Unwrap() error { return ErrNotFound }

// IsNotFoundError reports whether the condition is true.
// IsNotFoundError 用于返回条件是否成立。
func IsNotFoundError(err error) bool { return errors.Is(err, ErrNotFound) }

// ConflictError marks one duplicate or scope-mismatch failure that should surface as a conflict response.
// ConflictError 用于标记重复创建或范围不匹配失败，并映射为 conflict 响应。
type ConflictError struct {
	Resource string
	Message  string
}

// Error executes the Error logic.
// Error 用于执行 Error 逻辑。
func (e ConflictError) Error() string {
	if e.Resource == "" {
		return e.Message
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("%s conflict", e.Resource)
	}
	return fmt.Sprintf("%s: %s", e.Resource, e.Message)
}

// Unwrap executes the Unwrap logic.
// Unwrap 用于执行 Unwrap 逻辑。
func (e ConflictError) Unwrap() error { return ErrConflict }

// IsConflictError reports whether the condition is true.
// IsConflictError 用于返回条件是否成立。
func IsConflictError(err error) bool { return errors.Is(err, ErrConflict) }

// ConfirmationRequiredError marks one destructive or hierarchy-creating action that requires an explicit second confirmation.
// ConfirmationRequiredError 用于标记需要显式二次确认的破坏性操作或层级创建动作。
type ConfirmationRequiredError struct {
	Resource string
	Message  string
	Code     string
}

// Error executes the Error logic.
// Error 用于执行 Error 逻辑。
func (e ConfirmationRequiredError) Error() string {
	if e.Resource == "" {
		return e.Message
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("%s confirmation required", e.Resource)
	}
	return fmt.Sprintf("%s: %s", e.Resource, e.Message)
}

// Unwrap executes the Unwrap logic.
// Unwrap 用于执行 Unwrap 逻辑。
func (e ConfirmationRequiredError) Unwrap() error { return ErrConfirm }

// IsConfirmationRequired reports whether the condition is true.
// IsConfirmationRequired 用于返回条件是否成立。
func IsConfirmationRequired(err error) bool { return errors.Is(err, ErrConfirm) }

// OutcomeUncertainError marks one persistence step whose upstream storage engine reported an error after the commit outcome became ambiguous.
// OutcomeUncertainError 用于标记一次持久化步骤在上游存储引擎报错后进入“提交结果不确定”的状态。
type OutcomeUncertainError struct {
	Operation string
	Message   string
}

// Error executes the Error logic.
// Error 用于执行 Error 逻辑。
func (e OutcomeUncertainError) Error() string {
	if strings.TrimSpace(e.Operation) == "" {
		return "storage outcome uncertain"
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("%s: storage outcome uncertain", e.Operation)
	}
	return fmt.Sprintf("%s: storage outcome uncertain: %s", e.Operation, strings.TrimSpace(e.Message))
}

// Unwrap executes the Unwrap logic.
// Unwrap 用于执行 Unwrap 逻辑。
func (e OutcomeUncertainError) Unwrap() error { return ErrOutcomeUncertain }

// IsOutcomeUncertain reports whether the condition is true.
// IsOutcomeUncertain 用于返回条件是否成立。
func IsOutcomeUncertain(err error) bool { return errors.Is(err, ErrOutcomeUncertain) }

// InvalidLLMOutputError captures malformed model output when processors expect structured JSON.
// InvalidLLMOutputError 用于在处理器期望结构化 JSON 时记录模型输出格式错误。
type InvalidLLMOutputError struct {
	Scene   string
	Message string
	Raw     string
}

// Error executes the Error logic.
// Error 用于执行 Error 逻辑。
func (e InvalidLLMOutputError) Error() string {
	if e.Scene == "" {
		return "invalid llm output: " + e.Message
	}
	return fmt.Sprintf("invalid llm output for %s: %s", e.Scene, e.Message)
}
