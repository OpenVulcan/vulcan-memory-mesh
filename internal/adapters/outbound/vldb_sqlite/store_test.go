// store_test.go verifies the SQLite local-FFI adapter keeps typed-parameter writes, schema-version behavior,
// and lifecycle-sensitive SQL helpers stable after the legacy gRPC fallback path was removed.
// store_test.go 用于验证在移除旧 gRPC fallback 路径后，
// SQLite 本地 FFI 适配器仍能保持强类型参数写入、schema 版本行为以及生命周期相关 SQL 助手逻辑稳定。
package vldb_sqlite

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
)

// TestStoreLoadUserByIDUsesTypedSQLiteParams verifies single-row lookups still use typed SQLite params instead of params_json.
// TestStoreLoadUserByIDUsesTypedSQLiteParams 用于验证单行查询仍会使用强类型 SQLite 参数，而不是 params_json。
func TestStoreLoadUserByIDUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{
			JsonData: `[{"id":1,"name":"default","profile":"","delete_confirm_code":"","created_at":"2026-04-01T00:00:00Z","updated_at":"2026-04-01T00:00:00Z"}]`,
		}, nil
	}

	record, err := store.loadUserByID(context.Background(), 1)
	if err != nil {
		t.Fatalf("loadUserByID returned error: %v", err)
	}
	if record.ID != 1 {
		t.Fatalf("loadUserByID returned wrong id: %d", record.ID)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	if len(captured.GetParams()) != 1 {
		t.Fatalf("expected one typed param, got %d", len(captured.GetParams()))
	}
	value := captured.GetParams()[0]
	if value.Kind != sqliteffi.SQLValueInt64 || value.Int64 != 1 {
		t.Fatalf("expected int64 sqlite param 1, got %#v", value)
	}
}

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

// TestStoreLoadMemoryNodesByIDsUsesTypedIDParams verifies memory-detail row lookups bind normalized memory ids instead of rendering them into SQL text.
// TestStoreLoadMemoryNodesByIDsUsesTypedIDParams 用于验证 memory 详情行查询会绑定规范化 memory id，而不是把它们渲染进 SQL 文本。
func TestStoreLoadMemoryNodesByIDsUsesTypedIDParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[
{"id":51,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":41,"vector_id":"vec-51","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory a","details":"details a","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20},
{"id":52,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":42,"vector_id":"vec-52","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory b","details":"details b","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":11,"updated_timestamp":21}
]`}, nil
	}

	rows, err := store.LoadMemoryNodesByIDs(context.Background(), []uint64{52, 0, 51, 52})
	if err != nil {
		t.Fatalf("LoadMemoryNodesByIDs returned error: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != 51 || rows[1].ID != 52 {
		t.Fatalf("unexpected loaded memory nodes: %+v", rows)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "WHERE id IN (?,?)") {
		t.Fatalf("expected typed memory placeholders, got %q", sql)
	}
	if strings.Contains(sql, "51") || strings.Contains(sql, "52") {
		t.Fatalf("expected memory ids to stay out of LoadMemoryNodesByIDs SQL, got %q", sql)
	}
	requireSQLiteInt64Params(t, captured.GetParams(), 51, 52)
}

// TestStoreLoadMemoryContextEdgesByMemoryIDsUsesTypedIDParams verifies context-edge lookups bind normalized memory ids instead of rendering them into SQL text.
// TestStoreLoadMemoryContextEdgesByMemoryIDsUsesTypedIDParams 用于验证情境边查询会绑定规范化 memory id，而不是把它们渲染进 SQL 文本。
func TestStoreLoadMemoryContextEdgesByMemoryIDsUsesTypedIDParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[
{"memory_id":51,"context_key":"phase","context_value":"alpha","support_count":1,"rebuttal_count":0,"last_supported_timestamp":10,"last_rebutted_timestamp":0,"created_timestamp":10,"updated_timestamp":20},
{"memory_id":52,"context_key":"phase","context_value":"beta","support_count":2,"rebuttal_count":0,"last_supported_timestamp":11,"last_rebutted_timestamp":0,"created_timestamp":11,"updated_timestamp":21}
]`}, nil
	}

	edges, err := store.LoadMemoryContextEdgesByMemoryIDs(context.Background(), []uint64{52, 0, 51, 52})
	if err != nil {
		t.Fatalf("LoadMemoryContextEdgesByMemoryIDs returned error: %v", err)
	}
	if len(edges) != 2 || edges[0].MemoryID != 51 || edges[1].MemoryID != 52 {
		t.Fatalf("unexpected loaded memory context edges: %+v", edges)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "WHERE memory_id IN (?,?)") {
		t.Fatalf("expected typed memory placeholders, got %q", sql)
	}
	if strings.Contains(sql, "51") || strings.Contains(sql, "52") {
		t.Fatalf("expected memory ids to stay out of LoadMemoryContextEdgesByMemoryIDs SQL, got %q", sql)
	}
	requireSQLiteInt64Params(t, captured.GetParams(), 51, 52)
}

// TestStoreDeleteMemoryNodesUsesTypedIDParams verifies manual delete reads and updates bind normalized memory ids instead of rendering them into SQL text.
// TestStoreDeleteMemoryNodesUsesTypedIDParams 用于验证手工删除的读取与更新都会绑定规范化 memory id，而不是把它们渲染进 SQL 文本。
func TestStoreDeleteMemoryNodesUsesTypedIDParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	queries := make([]*fakeQueryRequest, 0, 1)
	executed := make([]*fakeExecuteRequest, 0, 1)
	deletedFTSIDs := make([]string, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		queries = append(queries, req)
		return &fakeQueryJSONResponse{JsonData: `[
{"id":301,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":41,"vector_id":"vec-301","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory a","details":"details a","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20},
{"id":302,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":42,"vector_id":"vec-302","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory b","details":"details b","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":11,"updated_timestamp":21}
]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
	}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	reason := "  manual delete '; DROP TABLE vmm_memory_nodes;  "
	deletedAt := time.Unix(16000, 0).UTC()
	result, err := store.DeleteMemoryNodes(context.Background(), []uint64{302, 0, 301, 404, 302}, logicdomain.SearchFilter{ProjectID: 9}, deletedAt, reason)
	if err != nil {
		t.Fatalf("DeleteMemoryNodes returned error: %v", err)
	}
	if len(result.DeletedMemoryIDs) != 2 || result.DeletedMemoryIDs[0] != 301 || result.DeletedMemoryIDs[1] != 302 {
		t.Fatalf("unexpected deleted ids: %+v", result.DeletedMemoryIDs)
	}
	if len(result.NotFoundMemoryIDs) != 1 || result.NotFoundMemoryIDs[0] != 404 {
		t.Fatalf("unexpected not-found ids: %+v", result.NotFoundMemoryIDs)
	}
	if len(result.DeletedVectorIDs) != 2 || result.DeletedVectorIDs[0] != "vec-301" || result.DeletedVectorIDs[1] != "vec-302" {
		t.Fatalf("unexpected deleted vector ids: %+v", result.DeletedVectorIDs)
	}
	if len(queries) != 1 || len(executed) != 1 {
		t.Fatalf("expected one query and one update, got queries=%d executed=%d", len(queries), len(executed))
	}
	querySQL := queries[0].GetSql()
	if !strings.Contains(querySQL, "id IN (?,?,?)") {
		t.Fatalf("expected typed candidate memory placeholders, got %q", querySQL)
	}
	for _, leaked := range []string{"301", "302", "404"} {
		if strings.Contains(querySQL, leaked) {
			t.Fatalf("expected memory id %q to stay out of candidate SQL, got %q", leaked, querySQL)
		}
	}
	requireSQLiteInt64Params(t, queries[0].GetParams(), int64(logicdomain.MemoryStatusActive), 301, 302, 404)

	updateSQL := executed[0].GetSql()
	if !strings.Contains(updateSQL, "id IN (?,?)") {
		t.Fatalf("expected typed deleted memory placeholders, got %q", updateSQL)
	}
	for _, leaked := range []string{"301", "302", "404", "DROP TABLE", "manual delete"} {
		if strings.Contains(updateSQL, leaked) {
			t.Fatalf("expected value %q to stay out of update SQL, got %q", leaked, updateSQL)
		}
	}
	params := executed[0].GetParams()
	if len(params) != 6 {
		t.Fatalf("expected six update params, got %d: %#v", len(params), params)
	}
	requireSQLiteInt64Params(t, []sqliteffi.SQLValue{params[0]}, int64(logicdomain.MemoryStatusDeleted))
	if params[1].Kind != sqliteffi.SQLValueString || params[1].String != strings.TrimSpace(reason) {
		t.Fatalf("expected delete reason param, got %#v", params[1])
	}
	requireSQLiteInt64Params(t, params[2:], deletedAt.UnixMilli(), int64(logicdomain.MemoryStatusActive), 301, 302)
	if len(deletedFTSIDs) != 2 || deletedFTSIDs[0] != "301" || deletedFTSIDs[1] != "302" {
		t.Fatalf("unexpected deleted FTS ids: %+v", deletedFTSIDs)
	}
}

// TestStoreDeleteMemoryNodesReconcilesCommitUnknownStatusFlip verifies manual delete recovers when the durable rows prove the uncertain status update committed.
// TestStoreDeleteMemoryNodesReconcilesCommitUnknownStatusFlip 用于验证手工删除状态更新提交未知时，如果长期行证明已提交则恢复成功。
func TestStoreDeleteMemoryNodesReconcilesCommitUnknownStatusFlip(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	reason := "  manual delete  "
	deletedAt := time.Unix(16000, 0).UTC()
	queryCount := 0
	var capturedUpdate *fakeExecuteRequest
	deletedFTSIDs := make([]string, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_memory_nodes") {
			queryCount++
			if queryCount == 1 {
				requireSQLiteInt64Params(t, req.GetParams(), int64(logicdomain.MemoryStatusActive), 301, 302, 404)
				return &fakeQueryJSONResponse{JsonData: sqliteManualDeleteCandidateRowsJSON()}, nil
			}
			if capturedUpdate == nil {
				t.Fatal("expected manual-delete update request before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 301, 302)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeDeleteRowsJSON(logicdomain.MemoryStatusDeleted, reason, capturedUpdate.GetParams()[2].Int64, 301, 302)}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		capturedUpdate = req
		return nil, errors.New("failed to commit manual memory delete")
	}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	result, err := store.DeleteMemoryNodes(context.Background(), []uint64{302, 301, 404}, logicdomain.SearchFilter{ProjectID: 9}, deletedAt, reason)
	if err != nil {
		t.Fatalf("DeleteMemoryNodes returned error: %v", err)
	}
	if len(result.DeletedMemoryIDs) != 2 || result.DeletedMemoryIDs[0] != 301 || result.DeletedMemoryIDs[1] != 302 {
		t.Fatalf("unexpected recovered deleted ids: %+v", result.DeletedMemoryIDs)
	}
	if len(result.NotFoundMemoryIDs) != 1 || result.NotFoundMemoryIDs[0] != 404 {
		t.Fatalf("unexpected recovered not-found ids: %+v", result.NotFoundMemoryIDs)
	}
	if len(result.DeletedVectorIDs) != 2 || result.DeletedVectorIDs[0] != "vec-301" || result.DeletedVectorIDs[1] != "vec-302" {
		t.Fatalf("unexpected recovered vector cleanup coordinates: %+v", result.DeletedVectorIDs)
	}
	if len(deletedFTSIDs) != 2 || deletedFTSIDs[0] != "301" || deletedFTSIDs[1] != "302" {
		t.Fatalf("expected recovered delete to continue FTS cleanup, got %+v", deletedFTSIDs)
	}
	if queryCount != 2 {
		t.Fatalf("expected initial load and reconcile read, got %d memory queries", queryCount)
	}
}

// TestStoreDeleteMemoryNodesKeepsUnmatchedCommitUnknownUncertain verifies unresolved manual-delete ambiguity does not expose vector cleanup coordinates.
// TestStoreDeleteMemoryNodesKeepsUnmatchedCommitUnknownUncertain 用于验证手工删除无法对账时，不会暴露 vector 清理坐标。
func TestStoreDeleteMemoryNodesKeepsUnmatchedCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	reason := "manual delete"
	deletedAt := time.Unix(16000, 0).UTC()
	queryCount := 0
	var capturedUpdate *fakeExecuteRequest
	deletedFTSIDs := make([]string, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_memory_nodes") {
			queryCount++
			if queryCount == 1 {
				requireSQLiteInt64Params(t, req.GetParams(), int64(logicdomain.MemoryStatusActive), 301, 302)
				return &fakeQueryJSONResponse{JsonData: sqliteManualDeleteCandidateRowsJSON()}, nil
			}
			if capturedUpdate == nil {
				t.Fatal("expected manual-delete update request before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 301, 302)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeDeleteRowsJSON(logicdomain.MemoryStatusActive, reason, capturedUpdate.GetParams()[2].Int64, 301, 302)}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		capturedUpdate = req
		return nil, errors.New("failed to commit manual memory delete")
	}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	result, err := store.DeleteMemoryNodes(context.Background(), []uint64{301, 302}, logicdomain.SearchFilter{ProjectID: 9}, deletedAt, reason)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain manual delete error, got %v", err)
	}
	if len(result.DeletedMemoryIDs) != 0 || len(result.NotFoundMemoryIDs) != 0 || len(result.DeletedVectorIDs) != 0 {
		t.Fatalf("expected no cleanup coordinates after unresolved status flip, got %+v", result)
	}
	if !strings.Contains(err.Error(), "delete sqlite memory nodes") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected manual delete commit-unknown error: %v", err)
	}
	if len(deletedFTSIDs) != 0 {
		t.Fatalf("did not expect FTS cleanup after unresolved status flip, got %+v", deletedFTSIDs)
	}
	if queryCount != 2 {
		t.Fatalf("expected initial load and reconcile read, got %d memory queries", queryCount)
	}
}

// TestStoreDeleteMemoryNodesReturnsCleanupCoordinatesWhenFTSSyncFails verifies confirmed manual deletes still expose vector cleanup coordinates when the post-delete FTS sync boundary fails.
// TestStoreDeleteMemoryNodesReturnsCleanupCoordinatesWhenFTSSyncFails 用于验证手工删除已确认后，即使删除后的 FTS 同步边界失败，也会继续暴露向量清理坐标。
func TestStoreDeleteMemoryNodesReturnsCleanupCoordinatesWhenFTSSyncFails(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	deletedFTSIDs := make([]string, 0, 1)
	rebuildCalls := 0
	fake.queryJSONFunc = func(_ context.Context, _ *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		return &fakeQueryJSONResponse{JsonData: `[
{"id":301,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":41,"vector_id":"vec-301","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory a","details":"details a","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20},
{"id":302,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":42,"vector_id":"vec-302","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory b","details":"details b","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":11,"updated_timestamp":21}
]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
	}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{}, fmt.Errorf("fts delete unavailable")
	}
	fake.rebuildFtsIndexFunc = func(_ string, mode sqliteffi.TokenizerMode) (sqliteffi.RebuildFtsIndexResult, error) {
		rebuildCalls++
		return sqliteffi.RebuildFtsIndexResult{Success: true, TokenizerMode: mode}, nil
	}

	result, err := store.DeleteMemoryNodes(context.Background(), []uint64{301, 302, 404}, logicdomain.SearchFilter{ProjectID: 9}, time.Unix(16000, 0).UTC(), "manual delete")
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected FTS sync failure to be outcome-uncertain, got %v", err)
	}
	if len(result.DeletedMemoryIDs) != 2 || result.DeletedMemoryIDs[0] != 301 || result.DeletedMemoryIDs[1] != 302 {
		t.Fatalf("unexpected deleted ids after FTS failure: %+v", result.DeletedMemoryIDs)
	}
	if len(result.NotFoundMemoryIDs) != 1 || result.NotFoundMemoryIDs[0] != 404 {
		t.Fatalf("unexpected not-found ids after FTS failure: %+v", result.NotFoundMemoryIDs)
	}
	if len(result.DeletedVectorIDs) != 2 || result.DeletedVectorIDs[0] != "vec-301" || result.DeletedVectorIDs[1] != "vec-302" {
		t.Fatalf("unexpected vector cleanup coordinates after FTS failure: %+v", result.DeletedVectorIDs)
	}
	if len(deletedFTSIDs) != 1 || deletedFTSIDs[0] != "301" {
		t.Fatalf("unexpected attempted FTS delete ids: %+v", deletedFTSIDs)
	}
	if rebuildCalls != 1 {
		t.Fatalf("expected one FTS rebuild attempt after incremental delete failure, got %d", rebuildCalls)
	}
}

// TestStoreEnsureBuiltinMemoryFTSRejectsUnsuccessfulEnsureResult verifies FFI result bits are consumed before tokenizer metadata is trusted.
// TestStoreEnsureBuiltinMemoryFTSRejectsUnsuccessfulEnsureResult 用于验证在信任分词器元数据前，会消费 FFI 返回的结果位。
func TestStoreEnsureBuiltinMemoryFTSRejectsUnsuccessfulEnsureResult(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second, tokenizerMode: sqliteffi.TokenizerJieba, ftsIndexName: memoryFTSIndexName}

	fake.ensureFtsIndexFunc = func(_ string, mode sqliteffi.TokenizerMode) (sqliteffi.EnsureFtsIndexResult, error) {
		return sqliteffi.EnsureFtsIndexResult{Success: false, TokenizerMode: mode}, nil
	}

	err := store.ensureBuiltinMemoryFTS(context.Background())
	if err == nil {
		t.Fatalf("expected unsuccessful FTS ensure result to fail")
	}
	if !strings.Contains(err.Error(), "ensure sqlite builtin fts index") || !strings.Contains(err.Error(), "unsuccessful") {
		t.Fatalf("unexpected ensure error: %v", err)
	}
}

// TestStoreSyncMemoryFTSAfterWriteTreatsUnsuccessfulUpsertAsFailure verifies successful FFI status with a failed mutation result still triggers rebuild fallback.
// TestStoreSyncMemoryFTSAfterWriteTreatsUnsuccessfulUpsertAsFailure 用于验证 FFI 状态成功但变更结果位失败时，仍会触发重建兜底。
func TestStoreSyncMemoryFTSAfterWriteTreatsUnsuccessfulUpsertAsFailure(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second, tokenizerMode: sqliteffi.TokenizerJieba, ftsIndexName: memoryFTSIndexName}

	rebuildCalls := 0
	fake.upsertFtsDocumentFunc = func(_ string, _ sqliteffi.TokenizerMode, _ string, _ string, _ string, _ string) (sqliteffi.FtsMutationResult, error) {
		return sqliteffi.FtsMutationResult{Success: false, AffectedRows: 0}, nil
	}
	fake.rebuildFtsIndexFunc = func(_ string, mode sqliteffi.TokenizerMode) (sqliteffi.RebuildFtsIndexResult, error) {
		rebuildCalls++
		return sqliteffi.RebuildFtsIndexResult{Success: true, TokenizerMode: mode}, nil
	}

	err := store.syncMemoryFTSAfterWrite(context.Background(), []logicdomain.MemoryNodeRecord{{
		ID:       55,
		VectorID: "vec-55",
		Abstract: "memory abstract",
		Details:  "memory details",
	}}, nil)
	if err == nil {
		t.Fatalf("expected unsuccessful FTS upsert result to fail")
	}
	if rebuildCalls != 1 {
		t.Fatalf("expected one FTS rebuild attempt after unsuccessful upsert, got %d", rebuildCalls)
	}
	if !strings.Contains(err.Error(), "upsert sqlite memory fts document 55") || !strings.Contains(err.Error(), "unsuccessful") {
		t.Fatalf("unexpected upsert error: %v", err)
	}
}

// TestStoreSyncMemoryFTSAfterWriteTreatsUnsuccessfulDeleteAsFailure verifies delete result bits are handled the same way as upsert result bits.
// TestStoreSyncMemoryFTSAfterWriteTreatsUnsuccessfulDeleteAsFailure 用于验证删除结果位会按照与写入结果位相同的方式处理。
func TestStoreSyncMemoryFTSAfterWriteTreatsUnsuccessfulDeleteAsFailure(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second, tokenizerMode: sqliteffi.TokenizerJieba, ftsIndexName: memoryFTSIndexName}

	rebuildCalls := 0
	fake.deleteFtsDocumentFunc = func(_ string, _ string) (sqliteffi.FtsMutationResult, error) {
		return sqliteffi.FtsMutationResult{Success: false, AffectedRows: 0}, nil
	}
	fake.rebuildFtsIndexFunc = func(_ string, mode sqliteffi.TokenizerMode) (sqliteffi.RebuildFtsIndexResult, error) {
		rebuildCalls++
		return sqliteffi.RebuildFtsIndexResult{Success: true, TokenizerMode: mode}, nil
	}

	err := store.syncMemoryFTSAfterWrite(context.Background(), nil, []uint64{77})
	if err == nil {
		t.Fatalf("expected unsuccessful FTS delete result to fail")
	}
	if rebuildCalls != 1 {
		t.Fatalf("expected one FTS rebuild attempt after unsuccessful delete, got %d", rebuildCalls)
	}
	if !strings.Contains(err.Error(), "delete sqlite memory fts document 77") || !strings.Contains(err.Error(), "unsuccessful") {
		t.Fatalf("unexpected delete error: %v", err)
	}
}

// TestStoreRebuildMemoryFTSWithFallbackRejectsUnsuccessfulRebuildResult verifies fallback rebuild result bits are part of the recovery contract.
// TestStoreRebuildMemoryFTSWithFallbackRejectsUnsuccessfulRebuildResult 用于验证兜底重建的结果位属于恢复契约的一部分。
func TestStoreRebuildMemoryFTSWithFallbackRejectsUnsuccessfulRebuildResult(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second, tokenizerMode: sqliteffi.TokenizerJieba, ftsIndexName: memoryFTSIndexName}

	fake.rebuildFtsIndexFunc = func(_ string, mode sqliteffi.TokenizerMode) (sqliteffi.RebuildFtsIndexResult, error) {
		return sqliteffi.RebuildFtsIndexResult{Success: false, TokenizerMode: mode}, nil
	}

	err := store.rebuildMemoryFTSWithFallback(context.Background(), errors.New("incremental fts failed"))
	if err == nil {
		t.Fatalf("expected unsuccessful FTS rebuild result to fail")
	}
	if !strings.Contains(err.Error(), "incremental fts failed") ||
		!strings.Contains(err.Error(), "rebuild sqlite builtin fts index also failed") ||
		!strings.Contains(err.Error(), "unsuccessful") {
		t.Fatalf("unexpected rebuild fallback error: %v", err)
	}
}

// TestStoreDeleteMemoryNodesRejectsPartialStatusFlipBeforeVectorCleanup verifies stale delete drift cannot return active vector ids for cleanup.
// TestStoreDeleteMemoryNodesRejectsPartialStatusFlipBeforeVectorCleanup 用于验证删除状态切换发生漂移时，不会把仍可能 active 的 vector id 返回给清理链路。
func TestStoreDeleteMemoryNodesRejectsPartialStatusFlipBeforeVectorCleanup(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 1)
	deletedFTSIDs := make([]string, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, _ *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		return &fakeQueryJSONResponse{JsonData: `[
{"id":301,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":41,"vector_id":"vec-301","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory a","details":"details a","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20},
{"id":302,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":42,"vector_id":"vec-302","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory b","details":"details b","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":11,"updated_timestamp":21}
]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	_, err := store.DeleteMemoryNodes(context.Background(), []uint64{301, 302}, logicdomain.SearchFilter{ProjectID: 9}, time.Unix(16000, 0).UTC(), "manual delete")
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected partial delete status flip to be outcome-uncertain, got %v", err)
	}
	if len(executed) != 1 {
		t.Fatalf("expected one status update before failure, got %d", len(executed))
	}
	if len(deletedFTSIDs) != 0 {
		t.Fatalf("did not expect FTS cleanup after partial status flip, got %+v", deletedFTSIDs)
	}
}

// TestStoreLoadActiveMemoryVectorIDsUsesTypedIDParams verifies supersede cleanup coordinates bind normalized memory ids instead of rendering them into SQL text.
// TestStoreLoadActiveMemoryVectorIDsUsesTypedIDParams 用于验证 supersede 清理坐标查询会绑定规范化 memory id，而不是把它们渲染进 SQL 文本。
func TestStoreLoadActiveMemoryVectorIDsUsesTypedIDParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[
{"vector_id":"vec-81"},
{"vector_id":" "},
{"vector_id":"vec-82"}
]`}, nil
	}

	vectorIDs, err := store.loadActiveMemoryVectorIDs(context.Background(), []uint64{82, 0, 81, 82})
	if err != nil {
		t.Fatalf("loadActiveMemoryVectorIDs returned error: %v", err)
	}
	if len(vectorIDs) != 2 || vectorIDs[0] != "vec-81" || vectorIDs[1] != "vec-82" {
		t.Fatalf("unexpected active vector ids: %+v", vectorIDs)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "memory_status = ?") || !strings.Contains(sql, "id IN (?,?)") {
		t.Fatalf("expected typed active memory vector placeholders, got %q", sql)
	}
	for _, leaked := range []string{"81", "82"} {
		if strings.Contains(sql, leaked) {
			t.Fatalf("expected memory id %q to stay out of active vector SQL, got %q", leaked, sql)
		}
	}
	requireSQLiteInt64Params(t, captured.GetParams(), int64(logicdomain.MemoryStatusActive), 81, 82)
}

// TestStoreLoadProfileNodeStatusesByIDsUsesTypedIDParams verifies profile-node reconciliation reads bind normalized node ids.
// TestStoreLoadProfileNodeStatusesByIDsUsesTypedIDParams 用于验证画像节点对账读取会绑定规范化 node id。
func TestStoreLoadProfileNodeStatusesByIDsUsesTypedIDParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[
{"id":7,"profile_status":%d,"superseded_by_id":101},
{"id":9,"profile_status":%d,"superseded_by_id":101}
]`, logicdomain.ProfileStatusSuperseded, logicdomain.ProfileStatusSuperseded)}, nil
	}

	rows, err := store.loadProfileNodeStatusesByIDs(context.Background(), []uint64{9, 0, 7, 9})
	if err != nil {
		t.Fatalf("loadProfileNodeStatusesByIDs returned error: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != 7 || rows[1].ID != 9 {
		t.Fatalf("unexpected profile node statuses: %+v", rows)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "WHERE id IN (?,?)") {
		t.Fatalf("expected typed profile node placeholders, got %q", sql)
	}
	if strings.Contains(sql, "7") || strings.Contains(sql, "9") {
		t.Fatalf("expected profile node ids to stay out of status SQL, got %q", sql)
	}
	requireSQLiteInt64Params(t, captured.GetParams(), 7, 9)
}

// TestStoreLoadProfileInstructionByIDUsesTypedIDParam verifies manual-instruction reconciliation reads bind the instruction id.
// TestStoreLoadProfileInstructionByIDUsesTypedIDParam 用于验证手工指令对账读取会绑定 instruction id。
func TestStoreLoadProfileInstructionByIDUsesTypedIDParam(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{
			"id":55,
			"profile_type":%d,
			"bind_id":9,
			"instruction":"keep deployment notes current",
			"instruction_status":%d,
			"review_result_json":"",
			"failure_reason":"",
			"created_timestamp":10,
			"updated_timestamp":20
		}]`, logicdomain.ProfileTypeProject, logicdomain.ProfileInstructionStatusPending)}, nil
	}

	record, found, err := store.loadProfileInstructionByID(context.Background(), 55)
	if err != nil {
		t.Fatalf("loadProfileInstructionByID returned error: %v", err)
	}
	if !found || record.ID != 55 || record.BindID != 9 {
		t.Fatalf("unexpected loaded profile instruction: found=%v record=%+v", found, record)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "WHERE id = ?") {
		t.Fatalf("expected typed profile instruction id placeholder, got %q", sql)
	}
	if strings.Contains(sql, "55") {
		t.Fatalf("expected profile instruction id to stay out of SQL text, got %q", sql)
	}
	requireSQLiteInt64Params(t, captured.GetParams(), 55)
}

// TestStoreLoadProfileNodeByIDUsesTypedIDParam verifies manual-node reconciliation reads bind the profile node id.
// TestStoreLoadProfileNodeByIDUsesTypedIDParam 用于验证手工节点对账读取会绑定 profile node id。
func TestStoreLoadProfileNodeByIDUsesTypedIDParam(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{
			"id":101,
			"turn_id":null,
			"profile_type":%d,
			"bind_id":9,
			"content":"manual profile node",
			"profile_status":%d,
			"priority":%d,
			"profile_level":%d,
			"level_reason":"stable reason",
			"refresh_weight":32,
			"source_kind":%d,
			"source_id":55,
			"status_reason":"",
			"expires_timestamp":0,
			"superseded_by_id":0,
			"profile_date":"2026-07-05",
			"created_timestamp":10,
			"updated_timestamp":20,
			"profile_date_anchor_timestamp":10
		}]`,
			logicdomain.ProfileTypeProject,
			logicdomain.ProfileStatusActive,
			logicdomain.ProfilePriorityP1,
			logicdomain.ProfileLevelStable,
			logicdomain.ProfileSourceKindManualInstruction,
		)}, nil
	}

	record, found, err := store.loadProfileNodeByID(context.Background(), 101)
	if err != nil {
		t.Fatalf("loadProfileNodeByID returned error: %v", err)
	}
	if !found || record.ID != 101 || record.BindID != 9 {
		t.Fatalf("unexpected loaded profile node: found=%v record=%+v", found, record)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "WHERE n.id = ?") {
		t.Fatalf("expected typed profile node id placeholder, got %q", sql)
	}
	if strings.Contains(sql, "101") {
		t.Fatalf("expected profile node id to stay out of SQL text, got %q", sql)
	}
	requireSQLiteInt64Params(t, captured.GetParams(), 101)
}

// projectDeleteQueryResponder returns a QueryJSON fake for project resolution, delete planning counts, and profile-count rows used by DeleteProjectPath tests.
// projectDeleteQueryResponder 用于返回 DeleteProjectPath 测试所需的项目解析、删除规划计数和画像计数 QueryJSON 替身。
func projectDeleteQueryResponder(t *testing.T, sessions, messages, memories, remainingProjects, remainingSpaces, profiles int) func(context.Context, *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
	t.Helper()
	return func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE t.name = ?"):
			params := req.GetParams()
			if len(params) != 3 || params[0].Kind != sqliteffi.SQLValueString || params[0].String != "TeamA" ||
				params[1].Kind != sqliteffi.SQLValueString || params[1].String != "SpaceA" ||
				params[2].Kind != sqliteffi.SQLValueString || params[2].String != "ProjectA" {
				t.Fatalf("expected typed project path params, got %#v", params)
			}
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":101,
				"team_id":301,
				"space_id":401,
				"name":"ProjectA",
				"profile":"",
				"created_at":"2026-07-05T00:00:00Z",
				"updated_at":"2026-07-05T00:00:00Z",
				"team_name":"TeamA",
				"space_name":"SpaceA"
			}]`}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_sessions WHERE project_id = ?"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, sessions)}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_turn_records WHERE project_id = ?"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, messages)}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_memory_nodes WHERE project_id = ?"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, memories)}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_projects WHERE space_id = ? AND id <> ?"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, remainingProjects)}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_spaces WHERE team_id = ? AND id <> ?"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, remainingSpaces)}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_profile_nodes WHERE"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, profiles)}, nil
		default:
			t.Fatalf("unexpected project delete query: %q", sql)
			return nil, nil
		}
	}
}

// TestStoreDeleteProjectPathChecksPlannedRowsChanged verifies every destructive project-delete statement must match the row counts computed by the delete plan.
// TestStoreDeleteProjectPathChecksPlannedRowsChanged 用于验证项目删除的每条破坏性语句都必须匹配删除计划计算出的行数。
func TestStoreDeleteProjectPathChecksPlannedRowsChanged(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 7)
	fake.queryJSONFunc = projectDeleteQueryResponder(t, 2, 3, 4, 0, 0, 5)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "DELETE FROM vmm_profile_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 5}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 4}, nil
		case strings.Contains(sql, "DELETE FROM vmm_turn_records"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 3}, nil
		case strings.Contains(sql, "DELETE FROM vmm_sessions"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "DELETE FROM vmm_projects"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "DELETE FROM vmm_spaces"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "DELETE FROM vmm_teams"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		default:
			t.Fatalf("unexpected project delete statement: %q", sql)
			return nil, nil
		}
	}

	result, err := store.DeleteProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", true)
	if err != nil {
		t.Fatalf("DeleteProjectPath returned error: %v", err)
	}
	if result.DeletedProjects != 1 || result.DeletedSpaces != 1 || result.DeletedTeams != 1 ||
		result.DeletedSessions != 2 || result.DeletedMessages != 3 || result.DeletedMemories != 4 || result.DeletedProfiles != 5 {
		t.Fatalf("unexpected project delete result: %+v", result)
	}
	if len(executed) != 7 {
		t.Fatalf("expected seven checked delete statements, got %d", len(executed))
	}
}

// TestStoreDeleteProjectPathRejectsRowsChangedDrift verifies row-count drift stops the project delete before later relational rows are removed.
// TestStoreDeleteProjectPathRejectsRowsChangedDrift 用于验证行数漂移会中止项目删除，避免继续删除后续关系行。
func TestStoreDeleteProjectPathRejectsRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 2)
	fake.queryJSONFunc = projectDeleteQueryResponder(t, 2, 3, 4, 0, 0, 5)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "DELETE FROM vmm_profile_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 5}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 3}, nil
		default:
			t.Fatalf("unexpected project delete statement after row-count drift: %q", sql)
			return nil, nil
		}
	}

	_, err := store.DeleteProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", true)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected project delete drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "delete project memory nodes affected 3 rows, want 4") {
		t.Fatalf("expected memory row-count drift detail, got %v", err)
	}
	if len(executed) != 2 {
		t.Fatalf("expected delete to stop after memory row drift, got %d statements", len(executed))
	}
}

// TestStoreDeleteProjectPathMarksInitialCommitUnknownUncertain verifies the first destructive delete step preserves SQLite commit-boundary ambiguity.
// TestStoreDeleteProjectPathMarksInitialCommitUnknownUncertain 用于验证首个破坏性删除步骤会保留 SQLite 提交边界不确定语义。
func TestStoreDeleteProjectPathMarksInitialCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 1)
	fake.queryJSONFunc = projectDeleteQueryResponder(t, 2, 3, 4, 0, 0, 5)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		if !strings.Contains(req.GetSql(), "DELETE FROM vmm_profile_nodes") {
			t.Fatalf("expected first project delete step to delete profiles, got %q", req.GetSql())
		}
		return nil, errors.New("failed to commit project profile delete")
	}

	_, err := store.DeleteProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", true)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected initial project delete commit error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "delete project profile nodes") || !strings.Contains(err.Error(), "failed to commit project profile delete") {
		t.Fatalf("unexpected initial project delete commit error: %v", err)
	}
	if len(executed) != 1 {
		t.Fatalf("expected delete to stop after first commit-unknown step, got %d statements", len(executed))
	}
}

// TestStoreDeleteProjectPathMarksLaterErrorAfterMutationUncertain verifies ordinary later failures are unsafe after earlier destructive deletes committed.
// TestStoreDeleteProjectPathMarksLaterErrorAfterMutationUncertain 用于验证前序破坏性删除已提交后，后续普通失败也不能被当作干净失败。
func TestStoreDeleteProjectPathMarksLaterErrorAfterMutationUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 2)
	fake.queryJSONFunc = projectDeleteQueryResponder(t, 2, 3, 4, 0, 0, 5)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "DELETE FROM vmm_profile_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 5}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_nodes"):
			return nil, errors.New("project memory delete unavailable")
		default:
			t.Fatalf("unexpected project delete statement after prior mutation: %q", sql)
			return nil, nil
		}
	}

	_, err := store.DeleteProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", true)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected later project delete error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "delete project memory nodes") || !strings.Contains(err.Error(), "project memory delete unavailable") {
		t.Fatalf("unexpected later project delete error: %v", err)
	}
	if len(executed) != 2 {
		t.Fatalf("expected delete to stop after second statement failed, got %d statements", len(executed))
	}
}

// TestStoreMigrateProjectPathUsesTypedSQLiteParams verifies project migration writes bind hierarchy ids instead of rendering them into SQL text.
// TestStoreMigrateProjectPathUsesTypedSQLiteParams 用于验证项目迁移写入会绑定层级 id，而不是把它们渲染进 SQL 文本。
func TestStoreMigrateProjectPathUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 4)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "UPDATE vmm_sessions"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "UPDATE vmm_turn_records"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 5}, nil
		case strings.Contains(sql, "UPDATE vmm_memory_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 3}, nil
		case strings.Contains(sql, "UPDATE vmm_profile_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		default:
			t.Fatalf("unexpected project migration statement: %q", sql)
			return nil, nil
		}
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE t.name = ?"):
			params := req.GetParams()
			if len(params) != 3 || params[2].Kind != sqliteffi.SQLValueString {
				t.Fatalf("expected project path params, got %#v", params)
			}
			switch params[2].String {
			case "ProjectA":
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":101,
					"team_id":301,
					"space_id":401,
					"name":"ProjectA",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamA",
					"space_name":"SpaceA"
				}]`}, nil
			case "ProjectB":
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":202,
					"team_id":302,
					"space_id":402,
					"name":"ProjectB",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamB",
					"space_name":"SpaceB"
				}]`}, nil
			default:
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.id = ?"):
			params := req.GetParams()
			if len(params) != 1 || params[0].Kind != sqliteffi.SQLValueInt64 {
				t.Fatalf("expected project id param, got %#v", params)
			}
			switch params[0].Int64 {
			case 101:
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":101,
					"team_id":301,
					"space_id":401,
					"name":"ProjectA",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamA",
					"space_name":"SpaceA"
				}]`}, nil
			case 202:
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":202,
					"team_id":302,
					"space_id":402,
					"name":"ProjectB",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamB",
					"space_name":"SpaceB"
				}]`}, nil
			default:
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_turn_records"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":5}]`}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":3}]`}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_profile_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":1}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	result, err := store.MigrateProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", "TeamB/SpaceB/ProjectB", true)
	if err != nil {
		t.Fatalf("MigrateProjectPath returned error: %v", err)
	}
	if result.MigratedSessions != 2 || result.MigratedMessages != 5 || result.MigratedMemories != 3 {
		t.Fatalf("unexpected migration counts: %+v", result)
	}
	if len(executed) != 4 {
		t.Fatalf("expected four migration updates, got %d", len(executed))
	}
	for _, req := range executed {
		for _, leaked := range []string{"101", "202", "301", "302", "401", "402"} {
			if strings.Contains(req.GetSql(), leaked) {
				t.Fatalf("expected migration id %q to stay out of SQL text, got %q", leaked, req.GetSql())
			}
		}
	}

	sessionParams := executed[0].GetParams()
	if len(sessionParams) != 5 {
		t.Fatalf("expected five session migration params, got %d: %#v", len(sessionParams), sessionParams)
	}
	requireSQLiteInt64Params(t, sessionParams[:3], 302, 402, 202)
	if sessionParams[3].Kind != sqliteffi.SQLValueInt64 || sessionParams[3].Int64 <= 0 {
		t.Fatalf("expected session migration timestamp param, got %#v", sessionParams[3])
	}
	requireSQLiteInt64Params(t, sessionParams[4:], 101)

	turnParams := executed[1].GetParams()
	if len(turnParams) != 3 {
		t.Fatalf("expected three turn migration params, got %d: %#v", len(turnParams), turnParams)
	}
	requireSQLiteInt64Params(t, []sqliteffi.SQLValue{turnParams[0]}, 202)
	if turnParams[1].Kind != sqliteffi.SQLValueInt64 || turnParams[1].Int64 <= 0 {
		t.Fatalf("expected turn migration timestamp param, got %#v", turnParams[1])
	}
	requireSQLiteInt64Params(t, turnParams[2:], 101)

	memoryParams := executed[2].GetParams()
	if len(memoryParams) != 5 {
		t.Fatalf("expected five memory migration params, got %d: %#v", len(memoryParams), memoryParams)
	}
	requireSQLiteInt64Params(t, memoryParams[:3], 302, 402, 202)
	if memoryParams[3].Kind != sqliteffi.SQLValueInt64 || memoryParams[3].Int64 <= 0 {
		t.Fatalf("expected memory migration timestamp param, got %#v", memoryParams[3])
	}
	requireSQLiteInt64Params(t, memoryParams[4:], 101)

	profileParams := executed[3].GetParams()
	if len(profileParams) != 3 {
		t.Fatalf("expected three profile migration params, got %d: %#v", len(profileParams), profileParams)
	}
	requireSQLiteInt64Params(t, profileParams, 202, int64(logicdomain.ProfileTypeProject), 101)
}

// TestStoreMigrateProjectPathRejectsRowsChangedDrift verifies migration stops before later writes when SQLite reports fewer moved rows than the migration plan counted.
// TestStoreMigrateProjectPathRejectsRowsChangedDrift 用于验证当 SQLite 报告的迁移行数少于迁移计划时，迁移会在后续写入前停止。
func TestStoreMigrateProjectPathRejectsRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 3)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "UPDATE vmm_sessions"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "UPDATE vmm_turn_records"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 5}, nil
		case strings.Contains(sql, "UPDATE vmm_memory_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		default:
			t.Fatalf("unexpected project migration statement after row-count drift: %q", sql)
			return nil, nil
		}
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE t.name = ?"):
			params := req.GetParams()
			if len(params) != 3 || params[2].Kind != sqliteffi.SQLValueString {
				t.Fatalf("expected project path params, got %#v", params)
			}
			switch params[2].String {
			case "ProjectA":
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":101,
					"team_id":301,
					"space_id":401,
					"name":"ProjectA",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamA",
					"space_name":"SpaceA"
				}]`}, nil
			case "ProjectB":
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":202,
					"team_id":302,
					"space_id":402,
					"name":"ProjectB",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamB",
					"space_name":"SpaceB"
				}]`}, nil
			default:
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.id = ?"):
			params := req.GetParams()
			if len(params) != 1 || params[0].Kind != sqliteffi.SQLValueInt64 {
				t.Fatalf("expected project id param, got %#v", params)
			}
			switch params[0].Int64 {
			case 101:
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":101,
					"team_id":301,
					"space_id":401,
					"name":"ProjectA",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamA",
					"space_name":"SpaceA"
				}]`}, nil
			case 202:
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":202,
					"team_id":302,
					"space_id":402,
					"name":"ProjectB",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamB",
					"space_name":"SpaceB"
				}]`}, nil
			default:
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_turn_records"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":5}]`}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":3}]`}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_profile_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":1}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.MigrateProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", "TeamB/SpaceB/ProjectB", true)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected project migration drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "migrate project memory nodes affected 2 rows, want 3") {
		t.Fatalf("expected memory migration row-count drift detail, got %v", err)
	}
	if len(executed) != 3 {
		t.Fatalf("expected migration to stop before profile update, got %d statements", len(executed))
	}
}

// TestStoreMigrateProjectPathRejectsStaleEndpointAfterWriteLock verifies stale source resolution is rechecked under the write lock before migration updates run.
// TestStoreMigrateProjectPathRejectsStaleEndpointAfterWriteLock 用于验证陈旧源项目解析会在写锁下重新校验，并在迁移更新执行前被拒绝。
func TestStoreMigrateProjectPathRejectsStaleEndpointAfterWriteLock(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		t.Fatalf("expected stale migration endpoint to stop before writes, got %q", req.GetSql())
		return nil, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE t.name = ?"):
			params := req.GetParams()
			if len(params) != 3 || params[2].Kind != sqliteffi.SQLValueString {
				t.Fatalf("expected project path params, got %#v", params)
			}
			switch params[2].String {
			case "ProjectA":
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":101,
					"team_id":301,
					"space_id":401,
					"name":"ProjectA",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamA",
					"space_name":"SpaceA"
				}]`}, nil
			case "ProjectB":
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":202,
					"team_id":302,
					"space_id":402,
					"name":"ProjectB",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamB",
					"space_name":"SpaceB"
				}]`}, nil
			default:
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.id = ?"):
			params := req.GetParams()
			if len(params) != 1 || params[0].Kind != sqliteffi.SQLValueInt64 {
				t.Fatalf("expected project id param, got %#v", params)
			}
			if params[0].Int64 != 101 {
				t.Fatalf("expected source project to be reloaded first, got %#v", params)
			}
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":101,
				"team_id":301,
				"space_id":401,
				"name":"ProjectARecreated",
				"profile":"",
				"created_at":"2026-07-05T00:00:00Z",
				"updated_at":"2026-07-05T00:00:00Z",
				"team_name":"TeamA",
				"space_name":"SpaceA"
			}]`}, nil
		default:
			t.Fatalf("unexpected stale endpoint query: %q", sql)
			return nil, nil
		}
	}

	_, err := store.MigrateProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", "TeamB/SpaceB/ProjectB", true)
	if err == nil || !logicdomain.IsConflictError(err) {
		t.Fatalf("expected stale migration endpoint to be a conflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "source project changed before migration") {
		t.Fatalf("expected stale source detail, got %v", err)
	}
	if len(executed) != 0 {
		t.Fatalf("expected stale endpoint to stop before writes, got %d statements", len(executed))
	}
}

// TestStoreEnsureProjectPathRechecksHierarchyUnderWriteLock verifies a project created after the first lookup is returned as existing instead of inserting duplicates.
// TestStoreEnsureProjectPathRechecksHierarchyUnderWriteLock 用于验证第一次查询后被创建的项目会在写锁内作为已存在项目返回，而不是继续插入重复层级。
func TestStoreEnsureProjectPathRechecksHierarchyUnderWriteLock(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	teamLookups := 0
	spaceLookups := 0
	projectLookups := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE t.name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_teams") && strings.Contains(sql, "WHERE name = ?"):
			teamLookups++
			return &fakeQueryJSONResponse{JsonData: `[{"id":301,"name":"TeamA","profile":"","created_at":"2026-07-06T00:00:00Z","updated_at":"2026-07-06T00:00:00Z"}]`}, nil
		case strings.Contains(sql, "FROM vmm_spaces") && strings.Contains(sql, "WHERE team_id = ? AND name = ?"):
			spaceLookups++
			return &fakeQueryJSONResponse{JsonData: `[{"id":401,"team_id":301,"name":"SpaceA","profile":"","created_at":"2026-07-06T00:00:00Z","updated_at":"2026-07-06T00:00:00Z"}]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.space_id = ? AND p.name = ?"):
			projectLookups++
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":501,
				"team_id":301,
				"space_id":401,
				"name":"ProjectA",
				"profile":"",
				"created_at":"2026-07-06T00:00:00Z",
				"updated_at":"2026-07-06T00:00:00Z",
				"team_name":"TeamA",
				"space_name":"SpaceA"
			}]`}, nil
		default:
			t.Fatalf("unexpected ensure project query: %q", sql)
			return nil, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		t.Fatalf("expected locked project recheck to stop before inserts, got %q", req.GetSql())
		return nil, nil
	}

	result, err := store.EnsureProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", true)
	if err != nil {
		t.Fatalf("EnsureProjectPath returned error: %v", err)
	}
	if !result.Exists || result.Project.ID != 501 || result.CreatedTeam || result.CreatedSpace || result.CreatedProject {
		t.Fatalf("expected existing project result after locked recheck, got %+v", result)
	}
	if teamLookups != 2 || spaceLookups != 2 || projectLookups != 1 {
		t.Fatalf("unexpected locked lookup counts: teams=%d spaces=%d projects=%d", teamLookups, spaceLookups, projectLookups)
	}
}

// TestStoreEnsureProjectPathRejectsInsertRowsChangedDrift verifies hierarchy creation stops with an uncertain result when the final project insert does not report one inserted row.
// TestStoreEnsureProjectPathRejectsInsertRowsChangedDrift 用于验证当最终 project 插入没有报告一条写入时，层级创建会以结果不确定状态停止。
func TestStoreEnsureProjectPathRejectsInsertRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 3)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_teams"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_spaces"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":401}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_projects"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":501}]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE t.name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_teams") && strings.Contains(sql, "WHERE name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_spaces") && strings.Contains(sql, "WHERE team_id = ? AND name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.space_id = ? AND p.name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			t.Fatalf("unexpected ensure project drift query: %q", sql)
			return nil, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "INSERT INTO vmm_teams"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "INSERT INTO vmm_spaces"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "INSERT INTO vmm_projects"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		default:
			t.Fatalf("unexpected ensure project drift write: %q", sql)
			return nil, nil
		}
	}

	_, err := store.EnsureProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", true)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected project insert drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "insert project") || !strings.Contains(err.Error(), "affected 0 rows, want 1") {
		t.Fatalf("expected project insert row-count detail, got %v", err)
	}
	if len(executed) != 3 {
		t.Fatalf("expected team, space, and project insert attempts, got %d", len(executed))
	}
}

// TestStoreEnsureProjectPathReconcilesParentCommitUnknown verifies ambiguous Team/Space inserts are recovered by durable unique-key readback before project creation continues.
// TestStoreEnsureProjectPathReconcilesParentCommitUnknown 用于验证 Team/Space 插入提交结果不明时，会通过长期唯一键回读恢复并继续创建 project。
func TestStoreEnsureProjectPathReconcilesParentCommitUnknown(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	teamLookups := 0
	spaceLookups := 0
	projectLookups := 0
	executed := make([]*fakeExecuteRequest, 0, 3)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_teams"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_spaces"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":401}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_projects"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":501}]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE t.name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_teams") && strings.Contains(sql, "WHERE name = ?"):
			teamLookups++
			if teamLookups < 3 {
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
			return &fakeQueryJSONResponse{JsonData: `[{"id":301,"name":"TeamA","profile":"","created_at":"2026-07-06T00:00:00Z","updated_at":"2026-07-06T00:00:00Z"}]`}, nil
		case strings.Contains(sql, "FROM vmm_spaces") && strings.Contains(sql, "WHERE team_id = ? AND name = ?"):
			spaceLookups++
			if spaceLookups < 2 {
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
			return &fakeQueryJSONResponse{JsonData: `[{"id":401,"team_id":301,"name":"SpaceA","profile":"","created_at":"2026-07-06T00:00:00Z","updated_at":"2026-07-06T00:00:00Z"}]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.space_id = ? AND p.name = ?"):
			projectLookups++
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			t.Fatalf("unexpected ensure project parent reconcile query: %q", sql)
			return nil, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "INSERT INTO vmm_teams"):
			return nil, errors.New("failed to commit team insert")
		case strings.Contains(sql, "INSERT INTO vmm_spaces"):
			return nil, errors.New("failed to commit space insert")
		case strings.Contains(sql, "INSERT INTO vmm_projects"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		default:
			t.Fatalf("unexpected ensure project parent reconcile write: %q", sql)
			return nil, nil
		}
	}

	result, err := store.EnsureProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", true)
	if err != nil {
		t.Fatalf("EnsureProjectPath should recover parent commit-unknown inserts: %v", err)
	}
	if result.Project.ID != 501 || !result.CreatedTeam || !result.CreatedSpace || !result.CreatedProject {
		t.Fatalf("expected recovered parent hierarchy and created project, got %+v", result)
	}
	if teamLookups != 3 || spaceLookups != 2 || projectLookups != 1 {
		t.Fatalf("unexpected reconcile lookup counts: teams=%d spaces=%d projects=%d", teamLookups, spaceLookups, projectLookups)
	}
	if len(executed) != 3 {
		t.Fatalf("expected three hierarchy insert attempts, got %d", len(executed))
	}
}

// TestStoreEnsureProjectPathReconcilesProjectCommitUnknown verifies the final project insert can recover success by reading back the scoped project row.
// TestStoreEnsureProjectPathReconcilesProjectCommitUnknown 用于验证最终 project 插入提交结果不明时，可以通过范围内 project 行回读恢复成功。
func TestStoreEnsureProjectPathReconcilesProjectCommitUnknown(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	scopedProjectLookups := 0
	executed := make([]*fakeExecuteRequest, 0, 3)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_teams"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_spaces"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":401}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_projects"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":501}]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE t.name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_teams") && strings.Contains(sql, "WHERE name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_spaces") && strings.Contains(sql, "WHERE team_id = ? AND name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.space_id = ? AND p.name = ?"):
			scopedProjectLookups++
			if scopedProjectLookups == 1 {
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":501,
				"team_id":301,
				"space_id":401,
				"name":"ProjectA",
				"profile":"",
				"created_at":"2026-07-06T00:00:00Z",
				"updated_at":"2026-07-06T00:00:00Z",
				"team_name":"TeamA",
				"space_name":"SpaceA"
			}]`}, nil
		default:
			t.Fatalf("unexpected ensure project reconcile query: %q", sql)
			return nil, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "INSERT INTO vmm_teams"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "INSERT INTO vmm_spaces"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "INSERT INTO vmm_projects"):
			return nil, errors.New("failed to commit project insert")
		default:
			t.Fatalf("unexpected ensure project reconcile write: %q", sql)
			return nil, nil
		}
	}

	result, err := store.EnsureProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", true)
	if err != nil {
		t.Fatalf("EnsureProjectPath should recover project commit-unknown insert: %v", err)
	}
	if result.Project.ID != 501 || !result.CreatedTeam || !result.CreatedSpace || !result.CreatedProject {
		t.Fatalf("expected recovered project creation result, got %+v", result)
	}
	if scopedProjectLookups != 2 {
		t.Fatalf("expected pre-insert project lookup plus reconcile readback, got %d", scopedProjectLookups)
	}
	if len(executed) != 3 {
		t.Fatalf("expected three hierarchy insert attempts, got %d", len(executed))
	}
}

// TestStoreEnsureUserNameRechecksUnderWriteLock verifies a user created after the first lookup is returned as existing before a new id is allocated.
// TestStoreEnsureUserNameRechecksUnderWriteLock 用于验证第一次查询后被创建的用户会在分配新 ID 前作为已存在用户返回。
func TestStoreEnsureUserNameRechecksUnderWriteLock(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	userLookups := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		if !strings.Contains(sql, "FROM vmm_users") || !strings.Contains(sql, "WHERE name = ?") {
			t.Fatalf("unexpected ensure user query: %q", sql)
		}
		userLookups++
		if userLookups == 1 {
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[{"id":7,"name":"alice","profile":"","delete_confirm_code":"","created_at":"2026-07-06T00:00:00Z","updated_at":"2026-07-06T00:00:00Z"}]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		t.Fatalf("expected locked user recheck to stop before insert, got %q", req.GetSql())
		return nil, nil
	}

	result, err := store.EnsureUserName(context.Background(), "alice", true)
	if err != nil {
		t.Fatalf("EnsureUserName returned error: %v", err)
	}
	if !result.Exists || result.User.ID != 7 || result.Created {
		t.Fatalf("expected existing user result after locked recheck, got %+v", result)
	}
	if userLookups != 2 {
		t.Fatalf("expected pre-lock and locked user lookups, got %d", userLookups)
	}
}

// TestStoreEnsureUserNameReconcilesCommitUnknown verifies an ambiguous user insert is recovered by reading back the unique user name.
// TestStoreEnsureUserNameReconcilesCommitUnknown 用于验证提交结果不明的用户插入会通过唯一用户名回读恢复。
func TestStoreEnsureUserNameReconcilesCommitUnknown(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	userLookups := 0
	executed := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_users"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":7}]`}, nil
		case strings.Contains(sql, "FROM vmm_users") && strings.Contains(sql, "WHERE name = ?"):
			userLookups++
			if userLookups <= 2 {
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
			params := req.GetParams()
			if len(params) != 1 || params[0].String != "alice" {
				t.Fatalf("unexpected user reconcile params: %#v", params)
			}
			return &fakeQueryJSONResponse{JsonData: `[{"id":7,"name":"alice","profile":"","delete_confirm_code":"","created_at":"2026-07-06T00:00:00Z","updated_at":"2026-07-06T00:00:00Z"}]`}, nil
		default:
			t.Fatalf("unexpected ensure user reconcile query: %q", sql)
			return nil, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed++
		if !strings.Contains(req.GetSql(), "INSERT INTO vmm_users") {
			t.Fatalf("unexpected ensure user reconcile write: %q", req.GetSql())
		}
		return nil, errors.New("failed to commit user insert")
	}

	result, err := store.EnsureUserName(context.Background(), "alice", true)
	if err != nil {
		t.Fatalf("EnsureUserName should recover user commit-unknown insert: %v", err)
	}
	if !result.Created || result.Exists || result.User.ID != 7 || result.User.Name != "alice" {
		t.Fatalf("expected recovered user creation result, got %+v", result)
	}
	if userLookups != 3 {
		t.Fatalf("expected pre-lock, locked, and reconcile user lookups, got %d", userLookups)
	}
	if executed != 1 {
		t.Fatalf("expected one user insert attempt, got %d", executed)
	}
}

// TestStoreEnsureUserNameRejectsInsertRowsChangedDrift verifies user creation rejects a reported no-op insert as an ordinary pre-mutation failure.
// TestStoreEnsureUserNameRejectsInsertRowsChangedDrift 用于验证用户创建会把报告为 no-op 的插入拒绝为写入前普通失败。
func TestStoreEnsureUserNameRejectsInsertRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_users"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":8}]`}, nil
		case strings.Contains(sql, "FROM vmm_users") && strings.Contains(sql, "WHERE name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			t.Fatalf("unexpected ensure user drift query: %q", sql)
			return nil, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		if !strings.Contains(req.GetSql(), "INSERT INTO vmm_users") {
			t.Fatalf("unexpected ensure user drift write: %q", req.GetSql())
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}

	_, err := store.EnsureUserName(context.Background(), "alice", true)
	if err == nil {
		t.Fatal("expected user insert drift error")
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected user insert drift to stay ordinary before any mutation, got %v", err)
	}
	if !strings.Contains(err.Error(), "insert user") || !strings.Contains(err.Error(), "affected 0 rows, want 1") {
		t.Fatalf("expected user insert row-count detail, got %v", err)
	}
}

// TestStoreEnsureSessionRechecksUnderWriteLock verifies a session created after the first lookup is reused before a new id is allocated.
// TestStoreEnsureSessionRechecksUnderWriteLock 用于验证第一次查询后被创建的 session 会在分配新 ID 前被复用。
func TestStoreEnsureSessionRechecksUnderWriteLock(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	sessionLookups := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		if !strings.Contains(sql, "FROM vmm_sessions") || !strings.Contains(sql, "WHERE project_id = ? AND session_key = ?") {
			t.Fatalf("unexpected ensure session query: %q", sql)
		}
		sessionLookups++
		if sessionLookups == 1 {
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
		params := req.GetParams()
		if len(params) != 2 || params[0].Int64 != 9 || params[1].String != "sess-1" {
			t.Fatalf("unexpected locked session lookup params: %#v", params)
		}
		return &fakeQueryJSONResponse{JsonData: `[{
			"id":91,
			"session_key":"sess-1",
			"user_id":7,
			"team_id":2,
			"space_id":3,
			"project_id":9,
			"turn_count":4,
			"last_summarized_id":0,
			"last_compacted_turn_id":0,
			"summarize_content":"",
			"summarize_budget":0,
			"last_extract_observed_timestamp":0,
			"last_extract_completed_timestamp":0,
			"last_compacted_timestamp":0,
			"created_timestamp":1783270000000,
			"updated_timestamp":1783270000001
		}]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		t.Fatalf("expected locked session recheck to stop before insert, got %q", req.GetSql())
		return nil, nil
	}

	createdAt := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	session, err := store.ensureSession(
		context.Background(),
		"sess-1",
		logicdomain.UserRecord{ID: 7, Name: "alice", CreatedAt: createdAt},
		logicdomain.ProjectRecord{ID: 9, TeamID: 2, SpaceID: 3, TeamName: "TeamA", SpaceName: "SpaceA", Name: "ProjectA", CreatedAt: createdAt},
	)
	if err != nil {
		t.Fatalf("ensureSession returned error: %v", err)
	}
	if session.ID != 91 || session.TurnCount != 4 {
		t.Fatalf("expected locked existing session, got %+v", session)
	}
	if sessionLookups != 2 {
		t.Fatalf("expected pre-lock and locked session lookups, got %d", sessionLookups)
	}
}

// TestStoreEnsureSessionReconcilesCommitUnknown verifies an ambiguous session insert is recovered by the project-scoped session key.
// TestStoreEnsureSessionReconcilesCommitUnknown 用于验证提交结果不明的 session 插入会通过项目作用域内的 session key 回读恢复。
func TestStoreEnsureSessionReconcilesCommitUnknown(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	sessionLookups := 0
	executed := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_sessions") && strings.Contains(sql, "WHERE project_id = ? AND session_key = ?"):
			sessionLookups++
			params := req.GetParams()
			if len(params) != 2 || params[0].Int64 != 9 || params[1].String != "sess-1" {
				t.Fatalf("unexpected session reconcile params: %#v", params)
			}
			if sessionLookups <= 2 {
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":91,
				"session_key":"sess-1",
				"user_id":7,
				"team_id":2,
				"space_id":3,
				"project_id":9,
				"turn_count":0,
				"last_summarized_id":0,
				"last_compacted_turn_id":0,
				"summarize_content":"",
				"summarize_budget":0,
				"last_extract_observed_timestamp":0,
				"last_extract_completed_timestamp":0,
				"last_compacted_timestamp":0,
				"created_timestamp":1783270000000,
				"updated_timestamp":1783270000000
			}]`}, nil
		case strings.Contains(sql, "FROM vmm_users") && strings.Contains(sql, "WHERE id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[{"id":7,"name":"alice","profile":"","delete_confirm_code":"","created_at":"2026-07-06T00:00:00Z","updated_at":"2026-07-06T00:00:00Z"}]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":9,
				"team_id":2,
				"space_id":3,
				"name":"ProjectA",
				"profile":"",
				"created_at":"2026-07-06T00:00:00Z",
				"updated_at":"2026-07-06T00:00:00Z",
				"team_name":"TeamA",
				"space_name":"SpaceA"
			}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":91}]`}, nil
		default:
			t.Fatalf("unexpected ensure session reconcile query: %q", sql)
			return nil, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed++
		if !strings.Contains(req.GetSql(), "INSERT INTO vmm_sessions") {
			t.Fatalf("unexpected ensure session reconcile write: %q", req.GetSql())
		}
		return nil, errors.New("failed to commit session insert")
	}

	createdAt := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	session, err := store.ensureSession(
		context.Background(),
		"sess-1",
		logicdomain.UserRecord{ID: 7, Name: "alice", CreatedAt: createdAt},
		logicdomain.ProjectRecord{ID: 9, TeamID: 2, SpaceID: 3, TeamName: "TeamA", SpaceName: "SpaceA", Name: "ProjectA", CreatedAt: createdAt},
	)
	if err != nil {
		t.Fatalf("ensureSession should recover session commit-unknown insert: %v", err)
	}
	if session.ID != 91 || session.SessionKey != "sess-1" || session.ProjectID != 9 {
		t.Fatalf("expected recovered session, got %+v", session)
	}
	if sessionLookups != 3 {
		t.Fatalf("expected pre-lock, locked, and reconcile session lookups, got %d", sessionLookups)
	}
	if executed != 1 {
		t.Fatalf("expected one session insert attempt, got %d", executed)
	}
}

// TestStoreEnsureSessionRejectsInsertRowsChangedDrift verifies session creation rejects a reported no-op insert as an ordinary pre-mutation failure.
// TestStoreEnsureSessionRejectsInsertRowsChangedDrift 用于验证 session 创建会把报告为 no-op 的插入拒绝为写入前普通失败。
func TestStoreEnsureSessionRejectsInsertRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 1)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_sessions") && strings.Contains(sql, "WHERE project_id = ? AND session_key = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_users") && strings.Contains(sql, "WHERE id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[{"id":7,"name":"alice","profile":"","delete_confirm_code":"","created_at":"2026-07-06T00:00:00Z","updated_at":"2026-07-06T00:00:00Z"}]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":9,
				"team_id":2,
				"space_id":3,
				"name":"ProjectA",
				"profile":"",
				"created_at":"2026-07-06T00:00:00Z",
				"updated_at":"2026-07-06T00:00:00Z",
				"team_name":"TeamA",
				"space_name":"SpaceA"
			}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":91}]`}, nil
		default:
			t.Fatalf("unexpected ensure session drift query: %q", sql)
			return nil, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		if !strings.Contains(req.GetSql(), "INSERT INTO vmm_sessions") {
			t.Fatalf("unexpected ensure session drift write: %q", req.GetSql())
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}

	createdAt := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	_, err := store.ensureSession(
		context.Background(),
		"sess-1",
		logicdomain.UserRecord{ID: 7, Name: "alice", CreatedAt: createdAt},
		logicdomain.ProjectRecord{ID: 9, TeamID: 2, SpaceID: 3, TeamName: "TeamA", SpaceName: "SpaceA", Name: "ProjectA", CreatedAt: createdAt},
	)
	if err == nil {
		t.Fatal("expected session insert drift error")
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected session insert drift to stay ordinary before any mutation, got %v", err)
	}
	if !strings.Contains(err.Error(), "insert session") || !strings.Contains(err.Error(), "affected 0 rows, want 1") {
		t.Fatalf("expected session insert row-count detail, got %v", err)
	}
	if len(executed) != 1 {
		t.Fatalf("expected one session insert attempt, got %d", len(executed))
	}
}

// TestStoreEnsureSessionRejectsStaleUserBeforeInsert verifies user ids are revalidated under the write lock before a missing session is inserted.
// TestStoreEnsureSessionRejectsStaleUserBeforeInsert 用于验证缺失 session 插入前会在写锁内重新校验 user ID 没有被删除后复用。
func TestStoreEnsureSessionRejectsStaleUserBeforeInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_sessions") && strings.Contains(sql, "WHERE project_id = ? AND session_key = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_users") && strings.Contains(sql, "WHERE id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[{"id":7,"name":"bob","profile":"","delete_confirm_code":"","created_at":"2026-07-06T00:00:00Z","updated_at":"2026-07-06T00:00:00Z"}]`}, nil
		default:
			t.Fatalf("unexpected stale user session query: %q", sql)
			return nil, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		t.Fatalf("expected stale user to stop before insert, got %q", req.GetSql())
		return nil, nil
	}

	createdAt := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	_, err := store.ensureSession(
		context.Background(),
		"sess-1",
		logicdomain.UserRecord{ID: 7, Name: "alice", CreatedAt: createdAt},
		logicdomain.ProjectRecord{ID: 9, TeamID: 2, SpaceID: 3, TeamName: "TeamA", SpaceName: "SpaceA", Name: "ProjectA", CreatedAt: createdAt},
	)
	if err == nil || !logicdomain.IsConflictError(err) {
		t.Fatalf("expected stale user to be a conflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "user changed before session create") {
		t.Fatalf("expected stale user detail, got %v", err)
	}
}

// TestStoreEnsureSessionRejectsStaleProjectBeforeInsert verifies project ids are revalidated under the write lock before a missing session is inserted.
// TestStoreEnsureSessionRejectsStaleProjectBeforeInsert 用于验证缺失 session 插入前会在写锁内重新校验 project ID 没有被删除后复用。
func TestStoreEnsureSessionRejectsStaleProjectBeforeInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_sessions") && strings.Contains(sql, "WHERE project_id = ? AND session_key = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_users") && strings.Contains(sql, "WHERE id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[{"id":7,"name":"alice","profile":"","delete_confirm_code":"","created_at":"2026-07-06T00:00:00Z","updated_at":"2026-07-06T00:00:00Z"}]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":9,
				"team_id":2,
				"space_id":3,
				"name":"ProjectB",
				"profile":"",
				"created_at":"2026-07-06T00:00:00Z",
				"updated_at":"2026-07-06T00:00:00Z",
				"team_name":"TeamA",
				"space_name":"SpaceA"
			}]`}, nil
		default:
			t.Fatalf("unexpected stale project session query: %q", sql)
			return nil, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		t.Fatalf("expected stale project to stop before insert, got %q", req.GetSql())
		return nil, nil
	}

	createdAt := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	_, err := store.ensureSession(
		context.Background(),
		"sess-1",
		logicdomain.UserRecord{ID: 7, Name: "alice", CreatedAt: createdAt},
		logicdomain.ProjectRecord{ID: 9, TeamID: 2, SpaceID: 3, TeamName: "TeamA", SpaceName: "SpaceA", Name: "ProjectA", CreatedAt: createdAt},
	)
	if err == nil || !logicdomain.IsConflictError(err) {
		t.Fatalf("expected stale project to be a conflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "project changed before session create") {
		t.Fatalf("expected stale project detail, got %v", err)
	}
}

// TestStoreEnsureUserDeleteConfirmationUsesTypedSQLiteParams verifies generated confirmation codes stay out of the guarded UPDATE SQL text.
// TestStoreEnsureUserDeleteConfirmationUsesTypedSQLiteParams 用于验证生成的删除确认码不会进入受保护 UPDATE 的 SQL 文本。
func TestStoreEnsureUserDeleteConfirmationUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	// Return a user without an existing confirmation code so the first delete step must generate and persist one.
	// 返回一条尚无确认码的用户记录，让删除第一阶段必须生成并持久化确认码。
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_users") {
			return &fakeQueryJSONResponse{
				JsonData: `[{"id":7,"name":"alice","profile":"","delete_confirm_code":"","created_at":"2026-04-01T00:00:00Z","updated_at":"2026-04-01T00:00:00Z"}]`,
			}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	result, err := store.ensureUserDeleteConfirmation(context.Background(), logicdomain.UserRecord{ID: 7})
	if err != nil {
		t.Fatalf("ensureUserDeleteConfirmation returned error: %v", err)
	}
	if !result.RequiresConfirmation || len(result.ConfirmationCode) != 32 {
		t.Fatalf("unexpected confirmation result: %+v", result)
	}
	if strings.Trim(result.ConfirmationCode, "0123456789abcdef") != "" {
		t.Fatalf("confirmation code should be lowercase hex, got %q", result.ConfirmationCode)
	}
	if result.User.DeleteConfirmCode != result.ConfirmationCode {
		t.Fatalf("result user did not receive generated confirmation code: %+v", result.User)
	}
	if captured == nil {
		t.Fatal("expected ExecuteScript request to be captured")
	}
	if strings.Contains(captured.GetSql(), result.ConfirmationCode) {
		t.Fatalf("confirmation code leaked into SQL text: %q", captured.GetSql())
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	params := captured.GetParams()
	if len(params) != 3 {
		t.Fatalf("expected three confirmation update params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueString || params[0].String != result.ConfirmationCode {
		t.Fatalf("expected confirmation code string param, got %#v", params[0])
	}
	if params[1].Kind != sqliteffi.SQLValueString {
		t.Fatalf("expected updated_at string param, got %#v", params[1])
	}
	if _, err := time.Parse(time.RFC3339Nano, params[1].String); err != nil {
		t.Fatalf("updated_at param is not RFC3339Nano: %q", params[1].String)
	}
	if params[2].Kind != sqliteffi.SQLValueInt64 || params[2].Int64 != 7 {
		t.Fatalf("expected user id int64 param, got %#v", params[2])
	}
}

// TestStoreEnsureUserDeleteConfirmationReusesDurableCodeAfterGuardedNoop verifies concurrent first-step delete requests return the code that actually reached storage.
// TestStoreEnsureUserDeleteConfirmationReusesDurableCodeAfterGuardedNoop 用于验证并发删除第一阶段请求会返回实际进入长期存储的确认码。
func TestStoreEnsureUserDeleteConfirmationReusesDurableCodeAfterGuardedNoop(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	queryCalls := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_users") {
			queryCalls++
			if queryCalls == 1 {
				return &fakeQueryJSONResponse{
					JsonData: `[{"id":7,"name":"alice","profile":"","delete_confirm_code":"","created_at":"2026-04-01T00:00:00Z","updated_at":"2026-04-01T00:00:00Z"}]`,
				}, nil
			}
			return &fakeQueryJSONResponse{
				JsonData: `[{"id":7,"name":"alice","profile":"","delete_confirm_code":"persisted-code","created_at":"2026-04-01T00:00:00Z","updated_at":"2026-04-01T00:00:01Z"}]`,
			}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}

	result, err := store.ensureUserDeleteConfirmation(context.Background(), logicdomain.UserRecord{ID: 7})
	if err != nil {
		t.Fatalf("ensureUserDeleteConfirmation returned error: %v", err)
	}
	if result.ConfirmationCode != "persisted-code" || result.User.DeleteConfirmCode != "persisted-code" {
		t.Fatalf("expected durable confirmation code after guarded no-op, got %+v", result)
	}
	if queryCalls != 2 {
		t.Fatalf("expected confirmation flow to reload user after guarded no-op, got %d queries", queryCalls)
	}
}

// TestStoreEnsureUserDeleteConfirmationReconcilesCommitUnknownWithDurableCode verifies an ambiguous confirmation write returns the durable code after readback.
// TestStoreEnsureUserDeleteConfirmationReconcilesCommitUnknownWithDurableCode 用于验证确认码写入结果不明时，会在回读到长期确认码后正常返回该 code。
func TestStoreEnsureUserDeleteConfirmationReconcilesCommitUnknownWithDurableCode(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	queryCalls := 0
	generatedCode := ""
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if !strings.Contains(req.GetSql(), "FROM vmm_users") {
			t.Fatalf("unexpected confirmation reconcile query: %q", req.GetSql())
		}
		queryCalls++
		if queryCalls == 1 {
			return &fakeQueryJSONResponse{
				JsonData: `[{"id":7,"name":"alice","profile":"","delete_confirm_code":"","created_at":"2026-04-01T00:00:00Z","updated_at":"2026-04-01T00:00:00Z"}]`,
			}, nil
		}
		return &fakeQueryJSONResponse{
			JsonData: fmt.Sprintf(`[{"id":7,"name":"alice","profile":"","delete_confirm_code":%q,"created_at":"2026-04-01T00:00:00Z","updated_at":"2026-04-01T00:00:01Z"}]`, generatedCode),
		}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		if !strings.Contains(req.GetSql(), "UPDATE vmm_users") || !strings.Contains(req.GetSql(), "delete_confirm_code = ?") {
			t.Fatalf("unexpected confirmation update SQL: %q", req.GetSql())
		}
		params := req.GetParams()
		if len(params) != 3 || params[0].Kind != sqliteffi.SQLValueString {
			t.Fatalf("expected generated confirmation code param, got %#v", params)
		}
		generatedCode = params[0].String
		return nil, errors.New("failed to commit user delete confirmation code")
	}

	result, err := store.ensureUserDeleteConfirmation(context.Background(), logicdomain.UserRecord{ID: 7})
	if err != nil {
		t.Fatalf("ensureUserDeleteConfirmation should reconcile durable code: %v", err)
	}
	if generatedCode == "" || result.ConfirmationCode != generatedCode || result.User.DeleteConfirmCode != generatedCode {
		t.Fatalf("expected reconciled generated code %q, got %+v", generatedCode, result)
	}
	if queryCalls != 2 {
		t.Fatalf("expected initial load and commit-unknown reconcile readback, got %d queries", queryCalls)
	}
}

// TestStoreEnsureUserDeleteConfirmationKeepsOrdinaryWriteErrorOrdinary verifies non-commit-boundary confirmation failures are not upgraded or reconciled speculatively.
// TestStoreEnsureUserDeleteConfirmationKeepsOrdinaryWriteErrorOrdinary 用于验证非提交边界类确认码写入失败不会被升级，也不会被投机性对账。
func TestStoreEnsureUserDeleteConfirmationKeepsOrdinaryWriteErrorOrdinary(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	queryCalls := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if !strings.Contains(req.GetSql(), "FROM vmm_users") {
			t.Fatalf("unexpected confirmation ordinary-error query: %q", req.GetSql())
		}
		queryCalls++
		return &fakeQueryJSONResponse{
			JsonData: `[{"id":7,"name":"alice","profile":"","delete_confirm_code":"","created_at":"2026-04-01T00:00:00Z","updated_at":"2026-04-01T00:00:00Z"}]`,
		}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		if !strings.Contains(req.GetSql(), "UPDATE vmm_users") {
			t.Fatalf("unexpected confirmation update SQL: %q", req.GetSql())
		}
		return nil, errors.New("sqlite transport unavailable")
	}

	_, err := store.ensureUserDeleteConfirmation(context.Background(), logicdomain.UserRecord{ID: 7})
	if err == nil {
		t.Fatal("expected ordinary confirmation write error")
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected ordinary confirmation write error to stay ordinary, got %v", err)
	}
	if !strings.Contains(err.Error(), "persist user delete confirmation code") || !strings.Contains(err.Error(), "sqlite transport unavailable") {
		t.Fatalf("expected ordinary confirmation error detail, got %v", err)
	}
	if queryCalls != 1 {
		t.Fatalf("expected no speculative reconcile read for ordinary write error, got %d queries", queryCalls)
	}
}

// TestStoreEnsureUserDeleteConfirmationMarksUnreconciledCommitUnknownUncertain verifies an ambiguous confirmation write stays outcome-uncertain when readback cannot prove a durable code.
// TestStoreEnsureUserDeleteConfirmationMarksUnreconciledCommitUnknownUncertain 用于验证确认码写入结果不明且回读无法证明长期 code 时，会保留结果不确定错误。
func TestStoreEnsureUserDeleteConfirmationMarksUnreconciledCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	queryCalls := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if !strings.Contains(req.GetSql(), "FROM vmm_users") {
			t.Fatalf("unexpected confirmation unresolved query: %q", req.GetSql())
		}
		queryCalls++
		return &fakeQueryJSONResponse{
			JsonData: `[{"id":7,"name":"alice","profile":"","delete_confirm_code":"","created_at":"2026-04-01T00:00:00Z","updated_at":"2026-04-01T00:00:00Z"}]`,
		}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		if !strings.Contains(req.GetSql(), "UPDATE vmm_users") {
			t.Fatalf("unexpected confirmation update SQL: %q", req.GetSql())
		}
		return nil, errors.New("failed to commit user delete confirmation code")
	}

	_, err := store.ensureUserDeleteConfirmation(context.Background(), logicdomain.UserRecord{ID: 7})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected unreconciled commit-unknown confirmation write to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "persist user delete confirmation code") || !strings.Contains(err.Error(), "failed to commit user delete confirmation code") {
		t.Fatalf("expected confirmation commit detail, got %v", err)
	}
	if queryCalls != 2 {
		t.Fatalf("expected initial load and failed reconcile readback, got %d queries", queryCalls)
	}
}

// userDeleteQueryResponder returns a QueryJSON fake for user resolution, latest-code reloads, delete planning counts, and shared-profile detach counts.
// userDeleteQueryResponder 用于返回用户解析、最新确认码重载、删除规划计数和共享画像脱钩计数所需的 QueryJSON 替身。
func userDeleteQueryResponder(t *testing.T, sessions, messages, memories, userProfiles, sharedProfiles int) func(context.Context, *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
	t.Helper()
	return func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_users") && strings.Contains(sql, "WHERE name = ?"):
			params := req.GetParams()
			if len(params) != 1 || params[0].Kind != sqliteffi.SQLValueString || params[0].String != "alice" {
				t.Fatalf("expected typed user name param, got %#v", params)
			}
			return &fakeQueryJSONResponse{JsonData: `[{"id":7,"name":"alice","profile":"","delete_confirm_code":"confirm-me","created_at":"2026-04-01T00:00:00Z","updated_at":"2026-04-01T00:00:00Z"}]`}, nil
		case strings.Contains(sql, "FROM vmm_users") && strings.Contains(sql, "WHERE id = ?"):
			params := req.GetParams()
			if len(params) != 1 || params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 != 7 {
				t.Fatalf("expected typed user id param, got %#v", params)
			}
			return &fakeQueryJSONResponse{JsonData: `[{"id":7,"name":"alice","profile":"","delete_confirm_code":"confirm-me","created_at":"2026-04-01T00:00:00Z","updated_at":"2026-04-01T00:00:00Z"}]`}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_sessions WHERE user_id = ?"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, sessions)}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_turn_records WHERE session_id IN"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, messages)}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_memory_nodes WHERE user_id = ?"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, memories)}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_profile_nodes WHERE profile_type = ? AND bind_id = ?"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, userProfiles)}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_profile_nodes WHERE profile_type <> ?"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, sharedProfiles)}, nil
		default:
			t.Fatalf("unexpected user delete query: %q", sql)
			return nil, nil
		}
	}
}

// TestStoreDeleteUserRefChecksPlannedRowsChanged verifies user deletion cannot report success unless each destructive or detach write matches the precomputed plan.
// TestStoreDeleteUserRefChecksPlannedRowsChanged 用于验证用户删除必须让每条破坏性或脱钩写入都匹配预计算计划后才允许报告成功。
func TestStoreDeleteUserRefChecksPlannedRowsChanged(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 6)
	fake.queryJSONFunc = userDeleteQueryResponder(t, 2, 3, 4, 5, 6)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "DELETE FROM vmm_profile_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 5}, nil
		case strings.Contains(sql, "UPDATE vmm_profile_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 6}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 4}, nil
		case strings.Contains(sql, "DELETE FROM vmm_turn_records"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 3}, nil
		case strings.Contains(sql, "DELETE FROM vmm_sessions"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "DELETE FROM vmm_users"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		default:
			t.Fatalf("unexpected user delete statement: %q", sql)
			return nil, nil
		}
	}

	result, err := store.DeleteUserRef(context.Background(), "alice", "confirm-me")
	if err != nil {
		t.Fatalf("DeleteUserRef returned error: %v", err)
	}
	if result.DeletedUsers != 1 || result.DeletedSessions != 2 || result.DeletedMessages != 3 ||
		result.DeletedMemories != 4 || result.DeletedProfiles != 5 {
		t.Fatalf("unexpected user delete result: %+v", result)
	}
	if len(executed) != 6 {
		t.Fatalf("expected six checked user delete writes, got %d", len(executed))
	}
}

// TestStoreDeleteUserRefRejectsSharedProfileDetachDrift verifies shared-scope profile detach drift stops the delete before user-owned rows are removed.
// TestStoreDeleteUserRefRejectsSharedProfileDetachDrift 用于验证共享范围画像脱钩漂移会中止删除，避免继续删除用户拥有的后续行。
func TestStoreDeleteUserRefRejectsSharedProfileDetachDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 2)
	fake.queryJSONFunc = userDeleteQueryResponder(t, 2, 3, 4, 5, 6)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "DELETE FROM vmm_profile_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 5}, nil
		case strings.Contains(sql, "UPDATE vmm_profile_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 5}, nil
		default:
			t.Fatalf("unexpected user delete statement after detach row-count drift: %q", sql)
			return nil, nil
		}
	}

	_, err := store.DeleteUserRef(context.Background(), "alice", "confirm-me")
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected shared profile detach drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "detach surviving shared profile nodes from deleted user turns affected 5 rows, want 6") {
		t.Fatalf("expected detach row-count drift detail, got %v", err)
	}
	if len(executed) != 2 {
		t.Fatalf("expected delete to stop after shared profile detach drift, got %d statements", len(executed))
	}
}

// TestStoreDeleteUserRefMarksInitialCommitUnknownUncertain verifies the first destructive user-delete write preserves SQLite commit-boundary ambiguity.
// TestStoreDeleteUserRefMarksInitialCommitUnknownUncertain 用于验证用户删除的首个破坏性写入会保留 SQLite 提交边界不确定语义。
func TestStoreDeleteUserRefMarksInitialCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 1)
	fake.queryJSONFunc = userDeleteQueryResponder(t, 2, 3, 4, 5, 6)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		if !strings.Contains(req.GetSql(), "DELETE FROM vmm_profile_nodes") {
			t.Fatalf("expected first user delete write to remove user profile nodes, got %q", req.GetSql())
		}
		return nil, errors.New("failed to commit user profile delete")
	}

	_, err := store.DeleteUserRef(context.Background(), "alice", "confirm-me")
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected first user delete commit failure to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "delete user profile nodes by bind") || !strings.Contains(err.Error(), "failed to commit user profile delete") {
		t.Fatalf("expected delete profile commit detail, got %v", err)
	}
	if len(executed) != 1 {
		t.Fatalf("expected delete to stop after first commit-unknown write, got %d statements", len(executed))
	}
}

// TestStoreDeleteUserRefMarksLaterErrorAfterMutationUncertain verifies ordinary write failures after confirmed user-delete progress are outcome-uncertain.
// TestStoreDeleteUserRefMarksLaterErrorAfterMutationUncertain 用于验证用户删除已有确认写入后遇到普通写入失败时会被标记为结果不确定。
func TestStoreDeleteUserRefMarksLaterErrorAfterMutationUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 2)
	fake.queryJSONFunc = userDeleteQueryResponder(t, 2, 3, 4, 5, 6)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "DELETE FROM vmm_profile_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 5}, nil
		case strings.Contains(sql, "UPDATE vmm_profile_nodes"):
			return nil, errors.New("shared profile detach unavailable")
		default:
			t.Fatalf("unexpected user delete statement after post-mutation failure: %q", sql)
			return nil, nil
		}
	}

	_, err := store.DeleteUserRef(context.Background(), "alice", "confirm-me")
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected post-mutation user delete failure to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "detach surviving shared profile nodes from deleted user turns") || !strings.Contains(err.Error(), "shared profile detach unavailable") {
		t.Fatalf("expected shared profile detach failure detail, got %v", err)
	}
	if len(executed) != 2 {
		t.Fatalf("expected delete to stop after second write failure, got %d statements", len(executed))
	}
}

// TestStoreLoadRenderedProfileUsesTypedSQLiteParams verifies rendered-profile lookups keep the target id in typed params instead of raw SQL interpolation.
// TestStoreLoadRenderedProfileUsesTypedSQLiteParams 用于验证渲染画像读取会把目标 id 保持在强类型参数中，而不是直接插进 SQL 文本。
func TestStoreLoadRenderedProfileUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[{"profile":"project profile"}]`}, nil
	}

	profile, err := store.LoadRenderedProfile(context.Background(), logicdomain.ProfileTargetRef{
		ProfileType: logicdomain.ProfileTypeProject,
		BindID:      9,
	})
	if err != nil {
		t.Fatalf("LoadRenderedProfile returned error: %v", err)
	}
	if profile != "project profile" {
		t.Fatalf("unexpected rendered profile: %q", profile)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	if !strings.Contains(captured.GetSql(), "WHERE id = ? LIMIT 1") {
		t.Fatalf("expected placeholder-based profile lookup sql, got %q", captured.GetSql())
	}
	value := captured.GetParams()[0]
	if value.Kind != sqliteffi.SQLValueInt64 || value.Int64 != 9 {
		t.Fatalf("expected int64 sqlite param 9, got %#v", value)
	}
}

// TestStoreListActiveProfileNodesOmitsLimitWhenCallerRequestsFullSnapshot verifies active profile-node queries do not append a hidden LIMIT when the caller explicitly requests an unbounded snapshot.
// TestStoreListActiveProfileNodesOmitsLimitWhenCallerRequestsFullSnapshot 用于验证当调用方显式请求不受限快照时，active 画像节点查询不会再偷偷追加隐藏 LIMIT。
func TestStoreListActiveProfileNodesOmitsLimitWhenCallerRequestsFullSnapshot(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	nodes, err := store.ListActiveProfileNodes(context.Background(), logicdomain.ProfileTargetRef{
		ProfileType: logicdomain.ProfileTypeProject,
		BindID:      9,
	}, 0)
	if err != nil {
		t.Fatalf("ListActiveProfileNodes returned error: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected empty nodes slice, got %+v", nodes)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	if strings.Contains(captured.GetSql(), "LIMIT ?") {
		t.Fatalf("expected unbounded active profile query to omit LIMIT, got %q", captured.GetSql())
	}
	if len(captured.GetParams()) != 4 {
		t.Fatalf("expected four typed params without limit, got %d", len(captured.GetParams()))
	}
}

// TestStoreListActiveProfileNodesPreservesExplicitLargeLimit verifies callers can now request an explicit large active-node window without being silently capped to 256.
// TestStoreListActiveProfileNodesPreservesExplicitLargeLimit 用于验证调用方现在可以显式请求较大的 active 节点窗口，而不会被静默裁成 256。
func TestStoreListActiveProfileNodesPreservesExplicitLargeLimit(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	nodes, err := store.ListActiveProfileNodes(context.Background(), logicdomain.ProfileTargetRef{
		ProfileType: logicdomain.ProfileTypeProject,
		BindID:      9,
	}, 500)
	if err != nil {
		t.Fatalf("ListActiveProfileNodes returned error: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected empty nodes slice, got %+v", nodes)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	if !strings.Contains(captured.GetSql(), "LIMIT ?") {
		t.Fatalf("expected bounded active profile query to keep LIMIT placeholder, got %q", captured.GetSql())
	}
	if len(captured.GetParams()) != 5 {
		t.Fatalf("expected five typed params with explicit limit, got %d", len(captured.GetParams()))
	}
	value := captured.GetParams()[4]
	if value.Kind != sqliteffi.SQLValueInt64 || value.Int64 != 500 {
		t.Fatalf("expected explicit sqlite limit 500, got %#v", value)
	}
}

// TestStoreListActiveProfileNodesOrdersByAnchorTimeline verifies public active-node queries now sort by the resolved profile-date anchor instead of the stale stored profile_date string or priority buckets.
// TestStoreListActiveProfileNodesOrdersByAnchorTimeline 用于验证公开 active 节点查询现在会按解析后的画像日期锚点排序，而不是继续依赖旧的 profile_date 字符串或优先级桶。
func TestStoreListActiveProfileNodesOrdersByAnchorTimeline(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ListActiveProfileNodes(context.Background(), logicdomain.ProfileTargetRef{
		ProfileType: logicdomain.ProfileTypeProject,
		BindID:      9,
	}, 10)
	if err != nil {
		t.Fatalf("ListActiveProfileNodes returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	if !strings.Contains(captured.GetSql(), "ORDER BY profile_date_anchor_timestamp DESC, n.id ASC") {
		t.Fatalf("expected public active-node query to sort by anchor timeline desc, got %q", captured.GetSql())
	}
	if strings.Contains(captured.GetSql(), "priority ASC") || strings.Contains(captured.GetSql(), "refresh_weight DESC") || strings.Contains(captured.GetSql(), "profile_date DESC") {
		t.Fatalf("expected public active-node query to drop legacy priority/profile_date ordering, got %q", captured.GetSql())
	}
}

// TestStoreLoadActiveProfileNodesOrdersByAnchorTimeline verifies reviewer/lifecycle snapshots use the same corrected date anchor in ascending timeline order instead of the raw stored profile_date string.
// TestStoreLoadActiveProfileNodesOrdersByAnchorTimeline 用于验证 reviewer 与生命周期快照会按修正后的日期锚点正序排序，而不是继续依赖原始 profile_date 字符串。
func TestStoreLoadActiveProfileNodesOrdersByAnchorTimeline(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.loadActiveProfileNodes(context.Background(), logicdomain.ProfileTypeProject, 9, time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatalf("loadActiveProfileNodes returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	if !strings.Contains(captured.GetSql(), "ORDER BY profile_date_anchor_timestamp ASC, n.id ASC") {
		t.Fatalf("expected internal active snapshot query to sort by anchor timeline asc, got %q", captured.GetSql())
	}
	if strings.Contains(captured.GetSql(), "priority ASC") || strings.Contains(captured.GetSql(), "refresh_weight DESC") || strings.Contains(captured.GetSql(), "profile_date ASC") {
		t.Fatalf("expected internal active snapshot query to drop legacy priority/profile_date ordering, got %q", captured.GetSql())
	}
}

// TestStoreExecRetriesRetryableSQLiteErrors verifies SQLITE_BUSY style errors are retried inside the adapter before surfacing.
// TestStoreExecRetriesRetryableSQLiteErrors 用于验证 SQLITE_BUSY 这类错误会先在适配器内部重试，而不是立刻向外暴露。
func TestStoreExecRetriesRetryableSQLiteErrors(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	calls := 0
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		calls++
		if calls == 1 {
			return nil, fmt.Errorf("SQLITE_BUSY: database is locked")
		}
		if strings.TrimSpace(req.GetParamsJson()) != "" {
			t.Fatalf("expected retry attempt to keep typed params, got params_json=%q", req.GetParamsJson())
		}
		return &fakeExecuteResponse{Success: true}, nil
	}

	if err := store.exec(context.Background(), `UPDATE vmm_users SET profile = ? WHERE id = ?`, "vmm", 1); err != nil {
		t.Fatalf("exec returned error after retry: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected one retry and two total calls, got %d", calls)
	}
}

// TestStoreSeedDebugWorkspaceIfEmptyUsesTypedSQLiteParams verifies fresh-workspace debug bootstrap inserts deterministic owner rows through FFI-supported single-statement typed calls.
// TestStoreSeedDebugWorkspaceIfEmptyUsesTypedSQLiteParams 用于验证全新工作区调试启动会通过 FFI 支持的单语句强类型调用插入确定性的 owner 行。
func TestStoreSeedDebugWorkspaceIfEmptyUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var countQuery *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		countQuery = req
		if !strings.Contains(req.GetSql(), "COUNT(*)") || !strings.Contains(req.GetSql(), "vmm_projects") {
			t.Fatalf("expected project-count query before debug seed, got %q", req.GetSql())
		}
		return &fakeQueryJSONResponse{JsonData: `[{"count":0}]`}, nil
	}

	// Capture every single-statement ExecuteScript request so the test follows the real FFI parameter boundary used during schema bootstrap.
	// 捕获每一次单语句 ExecuteScript 请求，让测试跟随 schema 启动期间真实使用的 FFI 参数边界。
	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	if err := store.seedDebugWorkspaceIfEmpty(context.Background()); err != nil {
		t.Fatalf("seedDebugWorkspaceIfEmpty returned error: %v", err)
	}
	if countQuery == nil {
		t.Fatal("expected project-count query to be captured")
	}
	if strings.TrimSpace(countQuery.GetParamsJson()) != "" {
		t.Fatalf("expected project-count query to avoid params_json, got %q", countQuery.GetParamsJson())
	}
	if len(requests) != 4 {
		t.Fatalf("expected four parameterized seed statement requests, got %d", len(requests))
	}
	for _, request := range requests {
		if strings.TrimSpace(request.GetParamsJson()) != "" {
			t.Fatalf("expected typed params only, got params_json=%q", request.GetParamsJson())
		}
	}

	capturedSQL := sqliteExecutedSQLLog(requests)
	for _, fragment := range []string{"INSERT INTO vmm_users", "INSERT INTO vmm_teams", "INSERT INTO vmm_spaces", "INSERT INTO vmm_projects"} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected debug seed SQL to contain %q, got %q", fragment, capturedSQL)
		}
	}
	for _, forbidden := range []string{"BEGIN IMMEDIATE;", "COMMIT;", "OR IGNORE"} {
		if strings.Contains(capturedSQL, forbidden) {
			t.Fatalf("expected debug seed SQL to stay single-statement per FFI call, got %q", capturedSQL)
		}
	}
	if strings.Contains(capturedSQL, logicdomain.DefaultWorkspaceResourceName) {
		t.Fatalf("default workspace name leaked into SQL text: %q", capturedSQL)
	}
	if got := countSQLiteStringParam(requests, debugSeedDefaultName); got != 4 {
		t.Fatalf("expected default workspace name as four string params, got %d", got)
	}

	stringTimes := make([]string, 0, 8)
	for _, request := range requests {
		for _, param := range request.GetParams() {
			if param.Kind == sqliteffi.SQLValueString && param.String != debugSeedDefaultName {
				stringTimes = append(stringTimes, param.String)
			}
		}
	}
	if len(stringTimes) != 8 {
		t.Fatalf("expected eight seed timestamp params, got %d: %#v", len(stringTimes), stringTimes)
	}
	for _, value := range stringTimes {
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			t.Fatalf("seed timestamp param is not RFC3339Nano: %q", value)
		}
	}

	params := make([]sqliteffi.SQLValue, 0, 19)
	for _, request := range requests {
		params = append(params, request.GetParams()...)
	}
	if len(params) != 19 {
		t.Fatalf("expected nineteen debug seed params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 != debugSeedUserID {
		t.Fatalf("unexpected user seed id param: %#v", params[0])
	}
	if params[13].Kind != sqliteffi.SQLValueInt64 || params[13].Int64 != debugSeedProjectID ||
		params[14].Kind != sqliteffi.SQLValueInt64 || params[14].Int64 != debugSeedTeamID ||
		params[15].Kind != sqliteffi.SQLValueInt64 || params[15].Int64 != debugSeedSpaceID {
		t.Fatalf("unexpected project seed id params: %#v", params[13:16])
	}
}

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

// TestStoreCreateProfileInstructionUsesTypedSQLiteParams verifies manual instruction text is bound as typed values instead of being interpolated into the SQL script.
// TestStoreCreateProfileInstructionUsesTypedSQLiteParams 用于验证手工画像指令文本会作为强类型参数绑定，而不是被插入 SQL 脚本文本。
func TestStoreCreateProfileInstructionUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, _ *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		return &fakeQueryJSONResponse{JsonData: `[{"next_id":41}]`}, nil
	}
	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	instruction := "  keep 'quoted'; DROP TABLE vmm_profile_nodes;  "
	reviewResult := "  {\"decision\":\"needs 'review'\"}  "
	failureReason := "  pending reason '; DROP TABLE vmm_users;  "
	record, err := store.CreateProfileInstruction(context.Background(), logicdomain.ProfileInstructionRecord{
		ProfileType:   logicdomain.ProfileTypeProject,
		BindID:        7,
		Instruction:   instruction,
		Status:        logicdomain.ProfileInstructionStatusPending,
		ReviewResult:  reviewResult,
		FailureReason: failureReason,
	})
	if err != nil {
		t.Fatalf("CreateProfileInstruction returned error: %v", err)
	}
	if record.ID != 41 {
		t.Fatalf("instruction id = %d, want 41", record.ID)
	}
	if captured == nil {
		t.Fatal("expected ExecuteScript request to be captured")
	}
	if strings.Contains(captured.GetSql(), "DROP TABLE") || strings.Contains(captured.GetSql(), "needs 'review'") {
		t.Fatalf("profile instruction text leaked into SQL: %q", captured.GetSql())
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	params := captured.GetParams()
	if len(params) != 9 {
		t.Fatalf("expected nine typed params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 != 41 {
		t.Fatalf("expected id int64 param 41, got %#v", params[0])
	}
	if params[3].Kind != sqliteffi.SQLValueString || params[3].String != strings.TrimSpace(instruction) {
		t.Fatalf("expected instruction string param, got %#v", params[3])
	}
	if params[5].Kind != sqliteffi.SQLValueString || params[5].String != strings.TrimSpace(reviewResult) {
		t.Fatalf("expected review string param, got %#v", params[5])
	}
	if params[6].Kind != sqliteffi.SQLValueString || params[6].String != strings.TrimSpace(failureReason) {
		t.Fatalf("expected failure string param, got %#v", params[6])
	}
}

// TestStoreFailProfileInstructionUsesTypedSQLiteParams verifies failure diagnostics are bound as typed values so reviewer output cannot reshape the UPDATE script.
// TestStoreFailProfileInstructionUsesTypedSQLiteParams 用于验证失败诊断信息会作为强类型参数绑定，避免评审输出改变 UPDATE 脚本结构。
func TestStoreFailProfileInstructionUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	reviewResult := "  {\"reason\":\"bad 'shape'\"}  "
	failureReason := "  failed '; DROP TABLE vmm_profile_instructions;  "
	if err := store.FailProfileInstruction(context.Background(), 77, failureReason, reviewResult); err != nil {
		t.Fatalf("FailProfileInstruction returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected ExecuteScript request to be captured")
	}
	if strings.Contains(captured.GetSql(), "DROP TABLE") || strings.Contains(captured.GetSql(), "bad 'shape'") {
		t.Fatalf("profile failure text leaked into SQL: %q", captured.GetSql())
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	params := captured.GetParams()
	if !strings.Contains(captured.GetSql(), "WHERE id = ?") || !strings.Contains(captured.GetSql(), "instruction_status = ?") {
		t.Fatalf("expected failed update to guard the pending instruction status, got %q", captured.GetSql())
	}
	if len(params) != 6 {
		t.Fatalf("expected six typed params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 != int64(logicdomain.ProfileInstructionStatusFailed) {
		t.Fatalf("expected failed status int64 param, got %#v", params[0])
	}
	if params[1].Kind != sqliteffi.SQLValueString || params[1].String != strings.TrimSpace(reviewResult) {
		t.Fatalf("expected review string param, got %#v", params[1])
	}
	if params[2].Kind != sqliteffi.SQLValueString || params[2].String != strings.TrimSpace(failureReason) {
		t.Fatalf("expected failure string param, got %#v", params[2])
	}
	if params[4].Kind != sqliteffi.SQLValueInt64 || params[4].Int64 != 77 {
		t.Fatalf("expected instruction id int64 param, got %#v", params[4])
	}
	if params[5].Kind != sqliteffi.SQLValueInt64 || params[5].Int64 != int64(logicdomain.ProfileInstructionStatusPending) {
		t.Fatalf("expected pending status guard param, got %#v", params[5])
	}
}

// TestStoreFailProfileInstructionKeepsMismatchedCommitUnknownUncertain verifies status-only read-back is not enough to recover one failed-instruction commit.
// TestStoreFailProfileInstructionKeepsMismatchedCommitUnknownUncertain 用于验证仅凭状态相同不足以恢复一次失败指令的提交未知错误。
func TestStoreFailProfileInstructionKeepsMismatchedCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return nil, errors.New("failed to commit profile instruction failed")
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if !strings.Contains(req.GetSql(), "FROM vmm_profile_instructions") {
			t.Fatalf("unexpected failed-instruction reconcile query: %q", req.GetSql())
		}
		requireSQLiteInt64Params(t, req.GetParams(), 77)
		if captured == nil {
			t.Fatal("expected failed-instruction update before reconcile read")
		}
		updatedMs := captured.GetParams()[3].Int64
		return &fakeQueryJSONResponse{JsonData: sqliteProfileInstructionRowJSON(77, logicdomain.ProfileInstructionStatusFailed, `{"different":true}`, "different failure", updatedMs)}, nil
	}

	err := store.FailProfileInstruction(context.Background(), 77, "expected failure", `{"expected":true}`)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected mismatched failed-instruction commit to stay outcome-uncertain, got %v", err)
	}
}

// TestStoreApplyManualProfileInstructionUsesTypedSQLiteParamsForFinalTextWrites verifies applied review JSON and rendered profile blobs stay out of raw SQL templates.
// TestStoreApplyManualProfileInstructionUsesTypedSQLiteParamsForFinalTextWrites 用于验证 applied 评审 JSON 与渲染画像 Blob 不会进入原始 SQL 模板。
func TestStoreApplyManualProfileInstructionUsesTypedSQLiteParamsForFinalTextWrites(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	reviewResult := "  {\"accepted\":\"value '; DROP TABLE vmm_profile_nodes;\"}  "
	renderedProfile := "  rendered profile '; DROP TABLE vmm_projects;  "
	result, err := store.ApplyManualProfileInstruction(
		context.Background(),
		logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
		logicdomain.ProfileInstructionRecord{ID: 55},
		nil,
		nil,
		renderedProfile,
		reviewResult,
	)
	if err != nil {
		t.Fatalf("ApplyManualProfileInstruction returned error: %v", err)
	}
	if result.InstructionID != 55 {
		t.Fatalf("instruction id = %d, want 55", result.InstructionID)
	}
	if len(requests) != 2 {
		t.Fatalf("expected applied update and rendered profile update, got %d requests", len(requests))
	}

	rendered := requests[0]
	if strings.Contains(rendered.GetSql(), "DROP TABLE") || strings.Contains(rendered.GetSql(), "rendered profile") {
		t.Fatalf("rendered profile text leaked into SQL: %q", rendered.GetSql())
	}
	if !strings.Contains(rendered.GetSql(), "UPDATE vmm_projects") {
		t.Fatalf("expected project profile update SQL, got %q", rendered.GetSql())
	}
	renderedParams := rendered.GetParams()
	if len(renderedParams) != 3 {
		t.Fatalf("expected three rendered profile params, got %d: %#v", len(renderedParams), renderedParams)
	}
	if renderedParams[0].Kind != sqliteffi.SQLValueString || renderedParams[0].String != strings.TrimSpace(renderedProfile) {
		t.Fatalf("expected rendered profile string param, got %#v", renderedParams[0])
	}
	if renderedParams[2].Kind != sqliteffi.SQLValueInt64 || renderedParams[2].Int64 != 9 {
		t.Fatalf("expected rendered profile target id param, got %#v", renderedParams[2])
	}

	applied := requests[1]
	if strings.Contains(applied.GetSql(), "DROP TABLE") || strings.Contains(applied.GetSql(), "accepted") {
		t.Fatalf("applied review text leaked into SQL: %q", applied.GetSql())
	}
	appliedParams := applied.GetParams()
	if !strings.Contains(applied.GetSql(), "WHERE id = ?") || !strings.Contains(applied.GetSql(), "instruction_status = ?") {
		t.Fatalf("expected applied update to guard the pending instruction status, got %q", applied.GetSql())
	}
	if len(appliedParams) != 5 {
		t.Fatalf("expected five applied params, got %d: %#v", len(appliedParams), appliedParams)
	}
	if appliedParams[1].Kind != sqliteffi.SQLValueString || appliedParams[1].String != strings.TrimSpace(reviewResult) {
		t.Fatalf("expected applied review string param, got %#v", appliedParams[1])
	}
	if appliedParams[3].Kind != sqliteffi.SQLValueInt64 || appliedParams[3].Int64 != 55 {
		t.Fatalf("expected applied instruction id param, got %#v", appliedParams[3])
	}
	if appliedParams[4].Kind != sqliteffi.SQLValueInt64 || appliedParams[4].Int64 != int64(logicdomain.ProfileInstructionStatusPending) {
		t.Fatalf("expected applied pending guard param, got %#v", appliedParams[4])
	}
}

// TestStoreApplyManualProfileInstructionKeepsMismatchedAppliedCommitUnknownUncertain verifies applied status alone cannot recover the final instruction update.
// TestStoreApplyManualProfileInstructionKeepsMismatchedAppliedCommitUnknownUncertain 用于验证仅凭 applied 状态不能恢复最终指令状态写入的提交未知错误。
func TestStoreApplyManualProfileInstructionKeepsMismatchedAppliedCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "UPDATE vmm_profile_instructions") {
			return nil, errors.New("failed to commit manual profile instruction applied")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if !strings.Contains(req.GetSql(), "FROM vmm_profile_instructions") {
			t.Fatalf("unexpected applied-instruction reconcile query: %q", req.GetSql())
		}
		requireSQLiteInt64Params(t, req.GetParams(), 55)
		if len(requests) != 2 {
			t.Fatalf("expected rendered and applied updates before reconcile read, got %d requests", len(requests))
		}
		updatedMs := requests[1].GetParams()[2].Int64
		return &fakeQueryJSONResponse{JsonData: sqliteProfileInstructionRowJSON(55, logicdomain.ProfileInstructionStatusApplied, `{"different":true}`, "", updatedMs)}, nil
	}

	_, err := store.ApplyManualProfileInstruction(
		context.Background(),
		logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
		logicdomain.ProfileInstructionRecord{ID: 55},
		nil,
		nil,
		"rendered profile",
		`{"expected":true}`,
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected mismatched applied-instruction commit to stay outcome-uncertain, got %v", err)
	}
}

// TestStoreApplyManualProfileInstructionStopsBeforeAppliedWhenRenderedTargetMissing verifies a missing durable scope row cannot leave the manual instruction in applied state.
// TestStoreApplyManualProfileInstructionStopsBeforeAppliedWhenRenderedTargetMissing 用于验证长期 scope 行缺失时，不会继续把手工画像指令标记为 applied。
func TestStoreApplyManualProfileInstructionStopsBeforeAppliedWhenRenderedTargetMissing(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}

	_, err := store.ApplyManualProfileInstruction(
		context.Background(),
		logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
		logicdomain.ProfileInstructionRecord{ID: 55},
		nil,
		nil,
		"rendered profile",
		`{"accepted_nodes":[]}`,
	)
	if err == nil {
		t.Fatal("expected missing rendered profile target to fail")
	}
	if !strings.Contains(err.Error(), "update rendered manual profile target affected 0 rows, want 1") {
		t.Fatalf("unexpected missing target error: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected only rendered profile update before failure, got %d requests", len(requests))
	}
	if strings.Contains(requests[0].GetSql(), "vmm_profile_instructions") {
		t.Fatalf("did not expect applied status update after missing rendered target, got %q", requests[0].GetSql())
	}
}

// TestStoreApplyManualProfileInstructionMarksPostMutationFailureUncertain verifies post-node-write failures do not look like clean failures to the use case.
// TestStoreApplyManualProfileInstructionMarksPostMutationFailureUncertain 用于验证节点写入后的后续失败不会被用例层误认为干净失败。
func TestStoreApplyManualProfileInstructionMarksPostMutationFailureUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "UPDATE vmm_projects") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_profile_nodes") && strings.Contains(req.GetSql(), "MAX(id)") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":101}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ApplyManualProfileInstruction(
		context.Background(),
		logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
		logicdomain.ProfileInstructionRecord{ID: 55},
		[]logicdomain.ProfileNodeCandidate{{
			Content:       "project prefers Go",
			Status:        logicdomain.ProfileStatusActive,
			Priority:      logicdomain.ProfilePriorityP1,
			ProfileLevel:  logicdomain.ProfileLevelStable,
			RefreshWeight: 64,
			SourceKind:    logicdomain.ProfileSourceKindManualInstruction,
			SourceID:      55,
			ProfileDate:   "2026-07-05",
		}},
		nil,
		"rendered profile",
		`{"accepted_nodes":[{"index":0}]}`,
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected post-mutation rendered-profile failure to be outcome-uncertain, got %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected insert and rendered profile update before failure, got %d requests", len(requests))
	}
	if strings.Contains(requests[len(requests)-1].GetSql(), "vmm_profile_instructions") {
		t.Fatalf("did not expect applied status update after post-mutation failure, got %q", requests[len(requests)-1].GetSql())
	}
}

// TestStoreApplyManualProfileInstructionMarksSupersedeDriftUncertain verifies stale active-node drift is surfaced as an uncertain partial write after the replacement node is already inserted.
// TestStoreApplyManualProfileInstructionMarksSupersedeDriftUncertain 用于验证替代节点已插入后，如果旧 active 节点发生漂移，会以结果不确定暴露。
func TestStoreApplyManualProfileInstructionMarksSupersedeDriftUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "id IN (?,?)") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_profile_nodes") && strings.Contains(req.GetSql(), "MAX(id)") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":101}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ApplyManualProfileInstruction(
		context.Background(),
		logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
		logicdomain.ProfileInstructionRecord{ID: 55},
		[]logicdomain.ProfileNodeCandidate{{
			Content:          "project prefers Go",
			Status:           logicdomain.ProfileStatusActive,
			Priority:         logicdomain.ProfilePriorityP1,
			ProfileLevel:     logicdomain.ProfileLevelStable,
			RefreshWeight:    64,
			SourceKind:       logicdomain.ProfileSourceKindManualInstruction,
			SourceID:         55,
			ProfileDate:      "2026-07-05",
			SupersedeNodeIDs: []uint64{9, 7},
		}},
		nil,
		"rendered profile",
		`{"accepted_nodes":[{"index":0}]}`,
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected post-insert supersede drift to be outcome-uncertain, got %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected insert and supersede update before failure, got %d requests", len(requests))
	}
	if strings.Contains(requests[len(requests)-1].GetSql(), "vmm_profile_instructions") {
		t.Fatalf("did not expect applied status update after supersede drift, got %q", requests[len(requests)-1].GetSql())
	}
}

// TestStoreApplyManualProfileInstructionMarksRetireDriftUncertain verifies stale standalone retire decisions are not treated as clean failures after a new node has been inserted.
// TestStoreApplyManualProfileInstructionMarksRetireDriftUncertain 用于验证新节点已经插入后，独立退役决策的状态漂移不会被当成干净失败。
func TestStoreApplyManualProfileInstructionMarksRetireDriftUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "WHERE profile_status = ? AND id = ?") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_profile_nodes") && strings.Contains(req.GetSql(), "MAX(id)") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":101}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ApplyManualProfileInstruction(
		context.Background(),
		logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
		logicdomain.ProfileInstructionRecord{ID: 55},
		[]logicdomain.ProfileNodeCandidate{{
			Content:       "project prefers Go",
			Status:        logicdomain.ProfileStatusActive,
			Priority:      logicdomain.ProfilePriorityP1,
			ProfileLevel:  logicdomain.ProfileLevelStable,
			RefreshWeight: 64,
			SourceKind:    logicdomain.ProfileSourceKindManualInstruction,
			SourceID:      55,
			ProfileDate:   "2026-07-05",
		}},
		[]logicdomain.ProfileRetireDecision{{NodeID: 12, Reason: "stale profile"}},
		"rendered profile",
		`{"accepted_nodes":[{"index":0}],"retired_nodes":[{"node_id":12}]}`,
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected post-insert retire drift to be outcome-uncertain, got %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected insert and retire update before failure, got %d requests", len(requests))
	}
	if strings.Contains(requests[len(requests)-1].GetSql(), "vmm_profile_instructions") {
		t.Fatalf("did not expect applied status update after retire drift, got %q", requests[len(requests)-1].GetSql())
	}
}

// TestStoreApplyTurnAnalysisUsesTypedSQLiteParamsForExtractedTextWrites verifies post-action split writes keep extracted text out of SQL templates.
// TestStoreApplyTurnAnalysisUsesTypedSQLiteParamsForExtractedTextWrites 用于验证 post-action 拆分写入会让提炼文本远离 SQL 模板。
func TestStoreApplyTurnAnalysisUsesTypedSQLiteParamsForExtractedTextWrites(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "id IN (?,?)") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_users"):
			return &fakeQueryJSONResponse{JsonData: `[{"id":7,"name":"user","profile":"","delete_confirm_code":"","created_at":"2026-07-05T09:00:00Z","updated_at":"2026-07-05T09:00:00Z"}]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p"):
			return &fakeQueryJSONResponse{JsonData: `[{"id":9,"team_id":2,"space_id":3,"name":"project","profile":"","team_name":"team","space_name":"space","created_at":"2026-07-05T09:00:00Z","updated_at":"2026-07-05T09:00:00Z"}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_profile_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "SELECT vector_id") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"vector_id":"vec-old-superseded"}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	turnCreatedAt := time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)
	details := "  turn details '; DROP TABLE vmm_turn_records; --  "
	userProfile := "  merged user profile '; DROP TABLE vmm_users; --  "
	projectProfile := "  merged project profile '; DROP TABLE vmm_projects; --  "
	vectorID := "  vec-new '; DROP TABLE vmm_memory_nodes; --  "
	memoryAbstract := "  memory abstract '; DROP TABLE vmm_memory_nodes; --  "
	memoryDetails := "  memory details '; DROP TABLE vmm_memory_nodes; --  "
	dedupeHash := "  hash '; DROP TABLE vmm_memory_nodes; --  "
	contextKey := "Phase Key '; DROP TABLE vmm_memory_context_edges; --"
	contextValue := "Delivery Value '; DROP TABLE vmm_memory_context_edges; --"
	profileContent := "  profile content '; DROP TABLE vmm_profile_nodes; --  "
	profileLevelReason := "  profile level reason '; DROP TABLE vmm_profile_nodes; --  "
	profileStatusReason := "  profile status reason '; DROP TABLE vmm_profile_nodes; --  "
	result, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: turnCreatedAt, UpdatedAt: turnCreatedAt},
		logicdomain.TurnAnalysis{
			Details:              details,
			DetailsBudget:        123,
			UserProfileMerged:    true,
			MergedUserProfile:    userProfile,
			ProjectProfileMerged: true,
			MergedProjectProfile: projectProfile,
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:           vectorID,
				Vector:             []float32{0.25, 0.75},
				Category:           logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:           memoryAbstract,
				Details:            memoryDetails,
				SupersedeMemoryIDs: []uint64{77},
				ContextEdges: []logicdomain.MemoryContextEdgeCandidate{{
					ContextKey:   contextKey,
					ContextValue: contextValue,
					Relation:     logicdomain.MemoryContextRelationSupport,
				}},
				SourceKind:    logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:    logicdomain.MemoryScopeLevelProject,
				Priority:      logicdomain.MemoryPriorityP1,
				MemoryLevel:   logicdomain.MemoryLevelStable,
				RefreshWeight: 8,
				DedupeHash:    dedupeHash,
			}},
			ProfileNodes: []logicdomain.ProfileNodeCandidate{{
				ProfileType:      logicdomain.ProfileTypeUser,
				Content:          profileContent,
				Status:           logicdomain.ProfileStatusActive,
				Priority:         logicdomain.ProfilePriorityP1,
				ProfileLevel:     logicdomain.ProfileLevelStable,
				LevelReason:      profileLevelReason,
				RefreshWeight:    16,
				ProfileDate:      "2026-07-05",
				SourceKind:       logicdomain.ProfileSourceKindTurnExtract,
				SourceID:         44,
				StatusReason:     profileStatusReason,
				SupersedeNodeIDs: []uint64{12},
			}},
		},
	)
	if err != nil {
		t.Fatalf("ApplyTurnAnalysis returned error: %v", err)
	}
	if len(result.InsertedMemoryNodes) != 1 || result.InsertedMemoryNodes[0].ID != 201 {
		t.Fatalf("unexpected inserted memory nodes: %+v", result.InsertedMemoryNodes)
	}
	if len(result.SupersededVectorIDs) != 1 || result.SupersededVectorIDs[0] != "vec-old-superseded" {
		t.Fatalf("unexpected superseded vector ids: %+v", result.SupersededVectorIDs)
	}
	requireNoSQLiteTransactionControl(t, requests)

	capturedSQL := sqliteExecutedSQLLog(requests)
	for _, leaked := range []string{
		"DROP TABLE",
		"drop table",
		"turn details",
		"merged user profile",
		"merged project profile",
		"vec-new",
		"memory abstract",
		"memory details",
		"profile content",
		"profile level reason",
		"profile status reason",
	} {
		if strings.Contains(capturedSQL, leaked) {
			t.Fatalf("expected extracted text %q to stay out of SQL text, got %q", leaked, capturedSQL)
		}
	}
	if !strings.Contains(capturedSQL, "WHERE profile_status = ? AND profile_type = ? AND bind_id = ? AND id IN (?)") {
		t.Fatalf("expected profile supersede to guard status, target, and node id, got %q", capturedSQL)
	}
	for _, request := range requests {
		if strings.TrimSpace(request.GetParamsJson()) != "" {
			t.Fatalf("expected typed params only, got params_json=%q", request.GetParamsJson())
		}
	}
	for _, value := range []string{
		strings.TrimSpace(details),
		strings.TrimSpace(userProfile),
		strings.TrimSpace(projectProfile),
		strings.TrimSpace(vectorID),
		strings.TrimSpace(memoryAbstract),
		strings.TrimSpace(memoryDetails),
		strings.TrimSpace(dedupeHash),
		logicdomain.NormalizeMemoryContextKey(contextKey),
		logicdomain.NormalizeMemoryContextValue(contextValue),
		strings.TrimSpace(profileContent),
		strings.TrimSpace(profileLevelReason),
	} {
		if got := countSQLiteStringParam(requests, value); got != 1 {
			t.Fatalf("expected string param %q exactly once, got %d", value, got)
		}
	}
	if got := countSQLiteStringParam(requests, strings.TrimSpace(profileStatusReason)); got != 2 {
		t.Fatalf("expected profile status reason in insert and supersede params, got %d", got)
	}
}

// TestParameterizedTurnAnalysisUpdateStatementGuardsSessionPendingTurn verifies post-action only flips the current session's pending turn.
// TestParameterizedTurnAnalysisUpdateStatementGuardsSessionPendingTurn 用于验证 post-action 只会切换当前 session 的 pending turn。
func TestParameterizedTurnAnalysisUpdateStatementGuardsSessionPendingTurn(t *testing.T) {
	statement := parameterizedTurnAnalysisUpdateStatement(33, 44, "  turn details  ", 123, 4567)

	if !strings.Contains(statement.SQL, "WHERE id = ? AND session_id = ? AND extracted_status = ?") {
		t.Fatalf("expected turn analysis update to guard id, session, and pending status, got %q", statement.SQL)
	}
	if len(statement.Params) != 7 {
		t.Fatalf("expected seven turn analysis update params, got %d: %#v", len(statement.Params), statement.Params)
	}
	expectedParams := []any{
		"turn details",
		123,
		logicdomain.TurnExtractedStatusDone,
		int64(4567),
		uint64(44),
		uint64(33),
		logicdomain.TurnExtractedStatusPending,
	}
	for idx, expected := range expectedParams {
		if statement.Params[idx] != expected {
			t.Fatalf("param %d mismatch: got %#v, want %#v", idx, statement.Params[idx], expected)
		}
	}
}

// sqliteAnalyzedTurnRowJSON renders one analyzed turn row for first-mutation reconciliation tests.
// sqliteAnalyzedTurnRowJSON 用于为首个写入对账测试渲染一条已分析 turn 行。
func sqliteAnalyzedTurnRowJSON(turnID, sessionID, projectID uint64, details string, detailsBudget int, updatedMs int64) string {
	return fmt.Sprintf(
		`[{"id":%d,"session_id":%d,"project_id":%d,"dehydrated_content":%s,"dehydrated_budget":3,"extracted_status":%d,"details":%s,"details_budget":%d,"created_timestamp":10,"updated_timestamp":%d}]`,
		turnID,
		sessionID,
		projectID,
		strconv.Quote(`{"user":"analyzed"}`),
		logicdomain.TurnExtractedStatusDone,
		strconv.Quote(details),
		detailsBudget,
		updatedMs,
	)
}

// sqliteMemoryNodeRowJSONFromInsertParams mirrors parameterizedMemoryNodeInsertStatement so reconciliation tests use the captured typed write as the read-back source.
// sqliteMemoryNodeRowJSONFromInsertParams 用于镜像 parameterizedMemoryNodeInsertStatement，让对账测试直接用捕获到的强类型写入构造回读事实源。
func sqliteMemoryNodeRowJSONFromInsertParams(t *testing.T, params []sqliteffi.SQLValue) string {
	t.Helper()
	if len(params) != 33 {
		t.Fatalf("expected thirty-three memory insert params, got %d: %#v", len(params), params)
	}
	sourceTurnID := "null"
	if params[6].Kind == sqliteffi.SQLValueInt64 {
		sourceTurnID = strconv.FormatInt(params[6].Int64, 10)
	}
	return fmt.Sprintf(
		`[{"id":%d,"team_id":%d,"space_id":%d,"project_id":%d,"user_id":%d,"origin_session_id":%d,"source_turn_id":%s,"vector_id":%s,"vector_json":%s,"source_kind":%d,"scope_level":%d,"category":%d,"abstract":%s,"details":%s,"memory_status":%d,"priority":%d,"memory_level":%d,"refresh_weight":%d,"support_count":%d,"rebuttal_count":%d,"status_reason":%s,"expires_timestamp":%d,"last_recalled_timestamp":%d,"last_adopted_timestamp":%d,"last_reinforced_timestamp":%d,"recalled_count":%d,"adopted_count":%d,"reinforcement_count":%d,"cross_session_adopted_count":%d,"decay_disabled":%d,"dedupe_hash":%s,"created_timestamp":%d,"updated_timestamp":%d}]`,
		params[0].Int64,
		params[1].Int64,
		params[2].Int64,
		params[3].Int64,
		params[4].Int64,
		params[5].Int64,
		sourceTurnID,
		strconv.Quote(params[7].String),
		strconv.Quote(params[8].String),
		params[9].Int64,
		params[10].Int64,
		params[11].Int64,
		strconv.Quote(params[12].String),
		strconv.Quote(params[13].String),
		params[14].Int64,
		params[15].Int64,
		params[16].Int64,
		params[17].Int64,
		params[18].Int64,
		params[19].Int64,
		strconv.Quote(params[20].String),
		params[21].Int64,
		params[22].Int64,
		params[23].Int64,
		params[24].Int64,
		params[25].Int64,
		params[26].Int64,
		params[27].Int64,
		params[28].Int64,
		params[29].Int64,
		strconv.Quote(params[30].String),
		params[31].Int64,
		params[32].Int64,
	)
}

// sqliteAdoptableMemoryNodeRowsJSON renders one active memory row used as the factual source for adoption lifecycle tests.
// sqliteAdoptableMemoryNodeRowsJSON 用于渲染采纳生命周期测试所需的一条 active memory 事实源行。
func sqliteAdoptableMemoryNodeRowsJSON(expiresMs int64) string {
	return fmt.Sprintf(`[
{"id":201,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":11,"source_turn_id":6,"vector_id":"vec-201","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":%d,"category":3,"abstract":"adopted memory","details":"details","memory_status":%d,"priority":%d,"memory_level":%d,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":%d,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":1,"reinforcement_count":1,"cross_session_adopted_count":3,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20}
]`,
		logicdomain.MemoryScopeLevelSession,
		logicdomain.MemoryStatusActive,
		logicdomain.MemoryPriorityP0,
		logicdomain.MemoryLevelSession,
		expiresMs,
	)
}

// sqliteMemoryNodeRowJSONFromAdoptionUpdateParams mirrors parameterizedMemoryAdoptionUpdateStatement so commit-unknown reconciliation tests read back the captured lifecycle target.
// sqliteMemoryNodeRowJSONFromAdoptionUpdateParams 用于镜像 parameterizedMemoryAdoptionUpdateStatement，让提交未知对账测试回读捕获到的生命周期目标。
func sqliteMemoryNodeRowJSONFromAdoptionUpdateParams(t *testing.T, params []sqliteffi.SQLValue) string {
	t.Helper()
	if len(params) != 15 {
		t.Fatalf("expected fifteen memory adoption update params, got %d: %#v", len(params), params)
	}
	for index, param := range params {
		if param.Kind != sqliteffi.SQLValueInt64 {
			t.Fatalf("expected memory adoption update param %d to be int64, got %#v", index, param)
		}
	}
	return fmt.Sprintf(
		`[{"id":%d,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":11,"source_turn_id":6,"vector_id":"vec-201","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":%d,"category":3,"abstract":"adopted memory","details":"details","memory_status":%d,"priority":%d,"memory_level":%d,"refresh_weight":%d,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":%d,"last_recalled_timestamp":%d,"last_adopted_timestamp":%d,"last_reinforced_timestamp":%d,"recalled_count":%d,"adopted_count":%d,"reinforcement_count":%d,"cross_session_adopted_count":%d,"decay_disabled":%d,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":%d}]`,
		params[14].Int64,
		params[0].Int64,
		params[1].Int64,
		int64(logicdomain.MemoryPriorityP0),
		params[2].Int64,
		params[3].Int64,
		params[4].Int64,
		params[5].Int64,
		params[6].Int64,
		params[7].Int64,
		params[8].Int64,
		params[9].Int64,
		params[10].Int64,
		params[11].Int64,
		params[12].Int64,
		params[13].Int64,
	)
}

// sqliteManualDeleteCandidateRowsJSON renders the active rows used by manual-delete relation and cleanup-coordinate tests.
// sqliteManualDeleteCandidateRowsJSON 用于渲染手工删除关系写入与清理坐标测试所需的 active 行。
func sqliteManualDeleteCandidateRowsJSON() string {
	return `[
{"id":301,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":41,"vector_id":"vec-301","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory a","details":"details a","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20},
{"id":302,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":42,"vector_id":"vec-302","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory b","details":"details b","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":11,"updated_timestamp":21}
]`
}

// sqliteMemoryNodeDeleteRowsJSON renders durable memory rows with a controlled delete status, reason, and update timestamp for manual-delete reconciliation tests.
// sqliteMemoryNodeDeleteRowsJSON 用于为手工删除对账测试渲染可控制删除状态、原因和更新时间的长期 memory 行。
func sqliteMemoryNodeDeleteRowsJSON(status int, reason string, updatedMs int64, memoryIDs ...uint64) string {
	rows := make([]string, 0, len(memoryIDs))
	for _, memoryID := range memoryIDs {
		rows = append(rows, fmt.Sprintf(
			`{"id":%d,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":41,"vector_id":%s,"vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory a","details":"details a","memory_status":%d,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":%s,"expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":%d}`,
			memoryID,
			strconv.Quote(fmt.Sprintf("vec-%d", memoryID)),
			status,
			strconv.Quote(strings.TrimSpace(reason)),
			updatedMs,
		))
	}
	return "[" + strings.Join(rows, ",") + "]"
}

// sqliteMemoryNodeStatusRowsJSON renders durable memory rows with controlled lifecycle status for supersede reconciliation tests.
// sqliteMemoryNodeStatusRowsJSON 用于为 supersede 对账测试渲染可控制生命周期状态的长期 memory 行。
func sqliteMemoryNodeStatusRowsJSON(status int, updatedMs int64, memoryIDs ...uint64) string {
	rows := make([]string, 0, len(memoryIDs))
	for _, memoryID := range memoryIDs {
		rows = append(rows, fmt.Sprintf(
			`{"id":%d,"team_id":1,"space_id":2,"project_id":9,"user_id":7,"origin_session_id":33,"source_turn_id":44,"vector_id":%s,"vector_json":"[0.25,0.75]","source_kind":0,"scope_level":1,"category":3,"abstract":"old memory","details":"old memory details","memory_status":%d,"priority":2,"memory_level":1,"refresh_weight":8,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":%d}`,
			memoryID,
			strconv.Quote(fmt.Sprintf("vec-old-%d", memoryID)),
			status,
			updatedMs,
		))
	}
	return "[" + strings.Join(rows, ",") + "]"
}

// sqliteProfileInstructionRowJSON renders one durable manual-instruction row with controlled status payload for commit reconciliation tests.
// sqliteProfileInstructionRowJSON 用于为提交对账测试渲染一条带有可控状态载荷的长期手工指令行。
func sqliteProfileInstructionRowJSON(id uint64, status int, reviewResult, failureReason string, updatedMs int64) string {
	return fmt.Sprintf(
		`[{"id":%d,"profile_type":%d,"bind_id":9,"instruction":"keep deployment notes current","instruction_status":%d,"review_result_json":%s,"failure_reason":%s,"created_timestamp":10,"updated_timestamp":%d}]`,
		id,
		logicdomain.ProfileTypeProject,
		status,
		strconv.Quote(strings.TrimSpace(reviewResult)),
		strconv.Quote(strings.TrimSpace(failureReason)),
		updatedMs,
	)
}

// sqliteProfileNodeStatusRowsJSON renders durable profile-node lifecycle rows with controlled status payload for reconciliation tests.
// sqliteProfileNodeStatusRowsJSON 用于为对账测试渲染带有可控状态载荷的长期画像节点生命周期行。
func sqliteProfileNodeStatusRowsJSON(status int, reason string, updatedMs int64, nodeIDs ...uint64) string {
	rows := make([]string, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		rows = append(rows, fmt.Sprintf(
			`{"id":%d,"profile_status":%d,"superseded_by_id":0,"status_reason":%s,"updated_timestamp":%d}`,
			nodeID,
			status,
			strconv.Quote(strings.TrimSpace(reason)),
			updatedMs,
		))
	}
	return "[" + strings.Join(rows, ",") + "]"
}

// sqliteRenderedProfileBatchRowsJSON renders scope profile rows that mirror one captured ReplaceRenderedProfiles batch for reconciliation tests.
// sqliteRenderedProfileBatchRowsJSON 用于为对账测试渲染与捕获的 ReplaceRenderedProfiles 批量写入一致的 scope 画像行。
func sqliteRenderedProfileBatchRowsJSON(profiles map[uint64]string, updatedAt string, ids ...uint64) string {
	rows := make([]string, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, fmt.Sprintf(
			`{"id":%d,"profile":%s,"updated_at":%s}`,
			id,
			strconv.Quote(profiles[id]),
			strconv.Quote(updatedAt),
		))
	}
	return "[" + strings.Join(rows, ",") + "]"
}

// sqliteMemoryContextEdgeRowsJSONFromInsertRequests mirrors context-edge insert params so reconciliation tests use captured typed writes as read-back rows.
// sqliteMemoryContextEdgeRowsJSONFromInsertRequests 用于镜像情境边插入参数，让对账测试直接使用捕获的强类型写入作为回读行。
func sqliteMemoryContextEdgeRowsJSONFromInsertRequests(t *testing.T, requests ...*fakeExecuteRequest) string {
	t.Helper()
	rows := make([]string, 0, len(requests))
	for _, request := range requests {
		if request == nil {
			t.Fatal("expected non-nil context edge insert request")
		}
		params := request.GetParams()
		if len(params) != 9 {
			t.Fatalf("expected nine context edge insert params, got %d: %#v", len(params), params)
		}
		rows = append(rows, fmt.Sprintf(
			`{"memory_id":%d,"context_key":%s,"context_value":%s,"support_count":%d,"rebuttal_count":%d,"last_supported_timestamp":%d,"last_rebutted_timestamp":%d,"created_timestamp":%d,"updated_timestamp":%d}`,
			params[0].Int64,
			strconv.Quote(params[1].String),
			strconv.Quote(params[2].String),
			params[3].Int64,
			params[4].Int64,
			params[5].Int64,
			params[6].Int64,
			params[7].Int64,
			params[8].Int64,
		))
	}
	return "[" + strings.Join(rows, ",") + "]"
}

// sqliteProfileNodeRowJSONFromInsertParams mirrors parameterizedProfileNodeInsertStatement so profile insert reconciliation tests use captured typed params.
// sqliteProfileNodeRowJSONFromInsertParams 用于镜像 parameterizedProfileNodeInsertStatement，让画像插入对账测试使用捕获到的强类型参数。
func sqliteProfileNodeRowJSONFromInsertParams(t *testing.T, params []sqliteffi.SQLValue) string {
	t.Helper()
	if len(params) != 17 {
		t.Fatalf("expected seventeen profile insert params, got %d: %#v", len(params), params)
	}
	turnID := "null"
	if params[1].Kind == sqliteffi.SQLValueInt64 {
		turnID = strconv.FormatInt(params[1].Int64, 10)
	}
	return fmt.Sprintf(
		`[{"id":%d,"turn_id":%s,"profile_type":%d,"bind_id":%d,"content":%s,"profile_status":%d,"priority":%d,"profile_level":%d,"level_reason":%s,"refresh_weight":%d,"source_kind":%d,"source_id":%d,"status_reason":%s,"expires_timestamp":%d,"superseded_by_id":0,"profile_date":%s,"created_timestamp":%d,"updated_timestamp":%d,"profile_date_anchor_timestamp":%d}]`,
		params[0].Int64,
		turnID,
		params[2].Int64,
		params[3].Int64,
		strconv.Quote(params[4].String),
		params[5].Int64,
		params[6].Int64,
		params[7].Int64,
		strconv.Quote(params[8].String),
		params[9].Int64,
		params[10].Int64,
		params[11].Int64,
		strconv.Quote(params[12].String),
		params[13].Int64,
		strconv.Quote(params[14].String),
		params[15].Int64,
		params[16].Int64,
		params[15].Int64,
	)
}

// TestStoreApplyTurnAnalysisReconcilesCommitUnknownTurnUpdate verifies the first turn-status mutation can recover when the durable row has reached the exact analysis target.
// TestStoreApplyTurnAnalysisReconcilesCommitUnknownTurnUpdate 用于验证首个 turn 状态写入提交未知时，如果长期行达到精确分析目标即可恢复。
func TestStoreApplyTurnAnalysisReconcilesCommitUnknownTurnUpdate(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return nil, errors.New("failed to commit analyzed turn")
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_turn_records") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			requireSQLiteInt64Params(t, req.GetParams(), 44)
			if captured == nil {
				t.Fatal("expected turn update params before reconcile read")
			}
			return &fakeQueryJSONResponse{JsonData: sqliteAnalyzedTurnRowJSON(44, 33, 9, "turn details", 123, captured.GetParams()[3].Int64)}, nil
		}
		t.Fatalf("unexpected turn-analysis reconcile query: %q", req.GetSql())
		return nil, nil
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{Details: "turn details", DetailsBudget: 123},
	)
	if err != nil {
		t.Fatalf("ApplyTurnAnalysis returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected turn update request")
	}
	requireSQLiteInt64Params(t, captured.GetParams()[2:], int64(logicdomain.TurnExtractedStatusDone), captured.GetParams()[3].Int64, 44, 33, int64(logicdomain.TurnExtractedStatusPending))
}

// TestStoreApplyTurnAnalysisKeepsUnmatchedTurnUpdateCommitUnknownUncertain verifies a commit-unknown first mutation is not treated as recoverable when the read-back row differs.
// TestStoreApplyTurnAnalysisKeepsUnmatchedTurnUpdateCommitUnknownUncertain 用于验证首个写入提交未知且回读行不匹配时，不能把它当作可恢复成功。
func TestStoreApplyTurnAnalysisKeepsUnmatchedTurnUpdateCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return nil, errors.New("failed to commit analyzed turn")
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_turn_records") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			return &fakeQueryJSONResponse{JsonData: sqliteAnalyzedTurnRowJSON(44, 33, 9, "different details", 123, time.Now().UTC().UnixMilli())}, nil
		}
		t.Fatalf("unexpected turn-analysis reconcile query: %q", req.GetSql())
		return nil, nil
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{Details: "turn details", DetailsBudget: 123},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain turn update error, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect fresh vector reference before memory insert, got %v", err)
	}
	if !strings.Contains(err.Error(), "apply turn analysis") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected turn update commit-unknown error: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected only the turn update request before unresolved commit unknown, got %d", len(requests))
	}
}

// TestStoreApplyTurnAnalysisRejectsMissingMergedProfileTargetBeforeTurnUpdate verifies profile merge target drift is caught before the turn is marked extracted.
// TestStoreApplyTurnAnalysisRejectsMissingMergedProfileTargetBeforeTurnUpdate 用于验证合并画像目标漂移会在 turn 被标记为已提炼之前被拦截。
func TestStoreApplyTurnAnalysisRejectsMissingMergedProfileTargetBeforeTurnUpdate(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_users") {
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details:           "turn details",
			UserProfileMerged: true,
			MergedUserProfile: "merged user profile",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "load merged user profile target") {
		t.Fatalf("expected missing merged user target error, got %v", err)
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected missing merged profile target to remain a clean failure before turn update, got %v", err)
	}
	if len(requests) != 0 {
		t.Fatalf("expected no writes before missing merged profile target failure, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisMarksProfileTargetUpdateDriftUncertainWithoutFreshVectorReference verifies profile update drift after the turn update still allows fresh-vector rollback.
// TestStoreApplyTurnAnalysisMarksProfileTargetUpdateDriftUncertainWithoutFreshVectorReference 用于验证 turn 更新后的画像目标漂移会标记关系结果不确定，但仍允许回滚尚未被引用的新向量。
func TestStoreApplyTurnAnalysisMarksProfileTargetUpdateDriftUncertainWithoutFreshVectorReference(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "UPDATE vmm_users") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_users"):
			return &fakeQueryJSONResponse{JsonData: `[{"id":7,"name":"user","profile":"","delete_confirm_code":"","created_at":"2026-07-05T09:00:00Z","updated_at":"2026-07-05T09:00:00Z"}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details:           "turn details",
			UserProfileMerged: true,
			MergedUserProfile: "merged user profile",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:    "vec-new-turn",
				Vector:      []float32{0.25, 0.75},
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:    "project status",
				Details:     "project status",
				SourceKind:  logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelStable,
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected profile target update drift to be outcome-uncertain, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect fresh vector reference before memory insert, got %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected turn update and profile target update before failure, got %d requests", len(requests))
	}
	if strings.Contains(sqliteExecutedSQLLog(requests), "INSERT INTO vmm_memory_nodes") {
		t.Fatalf("did not expect memory insert after profile target drift, got %q", sqliteExecutedSQLLog(requests))
	}
}

// TestStoreApplyTurnAnalysisMarksMemoryInsertZeroRowsUncertainWithoutFreshVectorReference verifies a no-op memory insert stops before vectors become durable references.
// TestStoreApplyTurnAnalysisMarksMemoryInsertZeroRowsUncertainWithoutFreshVectorReference 用于验证 memory insert 未影响行时，会在新向量形成长期引用之前停止。
func TestStoreApplyTurnAnalysisMarksMemoryInsertZeroRowsUncertainWithoutFreshVectorReference(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_nodes") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:    "vec-new-turn",
				Vector:      []float32{0.25, 0.75},
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:    "project status",
				Details:     "project status",
				SourceKind:  logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelStable,
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected zero-row memory insert to be outcome-uncertain after turn update, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect fresh vector reference before memory insert affects a row, got %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected turn update and memory insert before failure, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryInsert verifies recovered memory inserts keep going without forcing vector rollback.
// TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryInsert 用于验证 memory insert 提交未知但可对账时会继续后续写入，不触发向量回滚语义。
func TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	var memoryInsert *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_nodes") {
			memoryInsert = req
			return nil, errors.New("failed to commit memory insert")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?)"):
			if memoryInsert == nil {
				t.Fatal("expected memory insert params before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 201)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeRowJSONFromInsertParams(t, memoryInsert.GetParams())}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	result, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:    "vec-new-turn",
				Vector:      []float32{0.25, 0.75},
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:    "project status",
				Details:     "project status",
				SourceKind:  logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelStable,
			}},
		},
	)
	if err != nil {
		t.Fatalf("ApplyTurnAnalysis returned error: %v", err)
	}
	if len(result.InsertedMemoryNodes) != 1 || result.InsertedMemoryNodes[0].ID != 201 {
		t.Fatalf("unexpected recovered memory insert result: %+v", result.InsertedMemoryNodes)
	}
	if len(requests) != 3 {
		t.Fatalf("expected turn update, memory insert, and context-edge cleanup, got %d requests", len(requests))
	}
	if memoryInsert == nil {
		t.Fatal("expected memory insert request")
	}
}

// TestStoreApplyTurnAnalysisKeepsUnmatchedMemoryInsertCommitUnknownFresh verifies unresolved memory insert ambiguity keeps fresh vectors protected from rollback.
// TestStoreApplyTurnAnalysisKeepsUnmatchedMemoryInsertCommitUnknownFresh 用于验证 memory insert 提交未知且无法对账时，会保护可能已被引用的新向量不被回滚。
func TestStoreApplyTurnAnalysisKeepsUnmatchedMemoryInsertCommitUnknownFresh(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_nodes") {
			return nil, errors.New("failed to commit memory insert")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?)"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:    "vec-new-turn",
				Vector:      []float32{0.25, 0.75},
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:    "project status",
				Details:     "project status",
				SourceKind:  logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelStable,
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain memory insert error, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected unresolved memory insert to protect fresh vector reference, got %v", err)
	}
	if !strings.Contains(err.Error(), "apply turn analysis") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected memory insert commit-unknown error: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected turn update and memory insert before unresolved commit unknown, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryContextEdgeCleanup verifies uncertain context-edge cleanup can recover when no edge rows remain.
// TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryContextEdgeCleanup 用于验证情境边清理提交未知时，如果回读确认没有残留边即可恢复。
func TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryContextEdgeCleanup(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "DELETE FROM vmm_memory_context_edges") {
			return nil, errors.New("failed to commit memory context edge cleanup")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges"):
			requireSQLiteInt64Params(t, req.GetParams(), 201)
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	result, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:    "vec-new-turn",
				Vector:      []float32{0.25, 0.75},
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:    "project status",
				Details:     "project status",
				SourceKind:  logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelStable,
			}},
		},
	)
	if err != nil {
		t.Fatalf("ApplyTurnAnalysis returned error: %v", err)
	}
	if len(result.InsertedMemoryNodes) != 1 || result.InsertedMemoryNodes[0].ID != 201 {
		t.Fatalf("unexpected recovered context cleanup result: %+v", result.InsertedMemoryNodes)
	}
	if len(requests) != 3 {
		t.Fatalf("expected turn update, memory insert, and context-edge cleanup, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryContextEdgeInsert verifies uncertain edge inserts recover from the durable edge-table prefix.
// TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryContextEdgeInsert 用于验证情境边插入提交未知时，可通过长期边表前缀状态恢复。
func TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryContextEdgeInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	edgeInsertRequests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_context_edges") {
			edgeInsertRequests = append(edgeInsertRequests, req)
			if len(edgeInsertRequests) == 1 {
				return nil, errors.New("failed to commit memory context edge insert")
			}
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges"):
			requireSQLiteInt64Params(t, req.GetParams(), 201)
			if len(edgeInsertRequests) != 1 {
				t.Fatalf("expected first context edge insert before reconcile read, got %d", len(edgeInsertRequests))
			}
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryContextEdgeRowsJSONFromInsertRequests(t, edgeInsertRequests...)}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	result, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:    "vec-new-turn",
				Vector:      []float32{0.25, 0.75},
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:    "project status",
				Details:     "project status",
				SourceKind:  logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelStable,
				ContextEdges: []logicdomain.MemoryContextEdgeCandidate{{
					ContextKey:   "deployment mode",
					ContextValue: "local oss",
					Relation:     logicdomain.MemoryContextRelationSupport,
				}, {
					ContextKey:   "phase",
					ContextValue: "analysis",
					Relation:     logicdomain.MemoryContextRelationRebuttal,
				}},
			}},
		},
	)
	if err != nil {
		t.Fatalf("ApplyTurnAnalysis returned error: %v", err)
	}
	if len(result.InsertedMemoryNodes) != 1 || result.InsertedMemoryNodes[0].ID != 201 {
		t.Fatalf("unexpected recovered context insert result: %+v", result.InsertedMemoryNodes)
	}
	if len(edgeInsertRequests) != 2 {
		t.Fatalf("expected both context edge inserts to run after reconciliation, got %d", len(edgeInsertRequests))
	}
	if len(requests) != 5 {
		t.Fatalf("expected turn update, memory insert, cleanup, and two context inserts, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisKeepsUnmatchedMemoryContextEdgeInsertCommitUnknownFresh verifies unresolved edge insert ambiguity keeps fresh vectors protected.
// TestStoreApplyTurnAnalysisKeepsUnmatchedMemoryContextEdgeInsertCommitUnknownFresh 用于验证情境边插入无法对账时，会保留新向量已被长期 memory 行引用的不确定状态。
func TestStoreApplyTurnAnalysisKeepsUnmatchedMemoryContextEdgeInsertCommitUnknownFresh(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_context_edges") {
			return nil, errors.New("failed to commit memory context edge insert")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges"):
			requireSQLiteInt64Params(t, req.GetParams(), 201)
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:    "vec-new-turn",
				Vector:      []float32{0.25, 0.75},
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:    "project status",
				Details:     "project status",
				SourceKind:  logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelStable,
				ContextEdges: []logicdomain.MemoryContextEdgeCandidate{{
					ContextKey:   "deployment mode",
					ContextValue: "local oss",
					Relation:     logicdomain.MemoryContextRelationSupport,
				}},
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain context edge insert error, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected unresolved context edge insert to protect fresh vector reference, got %v", err)
	}
	if !strings.Contains(err.Error(), "apply turn analysis") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected context edge insert commit-unknown error: %v", err)
	}
	if len(requests) != 4 {
		t.Fatalf("expected turn update, memory insert, cleanup, and context insert before unresolved commit unknown, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemorySupersede verifies uncertain memory supersede updates recover before FTS cleanup runs.
// TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemorySupersede 用于验证 memory supersede 提交未知时，如果长期行已达标即可恢复并继续执行 FTS 清理。
func TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemorySupersede(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	var supersedeRequest *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.HasPrefix(sqlText, "UPDATE vmm_memory_nodes") && strings.Contains(sqlText, "memory_status = ?") {
			supersedeRequest = req
			return nil, errors.New("failed to commit memory supersede")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "SELECT vector_id") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"vector_id":"vec-old-77"},{"vector_id":"vec-old-78"}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?,?)"):
			if supersedeRequest == nil {
				t.Fatal("expected memory supersede request before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 77, 78)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeStatusRowsJSON(logicdomain.MemoryStatusSuperseded, supersedeRequest.GetParams()[1].Int64, 77, 78)}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	deletedFTSIDs := []string{}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	result, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:           "vec-new-turn",
				Vector:             []float32{0.25, 0.75},
				Category:           logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:           "project status",
				Details:            "project status",
				SourceKind:         logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:         logicdomain.MemoryScopeLevelProject,
				Priority:           logicdomain.MemoryPriorityP1,
				MemoryLevel:        logicdomain.MemoryLevelStable,
				SupersedeMemoryIDs: []uint64{78, 0, 77, 78},
			}},
		},
	)
	if err != nil {
		t.Fatalf("ApplyTurnAnalysis returned error: %v", err)
	}
	if len(result.SupersededVectorIDs) != 2 || result.SupersededVectorIDs[0] != "vec-old-77" || result.SupersededVectorIDs[1] != "vec-old-78" {
		t.Fatalf("unexpected superseded vector ids: %+v", result.SupersededVectorIDs)
	}
	if len(deletedFTSIDs) != 2 || deletedFTSIDs[0] != "77" || deletedFTSIDs[1] != "78" {
		t.Fatalf("expected recovered supersede to continue FTS cleanup, got %+v", deletedFTSIDs)
	}
	if len(requests) != 4 {
		t.Fatalf("expected turn update, memory insert, context cleanup, and memory supersede, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisKeepsUnmatchedMemorySupersedeCommitUnknownFresh verifies unresolved memory supersede ambiguity keeps fresh vectors protected.
// TestStoreApplyTurnAnalysisKeepsUnmatchedMemorySupersedeCommitUnknownFresh 用于验证 memory supersede 无法对账时，会保留新向量已被长期 memory 行引用的不确定状态。
func TestStoreApplyTurnAnalysisKeepsUnmatchedMemorySupersedeCommitUnknownFresh(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	var supersedeRequest *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.HasPrefix(sqlText, "UPDATE vmm_memory_nodes") && strings.Contains(sqlText, "memory_status = ?") {
			supersedeRequest = req
			return nil, errors.New("failed to commit memory supersede")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "SELECT vector_id") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"vector_id":"vec-old-77"}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?)"):
			if supersedeRequest == nil {
				t.Fatal("expected memory supersede request before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 77)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeStatusRowsJSON(logicdomain.MemoryStatusActive, supersedeRequest.GetParams()[1].Int64, 77)}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	deletedFTSIDs := []string{}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:           "vec-new-turn",
				Vector:             []float32{0.25, 0.75},
				Category:           logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:           "project status",
				Details:            "project status",
				SourceKind:         logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:         logicdomain.MemoryScopeLevelProject,
				Priority:           logicdomain.MemoryPriorityP1,
				MemoryLevel:        logicdomain.MemoryLevelStable,
				SupersedeMemoryIDs: []uint64{77},
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain memory supersede error, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected unresolved memory supersede to protect fresh vector reference, got %v", err)
	}
	if !strings.Contains(err.Error(), "supersede turn analysis memory nodes") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected memory supersede commit-unknown error: %v", err)
	}
	if len(deletedFTSIDs) != 0 {
		t.Fatalf("did not expect FTS cleanup after unresolved supersede, got %+v", deletedFTSIDs)
	}
	if len(requests) != 4 {
		t.Fatalf("expected turn update, memory insert, context cleanup, and memory supersede before unresolved commit unknown, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisMarksProfileInsertZeroRowsUncertain verifies no-op profile inserts do not silently pass after the turn is marked extracted.
// TestStoreApplyTurnAnalysisMarksProfileInsertZeroRowsUncertain 用于验证 profile insert 未影响行时，不会在 turn 已标记提炼后静默通过。
func TestStoreApplyTurnAnalysisMarksProfileInsertZeroRowsUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_profile_nodes") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_profile_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			ProfileNodes: []logicdomain.ProfileNodeCandidate{{
				ProfileType:   logicdomain.ProfileTypeUser,
				Content:       "profile content",
				Status:        logicdomain.ProfileStatusActive,
				Priority:      logicdomain.ProfilePriorityP1,
				ProfileLevel:  logicdomain.ProfileLevelStable,
				RefreshWeight: 16,
				ProfileDate:   "2026-07-05",
				SourceKind:    logicdomain.ProfileSourceKindTurnExtract,
				SourceID:      44,
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected zero-row profile insert to be outcome-uncertain after turn update, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect fresh vector reference for profile-only insert failure, got %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected turn update and profile insert before failure, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisReconcilesCommitUnknownProfileInsert verifies profile-node inserts can recover from commit ambiguity when the durable row matches.
// TestStoreApplyTurnAnalysisReconcilesCommitUnknownProfileInsert 用于验证画像节点插入提交未知时，如果长期行匹配即可恢复。
func TestStoreApplyTurnAnalysisReconcilesCommitUnknownProfileInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	var profileInsert *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_profile_nodes") {
			profileInsert = req
			return nil, errors.New("failed to commit profile insert")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_profile_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "FROM vmm_profile_nodes AS n") && strings.Contains(sql, "WHERE n.id = ?"):
			if profileInsert == nil {
				t.Fatal("expected profile insert params before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 301)
			return &fakeQueryJSONResponse{JsonData: sqliteProfileNodeRowJSONFromInsertParams(t, profileInsert.GetParams())}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			ProfileNodes: []logicdomain.ProfileNodeCandidate{{
				ProfileType:   logicdomain.ProfileTypeUser,
				Content:       "profile content",
				Status:        logicdomain.ProfileStatusActive,
				Priority:      logicdomain.ProfilePriorityP1,
				ProfileLevel:  logicdomain.ProfileLevelStable,
				LevelReason:   "stable preference",
				RefreshWeight: 16,
				ProfileDate:   "2026-07-05",
				SourceKind:    logicdomain.ProfileSourceKindTurnExtract,
				SourceID:      44,
				StatusReason:  "accepted",
			}},
		},
	)
	if err != nil {
		t.Fatalf("ApplyTurnAnalysis returned error: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected turn update and profile insert, got %d requests", len(requests))
	}
	if profileInsert == nil {
		t.Fatal("expected profile insert request")
	}
}

// TestStoreApplyTurnAnalysisKeepsUnmatchedProfileInsertCommitUnknownUncertain verifies unresolved profile insert ambiguity stays visible without inventing vector references.
// TestStoreApplyTurnAnalysisKeepsUnmatchedProfileInsertCommitUnknownUncertain 用于验证无法对账的画像插入提交未知会保持可见，同时不虚构向量引用。
func TestStoreApplyTurnAnalysisKeepsUnmatchedProfileInsertCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_profile_nodes") {
			return nil, errors.New("failed to commit profile insert")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_profile_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "FROM vmm_profile_nodes AS n") && strings.Contains(sql, "WHERE n.id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			ProfileNodes: []logicdomain.ProfileNodeCandidate{{
				ProfileType:   logicdomain.ProfileTypeUser,
				Content:       "profile content",
				Status:        logicdomain.ProfileStatusActive,
				Priority:      logicdomain.ProfilePriorityP1,
				ProfileLevel:  logicdomain.ProfileLevelStable,
				RefreshWeight: 16,
				ProfileDate:   "2026-07-05",
				SourceKind:    logicdomain.ProfileSourceKindTurnExtract,
				SourceID:      44,
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain profile insert error, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect profile-only insert to invent fresh vector reference, got %v", err)
	}
	if !strings.Contains(err.Error(), "apply turn analysis") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected profile insert commit-unknown error: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected turn update and profile insert before unresolved commit unknown, got %d", len(requests))
	}
}

// TestStoreApplyTurnAnalysisMarksProfileSupersedeZeroRowsUncertain verifies profile supersede drift is surfaced after the replacement node exists.
// TestStoreApplyTurnAnalysisMarksProfileSupersedeZeroRowsUncertain 用于验证替代节点已落库后的画像 supersede 漂移会被明确上报。
func TestStoreApplyTurnAnalysisMarksProfileSupersedeZeroRowsUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.HasPrefix(sqlText, "UPDATE vmm_profile_nodes") && strings.Contains(sqlText, "superseded_by_id") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_profile_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			ProfileNodes: []logicdomain.ProfileNodeCandidate{{
				ProfileType:      logicdomain.ProfileTypeUser,
				Content:          "profile content",
				Status:           logicdomain.ProfileStatusActive,
				Priority:         logicdomain.ProfilePriorityP1,
				ProfileLevel:     logicdomain.ProfileLevelStable,
				RefreshWeight:    16,
				ProfileDate:      "2026-07-05",
				SourceKind:       logicdomain.ProfileSourceKindTurnExtract,
				SourceID:         44,
				StatusReason:     "accepted replacement",
				SupersedeNodeIDs: []uint64{12},
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected profile supersede drift to be outcome-uncertain, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect fresh vector reference for profile-only supersede failure, got %v", err)
	}
	if len(requests) != 3 {
		t.Fatalf("expected turn update, profile insert, and profile supersede before failure, got %d requests", len(requests))
	}
	if !strings.Contains(requests[len(requests)-1].GetSql(), "WHERE profile_status = ? AND profile_type = ? AND bind_id = ? AND id IN (?)") {
		t.Fatalf("expected guarded profile supersede as final request, got %q", requests[len(requests)-1].GetSql())
	}
}

// TestStoreApplyTurnAnalysisReconcilesCommitUnknownProfileSupersede verifies uncertain profile supersede updates recover when all retired nodes reached the replacement id.
// TestStoreApplyTurnAnalysisReconcilesCommitUnknownProfileSupersede 用于验证画像替代提交未知时，如果所有旧节点都指向替代节点即可恢复。
func TestStoreApplyTurnAnalysisReconcilesCommitUnknownProfileSupersede(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.HasPrefix(sqlText, "UPDATE vmm_profile_nodes") && strings.Contains(sqlText, "superseded_by_id") {
			return nil, errors.New("failed to commit profile supersede")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_profile_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "SELECT id, profile_status, superseded_by_id") && strings.Contains(sql, "FROM vmm_profile_nodes"):
			requireSQLiteInt64Params(t, req.GetParams(), 12, 13)
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"id":12,"profile_status":%d,"superseded_by_id":301},{"id":13,"profile_status":%d,"superseded_by_id":301}]`, logicdomain.ProfileStatusSuperseded, logicdomain.ProfileStatusSuperseded)}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			ProfileNodes: []logicdomain.ProfileNodeCandidate{{
				ProfileType:      logicdomain.ProfileTypeUser,
				Content:          "profile content",
				Status:           logicdomain.ProfileStatusActive,
				Priority:         logicdomain.ProfilePriorityP1,
				ProfileLevel:     logicdomain.ProfileLevelStable,
				RefreshWeight:    16,
				ProfileDate:      "2026-07-05",
				SourceKind:       logicdomain.ProfileSourceKindTurnExtract,
				SourceID:         44,
				StatusReason:     "accepted replacement",
				SupersedeNodeIDs: []uint64{12, 13},
			}},
		},
	)
	if err != nil {
		t.Fatalf("ApplyTurnAnalysis returned error: %v", err)
	}
	if len(requests) != 3 {
		t.Fatalf("expected turn update, profile insert, and profile supersede, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisKeepsUnmatchedProfileSupersedeCommitUnknownUncertain verifies unresolved profile supersede ambiguity remains visible.
// TestStoreApplyTurnAnalysisKeepsUnmatchedProfileSupersedeCommitUnknownUncertain 用于验证无法对账的画像替代提交未知仍保持结果不确定。
func TestStoreApplyTurnAnalysisKeepsUnmatchedProfileSupersedeCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.HasPrefix(sqlText, "UPDATE vmm_profile_nodes") && strings.Contains(sqlText, "superseded_by_id") {
			return nil, errors.New("failed to commit profile supersede")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_profile_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "SELECT id, profile_status, superseded_by_id") && strings.Contains(sql, "FROM vmm_profile_nodes"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"id":12,"profile_status":%d,"superseded_by_id":0}]`, logicdomain.ProfileStatusActive)}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			ProfileNodes: []logicdomain.ProfileNodeCandidate{{
				ProfileType:      logicdomain.ProfileTypeUser,
				Content:          "profile content",
				Status:           logicdomain.ProfileStatusActive,
				Priority:         logicdomain.ProfilePriorityP1,
				ProfileLevel:     logicdomain.ProfileLevelStable,
				RefreshWeight:    16,
				ProfileDate:      "2026-07-05",
				SourceKind:       logicdomain.ProfileSourceKindTurnExtract,
				SourceID:         44,
				StatusReason:     "accepted replacement",
				SupersedeNodeIDs: []uint64{12, 13},
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain profile supersede error, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect profile-only supersede to invent fresh vector reference, got %v", err)
	}
	if !strings.Contains(err.Error(), "apply turn analysis") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected profile supersede commit-unknown error: %v", err)
	}
	if len(requests) != 3 {
		t.Fatalf("expected turn update, profile insert, and profile supersede before unresolved commit unknown, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisRejectsMissingTurnBeforeMemoryInsert verifies a stale turn target stops the post-action write before fresh memory rows can reference it.
// TestStoreApplyTurnAnalysisRejectsMissingTurnBeforeMemoryInsert 用于验证过期 turn 目标会在新记忆行引用它之前阻断 post-action 写入。
func TestStoreApplyTurnAnalysisRejectsMissingTurnBeforeMemoryInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "UPDATE vmm_turn_records") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:    "vec-new-turn",
				Vector:      []float32{0.25, 0.75},
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:    "project status",
				Details:     "project status",
				SourceKind:  logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelStable,
			}},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "update turn analysis") {
		t.Fatalf("expected missing turn update error, got %v", err)
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected missing turn to remain a clean failure before memory insert, got %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected only the turn update request before failure, got %d requests", len(requests))
	}
	if strings.Contains(sqliteExecutedSQLLog(requests), "INSERT INTO vmm_memory_nodes") {
		t.Fatalf("did not expect memory insert after missing turn, got %q", sqliteExecutedSQLLog(requests))
	}
}

// TestStoreApplyTurnAnalysisMarksSupersedeDriftUncertain verifies post-action memory replacement drift is surfaced after new durable rows may already exist.
// TestStoreApplyTurnAnalysisMarksSupersedeDriftUncertain 用于验证 post-action 记忆替代目标漂移时，会在新持久化行可能已存在后按结果不确定上报。
func TestStoreApplyTurnAnalysisMarksSupersedeDriftUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "id IN (?,?)") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "SELECT vector_id") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"vector_id":"vec-old-turn-1"},{"vector_id":"vec-old-turn-2"}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:           "vec-new-turn",
				Vector:             []float32{0.25, 0.75},
				Category:           logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:           "project status",
				Details:            "project status",
				SupersedeMemoryIDs: []uint64{77, 78},
				SourceKind:         logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:         logicdomain.MemoryScopeLevelProject,
				Priority:           logicdomain.MemoryPriorityP1,
				MemoryLevel:        logicdomain.MemoryLevelStable,
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected turn-analysis supersede drift to be outcome-uncertain, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected supersede drift to retain fresh vector references, got %v", err)
	}
	if len(requests) != 4 {
		t.Fatalf("expected turn update, memory insert, edge cleanup, and supersede update before failure, got %d requests", len(requests))
	}
	if !strings.Contains(sqliteExecutedSQLLog(requests), "INSERT INTO vmm_memory_nodes") {
		t.Fatalf("expected memory insert before uncertain supersede failure, got %q", sqliteExecutedSQLLog(requests))
	}
	if !strings.Contains(requests[len(requests)-1].GetSql(), "id IN (?,?)") {
		t.Fatalf("expected final request to be supersede update, got %q", requests[len(requests)-1].GetSql())
	}
}

// TestStoreApplyTurnAnalysisMarksFTSFailureUncertain verifies post-action FTS sync failures are unsafe to treat as clean failures after durable memory writes.
// TestStoreApplyTurnAnalysisMarksFTSFailureUncertain 用于验证 post-action 持久化记忆写入后，FTS 同步失败不能再被当作干净失败处理。
func TestStoreApplyTurnAnalysisMarksFTSFailureUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.upsertFtsDocumentFunc = func(string, sqliteffi.TokenizerMode, string, string, string, string) (sqliteffi.FtsMutationResult, error) {
		return sqliteffi.FtsMutationResult{}, fmt.Errorf("fts unavailable")
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:    "vec-new-turn",
				Vector:      []float32{0.25, 0.75},
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:    "project status",
				Details:     "project status",
				SourceKind:  logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelStable,
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected turn-analysis fts failure to be outcome-uncertain, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected fts failure to retain fresh vector references, got %v", err)
	}
}

// TestStoreCreateDirectMemoryNodeUsesTypedSQLiteParamsForMemoryTextWrites verifies direct-write fallback inserts keep request text out of executable SQL.
// TestStoreCreateDirectMemoryNodeUsesTypedSQLiteParamsForMemoryTextWrites 用于验证主动写 fallback 插入会把请求文本隔离在可执行 SQL 之外。
func TestStoreCreateDirectMemoryNodeUsesTypedSQLiteParamsForMemoryTextWrites(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	// Capture relational writes so the assertion can inspect the actual SQLite execution boundary.
	// 捕获关系写入请求，让断言能够检查真实的 SQLite 执行边界。
	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "id IN (?,?)") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":211}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	// Use SQL-shaped text from the transport-facing fields so the test fails if raw interpolation returns.
	// 使用来自传输侧字段的 SQL 形状文本，确保原始字符串插值一旦回归就会触发测试失败。
	vectorID := "  vec-direct '; DROP TABLE vmm_memory_nodes; --  "
	memoryAbstract := "  direct abstract '; DROP TABLE vmm_memory_nodes; --  "
	memoryDetails := "  direct details '; DROP TABLE vmm_memory_nodes; --  "
	statusReason := "  direct reason '; DROP TABLE vmm_memory_nodes; --  "
	dedupeHash := "  direct hash '; DROP TABLE vmm_memory_nodes; --  "
	createdAt := time.Date(2026, 7, 5, 10, 15, 0, 0, time.UTC)
	created, err := store.CreateDirectMemoryNode(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:      vectorID,
			Vector:        []float32{0.125, 0.875},
			SourceKind:    logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:    logicdomain.MemoryScopeLevelProject,
			Category:      logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:      memoryAbstract,
			Details:       memoryDetails,
			Status:        logicdomain.MemoryStatusActive,
			Priority:      logicdomain.MemoryPriorityP1,
			MemoryLevel:   logicdomain.MemoryLevelStable,
			RefreshWeight: 2,
			StatusReason:  statusReason,
			DedupeHash:    dedupeHash,
			CreatedAt:     createdAt,
			UpdatedAt:     createdAt,
		},
	)
	if err != nil {
		t.Fatalf("CreateDirectMemoryNode returned error: %v", err)
	}
	if created.ID != 211 || created.VectorID != strings.TrimSpace(vectorID) || created.Abstract != strings.TrimSpace(memoryAbstract) {
		t.Fatalf("unexpected created direct memory row: %+v", created)
	}
	if len(requests) != 1 {
		t.Fatalf("expected one typed insert request, got %d", len(requests))
	}

	capturedSQL := sqliteExecutedSQLLog(requests)
	for _, leaked := range []string{"DROP TABLE", "vec-direct", "direct abstract", "direct details", "direct reason", "direct hash"} {
		if strings.Contains(capturedSQL, leaked) {
			t.Fatalf("expected direct memory text %q to stay out of SQL text, got %q", leaked, capturedSQL)
		}
	}
	if strings.TrimSpace(requests[0].GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", requests[0].GetParamsJson())
	}
	for _, value := range []string{
		strings.TrimSpace(vectorID),
		"[0.125,0.875]",
		strings.TrimSpace(memoryAbstract),
		strings.TrimSpace(memoryDetails),
		strings.TrimSpace(statusReason),
		strings.TrimSpace(dedupeHash),
	} {
		if got := countSQLiteStringParam(requests, value); got != 1 {
			t.Fatalf("expected string param %q exactly once, got %d", value, got)
		}
	}
}

// TestStoreCreateDirectMemoryNodeRejectsZeroRowInsertBeforeFreshVectorReference verifies fallback direct-write insert metadata is checked before FTS sync.
// TestStoreCreateDirectMemoryNodeRejectsZeroRowInsertBeforeFreshVectorReference 用于验证 fallback 主动写会在 FTS 同步前检查 insert 影响行数。
func TestStoreCreateDirectMemoryNodeRejectsZeroRowInsertBeforeFreshVectorReference(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":211}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.CreateDirectMemoryNode(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-direct",
			Vector:      []float32{0.125, 0.875},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "direct abstract",
			Details:     "direct details",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "insert direct memory node") {
		t.Fatalf("expected zero-row fallback insert error, got %v", err)
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected zero-row fallback insert to remain a clean failure, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect fresh vector reference before fallback insert affects a row, got %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected one fallback insert request before failure, got %d", len(requests))
	}
}

// TestStoreCreateDirectMemoryNodeReconcilesCommitUnknownInsert verifies fallback direct writes recover when the durable row already matches the insert.
// TestStoreCreateDirectMemoryNodeReconcilesCommitUnknownInsert 用于验证 fallback 主动写插入提交未知时，如果长期行已匹配插入目标即可恢复。
func TestStoreCreateDirectMemoryNodeReconcilesCommitUnknownInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var insertRequest *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_nodes") {
			insertRequest = req
			return nil, errors.New("failed to commit direct memory insert")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":211}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?)"):
			if insertRequest == nil {
				t.Fatal("expected direct memory insert request before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 211)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeRowJSONFromInsertParams(t, insertRequest.GetParams())}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	created, err := store.CreateDirectMemoryNode(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-direct",
			Vector:      []float32{0.125, 0.875},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "direct abstract",
			Details:     "direct details",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
	)
	if err != nil {
		t.Fatalf("CreateDirectMemoryNode returned error: %v", err)
	}
	if created.ID != 211 || created.VectorID != "vec-direct" {
		t.Fatalf("unexpected recovered direct memory row: %+v", created)
	}
}

// TestStoreCreateDirectMemoryNodeKeepsUnmatchedCommitUnknownFresh verifies unresolved fallback inserts keep the already-written vector protected.
// TestStoreCreateDirectMemoryNodeKeepsUnmatchedCommitUnknownFresh 用于验证 fallback 插入无法对账时，会保留已经写入的新向量保护。
func TestStoreCreateDirectMemoryNodeKeepsUnmatchedCommitUnknownFresh(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_nodes") {
			return nil, errors.New("failed to commit direct memory insert")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":211}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?)"):
			requireSQLiteInt64Params(t, req.GetParams(), 211)
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.CreateDirectMemoryNode(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-direct",
			Vector:      []float32{0.125, 0.875},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "direct abstract",
			Details:     "direct details",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain fallback insert error, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected unresolved fallback insert to protect fresh vector reference, got %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected only direct memory insert before unresolved commit unknown, got %d requests", len(requests))
	}
}

// TestStoreCreateDirectMemoryNodeMarksFTSFailureFreshVectorReferenceUncertain verifies fallback FTS failures retain the vector once the memory row exists.
// TestStoreCreateDirectMemoryNodeMarksFTSFailureFreshVectorReferenceUncertain 用于验证 fallback 记忆行已存在后，FTS 失败会保留新向量引用语义。
func TestStoreCreateDirectMemoryNodeMarksFTSFailureFreshVectorReferenceUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":211}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.upsertFtsDocumentFunc = func(string, sqliteffi.TokenizerMode, string, string, string, string) (sqliteffi.FtsMutationResult, error) {
		return sqliteffi.FtsMutationResult{}, fmt.Errorf("fts unavailable")
	}

	_, err := store.CreateDirectMemoryNode(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-direct",
			Vector:      []float32{0.125, 0.875},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "direct abstract",
			Details:     "direct details",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected fallback fts failure to be outcome-uncertain, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected fallback fts failure to retain fresh vector reference, got %v", err)
	}
}

// TestStoreApplyDirectMemoryWriteUsesTypedSQLiteParamsForInsertAndSupersede verifies direct-write replacement keeps typed params without unsupported cross-call transaction control.
// TestStoreApplyDirectMemoryWriteUsesTypedSQLiteParamsForInsertAndSupersede 用于验证主动写替代路径继续使用强类型参数，并且不依赖不受支持的跨调用事务控制。
func TestStoreApplyDirectMemoryWriteUsesTypedSQLiteParamsForInsertAndSupersede(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	// Capture every write statement because the direct-write applier splits one logical write into typed single statements.
	// 捕获每条写入语句，因为主动写 applier 会把一次逻辑写入拆成强类型单语句执行。
	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "id IN (?,?)") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "SELECT vector_id") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"vector_id":"vec-old-direct"}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	// Use duplicate and zero supersede ids to verify the status-update SQL binds the normalized numeric id list.
	// 使用重复和零值替代 id，验证状态更新 SQL 会绑定规范化后的数字 id 列表。
	vectorID := "  vec-apply '; DROP TABLE vmm_memory_nodes; --  "
	memoryAbstract := "  apply abstract '; DROP TABLE vmm_memory_nodes; --  "
	memoryDetails := "  apply details '; DROP TABLE vmm_memory_nodes; --  "
	statusReason := "  apply reason '; DROP TABLE vmm_memory_nodes; --  "
	dedupeHash := "  apply hash '; DROP TABLE vmm_memory_nodes; --  "
	createdAt := time.Date(2026, 7, 5, 11, 45, 0, 0, time.UTC)
	result, err := store.ApplyDirectMemoryWrite(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:      vectorID,
			Vector:        []float32{0.5, 0.75},
			SourceKind:    logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:    logicdomain.MemoryScopeLevelProject,
			Category:      logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:      memoryAbstract,
			Details:       memoryDetails,
			Status:        logicdomain.MemoryStatusActive,
			Priority:      logicdomain.MemoryPriorityP1,
			MemoryLevel:   logicdomain.MemoryLevelStable,
			RefreshWeight: 2,
			StatusReason:  statusReason,
			DedupeHash:    dedupeHash,
			CreatedAt:     createdAt,
			UpdatedAt:     createdAt,
		},
		[]uint64{77, 0, 77, 78},
	)
	if err != nil {
		t.Fatalf("ApplyDirectMemoryWrite returned error: %v", err)
	}
	if result.InsertedMemoryNode.ID != 301 || result.InsertedMemoryNode.VectorID != strings.TrimSpace(vectorID) {
		t.Fatalf("unexpected direct write apply result: %+v", result)
	}
	if len(result.SupersededVectorIDs) != 1 || result.SupersededVectorIDs[0] != "vec-old-direct" {
		t.Fatalf("unexpected superseded vector ids: %+v", result.SupersededVectorIDs)
	}
	if len(requests) != 2 {
		t.Fatalf("expected insert and supersede requests, got %d", len(requests))
	}
	requireNoSQLiteTransactionControl(t, requests)

	capturedSQL := sqliteExecutedSQLLog(requests)
	for _, leaked := range []string{"DROP TABLE", "vec-apply", "apply abstract", "apply details", "apply reason", "apply hash"} {
		if strings.Contains(capturedSQL, leaked) {
			t.Fatalf("expected direct memory text %q to stay out of SQL text, got %q", leaked, capturedSQL)
		}
	}
	if !strings.Contains(requests[1].GetSql(), "id IN (?,?)") {
		t.Fatalf("expected typed supersede id placeholders, got %q", requests[1].GetSql())
	}
	if strings.Contains(requests[1].GetSql(), "77") || strings.Contains(requests[1].GetSql(), "78") {
		t.Fatalf("expected supersede memory ids to stay out of SQL text, got %q", requests[1].GetSql())
	}
	supersedeParams := requests[1].GetParams()
	if len(supersedeParams) != 5 {
		t.Fatalf("expected five supersede params, got %d: %#v", len(supersedeParams), supersedeParams)
	}
	requireSQLiteInt64Params(t, []sqliteffi.SQLValue{supersedeParams[0]}, int64(logicdomain.MemoryStatusSuperseded))
	if supersedeParams[1].Kind != sqliteffi.SQLValueInt64 || supersedeParams[1].Int64 <= 0 {
		t.Fatalf("expected positive supersede updated timestamp param, got %#v", supersedeParams[1])
	}
	requireSQLiteInt64Params(t, supersedeParams[2:], int64(logicdomain.MemoryStatusActive), 77, 78)
	for _, request := range requests {
		if strings.TrimSpace(request.GetParamsJson()) != "" {
			t.Fatalf("expected typed params only, got params_json=%q", request.GetParamsJson())
		}
	}
	for _, value := range []string{
		strings.TrimSpace(vectorID),
		"[0.5,0.75]",
		strings.TrimSpace(memoryAbstract),
		strings.TrimSpace(memoryDetails),
		strings.TrimSpace(statusReason),
		strings.TrimSpace(dedupeHash),
	} {
		if got := countSQLiteStringParam(requests, value); got != 1 {
			t.Fatalf("expected string param %q exactly once, got %d", value, got)
		}
	}
}

// TestStoreApplyDirectMemoryWriteMarksSupersedeDriftUncertain verifies stale replacement targets are reported as partial writes after the new memory row is inserted.
// TestStoreApplyDirectMemoryWriteMarksSupersedeDriftUncertain 用于验证新记忆行已经插入后，替代目标发生漂移会按部分写入上报。
func TestStoreApplyDirectMemoryWriteMarksSupersedeDriftUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "id IN (?,?)") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "SELECT vector_id") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"vector_id":"vec-old-direct-1"},{"vector_id":"vec-old-direct-2"}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.ApplyDirectMemoryWrite(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-new",
			Vector:      []float32{0.5, 0.75},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "project phase",
			Details:     "project phase",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
		[]uint64{77, 78},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected direct-write supersede drift to be outcome-uncertain, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected direct-write supersede drift to retain fresh vector reference, got %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected insert and supersede update before failure, got %d requests", len(requests))
	}
}

// TestStoreApplyDirectMemoryWriteKeepsUnmatchedInsertCommitUnknownFresh verifies unresolved fast-path inserts keep the already-written vector protected.
// TestStoreApplyDirectMemoryWriteKeepsUnmatchedInsertCommitUnknownFresh 用于验证 fast-path 插入无法对账时，会保留已经写入的新向量保护。
func TestStoreApplyDirectMemoryWriteKeepsUnmatchedInsertCommitUnknownFresh(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_nodes") {
			return nil, errors.New("failed to commit direct memory insert")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?)"):
			requireSQLiteInt64Params(t, req.GetParams(), 301)
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.ApplyDirectMemoryWrite(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-new",
			Vector:      []float32{0.5, 0.75},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "project phase",
			Details:     "project phase",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
		nil,
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain direct-write insert error, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected unresolved direct-write insert to protect fresh vector reference, got %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected only direct memory insert before unresolved commit unknown, got %d requests", len(requests))
	}
}

// TestStoreApplyDirectMemoryWriteReconcilesCommitUnknownSupersede verifies uncertain fast-path supersede updates recover before FTS cleanup.
// TestStoreApplyDirectMemoryWriteReconcilesCommitUnknownSupersede 用于验证 fast-path supersede 提交未知时，如果长期行已达标即可恢复并继续执行 FTS 清理。
func TestStoreApplyDirectMemoryWriteReconcilesCommitUnknownSupersede(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	var supersedeRequest *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.HasPrefix(sqlText, "UPDATE vmm_memory_nodes") && strings.Contains(sqlText, "memory_status = ?") {
			supersedeRequest = req
			return nil, errors.New("failed to commit direct memory supersede")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "SELECT vector_id") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"vector_id":"vec-old-77"},{"vector_id":"vec-old-78"}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?,?)"):
			if supersedeRequest == nil {
				t.Fatal("expected direct memory supersede request before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 77, 78)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeStatusRowsJSON(logicdomain.MemoryStatusSuperseded, supersedeRequest.GetParams()[1].Int64, 77, 78)}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	deletedFTSIDs := []string{}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	result, err := store.ApplyDirectMemoryWrite(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-new",
			Vector:      []float32{0.5, 0.75},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "project phase",
			Details:     "project phase",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
		[]uint64{78, 0, 77, 78},
	)
	if err != nil {
		t.Fatalf("ApplyDirectMemoryWrite returned error: %v", err)
	}
	if len(result.SupersededVectorIDs) != 2 || result.SupersededVectorIDs[0] != "vec-old-77" || result.SupersededVectorIDs[1] != "vec-old-78" {
		t.Fatalf("unexpected superseded vector ids: %+v", result.SupersededVectorIDs)
	}
	if len(deletedFTSIDs) != 2 || deletedFTSIDs[0] != "77" || deletedFTSIDs[1] != "78" {
		t.Fatalf("expected recovered direct supersede to continue FTS cleanup, got %+v", deletedFTSIDs)
	}
	if len(requests) != 2 {
		t.Fatalf("expected direct insert and supersede requests, got %d", len(requests))
	}
}

// TestStoreApplyDirectMemoryWriteKeepsUnmatchedSupersedeCommitUnknownFresh verifies unresolved fast-path supersede ambiguity keeps fresh vectors protected.
// TestStoreApplyDirectMemoryWriteKeepsUnmatchedSupersedeCommitUnknownFresh 用于验证 fast-path supersede 无法对账时，会保留新向量已被长期 memory 行引用的不确定状态。
func TestStoreApplyDirectMemoryWriteKeepsUnmatchedSupersedeCommitUnknownFresh(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	var supersedeRequest *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.HasPrefix(sqlText, "UPDATE vmm_memory_nodes") && strings.Contains(sqlText, "memory_status = ?") {
			supersedeRequest = req
			return nil, errors.New("failed to commit direct memory supersede")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "SELECT vector_id") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"vector_id":"vec-old-77"}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?)"):
			if supersedeRequest == nil {
				t.Fatal("expected direct memory supersede request before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 77)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeStatusRowsJSON(logicdomain.MemoryStatusActive, supersedeRequest.GetParams()[1].Int64, 77)}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	deletedFTSIDs := []string{}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	_, err := store.ApplyDirectMemoryWrite(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-new",
			Vector:      []float32{0.5, 0.75},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "project phase",
			Details:     "project phase",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
		[]uint64{77},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain direct-write supersede error, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected unresolved direct-write supersede to protect fresh vector reference, got %v", err)
	}
	if !strings.Contains(err.Error(), "supersede direct memory nodes") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected direct-write supersede commit-unknown error: %v", err)
	}
	if len(deletedFTSIDs) != 0 {
		t.Fatalf("did not expect FTS cleanup after unresolved direct supersede, got %+v", deletedFTSIDs)
	}
	if len(requests) != 2 {
		t.Fatalf("expected direct insert and supersede before unresolved commit unknown, got %d requests", len(requests))
	}
}

// TestStoreApplyDirectMemoryWriteRejectsZeroRowInsertBeforeFreshVectorReference verifies a no-op first insert remains a clean direct-write failure.
// TestStoreApplyDirectMemoryWriteRejectsZeroRowInsertBeforeFreshVectorReference 用于验证首个 insert 未影响行时，主动写仍保持为尚未引用新向量的干净失败。
func TestStoreApplyDirectMemoryWriteRejectsZeroRowInsertBeforeFreshVectorReference(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_nodes") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ApplyDirectMemoryWrite(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-new",
			Vector:      []float32{0.5, 0.75},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "project phase",
			Details:     "project phase",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "insert direct memory node") {
		t.Fatalf("expected zero-row direct insert error, got %v", err)
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected zero-row first insert to remain a clean failure, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect fresh vector reference before direct insert affects a row, got %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected only direct memory insert before failure, got %d requests", len(requests))
	}
}

// TestStoreApplyManualProfileInstructionUsesTypedSQLiteParamsForNodeTextWrites verifies manual profile node text and reviewer reasons stay out of executable SQL.
// TestStoreApplyManualProfileInstructionUsesTypedSQLiteParamsForNodeTextWrites 用于验证手工画像节点文本与评审原因不会进入可执行 SQL。
func TestStoreApplyManualProfileInstructionUsesTypedSQLiteParamsForNodeTextWrites(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "id IN (?,?)") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_profile_nodes") && strings.Contains(req.GetSql(), "MAX(id)") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":101}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	content := "  manual node '; DROP TABLE vmm_profile_nodes; --  "
	levelReason := "  level reason '; DROP TABLE vmm_profile_nodes; --  "
	statusReason := "  supersede reason '; DROP TABLE vmm_profile_nodes; --  "
	retireReason := "  retire reason '; DROP TABLE vmm_profile_nodes; --  "
	renderedProfile := "rendered profile after node writes"
	reviewResult := `{"accepted_nodes":[{"index":0}]}`
	result, err := store.ApplyManualProfileInstruction(
		context.Background(),
		logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
		logicdomain.ProfileInstructionRecord{ID: 55},
		[]logicdomain.ProfileNodeCandidate{{
			Content:          content,
			Status:           logicdomain.ProfileStatusActive,
			Priority:         logicdomain.ProfilePriorityP1,
			ProfileLevel:     logicdomain.ProfileLevelStable,
			LevelReason:      levelReason,
			RefreshWeight:    64,
			SourceKind:       logicdomain.ProfileSourceKindManualInstruction,
			SourceID:         55,
			StatusReason:     statusReason,
			ProfileDate:      "2026-07-05",
			SupersedeNodeIDs: []uint64{9, 0, 7, 9},
		}},
		[]logicdomain.ProfileRetireDecision{
			{NodeID: 7, Reason: statusReason},
			{NodeID: 9, Reason: statusReason},
			{NodeID: 12, Reason: retireReason},
		},
		renderedProfile,
		reviewResult,
	)
	if err != nil {
		t.Fatalf("ApplyManualProfileInstruction returned error: %v", err)
	}
	if result.InstructionID != 55 || len(result.AcceptedNodes) != 1 || result.AcceptedNodes[0].ID != 101 {
		t.Fatalf("unexpected manual profile apply result: %+v", result)
	}
	if len(requests) != 5 {
		t.Fatalf("expected insert, supersede, retire, rendered, and applied requests, got %d", len(requests))
	}

	capturedSQL := sqliteExecutedSQLLog(requests)
	for _, leaked := range []string{"DROP TABLE", "manual node", "level reason", "supersede reason", "retire reason"} {
		if strings.Contains(capturedSQL, leaked) {
			t.Fatalf("expected profile node text %q to stay out of SQL text, got %q", leaked, capturedSQL)
		}
	}

	insertParams := requests[0].GetParams()
	if len(insertParams) != 17 {
		t.Fatalf("expected seventeen insert params, got %d: %#v", len(insertParams), insertParams)
	}
	if insertParams[1].Kind != sqliteffi.SQLValueNull {
		t.Fatalf("expected manual node turn id to be NULL, got %#v", insertParams[1])
	}
	if insertParams[4].Kind != sqliteffi.SQLValueString || insertParams[4].String != strings.TrimSpace(content) {
		t.Fatalf("expected content param, got %#v", insertParams[4])
	}
	if insertParams[8].Kind != sqliteffi.SQLValueString || insertParams[8].String != strings.TrimSpace(levelReason) {
		t.Fatalf("expected level reason param, got %#v", insertParams[8])
	}
	if insertParams[12].Kind != sqliteffi.SQLValueString || insertParams[12].String != strings.TrimSpace(statusReason) {
		t.Fatalf("expected status reason param, got %#v", insertParams[12])
	}

	supersedeParams := requests[1].GetParams()
	if !strings.Contains(requests[1].GetSql(), "WHERE profile_status = ? AND profile_type = ? AND bind_id = ? AND id IN (?,?)") {
		t.Fatalf("expected typed guarded supersede placeholders, got %q", requests[1].GetSql())
	}
	if strings.Contains(requests[1].GetSql(), "7") || strings.Contains(requests[1].GetSql(), "9") {
		t.Fatalf("expected supersede node ids to stay out of SQL text, got %q", requests[1].GetSql())
	}
	if len(supersedeParams) != 9 {
		t.Fatalf("expected nine supersede params, got %d: %#v", len(supersedeParams), supersedeParams)
	}
	if supersedeParams[1].Kind != sqliteffi.SQLValueInt64 || supersedeParams[1].Int64 != 101 {
		t.Fatalf("expected superseded_by_id param 101, got %#v", supersedeParams[1])
	}
	if supersedeParams[2].Kind != sqliteffi.SQLValueString || supersedeParams[2].String != strings.TrimSpace(statusReason) {
		t.Fatalf("expected supersede reason param, got %#v", supersedeParams[2])
	}
	requireSQLiteInt64Params(t, []sqliteffi.SQLValue{supersedeParams[0]}, int64(logicdomain.ProfileStatusSuperseded))
	if supersedeParams[3].Kind != sqliteffi.SQLValueInt64 || supersedeParams[3].Int64 <= 0 {
		t.Fatalf("expected supersede updated timestamp param, got %#v", supersedeParams[3])
	}
	requireSQLiteInt64Params(t, supersedeParams[4:], int64(logicdomain.ProfileStatusActive), int64(logicdomain.ProfileTypeProject), 9, 7, 9)

	retireParams := requests[2].GetParams()
	if len(retireParams) != 5 {
		t.Fatalf("expected five retire params, got %d: %#v", len(retireParams), retireParams)
	}
	if retireParams[1].Kind != sqliteffi.SQLValueString || retireParams[1].String != strings.TrimSpace(retireReason) {
		t.Fatalf("expected retire reason param, got %#v", retireParams[1])
	}
	if retireParams[4].Kind != sqliteffi.SQLValueInt64 || retireParams[4].Int64 != 12 {
		t.Fatalf("expected retire node id param, got %#v", retireParams[4])
	}
	if got := countSQLiteStringParam(requests, strings.TrimSpace(statusReason)); got != 2 {
		t.Fatalf("expected status reason to be bound twice, got %d", got)
	}
}

// TestStandaloneSQLiteProfileRetireDecisionsSkipsSuperseded verifies supersede decisions are not applied again by SQLite's standalone retire loop.
// TestStandaloneSQLiteProfileRetireDecisionsSkipsSuperseded 用于验证 supersede 决策不会被 SQLite 独立退役循环再次落库。
func TestStandaloneSQLiteProfileRetireDecisionsSkipsSuperseded(t *testing.T) {
	retired := []logicdomain.ProfileRetireDecision{
		{NodeID: 7, Reason: "replaced"},
		{NodeID: 12, Reason: "explicit retire"},
	}
	standalone := standaloneSQLiteProfileRetireDecisions(retired, map[uint64]struct{}{7: {}})
	if len(standalone) != 1 || standalone[0].NodeID != 12 {
		t.Fatalf("expected only standalone retire decision to remain, got %+v", standalone)
	}
}

// TestStoreConvergeExpiredProfileNodesUsesTypedSQLiteParamsForStatusReason verifies lifecycle expiry reasons and affected node ids are both bound as params.
// TestStoreConvergeExpiredProfileNodesUsesTypedSQLiteParamsForStatusReason 用于验证生命周期过期原因和受影响节点 id 都会作为参数绑定。
func TestStoreConvergeExpiredProfileNodesUsesTypedSQLiteParamsForStatusReason(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		switch {
		case strings.Contains(req.GetSql(), "expires_timestamp > 0"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{
				"id":41,
				"turn_id":null,
				"profile_type":%d,
				"bind_id":9,
				"content":"expired profile node",
				"profile_status":%d,
				"priority":%d,
				"profile_level":%d,
				"level_reason":"stable reason",
				"refresh_weight":32,
				"source_kind":%d,
				"source_id":77,
				"status_reason":"",
				"expires_timestamp":1,
				"superseded_by_id":0,
				"profile_date":"2026-07-05",
				"created_timestamp":1,
				"updated_timestamp":1
			}]`,
				logicdomain.ProfileTypeProject,
				logicdomain.ProfileStatusActive,
				logicdomain.ProfilePriorityP1,
				logicdomain.ProfileLevelStable,
				logicdomain.ProfileSourceKindManualInstruction,
			)}, nil
		case strings.Contains(req.GetSql(), "LEFT JOIN vmm_turn_records"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	snapshots, err := store.ConvergeExpiredProfileNodes(context.Background(), 1)
	if err != nil {
		t.Fatalf("ConvergeExpiredProfileNodes returned error: %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].ProfileType != logicdomain.ProfileTypeProject || snapshots[0].BindID != 9 {
		t.Fatalf("unexpected expired profile snapshots: %+v", snapshots)
	}
	if len(requests) != 1 {
		t.Fatalf("expected one expiry update request, got %d", len(requests))
	}
	if strings.Contains(requests[0].GetSql(), "expired by lifecycle convergence") {
		t.Fatalf("expected expiry reason to stay out of SQL text, got %q", requests[0].GetSql())
	}
	if !strings.Contains(requests[0].GetSql(), "id IN (?)") {
		t.Fatalf("expected typed expired id placeholder, got %q", requests[0].GetSql())
	}
	if strings.Contains(requests[0].GetSql(), "41") {
		t.Fatalf("expected expired node id to stay out of SQL text, got %q", requests[0].GetSql())
	}
	params := requests[0].GetParams()
	if len(params) != 5 {
		t.Fatalf("expected five expiry params, got %d: %#v", len(params), params)
	}
	if params[1].Kind != sqliteffi.SQLValueString || params[1].String != "expired by lifecycle convergence" {
		t.Fatalf("expected lifecycle reason string param, got %#v", params[1])
	}
	requireSQLiteInt64Params(t, []sqliteffi.SQLValue{params[0]}, int64(logicdomain.ProfileStatusExpired))
	if params[2].Kind != sqliteffi.SQLValueInt64 || params[2].Int64 <= 0 {
		t.Fatalf("expected expiry updated timestamp param, got %#v", params[2])
	}
	requireSQLiteInt64Params(t, params[3:], int64(logicdomain.ProfileStatusActive), 41)
}

// TestStoreConvergeExpiredProfileNodesMarksPartialDriftUncertain verifies partial expiry updates are not reported as clean convergence failures.
// TestStoreConvergeExpiredProfileNodesMarksPartialDriftUncertain 用于验证部分过期更新漂移不会被当作干净收敛失败上报。
func TestStoreConvergeExpiredProfileNodesMarksPartialDriftUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "expires_timestamp > 0") {
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[
{"id":41,"turn_id":null,"profile_type":%d,"bind_id":9,"content":"expired one","profile_status":%d,"priority":%d,"profile_level":%d,"level_reason":"stable reason","refresh_weight":32,"source_kind":%d,"source_id":77,"status_reason":"","expires_timestamp":1,"superseded_by_id":0,"profile_date":"2026-07-05","created_timestamp":1,"updated_timestamp":1},
{"id":42,"turn_id":null,"profile_type":%d,"bind_id":9,"content":"expired two","profile_status":%d,"priority":%d,"profile_level":%d,"level_reason":"stable reason","refresh_weight":32,"source_kind":%d,"source_id":78,"status_reason":"","expires_timestamp":1,"superseded_by_id":0,"profile_date":"2026-07-05","created_timestamp":2,"updated_timestamp":2}
]`,
				logicdomain.ProfileTypeProject,
				logicdomain.ProfileStatusActive,
				logicdomain.ProfilePriorityP1,
				logicdomain.ProfileLevelStable,
				logicdomain.ProfileSourceKindManualInstruction,
				logicdomain.ProfileTypeProject,
				logicdomain.ProfileStatusActive,
				logicdomain.ProfilePriorityP1,
				logicdomain.ProfileLevelStable,
				logicdomain.ProfileSourceKindManualInstruction,
			)}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	snapshots, err := store.ConvergeExpiredProfileNodes(context.Background(), 2)
	if err == nil {
		t.Fatal("expected partial expiry drift to fail")
	}
	if snapshots != nil {
		t.Fatalf("expected no snapshots after partial expiry drift, got %+v", snapshots)
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected partial expiry drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "expire sqlite profile nodes by lifecycle convergence affected 1 rows, want 2") {
		t.Fatalf("unexpected partial expiry drift error: %v", err)
	}
}

// TestStoreConvergeExpiredProfileNodesReconcilesCommitUnknownStatusFlip verifies lifecycle expiry can recover when the durable nodes reached the exact expired state.
// TestStoreConvergeExpiredProfileNodesReconcilesCommitUnknownStatusFlip 用于验证生命周期过期提交未知时，如果长期节点已经达到精确 expired 状态即可恢复。
func TestStoreConvergeExpiredProfileNodesReconcilesCommitUnknownStatusFlip(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return nil, errors.New("failed to commit sqlite profile expiry")
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "expires_timestamp > 0"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{
				"id":41,
				"turn_id":null,
				"profile_type":%d,
				"bind_id":9,
				"content":"expired one",
				"profile_status":%d,
				"priority":%d,
				"profile_level":%d,
				"level_reason":"stable reason",
				"refresh_weight":32,
				"source_kind":%d,
				"source_id":77,
				"status_reason":"",
				"expires_timestamp":1,
				"superseded_by_id":0,
				"profile_date":"2026-07-05",
				"created_timestamp":1,
				"updated_timestamp":1
			}]`,
				logicdomain.ProfileTypeProject,
				logicdomain.ProfileStatusActive,
				logicdomain.ProfilePriorityP1,
				logicdomain.ProfileLevelStable,
				logicdomain.ProfileSourceKindManualInstruction,
			)}, nil
		case strings.Contains(sql, "SELECT id, profile_status, superseded_by_id") && strings.Contains(sql, "FROM vmm_profile_nodes"):
			requireSQLiteInt64Params(t, req.GetParams(), 41)
			if captured == nil {
				t.Fatal("expected expiry update before reconcile read")
			}
			updatedMs := captured.GetParams()[2].Int64
			return &fakeQueryJSONResponse{JsonData: sqliteProfileNodeStatusRowsJSON(logicdomain.ProfileStatusExpired, "expired by lifecycle convergence", updatedMs, 41)}, nil
		case strings.Contains(sql, "n.profile_type = ?") && strings.Contains(sql, "n.bind_id = ?"):
			if captured == nil {
				t.Fatal("expected expiry update before active snapshot read")
			}
			updatedMs := captured.GetParams()[2].Int64
			requireSQLiteInt64Params(t, req.GetParams(), int64(logicdomain.ProfileTypeProject), 9, int64(logicdomain.ProfileStatusActive), updatedMs)
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{
				"id":43,
				"turn_id":null,
				"profile_type":%d,
				"bind_id":9,
				"content":"still active",
				"profile_status":%d,
				"priority":%d,
				"profile_level":%d,
				"level_reason":"stable reason",
				"refresh_weight":16,
				"source_kind":%d,
				"source_id":78,
				"status_reason":"",
				"expires_timestamp":0,
				"superseded_by_id":0,
				"profile_date":"2026-07-06",
				"created_timestamp":2,
				"updated_timestamp":2,
				"profile_date_anchor_timestamp":2
			}]`,
				logicdomain.ProfileTypeProject,
				logicdomain.ProfileStatusActive,
				logicdomain.ProfilePriorityP2,
				logicdomain.ProfileLevelStable,
				logicdomain.ProfileSourceKindManualInstruction,
			)}, nil
		default:
			t.Fatalf("unexpected profile expiry reconcile query: %q", sql)
			return nil, nil
		}
	}

	snapshots, err := store.ConvergeExpiredProfileNodes(context.Background(), 1)
	if err != nil {
		t.Fatalf("ConvergeExpiredProfileNodes returned error: %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].ProfileType != logicdomain.ProfileTypeProject || snapshots[0].BindID != 9 {
		t.Fatalf("unexpected recovered profile snapshots: %+v", snapshots)
	}
	if len(snapshots[0].Nodes) != 1 || snapshots[0].Nodes[0].ID != 43 {
		t.Fatalf("expected recovered snapshot to reload active nodes, got %+v", snapshots[0].Nodes)
	}
}

// TestStoreConvergeExpiredProfileNodesMarksCommitUnknownUncertain verifies profile lifecycle expiry preserves SQLite commit-boundary ambiguity.
// TestStoreConvergeExpiredProfileNodesMarksCommitUnknownUncertain 用于验证画像生命周期过期会保留 SQLite 提交边界不确定语义。
func TestStoreConvergeExpiredProfileNodesMarksCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("failed to commit sqlite profile expiry")
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "expires_timestamp > 0") {
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{
				"id":41,
				"turn_id":null,
				"profile_type":%d,
				"bind_id":9,
				"content":"expired one",
				"profile_status":%d,
				"priority":%d,
				"profile_level":%d,
				"level_reason":"stable reason",
				"refresh_weight":32,
				"source_kind":%d,
				"source_id":77,
				"status_reason":"",
				"expires_timestamp":1,
				"superseded_by_id":0,
				"profile_date":"2026-07-05",
				"created_timestamp":1,
				"updated_timestamp":1
			}]`,
				logicdomain.ProfileTypeProject,
				logicdomain.ProfileStatusActive,
				logicdomain.ProfilePriorityP1,
				logicdomain.ProfileLevelStable,
				logicdomain.ProfileSourceKindManualInstruction,
			)}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	snapshots, err := store.ConvergeExpiredProfileNodes(context.Background(), 1)
	if snapshots != nil {
		t.Fatalf("expected no snapshots after commit-unknown expiry update, got %+v", snapshots)
	}
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected profile expiry commit error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "mark expired profile nodes") || !strings.Contains(err.Error(), "failed to commit sqlite profile expiry") {
		t.Fatalf("unexpected profile expiry commit error: %v", err)
	}
}

// TestStoreReplaceRenderedProfilesChecksBatchRowsChanged verifies rendered profile batch writes require every target row to be updated.
// TestStoreReplaceRenderedProfilesChecksBatchRowsChanged 用于验证渲染画像批量写回要求每个目标行都被更新。
func TestStoreReplaceRenderedProfilesChecksBatchRowsChanged(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteBatchRequest
	fake.executeBatchFunc = func(_ context.Context, req *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: int64(len(req.GetItems())), StatementsExecuted: int64(len(req.GetItems()))}, nil
	}

	err := store.ReplaceRenderedProfiles(context.Background(), logicdomain.RenderedProfileSet{
		ProjectProfiles: map[uint64]string{9: "project profile", 7: "earlier project"},
	})
	if err != nil {
		t.Fatalf("ReplaceRenderedProfiles returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected rendered profile batch request")
	}
	if !strings.Contains(captured.GetSql(), "UPDATE vmm_projects") {
		t.Fatalf("expected project profile update SQL, got %q", captured.GetSql())
	}
	items := captured.GetItems()
	if len(items) != 2 {
		t.Fatalf("expected two rendered profile batch items, got %d", len(items))
	}
	requireSQLiteInt64Params(t, []sqliteffi.SQLValue{items[0].GetParams()[2], items[1].GetParams()[2]}, 7, 9)
}

// TestStoreReplaceRenderedProfilesMarksPriorBatchDriftUncertain verifies later clean misses become uncertain after an earlier scope batch has committed.
// TestStoreReplaceRenderedProfilesMarksPriorBatchDriftUncertain 用于验证前序 scope 批量写入已提交后，后续干净漏命中会按结果不确定上报。
func TestStoreReplaceRenderedProfilesMarksPriorBatchDriftUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	batchCalls := 0
	fake.executeBatchFunc = func(_ context.Context, _ *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		batchCalls++
		if batchCalls == 1 {
			return &fakeExecuteResponse{Success: true, RowsChanged: 1, StatementsExecuted: 1}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 0, StatementsExecuted: 1}, nil
	}

	err := store.ReplaceRenderedProfiles(context.Background(), logicdomain.RenderedProfileSet{
		UserProfiles: map[uint64]string{1: "user profile"},
		TeamProfiles: map[uint64]string{2: "team profile"},
	})
	if err == nil {
		t.Fatal("expected rendered profile drift to fail")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected rendered profile drift after prior batch to be outcome-uncertain, got %v", err)
	}
	if batchCalls != 2 {
		t.Fatalf("expected two rendered profile batch calls, got %d", batchCalls)
	}
	if !strings.Contains(err.Error(), "replace rendered team profiles: replace rendered profile batch affected 0 rows, want 1") {
		t.Fatalf("unexpected rendered profile drift error: %v", err)
	}
}

// TestStoreReplaceRenderedProfilesReconcilesInitialCommitUnknownBatch verifies a commit-uncertain rendered batch is accepted only when durable rows prove the same payload landed.
// TestStoreReplaceRenderedProfilesReconcilesInitialCommitUnknownBatch 用于验证提交不确定的渲染画像批量写入只有在长期行证明同一载荷已落库时才会恢复成功。
func TestStoreReplaceRenderedProfilesReconcilesInitialCommitUnknownBatch(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var capturedBatch *fakeExecuteBatchRequest
	var capturedQuery *fakeQueryRequest
	fake.executeBatchFunc = func(_ context.Context, req *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		capturedBatch = req
		return nil, errors.New("commit outcome unknown while replacing rendered project profiles")
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		capturedQuery = req
		if capturedBatch == nil {
			t.Fatal("expected rendered profile batch write before reconciliation read")
		}
		if !strings.Contains(req.GetSql(), "SELECT id, profile, updated_at") || !strings.Contains(req.GetSql(), "FROM vmm_projects") {
			t.Fatalf("unexpected rendered profile reconciliation query: %q", req.GetSql())
		}
		requireSQLiteInt64Params(t, req.GetParams(), 7, 9)
		items := capturedBatch.GetItems()
		if len(items) != 2 {
			t.Fatalf("expected two project profile batch items, got %d", len(items))
		}
		firstUpdatedAt := items[0].GetParams()[1]
		secondUpdatedAt := items[1].GetParams()[1]
		if firstUpdatedAt.Kind != sqliteffi.SQLValueString || secondUpdatedAt.Kind != sqliteffi.SQLValueString || firstUpdatedAt.String != secondUpdatedAt.String {
			t.Fatalf("expected one shared updated_at string across batch, got %#v and %#v", firstUpdatedAt, secondUpdatedAt)
		}
		return &fakeQueryJSONResponse{
			JsonData: sqliteRenderedProfileBatchRowsJSON(map[uint64]string{
				7: "earlier project",
				9: "project profile",
			}, firstUpdatedAt.String, 7, 9),
		}, nil
	}

	err := store.ReplaceRenderedProfiles(context.Background(), logicdomain.RenderedProfileSet{
		ProjectProfiles: map[uint64]string{9: "project profile", 7: "earlier project"},
	})
	if err != nil {
		t.Fatalf("expected rendered profile commit-unknown batch to reconcile, got %v", err)
	}
	if capturedQuery == nil {
		t.Fatal("expected rendered profile reconciliation query")
	}
}

// TestStoreReplaceRenderedProfilesMarksInitialCommitUnknownUncertain verifies the first scope batch keeps SQLite commit-boundary ambiguity visible.
// TestStoreReplaceRenderedProfilesMarksInitialCommitUnknownUncertain 用于验证首个 scope 批量写回会保留 SQLite 提交边界不确定语义。
func TestStoreReplaceRenderedProfilesMarksInitialCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeBatchFunc = func(context.Context, *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("commit outcome unknown while replacing rendered user profile")
	}

	err := store.ReplaceRenderedProfiles(context.Background(), logicdomain.RenderedProfileSet{
		UserProfiles: map[uint64]string{1: "user profile"},
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected initial rendered profile commit error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "replace rendered user profiles") || !strings.Contains(err.Error(), "commit outcome unknown while replacing rendered user profile") {
		t.Fatalf("unexpected rendered profile commit error: %v", err)
	}
}

// TestStoreReplaceRenderedProfilesKeepsInitialBatchErrorOrdinary verifies ordinary first-scope failures are not promoted without commit-boundary evidence.
// TestStoreReplaceRenderedProfilesKeepsInitialBatchErrorOrdinary 用于验证没有提交边界证据的首个 scope 普通失败不会被提升为结果不确定。
func TestStoreReplaceRenderedProfilesKeepsInitialBatchErrorOrdinary(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeBatchFunc = func(context.Context, *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("rendered profile table unavailable")
	}

	err := store.ReplaceRenderedProfiles(context.Background(), logicdomain.RenderedProfileSet{
		UserProfiles: map[uint64]string{1: "user profile"},
	})
	if err == nil {
		t.Fatal("expected initial rendered profile batch error")
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect ordinary initial rendered profile error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "replace rendered user profiles") || !strings.Contains(err.Error(), "rendered profile table unavailable") {
		t.Fatalf("unexpected rendered profile ordinary error: %v", err)
	}
}

// TestStoreReplaceNoiseEmbeddingCacheUsesExecuteBatch verifies repeated prototype inserts are merged into one ExecuteBatch call.
// TestStoreReplaceNoiseEmbeddingCacheUsesExecuteBatch 用于验证重复原型写入会合并成一次 ExecuteBatch 调用。
func TestStoreReplaceNoiseEmbeddingCacheUsesExecuteBatch(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	execCalls := 0
	batchCalls := 0
	var capturedBatch *fakeExecuteBatchRequest
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		execCalls++
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.executeBatchFunc = func(_ context.Context, req *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		batchCalls++
		capturedBatch = req
		return &fakeExecuteResponse{Success: true, RowsChanged: int64(len(req.GetItems())), StatementsExecuted: int64(len(req.GetItems()))}, nil
	}

	err := store.ReplaceNoiseEmbeddingCache(context.Background(), logicdomain.NoiseEmbeddingCacheQuery{
		Scope:     "noise",
		Language:  "zh-CN",
		Model:     "text-embedding-v4",
		Dimension: 1024,
		RulesHash: "rules",
	}, []logicdomain.NoiseEmbeddingCacheEntry{
		{
			Scope:        "noise",
			Language:     "zh-CN",
			CategoryName: "chat",
			Phrase:       "hello",
			Model:        "text-embedding-v4",
			Dimension:    1024,
			RulesHash:    "rules",
			Vector:       []float32{1, 2, 3},
			UpdatedAt:    time.Unix(0, 0).UTC(),
		},
		{
			Scope:        "noise",
			Language:     "zh-CN",
			CategoryName: "chat",
			Phrase:       "world",
			Model:        "text-embedding-v4",
			Dimension:    1024,
			RulesHash:    "rules",
			Vector:       []float32{4, 5, 6},
			UpdatedAt:    time.Unix(1, 0).UTC(),
		},
	})
	if err != nil {
		t.Fatalf("ReplaceNoiseEmbeddingCache returned error: %v", err)
	}
	if execCalls != 1 || batchCalls != 1 {
		t.Fatalf("expected delete+batch write, exec=%d batch=%d", execCalls, batchCalls)
	}
	if capturedBatch == nil || len(capturedBatch.GetItems()) != 2 {
		t.Fatalf("unexpected batch capture: %#v", capturedBatch)
	}
}

// TestStoreReplaceNoiseEmbeddingCacheClearCommitOutcomeUncertain verifies cache delete failures keep commit-boundary uncertainty visible while callers can still degrade.
// TestStoreReplaceNoiseEmbeddingCacheClearCommitOutcomeUncertain 用于验证缓存删除失败会保留提交边界不确定语义，同时调用方仍可降级处理。
func TestStoreReplaceNoiseEmbeddingCacheClearCommitOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	batchCalls := 0
	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("failed to commit sqlite noise cache delete")
	}
	fake.executeBatchFunc = func(context.Context, *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		batchCalls++
		return &fakeExecuteResponse{Success: true}, nil
	}

	err := store.ReplaceNoiseEmbeddingCache(context.Background(), logicdomain.NoiseEmbeddingCacheQuery{
		Scope:     "noise",
		Language:  "zh-CN",
		Model:     "text-embedding-v4",
		Dimension: 1024,
		RulesHash: "rules",
	}, []logicdomain.NoiseEmbeddingCacheEntry{{Vector: []float32{1}, UpdatedAt: time.Unix(0, 0).UTC()}})
	if err == nil {
		t.Fatal("expected noise cache delete commit-boundary error")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain noise cache delete error, got %T %v", err, err)
	}
	if batchCalls != 0 {
		t.Fatalf("expected delete failure to stop before batch insert, got %d batch calls", batchCalls)
	}
}

// TestStoreReplaceNoiseEmbeddingCacheInsertCommitOutcomeUncertain verifies cache insert failures after delete keep commit-boundary uncertainty visible.
// TestStoreReplaceNoiseEmbeddingCacheInsertCommitOutcomeUncertain 用于验证缓存删除后的插入失败会保留提交边界不确定语义。
func TestStoreReplaceNoiseEmbeddingCacheInsertCommitOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.executeBatchFunc = func(context.Context, *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("commit outcome unknown while inserting sqlite noise cache")
	}

	err := store.ReplaceNoiseEmbeddingCache(context.Background(), logicdomain.NoiseEmbeddingCacheQuery{
		Scope:     "noise",
		Language:  "zh-CN",
		Model:     "text-embedding-v4",
		Dimension: 1024,
		RulesHash: "rules",
	}, []logicdomain.NoiseEmbeddingCacheEntry{{Vector: []float32{1}, UpdatedAt: time.Unix(0, 0).UTC()}})
	if err == nil {
		t.Fatal("expected noise cache insert commit-boundary error")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain noise cache insert error, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "insert noise embedding cache rows") {
		t.Fatalf("unexpected noise cache insert error detail: %v", err)
	}
}

// TestStoreReplaceMemoryVectorsUsesExecuteBatch verifies vector_json rebuilds are persisted through one homogeneous ExecuteBatch call.
// TestStoreReplaceMemoryVectorsUsesExecuteBatch 用于验证 vector_json 重建会通过一次同构 ExecuteBatch 持久化。
func TestStoreReplaceMemoryVectorsUsesExecuteBatch(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	execCalls := 0
	batchCalls := 0
	var capturedBatch *fakeExecuteBatchRequest
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		execCalls++
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.executeBatchFunc = func(_ context.Context, req *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		batchCalls++
		capturedBatch = req
		return &fakeExecuteResponse{Success: true, RowsChanged: int64(len(req.GetItems())), StatementsExecuted: int64(len(req.GetItems()))}, nil
	}

	err := store.ReplaceMemoryVectors(context.Background(), []logicdomain.MemoryRecord{
		{ID: "vec-1", Vector: []float32{1, 2, 3}},
		{ID: "vec-2", Vector: []float32{4, 5, 6}},
	})
	if err != nil {
		t.Fatalf("ReplaceMemoryVectors returned error: %v", err)
	}
	if execCalls != 0 || batchCalls != 1 {
		t.Fatalf("expected batch-only vector rebuild, exec=%d batch=%d", execCalls, batchCalls)
	}
	first := capturedBatch.GetItems()[0].GetParams()
	if first[0].Kind != sqliteffi.SQLValueString || first[0].String != "[1,2,3]" {
		t.Fatalf("expected vector json string param, got %#v", first[0])
	}
	if first[1].Kind != sqliteffi.SQLValueString || first[1].String != "vec-1" {
		t.Fatalf("expected vector id string param, got %#v", first[1])
	}
}

// TestStoreReplaceMemoryVectorsRejectsRowsChangedDrift verifies split rebuild cannot report success when a durable vector row was missed.
// TestStoreReplaceMemoryVectorsRejectsRowsChangedDrift 用于验证 split 重建漏掉长期向量行时不能报告成功。
func TestStoreReplaceMemoryVectorsRejectsRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeBatchFunc = func(_ context.Context, _ *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1, StatementsExecuted: 2}, nil
	}

	err := store.ReplaceMemoryVectors(context.Background(), []logicdomain.MemoryRecord{
		{ID: "vec-1", Vector: []float32{1, 2, 3}},
		{ID: "vec-2", Vector: []float32{4, 5, 6}},
	})
	if err == nil {
		t.Fatal("expected vector replacement row-count drift to fail")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected partial vector replacement drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "replace sqlite memory vectors affected 1 rows, want 2") {
		t.Fatalf("unexpected vector replacement drift error: %v", err)
	}
}

// TestStoreReplaceMemoryVectorsCommitOutcomeUncertain verifies split rebuild vector rewrites preserve ambiguous ExecuteBatch commit semantics.
// TestStoreReplaceMemoryVectorsCommitOutcomeUncertain 用于验证 split 重建向量重写会保留 ExecuteBatch 提交结果不明语义。
func TestStoreReplaceMemoryVectorsCommitOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeBatchFunc = func(context.Context, *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("failed to commit sqlite vector replacement batch")
	}

	err := store.ReplaceMemoryVectors(context.Background(), []logicdomain.MemoryRecord{
		{ID: "vec-1", Vector: []float32{1, 2, 3}},
	})
	if err == nil {
		t.Fatal("expected vector replacement commit-boundary error")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain vector replacement error, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "replace sqlite memory vectors") {
		t.Fatalf("unexpected vector replacement error detail: %v", err)
	}
}

// TestNormalizeTurnMemoryNodeRecordPreservesCandidateExpiry verifies split-mode post-action writes keep explicit analyzer expiry values instead of falling back to the default horizon.
// TestNormalizeTurnMemoryNodeRecordPreservesCandidateExpiry 用于验证 split 模式 post-action 写入会保留分析器显式过期时间，而不是退回默认保留周期。
func TestNormalizeTurnMemoryNodeRecordPreservesCandidateExpiry(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	expiresAt := now.Add(2 * time.Hour)
	record := normalizeTurnMemoryNodeRecord(
		logicdomain.SessionRef{TeamID: 1, SpaceID: 2, ProjectID: 3, UserID: 4, SessionID: 5},
		logicdomain.PersistedTurnRecord{ID: 6, CreatedAt: now},
		logicdomain.MemoryNodeCandidate{
			VectorID:      "vec-explicit",
			Vector:        []float32{0.1, 0.2},
			Category:      logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:      "显式短期记忆",
			ScopeLevel:    logicdomain.MemoryScopeLevelSession,
			RefreshWeight: 1,
			ExpiresAt:     expiresAt,
		},
		7,
		now,
	)
	if !record.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("expires_at = %v, want %v", record.ExpiresAt, expiresAt)
	}
}

// TestStoreClearMemoryVectorsUsesExecuteBatch verifies vector resets clear vector_json through one homogeneous ExecuteBatch call.
// TestStoreClearMemoryVectorsUsesExecuteBatch 用于验证向量重置会通过一次同构 ExecuteBatch 清空 vector_json。
func TestStoreClearMemoryVectorsUsesExecuteBatch(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	execCalls := 0
	batchCalls := 0
	var capturedBatch *fakeExecuteBatchRequest
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		execCalls++
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.executeBatchFunc = func(_ context.Context, req *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		batchCalls++
		capturedBatch = req
		return &fakeExecuteResponse{Success: true, RowsChanged: int64(len(req.GetItems())), StatementsExecuted: int64(len(req.GetItems()))}, nil
	}

	err := store.ClearMemoryVectors(context.Background(), []string{"vec-1", "vec-2"})
	if err != nil {
		t.Fatalf("ClearMemoryVectors returned error: %v", err)
	}
	if execCalls != 0 || batchCalls != 1 {
		t.Fatalf("expected batch-only vector clear, exec=%d batch=%d", execCalls, batchCalls)
	}
	if !strings.Contains(capturedBatch.GetSql(), "SET vector_json = '[]'") {
		t.Fatalf("expected vector reset sql, got %q", capturedBatch.GetSql())
	}
	first := capturedBatch.GetItems()[0].GetParams()
	if first[0].Kind != sqliteffi.SQLValueString || first[0].String != "vec-1" {
		t.Fatalf("expected vector id string param, got %#v", first[0])
	}
}

// TestStoreClearMemoryVectorsRejectsRowsChangedDrift verifies split reset fails when durable vector rows are not all cleared.
// TestStoreClearMemoryVectorsRejectsRowsChangedDrift 用于验证 split reset 未清空全部长期向量行时会失败。
func TestStoreClearMemoryVectorsRejectsRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeBatchFunc = func(_ context.Context, _ *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 0, StatementsExecuted: 2}, nil
	}

	err := store.ClearMemoryVectors(context.Background(), []string{"vec-1", "vec-2"})
	if err == nil {
		t.Fatal("expected vector clear row-count drift to fail")
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected zero-row vector clear drift to remain a clean failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "clear sqlite memory vectors affected 0 rows, want 2") {
		t.Fatalf("unexpected vector clear drift error: %v", err)
	}
}

// TestStoreClearMemoryVectorsCommitOutcomeUncertain verifies split reset vector clears preserve ambiguous ExecuteBatch commit semantics.
// TestStoreClearMemoryVectorsCommitOutcomeUncertain 用于验证 split reset 向量清空会保留 ExecuteBatch 提交结果不明语义。
func TestStoreClearMemoryVectorsCommitOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeBatchFunc = func(context.Context, *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("commit outcome unknown while clearing sqlite vectors")
	}

	err := store.ClearMemoryVectors(context.Background(), []string{"vec-1"})
	if err == nil {
		t.Fatal("expected vector clear commit-boundary error")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain vector clear error, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "clear sqlite memory vectors") {
		t.Fatalf("unexpected vector clear error detail: %v", err)
	}
}

// TestStoreGetSchemaComponentVersionReadsComponentTable verifies schema versions are read only from the shared component table.
// TestStoreGetSchemaComponentVersionReadsComponentTable 用于验证 schema 版本只会从共享组件表读取。
func TestStoreGetSchemaComponentVersionReadsComponentTable(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "FROM vmm_schema_versions") {
			return &fakeQueryJSONResponse{JsonData: `[{"component":"sqlite","schema_version":20}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	version, err := store.GetSchemaComponentVersion(context.Background(), "sqlite")
	if err != nil {
		t.Fatalf("GetSchemaComponentVersion returned error: %v", err)
	}
	if version != 20 {
		t.Fatalf("schema version = %d, want 20", version)
	}
}

// TestStoreSetSchemaComponentVersionPersistsComponentRowsOnly verifies version writes only touch the shared component row.
// TestStoreSetSchemaComponentVersionPersistsComponentRowsOnly 用于验证版本写入只会触达共享组件版本行。
func TestStoreSetSchemaComponentVersionPersistsComponentRowsOnly(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &fakeExecuteResponse{Success: true}, nil
	}

	if err := store.SetSchemaComponentVersion(context.Background(), "sqlite", 15); err != nil {
		t.Fatalf("SetSchemaComponentVersion returned error: %v", err)
	}
	joined := strings.Join(executedSQL, "\n")
	if !strings.Contains(joined, "CREATE TABLE IF NOT EXISTS vmm_schema_versions") ||
		!strings.Contains(joined, "INSERT INTO vmm_schema_versions") {
		t.Fatalf("expected shared component table writes, got %q", joined)
	}
	if strings.Contains(joined, "vmm_version") {
		t.Fatalf("expected no legacy singleton writes, got %q", joined)
	}
}

// TestStoreSetSchemaComponentVersionCommitOutcomeUncertain verifies version writes do not hide commit-boundary uncertainty as an ordinary persistence failure.
// TestStoreSetSchemaComponentVersionCommitOutcomeUncertain 用于验证版本写入不会把提交边界不确定伪装成普通持久化失败。
func TestStoreSetSchemaComponentVersionCommitOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.Contains(sqlText, "INSERT INTO vmm_schema_versions") {
			return nil, errors.New("failed to commit sqlite schema version transaction")
		}
		return &fakeExecuteResponse{Success: true}, nil
	}

	err := store.SetSchemaComponentVersion(context.Background(), "lancedb", 3)
	if err == nil {
		t.Fatal("expected schema version commit-boundary error")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain schema version error, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "persist component lancedb schema version 3") {
		t.Fatalf("unexpected schema version error detail: %v", err)
	}
}

// TestStoreSetSchemaComponentVersionTableBootstrapOutcomeUncertain verifies version-table bootstrap preserves commit-boundary uncertainty before version upsert begins.
// TestStoreSetSchemaComponentVersionTableBootstrapOutcomeUncertain 用于验证版本表 bootstrap 在版本 upsert 前会保留提交边界不确定语义。
func TestStoreSetSchemaComponentVersionTableBootstrapOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executeCalls := 0
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executeCalls++
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.Contains(sqlText, "CREATE TABLE IF NOT EXISTS vmm_schema_versions") {
			return nil, errors.New("commit outcome unknown while creating schema version table")
		}
		t.Fatalf("unexpected schema version statement after failed bootstrap: %q", sqlText)
		return nil, nil
	}

	err := store.SetSchemaComponentVersion(context.Background(), "sqlite", 20)
	if err == nil {
		t.Fatal("expected schema version table bootstrap commit-boundary error")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain schema version table bootstrap error, got %T %v", err, err)
	}
	if executeCalls != 1 {
		t.Fatalf("expected bootstrap failure to stop before version upsert, got %d execute calls", executeCalls)
	}
}

// TestStoreEnsureSQLiteSchemaRejectsUnsupportedPre19Version verifies versions older than 19 are explicitly rejected instead of silently reset.
// TestStoreEnsureSQLiteSchemaRejectsUnsupportedPre19Version 用于验证旧于 19 的版本会被显式拒绝，而不是静默重建。
func TestStoreEnsureSQLiteSchemaRejectsUnsupportedPre19Version(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "FROM vmm_schema_versions") {
			return &fakeQueryJSONResponse{JsonData: `[{"component":"sqlite","schema_version":18}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	err := store.ensureSQLiteSchema(context.Background())
	if err == nil {
		t.Fatal("expected ensureSQLiteSchema to reject unsupported pre-19 version")
	}
	if !strings.Contains(err.Error(), "minimum supported version is 19") {
		t.Fatalf("unexpected ensureSQLiteSchema error: %v", err)
	}
	joined := strings.Join(executedSQL, "\n")
	if strings.Contains(joined, "DROP TABLE IF EXISTS vmm_sessions;") {
		t.Fatalf("expected unsupported version to stop before destructive reset, got %q", joined)
	}
}

// TestStoreEnsureSQLiteSchemaBootstrapsFreshDatabaseToCurrentBaseline verifies an empty database still bootstraps straight to version 20.
// TestStoreEnsureSQLiteSchemaBootstrapsFreshDatabaseToCurrentBaseline 用于验证空数据库仍会直接初始化到 20 基线。
func TestStoreEnsureSQLiteSchemaBootstrapsFreshDatabaseToCurrentBaseline(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_schema_versions"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count FROM vmm_projects"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":0}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	if err := store.ensureSQLiteSchema(context.Background()); err != nil {
		t.Fatalf("ensureSQLiteSchema returned error: %v", err)
	}
	joined := strings.Join(executedSQL, "\n")
	if !strings.Contains(joined, "CREATE TABLE IF NOT EXISTS vmm_sessions") ||
		!strings.Contains(joined, "INSERT INTO vmm_schema_versions") {
		t.Fatalf("expected fresh bootstrap SQL, got %q", joined)
	}
	if !strings.Contains(joined, "BEGIN IMMEDIATE;\nCREATE TABLE IF NOT EXISTS vmm_noise_embeddings") ||
		!strings.Contains(joined, "CREATE INDEX IF NOT EXISTS idx_vmm_profile_instructions_target ON vmm_profile_instructions(profile_type, bind_id, id);\n\nCOMMIT;") {
		t.Fatalf("expected fresh bootstrap schema SQL to run inside one transaction, got %q", joined)
	}
	if strings.Contains(joined, "vmm_version") {
		t.Fatalf("expected bootstrap to ignore legacy version table, got %q", joined)
	}
}

// TestStoreEnsureSQLiteSchemaMigratesNineteenToCurrentBaseline verifies existing version-19 databases drop removed work-memory tables.
// TestStoreEnsureSQLiteSchemaMigratesNineteenToCurrentBaseline 用于验证既有 19 版本数据库会删除已移除的工作记忆表。
func TestStoreEnsureSQLiteSchemaMigratesNineteenToCurrentBaseline(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "FROM vmm_schema_versions") {
			return &fakeQueryJSONResponse{JsonData: `[{"component":"sqlite","schema_version":19}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	if err := store.ensureSQLiteSchema(context.Background()); err != nil {
		t.Fatalf("ensureSQLiteSchema returned error: %v", err)
	}
	oldNodesTable := "vmm_" + "scratch" + "pad_nodes"
	oldPlansTable := "vmm_" + "scratch" + "pad_plans"
	joined := strings.Join(executedSQL, "\n")
	if !strings.Contains(joined, "DROP TABLE IF EXISTS "+oldNodesTable) ||
		!strings.Contains(joined, "DROP TABLE IF EXISTS "+oldPlansTable) ||
		!strings.Contains(joined, "INSERT INTO vmm_schema_versions") {
		t.Fatalf("expected old work-memory table drops plus version persistence, got %q", joined)
	}
	if !strings.Contains(joined, "BEGIN IMMEDIATE;\nDROP TABLE IF EXISTS "+oldNodesTable+";") ||
		!strings.Contains(joined, "DROP TABLE IF EXISTS "+oldPlansTable+";\nCOMMIT;") {
		t.Fatalf("expected 19->20 migration drops to run inside one transaction, got %q", joined)
	}
}

// TestSQLiteSchemaMigrationCommitErrorMarksOutcomeUncertain verifies schema scripts with their own commit boundary preserve ambiguous commit semantics.
// TestSQLiteSchemaMigrationCommitErrorMarksOutcomeUncertain 用于验证自带提交边界的 schema 脚本会保留提交不明语义。
func TestSQLiteSchemaMigrationCommitErrorMarksOutcomeUncertain(t *testing.T) {
	err := sqliteSchemaMigrationError("bootstrap sqlite schema", fmt.Errorf("apply current sqlite schema: %w", errors.New("failed to commit sqlite transaction")))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected sqlite schema migration commit error to be outcome-uncertain, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect sqlite schema migration uncertainty to mark fresh-vector retention, got %v", err)
	}
	if !strings.Contains(err.Error(), "apply current sqlite schema: failed to commit sqlite transaction") {
		t.Fatalf("unexpected sqlite schema migration error detail: %v", err)
	}
}

// TestStoreApplySchemaMigrationPlanSupportsFuture20To21Step verifies future upgrades can still chain from the current 20 baseline.
// TestStoreApplySchemaMigrationPlanSupportsFuture20To21Step 用于验证未来升级仍能从当前 20 基线继续向上衔接。
func TestStoreApplySchemaMigrationPlanSupportsFuture20To21Step(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	stepApplied := false
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "FROM vmm_schema_versions") {
			return &fakeQueryJSONResponse{JsonData: `[{"component":"sqlite","schema_version":20}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	err := store.applySchemaMigrationPlan(context.Background(), schemaMigrationPlan{
		Component:               schemaComponentSQLite,
		MinimumSupportedVersion: 20,
		TargetVersion:           21,
		Bootstrap:               bootstrapCurrentSQLiteSchema,
		Steps: []schemaMigrationStep{{
			FromVersion: 20,
			ToVersion:   21,
			Name:        "future migration",
			Up: func(ctx context.Context, s *Store) error {
				stepApplied = true
				return s.exec(ctx, `CREATE TABLE IF NOT EXISTS vmm_future_upgrade_marker(id INTEGER PRIMARY KEY)`)
			},
		}},
	})
	if err != nil {
		t.Fatalf("applySchemaMigrationPlan returned error: %v", err)
	}
	if !stepApplied {
		t.Fatal("expected 20->21 migration step to run")
	}
	joined := strings.Join(executedSQL, "\n")
	if !strings.Contains(joined, "CREATE TABLE IF NOT EXISTS vmm_future_upgrade_marker") ||
		!strings.Contains(joined, "INSERT INTO vmm_schema_versions") {
		t.Fatalf("expected future migration SQL plus version persistence, got %q", joined)
	}
}

// TestStoreListProjectMemoriesFiltersExpiredRows verifies rebuild-source queries stay aligned with the active/unexpired memory contract.
// TestStoreListProjectMemoriesFiltersExpiredRows 用于验证重建数据源查询与 active/未过期记忆契约保持一致。
func TestStoreListProjectMemoriesFiltersExpiredRows(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	rows, err := store.ListProjectMemories(context.Background(), 9)
	if err != nil {
		t.Fatalf("ListProjectMemories returned error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no rows from canned response, got %#v", rows)
	}
	if !strings.Contains(captured.GetSql(), "memory_status =") || !strings.Contains(captured.GetSql(), "expires_timestamp <= 0 OR expires_timestamp >") {
		t.Fatalf("expected active/unexpired filter in rebuild SQL, got %q", captured.GetSql())
	}
}

// TestStoreMarkSessionCompactedWaitsForWriteLockBeforeReadingLatestTurn verifies compact flow acquires the write lock before reading MAX(id).
// TestStoreMarkSessionCompactedWaitsForWriteLockBeforeReadingLatestTurn 用于验证 compact 流程会先拿写锁再读取 MAX(id)。
func TestStoreMarkSessionCompactedWaitsForWriteLockBeforeReadingLatestTurn(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	latestTurnQueryObserved := make(chan struct{}, 1)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") {
			latestTurnQueryObserved <- struct{}{}
			return &fakeQueryJSONResponse{JsonData: `[{"latest_turn_id":88}]`}, nil
		}
		if strings.Contains(req.GetSql(), "FROM vmm_sessions") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			return &fakeQueryJSONResponse{JsonData: sqliteCompactSessionRowJSON(41, 0, 0, 1)}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
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
		_, _, err := store.MarkSessionCompacted(context.Background(), logicdomain.SessionRef{SessionID: 41}, time.Unix(0, 0).UTC())
		done <- err
	}()

	select {
	case <-latestTurnQueryObserved:
		t.Fatal("expected latest-turn query to wait until write lock is released")
	case <-time.After(50 * time.Millisecond):
	}

	store.writeMu.Unlock()
	locked = false

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("MarkSessionCompacted returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("MarkSessionCompacted did not finish after write lock release")
	}
}

// TestStoreMarkSessionCompactedRejectsRowsChangedDrift verifies compact-boundary updates cannot silently miss the resolved session row.
// TestStoreMarkSessionCompactedRejectsRowsChangedDrift 用于验证 compact 边界更新不能静默漏掉已解析的 session 行。
func TestStoreMarkSessionCompactedRejectsRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") {
			return &fakeQueryJSONResponse{JsonData: `[{"latest_turn_id":88}]`}, nil
		}
		if strings.Contains(req.GetSql(), "FROM vmm_sessions") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			return &fakeQueryJSONResponse{JsonData: sqliteCompactSessionRowJSON(41, 0, 0, 1)}, nil
		}
		t.Fatalf("unexpected compact query: %q", req.GetSql())
		return nil, nil
	}
	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}

	_, _, err := store.MarkSessionCompacted(context.Background(), logicdomain.SessionRef{SessionID: 41}, time.UnixMilli(1000).UTC())
	if err == nil {
		t.Fatal("expected compact-boundary row drift to fail")
	}
	if !strings.Contains(err.Error(), "update session compact boundary: update session compact boundary affected 0 rows, want 1") {
		t.Fatalf("unexpected compact-boundary row drift error: %v", err)
	}
}

// TestStoreMarkSessionCompactedUsesDurableBoundaryForIdempotency verifies stale caller snapshots do not force a duplicate compact-boundary write.
// TestStoreMarkSessionCompactedUsesDurableBoundaryForIdempotency 用于验证调用方快照过期时，不会触发重复的 compact 边界写入。
func TestStoreMarkSessionCompactedUsesDurableBoundaryForIdempotency(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") {
			return &fakeQueryJSONResponse{JsonData: `[{"latest_turn_id":88}]`}, nil
		}
		if strings.Contains(req.GetSql(), "FROM vmm_sessions") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			requireSQLiteInt64Params(t, req.GetParams(), 41)
			return &fakeQueryJSONResponse{JsonData: sqliteCompactSessionRowJSON(41, 88, 1000, 1000)}, nil
		}
		t.Fatalf("unexpected compact query: %q", req.GetSql())
		return nil, nil
	}
	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		t.Fatal("stale SessionRef should not trigger compact-boundary update")
		return nil, nil
	}

	latestTurnID, updated, err := store.MarkSessionCompacted(context.Background(), logicdomain.SessionRef{SessionID: 41, LastCompactedTurnID: 0}, time.UnixMilli(2000).UTC())
	if err != nil {
		t.Fatalf("MarkSessionCompacted returned error: %v", err)
	}
	if latestTurnID != 88 || updated {
		t.Fatalf("unexpected compact idempotency result: latest=%d updated=%v", latestTurnID, updated)
	}
}

// TestStoreMarkSessionCompactedReconcilesCommitUnknown verifies commit-unknown compact updates are accepted only after the durable session row proves this boundary.
// TestStoreMarkSessionCompactedReconcilesCommitUnknown 用于验证提交结果不明的 compact 更新只有在持久化 session 行证明该边界后才会被接受。
func TestStoreMarkSessionCompactedReconcilesCommitUnknown(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return nil, errors.New("failed to commit compact boundary")
	}

	sessionReads := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") {
			return &fakeQueryJSONResponse{JsonData: `[{"latest_turn_id":88}]`}, nil
		}
		if strings.Contains(req.GetSql(), "FROM vmm_sessions") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			sessionReads++
			requireSQLiteInt64Params(t, req.GetParams(), 41)
			if sessionReads == 1 {
				return &fakeQueryJSONResponse{JsonData: sqliteCompactSessionRowJSON(41, 0, 0, 1)}, nil
			}
			return &fakeQueryJSONResponse{JsonData: sqliteCompactSessionRowJSON(41, 88, 1000, 1000)}, nil
		}
		t.Fatalf("unexpected compact query: %q", req.GetSql())
		return nil, nil
	}

	latestTurnID, updated, err := store.MarkSessionCompacted(context.Background(), logicdomain.SessionRef{SessionID: 41}, time.UnixMilli(1000).UTC())
	if err != nil {
		t.Fatalf("MarkSessionCompacted returned error: %v", err)
	}
	if latestTurnID != 88 || !updated {
		t.Fatalf("unexpected compact recovery result: latest=%d updated=%v", latestTurnID, updated)
	}
	if len(requests) != 1 {
		t.Fatalf("expected one compact-boundary update request, got %d", len(requests))
	}
	requireSQLiteInt64Params(t, requests[0].GetParams(), 88, 1000, 1000, 1000, 41)
	if sessionReads != 2 {
		t.Fatalf("expected two compact session reads, got %d", sessionReads)
	}
}

// TestStoreMarkSessionCompactedKeepsUnmatchedCommitUnknownUncertain verifies unresolved commit-unknown compact updates preserve the outcome-uncertain contract.
// TestStoreMarkSessionCompactedKeepsUnmatchedCommitUnknownUncertain 用于验证无法对账的提交结果不明 compact 更新会保留结果不确定契约。
func TestStoreMarkSessionCompactedKeepsUnmatchedCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("failed to commit compact boundary")
	}
	sessionReads := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") {
			return &fakeQueryJSONResponse{JsonData: `[{"latest_turn_id":88}]`}, nil
		}
		if strings.Contains(req.GetSql(), "FROM vmm_sessions") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			sessionReads++
			if sessionReads == 1 {
				return &fakeQueryJSONResponse{JsonData: sqliteCompactSessionRowJSON(41, 0, 0, 1)}, nil
			}
			return &fakeQueryJSONResponse{JsonData: sqliteCompactSessionRowJSON(41, 0, 0, 999)}, nil
		}
		t.Fatalf("unexpected compact query: %q", req.GetSql())
		return nil, nil
	}

	latestTurnID, updated, err := store.MarkSessionCompacted(context.Background(), logicdomain.SessionRef{SessionID: 41}, time.UnixMilli(1000).UTC())
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain compact error, got %v", err)
	}
	if latestTurnID != 88 || !updated {
		t.Fatalf("unexpected compact uncertainty result: latest=%d updated=%v", latestTurnID, updated)
	}
	if !strings.Contains(err.Error(), "mark session compacted") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected compact commit-unknown error: %v", err)
	}
}

// TestBuildProjectProfileNodesDeleteSQLUsesTypedParams verifies delete/count helpers emit placeholder SQL plus stable param ordering.
// TestBuildProjectProfileNodesDeleteSQLUsesTypedParams 用于验证删除/统计辅助逻辑会输出占位符 SQL 与稳定参数顺序。
func TestBuildProjectProfileNodesDeleteSQLUsesTypedParams(t *testing.T) {
	deleteSQL, deleteParams := buildProjectProfileNodesDeleteSQL(9, 5, 3, true, true)
	if strings.Contains(deleteSQL, "bind_id = 9") || strings.Count(deleteSQL, "?") != 7 {
		t.Fatalf("unexpected delete sql: %q", deleteSQL)
	}
	expected := []any{
		logicdomain.ProfileTypeProject, uint64(9), uint64(9),
		logicdomain.ProfileTypeSpace, uint64(5),
		logicdomain.ProfileTypeTeam, uint64(3),
	}
	if fmt.Sprint(deleteParams) != fmt.Sprint(expected) {
		t.Fatalf("unexpected delete params: got=%v want=%v", deleteParams, expected)
	}

	countSQL, countParams := buildProjectProfileNodesCountSQL(9, 5, 3, true, true)
	if strings.Contains(countSQL, "bind_id = 9") || strings.Count(countSQL, "?") != 7 {
		t.Fatalf("unexpected count sql: %q", countSQL)
	}
	if fmt.Sprint(countParams) != fmt.Sprint(expected) {
		t.Fatalf("unexpected count params: got=%v want=%v", countParams, expected)
	}
}

// TestStoreLoadMemoryNodesByVectorIDsFiltersExpiredRows verifies vector-hit enrichment still ignores inactive or expired rows.
// TestStoreLoadMemoryNodesByVectorIDsFiltersExpiredRows 用于验证向量命中回表仍会忽略 inactive 或已过期行。
func TestStoreLoadMemoryNodesByVectorIDsFiltersExpiredRows(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	vectorID := "vec-1 '; DROP TABLE vmm_memory_nodes; --"
	if _, err := store.LoadMemoryNodesByVectorIDs(context.Background(), []string{" " + vectorID + " ", "vec-2", vectorID}); err != nil {
		t.Fatalf("LoadMemoryNodesByVectorIDs returned error: %v", err)
	}
	if !strings.Contains(captured.GetSql(), fmt.Sprintf("memory_status = %d", logicdomain.MemoryStatusActive)) ||
		!strings.Contains(captured.GetSql(), "expires_timestamp <= 0 OR expires_timestamp >") ||
		!strings.Contains(captured.GetSql(), "vector_id IN (?,?)") {
		t.Fatalf("unexpected vector lookup sql: %q", captured.GetSql())
	}
	if strings.Contains(captured.GetSql(), "DROP TABLE") || strings.Contains(captured.GetSql(), "vec-1") {
		t.Fatalf("vector ids leaked into SQL text: %q", captured.GetSql())
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	params := captured.GetParams()
	if len(params) != 2 {
		t.Fatalf("expected two vector id params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueString || params[0].String != vectorID {
		t.Fatalf("expected first vector id param, got %#v", params[0])
	}
	if params[1].Kind != sqliteffi.SQLValueString || params[1].String != "vec-2" {
		t.Fatalf("expected second vector id param, got %#v", params[1])
	}
}

// TestStoreSearchLexicalMemoryOverfetchesBeforeRelationalFiltering verifies stale FTS rows cannot consume the final topK budget before active-row validation.
// TestStoreSearchLexicalMemoryOverfetchesBeforeRelationalFiltering 用于验证陈旧 FTS 行不会在 active 回表校验前耗尽最终 topK 预算。
func TestStoreSearchLexicalMemoryOverfetchesBeforeRelationalFiltering(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var capturedLimit uint32
	var capturedQuery *fakeQueryRequest
	queryCalls := 0
	fake.searchFtsFunc = func(_ string, _ sqliteffi.TokenizerMode, _ string, limit uint32, _ uint32) (sqliteffi.SearchResult, error) {
		capturedLimit = limit
		return sqliteffi.SearchResult{Hits: []sqliteffi.SearchHit{
			{ID: "201", Score: 0.99},
			{ID: "202", Score: 0.75},
		}}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		capturedQuery = req
		queryCalls++
		requireSQLiteInt64Params(t, req.GetParams(), 201, 202)
		return &fakeQueryJSONResponse{JsonData: `[{
			"id":202,
			"team_id":1,
			"space_id":2,
			"project_id":3,
			"user_id":4,
			"origin_session_id":5,
			"source_turn_id":6,
			"vector_id":"vec-202",
			"vector_json":"[0.1,0.2]",
			"source_kind":1,
			"scope_level":3,
			"category":1,
			"abstract":"valid lexical memory",
			"details":"valid lexical memory details",
			"memory_status":0,
			"priority":1,
			"memory_level":2,
			"refresh_weight":1,
			"support_count":1,
			"rebuttal_count":0,
			"status_reason":"",
			"expires_timestamp":4102444800000,
			"last_recalled_timestamp":0,
			"last_adopted_timestamp":0,
			"last_reinforced_timestamp":0,
			"recalled_count":0,
			"adopted_count":0,
			"reinforcement_count":0,
			"cross_session_adopted_count":0,
			"decay_disabled":0,
			"dedupe_hash":"hash-202",
			"created_timestamp":1712131200000,
			"updated_timestamp":1712131200000
		}]`}, nil
	}

	hits, err := store.SearchLexicalMemory(context.Background(), "memory", 1, logicdomain.SearchFilter{ProjectID: 3})
	if err != nil {
		t.Fatalf("SearchLexicalMemory returned error: %v", err)
	}
	if capturedLimit != 4 {
		t.Fatalf("expected overfetch limit 4, got %d", capturedLimit)
	}
	if queryCalls != 1 {
		t.Fatalf("expected one batched lexical materialization query, got %d", queryCalls)
	}
	sql := capturedQuery.GetSql()
	if !strings.Contains(sql, "id IN (?,?)") ||
		!strings.Contains(sql, fmt.Sprintf("memory_status = %d", logicdomain.MemoryStatusActive)) ||
		!strings.Contains(sql, "expires_timestamp <= 0 OR expires_timestamp >") {
		t.Fatalf("expected batched active lexical SQL, got %q", sql)
	}
	requireSQLiteInt64Params(t, capturedQuery.GetParams(), 201, 202)
	if len(hits) != 1 || hits[0].MemoryID != 202 {
		t.Fatalf("expected valid second lexical hit after stale row filtering, got %+v", hits)
	}
}

// TestStoreLoadMemoryContextEdgesByMemoryIDsQueriesDeterministically verifies edge lookups normalize ids into one stable relational read.
// TestStoreLoadMemoryContextEdgesByMemoryIDsQueriesDeterministically 用于验证情境边读取会把 id 规范化后合并成一次稳定查询。
func TestStoreLoadMemoryContextEdgesByMemoryIDsQueriesDeterministically(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{
			JsonData: `[{"memory_id":201,"context_key":"deployment_mode","context_value":"local oss","support_count":2,"rebuttal_count":1,"last_supported_timestamp":1000,"last_rebutted_timestamp":2000,"created_timestamp":3000,"updated_timestamp":4000}]`,
		}, nil
	}

	edges, err := store.LoadMemoryContextEdgesByMemoryIDs(context.Background(), []uint64{202, 201, 202})
	if err != nil {
		t.Fatalf("LoadMemoryContextEdgesByMemoryIDs returned error: %v", err)
	}
	if len(edges) != 1 || edges[0].MemoryID != 201 || edges[0].SupportCount != 2 || edges[0].RebuttalCount != 1 {
		t.Fatalf("unexpected context edges: %+v", edges)
	}
	if !strings.Contains(captured.GetSql(), "FROM vmm_memory_context_edges") || !strings.Contains(captured.GetSql(), "memory_id IN (?,?)") {
		t.Fatalf("unexpected context edge sql: %q", captured.GetSql())
	}
	if strings.Contains(captured.GetSql(), "201") || strings.Contains(captured.GetSql(), "202") {
		t.Fatalf("expected memory ids to stay out of context edge SQL, got %q", captured.GetSql())
	}
	requireSQLiteInt64Params(t, captured.GetParams(), 201, 202)
}

// TestEvolveAdoptedMemoryRecordStrengthensLifecycle verifies adoption records reinforcement evidence and promotes cross-session hot facts.
// TestEvolveAdoptedMemoryRecordStrengthensLifecycle 用于验证采纳会记录强化证据，并提升跨会话热点事实的生命周期。
func TestEvolveAdoptedMemoryRecordStrengthensLifecycle(t *testing.T) {
	adoptedAt := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	row := logicdomain.MemoryNodeRecord{
		ID:                       201,
		OriginSessionID:          11,
		ScopeLevel:               logicdomain.MemoryScopeLevelSession,
		Priority:                 logicdomain.MemoryPriorityP0,
		MemoryLevel:              logicdomain.MemoryLevelSession,
		RefreshWeight:            1,
		ExpiresAt:                adoptedAt.Add(24 * time.Hour),
		AdoptedCount:             1,
		ReinforcementCount:       1,
		CrossSessionAdoptedCount: 3,
	}

	updated := evolveAdoptedMemoryRecord(logicdomain.SessionRef{SessionID: 22}, row, adoptedAt)
	if !updated.LastReinforcedAt.Equal(adoptedAt) || updated.ReinforcementCount != 2 {
		t.Fatalf("unexpected reinforcement writeback: %+v", updated)
	}
	if updated.ScopeLevel != logicdomain.MemoryScopeLevelProject || updated.MemoryLevel != logicdomain.MemoryLevelPersistent {
		t.Fatalf("unexpected promoted lifecycle: %+v", updated)
	}
	if updated.CrossSessionAdoptedCount != 4 || !updated.ExpiresAt.After(row.ExpiresAt) {
		t.Fatalf("unexpected cross-session adoption result: %+v", updated)
	}
}

// TestParameterizedMemoryAdoptionUpdateStatementUsesTypedParams verifies adoption lifecycle fields stay out of raw SQL text.
// TestParameterizedMemoryAdoptionUpdateStatementUsesTypedParams 用于验证采纳生命周期字段不会进入原始 SQL 文本。
func TestParameterizedMemoryAdoptionUpdateStatementUsesTypedParams(t *testing.T) {
	adoptedAt := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	expiresAt := adoptedAt.Add(180 * 24 * time.Hour)
	statement := parameterizedMemoryAdoptionUpdateStatement(logicdomain.MemoryNodeRecord{
		ID:                       201,
		ScopeLevel:               logicdomain.MemoryScopeLevelProject,
		Status:                   logicdomain.MemoryStatusActive,
		MemoryLevel:              logicdomain.MemoryLevelPersistent,
		RefreshWeight:            3,
		ExpiresAt:                expiresAt,
		LastRecalledAt:           adoptedAt,
		LastAdoptedAt:            adoptedAt,
		LastReinforcedAt:         adoptedAt,
		RecalledCount:            4,
		AdoptedCount:             5,
		ReinforcementCount:       6,
		CrossSessionAdoptedCount: 7,
		DecayDisabled:            true,
		UpdatedAt:                adoptedAt,
	})
	if strings.Contains(statement.SQL, "201") || strings.Contains(statement.SQL, strconv.FormatInt(adoptedAt.UnixMilli(), 10)) {
		t.Fatalf("expected adoption values to stay out of SQL text, got %q", statement.SQL)
	}
	if !strings.Contains(statement.SQL, "SET scope_level = ?") || !strings.Contains(statement.SQL, "WHERE id = ?") {
		t.Fatalf("expected typed adoption placeholders, got %q", statement.SQL)
	}
	expected := []any{
		logicdomain.MemoryScopeLevelProject,
		logicdomain.MemoryStatusActive,
		logicdomain.MemoryLevelPersistent,
		3,
		expiresAt.UnixMilli(),
		adoptedAt.UnixMilli(),
		adoptedAt.UnixMilli(),
		adoptedAt.UnixMilli(),
		4,
		5,
		6,
		7,
		1,
		adoptedAt.UnixMilli(),
		uint64(201),
	}
	if len(statement.Params) != len(expected) {
		t.Fatalf("expected %d adoption params, got %d: %#v", len(expected), len(statement.Params), statement.Params)
	}
	for idx, value := range expected {
		if statement.Params[idx] != value {
			t.Fatalf("param %d = %#v, want %#v", idx, statement.Params[idx], value)
		}
	}
}

// TestStoreApplyMemoryAdoptionUsesTypedSQLiteParams verifies pre-check adoption writes bind evolved lifecycle fields.
// TestStoreApplyMemoryAdoptionUsesTypedSQLiteParams 用于验证 pre-check 采纳写入会绑定演进后的生命周期字段。
func TestStoreApplyMemoryAdoptionUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	adoptedAt := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	currentExpiresMs := adoptedAt.Add(24 * time.Hour).UnixMilli()
	var captured *fakeExecuteRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[
{"id":201,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":11,"source_turn_id":6,"vector_id":"vec-201","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":%d,"category":3,"abstract":"adopted memory","details":"details","memory_status":%d,"priority":%d,"memory_level":%d,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":%d,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":1,"reinforcement_count":1,"cross_session_adopted_count":3,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20}
]`,
				logicdomain.MemoryScopeLevelSession,
				logicdomain.MemoryStatusActive,
				logicdomain.MemoryPriorityP0,
				logicdomain.MemoryLevelSession,
				currentExpiresMs,
			)}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	records, err := store.ApplyMemoryAdoption(context.Background(), logicdomain.SessionRef{SessionID: 22}, []uint64{201}, adoptedAt)
	if err != nil {
		t.Fatalf("ApplyMemoryAdoption returned error: %v", err)
	}
	if len(records) != 1 || records[0].ID != "vec-201" {
		t.Fatalf("unexpected adopted records: %+v", records)
	}
	if captured == nil {
		t.Fatal("expected adoption update request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "SET scope_level = ?") || !strings.Contains(sql, "WHERE id = ?") {
		t.Fatalf("expected typed adoption SQL, got %q", sql)
	}
	for _, leaked := range []string{"201", strconv.FormatInt(adoptedAt.UnixMilli(), 10)} {
		if strings.Contains(sql, leaked) {
			t.Fatalf("expected adoption value %q to stay out of SQL text, got %q", leaked, sql)
		}
	}
	params := captured.GetParams()
	if len(params) != 15 {
		t.Fatalf("expected fifteen adoption params, got %d: %#v", len(params), params)
	}
	expectedExpiresMs := defaultUnifiedMemoryExpiry(logicdomain.MemoryScopeLevelProject, adoptedAt).UnixMilli()
	requireSQLiteInt64Params(t, params[:4],
		int64(logicdomain.MemoryScopeLevelProject),
		int64(logicdomain.MemoryStatusActive),
		int64(logicdomain.MemoryLevelPersistent),
		2,
	)
	requireSQLiteInt64Params(t, params[4:8], expectedExpiresMs, adoptedAt.UnixMilli(), adoptedAt.UnixMilli(), adoptedAt.UnixMilli())
	requireSQLiteInt64Params(t, params[8:], 1, 2, 2, 4, 0, adoptedAt.UnixMilli(), 201)
}

// TestStoreApplyMemoryAdoptionReconcilesCommitUnknownUpdate verifies adoption recovers when the durable row proves the uncertain update committed.
// TestStoreApplyMemoryAdoptionReconcilesCommitUnknownUpdate 用于验证采纳更新提交未知时，如果长期行证明更新已提交则可以恢复成功。
func TestStoreApplyMemoryAdoptionReconcilesCommitUnknownUpdate(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	adoptedAt := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	currentExpiresMs := adoptedAt.Add(24 * time.Hour).UnixMilli()
	queryCount := 0
	var captured *fakeExecuteRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_memory_nodes") {
			queryCount++
			if queryCount == 1 {
				requireSQLiteInt64Params(t, req.GetParams(), 201)
				return &fakeQueryJSONResponse{JsonData: sqliteAdoptableMemoryNodeRowsJSON(currentExpiresMs)}, nil
			}
			if captured == nil {
				t.Fatal("expected adoption update request before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 201)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeRowJSONFromAdoptionUpdateParams(t, captured.GetParams())}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return nil, errors.New("failed to commit memory adoption")
	}

	records, err := store.ApplyMemoryAdoption(context.Background(), logicdomain.SessionRef{SessionID: 22}, []uint64{201}, adoptedAt)
	if err != nil {
		t.Fatalf("ApplyMemoryAdoption returned error: %v", err)
	}
	if len(records) != 1 || records[0].ID != "vec-201" {
		t.Fatalf("unexpected recovered adopted records: %+v", records)
	}
	if queryCount != 2 {
		t.Fatalf("expected initial load and reconcile read, got %d memory queries", queryCount)
	}
}

// TestStoreApplyMemoryAdoptionKeepsUnmatchedCommitUnknownUncertain verifies adoption does not recover a commit-unknown update when the row still differs from the exact target.
// TestStoreApplyMemoryAdoptionKeepsUnmatchedCommitUnknownUncertain 用于验证采纳更新提交未知但回读行仍不匹配时，不能把结果恢复为成功。
func TestStoreApplyMemoryAdoptionKeepsUnmatchedCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	adoptedAt := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	currentExpiresMs := adoptedAt.Add(24 * time.Hour).UnixMilli()
	queryCount := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_memory_nodes") {
			queryCount++
			requireSQLiteInt64Params(t, req.GetParams(), 201)
			return &fakeQueryJSONResponse{JsonData: sqliteAdoptableMemoryNodeRowsJSON(currentExpiresMs)}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("failed to commit memory adoption")
	}

	records, err := store.ApplyMemoryAdoption(context.Background(), logicdomain.SessionRef{SessionID: 22}, []uint64{201}, adoptedAt)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain adoption update error, got %v", err)
	}
	if records != nil {
		t.Fatalf("expected no adopted records after unresolved commit unknown, got %+v", records)
	}
	if !strings.Contains(err.Error(), "apply memory adoption statement 1") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected adoption commit-unknown error: %v", err)
	}
	if queryCount != 2 {
		t.Fatalf("expected initial load and reconcile read, got %d memory queries", queryCount)
	}
}

// TestStoreApplyMemoryAdoptionRejectsUpdateRowsChangedDrift verifies adoption does not return vector-sync records when SQLite reports a missed row update.
// TestStoreApplyMemoryAdoptionRejectsUpdateRowsChangedDrift 用于验证 SQLite 报告行更新未命中时，采纳流程不会返回向量同步记录。
func TestStoreApplyMemoryAdoptionRejectsUpdateRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	adoptedAt := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[
{"id":201,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":11,"source_turn_id":6,"vector_id":"vec-201","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":3,"abstract":"adopted memory","details":"details","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":9999999999999,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":1,"reinforcement_count":1,"cross_session_adopted_count":3,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20}
]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}

	records, err := store.ApplyMemoryAdoption(context.Background(), logicdomain.SessionRef{SessionID: 22}, []uint64{201}, adoptedAt)
	if err == nil {
		t.Fatal("expected ApplyMemoryAdoption to reject missed SQLite update")
	}
	if records != nil {
		t.Fatalf("expected no adopted records after missed update, got %+v", records)
	}
	if !strings.Contains(err.Error(), "update adopted sqlite memory node affected 0 rows, want 1") {
		t.Fatalf("expected row-count drift error, got %v", err)
	}
}

// TestStoreApplyMemoryAdoptionMarksPartialRowsChangedDriftUncertain verifies adoption reports uncertainty after an earlier row has already changed.
// TestStoreApplyMemoryAdoptionMarksPartialRowsChangedDriftUncertain 用于验证采纳流程在已有前序行变更后遇到漂移时会上报结果不确定。
func TestStoreApplyMemoryAdoptionMarksPartialRowsChangedDriftUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	adoptedAt := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[
{"id":201,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":11,"source_turn_id":6,"vector_id":"vec-201","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":3,"abstract":"first adopted memory","details":"details","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":9999999999999,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":1,"reinforcement_count":1,"cross_session_adopted_count":3,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20},
{"id":202,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":12,"source_turn_id":7,"vector_id":"vec-202","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":0,"category":3,"abstract":"second adopted memory","details":"details","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":9999999999999,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":1,"reinforcement_count":1,"cross_session_adopted_count":3,"decay_disabled":0,"dedupe_hash":"","created_timestamp":11,"updated_timestamp":21}
]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	executeCount := 0
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executeCount++
		if executeCount == 1 {
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}

	records, err := store.ApplyMemoryAdoption(context.Background(), logicdomain.SessionRef{SessionID: 22}, []uint64{201, 202}, adoptedAt)
	if err == nil {
		t.Fatal("expected ApplyMemoryAdoption to reject partial SQLite update drift")
	}
	if records != nil {
		t.Fatalf("expected no adopted records after partial drift, got %+v", records)
	}
	if executeCount != 2 {
		t.Fatalf("expected two adoption update attempts, got %d", executeCount)
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected partial drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "apply memory adoption statement 2") {
		t.Fatalf("expected second statement drift context, got %v", err)
	}
}

// TestStoreApplyMemoryAdoptionSkipsExpiredActiveRows verifies expired active rows are not written back during adoption.
// TestStoreApplyMemoryAdoptionSkipsExpiredActiveRows 用于验证已过期 active 行在采纳时不会被回写。
func TestStoreApplyMemoryAdoptionSkipsExpiredActiveRows(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executeCalled := false
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "FROM vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[
{"id":201,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":6,"vector_id":"vec-201","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":3,"abstract":"过期记忆","details":"这条记忆在采纳回写前已经过期。","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20}
]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executeCalled = true
		return &fakeExecuteResponse{Success: true}, nil
	}

	if _, err := store.ApplyMemoryAdoption(context.Background(), logicdomain.SessionRef{SessionID: 77}, []uint64{201}, time.Unix(2, 0).UTC()); err != nil {
		t.Fatalf("ApplyMemoryAdoption returned error: %v", err)
	}
	if executeCalled {
		t.Fatal("expected expired active adoption target to skip write-back")
	}
}

// TestStoreApplyMemoryAdoptionLocksBeforeReading verifies adoption acquires the write lock before reading candidates.
// TestStoreApplyMemoryAdoptionLocksBeforeReading 用于验证采纳流程会在读取候选前先拿到写锁。
func TestStoreApplyMemoryAdoptionLocksBeforeReading(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	queryStarted := make(chan struct{}, 1)
	allowQueryReturn := make(chan struct{})
	done := make(chan error, 1)

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_memory_nodes") {
			select {
			case queryStarted <- struct{}{}:
			default:
			}
			<-allowQueryReturn
			return &fakeQueryJSONResponse{JsonData: `[
{"id":201,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":6,"vector_id":"vec-201","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":3,"abstract":"仍有效记忆","details":"用于验证读阶段位于写锁内。","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":9999999999999,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20}
]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	store.writeMu.Lock()
	go func() {
		_, err := store.ApplyMemoryAdoption(context.Background(), logicdomain.SessionRef{SessionID: 77}, []uint64{201}, time.Unix(100, 0).UTC())
		done <- err
	}()

	select {
	case <-queryStarted:
		t.Fatal("expected adoption query to wait until write lock is released")
	case <-time.After(150 * time.Millisecond):
	}

	store.writeMu.Unlock()
	close(allowQueryReturn)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ApplyMemoryAdoption returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("expected ApplyMemoryAdoption to finish after releasing write lock")
	}
}

// TestCurrentSchemaSQLContainsContextualMemoryTables verifies the managed schema and debug-clean script both know about contextual edge tables.
// TestCurrentSchemaSQLContainsContextualMemoryTables 用于验证受管 schema 与 debug-clean 脚本都包含情境边表。
func TestCurrentSchemaSQLContainsContextualMemoryTables(t *testing.T) {
	requiredFragments := []string{
		"support_count INTEGER NOT NULL DEFAULT 0",
		"rebuttal_count INTEGER NOT NULL DEFAULT 0",
		"CREATE TABLE IF NOT EXISTS vmm_memory_context_edges",
		"idx_vmm_memory_context_edges_lookup",
		"idx_vmm_memory_context_edges_memory",
	}
	for _, fragment := range requiredFragments {
		if !strings.Contains(currentSchemaSQL, fragment) {
			t.Fatalf("expected schema to contain %q", fragment)
		}
	}
	if !strings.Contains(debugCleanManagedSchemaSQL, "DROP TABLE IF EXISTS vmm_memory_context_edges;") {
		t.Fatal("expected debug clean schema script to drop vmm_memory_context_edges")
	}
	legacyTablePrefix := "vmm_" + "scratch" + "pad"
	if strings.Contains(currentSchemaSQL, legacyTablePrefix) {
		t.Fatalf("expected current sqlite schema to omit removed work-memory tables")
	}
	legacyNodesTable := legacyTablePrefix + "_nodes"
	legacyPlansTable := legacyTablePrefix + "_plans"
	if !strings.Contains(debugCleanManagedSchemaSQL, "DROP TABLE IF EXISTS "+legacyNodesTable+";") ||
		!strings.Contains(debugCleanManagedSchemaSQL, "DROP TABLE IF EXISTS "+legacyPlansTable+";") {
		t.Fatalf("expected debug-clean script to keep legacy work-memory table drops")
	}
}

// TestParameterizedMemoryNodesSupersedeStatementUsesTypedIDParams verifies memory supersede updates bind normalized memory ids.
// TestParameterizedMemoryNodesSupersedeStatementUsesTypedIDParams 用于验证 memory supersede 更新会绑定规范化 memory id。
func TestParameterizedMemoryNodesSupersedeStatementUsesTypedIDParams(t *testing.T) {
	statement, ok := parameterizedMemoryNodesSupersedeStatement([]uint64{78, 0, 77, 78}, 12345)
	if !ok {
		t.Fatal("expected supersede statement for non-empty memory ids")
	}
	if !strings.Contains(statement.SQL, "WHERE memory_status = ? AND id IN (?,?)") {
		t.Fatalf("expected typed supersede placeholders, got %q", statement.SQL)
	}
	if strings.Contains(statement.SQL, "77") || strings.Contains(statement.SQL, "78") {
		t.Fatalf("expected supersede memory ids to stay out of SQL text, got %q", statement.SQL)
	}
	expected := []any{
		logicdomain.MemoryStatusSuperseded,
		int64(12345),
		logicdomain.MemoryStatusActive,
		uint64(77),
		uint64(78),
	}
	if len(statement.Params) != len(expected) {
		t.Fatalf("expected %d params, got %d: %#v", len(expected), len(statement.Params), statement.Params)
	}
	for idx, value := range expected {
		if statement.Params[idx] != value {
			t.Fatalf("param %d = %#v, want %#v", idx, statement.Params[idx], value)
		}
	}
}

// TestParameterizedProfileNodesSupersedeStatementUsesTypedIDParams verifies profile-node supersede updates bind normalized node ids and reviewer reasons.
// TestParameterizedProfileNodesSupersedeStatementUsesTypedIDParams 用于验证画像节点 supersede 更新会绑定规范化 node id 和评审原因。
func TestParameterizedProfileNodesSupersedeStatementUsesTypedIDParams(t *testing.T) {
	statement, ok := parameterizedProfileNodesSupersedeStatement(logicdomain.ProfileTypeUser, 42, []uint64{9, 0, 7, 9}, 101, "  accepted replacement  ", 12345)
	if !ok {
		t.Fatal("expected profile supersede statement for non-empty node ids")
	}
	if !strings.Contains(statement.SQL, "WHERE profile_status = ? AND profile_type = ? AND bind_id = ? AND id IN (?,?)") {
		t.Fatalf("expected typed profile supersede placeholders, got %q", statement.SQL)
	}
	if strings.Contains(statement.SQL, "7") || strings.Contains(statement.SQL, "9") || strings.Contains(statement.SQL, "accepted replacement") {
		t.Fatalf("expected node ids and reason to stay out of SQL text, got %q", statement.SQL)
	}
	expected := []any{
		logicdomain.ProfileStatusSuperseded,
		uint64(101),
		"accepted replacement",
		int64(12345),
		logicdomain.ProfileStatusActive,
		logicdomain.ProfileTypeUser,
		uint64(42),
		uint64(7),
		uint64(9),
	}
	if len(statement.Params) != len(expected) {
		t.Fatalf("expected %d params, got %d: %#v", len(expected), len(statement.Params), statement.Params)
	}
	for idx, value := range expected {
		if statement.Params[idx] != value {
			t.Fatalf("param %d = %#v, want %#v", idx, statement.Params[idx], value)
		}
	}
}

// TestParameterizedProfileNodesExpireStatementUsesTypedIDParams verifies profile-node expiry updates bind normalized node ids and lifecycle reasons.
// TestParameterizedProfileNodesExpireStatementUsesTypedIDParams 用于验证画像节点过期更新会绑定规范化 node id 和生命周期原因。
func TestParameterizedProfileNodesExpireStatementUsesTypedIDParams(t *testing.T) {
	statement, ok := parameterizedProfileNodesExpireStatement([]uint64{9, 0, 7, 9}, "  expired by lifecycle  ", 12345)
	if !ok {
		t.Fatal("expected profile expiry statement for non-empty node ids")
	}
	if !strings.Contains(statement.SQL, "WHERE profile_status = ? AND id IN (?,?)") {
		t.Fatalf("expected typed profile expiry placeholders, got %q", statement.SQL)
	}
	if strings.Contains(statement.SQL, "7") || strings.Contains(statement.SQL, "9") || strings.Contains(statement.SQL, "expired by lifecycle") {
		t.Fatalf("expected node ids and reason to stay out of SQL text, got %q", statement.SQL)
	}
	expected := []any{
		logicdomain.ProfileStatusExpired,
		"expired by lifecycle",
		int64(12345),
		logicdomain.ProfileStatusActive,
		uint64(7),
		uint64(9),
	}
	if len(statement.Params) != len(expected) {
		t.Fatalf("expected %d params, got %d: %#v", len(expected), len(statement.Params), statement.Params)
	}
	for idx, value := range expected {
		if statement.Params[idx] != value {
			t.Fatalf("param %d = %#v, want %#v", idx, statement.Params[idx], value)
		}
	}
}

// TestParameterizedMemoryContextEdgesReplaceStatementsBindExtractedLabels verifies edge persistence clears old rows first and binds normalized replacements second.
// TestParameterizedMemoryContextEdgesReplaceStatementsBindExtractedLabels 用于验证情境边持久化会先清空旧行，再参数化绑定规范化替代行。
func TestParameterizedMemoryContextEdgesReplaceStatementsBindExtractedLabels(t *testing.T) {
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	statements := parameterizedMemoryContextEdgesReplaceStatements(201, []logicdomain.MemoryContextEdge{
		{
			MemoryID:        999,
			ContextKey:      "task_stage",
			ContextValue:    "phase4",
			SupportCount:    2,
			RebuttalCount:   1,
			LastSupportedAt: now,
			LastRebuttedAt:  now,
			CreatedAt:       now,
			UpdatedAt:       now,
		},
	})
	if len(statements) != 2 {
		t.Fatalf("expected delete and insert statements, got %+v", statements)
	}
	if !strings.Contains(statements[0].SQL, "DELETE FROM vmm_memory_context_edges") || !strings.Contains(statements[1].SQL, "INSERT INTO vmm_memory_context_edges") {
		t.Fatalf("unexpected context edge statements: %+v", statements)
	}
	if strings.Contains(sqliteExecutedSQLLog([]*fakeExecuteRequest{{SQL: statements[0].SQL}, {SQL: statements[1].SQL}}), "task_stage") ||
		strings.Contains(sqliteExecutedSQLLog([]*fakeExecuteRequest{{SQL: statements[0].SQL}, {SQL: statements[1].SQL}}), "phase4") {
		t.Fatalf("context edge labels leaked into SQL templates: %+v", statements)
	}
	if len(statements[0].Params) != 1 || statements[0].Params[0] != uint64(201) {
		t.Fatalf("delete statement did not use replacement memory id param: %+v", statements[0])
	}
	if len(statements[1].Params) != 9 {
		t.Fatalf("expected nine insert params, got %+v", statements[1].Params)
	}
	if statements[1].Params[0] != uint64(201) || statements[1].Params[1] != "task_stage" || statements[1].Params[2] != "phase4" {
		t.Fatalf("insert statement did not bind canonical owner and labels: %+v", statements[1].Params)
	}
	for _, param := range statements[1].Params {
		if param == uint64(999) {
			t.Fatalf("context edge params leaked stale embedded edge memory id: %+v", statements[1].Params)
		}
	}
}
