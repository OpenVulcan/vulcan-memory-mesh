// Package controller exposes typed client errors for transport failures whose server-side mutation outcome cannot be proven.
// controller 包用于暴露类型化客户端错误，表示传输失败后无法证明服务端写操作结果的场景。
package controller

import (
	"errors"
	"fmt"
)

// ErrMutationOutcomeUncertain identifies a mutation that may already have committed before the transport failed.
// ErrMutationOutcomeUncertain 标识可能已在传输失败前提交、但客户端无法确认结果的写操作。
var ErrMutationOutcomeUncertain = errors.New("controller mutation outcome is uncertain")

// MutationOutcomeUncertainError records the failed operation together with transport and session-recovery errors.
// MutationOutcomeUncertainError 记录结果不确定的操作，以及对应的传输错误和会话恢复错误。
type MutationOutcomeUncertainError struct {
	// Operation identifies the controller SDK mutation method.
	// Operation 标识 controller SDK 的写操作方法。
	Operation string
	// Cause is the recoverable RPC error returned by the original mutation attempt.
	// Cause 是原始写操作返回的可恢复 RPC 错误。
	Cause error
	// RecoveryError reports a reconnect or desired-state replay failure when recovery did not complete.
	// RecoveryError 记录重连或期望状态重放未完成时的恢复错误。
	RecoveryError error
}

// Error returns a diagnostic message without implying that the mutation failed or succeeded on the server.
// Error 返回诊断信息，但不会暗示服务端写操作已经失败或成功。
func (e *MutationOutcomeUncertainError) Error() string {
	if e == nil {
		return ErrMutationOutcomeUncertain.Error()
	}
	if e.RecoveryError != nil {
		return fmt.Sprintf("%s: %s: transport error: %v; session recovery error: %v", ErrMutationOutcomeUncertain, e.Operation, e.Cause, e.RecoveryError)
	}
	return fmt.Sprintf("%s: %s: transport error: %v", ErrMutationOutcomeUncertain, e.Operation, e.Cause)
}

// Unwrap exposes the original transport error for gRPC status inspection.
// Unwrap 暴露原始传输错误，供调用方检查 gRPC 状态。
func (e *MutationOutcomeUncertainError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Is makes errors.Is recognize the stable uncertain-outcome sentinel.
// Is 使 errors.Is 能够识别稳定的结果不确定哨兵错误。
func (e *MutationOutcomeUncertainError) Is(target error) bool {
	return target == ErrMutationOutcomeUncertain
}

// IsMutationOutcomeUncertain reports whether an error represents an unconfirmed controller-side mutation outcome.
// IsMutationOutcomeUncertain 判断错误是否表示 controller 侧写操作结果无法确认。
func IsMutationOutcomeUncertain(err error) bool {
	return errors.Is(err, ErrMutationOutcomeUncertain)
}
