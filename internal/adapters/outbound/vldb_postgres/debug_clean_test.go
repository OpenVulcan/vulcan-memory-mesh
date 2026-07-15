// debug_clean_test.go verifies PostgreSQL debug-clean error classification without requiring a live PostgreSQL instance.
// debug_clean_test.go 用于在不依赖真实 PostgreSQL 实例的情况下验证 PostgreSQL 调试清理错误分类。
package vldb_postgres

import (
	"errors"
	"strings"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestPostgresDebugCleanCommitErrorMarksOutcomeUncertain verifies destructive cleanup reports ambiguous commits without pretending to know the final schema state.
// TestPostgresDebugCleanCommitErrorMarksOutcomeUncertain 用于验证破坏性清理在提交结果不明时会上报结果不确定，而不是假装已知最终 schema 状态。
func TestPostgresDebugCleanCommitErrorMarksOutcomeUncertain(t *testing.T) {
	err := postgresCommitOutcomeUncertainError("clean postgres managed schema", "commit postgres debug-clean tx", errors.New("commit acknowledgement lost"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected debug-clean commit error to be outcome-uncertain, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect debug-clean commit ambiguity to mark fresh-vector uncertainty, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres debug-clean tx: commit acknowledgement lost") {
		t.Fatalf("unexpected debug-clean commit detail: %v", err)
	}
}
