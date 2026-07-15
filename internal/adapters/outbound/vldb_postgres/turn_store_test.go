// turn_store_test.go verifies PostgreSQL turn-store guards that protect append and session-counter consistency without requiring a live database.
// turn_store_test.go 用于验证 PostgreSQL turn store 在无真实数据库时仍能保护追加 turn 与 session 计数一致性。
package vldb_postgres

import (
	"errors"
	"strings"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestRequirePostgresTurnAppendRowsAffectedRejectsDrift verifies append-turn cannot return a persisted turn id when the owning session counter was not updated.
// TestRequirePostgresTurnAppendRowsAffectedRejectsDrift 用于验证当所属 session 计数没有更新时，追加 turn 不能返回已持久化 turn id。
func TestRequirePostgresTurnAppendRowsAffectedRejectsDrift(t *testing.T) {
	if err := requirePostgresTurnAppendRowsAffected("update postgres session turn counters", 42, 1); err != nil {
		t.Fatalf("expected matching row count to succeed, got %v", err)
	}

	err := requirePostgresTurnAppendRowsAffected("update postgres session turn counters", 42, 0)
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect pre-commit session counter drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "update postgres session turn counters for session 42 affected 0 rows, want 1") {
		t.Fatalf("unexpected session counter drift error: %v", err)
	}
}

// TestPostgresTurnAppendCommitErrorMarksOutcomeUncertain verifies append commits expose ambiguous turn-row and session-counter state.
// TestPostgresTurnAppendCommitErrorMarksOutcomeUncertain 用于验证追加 turn 提交会暴露 turn 行与 session 计数结果不确定。
func TestPostgresTurnAppendCommitErrorMarksOutcomeUncertain(t *testing.T) {
	err := postgresTurnAppendCommitOutcomeUncertainError(errors.New("commit acknowledgement lost"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain turn append commit error, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect turn append commit ambiguity to mark fresh-vector uncertainty, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres turn append tx: commit acknowledgement lost") {
		t.Fatalf("unexpected turn append commit error detail: %v", err)
	}
}

// TestBuildPostgresMarkTurnAsCorruptedSQLGuardsPendingTurn verifies corrupted-turn marking cannot update outside the owning pending row.
// TestBuildPostgresMarkTurnAsCorruptedSQLGuardsPendingTurn 用于验证损坏 turn 标记不能越过所属 pending 行的边界更新。
func TestBuildPostgresMarkTurnAsCorruptedSQLGuardsPendingTurn(t *testing.T) {
	sqlText := buildPostgresMarkTurnAsCorruptedSQL(`"public"."vmm_turn_records"`)

	for _, fragment := range []string{
		`UPDATE "public"."vmm_turn_records"`,
		"SET extracted_status = $1",
		"updated_at = $2",
		"WHERE id = $3",
		"AND session_id = $4",
		"AND extracted_status = $5",
	} {
		if !strings.Contains(sqlText, fragment) {
			t.Fatalf("expected corrupted-turn SQL to contain %q, got %q", fragment, sqlText)
		}
	}
}

// TestPostgresSessionCompactCommitErrorMarksOutcomeUncertain verifies compact-boundary commits expose ambiguous session state.
// TestPostgresSessionCompactCommitErrorMarksOutcomeUncertain 用于验证 compact 边界提交会暴露 session 状态结果不确定。
func TestPostgresSessionCompactCommitErrorMarksOutcomeUncertain(t *testing.T) {
	err := postgresSessionCompactCommitOutcomeUncertainError(errors.New("network closed"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain session compact commit error, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect session compact commit ambiguity to mark fresh-vector uncertainty, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres compact tx: network closed") {
		t.Fatalf("unexpected session compact commit error detail: %v", err)
	}
}

// TestRequirePostgresCorruptedTurnRowsAffectedRejectsDrift verifies corrupted-turn marking cannot silently miss the pending row.
// TestRequirePostgresCorruptedTurnRowsAffectedRejectsDrift 用于验证损坏 turn 标记不能静默漏掉目标 pending 行。
func TestRequirePostgresCorruptedTurnRowsAffectedRejectsDrift(t *testing.T) {
	if err := requirePostgresCorruptedTurnRowsAffected("mark postgres turn as corrupted", 42, 77, 1); err != nil {
		t.Fatalf("expected matching row count to succeed, got %v", err)
	}

	err := requirePostgresCorruptedTurnRowsAffected("mark postgres turn as corrupted", 42, 77, 0)
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect pre-commit corrupted-turn drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "mark postgres turn as corrupted for session 42 turn 77 affected 0 rows, want 1") {
		t.Fatalf("unexpected corrupted-turn drift error: %v", err)
	}
}

// TestRequirePostgresSessionStateRowsAffectedRejectsDrift verifies session-state updates cannot silently miss the resolved session row.
// TestRequirePostgresSessionStateRowsAffectedRejectsDrift 用于验证 session 状态更新不能静默漏掉已解析的 session 行。
func TestRequirePostgresSessionStateRowsAffectedRejectsDrift(t *testing.T) {
	if err := requirePostgresSessionStateRowsAffected("advance postgres session extract window", 42, 1); err != nil {
		t.Fatalf("expected matching row count to succeed, got %v", err)
	}

	err := requirePostgresSessionStateRowsAffected("update postgres session compact boundary", 42, 0)
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect pre-commit session-state drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "update postgres session compact boundary for session 42 affected 0 rows, want 1") {
		t.Fatalf("unexpected session-state drift error: %v", err)
	}
}
