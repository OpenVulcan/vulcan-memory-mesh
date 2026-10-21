// SQLite adapter tests for session scope test.
// SQLite 适配层会话范围创建测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	"context"
	"errors"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"testing"
	"time"
)

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
