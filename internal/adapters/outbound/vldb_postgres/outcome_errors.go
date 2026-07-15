// outcome_errors.go contains PostgreSQL outcome-classification helpers for write paths whose commit result controls later app-side side effects.
// outcome_errors.go 存放 PostgreSQL 写入路径的结果分类辅助函数，用于处理提交结果会影响后续应用侧副作用的场景。
package vldb_postgres

import (
	"fmt"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// postgresOutcomeUncertainError converts a non-nil PostgreSQL persistence error for operation and message into an uncertain result, returns nil for nil input, and leaves FreshVectorReference unset for writes that do not create fresh vector references.
// postgresOutcomeUncertainError 用于把 operation 与 message 对应的非空 PostgreSQL 持久化错误转换为结果不确定错误；输入为空时返回 nil，并且对不会创建新向量引用的写入不设置 FreshVectorReference。
func postgresOutcomeUncertainError(operation string, message string, err error) error {
	if err == nil {
		return nil
	}
	return logicdomain.OutcomeUncertainError{
		Operation: operation,
		Message:   fmt.Sprintf("%s: %v", message, err),
	}
}

// postgresCommitOutcomeUncertainError converts a non-nil PostgreSQL commit error for operation and message into an uncertain result, returns nil for nil input, and leaves FreshVectorReference unset for writes that do not create fresh vector references.
// postgresCommitOutcomeUncertainError 用于把 operation 与 message 对应的非空 PostgreSQL 提交错误转换为结果不确定错误；输入为空时返回 nil，并且对不会创建新向量引用的写入不设置 FreshVectorReference。
func postgresCommitOutcomeUncertainError(operation string, message string, err error) error {
	return postgresOutcomeUncertainError(operation, message, err)
}

// postgresRowsAffectedDriftError builds a shared ordinary row-count drift error for PostgreSQL writes that can still roll back before the commit boundary becomes ambiguous.
// postgresRowsAffectedDriftError 用于为仍可在提交边界变得不确定前回滚的 PostgreSQL 写入构建统一的普通行数漂移错误。
func postgresRowsAffectedDriftError(action string, rowsAffected, expectedRows int64) error {
	if rowsAffected == expectedRows {
		return nil
	}
	return fmt.Errorf("%s affected %d rows, want %d", action, rowsAffected, expectedRows)
}

// postgresClaimCommitError classifies a lease-claim commit error using claimedRows, returning ordinary errors for no-op claims and outcome-uncertain errors once at least one lease row may have committed.
// postgresClaimCommitError 用于根据 claimedRows 分类租约领取提交错误；无领取行时返回普通错误，至少一条租约行可能提交后返回结果不确定错误。
func postgresClaimCommitError(operation string, message string, claimedRows int, err error) error {
	if err == nil {
		return nil
	}
	if claimedRows <= 0 {
		return fmt.Errorf("%s: %w", message, err)
	}
	return postgresCommitOutcomeUncertainError(operation, message, err)
}

// postgresFreshVectorCommitOutcomeUncertainError converts a non-nil PostgreSQL commit error for operation and message into an uncertain result, returns nil for nil input, and marks fresh-vector references because the commit acknowledgement may be lost after durable memory rows were written.
// postgresFreshVectorCommitOutcomeUncertainError 用于把 operation 与 message 对应的非空 PostgreSQL 提交错误转换为结果不确定错误；输入为空时返回 nil，并因提交确认可能在长期记忆行写入后丢失而标记新向量引用不确定。
func postgresFreshVectorCommitOutcomeUncertainError(operation string, message string, err error) error {
	if err == nil {
		return nil
	}
	return logicdomain.OutcomeUncertainError{
		Operation:            operation,
		Message:              fmt.Sprintf("%s: %v", message, err),
		FreshVectorReference: true,
	}
}
