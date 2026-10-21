// Behavioral tests grouped by retention turn test.
// SQLite 适配层冷轮次回收行为测试，沿用同包夹具验证对应业务链路。
package vldb_sqlite

import (
	"context"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"testing"
	"time"
)

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
