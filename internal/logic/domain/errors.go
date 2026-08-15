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
	ErrValidation        = errors.New("validation failed")
	ErrTimeout           = errors.New("request timeout")
	ErrNotFound          = errors.New("resource not found")
	ErrConflict          = errors.New("resource conflict")
	ErrProtectedResource = errors.New("protected resource")
	ErrConfirm           = errors.New("confirmation required")
	ErrOutcomeUncertain  = errors.New("storage outcome uncertain")
	ErrInvalidLLMOutput  = errors.New("invalid llm output")
)

// ValidationError marks one concrete field-level validation failure coming from domain or use-case checks.
// ValidationError 用于标记来自领域层或用例层的单个字段级校验失败。
type ValidationError struct {
	Field   string
	Message string
}

// Error renders the validation field and its failure message for transport-level mapping.
// Error 用于渲染校验字段及失败消息，供传输层映射使用。
func (e ValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// Unwrap exposes the validation sentinel so callers can classify the typed error with errors.Is.
// Unwrap 用于暴露校验哨兵错误，使调用方可通过 errors.Is 分类该类型错误。
func (e ValidationError) Unwrap() error { return ErrValidation }

// IsValidationError reports whether an error belongs to the request-validation category.
// IsValidationError 用于报告错误是否属于请求校验类别。
func IsValidationError(err error) bool { return errors.Is(err, ErrValidation) }

// NotFoundError marks one hierarchy or user lookup failure that should be surfaced as a stable not-found response.
// NotFoundError 用于标记一次层级或用户查询失败，并将其稳定映射为 not-found 响应。
type NotFoundError struct {
	Resource string
	Message  string
}

// Error renders the missing resource name and lookup key for diagnostics.
// Error 用于渲染缺失资源名称及查询键，便于诊断。
func (e NotFoundError) Error() string {
	if e.Resource == "" {
		return e.Message
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("%s not found", e.Resource)
	}
	return fmt.Sprintf("%s: %s", e.Resource, e.Message)
}

// Unwrap exposes the not-found sentinel for errors.Is classification.
// Unwrap 用于暴露未找到哨兵错误，供 errors.Is 分类。
func (e NotFoundError) Unwrap() error { return ErrNotFound }

// IsNotFoundError reports whether an error belongs to the missing-resource category.
// IsNotFoundError 用于报告错误是否属于资源未找到类别。
func IsNotFoundError(err error) bool { return errors.Is(err, ErrNotFound) }

// ConflictError marks one duplicate or scope-mismatch failure that should surface as a conflict response.
// ConflictError 用于标记重复创建或范围不匹配失败，并映射为 conflict 响应。
type ConflictError struct {
	Resource string
	Message  string
}

// Error renders the conflicting resource and reason without losing the domain category.
// Error 用于渲染发生冲突的资源及原因，同时保留领域错误类别。
func (e ConflictError) Error() string {
	if e.Resource == "" {
		return e.Message
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("%s conflict", e.Resource)
	}
	return fmt.Sprintf("%s: %s", e.Resource, e.Message)
}

// Unwrap exposes the conflict sentinel for errors.Is classification.
// Unwrap 用于暴露冲突哨兵错误，供 errors.Is 分类。
func (e ConflictError) Unwrap() error { return ErrConflict }

// IsConflictError reports whether an error represents a domain resource conflict.
// IsConflictError 用于报告错误是否表示领域资源冲突。
func IsConflictError(err error) bool { return errors.Is(err, ErrConflict) }

// ProtectedResourceError marks one built-in resource invariant that forbids destructive admin operations even when the caller supplied otherwise valid parameters.
// ProtectedResourceError 用于标记某个内建资源不变量：即使调用方提供了合法参数，也禁止执行破坏性管理操作。
type ProtectedResourceError struct {
	Resource string
	Message  string
}

// Error renders why a protected resource cannot be mutated by the requested operation.
// Error 用于渲染受保护资源无法执行请求变更的原因。
func (e ProtectedResourceError) Error() string {
	if e.Resource == "" {
		return e.Message
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("%s is protected", e.Resource)
	}
	return fmt.Sprintf("%s: %s", e.Resource, e.Message)
}

// Unwrap exposes the protected-resource sentinel for errors.Is classification.
// Unwrap 用于暴露受保护资源哨兵错误，供 errors.Is 分类。
func (e ProtectedResourceError) Unwrap() error { return ErrProtectedResource }

// IsProtectedResourceError reports whether a requested mutation targets a protected resource.
// IsProtectedResourceError 用于报告请求的变更是否作用于受保护资源。
func IsProtectedResourceError(err error) bool { return errors.Is(err, ErrProtectedResource) }

// ConfirmationRequiredError marks one destructive or hierarchy-creating action that requires an explicit second confirmation.
// ConfirmationRequiredError 用于标记需要显式二次确认的破坏性操作或层级创建动作。
type ConfirmationRequiredError struct {
	Resource string
	Message  string
	Code     string
}

// Error renders the destructive operation and confirmation token required from the caller.
// Error 用于渲染破坏性操作及调用方必须提供的确认令牌。
func (e ConfirmationRequiredError) Error() string {
	if e.Resource == "" {
		return e.Message
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("%s confirmation required", e.Resource)
	}
	return fmt.Sprintf("%s: %s", e.Resource, e.Message)
}

// Unwrap exposes the confirmation-required sentinel for errors.Is classification.
// Unwrap 用于暴露需要确认的哨兵错误，供 errors.Is 分类。
func (e ConfirmationRequiredError) Unwrap() error { return ErrConfirm }

// IsConfirmationRequired reports whether a destructive operation still requires caller confirmation.
// IsConfirmationRequired 用于报告破坏性操作是否仍需调用方确认。
func IsConfirmationRequired(err error) bool { return errors.Is(err, ErrConfirm) }

// OutcomeUncertainError marks one persistence step whose upstream storage engine reported an error after the commit outcome became ambiguous.
// OutcomeUncertainError 用于标记一次持久化步骤在上游存储引擎报错后进入“提交结果不确定”的状态。
type OutcomeUncertainError struct {
	// Operation names the persistence step whose final side effects are no longer fully knowable by the caller.
	// Operation 用于标记调用方已无法完全确认最终副作用的持久化步骤。
	Operation string
	// Message keeps the storage-facing detail that explains why the outcome became uncertain.
	// Message 用于保留导致结果不确定的存储侧细节。
	Message string
	// FreshVectorReference reports whether a freshly written vector may already be referenced by a durable memory row.
	// FreshVectorReference 用于标记刚写入的新向量是否可能已经被长期 memory 行引用。
	FreshVectorReference bool
}

// Error renders an operation whose final persistence outcome cannot be proven after a partial failure.
// Error 用于渲染发生局部失败后无法确认最终持久化结果的操作。
func (e OutcomeUncertainError) Error() string {
	if strings.TrimSpace(e.Operation) == "" {
		return "storage outcome uncertain"
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("%s: storage outcome uncertain", e.Operation)
	}
	return fmt.Sprintf("%s: storage outcome uncertain: %s", e.Operation, strings.TrimSpace(e.Message))
}

// Unwrap exposes the outcome-uncertain sentinel for errors.Is classification.
// Unwrap 用于暴露结果不确定哨兵错误，供 errors.Is 分类。
func (e OutcomeUncertainError) Unwrap() error { return ErrOutcomeUncertain }

// IsOutcomeUncertain reports whether a persistence operation may have partially committed.
// IsOutcomeUncertain 用于报告持久化操作是否可能已经局部提交。
func IsOutcomeUncertain(err error) bool { return errors.Is(err, ErrOutcomeUncertain) }

// IsFreshVectorReferenceUncertain reports whether a failed persistence step may already reference freshly written vectors.
// IsFreshVectorReferenceUncertain 用于判断失败的持久化步骤是否可能已经引用了刚写入的新向量。
func IsFreshVectorReferenceUncertain(err error) bool {
	var uncertain OutcomeUncertainError
	if !errors.As(err, &uncertain) {
		return false
	}
	return uncertain.FreshVectorReference
}

// InvalidLLMOutputError captures malformed model output when processors expect structured JSON.
// InvalidLLMOutputError 用于在处理器期望结构化 JSON 时记录模型输出格式错误。
type InvalidLLMOutputError struct {
	Scene   string
	Message string
	Raw     string
	// Execution preserves the completed physical call facts when parsing or validation rejects its semantic payload.
	// Execution 用于在解析或校验拒绝语义载荷时保留已完成物理调用事实。
	Execution *LLMExecutionMetadata
}

// Error renders the model-output validation failure while keeping raw output out of the message.
// Error 用于渲染模型输出校验失败，同时避免在错误消息中包含原始输出。
func (e InvalidLLMOutputError) Error() string {
	if e.Scene == "" {
		return "invalid llm output: " + e.Message
	}
	return fmt.Sprintf("invalid llm output for %s: %s", e.Scene, e.Message)
}

// Unwrap exposes the invalid-model-output sentinel for errors.Is classification.
// Unwrap 用于暴露无效模型输出哨兵错误，供 errors.Is 分类。
func (e InvalidLLMOutputError) Unwrap() error { return ErrInvalidLLMOutput }

// IsInvalidLLMOutputError reports whether model output failed a structured contract check.
// IsInvalidLLMOutputError 用于报告模型输出是否未通过结构化契约校验。
func IsInvalidLLMOutputError(err error) bool { return errors.Is(err, ErrInvalidLLMOutput) }

// LLMExecutionContextError preserves completed LLM identities when a later post-processing or persistence stage fails.
// LLMExecutionContextError 用于在后续处理或持久化阶段失败时保留已经完成的 LLM 身份事实。
type LLMExecutionContextError struct {
	// Cause is the original post-processing or persistence failure.
	// Cause 是原始后处理或持久化失败。
	Cause error
	// Executions preserves every completed physical LLM call that preceded the failure.
	// Executions 保留失败前已经完成的每一次物理 LLM 调用。
	Executions []LLMExecutionMetadata
}

// Error returns the original failure text without serializing model output or usage into the public message.
// Error 用于返回原始失败文本，不把模型输出或用量序列化到公开错误消息中。
func (e LLMExecutionContextError) Error() string {
	if e.Cause == nil {
		return "llm post-processing failed"
	}
	return e.Cause.Error()
}

// Unwrap exposes the original failure for errors.Is and errors.As classification.
// Unwrap 用于暴露原始失败，供 errors.Is 与 errors.As 分类。
func (e LLMExecutionContextError) Unwrap() error { return e.Cause }
