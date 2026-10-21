// SQLite adapter tests for store test.
// SQLite 适配层执行与启动测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	"context"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
	"strings"
	"testing"
	"time"
)

// TestStoreExecRetriesRetryableSQLiteErrors verifies SQLITE_BUSY style errors are retried inside the adapter before surfacing.
// TestStoreExecRetriesRetryableSQLiteErrors 用于验证 SQLITE_BUSY 这类错误会先在适配器内部重试，而不是立刻向外暴露。
func TestStoreExecRetriesRetryableSQLiteErrors(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	calls := 0
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		calls++
		if calls == 1 {
			return nil, fmt.Errorf("SQLITE_BUSY: database is locked")
		}
		if strings.TrimSpace(req.GetParamsJson()) != "" {
			t.Fatalf("expected retry attempt to keep typed params, got params_json=%q", req.GetParamsJson())
		}
		return &fakeExecuteResponse{Success: true}, nil
	}

	if err := store.exec(context.Background(), `UPDATE vmm_users SET profile = ? WHERE id = ?`, "vmm", 1); err != nil {
		t.Fatalf("exec returned error after retry: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected one retry and two total calls, got %d", calls)
	}
}

// TestStoreSeedDebugWorkspaceIfEmptyUsesTypedSQLiteParams verifies fresh-workspace debug bootstrap inserts deterministic owner rows through FFI-supported single-statement typed calls.
// TestStoreSeedDebugWorkspaceIfEmptyUsesTypedSQLiteParams 用于验证全新工作区调试启动会通过 FFI 支持的单语句强类型调用插入确定性的 owner 行。
func TestStoreSeedDebugWorkspaceIfEmptyUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var countQuery *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		countQuery = req
		if !strings.Contains(req.GetSql(), "COUNT(*)") || !strings.Contains(req.GetSql(), "vmm_projects") {
			t.Fatalf("expected project-count query before debug seed, got %q", req.GetSql())
		}
		return &fakeQueryJSONResponse{JsonData: `[{"count":0}]`}, nil
	}

	// Capture every single-statement ExecuteScript request so the test follows the real FFI parameter boundary used during schema bootstrap.
	// 捕获每一次单语句 ExecuteScript 请求，让测试跟随 schema 启动期间真实使用的 FFI 参数边界。
	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	if err := store.seedDebugWorkspaceIfEmpty(context.Background()); err != nil {
		t.Fatalf("seedDebugWorkspaceIfEmpty returned error: %v", err)
	}
	if countQuery == nil {
		t.Fatal("expected project-count query to be captured")
	}
	if strings.TrimSpace(countQuery.GetParamsJson()) != "" {
		t.Fatalf("expected project-count query to avoid params_json, got %q", countQuery.GetParamsJson())
	}
	if len(requests) != 4 {
		t.Fatalf("expected four parameterized seed statement requests, got %d", len(requests))
	}
	for _, request := range requests {
		if strings.TrimSpace(request.GetParamsJson()) != "" {
			t.Fatalf("expected typed params only, got params_json=%q", request.GetParamsJson())
		}
	}

	capturedSQL := sqliteExecutedSQLLog(requests)
	for _, fragment := range []string{"INSERT INTO vmm_users", "INSERT INTO vmm_teams", "INSERT INTO vmm_spaces", "INSERT INTO vmm_projects"} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected debug seed SQL to contain %q, got %q", fragment, capturedSQL)
		}
	}
	for _, forbidden := range []string{"BEGIN IMMEDIATE;", "COMMIT;", "OR IGNORE"} {
		if strings.Contains(capturedSQL, forbidden) {
			t.Fatalf("expected debug seed SQL to stay single-statement per FFI call, got %q", capturedSQL)
		}
	}
	if strings.Contains(capturedSQL, logicdomain.DefaultWorkspaceResourceName) {
		t.Fatalf("default workspace name leaked into SQL text: %q", capturedSQL)
	}
	if got := countSQLiteStringParam(requests, debugSeedDefaultName); got != 4 {
		t.Fatalf("expected default workspace name as four string params, got %d", got)
	}

	stringTimes := make([]string, 0, 8)
	for _, request := range requests {
		for _, param := range request.GetParams() {
			if param.Kind == sqliteffi.SQLValueString && param.String != debugSeedDefaultName {
				stringTimes = append(stringTimes, param.String)
			}
		}
	}
	if len(stringTimes) != 8 {
		t.Fatalf("expected eight seed timestamp params, got %d: %#v", len(stringTimes), stringTimes)
	}
	for _, value := range stringTimes {
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			t.Fatalf("seed timestamp param is not RFC3339Nano: %q", value)
		}
	}

	params := make([]sqliteffi.SQLValue, 0, 19)
	for _, request := range requests {
		params = append(params, request.GetParams()...)
	}
	if len(params) != 19 {
		t.Fatalf("expected nineteen debug seed params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 != debugSeedUserID {
		t.Fatalf("unexpected user seed id param: %#v", params[0])
	}
	if params[13].Kind != sqliteffi.SQLValueInt64 || params[13].Int64 != debugSeedProjectID ||
		params[14].Kind != sqliteffi.SQLValueInt64 || params[14].Int64 != debugSeedTeamID ||
		params[15].Kind != sqliteffi.SQLValueInt64 || params[15].Int64 != debugSeedSpaceID {
		t.Fatalf("unexpected project seed id params: %#v", params[13:16])
	}
}
