// SQLite adapter tests for memory adoption test.
// SQLite 适配层记忆采纳测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	"context"
	"errors"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strconv"
	"strings"
	"testing"
	"time"
)

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
