// scratchpad_test.go verifies scratchpad SQL helpers stay atomic after the storage path became local FFI only.
// scratchpad_test.go 用于验证在存储路径完全切到本地 FFI 后，scratchpad SQL 助手仍保持原子性。
package vldb_sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestUpsertScratchpadItemsUsesOneAtomicExecuteScript verifies scratchpad upsert emits one explicit transaction script.
// TestUpsertScratchpadItemsUsesOneAtomicExecuteScript 用于验证 scratchpad upsert 会发出单条显式事务脚本。
func TestUpsertScratchpadItemsUsesOneAtomicExecuteScript(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executeCalls := 0
	executeBatchCalls := 0
	capturedSQL := ""
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "SELECT id, plan_id, item_key, item_value"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_scratchpad_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":41}]`}, nil
		default:
			t.Fatalf("unexpected sqlite scratchpad upsert query: %q", sql)
			return nil, nil
		}
	}
	fake.executeBatchFunc = func(_ context.Context, _ *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		executeBatchCalls++
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executeCalls++
		capturedSQL = req.GetSql()
		return &fakeExecuteResponse{Success: true}, nil
	}

	result, err := store.UpsertScratchpadItems(context.Background(), 9, []logicdomain.ScratchpadItem{
		{Key: "方案", Value: "补齐并发首写保护"},
		{Key: "文件", Value: "internal/adapters/outbound/vldb_postgres/scratchpad.go"},
	}, time.Unix(100, 0).UTC())
	if err != nil {
		t.Fatalf("UpsertScratchpadItems returned error: %v", err)
	}
	if result.InsertedCount != 2 || result.UpdatedCount != 0 {
		t.Fatalf("unexpected upsert result: %+v", result)
	}
	if executeBatchCalls != 0 || executeCalls != 1 {
		t.Fatalf("expected one atomic ExecuteScript only, exec=%d batch=%d", executeCalls, executeBatchCalls)
	}
	for _, fragment := range []string{"BEGIN IMMEDIATE;", "INSERT INTO vmm_scratchpad_nodes", "UPDATE vmm_scratchpad_plans", "COMMIT;"} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected atomic upsert sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
}

// TestDeleteScratchpadItemsUsesOneAtomicExecuteScript verifies scratchpad delete uses one explicit transaction script.
// TestDeleteScratchpadItemsUsesOneAtomicExecuteScript 用于验证 scratchpad delete 会使用单条显式事务脚本。
func TestDeleteScratchpadItemsUsesOneAtomicExecuteScript(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executeCalls := 0
	capturedSQL := ""
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "SELECT COUNT(*) AS count") && strings.Contains(sql, "item_key IN"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":1}]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count") && strings.Contains(sql, "WHERE plan_id = ?") && !strings.Contains(sql, "item_key IN"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":3}]`}, nil
		default:
			t.Fatalf("unexpected sqlite scratchpad delete query: %q", sql)
			return nil, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executeCalls++
		capturedSQL = req.GetSql()
		return &fakeExecuteResponse{Success: true}, nil
	}

	result, err := store.DeleteScratchpadItems(context.Background(), 9, []string{"方案"}, time.Unix(100, 0).UTC())
	if err != nil {
		t.Fatalf("DeleteScratchpadItems returned error: %v", err)
	}
	if result.DeletedCount != 1 || result.RemainingCount != 2 {
		t.Fatalf("unexpected delete result: %+v", result)
	}
	if executeCalls != 1 {
		t.Fatalf("expected one atomic ExecuteScript call, got %d", executeCalls)
	}
	for _, fragment := range []string{"BEGIN IMMEDIATE;", "DELETE FROM vmm_scratchpad_nodes", "UPDATE vmm_scratchpad_plans", "COMMIT;"} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected atomic delete sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
}

// TestDeleteExpiredScratchpadSessionsDeletesPlanRowsByID verifies GC deletes node rows by plan_id and plan rows by id.
// TestDeleteExpiredScratchpadSessionsDeletesPlanRowsByID 用于验证 GC 会按 plan_id 删除节点行、按 id 删除计划行。
func TestDeleteExpiredScratchpadSessionsDeletesPlanRowsByID(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	capturedSQL := make([]string, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_scratchpad_plans") && strings.Contains(sql, "updated_timestamp <="):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":11,"project_id":9,"user_id":7,"session_key":"sess-1","plan_name":"USER_AUTH_PLAN","plan_name_norm":"user_auth_plan","created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count") && strings.Contains(sql, "FROM vmm_scratchpad_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":2}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		capturedSQL = append(capturedSQL, req.GetSql())
		return &fakeExecuteResponse{Success: true}, nil
	}

	result, err := store.DeleteExpiredScratchpadSessions(context.Background(), time.Unix(100, 0).UTC(), 8)
	if err != nil {
		t.Fatalf("DeleteExpiredScratchpadSessions returned error: %v", err)
	}
	if result.DeletedPlanCount != 1 || result.DeletedNodeCount != 2 {
		t.Fatalf("unexpected gc result: %+v", result)
	}
	if len(capturedSQL) != 1 {
		t.Fatalf("expected one atomic delete script, got %d: %q", len(capturedSQL), capturedSQL)
	}
	if !strings.Contains(capturedSQL[0], "DELETE FROM vmm_scratchpad_nodes WHERE plan_id IN (11)") ||
		!strings.Contains(capturedSQL[0], "DELETE FROM vmm_scratchpad_plans WHERE id IN (11)") {
		t.Fatalf("unexpected scratchpad gc sql: %q", capturedSQL[0])
	}
}
