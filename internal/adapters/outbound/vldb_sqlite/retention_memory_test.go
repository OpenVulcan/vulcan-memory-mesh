// Behavioral tests grouped by retention memory test.
// SQLite 适配层冷记忆回收行为测试，沿用同包夹具验证对应业务链路。
package vldb_sqlite

import (
	"context"
	"errors"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
	"strings"
	"testing"
	"time"
)

// TestRecycleColdMemoriesCopiesRowsIntoTrash verifies recycle writes both trash tables and removes hot rows without touching the retired legacy FTS mirror.
// TestRecycleColdMemoriesCopiesRowsIntoTrash 用于验证回收会写入两张回收站表并删除热表数据，同时不会触碰已退役的旧 FTS 镜像表。
func TestRecycleColdMemoriesCopiesRowsIntoTrash(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	expectSQLiteFTSDeletes(t, fake, "11", "12")
	store := &Store{database: fake, timeout: time.Second}

	queries := make([]*fakeQueryRequest, 0, 3)
	executed := make([]*fakeExecuteRequest, 0, 7)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		queries = append(queries, req)
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "memory_status IN"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":11,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":6,"vector_id":"vec-11","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":1,"category":2,"abstract":"阶段已变更","details":"项目阶段从 A 进入 B","memory_status":1,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":0,"rebuttal_count":0,"status_reason":"superseded","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":1,"updated_timestamp":2},
{"id":12,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":7,"vector_id":"vec-12","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":1,"category":2,"abstract":"阶段继续变更","details":"项目阶段从 B 进入 C","memory_status":2,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":0,"rebuttal_count":0,"status_reason":"deleted","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":3,"updated_timestamp":4}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges"):
			return &fakeQueryJSONResponse{JsonData: `[
{"memory_id":11,"context_key":"phase","context_value":"delivery","support_count":1,"rebuttal_count":0,"last_supported_timestamp":1,"last_rebutted_timestamp":0,"created_timestamp":1,"updated_timestamp":2},
{"memory_id":12,"context_key":"phase","context_value":"rollout","support_count":1,"rebuttal_count":0,"last_supported_timestamp":3,"last_rebutted_timestamp":0,"created_timestamp":3,"updated_timestamp":4}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":7}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "INSERT INTO vmm_recycle_batches"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "INSERT INTO vmm_memory_nodes_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "INSERT INTO vmm_memory_context_edges_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_context_edges"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "INSERT INTO vmm_turn_records_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "DELETE FROM vmm_turn_records"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		}
		return &fakeExecuteResponse{Success: true}, nil
	}

	reason := "  terminal memory recycle '; DROP TABLE vmm_memory_nodes;  "
	result, err := store.RecycleColdMemories(context.Background(), logicdomain.MemoryRecycleQuery{
		Limit:         32,
		RecycledAt:    time.Unix(100, 0).UTC(),
		RecycleReason: reason,
	})
	if err != nil {
		t.Fatalf("RecycleColdMemories returned error: %v", err)
	}
	if result.BatchID != 7 || result.RecycledMemoryCount != 2 || result.RecycledContextCount != 2 {
		t.Fatalf("unexpected recycle result: %+v", result)
	}
	if len(queries) != 3 {
		t.Fatalf("expected candidate, context, and batch-id queries, got %d", len(queries))
	}
	contextQuery := queries[1]
	if !strings.Contains(contextQuery.GetSql(), "memory_id IN (?,?)") {
		t.Fatalf("expected typed memory placeholders in context query, got %q", contextQuery.GetSql())
	}
	if strings.Contains(contextQuery.GetSql(), "11") || strings.Contains(contextQuery.GetSql(), "12") {
		t.Fatalf("expected memory ids to stay out of context query SQL, got %q", contextQuery.GetSql())
	}
	requireSQLiteInt64Params(t, contextQuery.GetParams(), 11, 12)
	if len(executed) != 5 {
		t.Fatalf("expected five ordered cold-memory recycle statements, got %d", len(executed))
	}
	requireNoSQLiteTransactionControl(t, executed)
	capturedSQL := sqliteExecutedSQLLog(executed)
	for _, fragment := range []string{"INSERT INTO vmm_memory_nodes_trash", "INSERT INTO vmm_memory_context_edges_trash", "DELETE FROM vmm_memory_nodes"} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected recycle sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
	if strings.Contains(capturedSQL, "vmm_memory_nodes_fts") {
		t.Fatalf("expected recycle sql to stop referencing retired legacy FTS table, got %q", capturedSQL)
	}
	if strings.Contains(capturedSQL, "DROP TABLE") || strings.Contains(capturedSQL, "terminal memory recycle") {
		t.Fatalf("expected recycle reason to stay out of SQL text, got %q", capturedSQL)
	}
	for _, request := range executed[1:] {
		if !strings.Contains(request.GetSql(), "IN (?,?)") {
			t.Fatalf("expected typed memory placeholders in recycle SQL, got %q", request.GetSql())
		}
		if strings.Contains(request.GetSql(), "11") || strings.Contains(request.GetSql(), "12") {
			t.Fatalf("expected memory ids to stay out of recycle SQL text, got %q", request.GetSql())
		}
	}
	requireSQLiteInt64Params(t, executed[1].GetParams()[3:], 11, 12)
	requireSQLiteInt64Params(t, executed[2].GetParams()[3:], 11, 12)
	requireSQLiteInt64Params(t, executed[3].GetParams(), 11, 12)
	requireSQLiteInt64Params(t, executed[4].GetParams(), 11, 12)
	if got := countSQLiteStringParam(executed, strings.TrimSpace(reason)); got != 3 {
		t.Fatalf("expected recycle reason to be bound three times, got %d", got)
	}
}

// installColdMemoryRecycleQueryFixtures wires deterministic QueryJSON responses for two terminal memories and two context edges used by cold-memory recycle drift tests.
// installColdMemoryRecycleQueryFixtures 用于为 cold-memory 回收漂移测试安装两个终态 memory 和两个 context edge 的确定性 QueryJSON 响应。
// The fake parameter receives the query fixture callbacks and the helper returns no value.
// fake 参数接收查询夹具回调；该辅助函数无返回值。
func installColdMemoryRecycleQueryFixtures(fake *fakeSQLiteDatabase) {
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "memory_status IN"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":11,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":6,"vector_id":"vec-11","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":1,"category":2,"abstract":"phase changed","details":"project moved from A to B","memory_status":1,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":0,"rebuttal_count":0,"status_reason":"superseded","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":1,"updated_timestamp":2},
{"id":12,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":7,"vector_id":"vec-12","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":1,"category":2,"abstract":"phase changed again","details":"project moved from B to C","memory_status":2,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":0,"rebuttal_count":0,"status_reason":"deleted","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":3,"updated_timestamp":4}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges"):
			return &fakeQueryJSONResponse{JsonData: `[
{"memory_id":11,"context_key":"phase","context_value":"delivery","support_count":1,"rebuttal_count":0,"last_supported_timestamp":1,"last_rebutted_timestamp":0,"created_timestamp":1,"updated_timestamp":2},
{"memory_id":12,"context_key":"phase","context_value":"rollout","support_count":1,"rebuttal_count":0,"last_supported_timestamp":3,"last_rebutted_timestamp":0,"created_timestamp":3,"updated_timestamp":4}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":7}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
}

// TestRecycleColdMemoriesRejectsTrashCopyRowsChangedDrift verifies hot memories are not deleted when the memory-trash audit copy misses selected rows.
// TestRecycleColdMemoriesRejectsTrashCopyRowsChangedDrift 用于验证 memory-trash 审计复制漏掉已选行时，不会继续删除热表 memory。
func TestRecycleColdMemoriesRejectsTrashCopyRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}
	installColdMemoryRecycleQueryFixtures(fake)

	// Report a partial trash copy after the batch row exists so the store must stop before destructive hot-table deletes.
	// 在 batch 行已经存在后报告不完整 trash 复制，要求存储层在破坏性热表删除前停止。
	executed := make([]*fakeExecuteRequest, 0, 2)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "INSERT INTO vmm_recycle_batches"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "INSERT INTO vmm_memory_nodes_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		default:
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
	}

	_, err := store.RecycleColdMemories(context.Background(), logicdomain.MemoryRecycleQuery{
		Limit:      32,
		RecycledAt: time.Unix(100, 0).UTC(),
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected partial memory trash copy to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "copy sqlite cold memories to trash affected 1 rows, want 2") {
		t.Fatalf("unexpected memory trash drift error: %v", err)
	}
	if len(executed) != 2 {
		t.Fatalf("expected batch insert and memory trash copy only, got %d", len(executed))
	}
	for _, request := range executed {
		if strings.Contains(request.GetSql(), "DELETE FROM vmm_memory_nodes") {
			t.Fatalf("did not expect hot memory delete after trash copy drift: %q", request.GetSql())
		}
	}
}

// TestRecycleColdMemoriesRejectsContextDeleteRowsChangedDrift verifies hot memories remain in place when copied context edges cannot be removed from the hot table exactly.
// TestRecycleColdMemoriesRejectsContextDeleteRowsChangedDrift 用于验证已复制的 context edge 无法从热表精确删除时，热表 memory 会继续保留。
func TestRecycleColdMemoriesRejectsContextDeleteRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}
	installColdMemoryRecycleQueryFixtures(fake)

	// Let both trash copies succeed, then drift the context hot-table delete so the later memory delete must not run.
	// 让两类 trash 复制都成功，再让 context 热表删除漂移，要求后续 memory 删除不得执行。
	executed := make([]*fakeExecuteRequest, 0, 4)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "INSERT INTO vmm_recycle_batches"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "INSERT INTO vmm_memory_nodes_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "INSERT INTO vmm_memory_context_edges_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_context_edges"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		default:
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
	}

	_, err := store.RecycleColdMemories(context.Background(), logicdomain.MemoryRecycleQuery{
		Limit:      32,
		RecycledAt: time.Unix(100, 0).UTC(),
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected partial context delete to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "delete recycled sqlite cold memory contexts affected 1 rows, want 2") {
		t.Fatalf("unexpected context delete drift error: %v", err)
	}
	if len(executed) != 4 {
		t.Fatalf("expected recycle to stop before hot memory delete, got %d statements", len(executed))
	}
	for _, request := range executed {
		if strings.Contains(request.GetSql(), "DELETE FROM vmm_memory_nodes") {
			t.Fatalf("did not expect hot memory delete after context delete drift: %q", request.GetSql())
		}
	}
}

// TestRecycleColdMemoriesReturnsOutcomeUncertainWhenHotDeleteErrors verifies a hot-memory delete execution failure is not reported as a clean recycle failure after trash copies exist.
// TestRecycleColdMemoriesReturnsOutcomeUncertainWhenHotDeleteErrors 用于验证 trash 副本已存在后，热表 memory 删除执行错误不会被上报为干净的回收失败。
func TestRecycleColdMemoriesReturnsOutcomeUncertainWhenHotDeleteErrors(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}
	installColdMemoryRecycleQueryFixtures(fake)

	// Let all audit copies and context cleanup succeed, then fail the final hot-memory delete to simulate an uncertain destructive boundary.
	// 让所有审计复制和 context 清理成功，再让最后的热表 memory 删除失败，用于模拟破坏性边界结果不确定。
	executed := make([]*fakeExecuteRequest, 0, 5)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "INSERT INTO vmm_recycle_batches"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "INSERT INTO vmm_memory_nodes_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "INSERT INTO vmm_memory_context_edges_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_context_edges"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_nodes"):
			return &fakeExecuteResponse{Success: false, Message: "delete failed after apply"}, nil
		default:
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
	}

	_, err := store.RecycleColdMemories(context.Background(), logicdomain.MemoryRecycleQuery{
		Limit:      32,
		RecycledAt: time.Unix(100, 0).UTC(),
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected hot memory delete error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "delete recycled sqlite cold memories") || !strings.Contains(err.Error(), "delete failed after apply") {
		t.Fatalf("unexpected hot memory delete error: %v", err)
	}
	if len(executed) != 5 {
		t.Fatalf("expected recycle to reach hot memory delete, got %d statements", len(executed))
	}
}

// TestRecycleColdMemoriesReturnsCleanupCoordinatesWhenFTSSyncFails verifies vector cleanup coordinates survive a late FTS synchronization failure after hot rows have already left the active table.
// TestRecycleColdMemoriesReturnsCleanupCoordinatesWhenFTSSyncFails 用于验证热表行已经离开 active 表后，即使后置 FTS 同步失败，也会保留向量清理坐标。
func TestRecycleColdMemoriesReturnsCleanupCoordinatesWhenFTSSyncFails(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}
	installColdMemoryRecycleQueryFixtures(fake)

	// Let the relational recycle finish completely, then fail the first FTS delete so the caller can still clean obsolete sidecar vectors.
	// 让关系侧回收完整完成，再让首个 FTS 删除失败，以便调用方仍能清理已过时的旁路向量。
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "INSERT INTO vmm_recycle_batches"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "INSERT INTO vmm_memory_nodes_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "INSERT INTO vmm_memory_context_edges_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_context_edges"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		default:
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
	}
	deletedFTSIDs := make([]string, 0, 2)
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{}, errors.New("fts delete unavailable")
	}

	result, err := store.RecycleColdMemories(context.Background(), logicdomain.MemoryRecycleQuery{
		Limit:      32,
		RecycledAt: time.Unix(100, 0).UTC(),
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected FTS sync failure to be outcome-uncertain, got %v", err)
	}
	if result.BatchID != 7 || result.RecycledMemoryCount != 2 || result.RecycledContextCount != 2 {
		t.Fatalf("unexpected recycle result after FTS failure: %+v", result)
	}
	if len(result.RecycledVectorIDs) != 2 || result.RecycledVectorIDs[0] != "vec-11" || result.RecycledVectorIDs[1] != "vec-12" {
		t.Fatalf("expected vector cleanup ids to survive FTS failure, got %+v", result.RecycledVectorIDs)
	}
	if len(deletedFTSIDs) != 1 || deletedFTSIDs[0] != "11" {
		t.Fatalf("expected first FTS delete attempt to fail, got %+v", deletedFTSIDs)
	}
}

// TestRecycleColdMemoriesRejectsPartialHotDeleteBeforeVectorCleanup verifies cold recycle cannot return vector ids when hot memory deletion drifts.
// TestRecycleColdMemoriesRejectsPartialHotDeleteBeforeVectorCleanup 用于验证 cold memory 回收删除热表行发生漂移时，不会返回 vector 清理坐标。
func TestRecycleColdMemoriesRejectsPartialHotDeleteBeforeVectorCleanup(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 5)
	deletedFTSIDs := make([]string, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "memory_status IN"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":11,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":6,"vector_id":"vec-11","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":1,"category":2,"abstract":"阶段已变更","details":"项目阶段从 A 进入 B","memory_status":1,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":0,"rebuttal_count":0,"status_reason":"superseded","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":1,"updated_timestamp":2},
{"id":12,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":7,"vector_id":"vec-12","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":1,"category":2,"abstract":"阶段继续变更","details":"项目阶段从 B 进入 C","memory_status":2,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":0,"rebuttal_count":0,"status_reason":"deleted","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":3,"updated_timestamp":4}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":7}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "INSERT INTO vmm_recycle_batches"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "INSERT INTO vmm_memory_nodes_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "INSERT INTO vmm_memory_context_edges_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_context_edges"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		}
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	_, err := store.RecycleColdMemories(context.Background(), logicdomain.MemoryRecycleQuery{
		Limit:      32,
		RecycledAt: time.Unix(100, 0).UTC(),
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected partial hot memory delete to be outcome-uncertain, got %v", err)
	}
	if len(executed) != 5 {
		t.Fatalf("expected five recycle statements before failure, got %d", len(executed))
	}
	if len(deletedFTSIDs) != 0 {
		t.Fatalf("did not expect FTS cleanup after partial hot delete, got %+v", deletedFTSIDs)
	}
}
