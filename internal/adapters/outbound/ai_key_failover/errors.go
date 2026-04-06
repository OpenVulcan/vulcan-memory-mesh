// errors.go defines typed failover errors that let higher-level route wrappers distinguish exhausted key pools from ordinary provider faults.
// errors.go 用于定义带类型的容灾错误，让更高一层的路由包装器可以区分“Key 池耗尽”和普通 provider 故障。
package ai_key_failover

import "errors"

// exhaustedCandidatesError marks one fixed-route selector failure caused by every candidate key becoming unavailable or over budget inside the current route.
// exhaustedCandidatesError 用于标记某条固定路由的选择器失败，表示当前路由内所有候选 Key 都已经不可用或超出预算。
type exhaustedCandidatesError struct {
	message string
}

// Error returns the stable human-readable failure text preserved for logs and callers.
// Error 用于返回稳定的人类可读失败文本，供日志和调用方继续复用。
func (e *exhaustedCandidatesError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

// newExhaustedCandidatesError wraps one existing selector message into a typed route-exhaustion error without changing the visible text.
// newExhaustedCandidatesError 用于把现有选择器错误消息包装成带类型的“路由耗尽”错误，同时不改变对外可见文本。
func newExhaustedCandidatesError(message string) error {
	return &exhaustedCandidatesError{message: message}
}

// isExhaustedCandidatesError reports whether one returned error means the current fixed route has no further healthy or budget-eligible keys to try.
// isExhaustedCandidatesError 用于判断某个返回错误是否表示当前固定路由已经没有更多健康或预算可用的 Key 可尝试。
func isExhaustedCandidatesError(err error) bool {
	var target *exhaustedCandidatesError
	return errors.As(err, &target)
}
