// SQLite adapter tests for turn append test.
// SQLite 适配层轮次追加测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	"context"
	"errors"
	"fmt"
	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sqliteSessionRowJSON renders one vmm_sessions query response row for append-counter tests.
// sqliteSessionRowJSON 用于为追加计数测试渲染一条 vmm_sessions 查询响应行。
func sqliteSessionRowJSON(sessionID, projectID uint64, turnCount, summarizeBudget int, updatedMs int64) string {
	return fmt.Sprintf(
		`[{"id":%d,"session_key":"sess-append","user_id":7,"team_id":8,"space_id":9,"project_id":%d,"turn_count":%d,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":%d,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":%d}]`,
		sessionID,
		projectID,
		turnCount,
		summarizeBudget,
		updatedMs,
	)
}

// sqliteCompactSessionRowJSON renders one vmm_sessions query response row for compact-boundary reconciliation tests.
// sqliteCompactSessionRowJSON 用于为 compact 边界对账测试渲染一条 vmm_sessions 查询响应行。
func sqliteCompactSessionRowJSON(sessionID, lastCompactedTurnID uint64, compactedMs, updatedMs int64) string {
	return fmt.Sprintf(
		`[{"id":%d,"session_key":"sess-compact","user_id":7,"team_id":8,"space_id":9,"project_id":10,"turn_count":11,"last_summarized_id":0,"last_compacted_turn_id":%d,"summarize_content":"","summarize_budget":12,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":%d,"created_timestamp":1,"updated_timestamp":%d}]`,
		sessionID,
		lastCompactedTurnID,
		compactedMs,
		updatedMs,
	)
}

// TestStoreAppendTurnRecordUsesTypedSQLiteParamsForSplitWrites verifies cleaned turn payloads are inserted as typed params while session counter writes avoid unsupported cross-call transaction control.
// TestStoreAppendTurnRecordUsesTypedSQLiteParamsForSplitWrites 用于验证清洗后 turn 载荷会作为强类型参数插入，并且 session 计数写入不会依赖不受支持的跨调用事务控制。
func TestStoreAppendTurnRecordUsesTypedSQLiteParamsForSplitWrites(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	// Capture the split write requests so the test can inspect both SQL templates and typed parameters.
	// 捕获拆分后的写入请求，让测试同时检查 SQL 模板和强类型参数。
	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "id IN (?,?)") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_sessions") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			return &fakeQueryJSONResponse{JsonData: sqliteSessionRowJSON(33, 9, 12, 40, 1000)}, nil
		}
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_turn_records") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":501}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	// Build the expected dehydrated JSON through the same persistence serializer so the assertion follows the real storage contract.
	// 通过同一个持久化序列化器构造预期脱水 JSON，让断言跟随真实存储契约。
	createdAt := time.Date(2026, 7, 5, 12, 30, 0, 0, time.UTC)
	turn := logicdomain.TurnRecord{
		UserContent: "  turn user '; DROP TABLE vmm_turn_records; --  ",
		Timeline: []logicdomain.TurnTimelineItem{{
			Type:    "user",
			Content: "  timeline user '; DROP TABLE vmm_turn_records; --  ",
		}},
		AssistantContent: "  turn assistant '; DROP TABLE vmm_sessions; --  ",
		CreatedAt:        createdAt,
	}
	expectedPayload, expectedBudget, err := storageutil.DehydrateTurn(turn)
	if err != nil {
		t.Fatalf("buildDehydratedTurn returned error: %v", err)
	}

	persisted, err := store.AppendTurnRecord(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, ProjectID: 9},
		turn,
	)
	if err != nil {
		t.Fatalf("AppendTurnRecord returned error: %v", err)
	}
	if persisted.ID != 501 || persisted.SessionID != 33 || persisted.ProjectID != 9 || persisted.DehydratedBudget != expectedBudget {
		t.Fatalf("unexpected persisted turn: %+v", persisted)
	}
	if !persisted.CreatedAt.Equal(createdAt) {
		t.Fatalf("created_at = %v, want %v", persisted.CreatedAt, createdAt)
	}
	if len(requests) != 2 {
		t.Fatalf("expected insert and update requests, got %d", len(requests))
	}
	requireNoSQLiteTransactionControl(t, requests)

	capturedSQL := sqliteExecutedSQLLog(requests)
	for _, leaked := range []string{"DROP TABLE", "turn user", "timeline user", "turn assistant"} {
		if strings.Contains(capturedSQL, leaked) {
			t.Fatalf("expected turn payload text %q to stay out of SQL text, got %q", leaked, capturedSQL)
		}
	}
	for _, request := range requests {
		if strings.TrimSpace(request.GetParamsJson()) != "" {
			t.Fatalf("expected typed params only, got params_json=%q", request.GetParamsJson())
		}
	}
	if got := countSQLiteStringParam(requests, expectedPayload); got != 1 {
		t.Fatalf("expected dehydrated payload param exactly once, got %d", got)
	}

	insertParams := requests[0].GetParams()
	if len(insertParams) != 8 {
		t.Fatalf("expected eight turn insert params, got %d: %#v", len(insertParams), insertParams)
	}
	if insertParams[3].Kind != sqliteffi.SQLValueString || insertParams[3].String != expectedPayload {
		t.Fatalf("expected dehydrated payload param, got %#v", insertParams[3])
	}
	if insertParams[4].Kind != sqliteffi.SQLValueInt64 || insertParams[4].Int64 != int64(expectedBudget) {
		t.Fatalf("expected dehydrated budget param, got %#v", insertParams[4])
	}
	updateParams := requests[1].GetParams()
	if len(updateParams) != 5 {
		t.Fatalf("expected five session update params, got %d: %#v", len(updateParams), updateParams)
	}
	if updateParams[0].Kind != sqliteffi.SQLValueInt64 || updateParams[0].Int64 != 13 {
		t.Fatalf("expected absolute turn count param, got %#v", updateParams[0])
	}
	if updateParams[1].Kind != sqliteffi.SQLValueInt64 || updateParams[1].Int64 != int64(40+expectedBudget) {
		t.Fatalf("expected absolute summarize budget param, got %#v", updateParams[1])
	}
	if updateParams[2].Kind != sqliteffi.SQLValueInt64 || updateParams[3].Kind != sqliteffi.SQLValueInt64 || updateParams[2].Int64 != updateParams[3].Int64 || updateParams[2].Int64 <= 0 {
		t.Fatalf("expected paired updated timestamp params, got %#v", updateParams[2:4])
	}
	if updateParams[4].Kind != sqliteffi.SQLValueInt64 || updateParams[4].Int64 != 33 {
		t.Fatalf("expected session id param, got %#v", updateParams[4])
	}
}

// TestStoreAppendTurnRecordRejectsInsertRowsChangedDrift verifies a turn insert that reports no inserted row stays ordinary and stops before the session counter update.
// TestStoreAppendTurnRecordRejectsInsertRowsChangedDrift 用于验证当 turn 插入报告未插入任何行时，会保持普通错误并在 session 计数更新前停止。
func TestStoreAppendTurnRecordRejectsInsertRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	// Allocate a deterministic turn id so the test isolates affected-row verification instead of id generation.
	// 分配确定性的 turn ID，让测试聚焦影响行数验收，而不是 ID 生成。
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_sessions") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			return &fakeQueryJSONResponse{JsonData: sqliteSessionRowJSON(33, 9, 0, 0, 1000)}, nil
		}
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_turn_records") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":502}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	// Return zero changed rows for the insert because SQLite INSERT should report exactly one durable row for this append contract.
	// 为插入返回零影响行，因为该追加契约要求 SQLite INSERT 精确报告一条长期行。
	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}

	_, err := store.AppendTurnRecord(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, ProjectID: 9},
		logicdomain.TurnRecord{UserContent: "hello"},
	)
	if err == nil {
		t.Fatal("expected turn insert drift error")
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected turn insert drift to stay ordinary before any mutation, got %v", err)
	}
	if !strings.Contains(err.Error(), "insert appended turn record") || !strings.Contains(err.Error(), "affected 0 rows, want 1") {
		t.Fatalf("unexpected insert drift error: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected only the turn insert request, got %d", len(requests))
	}
	if !strings.Contains(requests[0].GetSql(), "INSERT INTO vmm_turn_records") {
		t.Fatalf("expected first request to insert turn, got %q", requests[0].GetSql())
	}
}

// TestStoreAppendTurnRecordRejectsSessionProjectMismatchBeforeInsert verifies stale resolved scopes cannot create turns under the wrong project.
// TestStoreAppendTurnRecordRejectsSessionProjectMismatchBeforeInsert 用于验证陈旧的已解析范围不能在错误 project 下创建 turn。
func TestStoreAppendTurnRecordRejectsSessionProjectMismatchBeforeInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_turn_records") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":508}]`}, nil
		}
		if strings.Contains(req.GetSql(), "FROM vmm_sessions") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			return &fakeQueryJSONResponse{JsonData: sqliteSessionRowJSON(33, 10, 0, 0, 1000)}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		t.Fatalf("unexpected ExecuteScript after session project mismatch: %q", req.GetSql())
		return nil, nil
	}

	_, err := store.AppendTurnRecord(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, ProjectID: 9},
		logicdomain.TurnRecord{UserContent: "hello"},
	)
	if err == nil || !logicdomain.IsConflictError(err) {
		t.Fatalf("expected session project conflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "belongs to project 10") || !strings.Contains(err.Error(), "not project 9") {
		t.Fatalf("unexpected session project conflict error: %v", err)
	}
}

// TestStoreAppendTurnRecordReconcilesCommitUnknownInsert verifies an ambiguous turn insert is recovered only after the exact row is visible by primary key.
// TestStoreAppendTurnRecordReconcilesCommitUnknownInsert 用于验证 turn 插入提交结果不明时，仅在按主键回读到精确行后恢复。
func TestStoreAppendTurnRecordReconcilesCommitUnknownInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	createdAt := time.Date(2026, 7, 6, 8, 30, 0, 123000000, time.UTC)
	turn := logicdomain.TurnRecord{UserContent: "hello", AssistantContent: "reply", CreatedAt: createdAt}
	expectedPayload, expectedBudget, err := storageutil.DehydrateTurn(turn)
	if err != nil {
		t.Fatalf("buildDehydratedTurn returned error: %v", err)
	}

	// Capture the real typed INSERT params so the read-back row mirrors the exact append attempt, including the runtime updated timestamp.
	// 捕获真实的强类型 INSERT 参数，让回读行精确镜像本次追加尝试，包括运行时生成的更新时间戳。
	requests := []*fakeExecuteRequest{}
	var capturedInsertParams []sqliteffi.SQLValue
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_turn_records") {
			capturedInsertParams = append([]sqliteffi.SQLValue(nil), req.GetParams()...)
			return nil, errors.New("failed to commit turn insert")
		}
		if strings.Contains(req.GetSql(), "UPDATE vmm_sessions") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		}
		t.Fatalf("unexpected ExecuteScript SQL: %q", req.GetSql())
		return nil, nil
	}

	// Serve the id allocation first, then expose the exact committed row during insert reconciliation.
	// 先提供 ID 分配结果，再在插入对账阶段暴露精确落库行。
	reconcileReads := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_sessions") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			return &fakeQueryJSONResponse{JsonData: sqliteSessionRowJSON(33, 9, 5, 60, 1000)}, nil
		}
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_turn_records") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":504}]`}, nil
		}
		if strings.Contains(req.GetSql(), "FROM vmm_turn_records") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			reconcileReads++
			if len(capturedInsertParams) != 8 {
				t.Fatalf("expected captured insert params, got %#v", capturedInsertParams)
			}
			params := req.GetParams()
			if len(params) != 1 || params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 != 504 {
				t.Fatalf("expected reconcile query by turn id 504, got %#v", params)
			}
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(
				`[{"id":%d,"session_id":%d,"project_id":%d,"dehydrated_content":%s,"dehydrated_budget":%d,"extracted_status":%d,"details":"","details_budget":0,"created_timestamp":%d,"updated_timestamp":%d}]`,
				capturedInsertParams[0].Int64,
				capturedInsertParams[1].Int64,
				capturedInsertParams[2].Int64,
				strconv.Quote(capturedInsertParams[3].String),
				capturedInsertParams[4].Int64,
				capturedInsertParams[5].Int64,
				capturedInsertParams[6].Int64,
				capturedInsertParams[7].Int64,
			)}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	persisted, err := store.AppendTurnRecord(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, ProjectID: 9},
		turn,
	)
	if err != nil {
		t.Fatalf("AppendTurnRecord returned error: %v", err)
	}
	if persisted.ID != 504 || persisted.SessionID != 33 || persisted.ProjectID != 9 || persisted.DehydratedBudget != expectedBudget {
		t.Fatalf("unexpected persisted turn: %+v", persisted)
	}
	if !persisted.CreatedAt.Equal(createdAt) {
		t.Fatalf("created_at = %v, want %v", persisted.CreatedAt, createdAt)
	}
	if reconcileReads != 1 {
		t.Fatalf("expected one reconcile read, got %d", reconcileReads)
	}
	if len(requests) != 2 {
		t.Fatalf("expected recovered insert and session update requests, got %d", len(requests))
	}
	if !strings.Contains(requests[1].GetSql(), "UPDATE vmm_sessions") {
		t.Fatalf("expected recovered append to update session counters, got %q", requests[1].GetSql())
	}
	if got := countSQLiteStringParam(requests, expectedPayload); got != 1 {
		t.Fatalf("expected dehydrated payload param exactly once, got %d", got)
	}
}

// TestStoreAppendTurnRecordKeepsMismatchedCommitUnknownUncertain verifies ambiguous inserts are not recovered from unrelated rows that merely reuse the allocated id.
// TestStoreAppendTurnRecordKeepsMismatchedCommitUnknownUncertain 用于验证插入提交不明时，不会用仅复用了分配 ID 的非匹配行恢复。
func TestStoreAppendTurnRecordKeepsMismatchedCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	turn := logicdomain.TurnRecord{UserContent: "hello", CreatedAt: time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)}
	requests := []*fakeExecuteRequest{}
	var capturedInsertParams []sqliteffi.SQLValue

	// Return commit-unknown for the insert and fail the test if the store incorrectly advances to the session counter update.
	// 为插入返回提交结果不明；如果存储层错误推进到 session 计数更新，则测试直接失败。
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_turn_records") {
			capturedInsertParams = append([]sqliteffi.SQLValue(nil), req.GetParams()...)
			return nil, errors.New("failed to commit turn insert")
		}
		t.Fatalf("unexpected ExecuteScript after mismatched reconcile row: %q", req.GetSql())
		return nil, nil
	}

	// Return a row under the allocated id with different dehydrated content so reconciliation must preserve uncertainty.
	// 返回一条占用已分配 ID 但脱水内容不同的行，要求对账必须保留不确定状态。
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_sessions") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			return &fakeQueryJSONResponse{JsonData: sqliteSessionRowJSON(33, 9, 5, 60, 1000)}, nil
		}
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_turn_records") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":505}]`}, nil
		}
		if strings.Contains(req.GetSql(), "FROM vmm_turn_records") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			if len(capturedInsertParams) != 8 {
				t.Fatalf("expected captured insert params, got %#v", capturedInsertParams)
			}
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(
				`[{"id":%d,"session_id":%d,"project_id":%d,"dehydrated_content":%s,"dehydrated_budget":%d,"extracted_status":%d,"details":"","details_budget":0,"created_timestamp":%d,"updated_timestamp":%d}]`,
				capturedInsertParams[0].Int64,
				capturedInsertParams[1].Int64,
				capturedInsertParams[2].Int64,
				strconv.Quote(`{"user":"different"}`),
				capturedInsertParams[4].Int64,
				capturedInsertParams[5].Int64,
				capturedInsertParams[6].Int64,
				capturedInsertParams[7].Int64,
			)}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.AppendTurnRecord(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, ProjectID: 9},
		turn,
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain mismatched reconcile error, got %v", err)
	}
	if !strings.Contains(err.Error(), "append turn record insert") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected mismatched reconcile error: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected only the turn insert request, got %d", len(requests))
	}
}

// TestStoreAppendTurnRecordReturnsOutcomeUncertainWhenSessionCounterDrifts verifies a post-insert session counter miss is not reported as a clean append failure.
// TestStoreAppendTurnRecordReturnsOutcomeUncertainWhenSessionCounterDrifts 用于验证插入后 session 计数更新未命中时，不会被上报成可安全重试的干净追加失败。
func TestStoreAppendTurnRecordReturnsOutcomeUncertainWhenSessionCounterDrifts(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	// Allocate a deterministic turn id so the request order and post-insert drift are easy to inspect.
	// 分配确定性的 turn ID，让请求顺序和插入后的漂移更容易检查。
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_sessions") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			return &fakeQueryJSONResponse{JsonData: sqliteSessionRowJSON(33, 9, 8, 90, 1000)}, nil
		}
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_turn_records") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":503}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	// Confirm the turn insert, then make the session counter update miss so the store must mark the append outcome as uncertain.
	// 先确认 turn 插入，再让 session 计数更新未命中，要求存储层把追加结果标记为不确定。
	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_turn_records") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}

	_, err := store.AppendTurnRecord(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, ProjectID: 9},
		logicdomain.TurnRecord{UserContent: "hello"},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain session counter drift, got %v", err)
	}
	if !strings.Contains(err.Error(), "append turn record") || !strings.Contains(err.Error(), "update appended turn session counters affected 0 rows, want 1") {
		t.Fatalf("unexpected session counter drift error: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected turn insert and session update requests, got %d", len(requests))
	}
	if !strings.Contains(requests[1].GetSql(), "UPDATE vmm_sessions") {
		t.Fatalf("expected second request to update session counters, got %q", requests[1].GetSql())
	}
}

// TestStoreAppendTurnRecordReconcilesCommitUnknownSessionCounterUpdate verifies an ambiguous session counter update is recovered from the absolute counter target.
// TestStoreAppendTurnRecordReconcilesCommitUnknownSessionCounterUpdate 用于验证 session 计数更新提交结果不明时，可以通过绝对计数目标恢复。
func TestStoreAppendTurnRecordReconcilesCommitUnknownSessionCounterUpdate(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	turn := logicdomain.TurnRecord{UserContent: "hello", CreatedAt: time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)}
	_, expectedBudget, err := storageutil.DehydrateTurn(turn)
	if err != nil {
		t.Fatalf("buildDehydratedTurn returned error: %v", err)
	}

	requests := []*fakeExecuteRequest{}
	var capturedUpdateParams []sqliteffi.SQLValue
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_turn_records") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		}
		if strings.Contains(req.GetSql(), "UPDATE vmm_sessions") {
			capturedUpdateParams = append([]sqliteffi.SQLValue(nil), req.GetParams()...)
			return nil, errors.New("failed to commit session counter update")
		}
		t.Fatalf("unexpected ExecuteScript SQL: %q", req.GetSql())
		return nil, nil
	}

	// Return the pre-update session first, then the post-update absolute target during commit-unknown reconciliation.
	// 先返回更新前 session，再在提交未知对账阶段返回更新后的绝对目标。
	sessionReads := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_turn_records") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":506}]`}, nil
		}
		if strings.Contains(req.GetSql(), "FROM vmm_sessions") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			sessionReads++
			if len(capturedUpdateParams) == 0 {
				return &fakeQueryJSONResponse{JsonData: sqliteSessionRowJSON(33, 9, 8, 90, 1000)}, nil
			}
			return &fakeQueryJSONResponse{JsonData: sqliteSessionRowJSON(33, 9, int(capturedUpdateParams[0].Int64), int(capturedUpdateParams[1].Int64), capturedUpdateParams[2].Int64+10)}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	persisted, err := store.AppendTurnRecord(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, ProjectID: 9},
		turn,
	)
	if err != nil {
		t.Fatalf("AppendTurnRecord returned error: %v", err)
	}
	if persisted.ID != 506 || persisted.DehydratedBudget != expectedBudget {
		t.Fatalf("unexpected persisted turn: %+v", persisted)
	}
	if sessionReads != 2 {
		t.Fatalf("expected pre-update and reconcile session reads, got %d", sessionReads)
	}
	if len(requests) != 2 {
		t.Fatalf("expected turn insert and session update requests, got %d", len(requests))
	}
	if len(capturedUpdateParams) != 5 || capturedUpdateParams[0].Int64 != 9 || capturedUpdateParams[1].Int64 != int64(90+expectedBudget) {
		t.Fatalf("unexpected absolute session update params: %#v", capturedUpdateParams)
	}
}

// TestStoreAppendTurnRecordKeepsUnmatchedCommitUnknownSessionCounterUpdateUncertain verifies ambiguous counter updates are not recovered when the session row misses the target.
// TestStoreAppendTurnRecordKeepsUnmatchedCommitUnknownSessionCounterUpdateUncertain 用于验证 session 计数更新提交不明且回读未达目标时，仍保持结果不确定。
func TestStoreAppendTurnRecordKeepsUnmatchedCommitUnknownSessionCounterUpdateUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	var capturedUpdateParams []sqliteffi.SQLValue
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_turn_records") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		}
		if strings.Contains(req.GetSql(), "UPDATE vmm_sessions") {
			capturedUpdateParams = append([]sqliteffi.SQLValue(nil), req.GetParams()...)
			return nil, errors.New("failed to commit session counter update")
		}
		t.Fatalf("unexpected ExecuteScript SQL: %q", req.GetSql())
		return nil, nil
	}

	// Keep the old counters visible after the ambiguous update so reconciliation cannot prove the session counter write landed.
	// 在不明确更新之后仍暴露旧计数，让对账无法证明 session 计数写入已经落库。
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_turn_records") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":507}]`}, nil
		}
		if strings.Contains(req.GetSql(), "FROM vmm_sessions") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			if len(capturedUpdateParams) == 0 {
				return &fakeQueryJSONResponse{JsonData: sqliteSessionRowJSON(33, 9, 8, 90, 1000)}, nil
			}
			return &fakeQueryJSONResponse{JsonData: sqliteSessionRowJSON(33, 9, 8, 90, capturedUpdateParams[2].Int64+10)}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.AppendTurnRecord(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, ProjectID: 9},
		logicdomain.TurnRecord{UserContent: "hello"},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain session update reconcile miss, got %v", err)
	}
	if !strings.Contains(err.Error(), "append turn record") || !strings.Contains(err.Error(), "failed to commit session counter update") {
		t.Fatalf("unexpected session update reconcile miss error: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected turn insert and session update requests, got %d", len(requests))
	}
}
