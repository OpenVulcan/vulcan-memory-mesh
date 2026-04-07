// errors.go defines typed failover errors that let higher-level route wrappers distinguish exhausted key pools from ordinary provider faults.
// errors.go 用于定义带类型的容灾错误，让更高一层的路由包装器可以区分“Key 池耗尽”和普通 provider 故障。
package ai_key_failover

import "errors"

// exhaustedCandidatesReason explains why the fixed-route selector could not produce any currently usable candidate.
// exhaustedCandidatesReason 用于说明固定路由选择器为何无法产出任何当前可用的候选项。
type exhaustedCandidatesReason string

const (
	// exhaustedCandidatesReasonUnavailable marks permanent or cooldown-based candidate loss such as invalid credentials or explicit key quarantine.
	// exhaustedCandidatesReasonUnavailable 用于标记永久性或冷却期内的候选不可用场景，例如无效凭据或显式 Key 隔离。
	exhaustedCandidatesReasonUnavailable exhaustedCandidatesReason = "unavailable"

	// exhaustedCandidatesReasonBudget marks temporary exhaustion caused only by RPM/TPM/RPD budget windows.
	// exhaustedCandidatesReasonBudget 用于标记仅由 RPM/TPM/RPD 预算窗口导致的临时耗尽场景。
	exhaustedCandidatesReasonBudget exhaustedCandidatesReason = "budget"
)

// exhaustedCandidatesError marks one fixed-route selector failure caused by every candidate key becoming unavailable or over budget inside the current route.
// exhaustedCandidatesError 用于标记某条固定路由的选择器失败，表示当前路由内所有候选 Key 都已经不可用或超出预算。
type exhaustedCandidatesError struct {
	message string
	reason  exhaustedCandidatesReason
}

// Error returns the stable human-readable failure text preserved for logs and callers.
// Error 用于返回稳定的人类可读失败文本，供日志和调用方继续复用。
func (e *exhaustedCandidatesError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

// newExhaustedCandidatesError wraps one selector message into a typed exhaustion error that defaults to the generic unavailable-or-unhealthy cause.
// newExhaustedCandidatesError 用于把选择器错误消息包装成带类型的耗尽错误，并默认表示“候选不可用/不健康”原因。
func newExhaustedCandidatesError(message string) error {
	return &exhaustedCandidatesError{message: message, reason: exhaustedCandidatesReasonUnavailable}
}

// newBudgetExhaustedCandidatesError wraps one selector message into a typed exhaustion error that specifically means the current candidates are only blocked by local budgets.
// newBudgetExhaustedCandidatesError 用于把选择器错误消息包装成带类型的耗尽错误，并显式表示当前候选只是被本地预算暂时挡住。
func newBudgetExhaustedCandidatesError(message string) error {
	return &exhaustedCandidatesError{message: message, reason: exhaustedCandidatesReasonBudget}
}

// isExhaustedCandidatesError reports whether one returned error means the current fixed route has no further healthy or budget-eligible keys to try.
// isExhaustedCandidatesError 用于判断某个返回错误是否表示当前固定路由已经没有更多健康或预算可用的 Key 可尝试。
func isExhaustedCandidatesError(err error) bool {
	var target *exhaustedCandidatesError
	return errors.As(err, &target)
}

// isBudgetExhaustedCandidatesError reports whether one exhaustion error specifically means the remaining candidates are blocked only by RPM/TPM/RPD windows.
// isBudgetExhaustedCandidatesError 用于判断某个耗尽错误是否明确表示剩余候选只是被 RPM/TPM/RPD 窗口暂时挡住。
func isBudgetExhaustedCandidatesError(err error) bool {
	var target *exhaustedCandidatesError
	return errors.As(err, &target) && target != nil && target.reason == exhaustedCandidatesReasonBudget
}

// IsExhaustedCandidatesError reports whether one returned error means the current route cannot produce any more eligible keys or nodes.
// IsExhaustedCandidatesError 用于判断某个错误是否表示当前路由已无法再产出可用 Key 或节点。
func IsExhaustedCandidatesError(err error) bool {
	return isExhaustedCandidatesError(err)
}

// IsBudgetExhaustedCandidatesError reports whether one returned error means the current candidates are only waiting for a later budget window instead of being permanently unhealthy.
// IsBudgetExhaustedCandidatesError 用于判断某个错误是否表示当前候选只是等待后续预算窗口恢复，而不是已经永久不健康。
func IsBudgetExhaustedCandidatesError(err error) bool {
	return isBudgetExhaustedCandidatesError(err)
}
