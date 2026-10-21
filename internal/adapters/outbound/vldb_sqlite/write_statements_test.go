// SQLite adapter tests for write statements test.
// SQLite 适配层写入语句参数绑定测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"testing"
	"time"
)

// TestParameterizedMemoryNodesSupersedeStatementUsesTypedIDParams verifies memory supersede updates bind normalized memory ids.
// TestParameterizedMemoryNodesSupersedeStatementUsesTypedIDParams 用于验证 memory supersede 更新会绑定规范化 memory id。
func TestParameterizedMemoryNodesSupersedeStatementUsesTypedIDParams(t *testing.T) {
	statement, ok := parameterizedMemoryNodesSupersedeStatement([]uint64{78, 0, 77, 78}, 12345)
	if !ok {
		t.Fatal("expected supersede statement for non-empty memory ids")
	}
	if !strings.Contains(statement.SQL, "WHERE memory_status = ? AND id IN (?,?)") {
		t.Fatalf("expected typed supersede placeholders, got %q", statement.SQL)
	}
	if strings.Contains(statement.SQL, "77") || strings.Contains(statement.SQL, "78") {
		t.Fatalf("expected supersede memory ids to stay out of SQL text, got %q", statement.SQL)
	}
	expected := []any{
		logicdomain.MemoryStatusSuperseded,
		int64(12345),
		logicdomain.MemoryStatusActive,
		uint64(77),
		uint64(78),
	}
	if len(statement.Params) != len(expected) {
		t.Fatalf("expected %d params, got %d: %#v", len(expected), len(statement.Params), statement.Params)
	}
	for idx, value := range expected {
		if statement.Params[idx] != value {
			t.Fatalf("param %d = %#v, want %#v", idx, statement.Params[idx], value)
		}
	}
}

// TestParameterizedProfileNodesSupersedeStatementUsesTypedIDParams verifies profile-node supersede updates bind normalized node ids and reviewer reasons.
// TestParameterizedProfileNodesSupersedeStatementUsesTypedIDParams 用于验证画像节点 supersede 更新会绑定规范化 node id 和评审原因。
func TestParameterizedProfileNodesSupersedeStatementUsesTypedIDParams(t *testing.T) {
	statement, ok := parameterizedProfileNodesSupersedeStatement(logicdomain.ProfileTypeUser, 42, []uint64{9, 0, 7, 9}, 101, "  accepted replacement  ", 12345)
	if !ok {
		t.Fatal("expected profile supersede statement for non-empty node ids")
	}
	if !strings.Contains(statement.SQL, "WHERE profile_status = ? AND profile_type = ? AND bind_id = ? AND id IN (?,?)") {
		t.Fatalf("expected typed profile supersede placeholders, got %q", statement.SQL)
	}
	if strings.Contains(statement.SQL, "7") || strings.Contains(statement.SQL, "9") || strings.Contains(statement.SQL, "accepted replacement") {
		t.Fatalf("expected node ids and reason to stay out of SQL text, got %q", statement.SQL)
	}
	expected := []any{
		logicdomain.ProfileStatusSuperseded,
		uint64(101),
		"accepted replacement",
		int64(12345),
		logicdomain.ProfileStatusActive,
		logicdomain.ProfileTypeUser,
		uint64(42),
		uint64(7),
		uint64(9),
	}
	if len(statement.Params) != len(expected) {
		t.Fatalf("expected %d params, got %d: %#v", len(expected), len(statement.Params), statement.Params)
	}
	for idx, value := range expected {
		if statement.Params[idx] != value {
			t.Fatalf("param %d = %#v, want %#v", idx, statement.Params[idx], value)
		}
	}
}

// TestParameterizedProfileNodesExpireStatementUsesTypedIDParams verifies profile-node expiry updates bind normalized node ids and lifecycle reasons.
// TestParameterizedProfileNodesExpireStatementUsesTypedIDParams 用于验证画像节点过期更新会绑定规范化 node id 和生命周期原因。
func TestParameterizedProfileNodesExpireStatementUsesTypedIDParams(t *testing.T) {
	statement, ok := parameterizedProfileNodesExpireStatement([]uint64{9, 0, 7, 9}, "  expired by lifecycle  ", 12345)
	if !ok {
		t.Fatal("expected profile expiry statement for non-empty node ids")
	}
	if !strings.Contains(statement.SQL, "WHERE profile_status = ? AND id IN (?,?)") {
		t.Fatalf("expected typed profile expiry placeholders, got %q", statement.SQL)
	}
	if strings.Contains(statement.SQL, "7") || strings.Contains(statement.SQL, "9") || strings.Contains(statement.SQL, "expired by lifecycle") {
		t.Fatalf("expected node ids and reason to stay out of SQL text, got %q", statement.SQL)
	}
	expected := []any{
		logicdomain.ProfileStatusExpired,
		"expired by lifecycle",
		int64(12345),
		logicdomain.ProfileStatusActive,
		uint64(7),
		uint64(9),
	}
	if len(statement.Params) != len(expected) {
		t.Fatalf("expected %d params, got %d: %#v", len(expected), len(statement.Params), statement.Params)
	}
	for idx, value := range expected {
		if statement.Params[idx] != value {
			t.Fatalf("param %d = %#v, want %#v", idx, statement.Params[idx], value)
		}
	}
}

// TestParameterizedMemoryContextEdgesReplaceStatementsBindExtractedLabels verifies edge persistence clears old rows first and binds normalized replacements second.
// TestParameterizedMemoryContextEdgesReplaceStatementsBindExtractedLabels 用于验证情境边持久化会先清空旧行，再参数化绑定规范化替代行。
func TestParameterizedMemoryContextEdgesReplaceStatementsBindExtractedLabels(t *testing.T) {
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	statements := parameterizedMemoryContextEdgesReplaceStatements(201, []logicdomain.MemoryContextEdge{
		{
			MemoryID:        999,
			ContextKey:      "task_stage",
			ContextValue:    "phase4",
			SupportCount:    2,
			RebuttalCount:   1,
			LastSupportedAt: now,
			LastRebuttedAt:  now,
			CreatedAt:       now,
			UpdatedAt:       now,
		},
	})
	if len(statements) != 2 {
		t.Fatalf("expected delete and insert statements, got %+v", statements)
	}
	if !strings.Contains(statements[0].SQL, "DELETE FROM vmm_memory_context_edges") || !strings.Contains(statements[1].SQL, "INSERT INTO vmm_memory_context_edges") {
		t.Fatalf("unexpected context edge statements: %+v", statements)
	}
	if strings.Contains(sqliteExecutedSQLLog([]*fakeExecuteRequest{{SQL: statements[0].SQL}, {SQL: statements[1].SQL}}), "task_stage") ||
		strings.Contains(sqliteExecutedSQLLog([]*fakeExecuteRequest{{SQL: statements[0].SQL}, {SQL: statements[1].SQL}}), "phase4") {
		t.Fatalf("context edge labels leaked into SQL templates: %+v", statements)
	}
	if len(statements[0].Params) != 1 || statements[0].Params[0] != uint64(201) {
		t.Fatalf("delete statement did not use replacement memory id param: %+v", statements[0])
	}
	if len(statements[1].Params) != 9 {
		t.Fatalf("expected nine insert params, got %+v", statements[1].Params)
	}
	if statements[1].Params[0] != uint64(201) || statements[1].Params[1] != "task_stage" || statements[1].Params[2] != "phase4" {
		t.Fatalf("insert statement did not bind canonical owner and labels: %+v", statements[1].Params)
	}
	for _, param := range statements[1].Params {
		if param == uint64(999) {
			t.Fatalf("context edge params leaked stale embedded edge memory id: %+v", statements[1].Params)
		}
	}
}
