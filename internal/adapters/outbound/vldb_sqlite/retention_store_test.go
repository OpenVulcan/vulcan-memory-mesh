// retention_store_test.go verifies retention SQL helpers still archive, purge, and queue work correctly after the storage adapter became local FFI only.
// retention_store_test.go 用于验证在存储适配器完全切到本地 FFI 后，回收、清理与队列 SQL 助手仍能正确归档、清理并入队。
package vldb_sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
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

// TestRecycleColdMemoriesCopiesRowsIntoTrash verifies recycle writes both trash tables and removes hot rows without touching the retired legacy FTS mirror.
// TestRecycleColdMemoriesCopiesRowsIntoTrash 用于验证回收会写入两张回收站表并删除热表数据，同时不会触碰已退役的旧 FTS 镜像表。
func TestRecycleColdMemoriesCopiesRowsIntoTrash(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
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

// TestRecycleIdleSessionsMovesStaleSessionRowsIntoTrash verifies idle-session recycle moves stale memories and eligible turns into the trash tables.
// TestRecycleIdleSessionsMovesStaleSessionRowsIntoTrash 用于验证 idle-session 回收会把陈旧记忆和可归档 turn 迁入回收站。
func TestRecycleIdleSessionsMovesStaleSessionRowsIntoTrash(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	queries := make([]*fakeQueryRequest, 0, 5)
	executed := make([]*fakeExecuteRequest, 0, 8)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		queries = append(queries, req)
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":21,"session_key":"sess-idle","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "origin_session_id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":31,"team_id":2,"space_id":3,"project_id":4,"user_id":1,"origin_session_id":21,"source_turn_id":41,"vector_id":"vec-31","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"短期待办已过期","details":"该 session 里的临时 TODO 已失效","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20},
{"id":32,"team_id":2,"space_id":3,"project_id":4,"user_id":1,"origin_session_id":21,"source_turn_id":42,"vector_id":"vec-32","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":0,"category":4,"abstract":"短期待办二已过期","details":"该 session 里的第二条临时 TODO 已失效","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":11,"updated_timestamp":21}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count FROM vmm_memory_context_edges"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records tr"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":41,"session_id":21,"project_id":4,"dehydrated_content":"{\"user\":\"old\"}","dehydrated_budget":5,"extracted_status":1,"details":"旧轮次摘要","details_budget":2,"created_timestamp":10,"updated_timestamp":20},
{"id":42,"session_id":21,"project_id":4,"dehydrated_content":"{\"assistant\":\"old\"}","dehydrated_budget":4,"extracted_status":1,"details":"旧助手轮次摘要","details_budget":2,"created_timestamp":11,"updated_timestamp":21}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":9}]`}, nil
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

	reason := "  idle session recycle '; DROP TABLE vmm_sessions;  "
	result, err := store.RecycleIdleSessions(context.Background(), logicdomain.SessionIdleRecycleQuery{
		Limit:             8,
		RecycledAt:        time.Unix(300, 0).UTC(),
		IdleBefore:        time.Unix(200, 0).UTC(),
		TurnHotWindowSize: 8,
		RecycleReason:     reason,
	})
	if err != nil {
		t.Fatalf("RecycleIdleSessions returned error: %v", err)
	}
	if len(result.BatchIDs) != 1 || result.BatchIDs[0] != 9 {
		t.Fatalf("unexpected batch ids: %v", result.BatchIDs)
	}
	if result.RecycledMemoryCount != 2 || result.RecycledContextCount != 2 || result.RecycledTurnCount != 2 {
		t.Fatalf("unexpected idle-session recycle counts: %+v", result)
	}
	if len(queries) != 5 {
		t.Fatalf("expected session, memory, context-count, turn, and batch-id queries, got %d", len(queries))
	}
	contextCountQuery := queries[2]
	if !strings.Contains(contextCountQuery.GetSql(), "memory_id IN (?,?)") {
		t.Fatalf("expected typed memory placeholders in idle context count SQL, got %q", contextCountQuery.GetSql())
	}
	if strings.Contains(contextCountQuery.GetSql(), "31") || strings.Contains(contextCountQuery.GetSql(), "32") {
		t.Fatalf("expected memory ids to stay out of idle context count SQL, got %q", contextCountQuery.GetSql())
	}
	requireSQLiteInt64Params(t, contextCountQuery.GetParams(), 31, 32)
	turnQuery := queries[3]
	if !strings.Contains(turnQuery.GetSql(), "mn.id NOT IN (?,?)") {
		t.Fatalf("expected typed ignored memory placeholders in idle turn query, got %q", turnQuery.GetSql())
	}
	if !strings.Contains(turnQuery.GetSql(), "LIMIT ?") {
		t.Fatalf("expected idle turn hot-window limit to use typed placeholder, got %q", turnQuery.GetSql())
	}
	if strings.Contains(turnQuery.GetSql(), "31") || strings.Contains(turnQuery.GetSql(), "32") {
		t.Fatalf("expected memory ids to stay out of idle turn query SQL, got %q", turnQuery.GetSql())
	}
	requireSQLiteInt64Params(t, turnQuery.GetParams(), 21, 8, 21, int64(logicdomain.TurnExtractedStatusPending), 31, 32)
	if len(executed) != 7 {
		t.Fatalf("expected seven ordered idle-session recycle statements, got %d", len(executed))
	}
	requireNoSQLiteTransactionControl(t, executed)
	capturedSQL := sqliteExecutedSQLLog(executed)
	for _, fragment := range []string{"INSERT INTO vmm_memory_nodes_trash", "INSERT INTO vmm_turn_records_trash", "DELETE FROM vmm_turn_records"} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected idle-session recycle sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
	if strings.Contains(capturedSQL, "DROP TABLE") || strings.Contains(capturedSQL, "idle session recycle") {
		t.Fatalf("expected idle-session reason to stay out of SQL text, got %q", capturedSQL)
	}
	for _, request := range executed[1:5] {
		if !strings.Contains(request.GetSql(), "IN (?,?)") {
			t.Fatalf("expected typed memory placeholders in idle memory SQL, got %q", request.GetSql())
		}
		if strings.Contains(request.GetSql(), "31") || strings.Contains(request.GetSql(), "32") {
			t.Fatalf("expected memory ids to stay out of idle memory SQL text, got %q", request.GetSql())
		}
	}
	requireSQLiteInt64Params(t, executed[1].GetParams()[3:], 31, 32)
	requireSQLiteInt64Params(t, executed[2].GetParams()[3:], 31, 32)
	requireSQLiteInt64Params(t, executed[3].GetParams(), 31, 32)
	requireSQLiteInt64Params(t, executed[4].GetParams(), 31, 32)
	for _, request := range executed[5:] {
		if !strings.Contains(request.GetSql(), "IN (?,?)") {
			t.Fatalf("expected typed turn placeholders in idle turn SQL, got %q", request.GetSql())
		}
		if strings.Contains(request.GetSql(), "41") || strings.Contains(request.GetSql(), "42") {
			t.Fatalf("expected turn ids to stay out of idle turn SQL text, got %q", request.GetSql())
		}
	}
	requireSQLiteInt64Params(t, executed[5].GetParams()[3:], 41, 42)
	requireSQLiteInt64Params(t, executed[6].GetParams(), 41, 42)
	if got := countSQLiteStringParam(executed, strings.TrimSpace(reason)); got != 4 {
		t.Fatalf("expected idle-session reason to be bound four times, got %d", got)
	}
}

// TestRecycleIdleSessionsReturnsCleanupCoordinatesWhenFTSSyncFails verifies idle-session recycle returns archived vector ids when the late FTS cleanup fails after hot memories are gone.
// TestRecycleIdleSessionsReturnsCleanupCoordinatesWhenFTSSyncFails 用于验证 idle-session 回收在热表 memory 已删除后发生后置 FTS 清理失败时，仍会返回已归档向量 id。
func TestRecycleIdleSessionsReturnsCleanupCoordinatesWhenFTSSyncFails(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	// Provide one idle session with two expired session memories and no recyclable turns so the test isolates the memory FTS boundary.
	// 提供一个包含两条过期 session memory 且没有可回收 turn 的 idle session，让测试聚焦 memory FTS 边界。
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":21,"session_key":"sess-idle","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "origin_session_id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":31,"team_id":2,"space_id":3,"project_id":4,"user_id":1,"origin_session_id":21,"source_turn_id":41,"vector_id":"vec-31","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"expired todo","details":"expired temporary todo","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20},
{"id":32,"team_id":2,"space_id":3,"project_id":4,"user_id":1,"origin_session_id":21,"source_turn_id":42,"vector_id":"vec-32","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":0,"category":4,"abstract":"expired second todo","details":"expired second temporary todo","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":11,"updated_timestamp":21}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count FROM vmm_memory_context_edges"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":0}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records tr"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":9}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
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
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		default:
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
	}
	deletedFTSIDs := make([]string, 0, 2)
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{}, errors.New("idle fts delete unavailable")
	}

	result, err := store.RecycleIdleSessions(context.Background(), logicdomain.SessionIdleRecycleQuery{
		Limit:             8,
		RecycledAt:        time.Unix(300, 0).UTC(),
		IdleBefore:        time.Unix(200, 0).UTC(),
		TurnHotWindowSize: 8,
		RecycleReason:     logicdomain.RecycleReasonIdleSessionCompact,
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected idle-session FTS failure to be outcome-uncertain, got %v", err)
	}
	if len(result.BatchIDs) != 1 || result.BatchIDs[0] != 9 || len(result.SessionIDs) != 1 || result.SessionIDs[0] != 21 {
		t.Fatalf("unexpected idle recycle coordinates after FTS failure: %+v", result)
	}
	if result.RecycledMemoryCount != 2 || result.RecycledContextCount != 0 || result.RecycledTurnCount != 0 {
		t.Fatalf("unexpected idle recycle counts after FTS failure: %+v", result)
	}
	if len(result.RecycledVectorIDs) != 2 || result.RecycledVectorIDs[0] != "vec-31" || result.RecycledVectorIDs[1] != "vec-32" {
		t.Fatalf("expected idle vector cleanup ids to survive FTS failure, got %+v", result.RecycledVectorIDs)
	}
	if len(deletedFTSIDs) != 1 || deletedFTSIDs[0] != "31" {
		t.Fatalf("expected first idle FTS delete attempt to fail, got %+v", deletedFTSIDs)
	}
}

// TestRecycleIdleSessionsRejectsBatchInsertRowsChangedDriftAsOrdinary verifies idle-session recycle stops before trash writes when the first batch insert reports no row.
// TestRecycleIdleSessionsRejectsBatchInsertRowsChangedDriftAsOrdinary 用于验证 idle-session 回收的首个 batch 插入报告未写入时，会在写入 trash 前以普通错误停止。
func TestRecycleIdleSessionsRejectsBatchInsertRowsChangedDriftAsOrdinary(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 1)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":21,"session_key":"sess-idle","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "origin_session_id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":31,"team_id":2,"space_id":3,"project_id":4,"user_id":1,"origin_session_id":21,"source_turn_id":41,"vector_id":"vec-31","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"expired todo","details":"expired temporary todo","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count FROM vmm_memory_context_edges"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":0}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records tr"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":9}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		if !strings.Contains(req.GetSql(), "INSERT INTO vmm_recycle_batches") {
			t.Fatalf("expected recycle batch insert to be first and only write, got %q", req.GetSql())
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}

	_, err := store.RecycleIdleSessions(context.Background(), logicdomain.SessionIdleRecycleQuery{
		Limit:             8,
		RecycledAt:        time.Unix(300, 0).UTC(),
		IdleBefore:        time.Unix(200, 0).UTC(),
		TurnHotWindowSize: 8,
		RecycleReason:     logicdomain.RecycleReasonIdleSessionCompact,
	})
	if err == nil {
		t.Fatal("expected idle-session batch insert drift error")
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected first batch insert drift to stay ordinary before any mutation, got %v", err)
	}
	if !strings.Contains(err.Error(), "insert sqlite idle-session recycle batch") || !strings.Contains(err.Error(), "affected 0 rows, want 1") {
		t.Fatalf("expected batch insert row-count detail, got %v", err)
	}
	if len(executed) != 1 {
		t.Fatalf("expected only the batch insert write, got %d", len(executed))
	}
}

// TestRecycleIdleSessionsReturnsOutcomeUncertainWhenMemoryTrashCopyErrors verifies a post-batch memory trash copy execution failure is not reported as a clean idle recycle failure.
// TestRecycleIdleSessionsReturnsOutcomeUncertainWhenMemoryTrashCopyErrors 用于验证 batch 已写入后的 memory trash 复制执行错误不会被上报为干净的 idle 回收失败。
func TestRecycleIdleSessionsReturnsOutcomeUncertainWhenMemoryTrashCopyErrors(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":21,"session_key":"sess-idle","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "origin_session_id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":31,"team_id":2,"space_id":3,"project_id":4,"user_id":1,"origin_session_id":21,"source_turn_id":41,"vector_id":"vec-31","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"expired todo","details":"expired temporary todo","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count FROM vmm_memory_context_edges"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":0}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records tr"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":9}]`}, nil
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
			return &fakeExecuteResponse{Success: false, Message: "memory trash copy failed after apply"}, nil
		default:
			t.Fatalf("expected idle memory trash copy error to stop before later writes, got %q", sql)
			return nil, nil
		}
	}

	_, err := store.RecycleIdleSessions(context.Background(), logicdomain.SessionIdleRecycleQuery{
		Limit:             8,
		RecycledAt:        time.Unix(300, 0).UTC(),
		IdleBefore:        time.Unix(200, 0).UTC(),
		TurnHotWindowSize: 8,
		RecycleReason:     logicdomain.RecycleReasonIdleSessionCompact,
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected idle memory trash copy error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "copy sqlite idle-session memories to trash") || !strings.Contains(err.Error(), "memory trash copy failed after apply") {
		t.Fatalf("unexpected idle memory trash copy error: %v", err)
	}
	if len(executed) != 2 {
		t.Fatalf("expected batch insert and memory trash copy only, got %d", len(executed))
	}
}

// TestRecycleIdleSessionsRejectsPartialHotMemoryDeleteBeforeVectorCleanup verifies idle-session recycle cannot return vector ids when hot memory deletion drifts.
// TestRecycleIdleSessionsRejectsPartialHotMemoryDeleteBeforeVectorCleanup 用于验证 idle-session 回收删除热表 memory 行发生漂移时，不会继续进入 vector 清理坐标链路。
func TestRecycleIdleSessionsRejectsPartialHotMemoryDeleteBeforeVectorCleanup(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 5)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":21,"session_key":"sess-idle","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "origin_session_id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":31,"team_id":2,"space_id":3,"project_id":4,"user_id":1,"origin_session_id":21,"source_turn_id":41,"vector_id":"vec-31","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"短期待办已过期","details":"该 session 里的临时 TODO 已失效","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20},
{"id":32,"team_id":2,"space_id":3,"project_id":4,"user_id":1,"origin_session_id":21,"source_turn_id":42,"vector_id":"vec-32","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":0,"category":4,"abstract":"短期待办二已过期","details":"该 session 里的第二条临时 TODO 已失效","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":11,"updated_timestamp":21}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count FROM vmm_memory_context_edges"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records tr"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":41,"session_id":21,"project_id":4,"dehydrated_content":"{\"user\":\"old\"}","dehydrated_budget":5,"extracted_status":1,"details":"旧轮次摘要","details_budget":2,"created_timestamp":10,"updated_timestamp":20},
{"id":42,"session_id":21,"project_id":4,"dehydrated_content":"{\"assistant\":\"old\"}","dehydrated_budget":4,"extracted_status":1,"details":"旧助手轮次摘要","details_budget":2,"created_timestamp":11,"updated_timestamp":21}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":9}]`}, nil
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
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		}
		return &fakeExecuteResponse{Success: true}, nil
	}

	_, err := store.RecycleIdleSessions(context.Background(), logicdomain.SessionIdleRecycleQuery{
		Limit:             8,
		RecycledAt:        time.Unix(300, 0).UTC(),
		IdleBefore:        time.Unix(200, 0).UTC(),
		TurnHotWindowSize: 8,
		RecycleReason:     logicdomain.RecycleReasonIdleSessionCompact,
	})
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain idle-session recycle error, got %v", err)
	}
	if len(executed) != 5 {
		t.Fatalf("expected five statements before partial hot memory delete stopped the pass, got %d", len(executed))
	}
	capturedSQL := sqliteExecutedSQLLog(executed)
	for _, forbidden := range []string{"INSERT INTO vmm_turn_records_trash", "DELETE FROM vmm_turn_records"} {
		if strings.Contains(capturedSQL, forbidden) {
			t.Fatalf("expected partial hot memory delete to stop before %q, got %q", forbidden, capturedSQL)
		}
	}
}

// TestRecycleIdleSessionsRejectsPartialContextDeleteBeforeHotMemoryDelete verifies idle-session recycle stops before hot memory deletion when audited context removal drifts.
// TestRecycleIdleSessionsRejectsPartialContextDeleteBeforeHotMemoryDelete 用于验证 idle-session 回收的 context 删除偏离审计计数时，会在删除热表 memory 前停止。
func TestRecycleIdleSessionsRejectsPartialContextDeleteBeforeHotMemoryDelete(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 4)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":21,"session_key":"sess-idle","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "origin_session_id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":31,"team_id":2,"space_id":3,"project_id":4,"user_id":1,"origin_session_id":21,"source_turn_id":41,"vector_id":"vec-31","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"短期待办已过期","details":"该 session 里的临时 TODO 已失效","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20},
{"id":32,"team_id":2,"space_id":3,"project_id":4,"user_id":1,"origin_session_id":21,"source_turn_id":42,"vector_id":"vec-32","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":0,"category":4,"abstract":"短期待办二已过期","details":"该 session 里的第二条临时 TODO 已失效","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":11,"updated_timestamp":21}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count FROM vmm_memory_context_edges"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records tr"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":41,"session_id":21,"project_id":4,"dehydrated_content":"{\"user\":\"old\"}","dehydrated_budget":5,"extracted_status":1,"details":"旧轮次摘要","details_budget":2,"created_timestamp":10,"updated_timestamp":20},
{"id":42,"session_id":21,"project_id":4,"dehydrated_content":"{\"assistant\":\"old\"}","dehydrated_budget":4,"extracted_status":1,"details":"旧助手轮次摘要","details_budget":2,"created_timestamp":11,"updated_timestamp":21}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":9}]`}, nil
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
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		default:
			return &fakeExecuteResponse{Success: true}, nil
		}
	}

	_, err := store.RecycleIdleSessions(context.Background(), logicdomain.SessionIdleRecycleQuery{
		Limit:             8,
		RecycledAt:        time.Unix(300, 0).UTC(),
		IdleBefore:        time.Unix(200, 0).UTC(),
		TurnHotWindowSize: 8,
		RecycleReason:     logicdomain.RecycleReasonIdleSessionCompact,
	})
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain idle-session recycle error, got %v", err)
	}
	if len(executed) != 4 {
		t.Fatalf("expected four statements before partial context delete stopped the pass, got %d", len(executed))
	}
	capturedSQL := sqliteExecutedSQLLog(executed)
	for _, forbidden := range []string{"DELETE FROM vmm_memory_nodes", "INSERT INTO vmm_turn_records_trash", "DELETE FROM vmm_turn_records"} {
		if strings.Contains(capturedSQL, forbidden) {
			t.Fatalf("expected partial context delete to stop before %q, got %q", forbidden, capturedSQL)
		}
	}
}

// TestRecycleIdleSessionsReturnsOutcomeUncertainForPartialTurnTrashCopy verifies partial turn audit-copy drift stops before hot turn deletion.
// TestRecycleIdleSessionsReturnsOutcomeUncertainForPartialTurnTrashCopy 用于验证 turn 审计复制发生部分漂移时，会在删除热表 turn 前以结果不确定停止。
func TestRecycleIdleSessionsReturnsOutcomeUncertainForPartialTurnTrashCopy(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":21,"session_key":"sess-idle","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "origin_session_id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records tr"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":41,"session_id":21,"project_id":4,"dehydrated_content":"{\"user\":\"old\"}","dehydrated_budget":5,"extracted_status":1,"details":"old turn","details_budget":2,"created_timestamp":10,"updated_timestamp":20},
{"id":42,"session_id":21,"project_id":4,"dehydrated_content":"{\"assistant\":\"old\"}","dehydrated_budget":4,"extracted_status":1,"details":"old assistant turn","details_budget":2,"created_timestamp":11,"updated_timestamp":21}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":9}]`}, nil
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
		case strings.Contains(sql, "INSERT INTO vmm_turn_records_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		default:
			t.Fatalf("expected partial turn copy to stop before hot delete, got %q", sql)
			return nil, nil
		}
	}

	result, err := store.RecycleIdleSessions(context.Background(), logicdomain.SessionIdleRecycleQuery{
		Limit:             8,
		RecycledAt:        time.Unix(300, 0).UTC(),
		IdleBefore:        time.Unix(200, 0).UTC(),
		TurnHotWindowSize: 8,
		RecycleReason:     logicdomain.RecycleReasonIdleSessionCompact,
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected partial idle turn copy to be outcome-uncertain, got %v", err)
	}
	if len(result.BatchIDs) != 1 || result.BatchIDs[0] != 9 || len(result.SessionIDs) != 1 || result.SessionIDs[0] != 21 {
		t.Fatalf("unexpected idle recycle coordinates after partial turn copy: %+v", result)
	}
	if result.RecycledTurnCount != 0 || result.TurnDriftCount != 2 {
		t.Fatalf("unexpected partial turn copy result: %+v", result)
	}
	if len(executed) != 2 {
		t.Fatalf("expected batch insert and turn trash copy only, got %d", len(executed))
	}
	if strings.Contains(sqliteExecutedSQLLog(executed), "DELETE FROM vmm_turn_records") {
		t.Fatalf("expected partial turn copy to stop before hot delete, got %q", sqliteExecutedSQLLog(executed))
	}
}

// TestRecycleIdleSessionsReturnsOutcomeUncertainForPartialTurnDeleteWithoutBlockingVectors verifies post-memory turn drift returns an error without losing safe vector cleanup coordinates.
// TestRecycleIdleSessionsReturnsOutcomeUncertainForPartialTurnDeleteWithoutBlockingVectors 用于验证 memory 删除后的 turn 漂移会返回错误，但不会丢失已安全的 vector 清理坐标。
func TestRecycleIdleSessionsReturnsOutcomeUncertainForPartialTurnDeleteWithoutBlockingVectors(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":21,"session_key":"sess-idle","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "origin_session_id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":31,"team_id":2,"space_id":3,"project_id":4,"user_id":1,"origin_session_id":21,"source_turn_id":41,"vector_id":"vec-31","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"短期待办已过期","details":"该 session 里的临时 TODO 已失效","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20},
{"id":32,"team_id":2,"space_id":3,"project_id":4,"user_id":1,"origin_session_id":21,"source_turn_id":42,"vector_id":"vec-32","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":0,"category":4,"abstract":"短期待办二已过期","details":"该 session 里的第二条临时 TODO 已失效","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":11,"updated_timestamp":21}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count FROM vmm_memory_context_edges"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records tr"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":41,"session_id":21,"project_id":4,"dehydrated_content":"{\"user\":\"old\"}","dehydrated_budget":5,"extracted_status":1,"details":"旧轮次摘要","details_budget":2,"created_timestamp":10,"updated_timestamp":20},
{"id":42,"session_id":21,"project_id":4,"dehydrated_content":"{\"assistant\":\"old\"}","dehydrated_budget":4,"extracted_status":1,"details":"旧助手轮次摘要","details_budget":2,"created_timestamp":11,"updated_timestamp":21}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":9}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
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
		case strings.Contains(sql, "INSERT INTO vmm_turn_records_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "DELETE FROM vmm_turn_records"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		default:
			return &fakeExecuteResponse{Success: true}, nil
		}
	}

	result, err := store.RecycleIdleSessions(context.Background(), logicdomain.SessionIdleRecycleQuery{
		Limit:             8,
		RecycledAt:        time.Unix(300, 0).UTC(),
		IdleBefore:        time.Unix(200, 0).UTC(),
		TurnHotWindowSize: 8,
		RecycleReason:     logicdomain.RecycleReasonIdleSessionCompact,
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected partial idle turn delete to be outcome-uncertain, got %v", err)
	}
	if len(result.BatchIDs) != 1 || result.BatchIDs[0] != 9 || len(result.SessionIDs) != 1 || result.SessionIDs[0] != 21 {
		t.Fatalf("unexpected idle recycle coordinates after partial turn delete: %+v", result)
	}
	if result.RecycledMemoryCount != 2 || result.RecycledTurnCount != 0 || result.TurnDriftCount != 2 {
		t.Fatalf("unexpected partial turn recycle result: %+v", result)
	}
	if got := strings.Join(result.RecycledVectorIDs, ","); got != "vec-31,vec-32" {
		t.Fatalf("expected safe vector cleanup coordinates to be preserved, got %v", result.RecycledVectorIDs)
	}
}

// TestRecycleColdTurnsUsesTypedReasonAndTurnIDParams verifies cold-turn execution binds both caller reason text and normalized turn ids as typed params.
// TestRecycleColdTurnsUsesTypedReasonAndTurnIDParams 用于验证冷 turn 执行会把调用方原因文本与规范化 turn id 都作为强类型参数绑定。
func TestRecycleColdTurnsUsesTypedReasonAndTurnIDParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	queries := make([]*fakeQueryRequest, 0, 4)
	executed := make([]*fakeExecuteRequest, 0, 5)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		queries = append(queries, req)
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":31,"session_key":"sess-cold","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count") && strings.Contains(sql, "FROM vmm_turn_records"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":0}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records tr"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":91,"session_id":31,"project_id":4,"dehydrated_content":"{\"user\":\"old\"}","dehydrated_budget":5,"extracted_status":1,"details":"old turn","details_budget":2,"created_timestamp":10,"updated_timestamp":20},
{"id":92,"session_id":31,"project_id":4,"dehydrated_content":"{\"assistant\":\"old\"}","dehydrated_budget":4,"extracted_status":1,"details":"old assistant turn","details_budget":2,"created_timestamp":11,"updated_timestamp":21}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":13}]`}, nil
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
		case strings.Contains(sql, "INSERT INTO vmm_turn_records_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "DELETE FROM vmm_turn_records"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		}
		return &fakeExecuteResponse{Success: true}, nil
	}

	reason := "  cold turn archive '; DROP TABLE vmm_turn_records;  "
	result, err := store.RecycleColdTurns(context.Background(), logicdomain.ColdTurnRecycleQuery{
		SessionID:         31,
		RecycledAt:        time.Unix(400, 0).UTC(),
		TurnHotWindowSize: 8,
		RecycleReason:     reason,
	})
	if err != nil {
		t.Fatalf("RecycleColdTurns returned error: %v", err)
	}
	if result.BatchID != 13 || result.SessionID != 31 || result.ProjectID != 4 || result.RecycledTurnCount != 2 {
		t.Fatalf("unexpected cold-turn recycle result: %+v", result)
	}
	var turnQuery *fakeQueryRequest
	for _, query := range queries {
		if strings.Contains(query.GetSql(), "FROM vmm_turn_records tr") {
			turnQuery = query
			break
		}
	}
	if turnQuery == nil {
		t.Fatal("expected cold-turn candidate query to be captured")
	}
	if !strings.Contains(turnQuery.GetSql(), "LIMIT ?") {
		t.Fatalf("expected cold-turn hot-window limit to use typed placeholder, got %q", turnQuery.GetSql())
	}
	requireSQLiteInt64Params(t, turnQuery.GetParams(), 31, 8, 31, int64(logicdomain.TurnExtractedStatusPending))
	if len(executed) != 3 {
		t.Fatalf("expected three ordered cold-turn recycle statements, got %d", len(executed))
	}
	requireNoSQLiteTransactionControl(t, executed)
	capturedSQL := sqliteExecutedSQLLog(executed)
	for _, fragment := range []string{"INSERT INTO vmm_recycle_batches", "INSERT INTO vmm_turn_records_trash", "DELETE FROM vmm_turn_records", "WHERE id IN (?,?)"} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected cold-turn recycle sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
	for _, request := range executed[1:] {
		if strings.Contains(request.GetSql(), "91") || strings.Contains(request.GetSql(), "92") {
			t.Fatalf("expected turn ids to stay out of cold-turn SQL text, got %q", request.GetSql())
		}
	}
	requireSQLiteInt64Params(t, executed[1].GetParams()[3:], 91, 92)
	requireSQLiteInt64Params(t, executed[2].GetParams(), 91, 92)
	if strings.Contains(capturedSQL, "DROP TABLE") || strings.Contains(capturedSQL, "cold turn archive") {
		t.Fatalf("expected cold-turn reason to stay out of SQL text, got %q", capturedSQL)
	}
	if got := countSQLiteStringParam(executed, strings.TrimSpace(reason)); got != 2 {
		t.Fatalf("expected cold-turn reason to be bound twice, got %d", got)
	}
}

// TestRecycleColdTurnsReturnsOutcomeUncertainWhenBatchInsertOverReportsRows verifies a cold-turn batch insert that changes too many rows stops before turn trash writes.
// TestRecycleColdTurnsReturnsOutcomeUncertainWhenBatchInsertOverReportsRows 用于验证 cold-turn 批次插入报告超量写入时，会以结果不确定停止在 turn trash 写入前。
func TestRecycleColdTurnsReturnsOutcomeUncertainWhenBatchInsertOverReportsRows(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 1)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":31,"session_key":"sess-cold","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count") && strings.Contains(sql, "FROM vmm_turn_records"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":0}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records tr"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":91,"session_id":31,"project_id":4,"dehydrated_content":"{\"user\":\"old\"}","dehydrated_budget":5,"extracted_status":1,"details":"old turn","details_budget":2,"created_timestamp":10,"updated_timestamp":20},
{"id":92,"session_id":31,"project_id":4,"dehydrated_content":"{\"assistant\":\"old\"}","dehydrated_budget":4,"extracted_status":1,"details":"old assistant turn","details_budget":2,"created_timestamp":11,"updated_timestamp":21}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":13}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		if !strings.Contains(req.GetSql(), "INSERT INTO vmm_recycle_batches") {
			t.Fatalf("expected cold-turn batch insert to be first and only write, got %q", req.GetSql())
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
	}

	_, err := store.RecycleColdTurns(context.Background(), logicdomain.ColdTurnRecycleQuery{
		SessionID:         31,
		RecycledAt:        time.Unix(400, 0).UTC(),
		TurnHotWindowSize: 8,
		RecycleReason:     logicdomain.RecycleReasonColdTurnArchive,
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected over-reported batch insert to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "insert sqlite cold-turn recycle batch") || !strings.Contains(err.Error(), "affected 2 rows, want 1") {
		t.Fatalf("expected cold-turn batch insert row-count detail, got %v", err)
	}
	if len(executed) != 1 {
		t.Fatalf("expected only the batch insert write, got %d", len(executed))
	}
}

// TestRecycleColdTurnsReturnsOutcomeUncertainWhenTrashCopyErrors verifies a post-batch turn trash copy execution failure is not reported as a clean cold-turn recycle failure.
// TestRecycleColdTurnsReturnsOutcomeUncertainWhenTrashCopyErrors 用于验证 batch 已写入后的 turn trash 复制执行错误不会被上报为干净的 cold-turn 回收失败。
func TestRecycleColdTurnsReturnsOutcomeUncertainWhenTrashCopyErrors(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":31,"session_key":"sess-cold","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count") && strings.Contains(sql, "FROM vmm_turn_records"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":0}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records tr"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":91,"session_id":31,"project_id":4,"dehydrated_content":"{\"user\":\"old\"}","dehydrated_budget":5,"extracted_status":1,"details":"old turn","details_budget":2,"created_timestamp":10,"updated_timestamp":20},
{"id":92,"session_id":31,"project_id":4,"dehydrated_content":"{\"assistant\":\"old\"}","dehydrated_budget":4,"extracted_status":1,"details":"old assistant turn","details_budget":2,"created_timestamp":11,"updated_timestamp":21}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":13}]`}, nil
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
		case strings.Contains(sql, "INSERT INTO vmm_turn_records_trash"):
			return &fakeExecuteResponse{Success: false, Message: "turn trash copy failed after apply"}, nil
		default:
			t.Fatalf("expected cold-turn trash copy error to stop before hot delete, got %q", sql)
			return nil, nil
		}
	}

	_, err := store.RecycleColdTurns(context.Background(), logicdomain.ColdTurnRecycleQuery{
		SessionID:         31,
		RecycledAt:        time.Unix(400, 0).UTC(),
		TurnHotWindowSize: 8,
		RecycleReason:     logicdomain.RecycleReasonColdTurnArchive,
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected cold-turn trash copy error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "copy sqlite cold turns to trash") || !strings.Contains(err.Error(), "turn trash copy failed after apply") {
		t.Fatalf("unexpected cold-turn trash copy error: %v", err)
	}
	if len(executed) != 2 {
		t.Fatalf("expected batch insert and turn trash copy only, got %d", len(executed))
	}
}

// TestRecycleColdTurnsRejectsPartialDeleteBeforeJobCompletion verifies a claimed cold-turn job stays retryable when hot-table turn deletion drifts.
// TestRecycleColdTurnsRejectsPartialDeleteBeforeJobCompletion 用于验证已领取 cold-turn 任务在热表 turn 删除漂移时保持可重试，而不是被错误完成。
func TestRecycleColdTurnsRejectsPartialDeleteBeforeJobCompletion(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 3)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":31,"session_key":"sess-cold","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count") && strings.Contains(sql, "FROM vmm_turn_records"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":0}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records tr"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":91,"session_id":31,"project_id":4,"dehydrated_content":"{\"user\":\"old\"}","dehydrated_budget":5,"extracted_status":1,"details":"old turn","details_budget":2,"created_timestamp":10,"updated_timestamp":20},
{"id":92,"session_id":31,"project_id":4,"dehydrated_content":"{\"assistant\":\"old\"}","dehydrated_budget":4,"extracted_status":1,"details":"old assistant turn","details_budget":2,"created_timestamp":11,"updated_timestamp":21}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":13}]`}, nil
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
		case strings.Contains(sql, "INSERT INTO vmm_turn_records_trash"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "DELETE FROM vmm_turn_records"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		default:
			return &fakeExecuteResponse{Success: true}, nil
		}
	}

	_, err := store.RecycleColdTurns(context.Background(), logicdomain.ColdTurnRecycleQuery{
		SessionID:         31,
		RecycledAt:        time.Unix(400, 0).UTC(),
		TurnHotWindowSize: 8,
		RecycleReason:     logicdomain.RecycleReasonColdTurnArchive,
	})
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain cold-turn recycle error, got %v", err)
	}
	if len(executed) != 3 {
		t.Fatalf("expected batch, trash-copy, and delete statements before partial delete stopped completion, got %d", len(executed))
	}
}

// TestBuildSQLiteIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows verifies the prefilter keeps both stale-memory and old-turn existence checks.
// TestBuildSQLiteIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows 用于验证 session 预过滤会同时包含陈旧记忆与旧 turn 的存在性检查。
func TestBuildSQLiteIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows(t *testing.T) {
	clause, args := buildSQLiteIdleSessionCandidateAvailabilityClause(12345, 8)
	if !strings.Contains(clause, "FROM vmm_memory_nodes m") ||
		!strings.Contains(clause, "FROM vmm_turn_records tr") ||
		!strings.Contains(clause, "LIMIT ?") ||
		!strings.Contains(clause, "NOT EXISTS (SELECT 1 FROM vmm_profile_nodes pn WHERE pn.turn_id = tr.id)") {
		t.Fatalf("unexpected candidate availability clause: %q", clause)
	}
	if len(args) != 6 {
		t.Fatalf("candidate availability args len = %d, want 6", len(args))
	}
	expectedArgs := []any{
		logicdomain.MemoryScopeLevelSession,
		logicdomain.MemoryStatusActive,
		int64(12345),
		int64(12345),
		8,
		logicdomain.TurnExtractedStatusPending,
	}
	for idx, expected := range expectedArgs {
		if args[idx] != expected {
			t.Fatalf("candidate availability arg %d = %#v, want %#v", idx, args[idx], expected)
		}
	}
}

// TestBuildSQLiteColdTurnJobSessionAvailabilityClauseUsesTypedHotWindowLimit verifies cold-turn job scans bind the hot-window limit.
// TestBuildSQLiteColdTurnJobSessionAvailabilityClauseUsesTypedHotWindowLimit 用于验证 cold-turn job 扫描会绑定热窗口 limit。
func TestBuildSQLiteColdTurnJobSessionAvailabilityClauseUsesTypedHotWindowLimit(t *testing.T) {
	clause, args := buildSQLiteColdTurnJobSessionAvailabilityClause(8)
	if !strings.Contains(clause, "LIMIT ?") {
		t.Fatalf("expected cold-turn availability limit placeholder, got %q", clause)
	}
	if strings.Contains(clause, "LIMIT 8") {
		t.Fatalf("expected cold-turn hot-window size to stay out of SQL text, got %q", clause)
	}
	expectedArgs := []any{8, logicdomain.TurnExtractedStatusPending}
	if len(args) != len(expectedArgs) {
		t.Fatalf("cold-turn availability args len = %d, want %d", len(args), len(expectedArgs))
	}
	for idx, expected := range expectedArgs {
		if args[idx] != expected {
			t.Fatalf("cold-turn availability arg %d = %#v, want %#v", idx, args[idx], expected)
		}
	}
}

// TestBuildSQLiteIdleSessionTurnReferenceClauseUsesTypedIgnoredMemoryIDs verifies turn-reference checks bind same-batch memory ids instead of rendering them into SQL text.
// TestBuildSQLiteIdleSessionTurnReferenceClauseUsesTypedIgnoredMemoryIDs 用于验证 turn 引用检查会绑定同批 memory id，而不是把它们渲染进 SQL 文本。
func TestBuildSQLiteIdleSessionTurnReferenceClauseUsesTypedIgnoredMemoryIDs(t *testing.T) {
	clause, args := buildSQLiteIdleSessionTurnReferenceClause([]uint64{32, 0, 31, 31})
	if !strings.Contains(clause, "mn.id NOT IN (?,?)") {
		t.Fatalf("expected typed ignored memory placeholders, got %q", clause)
	}
	if strings.Contains(clause, "31") || strings.Contains(clause, "32") {
		t.Fatalf("expected memory ids to stay out of reference SQL, got %q", clause)
	}
	if len(args) != 2 || args[0] != uint64(31) || args[1] != uint64(32) {
		t.Fatalf("expected normalized ignored memory params [31 32], got %#v", args)
	}
	emptyClause, emptyArgs := buildSQLiteIdleSessionTurnReferenceClause(nil)
	if !strings.Contains(emptyClause, "WHERE mn.source_turn_id = tr.id") || strings.Contains(emptyClause, "NOT IN") {
		t.Fatalf("unexpected empty reference clause: %q", emptyClause)
	}
	if len(emptyArgs) != 0 {
		t.Fatalf("expected no empty reference params, got %#v", emptyArgs)
	}
}

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

// TestClaimPendingRecycleJobsUsesUpdateReturning verifies recycle-job leasing selects and updates due rows in one typed SQLite statement.
// TestClaimPendingRecycleJobsUsesUpdateReturning 用于验证回收任务领取会在一条强类型 SQLite 语句中选择并更新到期行。
func TestClaimPendingRecycleJobsUsesUpdateReturning(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	claimUntil := time.Unix(700, 0).UTC()
	dueBefore := time.Unix(600, 0).UTC()
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[
{"id":902,"session_id":22,"project_id":10,"job_type":"cold_turn","attempt_count":1,"next_run_timestamp":700000,"claimed_timestamp":699000,"last_error":"","created_timestamp":1000,"updated_timestamp":699000},
{"id":901,"session_id":21,"project_id":9,"job_type":"cold_turn","attempt_count":0,"next_run_timestamp":700000,"claimed_timestamp":699000,"last_error":"","created_timestamp":900,"updated_timestamp":699000}
]`}, nil
	}

	jobs, err := store.ClaimPendingRecycleJobs(context.Background(), logicdomain.RecycleJobTypeColdTurn, dueBefore, claimUntil, 8)
	if err != nil {
		t.Fatalf("ClaimPendingRecycleJobs returned error: %v", err)
	}
	if len(jobs) != 2 || jobs[0].ID != 901 || jobs[1].ID != 902 {
		t.Fatalf("expected claimed jobs sorted by id, got %+v", jobs)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	for _, fragment := range []string{"UPDATE vmm_recycle_jobs", "WHERE id IN (", "ORDER BY next_run_timestamp ASC, id ASC", "LIMIT ?", "RETURNING id, session_id, project_id"} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("expected claim sql to contain %q, got %q", fragment, sql)
		}
	}
	for _, forbidden := range []string{"BEGIN IMMEDIATE;", "COMMIT;", "ROLLBACK;"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("expected claim sql to stay single-statement, got %q", sql)
		}
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	params := captured.GetParams()
	if len(params) != 6 {
		t.Fatalf("expected six claim params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 <= 0 || params[2].Kind != sqliteffi.SQLValueInt64 || params[2].Int64 != params[0].Int64 {
		t.Fatalf("expected claimed and updated timestamps to share one positive claim time, got %#v and %#v", params[0], params[2])
	}
	if params[1].Kind != sqliteffi.SQLValueInt64 || params[1].Int64 != claimUntil.UnixMilli() {
		t.Fatalf("expected claim-until timestamp param, got %#v", params[1])
	}
	if params[3].Kind != sqliteffi.SQLValueString || params[3].String != logicdomain.RecycleJobTypeColdTurn {
		t.Fatalf("expected job type param, got %#v", params[3])
	}
	if params[4].Kind != sqliteffi.SQLValueInt64 || params[4].Int64 != dueBefore.UnixMilli() {
		t.Fatalf("expected due-before timestamp param, got %#v", params[4])
	}
	if params[5].Kind != sqliteffi.SQLValueInt64 || params[5].Int64 != 8 {
		t.Fatalf("expected limit param, got %#v", params[5])
	}
}

// TestClaimPendingRecycleJobsMarksCommitUnknownAsOutcomeUncertain verifies claim failures at SQLite's commit boundary are surfaced as ambiguous queue mutations.
// TestClaimPendingRecycleJobsMarksCommitUnknownAsOutcomeUncertain 用于验证 SQLite 提交边界上的领取失败会作为不确定队列变更向上暴露。
func TestClaimPendingRecycleJobsMarksCommitUnknownAsOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(context.Context, *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		return nil, errors.New("failed to commit recycle claim update")
	}

	_, err := store.ClaimPendingRecycleJobs(context.Background(), logicdomain.RecycleJobTypeColdTurn, time.Unix(600, 0).UTC(), time.Unix(700, 0).UTC(), 8)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain claim error, got %v", err)
	}
	if !strings.Contains(err.Error(), "claim sqlite pending recycle jobs") || !strings.Contains(err.Error(), "failed to commit recycle claim update") {
		t.Fatalf("unexpected claim error text: %v", err)
	}
}

// TestCompleteRecycleJobsUsesTypedIDParams verifies completed recycle-job cleanup deletes normalized ids through typed placeholders.
// TestCompleteRecycleJobsUsesTypedIDParams 用于验证已完成回收任务清理会通过强类型占位符删除规范化后的 id。
func TestCompleteRecycleJobsUsesTypedIDParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
	}

	if err := store.CompleteRecycleJobs(context.Background(), []uint64{44, 0, 43, 44}, time.Unix(800, 0).UTC()); err != nil {
		t.Fatalf("CompleteRecycleJobs returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected ExecuteScript request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "DELETE FROM vmm_recycle_jobs") || !strings.Contains(sql, "WHERE id IN (?,?)") {
		t.Fatalf("expected placeholder delete SQL, got %q", sql)
	}
	for _, forbidden := range []string{"BEGIN IMMEDIATE;", "COMMIT;", "43", "44"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("expected completed job ids to stay out of SQL text, got %q", sql)
		}
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	params := captured.GetParams()
	if len(params) != 2 {
		t.Fatalf("expected two completed job id params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 != 43 ||
		params[1].Kind != sqliteffi.SQLValueInt64 || params[1].Int64 != 44 {
		t.Fatalf("expected normalized completed job ids 43 and 44, got %#v", params)
	}
}

// TestCompleteRecycleJobsRejectsPartialDelete verifies job completion cannot report success when the queue delete misses claimed job ids.
// TestCompleteRecycleJobsRejectsPartialDelete 用于验证队列删除未命中全部已领取 job id 时，任务完成不能报告成功。
func TestCompleteRecycleJobsRejectsPartialDelete(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	err := store.CompleteRecycleJobs(context.Background(), []uint64{44, 43}, time.Unix(800, 0).UTC())
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain complete recycle jobs error, got %v", err)
	}
}

// TestCompleteRecycleJobsMarksCommitUnknownAsOutcomeUncertain verifies commit-boundary failures keep the recycle-job completion outcome uncertain.
// TestCompleteRecycleJobsMarksCommitUnknownAsOutcomeUncertain 用于验证提交边界失败会保留回收任务完成结果不确定语义。
func TestCompleteRecycleJobsMarksCommitUnknownAsOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("failed to commit sqlite transaction")
	}

	err := store.CompleteRecycleJobs(context.Background(), []uint64{44, 43}, time.Unix(800, 0).UTC())
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected commit-unknown complete recycle jobs error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "complete sqlite recycle jobs") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected complete recycle jobs commit error: %v", err)
	}
}

// TestEnqueueColdTurnRecycleJobsUsesTypedSQLiteParams verifies recycle-job enqueue writes keep the stable job type out of executable SQL while avoiding unsupported cross-call transaction control.
// TestEnqueueColdTurnRecycleJobsUsesTypedSQLiteParams 用于验证回收任务入队会把稳定 job type 留在强类型参数中，并避免不受支持的跨调用事务控制。
func TestEnqueueColdTurnRecycleJobsUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	// Capture the insert writes because enqueue emits one typed INSERT per candidate session.
	// 捕获入队写入，因为入队会为每个候选 session 发出一条强类型 INSERT。
	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":11,
				"session_key":"session-a",
				"user_id":7,
				"team_id":2,
				"space_id":3,
				"project_id":9,
				"turn_count":20,
				"last_summarized_id":0,
				"last_compacted_turn_id":0,
				"summarize_content":"",
				"summarize_budget":0,
				"last_extract_observed_timestamp":0,
				"last_extract_completed_timestamp":0,
				"last_compacted_timestamp":0,
				"created_timestamp":1,
				"updated_timestamp":2
			},{
				"id":12,
				"session_key":"session-b",
				"user_id":8,
				"team_id":2,
				"space_id":3,
				"project_id":10,
				"turn_count":24,
				"last_summarized_id":0,
				"last_compacted_turn_id":0,
				"summarize_content":"",
				"summarize_budget":0,
				"last_extract_observed_timestamp":0,
				"last_extract_completed_timestamp":0,
				"last_compacted_timestamp":0,
				"created_timestamp":1,
				"updated_timestamp":3
			}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_recycle_jobs"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":900}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	scannedAt := time.Unix(100, 0).UTC()
	nextRunAt := time.Unix(120, 0).UTC()
	enqueued, err := store.EnqueueColdTurnRecycleJobs(context.Background(), logicdomain.ColdTurnRecycleJobEnqueueQuery{
		Limit:             2,
		ScannedAt:         scannedAt,
		NextRunAt:         nextRunAt,
		TurnHotWindowSize: 4,
	})
	if err != nil {
		t.Fatalf("EnqueueColdTurnRecycleJobs returned error: %v", err)
	}
	if enqueued != 2 {
		t.Fatalf("expected two enqueued jobs, got %d", enqueued)
	}
	if len(requests) != 2 {
		t.Fatalf("expected two typed insert requests, got %d", len(requests))
	}
	requireNoSQLiteTransactionControl(t, requests)

	capturedSQL := sqliteExecutedSQLLog(requests)
	if strings.Contains(capturedSQL, logicdomain.RecycleJobTypeColdTurn) {
		t.Fatalf("recycle job type leaked into SQL: %q", capturedSQL)
	}
	if strings.Contains(capturedSQL, "INSERT OR IGNORE") {
		t.Fatalf("recycle job enqueue should not hide non-target constraint failures: %q", capturedSQL)
	}
	if !strings.Contains(capturedSQL, "ON CONFLICT(session_id, job_type) DO UPDATE") {
		t.Fatalf("recycle job enqueue should refresh equivalent existing jobs: %q", capturedSQL)
	}
	for _, request := range requests {
		if strings.TrimSpace(request.GetParamsJson()) != "" {
			t.Fatalf("expected typed params only, got params_json=%q", request.GetParamsJson())
		}
	}
	if got := countSQLiteStringParam(requests, logicdomain.RecycleJobTypeColdTurn); got != 2 {
		t.Fatalf("expected cold-turn job type param twice, got %d", got)
	}

	firstInsertParams := requests[0].GetParams()
	if len(firstInsertParams) != 8 {
		t.Fatalf("expected eight first insert params, got %d: %#v", len(firstInsertParams), firstInsertParams)
	}
	if firstInsertParams[0].Kind != sqliteffi.SQLValueInt64 || firstInsertParams[0].Int64 != 900 {
		t.Fatalf("expected first job id 900, got %#v", firstInsertParams[0])
	}
	if firstInsertParams[1].Kind != sqliteffi.SQLValueInt64 || firstInsertParams[1].Int64 != 11 {
		t.Fatalf("expected first session id 11, got %#v", firstInsertParams[1])
	}
	if firstInsertParams[3].Kind != sqliteffi.SQLValueString || firstInsertParams[3].String != logicdomain.RecycleJobTypeColdTurn {
		t.Fatalf("expected first job type param, got %#v", firstInsertParams[3])
	}
	if firstInsertParams[4].Kind != sqliteffi.SQLValueInt64 || firstInsertParams[4].Int64 != nextRunAt.UnixMilli() {
		t.Fatalf("expected first next_run_timestamp param, got %#v", firstInsertParams[4])
	}
	if firstInsertParams[5].Kind != sqliteffi.SQLValueString || firstInsertParams[5].String != "" {
		t.Fatalf("expected first last_error empty string param, got %#v", firstInsertParams[5])
	}
	if firstInsertParams[6].Kind != sqliteffi.SQLValueInt64 || firstInsertParams[6].Int64 != scannedAt.UnixMilli() {
		t.Fatalf("expected first created timestamp param, got %#v", firstInsertParams[6])
	}

	secondInsertParams := requests[1].GetParams()
	if len(secondInsertParams) != 8 {
		t.Fatalf("expected eight second insert params, got %d: %#v", len(secondInsertParams), secondInsertParams)
	}
	if secondInsertParams[0].Kind != sqliteffi.SQLValueInt64 || secondInsertParams[0].Int64 != 901 {
		t.Fatalf("expected second job id 901, got %#v", secondInsertParams[0])
	}
	if secondInsertParams[1].Kind != sqliteffi.SQLValueInt64 || secondInsertParams[1].Int64 != 12 {
		t.Fatalf("expected second session id 12, got %#v", secondInsertParams[1])
	}
	if secondInsertParams[2].Kind != sqliteffi.SQLValueInt64 || secondInsertParams[2].Int64 != 10 {
		t.Fatalf("expected second project id 10, got %#v", secondInsertParams[2])
	}
}

// TestEnqueueColdTurnRecycleJobsRejectsPartialInsert verifies enqueue counts preserve earlier durable jobs without counting the candidate that SQLite neither inserted nor refreshed.
// TestEnqueueColdTurnRecycleJobsRejectsPartialInsert 用于验证入队数量会保留更早已持久化的任务，但不会计入 SQLite 既未插入也未刷新的候选任务。
func TestEnqueueColdTurnRecycleJobsRejectsPartialInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if len(requests) == 2 {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":11,
				"session_key":"session-a",
				"user_id":7,
				"team_id":2,
				"space_id":3,
				"project_id":9,
				"turn_count":20,
				"last_summarized_id":0,
				"last_compacted_turn_id":0,
				"summarize_content":"",
				"summarize_budget":0,
				"last_extract_observed_timestamp":0,
				"last_extract_completed_timestamp":0,
				"last_compacted_timestamp":0,
				"created_timestamp":1,
				"updated_timestamp":2
			},{
				"id":12,
				"session_key":"session-b",
				"user_id":8,
				"team_id":2,
				"space_id":3,
				"project_id":10,
				"turn_count":24,
				"last_summarized_id":0,
				"last_compacted_turn_id":0,
				"summarize_content":"",
				"summarize_budget":0,
				"last_extract_observed_timestamp":0,
				"last_extract_completed_timestamp":0,
				"last_compacted_timestamp":0,
				"created_timestamp":1,
				"updated_timestamp":3
			}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_recycle_jobs"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":900}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	enqueued, err := store.EnqueueColdTurnRecycleJobs(context.Background(), logicdomain.ColdTurnRecycleJobEnqueueQuery{
		Limit:             2,
		ScannedAt:         time.Unix(100, 0).UTC(),
		NextRunAt:         time.Unix(120, 0).UTC(),
		TurnHotWindowSize: 4,
	})
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain enqueue recycle jobs error, got %v", err)
	}
	if enqueued != 1 {
		t.Fatalf("enqueued count = %d, want 1 confirmed durable job before drift", enqueued)
	}
	if len(requests) != 2 {
		t.Fatalf("expected enqueue to stop after the second insert drifted, got %d requests", len(requests))
	}
}

// TestEnqueueColdTurnRecycleJobsMarksInitialCommitUnknownAsOutcomeUncertain verifies the first autocommit enqueue failure still preserves commit-boundary ambiguity.
// TestEnqueueColdTurnRecycleJobsMarksInitialCommitUnknownAsOutcomeUncertain 用于验证首条自动提交入队失败仍会保留提交边界不确定语义。
func TestEnqueueColdTurnRecycleJobsMarksInitialCommitUnknownAsOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":11,
				"session_key":"session-a",
				"user_id":7,
				"team_id":2,
				"space_id":3,
				"project_id":9,
				"turn_count":20,
				"last_summarized_id":0,
				"last_compacted_turn_id":0,
				"summarize_content":"",
				"summarize_budget":0,
				"last_extract_observed_timestamp":0,
				"last_extract_completed_timestamp":0,
				"last_compacted_timestamp":0,
				"created_timestamp":1,
				"updated_timestamp":2
			}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_recycle_jobs"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":900}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("failed to commit cold-turn enqueue insert")
	}

	enqueued, err := store.EnqueueColdTurnRecycleJobs(context.Background(), logicdomain.ColdTurnRecycleJobEnqueueQuery{
		Limit:             1,
		ScannedAt:         time.Unix(100, 0).UTC(),
		NextRunAt:         time.Unix(120, 0).UTC(),
		TurnHotWindowSize: 4,
	})
	if enqueued != 0 {
		t.Fatalf("enqueued count = %d, want 0 before the first commit-unknown result", enqueued)
	}
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain initial enqueue error, got %v", err)
	}
	if !strings.Contains(err.Error(), "enqueue sqlite cold-turn recycle job 1") || !strings.Contains(err.Error(), "failed to commit cold-turn enqueue insert") {
		t.Fatalf("unexpected initial enqueue error: %v", err)
	}
}

// TestRetryRecycleJobsUsesTypedSQLiteParams verifies retry diagnostics and normalized job ids are bound as typed params.
// TestRetryRecycleJobsUsesTypedSQLiteParams 用于验证重试诊断信息和规范化后的 job id 都会作为强类型参数绑定。
func TestRetryRecycleJobsUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
	}

	lastError := "  recycle failed '; DROP TABLE vmm_recycle_jobs;  "
	if err := store.RetryRecycleJobs(context.Background(), []uint64{22, 21, 22}, time.Unix(9, 0).UTC(), lastError); err != nil {
		t.Fatalf("RetryRecycleJobs returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected ExecuteScript request to be captured")
	}
	if strings.Contains(captured.GetSql(), "DROP TABLE") || strings.Contains(captured.GetSql(), "recycle failed") {
		t.Fatalf("retry recycle last_error leaked into SQL: %q", captured.GetSql())
	}
	if !strings.Contains(captured.GetSql(), "WHERE id IN (?,?)") {
		t.Fatalf("expected placeholder id list in SQL, got %q", captured.GetSql())
	}
	for _, forbidden := range []string{"BEGIN", "COMMIT", "21", "22"} {
		if strings.Contains(captured.GetSql(), forbidden) {
			t.Fatalf("retry recycle SQL should keep ids and transaction control out of text, got %q", captured.GetSql())
		}
	}
	if strings.Contains(captured.GetSql(), "ROLLBACK") {
		t.Fatalf("retry recycle should stay a single atomic UPDATE, got %q", captured.GetSql())
	}
	params := captured.GetParams()
	if len(params) != 5 {
		t.Fatalf("expected five typed params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 != time.Unix(9, 0).UTC().UnixMilli() {
		t.Fatalf("expected next_run_timestamp param, got %#v", params[0])
	}
	if params[1].Kind != sqliteffi.SQLValueString || params[1].String != strings.TrimSpace(lastError) {
		t.Fatalf("expected last_error string param, got %#v", params[1])
	}
	if params[2].Kind != sqliteffi.SQLValueInt64 || params[2].Int64 == 0 {
		t.Fatalf("expected updated_timestamp int64 param, got %#v", params[2])
	}
	if params[3].Kind != sqliteffi.SQLValueInt64 || params[3].Int64 != 21 ||
		params[4].Kind != sqliteffi.SQLValueInt64 || params[4].Int64 != 22 {
		t.Fatalf("expected normalized retry job ids 21 and 22, got %#v", params[3:])
	}
}

// TestRetryRecycleJobsRejectsPartialUpdate verifies failed job reschedule cannot silently miss claimed job ids.
// TestRetryRecycleJobsRejectsPartialUpdate 用于验证失败任务重调度不能静默漏掉已领取 job id。
func TestRetryRecycleJobsRejectsPartialUpdate(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	err := store.RetryRecycleJobs(context.Background(), []uint64{22, 21}, time.Unix(9, 0).UTC(), "failed")
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain retry recycle jobs error, got %v", err)
	}
}

// TestRetryRecycleJobsMarksCommitUnknownAsOutcomeUncertain verifies commit-boundary failures keep the retry update outcome uncertain.
// TestRetryRecycleJobsMarksCommitUnknownAsOutcomeUncertain 用于验证提交边界失败会保留回收任务重试更新结果不确定语义。
func TestRetryRecycleJobsMarksCommitUnknownAsOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("commit outcome unknown after retry update")
	}

	err := store.RetryRecycleJobs(context.Background(), []uint64{22, 21}, time.Unix(9, 0).UTC(), "failed")
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected commit-unknown retry recycle jobs error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "retry sqlite recycle jobs") || !strings.Contains(err.Error(), "commit outcome unknown") {
		t.Fatalf("unexpected retry recycle jobs commit error: %v", err)
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

// TestEnqueueVectorGCJobsPersistsRetryRows verifies failed sidecar deletes are persisted into the vector-gc queue table.
// TestEnqueueVectorGCJobsPersistsRetryRows 用于验证失败的旁路删除会被持久化到 vector-gc 队列表。
func TestEnqueueVectorGCJobsPersistsRetryRows(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 4)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_vector_gc_jobs") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":41}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	vectorID := "vec-2'; DROP TABLE vmm_vector_gc_jobs;--"
	jobType := "  custom_gc '; DROP TABLE vmm_vector_gc_jobs;  "
	if err := store.EnqueueVectorGCJobs(context.Background(), logicdomain.VectorGCJobEnqueueQuery{
		BatchID:   7,
		JobType:   jobType,
		VectorIDs: []string{"vec-1", " " + vectorID + " ", "vec-1"},
		NextRunAt: time.Unix(500, 0).UTC(),
	}); err != nil {
		t.Fatalf("EnqueueVectorGCJobs returned error: %v", err)
	}
	requireNoSQLiteTransactionControl(t, executed)
	if len(executed) != 2 {
		t.Fatalf("expected two typed insert requests, got %d", len(executed))
	}
	capturedSQL := sqliteExecutedSQLLog(executed)
	for _, fragment := range []string{"INSERT INTO vmm_vector_gc_jobs", "ON CONFLICT(vector_id, job_type, batch_id) DO UPDATE"} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected enqueue sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
	for _, leaked := range []string{"DROP TABLE", "vec-1", "custom_gc"} {
		if strings.Contains(capturedSQL, leaked) {
			t.Fatalf("expected enqueue text value %q to stay out of SQL text, got %q", leaked, capturedSQL)
		}
	}
	if got := countSQLiteStringParam(executed, "vec-1"); got != 1 {
		t.Fatalf("expected deduped vec-1 param once, got %d", got)
	}
	if got := countSQLiteStringParam(executed, vectorID); got != 1 {
		t.Fatalf("expected malicious vector id param once, got %d", got)
	}
	if got := countSQLiteStringParam(executed, strings.TrimSpace(jobType)); got != 2 {
		t.Fatalf("expected job type param once per vector id, got %d", got)
	}
}

// TestEnqueueVectorGCJobsRejectsPartialInsert verifies failed vector cleanup compensation reports the confirmed retry rows before a later row is neither inserted nor refreshed.
// TestEnqueueVectorGCJobsRejectsPartialInsert 用于验证向量清理补偿在后续重试行既未插入也未刷新时，会报告此前已确认的重试行。
func TestEnqueueVectorGCJobsRejectsPartialInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_vector_gc_jobs") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":41}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		if len(executed) == 2 {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	err := store.EnqueueVectorGCJobs(context.Background(), logicdomain.VectorGCJobEnqueueQuery{
		BatchID:   7,
		JobType:   logicdomain.VectorGCJobTypeRetentionRecycle,
		VectorIDs: []string{"vec-1", "vec-2"},
		NextRunAt: time.Unix(500, 0).UTC(),
	})
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain vector gc enqueue error, got %v", err)
	}
	if !strings.Contains(err.Error(), "after 1 confirmed jobs") {
		t.Fatalf("expected partial enqueue count in error, got %v", err)
	}
	if len(executed) != 2 {
		t.Fatalf("expected enqueue to stop after second insert drifted, got %d requests", len(executed))
	}
}

// TestEnqueueVectorGCJobsRejectsInitialInsertDriftAsOrdinary verifies a first-row enqueue drift without any mutation is not reported as an uncertain durable queue state.
// TestEnqueueVectorGCJobsRejectsInitialInsertDriftAsOrdinary 用于验证首条入队未产生任何突变时，不会被报告为持久队列状态不确定。
func TestEnqueueVectorGCJobsRejectsInitialInsertDriftAsOrdinary(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 1)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_vector_gc_jobs") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":41}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}

	err := store.EnqueueVectorGCJobs(context.Background(), logicdomain.VectorGCJobEnqueueQuery{
		BatchID:   7,
		JobType:   logicdomain.VectorGCJobTypeRetentionRecycle,
		VectorIDs: []string{"vec-1"},
		NextRunAt: time.Unix(500, 0).UTC(),
	})
	if err == nil {
		t.Fatal("expected initial vector gc enqueue drift to fail")
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect initial vector gc enqueue drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "after 0 confirmed jobs") {
		t.Fatalf("expected zero confirmed job count in error, got %v", err)
	}
	if len(executed) != 1 {
		t.Fatalf("expected enqueue to stop after first insert drifted, got %d requests", len(executed))
	}
}

// TestEnqueueVectorGCJobsMarksInitialCommitUnknownAsOutcomeUncertain verifies first-row vector retry enqueue failures preserve commit-boundary ambiguity.
// TestEnqueueVectorGCJobsMarksInitialCommitUnknownAsOutcomeUncertain 用于验证首条 vector 重试入队失败会保留提交边界不确定语义。
func TestEnqueueVectorGCJobsMarksInitialCommitUnknownAsOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_vector_gc_jobs") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":41}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("commit outcome unknown while enqueuing vector gc job")
	}

	err := store.EnqueueVectorGCJobs(context.Background(), logicdomain.VectorGCJobEnqueueQuery{
		BatchID:   7,
		JobType:   logicdomain.VectorGCJobTypeRetentionRecycle,
		VectorIDs: []string{"vec-1"},
		NextRunAt: time.Unix(500, 0).UTC(),
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain initial vector enqueue error, got %v", err)
	}
	if !strings.Contains(err.Error(), "enqueue sqlite vector gc job 1 after 0 confirmed jobs") || !strings.Contains(err.Error(), "commit outcome unknown while enqueuing vector gc job") {
		t.Fatalf("unexpected initial vector enqueue error: %v", err)
	}
}

// TestSQLiteVectorGCJobsClaimRetryAndComplete verifies the persistent vector-gc queue still supports claim, retry, and complete flows.
// TestSQLiteVectorGCJobsClaimRetryAndComplete 用于验证持久化 vector-gc 队列仍支持领取、重试与完成流转。
func TestSQLiteVectorGCJobsClaimRetryAndComplete(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	querySQL := make([]string, 0, 1)
	executedSQL := make([]string, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		querySQL = append(querySQL, sql)
		if strings.Contains(sql, "UPDATE vmm_vector_gc_jobs") && strings.Contains(sql, "RETURNING") {
			return &fakeQueryJSONResponse{JsonData: `[
{"id":51,"batch_id":7,"vector_id":"vec-retry-1","job_type":"retention_recycle_vector_delete","attempt_count":2,"next_run_timestamp":1000,"claimed_timestamp":0,"completed_timestamp":0,"last_error":"old error","created_timestamp":10,"updated_timestamp":20}
]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, req.GetSql())
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	jobs, err := store.ClaimPendingVectorGCJobs(context.Background(), time.Unix(2, 0).UTC(), time.Unix(5, 0).UTC(), 8)
	if err != nil {
		t.Fatalf("ClaimPendingVectorGCJobs returned error: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != 51 || jobs[0].VectorID != "vec-retry-1" {
		t.Fatalf("unexpected claimed jobs: %+v", jobs)
	}
	if err := store.RetryVectorGCJobs(context.Background(), []uint64{51}, time.Unix(9, 0).UTC(), "retry failed"); err != nil {
		t.Fatalf("RetryVectorGCJobs returned error: %v", err)
	}
	if err := store.CompleteVectorGCJobs(context.Background(), []uint64{51}, time.Unix(12, 0).UTC()); err != nil {
		t.Fatalf("CompleteVectorGCJobs returned error: %v", err)
	}
	if len(querySQL) != 1 || !strings.Contains(querySQL[0], "UPDATE vmm_vector_gc_jobs") {
		t.Fatalf("expected vector gc claim to use one update-returning query, got %#v", querySQL)
	}
	if len(executedSQL) != 2 {
		t.Fatalf("executed sql count = %d, want 2", len(executedSQL))
	}
}

// TestClaimPendingVectorGCJobsUsesUpdateReturning verifies vector-gc leasing selects and updates due rows in one typed SQLite statement.
// TestClaimPendingVectorGCJobsUsesUpdateReturning 用于验证 vector-gc 任务领取会在一条强类型 SQLite 语句中选择并更新到期行。
func TestClaimPendingVectorGCJobsUsesUpdateReturning(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	claimUntil := time.Unix(700, 0).UTC()
	dueBefore := time.Unix(600, 0).UTC()
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[
{"id":52,"batch_id":7,"vector_id":"vec-52","job_type":"retention_recycle_vector_delete","attempt_count":1,"next_run_timestamp":700000,"claimed_timestamp":699000,"completed_timestamp":0,"last_error":"","created_timestamp":1000,"updated_timestamp":699000},
{"id":51,"batch_id":7,"vector_id":"vec-51","job_type":"retention_recycle_vector_delete","attempt_count":0,"next_run_timestamp":700000,"claimed_timestamp":699000,"completed_timestamp":0,"last_error":"","created_timestamp":900,"updated_timestamp":699000}
]`}, nil
	}

	jobs, err := store.ClaimPendingVectorGCJobs(context.Background(), dueBefore, claimUntil, 8)
	if err != nil {
		t.Fatalf("ClaimPendingVectorGCJobs returned error: %v", err)
	}
	if len(jobs) != 2 || jobs[0].ID != 51 || jobs[1].ID != 52 {
		t.Fatalf("expected claimed vector jobs sorted by id, got %+v", jobs)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	for _, fragment := range []string{"UPDATE vmm_vector_gc_jobs", "WHERE completed_timestamp = 0", "ORDER BY next_run_timestamp ASC, id ASC", "LIMIT ?", "RETURNING id, batch_id, vector_id"} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("expected vector claim sql to contain %q, got %q", fragment, sql)
		}
	}
	for _, forbidden := range []string{"BEGIN IMMEDIATE;", "COMMIT;", "ROLLBACK;"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("expected vector claim sql to stay single-statement, got %q", sql)
		}
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	params := captured.GetParams()
	if len(params) != 5 {
		t.Fatalf("expected five vector claim params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 <= 0 || params[2].Kind != sqliteffi.SQLValueInt64 || params[2].Int64 != params[0].Int64 {
		t.Fatalf("expected claimed and updated timestamps to share one positive claim time, got %#v and %#v", params[0], params[2])
	}
	if params[1].Kind != sqliteffi.SQLValueInt64 || params[1].Int64 != claimUntil.UnixMilli() {
		t.Fatalf("expected claim-until timestamp param, got %#v", params[1])
	}
	if params[3].Kind != sqliteffi.SQLValueInt64 || params[3].Int64 != dueBefore.UnixMilli() {
		t.Fatalf("expected due-before timestamp param, got %#v", params[3])
	}
	if params[4].Kind != sqliteffi.SQLValueInt64 || params[4].Int64 != 8 {
		t.Fatalf("expected limit param, got %#v", params[4])
	}
}

// TestClaimPendingVectorGCJobsMarksCommitUnknownAsOutcomeUncertain verifies vector-gc claim failures at SQLite's commit boundary preserve ambiguity for retry orchestration.
// TestClaimPendingVectorGCJobsMarksCommitUnknownAsOutcomeUncertain 用于验证 vector-gc 领取在 SQLite 提交边界失败时会为重试编排保留结果不确定语义。
func TestClaimPendingVectorGCJobsMarksCommitUnknownAsOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(context.Context, *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		return nil, errors.New("commit outcome unknown after vector claim")
	}

	_, err := store.ClaimPendingVectorGCJobs(context.Background(), time.Unix(600, 0).UTC(), time.Unix(700, 0).UTC(), 8)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain vector claim error, got %v", err)
	}
	if !strings.Contains(err.Error(), "claim sqlite pending vector gc jobs") || !strings.Contains(err.Error(), "commit outcome unknown after vector claim") {
		t.Fatalf("unexpected vector claim error text: %v", err)
	}
}

// TestCompleteVectorGCJobsUsesTypedIDParams verifies completed vector-gc cleanup deletes normalized ids through typed placeholders.
// TestCompleteVectorGCJobsUsesTypedIDParams 用于验证已完成 vector-gc 任务清理会通过强类型占位符删除规范化后的 id。
func TestCompleteVectorGCJobsUsesTypedIDParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
	}

	if err := store.CompleteVectorGCJobs(context.Background(), []uint64{52, 0, 51, 52}, time.Unix(800, 0).UTC()); err != nil {
		t.Fatalf("CompleteVectorGCJobs returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected ExecuteScript request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "DELETE FROM vmm_vector_gc_jobs") || !strings.Contains(sql, "WHERE id IN (?,?)") {
		t.Fatalf("expected placeholder vector delete SQL, got %q", sql)
	}
	for _, forbidden := range []string{"BEGIN IMMEDIATE;", "COMMIT;", "51", "52"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("expected completed vector job ids to stay out of SQL text, got %q", sql)
		}
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	params := captured.GetParams()
	if len(params) != 2 {
		t.Fatalf("expected two completed vector job id params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 != 51 ||
		params[1].Kind != sqliteffi.SQLValueInt64 || params[1].Int64 != 52 {
		t.Fatalf("expected normalized completed vector job ids 51 and 52, got %#v", params)
	}
}

// TestCompleteVectorGCJobsRejectsPartialDelete verifies vector-GC completion cannot silently miss claimed retry jobs.
// TestCompleteVectorGCJobsRejectsPartialDelete 用于验证向量 GC 完成流程不能静默漏删已领取重试任务。
func TestCompleteVectorGCJobsRejectsPartialDelete(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	err := store.CompleteVectorGCJobs(context.Background(), []uint64{52, 51}, time.Unix(800, 0).UTC())
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain complete vector gc jobs error, got %v", err)
	}
}

// TestCompleteVectorGCJobsMarksCommitUnknownAsOutcomeUncertain verifies commit-boundary failures keep the vector-GC completion outcome uncertain.
// TestCompleteVectorGCJobsMarksCommitUnknownAsOutcomeUncertain 用于验证提交边界失败会保留向量 GC 完成结果不确定语义。
func TestCompleteVectorGCJobsMarksCommitUnknownAsOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("failed to commit sqlite vector gc completion")
	}

	err := store.CompleteVectorGCJobs(context.Background(), []uint64{52, 51}, time.Unix(800, 0).UTC())
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected commit-unknown complete vector gc jobs error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "complete sqlite vector gc jobs") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected complete vector gc jobs commit error: %v", err)
	}
}

// TestRetryVectorGCJobsUsesTypedSQLiteParams verifies vector retry diagnostics and normalized job ids are bound as typed params.
// TestRetryVectorGCJobsUsesTypedSQLiteParams 用于验证向量重试诊断信息和规范化后的 job id 都会作为强类型参数绑定。
func TestRetryVectorGCJobsUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
	}

	lastError := "  vector gc failed '; DROP TABLE vmm_vector_gc_jobs;  "
	if err := store.RetryVectorGCJobs(context.Background(), []uint64{52, 51, 52}, time.Unix(11, 0).UTC(), lastError); err != nil {
		t.Fatalf("RetryVectorGCJobs returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected ExecuteScript request to be captured")
	}
	if strings.Contains(captured.GetSql(), "DROP TABLE") || strings.Contains(captured.GetSql(), "vector gc failed") {
		t.Fatalf("retry vector last_error leaked into SQL: %q", captured.GetSql())
	}
	if !strings.Contains(captured.GetSql(), "WHERE id IN (?,?)") || !strings.Contains(captured.GetSql(), "completed_timestamp = 0") {
		t.Fatalf("expected normalized vector retry predicate in SQL, got %q", captured.GetSql())
	}
	for _, forbidden := range []string{"BEGIN", "COMMIT", "ROLLBACK", "51", "52"} {
		if strings.Contains(captured.GetSql(), forbidden) {
			t.Fatalf("retry vector SQL should keep ids and transaction control out of text, got %q", captured.GetSql())
		}
	}
	params := captured.GetParams()
	if len(params) != 5 {
		t.Fatalf("expected five typed params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 != time.Unix(11, 0).UTC().UnixMilli() {
		t.Fatalf("expected next_run_timestamp param, got %#v", params[0])
	}
	if params[1].Kind != sqliteffi.SQLValueString || params[1].String != strings.TrimSpace(lastError) {
		t.Fatalf("expected last_error string param, got %#v", params[1])
	}
	if params[2].Kind != sqliteffi.SQLValueInt64 || params[2].Int64 == 0 {
		t.Fatalf("expected updated_timestamp int64 param, got %#v", params[2])
	}
	if params[3].Kind != sqliteffi.SQLValueInt64 || params[3].Int64 != 51 ||
		params[4].Kind != sqliteffi.SQLValueInt64 || params[4].Int64 != 52 {
		t.Fatalf("expected normalized retry vector job ids 51 and 52, got %#v", params[3:])
	}
}

// TestRetryVectorGCJobsRejectsPartialUpdate verifies vector-GC retry cannot silently miss claimed retry jobs.
// TestRetryVectorGCJobsRejectsPartialUpdate 用于验证向量 GC 重试流程不能静默漏掉已领取重试任务。
func TestRetryVectorGCJobsRejectsPartialUpdate(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	err := store.RetryVectorGCJobs(context.Background(), []uint64{52, 51}, time.Unix(11, 0).UTC(), "failed")
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain retry vector gc jobs error, got %v", err)
	}
}

// TestRetryVectorGCJobsMarksCommitUnknownAsOutcomeUncertain verifies commit-boundary failures keep the vector-GC retry update outcome uncertain.
// TestRetryVectorGCJobsMarksCommitUnknownAsOutcomeUncertain 用于验证提交边界失败会保留向量 GC 重试更新结果不确定语义。
func TestRetryVectorGCJobsMarksCommitUnknownAsOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("commit outcome unknown after vector gc retry")
	}

	err := store.RetryVectorGCJobs(context.Background(), []uint64{52, 51}, time.Unix(11, 0).UTC(), "failed")
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected commit-unknown retry vector gc jobs error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "retry sqlite vector gc jobs") || !strings.Contains(err.Error(), "commit outcome unknown") {
		t.Fatalf("unexpected retry vector gc jobs commit error: %v", err)
	}
}
