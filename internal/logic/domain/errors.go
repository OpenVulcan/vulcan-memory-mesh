// errors.go declares reusable domain-level errors shared by validation and processor logic.
// errors.go 用于声明校验和处理器逻辑复用的领域级错误。
package domain

import (
	"errors"
	"fmt"
)

// Variables expose reusable sentinel errors that higher layers can match without string parsing.
// Variables 用于暴露可复用的哨兵错误，方便上层在不解析字符串的情况下做匹配。
var (
	ErrValidation = errors.New("validation failed")
	ErrTimeout    = errors.New("request timeout")
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
