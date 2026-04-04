// retention_store_test.go verifies SQLite retention helpers build the expected recycle and purge statements without regressing to partial hot-table deletes.
// retention_store_test.go 用于验证 SQLite retention 辅助逻辑会构造预期的回收与 purge 语句，避免回归成只删热表的半实现。
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

// TestRecycleColdMemoriesCopiesRowsIntoTrashAndDeletesFTS verifies SQLite recycle now writes both trash tables and clears the FTS mirror inside the same script.
// TestRecycleColdMemoriesCopiesRowsIntoTrashAndDeletesFTS 用于验证 SQLite 回收现在会同时写入两张回收站表，并在同一脚本里清理 FTS 镜像。
func TestRecycleColdMemoriesCopiesRowsIntoTrashAndDeletesFTS(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	var capturedSQL string
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "memory_status IN"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[
{"id":11,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":6,"vector_id":"vec-11","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":1,"category":2,"abstract":"阶段已变更","details":"项目阶段从 A 进入 B","memory_status":1,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":0,"rebuttal_count":0,"status_reason":"superseded","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[
{"memory_id":11,"context_key":"phase","context_value":"delivery","support_count":1,"rebuttal_count":0,"last_supported_timestamp":1,"last_rebutted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"next_id":7}]`}, nil
		default:
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		capturedSQL = req.GetSql()
		return &sqlitev1.ExecuteResponse{Success: true}, nil
	}

	result, err := store.RecycleColdMemories(context.Background(), logicdomain.MemoryRecycleQuery{
		Limit:         32,
		RecycledAt:    time.Unix(100, 0).UTC(),
		RecycleReason: "terminal memory recycle",
	})
	if err != nil {
		t.Fatalf("RecycleColdMemories returned error: %v", err)
	}
	if result.BatchID != 7 {
		t.Fatalf("batch id = %d, want 7", result.BatchID)
	}
	if result.RecycledMemoryCount != 1 || result.RecycledContextCount != 1 {
		t.Fatalf("recycle counts = %+v", result)
	}
	if len(result.RecycledVectorIDs) != 1 || result.RecycledVectorIDs[0] != "vec-11" {
		t.Fatalf("vector ids = %v", result.RecycledVectorIDs)
	}
	for _, fragment := range []string{
		"INSERT INTO vmm_memory_nodes_trash",
		"INSERT INTO vmm_memory_context_edges_trash",
		"DELETE FROM vmm_memory_nodes_fts",
		"DELETE FROM vmm_memory_nodes",
		"COMMIT;",
	} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected recycle sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
}

// TestRecycleIdleSessionsMovesStaleSessionRowsIntoTrash verifies idle-session recycle copies stale session memories and eligible turns into the trash tables, while still clearing the hot FTS mirror.
// TestRecycleIdleSessionsMovesStaleSessionRowsIntoTrash 用于验证 idle-session 回收会把陈旧 session 记忆和可归档 turn 迁入回收站，同时清理热表 FTS 镜像。
func TestRecycleIdleSessionsMovesStaleSessionRowsIntoTrash(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	var capturedSQL string
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[
{"id":21,"session_key":"sess-idle","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "origin_session_id = ?"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[
{"id":31,"team_id":2,"space_id":3,"project_id":4,"user_id":1,"origin_session_id":21,"source_turn_id":41,"vector_id":"vec-31","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"短期待办已过期","details":"该 session 里的临时 TODO 已失效","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count FROM vmm_memory_context_edges"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records tr"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[
{"id":41,"session_id":21,"project_id":4,"dehydrated_content":"{\"user\":\"old\"}","dehydrated_budget":5,"extracted_status":1,"details":"旧轮次摘要","details_budget":2,"created_timestamp":10,"updated_timestamp":20}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"next_id":9}]`}, nil
		default:
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		capturedSQL = req.GetSql()
		return &sqlitev1.ExecuteResponse{Success: true}, nil
	}

	result, err := store.RecycleIdleSessions(context.Background(), logicdomain.SessionIdleRecycleQuery{
		Limit:             8,
		RecycledAt:        time.Unix(300, 0).UTC(),
		IdleBefore:        time.Unix(200, 0).UTC(),
		TurnHotWindowSize: 8,
		RecycleReason:     "idle session recycle",
	})
	if err != nil {
		t.Fatalf("RecycleIdleSessions returned error: %v", err)
	}
	if len(result.BatchIDs) != 1 || result.BatchIDs[0] != 9 {
		t.Fatalf("batch ids = %v", result.BatchIDs)
	}
	if len(result.SessionIDs) != 1 || result.SessionIDs[0] != 21 {
		t.Fatalf("session ids = %v", result.SessionIDs)
	}
	if result.RecycledMemoryCount != 1 || result.RecycledContextCount != 2 || result.RecycledTurnCount != 1 {
		t.Fatalf("idle-session recycle counts = %+v", result)
	}
	if len(result.RecycledVectorIDs) != 1 || result.RecycledVectorIDs[0] != "vec-31" {
		t.Fatalf("idle-session vector ids = %v", result.RecycledVectorIDs)
	}
	for _, fragment := range []string{
		"INSERT INTO vmm_memory_nodes_trash",
		"INSERT INTO vmm_turn_records_trash",
		"DELETE FROM vmm_memory_nodes_fts",
		"DELETE FROM vmm_turn_records",
		"VALUES (9, 'session_idle_recycle', 21, 4",
		"COMMIT;",
	} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected idle-session recycle sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
}

// TestPurgeExpiredTrashDeletesAllTrashTablesAndMarksBatchesPurged verifies SQLite purge now hard-deletes old trash rows from every trash table and records the batch purge timestamp.
// TestPurgeExpiredTrashDeletesAllTrashTablesAndMarksBatchesPurged 用于验证 SQLite purge 现在会从每张回收站表硬删除过期行，并记录批次 purge 时间戳。
func TestPurgeExpiredTrashDeletesAllTrashTablesAndMarksBatchesPurged(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	var capturedSQL string
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_recycle_batches"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"id":7},{"id":8}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes_trash"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges_trash"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"count":3}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records_trash"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"count":4}]`}, nil
		default:
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		capturedSQL = req.GetSql()
		return &sqlitev1.ExecuteResponse{Success: true}, nil
	}

	result, err := store.PurgeExpiredTrash(context.Background(), time.Unix(200, 0).UTC(), 16)
	if err != nil {
		t.Fatalf("PurgeExpiredTrash returned error: %v", err)
	}
	if len(result.BatchIDs) != 2 || result.BatchIDs[0] != 7 || result.BatchIDs[1] != 8 {
		t.Fatalf("purged batch ids = %v", result.BatchIDs)
	}
	if result.PurgedMemoryCount != 2 || result.PurgedContextCount != 3 || result.PurgedTurnCount != 4 {
		t.Fatalf("purge counts = %+v", result)
	}
	for _, fragment := range []string{
		"DELETE FROM vmm_memory_context_edges_trash",
		"DELETE FROM vmm_memory_nodes_trash",
		"DELETE FROM vmm_turn_records_trash",
		"UPDATE vmm_recycle_batches",
		"SET purged_at =",
		"COMMIT;",
	} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected purge sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
}
