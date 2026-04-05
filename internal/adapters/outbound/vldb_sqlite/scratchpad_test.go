// scratchpad_test.go verifies the sqlite-first DWM adapter keeps plan GC and deterministic SQL filters aligned with the isolated scratchpad contract.
// scratchpad_test.go 用于验证 sqlite-first DWM 适配器会让计划 GC 和确定性 SQL 过滤条件持续符合隔离 scratchpad 契约。
package vldb_sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	sqlitev1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite/proto/v1"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"google.golang.org/grpc"
)

// TestUpsertScratchpadItemsUsesOneAtomicExecuteScript verifies sqlite scratchpad upsert now emits one explicit transaction script, so node writes and parent plan timestamp refresh cannot diverge across separate RPC calls.
// TestUpsertScratchpadItemsUsesOneAtomicExecuteScript 用于验证 sqlite scratchpad upsert 现在会发出单条显式事务脚本，避免节点写入与父计划时间戳刷新分裂成多个 RPC。
func TestUpsertScratchpadItemsUsesOneAtomicExecuteScript(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	executeCalls := 0
	executeBatchCalls := 0
	capturedSQL := ""
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "SELECT id, plan_id, item_key, item_value"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_scratchpad_nodes"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"next_id":41}]`}, nil
		default:
			t.Fatalf("unexpected sqlite scratchpad upsert query: %q", sql)
			return nil, nil
		}
	}
	fake.executeBatchFunc = func(_ context.Context, _ *sqlitev1.ExecuteBatchRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteBatchResponse, error) {
		executeBatchCalls++
		return &sqlitev1.ExecuteBatchResponse{Success: true}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		executeCalls++
		capturedSQL = req.GetSql()
		return &sqlitev1.ExecuteResponse{Success: true}, nil
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
	if executeBatchCalls != 0 {
		t.Fatalf("expected upsert to avoid ExecuteBatch, got %d batch calls", executeBatchCalls)
	}
	if executeCalls != 1 {
		t.Fatalf("expected one atomic ExecuteScript call, got %d", executeCalls)
	}
	for _, fragment := range []string{
		"BEGIN IMMEDIATE;",
		"INSERT INTO vmm_scratchpad_nodes",
		"UPDATE vmm_scratchpad_plans",
		"SET updated_timestamp =",
		"COMMIT;",
	} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected atomic upsert sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
}

// TestDeleteScratchpadItemsUsesOneAtomicExecuteScript verifies sqlite scratchpad delete now uses one explicit transaction script, so key deletion and plan timestamp refresh share one all-or-nothing boundary.
// TestDeleteScratchpadItemsUsesOneAtomicExecuteScript 用于验证 sqlite scratchpad delete 现在会使用单条显式事务脚本，让 key 删除与计划时间戳刷新共享同一个全有或全无边界。
func TestDeleteScratchpadItemsUsesOneAtomicExecuteScript(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	executeCalls := 0
	capturedSQL := ""
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "SELECT COUNT(*) AS count") && strings.Contains(sql, "item_key IN"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"count":1}]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count") && strings.Contains(sql, "WHERE plan_id = ?") && !strings.Contains(sql, "item_key IN"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"count":3}]`}, nil
		default:
			t.Fatalf("unexpected sqlite scratchpad delete query: %q", sql)
			return nil, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		executeCalls++
		capturedSQL = req.GetSql()
		return &sqlitev1.ExecuteResponse{Success: true}, nil
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
	for _, fragment := range []string{
		"BEGIN IMMEDIATE;",
		"DELETE FROM vmm_scratchpad_nodes",
		"UPDATE vmm_scratchpad_plans",
		"COMMIT;",
	} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected atomic delete sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
}

// TestDeleteExpiredScratchpadSessionsDeletesPlanRowsByID verifies scratchpad GC deletes node rows by plan_id and plan rows by id, so the plan-table cleanup cannot silently target a non-existent plan_id column again.
// TestDeleteExpiredScratchpadSessionsDeletesPlanRowsByID 用于验证 scratchpad GC 会按 plan_id 删除节点行、按 id 删除计划行，避免计划表清理再次静默写到不存在的 plan_id 列上。
func TestDeleteExpiredScratchpadSessionsDeletesPlanRowsByID(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	capturedSQL := make([]string, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_scratchpad_plans") && strings.Contains(sql, "updated_timestamp <="):
			return &sqlitev1.QueryJsonResponse{JsonData: `[
{"id":11,"project_id":9,"user_id":7,"session_key":"sess-1","plan_name":"USER_AUTH_PLAN","plan_name_norm":"user_auth_plan","created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count") && strings.Contains(sql, "FROM vmm_scratchpad_nodes"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"count":2}]`}, nil
		default:
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		capturedSQL = append(capturedSQL, req.GetSql())
		return &sqlitev1.ExecuteResponse{Success: true}, nil
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
	if !strings.Contains(capturedSQL[0], "BEGIN IMMEDIATE;") {
		t.Fatalf("expected gc sql to start one explicit transaction, got %q", capturedSQL[0])
	}
	if !strings.Contains(capturedSQL[0], "DELETE FROM vmm_scratchpad_nodes WHERE plan_id IN (11)") {
		t.Fatalf("expected node delete to filter by plan_id, got %q", capturedSQL[0])
	}
	if !strings.Contains(capturedSQL[0], "DELETE FROM vmm_scratchpad_plans WHERE id IN (11)") {
		t.Fatalf("expected plan delete to filter by id, got %q", capturedSQL[0])
	}
	if strings.Contains(capturedSQL[0], "DELETE FROM vmm_scratchpad_plans WHERE plan_id IN") {
		t.Fatalf("plan delete still targets plan_id: %q", capturedSQL[0])
	}
	if !strings.Contains(capturedSQL[0], "COMMIT;") {
		t.Fatalf("expected gc sql to commit one atomic transaction, got %q", capturedSQL[0])
	}
}
