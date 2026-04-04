// retention_store_test.go verifies SQLite cold-memory recycle helpers build the expected trash-copy and purge statements without regressing to partial hot-table deletes.
// retention_store_test.go 用于验证 SQLite 冷记忆回收辅助逻辑会构造预期的 trash 复制与 purge 语句，避免回归成只删热表的半实现。
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
		case strings.Contains(sql, "FROM vmm_recycle_batches"):
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

// TestPurgeExpiredMemoryTrashDeletesTrashTablesAndMarksBatchesPurged verifies SQLite purge now hard-deletes old trash rows and records the batch purge timestamp.
// TestPurgeExpiredMemoryTrashDeletesTrashTablesAndMarksBatchesPurged 用于验证 SQLite purge 现在会硬删除过期回收站行，并记录批次 purge 时间戳。
func TestPurgeExpiredMemoryTrashDeletesTrashTablesAndMarksBatchesPurged(t *testing.T) {
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
		default:
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		capturedSQL = req.GetSql()
		return &sqlitev1.ExecuteResponse{Success: true}, nil
	}

	result, err := store.PurgeExpiredMemoryTrash(context.Background(), time.Unix(200, 0).UTC(), 16)
	if err != nil {
		t.Fatalf("PurgeExpiredMemoryTrash returned error: %v", err)
	}
	if len(result.BatchIDs) != 2 || result.BatchIDs[0] != 7 || result.BatchIDs[1] != 8 {
		t.Fatalf("purged batch ids = %v", result.BatchIDs)
	}
	if result.PurgedMemoryCount != 2 || result.PurgedContextCount != 3 {
		t.Fatalf("purge counts = %+v", result)
	}
	for _, fragment := range []string{
		"DELETE FROM vmm_memory_context_edges_trash",
		"DELETE FROM vmm_memory_nodes_trash",
		"UPDATE vmm_recycle_batches",
		"SET purged_at =",
		"COMMIT;",
	} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected purge sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
}
