// SQLite adapter tests for session compact test.
// SQLite 适配层会话压缩测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	"context"
	"errors"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"testing"
	"time"
)

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
