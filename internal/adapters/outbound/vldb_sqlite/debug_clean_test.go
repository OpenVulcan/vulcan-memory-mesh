// debug_clean_test.go verifies SQLite debug-clean transactional safety and error classification without opening a real SQLite database.
// debug_clean_test.go 用于在不打开真实 SQLite 数据库的情况下验证 SQLite 调试清理的事务安全与错误分类。
package vldb_sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestDebugCleanManagedSchemaSQLUsesSingleTransaction verifies the destructive drop script cannot partially clean tables on ordinary statement failures.
// TestDebugCleanManagedSchemaSQLUsesSingleTransaction 用于验证破坏性删表脚本不会在普通语句失败时只清理部分表。
func TestDebugCleanManagedSchemaSQLUsesSingleTransaction(t *testing.T) {
	sqlText := strings.TrimSpace(debugCleanManagedSchemaSQL)
	if !strings.HasPrefix(sqlText, "BEGIN IMMEDIATE;") {
		t.Fatalf("debug clean sql should start one immediate transaction, got %q", sqlText)
	}
	if !strings.HasSuffix(sqlText, "COMMIT;") {
		t.Fatalf("debug clean sql should end with commit, got %q", sqlText)
	}
	if strings.Count(sqlText, "BEGIN IMMEDIATE;") != 1 || strings.Count(sqlText, "COMMIT;") != 1 {
		t.Fatalf("debug clean sql should contain exactly one transaction boundary, got %q", sqlText)
	}
}

// TestDebugCleanWithDatabaseSendsTransactionalScript verifies the FFI receives the transactional cleanup script unchanged.
// TestDebugCleanWithDatabaseSendsTransactionalScript 用于验证 FFI 会收到未被拆分的事务化清理脚本。
func TestDebugCleanWithDatabaseSendsTransactionalScript(t *testing.T) {
	var capturedSQL string
	database := &fakeSQLiteDatabase{
		executeScriptFunc: func(_ context.Context, request *fakeExecuteRequest) (*fakeExecuteResponse, error) {
			capturedSQL = request.GetSql()
			return &fakeExecuteResponse{Success: true}, nil
		},
	}
	if err := debugCleanWithDatabase(context.Background(), database, time.Second); err != nil {
		t.Fatalf("debugCleanWithDatabase returned error: %v", err)
	}
	if capturedSQL != debugCleanManagedSchemaSQL {
		t.Fatalf("captured debug clean sql = %q, want %q", capturedSQL, debugCleanManagedSchemaSQL)
	}
}

// TestSQLiteDebugCleanCommitErrorMarksOutcomeUncertain verifies commit-boundary gateway failures do not masquerade as ordinary cleanup errors.
// TestSQLiteDebugCleanCommitErrorMarksOutcomeUncertain 用于验证提交边界的网关失败不会伪装成普通清理错误。
func TestSQLiteDebugCleanCommitErrorMarksOutcomeUncertain(t *testing.T) {
	database := &fakeSQLiteDatabase{
		executeScriptFunc: func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
			return nil, errors.New("failed to commit sqlite transaction")
		},
	}
	err := debugCleanWithDatabase(context.Background(), database, time.Second)
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected sqlite debug-clean commit error to be outcome-uncertain, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect sqlite debug-clean uncertainty to mark fresh-vector retention, got %v", err)
	}
	if !strings.Contains(err.Error(), "debug clean sqlite schema: failed to commit sqlite transaction") {
		t.Fatalf("unexpected sqlite debug-clean error detail: %v", err)
	}
}
