// Behavioral tests grouped by retention purge test.
// SQLite 适配层回收站清理行为测试，沿用同包夹具验证对应业务链路。
package vldb_sqlite

import (
	"context"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"testing"
	"time"
)

// TestPurgeExpiredTrashDeletesAllTrashTablesAndBatchMetadata verifies purge deletes every trash table plus recycle-batch metadata.
// TestPurgeExpiredTrashDeletesAllTrashTablesAndBatchMetadata 用于验证 purge 会删除所有回收站表及批次元数据。
func TestPurgeExpiredTrashDeletesAllTrashTablesAndBatchMetadata(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	queries := make([]*fakeQueryRequest, 0, 4)
	executed := make([]*fakeExecuteRequest, 0, 4)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		queries = append(queries, req)
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"id":7},{"id":8}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes_trash"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges_trash"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":3}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records_trash"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":4}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "DELETE FROM vmm_memory_context_edges_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 3}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_nodes_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "DELETE FROM vmm_turn_records_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 4}, nil
		case strings.Contains(sql, "DELETE FROM vmm_recycle_batches"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		default:
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
	}

	result, err := store.PurgeExpiredTrash(context.Background(), time.Unix(200, 0).UTC(), 16)
	if err != nil {
		t.Fatalf("PurgeExpiredTrash returned error: %v", err)
	}
	if len(result.BatchIDs) != 2 || result.PurgedMemoryCount != 2 || result.PurgedContextCount != 3 || result.PurgedTurnCount != 4 {
		t.Fatalf("unexpected purge result: %+v", result)
	}
	if len(queries) != 4 {
		t.Fatalf("expected one batch selection and three typed count queries, got %d", len(queries))
	}
	for _, query := range queries[1:] {
		sql := query.GetSql()
		if !strings.Contains(sql, "batch_id IN (?,?)") {
			t.Fatalf("expected typed batch placeholders in count SQL, got %q", sql)
		}
		if strings.Contains(sql, "7") || strings.Contains(sql, "8") {
			t.Fatalf("expected batch ids to stay out of count SQL text, got %q", sql)
		}
		requireSQLiteInt64Params(t, query.GetParams(), 7, 8)
	}
	if len(executed) != 4 {
		t.Fatalf("expected four ordered purge delete statements, got %d", len(executed))
	}
	requireNoSQLiteTransactionControl(t, executed)
	capturedSQL := sqliteExecutedSQLLog(executed)
	for _, fragment := range []string{"DELETE FROM vmm_memory_context_edges_trash", "DELETE FROM vmm_memory_nodes_trash", "DELETE FROM vmm_turn_records_trash", "DELETE FROM vmm_recycle_batches"} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected purge sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
	for _, request := range executed {
		sql := request.GetSql()
		if !strings.Contains(sql, "IN (?,?)") {
			t.Fatalf("expected typed batch placeholders in purge SQL, got %q", sql)
		}
		if strings.Contains(sql, "7") || strings.Contains(sql, "8") {
			t.Fatalf("expected batch ids to stay out of purge SQL text, got %q", sql)
		}
		requireSQLiteInt64Params(t, request.GetParams(), 7, 8)
	}
}

// TestPurgeExpiredTrashRejectsPartialBatchMetadataDelete verifies permanent purge cannot silently leave selected batch metadata behind.
// TestPurgeExpiredTrashRejectsPartialBatchMetadataDelete 用于验证永久清理不能静默遗留已选批次元数据。
func TestPurgeExpiredTrashRejectsPartialBatchMetadataDelete(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"id":7},{"id":8}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes_trash"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges_trash"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":3}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records_trash"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":4}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "DELETE FROM vmm_memory_context_edges_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 3}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_nodes_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "DELETE FROM vmm_turn_records_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 4}, nil
		case strings.Contains(sql, "DELETE FROM vmm_recycle_batches"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		default:
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
	}

	_, err := store.PurgeExpiredTrash(context.Background(), time.Unix(200, 0).UTC(), 16)
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain purge drift, got %v", err)
	}
	if !strings.Contains(err.Error(), "delete sqlite recycle batch metadata affected 1 rows, want 2") {
		t.Fatalf("unexpected purge drift error: %v", err)
	}
}

// TestPurgeExpiredTrashRejectsInitialDeleteDriftAsOrdinary verifies the first purge delete remains an ordinary error when SQLite reports that no trash row changed.
// TestPurgeExpiredTrashRejectsInitialDeleteDriftAsOrdinary 用于验证首个 purge 删除未改变任何 trash 行时，会保持为普通错误。
func TestPurgeExpiredTrashRejectsInitialDeleteDriftAsOrdinary(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 1)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"id":7},{"id":8}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes_trash"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges_trash"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":3}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records_trash"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":4}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}

	_, err := store.PurgeExpiredTrash(context.Background(), time.Unix(200, 0).UTC(), 16)
	if err == nil {
		t.Fatal("expected initial purge delete drift to fail")
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect initial purge delete drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "delete sqlite memory-context trash rows affected 0 rows, want 3") {
		t.Fatalf("unexpected initial purge drift error: %v", err)
	}
	if len(executed) != 1 {
		t.Fatalf("expected purge to stop after first delete drifted, got %d requests", len(executed))
	}
}
