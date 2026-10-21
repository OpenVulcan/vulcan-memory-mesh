// Behavioral tests grouped by retention session test.
// SQLite 适配层闲置会话回收行为测试，沿用同包夹具验证对应业务链路。
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

// TestRecycleIdleSessionsMovesStaleSessionRowsIntoTrash verifies idle-session recycle moves stale memories and eligible turns into the trash tables.
// TestRecycleIdleSessionsMovesStaleSessionRowsIntoTrash 用于验证 idle-session 回收会把陈旧记忆和可归档 turn 迁入回收站。
func TestRecycleIdleSessionsMovesStaleSessionRowsIntoTrash(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	expectSQLiteFTSDeletes(t, fake, "31", "32")
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
