// scratchpad_test.go verifies the sqlite-first DWM adapter keeps plan GC and deterministic SQL filters aligned with the isolated scratchpad contract.
// scratchpad_test.go 用于验证 sqlite-first DWM 适配器会让计划 GC 和确定性 SQL 过滤条件持续符合隔离 scratchpad 契约。
package vldb_sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	sqlitev1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite/proto/v1"
	"google.golang.org/grpc"
)

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
	if len(capturedSQL) != 2 {
		t.Fatalf("expected two delete scripts, got %d: %q", len(capturedSQL), capturedSQL)
	}
	if !strings.Contains(capturedSQL[0], "DELETE FROM vmm_scratchpad_nodes WHERE plan_id IN (?)") {
		t.Fatalf("expected node delete to filter by plan_id, got %q", capturedSQL[0])
	}
	if !strings.Contains(capturedSQL[1], "DELETE FROM vmm_scratchpad_plans WHERE id IN (?)") {
		t.Fatalf("expected plan delete to filter by id, got %q", capturedSQL[1])
	}
	if strings.Contains(capturedSQL[1], "DELETE FROM vmm_scratchpad_plans WHERE plan_id IN") {
		t.Fatalf("plan delete still targets plan_id: %q", capturedSQL[1])
	}
}
