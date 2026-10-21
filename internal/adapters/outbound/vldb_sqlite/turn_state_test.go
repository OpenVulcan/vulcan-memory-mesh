// SQLite adapter tests for turn state test.
// SQLite 适配层轮次读取与状态更新测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	"context"
	"errors"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestStoreLoadTurnsByIDsUsesTypedIDParams verifies turn-detail batch lookups bind normalized turn ids instead of rendering them into SQL text.
// TestStoreLoadTurnsByIDsUsesTypedIDParams 用于验证 turn 详情批量查询会绑定规范化 turn id，而不是把它们渲染进 SQL 文本。
func TestStoreLoadTurnsByIDsUsesTypedIDParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[
{"id":41,"session_id":7,"project_id":9,"dehydrated_content":"{\"user\":\"a\"}","dehydrated_budget":1,"extracted_status":1,"details":"turn a","details_budget":1,"created_timestamp":10,"updated_timestamp":20},
{"id":42,"session_id":7,"project_id":9,"dehydrated_content":"{\"assistant\":\"b\"}","dehydrated_budget":1,"extracted_status":1,"details":"turn b","details_budget":1,"created_timestamp":11,"updated_timestamp":21}
]`}, nil
	}

	rows, err := store.LoadTurnsByIDs(context.Background(), []uint64{42, 0, 41, 42})
	if err != nil {
		t.Fatalf("LoadTurnsByIDs returned error: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != 41 || rows[1].ID != 42 {
		t.Fatalf("unexpected loaded turns: %+v", rows)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "WHERE id IN (?,?)") {
		t.Fatalf("expected typed turn placeholders, got %q", sql)
	}
	if strings.Contains(sql, "41") || strings.Contains(sql, "42") {
		t.Fatalf("expected turn ids to stay out of LoadTurnsByIDs SQL, got %q", sql)
	}
	requireSQLiteInt64Params(t, captured.GetParams(), 41, 42)
}

// TestStoreLoadTurnWindowsUsesTypedAnchorAndRadiusParams verifies turn-window lookups bind anchor ids and radius as typed SQLite params.
// TestStoreLoadTurnWindowsUsesTypedAnchorAndRadiusParams 用于验证 turn 窗口查询会把锚点 id 和半径都作为 SQLite 强类型参数绑定。
func TestStoreLoadTurnWindowsUsesTypedAnchorAndRadiusParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[
{"target_id":41,"turn_id":39,"relative_pos":-2},
{"target_id":42,"turn_id":43,"relative_pos":1}
]`}, nil
	}

	windows, err := store.LoadTurnWindows(context.Background(), []uint64{42, 41, 42}, 2)
	if err != nil {
		t.Fatalf("LoadTurnWindows returned error: %v", err)
	}
	if len(windows) != 2 || len(windows[41].PreviousTurnIDs) != 1 || windows[41].PreviousTurnIDs[0] != 39 || len(windows[42].NextTurnIDs) != 1 || windows[42].NextTurnIDs[0] != 43 {
		t.Fatalf("unexpected turn windows: %+v", windows)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "WHERE id IN (?,?)") || !strings.Contains(sql, "t.target_rn - ?") || !strings.Contains(sql, "t.target_rn + ?") {
		t.Fatalf("expected typed anchor and radius placeholders, got %q", sql)
	}
	if strings.Contains(sql, "41") || strings.Contains(sql, "42") {
		t.Fatalf("expected anchor turn ids to stay out of LoadTurnWindows SQL, got %q", sql)
	}
	requireSQLiteInt64Params(t, captured.GetParams(), 41, 42, 2, 2)
}

// sqliteCorruptedTurnRowJSON renders one vmm_turn_records row used by corrupted-turn mark reconciliation tests.
// sqliteCorruptedTurnRowJSON 用于渲染损坏 turn 标记对账测试使用的一条 vmm_turn_records 行。
func sqliteCorruptedTurnRowJSON(turnID, sessionID, projectID uint64, status int, details string, detailsBudget int, createdMs, updatedMs int64) string {
	return fmt.Sprintf(
		`[{"id":%d,"session_id":%d,"project_id":%d,"dehydrated_content":%s,"dehydrated_budget":3,"extracted_status":%d,"details":%s,"details_budget":%d,"created_timestamp":%d,"updated_timestamp":%d}]`,
		turnID,
		sessionID,
		projectID,
		strconv.Quote(`{"user":"broken"}`),
		status,
		strconv.Quote(details),
		detailsBudget,
		createdMs,
		updatedMs,
	)
}

// TestStoreMarkTurnAsCorruptedUsesTypedSQLiteParams verifies corrupted-turn status writes bind every state and identity value.
// TestStoreMarkTurnAsCorruptedUsesTypedSQLiteParams 用于验证损坏 turn 状态写入会绑定所有状态值和身份值。
func TestStoreMarkTurnAsCorruptedUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_turn_records") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			requireSQLiteInt64Params(t, req.GetParams(), 77)
			return &fakeQueryJSONResponse{JsonData: sqliteCorruptedTurnRowJSON(77, 88, 9, logicdomain.TurnExtractedStatusPending, "", 0, 10, 20)}, nil
		}
		t.Fatalf("unexpected corrupted-turn query: %q", req.GetSql())
		return nil, nil
	}
	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	err := store.MarkTurnAsCorrupted(context.Background(), logicdomain.SessionRef{SessionID: 88}, 77)
	if err != nil {
		t.Fatalf("MarkTurnAsCorrupted returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected ExecuteScript request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "SET extracted_status = ?, updated_timestamp = ?") ||
		!strings.Contains(sql, "WHERE id = ? AND session_id = ? AND extracted_status = ?") {
		t.Fatalf("expected typed corrupted-turn placeholders, got %q", sql)
	}
	for _, leaked := range []string{"77", "88"} {
		if strings.Contains(sql, leaked) {
			t.Fatalf("expected value %q to stay out of corrupted-turn SQL, got %q", leaked, sql)
		}
	}
	params := captured.GetParams()
	if len(params) != 5 {
		t.Fatalf("expected five corrupted-turn params, got %d: %#v", len(params), params)
	}
	requireSQLiteInt64Params(t, []sqliteffi.SQLValue{params[0]}, int64(logicdomain.TurnExtractedStatusDone))
	if params[1].Kind != sqliteffi.SQLValueInt64 || params[1].Int64 <= 0 {
		t.Fatalf("expected positive updated timestamp param, got %#v", params[1])
	}
	requireSQLiteInt64Params(t, params[2:], 77, 88, int64(logicdomain.TurnExtractedStatusPending))
}

// TestStoreMarkTurnAsCorruptedRejectsMissingPendingTurn verifies the queue does not silently accept a corrupted-turn mark that matched no pending row.
// TestStoreMarkTurnAsCorruptedRejectsMissingPendingTurn 用于验证队列不会静默接受未命中 pending 行的损坏 turn 标记。
func TestStoreMarkTurnAsCorruptedRejectsMissingPendingTurn(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_turn_records") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
		t.Fatalf("unexpected corrupted-turn query: %q", req.GetSql())
		return nil, nil
	}
	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}

	err := store.MarkTurnAsCorrupted(context.Background(), logicdomain.SessionRef{SessionID: 88}, 77)
	if err == nil {
		t.Fatal("expected missing pending turn to fail")
	}
	if !strings.Contains(err.Error(), "mark turn as corrupted affected 0 rows, want 1") {
		t.Fatalf("unexpected missing pending turn error: %v", err)
	}
}

// TestStoreMarkTurnAsCorruptedWaitsForWriteLockBeforeReadingTurn verifies corrupted marks share the turn state-machine write boundary.
// TestStoreMarkTurnAsCorruptedWaitsForWriteLockBeforeReadingTurn 用于验证损坏 turn 标记会共享 turn 状态机写入边界。
func TestStoreMarkTurnAsCorruptedWaitsForWriteLockBeforeReadingTurn(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	turnQueryObserved := make(chan struct{}, 1)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_turn_records") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			turnQueryObserved <- struct{}{}
			return &fakeQueryJSONResponse{JsonData: sqliteCorruptedTurnRowJSON(77, 88, 9, logicdomain.TurnExtractedStatusPending, "", 0, 10, 20)}, nil
		}
		t.Fatalf("unexpected corrupted-turn query: %q", req.GetSql())
		return nil, nil
	}
	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	locked := false
	store.writeMu.Lock()
	locked = true
	defer func() {
		if locked {
			store.writeMu.Unlock()
		}
	}()

	done := make(chan error, 1)
	go func() {
		done <- store.MarkTurnAsCorrupted(context.Background(), logicdomain.SessionRef{SessionID: 88}, 77)
	}()

	select {
	case <-turnQueryObserved:
		t.Fatal("expected corrupted-turn query to wait until write lock is released")
	case <-time.After(50 * time.Millisecond):
	}

	store.writeMu.Unlock()
	locked = false

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("MarkTurnAsCorrupted returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("MarkTurnAsCorrupted did not finish after write lock release")
	}
}

// TestStoreMarkTurnAsCorruptedReconcilesCommitUnknown verifies commit-unknown corrupted marks are accepted only after the same turn row proves the guarded state change.
// TestStoreMarkTurnAsCorruptedReconcilesCommitUnknown 用于验证提交结果不明的损坏 turn 标记只有在同一条 turn 行证明受保护状态迁移后才会被接受。
func TestStoreMarkTurnAsCorruptedReconcilesCommitUnknown(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return nil, errors.New("failed to commit corrupted turn mark")
	}

	turnReads := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_turn_records") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			turnReads++
			requireSQLiteInt64Params(t, req.GetParams(), 77)
			if turnReads == 1 {
				return &fakeQueryJSONResponse{JsonData: sqliteCorruptedTurnRowJSON(77, 88, 9, logicdomain.TurnExtractedStatusPending, "", 0, 10, 20)}, nil
			}
			if captured == nil {
				t.Fatal("expected corrupted-turn update params before reconcile read")
			}
			return &fakeQueryJSONResponse{JsonData: sqliteCorruptedTurnRowJSON(77, 88, 9, logicdomain.TurnExtractedStatusDone, "", 0, 10, captured.GetParams()[1].Int64)}, nil
		}
		t.Fatalf("unexpected corrupted-turn query: %q", req.GetSql())
		return nil, nil
	}

	err := store.MarkTurnAsCorrupted(context.Background(), logicdomain.SessionRef{SessionID: 88}, 77)
	if err != nil {
		t.Fatalf("MarkTurnAsCorrupted returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected corrupted-turn update request")
	}
	requireSQLiteInt64Params(t, captured.GetParams()[0:1], int64(logicdomain.TurnExtractedStatusDone))
	if captured.GetParams()[1].Kind != sqliteffi.SQLValueInt64 || captured.GetParams()[1].Int64 <= 0 {
		t.Fatalf("expected positive corrupted-turn update timestamp, got %#v", captured.GetParams()[1])
	}
	requireSQLiteInt64Params(t, captured.GetParams()[2:], 77, 88, int64(logicdomain.TurnExtractedStatusPending))
	if turnReads != 2 {
		t.Fatalf("expected two corrupted-turn reads, got %d", turnReads)
	}
}

// TestStoreMarkTurnAsCorruptedKeepsUnmatchedCommitUnknownUncertain verifies unmatched commit-unknown corrupted marks remain unsafe to retry blindly.
// TestStoreMarkTurnAsCorruptedKeepsUnmatchedCommitUnknownUncertain 用于验证无法对账的提交结果不明损坏 turn 标记仍不能被盲目重试。
func TestStoreMarkTurnAsCorruptedKeepsUnmatchedCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return nil, errors.New("failed to commit corrupted turn mark")
	}

	turnReads := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_turn_records") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			turnReads++
			if turnReads == 1 {
				return &fakeQueryJSONResponse{JsonData: sqliteCorruptedTurnRowJSON(77, 88, 9, logicdomain.TurnExtractedStatusPending, "", 0, 10, 20)}, nil
			}
			if captured == nil {
				t.Fatal("expected corrupted-turn update params before reconcile read")
			}
			return &fakeQueryJSONResponse{JsonData: sqliteCorruptedTurnRowJSON(77, 88, 9, logicdomain.TurnExtractedStatusDone, "normal analysis won", 9, 10, captured.GetParams()[1].Int64)}, nil
		}
		t.Fatalf("unexpected corrupted-turn query: %q", req.GetSql())
		return nil, nil
	}

	err := store.MarkTurnAsCorrupted(context.Background(), logicdomain.SessionRef{SessionID: 88}, 77)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain corrupted-turn error, got %v", err)
	}
	if !strings.Contains(err.Error(), "mark turn as corrupted") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected corrupted-turn commit-unknown error: %v", err)
	}
	if turnReads != 2 {
		t.Fatalf("expected two corrupted-turn reads, got %d", turnReads)
	}
}

// sqliteSessionExtractWindowRowJSON renders one vmm_sessions row with extract-window checkpoints for reconcile tests.
// sqliteSessionExtractWindowRowJSON 用于为对账测试渲染一条带提炼窗口检查点的 vmm_sessions 行。
func sqliteSessionExtractWindowRowJSON(sessionID uint64, observedMs, completedMs, updatedMs int64) string {
	return fmt.Sprintf(
		`[{"id":%d,"session_key":"sess-window","user_id":7,"team_id":8,"space_id":9,"project_id":10,"turn_count":11,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":12,"last_extract_observed_timestamp":%d,"last_extract_completed_timestamp":%d,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":%d}]`,
		sessionID,
		observedMs,
		completedMs,
		updatedMs,
	)
}

// TestStoreAdvanceSessionExtractWindowChecksRowsChangedAndUsesLatestTimestamp verifies extract-window writes bind timestamps and require the resolved session row.
// TestStoreAdvanceSessionExtractWindowChecksRowsChangedAndUsesLatestTimestamp 用于验证提炼窗口写入会绑定时间戳并要求命中已解析的 session 行。
func TestStoreAdvanceSessionExtractWindowChecksRowsChangedAndUsesLatestTimestamp(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	observedAt := time.UnixMilli(3000).UTC()
	completedAt := time.UnixMilli(2000).UTC()
	if err := store.AdvanceSessionExtractWindow(context.Background(), 88, observedAt, completedAt); err != nil {
		t.Fatalf("AdvanceSessionExtractWindow returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected ExecuteScript request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "last_extract_observed_timestamp") || !strings.Contains(sql, "updated_timestamp") || !strings.Contains(sql, "WHERE id = ?") {
		t.Fatalf("unexpected extract-window SQL: %q", sql)
	}
	requireSQLiteInt64Params(t, captured.GetParams(), 3000, 3000, 2000, 2000, 3000, 3000, 88)
}

// TestStoreAdvanceSessionExtractWindowRejectsRowsChangedDrift verifies extract-window advancement cannot silently miss the resolved session row.
// TestStoreAdvanceSessionExtractWindowRejectsRowsChangedDrift 用于验证提炼窗口推进不能静默漏掉已解析的 session 行。
func TestStoreAdvanceSessionExtractWindowRejectsRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}

	err := store.AdvanceSessionExtractWindow(context.Background(), 88, time.UnixMilli(3000).UTC(), time.UnixMilli(2000).UTC())
	if err == nil {
		t.Fatal("expected extract-window row drift to fail")
	}
	if !strings.Contains(err.Error(), "advance session extract window: update session extract window affected 0 rows, want 1") {
		t.Fatalf("unexpected extract-window row drift error: %v", err)
	}
}

// TestStoreAdvanceSessionExtractWindowReconcilesCommitUnknown verifies a commit-unknown checkpoint update is accepted only after the session row proves every timestamp reached the requested boundary.
// TestStoreAdvanceSessionExtractWindowReconcilesCommitUnknown 用于验证提交结果不明的检查点更新只有在 session 回读行证明所有时间戳均达到请求边界后才会被接受。
func TestStoreAdvanceSessionExtractWindowReconcilesCommitUnknown(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return nil, errors.New("failed to commit extract window")
	}

	reconcileReads := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_sessions") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			reconcileReads++
			requireSQLiteInt64Params(t, req.GetParams(), 88)
			return &fakeQueryJSONResponse{JsonData: sqliteSessionExtractWindowRowJSON(88, 3000, 2000, 3000)}, nil
		}
		t.Fatalf("unexpected reconcile query SQL: %q", req.GetSql())
		return nil, nil
	}

	err := store.AdvanceSessionExtractWindow(context.Background(), 88, time.UnixMilli(3000).UTC(), time.UnixMilli(2000).UTC())
	if err != nil {
		t.Fatalf("AdvanceSessionExtractWindow returned error: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected one checkpoint update request, got %d", len(requests))
	}
	requireSQLiteInt64Params(t, requests[0].GetParams(), 3000, 3000, 2000, 2000, 3000, 3000, 88)
	if reconcileReads != 1 {
		t.Fatalf("expected one reconcile read, got %d", reconcileReads)
	}
}

// TestStoreAdvanceSessionExtractWindowReconcilesCommitUnknownCompletedOnly verifies zero observed timestamps stay a missing checkpoint instead of becoming a 1970 comparison target.
// TestStoreAdvanceSessionExtractWindowReconcilesCommitUnknownCompletedOnly 用于验证零 observed 时间戳会保持为未请求的检查点，而不会变成 1970 年的比较目标。
func TestStoreAdvanceSessionExtractWindowReconcilesCommitUnknownCompletedOnly(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return nil, errors.New("failed to commit extract window")
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_sessions") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			return &fakeQueryJSONResponse{JsonData: sqliteSessionExtractWindowRowJSON(88, 0, 2000, 2000)}, nil
		}
		t.Fatalf("unexpected reconcile query SQL: %q", req.GetSql())
		return nil, nil
	}

	err := store.AdvanceSessionExtractWindow(context.Background(), 88, time.Time{}, time.UnixMilli(2000).UTC())
	if err != nil {
		t.Fatalf("AdvanceSessionExtractWindow returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected checkpoint update request")
	}
	requireSQLiteInt64Params(t, captured.GetParams(), 0, 0, 2000, 2000, 2000, 2000, 88)
}

// TestStoreAdvanceSessionExtractWindowKeepsUnmatchedCommitUnknownUncertain verifies commit-unknown updates stay uncertain when the read-back session row has not reached the requested checkpoint.
// TestStoreAdvanceSessionExtractWindowKeepsUnmatchedCommitUnknownUncertain 用于验证提交结果不明时，如果回读 session 行没有达到请求检查点，就必须保留结果不确定语义。
func TestStoreAdvanceSessionExtractWindowKeepsUnmatchedCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("failed to commit extract window")
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_sessions") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			return &fakeQueryJSONResponse{JsonData: sqliteSessionExtractWindowRowJSON(88, 2999, 2000, 3000)}, nil
		}
		t.Fatalf("unexpected reconcile query SQL: %q", req.GetSql())
		return nil, nil
	}

	err := store.AdvanceSessionExtractWindow(context.Background(), 88, time.UnixMilli(3000).UTC(), time.UnixMilli(2000).UTC())
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain extract-window error, got %v", err)
	}
	if !strings.Contains(err.Error(), "advance session extract window") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected extract-window commit-unknown error: %v", err)
	}
}

// TestStoreRecordTurnAnalysisFailureProjectsTerminalPass verifies the fifth durable failure is projected as one terminal Pass result.
// TestStoreRecordTurnAnalysisFailureProjectsTerminalPass 用于验证第 5 次持久化失败会投影为终止 Pass 结果。
func TestStoreRecordTurnAnalysisFailureProjectsTerminalPass(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var capturedExecute *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		capturedExecute = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if !strings.Contains(req.GetSql(), "FROM vmm_turn_analysis_failures") {
			t.Fatalf("unexpected turn-analysis failure query: %q", req.GetSql())
		}
		return &fakeQueryJSONResponse{
			JsonData: `[{"turn_id":77,"session_id":88,"stage":"post_action","attempt_count":5,"terminal_status":"passed"}]`,
			RowCount: 1,
		}, nil
	}

	result, err := store.RecordTurnAnalysisFailure(
		context.Background(),
		logicdomain.SessionRef{SessionID: 88},
		77,
		"post_action",
		"slow model timed out",
		5,
		false,
	)
	if err != nil {
		t.Fatalf("RecordTurnAnalysisFailure returned error: %v", err)
	}
	if !result.Passed || result.AttemptCount != 5 || result.Status != logicdomain.TurnAnalysisFailureStatusPassed {
		t.Fatalf("unexpected terminal failure result: %+v", result)
	}
	if capturedExecute == nil {
		t.Fatal("expected failure UPSERT to be captured")
	}
	if !strings.Contains(capturedExecute.GetSql(), "attempt_count = vmm_turn_analysis_failures.attempt_count + 1") ||
		!strings.Contains(capturedExecute.GetSql(), "vmm_turn_analysis_failures.attempt_count + 1 >= ?") {
		t.Fatalf("failure UPSERT does not atomically increment and compare the threshold: %q", capturedExecute.GetSql())
	}
}
