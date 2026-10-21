// Behavioral tests grouped by retention store test.
// SQLite 适配层保留流程的公共夹具行为测试，沿用同包夹具验证对应业务链路。
package vldb_sqlite

import (
	"context"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
	"strings"
	"testing"
	"time"
)

// sqliteExecutedSQLLog joins captured ExecuteScript SQL texts so tests can assert write content after operations are split into typed single statements.
// sqliteExecutedSQLLog 用于拼接捕获到的 ExecuteScript SQL 文本，让测试在写入拆成强类型单语句后仍能断言写入内容。
func sqliteExecutedSQLLog(requests []*fakeExecuteRequest) string {
	parts := make([]string, 0, len(requests))
	for _, request := range requests {
		parts = append(parts, request.GetSql())
	}
	return strings.Join(parts, "\n")
}

// countSQLiteStringParam counts exact string params across captured ExecuteScript requests.
// countSQLiteStringParam 用于统计捕获到的 ExecuteScript 请求中精确匹配的字符串参数数量。
func countSQLiteStringParam(requests []*fakeExecuteRequest, value string) int {
	count := 0
	for _, request := range requests {
		for _, param := range request.GetParams() {
			if param.Kind == sqliteffi.SQLValueString && param.String == value {
				count++
			}
		}
	}
	return count
}

// requireNoSQLiteTransactionControl verifies typed split writes do not rely on transaction control that the real FFI cannot preserve across ExecuteScript calls.
// requireNoSQLiteTransactionControl 用于验证强类型拆分写入不会依赖真实 FFI 无法跨 ExecuteScript 调用保留的事务控制。
func requireNoSQLiteTransactionControl(t *testing.T, requests []*fakeExecuteRequest) {
	t.Helper()
	for _, request := range requests {
		sql := strings.TrimSpace(request.GetSql())
		for _, forbidden := range []string{"BEGIN IMMEDIATE;", "COMMIT;", "ROLLBACK;"} {
			if strings.Contains(sql, forbidden) {
				t.Fatalf("expected typed split write to avoid %q, got %q", forbidden, sql)
			}
		}
	}
}

// requireSQLiteInt64Params verifies captured SQLite params match one exact int64 sequence.
// requireSQLiteInt64Params 用于验证捕获到的 SQLite 参数与指定 int64 序列完全一致。
func requireSQLiteInt64Params(t *testing.T, params []sqliteffi.SQLValue, values ...int64) {
	t.Helper()
	if len(params) != len(values) {
		t.Fatalf("expected %d int64 params, got %d: %#v", len(values), len(params), params)
	}
	for idx, value := range values {
		if params[idx].Kind != sqliteffi.SQLValueInt64 || params[idx].Int64 != value {
			t.Fatalf("expected int64 param %d to be %d, got %#v", idx, value, params[idx])
		}
	}
}

// TestExecWriteStatementsStopsOnStatementFailure verifies ordered typed writes stop at the first failed statement without emitting unsupported cross-call transaction control.
// TestExecWriteStatementsStopsOnStatementFailure 用于验证顺序强类型写入会在首个失败语句处停止，并且不会发出不受支持的跨调用事务控制。
func TestExecWriteStatementsStopsOnStatementFailure(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0, 4)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		executedSQL = append(executedSQL, sql)
		switch {
		case strings.Contains(sql, "INSERT INTO broken_table"):
			return &fakeExecuteResponse{Success: false, Message: "statement failed"}, nil
		default:
			return &fakeExecuteResponse{Success: true}, nil
		}
	}

	err := store.execWriteStatements(context.Background(),
		sqliteWriteStatement{SQL: "UPDATE ok_table SET value = ?;", Params: []any{1}},
		sqliteWriteStatement{SQL: "INSERT INTO broken_table(value) VALUES (?);", Params: []any{"bad"}},
	)
	if err == nil {
		t.Fatal("expected ordered write failure")
	}
	for _, fragment := range []string{"execute sqlite write statement 2", "statement failed"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("expected ordered write error to contain %q, got %q", fragment, err.Error())
		}
	}
	if got := strings.Join(executedSQL, "|"); got != "UPDATE ok_table SET value = ?;|INSERT INTO broken_table(value) VALUES (?);" {
		t.Fatalf("unexpected ordered write execution order: %s", got)
	}
}

// TestSQLiteAutocommitRowsChangedDriftErrorClassifiesMutation verifies single-statement queue drift is ordinary when no row changed and uncertain after partial mutation.
// TestSQLiteAutocommitRowsChangedDriftErrorClassifiesMutation 用于验证单语句队列漂移在未改变任何行时是普通错误，而发生局部突变后才是不确定错误。
func TestSQLiteAutocommitRowsChangedDriftErrorClassifiesMutation(t *testing.T) {
	zeroErr := sqliteAutocommitRowsChangedDriftError("complete vector gc jobs", "complete sqlite vector gc jobs", 0, 2)
	if zeroErr == nil {
		t.Fatal("expected zero-row drift to fail")
	}
	if logicdomain.IsOutcomeUncertain(zeroErr) {
		t.Fatalf("did not expect zero-row drift to be outcome-uncertain, got %v", zeroErr)
	}
	if !strings.Contains(zeroErr.Error(), "complete sqlite vector gc jobs affected 0 rows, want 2") {
		t.Fatalf("unexpected zero-row drift error: %v", zeroErr)
	}

	partialErr := sqliteAutocommitRowsChangedDriftError("retry recycle jobs", "retry sqlite recycle jobs", 1, 2)
	if !logicdomain.IsOutcomeUncertain(partialErr) {
		t.Fatalf("expected partial row drift to be outcome-uncertain, got %v", partialErr)
	}
	if !strings.Contains(partialErr.Error(), "retry sqlite recycle jobs affected 1 rows, want 2") {
		t.Fatalf("unexpected partial row drift error: %v", partialErr)
	}
}
