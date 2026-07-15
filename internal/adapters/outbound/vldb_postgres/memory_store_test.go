// memory_store_test.go verifies PostgreSQL direct-memory helpers that guard lifecycle writes after fresh rows may already exist.
// memory_store_test.go 用于验证 PostgreSQL 主动记忆 helper，确保新行可能已存在后的生命周期写入具备保护。
package vldb_postgres

import (
	"errors"
	"strings"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestBuildPostgresMemoryNodesSupersedeSQLGuardsActiveIDs verifies shared memory replacement SQL keeps the active-status guard.
// TestBuildPostgresMemoryNodesSupersedeSQLGuardsActiveIDs 用于验证共享记忆替代 SQL 保留 active 状态保护条件。
func TestBuildPostgresMemoryNodesSupersedeSQLGuardsActiveIDs(t *testing.T) {
	sqlText := buildPostgresMemoryNodesSupersedeSQL(`"public"."vmm_memory_nodes"`)

	for _, fragment := range []string{
		`UPDATE "public"."vmm_memory_nodes"`,
		"SET memory_status = $1",
		"updated_at = $2",
		"WHERE memory_status = $3",
		"AND id = ANY($4)",
	} {
		if !strings.Contains(sqlText, fragment) {
			t.Fatalf("expected memory supersede SQL to contain %q, got %q", fragment, sqlText)
		}
	}
}

// TestDirectMemoryNodeInsertSQLRejectsConflictUpsert verifies direct writes create a fresh durable row instead of overwriting an existing vector row.
// TestDirectMemoryNodeInsertSQLRejectsConflictUpsert 用于验证主动写会创建新的长期行，而不是在 vector 冲突时覆盖已有行。
func TestDirectMemoryNodeInsertSQLRejectsConflictUpsert(t *testing.T) {
	sqlText := directMemoryNodeInsertSQL(`"public"."vmm_memory_nodes"`)

	if !strings.Contains(sqlText, `INSERT INTO "public"."vmm_memory_nodes"`) {
		t.Fatalf("expected direct memory SQL to insert into memory table, got %q", sqlText)
	}
	if !strings.Contains(sqlText, "RETURNING id") {
		t.Fatalf("expected direct memory SQL to return inserted row, got %q", sqlText)
	}
	if strings.Contains(sqlText, "ON CONFLICT") || strings.Contains(sqlText, "DO UPDATE") {
		t.Fatalf("expected direct memory SQL to reject conflict-upsert semantics, got %q", sqlText)
	}
}

// TestRequirePostgresDirectMemoryWriteRowsAffectedRejectsDrift verifies direct-write supersede drift stays an ordinary pre-commit error before any fresh vector can be durably referenced.
// TestRequirePostgresDirectMemoryWriteRowsAffectedRejectsDrift 用于验证主动写 supersede 漂移在新向量被长期引用前保持为普通提交前错误。
func TestRequirePostgresDirectMemoryWriteRowsAffectedRejectsDrift(t *testing.T) {
	if err := requirePostgresDirectMemoryWriteRowsAffected("supersede postgres direct-write memory nodes", 2, 2); err != nil {
		t.Fatalf("expected matching row count to succeed, got %v", err)
	}

	err := requirePostgresDirectMemoryWriteRowsAffected("supersede postgres direct-write memory nodes", 1, 2)
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect pre-commit direct-write drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "supersede postgres direct-write memory nodes affected 1 rows, want 2") {
		t.Fatalf("unexpected direct-write drift error: %v", err)
	}
}

// TestPostgresDirectMemoryWriteCommitErrorMarksFreshVectorReferenceUncertain verifies commit ambiguity keeps the fresh vector for a possibly committed direct-write row.
// TestPostgresDirectMemoryWriteCommitErrorMarksFreshVectorReferenceUncertain 用于验证主动写提交结果不明时会保留可能已被提交行引用的新向量。
func TestPostgresDirectMemoryWriteCommitErrorMarksFreshVectorReferenceUncertain(t *testing.T) {
	// err carries a synthetic commit failure after the direct-write insert may have reached PostgreSQL.
	// err 保存主动写插入可能已经到达 PostgreSQL 后发生的模拟提交失败。
	err := postgresFreshVectorCommitOutcomeUncertainError("apply direct memory write", "commit postgres direct memory write tx", errors.New("connection reset"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain commit error, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected fresh-vector uncertainty on direct-write commit ambiguity, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres direct memory write tx: connection reset") {
		t.Fatalf("unexpected direct-write commit error detail: %v", err)
	}
}

// TestPostgresCreateDirectMemoryNodeCommitErrorMarksFreshVectorReferenceUncertain verifies fallback commit ambiguity keeps the fresh vector for a possibly committed direct-memory row.
// TestPostgresCreateDirectMemoryNodeCommitErrorMarksFreshVectorReferenceUncertain 用于验证 fallback 提交结果不明时会保留可能已被提交行引用的新向量。
func TestPostgresCreateDirectMemoryNodeCommitErrorMarksFreshVectorReferenceUncertain(t *testing.T) {
	// err carries a synthetic fallback commit failure after the direct-memory insert may have reached PostgreSQL.
	// err 保存 fallback 主动记忆插入可能已经到达 PostgreSQL 后发生的模拟提交失败。
	err := postgresFreshVectorCommitOutcomeUncertainError("create direct memory node", "commit postgres direct memory node tx", errors.New("commit timeout"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain fallback commit error, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected fresh-vector uncertainty on fallback commit ambiguity, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres direct memory node tx: commit timeout") {
		t.Fatalf("unexpected fallback commit error detail: %v", err)
	}
}

// TestPostgresMemoryDeleteCommitErrorMarksOutcomeUncertain verifies delete commits expose ambiguous durable status flips without fresh-vector retention.
// TestPostgresMemoryDeleteCommitErrorMarksOutcomeUncertain 用于验证删除提交会暴露 durable 状态切换不确定性，但不会标记新向量保留。
func TestPostgresMemoryDeleteCommitErrorMarksOutcomeUncertain(t *testing.T) {
	err := postgresMemoryDeleteCommitOutcomeUncertainError(errors.New("commit acknowledgement lost"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain memory delete commit error, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect memory delete commit ambiguity to mark fresh-vector uncertainty, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres memory delete tx: commit acknowledgement lost") {
		t.Fatalf("unexpected memory delete commit error detail: %v", err)
	}
}

// TestPostgresMemoryVectorReplaceCommitErrorMarksOutcomeUncertain verifies maintenance vector rewrites expose ambiguous inline embedding state without fresh-vector retention.
// TestPostgresMemoryVectorReplaceCommitErrorMarksOutcomeUncertain 用于验证维护向量重写会暴露内联 embedding 状态不确定，但不会标记新向量保留。
func TestPostgresMemoryVectorReplaceCommitErrorMarksOutcomeUncertain(t *testing.T) {
	err := postgresMemoryVectorReplaceCommitOutcomeUncertainError(errors.New("network closed"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain memory vector replace commit error, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect memory vector replace commit ambiguity to mark fresh-vector uncertainty, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres vector rebuild tx: network closed") {
		t.Fatalf("unexpected memory vector replace commit error detail: %v", err)
	}
}
