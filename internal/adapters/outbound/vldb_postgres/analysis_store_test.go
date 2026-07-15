// analysis_store_test.go verifies PostgreSQL turn-analysis helpers that guard deterministic write-back boundaries without requiring a live database.
// analysis_store_test.go 用于验证 PostgreSQL turn-analysis 辅助逻辑在无真实数据库时仍能保护确定性写回边界。
package vldb_postgres

import (
	"errors"
	"strings"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestRequirePostgresTurnAnalysisRowsAffectedRejectsDrift verifies turn-analysis write-back cannot silently miss known update targets.
// TestRequirePostgresTurnAnalysisRowsAffectedRejectsDrift 用于验证 turn-analysis 写回不能静默漏掉已知更新目标。
func TestRequirePostgresTurnAnalysisRowsAffectedRejectsDrift(t *testing.T) {
	if err := requirePostgresTurnAnalysisRowsAffected("update postgres turn analysis details", 1, 1); err != nil {
		t.Fatalf("expected matching row count to succeed, got %v", err)
	}
	err := requirePostgresTurnAnalysisRowsAffected("supersede postgres memory nodes", 1, 2)
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect pre-commit row drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "supersede postgres memory nodes affected 1 rows, want 2") {
		t.Fatalf("unexpected row drift error: %v", err)
	}
}

// TestPostgresTurnAnalysisCommitErrorMarksFreshVectorReferenceUncertain verifies commit ambiguity keeps vectors that may already be durable relational references.
// TestPostgresTurnAnalysisCommitErrorMarksFreshVectorReferenceUncertain 用于验证提交结果不明时会保留可能已被关系行引用的新向量。
func TestPostgresTurnAnalysisCommitErrorMarksFreshVectorReferenceUncertain(t *testing.T) {
	// err carries a synthetic commit failure after the transaction may have reached PostgreSQL.
	// err 保存事务可能已经到达 PostgreSQL 后发生的模拟提交失败。
	err := postgresFreshVectorCommitOutcomeUncertainError("apply turn analysis", "commit postgres turn analysis tx", errors.New("network closed"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain commit error, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected fresh-vector uncertainty on commit ambiguity, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres turn analysis tx: network closed") {
		t.Fatalf("unexpected commit error detail: %v", err)
	}
}

// TestBuildPostgresTurnAnalysisUpdateSQLGuardsSessionPendingTurn verifies turn-analysis write-back cannot update a stale or already-processed turn.
// TestBuildPostgresTurnAnalysisUpdateSQLGuardsSessionPendingTurn 用于验证 turn-analysis 回写不能更新过期或已处理的 turn。
func TestBuildPostgresTurnAnalysisUpdateSQLGuardsSessionPendingTurn(t *testing.T) {
	sqlText := buildPostgresTurnAnalysisUpdateSQL(`"public"."vmm_turn_records"`)

	for _, fragment := range []string{
		`UPDATE "public"."vmm_turn_records"`,
		"SET details = $1",
		"details_budget = $2",
		"extracted_status = $3",
		"updated_at = $4",
		"WHERE id = $5",
		"AND session_id = $6",
		"AND extracted_status = $7",
	} {
		if !strings.Contains(sqlText, fragment) {
			t.Fatalf("expected turn-analysis SQL to contain %q, got %q", fragment, sqlText)
		}
	}
}

// TestBuildPostgresProfileNodesSupersedeSQLGuardsTarget verifies profile replacement cannot retire nodes outside the reviewed target.
// TestBuildPostgresProfileNodesSupersedeSQLGuardsTarget 用于验证画像替代不能退役评审目标范围之外的节点。
func TestBuildPostgresProfileNodesSupersedeSQLGuardsTarget(t *testing.T) {
	sqlText := buildPostgresProfileNodesSupersedeSQL(`"public"."vmm_profile_nodes"`)

	for _, fragment := range []string{
		`UPDATE "public"."vmm_profile_nodes"`,
		"SET profile_status = $1",
		"superseded_by_id = $2",
		"status_reason = $3",
		"updated_at = $4",
		"WHERE profile_status = $5",
		"AND profile_type = $6",
		"AND bind_id = $7",
		"AND id = ANY($8)",
	} {
		if !strings.Contains(sqlText, fragment) {
			t.Fatalf("expected profile supersede SQL to contain %q, got %q", fragment, sqlText)
		}
	}
}

// TestRequirePostgresMemoryAdoptionRowsAffectedRejectsDrift verifies adoption lifecycle sync cannot return records whose relational update missed the locked row.
// TestRequirePostgresMemoryAdoptionRowsAffectedRejectsDrift 用于验证采纳生命周期同步不能返回关系更新未命中的已锁定行记录。
func TestRequirePostgresMemoryAdoptionRowsAffectedRejectsDrift(t *testing.T) {
	if err := requirePostgresMemoryAdoptionRowsAffected(201, 1); err != nil {
		t.Fatalf("expected matching row count to succeed, got %v", err)
	}
	err := requirePostgresMemoryAdoptionRowsAffected(201, 0)
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect adoption row drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "update postgres adopted memory node 201 affected 0 rows, want 1") {
		t.Fatalf("unexpected row drift error: %v", err)
	}
}

// TestPostgresMemoryAdoptionCommitErrorMarksOutcomeUncertainWithoutFreshVector verifies adoption commit ambiguity reports relational uncertainty without fresh-vector retention.
// TestPostgresMemoryAdoptionCommitErrorMarksOutcomeUncertainWithoutFreshVector 用于验证采纳提交结果不明时只报告关系结果不确定，而不标记新向量保留。
func TestPostgresMemoryAdoptionCommitErrorMarksOutcomeUncertainWithoutFreshVector(t *testing.T) {
	// err carries a synthetic commit failure after lifecycle updates may have reached PostgreSQL.
	// err 保存生命周期更新可能已经到达 PostgreSQL 后发生的模拟提交失败。
	err := postgresCommitOutcomeUncertainError("apply memory adoption", "commit postgres memory adoption tx", errors.New("commit acknowledgement lost"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain adoption commit error, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect adoption commit ambiguity to mark fresh-vector uncertainty, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres memory adoption tx: commit acknowledgement lost") {
		t.Fatalf("unexpected adoption commit error detail: %v", err)
	}
}
