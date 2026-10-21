// SQLite adapter tests for user mutations test.
// SQLite 适配层用户变更测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	"context"
	"errors"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
	"strings"
	"testing"
	"time"
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
